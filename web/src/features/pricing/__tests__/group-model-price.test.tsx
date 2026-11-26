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
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { ModelPriceCell } from '../components/model-price-cell'
import { getDynamicPricingSummary } from '../lib/dynamic-price'
import {
  resolveDisplayGroupPricing,
  withModelGroupPricing,
} from '../lib/group-model-price'
import { getDisplayGroupRatio } from '../lib/model-helpers'
import type { PricingModel } from '../types'

const model: PricingModel = {
  id: 1,
  model_name: 'image-price-fixture',
  quota_type: 0,
  billing_mode: 'tiered_expr',
  billing_expr: 'tier("base", fixed(0.08)) * image_count',
  model_ratio: 1,
  completion_ratio: 1,
  model_price: 0.08,
  enable_groups: ['default', 'vip', 'hidden'],
  group_ratio: { default: 1, vip: 0.5 },
  group_model_pricing: {
    vip: {
      unit: 'image',
      expression: 'tier("group_model", fixed(0.2)) * image_count',
      enabled: true,
    },
  },
}

describe('group model price display', () => {
  it('replaces the selected group price without changing the cached base model', () => {
    const scoped = withModelGroupPricing(model, 'vip')
    expect(scoped.billing_expr).toContain('fixed(0.2)')
    expect(scoped.billing_mode).toBe('tiered_expr')
    expect(model.billing_expr).toBe('tier("base", fixed(0.08)) * image_count')
    expect(withModelGroupPricing(model, 'default')).toBe(model)
    const summary = getDynamicPricingSummary(scoped, {
      tokenUnit: 'M',
      groupRatioMultiplier: 0.5,
    })
    expect(summary?.primaryEntries[0].unit).toBe('image')
    expect(summary?.primaryEntries[0].formatted).toContain('0.1')
  })

  it('compares the scoped unit price times its group ratio for the all-groups summary', () => {
    const display = resolveDisplayGroupPricing(model)
    expect(display.selectedGroup).toBe('default')
    expect(display.model.model_price).toBe(0.08)
    expect(resolveDisplayGroupPricing(model, 'vip').selectedGroup).toBe('vip')
  })

  it('preserves a free group price and excludes hidden groups', () => {
    const free = {
      ...model,
      group_model_pricing: {
        vip: {
          unit: 'image' as const,
          expression: 'tier("group_model", fixed(0)) * image_count',
          enabled: true,
        },
      },
    }
    const display = resolveDisplayGroupPricing(free)
    expect(display.selectedGroup).toBe('vip')
    expect(
      getDynamicPricingSummary(display.model, { tokenUnit: 'M' })
        ?.primaryEntries[0].value
    ).toBe(0)
  })

  it('falls back to public pricing when the rule is disabled', () => {
    expect(
      withModelGroupPricing(
        {
          ...model,
          group_model_pricing: {
            vip: {
              unit: 'image',
              expression: 'tier("group_model", fixed(0.2)) * image_count',
              enabled: false,
            },
          },
        },
        'vip'
      ).model_price
    ).toBe(0.08)
  })

  it('keeps per-second task display units and replaces provider-specific prices', () => {
    const video = withModelGroupPricing(
      {
        ...model,
        group_model_pricing: {
          vip: {
            unit: 'second',
            expression: 'tier("group_model", u("seconds") * 0.3)',
            enabled: true,
          },
        },
      },
      'vip'
    )
    expect(video.billing_usage_schema?.seconds.unit).toBe('second')
    expect(video.billing_plugin_variants).toBeUndefined()
    const summary = getDynamicPricingSummary(video, {
      tokenUnit: 'M',
      usageSchema: video.billing_usage_schema,
    })
    expect(summary?.primaryEntries[0].value).toBe(0.3)
  })

  it('renders the selected group image price in the normal price cell', () => {
    render(<ModelPriceCell model={model} options={{ selectedGroup: 'vip' }} />)
    expect(screen.getByText(/0\.1/)).toBeInTheDocument()
  })

  it('applies the selected ratio when the model serves all groups', () => {
    const allGroups = {
      ...model,
      enable_groups: ['all'],
      group_ratio: { default: 1, vip: 2 },
    }
    const display = resolveDisplayGroupPricing(allGroups, 'vip')
    expect(getDisplayGroupRatio(display.model, display.selectedGroup)).toBe(2)
    expect(
      getDynamicPricingSummary(display.model, {
        tokenUnit: 'M',
        groupRatioMultiplier: getDisplayGroupRatio(
          display.model,
          display.selectedGroup
        ),
      })?.primaryEntries[0].formatted
    ).toContain('0.4')
    expect(getDisplayGroupRatio(allGroups)).toBe(1)
  })

  it('does not compare unlike token and image units as a numerical minimum', () => {
    const mixed = {
      ...model,
      group_model_pricing: {
        vip: {
          unit: 'token' as const,
          expression: 'tier("group_model", p * 2 + c * 8)',
          enabled: true,
        },
      },
    }
    expect(resolveDisplayGroupPricing(mixed).mixedGroupUnits).toBe(true)
    const selected = resolveDisplayGroupPricing(mixed, 'vip')
    expect(selected.mixedGroupUnits).toBeUndefined()
    expect(selected.model.billing_expr).toContain('p * 2')
  })

  it('includes an authorized group override when its ratio uses the default one', () => {
    const display = resolveDisplayGroupPricing({
      ...model,
      enable_groups: ['vip'],
      group_ratio: {},
    })
    expect(display.selectedGroup).toBe('vip')
    expect(display.model.billing_expr).toContain('fixed(0.2)')
    expect(getDisplayGroupRatio(display.model, display.selectedGroup)).toBe(1)
  })
})
