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
import type { ModelConcurrencyRule } from '../api'

export function modelConcurrencyRuleKey(rule: {
  group?: string
  model: string
}) {
  return JSON.stringify([rule.group ?? '', rule.model])
}

export function sameConcurrencyRule(
  left: ModelConcurrencyRule | undefined,
  right: ModelConcurrencyRule | undefined
) {
  const value = (rule: ModelConcurrencyRule | undefined) =>
    rule && [
      rule.group ?? '',
      rule.model,
      rule.enabled,
      rule.max_concurrent,
      rule.default_user_limit,
      rule.other_users_limit,
      [...rule.user_limits].sort((a, b) => a.user_id - b.user_id),
    ]
  return JSON.stringify(value(left)) === JSON.stringify(value(right))
}
