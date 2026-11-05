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
import type { ModelPricingPluginVariant } from '@/features/model-pricing/api'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import type { ExpressionNode } from '@/features/pricing/lib/billing-expression/types'
import { parseVisualBillingDocument } from '@/features/pricing/lib/billing-expression/visual'

import type { PricingMode } from './model-pricing-core'

export type SimpleImagePrice = { unit: 'request' | 'image'; amount: number }
export type SimpleImagePriceRow = {
  key: string
  name: string
  price: SimpleImagePrice | null
}
export type SimpleImagePricingState = {
  source: string
  rows: SimpleImagePriceRow[]
  complex: boolean
  common: SimpleImagePrice | null
  pluginKeys: string[]
}

function amount(node: ExpressionNode): number | null {
  if (
    node.kind === 'literal' &&
    typeof node.value === 'number' &&
    Number.isFinite(node.value) &&
    node.value >= 0
  ) {
    return node.value
  }
  return null
}

function readTaskPrice(expression: string): SimpleImagePrice | null {
  const compiled = compileBillingExpression(expression)
  if (compiled.status !== 'ready') return null
  let node = compiled.ast
  if (node.kind === 'call' && node.name === 'tier') node = node.args[1]
  let constant = 0
  let imagePrice: number | null = null
  const terms = [node]
  while (terms.length > 0) {
    const term = terms.pop()
    if (!term) {
      return null
    }
    if (term.kind === 'binary' && term.operator === '+') {
      terms.push(term.left, term.right)
      continue
    }
    const value = amount(term)
    if (value !== null) {
      constant += value
      continue
    }
    if (
      term.kind !== 'binary' ||
      term.operator !== '*' ||
      imagePrice !== null
    ) {
      return null
    }
    const usage = term.left
    const coefficient = amount(term.right)
    if (
      usage.kind !== 'call' ||
      usage.name !== 'u' ||
      usage.args[0]?.kind !== 'literal' ||
      usage.args[0].value !== 'image_count' ||
      coefficient === null
    ) {
      return null
    }
    imagePrice = coefficient
  }
  if (
    !Number.isFinite(constant) ||
    (constant > 0 && imagePrice !== null && imagePrice > 0)
  ) {
    return null
  }
  if (imagePrice !== null && constant === 0) {
    return { unit: 'image', amount: imagePrice }
  }
  return { unit: 'request', amount: constant }
}

function readOrdinaryPrice(expression: string): SimpleImagePrice | null {
  const document = parseVisualBillingDocument(expression)
  if (
    !document ||
    document.root.kind !== 'tier' ||
    document.root.billingUnit !== 'request'
  ) {
    return null
  }
  return {
    unit: document.imageCount ? 'image' : 'request',
    amount: Number(document.root.fixedPrice),
  }
}

export function getSimpleImagePricingState(input: {
  name: string
  mode: PricingMode
  expression: string
  legacyPrice: string
  plugins: Record<string, string>
  variants: ModelPricingPluginVariant[]
  configured: boolean
}): SimpleImagePricingState | null {
  // Other legacy image-like aliases can use native per-request settlement.
  // Keep their existing editor instead of inferring an image-count multiplier.
  if (
    input.mode === 'per-request' &&
    !/(?:gpt[-_]image|dall[-_.]?e)/i.test(input.name)
  ) {
    return null
  }
  const activeVariants = input.variants.filter((variant) => !variant.stale)
  const variants = activeVariants.filter(
    (variant) =>
      !variant.stale && variant.usage_schema.image_count?.unit === 'count'
  )
  if (variants.length !== activeVariants.length) return null
  if (
    variants.length === 0 &&
    !/(?:gpt[-_]image|imagen|dall[-_.]?e|banana|flux|qwen[-_]?image|z-image)/i.test(
      input.name
    )
  ) {
    return null
  }
  const legacy =
    input.mode === 'per-request' &&
    input.legacyPrice !== '' &&
    Number.isFinite(Number(input.legacyPrice)) &&
    Number(input.legacyPrice) >= 0
      ? { unit: 'image' as const, amount: Number(input.legacyPrice) }
      : null
  const ordinary =
    input.mode === 'tiered_expr' ? readOrdinaryPrice(input.expression) : legacy
  const rows: SimpleImagePriceRow[] = [
    { key: '__ordinary__', name: 'Ordinary OpenAI', price: ordinary },
  ]
  for (const variant of variants) {
    const separate = Object.hasOwn(input.plugins, variant.plugin_key)
    let expression = input.expression
    if (separate) expression = input.plugins[variant.plugin_key]
    rows.push({
      key: variant.plugin_key,
      name: variant.plugin_name,
      price: separate
        ? readTaskPrice(expression)
        : (legacy ?? readTaskPrice(expression)),
    })
  }
  const first = rows[0].price
  const common =
    first &&
    rows.every(
      (row) =>
        row.price?.unit === first.unit && row.price.amount === first.amount
    )
      ? first
      : null
  return {
    source: JSON.stringify(input),
    rows,
    common,
    complex:
      input.mode === 'image_quality' ||
      input.mode === 'video_per_second' ||
      (input.mode === 'per-request' && /dall[-_.]?e/i.test(input.name)) ||
      (input.configured && rows.some((row) => !row.price)),
    pluginKeys: variants.map((variant) => variant.plugin_key),
  }
}

export function simpleImageExpressions(price: SimpleImagePrice) {
  const value = String(price.amount)
  if (price.unit === 'image') {
    return {
      ordinary: `tier("image", fixed(${value})) * image_count`,
      task: `tier("image", u("image_count") * ${value})`,
    }
  }
  return {
    ordinary: `tier("request", fixed(${value}))`,
    task: `tier("request", ${value})`,
  }
}
