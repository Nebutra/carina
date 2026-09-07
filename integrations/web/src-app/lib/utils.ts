import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...values: Array<Parameters<typeof clsx>[0]>) {
  return twMerge(clsx(values))
}
