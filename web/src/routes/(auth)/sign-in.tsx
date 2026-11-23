import { createFileRoute, useRouter } from '@tanstack/react-router'
import { useEffect } from 'react'
import { useSessionState } from '@/lib/session'
import { FullPageSpinner } from '@/components/full-page-spinner'
import { AuthPage } from '@/pages/auth'

type SignInSearch = { redirect?: string }

export const Route = createFileRoute('/(auth)/sign-in')({
  // Only same-origin paths: a crafted ?redirect=//evil.example must not be
  // followed after a successful sign-in.
  validateSearch: (search: Record<string, unknown>): SignInSearch => {
    const r = search.redirect
    return typeof r === 'string' && r.startsWith('/') && !r.startsWith('//')
      ? { redirect: r }
      : {}
  },
  component: SignIn,
})

function SignIn() {
  const { session, reload } = useSessionState()
  const { redirect } = Route.useSearch()
  const router = useRouter()

  const signedIn = session.state === 'signed-in'
  useEffect(() => {
    if (signedIn) router.history.replace(redirect ?? '/')
  }, [signedIn, redirect, router])

  if (session.state === 'loading' || signedIn) return <FullPageSpinner />
  return (
    <AuthPage
      mode={session.state === 'setup' ? 'setup' : 'login'}
      onDone={() => void reload()}
    />
  )
}
