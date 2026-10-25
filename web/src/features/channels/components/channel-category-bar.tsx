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
import { Loader2, Plus } from 'lucide-react'
import { useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { createChannelCategory, getChannelCategories } from '../api'

const UNCATEGORIZED = '__uncategorized__'

type ChannelCategoryBarProps = {
  selectedCategory: string
  categoryCounts?: Record<string, number>
  onSelect: (category: string) => void
}

export function ChannelCategoryBar({
  selectedCategory,
  categoryCounts,
  onSelect,
}: ChannelCategoryBarProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const submitLock = useRef(false)

  const { data } = useQuery({
    queryKey: ['channel-categories'],
    queryFn: getChannelCategories,
  })

  const categories = data?.data || []
  const allCount = useMemo(
    () =>
      Object.values(categoryCounts || {}).reduce(
        (sum, count) => sum + (Number(count) || 0),
        0
      ),
    [categoryCounts]
  )
  const uncategorizedCount = Number(categoryCounts?.[UNCATEGORIZED] || 0)

  const handleCreate = async () => {
    const trimmed = name.trim()
    if (!trimmed || submitLock.current) return
    submitLock.current = true
    setSubmitting(true)
    try {
      const response = await createChannelCategory(trimmed)
      requireServerSuccess(response)
      await queryClient.invalidateQueries({ queryKey: ['channel-categories'] })
      toast.success(t('Channel category created'))
      setCreateOpen(false)
      setName('')
      onSelect(trimmed)
    } catch (error) {
      handleServerError(error, t('Failed to create category'))
    } finally {
      submitLock.current = false
      setSubmitting(false)
    }
  }

  const renderCategoryButton = (
    value: string,
    label: string,
    count: number
  ) => (
    <Button
      key={value}
      type='button'
      size='sm'
      variant={selectedCategory === value ? 'default' : 'outline'}
      className='h-8 shrink-0 gap-1.5 rounded-full px-3'
      aria-pressed={selectedCategory === value}
      onClick={() => onSelect(value)}
    >
      <span>{label}</span>
      <span className='text-[11px] opacity-70'>{count}</span>
    </Button>
  )

  return (
    <>
      <div className='flex max-w-[min(46vw,42rem)] [scrollbar-width:none] items-center gap-1.5 overflow-x-auto py-0.5 [&::-webkit-scrollbar]:hidden'>
        {renderCategoryButton('all', t('All'), allCount)}
        {categories.map((category) =>
          renderCategoryButton(
            category.name,
            category.name,
            Number(category.channel_count || 0)
          )
        )}
        {uncategorizedCount > 0 &&
          renderCategoryButton(
            UNCATEGORIZED,
            t('Uncategorized'),
            uncategorizedCount
          )}
        <Button
          type='button'
          variant='outline'
          size='icon-sm'
          className='h-8 w-8 shrink-0 rounded-full border-dashed'
          aria-label={t('Create Channel Category')}
          onClick={() => setCreateOpen(true)}
        >
          <Plus className='size-4' />
        </Button>
      </div>

      <Dialog
        open={createOpen}
        onOpenChange={(open) => {
          if (!submitting) setCreateOpen(open)
        }}
        title={t('Create Channel Category')}
        description={t(
          'Create a category first, then assign channels to it directly from the channel list.'
        )}
        contentClassName='sm:max-w-md'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => setCreateOpen(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='button'
              onClick={handleCreate}
              disabled={!name.trim() || submitting}
            >
              {submitting && <Loader2 className='mr-2 size-4 animate-spin' />}
              {t('Create')}
            </Button>
          </>
        }
      >
        <div className='space-y-2'>
          <Label htmlFor='channel-category-name'>{t('Category Name')}</Label>
          <Input
            id='channel-category-name'
            value={name}
            maxLength={64}
            autoFocus
            placeholder={t('For example: 1K or Video')}
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                void handleCreate()
              }
            }}
          />
        </div>
      </Dialog>
    </>
  )
}
