import * as ScrollAreaPrimitive from '@radix-ui/react-scroll-area'
import type { ComponentPropsWithoutRef, ReactNode } from 'react'
import { cn } from '../../lib/utils'

type ScrollAreaProps = ComponentPropsWithoutRef<typeof ScrollAreaPrimitive.Root> & {
  children?: ReactNode
}

export function ScrollArea({ className, children, ...props }: ScrollAreaProps) {
  return <ScrollAreaPrimitive.Root className={cn('ui-scroll-area', className)} {...props}><ScrollAreaPrimitive.Viewport className="ui-scroll-viewport">{children}</ScrollAreaPrimitive.Viewport><ScrollAreaPrimitive.Scrollbar orientation="vertical" className="ui-scrollbar"><ScrollAreaPrimitive.Thumb className="ui-scroll-thumb" /></ScrollAreaPrimitive.Scrollbar></ScrollAreaPrimitive.Root>
}
