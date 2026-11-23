import { useEffect, useRef } from 'react'
import { createFileRoute, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useSessionState } from '@/lib/session'
import { AuthenticatedLayout } from '@/components/layout/authenticated-layout'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ConfigDrawer } from '@/components/config-drawer'
import { FullPageSpinner } from '@/components/full-page-spinner'
import { LocaleSwitch } from '@/components/locale-switch'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { Search } from '@/components/search'
import { ThemeSwitch } from '@/components/theme-switch'
import { UpdateNotice } from '@/components/update-notice'

export const Route = createFileRoute('/_authenticated')({
  component: AuthenticatedRoute,
})

function AuthenticatedRoute() {
  const { session } = useSessionState()
  const href = useLocation({ select: (l) => l.href })
  const navigate = useNavigate()

  // Redirect exactly once per sign-out. While the transition is pending this
  // route is still mounted but useLocation already reports the target, so
  // re-running on every href change would redirect to the sign-in page from
  // the sign-in page, forever.
  const signedOut = session.state === 'anonymous' || session.state === 'setup'
  const redirected = useRef(false)
  useEffect(() => {
    if (!signedOut) {
      redirected.current = false
      return
    }
    if (redirected.current) return
    redirected.current = true
    void navigate({
      to: '/sign-in',
      search: href === '/' ? {} : { redirect: href },
      replace: true,
    })
  }, [signedOut, href, navigate])

  if (session.state !== 'signed-in') return <FullPageSpinner />

  return (
    <AuthenticatedLayout>
      <Header fixed>
        <Search />
        <div className='ms-auto flex items-center gap-1 sm:gap-2'>
          <LocaleSwitch />
          <ThemeSwitch />
          <ConfigDrawer />
          <ProfileDropdown />
        </div>
      </Header>
      <Main>
        <UpdateNotice />
        <Outlet />
      </Main>
    </AuthenticatedLayout>
  )
}
