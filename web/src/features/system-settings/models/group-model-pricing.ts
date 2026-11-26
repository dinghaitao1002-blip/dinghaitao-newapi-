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
import { z } from 'zod'

export const groupPriceUnits = ['request', 'image', 'second', 'token'] as const
export type GroupPriceUnit = (typeof groupPriceUnits)[number]
export type GroupModelPrice = {
  unit: GroupPriceUnit
  expression: string
  enabled: boolean
}
export type GroupModelPrices = Record<string, Record<string, GroupModelPrice>>

const numeric = '(\\d+(?:\\.\\d*)?(?:[eE][+-]?\\d+)?|\\.\\d+(?:[eE][+-]?\\d+)?)'
const priceAmount = new RegExp(`^${numeric}$`)
const patterns: Record<GroupPriceUnit, RegExp> = {
  request: new RegExp(`^tier\\("group_model",fixed\\(${numeric}\\)\\)$`),
  image: new RegExp(
    `^tier\\("group_model",fixed\\(${numeric}\\)\\)\\*image_count$`
  ),
  second: new RegExp(`^tier\\("group_model",u\\("seconds"\\)\\*${numeric}\\)$`),
  token: new RegExp(
    `^tier\\("group_model",p\\*${numeric}\\+c\\*${numeric}\\)$`
  ),
}

export function readGroupPrice(rule: GroupModelPrice): {
  price: string
  outputPrice: string
} | null {
  const source = rule.expression.replaceAll(
    /("(?:\\.|[^"\\])*")|\s+/g,
    (_match, literal: string | undefined) => literal ?? ''
  )
  const match = patterns[rule.unit]?.exec(source)
  if (!match) return null
  if (!Number.isFinite(Number(match[1]))) return null
  if (rule.unit === 'token' && !Number.isFinite(Number(match[2]))) return null
  return { price: match[1], outputPrice: match[2] ?? '' }
}

export function groupPriceExpression(
  unit: GroupPriceUnit,
  price: string,
  outputPrice = ''
): string {
  if (unit === 'token') {
    return `tier("group_model", p * ${price} + c * ${outputPrice})`
  }
  if (unit === 'second') return `tier("group_model", u("seconds") * ${price})`
  const fixed = `tier("group_model", fixed(${price}))`
  return unit === 'image' ? `${fixed} * image_count` : fixed
}

export const groupModelPriceSchema = z
  .object({
    unit: z.enum(groupPriceUnits),
    expression: z.string().min(1),
    enabled: z.boolean(),
  })
  .strict()
  .refine((rule) => readGroupPrice(rule) !== null)

export const groupModelPricesSchema = z.record(
  z.string().trim().min(1),
  z.record(z.string().trim().min(1), groupModelPriceSchema)
)

export function parseGroupModelPrices(
  value: string | undefined
): GroupModelPrices | null {
  try {
    const parsed = groupModelPricesSchema.safeParse(JSON.parse(value || '{}'))
    return parsed.success ? parsed.data : null
  } catch {
    return null
  }
}

export const groupPriceRowSchema = z
  .object({
    model: z.string().trim().min(1, 'Select a model'),
    unit: z.enum(groupPriceUnits),
    price: z
      .string()
      .regex(priceAmount, 'Enter a finite, non-negative price')
      .refine(
        (value) => Number.isFinite(Number(value)),
        'Enter a finite, non-negative price'
      ),
    outputPrice: z.string(),
    enabled: z.boolean(),
  })
  .superRefine((row, ctx) => {
    if (
      row.unit === 'token' &&
      (!priceAmount.test(row.outputPrice) ||
        !Number.isFinite(Number(row.outputPrice)))
    ) {
      ctx.addIssue({
        code: 'custom',
        path: ['outputPrice'],
        message: 'Enter a finite, non-negative price',
      })
    }
  })

export const groupModelPricingForceSchema = z.record(
  z
    .string()
    .min(1)
    .refine((key) => key === key.trim()),
  z.boolean()
)

export function parseGroupModelPricingForce(
  value: string | undefined
): Record<string, boolean> | null {
  try {
    const parsed = groupModelPricingForceSchema.safeParse(
      JSON.parse(value || '{}')
    )
    return parsed.success ? parsed.data : null
  } catch {
    return null
  }
}

export const groupPriceFormSchema = z
  .object({
    rows: z.array(groupPriceRowSchema),
    forceGroupPrice: z.boolean().optional(),
  })
  .superRefine((form, ctx) => {
    const seen = new Set<string>()
    form.rows.forEach((row, index) => {
      if (seen.has(row.model.trim())) {
        ctx.addIssue({
          code: 'custom',
          path: ['rows', index, 'model'],
          message: 'This model already has a price in this group',
        })
      }
      seen.add(row.model.trim())
    })
  })
export type GroupPriceFormValues = z.infer<typeof groupPriceFormSchema>
