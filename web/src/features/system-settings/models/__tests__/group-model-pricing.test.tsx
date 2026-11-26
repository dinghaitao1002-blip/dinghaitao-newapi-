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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { BillingSettings } from '../../billing'
import { SettingsPageProvider } from '../../components/settings-page-context'
import {
  groupPriceExpression,
  groupPriceFormSchema,
  parseGroupModelPrices,
  parseGroupModelPricingForce,
  readGroupPrice,
  type GroupPriceUnit,
} from '../group-model-pricing'
import { GroupModelPricingDialog } from '../group-model-pricing-dialog'
import { GroupRatioForm, type GroupFormValues } from '../group-ratio-form'

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => client.clear())

function Fixture(props: {
  initial?: string
  initialForce?: string
  onSave?: (values: Record<string, unknown>) => Promise<void>
}) {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  const form = useForm<GroupFormValues>({
    defaultValues: {
      GroupRatio: '{"default":1,"vip":0.5}',
      TopupGroupRatio: '{}',
      UserUsableGroups: '{}',
      GroupGroupRatio: '{}',
      AutoGroups: '[]',
      MaxTokenAutoGroups: 5,
      DefaultUseAutoGroup: false,
      GroupSpecialUsableGroup: '{}',
      GroupModelPricing: props.initial ?? '{}',
      GroupModelPricingForce: props.initialForce ?? '{}',
    },
  })
  return (
    <QueryClientProvider client={client}>
      <SettingsPageProvider actionsContainer={actions}>
        <div ref={setActions} />
        <GroupRatioForm
          form={form}
          isSaving={false}
          onSave={props.onSave ?? (async () => {})}
        />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

function mockModels(names = ['image-a', 'video-b', 'token-c']) {
  return vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        items: names.map((model_name) => ({ model_name })),
        total: names.length,
      },
    },
  })
}

const imageRule = {
  unit: 'image',
  expression: 'tier("group_model", fixed(0.007)) * image_count',
  enabled: true,
}

describe('group model pricing', () => {
  it.each([
    ['request', '0.003', ''],
    ['image', '0.007', ''],
    ['second', '0.04', ''],
    ['token', '2.5', '10'],
  ] as const)(
    'round-trips %s prices without losing units or decimals',
    (unit, price, outputPrice) => {
      const expression = groupPriceExpression(unit, price, outputPrice)
      expect(readGroupPrice({ unit, expression, enabled: true })).toEqual({
        price,
        outputPrice,
      })
      expect(
        parseGroupModelPrices(
          JSON.stringify({
            vip: { model: { unit, expression, enabled: true } },
          })
        )?.vip.model.expression
      ).toBe(expression)
    }
  )

  it('rejects negative, blank, duplicate, mismatched and non-finite prices while preserving explicit zero', () => {
    const row = {
      model: 'image-a',
      unit: 'image' as GroupPriceUnit,
      price: '0',
      outputPrice: '',
      enabled: true,
    }
    expect(groupPriceFormSchema.safeParse({ rows: [row] }).success).toBe(true)
    for (const price of ['', '-1', 'Infinity', '1e999']) {
      expect(
        groupPriceFormSchema.safeParse({ rows: [{ ...row, price }] }).success
      ).toBe(false)
    }
    expect(groupPriceFormSchema.safeParse({ rows: [row, row] }).success).toBe(
      false
    )
    expect(
      parseGroupModelPrices(
        JSON.stringify({ vip: { 'image-a': { ...imageRule, unit: 'second' } } })
      )
    ).toBeNull()
    expect(parseGroupModelPrices('{broken')).toBeNull()
  })

  it('applies several model prices only to the selected group and saves them after the page save', async () => {
    mockModels()
    const user = userEvent.setup()
    const onSave = vi.fn(async (_values: Record<string, unknown>) => {})
    const initial = JSON.stringify({
      default: { 'image-a': imageRule },
      vip: {
        'image-a': {
          ...imageRule,
          expression: groupPriceExpression('image', '0.008'),
        },
      },
    })
    render(<Fixture initial={initial} onSave={onSave} />)
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    const dialog = within(screen.getByRole('dialog'))
    await user.clear(dialog.getByRole('textbox', { name: 'Unit price (USD)' }))
    await user.type(
      dialog.getByRole('textbox', { name: 'Unit price (USD)' }),
      '0.000125'
    )
    await user.click(dialog.getByRole('button', { name: 'Add model price' }))
    const row = within(dialog.getByRole('region', { name: 'Model price 2' }))
    await user.click(row.getByRole('combobox', { name: 'Model' }))
    await user.click(await screen.findByRole('option', { name: 'video-b' }))
    await user.click(row.getByRole('combobox', { name: 'Billing unit' }))
    await user.click(screen.getByRole('option', { name: 'Per second' }))
    await user.type(
      row.getByRole('textbox', { name: 'Unit price (USD)' }),
      '0.03'
    )
    await user.click(dialog.getByRole('button', { name: 'Apply to draft' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(onSave).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    const values = onSave.mock.calls[0][0] as unknown as {
      GroupModelPricing: string
      GroupRatio: string
    }
    const prices = JSON.parse(values.GroupModelPricing)
    expect(prices.default['image-a']).toEqual(imageRule)
    expect(prices.vip['image-a'].expression).toBe(
      groupPriceExpression('image', '0.000125')
    )
    expect(prices.vip['video-b']).toEqual({
      unit: 'second',
      expression: groupPriceExpression('second', '0.03'),
      enabled: true,
    })
    expect(JSON.parse(values.GroupRatio)).toEqual({ default: 1, vip: 0.5 })
  })

  it('keeps disabled prices and removes prices in the draft while cancelling discards dialog changes', async () => {
    mockModels()
    const user = userEvent.setup()
    const initial = JSON.stringify({
      vip: {
        'image-a': imageRule,
        'video-b': {
          unit: 'second',
          expression: groupPriceExpression('second', '0.1'),
          enabled: true,
        },
      },
    })
    const onSave = vi.fn(async (_values: Record<string, unknown>) => {})
    render(<Fixture initial={initial} onSave={onSave} />)
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    const dialog = within(screen.getByRole('dialog'))
    await user.click(dialog.getAllByRole('button', { name: 'Remove' })[0])
    await user.click(dialog.getByRole('button', { name: 'Cancel' }))
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    expect(
      within(screen.getByRole('dialog')).getAllByRole('region')
    ).toHaveLength(2)
    await user.click(
      screen.getAllByRole('switch', { name: 'Enable group price' })[0]
    )
    await user.click(
      within(screen.getByRole('dialog')).getAllByRole('button', {
        name: 'Remove',
      })[1]
    )
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(
      JSON.parse(String(onSave.mock.calls[0][0].GroupModelPricing))
    ).toEqual({ vip: { 'image-a': { ...imageRule, enabled: false } } })
  })

  it('applies force-group policy only to the edited group and saves it after parent Save', async () => {
    mockModels()
    const user = userEvent.setup()
    const onSave = vi.fn(async (_values: Record<string, unknown>) => {})
    render(<Fixture initialForce='{"default":true}' onSave={onSave} />)
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    const toggle = screen.getByRole('switch', {
      name: 'Force group model prices',
    })
    expect(toggle).not.toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(onSave).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(
      JSON.parse(String(onSave.mock.calls[0][0].GroupModelPricingForce))
    ).toEqual({ default: true, vip: true })
  })

  it('cancels force-policy changes and retains the saved switch state when reopened', async () => {
    mockModels()
    const user = userEvent.setup()
    render(<Fixture initialForce='{"vip":true}' />)
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    await user.click(
      screen.getByRole('switch', { name: 'Force group model prices' })
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    expect(
      screen.getByRole('switch', { name: 'Force group model prices' })
    ).toBeChecked()
  })

  it('moves group prices and force policy during rename, and removes only the deleted group', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn(async (_values: Record<string, unknown>) => {})
    render(
      <Fixture
        initial={JSON.stringify({
          vip: { 'image-a': imageRule },
          default: { 'image-a': imageRule },
        })}
        initialForce='{"vip":true,"default":true}'
        onSave={onSave}
      />
    )
    const group = screen.getByDisplayValue<HTMLInputElement>('vip')
    await user.clear(group)
    await user.type(group, 'renamed')
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce())
    expect(
      JSON.parse(String(onSave.mock.calls[0][0].GroupModelPricing))
    ).toEqual({
      renamed: { 'image-a': imageRule },
      default: { 'image-a': imageRule },
    })
    expect(
      JSON.parse(String(onSave.mock.calls[0][0].GroupModelPricingForce))
    ).toEqual({ renamed: true, default: true })
    const row = group.closest('tr')
    if (!row) throw new Error('Expected a group row')
    await user.click(within(row).getByRole('button', { name: 'Delete' }))
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(2))
    expect(
      JSON.parse(String(onSave.mock.calls[1][0].GroupModelPricingForce))
    ).toEqual({ default: true })
    expect(
      JSON.parse(String(onSave.mock.calls[1][0].GroupModelPricing))
    ).toEqual({ default: { 'image-a': imageRule } })
  })

  it('rejects non-boolean force policy without replacing it with an empty map', () => {
    expect(parseGroupModelPricingForce('{"vip": "true"}')).toBeNull()
    expect(parseGroupModelPricingForce('{"vip": null}')).toBeNull()
    expect(parseGroupModelPricingForce('{" vip ": true}')).toBeNull()
    expect(parseGroupModelPricingForce('[]')).toBeNull()
    expect(parseGroupModelPricingForce('{"vip": false}')).toEqual({
      vip: false,
    })
    render(<Fixture initialForce='{"vip":"true"}' />)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Group price priority must be a JSON object'
    )
    expect(
      screen.getByRole('button', { name: 'Model prices for vip' })
    ).toBeDisabled()
  })

  it('hydrates saved force policy on the full billing page and keeps the refreshed policy after saving', async () => {
    const options: Record<string, string> = {
      GroupRatio: '{"vip":1}',
      UserUsableGroups: '{"vip":"VIP"}',
      'billing_setting.group_model_pricing': JSON.stringify({
        vip: { 'image-a': imageRule },
      }),
      'billing_setting.group_model_pricing_force': '{"vip":true}',
    }
    vi.spyOn(api, 'get').mockImplementation(async (url) => ({
      data: {
        success: true,
        data:
          url === '/api/option/'
            ? Object.entries(options).map(([key, value]) => ({ key, value }))
            : { items: [], total: 0 },
      },
    }))
    const put = vi
      .spyOn(api, 'put')
      .mockImplementation(async (_url, body: unknown) => {
        const request = body as { key: string; value: string }
        options[request.key] = request.value
        return { data: { success: true } }
      })
    const root = createRootRoute()
    const authenticated = createRoute({
      getParentRoute: () => root,
      id: '_authenticated',
    })
    const billing = createRoute({
      getParentRoute: () => authenticated,
      path: 'system-settings/billing/$section',
      component: BillingSettings,
    })
    const router = createRouter({
      routeTree: root.addChildren([authenticated.addChildren([billing])]),
      history: createMemoryHistory({
        initialEntries: ['/system-settings/billing/group-pricing'],
      }),
    })
    await router.load()
    client.setQueryData(['pricing'], { data: [] })
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Model prices for vip' })
    )
    expect(
      screen.getByRole('switch', { name: 'Force group model prices' })
    ).toBeChecked()
    await user.click(
      screen.getByRole('switch', { name: 'Force group model prices' })
    )
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/option/', {
        key: 'billing_setting.group_model_pricing_force',
        value: '{}',
      })
    )
    await waitFor(() => expect(client.isFetching()).toBe(0))
    expect(client.getQueryState(['pricing'])?.isInvalidated).toBe(true)
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    expect(
      screen.getByRole('switch', { name: 'Force group model prices' })
    ).not.toBeChecked()
  })

  it('loads every catalog page and offers channel-only models with long names for keyboard selection', async () => {
    const longName = 'channel-custom-image-model-with-a-very-long-name'
    const get = vi
      .spyOn(api, 'get')
      .mockImplementation(async (_url, config) => ({
        data: {
          success: true,
          data: {
            items: [
              {
                model_name:
                  (config?.params as { p: number } | undefined)?.p === 1
                    ? 'first-model'
                    : longName,
              },
            ],
            total: 2,
          },
        },
      }))
    const user = userEvent.setup()
    render(<Fixture />)
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    await user.click(screen.getByRole('button', { name: 'Add model price' }))
    const model = screen.getByRole('combobox', { name: 'Model' })
    await waitFor(() => expect(model).toBeEnabled())
    await user.click(model)
    await user.type(model, 'channel-custom')
    await user.keyboard('{ArrowDown}{Enter}')
    expect(model).toHaveValue(longName)
    expect(get).toHaveBeenCalledWith('/api/models/', {
      params: { include_channel_models: true, p: 2, page_size: 100 },
    })
  })

  it('shows a failed catalog response and reloads successfully without discarding existing prices', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Catalog temporarily unavailable' },
      })
      .mockResolvedValue({
        data: {
          success: true,
          data: { items: [{ model_name: 'image-a' }], total: 1 },
        },
      })
    const user = userEvent.setup()
    render(
      <Fixture initial={JSON.stringify({ vip: { 'image-a': imageRule } })} />
    )
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    expect(
      await screen.findByText('Catalog temporarily unavailable')
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(
        screen.queryByText('Catalog temporarily unavailable')
      ).not.toBeInTheDocument()
    )
    expect(
      screen.getByRole('textbox', { name: 'Unit price (USD)' })
    ).toHaveValue('0.007')
  })

  it('requires token output prices and rejects duplicate models before applying', async () => {
    mockModels()
    const user = userEvent.setup()
    const apply = vi.fn()
    render(
      <QueryClientProvider client={client}>
        <GroupModelPricingDialog
          group='vip'
          prices={{
            'token-c': {
              unit: 'token',
              expression: groupPriceExpression('token', '2', '8'),
              enabled: true,
            },
          }}
          onClose={() => {}}
          onApply={apply}
        />
      </QueryClientProvider>
    )
    await user.clear(
      screen.getByRole('textbox', { name: 'Output price (USD / 1M tokens)' })
    )
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(
      await screen.findByText('Enter a finite, non-negative price')
    ).toBeVisible()
    expect(apply).not.toHaveBeenCalled()
    await user.type(
      screen.getByRole('textbox', { name: 'Output price (USD / 1M tokens)' }),
      '0'
    )
    await user.click(screen.getByRole('button', { name: 'Add model price' }))
    const row = within(screen.getByRole('region', { name: 'Model price 2' }))
    await user.click(row.getByRole('combobox', { name: 'Model' }))
    await user.click(await screen.findByRole('option', { name: 'token-c' }))
    await user.type(row.getByRole('textbox', { name: 'Unit price (USD)' }), '0')
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(
      await screen.findByText('This model already has a price in this group')
    ).toBeVisible()
    expect(apply).not.toHaveBeenCalled()
  })

  it('shows invalid JSON rules instead of replacing them with empty prices', async () => {
    render(<Fixture initial='{"vip":{"image-a":{"price":0.5}}}' />)
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Group model prices contain invalid rules'
    )
    expect(
      screen.getByRole('button', { name: 'Model prices for vip' })
    ).toBeDisabled()
  })

  it('shares price changes between the JSON editor and group dialog without saving prematurely', async () => {
    mockModels()
    const user = userEvent.setup()
    const onSave = vi.fn(async (_values: Record<string, unknown>) => {})
    render(<Fixture onSave={onSave} />)
    await user.click(screen.getByRole('button', { name: 'Switch to JSON' }))
    const json = screen.getByRole('textbox', { name: 'Group model prices' })
    const next = JSON.stringify({ vip: { 'image-a': imageRule } })
    fireEvent.input(json, { target: { value: next } })
    await user.click(screen.getByRole('button', { name: 'Switch to Visual' }))
    await user.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    expect(
      screen.getByRole('textbox', { name: 'Unit price (USD)' })
    ).toHaveValue('0.007')
    await user.clear(screen.getByRole('textbox', { name: 'Unit price (USD)' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Unit price (USD)' }),
      '0'
    )
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    await user.click(screen.getByRole('button', { name: 'Switch to JSON' }))
    expect(
      JSON.parse(
        (
          screen.getByRole('textbox', {
            name: 'Group model prices',
          }) as HTMLTextAreaElement
        ).value
      )
    ).toEqual({
      vip: {
        'image-a': {
          ...imageRule,
          expression: groupPriceExpression('image', '0'),
        },
      },
    })
    expect(onSave).not.toHaveBeenCalled()
  })
})
