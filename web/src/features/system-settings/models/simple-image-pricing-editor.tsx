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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  formatPricingAmount,
  type PricingCurrency,
} from '@/features/model-pricing/currency'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'

import type {
  SimpleImagePrice,
  SimpleImagePricingState,
} from './simple-image-pricing'

export function SimpleImagePricingEditor(props: {
  state: SimpleImagePricingState
  currency: PricingCurrency
  onChange: (price: SimpleImagePrice) => void
}) {
  const { t } = useTranslation()
  const priceId = useId()
  const [confirmed, setConfirmed] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [draftAmount, setDraftAmount] = useState<string | null>(null)
  const [draftUnit, setDraftUnit] = useState<SimpleImagePrice['unit'] | null>(
    null
  )
  const source = props.state.source
  useEffect(() => {
    setDraftAmount(null)
    setDraftUnit(null)
    setConfirmed(false)
  }, [source])
  const unit =
    draftUnit ??
    props.state.common?.unit ??
    props.state.rows[0].price?.unit ??
    'image'
  const value =
    draftAmount ?? (props.state.common ? String(props.state.common.amount) : '')
  const locked = props.state.complex && !confirmed
  const invalid =
    !locked && (draftAmount === '' || (draftUnit !== null && value === ''))
  const apply = (nextUnit: SimpleImagePrice['unit'], nextValue: string) => {
    if (
      nextValue !== '' &&
      Number.isFinite(Number(nextValue)) &&
      Number(nextValue) >= 0
    ) {
      props.onChange({ unit: nextUnit, amount: Number(nextValue) })
    }
  }
  return (
    <section
      aria-label={t('Simple image pricing')}
      className='space-y-4 rounded-lg border p-4'
      data-billing-invalid={invalid || undefined}
    >
      <div className='space-y-1'>
        <h4 className='font-medium'>{t('Simple image pricing')}</h4>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Set a unit price for ordinary and background image requests together. Group multipliers still apply.'
          )}
        </p>
      </div>
      <div className='text-muted-foreground space-y-1 text-sm'>
        {props.state.rows.map((row) => {
          let description = t('Advanced or unconfigured pricing')
          if (row.price) {
            description = `${formatPricingAmount(row.price.amount, props.currency)}/${t(row.price.unit)}`
          }
          return (
            <p key={row.key}>
              {row.key === '__ordinary__' ? t('Ordinary OpenAI') : row.name}:{' '}
              {description}
            </p>
          )
        })}
      </div>
      {locked ? (
        <Alert>
          <AlertDescription className='space-y-3'>
            <p>
              {t(
                'Existing advanced rules are preserved. Use advanced settings, or explicitly replace them with a simple price.'
              )}
            </p>
            <Button
              type='button'
              variant='outline'
              onClick={() => setDialogOpen(true)}
            >
              {t('Use simple pricing')}
            </Button>
          </AlertDescription>
        </Alert>
      ) : (
        <>
          {!props.state.common && (
            <p className='text-sm text-amber-700 dark:text-amber-400'>
              {t(
                'Prices differ or are not configured. Entering a price updates both ordinary and background image pricing.'
              )}
            </p>
          )}
          <div className='grid gap-4 sm:grid-cols-2'>
            <Field>
              <FieldLabel>{t('Charge by')}</FieldLabel>
              <Select
                value={unit}
                onValueChange={(next) => {
                  if (next !== 'request' && next !== 'image') return
                  setDraftUnit(next)
                  apply(next, value)
                }}
                items={[
                  { value: 'request', label: t('Per request') },
                  { value: 'image', label: t('Per image') },
                ]}
              >
                <SelectTrigger aria-label={t('Charge by')} className='w-full'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='request'>{t('Per request')}</SelectItem>
                  <SelectItem value='image'>{t('Per image')}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor={priceId}>{t('Unit price')}</FieldLabel>
              <div className='flex items-center gap-2'>
                <PricingAmountInput
                  id={priceId}
                  aria-label={t('Unit price')}
                  currency={props.currency}
                  value={value}
                  aria-invalid={invalid || undefined}
                  onChange={(next) => {
                    setDraftAmount(next)
                    apply(unit, next)
                  }}
                />
                <span className='text-muted-foreground shrink-0 text-sm'>
                  {props.currency.symbol}/{t(unit)}
                </span>
              </div>
              <FieldDescription>
                {unit === 'image'
                  ? t(
                      'One request returning three images is charged three unit prices.'
                    )
                  : t(
                      'One successful request is charged once, regardless of image count.'
                    )}
              </FieldDescription>
            </Field>
          </div>
        </>
      )}
      <ConfirmDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title={t('Use simple pricing')}
        desc={t(
          'Existing expressions and request rules will only be replaced after you enter a new simple price. Changes apply after saving.'
        )}
        confirmText={t('Use simple pricing')}
        handleConfirm={() => {
          setConfirmed(true)
          setDialogOpen(false)
        }}
      />
    </section>
  )
}
