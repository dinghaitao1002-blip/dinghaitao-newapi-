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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type {
  ConcurrencyFocusedUser,
  ModelConcurrencyRule,
  ModelConcurrencyUsage,
} from './api'

export function ConcurrencyRuleCard(props: {
  rule: ModelConcurrencyRule
  focusedUser?: ConcurrencyFocusedUser
  usage?: ModelConcurrencyUsage
  usageAvailable: boolean
  disabled: boolean
  onEdit: () => void
  onDelete: () => void
  onToggle: (enabled: boolean) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const limit = (value: number) =>
    value === 0 ? t('Unlimited') : formatNumber(value, locale)
  const focusedOverride = props.rule.user_limits.find(
    (user) => user.user_id === props.focusedUser?.id
  )
  const visibleUsers = props.focusedUser
    ? [
        {
          user_id: props.focusedUser.id,
          max_concurrent:
            focusedOverride?.max_concurrent ?? props.rule.default_user_limit,
        },
      ]
    : props.rule.user_limits
  return (
    <article
      className='space-y-4 rounded-xl border p-4'
      aria-label={props.rule.model}
    >
      <div className='flex flex-wrap items-center gap-3'>
        <h4 className='min-w-0 flex-1 font-mono text-sm font-semibold break-all'>
          {props.rule.model}
        </h4>
        <Switch
          aria-label={t('Enable concurrency for {{model}}', {
            model: props.rule.model,
          })}
          checked={props.rule.enabled}
          disabled={props.disabled || !!props.focusedUser}
          onCheckedChange={props.onToggle}
        />
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled}
          onClick={props.onEdit}
          aria-label={t('Edit concurrency for {{model}}', {
            model: props.rule.model,
          })}
        >
          {t('Edit')}
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={props.disabled || (!!props.focusedUser && !focusedOverride)}
          onClick={props.onDelete}
          aria-label={
            props.focusedUser
              ? t('Restore default concurrency for {{model}}', {
                  model: props.rule.model,
                })
              : t('Delete concurrency for {{model}}', {
                  model: props.rule.model,
                })
          }
        >
          {props.focusedUser ? t('Restore default') : t('Delete')}
        </Button>
      </div>
      {!props.rule.enabled && (
        <p role='status' className='text-muted-foreground text-sm'>
          {t(
            'This model rule is disabled. Saved overrides do not take effect.'
          )}
        </p>
      )}
      {props.focusedUser && !focusedOverride && (
        <p className='text-muted-foreground text-sm'>
          {t('Using default per-user concurrency: {{limit}}', {
            limit: limit(props.rule.default_user_limit),
          })}
        </p>
      )}
      <dl className='grid gap-3 text-sm sm:grid-cols-3'>
        <div>
          <dt className='text-muted-foreground'>
            {t('Model total concurrency')}
          </dt>
          <dd>{limit(props.rule.max_concurrent)}</dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Default concurrency per user')}
          </dt>
          <dd>{limit(props.rule.default_user_limit)}</dd>
        </div>
        <div>
          <dt className='text-muted-foreground'>
            {t('Other users combined concurrency')}
          </dt>
          <dd>{limit(props.rule.other_users_limit)}</dd>
        </div>
      </dl>
      <div className='bg-muted/40 space-y-2 rounded-lg p-3 text-sm'>
        <p>
          {t('Active requests: {{total}}; other users: {{others}}', {
            total: props.usageAvailable
              ? formatNumber(props.usage?.total ?? 0, locale)
              : t('Unavailable'),
            others: props.usageAvailable
              ? formatNumber(props.usage?.other_users ?? 0, locale)
              : t('Unavailable'),
          })}
        </p>
        {visibleUsers.map((user) => (
          <p key={user.user_id}>
            {t('User #{{id}}: limit {{limit}}; active {{current}}', {
              id: String(user.user_id),
              limit: limit(user.max_concurrent),
              current: props.usageAvailable
                ? formatNumber(
                    props.usage?.users.find(
                      (entry) => entry.user_id === user.user_id
                    )?.current ?? 0,
                    locale
                  )
                : t('Unavailable'),
            })}
          </p>
        ))}
      </div>
    </article>
  )
}
