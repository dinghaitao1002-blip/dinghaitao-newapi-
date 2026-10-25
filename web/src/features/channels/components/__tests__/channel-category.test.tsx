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
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ChannelCategoryBar } from '../channel-category-bar'
import { ChannelCategorySelector } from '../channel-category-selector'

const clients: QueryClient[] = []

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
})

function categoryClient() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [
        { id: 1, name: 'Original images', channel_count: 2 },
        { id: 2, name: 'Upscaled images', channel_count: 1 },
      ],
    },
  })
  return client
}

it('selects an administrative category without changing the routing group', async () => {
  const select = vi.fn()
  render(
    <QueryClientProvider client={categoryClient()}>
      <ChannelCategoryBar
        selectedCategory='all'
        categoryCounts={{ 'Original images': 2, 'Upscaled images': 1 }}
        onSelect={select}
      />
    </QueryClientProvider>
  )
  const category = await screen.findByRole('button', {
    name: /Original images\s*2/,
  })
  expect(screen.getByRole('button', { name: /All\s*3/ })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
  await userEvent.setup().click(category)
  expect(select).toHaveBeenCalledWith('Original images')
})

it('creates a trimmed category and selects it after the server accepts it', async () => {
  const select = vi.fn()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  render(
    <QueryClientProvider client={categoryClient()}>
      <ChannelCategoryBar selectedCategory='all' onSelect={select} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: 'Create Channel Category' })
  )
  await user.type(screen.getByLabelText('Category Name'), '  Archive  ')
  await user.click(screen.getByRole('button', { name: 'Create' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel/categories',
      { name: 'Archive' },
      expect.anything()
    )
  )
  expect(select).toHaveBeenCalledWith('Archive')
})

it('keeps the category dialog open and selection unchanged when the server rejects creation', async () => {
  const select = vi.fn()
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Category exists' },
  })
  render(
    <QueryClientProvider client={categoryClient()}>
      <ChannelCategoryBar selectedCategory='all' onSelect={select} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(
    screen.getByRole('button', { name: 'Create Channel Category' })
  )
  await user.type(screen.getByLabelText('Category Name'), 'Existing')
  await user.click(screen.getByRole('button', { name: 'Create' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Create' })).not.toBeDisabled()
  )
  expect(select).not.toHaveBeenCalled()
  expect(screen.getByRole('dialog')).toBeVisible()
})

it('assigns only the selected channel category using the dedicated endpoint', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  render(
    <QueryClientProvider client={categoryClient()}>
      <ChannelCategorySelector channelId={83} value='Original images' />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Channel Category' }))
  await user.click(
    await screen.findByRole('option', { name: 'Upscaled images' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel/category/assign',
      { ids: [83], category: 'Upscaled images' },
      expect.anything()
    )
  )
})
