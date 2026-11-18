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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { ConcurrencyRuleDialog } from '../concurrency-rule-dialog'

let client: QueryClient

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(() => client.clear())

describe('model concurrency model selection', () => {
  it('loads every catalog page, searches a configured custom model, and applies the selected name to the draft', async () => {
    const firstPage = Array.from({ length: 100 }, (_, id) => ({
      id,
      model_name: `catalog-model-${id}`,
      name_rule: 0,
      status: 1,
    }))
    const secondPage = [
      {
        id: 101,
        model_name: 'gemini-3-pro-image-preview',
        name_rule: 0,
        status: 0,
      },
      { id: 102, model_name: 'gemini-*', name_rule: 1, status: 1 },
      { id: 103, model_name: 'catalog-model-0', name_rule: 0, status: 1 },
    ]
    const get = vi
      .spyOn(api, 'get')
      .mockImplementation(async (_url, config) => {
        const params = config?.params as { p?: number } | undefined
        return {
          data: {
            success: true,
            data: {
              items: params?.p === 1 ? firstPage : secondPage,
              total: 103,
              page: params?.p,
              page_size: 100,
            },
          },
        }
      })
    const save = vi.fn()
    render(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open
          onOpenChange={vi.fn()}
          rule={null}
          models={[]}
          onSave={save}
        />
      </QueryClientProvider>
    )
    const input = screen.getByRole('combobox', { name: 'Model name' })
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    expect(get).toHaveBeenNthCalledWith(1, '/api/models/', {
      params: { include_channel_models: true, p: 1, page_size: 100 },
    })
    expect(get).toHaveBeenNthCalledWith(2, '/api/models/', {
      params: { include_channel_models: true, p: 2, page_size: 100 },
    })
    const user = userEvent.setup()
    await user.click(input)
    expect(
      screen.getAllByRole('option', { name: 'catalog-model-0' })
    ).toHaveLength(1)
    await user.type(input, 'gemini')
    const option = await screen.findByRole('option', {
      name: 'gemini-3-pro-image-preview',
    })
    expect(
      screen.queryByRole('option', { name: 'gemini-*' })
    ).not.toBeInTheDocument()
    await user.click(option)
    expect(input).toHaveValue('gemini-3-pro-image-preview')
    expect(save).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(save).toHaveBeenCalledExactlyOnceWith({
      model: 'gemini-3-pro-image-preview',
      enabled: true,
      max_concurrent: 0,
      default_user_limit: 0,
      other_users_limit: 0,
      user_limits: [],
    })
  })

  it('does not fetch before the dialog opens and supports selecting a model with the keyboard', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          items: [{ model_name: 'image-pro', name_rule: 0 }],
          total: 1,
        },
      },
    })
    const save = vi.fn()
    const view = render(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open={false}
          onOpenChange={vi.fn()}
          rule={null}
          models={[]}
          onSave={save}
        />
      </QueryClientProvider>
    )
    expect(get).not.toHaveBeenCalled()
    view.rerender(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open
          onOpenChange={vi.fn()}
          rule={null}
          models={[]}
          onSave={save}
        />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Model name' })
    await waitFor(() => expect(get).toHaveBeenCalledOnce())
    await user.click(input)
    await user.type(input, 'image')
    await screen.findByRole('option', { name: 'image-pro' })
    await user.keyboard('{ArrowDown}{Enter}')
    expect(input).toHaveValue('image-pro')
    expect(input).toHaveAttribute('aria-expanded', 'false')
    expect(save).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(save.mock.calls[0]?.[0].model).toBe('image-pro')
  })

  it('allows entering a new model when the catalog is empty', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    const save = vi.fn()
    render(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open
          onOpenChange={vi.fn()}
          rule={null}
          models={[]}
          onSave={save}
        />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.type(
      screen.getByRole('combobox', { name: 'Model name' }),
      'custom-future-model'
    )
    expect(await screen.findByText('No matching models')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(save.mock.calls[0]?.[0].model).toBe('custom-future-model')
  })

  it('shows a partial-page failure, keeps manual entry usable, and refreshes the whole catalog on retry', async () => {
    let failSecondPage = true
    vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
      const params = config?.params as { p?: number } | undefined
      if (params?.p === 2 && failSecondPage) {
        return { data: { success: false, message: 'Catalog unavailable' } }
      }
      const items =
        params?.p === 1
          ? Array.from({ length: 100 }, () => ({
              model_name: 'first-page-model',
              name_rule: 0,
            }))
          : [{ model_name: 'last-page-model', name_rule: 0 }]
      return { data: { success: true, data: { items, total: 101 } } }
    })
    const save = vi.fn()
    render(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open
          onOpenChange={vi.fn()}
          rule={null}
          models={[]}
          onSave={save}
        />
      </QueryClientProvider>
    )
    expect(
      await screen.findByText(
        'Failed to load models. Retry or enter a model name.'
      )
    ).toBeVisible()
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Model name' })
    await user.click(input)
    expect(
      screen.queryByRole('option', { name: 'first-page-model' })
    ).not.toBeInTheDocument()
    await user.type(input, 'my-custom-model')
    failSecondPage = false
    await user.click(screen.getByRole('button', { name: 'Refresh models' }))
    await waitFor(() =>
      expect(
        screen.queryByText(
          'Failed to load models. Retry or enter a model name.'
        )
      ).not.toBeInTheDocument()
    )
    expect(input).toHaveValue('my-custom-model')
    await user.clear(input)
    await user.type(input, 'last-page')
    await user.click(
      await screen.findByRole('option', { name: 'last-page-model' })
    )
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(save.mock.calls[0]?.[0].model).toBe('last-page-model')
  })

  it('preserves an old configured name missing from the catalog when editing a rule', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    const save = vi.fn()
    const rule = {
      model: 'retired-model',
      enabled: true,
      max_concurrent: 10,
      default_user_limit: 2,
      other_users_limit: 2,
      user_limits: [],
    }
    render(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open
          onOpenChange={vi.fn()}
          rule={rule}
          models={['retired-model']}
          onSave={save}
        />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Model name' })
    expect(input).toHaveValue('retired-model')
    await user.click(input)
    expect(
      await screen.findByRole('option', { name: 'retired-model' })
    ).toBeVisible()
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: 'Apply to draft' }))
    expect(save).toHaveBeenCalledExactlyOnceWith(rule)
  })

  it('does not fetch models or allow model changes while editing only a focused user override', async () => {
    const get = vi.spyOn(api, 'get')
    const rule = {
      model: 'image-pro',
      enabled: true,
      max_concurrent: 10,
      default_user_limit: 2,
      other_users_limit: 2,
      user_limits: [{ user_id: 42, max_concurrent: 4 }],
    }
    render(
      <QueryClientProvider client={client}>
        <ConcurrencyRuleDialog
          open
          onOpenChange={vi.fn()}
          rule={rule}
          models={['image-pro']}
          focusedUser={{ id: 42, username: 'customer' }}
          onSave={vi.fn()}
        />
      </QueryClientProvider>
    )
    expect(screen.getByRole('combobox', { name: 'Model name' })).toBeDisabled()
    expect(get).not.toHaveBeenCalled()
    expect(
      screen.queryByRole('button', { name: 'Refresh models' })
    ).not.toBeInTheDocument()
  })
})
