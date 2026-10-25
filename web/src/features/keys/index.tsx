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
import { Check, Copy } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'

import { ApiKeysDialogs } from './components/api-keys-dialogs'
import { ApiKeysPrimaryButtons } from './components/api-keys-primary-buttons'
import { ApiKeysProvider } from './components/api-keys-provider'
import { ApiKeysTable } from './components/api-keys-table'

export function ApiKeys() {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const apiEndpoint = 'https://nicolessss.com'

  const copyEndpoint = async () => {
    await navigator.clipboard.writeText(apiEndpoint)
    setCopied(true)
    window.setTimeout(() => setCopied(false), 1500)
  }

  return (
    <ApiKeysProvider>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('API Keys')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <ApiKeysPrimaryButtons />
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='bg-muted/40 mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border px-4 py-3'>
            <div className='min-w-0'>
              <div className='text-sm font-medium'>{t('API Endpoint')}</div>
              <code className='text-muted-foreground block truncate text-xs sm:text-sm'>
                {apiEndpoint}
              </code>
            </div>
            <Button variant='outline' size='sm' onClick={copyEndpoint}>
              {copied ? <Check className='mr-1 h-4 w-4' /> : <Copy className='mr-1 h-4 w-4' />}
              {copied ? t('Copied') : t('Copy')}
            </Button>
          </div>
          <ApiKeysTable />
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <ApiKeysDialogs />
    </ApiKeysProvider>
  )
}
