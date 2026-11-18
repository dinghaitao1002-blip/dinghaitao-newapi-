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
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import {
  type ModelConcurrencyConfig,
  type ModelConcurrencyData,
  modelConcurrencyQueryKey,
} from '../api'
import { ModelConcurrencySection } from '../model-concurrency-section'

let queryClient: QueryClient
let serverData: ModelConcurrencyData

function SettingsFixture() {
  const [actionsContainer, setActionsContainer] =
    useState<HTMLDivElement | null>(null)
  return (
    <QueryClientProvider client={queryClient}>
      <div ref={setActionsContainer} />
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <ModelConcurrencySection />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

function existingRule() {
  return {
    model: 'image-pro',
    enabled: true,
    max_concurrent: 10,
    default_user_limit: 2,
    other_users_limit: 2,
    user_limits: [{ user_id: 41, max_concurrent: 8 }],
  }
}

beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  serverData = {
    config: { enabled: true, rules: [existingRule()] },
    storage: 'redis',
    usage_available: true,
    usage: [
      {
        model: 'image-pro',
        total: 5,
        other_users: 1,
        users: [{ user_id: 41, current: 4 }],
      },
    ],
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (String(url).startsWith('/api/user/search?')) {
      return {
        data: {
          success: true,
          data: {
            items: [
              {
                id: 72,
                username: 'priority-customer',
                display_name: 'Priority customer',
              },
            ],
            total: 1,
          },
        },
      }
    }
    return { data: { success: true, data: structuredClone(serverData) } }
  })
  vi.spyOn(api, 'patch').mockImplementation(async (_url, body) => {
    serverData.config = structuredClone(
      (body as { config: ModelConcurrencyConfig }).config
    )
    return { data: { success: true, data: { config: serverData.config } } }
  })
})

afterEach(() => queryClient.clear())

describe('model concurrency settings', () => {
  it('shows saved limits and live occupancy without writing settings', async () => {
    render(<SettingsFixture />)
    expect(
      await screen.findByRole('article', { name: 'image-pro' })
    ).toBeVisible()
    expect(
      screen.getByText(
        'Image queues, video tasks and task plugins keep concurrency slots until completion. Other endpoints, including Midjourney, count only while the HTTP request is running.'
      )
    ).toBeVisible()
    expect(screen.getByText('Active requests: 5; other users: 1')).toBeVisible()
    expect(screen.getByText('User #41: limit 8; active 4')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('adds a model with unlimited levels in the draft and saves a full configuration', async () => {
    serverData.config = { enabled: false, rules: [] }
    render(<SettingsFixture />)
    expect(await screen.findByText('No model concurrency rules')).toBeVisible()
    await userEvent.click(
      screen.getByRole('button', { name: 'Add model rule' })
    )
    const dialog = screen.getByRole('dialog')
    expect(
      within(dialog).getByRole('textbox', { name: 'Model name' })
    ).toHaveAccessibleDescription(
      'Parameter suffixes share the base model concurrency limit. A separate enabled rule takes priority.'
    )
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: 'Model name' }),
      'image-fast'
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Apply to draft' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(api.patch).not.toHaveBeenCalled()
    await userEvent.click(
      screen.getByRole('switch', { name: 'Enable model concurrency limits' })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledExactlyOnceWith(
        '/api/option/model_concurrency',
        {
          config: {
            enabled: true,
            rules: [
              {
                model: 'image-fast',
                enabled: true,
                max_concurrent: 0,
                default_user_limit: 0,
                other_users_limit: 0,
                user_limits: [],
              },
            ],
          },
        }
      )
    )
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Save Changes' })
      ).toBeDisabled()
    )
  })

  it('refreshes changed server settings when there are no unsaved edits', async () => {
    render(<SettingsFixture />)
    const toggle = await screen.findByRole('switch', {
      name: 'Enable model concurrency limits',
    })
    expect(toggle).toBeChecked()
    serverData.config.enabled = false
    serverData.config.rules[0].max_concurrent = 22
    await act(async () => {
      await queryClient.invalidateQueries({
        queryKey: modelConcurrencyQueryKey,
        exact: true,
      })
    })
    await waitFor(() => expect(toggle).not.toBeChecked())
    expect(screen.getByText('22')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
  })

  it('edits limits and user overrides, and preserves the draft during a usage refresh', async () => {
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    const dialog = screen.getByRole('dialog')
    fireEvent.change(
      within(dialog).getByRole('spinbutton', {
        name: 'Model total concurrency',
      }),
      { target: { value: '20' } }
    )
    fireEvent.change(
      within(dialog).getByRole('spinbutton', {
        name: 'Concurrency for override 1',
      }),
      { target: { value: '16' } }
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Apply to draft' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    serverData.usage[0].total = 6
    await act(async () => {
      await queryClient.invalidateQueries({
        queryKey: modelConcurrencyQueryKey,
        exact: true,
      })
    })
    expect(
      await screen.findByText('Active requests: 6; other users: 1')
    ).toBeVisible()
    expect(screen.getByText('User #41: limit 16; active 4')).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/api/option/model_concurrency', {
        config: {
          enabled: true,
          rules: [
            {
              ...existingRule(),
              max_concurrent: 20,
              user_limits: [{ user_id: 41, max_concurrent: 16 }],
            },
          ],
        },
      })
    )
  })

  it('searches the existing user endpoint and adds the selected user override', async () => {
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Find a user' }),
      'priority'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    const selector = await screen.findByRole('combobox', {
      name: 'Select user to add an override',
    })
    await userEvent.click(selector)
    await screen.findByRole('option', { name: /priority-customer/ })
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(
      screen.getByRole('spinbutton', { name: 'User ID for override 2' })
    ).toHaveValue(72)
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Concurrency for override 2' }),
      { target: { value: '3' } }
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply to draft' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/api/option/model_concurrency', {
        config: {
          enabled: true,
          rules: [
            {
              ...existingRule(),
              user_limits: [
                { user_id: 41, max_concurrent: 8 },
                { user_id: 72, max_concurrent: 3 },
              ],
            },
          ],
        },
      })
    )
    expect(api.get).toHaveBeenCalledWith(
      expect.stringContaining('/api/user/search?keyword=priority')
    )
  })

  it('rejects negative or fractional limits without changing the saved configuration', async () => {
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    const input = screen.getByRole('spinbutton', {
      name: 'Default concurrency per user',
    })
    fireEvent.change(input, { target: { value: '-1' } })
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply to draft' })
    )
    expect(
      await screen.findByText('Enter a whole number from 0 to 1,000,000')
    ).toBeVisible()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    fireEvent.change(input, { target: { value: '1.5' } })
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply to draft' })
    )
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('rejects duplicate users in one model rule', async () => {
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Add by user ID' })
    )
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'User ID for override 2' }),
      { target: { value: '41' } }
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply to draft' })
    )
    expect(
      await screen.findByText('This user already has an override')
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('rejects a second rule with the same model name', async () => {
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('button', { name: 'Add model rule' })
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Model name' }),
      'image-pro'
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply to draft' })
    )
    expect(
      await screen.findByText('A rule for this model already exists')
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('confirms rule deletion and saves the remaining rules', async () => {
    serverData.config.rules.push({ ...existingRule(), model: 'image-fast' })
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Delete concurrency for image-pro',
      })
    )
    await userEvent.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Delete',
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(
      screen.queryByRole('article', { name: 'image-pro' })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('article', { name: 'image-fast' })).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/api/option/model_concurrency', {
        config: {
          enabled: true,
          rules: [{ ...existingRule(), model: 'image-fast' }],
        },
      })
    )
  })

  it('keeps configuration editable when Redis occupancy is unavailable and never displays zero occupancy', async () => {
    serverData.usage_available = false
    serverData.usage = []
    render(<SettingsFixture />)
    expect(
      await screen.findByText(
        'Active requests: Unavailable; other users: Unavailable'
      )
    ).toBeVisible()
    expect(
      screen.getByText('User #41: limit 8; active Unavailable')
    ).toBeVisible()
    await userEvent.click(
      screen.getByRole('switch', { name: 'Enable model concurrency limits' })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/api/option/model_concurrency', {
        config: { enabled: false, rules: [existingRule()] },
      })
    )
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Save Changes' })
      ).toBeDisabled()
    )
  })

  it('keeps edits and reports a rejected save without pretending it succeeded', async () => {
    vi.mocked(api.patch).mockResolvedValueOnce({
      data: { success: false, message: 'Save rejected' },
    })
    render(<SettingsFixture />)
    await userEvent.click(
      await screen.findByRole('switch', {
        name: 'Enable concurrency for image-pro',
      })
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Save rejected')
    expect(
      screen.getByRole('switch', { name: 'Enable concurrency for image-pro' })
    ).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
    await userEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(api.patch).toHaveBeenCalledTimes(2))
  })

  it('shows retry after initial loading fails without exposing editable fallback limits', async () => {
    vi.mocked(api.get).mockRejectedValueOnce(new Error('Unavailable'))
    render(<SettingsFixture />)
    expect(
      await screen.findByText('Failed to load concurrency limits')
    ).toBeVisible()
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('article', { name: 'image-pro' })
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  })
})
