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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ConcurrencyRuleDialog } from '../concurrency-rule-dialog'

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (String(url).startsWith('/api/user/search?')) {
      return {
        data: {
          success: true,
          data: {
            items: [
              { id: 193, username: 'vq3d5cbm', display_name: 'vq3d5cbm' },
            ],
            total: 1,
          },
        },
      }
    }
    return { data: { success: true, data: { items: [], total: 0 } } }
  })
})
afterEach(() => client.clear())

it('fills an empty manual row with the selected real identity, keeps its limit, and preserves another user override', async () => {
  const save = vi.fn()
  const existing = {
    model: 'image-pro',
    enabled: true,
    max_concurrent: 0,
    default_user_limit: 5,
    other_users_limit: 0,
    user_limits: [{ user_id: 2, max_concurrent: 1 }],
  }
  render(
    <QueryClientProvider client={client}>
      <ConcurrencyRuleDialog
        open
        rule={existing}
        models={['image-pro']}
        onOpenChange={vi.fn()}
        onSave={save}
      />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Add by user ID' }))
  fireEvent.change(
    screen.getByRole('spinbutton', { name: 'Concurrency for override 2' }),
    { target: { value: '8' } }
  )
  await user.type(
    screen.getByRole('textbox', { name: 'Find a user' }),
    'vq3d5cbm'
  )
  await user.click(screen.getByRole('button', { name: 'Search' }))
  const selector = await screen.findByRole('combobox', {
    name: 'Select user to add an override',
  })
  await user.click(selector)
  await user.click(
    await screen.findByRole('option', { name: /vq3d5cbm \(#193\)/ })
  )
  expect(await screen.findByText('vq3d5cbm')).toBeVisible()
  const selectedId = screen.getByRole('spinbutton', {
    name: 'User ID for override 2',
  })
  expect(selectedId).toHaveValue(193)
  expect(selectedId).toHaveAttribute('readonly')
  expect(
    screen.getByRole('spinbutton', { name: 'Concurrency for override 2' })
  ).toHaveValue(8)
  expect(
    screen.queryByRole('spinbutton', { name: 'User ID for override 3' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('spinbutton', { name: 'User ID for override 1' })
  ).toHaveValue(2)
  await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
  await waitFor(() =>
    expect(save).toHaveBeenCalledExactlyOnceWith({
      ...existing,
      user_limits: [
        { user_id: 2, max_concurrent: 1 },
        { user_id: 193, max_concurrent: 8 },
      ],
    })
  )
})
