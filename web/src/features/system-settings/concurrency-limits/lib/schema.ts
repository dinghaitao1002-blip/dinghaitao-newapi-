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

const concurrencyLimitSchema = z
  .number({ error: 'Enter a whole number from 0 to 1,000,000' })
  .int('Enter a whole number from 0 to 1,000,000')
  .min(0, 'Enter a whole number from 0 to 1,000,000')
  .max(1000000, 'Enter a whole number from 0 to 1,000,000')

export const modelConcurrencyRuleSchema = z.object({
  model: z
    .string()
    .trim()
    .min(1, 'Model name is required')
    .refine(
      (value) => new TextEncoder().encode(value).length <= 256,
      'Model name must be at most 256 bytes'
    ),
  enabled: z.boolean(),
  max_concurrent: concurrencyLimitSchema,
  default_user_limit: concurrencyLimitSchema,
  other_users_limit: concurrencyLimitSchema,
  user_limits: z
    .array(
      z.object({
        user_id: z
          .number({ error: 'Enter a positive user ID' })
          .int('Enter a positive user ID')
          .positive('Enter a positive user ID')
          .max(Number.MAX_SAFE_INTEGER, 'Enter a positive user ID'),
        max_concurrent: concurrencyLimitSchema,
      })
    )
    .max(1000, 'A model can have at most 1,000 user overrides')
    .superRefine((users, context) => {
      const seen = new Set<number>()
      users.forEach((user, index) => {
        if (seen.has(user.user_id)) {
          context.addIssue({
            code: 'custom',
            message: 'This user already has an override',
            path: [index, 'user_id'],
          })
        }
        seen.add(user.user_id)
      })
    }),
})

export type ModelConcurrencyRuleValues = z.infer<
  typeof modelConcurrencyRuleSchema
>
