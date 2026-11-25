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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { SettingsPageProvider } from '../components/settings-page-context'
import type { ConcurrencyFocusedUser } from './api'
import { ModelConcurrencySection } from './model-concurrency-section'

export function ModelConcurrencyDialog(props: {
  group?: string
  focusedUser?: ConcurrencyFocusedUser
  initialModel?: string
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [actionsContainer, setActionsContainer] =
    useState<HTMLDivElement | null>(null)
  const canManage = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )
  if (!canManage) return null
  let title = t('Model concurrency')
  let description = t('Manage shared model limits and user overrides.')
  if (props.group !== undefined) {
    title = t('Model concurrency for group {{group}}', { group: props.group })
    description = t(
      'Choose a model in this group, then set shared limits and individual user concurrency. Pricing is saved separately.'
    )
  } else if (props.focusedUser) {
    description = t('Set model concurrency for {{username}} (#{{id}}).', {
      username: props.focusedUser.username,
      id: String(props.focusedUser.id),
    })
  }
  return (
    <Dialog
      open
      onOpenChange={props.onOpenChange}
      title={title}
      description={description}
      descriptionClassName='break-words'
      contentClassName='sm:max-w-5xl'
      contentHeight='min(65vh, 720px)'
      footer={<div ref={setActionsContainer} />}
    >
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <ModelConcurrencySection
          key={JSON.stringify([
            props.group,
            props.focusedUser?.id,
            props.initialModel,
          ])}
          focusedUser={props.focusedUser}
          group={props.group}
          initialModel={props.initialModel}
        />
      </SettingsPageProvider>
    </Dialog>
  )
}
