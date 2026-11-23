import { cn } from '@/lib/utils'

// A placeholder block. The pale band that crosses it every 1.2s lives in the
// `.skeleton` class (styles/polyglot.css), where reduced motion turns it into
// a soft pulse.
function Skeleton({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot='skeleton'
      aria-hidden='true'
      className={cn('skeleton rounded-md', className)}
      {...props}
    />
  )
}

export { Skeleton }
