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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import type {
  ModelPricingConfig,
  ModelPricingPluginVariant,
} from '@/features/model-pricing/api'
import { USD_PRICING_CURRENCY } from '@/features/model-pricing/currency'
import { ModelPricingPanel } from '@/features/model-pricing/model-pricing-panel'
import { pricingOptions } from '@/features/model-pricing/pricing'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'

import {
  ModelPricingEditorPanel,
  type ModelPricingEditorPanelHandle,
  type ModelRatioData,
} from '../model-pricing-sheet'
import { TaskPluginPricingEditor } from '../task-plugin-pricing-editor'

let client: QueryClient | undefined

afterEach(() => {
  client?.clear()
  useAuthStore.getState().auth.setUser(null)
  localStorage.clear()
  vi.restoreAllMocks()
})

it('keeps provider drafts across tabs and saves nested expressions with the model version', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'administrator', role: 100 })
  usePricingPreferencesStore.setState({ currency: 'USD' })
  const expression = 'tier("base", u("seconds") * 0.4)'
  const values = {
    'billing_setting.billing_mode': 'tiered_expr',
    'billing_setting.billing_expr': expression,
  }
  const snapshot: ModelPricingConfig = {
    entries: [
      {
        model_name: 'shared',
        version: 'version-with-providers',
        configured: values,
        effective: values,
        usage_schema: {
          seconds: {
            type: 'number',
            unit: 'second',
            description: 'Video unit price',
          },
        },
        plugin_variants: [
          {
            plugin_key: 'alpha',
            plugin_name: 'Alpha',
            usage_schema: {
              seconds: {
                type: 'number',
                unit: 'second',
                description: 'Video unit price',
              },
            },
            configured: '',
            effective: expression,
            compatible: true,
          },
          {
            plugin_key: 'beta',
            plugin_name: 'Beta',
            usage_schema: {
              credits: {
                type: 'number',
                unit: 'credit',
                description: 'Credit unit price',
              },
            },
            configured: '',
            effective: expression,
            compatible: false,
          },
        ],
      },
    ],
    options: pricingOptions({}),
    empty_version: 'empty',
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/model_pricing') {
      return { data: { success: true, data: snapshot } }
    }
    if (url === '/api/pricing') {
      return { data: { success: true, data: [], vendors: [] } }
    }
    return { data: { success: true, data: {} } }
  })
  let finishSave: (() => void) | undefined
  const save = vi.spyOn(api, 'patch').mockImplementation(
    () =>
      new Promise((resolve) => {
        finishSave = () => resolve({ data: { success: true } })
      })
  )
  const dirty = vi.fn()
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelPricingPanel modelName='shared' onDirtyChange={dirty} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  const defaultTab = await screen.findByRole('tab', { name: 'Default' })
  expect(defaultTab).toHaveAttribute('aria-selected', 'true')
  expect(
    await screen.findByText(
      'The model-level expression cannot be evaluated by: Beta'
    )
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Beta' }))
  const betaPanel = screen.getByRole('tabpanel', { name: 'Beta' })
  expect(
    within(betaPanel).getByText('Not configured for this provider')
  ).toBeVisible()
  const separate = within(betaPanel).getByRole('switch', {
    name: 'Set separately for this provider',
  })
  expect(separate).not.toBeChecked()
  await user.click(separate)
  expect(separate).toBeChecked()
  await user.click(separate)
  expect(separate).not.toBeChecked()
  expect(
    within(betaPanel).getByText('Use model-level expression')
  ).toBeVisible()
  expect(
    within(betaPanel).queryByRole('textbox', { name: 'credits' })
  ).not.toBeInTheDocument()
  await user.click(separate)
  const price = within(betaPanel).getByRole('textbox', { name: 'credits' })
  await user.clear(price)
  await user.type(price, '1.5')
  expect(dirty).toHaveBeenLastCalledWith(true)
  await user.click(defaultTab)
  expect(
    screen.queryByText(
      'The model-level expression cannot be evaluated by: Beta'
    )
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Beta' }))
  expect(price).toHaveValue('1.5')
  await user.click(screen.getByRole('tab', { name: 'Ordinary OpenAI' }))
  await user.click(
    screen.getByRole('button', { name: 'Configure ordinary OpenAI pricing' })
  )
  const ordinaryPrice = screen.getByRole('textbox', { name: 'Input price' })
  await user.clear(ordinaryPrice)
  await user.type(ordinaryPrice, '2')
  const saveButton = screen.getByRole('button', { name: 'Save model prices' })
  await user.click(saveButton)
  await waitFor(() => expect(save).toHaveBeenCalledOnce())
  expect(saveButton).toBeDisabled()
  const changes = save.mock.calls[0][1] as {
    changes: { expected_version: string; pricing: Record<string, unknown> }[]
  }
  expect(changes.changes[0].expected_version).toBe('version-with-providers')
  expect(changes.changes[0].pricing['billing_setting.billing_expr']).toBe(
    'tier("base", p * 2 + c * 0)'
  )
  expect(
    changes.changes[0].pricing['billing_setting.plugin_billing_expr']
  ).toEqual({ alpha: expression, beta: 'tier("base", u("credits") * 1.5)' })
  await act(async () => {
    finishSave?.()
  })
  await waitFor(() => expect(saveButton).toBeEnabled())
})

it('keeps long provider names accessible while allowing the tabs to wrap', () => {
  const longName =
    'Provider with a long international service name and a regional deployment designation'
  render(
    <TaskPluginPricingEditor
      variants={[
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          usage_schema: {},
          configured: '',
          effective: '1',
          compatible: true,
        },
        {
          plugin_key: 'beta',
          plugin_name: longName,
          usage_schema: {},
          configured: '',
          effective: '1',
          compatible: true,
        },
      ]}
      expressions={{}}
      onChange={vi.fn()}
      modelExpression='1'
      currency={USD_PRICING_CURRENCY}
    >
      <span>Default editor</span>
    </TaskPluginPricingEditor>
  )
  expect(screen.getByRole('tablist', { name: 'Provider' })).toHaveClass(
    'flex-wrap'
  )
  expect(screen.getByRole('tab', { name: longName })).toHaveClass(
    'max-w-full',
    'min-w-0'
  )
  expect(screen.getByText(longName)).toHaveAttribute('title', longName)
})

it('lets an administrator remove an unavailable provider’s saved price', async () => {
  const expression = 'u("images")'
  const { ref } = renderEditor(
    {
      name: 'shared',
      billingMode: 'tiered_expr',
      billingExpr: expression,
      pluginBillingExpr: { retired: expression },
    },
    [
      {
        plugin_key: 'retired',
        plugin_name: 'Retired',
        stale: true,
        usage_schema: {},
        configured: expression,
        effective: '',
        compatible: false,
      },
    ]
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'Retired' }))
  expect(
    screen.getByText('This provider is unavailable for this model.')
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Remove saved price' }))
  expect(await ref.current?.commitDraft()).toMatchObject({
    pluginBillingExpr: {},
    billingExpr: expression,
  })
  expect(screen.getByRole('tab', { name: 'Default' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
})

it('shows pending server validation for an expression the browser cannot check', async () => {
  render(
    <TaskPluginPricingEditor
      variants={[
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          usage_schema: {},
          configured: '',
          effective: '1',
          compatible: true,
        },
      ]}
      expressions={{}}
      onChange={vi.fn()}
      modelExpression='futureFunction()'
      currency={USD_PRICING_CURRENCY}
    >
      <span>Default editor</span>
    </TaskPluginPricingEditor>
  )
  await userEvent.setup().click(screen.getByRole('tab', { name: 'Alpha' }))
  expect(
    screen.getByText('Expression compatibility will be checked when saving.')
  ).toBeVisible()
})

function renderEditor(
  editData: ModelRatioData,
  variants: ModelPricingPluginVariant[],
  dirty = vi.fn()
) {
  usePricingPreferencesStore.setState({ currency: 'USD' })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [], vendors: [] },
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { effective: {} } },
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const ref = createRef<ModelPricingEditorPanelHandle>()
  render(
    <QueryClientProvider client={client}>
      <ModelPricingEditorPanel
        ref={ref}
        embedded
        editData={editData}
        pluginVariants={variants}
        onDirtyChange={dirty}
      />
    </QueryClientProvider>
  )
  return { ref, dirty }
}

const imageProvider = {
  plugin_key: 'chatgpt2api',
  plugin_name: 'ChatGPT2API',
  usage_schema: {
    image_count: { type: 'number' as const, unit: 'count' as const },
  },
  configured: 'tier("image", u("image_count") * 0)',
  effective: 'tier("image", u("image_count") * 0)',
  compatible: true,
}

function renderSimpleImageEditor() {
  return renderEditor(
    {
      name: 'gpt-image-2.6-flare',
      billingMode: 'tiered_expr',
      billingExpr: 'tier("image", fixed(0.007)) * image_count',
      pluginBillingExpr: { chatgpt2api: imageProvider.configured },
    },
    [imageProvider]
  )
}

it('shows different ordinary and background image prices without changing an untouched draft', async () => {
  const { ref, dirty } = renderSimpleImageEditor()
  expect(screen.getByText('Simple image pricing')).toBeVisible()
  expect(screen.getByText('Ordinary OpenAI: $0.007/image')).toBeVisible()
  expect(screen.getByText('ChatGPT2API: $0/image')).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Unit price' })).toHaveValue('')
  expect(
    screen.queryByRole('tab', { name: 'Expression' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('complementary', { name: 'Preview' })
  ).not.toBeInTheDocument()
  expect(dirty).toHaveBeenLastCalledWith(false)
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("image", fixed(0.007)) * image_count',
    pluginBillingExpr: { chatgpt2api: imageProvider.configured },
  })
})

it('updates ordinary and background expressions together only after an image price is entered', async () => {
  const { ref } = renderSimpleImageEditor()
  const user = userEvent.setup()
  await user.type(screen.getByRole('textbox', { name: 'Unit price' }), '0.012')
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingMode: 'tiered_expr',
    billingExpr: 'tier("image", fixed(0.012)) * image_count',
    pluginBillingExpr: {
      chatgpt2api: 'tier("image", u("image_count") * 0.012)',
    },
  })
  await user.click(screen.getByRole('combobox', { name: 'Charge by' }))
  await user.click(screen.getByRole('option', { name: 'Per request' }))
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("request", fixed(0.012))',
    pluginBillingExpr: { chatgpt2api: 'tier("request", 0.012)' },
  })
})

it('keeps an explicit zero provider price distinct from an unchanged legacy image price', async () => {
  const { ref } = renderEditor(
    {
      name: 'gpt-image-2',
      billingMode: 'per-request',
      price: '0.007',
      pluginBillingExpr: { chatgpt2api: imageProvider.configured },
    },
    [imageProvider]
  )
  expect(screen.getByText('Ordinary OpenAI: $0.007/image')).toBeVisible()
  expect(screen.getByText('ChatGPT2API: $0/image')).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Unit price' })).toHaveValue('')
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingMode: 'per-request',
    price: '0.007',
    pluginBillingExpr: { chatgpt2api: imageProvider.configured },
  })
})

it('keeps native legacy image aliases in their existing per-request editor', async () => {
  const { ref } = renderEditor(
    {
      name: 'gemini-nano-banana-2.1',
      billingMode: 'per-request',
      price: '0.007',
    },
    []
  )
  expect(
    screen.queryByRole('region', { name: 'Simple image pricing' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue(
    '0.007'
  )
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingMode: 'per-request',
    price: '0.007',
  })
})

it('blocks saving an emptied simple price instead of replacing it with zero', async () => {
  const { ref } = renderSimpleImageEditor()
  const user = userEvent.setup()
  const price = screen.getByRole('textbox', { name: 'Unit price' })
  await user.type(price, '0.012')
  await user.clear(price)
  expect(price).toHaveAttribute('aria-invalid', 'true')
  expect(await ref.current?.commitDraft()).toBeNull()
  await user.type(price, '0')
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("image", fixed(0)) * image_count',
    pluginBillingExpr: { chatgpt2api: 'tier("image", u("image_count") * 0)' },
  })
})

it('refreshes the simple summary after an advanced provider price is edited', async () => {
  const { ref } = renderSimpleImageEditor()
  const user = userEvent.setup()
  await user.type(screen.getByRole('textbox', { name: 'Unit price' }), '0.012')
  await user.click(screen.getByRole('button', { name: 'Advanced settings' }))
  expect(screen.getByRole('complementary', { name: 'Preview' })).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'ChatGPT2API' }))
  const advancedPrice = screen.getByRole('textbox', { name: 'image_count' })
  await user.clear(advancedPrice)
  await user.type(advancedPrice, '0.03')
  expect(screen.getByRole('textbox', { name: 'Unit price' })).toHaveValue('')
  expect(screen.getByText('ChatGPT2API: $0.03/image')).toBeVisible()
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("image", fixed(0.012)) * image_count',
    pluginBillingExpr: { chatgpt2api: 'tier("base", u("image_count") * 0.03)' },
  })
})

it('keeps separate quality pricing behind the explicit simple-pricing replacement confirmation', () => {
  renderEditor({ name: 'gpt-image-2', billingMode: 'image_quality' }, [])
  expect(
    screen.getByRole('button', { name: 'Use simple pricing' })
  ).toBeVisible()
  expect(
    screen.queryByRole('textbox', { name: 'Unit price' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Advanced settings' })
  ).toHaveAttribute('aria-expanded', 'true')
})

it('shows an inherited provider price from the current advanced draft instead of its old server snapshot', async () => {
  const expression = 'tier("image", u("image_count") * 0.007)'
  const { ref } = renderEditor(
    {
      name: 'gpt-image-2',
      billingMode: 'tiered_expr',
      billingExpr: expression,
    },
    [{ ...imageProvider, configured: '', effective: expression }]
  )
  expect(screen.getByText('ChatGPT2API: $0.007/image')).toBeVisible()
  const user = userEvent.setup()
  const price = screen.getByRole('textbox', { name: 'image_count' })
  await user.clear(price)
  await user.type(price, '0.02')
  expect(screen.getByText('ChatGPT2API: $0.02/image')).toBeVisible()
  expect(
    screen.queryByText('ChatGPT2API: $0.007/image')
  ).not.toBeInTheDocument()
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("base", u("image_count") * 0.02)',
    pluginBillingExpr: {},
  })
})

it.each([
  {
    name: 'gpt-image-2',
    billingMode: 'per-token' as const,
    ratio: '2',
    billingExpr: 'tier("request", fixed(0.01))',
  },
  { name: 'dall-e-3', billingMode: 'per-request' as const, price: '0.04' },
])(
  'preserves $name legacy pricing without treating retained expressions or size multipliers as a simple price',
  async (draft) => {
    const { ref } = renderEditor(draft, [])
    expect(
      screen.getByRole('button', { name: 'Use simple pricing' })
    ).toBeVisible()
    expect(
      screen.queryByRole('textbox', { name: 'Unit price' })
    ).not.toBeInTheDocument()
    expect(await ref.current?.commitDraft()).toMatchObject({
      name: draft.name,
      billingMode: draft.billingMode,
      price: draft.price ?? '',
      ratio: draft.ratio ?? '',
    })
  }
)

it('keeps complex image rules in advanced settings until simple pricing is explicitly confirmed', async () => {
  const expression =
    'param("quality") == "high" ? tier("high", fixed(0.02)) : tier("normal", fixed(0.01))'
  const { ref } = renderEditor(
    {
      name: 'gpt-image-2',
      billingMode: 'tiered_expr',
      billingExpr: expression,
    },
    []
  )
  expect(screen.getByRole('tab', { name: 'Expression' })).toBeVisible()
  expect(
    screen.queryByRole('textbox', { name: 'Unit price' })
  ).not.toBeInTheDocument()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Use simple pricing' }))
  const dialog = screen.getByRole('alertdialog')
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: expression,
  })
  await user.click(screen.getByRole('button', { name: 'Use simple pricing' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Use simple pricing',
    })
  )
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: expression,
  })
  await user.type(screen.getByRole('textbox', { name: 'Unit price' }), '0.03')
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("image", fixed(0.03)) * image_count',
  })
})

it('shows and removes a single provider override while retaining the per-call default', async () => {
  const expression = 'tier("base", u("seconds") * 0.4)'
  const { ref } = renderEditor(
    {
      name: 'shared',
      billingMode: 'per-request',
      price: '0.25',
      pluginBillingExpr: { alpha: expression },
    },
    [
      {
        plugin_key: 'alpha',
        plugin_name: 'Alpha',
        usage_schema: { seconds: { type: 'number', unit: 'second' } },
        configured: expression,
        effective: expression,
        compatible: true,
      },
    ]
  )
  expect(
    screen.getByRole('tab', { name: 'Per-request (deprecated)' })
  ).toHaveAttribute('aria-selected', 'true')
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'Alpha' }))
  await user.click(
    screen.getByRole('switch', { name: 'Set separately for this provider' })
  )
  expect(screen.getByText('Use model-level pricing')).toBeVisible()
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingMode: 'per-request',
    price: '0.25',
    pluginBillingExpr: {},
  })
})

it('clears dirty state after restoring an override in a different key order', async () => {
  const expression = 'tier("base", u("seconds") * 0.4)'
  const { dirty } = renderEditor(
    {
      name: 'shared',
      billingMode: 'tiered_expr',
      billingExpr: expression,
      pluginBillingExpr: { alpha: expression, beta: expression },
    },
    ['alpha', 'beta'].map((key) => ({
      plugin_key: key,
      plugin_name: key,
      usage_schema: { seconds: { type: 'number', unit: 'second' } },
      configured: expression,
      effective: expression,
      compatible: true,
    }))
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'alpha' }))
  const toggle = screen.getByRole('switch', {
    name: 'Set separately for this provider',
  })
  await user.click(toggle)
  expect(dirty).toHaveBeenLastCalledWith(true)
  await user.click(toggle)
  expect(dirty).toHaveBeenLastCalledWith(false)
})

it('opens task expression pricing when only provider metadata supplies a schema', async () => {
  renderEditor(
    { name: 'shared', billingMode: 'per-token' },
    ['alpha', 'beta'].map((key) => ({
      plugin_key: key,
      plugin_name: key,
      usage_schema: { seconds: { type: 'number', unit: 'second' } },
      configured: '',
      effective: '',
      compatible: false,
    }))
  )
  await waitFor(() =>
    expect(screen.getByRole('tab', { name: 'Expression' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  )
  expect(screen.getByRole('textbox', { name: 'seconds' })).toBeVisible()
})

it('configures ordinary OpenAI pricing while preserving inherited and saved plugin prices', async () => {
  const expression = 'tier("base", u("seconds") * 0.4)'
  const savedOverride = 'tier("base", u("seconds") * 0.7)'
  const { ref, dirty } = renderEditor(
    {
      name: 'shared',
      billingMode: 'tiered_expr',
      billingExpr: expression,
      pluginBillingExpr: { beta: savedOverride },
    },
    ['alpha', 'beta'].map((key) => ({
      plugin_key: key,
      plugin_name: key,
      usage_schema: { seconds: { type: 'number', unit: 'second' } },
      configured: key === 'beta' ? savedOverride : '',
      effective: key === 'beta' ? savedOverride : expression,
      compatible: true,
    }))
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'Ordinary OpenAI' }))
  expect(dirty).toHaveBeenLastCalledWith(false)
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: expression,
    pluginBillingExpr: { beta: savedOverride },
  })
  await user.click(
    screen.getByRole('button', { name: 'Configure ordinary OpenAI pricing' })
  )
  const price = screen.getByRole('textbox', { name: 'Input price' })
  await user.clear(price)
  await user.type(price, '2.5')
  expect(dirty).toHaveBeenLastCalledWith(true)
  await user.click(screen.getByRole('tab', { name: 'alpha' }))
  expect(screen.getByRole('textbox', { name: 'seconds' })).toHaveValue('0.4')
  await user.click(screen.getByRole('tab', { name: 'beta' }))
  expect(screen.getByRole('textbox', { name: 'seconds' })).toHaveValue('0.7')
  const draft = await ref.current?.commitDraft()
  expect(draft).toMatchObject({
    billingMode: 'tiered_expr',
    billingExpr: 'tier("base", p * 2.5 + c * 0)',
    pluginBillingExpr: { alpha: expression, beta: savedOverride },
  })
})

it('starts ordinary image pricing per returned image and keeps the plugin image price', async () => {
  const expression = 'tier("base", u("image_count") * 0.04)'
  const { ref } = renderEditor(
    {
      name: 'image-model',
      billingMode: 'tiered_expr',
      billingExpr: expression,
    },
    [
      {
        plugin_key: 'alibaba',
        plugin_name: 'Alibaba',
        usage_schema: { image_count: { type: 'number', unit: 'count' } },
        configured: '',
        effective: expression,
        compatible: true,
      },
    ]
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'Ordinary OpenAI' }))
  await user.click(
    screen.getByRole('button', { name: 'Configure ordinary OpenAI pricing' })
  )
  const price = screen.getByRole('textbox', { name: 'Price per image' })
  await user.clear(price)
  await user.type(price, '0.02')
  expect(
    within(screen.getByRole('tabpanel', { name: 'Ordinary OpenAI' })).getByRole(
      'tablist'
    )
  ).toHaveClass('flex-wrap')
  const ordinary = screen.getByRole('tab', { name: 'Ordinary OpenAI' })
  await user.click(ordinary)
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Alibaba' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  await user.keyboard('{ArrowLeft}{Enter}')
  expect(ordinary).toHaveAttribute('aria-selected', 'true')
  expect(price).toHaveValue('0.02')
  expect(await ref.current?.commitDraft()).toMatchObject({
    billingExpr: 'tier("image", fixed(0.02)) * image_count',
    pluginBillingExpr: { alibaba: expression },
  })
})

it.each([
  ['token', 'tier("base", p * 2 + c * 4)', 'Input price', '2'],
  [
    'per-image',
    'tier("image", fixed(0.04)) * image_count',
    'Price per image',
    '0.04',
  ],
  ['explicit zero', 'tier("free", fixed(0))', 'Price per request', '0'],
])(
  'loads ordinary %s pricing independently of plugin usage metadata',
  async (_kind, expression, field, value) => {
    const override = 'tier("base", u("seconds") * 0.4)'
    const { ref } = renderEditor(
      {
        name: 'shared',
        billingMode: 'tiered_expr',
        billingExpr: expression,
        pluginBillingExpr: { alpha: override },
      },
      [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          usage_schema: { seconds: { type: 'number', unit: 'second' } },
          configured: override,
          effective: override,
          compatible: true,
        },
      ]
    )
    expect(
      screen.getByRole('tab', { name: 'Ordinary OpenAI' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('textbox', { name: field })).toHaveValue(value)
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Alpha' }))
    const price = screen.getByRole('textbox', { name: 'seconds' })
    expect(
      screen.getByRole('switch', { name: 'Set separately for this provider' })
    ).toHaveAttribute('aria-disabled', 'true')
    await user.clear(price)
    await user.type(price, '0.6')
    expect(await ref.current?.commitDraft()).toMatchObject({
      billingExpr: expression,
      pluginBillingExpr: { alpha: 'tier("base", u("seconds") * 0.6)' },
    })
  }
)
