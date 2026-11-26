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
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { SettingsPageProvider } from '../../components/settings-page-context'
import {
  groupPriceExpression,
  groupPriceFormSchema,
  parseGroupModelPrices,
  readGroupPrice,
  type GroupPriceUnit,
} from '../group-model-pricing'
import { GroupModelPricingDialog } from '../group-model-pricing-dialog'
import { GroupRatioForm, type GroupFormValues } from '../group-ratio-form'

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => {
  client.clear()
  useAuthStore.getState().auth.setUser(null)
})

function Fixture(props: {
  initial?: string
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

describe('concurrency entry points within group pricing', () => {
  it('opens a group with no price rules and does not save settings just by browsing', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'operator', role: 100 })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          config: { enabled: true, rules: [] },
          usage: [],
          usage_available: true,
          storage: 'redis',
        },
      },
    })
    const save = vi.spyOn(api, 'patch')
    render(<Fixture />)
    await userEvent.click(
      screen.getByRole('button', { name: 'Model concurrency for group vip' })
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Model concurrency for group vip',
    })
    expect(
      await within(dialog).findByText('No model concurrency rules')
    ).toBeVisible()
    expect(
      within(dialog).getByRole('button', { name: 'Add model rule' })
    ).toBeEnabled()
    expect(save).not.toHaveBeenCalled()
  })
  it('saves model concurrency separately and preserves an unsaved price in the underlying dialog', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'operator', role: 100 })
    const concurrency = {
      enabled: true,
      rules: [
        {
          model: 'image-a',
          enabled: true,
          max_concurrent: 20,
          default_user_limit: 3,
          other_users_limit: 0,
          user_limits: [{ user_id: 193, max_concurrent: 12 }],
        },
      ],
    }
    vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/models/') {
        return {
          data: {
            success: true,
            data: { items: [{ model_name: 'image-a' }], total: 1 },
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            config: concurrency,
            usage: [],
            storage: 'redis',
            usage_available: true,
          },
        },
      }
    })
    const patch = vi
      .spyOn(api, 'patch')
      .mockImplementation(async (_url, body) => ({
        data: { success: true, data: body },
      }))
    const onSave = vi.fn(async (_values: Record<string, unknown>) => {})
    render(
      <Fixture
        initial={JSON.stringify({ vip: { 'image-a': imageRule } })}
        onSave={onSave}
      />
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Model prices for vip' })
    )
    const price = screen.getByRole('textbox', { name: 'Unit price (USD)' })
    fireEvent.change(price, { target: { value: '0.123' } })
    await userEvent.click(
      screen.getByRole('button', { name: 'User concurrency for image-a' })
    )
    const rule = await screen.findByRole('dialog', {
      name: 'Add model concurrency',
    })
    expect(
      within(rule).getByRole('spinbutton', {
        name: 'Default concurrency per user',
      })
    ).toHaveValue(3)
    fireEvent.change(
      within(rule).getByRole('spinbutton', {
        name: 'Default concurrency per user',
      }),
      { target: { value: '5' } }
    )
    await userEvent.click(
      within(rule).getByRole('button', { name: 'Apply to draft' })
    )
    const group = screen.getByRole('dialog', {
      name: 'Model concurrency for group vip',
    })
    await userEvent.click(
      within(group).getByRole('button', { name: 'Save Changes' })
    )
    await waitFor(() => expect(patch).toHaveBeenCalledOnce())
    expect(patch.mock.calls[0][0]).toBe('/api/option/model_concurrency')
    expect(
      (patch.mock.calls[0][1] as { config: { rules: unknown[] } }).config.rules
    ).toEqual([
      concurrency.rules[0],
      { ...concurrency.rules[0], group: 'vip', default_user_limit: 5 },
    ])
    expect(onSave).not.toHaveBeenCalled()
    await userEvent.click(within(group).getByRole('button', { name: 'Close' }))
    expect(
      screen.getByRole('textbox', { name: 'Unit price (USD)' })
    ).toHaveValue('0.123')
  })
  it('disables concurrency for a renamed group until group settings have been saved', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'operator', role: 100 })
    render(<Fixture />)
    const names = screen.getAllByRole('textbox', { name: 'Group name' })
    const vip = names.find(
      (input) => (input as HTMLInputElement).value === 'vip'
    )
    if (!vip) {
      throw new Error('Expected the saved vip group input')
    }
    fireEvent.change(vip, { target: { value: 'vip-new' } })
    expect(
      screen.getByRole('button', {
        name: 'Model concurrency for group vip-new',
      })
    ).toBeDisabled()
    expect(
      screen.getByRole('button', {
        name: 'Model concurrency for group default',
      })
    ).toBeEnabled()
  })
})
