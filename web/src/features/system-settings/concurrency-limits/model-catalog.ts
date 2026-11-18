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
import { getModels } from '@/features/models/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export async function getConcurrencyModelCatalog(): Promise<string[]> {
  const names = new Set<string>()
  let received = 0
  for (let page = 1; ; page++) {
    const response = requireServerSuccess(
      await getModels({ include_channel_models: true, p: page, page_size: 100 })
    )
    const data = response.data
    if (!data || !Array.isArray(data.items)) {
      throw new Error('Model catalog returned no model list')
    }
    if (!Number.isSafeInteger(data.total) || data.total < 0) {
      throw new Error('Model catalog returned an invalid total')
    }
    for (const model of data.items) {
      if (model.name_rule === 0 && model.model_name.trim()) {
        names.add(model.model_name.trim())
      }
    }
    received += data.items.length
    if (received >= data.total) return [...names].sort()
    if (data.items.length === 0) {
      throw new Error('Model catalog ended before all models were loaded')
    }
  }
}
