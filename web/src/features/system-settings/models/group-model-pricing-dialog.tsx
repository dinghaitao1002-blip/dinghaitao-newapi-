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
import { Plus, Trash2 } from 'lucide-react'
import { useMemo } from 'react'
import { useFieldArray, useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import { getModels } from '@/features/models/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  groupPriceExpression,
  groupPriceFormSchema,
  groupPriceUnits,
  readGroupPrice,
  type GroupModelPrice,
  type GroupPriceFormValues,
} from './group-model-pricing'

const unitLabelKeys = {
  request: 'Per request',
  image: 'Per image',
  second: 'Per second',
  token: 'Per 1M tokens',
} as const

export function GroupModelPricingDialog(props: {
  group: string
  prices: Record<string, GroupModelPrice>
  forceGroupPrice?: boolean
  onClose: () => void
  onConfigureConcurrency?: (model: string) => void
  onApply: (
    prices: Record<string, GroupModelPrice>,
    forceGroupPrice: boolean
  ) => void
}) {
  const { t } = useTranslation()
  const form = useForm<GroupPriceFormValues>({
    resolver: zodResolver(groupPriceFormSchema),
    defaultValues: {
      forceGroupPrice: props.forceGroupPrice ?? false,
      rows: Object.entries(props.prices).map(([model, rule]) => ({
        model,
        unit: rule.unit,
        enabled: rule.enabled,
        price: readGroupPrice(rule)?.price ?? '',
        outputPrice: readGroupPrice(rule)?.outputPrice ?? '',
      })),
    },
  })
  const rows = useFieldArray({ control: form.control, name: 'rows' })
  const values = useWatch({ control: form.control, name: 'rows' })
  const catalog = useQuery({
    queryKey: ['group-model-pricing-catalog'],
    queryFn: async () => {
      const names = new Set<string>()
      let page = 1
      let count = 0
      for (;;) {
        const response = requireServerSuccess(
          await getModels({
            include_channel_models: true,
            p: page,
            page_size: 100,
          })
        )
        const items = response.data?.items ?? []
        for (const model of items) names.add(model.model_name)
        count += items.length
        if (!items.length || count >= (response.data?.total ?? 0)) break
        page += 1
      }
      return [...names].sort()
    },
    refetchOnWindowFocus: false,
  })
  const options = useMemo(
    () =>
      [...new Set([...(catalog.data ?? []), ...Object.keys(props.prices)])].map(
        (name) => ({ value: name, label: name })
      ),
    [catalog.data, props.prices]
  )

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Model prices for {{group}}', { group: props.group })}
      description={t(
        'Set a separate base price for each model in this group. Prices are in USD and the effective group ratio applies once.'
      )}
      contentClassName='sm:max-w-4xl'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          <Button
            type='button'
            onClick={form.handleSubmit((data) => {
              props.onApply(
                Object.fromEntries(
                  data.rows.map((row) => [
                    row.model,
                    {
                      unit: row.unit,
                      expression: groupPriceExpression(
                        row.unit,
                        row.price,
                        row.outputPrice
                      ),
                      enabled: row.enabled,
                    },
                  ])
                ),
                data.forceGroupPrice ?? false
              )
            })}
          >
            {t('Apply to draft')}
          </Button>
        </>
      }
    >
      {catalog.isPending && (
        <LoadingState message={t('Loading models...')} size='sm' inline />
      )}
      {catalog.isError && (
        <ErrorState
          description={catalog.error.message}
          onRetry={() => void catalog.refetch()}
        />
      )}
      <Form {...form}>
        <div className='space-y-3'>
          <FormField
            control={form.control}
            name='forceGroupPrice'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-4 rounded-lg border p-4'>
                <div className='space-y-1'>
                  <FormLabel>{t('Force group model prices')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Enabled model prices in this group override user private prices. Models without an enabled group price keep their existing pricing.'
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    checked={field.value ?? false}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          {rows.fields.length === 0 && (
            <EmptyState
              title={t('No model prices in this group')}
              description={t(
                'Models without an enabled group price keep their existing pricing.'
              )}
            />
          )}
          {rows.fields.map((row, index) => (
            <section
              key={row.id}
              aria-label={t('Model price {{index}}', { index: index + 1 })}
              className='min-w-0 space-y-3 rounded-lg border p-4'
            >
              <div className='grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_12rem]'>
                <FormField
                  control={form.control}
                  name={`rows.${index}.model`}
                  render={({ field }) => (
                    <FormItem className='min-w-0'>
                      <FormLabel>{t('Model')}</FormLabel>
                      <FormControl>
                        <Combobox
                          options={options}
                          value={field.value}
                          onValueChange={field.onChange}
                          onBlur={field.onBlur}
                          ref={field.ref}
                          aria-label={t('Model')}
                          placeholder={t('Select a model')}
                          disabled={catalog.isPending && !options.length}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name={`rows.${index}.unit`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Billing unit')}</FormLabel>
                      <Select
                        value={field.value}
                        onValueChange={field.onChange}
                      >
                        <FormControl>
                          <SelectTrigger
                            aria-label={t('Billing unit')}
                            className='w-full'
                          >
                            <SelectValue>
                              {t(unitLabelKeys[field.value])}
                            </SelectValue>
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            {groupPriceUnits.map((unit) => (
                              <SelectItem key={unit} value={unit}>
                                {t(unitLabelKeys[unit])}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                    </FormItem>
                  )}
                />
              </div>
              <div className='grid gap-3 sm:grid-cols-2'>
                <FormField
                  control={form.control}
                  name={`rows.${index}.price`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {values[index]?.unit === 'token'
                          ? t('Input price (USD / 1M tokens)')
                          : t('Unit price (USD)')}
                      </FormLabel>
                      <FormControl>
                        <PricingAmountInput
                          {...field}
                          onChange={field.onChange}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                {values[index]?.unit === 'token' && (
                  <FormField
                    control={form.control}
                    name={`rows.${index}.outputPrice`}
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Output price (USD / 1M tokens)')}
                        </FormLabel>
                        <FormControl>
                          <PricingAmountInput
                            {...field}
                            onChange={field.onChange}
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                )}
              </div>
              <div className='flex flex-wrap items-center justify-between gap-3'>
                <FormField
                  control={form.control}
                  name={`rows.${index}.enabled`}
                  render={({ field }) => (
                    <FormItem className='flex items-center gap-2'>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                      <FormLabel>{t('Enable group price')}</FormLabel>
                    </FormItem>
                  )}
                />
                {props.onConfigureConcurrency && (
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    disabled={!values[index]?.model.trim()}
                    onClick={() =>
                      props.onConfigureConcurrency?.(
                        values[index]?.model.trim() ?? ''
                      )
                    }
                    aria-label={t('User concurrency for {{model}}', {
                      model: values[index]?.model || t('Model'),
                    })}
                  >
                    {t('User concurrency')}
                  </Button>
                )}
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => rows.remove(index)}
                >
                  <Trash2 aria-hidden className='mr-2 size-4' />
                  {t('Remove')}
                </Button>
              </div>
            </section>
          ))}
          <Button
            type='button'
            variant='outline'
            onClick={() =>
              rows.append({
                model: '',
                unit: 'image',
                price: '',
                outputPrice: '',
                enabled: true,
              })
            }
          >
            <Plus aria-hidden className='mr-2 size-4' />
            {t('Add model price')}
          </Button>
        </div>
      </Form>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Disabled or removed prices keep existing pricing rules. Click “Save group settings” after applying the draft to make changes effective.'
        )}
      </p>
    </Dialog>
  )
}
