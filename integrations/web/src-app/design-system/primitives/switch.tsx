import { forwardRef } from 'react'
import type { ChangeEvent, InputHTMLAttributes } from 'react'
import { cn } from '../../lib/utils'

export type SwitchProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  /** Convenience callback matching controlled switch primitives. */
  onCheckedChange?: (checked: boolean) => void
}

export const Switch = forwardRef<HTMLInputElement, SwitchProps>(function Switch({ className, onChange, onCheckedChange, role = 'switch', ...props }, ref) {
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    onChange?.(event)
    onCheckedChange?.(event.currentTarget.checked)
  }

  return <input ref={ref} type="checkbox" role={role} className={cn('ui-switch', className)} onChange={handleChange} {...props} />
})

Switch.displayName = 'Switch'
