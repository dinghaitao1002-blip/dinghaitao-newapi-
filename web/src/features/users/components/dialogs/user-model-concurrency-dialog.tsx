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
import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'
import type { ConcurrencyFocusedUser } from '@/features/system-settings/concurrency-limits/api'
import { ModelConcurrencySection } from '@/features/system-settings/concurrency-limits/model-concurrency-section'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export function UserModelConcurrencyDialog(props: {
  user: ConcurrencyFocusedUser | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [actionsContainer, setActionsContainer] =
    useState<HTMLDivElement | null>(null)
  const canManage = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )
  if (!canManage) return null
  return (
    <Dialog
      open
      onOpenChange={props.onOpenChange}
      title={t('Model concurrency')}
      description={
        props.user
          ? t('Set model concurrency for {{username}} (#{{id}}).', {
              username: props.user.username,
              id: String(props.user.id),
            })
          : t('Manage shared model limits and user overrides.')
      }
      descriptionClassName='break-words'
      contentClassName='sm:max-w-5xl'
      contentHeight='min(65vh, 720px)'
      footer={<div ref={setActionsContainer} />}
    >
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <ModelConcurrencySection
          key={props.user?.id ?? 'global'}
          focusedUser={props.user ?? undefined}
        />
      </SettingsPageProvider>
    </Dialog>
  )
}
