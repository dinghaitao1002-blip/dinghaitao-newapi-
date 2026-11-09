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
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import type { ExpressionNode } from '@/features/pricing/lib/billing-expression/types'
import type { ModelRatioData } from '@/features/system-settings/models/model-pricing-core'

import type { ModelPricingPluginVariant } from './api'

/** Match the same strict, unconditional image shapes as billingexpr.ImageUnitPrice. */
function imageUnitPrice(expression: string, task: boolean): number | null {
  const compiled = compileBillingExpression(expression)
  if (compiled.status !== 'ready') return null
  let node = compiled.ast
  if (task && isTier(node)) {
    node = node.args[1]
  }
  if (node.kind !== 'binary' || node.operator !== '*') return null
  for (const [quantity, unitPrice] of [
    [node.left, node.right],
    [node.right, node.left],
  ]) {
    if (task) {
      if (
        quantity.kind !== 'call' ||
        quantity.name !== 'u' ||
        quantity.args[0].kind !== 'literal' ||
        quantity.args[0].value !== 'image_count'
      ) {
        continue
      }
      if (
        unitPrice.kind === 'literal' &&
        typeof unitPrice.value === 'number' &&
        Number.isFinite(unitPrice.value) &&
        unitPrice.value >= 0
      ) {
        return unitPrice.value
      }
      continue
    }
    if (
      quantity.kind !== 'variable' ||
      quantity.name !== 'image_count' ||
      !isTier(unitPrice)
    ) {
      continue
    }
    const fixed = unitPrice.args[1]
    if (
      fixed.kind !== 'call' ||
      fixed.name !== 'fixed' ||
      fixed.args[0].kind !== 'literal'
    ) {
      continue
    }
    const price = fixed.args[0].value
    if (
      typeof price === 'number' &&
      Number.isFinite(price * 1_000_000) &&
      price >= 0
    ) {
      return price
    }
  }
  return null
}

function isTier(
  node: ExpressionNode
): node is Extract<ExpressionNode, { kind: 'call' }> {
  return (
    node.kind === 'call' &&
    node.name === 'tier' &&
    node.args[0].kind === 'literal' &&
    typeof node.args[0].value === 'string'
  )
}

/** Other providers, independent prices and request rules retain the advanced editor. */
export function sharedImageUnitPrice(
  draft: ModelRatioData | null | undefined,
  variants: ModelPricingPluginVariant[] | undefined
): number | null {
  if (
    !draft ||
    draft.billingMode !== 'tiered_expr' ||
    draft.requestRuleExpr?.trim() ||
    variants?.length !== 1
  ) {
    return null
  }
  const provider = variants[0]
  if (
    provider.plugin_key !== 'chatgpt2api' ||
    provider.stale ||
    !provider.compatible ||
    provider.usage_schema.image_count?.type !== 'number' ||
    provider.usage_schema.image_count.unit !== 'count' ||
    Object.keys(provider.usage_schema).length !== 1
  ) {
    return null
  }
  if (
    Object.keys(draft.pluginBillingExpr ?? {}).some(
      (key) => key !== provider.plugin_key
    )
  ) {
    return null
  }
  const ordinaryPrice = imageUnitPrice(draft.billingExpr ?? '', false)
  const taskPrice = imageUnitPrice(
    draft.pluginBillingExpr?.[provider.plugin_key] ?? provider.configured,
    true
  )
  return ordinaryPrice !== null && ordinaryPrice === taskPrice
    ? ordinaryPrice
    : null
}

export function isValidSharedImagePrice(value: string): boolean {
  if (value.trim() === '') return false
  const price = Number(value)
  return Number.isFinite(price * 1_000_000) && price >= 0
}

/** Two transport grammars, one user-entered price in a single versioned draft. */
export function sharedImagePricingDraft(
  draft: ModelRatioData,
  price: number
): ModelRatioData {
  if (!isValidSharedImagePrice(String(price))) {
    throw new Error('Invalid image price')
  }
  return {
    ...draft,
    billingMode: 'tiered_expr',
    billingExpr: `tier("image", fixed(${price})) * image_count`,
    requestRuleExpr: '',
    pluginBillingExpr: {
      ...draft.pluginBillingExpr,
      chatgpt2api: `tier("image", u("image_count") * ${price})`,
    },
  }
}
