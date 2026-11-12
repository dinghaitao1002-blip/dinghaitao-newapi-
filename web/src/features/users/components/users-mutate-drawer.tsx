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
import { useEffect, useRef, useState } from 'react'
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
import { PermissionMatrix } from '@/components/permission-matrix'
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
  type AdminPermissionMatrix,
} from '@/lib/admin-permissions'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota, parseQuotaFromDollars } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { accountPasswordSchema } from '@/lib/password-policy'
import { ROLE } from '@/lib/roles'
import { AuthOperationError } from '@/lib/secure-verification'
import { requireServerSuccess } from '@/lib/server-error-message'
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
} from '../api'
import { BINDING_FIELDS, ERROR_MESSAGES, SUCCESS_MESSAGES } from '../constants'
import {
  userFormSchema,
  type UserFormValues,
  USER_FORM_DEFAULT_VALUES,
  transformFormDataToPayload,
  transformUserToFormDefaults,
} from '../lib'
import type { User } from '../types'
import { UserQuotaDialog } from './user-quota-dialog'
import { useUsers } from './users-provider'

type UsersMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: User
}

export function UsersMutateDrawer({
  open,
  onOpenChange,
  currentRow,
}: UsersMutateDrawerProps) {
  const { t } = useTranslation()
  const isUpdate = !!currentRow
  const { triggerRefresh, requestVerification } = useUsers()
  const currentUser = useAuthStore((s) => s.auth.user)
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [quotaDialogOpen, setQuotaDialogOpen] = useState(false)
  const [privateModelName, setPrivateModelName] = useState('')
  const [privateModelPrice, setPrivateModelPrice] = useState('')
  const [imagePriceSheetOpen, setImagePriceSheetOpen] = useState(false)
  const [imagePriceModelName, setImagePriceModelName] = useState('')
  const [imageUnitPrice, setImageUnitPrice] = useState('')
  const [hasLegacyTierDifferences, setHasLegacyTierDifferences] =
    useState(false)
  const [isLoadingImagePrice, setIsLoadingImagePrice] = useState(false)
  const [isSavingImagePrice, setIsSavingImagePrice] = useState(false)
  // Matrix as loaded from the server; an unchanged matrix is not resubmitted so
  // routine edits of an administrator do not require step-up verification.
  const loadedPermissions = useRef<AdminPermissionMatrix | undefined>(undefined)

  // Fetch groups
  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
    staleTime: 5 * 60 * 1000,
  })

  const groups = groupsData?.data || []

  // Permission catalog is owned by the backend; fetched once and reused.
  const { data: permissionCatalog = EMPTY_PERMISSION_CATALOG } = useQuery({
    queryKey: ['admin-permission-catalog'],
    queryFn: async () => requireServerSuccess(await getPermissionCatalog()),
    staleTime: 5 * 60 * 1000,
  })

  const { data: privatePricesData, refetch: refetchPrivatePrices } = useQuery({
    queryKey: ['user-model-prices', currentRow?.id],
    queryFn: () => {
      if (!currentRow) throw new Error(t('User is required'))
      return getUserModelPrices(currentRow.id).then(requireServerSuccess)
    },
    enabled: open && isUpdate && !!currentRow,
    staleTime: 0,
  })

  const { data: perCallModelsData } = useQuery({
    queryKey: ['per-call-priced-models'],
    queryFn: async () => requireServerSuccess(await getPerCallPricedModels()),
    enabled: open && isUpdate,
    staleTime: 5 * 60 * 1000,
  })

  const privatePrices = privatePricesData?.data || []
  const perCallModels = perCallModelsData?.data || []
  const privateModelUnits = new Map(
    perCallModels.map((item) => [item.model_name, item.pricing_unit])
  )
  const supportsUnifiedImagePrice = (modelName: string) => {
    const model = perCallModels.find((item) => item.model_name === modelName)
    return (
      model?.supports_unified_image_price ?? model?.pricing_unit === 'image'
    )
  }
  const selectedPrivatePriceIsPerSecond =
    privateModelUnits.get(privateModelName) === 'second'

  const form = useForm<UserFormValues>({
    resolver: zodResolver(userFormSchema),
    defaultValues: USER_FORM_DEFAULT_VALUES,
  })

  // Load existing data when updating
  useEffect(() => {
    if (open && isUpdate && currentRow) {
      // For update, fetch fresh data
      getUser(currentRow.id)
        .then((result) => {
          if (result.success && result.data) {
            loadedPermissions.current = result.data.admin_permissions
            form.reset(transformUserToFormDefaults(result.data))
          } else {
            handleServerError(result, t('Failed to load'))
          }
        })
        .catch((error) => handleServerError(error, t('Failed to load')))
    } else if (open && !isUpdate) {
      // For create, reset to defaults
      form.reset(USER_FORM_DEFAULT_VALUES)
    }
  }, [open, isUpdate, currentRow, form, t])

  const { meta: currencyMeta } = getCurrencyDisplay()
  const currencyLabel = getCurrencyLabel()
  const tokensOnly = currencyMeta.kind === 'tokens'

  const currentQuotaRaw = form.watch('quota_dollars') || 0
  const selectedRole = form.watch('role')
  const canEditAdminPermissions = currentUser?.role === ROLE.SUPER_ADMIN
  const targetIsAdmin = (selectedRole ?? currentRow?.role ?? 0) >= ROLE.ADMIN

  const onSubmit = async (data: UserFormValues) => {
    if (!isUpdate || data.password) {
      if (!accountPasswordSchema.safeParse(data.password ?? '').success) {
        form.setError('password', {
          type: 'manual',
          message: t('Password must contain between 8 and 128 characters.'),
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
      if (
        isUpdate &&
        payload.admin_permissions &&
        JSON.stringify(payload.admin_permissions) ===
          JSON.stringify(
            normalizeAdminPermissions(
              loadedPermissions.current,
              permissionCatalog
            )
          )
      ) {
        delete payload.admin_permissions
      }
      // Resetting a password, rewriting admin permissions, or creating an
      // administrator changes who can sign in or what they may do.
      let proofToken: string | undefined
      if (isUpdate && currentRow) {
        if (payload.password || payload.admin_permissions) {
          const proof = await requestVerification({
            scope: 'admin.user.update',
            context: { user_id: currentRow.id },
            title: t('Verify to update user credentials'),
            description: t(
              'Confirm your identity before changing the account {{username}}.',
              { username: currentRow.username }
            ),
          })
          if (!proof) return
          proofToken = proof.proof_token
        }
      } else if ((payload.role ?? 0) >= ROLE.ADMIN) {
        const proof = await requestVerification({
          scope: 'admin.user.create',
          context: { role: payload.role ?? ROLE.ADMIN },
          title: t('Verify to create administrator'),
          description: t(
            'Confirm your identity before creating an administrator account.'
          ),
        })
        if (!proof) return
        proofToken = proof.proof_token
      }
      const result = isUpdate
        ? await updateUser(
            payload as typeof payload & { id: number },
            proofToken
          )
        : await createUser(payload, proofToken)

      if (result.success) {
        toast.success(
          isUpdate
            ? t(SUCCESS_MESSAGES.USER_UPDATED)
            : t(SUCCESS_MESSAGES.USER_CREATED)
        )
        onOpenChange(false)
        triggerRefresh()
      } else {
        handleServerError(result, t(ERROR_MESSAGES.CREATE_FAILED))
      }
    } catch (error) {
      handleServerError(
        AuthOperationError.from(error),
        t(ERROR_MESSAGES.UNEXPECTED)
      )
    } finally {
      setIsSubmitting(false)
    }
  }

  const refreshUserData = async () => {
    if (!currentRow) return
    try {
      const result = requireServerSuccess(await getUser(currentRow.id))
      if (result.success && result.data) {
        loadedPermissions.current = result.data.admin_permissions
        form.reset(transformUserToFormDefaults(result.data))
      }
      triggerRefresh()
    } catch (error) {
      handleServerError(error, t('Failed to load'))
    }
  }

  const handleSavePrivatePrice = async () => {
    if (!currentRow) return
    const price = Number(privateModelPrice)

    if (!privateModelName) {
      toast.error(t('请选择模型'))
      return
    }

    if (
      privateModelPrice.trim() === '' ||
      !Number.isFinite(price) ||
      price < 0
    ) {
      toast.error(t('请输入正确的秘价'))
      return
    }

    try {
      requireServerSuccess(
        await upsertUserModelPrice(currentRow.id, {
          model_name: privateModelName,
          price,
          enabled: true,
          ...(supportsUnifiedImagePrice(privateModelName)
            ? { unified_image_price: true }
            : {}),
        })
      )
      toast.success(t('模型秘价已保存'))
      setPrivateModelName('')
      setPrivateModelPrice('')
      void refetchPrivatePrices()
    } catch (error) {
      handleServerError(error, t('保存模型秘价失败'))
    }
  }

  const handleDeletePrivatePrice = async (modelName: string) => {
    if (!currentRow || !perCallModelsData) return

    try {
      requireServerSuccess(
        await deleteUserModelPrice(
          currentRow.id,
          modelName,
          supportsUnifiedImagePrice(modelName) ||
            !perCallModels.some((item) => item.model_name === modelName)
        )
      )
      toast.success(t('模型秘价已删除'))
      void refetchPrivatePrices()
    } catch (error) {
      handleServerError(error, t('删除模型秘价失败'))
    }
  }

  const openImagePriceSheet = async (modelName: string) => {
    if (!currentRow || !modelName || isLoadingImagePrice) return
    setIsLoadingImagePrice(true)
    try {
      const [qualityResult, flatResult] = await Promise.all([
        getUserModelImageQualityPrices(currentRow.id, modelName).then(
          requireServerSuccess
        ),
        getUserModelPrices(currentRow.id).then(requireServerSuccess),
      ])
      const flatPrice = flatResult.data?.find(
        (item) => item.model_name === modelName
      )
      const prices = new Set(
        (qualityResult.data ?? [])
          .filter((item) => item.enabled)
          .map((item) => item.price)
      )
      if (flatPrice?.enabled) prices.add(flatPrice.price)
      const differingPrices = prices.size > 1
      setHasLegacyTierDifferences(differingPrices)
      const currentPrice =
        prices.size === 1
          ? [...prices][0]
          : (flatPrice?.price ??
            perCallModels.find((item) => item.model_name === modelName)
              ?.model_price)
      setImageUnitPrice(
        differingPrices || currentPrice === undefined
          ? ''
          : String(currentPrice)
      )
      setImagePriceModelName(modelName)
      setImagePriceSheetOpen(true)
    } catch (error) {
      handleServerError(error, t('Failed to load image price'))
    } finally {
      setIsLoadingImagePrice(false)
    }
  }

  const handleSaveImagePrivatePrice = async () => {
    if (!currentRow || !imagePriceModelName || isSavingImagePrice) return
    const price = Number(imageUnitPrice)
    if (imageUnitPrice.trim() === '' || !Number.isFinite(price) || price < 0) {
      toast.error(t('请输入正确的秘价'))
      return
    }
    setIsSavingImagePrice(true)
    try {
      requireServerSuccess(
        await upsertUserModelPrice(currentRow.id, {
          model_name: imagePriceModelName,
          price,
          enabled: true,
          unified_image_price: true,
        })
      )
      toast.success(t('模型秘价已保存'))
      setImagePriceSheetOpen(false)
      void refetchPrivatePrices()
    } catch (error) {
      handleServerError(error, t('保存模型秘价失败'))
    } finally {
      setIsSavingImagePrice(false)
    }
  }

  const handleDeleteImagePrivatePrice = async () => {
    if (!currentRow || !imagePriceModelName || isSavingImagePrice) return
    setIsSavingImagePrice(true)
    try {
      requireServerSuccess(
        await deleteUserModelPrice(currentRow.id, imagePriceModelName, true)
      )
      toast.success(t('模型秘价已删除'))
      setImageUnitPrice('')
      setImagePriceSheetOpen(false)
      void refetchPrivatePrices()
    } catch (error) {
      handleServerError(error, t('删除模型秘价失败'))
    } finally {
      setIsSavingImagePrice(false)
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
                            value !== null &&
                            field.onChange(Number.parseInt(value))
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
                              : t('Enter password (8–128 characters)')
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
                        <FormControl>
                          <Combobox
                            options={groups.map((group) => ({
                              value: group,
                              label: group,
                            }))}
                            onValueChange={field.onChange}
                            value={field.value}
                            className='w-full'
                            placeholder={t('Select a group')}
                          />
                        </FormControl>
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
                            <PermissionMatrix
                              resources={permissionCatalog.resources}
                              value={selected}
                              onChange={field.onChange}
                            />
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
                  <h3 className='text-sm font-medium'>{t('模型秘价设置')}</h3>
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Supports private prices per request, per image, and per second. Video prices are charged per second of requested duration. Other billing rules remain unchanged.'
                    )}
                  </p>

                  <div className='space-y-3 rounded-md border p-3'>
                    <div className='grid grid-cols-1 gap-2 sm:grid-cols-[1fr_140px_auto]'>
                      <Select
                        items={perCallModels.map((item) => ({
                          value: item.model_name,
                          label: `${item.model_name} (${item.model_price}${item.pricing_unit === 'second' ? ` / ${t('second')}` : ''})`,
                        }))}
                        value={privateModelName}
                        onValueChange={(value) =>
                          value !== null && setPrivateModelName(value)
                        }
                      >
                        <SelectTrigger
                          aria-label={t('Select a model for private pricing')}
                        >
                          <SelectValue
                            placeholder={t(
                              'Select a model for private pricing'
                            )}
                          />
                        </SelectTrigger>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            {perCallModels.map((item) => (
                              <SelectItem
                                key={item.model_name}
                                value={item.model_name}
                              >
                                {item.model_name} / {item.model_price}
                                {item.pricing_unit === 'second' &&
                                  ` / ${t('second')}`}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        </SelectContent>
                      </Select>

                      <Input
                        value={privateModelPrice}
                        onChange={(e) => setPrivateModelPrice(e.target.value)}
                        placeholder={
                          selectedPrivatePriceIsPerSecond
                            ? t('Private price per second, e.g. 0.03')
                            : t('秘价，例如 0.001')
                        }
                        inputMode='decimal'
                      />

                      <div className='flex gap-2'>
                        <Button type='button' onClick={handleSavePrivatePrice}>
                          {t('添加/更新')}
                        </Button>
                        {supportsUnifiedImagePrice(privateModelName) && (
                          <Button
                            type='button'
                            variant='outline'
                            disabled={!privateModelName || isLoadingImagePrice}
                            onClick={() =>
                              openImagePriceSheet(privateModelName)
                            }
                          >
                            {t('Set image price')}
                          </Button>
                        )}
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
                                {t('秘价')}: {item.price} /{' '}
                                {privateModelUnits.get(item.model_name) ===
                                  'second' && <>{t('second')} / </>}
                                {item.enabled ? t('启用') : t('关闭')}
                              </div>
                            </div>
                            <div className='flex shrink-0 gap-2'>
                              {supportsUnifiedImagePrice(item.model_name) && (
                                <Button
                                  type='button'
                                  variant='outline'
                                  disabled={isLoadingImagePrice}
                                  onClick={() =>
                                    openImagePriceSheet(item.model_name)
                                  }
                                >
                                  {t('Edit image price')}
                                </Button>
                              )}
                              <Button
                                type='button'
                                variant='outline'
                                disabled={!perCallModelsData}
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
                      'Third-party account bindings (read-only, managed by user in Security & Access)'
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

      <Sheet open={imagePriceSheetOpen} onOpenChange={setImagePriceSheetOpen}>
        <SheetContent
          className={sideDrawerContentClassName('sm:max-w-[520px]')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('Image private price')}</SheetTitle>
            <SheetDescription>
              {t(
                'This user pays the same image unit price for every quality and resolution, for synchronous and asynchronous generation.'
              )}
            </SheetDescription>
          </SheetHeader>

          <div className={sideDrawerFormClassName()}>
            <SideDrawerSection>
              <div className='space-y-1'>
                <Label htmlFor='user-image-price-model'>{t('模型')}</Label>
                <Input
                  id='user-image-price-model'
                  value={imagePriceModelName || '-'}
                  readOnly
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='user-image-unit-price'>
                  {t('Price per image')} ({t('USD')})
                </Label>
                <Input
                  id='user-image-unit-price'
                  inputMode='decimal'
                  placeholder='0.00'
                  value={imageUnitPrice}
                  disabled={isSavingImagePrice}
                  onChange={(e) => setImageUnitPrice(e.target.value)}
                />
                <p className='text-muted-foreground text-xs'>
                  {t('Final cost = unit price × image count.')}
                </p>
                {hasLegacyTierDifferences && (
                  <p className='text-muted-foreground text-xs' role='status'>
                    {t(
                      'Existing tier prices differ. Enter a single price to replace them when you save.'
                    )}
                  </p>
                )}
              </div>
            </SideDrawerSection>
          </div>

          <SheetFooter className={sideDrawerFooterClassName()}>
            <Button
              type='button'
              variant='outline'
              onClick={handleDeleteImagePrivatePrice}
              disabled={isSavingImagePrice}
            >
              {t('Delete image price')}
            </Button>
            <Button
              type='button'
              onClick={handleSaveImagePrivatePrice}
              disabled={isSavingImagePrice}
            >
              {isSavingImagePrice ? t('Saving...') : t('Save image price')}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </>
  )
}
