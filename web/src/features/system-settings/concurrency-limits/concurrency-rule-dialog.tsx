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
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import { SettingsSwitchItem } from '../components/settings-form-layout'
import type { ConcurrencyFocusedUser, ModelConcurrencyRule } from './api'
import { ConcurrencyUserOverrides } from './concurrency-user-overrides'
import {
  modelConcurrencyRuleSchema,
  type ModelConcurrencyRuleValues,
} from './lib/schema'
import { getConcurrencyModelCatalog } from './model-catalog'

const FORM_ID = 'model-concurrency-rule-form'
const emptyRule: ModelConcurrencyRule = {
  model: '',
  enabled: true,
  max_concurrent: 0,
  default_user_limit: 0,
  other_users_limit: 0,
  user_limits: [],
}
const limitFields = [
  { name: 'max_concurrent', label: 'Model total concurrency' },
  { name: 'default_user_limit', label: 'Default concurrency per user' },
  { name: 'other_users_limit', label: 'Other users combined concurrency' },
] as const

export function ConcurrencyRuleDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  rule: ModelConcurrencyRule | null
  models: string[]
  focusedUser?: ConcurrencyFocusedUser
  onSave: (rule: ModelConcurrencyRule) => void
}) {
  const { t } = useTranslation()
  const catalog = useQuery({
    queryKey: ['model-concurrency-catalog'],
    queryFn: getConcurrencyModelCatalog,
    enabled: props.open && !props.focusedUser,
    staleTime: 60_000,
    retry: false,
    meta: { errorToast: false },
  })
  const modelOptions = useMemo(
    () =>
      [...new Set([...(catalog.data ?? []), ...props.models])]
        .sort()
        .map((model) => ({ value: model, label: model })),
    [catalog.data, props.models]
  )
  const form = useForm<ModelConcurrencyRuleValues>({
    resolver: zodResolver(modelConcurrencyRuleSchema),
    defaultValues: emptyRule,
  })
  useEffect(() => {
    if (!props.open) return
    const values = props.rule ?? emptyRule
    if (
      !props.focusedUser ||
      values.user_limits.some((user) => user.user_id === props.focusedUser?.id)
    ) {
      form.reset(values)
      return
    }
    form.reset({
      ...values,
      user_limits: [
        ...values.user_limits,
        {
          user_id: props.focusedUser.id,
          max_concurrent: values.default_user_limit,
        },
      ],
    })
  }, [props.open, props.rule, props.focusedUser, form])
  const submit = (values: ModelConcurrencyRuleValues) => {
    if (
      props.models.includes(values.model) &&
      values.model !== props.rule?.model
    ) {
      form.setError('model', {
        message: 'A rule for this model already exists',
      })
      return
    }
    if (props.focusedUser) {
      const userId = props.focusedUser.id
      const selectedOverride = values.user_limits.find(
        (user) => user.user_id === userId
      )
      if (!selectedOverride) return
      const original = props.rule ?? { ...emptyRule, model: values.model }
      const hasOverride = original.user_limits.some(
        (user) => user.user_id === userId
      )
      const userLimits = hasOverride
        ? original.user_limits.map((user) =>
            user.user_id === userId ? selectedOverride : user
          )
        : [...original.user_limits, selectedOverride]
      props.onSave({ ...original, user_limits: userLimits })
    } else {
      props.onSave(values)
    }
    props.onOpenChange(false)
  }
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        props.rule ? t('Edit model concurrency') : t('Add model concurrency')
      }
      description={t(
        'User overrides replace the default per-user limit. All limits apply together; 0 means unlimited at that level.'
      )}
      contentClassName='sm:max-w-2xl'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={FORM_ID}>
            {t('Apply to draft')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={FORM_ID}
          className='space-y-5'
          onSubmit={form.handleSubmit(submit)}
          noValidate
        >
          <FormField
            control={form.control}
            name='model'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Model name')}</FormLabel>
                <FormControl>
                  <Combobox
                    ref={field.ref}
                    name={field.name}
                    value={field.value}
                    onBlur={field.onBlur}
                    onValueChange={(value) => field.onChange(value ?? '')}
                    options={modelOptions}
                    allowCustomValue
                    openOnFocus={false}
                    placeholder={t('Choose or enter a model name')}
                    emptyText={t('No matching models')}
                    disabled={!!props.focusedUser && !!props.rule}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Parameter suffixes share the base model concurrency limit. A separate enabled rule takes priority.'
                  )}
                </FormDescription>
                {!props.focusedUser && (
                  <>
                    <p className='text-muted-foreground text-sm'>
                      {t(
                        'Choose a model from the list, or enter a custom model name.'
                      )}
                    </p>
                    <div className='flex flex-wrap items-center gap-2'>
                      {catalog.isFetching && (
                        <p
                          role='status'
                          className='text-muted-foreground text-sm'
                        >
                          {t('Loading models...')}
                        </p>
                      )}
                      {catalog.isError && (
                        <p role='alert' className='text-destructive text-sm'>
                          {t(
                            'Failed to load models. Retry or enter a model name.'
                          )}
                        </p>
                      )}
                      <Button
                        type='button'
                        size='sm'
                        variant='outline'
                        disabled={catalog.isFetching}
                        onClick={() => void catalog.refetch()}
                      >
                        {t('Refresh models')}
                      </Button>
                    </div>
                  </>
                )}
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <FormLabel>{t('Enable this model rule')}</FormLabel>
                <FormControl>
                  <Switch
                    checked={field.value}
                    disabled={!!props.focusedUser}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <div className='grid gap-4 sm:grid-cols-3'>
            {limitFields.map((item) => (
              <FormField
                key={item.name}
                control={form.control}
                name={item.name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t(item.label)}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        type='number'
                        disabled={!!props.focusedUser}
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
            ))}
          </div>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Other users are users without an override. Their individual default limit and combined limit both apply.'
            )}
          </p>
          <ConcurrencyUserOverrides focusedUser={props.focusedUser} />
          {(form.formState.errors.user_limits?.message ||
            form.formState.errors.user_limits?.root?.message) && (
            <p role='alert' className='text-destructive text-sm'>
              {t(
                form.formState.errors.user_limits?.message ??
                  form.formState.errors.user_limits?.root?.message ??
                  ''
              )}
            </p>
          )}
          <p className='text-muted-foreground text-sm'>
            {t(
              'Changes take effect only after Save Changes on the settings page.'
            )}
          </p>
        </form>
      </Form>
    </Dialog>
  )
}
