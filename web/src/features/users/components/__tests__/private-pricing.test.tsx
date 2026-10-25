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
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { UsersMutateDrawer } from '../users-mutate-drawer'
import { UsersProvider } from '../users-provider'

const target: User = {
  id: 23,
  username: 'private-price-user',
  display_name: 'Private price user',
  role: 1,
  status: 1,
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
}
let client: QueryClient | undefined

afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

async function openPricing() {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'operator', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = []
    if (url === '/api/user/23') data = target
    if (url === '/api/group/') data = ['default']
    if (url === '/api/authz/catalog') data = { resources: [], roles: [] }
    if (url === '/api/user/model-prices/per-call-models') {
      data = [{ model_name: 'image-custom', model_price: 0.05 }]
    }
    return { data: { success: true, data } }
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <UsersMutateDrawer open onOpenChange={() => {}} currentRow={target} />
      </UsersProvider>
    </QueryClientProvider>
  )
  await screen.findByDisplayValue('Private price user')
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: '选择按次模型' }))
  await user.click(
    await screen.findByRole('option', { name: 'image-custom / 0.05' })
  )
  return user
}

it('saves an explicit zero private price for the selected user without changing the public model price', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing()
  await user.type(screen.getByPlaceholderText('秘价，例如 0.001'), '0')
  await user.click(screen.getByRole('button', { name: '添加/更新' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith('/api/user/23/model-prices', {
      model_name: 'image-custom',
      price: 0,
      enabled: true,
    })
  )
})

it('does not interpret an empty private-price field as a zero price', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const notify = vi.spyOn(toast, 'error')
  const user = await openPricing()
  await user.click(screen.getByRole('button', { name: '添加/更新' }))
  await waitFor(() => expect(notify).toHaveBeenCalledWith('请输入正确的秘价'))
  expect(post).not.toHaveBeenCalled()
})

it('shows a failed private-price request and keeps the entered price for retry', async () => {
  vi.spyOn(api, 'post').mockRejectedValue(
    new Error('Price service unavailable')
  )
  const notify = vi.spyOn(toast, 'error')
  const user = await openPricing()
  const input = screen.getByPlaceholderText('秘价，例如 0.001')
  await user.type(input, '0.02')
  await user.click(screen.getByRole('button', { name: '添加/更新' }))
  await waitFor(() => expect(notify).toHaveBeenCalled())
  expect(input).toHaveValue('0.02')
})
