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
import { AlertTriangle, Save } from 'lucide-react'
import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useState,
} from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { sideDrawerContentClassName } from '@/components/drawer-layout'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
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
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'

import {
  EMPTY_LANE_ENABLED,
  EMPTY_LANE_PRICES,
  buildPreviewRows,
  createInitialLaneState,
  createModelPricingSchema,
  hasValue,
  laneConfigs,
  numericDraftRegex,
  ratioFieldByLane,
  toNumberOrNull,
  type LaneKey,
  type ModelPricingFormValues,
  type ModelRatioData,
  type PricingMode,
} from './model-pricing-core'
import { PriceInput, PriceLane } from './model-pricing-inputs'
import { formatPricingNumber } from './pricing-format'
import { TieredPricingEditor } from './tiered-pricing-editor'

export type { ModelRatioData } from './model-pricing-core'

const IMAGE_QUALITY_LEVELS = ['low', 'medium', 'high'] as const
const IMAGE_RESOLUTION_LEVELS = ['1k', '2k', '4k'] as const

type ImageQualityLevel = (typeof IMAGE_QUALITY_LEVELS)[number]
type ImageResolutionLevel = (typeof IMAGE_RESOLUTION_LEVELS)[number]
type ImageQualityPriceDraft = Record<ImageQualityLevel, Record<ImageResolutionLevel, string>>

type ImageQualityPriceItem = {
  quality: ImageQualityLevel
  resolution: ImageResolutionLevel
  price: number
  enabled: boolean
}

type VideoResolutionKey = '480p' | '720p' | '1080p' | '4k'

function createEmptyVideoResolutionEnabled(): Record<VideoResolutionKey, boolean> {
  return {
    '480p': false,
    '720p': false,
    '1080p': false,
    '4k': false,
  }
}

function hasConfiguredVideoPrice(value?: string): boolean {
  return typeof value === 'string' && value.trim() !== ''
}

function createEmptyImageQualityPriceDraft(): ImageQualityPriceDraft {
  return {
    low: { '1k': '', '2k': '', '4k': '' },
    medium: { '1k': '', '2k': '', '4k': '' },
    high: { '1k': '', '2k': '', '4k': '' },
  }
}

function imageQualityItemsToDraft(items?: ImageQualityPriceItem[]): ImageQualityPriceDraft {
  const draft = createEmptyImageQualityPriceDraft()
  for (const item of items || []) {
    if (
      IMAGE_QUALITY_LEVELS.includes(item.quality) &&
      IMAGE_RESOLUTION_LEVELS.includes(item.resolution)
    ) {
      draft[item.quality][item.resolution] = String(item.price)
    }
  }
  return draft
}


type ModelPricingSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  editData?: ModelRatioData | null
  onSave?: () => void | Promise<void>
  isSaving?: boolean
}

type ModelPricingEditorPanelProps = Omit<
  ModelPricingSheetProps,
  'open' | 'onOpenChange'
> & {
  className?: string
}

export type ModelPricingEditorPanelHandle = {
  commitDraft: () => Promise<ModelRatioData | null>
}

export const ModelPricingSheet = forwardRef<
  ModelPricingEditorPanelHandle,
  ModelPricingSheetProps
>(function ModelPricingSheet(
  { open, onOpenChange, editData, onSave, isSaving },
  ref
) {
  const { t } = useTranslation()
  const title = editData ? t('Edit model pricing') : t('Add model pricing')
  const description = editData?.name || t('New model')

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side='right'
        className={sideDrawerContentClassName('sm:max-w-2xl')}
      >
        <SheetHeader className='sr-only'>
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>{description}</SheetDescription>
        </SheetHeader>
        <ModelPricingEditorPanel
          ref={ref}
          editData={editData}
          onSave={onSave}
          isSaving={isSaving}
          className='h-full rounded-none border-0'
        />
      </SheetContent>
    </Sheet>
  )
})

export const ModelPricingEditorPanel = forwardRef<
  ModelPricingEditorPanelHandle,
  ModelPricingEditorPanelProps
>(function ModelPricingEditorPanel(
  { editData, className, onSave, isSaving },
  ref
) {
  const { t } = useTranslation()
  const [pricingMode, setPricingMode] = useState<PricingMode>('per-token')
  const [promptPrice, setPromptPrice] = useState('')
  const [lanePrices, setLanePrices] = useState<Record<LaneKey, string>>({
    ...EMPTY_LANE_PRICES,
  })
  const [laneEnabled, setLaneEnabled] = useState<Record<LaneKey, boolean>>({
    ...EMPTY_LANE_ENABLED,
  })
  const [billingExpr, setBillingExpr] = useState('')
  const [requestRuleExpr, setRequestRuleExpr] = useState('')
  const [editorReloadToken, setEditorReloadToken] = useState(0)
  const [imageQualityPriceDraft, setImageQualityPriceDraft] = useState<ImageQualityPriceDraft>(() =>
    createEmptyImageQualityPriceDraft()
  )
  const [isSavingImageQualityPricing, setIsSavingImageQualityPricing] = useState(false)
  const [videoResolutionEnabled, setVideoResolutionEnabled] = useState<
    Record<VideoResolutionKey, boolean>
  >(() => createEmptyVideoResolutionEnabled())
  const isEditMode = !!editData

  const form = useForm<ModelPricingFormValues>({
    resolver: zodResolver(createModelPricingSchema(t)),
    defaultValues: {
      name: '',
      price: '',
      ratio: '',
      cacheRatio: '',
      createCacheRatio: '',
      completionRatio: '',
      imageRatio: '',
      audioRatio: '',
      audioCompletionRatio: '',
      videoPrice480p: '',
      videoPrice720p: '',
      videoPrice1080p: '',
      videoPrice4k: '',
    },
  })


  useEffect(() => {
    const modelName = editData?.name?.trim()
    if (!modelName) {
      setImageQualityPriceDraft(createEmptyImageQualityPriceDraft())
      return
    }

    let cancelled = false
    async function loadImageQualityPrices() {
      try {
        const res = await api.get('/api/models/image-quality-prices', {
          params: { model_name: modelName },
        })
        if (cancelled) return
        if (res.data?.success === false) {
          throw new Error(res.data?.message || t('加载生图品质定价失败'))
        }
        const items = (res.data?.data || []) as ImageQualityPriceItem[]
        setImageQualityPriceDraft(imageQualityItemsToDraft(items))
      } catch {
        if (!cancelled) {
          setImageQualityPriceDraft(createEmptyImageQualityPriceDraft())
        }
      }
    }

    loadImageQualityPrices()
    return () => {
      cancelled = true
    }
  }, [editData?.name])

  useEffect(() => {
    const nextLaneState = createInitialLaneState(editData)
    const nextVideoResolutionEnabled =
      editData?.billingMode === 'video_per_second'
        ? {
            '480p': hasConfiguredVideoPrice(editData.videoPrice480p),
            '720p': hasConfiguredVideoPrice(editData.videoPrice720p),
            '1080p': hasConfiguredVideoPrice(editData.videoPrice1080p),
            '4k': hasConfiguredVideoPrice(editData.videoPrice4k),
          }
        : createEmptyVideoResolutionEnabled()

    if (editData) {
      form.reset({
        name: editData.name,
        price: editData.price || '',
        ratio: editData.ratio || '',
        cacheRatio: editData.cacheRatio || '',
        createCacheRatio: editData.createCacheRatio || '',
        completionRatio: editData.completionRatio || '',
        imageRatio: editData.imageRatio || '',
        audioRatio: editData.audioRatio || '',
        audioCompletionRatio: editData.audioCompletionRatio || '',
        videoPrice480p: editData.videoPrice480p || '',
        videoPrice720p: editData.videoPrice720p || '',
        videoPrice1080p: editData.videoPrice1080p || '',
        videoPrice4k: editData.videoPrice4k || '',
      })
      setPricingMode(
        editData.billingMode === 'image_quality'
          ? 'image_quality'
          : editData.billingMode === 'video_per_second'
            ? 'video_per_second'
          : editData.billingMode === 'tiered_expr'
            ? 'tiered_expr'
            : editData.price
              ? 'per-request'
              : 'per-token'
      )
      setBillingExpr(editData.billingExpr || '')
      setRequestRuleExpr(editData.requestRuleExpr || '')
    } else {
      form.reset({
        name: '',
        price: '',
        ratio: '',
        cacheRatio: '',
        createCacheRatio: '',
        completionRatio: '',
        imageRatio: '',
        audioRatio: '',
        audioCompletionRatio: '',
        videoPrice480p: '',
        videoPrice720p: '',
        videoPrice1080p: '',
        videoPrice4k: '',
      })
      setPricingMode('per-token')
      setBillingExpr('')
      setRequestRuleExpr('')
    }

    setPromptPrice(nextLaneState.promptPrice)
    setLanePrices(nextLaneState.prices)
    setLaneEnabled(nextLaneState.enabled)
    setVideoResolutionEnabled(nextVideoResolutionEnabled)
    setEditorReloadToken((token) => token + 1)
  }, [editData, form])

  const setFormValue = (field: keyof ModelPricingFormValues, value: string) => {
    form.setValue(field, value, {
      shouldDirty: true,
      shouldValidate: true,
    })
  }

  const deriveLaneRatio = (
    lane: LaneKey,
    price: string,
    nextPromptPrice = promptPrice,
    nextLanePrices = lanePrices
  ) => {
    const priceNumber = toNumberOrNull(price)
    if (priceNumber === null) return ''

    if (lane === 'audioOutput') {
      const audioInputPrice = toNumberOrNull(nextLanePrices.audioInput)
      if (audioInputPrice === null || audioInputPrice === 0) return ''
      return formatPricingNumber(priceNumber / audioInputPrice)
    }

    const inputPrice = toNumberOrNull(nextPromptPrice)
    if (inputPrice === null || inputPrice === 0) return ''
    return formatPricingNumber(priceNumber / inputPrice)
  }

  const syncLaneRatios = (
    nextPromptPrice = promptPrice,
    nextLanePrices = lanePrices,
    nextLaneEnabled = laneEnabled
  ) => {
    const inputPrice = toNumberOrNull(nextPromptPrice)
    setFormValue(
      'ratio',
      inputPrice !== null ? formatPricingNumber(inputPrice / 2) : ''
    )

    laneConfigs.forEach(({ key }) => {
      const ratioField = ratioFieldByLane[key]
      if (!nextLaneEnabled[key]) {
        setFormValue(ratioField, '')
        return
      }
      setFormValue(
        ratioField,
        deriveLaneRatio(
          key,
          nextLanePrices[key],
          nextPromptPrice,
          nextLanePrices
        )
      )
    })
  }

  const handlePromptPriceChange = (value: string) => {
    if (!numericDraftRegex.test(value)) return
    setPromptPrice(value)
    syncLaneRatios(value, lanePrices, laneEnabled)
  }

  const handleLanePriceChange = (lane: LaneKey, value: string) => {
    if (!numericDraftRegex.test(value)) return
    const nextLanePrices = { ...lanePrices, [lane]: value }
    setLanePrices(nextLanePrices)

    if (laneEnabled[lane]) {
      setFormValue(
        ratioFieldByLane[lane],
        deriveLaneRatio(lane, value, promptPrice, nextLanePrices)
      )
    }

    if (lane === 'audioInput' && laneEnabled.audioOutput) {
      setFormValue(
        'audioCompletionRatio',
        deriveLaneRatio(
          'audioOutput',
          nextLanePrices.audioOutput,
          promptPrice,
          nextLanePrices
        )
      )
    }
  }

  const handleLaneToggle = (lane: LaneKey, checked: boolean) => {
    const nextEnabled = { ...laneEnabled, [lane]: checked }
    let nextPrices = lanePrices

    if (!checked) {
      nextPrices = { ...nextPrices, [lane]: '' }
      setFormValue(ratioFieldByLane[lane], '')
      if (lane === 'audioInput') {
        nextEnabled.audioOutput = false
        nextPrices.audioOutput = ''
        setFormValue('audioCompletionRatio', '')
      }
    }

    setLaneEnabled(nextEnabled)
    setLanePrices(nextPrices)

    if (checked) {
      setFormValue(
        ratioFieldByLane[lane],
        deriveLaneRatio(lane, nextPrices[lane], promptPrice, nextPrices)
      )
    }
  }

  const handleModeChange = (value: string) => {
    const nextMode = value as PricingMode
    setPricingMode(nextMode)
    if (nextMode === 'tiered_expr' && !billingExpr) {
      setBillingExpr('tier("base", p * 0 + c * 0)')
    }
  }

  const handleVideoResolutionToggle = (
    resolution: VideoResolutionKey,
    checked: boolean
  ) => {
    setVideoResolutionEnabled((prev) => ({
      ...prev,
      [resolution]: checked,
    }))
  }

  const renderVideoResolutionToggle = (resolution: VideoResolutionKey) => {
    const enabled = videoResolutionEnabled[resolution]
    return (
      <div className='flex items-center gap-2'>
        <span
          className={
            enabled
              ? 'text-primary text-xs font-medium'
              : 'text-muted-foreground text-xs'
          }
        >
          {t(enabled ? 'Enabled' : 'Disabled')}
        </span>
        <Switch
          checked={enabled}
          onCheckedChange={(checked) =>
            handleVideoResolutionToggle(resolution, checked)
          }
        />
      </div>
    )
  }

  const handleImageQualityPriceChange = (
    quality: ImageQualityLevel,
    resolution: ImageResolutionLevel,
    value: string
  ) => {
    if (!numericDraftRegex.test(value)) return
    setImageQualityPriceDraft((prev) => ({
      ...prev,
      [quality]: {
        ...prev[quality],
        [resolution]: value,
      },
    }))
  }

  const saveImageQualityPricing = async () => {
    const modelName = form.getValues('name').trim()
    if (!modelName) {
      toast.error(t('Model name is required'))
      return
    }

    const prices: ImageQualityPriceItem[] = []
    for (const quality of IMAGE_QUALITY_LEVELS) {
      for (const resolution of IMAGE_RESOLUTION_LEVELS) {
        const raw = imageQualityPriceDraft[quality][resolution]
        const price = Number(raw)
        if (raw === '' || !Number.isFinite(price) || price < 0) {
          toast.error(t('请完整填写所有品质和分辨率的价格'))
          return
        }
        prices.push({ quality, resolution, price, enabled: true })
      }
    }

    let imageQualityPricingSaved = false
    setIsSavingImageQualityPricing(true)
    try {
      const res = await api.post('/api/models/image-quality-prices', {
        model_name: modelName,
        enabled: true,
        prices,
      })
      if (res.data?.success === false) {
        throw new Error(res.data?.message || t('保存生图品质定价失败'))
      }
      imageQualityPricingSaved = true

      // The quality-price matrix and the model billing mode are persisted by
      // different APIs. Save the outer model form as part of the same user
      // action so the model immediately leaves the "unset pricing" list.
      await onSave?.()

      toast.success(t('生图品质定价和计费模式已保存'))
    } catch (error: any) {
      const fallbackMessage = imageQualityPricingSaved
        ? t('品质价格已保存，但模型计费模式保存失败，请重试')
        : t('保存生图品质定价失败')
      toast.error(
        error?.response?.data?.message || error?.message || fallbackMessage
      )
    } finally {
      setIsSavingImageQualityPricing(false)
    }
  }

  const watchedValues = form.watch()
  const previewRows = useMemo(
    () =>
      buildPreviewRows(
        watchedValues,
        pricingMode,
        billingExpr,
        requestRuleExpr,
        promptPrice,
        lanePrices,
        laneEnabled,
        t
      ),
    [
      billingExpr,
      laneEnabled,
      lanePrices,
      pricingMode,
      promptPrice,
      requestRuleExpr,
      t,
      watchedValues,
    ]
  )

  const warnings = useMemo(() => {
    const nextWarnings: string[] = []
    const hasConflict =
      !!editData?.price &&
      [
        editData.ratio,
        editData.completionRatio,
        editData.cacheRatio,
        editData.createCacheRatio,
        editData.imageRatio,
        editData.audioRatio,
        editData.audioCompletionRatio,
      ].some(hasValue)

    if (hasConflict) {
      nextWarnings.push(
        t(
          'This model has both fixed-price and token-price settings. Saving the current mode will rewrite the conflicting fields.'
        )
      )
    }

    if (
      pricingMode === 'per-token' &&
      toNumberOrNull(promptPrice) === null &&
      laneConfigs.some(
        ({ key }) => laneEnabled[key] && hasValue(lanePrices[key])
      )
    ) {
      nextWarnings.push(
        t('Input price is required before saving dependent prices.')
      )
    }

    if (
      pricingMode === 'per-token' &&
      laneEnabled.audioOutput &&
      !hasValue(lanePrices.audioInput)
    ) {
      nextWarnings.push(t('Audio output price requires an audio input price.'))
    }

    return nextWarnings
  }, [editData, laneEnabled, lanePrices, pricingMode, promptPrice, t])

  const validatePricingValues = useCallback(() => {
    if (
      pricingMode === 'per-token' &&
      toNumberOrNull(promptPrice) === null &&
      laneConfigs.some(
        ({ key }) => laneEnabled[key] && hasValue(lanePrices[key])
      )
    ) {
      form.setError('ratio', {
        message: t('Input price is required before saving dependent prices.'),
      })
      return false
    }

    if (
      pricingMode === 'per-token' &&
      laneEnabled.audioOutput &&
      !hasValue(lanePrices.audioInput)
    ) {
      form.setError('audioRatio', {
        message: t('Audio output price requires an audio input price.'),
      })
      return false
    }

    return true
  }, [form, laneEnabled, lanePrices, pricingMode, promptPrice, t])

  const buildSubmitData = useCallback(
    (values: ModelPricingFormValues) => {
      const data: ModelRatioData = {
        name: values.name.trim(),
        billingMode: pricingMode,
        price: values.price || '',
        ratio: values.ratio || '',
        cacheRatio: values.cacheRatio || '',
        createCacheRatio: values.createCacheRatio || '',
        completionRatio: values.completionRatio || '',
        imageRatio: values.imageRatio || '',
        audioRatio: values.audioRatio || '',
        audioCompletionRatio: values.audioCompletionRatio || '',
        videoPrice480p: videoResolutionEnabled['480p']
          ? values.videoPrice480p || ''
          : '',
        videoPrice720p: videoResolutionEnabled['720p']
          ? values.videoPrice720p || ''
          : '',
        videoPrice1080p: videoResolutionEnabled['1080p']
          ? values.videoPrice1080p || ''
          : '',
        videoPrice4k: videoResolutionEnabled['4k']
          ? values.videoPrice4k || ''
          : '',
      }

      if (pricingMode === 'tiered_expr') {
        data.billingExpr = billingExpr
        data.requestRuleExpr = requestRuleExpr
      } else if (pricingMode === 'video_per_second') {
        const videoPrices: Record<string, number> = {}
        if (videoResolutionEnabled['480p']) {
          videoPrices['480p'] = Number(values.videoPrice480p || 0)
        }
        if (videoResolutionEnabled['720p']) {
          videoPrices['720p'] = Number(values.videoPrice720p || 0)
        }
        if (videoResolutionEnabled['1080p']) {
          videoPrices['1080p'] = Number(values.videoPrice1080p || 0)
        }
        if (videoResolutionEnabled['4k']) {
          videoPrices['4k'] = Number(values.videoPrice4k || 0)
        }
        data.billingExpr = JSON.stringify(videoPrices)
      }

      return data
    },
    [billingExpr, pricingMode, requestRuleExpr, videoResolutionEnabled]
  )

  useImperativeHandle(
    ref,
    () => ({
      commitDraft: async () => {
        const isValid = await form.trigger()
        if (!isValid || !validatePricingValues()) return null
        return buildSubmitData(form.getValues())
      },
    }),
    [form, validatePricingValues, buildSubmitData]
  )

  const showActions = Boolean(onSave)

  return (
    <div
      className={cn(
        'bg-background flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border',
        className
      )}
    >
      <div className='border-b p-4'>
        <div className='flex flex-wrap items-start justify-between gap-3'>
          <div className='min-w-0'>
            <h3 className='truncate text-base font-medium'>
              {isEditMode ? t('Edit model pricing') : t('Add model pricing')}
            </h3>
          </div>
        </div>
      </div>

      <Form {...form}>
        <form
          onSubmit={(event) => event.preventDefault()}
          className='flex min-h-0 flex-1 flex-col'
          autoComplete='off'
        >
          <div className='min-h-0 flex-1 overflow-y-auto p-4 pb-6'>
            <div className='grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(220px,260px)]'>
              <FieldGroup>
                {warnings.length > 0 && (
                  <Alert variant='destructive'>
                    <AlertTriangle data-icon='inline-start' />
                    <AlertDescription>
                      <div className='flex flex-col gap-1'>
                        {warnings.map((warning) => (
                          <span key={warning}>{warning}</span>
                        ))}
                      </div>
                    </AlertDescription>
                  </Alert>
                )}

                <FormField
                  control={form.control}
                  name='name'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Model name')}</FormLabel>
                      <FormControl>
                        <Input
                          placeholder={t('gpt-4')}
                          {...field}
                          disabled={isEditMode}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'The exact model identifier as used in API requests.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <Tabs
                  value={pricingMode}
                  onValueChange={handleModeChange}
                  className='gap-4'
                >
                  <TabsList className='grid w-full grid-cols-5'>
                    <TabsTrigger value='per-token'>
                      {t('Per-token')}
                    </TabsTrigger>
                    <TabsTrigger value='per-request'>
                      {t('Per-request')}
                    </TabsTrigger>
                    <TabsTrigger value='tiered_expr'>
                      {t('Expression')}
                    </TabsTrigger>
                    <TabsTrigger value='video_per_second'>
                      {t('按秒')}
                    </TabsTrigger>
                    <TabsTrigger value='image_quality'>
                      {t('按品质')}
                    </TabsTrigger>
                  </TabsList>

                  <TabsContent value='per-token' className='pt-0'>
                    <FieldGroup className='gap-5'>
                      <Field>
                        <FieldLabel>{t('Input price')}</FieldLabel>
                        <PriceInput
                          value={promptPrice}
                          placeholder='3'
                          onChange={handlePromptPriceChange}
                        />
                        <FieldDescription>
                          {t('USD price per 1M input tokens.')}
                        </FieldDescription>
                      </Field>

                      <div className='grid gap-3 sm:grid-cols-[repeat(auto-fit,minmax(400px,1fr))]'>
                        {laneConfigs.map((lane) => {
                          const disabled =
                            lane.key === 'audioOutput' &&
                            (!laneEnabled.audioInput ||
                              !hasValue(lanePrices.audioInput))
                          return (
                            <PriceLane
                              key={lane.key}
                              title={t(lane.titleKey)}
                              description={t(lane.descriptionKey)}
                              placeholder={lane.placeholder}
                              value={lanePrices[lane.key]}
                              enabled={laneEnabled[lane.key]}
                              disabled={disabled}
                              onEnabledChange={(checked) =>
                                handleLaneToggle(lane.key, checked)
                              }
                              onChange={(value) =>
                                handleLanePriceChange(lane.key, value)
                              }
                            />
                          )
                        })}
                      </div>
                    </FieldGroup>
                  </TabsContent>

                  <TabsContent value='per-request' className='pt-0'>
                    <FieldGroup className='gap-5'>
                      <FormField
                        control={form.control}
                        name='price'
                        render={({ field }) => (
                          <FormItem className='contents'>
                            <Field>
                              <FieldLabel>{t('Fixed price')}</FieldLabel>
                              <FormControl>
                                <InputGroup>
                                  <InputGroupAddon>$</InputGroupAddon>
                                  <InputGroupInput
                                    inputMode='decimal'
                                    placeholder='0.01'
                                    {...field}
                                    onChange={(event) => {
                                      const value = event.target.value
                                      if (numericDraftRegex.test(value)) {
                                        field.onChange(value)
                                      }
                                    }}
                                  />
                                  <InputGroupAddon align='inline-end'>
                                    {t('per request')}
                                  </InputGroupAddon>
                                </InputGroup>
                              </FormControl>
                              <FieldDescription>
                                {t(
                                  'Cost in USD per request, regardless of tokens used.'
                                )}
                              </FieldDescription>
                              <FormMessage />
                            </Field>
                          </FormItem>
                        )}
                      />

                    </FieldGroup>
                  </TabsContent>

                  <TabsContent value='video_per_second' className='pt-0'>
                    <FieldGroup className='gap-5'>
                      <FormField
                        control={form.control}
                        name='videoPrice480p'
                        render={({ field }) => (
                          <FormItem className='contents'>
                            <Field>
                              <div className='flex items-center justify-between gap-3'>
                                <FieldLabel>480p</FieldLabel>
                                {renderVideoResolutionToggle('480p')}
                              </div>
                              <FormControl>
                                <InputGroup>
                                  <InputGroupAddon>$</InputGroupAddon>
                                  <InputGroupInput
                                    {...field}
                                    inputMode='decimal'
                                    value={
                                      videoResolutionEnabled['480p']
                                        ? field.value
                                        : ''
                                    }
                                    placeholder={
                                      videoResolutionEnabled['480p']
                                        ? '0.03'
                                        : t('Disabled')
                                    }
                                    disabled={!videoResolutionEnabled['480p']}
                                    onChange={(event) => {
                                      const value = event.target.value
                                      if (numericDraftRegex.test(value)) {
                                        field.onChange(value)
                                      }
                                    }}
                                  />
                                  <InputGroupAddon align='inline-end'>
                                    {t('每秒')}
                                  </InputGroupAddon>
                                </InputGroup>
                              </FormControl>
                              <FieldDescription>
                                {videoResolutionEnabled['480p']
                                  ? t('Video generation price 每秒.')
                                  : t('Disabled')}
                              </FieldDescription>
                              <FormMessage />
                            </Field>
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='videoPrice720p'
                        render={({ field }) => (
                          <FormItem className='contents'>
                            <Field>
                              <div className='flex items-center justify-between gap-3'>
                                <FieldLabel>720p</FieldLabel>
                                {renderVideoResolutionToggle('720p')}
                              </div>
                              <FormControl>
                                <InputGroup>
                                  <InputGroupAddon>$</InputGroupAddon>
                                  <InputGroupInput
                                    {...field}
                                    inputMode='decimal'
                                    value={
                                      videoResolutionEnabled['720p']
                                        ? field.value
                                        : ''
                                    }
                                    placeholder={
                                      videoResolutionEnabled['720p']
                                        ? '0.05'
                                        : t('Disabled')
                                    }
                                    disabled={!videoResolutionEnabled['720p']}
                                    onChange={(event) => {
                                      const value = event.target.value
                                      if (numericDraftRegex.test(value)) {
                                        field.onChange(value)
                                      }
                                    }}
                                  />
                                  <InputGroupAddon align='inline-end'>
                                    {t('每秒')}
                                  </InputGroupAddon>
                                </InputGroup>
                              </FormControl>
                              <FieldDescription>
                                {videoResolutionEnabled['720p']
                                  ? t('Video generation price 每秒.')
                                  : t('Disabled')}
                              </FieldDescription>
                              <FormMessage />
                            </Field>
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name='videoPrice1080p'
                        render={({ field }) => (
                          <FormItem className='contents'>
                            <Field>
                              <div className='flex items-center justify-between gap-3'>
                                <FieldLabel>1080p</FieldLabel>
                                {renderVideoResolutionToggle('1080p')}
                              </div>
                              <FormControl>
                                <InputGroup>
                                  <InputGroupAddon>$</InputGroupAddon>
                                  <InputGroupInput
                                    {...field}
                                    inputMode='decimal'
                                    value={
                                      videoResolutionEnabled['1080p']
                                        ? field.value
                                        : ''
                                    }
                                    placeholder={
                                      videoResolutionEnabled['1080p']
                                        ? '0.08'
                                        : t('Disabled')
                                    }
                                    disabled={!videoResolutionEnabled['1080p']}
                                    onChange={(event) => {
                                      const value = event.target.value
                                      if (numericDraftRegex.test(value)) {
                                        field.onChange(value)
                                      }
                                    }}
                                  />
                                  <InputGroupAddon align='inline-end'>
                                    {t('每秒')}
                                  </InputGroupAddon>
                                </InputGroup>
                              </FormControl>
                              <FieldDescription>
                                {videoResolutionEnabled['1080p']
                                  ? t('Video generation price 每秒.')
                                  : t('Disabled')}
                              </FieldDescription>
                              <FormMessage />
                            </Field>
                          </FormItem>
                        )}
                      />
                      <FormField
                        control={form.control}
                        name='videoPrice4k'
                        render={({ field }) => (
                          <FormItem className='contents'>
                            <Field>
                              <div className='flex items-center justify-between gap-3'>
                                <FieldLabel>4K</FieldLabel>
                                {renderVideoResolutionToggle('4k')}
                              </div>
                              <FormControl>
                                <InputGroup>
                                  <InputGroupAddon>$</InputGroupAddon>
                                  <InputGroupInput
                                    {...field}
                                    inputMode='decimal'
                                    value={
                                      videoResolutionEnabled['4k']
                                        ? field.value
                                        : ''
                                    }
                                    placeholder={
                                      videoResolutionEnabled['4k']
                                        ? '0.00'
                                        : t('Disabled')
                                    }
                                    disabled={!videoResolutionEnabled['4k']}
                                    onChange={(event) => {
                                      const value = event.target.value
                                      if (numericDraftRegex.test(value)) {
                                        field.onChange(value)
                                      }
                                    }}
                                  />
                                  <InputGroupAddon align='inline-end'>
                                    {t('每秒')}
                                  </InputGroupAddon>
                                </InputGroup>
                              </FormControl>
                              <FieldDescription>
                                {videoResolutionEnabled['4k']
                                  ? t('Video generation price 每秒.')
                                  : t('Disabled')}
                              </FieldDescription>
                              <FormMessage />
                            </Field>
                          </FormItem>
                        )}
                      />
                    </FieldGroup>
                  </TabsContent>

                  <TabsContent value='tiered_expr' className='pt-0'>
                    <FieldGroup className='gap-5'>
                      <TieredPricingEditor
                        key={editorReloadToken}
                        modelName={watchedValues.name}
                        billingExpr={billingExpr}
                        requestRuleExpr={requestRuleExpr}
                        onBillingExprChange={setBillingExpr}
                        onRequestRuleExprChange={setRequestRuleExpr}
                      />
                    </FieldGroup>
                  </TabsContent>
                  <TabsContent value='image_quality' className='pt-0'>
                <div className='rounded-lg border p-3'>
                  <div className='flex items-start justify-between gap-3'>
                    <div className='space-y-1'>
                      <div className='text-sm font-medium'>{t('生图品质定价')}</div>
                      <p className='text-muted-foreground text-xs'>
                        {t('开启后，该生图模型会按照请求中的 quality 和 size 匹配价格计费。')}
                      </p>
                    </div>
                  </div>

                    <div className='mt-4 space-y-4'>
                      {IMAGE_QUALITY_LEVELS.map((quality) => (
                        <div key={quality} className='space-y-2'>
                          <div className='text-sm font-medium'>{quality}</div>
                          <div className='grid grid-cols-3 gap-2'>
                            {IMAGE_RESOLUTION_LEVELS.map((resolution) => (
                              <div
                                key={`${quality}-${resolution}`}
                                className='space-y-1'
                              >
                                <FieldLabel className='text-xs uppercase'>
                                  {resolution}
                                </FieldLabel>
                                <InputGroup>
                                  <InputGroupAddon>$</InputGroupAddon>
                                  <InputGroupInput
                                    inputMode='decimal'
                                    placeholder='0.00'
                                    value={
                                      imageQualityPriceDraft[quality][resolution]
                                    }
                                    onChange={(event) =>
                                      handleImageQualityPriceChange(
                                        quality,
                                        resolution,
                                        event.target.value
                                      )
                                    }
                                  />
                                </InputGroup>
                              </div>
                            ))}
                          </div>
                        </div>
                      ))}
                      <p className='text-muted-foreground text-xs'>
                        {t('最终费用 = 对应品质分辨率单价 × 图片数量 n。用户专属品质密价优先级高于这里的公开价格。')}
                      </p>
                    </div>

                  <div className='mt-4 flex justify-end'>
                    <Button
                      type='button'
                      variant='outline'
                      onClick={saveImageQualityPricing}
                      disabled={
                        isSavingImageQualityPricing || !form.watch('name')
                      }
                    >
                        {isSavingImageQualityPricing
                          ? t('Saving...')
                          : t('保存生图品质定价')}
                    </Button>
                  </div>
                </div>
                  </TabsContent>
                </Tabs>
              </FieldGroup>

              <aside className='bg-muted/20 sticky top-0 rounded-lg border'>
                <div className='border-b px-3 py-2'>
                  <div className='text-sm font-medium'>{t('Preview')}</div>
                </div>
                <div className='divide-y'>
                  {previewRows.map((row) => (
                    <div key={row.key} className='grid gap-1 px-3 py-2.5'>
                      <span className='text-muted-foreground text-xs'>
                        {row.label}
                      </span>
                      <span
                        className={cn(
                          'min-w-0 text-sm',
                          row.multiline
                            ? 'font-mono text-xs leading-5 break-words whitespace-pre-wrap'
                            : 'truncate'
                        )}
                      >
                        {row.value}
                      </span>
                    </div>
                  ))}
                </div>
              </aside>
            </div>
          </div>
          {showActions && (
            <div className='bg-background/95 supports-[backdrop-filter]:bg-background/80 shrink-0 border-t p-3 backdrop-blur'>
              <div className='flex flex-col-reverse gap-2 sm:flex-row sm:justify-end'>
                {onSave && (
                  <Button
                    type='button'
                    onClick={onSave}
                    disabled={isSaving}
                    className='w-full sm:w-auto'
                  >
                    <Save data-icon='inline-start' />
                    {isSaving ? t('Saving...') : t('Save model prices')}
                  </Button>
                )}
              </div>
            </div>
          )}
        </form>
      </Form>
    </div>
  )
})
