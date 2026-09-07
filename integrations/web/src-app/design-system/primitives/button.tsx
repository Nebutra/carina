import { forwardRef } from 'react'
import type { ButtonHTMLAttributes } from 'react'
import { cn } from '../../lib/utils'

export type ButtonVariant = 'primary' | 'secondary' | 'outline' | 'ghost' | 'destructive'
export type ButtonSize = 'default' | 'icon' | 'icon-sm' | 'compact' | 'large'

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant
  size?: ButtonSize
}

/** Project-owned button contract. Feature code should consume this instead of styling native buttons. */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { className, variant = 'secondary', size = 'default', type = 'button', ...props },
  ref,
) {
  return <button ref={ref} type={type} className={cn('ui-button', `ui-button-${variant}`, `ui-button-${size}`, className)} {...props} />
})

Button.displayName = 'Button'
