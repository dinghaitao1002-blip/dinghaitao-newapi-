/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import enResources from '@/i18n/locales/en.json'
import frResources from '@/i18n/locales/fr.json'
import jaResources from '@/i18n/locales/ja.json'
import ruResources from '@/i18n/locales/ru.json'
import viResources from '@/i18n/locales/vi.json'
import zhTWResources from '@/i18n/locales/zh-TW.json'
import zhResources from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type {
  ModelConcurrencyConfig,
  ModelConcurrencyData,
  ModelConcurrencyRule,
} from '../api'
import {
  mergeConcurrencyChanges,
  mergeUserConcurrencyChanges,
  UserConcurrencyConflict,
} from '../lib/merge-user-overrides'
import { ModelConcurrencySection } from '../model-concurrency-section'

let client: QueryClient
let data: ModelConcurrencyData
function rule(group?: string): ModelConcurrencyRule {
  return {
    group,
    model: 'image-pro',
    enabled: true,
    max_concurrent: 20,
    default_user_limit: 3,
    other_users_limit: 8,
    user_limits: [{ user_id: 193, max_concurrent: 12 }],
  }
}
function Fixture(props: { group?: string; initialModel?: string }) {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  return (
    <QueryClientProvider client={client}>
      <div ref={setActions} />
      <SettingsPageProvider actionsContainer={actions}>
        <ModelConcurrencySection {...props} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  data = {
    config: { enabled: true, rules: [rule(), rule('vip'), rule('other')] },
    usage_available: true,
    storage: 'redis',
    usage: [
      { model: 'image-pro', total: 4, other_users: 1, users: [] },
      {
        group: 'vip',
        model: 'image-pro',
        total: 2,
        other_users: 0,
        users: [{ user_id: 193, current: 2 }],
      },
      {
        group: 'other',
        model: 'image-pro',
        total: 8,
        other_users: 1,
        users: [],
      },
    ],
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/models/') {
      return {
        data: {
          success: true,
          data: { items: [{ model_name: 'image-pro' }], total: 1 },
        },
      }
    }
    return { data: { success: true, data: structuredClone(data) } }
  })
  vi.spyOn(api, 'patch').mockImplementation(async (_url, body) => {
    data.config = structuredClone(
      (body as { config: ModelConcurrencyConfig }).config
    )
    return { data: { success: true, data: { config: data.config } } }
  })
})
afterEach(() => client.clear())

describe('group model concurrency settings', () => {
  it('shows only the selected group and its live occupancy when model names match', async () => {
    render(<Fixture group='vip' />)
    expect(
      await screen.findByText('Active requests: 2; other users: 0')
    ).toBeVisible()
    expect(screen.getAllByRole('article')).toHaveLength(1)
    expect(screen.getByText('User #193: limit 12; active 2')).toBeVisible()
    expect(
      screen.getByRole('switch', { name: 'Enable model concurrency limits' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(api.patch).not.toHaveBeenCalled()
  })
  it('edits one group and merges fresh changes to another group and the global switch', async () => {
    render(<Fixture group='vip' />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    const dialog = screen.getByRole('dialog')
    fireEvent.change(
      within(dialog).getByRole('spinbutton', {
        name: 'Default concurrency per user',
      }),
      { target: { value: '7' } }
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Apply to draft' })
    )
    data.config.enabled = false
    data.config.rules[2].default_user_limit = 9
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(api.patch).toHaveBeenCalledOnce())
    expect(data.config.enabled).toBe(false)
    expect(
      data.config.rules.map((x) => [x.group ?? '', x.default_user_limit])
    ).toEqual([
      ['', 3],
      ['vip', 7],
      ['other', 9],
    ])
    expect(data.config.rules[1].user_limits).toEqual([
      { user_id: 193, max_concurrent: 12 },
    ])
  })
  it('shows only legacy defaults in the global editor and preserves grouped rules on save', async () => {
    render(<Fixture />)
    expect(
      await screen.findByText('Active requests: 4; other users: 1')
    ).toBeVisible()
    expect(screen.getAllByRole('article')).toHaveLength(1)
    await userEvent.click(
      screen.getByRole('button', { name: 'Edit concurrency for image-pro' })
    )
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Default concurrency per user' }),
      { target: { value: '5' } }
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply to draft' })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(data.config.rules[0].default_user_limit).toBe(5))
    expect(data.config.rules.slice(1)).toEqual([rule('vip'), rule('other')])
  })
  it('prefills a model shortcut from legacy defaults without creating a rule on cancel', async () => {
    data.config.rules = [rule(), rule('other')]
    render(<Fixture group='vip' initialModel='image-pro' />)
    const dialog = await screen.findByRole('dialog', {
      name: 'Add model concurrency',
    })
    expect(
      within(dialog).getByRole('spinbutton', {
        name: 'Default concurrency per user',
      })
    ).toHaveValue(3)
    expect(
      within(dialog).getByRole('spinbutton', {
        name: 'Concurrency for override 1',
      })
    ).toHaveValue(12)
    expect(
      within(dialog).getByText(
        'Starting from the global limits for this model. Applying this draft creates a separate rule for this group.'
      )
    ).toBeVisible()
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Cancel' })
    )
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
    expect(api.patch).not.toHaveBeenCalled()
  })
  it('adds a scoped model without implicitly enabling disabled global protection', async () => {
    data.config = { enabled: false, rules: [rule()] }
    render(<Fixture group='vip' initialModel='image-pro' />)
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(
      within(dialog).getByRole('spinbutton', {
        name: 'Default concurrency per user',
      }),
      { target: { value: '4' } }
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Apply to draft' })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(api.patch).toHaveBeenCalledOnce())
    expect(data.config.enabled).toBe(false)
    expect(data.config.rules).toEqual([
      rule(),
      { ...rule('vip'), default_user_limit: 4 },
    ])
    expect(
      screen.getByText(
        'Model concurrency limits are disabled. Saved overrides do not take effect.'
      )
    ).toBeVisible()
  })
  it('requires explicit confirmation before enabling all saved rules from a group', async () => {
    data.config.enabled = false
    render(<Fixture group='vip' />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Enable saved concurrency limits',
      })
    )
    const confirm = screen.getByRole('alertdialog')
    expect(
      within(confirm).getByText(
        'This enables saved global and group concurrency rules after Save Changes. Existing limits are preserved.'
      )
    ).toBeVisible()
    await userEvent.click(
      within(confirm).getByRole('button', { name: 'Enable' })
    )
    expect(api.patch).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(data.config.enabled).toBe(true))
  })
  it('keeps the scoped draft when saving fails', async () => {
    vi.mocked(api.patch).mockRejectedValueOnce(
      new Error('Could not save concurrency')
    )
    render(<Fixture group='vip' />)
    await userEvent.click(
      await screen.findByRole('switch', {
        name: 'Enable concurrency for image-pro',
      })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(await screen.findByText('Could not save concurrency')).toBeVisible()
    expect(
      screen.getByRole('switch', { name: 'Enable concurrency for image-pro' })
    ).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
  })
})

describe('group-aware concurrency merge', () => {
  it('merges a focused user only into the matching group/model identity', () => {
    const baseline = data.config
    const draft = structuredClone(baseline)
    draft.rules[1].user_limits[0].max_concurrent = 6
    const latest = structuredClone(baseline)
    latest.rules[2].user_limits[0].max_concurrent = 90
    const result = mergeUserConcurrencyChanges(baseline, draft, latest, 193)
    expect(result.rules.map((x) => x.user_limits[0].max_concurrent)).toEqual([
      12, 6, 90,
    ])
  })
  it('refuses a conflicting edit to the same scoped model while allowing unrelated changes', () => {
    const draft = structuredClone(data.config)
    draft.rules[1].default_user_limit = 6
    const latest = structuredClone(data.config)
    latest.rules[1].default_user_limit = 9
    expect(() => mergeConcurrencyChanges(data.config, draft, latest)).toThrow(
      UserConcurrencyConflict
    )
  })
})

describe('group concurrency locale resources', () => {
  const keys = [
    'Model concurrency for group {{group}}',
    'Choose a model in this group, then set shared limits and individual user concurrency. Pricing is saved separately.',
    'Limits apply only to requests using group {{group}}. Models without an enabled group rule use the global concurrency defaults.',
    'Global rules are defaults for groups without an enabled model concurrency rule.',
    'Enable saved concurrency limits',
    'Enable all saved concurrency rules?',
    'This enables saved global and group concurrency rules after Save Changes. Existing limits are preserved.',
    'Starting from the global limits for this model. Applying this draft creates a separate rule for this group.',
    'Save new or renamed groups before configuring model concurrency. Renaming or deleting a group preserves concurrency rules under the original name.',
    'User concurrency for {{model}}',
    'User concurrency',
    'Model total concurrency in this group',
    'Manage group model limits in Group pricing under system settings.',
  ]
  const resources = {
    en: enResources,
    zhCN: zhResources,
    zhTW: zhTWResources,
    fr: frResources,
    ru: ruResources,
    ja: jaResources,
    vi: viResources,
  }
  it.each(Object.entries(resources))(
    'resolves all group concurrency labels from the translation namespace for %s',
    async (language, resource) => {
      const instance = createInstance()
      await instance.init({
        resources,
        lng: language,
        fallbackLng: false,
        nsSeparator: false,
        interpolation: { escapeValue: false },
      })
      for (const key of keys) {
        expect(resource.translation).toHaveProperty([key])
        expect(resource).not.toHaveProperty([key])
        expect(instance.exists(key)).toBe(true)
      }
      if (language === 'zhCN') {
        expect(
          instance.t('Model concurrency for group {{group}}', { group: 'vip' })
        ).toBe('分组 vip 的模型并发')
        expect(
          instance.t('User concurrency for {{model}}', { model: 'image-pro' })
        ).toBe('image-pro 的用户并发')
      }
    }
  )
})
