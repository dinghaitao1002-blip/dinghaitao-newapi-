import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'

import {
  assignChannelCategory,
  getChannelCategories,
} from '../api'
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
  const { data } = useQuery({
    queryKey: ['channel-categories'],
    queryFn: getChannelCategories,
  })

  const handleChange = async (nextValue: string | null) => {
    if (!nextValue || saving) return
    const category = nextValue === UNCATEGORIZED ? '' : nextValue
    if (category === value) return
    setSaving(true)
    try {
      const response = await assignChannelCategory([channelId], category)
      if (!response.success) {
        toast.error(response.message || t('Failed to assign category'))
        return
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() }),
        queryClient.invalidateQueries({ queryKey: ['channel-categories'] }),
      ])
      toast.success(t('Channel category updated'))
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to assign category')
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <Select<string>
      value={value || UNCATEGORIZED}
      onValueChange={handleChange}
      disabled={saving}
    >
      <SelectTrigger className='h-7 min-w-[7rem] rounded-full px-2.5 text-xs'>
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
