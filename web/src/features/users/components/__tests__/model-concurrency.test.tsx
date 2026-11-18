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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type {
  ModelConcurrencyConfig,
  ModelConcurrencyData,
} from '@/features/system-settings/concurrency-limits/api'
import { api } from '@/lib/api'
import { Route as UsersRoute } from '@/routes/_authenticated/users/index'
import { useAuthStore } from '@/stores/auth-store'

import { Users } from '../..'
import type { User } from '../../types'
import { UserModelConcurrencyDialog } from '../dialogs/user-model-concurrency-dialog'

let client: QueryClient
let serverData: ModelConcurrencyData
let users: User[]

async function renderUsers(role = 100) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'operator', role })
  const root = createRootRoute()
  const authenticated = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
  })
  const route = createRoute({
    getParentRoute: () => authenticated,
    path: 'users/',
    validateSearch: UsersRoute.options.validateSearch,
    component: Users,
  })
  const router = createRouter({
    routeTree: root.addChildren([authenticated.addChildren([route])]),
    history: createMemoryHistory({ initialEntries: ['/users/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await act(() => router.load())
  await screen.findByText(users[0].username)
}

async function openForUser(username = 'priority-customer') {
  const row = screen.getByRole('row', { name: new RegExp(username) })
  await userEvent.click(within(row).getByRole('button', { name: 'Open menu' }))
  await userEvent.click(
    await screen.findByRole('menuitem', { name: 'Model concurrency' })
  )
  return screen.findByRole('dialog', { name: 'Model concurrency' })
}

async function setFocusedLimit(dialog: HTMLElement, value: string) {
  await userEvent.click(
    await within(dialog).findByRole('button', {
      name: 'Edit concurrency for image-pro',
    })
  )
  const editor = screen.getByRole('dialog', { name: 'Edit model concurrency' })
  fireEvent.change(
    within(editor).getByRole('spinbutton', { name: 'Maximum concurrency' }),
    { target: { value } }
  )
  await userEvent.click(
    within(editor).getByRole('button', { name: 'Apply to draft' })
  )
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: 'Edit model concurrency' })
    ).not.toBeInTheDocument()
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  users = [
    {
      id: 41,
      username: 'priority-customer',
      display_name: 'Priority customer',
      role: 1,
      status: 1,
      quota: 0,
      used_quota: 0,
      request_count: 0,
      group: 'default',
    },
  ]
  serverData = {
    config: {
      enabled: true,
      rules: [
        {
          model: 'image-pro',
          enabled: true,
          max_concurrent: 50,
          default_user_limit: 3,
          other_users_limit: 10,
          user_limits: [
            { user_id: 41, max_concurrent: 40 },
            { user_id: 99, max_concurrent: 1 },
          ],
        },
        {
          model: 'image-other',
          enabled: false,
          max_concurrent: 7,
          default_user_limit: 1,
          other_users_limit: 2,
          user_limits: [{ user_id: 99, max_concurrent: 3 }],
        },
      ],
    },
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
    if (url === '/api/option/model_concurrency') {
      return { data: { success: true, data: structuredClone(serverData) } }
    }
    if (url === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url === '/api/authz/catalog') {
      return { data: { success: true, data: { resources: [], roles: [] } } }
    }
    if (url === '/api/user/') {
      return {
        data: { success: true, data: { items: users, total: users.length } },
      }
    }
    return { data: { success: true, data: [] } }
  })
  vi.spyOn(api, 'patch').mockImplementation(async (_url, body) => {
    serverData.config = structuredClone(
      (body as { config: ModelConcurrencyConfig }).config
    )
    return {
      data: {
        success: true,
        data: { config: structuredClone(serverData.config) },
      },
    }
  })
})

afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.reset()
  localStorage.clear()
})

describe('user model concurrency entry points', () => {
  it('opens complete management from the users header without fetching concurrency before opening', async () => {
    await renderUsers()
    expect(api.get).not.toHaveBeenCalledWith('/api/option/model_concurrency')
    await userEvent.click(
      screen.getByRole('button', { name: 'Model concurrency' })
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Model concurrency',
    })
    expect(
      await within(dialog).findByRole('article', { name: 'image-pro' })
    ).toBeVisible()
    expect(
      within(dialog).getByRole('switch', {
        name: 'Enable model concurrency limits',
      })
    ).not.toHaveAttribute('aria-disabled', 'true')
    expect(
      within(dialog).getByRole('button', {
        name: 'Delete concurrency for image-pro',
      })
    ).toBeEnabled()
    expect(
      within(dialog).getByText('User #99: limit 1; active 0')
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('opens the row user preselected and saves only their changed limit while preserving shared settings and others', async () => {
    await renderUsers()
    const original = structuredClone(serverData.config)
    const dialog = await openForUser()
    expect(
      within(dialog).getByText(
        'Set model concurrency for priority-customer (#41).'
      )
    ).toBeVisible()
    const globalSwitch = await within(dialog).findByRole('switch', {
      name: 'Enable model concurrency limits',
    })
    expect(globalSwitch).toHaveAttribute('aria-disabled', 'true')
    expect(
      within(dialog).getByRole('switch', {
        name: 'Enable concurrency for image-pro',
      })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      within(dialog).queryByText('User #99: limit 1; active 0')
    ).not.toBeInTheDocument()
    await userEvent.click(
      within(dialog).getByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    const editor = screen.getByRole('dialog', {
      name: 'Edit model concurrency',
    })
    const limit = within(editor).getByRole('spinbutton', {
      name: 'Maximum concurrency',
    })
    expect(limit).toHaveValue(40)
    expect(
      within(editor).getByRole('textbox', { name: 'Model name' })
    ).toBeDisabled()
    expect(
      within(editor).getByRole('spinbutton', {
        name: 'Model total concurrency',
      })
    ).toBeDisabled()
    expect(
      within(editor).getByRole('spinbutton', {
        name: 'Default concurrency per user',
      })
    ).toBeDisabled()
    expect(
      within(editor).getByRole('spinbutton', {
        name: 'Other users combined concurrency',
      })
    ).toBeDisabled()
    expect(
      within(editor).queryByRole('spinbutton', { name: /User ID/ })
    ).not.toBeInTheDocument()
    expect(
      within(editor).queryByRole('button', { name: 'Add by user ID' })
    ).not.toBeInTheDocument()
    fireEvent.change(limit, { target: { value: '35' } })
    await userEvent.click(
      within(editor).getByRole('button', { name: 'Apply to draft' })
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Edit model concurrency' })
      ).not.toBeInTheDocument()
    )
    expect(api.patch).not.toHaveBeenCalled()
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Save Changes' })
    )
    original.rules[0].user_limits[0].max_concurrent = 35
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledExactlyOnceWith(
        '/api/option/model_concurrency',
        { config: original }
      )
    )
  })

  it('prefills the default limit when the selected user has no override and does not save on cancel', async () => {
    users[0].id = 72
    await renderUsers()
    const dialog = await openForUser()
    await userEvent.click(
      await within(dialog).findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    const editor = screen.getByRole('dialog', {
      name: 'Edit model concurrency',
    })
    expect(
      within(editor).getByRole('spinbutton', { name: 'Maximum concurrency' })
    ).toHaveValue(3)
    await userEvent.click(
      within(editor).getByRole('button', { name: 'Cancel' })
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Edit model concurrency' })
      ).not.toBeInTheDocument()
    )
    expect(
      within(dialog).getByRole('button', { name: 'Save Changes' })
    ).toBeDisabled()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('restores only the selected user default without deleting the model rule or another user override', async () => {
    await renderUsers()
    const original = structuredClone(serverData.config)
    const dialog = await openForUser()
    await userEvent.click(
      await within(dialog).findByRole('button', {
        name: 'Restore default concurrency for image-pro',
      })
    )
    const confirmation = screen.getByRole('alertdialog')
    await userEvent.click(
      within(confirmation).getByRole('button', { name: 'Restore default' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(
      within(dialog).getByRole('article', { name: 'image-pro' })
    ).toBeVisible()
    expect(
      within(dialog).getByRole('button', {
        name: 'Restore default concurrency for image-pro',
      })
    ).toBeDisabled()
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Save Changes' })
    )
    original.rules[0].user_limits = [{ user_id: 99, max_concurrent: 1 }]
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledExactlyOnceWith(
        '/api/option/model_concurrency',
        { config: original }
      )
    )
  })

  it('keeps inactive settings read-only and offers focused users only existing model rules', async () => {
    serverData.config.enabled = false
    serverData.config.rules[0].enabled = false
    await renderUsers()
    const dialog = await openForUser()
    expect(
      await within(dialog).findByText(
        'Model concurrency limits are disabled. Saved overrides do not take effect.'
      )
    ).toBeVisible()
    expect(
      within(dialog).getAllByText(
        'This model rule is disabled. Saved overrides do not take effect.'
      )
    ).toHaveLength(2)
    expect(
      within(dialog).queryByRole('button', { name: 'Add model rule' })
    ).not.toBeInTheDocument()
    expect(
      within(dialog).getByText(
        'Add shared model rules from Model concurrency at the top of Users.'
      )
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('merges only the changed user override into fresh shared settings and other user limits', async () => {
    await renderUsers()
    const dialog = await openForUser()
    await setFocusedLimit(dialog, '35')
    serverData.config.enabled = false
    serverData.config.rules[0].enabled = false
    serverData.config.rules[0].max_concurrent = 70
    serverData.config.rules[0].default_user_limit = 5
    serverData.config.rules[0].other_users_limit = 20
    serverData.config.rules[0].user_limits[1].max_concurrent = 6
    serverData.config.rules[1].user_limits.push({
      user_id: 41,
      max_concurrent: 2,
    })
    const expected = structuredClone(serverData.config)
    expected.rules[0].user_limits[0].max_concurrent = 35
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Save Changes' })
    )
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledExactlyOnceWith(
        '/api/option/model_concurrency',
        { config: expected }
      )
    )
    expect(api.get).toHaveBeenCalledWith('/api/option/model_concurrency', {
      disableDuplicate: true,
    })
  })

  it.each(['changed', 'deleted'])(
    'keeps the draft and rejects saving when the selected model override was %s elsewhere',
    async (change) => {
      await renderUsers()
      const dialog = await openForUser()
      await setFocusedLimit(dialog, '35')
      if (change === 'changed') {
        serverData.config.rules[0].user_limits[0].max_concurrent = 37
      } else {
        serverData.config.rules.shift()
      }
      await userEvent.click(
        within(dialog).getByRole('button', { name: 'Save Changes' })
      )
      expect(await within(dialog).findByRole('alert')).toHaveTextContent(
        'Concurrency for image-pro changed elsewhere. Reset to the latest settings and try again.'
      )
      expect(api.patch).not.toHaveBeenCalled()
      expect(
        within(dialog).getByText('User #41: limit 35; active 4')
      ).toBeVisible()
      expect(
        within(dialog).getByRole('button', { name: 'Save Changes' })
      ).toBeEnabled()
    }
  )

  it('discards the previous user draft when the dialog changes to another selected user', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'operator', role: 100 })
    const view = render(
      <QueryClientProvider client={client}>
        <UserModelConcurrencyDialog
          user={users[0]}
          onOpenChange={() => undefined}
        />
      </QueryClientProvider>
    )
    const dialog = screen.getByRole('dialog', { name: 'Model concurrency' })
    await within(dialog).findByRole('article', { name: 'image-pro' })
    await setFocusedLimit(dialog, '35')
    expect(
      within(dialog).getByRole('button', { name: 'Save Changes' })
    ).toBeEnabled()
    view.rerender(
      <QueryClientProvider client={client}>
        <UserModelConcurrencyDialog
          user={{ id: 72, username: 'other-customer' }}
          onOpenChange={() => undefined}
        />
      </QueryClientProvider>
    )
    expect(
      await within(dialog).findByText(
        'Set model concurrency for other-customer (#72).'
      )
    ).toBeVisible()
    await waitFor(() =>
      expect(
        within(dialog).getByRole('button', { name: 'Save Changes' })
      ).toBeDisabled()
    )
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('shows the user override capacity error when a focused user cannot be added to a full model', async () => {
    users[0].id = 72
    serverData.config.rules[0].user_limits = Array.from(
      { length: 1000 },
      (_, index) => ({ user_id: 1000 + index, max_concurrent: 1 })
    )
    await renderUsers()
    const dialog = await openForUser()
    await userEvent.click(
      await within(dialog).findByRole('button', {
        name: 'Edit concurrency for image-pro',
      })
    )
    const editor = screen.getByRole('dialog', {
      name: 'Edit model concurrency',
    })
    await userEvent.click(
      within(editor).getByRole('button', { name: 'Apply to draft' })
    )
    expect(
      await within(editor).findByText(
        'A model can have at most 1,000 user overrides'
      )
    ).toBeVisible()
    expect(api.patch).not.toHaveBeenCalled()
  })

  it('does not expose either concurrency entry to an administrator or fetch the root-only configuration', async () => {
    await renderUsers(10)
    expect(
      screen.queryByRole('button', { name: 'Model concurrency' })
    ).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Open menu' }))
    expect(
      screen.queryByRole('menuitem', { name: 'Model concurrency' })
    ).not.toBeInTheDocument()
    expect(api.get).not.toHaveBeenCalledWith('/api/option/model_concurrency')
  })

  it('does not fetch configuration when a non-root caller mounts the dialog directly', () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    render(
      <QueryClientProvider client={client}>
        <UserModelConcurrencyDialog
          user={users[0]}
          onOpenChange={() => undefined}
        />
      </QueryClientProvider>
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.get).not.toHaveBeenCalledWith('/api/option/model_concurrency')
  })
})
