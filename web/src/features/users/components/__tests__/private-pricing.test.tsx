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
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type {
  PerCallPricedModel,
  User,
  UserModelImageQualityPrice,
  UserModelPrice,
} from '../../types'
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

async function openPricing(
  model: PerCallPricedModel = {
    model_name: 'image-custom',
    model_price: 0.05,
  },
  privatePrices: UserModelPrice[] = [],
  qualityPrices: UserModelImageQualityPrice[] = [],
  pendingModels?: Promise<PerCallPricedModel[]>
) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'operator', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = []
    if (url === '/api/user/23') data = target
    if (url === '/api/group/') data = ['default']
    if (url === '/api/authz/catalog') data = { resources: [], roles: [] }
    if (url === '/api/user/model-prices/per-call-models') {
      data = pendingModels ? await pendingModels : [model]
    }
    if (url === '/api/user/23/model-prices') data = privatePrices
    if (url === '/api/user/23/model-image-quality-prices') data = qualityPrices
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
  if (pendingModels) return user
  await user.click(
    screen.getByRole('combobox', { name: 'Select a model for private pricing' })
  )
  await user.click(
    await screen.findByRole('option', {
      name: `${model.model_name} / ${model.model_price}${model.pricing_unit === 'second' ? ' / second' : ''}`,
    })
  )
  return user
}

it.each(['gpt-image-2.6-flare', 'gpt-image-2.6-sunburst'])(
  'saves a private price when %s is returned as a per-image fixed-price model',
  async (modelName) => {
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true } })
    const notify = vi.spyOn(toast, 'success')
    const user = await openPricing({
      model_name: modelName,
      model_price: 0.007,
      pricing_unit: 'image',
    })

    expect(
      screen.getByText(
        'Supports private prices per request, per image, and per second. Video prices are charged per second of requested duration. Other billing rules remain unchanged.'
      )
    ).toBeVisible()
    await user.type(screen.getByPlaceholderText('秘价，例如 0.001'), '0.004')
    await user.click(screen.getByRole('button', { name: '添加/更新' }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/user/23/model-prices', {
        model_name: modelName,
        price: 0.004,
        enabled: true,
        unified_image_price: true,
      })
    )
    await waitFor(() => expect(notify).toHaveBeenCalledWith('模型秘价已保存'))
    expect(screen.getByPlaceholderText('秘价，例如 0.001')).toHaveValue('')
  }
)

it('saves a per-second video private price and does not offer image quality pricing', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing({
    model_name: 'veo-3.1-fast',
    model_price: 0.05,
    pricing_unit: 'second',
  })

  expect(
    screen.queryByRole('button', { name: 'Set image price' })
  ).not.toBeInTheDocument()
  const input = screen.getByPlaceholderText(
    'Private price per second, e.g. 0.03'
  )
  await user.type(input, '0.025')
  await user.click(screen.getByRole('button', { name: '添加/更新' }))

  await waitFor(() =>
    expect(post).toHaveBeenCalledWith('/api/user/23/model-prices', {
      model_name: 'veo-3.1-fast',
      price: 0.025,
      enabled: true,
    })
  )
})

it('labels an existing video private price per second and deletes only that user override', async () => {
  const remove = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing(
    { model_name: 'omni-1.2-flash', model_price: 0.06, pricing_unit: 'second' },
    [
      {
        id: 1,
        user_id: target.id,
        model_name: 'omni-1.2-flash',
        price: 0.02,
        enabled: true,
      },
    ]
  )

  expect(screen.getByText('秘价: 0.02 / second / 启用')).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Edit image price' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: '删除' }))
  await waitFor(() =>
    expect(remove).toHaveBeenCalledWith('/api/user/23/model-prices', {
      params: { model_name: 'omni-1.2-flash' },
    })
  )
})

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

it('edits one image unit price and saves a unified override for the selected user and model', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing(
    {
      model_name: 'gemini-3-pro-image-preview',
      model_price: 0.08,
      pricing_unit: 'image',
    },
    [
      {
        id: 1,
        user_id: target.id,
        model_name: 'gemini-3-pro-image-preview',
        price: 0.06,
        enabled: true,
      },
    ]
  )
  await user.click(screen.getByRole('button', { name: 'Edit image price' }))
  const sheet = await screen.findByRole('dialog', {
    name: 'Image private price',
  })
  const price = within(sheet).getByRole('textbox', {
    name: 'Price per image (USD)',
  })
  expect(price).toHaveValue('0.06')
  expect(within(sheet).queryByText('low')).not.toBeInTheDocument()
  expect(within(sheet).queryByText('4K')).not.toBeInTheDocument()
  await user.clear(price)
  await user.type(price, '0.025')
  await user.click(
    within(sheet).getByRole('button', { name: 'Save image price' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith('/api/user/23/model-prices', {
      model_name: 'gemini-3-pro-image-preview',
      price: 0.025,
      enabled: true,
      unified_image_price: true,
    })
  )
  await waitFor(() => expect(sheet).not.toBeInTheDocument())
})

it('leaves differing legacy tier prices unchanged until a uniform image price is explicitly saved', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValue(new Error('Price service unavailable'))
  const remove = vi.spyOn(api, 'delete')
  const user = await openPricing(
    {
      model_name: 'gemini-3-pro-image-preview',
      model_price: 0.08,
      pricing_unit: 'image',
    },
    [],
    [
      {
        id: 1,
        user_id: target.id,
        model_name: 'gemini-3-pro-image-preview',
        quality: 'low',
        resolution: '1k',
        price: 0.02,
        enabled: true,
      },
      {
        id: 2,
        user_id: target.id,
        model_name: 'gemini-3-pro-image-preview',
        quality: 'high',
        resolution: '4k',
        price: 0.08,
        enabled: true,
      },
    ]
  )
  await user.click(screen.getByRole('button', { name: 'Set image price' }))
  const sheet = await screen.findByRole('dialog', {
    name: 'Image private price',
  })
  const price = within(sheet).getByRole('textbox', {
    name: 'Price per image (USD)',
  })
  expect(price).toHaveValue('')
  expect(
    within(sheet).getByText(
      'Existing tier prices differ. Enter a single price to replace them when you save.'
    )
  ).toBeVisible()
  await user.click(
    within(sheet).getByRole('button', { name: 'Save image price' })
  )
  expect(post).not.toHaveBeenCalled()
  expect(remove).not.toHaveBeenCalled()
  await user.type(price, '0.03')
  await user.click(
    within(sheet).getByRole('button', { name: 'Save image price' })
  )
  await waitFor(() => expect(post).toHaveBeenCalled())
  expect(price).toHaveValue('0.03')
  expect(sheet).toBeVisible()
})

it('deletes both image price representations only for the edited user and model', async () => {
  const remove = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing({
    model_name: 'gpt-image-2.6-sunburst',
    model_price: 0.007,
    pricing_unit: 'image',
  })
  await user.click(screen.getByRole('button', { name: 'Set image price' }))
  const sheet = await screen.findByRole('dialog', {
    name: 'Image private price',
  })
  await user.click(
    within(sheet).getByRole('button', { name: 'Delete image price' })
  )
  await waitFor(() =>
    expect(remove).toHaveBeenCalledWith('/api/user/23/model-prices', {
      params: {
        model_name: 'gpt-image-2.6-sunburst',
        unified_image_price: true,
      },
    })
  )
})

it('waits for model metadata before allowing list deletion and then removes both image price representations', async () => {
  const model: PerCallPricedModel = {
    model_name: 'gpt-image-2.6-sunburst',
    model_price: 0.007,
    pricing_unit: 'image',
  }
  let resolveModels!: (models: PerCallPricedModel[]) => void
  const pendingModels = new Promise<PerCallPricedModel[]>((resolve) => {
    resolveModels = resolve
  })
  const remove = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing(
    model,
    [
      {
        id: 1,
        user_id: target.id,
        model_name: model.model_name,
        price: 0.004,
        enabled: true,
      },
    ],
    [],
    pendingModels
  )
  const button = await screen.findByRole('button', { name: '删除' })
  expect(button).toBeDisabled()
  await user.click(button)
  expect(remove).not.toHaveBeenCalled()
  resolveModels([model])
  await waitFor(() => expect(button).toBeEnabled())
  await user.click(button)
  await waitFor(() =>
    expect(remove).toHaveBeenCalledWith('/api/user/23/model-prices', {
      params: { model_name: model.model_name, unified_image_price: true },
    })
  )
})

it('also clears legacy image tiers when deleting a private price for a retired model', async () => {
  const remove = vi
    .spyOn(api, 'delete')
    .mockResolvedValue({ data: { success: true } })
  const user = await openPricing(undefined, [
    {
      id: 1,
      user_id: target.id,
      model_name: 'retired-image',
      price: 0.02,
      enabled: true,
    },
  ])
  await user.click(screen.getByRole('button', { name: '删除' }))
  await waitFor(() =>
    expect(remove).toHaveBeenCalledWith('/api/user/23/model-prices', {
      params: { model_name: 'retired-image', unified_image_price: true },
    })
  )
})
