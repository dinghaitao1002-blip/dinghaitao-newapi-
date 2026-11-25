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
import { useEffect, useMemo, useRef, useState } from 'react'
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
  type ConcurrencyFocusedUser,
  type ModelConcurrencyConfig,
  type ModelConcurrencyData,
  type ModelConcurrencyRule,
} from './api'
import { ConcurrencyRuleCard } from './concurrency-rule-card'
import { ConcurrencyRuleDialog } from './concurrency-rule-dialog'
import {
  mergeConcurrencyChanges,
  mergeUserConcurrencyChanges,
  UserConcurrencyConflict,
} from './lib/merge-user-overrides'
import { modelConcurrencyRuleKey } from './lib/rule-identity'

export function ModelConcurrencySection(props: {
  focusedUser?: ConcurrencyFocusedUser
  group?: string
  initialModel?: string
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const query = useQuery({
    queryKey: modelConcurrencyQueryKey,
    queryFn: () => getModelConcurrency(),
    refetchInterval: 5000,
  })
  const [draft, setDraft] = useState<ModelConcurrencyConfig | null>(null)
  const [baseline, setBaseline] = useState<ModelConcurrencyConfig | null>(null)
  const [dialog, setDialog] = useState<{
    index: number
    rule: ModelConcurrencyRule | null
    initialModel?: string
  } | null>(null)
  const globalRules = useMemo(
    () => draft?.rules.filter((rule) => !rule.group) ?? [],
    [draft?.rules]
  )
  const openedInitialModel = useRef(false)
  const [confirmEnable, setConfirmEnable] = useState(false)
  const [deleteIndex, setDeleteIndex] = useState<number | null>(null)
  useEffect(() => {
    if (!query.data || dialog || deleteIndex !== null) return
    const hasUnsavedChanges =
      draft !== null && JSON.stringify(draft) !== JSON.stringify(baseline)
    if (!hasUnsavedChanges) {
      setDraft(query.data.config)
      setBaseline(query.data.config)
    }
  }, [query.data, draft, baseline, dialog, deleteIndex])
  useEffect(() => {
    if (!draft || !props.initialModel || openedInitialModel.current) return
    openedInitialModel.current = true
    const index = draft.rules.findIndex(
      (rule) =>
        (rule.group ?? '') === (props.group ?? '') &&
        rule.model === props.initialModel
    )
    setDialog({
      index,
      rule: draft.rules[index] ?? null,
      initialModel: props.initialModel,
    })
  }, [draft, props.initialModel, props.group])
  const save = useMutation({
    mutationFn: async (config: ModelConcurrencyConfig) => {
      if (!baseline) return updateModelConcurrency(config)
      const latest = await getModelConcurrency({ fresh: true })
      client.setQueryData(modelConcurrencyQueryKey, latest)
      try {
        return await updateModelConcurrency(
          props.focusedUser
            ? mergeUserConcurrencyChanges(
                baseline,
                config,
                latest.config,
                props.focusedUser.id
              )
            : mergeConcurrencyChanges(baseline, config, latest.config)
        )
      } catch (error) {
        if (error instanceof UserConcurrencyConflict) {
          throw new Error(
            t(
              'Concurrency for {{model}} changed elsewhere. Reset to the latest settings and try again.',
              { model: error.model }
            )
          )
        }
        throw error
      }
    },
    onMutate: () =>
      client.cancelQueries({ queryKey: modelConcurrencyQueryKey, exact: true }),
    onSuccess: (data) => {
      client.setQueryData<ModelConcurrencyData>(
        modelConcurrencyQueryKey,
        (previous) =>
          previous ? { ...previous, config: data.config } : undefined
      )
      void client.invalidateQueries({
        queryKey: modelConcurrencyQueryKey,
        exact: true,
      })
      setDraft(data.config)
      setBaseline(data.config)
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
  const dirty = JSON.stringify(draft) !== JSON.stringify(baseline)
  const visibleRules = draft.rules
    .map((rule, index) => ({ rule, index }))
    .filter(({ rule }) => (rule.group ?? '') === (props.group ?? ''))
  return (
    <SettingsSection title={t('Concurrency Limits')}>
      <SettingsPageFormActions
        onSave={() => save.mutate(draft)}
        onReset={() => {
          setDraft(query.data.config)
          setBaseline(query.data.config)
          save.reset()
        }}
        isSaving={save.isPending}
        isSaveDisabled={!dirty}
        isResetDisabled={!dirty}
      />
      {props.focusedUser && (
        <p className='rounded-lg border p-3 text-sm break-words'>
          {t(
            'Concurrency settings for {{username}} (#{{id}}). Shared limits and other users are preserved.',
            {
              username: props.focusedUser.username,
              id: String(props.focusedUser.id),
            }
          )}
        </p>
      )}
      <p className='text-muted-foreground text-sm'>
        {props.group !== undefined
          ? t(
              'Limits apply only to requests using group {{group}}. Models without an enabled group rule use the global concurrency defaults.',
              { group: props.group }
            )
          : t(
              'Global rules are defaults for groups without an enabled model concurrency rule.'
            )}
      </p>
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
        disabled={
          save.isPending || !!props.focusedUser || props.group !== undefined
        }
        onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
        className='rounded-xl border p-4'
      />
      {!draft.enabled && (
        <div className='space-y-2'>
          <p role='status' className='text-muted-foreground text-sm'>
            {t(
              'Model concurrency limits are disabled. Saved overrides do not take effect.'
            )}
          </p>
          {props.group !== undefined && !props.focusedUser && (
            <Button
              type='button'
              variant='outline'
              onClick={() => setConfirmEnable(true)}
              disabled={save.isPending}
            >
              {t('Enable saved concurrency limits')}
            </Button>
          )}
        </div>
      )}
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
        {!props.focusedUser && (
          <Button
            type='button'
            disabled={save.isPending || draft.rules.length >= 1000}
            onClick={() => setDialog({ index: -1, rule: null })}
          >
            {t('Add model rule')}
          </Button>
        )}
      </div>
      {props.focusedUser && visibleRules.length > 0 && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'Manage group model limits in Group pricing under system settings.'
          )}
        </p>
      )}
      {visibleRules.length === 0 && (
        <EmptyState
          title={t('No model concurrency rules')}
          description={
            props.focusedUser
              ? t(
                  'Manage group model limits in Group pricing under system settings.'
                )
              : t('Add a model rule to configure shared and per-user limits.')
          }
          bordered
        />
      )}
      {visibleRules.map(({ rule, index }) => (
        <ConcurrencyRuleCard
          key={modelConcurrencyRuleKey(rule)}
          rule={rule}
          focusedUser={props.focusedUser}
          usageAvailable={query.data.usage_available && !query.isError}
          usage={query.data.usage.find(
            (usage) =>
              modelConcurrencyRuleKey(usage) === modelConcurrencyRuleKey(rule)
          )}
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
        models={visibleRules.map(({ rule }) => rule.model)}
        group={props.group}
        initialModel={dialog?.initialModel}
        globalRules={globalRules}
        focusedUser={props.focusedUser}
        onOpenChange={(open) => {
          if (!open) setDialog(null)
        }}
        onSave={(rule) => {
          if (!dialog) return
          const rules = [...draft.rules]
          const scopedRule = { ...rule, group: props.group }
          if (dialog.index < 0) rules.push(scopedRule)
          else rules[dialog.index] = scopedRule
          setDraft({ ...draft, rules })
        }}
      />
      <ConfirmDialog
        open={confirmEnable}
        onOpenChange={setConfirmEnable}
        title={t('Enable all saved concurrency rules?')}
        desc={t(
          'This enables saved global and group concurrency rules after Save Changes. Existing limits are preserved.'
        )}
        confirmText={t('Enable')}
        handleConfirm={() => {
          setDraft({ ...draft, enabled: true })
          setConfirmEnable(false)
        }}
      />
      <ConfirmDialog
        open={deleteIndex !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteIndex(null)
        }}
        title={
          props.focusedUser
            ? t('Restore default concurrency for this user?')
            : t('Delete model concurrency rule?')
        }
        desc={
          props.focusedUser
            ? t(
                'Only this user override will be removed. The model rule and other users are preserved. Save Changes to apply.'
              )
            : t(
                'This removes the rule from the draft. Save Changes to apply the removal.'
              )
        }
        destructive={!props.focusedUser}
        confirmText={props.focusedUser ? t('Restore default') : t('Delete')}
        handleConfirm={() => {
          if (props.focusedUser) {
            const userId = props.focusedUser.id
            setDraft({
              ...draft,
              rules: draft.rules.map((rule, index) =>
                index === deleteIndex
                  ? {
                      ...rule,
                      user_limits: rule.user_limits.filter(
                        (user) => user.user_id !== userId
                      ),
                    }
                  : rule
              ),
            })
          } else {
            setDraft({
              ...draft,
              rules: draft.rules.filter((_, index) => index !== deleteIndex),
            })
          }
          setDeleteIndex(null)
        }}
      />
    </SettingsSection>
  )
}
