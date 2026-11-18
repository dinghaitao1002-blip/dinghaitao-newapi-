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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { searchUsers } from '@/features/users/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export type ConcurrencySelectedUser = {
  id: number
  username: string
  display_name?: string
}

// The shared select-style Combobox filters its loaded options locally. Search
// the existing user endpoint first so this form also works with large user pools.
export function ConcurrencyUserSearch(props: {
  onSelect: (user: ConcurrencySelectedUser) => void
  excludedUserIds: number[]
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [keyword, setKeyword] = useState('')
  const users = useQuery({
    queryKey: ['model-concurrency', 'user-search', keyword],
    queryFn: async () => {
      const response = requireServerSuccess(
        await searchUsers({ keyword, page_size: 20 })
      )
      return response.data?.items ?? []
    },
    enabled: !!keyword,
  })
  const currentQuery = !!keyword && search.trim() === keyword
  const canSelectResults =
    currentQuery && users.isSuccess && !users.isFetching && !props.disabled
  return (
    <div className='space-y-2'>
      <Label htmlFor='concurrency-user-search'>{t('Find a user')}</Label>
      <div className='flex gap-2'>
        <Input
          id='concurrency-user-search'
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={t('Username, email or user ID')}
          disabled={props.disabled}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              setKeyword(search.trim())
              if (keyword === search.trim() && keyword) void users.refetch()
            }
          }}
        />
        <Button
          type='button'
          variant='outline'
          disabled={props.disabled || !search.trim() || users.isFetching}
          onClick={() => {
            setKeyword(search.trim())
            if (keyword === search.trim()) void users.refetch()
          }}
        >
          {users.isFetching ? t('Searching...') : t('Search')}
        </Button>
      </div>
      {currentQuery && users.isError && (
        <p className='text-destructive text-sm' role='alert'>
          {t('Failed to search users. Try again.')}
        </p>
      )}
      {canSelectResults && (
        <Combobox
          aria-label={t('Select user to add an override')}
          placeholder={t('Select user to add an override')}
          value={null}
          disabled={props.disabled}
          options={(users.data ?? []).map((user) => ({
            value: String(user.id),
            label: `${user.username} (#${user.id})`,
            description: user.display_name || undefined,
            disabled: props.excludedUserIds.includes(user.id),
          }))}
          emptyText={t('No users found')}
          onValueChange={(value: string | null) => {
            if (!value || !canSelectResults) return
            const selected = users.data?.find(
              (user) => String(user.id) === value
            )
            if (
              !selected ||
              !Number.isSafeInteger(selected.id) ||
              selected.id <= 0 ||
              props.excludedUserIds.includes(selected.id) ||
              typeof selected.username !== 'string' ||
              !selected.username.trim()
            ) {
              return
            }
            const identity: ConcurrencySelectedUser = {
              id: selected.id,
              username: selected.username,
            }
            if (typeof selected.display_name === 'string') {
              identity.display_name = selected.display_name
            }
            props.onSelect(identity)
          }}
        />
      )}
    </div>
  )
}
