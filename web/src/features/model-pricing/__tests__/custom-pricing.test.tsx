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
import { useAuthStore } from '@/stores/auth-store'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { ModelPricingPanel } from '../model-pricing-panel'
import { pricingOptions } from '../pricing'

let client: QueryClient | undefined

afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.getState().auth.setUser(null)
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  localStorage.clear()
  vi.restoreAllMocks()
})

function renderQualityPricing(onDirtyChange?: (dirty: boolean) => void) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'administrator', role: 100 })
  usePricingPreferencesStore.setState({ currency: 'USD' })
  const prices = ['low', 'medium', 'high'].flatMap((quality) =>
    ['1k', '2k', '4k'].map((resolution) => ({
      quality,
      resolution,
      price: 0.05,
      enabled: true,
    }))
  )
  const values = { 'billing_setting.billing_mode': 'image_quality' }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/model_pricing') {
      return {
        data: {
          success: true,
          data: {
            entries: [
              {
                model_name: 'image-custom',
                version: 'v1',
                configured: values,
                effective: values,
              },
            ],
            options: pricingOptions({}),
            empty_version: 'empty',
          },
        },
      }
    }
    if (url === '/api/models/image-quality-prices') {
      return { data: { success: true, data: prices } }
    }
    if (url === '/api/pricing') {
      return { data: { success: true, data: [], vendors: [] } }
    }
    return { data: { success: true, data: {} } }
  })
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  client.setQueryData(['status'], null)
  render(
    <QueryClientProvider client={client}>
      <ModelPricingPanel
        modelName='image-custom'
        onDirtyChange={onDirtyChange}
      />
    </QueryClientProvider>
  )
  return prices
}

it('saves the quality matrix before the official versioned model-pricing update when Save model prices is clicked', async () => {
  const writes: string[] = []
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    writes.push(String(url))
    return { data: { success: true } }
  })
  vi.spyOn(api, 'patch').mockImplementation(async (url) => {
    writes.push(String(url))
    return { data: { success: true } }
  })
  const prices = renderQualityPricing()
  await screen.findAllByDisplayValue('0.05')
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Save model prices' }))
  await waitFor(() =>
    expect(writes).toEqual([
      '/api/models/image-quality-prices',
      '/api/option/model_pricing',
    ])
  )
  expect(post).toHaveBeenCalledWith('/api/models/image-quality-prices', {
    model_name: 'image-custom',
    enabled: true,
    prices,
  })
})

it('does not enable the quality billing mode when saving its matrix fails', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Matrix save failed' },
  })
  const patch = vi
    .spyOn(api, 'patch')
    .mockResolvedValue({ data: { success: true } })
  renderQualityPricing()
  await screen.findAllByDisplayValue('0.05')
  const save = screen.getByRole('button', { name: 'Save model prices' })
  await userEvent.setup().click(save)
  await waitFor(() => expect(api.post).toHaveBeenCalled())
  expect(patch).not.toHaveBeenCalled()
})

it('marks quality matrix edits dirty so closing the editor can warn about unsaved prices', async () => {
  const dirty = vi.fn()
  renderQualityPricing(dirty)
  await screen.findAllByDisplayValue('0.05')
  dirty.mockClear()
  const price = screen.getByRole('textbox', { name: 'low 1k' })
  const user = userEvent.setup()
  await user.clear(price)
  await user.type(price, '0.09')
  await waitFor(() => expect(dirty).toHaveBeenLastCalledWith(true))
})

it('displays quality prices in the selected currency and saves canonical USD', async () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7,
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  vi.spyOn(api, 'patch').mockResolvedValue({ data: { success: true } })
  renderQualityPricing()
  await screen.findAllByDisplayValue('0.05')
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Pricing currency' }))
  await user.click(
    await screen.findByRole('option', { name: 'Site currency (CNY)' })
  )
  const price = screen.getByRole('textbox', { name: 'low 1k' })
  expect(price).toHaveValue('0.35')
  await user.clear(price)
  await user.type(price, '0.7')
  await user.click(screen.getByRole('button', { name: 'Save model prices' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/models/image-quality-prices',
      expect.objectContaining({
        prices: expect.arrayContaining([
          { quality: 'low', resolution: '1k', price: 0.1, enabled: true },
        ]),
      })
    )
  )
})
