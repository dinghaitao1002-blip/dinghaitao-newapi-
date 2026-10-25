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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { assignChannelCategory, getChannelCategories } from '../api'
import { channelsQueryKeys } from '../lib'

const UNCATEGORIZED = '__uncategorized__'

export function ChannelCategorySelector({
  channelId,
  value,
}: {
  channelId: number
  value: string
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [saving, setSaving] = useState(false)
  const saveLock = useRef(false)
  const { data } = useQuery({
    queryKey: ['channel-categories'],
    queryFn: getChannelCategories,
  })

  const handleChange = async (nextValue: string | null) => {
    if (!nextValue || saveLock.current) return
    const category = nextValue === UNCATEGORIZED ? '' : nextValue
    if (category === value) return
    saveLock.current = true
    setSaving(true)
    try {
      const response = await assignChannelCategory([channelId], category)
      requireServerSuccess(response)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() }),
        queryClient.invalidateQueries({ queryKey: ['channel-categories'] }),
      ])
      toast.success(t('Channel category updated'))
    } catch (error) {
      handleServerError(error, t('Failed to assign category'))
    } finally {
      saveLock.current = false
      setSaving(false)
    }
  }

  return (
    <Select<string>
      value={value || UNCATEGORIZED}
      onValueChange={handleChange}
      disabled={saving}
    >
      <SelectTrigger
        aria-label={t('Channel Category')}
        className='h-7 min-w-[7rem] rounded-full px-2.5 text-xs'
      >
        {saving ? (
          <Loader2 className='size-3.5 animate-spin' />
        ) : (
          <span className='truncate'>{value || t('Uncategorized')}</span>
        )}
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false}>
        <SelectGroup>
          <SelectItem value={UNCATEGORIZED}>{t('Uncategorized')}</SelectItem>
          {(data?.data || []).map((category) => (
            <SelectItem key={category.name} value={category.name}>
              {category.name}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}
