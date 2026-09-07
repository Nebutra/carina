import { forwardRef } from 'react'
import type { LabelHTMLAttributes } from 'react'
import { cn } from '../../lib/utils'

export type LabelProps = LabelHTMLAttributes<HTMLLabelElement>

export const Label = forwardRef<HTMLLabelElement, LabelProps>(function Label({ className, ...props }, ref) {
  return <label ref={ref} className={cn('ui-label', className)} {...props} />
})

Label.displayName = 'Label'
