import * as TooltipPrimitive from '@radix-ui/react-tooltip'
import { forwardRef } from 'react'
import type { ComponentPropsWithoutRef, ComponentRef } from 'react'
import { cn } from '../../lib/utils'

export const TooltipProvider = TooltipPrimitive.Provider
export const Tooltip = TooltipPrimitive.Root

export const TooltipTrigger = forwardRef<
  ComponentRef<typeof TooltipPrimitive.Trigger>,
  ComponentPropsWithoutRef<typeof TooltipPrimitive.Trigger>
>(function TooltipTrigger({ className, ...props }, ref) {
  return <TooltipPrimitive.Trigger ref={ref} className={cn('ui-tooltip-trigger', className)} {...props} />
})

TooltipTrigger.displayName = 'TooltipTrigger'

export const TooltipContent = forwardRef<
  ComponentRef<typeof TooltipPrimitive.Content>,
  ComponentPropsWithoutRef<typeof TooltipPrimitive.Content>
>(function TooltipContent({ className, sideOffset = 6, ...props }, ref) {
  return <TooltipPrimitive.Portal>
    <TooltipPrimitive.Content
      ref={ref}
      className={cn('ui-tooltip-content', className)}
      sideOffset={sideOffset}
      collisionPadding={8}
      {...props}
    />
  </TooltipPrimitive.Portal>
})

TooltipContent.displayName = 'TooltipContent'
