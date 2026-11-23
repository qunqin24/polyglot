import { useEffect, useRef } from 'react'
import { useRouterState } from '@tanstack/react-router'
import LoadingBar, { type LoadingBarRef } from 'react-top-loading-bar'

export function NavigationProgress() {
  const ref = useRef<LoadingBarRef>(null)
  const state = useRouterState()

  // A navigation that finishes within 300 ms shows no bar at all.
  useEffect(() => {
    if (state.status !== 'pending') {
      ref.current?.complete()
      return
    }
    const timer = setTimeout(() => ref.current?.continuousStart(), 300)
    return () => clearTimeout(timer)
  }, [state.status])

  return (
    <LoadingBar
      color='var(--brand)'
      ref={ref}
      shadow={false}
      height={2}
    />
  )
}
