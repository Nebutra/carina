import * as DialogPrimitive from '@radix-ui/react-dialog'
import { forwardRef } from 'react'
import type { ComponentPropsWithoutRef, ComponentRef, HTMLAttributes } from 'react'
import { cn } from '../../lib/utils'

export type DialogProps = ComponentPropsWithoutRef<typeof DialogPrimitive.Root>

/**
 * Project-owned modal contract. Radix owns portal, focus containment,
 * Escape handling, outside interaction, and focus restoration.
 */
export const Dialog = DialogPrimitive.Root

export const DialogTrigger = forwardRef<
  ComponentRef<typeof DialogPrimitive.Trigger>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Trigger>
>(function DialogTrigger({ className, ...props }, ref) {
  return <DialogPrimitive.Trigger ref={ref} className={cn('ui-dialog-trigger', className)} {...props} />
})

DialogTrigger.displayName = 'DialogTrigger'

export const DialogClose = forwardRef<
  ComponentRef<typeof DialogPrimitive.Close>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Close>
>(function DialogClose({ className, ...props }, ref) {
  return <DialogPrimitive.Close ref={ref} className={cn('ui-dialog-close', className)} {...props} />
})

DialogClose.displayName = 'DialogClose'

export type DialogContentProps = ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & {
  /** Close when clicking the scrim. Defaults to true for modal dialogs. */
  closeOnOverlayClick?: boolean
}

export const DialogContent = forwardRef<
  ComponentRef<typeof DialogPrimitive.Content>,
  DialogContentProps
>(function DialogContent({ className, children, closeOnOverlayClick = true, onPointerDownOutside, ...props }, ref) {
  return <DialogPrimitive.Portal>
    <DialogPrimitive.Overlay className="ui-dialog-overlay" />
    <DialogPrimitive.Content
      ref={ref}
      className={cn('ui-dialog-content', className)}
      onPointerDownOutside={(event) => {
        onPointerDownOutside?.(event)
        if (!closeOnOverlayClick) event.preventDefault()
      }}
      {...props}
    >
      {children}
    </DialogPrimitive.Content>
  </DialogPrimitive.Portal>
})

DialogContent.displayName = 'DialogContent'

export function DialogHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('ui-dialog-header', className)} {...props} />
}

export function DialogFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('ui-dialog-footer', className)} {...props} />
}

export const DialogTitle = forwardRef<
  ComponentRef<typeof DialogPrimitive.Title>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(function DialogTitle({ className, ...props }, ref) {
  return <DialogPrimitive.Title ref={ref} className={cn('ui-dialog-title', className)} {...props} />
})

DialogTitle.displayName = 'DialogTitle'

export const DialogDescription = forwardRef<
  ComponentRef<typeof DialogPrimitive.Description>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(function DialogDescription({ className, ...props }, ref) {
  return <DialogPrimitive.Description ref={ref} className={cn('ui-dialog-description', className)} {...props} />
})

DialogDescription.displayName = 'DialogDescription'
