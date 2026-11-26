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
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { CachedPriceCell } from '../components/cached-price-cell'
import { ModelDetailsContent } from '../components/model-details'
import { ModelPriceCell } from '../components/model-price-cell'
import type { PricingModel } from '../types'

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

const base: PricingModel = {
  id: 1,
  model_name: 'custom',
  enable_groups: [],
  quota_type: 0,
  model_ratio: Number.NaN,
  completion_ratio: Number.NaN,
}
afterEach(cleanup)

it.each([
  ['image', 'tier("group_model", fixed(0.01)) * image_count'],
  ['token', 'tier("group_model", p * 10 + c * 40)'],
] as const)(
  'does not show global cache pricing after a %s group override replaces it',
  (unit, expression) => {
    render(
      <CachedPriceCell
        model={{
          ...base,
          enable_groups: ['default', 'vip'],
          group_ratio: { default: 1, vip: 0.5 },
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", p * 5 + c * 30 + cr * 1.25)',
          group_model_pricing: { vip: { unit, expression, enabled: true } },
        }}
        options={{ selectedGroup: 'vip', tokenUnit: 'M' }}
      />
    )
    expect(screen.getByText('—')).toBeVisible()
    expect(screen.queryByText(/1\.25/)).not.toBeInTheDocument()
  }
)

it('retains global cache pricing in groups without an override', () => {
  render(
    <CachedPriceCell
      model={{
        ...base,
        enable_groups: ['default', 'vip'],
        group_ratio: { default: 1, vip: 0.5 },
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("base", p * 5 + c * 30 + cr * 1.25)',
        group_model_pricing: {
          vip: {
            unit: 'image',
            expression: 'tier("group_model", fixed(0.01)) * image_count',
            enabled: true,
          },
        },
      }}
      options={{ selectedGroup: 'default', tokenUnit: 'M' }}
    />
  )
  expect(screen.getByText(/1\.25/)).toBeVisible()
})

it('does not quote a global token cache price when all-group prices have different units', () => {
  render(
    <CachedPriceCell
      model={{
        ...base,
        enable_groups: ['default', 'vip'],
        group_ratio: { default: 1, vip: 0.5 },
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("base", p * 5 + c * 30 + cr * 1.25)',
        group_model_pricing: {
          vip: {
            unit: 'image',
            expression: 'tier("group_model", fixed(0.01)) * image_count',
            enabled: true,
          },
        },
      }}
      options={{ tokenUnit: 'M' }}
    />
  )
  expect(screen.getByText('—')).toBeVisible()
})

it('shows a quality model group override and preserves the quality grid for other groups', () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={{
          ...base,
          enable_groups: ['default', 'vip'],
          billing_mode: 'image_quality',
          image_quality_pricing_enabled: true,
          image_quality_prices: [
            { quality: 'high', resolution: '1k', price: 0.1, enabled: true },
          ],
          group_model_pricing: {
            vip: {
              unit: 'image',
              expression: 'tier("group_model", fixed(0.2)) * image_count',
              enabled: true,
            },
          },
        }}
        groupRatio={{ default: 2, vip: 0.5 }}
        usableGroup={{
          default: { desc: '', ratio: 2 },
          vip: { desc: '', ratio: 0.5 },
        }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )
  expect(screen.getByText('Pricing by Group')).toBeVisible()
  expect(screen.getByText('0.2')).toBeVisible()
  expect(screen.getByText(/^0\.1\//)).toBeVisible()
  expect(screen.getByText('按图片品质收费')).toBeVisible()
})

it('keeps both global context tiers visible for unmodified groups while showing the vip override', () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={{
          ...base,
          enable_groups: ['default', 'vip'],
          billing_mode: 'tiered_expr',
          billing_expr:
            'len <= 200000 ? tier("small-context", p * 3 + c * 15) : tier("large-context", p * 6 + c * 22.5)',
          group_model_pricing: {
            vip: {
              unit: 'request',
              expression: 'tier("group_model", fixed(0.004))',
              enabled: true,
            },
          },
        }}
        groupRatio={{ default: 2, vip: 1 }}
        usableGroup={{
          default: { desc: '', ratio: 2 },
          vip: { desc: '', ratio: 1 },
        }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )
  expect(screen.getByText('small-context')).toBeVisible()
  expect(screen.getByText('large-context')).toBeVisible()
  expect(screen.getByText(/^0\.004\//)).toBeVisible()
  client.clear()
})

it('labels quality matrix billing instead of displaying an unset token price', () => {
  render(<ModelPriceCell model={{ ...base, billing_mode: 'image_quality' }} />)
  expect(screen.getByText('按品质收费')).toBeVisible()
  expect(screen.queryByText('Unset price')).not.toBeInTheDocument()
})

it('shows zero and paid resolution prices while leaving disabled resolutions unset for per-second billing', () => {
  render(
    <ModelPriceCell
      model={{
        ...base,
        billing_mode: 'video_per_second',
        billing_expr: '{"720p":0,"1080p":0.12}',
      }}
    />
  )
  expect(screen.getByText('720P')).toBeVisible()
  expect(screen.getByText('$0')).toBeVisible()
  expect(screen.getByText('$0.12')).toBeVisible()
  expect(screen.getAllByText('-')).toHaveLength(2)
  expect(screen.getByText('second')).toBeVisible()
  expect(screen.queryByText('Unset price')).not.toBeInTheDocument()
})

it('shows the quality matrix without fallback token or group prices when a built-in expression is also present', () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={{
          ...base,
          model_name: 'gpt-image-2',
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("builtin", p * 5 + c * 30)',
          model_ratio: 2.5,
          completion_ratio: 6,
          image_quality_pricing_enabled: true,
          image_quality_prices: [
            { quality: 'low', resolution: '1k', price: 0.05, enabled: true },
          ],
        }}
        groupRatio={{ default: 2 }}
        usableGroup={{ default: { desc: '', ratio: 2 } }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )
  expect(screen.getByText('按图片品质收费')).toBeVisible()
  expect(screen.getByText('0.05')).toBeVisible()
  expect(screen.queryByText('Pricing by Group')).not.toBeInTheDocument()
  expect(screen.queryByText('Input / 1M token')).not.toBeInTheDocument()
  expect(screen.queryByText('builtin')).not.toBeInTheDocument()
  client.clear()
})
