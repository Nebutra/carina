import { forwardRef } from 'react'
import type { ChangeEvent, InputHTMLAttributes } from 'react'
import { cn } from '../../lib/utils'

export type CheckboxProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  /** Convenience callback matching controlled checkbox primitives. */
  onCheckedChange?: (checked: boolean) => void
}

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox({ className, onChange, onCheckedChange, ...props }, ref) {
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    onChange?.(event)
    onCheckedChange?.(event.currentTarget.checked)
  }
  return <input ref={ref} type="checkbox" className={cn('ui-checkbox', className)} onChange={handleChange} {...props} />
})

Checkbox.displayName = 'Checkbox'
