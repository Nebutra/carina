import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '../../lib/utils'

export type BadgeTone = 'neutral' | 'success' | 'accent' | 'warning' | 'danger' | 'info'

export type BadgeProps = HTMLAttributes<HTMLSpanElement> & {
  tone?: BadgeTone
  children?: ReactNode
}

export function Badge({ className, tone = 'neutral', children, ...props }: BadgeProps) {
  return <span className={cn('ui-badge', `ui-badge-${tone}`, className)} {...props}>{children}</span>
}
