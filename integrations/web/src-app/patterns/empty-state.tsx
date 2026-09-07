import type { ReactNode } from 'react'

import { cn } from '../design-system'

export type EmptyStateProps = {
  title: string
  description: string
  icon?: ReactNode
  action?: ReactNode
  className?: string
  compact?: boolean
}

/** Shared empty and recovery state so every surface has the same hierarchy. */
export function EmptyState({ title, description, icon, action, className, compact = false }: EmptyStateProps) {
  return <div className={cn('react-empty', compact && 'compact', className)}>
    {icon}
    <strong>{title}</strong>
    <span>{description}</span>
    {action}
  </div>
}
