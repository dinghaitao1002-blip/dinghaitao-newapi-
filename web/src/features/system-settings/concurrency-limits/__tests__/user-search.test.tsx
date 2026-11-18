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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { Dialog } from '@/components/dialog'
import { api } from '@/lib/api'

import { ConcurrencyUserSearch } from '../user-search'

let client: QueryClient
const target = {
  id: 193,
  username: 'vq3d5cbm',
  display_name: 'Priority account',
}
const other = { id: 2, username: 'mouxi', display_name: 'vq3d5cbm team' }

function response(items = [target]) {
  return { data: { success: true, data: { items, total: items.length } } }
}

function pendingResponse() {
  let resolve!: (value: ReturnType<typeof response>) => void
  const promise = new Promise<ReturnType<typeof response>>((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}

function renderSearch(selected = vi.fn(), excludedUserIds: number[] = []) {
  return {
    selected,
    ...render(
      <QueryClientProvider client={client}>
        <Dialog open title='User concurrency'>
          <ConcurrencyUserSearch
            onSelect={selected}
            excludedUserIds={excludedUserIds}
          />
        </Dialog>
      </QueryClientProvider>
    ),
  }
}

async function search(keyword: string) {
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Find a user' }),
    keyword
  )
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  return screen.findByRole('combobox', {
    name: 'Select user to add an override',
  })
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockResolvedValue(response())
})

afterEach(() => client.clear())

describe('concurrency user search identity', () => {
  it.each(['click', 'keyboard'])(
    'returns the selected account identity and actual ID 193 instead of row position or ID 2 using %s',
    async (method) => {
      vi.mocked(api.get).mockResolvedValue(response([other, target]))
      const { selected } = renderSearch()
      const selector = await search('vq3d5cbm')
      await userEvent.click(selector)
      if (method === 'keyboard') {
        await userEvent.type(selector, 'vq3d5cbm')
        await userEvent.keyboard('{ArrowDown}{Enter}')
      } else {
        await userEvent.click(
          screen.getByRole('option', { name: /vq3d5cbm \(#193\)/ })
        )
      }
      expect(selected).toHaveBeenCalledExactlyOnceWith(target)
    }
  )

  it('hides previous results as soon as editable search text differs from the submitted keyword', async () => {
    const { selected } = renderSearch()
    await search('vq3d5cbm')
    const input = screen.getByRole('textbox', { name: 'Find a user' })
    await userEvent.clear(input)
    await userEvent.type(input, 'mouxi')
    expect(
      screen.queryByRole('combobox', { name: 'Select user to add an override' })
    ).not.toBeInTheDocument()
    expect(selected).not.toHaveBeenCalled()
    expect(api.get).toHaveBeenCalledTimes(1)
  })

  it('hides cached results during a refetch and selects only the refreshed identity afterwards', async () => {
    const pending = pendingResponse()
    const { selected } = renderSearch()
    await search('vq3d5cbm')
    vi.mocked(api.get).mockReturnValueOnce(pending.promise)
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    expect(
      screen.queryByRole('combobox', { name: 'Select user to add an override' })
    ).not.toBeInTheDocument()
    const refreshed = { ...target, display_name: 'Updated account name' }
    await act(async () => pending.resolve(response([refreshed])))
    const selector = await screen.findByRole('combobox', {
      name: 'Select user to add an override',
    })
    await userEvent.click(selector)
    await userEvent.click(
      screen.getByRole('option', { name: /vq3d5cbm \(#193\)/ })
    )
    expect(selected).toHaveBeenCalledExactlyOnceWith(refreshed)
  })

  it('hides cached results when refreshing the submitted query fails', async () => {
    const { selected } = renderSearch()
    await search('vq3d5cbm')
    vi.mocked(api.get).mockRejectedValueOnce(new Error('Unavailable'))
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Failed to search users. Try again.'
    )
    expect(
      screen.queryByRole('combobox', { name: 'Select user to add an override' })
    ).not.toBeInTheDocument()
    expect(selected).not.toHaveBeenCalled()
  })

  it('does not select an already configured user by pointer or keyboard', async () => {
    const { selected } = renderSearch(vi.fn(), [193])
    const selector = await search('vq3d5cbm')
    await userEvent.click(selector)
    const option = screen.getByRole('option', { name: /vq3d5cbm \(#193\)/ })
    expect(option).toHaveAttribute('aria-disabled', 'true')
    await userEvent.click(option)
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(selected).not.toHaveBeenCalled()
  })

  it('searches by email and returns the account username with its actual ID', async () => {
    const { selected } = renderSearch()
    const selector = await search('owner@example.com')
    await userEvent.click(selector)
    await userEvent.click(
      screen.getByRole('option', { name: /vq3d5cbm \(#193\)/ })
    )
    expect(api.get).toHaveBeenCalledWith(
      expect.stringContaining('keyword=owner%40example.com')
    )
    expect(selected).toHaveBeenCalledExactlyOnceWith(target)
  })

  it('does not display or select a closed previous request after remounting a fresh search', async () => {
    const pending = pendingResponse()
    vi.mocked(api.get).mockReturnValueOnce(pending.promise)
    const previous = renderSearch()
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Find a user' }),
      'vq3d5cbm'
    )
    await userEvent.click(screen.getByRole('button', { name: 'Search' }))
    previous.unmount()
    const current = renderSearch()
    await act(async () => pending.resolve(response()))
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(previous.selected).not.toHaveBeenCalled()
    expect(current.selected).not.toHaveBeenCalled()
    vi.mocked(api.get).mockResolvedValueOnce(response([other]))
    const selector = await search('mouxi')
    await userEvent.click(selector)
    await userEvent.click(screen.getByRole('option', { name: /mouxi \(#2\)/ }))
    expect(current.selected).toHaveBeenCalledExactlyOnceWith(other)
    expect(previous.selected).not.toHaveBeenCalled()
  })

  it('does not append a non-positive ID returned in an invalid result', async () => {
    vi.mocked(api.get).mockResolvedValueOnce(response([{ ...target, id: 0 }]))
    const { selected } = renderSearch()
    const selector = await search('vq3d5cbm')
    await userEvent.click(selector)
    await userEvent.click(
      screen.getByRole('option', { name: /vq3d5cbm \(#0\)/ })
    )
    expect(selected).not.toHaveBeenCalled()
  })
})
