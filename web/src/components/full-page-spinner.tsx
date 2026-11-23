import { Spinner } from '@/components/kit/misc'
import { useDelayedFlag } from '@/lib/hooks'

// The page is reserved at once; the spinner shows only if the wait outlasts
// 300 ms, so a fast load does not flash one.
export function FullPageSpinner() {
  const shown = useDelayedFlag(true)
  return (
    <div className='flex min-h-svh items-center justify-center' aria-busy='true'>
      {shown && <Spinner className='size-5 text-muted-foreground' />}
    </div>
  )
}
