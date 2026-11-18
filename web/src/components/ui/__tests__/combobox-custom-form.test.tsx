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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef, useEffect, type FocusEvent } from 'react'
import { useForm } from 'react-hook-form'
import { describe, expect, it, vi } from 'vitest'

import { Dialog } from '@/components/dialog'

import { Button } from '../button'
import { Combobox } from '../combobox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '../form'

const options = [
  { value: 'image-pro', label: 'Image Pro' },
  { value: 'image-fast', label: 'Image Fast' },
]

function CustomModelForm() {
  const form = useForm({ defaultValues: { model: 'custom-image' } })
  useEffect(() => {
    form.setError('model', { message: 'Check this model name.' })
  }, [form])
  return (
    <Form {...form}>
      <form aria-label='Image request'>
        <FormField
          control={form.control}
          name='model'
          render={({ field }) => (
            <FormItem>
              <FormLabel>Model</FormLabel>
              <FormControl>
                <Combobox
                  {...field}
                  options={options}
                  allowCustomValue
                  openOnFocus={false}
                />
              </FormControl>
              <FormDescription>
                Choose an available model or type a custom name.
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </form>
    </Form>
  )
}

describe('custom-value combobox form forwarding', () => {
  it('preserves the form label, description, validation state and named input in a dialog', async () => {
    render(
      <Dialog open title='Configure image request'>
        <CustomModelForm />
      </Dialog>
    )
    const input = screen.getByRole('combobox', { name: 'Model' })
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(input).toHaveAccessibleDescription(
      'Choose an available model or type a custom name. Check this model name.'
    )
    expect(input).toHaveAttribute('name', 'model')
    const data = new FormData(
      screen.getByRole('form', { name: 'Image request' }) as HTMLFormElement
    )
    expect(data.get('model')).toBe('custom-image')
  })

  it('keeps a disabled custom-value input locked without opening options or committing changes', async () => {
    const change = vi.fn()
    render(
      <Dialog open title='Locked model'>
        <Combobox
          options={options}
          allowCustomValue
          openOnFocus={false}
          disabled
          value='image-pro'
          onValueChange={change}
          aria-label='Model'
        />
        <Button type='button'>Continue</Button>
      </Dialog>
    )
    const input = screen.getByRole('combobox', { name: 'Model' })
    expect(input).toBeDisabled()
    const user = userEvent.setup()
    await user.click(input)
    await user.type(input, 'changed')
    await user.keyboard('{ArrowDown}{Enter}')
    expect(input).toHaveValue('Image Pro')
    expect(input).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(change).not.toHaveBeenCalled()
  })

  it('exposes the real input through an object ref and forwards blur while closing its popup', async () => {
    const inputRef = createRef<HTMLInputElement>()
    const blur = vi.fn<(event: FocusEvent<HTMLInputElement>) => void>()
    render(
      <Dialog open title='Choose model'>
        <Combobox
          options={options}
          allowCustomValue
          openOnFocus={false}
          value='image-pro'
          ref={inputRef}
          onBlur={blur}
          aria-label='Model'
        />
        <Button type='button'>Continue</Button>
      </Dialog>
    )
    const input = screen.getByRole('combobox', { name: 'Model' })
    expect(inputRef.current).toBe(input)
    act(() => inputRef.current?.focus())
    expect(input).toHaveFocus()
    const user = userEvent.setup()
    await user.click(input)
    expect(
      await screen.findByRole('option', { name: 'Image Fast' })
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(blur).toHaveBeenCalledTimes(1)
    expect(blur.mock.calls[0][0].target).toBe(input)
    expect(input).toHaveAttribute('aria-expanded', 'false')
  })

  it('calls a React 19 callback ref cleanup on unmount while retaining the local input focus behavior', async () => {
    const cleanup = vi.fn()
    const inputRef = vi.fn((node: HTMLInputElement | null) => {
      if (node) return cleanup
    })
    const change = vi.fn()
    const view = render(
      <Dialog open title='Choose model'>
        <Combobox
          options={options}
          allowCustomValue
          openOnFocus={false}
          value='image-pro'
          ref={inputRef}
          onValueChange={change}
          aria-label='Model'
        />
      </Dialog>
    )
    const input = screen.getByRole('combobox', { name: 'Model' })
    expect(inputRef).toHaveBeenCalledWith(input)
    const user = userEvent.setup()
    await user.click(input)
    await user.click(await screen.findByRole('option', { name: 'Image Fast' }))
    expect(change).toHaveBeenCalledWith('image-fast')
    expect(input).toHaveFocus()
    view.unmount()
    expect(cleanup).toHaveBeenCalledTimes(1)
    expect(inputRef).not.toHaveBeenCalledWith(null)
  })
})
