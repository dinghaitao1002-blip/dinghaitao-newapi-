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
import { FILTER_ALL } from '../constants'
import type { PricingModel } from '../types'
import { getDynamicPricingSummary } from './dynamic-price'
import { getConfiguredGroupRatio } from './model-helpers'
import { parseVideoSecondPrices } from './price'

/** A group override replaces the public unit price, including provider variants. */
export function withModelGroupPricing(
  model: PricingModel,
  group: string
): PricingModel {
  const rule = model.group_model_pricing?.[group]
  if (!rule?.enabled) return model
  return {
    ...model,
    group_model_pricing: undefined,
    billing_plugin_variants: undefined,
    billing_mode: 'tiered_expr',
    billing_expr: rule.expression,
    billing_usage_schema:
      rule.unit === 'second'
        ? { seconds: { type: 'number', unit: 'second' } }
        : undefined,
    billing_usage_examples: undefined,
    image_quality_pricing_enabled: false,
    image_quality_prices: undefined,
    quota_type: rule.unit === 'request' ? 1 : 0,
  }
}

function summaryUnitPrice(model: PricingModel): number {
  const summary = getDynamicPricingSummary(model, { tokenUnit: 'M' })
  if (summary?.primaryEntries.length) {
    return (
      summary.primaryEntries.find((entry) => entry.field === 'inputPrice') ??
      summary.primaryEntries[0]
    ).value
  }
  if (
    model.image_quality_pricing_enabled ||
    model.billing_mode === 'image_quality'
  ) {
    const prices = model.image_quality_prices?.filter((item) => item.enabled)
    return prices?.length
      ? Math.min(...prices.map((item) => item.price))
      : Infinity
  }
  if (model.billing_mode === 'video_per_second') {
    const prices = Object.values(parseVideoSecondPrices(model)).filter(
      (price): price is number =>
        typeof price === 'number' && Number.isFinite(price)
    )
    return prices.length ? Math.min(...prices) : Infinity
  }
  return model.quota_type === 1
    ? (model.model_price ?? Infinity)
    : model.model_ratio * 2
}

function summaryUnit(model: PricingModel): string {
  if (
    model.image_quality_pricing_enabled ||
    model.billing_mode === 'image_quality'
  ) {
    return 'image'
  }
  if (model.billing_mode === 'video_per_second') {
    return 'second'
  }
  const entries = getDynamicPricingSummary(model, {
    tokenUnit: 'M',
  })?.primaryEntries
  if (entries?.length) {
    const units = new Set(
      entries.map((entry) =>
        entry.key === 'image_count' ? 'image' : entry.unit
      )
    )
    return units.size === 1 ? [...units][0] : 'mixed'
  }
  return model.quota_type === 1 ? 'request' : 'token'
}

/** Preserve the existing lowest-price summary, comparing group unit prices too. */
export function resolveDisplayGroupPricing(
  model: PricingModel,
  selectedGroup?: string
): { model: PricingModel; selectedGroup?: string; mixedGroupUnits?: boolean } {
  if (!Object.keys(model.group_model_pricing ?? {}).length) {
    return { model, selectedGroup }
  }
  if (selectedGroup && selectedGroup !== FILTER_ALL) {
    return { model: withModelGroupPricing(model, selectedGroup), selectedGroup }
  }
  let bestGroup: string | undefined
  let bestPrice = Infinity
  const groups = (model.enable_groups ?? []).includes('all')
    ? Object.keys(model.group_ratio ?? {})
    : (model.enable_groups ?? []).filter(
        (group) =>
          Object.hasOwn(model.group_ratio ?? {}, group) ||
          model.group_model_pricing?.[group]?.enabled
      )
  const units = new Set(
    groups.map((group) => summaryUnit(withModelGroupPricing(model, group)))
  )
  // A per-image amount and a per-million-token amount have no meaningful
  // numerical minimum. Let the user choose a group to see its actual price.
  if (units.size > 1 || units.has('mixed')) {
    return { model, selectedGroup, mixedGroupUnits: true }
  }
  for (const group of groups) {
    const candidate = withModelGroupPricing(model, group)
    const price =
      summaryUnitPrice(candidate) *
      getConfiguredGroupRatio(model.group_ratio ?? {}, group)
    if (price < bestPrice) {
      bestGroup = group
      bestPrice = price
    }
  }
  return {
    model: bestGroup ? withModelGroupPricing(model, bestGroup) : model,
    selectedGroup: bestGroup ?? selectedGroup,
  }
}
