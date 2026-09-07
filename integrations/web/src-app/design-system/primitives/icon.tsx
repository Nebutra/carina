import { forwardRef } from 'react'
import type { ComponentPropsWithoutRef } from 'react'
import type { LucideIcon } from 'lucide-react'
import { cn } from '../../lib/utils'
import { Button } from './button'
import { Tooltip, TooltipContent, TooltipTrigger } from './tooltip'

export type IconProps = Omit<ComponentPropsWithoutRef<'svg'>, 'ref'> & {
  icon: LucideIcon
  label?: string
}

export function Icon({ icon: IconComponent, label, className, ...props }: IconProps) {
  return <IconComponent className={cn('ui-icon', className)} aria-hidden={label ? undefined : true} aria-label={label} {...props} />
}

export type IconButtonProps = Omit<ComponentPropsWithoutRef<'button'>, 'children'> & {
  icon: LucideIcon
  label: string
}

/** Icon-only actions always expose the same accessible label and visual tooltip. */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton({ icon, label, className, title, ...props }, ref) {
  const IconComponent = icon
  const tooltip = title || label
  return <Tooltip>
    <TooltipTrigger asChild>
      <Button ref={ref} variant="ghost" size="icon" className={cn('ui-icon-button', className)} aria-label={label} {...props}>
        <IconComponent aria-hidden="true" />
      </Button>
    </TooltipTrigger>
    <TooltipContent>{tooltip}</TooltipContent>
  </Tooltip>
})

IconButton.displayName = 'IconButton'
