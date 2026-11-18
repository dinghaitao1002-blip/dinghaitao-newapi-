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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import {
  getModelConcurrency,
  modelConcurrencyQueryKey,
  updateModelConcurrency,
  type ModelConcurrencyConfig,
  type ModelConcurrencyRule,
} from './api'
import { ConcurrencyRuleCard } from './concurrency-rule-card'
import { ConcurrencyRuleDialog } from './concurrency-rule-dialog'

export function ModelConcurrencySection() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const query = useQuery({
    queryKey: modelConcurrencyQueryKey,
    queryFn: getModelConcurrency,
    refetchInterval: 5000,
  })
  const [draft, setDraft] = useState<ModelConcurrencyConfig | null>(null)
  const [dialog, setDialog] = useState<{
    index: number
    rule: ModelConcurrencyRule | null
  } | null>(null)
  const [deleteIndex, setDeleteIndex] = useState<number | null>(null)
  useEffect(() => {
    if (query.data && draft === null) setDraft(query.data.config)
  }, [query.data, draft])
  const save = useMutation({
    mutationFn: updateModelConcurrency,
    onMutate: () =>
      client.cancelQueries({ queryKey: modelConcurrencyQueryKey, exact: true }),
    onSuccess: (data) => {
      client.setQueryData(modelConcurrencyQueryKey, {
        ...query.data,
        config: data.config,
      })
      void client.invalidateQueries({
        queryKey: modelConcurrencyQueryKey,
        exact: true,
      })
      setDraft(data.config)
      toast.success(t('Concurrency limits saved'))
    },
    onError: (error) => handleServerError(error),
  })
  if (!draft || !query.data) {
    if (query.isError) {
      return (
        <ErrorState
          title={t('Failed to load concurrency limits')}
          onRetry={() => void query.refetch()}
        />
      )
    }
    return <LoadingState />
  }
  const dirty = JSON.stringify(draft) !== JSON.stringify(query.data.config)
  return (
    <SettingsSection title={t('Concurrency Limits')}>
      <SettingsPageFormActions
        onSave={() => save.mutate(draft)}
        onReset={() => setDraft(query.data.config)}
        isSaving={save.isPending}
        isSaveDisabled={!dirty}
        isResetDisabled={!dirty}
      />
      <p className='text-muted-foreground text-sm'>
        {t(
          'Limit active requests by model and user. Requests above a limit return 429 without waiting.'
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Image queues, video tasks and task plugins keep concurrency slots until completion. Other endpoints, including Midjourney, count only while the HTTP request is running.'
        )}
      </p>
      <SettingsSwitchField
        controlId='enable-model-concurrency'
        label={t('Enable model concurrency limits')}
        checked={draft.enabled}
        disabled={save.isPending}
        onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
        className='rounded-xl border p-4'
      />
      <p className='text-muted-foreground text-sm'>
        {query.data.storage === 'redis'
          ? t('Redis counters are shared across server instances.')
          : t(
              'Memory counters apply only to this server. Use Redis for shared limits across instances.'
            )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t('Live usage reflects saved settings and refreshes every 5 seconds.')}
      </p>
      {!query.data.usage_available && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Concurrency storage is unavailable')}{' '}
          {t(
            'Active request counts are unavailable. Configuration can still be changed.'
          )}
        </p>
      )}
      {query.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Usage refresh failed. Displayed counts may be out of date.')}{' '}
          <Button
            type='button'
            size='sm'
            variant='link'
            onClick={() => void query.refetch()}
          >
            {t('Retry')}
          </Button>
        </p>
      )}
      {save.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {getServerErrorMessage(save.error)}
        </p>
      )}
      <div className='flex items-center justify-between gap-3'>
        <h4 className='font-semibold'>{t('Model concurrency rules')}</h4>
        <Button
          type='button'
          disabled={save.isPending || draft.rules.length >= 1000}
          onClick={() => setDialog({ index: -1, rule: null })}
        >
          {t('Add model rule')}
        </Button>
      </div>
      {draft.rules.length === 0 && (
        <EmptyState
          title={t('No model concurrency rules')}
          description={t(
            'Add a model rule to configure shared and per-user limits.'
          )}
          bordered
        />
      )}
      {draft.rules.map((rule, index) => (
        <ConcurrencyRuleCard
          key={rule.model}
          rule={rule}
          usageAvailable={query.data.usage_available && !query.isError}
          usage={query.data.usage.find((usage) => usage.model === rule.model)}
          disabled={save.isPending}
          onEdit={() => setDialog({ index, rule })}
          onDelete={() => setDeleteIndex(index)}
          onToggle={(enabled) =>
            setDraft({
              ...draft,
              rules: draft.rules.map((item, itemIndex) =>
                itemIndex === index ? { ...item, enabled } : item
              ),
            })
          }
        />
      ))}
      <ConcurrencyRuleDialog
        open={dialog !== null}
        rule={dialog?.rule ?? null}
        models={draft.rules.map((rule) => rule.model)}
        onOpenChange={(open) => {
          if (!open) setDialog(null)
        }}
        onSave={(rule) => {
          if (!dialog) return
          const rules = [...draft.rules]
          if (dialog.index < 0) rules.push(rule)
          else rules[dialog.index] = rule
          setDraft({ ...draft, rules })
        }}
      />
      <ConfirmDialog
        open={deleteIndex !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteIndex(null)
        }}
        title={t('Delete model concurrency rule?')}
        desc={t(
          'This removes the rule from the draft. Save Changes to apply the removal.'
        )}
        destructive
        confirmText={t('Delete')}
        handleConfirm={() => {
          setDraft({
            ...draft,
            rules: draft.rules.filter((_, index) => index !== deleteIndex),
          })
          setDeleteIndex(null)
        }}
      />
    </SettingsSection>
  )
}
