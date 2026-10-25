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
import { useMutation, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Save } from 'lucide-react'
import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
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
import { InputGroup, InputGroupAddon } from '@/components/ui/input-group'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  previewModelPricing,
  previewModelPricingConversion,
  type ModelPricingPluginVariant,
} from '@/features/model-pricing/api'
import {
  getSitePricingCurrency,
  isValidPricingCurrency,
  USD_PRICING_CURRENCY,
} from '@/features/model-pricing/currency'
import { pricingFromDraft, pricingRow } from '@/features/model-pricing/pricing'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import {
  PricingConversionDialog,
  type PricingConversionPreview,
} from '@/features/model-pricing/pricing-conversion-dialog'
import { PricingCurrencySelector } from '@/features/model-pricing/pricing-currency-selector'
import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'
import { combineBillingExpr } from '@/features/pricing/lib/billing-expr'
import { pluginExpressionsEqual } from '@/features/pricing/lib/plugin-pricing'
import {
  createDefaultTaskVisualConfig,
  generateTaskExprFromConfig,
} from '@/features/pricing/lib/task-expr'
import type { BillingUsageSchema } from '@/features/pricing/types'
import { useDebounce } from '@/hooks/use-debounce'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import {
  EMPTY_LANE_ENABLED,
  EMPTY_LANE_PRICES,
  buildPreviewRows,
  createInitialLaneState,
  createModelPricingSchema,
  hasValue,
  laneConfigs,
  ratioFieldByLane,
  toNumberOrNull,
  usesTaskModelPricing,
  type LaneKey,
  type ModelPricingFormValues,
  type ModelRatioData,
  type PricingMode,
} from './model-pricing-core'
import { PriceInput, PriceLane } from './model-pricing-inputs'
import { formatPricingNumber } from './pricing-format'
import { TaskPluginPricingEditor } from './task-plugin-pricing-editor'
import { TaskUsagePricingEditor } from './task-usage-pricing-editor'
import { TieredPricingEditor } from './tiered-pricing-editor'

export type { ModelRatioData } from './model-pricing-core'

const numericDraftRegex = /^\d*\.?\d*$/

const IMAGE_QUALITY_LEVELS = ['low', 'medium', 'high'] as const
const IMAGE_RESOLUTION_LEVELS = ['1k', '2k', '4k'] as const

type ImageQualityLevel = (typeof IMAGE_QUALITY_LEVELS)[number]
type ImageResolutionLevel = (typeof IMAGE_RESOLUTION_LEVELS)[number]
type ImageQualityPriceDraft = Record<
  ImageQualityLevel,
  Record<ImageResolutionLevel, string>
>

type ImageQualityPriceItem = {
  quality: ImageQualityLevel
  resolution: ImageResolutionLevel
  price: number
  enabled: boolean
}

type VideoResolutionKey = '480p' | '720p' | '1080p' | '4k'

function createEmptyVideoResolutionEnabled(): Record<
  VideoResolutionKey,
  boolean
> {
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

function imageQualityItemsToDraft(
  items?: ImageQualityPriceItem[]
): ImageQualityPriceDraft {
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
  usageSchema?: BillingUsageSchema
  pluginVariants?: ModelPricingPluginVariant[]
  onDirtyChange?: (dirty: boolean) => void
}

type ModelPricingEditorPanelProps = Omit<
  ModelPricingSheetProps,
  'open' | 'onOpenChange'
> & {
  className?: string
  embedded?: boolean
  scrollHeader?: ReactNode
}

export type ModelPricingEditorPanelHandle = {
  commitDraft: () => Promise<ModelRatioData | null>
}

const DEFAULT_TOKEN_BILLING_EXPR = 'tier("base", p * 0 + c * 0)'

export const ModelPricingSheet = forwardRef<
  ModelPricingEditorPanelHandle,
  ModelPricingSheetProps
>(function ModelPricingSheet(
  {
    open,
    onOpenChange,
    editData,
    onSave,
    isSaving,
    usageSchema,
    pluginVariants,
    onDirtyChange,
  },
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
          usageSchema={usageSchema}
          pluginVariants={pluginVariants}
          onDirtyChange={onDirtyChange}
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
  {
    editData,
    className,
    onSave,
    isSaving,
    usageSchema,
    pluginVariants,
    onDirtyChange,
    embedded = false,
    scrollHeader,
  },
  ref
) {
  const { t } = useTranslation()
  const promptPriceId = useId()
  const formElementRef = useRef<HTMLFormElement>(null)
  const currencyConfig = useSystemConfigStore((state) => state.config.currency)
  const preference = usePricingPreferencesStore((state) => state.currency)
  const siteCurrency = useMemo(
    () => getSitePricingCurrency(currencyConfig),
    [currencyConfig]
  )
  const currency =
    preference === 'site' && isValidPricingCurrency(siteCurrency)
      ? siteCurrency
      : USD_PRICING_CURRENCY
  const [pricingMode, setPricingMode] = useState<PricingMode>('tiered_expr')
  const [promptPrice, setPromptPrice] = useState('')
  const [lanePrices, setLanePrices] = useState<Record<LaneKey, string>>({
    ...EMPTY_LANE_PRICES,
  })
  const [laneEnabled, setLaneEnabled] = useState<Record<LaneKey, boolean>>({
    ...EMPTY_LANE_ENABLED,
  })
  const [billingExpr, setBillingExpr] = useState(DEFAULT_TOKEN_BILLING_EXPR)
  const [conversionReason, setConversionReason] = useState('')
  const [wasConverted, setWasConverted] = useState(false)
  const conversionGeneration = useRef(0)
  const [conversionPreview, setConversionPreview] =
    useState<PricingConversionPreview | null>(null)
  const conversion = useMutation({
    mutationFn: previewModelPricingConversion,
    meta: { errorToast: false },
  })
  const [requestRuleExpr, setRequestRuleExpr] = useState('')
  const [pluginExpressions, setPluginExpressions] = useState<
    Record<string, string>
  >({})
  const [editorReloadToken, setEditorReloadToken] = useState(0)
  const [imageQualityPriceDraft, setImageQualityPriceDraft] =
    useState<ImageQualityPriceDraft>(() => createEmptyImageQualityPriceDraft())
  const [savedImageQualityPriceDraft, setSavedImageQualityPriceDraft] =
    useState<ImageQualityPriceDraft>(createEmptyImageQualityPriceDraft)
  const [isSavingImageQualityPricing, setIsSavingImageQualityPricing] =
    useState(false)
  const [videoResolutionEnabled, setVideoResolutionEnabled] = useState<
    Record<VideoResolutionKey, boolean>
  >(() => createEmptyVideoResolutionEnabled())
  const [ordinaryPricingModel, setOrdinaryPricingModel] = useState<
    string | null
  >(null)
  const autoSwitchedForRef = useRef<string | null>(null)
  const isEditMode = !!editData
  const hasLegacyPricing =
    editData &&
    [
      editData.price,
      editData.ratio,
      editData.completionRatio,
      editData.cacheRatio,
      editData.createCacheRatio,
      editData.imageRatio,
      editData.audioRatio,
      editData.audioCompletionRatio,
    ].some(hasValue)
  let initialPricingMode: PricingMode = 'tiered_expr'
  if (
    editData?.billingMode === 'image_quality' ||
    editData?.billingMode === 'video_per_second'
  ) {
    initialPricingMode = editData.billingMode
  } else if (editData?.billingMode !== 'tiered_expr' && hasLegacyPricing) {
    initialPricingMode = hasValue(editData?.price) ? 'per-request' : 'per-token'
  }
  const initialBillingExpr =
    editData?.billingExpr ||
    (initialPricingMode === 'tiered_expr' ? DEFAULT_TOKEN_BILLING_EXPR : '')
  const { models: pricingModels } = usePricingData()

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
  const watchedValues = form.watch()
  let previewRequest = ''
  if (pricingMode === 'per-token' && watchedValues.name.trim()) {
    try {
      previewRequest = JSON.stringify({
        model_name: watchedValues.name.trim(),
        pricing: pricingFromDraft({
          ...watchedValues,
          billingMode: 'per-token',
        }),
      })
    } catch {
      // Incomplete numeric input is validated by the editor before saving.
    }
  }
  const debouncedPreviewRequest = useDebounce(previewRequest, 250)
  const pricePreview = useQuery({
    queryKey: ['model-pricing-preview', debouncedPreviewRequest],
    queryFn: () => previewModelPricing(JSON.parse(debouncedPreviewRequest)),
    enabled:
      Boolean(debouncedPreviewRequest) &&
      debouncedPreviewRequest === previewRequest,
    retry: false,
    refetchOnWindowFocus: false,
    meta: { errorToast: false },
  })
  const effectivePreview =
    previewRequest === debouncedPreviewRequest ? pricePreview.data : undefined
  const usageSchemaByModel = useMemo(
    () =>
      new Map(
        pricingModels.map((model) => [
          model.model_name,
          model.billing_usage_schema,
        ])
      ),
    [pricingModels]
  )
  const usageExamplesByModel = useMemo(
    () =>
      new Map(
        pricingModels.map((model) => [
          model.model_name,
          model.billing_usage_examples,
        ])
      ),
    [pricingModels]
  )
  const taskUsageSchema =
    usageSchema ??
    pluginVariants?.find((variant) => !variant.stale)?.usage_schema ??
    usageSchemaByModel.get(watchedValues.name.trim())
  const taskUsageExamples =
    usageExamplesByModel.get(watchedValues.name.trim()) ??
    pluginVariants?.find((variant) => !variant.stale)?.usage_examples
  const usesTaskPricing =
    ordinaryPricingModel !== watchedValues.name &&
    usesTaskModelPricing(editData, taskUsageSchema, pluginVariants)
  const defaultTaskBillingExpr = useMemo(
    () =>
      taskUsageSchema
        ? generateTaskExprFromConfig(
            createDefaultTaskVisualConfig(taskUsageSchema),
            taskUsageSchema
          )
        : '',
    [taskUsageSchema]
  )
  const resolvedBillingExpr =
    usesTaskPricing &&
    taskUsageSchema &&
    (!billingExpr || billingExpr === DEFAULT_TOKEN_BILLING_EXPR)
      ? defaultTaskBillingExpr
      : billingExpr

  useEffect(() => {
    const modelName = editData?.name?.trim()
    if (!modelName) {
      setImageQualityPriceDraft(createEmptyImageQualityPriceDraft())
      setSavedImageQualityPriceDraft(createEmptyImageQualityPriceDraft())
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
        const draft = imageQualityItemsToDraft(items)
        setImageQualityPriceDraft(draft)
        setSavedImageQualityPriceDraft(draft)
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
  }, [editData?.name, t])

  useEffect(() => {
    conversionGeneration.current += 1
    setConversionReason('')
    setPluginExpressions(editData?.pluginBillingExpr ?? {})
    setOrdinaryPricingModel(null)
    setWasConverted(false)
    setConversionPreview(null)
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
      setPricingMode(initialPricingMode)
      setBillingExpr(initialBillingExpr)
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
      setPricingMode('tiered_expr')
      setBillingExpr(DEFAULT_TOKEN_BILLING_EXPR)
      setRequestRuleExpr('')
    }

    setPromptPrice(nextLaneState.promptPrice)
    setLanePrices(nextLaneState.prices)
    setLaneEnabled(nextLaneState.enabled)
    setVideoResolutionEnabled(nextVideoResolutionEnabled)
    setEditorReloadToken((token) => token + 1)
    autoSwitchedForRef.current = null
  }, [editData, form, initialPricingMode, initialBillingExpr])

  useEffect(() => {
    if (!editData) return
    if (
      ['tiered_expr', 'image_quality', 'video_per_second'].includes(
        editData.billingMode || ''
      )
    ) {
      return
    }
    if (editData.price || editData.ratio) return

    const schema = taskUsageSchema
    if (!schema || Object.keys(schema).length === 0) return
    if (autoSwitchedForRef.current === editData.name) return

    setPricingMode('tiered_expr')
    autoSwitchedForRef.current = editData.name
  }, [editData, taskUsageSchema])

  useEffect(() => {
    onDirtyChange?.(
      form.formState.isDirty ||
        (pricingMode === 'image_quality' &&
          JSON.stringify(imageQualityPriceDraft) !==
            JSON.stringify(savedImageQualityPriceDraft)) ||
        pricingMode !== initialPricingMode ||
        billingExpr !== initialBillingExpr ||
        requestRuleExpr !== (editData?.requestRuleExpr ?? '') ||
        !pluginExpressionsEqual(pluginExpressions, editData?.pluginBillingExpr)
    )
  }, [
    onDirtyChange,
    imageQualityPriceDraft,
    savedImageQualityPriceDraft,
    form.formState.isDirty,
    pricingMode,
    billingExpr,
    requestRuleExpr,
    editData,
    initialPricingMode,
    initialBillingExpr,
    pluginExpressions,
  ])

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
    setPromptPrice(value)
    syncLaneRatios(value, lanePrices, laneEnabled)
  }

  const handleLanePriceChange = (lane: LaneKey, value: string) => {
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
    conversionGeneration.current += 1
    setConversionReason('')
    const nextMode = value as PricingMode
    setPricingMode(nextMode)
    if (nextMode === 'tiered_expr' && !billingExpr) {
      setBillingExpr(defaultTaskBillingExpr || DEFAULT_TOKEN_BILLING_EXPR)
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

  const saveImageQualityPricing = useCallback(async () => {
    const modelName = form.getValues('name').trim()
    if (!modelName) {
      toast.error(t('Model name is required'))
      return false
    }

    const prices: ImageQualityPriceItem[] = []
    for (const quality of IMAGE_QUALITY_LEVELS) {
      for (const resolution of IMAGE_RESOLUTION_LEVELS) {
        const raw = imageQualityPriceDraft[quality][resolution]
        const price = Number(raw)
        if (raw === '' || !Number.isFinite(price) || price < 0) {
          toast.error(t('请完整填写所有品质和分辨率的价格'))
          return false
        }
        prices.push({ quality, resolution, price, enabled: true })
      }
    }

    setIsSavingImageQualityPricing(true)
    try {
      const res = await api.post('/api/models/image-quality-prices', {
        model_name: modelName,
        enabled: true,
        prices,
      })
      requireServerSuccess(res.data)
      setSavedImageQualityPriceDraft(imageQualityPriceDraft)
      return true
    } catch (error) {
      handleServerError(error, t('保存生图品质定价失败'))
      return false
    } finally {
      setIsSavingImageQualityPricing(false)
    }
  }, [form, imageQualityPriceDraft, t])

  const previewRows = useMemo(() => {
    let previewLanes = {
      promptPrice,
      prices: lanePrices,
      enabled: laneEnabled,
    }
    if (pricingMode === 'per-token' && effectivePreview) {
      previewLanes = createInitialLaneState(
        pricingRow(watchedValues.name, effectivePreview.effective)
      )
    }
    return buildPreviewRows(
      watchedValues,
      pricingMode,
      resolvedBillingExpr,
      requestRuleExpr,
      previewLanes.promptPrice,
      previewLanes.prices,
      previewLanes.enabled,
      t,
      currency,
      effectivePreview?.cacheWriteMode,
      effectivePreview?.billingDetails
    )
  }, [
    resolvedBillingExpr,
    laneEnabled,
    lanePrices,
    pricingMode,
    promptPrice,
    requestRuleExpr,
    t,
    watchedValues,
    currency,
    effectivePreview,
  ])

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
      ((toNumberOrNull(promptPrice) === 0 &&
        laneConfigs.some(
          ({ key }) =>
            laneEnabled[key] && (toNumberOrNull(lanePrices[key]) ?? 0) > 0
        )) ||
        (toNumberOrNull(lanePrices.audioInput) === 0 &&
          laneEnabled.audioOutput &&
          (toNumberOrNull(lanePrices.audioOutput) ?? 0) > 0))
    ) {
      form.setError('ratio', {
        message: t(
          'Use expression pricing when a dependent price is non-zero and its base price is zero.'
        ),
      })
      return false
    }
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
        ...(editData?.pluginBillingExpr ||
        pluginVariants?.length ||
        Object.keys(pluginExpressions).length
          ? { pluginBillingExpr: pluginExpressions }
          : {}),
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
        data.billingExpr = resolvedBillingExpr
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
    [
      pricingMode,
      requestRuleExpr,
      resolvedBillingExpr,
      pluginExpressions,
      editData,
      pluginVariants,
      videoResolutionEnabled,
    ]
  )

  const convertPricing = async () => {
    if (
      conversion.isPending ||
      conversionPreview ||
      billingExpr.trim() ||
      pricingMode === 'tiered_expr'
    ) {
      return
    }
    if (!(await form.trigger()) || !validatePricingValues()) return
    const draft = buildSubmitData(form.getValues())
    const generation = ++conversionGeneration.current
    setConversionReason('')
    try {
      const pricing = pricingFromDraft(draft)
      const result = await conversion.mutateAsync({
        model_name: draft.name,
        pricing,
      })
      if (generation !== conversionGeneration.current) return
      const current = buildSubmitData(form.getValues())
      if (
        current.name !== draft.name ||
        JSON.stringify(pricingFromDraft(current)) !== JSON.stringify(pricing)
      ) {
        setConversionReason(
          'Prices changed while preparing the conversion. Try again.'
        )
        return
      }
      if (result.unsupported_reason) {
        setConversionReason(result.unsupported_reason)
        return
      }
      if (!result.expression || !result.effective) {
        throw new Error(t('Failed to prepare pricing conversion'))
      }
      setConversionPreview({
        modelName: draft.name,
        effective: result.effective,
        expression: result.expression,
        draftFingerprint: JSON.stringify(draft),
        cacheWriteMode: result.cache_write_mode,
        billingDetails: result.billing_details,
      })
    } catch (error) {
      handleServerError(error, t('Failed to prepare pricing conversion'))
    }
  }

  const applyConversion = () => {
    if (!conversionPreview) return
    setConversionPreview(null)
    if (
      billingExpr.trim() ||
      pricingMode === 'tiered_expr' ||
      JSON.stringify(buildSubmitData(form.getValues())) !==
        conversionPreview.draftFingerprint
    ) {
      setConversionReason(
        'Prices changed while preparing the conversion. Try again.'
      )
      return
    }
    setBillingExpr(conversionPreview.expression)
    setWasConverted(true)
    setPricingMode('tiered_expr')
    setEditorReloadToken((token) => token + 1)
  }

  useImperativeHandle(
    ref,
    () => ({
      commitDraft: async () => {
        if (
          formElementRef.current?.querySelector('[data-billing-invalid="true"]')
        ) {
          return null
        }
        const amounts =
          formElementRef.current?.querySelectorAll<HTMLInputElement>(
            'input[data-pricing-amount]'
          )
        if (amounts && [...amounts].some((input) => !input.reportValidity())) {
          return null
        }
        const isValid = await form.trigger()
        if (!isValid || !validatePricingValues()) return null
        // Commit the separate quality matrix before enabling its model billing
        // mode in the official versioned pricing API.
        if (
          pricingMode === 'image_quality' &&
          !(await saveImageQualityPricing())
        ) {
          return null
        }
        return buildSubmitData(form.getValues())
      },
    }),
    [
      form,
      validatePricingValues,
      buildSubmitData,
      pricingMode,
      saveImageQualityPricing,
    ]
  )

  const configureOrdinaryPricing = () => {
    const inheritedExpression = combineBillingExpr(
      resolvedBillingExpr,
      requestRuleExpr
    )
    setPluginExpressions((current) => {
      const next = { ...current }
      for (const variant of pluginVariants ?? []) {
        if (!variant.stale && !Object.hasOwn(next, variant.plugin_key)) {
          next[variant.plugin_key] = inheritedExpression
        }
      }
      return next
    })
    setOrdinaryPricingModel(watchedValues.name)
    setPricingMode('tiered_expr')
    setBillingExpr(
      taskUsageSchema?.image_count?.unit === 'count'
        ? 'tier("image", fixed(0)) * image_count'
        : DEFAULT_TOKEN_BILLING_EXPR
    )
    setRequestRuleExpr('')
    setEditorReloadToken((token) => token + 1)
  }

  const expressionEditor =
    usesTaskPricing && taskUsageSchema ? (
      <TaskUsagePricingEditor
        currency={currency}
        key={`${editorReloadToken}:${watchedValues.name}`}
        billingExpr={resolvedBillingExpr}
        requestRuleExpr={requestRuleExpr}
        usageSchema={taskUsageSchema}
        usageExamples={taskUsageExamples}
        onBillingExprChange={setBillingExpr}
        onRequestRuleExprChange={setRequestRuleExpr}
      />
    ) : (
      <TieredPricingEditor
        currency={currency}
        key={editorReloadToken}
        modelName={watchedValues.name}
        billingExpr={billingExpr}
        requestRuleExpr={requestRuleExpr}
        onBillingExprChange={setBillingExpr}
        onRequestRuleExprChange={setRequestRuleExpr}
      />
    )

  const showActions = Boolean(onSave)

  return (
    <div
      className={cn(
        'bg-background flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-xl border',
        className
      )}
    >
      {!embedded && (
        <div className='border-b p-4'>
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <div className='min-w-0'>
              <h3 className='truncate text-base font-medium'>
                {isEditMode ? t('Edit model pricing') : t('Add model pricing')}
              </h3>
            </div>
          </div>
        </div>
      )}

      <Form {...form}>
        <form
          ref={formElementRef}
          onSubmit={(event) => event.preventDefault()}
          className='flex min-h-0 flex-1 flex-col'
          autoComplete='off'
        >
          <div
            role='region'
            aria-label={
              isEditMode ? t('Edit model pricing') : t('Add model pricing')
            }
            className='@container/pricing-editor min-h-0 flex-1 overflow-y-auto overscroll-contain p-4 pb-6'
          >
            {scrollHeader && (
              <div className='mb-4 space-y-3'>{scrollHeader}</div>
            )}
            <div className='grid min-w-0 items-start gap-4 @min-[960px]/pricing-editor:grid-cols-[minmax(0,1fr)_260px]'>
              <FieldGroup className='min-w-0'>
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

                {!embedded && (
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
                )}

                <PricingCurrencySelector siteCurrency={siteCurrency} />

                <TaskPluginPricingEditor
                  key={`${editorReloadToken}:${watchedValues.name}`}
                  variants={pluginVariants ?? []}
                  expressions={pluginExpressions}
                  onChange={setPluginExpressions}
                  modelExpression={combineBillingExpr(
                    resolvedBillingExpr,
                    requestRuleExpr
                  )}
                  modelBillingMode={
                    pricingMode === 'tiered_expr' ? 'tiered_expr' : 'ratio'
                  }
                  currency={currency}
                  ordinaryPricing={!usesTaskPricing}
                  onConfigureOrdinaryPricing={configureOrdinaryPricing}
                >
                  <Tabs
                    key={editorReloadToken}
                    value={pricingMode}
                    onValueChange={handleModeChange}
                    className='gap-4'
                  >
                    <TabsList className='w-full flex-wrap group-data-horizontal/tabs:h-auto'>
                      <TabsTrigger value='tiered_expr'>
                        {t('Expression')}
                      </TabsTrigger>
                      <TabsTrigger value='per-token'>
                        {t('Per-token (deprecated)')}
                      </TabsTrigger>
                      <TabsTrigger value='per-request'>
                        {t('Per-request (deprecated)')}
                      </TabsTrigger>
                      <TabsTrigger value='video_per_second'>
                        {t('按秒')}
                      </TabsTrigger>
                      <TabsTrigger value='image_quality'>
                        {t('按品质')}
                      </TabsTrigger>
                    </TabsList>

                    {(pricingMode === 'per-token' ||
                      pricingMode === 'per-request') && (
                      <Alert className='border-amber-500/40 bg-amber-500/10 p-4 text-amber-900 dark:text-amber-100'>
                        <AlertTriangle aria-hidden='true' className='size-5' />
                        <AlertDescription className='space-y-3 text-sm text-inherit'>
                          <p className='font-medium'>
                            {t(
                              'Legacy pricing is deprecated. Convert the current prices to an expression draft, then save to apply it.'
                            )}
                          </p>
                          <Button
                            type='button'
                            className='w-full sm:w-auto'
                            disabled={
                              conversion.isPending ||
                              Boolean(billingExpr.trim())
                            }
                            onClick={() => void convertPricing()}
                          >
                            {conversion.isPending
                              ? t('Preparing conversion...')
                              : t('Convert to expression')}
                          </Button>
                          {billingExpr.trim() && (
                            <p>
                              {t(
                                'An expression draft already exists. Open the Expression tab to keep editing it.'
                              )}
                            </p>
                          )}
                          {conversionReason && (
                            <p role='status'>{t(conversionReason)}</p>
                          )}
                        </AlertDescription>
                      </Alert>
                    )}
                    {(pricingMode === 'per-token' ||
                      pricingMode === 'per-request' ||
                      wasConverted) && (
                      <p className='text-muted-foreground text-xs'>
                        {t(
                          'After conversion, expression reservation and rounding rules apply. Effective unit prices are preserved; individual rounded charges may differ.'
                        )}
                      </p>
                    )}

                    <TabsContent
                      value='per-token'
                      className='@container/pricing-fields min-w-0 pt-0'
                    >
                      {taskUsageSchema &&
                        Object.keys(taskUsageSchema).length > 0 && (
                          <Alert className='mb-4'>
                            <AlertDescription className='flex flex-col gap-3 text-xs'>
                              <p>
                                {t(
                                  'This is a task model billed by usage (e.g. seconds, resolution). Prices entered here act as a per-call base rate, not per-token prices.'
                                )}
                              </p>
                              <p>
                                {t(
                                  'Tip: after configuring one model, select others in the table and use bulk copy.'
                                )}
                              </p>
                              <Button
                                type='button'
                                variant='outline'
                                size='sm'
                                className='w-fit'
                                onClick={() => handleModeChange('tiered_expr')}
                              >
                                {t('Configure task pricing')}
                              </Button>
                            </AlertDescription>
                          </Alert>
                        )}
                      <p className='text-muted-foreground mb-3 text-xs'>
                        {embedded && (
                          <>
                            {t('{{currency}} price per 1M tokens.', {
                              currency: currency.label,
                            })}{' '}
                          </>
                        )}
                        {t(
                          'Unset legacy prices use the billing engine defaults.'
                        )}
                      </p>
                      <div
                        className={cn(
                          'grid min-w-0 gap-3',
                          embedded && '@min-[560px]/pricing-fields:grid-cols-2'
                        )}
                      >
                        <Field
                          className={cn(
                            'min-w-0',
                            embedded && 'rounded-lg border p-3'
                          )}
                        >
                          <FieldLabel htmlFor={promptPriceId}>
                            {t('Input price')}
                          </FieldLabel>
                          <FieldDescription
                            id={`${promptPriceId}-description`}
                            className={embedded ? 'text-xs' : undefined}
                          >
                            {t('{{currency}} price per 1M input tokens.', {
                              currency: currency.label,
                            })}
                          </FieldDescription>
                          <PriceInput
                            currency={currency}
                            id={promptPriceId}
                            aria-describedby={`${promptPriceId}-description`}
                            value={promptPrice}
                            placeholder='3'
                            onChange={handlePromptPriceChange}
                          />
                        </Field>

                        {laneConfigs.map((lane) => {
                          const disabled =
                            lane.key === 'audioOutput' &&
                            (!laneEnabled.audioInput ||
                              !hasValue(lanePrices.audioInput))
                          return (
                            <PriceLane
                              currency={currency}
                              key={lane.key}
                              compact={embedded}
                              title={t(lane.titleKey)}
                              description={t(lane.descriptionKey)}
                              placeholder={lane.placeholder}
                              value={lanePrices[lane.key]}
                              enabled={laneEnabled[lane.key]}
                              disabled={disabled}
                              disabledReason={
                                disabled
                                  ? t(
                                      'Audio output price requires an audio input price.'
                                    )
                                  : undefined
                              }
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
                    </TabsContent>

                    <TabsContent value='per-request' className='pt-0'>
                      <FieldGroup className='gap-5'>
                        <FormField
                          control={form.control}
                          name='price'
                          render={({ field }) => (
                            <FormItem className='contents'>
                              <Field>
                                <FormLabel>{t('Fixed price')}</FormLabel>
                                <InputGroup className='has-[[data-pricing-error]]:h-auto has-[[data-pricing-error]]:flex-wrap'>
                                  <InputGroupAddon>
                                    {currency.symbol}
                                  </InputGroupAddon>
                                  <FormControl>
                                    <PricingAmountInput
                                      {...field}
                                      value={field.value ?? ''}
                                      currency={currency}
                                      grouped
                                      placeholder='0.01'
                                      onChange={field.onChange}
                                    />
                                  </FormControl>
                                  <InputGroupAddon align='inline-end'>
                                    {t('per request')}
                                  </InputGroupAddon>
                                </InputGroup>
                                <FormDescription>
                                  {t(
                                    'Cost in {{currency}} per request, regardless of tokens used.',
                                    { currency: currency.label }
                                  )}
                                </FormDescription>
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
                                    <InputGroupAddon>
                                      {currency.symbol}
                                    </InputGroupAddon>
                                    <PricingAmountInput
                                      currency={currency}
                                      grouped
                                      {...field}
                                      inputMode='decimal'
                                      value={
                                        videoResolutionEnabled['480p']
                                          ? (field.value ?? '')
                                          : ''
                                      }
                                      placeholder={
                                        videoResolutionEnabled['480p']
                                          ? '0.03'
                                          : t('Disabled')
                                      }
                                      disabled={!videoResolutionEnabled['480p']}
                                      onChange={field.onChange}
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
                                    <InputGroupAddon>
                                      {currency.symbol}
                                    </InputGroupAddon>
                                    <PricingAmountInput
                                      currency={currency}
                                      grouped
                                      {...field}
                                      inputMode='decimal'
                                      value={
                                        videoResolutionEnabled['720p']
                                          ? (field.value ?? '')
                                          : ''
                                      }
                                      placeholder={
                                        videoResolutionEnabled['720p']
                                          ? '0.05'
                                          : t('Disabled')
                                      }
                                      disabled={!videoResolutionEnabled['720p']}
                                      onChange={field.onChange}
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
                                    <InputGroupAddon>
                                      {currency.symbol}
                                    </InputGroupAddon>
                                    <PricingAmountInput
                                      currency={currency}
                                      grouped
                                      {...field}
                                      inputMode='decimal'
                                      value={
                                        videoResolutionEnabled['1080p']
                                          ? (field.value ?? '')
                                          : ''
                                      }
                                      placeholder={
                                        videoResolutionEnabled['1080p']
                                          ? '0.08'
                                          : t('Disabled')
                                      }
                                      disabled={
                                        !videoResolutionEnabled['1080p']
                                      }
                                      onChange={field.onChange}
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
                                    <InputGroupAddon>
                                      {currency.symbol}
                                    </InputGroupAddon>
                                    <PricingAmountInput
                                      currency={currency}
                                      grouped
                                      {...field}
                                      inputMode='decimal'
                                      value={
                                        videoResolutionEnabled['4k']
                                          ? (field.value ?? '')
                                          : ''
                                      }
                                      placeholder={
                                        videoResolutionEnabled['4k']
                                          ? '0.00'
                                          : t('Disabled')
                                      }
                                      disabled={!videoResolutionEnabled['4k']}
                                      onChange={field.onChange}
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

                    <TabsContent value='image_quality' className='pt-0'>
                      <div className='rounded-lg border p-3'>
                        <div className='flex items-start justify-between gap-3'>
                          <div className='space-y-1'>
                            <div className='text-sm font-medium'>
                              {t('生图品质定价')}
                            </div>
                            <p className='text-muted-foreground text-xs'>
                              {t(
                                '开启后，该生图模型会按照请求中的 quality 和 size 匹配价格计费。'
                              )}
                            </p>
                          </div>
                        </div>

                        <div className='mt-4 space-y-4'>
                          {IMAGE_QUALITY_LEVELS.map((quality) => (
                            <div key={quality} className='space-y-2'>
                              <div className='text-sm font-medium'>
                                {quality}
                              </div>
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
                                      <InputGroupAddon>
                                        {currency.symbol}
                                      </InputGroupAddon>
                                      <PricingAmountInput
                                        currency={currency}
                                        grouped
                                        inputMode='decimal'
                                        aria-label={`${quality} ${resolution}`}
                                        placeholder='0.00'
                                        value={
                                          imageQualityPriceDraft[quality][
                                            resolution
                                          ]
                                        }
                                        onChange={(value) =>
                                          handleImageQualityPriceChange(
                                            quality,
                                            resolution,
                                            value
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
                            {t(
                              '最终费用 = 对应品质分辨率单价 × 图片数量 n。用户专属品质密价优先级高于这里的公开价格。'
                            )}
                          </p>
                        </div>

                        <div className='mt-4 flex justify-end'>
                          <Button
                            type='button'
                            variant='outline'
                            onClick={() => {
                              if (onSave) void onSave()
                              else void saveImageQualityPricing()
                            }}
                            disabled={
                              isSavingImageQualityPricing ||
                              isSaving ||
                              !form.watch('name')
                            }
                          >
                            {isSavingImageQualityPricing
                              ? t('Saving...')
                              : t('保存生图品质定价')}
                          </Button>
                        </div>
                      </div>
                    </TabsContent>
                    <TabsContent value='tiered_expr' className='pt-0'>
                      <FieldGroup className='gap-5'>
                        {expressionEditor}
                      </FieldGroup>
                    </TabsContent>
                  </Tabs>
                </TaskPluginPricingEditor>
              </FieldGroup>

              <aside
                aria-label={t('Preview')}
                className='bg-muted/20 min-w-0 rounded-lg border @min-[960px]/pricing-editor:sticky @min-[960px]/pricing-editor:top-0'
              >
                <div className='border-b px-3 py-2'>
                  <div className='text-sm font-medium'>
                    {!usesTaskPricing && pluginVariants?.length ? (
                      <>{t('Ordinary OpenAI')} · </>
                    ) : null}
                    {t('Preview')}
                  </div>
                </div>
                <div className='divide-y'>
                  {pricingMode === 'per-token' && !effectivePreview && (
                    <p
                      className='text-muted-foreground px-3 py-2 text-xs'
                      role='status'
                    >
                      {pricePreview.isError
                        ? t('Failed to load model pricing')
                        : t('Loading...')}
                    </p>
                  )}
                  {(pricingMode !== 'per-token' || effectivePreview) &&
                    previewRows.map((row) => (
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
                    disabled={isSaving || isSavingImageQualityPricing}
                    className='w-full sm:w-auto'
                  >
                    <Save data-icon='inline-start' />
                    {isSaving || isSavingImageQualityPricing
                      ? t('Saving...')
                      : t('Save model prices')}
                  </Button>
                )}
              </div>
            </div>
          )}
        </form>
      </Form>
      {conversionPreview && (
        <PricingConversionDialog
          preview={conversionPreview}
          currency={currency}
          onCancel={() => {
            conversionGeneration.current += 1
            setConversionPreview(null)
            setConversionReason('')
          }}
          onConfirm={applyConversion}
        />
      )}
    </div>
  )
})
