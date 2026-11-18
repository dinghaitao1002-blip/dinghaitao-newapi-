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
import type { ModelConcurrencyConfig, ModelConcurrencyRule } from '../api'

export class UserConcurrencyConflict extends Error {
  model: string
  constructor(model: string) {
    super(`Concurrency conflict for ${model}`)
    this.model = model
  }
}

// Merge only the focused user's edited overrides. A concurrent edit to that
// same override, or deletion of its model, must be resolved before writing.
export function mergeUserConcurrencyChanges(
  baseline: ModelConcurrencyConfig,
  draft: ModelConcurrencyConfig,
  latest: ModelConcurrencyConfig,
  userId: number
): ModelConcurrencyConfig {
  const changedRules = new Map<string, ModelConcurrencyRule>()
  for (const originalRule of baseline.rules) {
    const original = originalRule.user_limits.find(
      (user) => user.user_id === userId
    )
    const edited = draft.rules
      .find((rule) => rule.model === originalRule.model)
      ?.user_limits.find((user) => user.user_id === userId)
    if (original?.max_concurrent === edited?.max_concurrent) continue
    const currentRule = latest.rules.find(
      (rule) => rule.model === originalRule.model
    )
    const current = currentRule?.user_limits.find(
      (user) => user.user_id === userId
    )
    if (!currentRule || current?.max_concurrent !== original?.max_concurrent) {
      throw new UserConcurrencyConflict(originalRule.model)
    }
    let userLimits = currentRule.user_limits.filter(
      (user) => user.user_id !== userId
    )
    if (edited) {
      userLimits = current
        ? currentRule.user_limits.map((user) =>
            user.user_id === userId ? edited : user
          )
        : [...currentRule.user_limits, edited]
    }
    changedRules.set(currentRule.model, {
      ...currentRule,
      user_limits: userLimits,
    })
  }
  return {
    ...latest,
    rules: latest.rules.map((rule) => changedRules.get(rule.model) ?? rule),
  }
}
