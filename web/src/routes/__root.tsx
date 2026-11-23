import { createRootRoute, Outlet } from '@tanstack/react-router'
import { Toaster } from '@/components/ui/sonner'
import { NavigationProgress } from '@/components/navigation-progress'
import { GeneralError } from '@/features/errors/general-error'
import { NotFoundError } from '@/features/errors/not-found-error'

export const Route = createRootRoute({
  component: () => (
    <>
      <NavigationProgress />
      <Outlet />
      <Toaster duration={3500} position='top-right' richColors />
    </>
  ),
  notFoundComponent: NotFoundError,
  errorComponent: GeneralError,
})
