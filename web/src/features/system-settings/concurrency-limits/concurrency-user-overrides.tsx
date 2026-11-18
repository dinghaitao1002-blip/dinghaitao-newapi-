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
import { useFieldArray, useFormContext } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import type { ConcurrencyFocusedUser } from './api'
import type { ModelConcurrencyRuleValues } from './lib/schema'
import { ConcurrencyUserSearch } from './user-search'

export function ConcurrencyUserOverrides(props: {
  focusedUser?: ConcurrencyFocusedUser
}) {
  const { t } = useTranslation()
  const form = useFormContext<ModelConcurrencyRuleValues>()
  const userFields = useFieldArray({
    control: form.control,
    name: 'user_limits',
  })
  const users = form.watch('user_limits')
  return (
    <div className='space-y-3 border-t pt-4'>
      <h4 className='text-sm font-semibold'>
        {t('User concurrency overrides')}
      </h4>
      {!props.focusedUser && (
        <>
          <ConcurrencyUserSearch
            excludedUserIds={users.map((user) => user.user_id)}
            disabled={users.length >= 1000}
            onSelect={(userId) =>
              userFields.append({ user_id: userId, max_concurrent: 0 })
            }
          />
          <Button
            type='button'
            variant='outline'
            disabled={users.length >= 1000}
            onClick={() =>
              userFields.append({ user_id: Number.NaN, max_concurrent: 0 })
            }
          >
            {t('Add by user ID')}
          </Button>
        </>
      )}
      {userFields.fields.map((user, index) => {
        if (props.focusedUser && user.user_id !== props.focusedUser.id) {
          return null
        }
        return (
          <div
            key={user.id}
            className={
              props.focusedUser
                ? 'rounded-lg border p-3'
                : 'grid grid-cols-2 items-start gap-2 rounded-lg border p-3 sm:grid-cols-[1fr_1fr_auto]'
            }
          >
            {!props.focusedUser && (
              <FormField
                control={form.control}
                name={`user_limits.${index}.user_id`}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('User ID')}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        aria-label={t('User ID for override {{number}}', {
                          number: index + 1,
                        })}
                        type='number'
                        min={1}
                        step={1}
                        value={Number.isNaN(field.value) ? '' : field.value}
                        onChange={(event) =>
                          field.onChange(
                            event.target.value === ''
                              ? Number.NaN
                              : Number(event.target.value)
                          )
                        }
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}
            <FormField
              control={form.control}
              name={`user_limits.${index}.max_concurrent`}
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Maximum concurrency')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      aria-label={
                        props.focusedUser
                          ? t('Maximum concurrency')
                          : t('Concurrency for override {{number}}', {
                              number: index + 1,
                            })
                      }
                      type='number'
                      min={0}
                      max={1000000}
                      step={1}
                      value={Number.isNaN(field.value) ? '' : field.value}
                      onChange={(event) =>
                        field.onChange(
                          event.target.value === ''
                            ? Number.NaN
                            : Number(event.target.value)
                        )
                      }
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            {!props.focusedUser && (
              <Button
                type='button'
                variant='ghost'
                className='col-span-2 sm:col-span-1 sm:mt-6'
                aria-label={t('Remove override {{number}}', {
                  number: index + 1,
                })}
                onClick={() => userFields.remove(index)}
              >
                {t('Remove')}
              </Button>
            )}
          </div>
        )
      })}
    </div>
  )
}
