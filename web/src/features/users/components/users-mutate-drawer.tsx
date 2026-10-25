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
import { Pencil, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  EMPTY_PERMISSION_CATALOG,
  hasPermission,
  normalizeAdminPermissions,
} from '@/lib/admin-permissions'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota, parseQuotaFromDollars } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  createUser,
  updateUser,
  getUser,
  getGroups,
  getPermissionCatalog,
  getUserModelPrices,
  upsertUserModelPrice,
  deleteUserModelPrice,
  getPerCallPricedModels,
  getUserModelImageQualityPrices,
  saveUserModelImageQualityPrices,
  deleteUserModelImageQualityPrices,
} from '../api'
import { BINDING_FIELDS, ERROR_MESSAGES, SUCCESS_MESSAGES } from '../constants'
import {
  userFormSchema,
  type UserFormValues,
  USER_FORM_DEFAULT_VALUES,
  transformFormDataToPayload,
  transformUserToFormDefaults,
} from '../lib'
import { type User } from '../types'
import { UserQuotaDialog } from './user-quota-dialog'
import { useUsers } from './users-provider'

type UsersMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: User
}

const IMAGE_QUALITY_LEVELS = ['low', 'medium', 'high'] as const
const IMAGE_RESOLUTION_LEVELS = ['1k', '2k', '4k'] as const

type ImageQualityLevel = (typeof IMAGE_QUALITY_LEVELS)[number]
type ImageResolutionLevel = (typeof IMAGE_RESOLUTION_LEVELS)[number]
type ImageQualityPriceDraft = Record<ImageQualityLevel, Record<ImageResolutionLevel, string>>

function createEmptyImageQualityPriceDraft(): ImageQualityPriceDraft {
  return {
    low: { '1k': '', '2k': '', '4k': '' },
    medium: { '1k': '', '2k': '', '4k': '' },
    high: { '1k': '', '2k': '', '4k': '' },
  }
}


export function UsersMutateDrawer({
  open,
  onOpenChange,
  currentRow,
}: UsersMutateDrawerProps) {
  const { t } = useTranslation()
  const isUpdate = !!currentRow
  const { triggerRefresh } = useUsers()
  const currentUser = useAuthStore((s) => s.auth.user)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [quotaDialogOpen, setQuotaDialogOpen] = useState(false)
  const [privateModelName, setPrivateModelName] = useState('')
  const [privateModelPrice, setPrivateModelPrice] = useState('')
  const [imageQualitySheetOpen, setImageQualitySheetOpen] = useState(false)
  const [imageQualityModelName, setImageQualityModelName] = useState('')
  const [imageQualityPriceDraft, setImageQualityPriceDraft] = useState<ImageQualityPriceDraft>(() =>
    createEmptyImageQualityPriceDraft()
  )
  const [isSavingImageQualityPrice, setIsSavingImageQualityPrice] = useState(false)

  // Fetch groups
  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: getGroups,
    staleTime: 5 * 60 * 1000,
  })

  const groups = groupsData?.data || []

  // Permission catalog is owned by the backend; fetched once and reused.
  const { data: permissionCatalog = EMPTY_PERMISSION_CATALOG } = useQuery({
    queryKey: ['admin-permission-catalog'],
    queryFn: getPermissionCatalog,
    staleTime: 5 * 60 * 1000,
  })

  const {
    data: privatePricesData,
    refetch: refetchPrivatePrices,
  } = useQuery({
    queryKey: ['user-model-prices', currentRow?.id],
    queryFn: () => getUserModelPrices(currentRow!.id),
    enabled: open && isUpdate && !!currentRow,
    staleTime: 0,
  })

  const { data: perCallModelsData } = useQuery({
    queryKey: ['per-call-priced-models'],
    queryFn: getPerCallPricedModels,
    enabled: open && isUpdate,
    staleTime: 5 * 60 * 1000,
  })

  const privatePrices = privatePricesData?.data || []
  const perCallModels = perCallModelsData?.data || []

  const form = useForm<UserFormValues>({
    resolver: zodResolver(userFormSchema),
    defaultValues: USER_FORM_DEFAULT_VALUES,
  })

  // Load existing data when updating
  useEffect(() => {
    if (open && isUpdate && currentRow) {
      // For update, fetch fresh data
      getUser(currentRow.id).then((result) => {
        if (result.success && result.data) {
          form.reset(transformUserToFormDefaults(result.data))
        }
      })
    } else if (open && !isUpdate) {
      // For create, reset to defaults
      form.reset(USER_FORM_DEFAULT_VALUES)
    }
  }, [open, isUpdate, currentRow, form])

  const { meta: currencyMeta } = getCurrencyDisplay()
  const currencyLabel = getCurrencyLabel()
  const tokensOnly = currencyMeta.kind === 'tokens'

  const currentQuotaRaw = form.watch('quota_dollars') || 0
  const selectedRole = form.watch('role')
  const canEditAdminPermissions = currentUser?.role === ROLE.SUPER_ADMIN
  const targetIsAdmin = (selectedRole ?? currentRow?.role ?? 0) >= ROLE.ADMIN

  const onSubmit = async (data: UserFormValues) => {
    if (!isUpdate) {
      const passwordLength = data.password?.length || 0
      if (passwordLength < 8 || passwordLength > 20) {
        form.setError('password', {
          type: 'manual',
          message: t('Password must be between 8 and 20 characters'),
        })
        return
      }
    }

    setIsSubmitting(true)
    try {
      const payload = transformFormDataToPayload(
        data,
        currentRow?.id,
        permissionCatalog
      )
      const result = isUpdate
        ? await updateUser(payload as typeof payload & { id: number })
        : await createUser(payload)

      if (result.success) {
        toast.success(
          isUpdate
            ? t(SUCCESS_MESSAGES.USER_UPDATED)
            : t(SUCCESS_MESSAGES.USER_CREATED)
        )
        onOpenChange(false)
        triggerRefresh()
      } else {
        toast.error(
          result.message ||
            (isUpdate
              ? t(ERROR_MESSAGES.UPDATE_FAILED)
              : t(ERROR_MESSAGES.CREATE_FAILED))
        )
      }
    } catch (_error) {
      toast.error(t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setIsSubmitting(false)
    }
  }

  const refreshUserData = async () => {
    if (!currentRow) return
    const result = await getUser(currentRow.id)
    if (result.success && result.data) {
      form.reset(transformUserToFormDefaults(result.data))
    }
    triggerRefresh()
  }

  const handleSavePrivatePrice = async () => {
    if (!currentRow) return
    const price = Number(privateModelPrice)

    if (!privateModelName) {
      toast.error(t('请选择模型'))
      return
    }

    if (!Number.isFinite(price) || price < 0) {
      toast.error(t('请输入正确的秘价'))
      return
    }

    const result = await upsertUserModelPrice(currentRow.id, {
      model_name: privateModelName,
      price,
      enabled: true,
    })

    if (result.success) {
      toast.success(t('模型秘价已保存'))
      setPrivateModelName('')
      setPrivateModelPrice('')
      refetchPrivatePrices()
    } else {
      toast.error(result.message || t('保存模型秘价失败'))
    }
  }

  const handleDeletePrivatePrice = async (modelName: string) => {
    if (!currentRow) return

    const result = await deleteUserModelPrice(currentRow.id, modelName)
    if (result.success) {
      toast.success(t('模型秘价已删除'))
      refetchPrivatePrices()
    } else {
      toast.error(result.message || t('删除模型秘价失败'))
    }
  }

  const openImageQualityPriceSheet = async (modelName: string) => {
    if (!currentRow || !modelName) return
    setImageQualityModelName(modelName)
    const draft = createEmptyImageQualityPriceDraft()

    const result = await getUserModelImageQualityPrices(currentRow.id, modelName)
    if (result.success && result.data) {
      for (const item of result.data) {
        if (
          IMAGE_QUALITY_LEVELS.includes(item.quality) &&
          IMAGE_RESOLUTION_LEVELS.includes(item.resolution)
        ) {
          draft[item.quality][item.resolution] = String(item.price)
        }
      }
    }

    setImageQualityPriceDraft(draft)
    setImageQualitySheetOpen(true)
  }

  const handleImageQualityDraftChange = (
    quality: ImageQualityLevel,
    resolution: ImageResolutionLevel,
    value: string
  ) => {
    if (!/^(\d+(\.\d*)?|\.\d*)?$/.test(value)) return
    setImageQualityPriceDraft((prev) => ({
      ...prev,
      [quality]: {
        ...prev[quality],
        [resolution]: value,
      },
    }))
  }

  const handleSaveImageQualityPrivatePrice = async () => {
    if (!currentRow || !imageQualityModelName) return

    const prices = []
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

    setIsSavingImageQualityPrice(true)
    try {
      const result = await saveUserModelImageQualityPrices(currentRow.id, {
        model_name: imageQualityModelName,
        enabled: true,
        prices,
      })
      if (result.success) {
        toast.success(t('用户品质密价已保存'))
        setImageQualitySheetOpen(false)
      } else {
        toast.error(result.message || t('保存用户品质密价失败'))
      }
    } finally {
      setIsSavingImageQualityPrice(false)
    }
  }

  const handleDeleteImageQualityPrivatePrice = async () => {
    if (!currentRow || !imageQualityModelName) return
    const result = await deleteUserModelImageQualityPrices(
      currentRow.id,
      imageQualityModelName
    )
    if (result.success) {
      toast.success(t('用户品质密价已删除'))
      setImageQualityPriceDraft(createEmptyImageQualityPriceDraft())
      setImageQualitySheetOpen(false)
    } else {
      toast.error(result.message || t('删除用户品质密价失败'))
    }
  }

  return (
    <>
      <Sheet
        open={open}
        onOpenChange={(v) => {
          onOpenChange(v)
          if (!v) {
            form.reset()
          }
        }}
      >
        <SheetContent
          className={sideDrawerContentClassName('sm:max-w-[600px]')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>
              {isUpdate ? t('Update') : t('Create')} {t('User')}
            </SheetTitle>
            <SheetDescription>
              {isUpdate
                ? t('Update the user by providing necessary info.')
                : t('Add a new user by providing necessary info.')}
            </SheetDescription>
          </SheetHeader>
          <Form {...form}>
            <form
              id='user-form'
              onSubmit={form.handleSubmit(onSubmit)}
              className={sideDrawerFormClassName()}
            >
              {/* Basic Information */}
              <SideDrawerSection>
                <h3 className='text-sm font-medium'>
                  {t('Basic Information')}
                </h3>

                <FormField
                  control={form.control}
                  name='username'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Username')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          placeholder={t('Enter username')}
                          disabled={isUpdate}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                {!isUpdate && (
                  <FormField
                    control={form.control}
                    name='role'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Role')}</FormLabel>
                        <Select
                          items={[
                            { value: '1', label: t('Common User') },
                            { value: '10', label: t('Admin') },
                          ]}
                          onValueChange={(value) =>
                            value !== null && field.onChange(parseInt(value))
                          }
                          value={String(field.value)}
                        >
                          <FormControl>
                            <SelectTrigger>
                              <SelectValue placeholder={t('Select a role')} />
                            </SelectTrigger>
                          </FormControl>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              <SelectItem value='1'>
                                {t('Common User')}
                              </SelectItem>
                              <SelectItem value='10'>{t('Admin')}</SelectItem>
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                        <FormDescription>
                          {t("Set the user's role (cannot be Root)")}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                )}

                <FormField
                  control={form.control}
                  name='display_name'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Display Name')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          placeholder={t('Enter display name')}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Leave empty to use username')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='password'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Password')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          type='password'
                          placeholder={
                            isUpdate
                              ? t('Leave empty to keep unchanged')
                              : t('Enter password (8-20 characters)')
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </SideDrawerSection>

              {/* Group & Quota Settings (Update only) */}
              {isUpdate && (
                <SideDrawerSection>
                  <h3 className='text-sm font-medium'>{t('Group & Quota')}</h3>

                  <FormField
                    control={form.control}
                    name='group'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Group')}</FormLabel>
                        <Select
                          items={[
                            ...groups.map((group) => ({
                              value: group,
                              label: group,
                            })),
                          ]}
                          onValueChange={field.onChange}
                          value={field.value}
                        >
                          <FormControl>
                            <SelectTrigger>
                              <SelectValue placeholder={t('Select a group')} />
                            </SelectTrigger>
                          </FormControl>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              {groups.map((group) => (
                                <SelectItem key={group} value={group}>
                                  {group}
                                </SelectItem>
                              ))}
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='quota_dollars'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Remaining Quota ({{currency}})', {
                            currency: currencyLabel,
                          })}
                        </FormLabel>
                        <div className='flex gap-2'>
                          <FormControl>
                            <Input
                              value={
                                tokensOnly
                                  ? String(field.value || 0)
                                  : (field.value || 0).toFixed(6)
                              }
                              readOnly
                              className='flex-1'
                            />
                          </FormControl>
                          <Button
                            type='button'
                            variant='outline'
                            onClick={() => setQuotaDialogOpen(true)}
                          >
                            <Pencil className='mr-1 h-4 w-4' />
                            {t('Adjust Quota')}
                          </Button>
                        </div>
                        <FormDescription>
                          {formatQuota(parseQuotaFromDollars(field.value || 0))}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />

                  <FormField
                    control={form.control}
                    name='remark'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Remark')}</FormLabel>
                        <FormControl>
                          <Textarea
                            {...field}
                            placeholder={t(
                              'Admin notes (only visible to admins)'
                            )}
                            rows={3}
                          />
                        </FormControl>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </SideDrawerSection>
              )}

              {canEditAdminPermissions &&
                targetIsAdmin &&
                permissionCatalog.resources.length > 0 && (
                  <SideDrawerSection>
                    <h3 className='text-sm font-medium'>
                      {t('Admin Permissions')}
                    </h3>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Default administrator permissions can be overridden for this user.'
                      )}
                    </p>
                    <FormField
                      control={form.control}
                      name='admin_permissions'
                      render={({ field }) => {
                        const selected = normalizeAdminPermissions(
                          field.value,
                          permissionCatalog
                        )
                        return (
                          <FormItem>
                            <div className='space-y-3'>
                              {permissionCatalog.resources.map((resource) => (
                                <div
                                  key={resource.resource}
                                  className='space-y-2 rounded-md border p-3'
                                >
                                  <div className='text-sm font-medium'>
                                    {t(resource.label_key)}
                                  </div>
                                  <div className='space-y-2'>
                                    {resource.actions.map((option) => (
                                      <label
                                        key={option.action}
                                        className='flex items-start gap-3'
                                      >
                                        <Checkbox
                                          checked={
                                            selected[resource.resource]?.[
                                              option.action
                                            ] === true
                                          }
                                          onCheckedChange={(checked) => {
                                            field.onChange({
                                              ...selected,
                                              [resource.resource]: {
                                                ...selected[resource.resource],
                                                [option.action]:
                                                  checked === true,
                                              },
                                            })
                                          }}
                                        />
                                        <span className='flex flex-col gap-1'>
                                          <span className='text-sm font-medium'>
                                            {t(option.label_key)}
                                          </span>
                                          <span className='text-muted-foreground text-xs'>
                                            {t(option.description_key)}
                                          </span>
                                        </span>
                                      </label>
                                    ))}
                                  </div>
                                </div>
                              ))}
                            </div>
                            <FormMessage />
                          </FormItem>
                        )
                      }}
                    />
                    {currentUser && (
                      <p className='text-muted-foreground text-xs'>
                        {hasPermission(
                          currentUser,
                          ADMIN_PERMISSION_RESOURCES.CHANNEL,
                          ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
                        )
                          ? t(
                              'Your account can edit sensitive channel settings.'
                            )
                          : t(
                              'Your account cannot edit sensitive channel settings.'
                            )}
                      </p>
                    )}
                  </SideDrawerSection>
                )}

                {/* User Private Model Pricing */}
                {isUpdate && currentRow && (
                  <SideDrawerSection>
                    <h3 className='text-sm font-medium'>
                      {t('模型秘价设置')}
                    </h3>
                    <p className='text-muted-foreground text-xs'>
                      {t('仅支持按次/固定价格模型。设置后，该用户调用此模型会优先使用这里的专属价格。')}
                    </p>

                    <div className='space-y-3 rounded-md border p-3'>
                      <div className='grid grid-cols-1 gap-2 sm:grid-cols-[1fr_140px_auto]'>
                        <Select
                          items={perCallModels.map((item) => ({
                            value: item.model_name,
                            label: `${item.model_name} (${item.model_price})`,
                          }))}
                          value={privateModelName}
                          onValueChange={(value) =>
                            value !== null && setPrivateModelName(value)
                          }
                        >
                          <SelectTrigger>
                            <SelectValue placeholder={t('选择按次模型')} />
                          </SelectTrigger>
                          <SelectContent alignItemWithTrigger={false}>
                            <SelectGroup>
                              {perCallModels.map((item) => (
                                <SelectItem
                                  key={item.model_name}
                                  value={item.model_name}
                                >
                                  {item.model_name} / {item.model_price}
                                </SelectItem>
                              ))}
                            </SelectGroup>
                          </SelectContent>
                        </Select>

                        <Input
                          value={privateModelPrice}
                          onChange={(e) => setPrivateModelPrice(e.target.value)}
                          placeholder={t('秘价，例如 0.001')}
                          inputMode='decimal'
                        />

                        <div className='flex gap-2'>
                          <Button
                            type='button'
                            onClick={handleSavePrivatePrice}
                          >
                            {t('添加/更新')}
                          </Button>
                          <Button
                            type='button'
                            variant='outline'
                            disabled={!privateModelName}
                            onClick={() =>
                              openImageQualityPriceSheet(privateModelName)
                            }
                          >
                            {t('配置品质密价')}
                          </Button>
                        </div>
                      </div>

                      <div className='space-y-2'>
                        {privatePrices.length === 0 ? (
                          <p className='text-muted-foreground text-xs'>
                            {t('暂无模型秘价规则')}
                          </p>
                        ) : (
                          privatePrices.map((item) => (
                            <div
                              key={item.id}
                              className='flex items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm'
                            >
                              <div className='min-w-0'>
                                <div className='truncate font-medium'>
                                  {item.model_name}
                                </div>
                                <div className='text-muted-foreground text-xs'>
                                  {t('秘价')}: {item.price} / {item.enabled ? t('启用') : t('关闭')}
                                </div>
                              </div>
                              <div className='flex shrink-0 gap-2'>
                                <Button
                                  type='button'
                                  variant='outline'
                                  onClick={() =>
                                    openImageQualityPriceSheet(item.model_name)
                                  }
                                >
                                  {t('品质密价')}
                                </Button>
                                <Button
                                  type='button'
                                  variant='outline'
                                  onClick={() =>
                                    handleDeletePrivatePrice(item.model_name)
                                  }
                                >
                                  <Trash2 className='mr-1 h-4 w-4' />
                                  {t('删除')}
                                </Button>
                              </div>
                            </div>
                          ))
                        )}
                      </div>
                    </div>
                  </SideDrawerSection>
                )}

              {/* Binding Information (Read-only) */}
              {isUpdate && (
                <SideDrawerSection>
                  <h3 className='text-sm font-medium'>
                    {t('Binding Information')}
                  </h3>
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Third-party account bindings (read-only, managed by user in profile settings)'
                    )}
                  </p>

                  <div className='flex flex-col gap-3'>
                    {BINDING_FIELDS.map(({ key, label }) => (
                      <div key={key}>
                        <Label className='text-muted-foreground text-xs'>
                          {t(label)}
                        </Label>
                        <Input
                          value={
                            (currentRow?.[key as keyof User] as string) || '-'
                          }
                          disabled
                          className='mt-1'
                        />
                      </div>
                    ))}
                  </div>
                </SideDrawerSection>
              )}
            </form>
          </Form>
          <SheetFooter className={sideDrawerFooterClassName()}>
            <SheetClose render={<Button variant='outline' />}>
              {t('Close')}
            </SheetClose>
            <Button form='user-form' type='submit' disabled={isSubmitting}>
              {isSubmitting ? t('Saving...') : t('Save changes')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      {/* Adjust Quota Dialog */}
      {currentRow && (
        <UserQuotaDialog
          open={quotaDialogOpen}
          onOpenChange={setQuotaDialogOpen}
          userId={currentRow.id}
          currentQuota={parseQuotaFromDollars(currentQuotaRaw || 0)}
          onSuccess={refreshUserData}
        />
      )}

      <Sheet open={imageQualitySheetOpen} onOpenChange={setImageQualitySheetOpen}>
        <SheetContent className={sideDrawerContentClassName('sm:max-w-[520px]')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('用户专属生图品质密价')}</SheetTitle>
            <SheetDescription>
              {t('设置后，该用户调用此生图模型时优先按照这里的品质/分辨率价格扣费。')}
            </SheetDescription>
          </SheetHeader>

          <div className={sideDrawerFormClassName()}>
            <SideDrawerSection>
              <div className='space-y-1'>
                <Label>{t('模型')}</Label>
                <Input value={imageQualityModelName || '-'} readOnly />
              </div>
              <p className='text-muted-foreground text-xs'>
                {t('必须完整填写 low / medium / high 下 1K / 2K / 4K 的价格。最终费用 = 对应单价 × 图片数量 n。')}
              </p>

              <div className='space-y-4 rounded-md border p-3'>
                {IMAGE_QUALITY_LEVELS.map((quality) => (
                  <div key={quality} className='space-y-2'>
                    <div className='text-sm font-medium'>{quality}</div>
                    <div className='grid grid-cols-3 gap-2'>
                      {IMAGE_RESOLUTION_LEVELS.map((resolution) => (
                        <div key={`${quality}-${resolution}`} className='space-y-1'>
                          <Label className='text-xs uppercase'>{resolution}</Label>
                          <Input
                            inputMode='decimal'
                            placeholder='0.00'
                            value={imageQualityPriceDraft[quality][resolution]}
                            onChange={(e) =>
                              handleImageQualityDraftChange(
                                quality,
                                resolution,
                                e.target.value
                              )
                            }
                          />
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            </SideDrawerSection>
          </div>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='button'
              variant='outline'
              onClick={handleDeleteImageQualityPrivatePrice}
            >
              {t('删除品质密价')}
            </Button>
            <Button
              type='button'
              onClick={handleSaveImageQualityPrivatePrice}
              disabled={isSavingImageQualityPrice}
            >
              {isSavingImageQualityPrice ? t('Saving...') : t('保存品质密价')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </>
  )
}
