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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export type ModelConcurrencyUserLimit = {
  user_id: number
  max_concurrent: number
}
export type ModelConcurrencyRule = {
  model: string
  enabled: boolean
  max_concurrent: number
  default_user_limit: number
  other_users_limit: number
  user_limits: ModelConcurrencyUserLimit[]
}
export type ModelConcurrencyConfig = {
  enabled: boolean
  rules: ModelConcurrencyRule[]
}
export type ModelConcurrencyUsage = {
  model: string
  total: number
  other_users: number
  users: { user_id: number; current: number }[]
}
export type ModelConcurrencyData = {
  config: ModelConcurrencyConfig
  usage: ModelConcurrencyUsage[]
  storage: 'redis' | 'memory'
  usage_available: boolean
  usage_error?: string
}
type Response<T> = {
  success: boolean
  message?: string
  data: T
}

export const modelConcurrencyQueryKey = ['model-concurrency'] as const

export async function getModelConcurrency(): Promise<ModelConcurrencyData> {
  const response = await api.get<Response<ModelConcurrencyData>>(
    '/api/option/model_concurrency'
  )
  return requireServerSuccess(response.data).data
}

export async function updateModelConcurrency(
  config: ModelConcurrencyConfig
): Promise<{ config: ModelConcurrencyConfig }> {
  const response = await api.patch<
    Response<{ config: ModelConcurrencyConfig }>
  >('/api/option/model_concurrency', {
    config,
  })
  return requireServerSuccess(response.data).data
}
