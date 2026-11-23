import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { I18nProvider } from '@/lib/i18n'
import { SessionProvider } from '@/lib/session'
import { DirectionProvider } from '@/context/direction-provider'
import { FontProvider } from '@/context/font-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { routeTree } from './routeTree.gen'
import './styles/index.css'

const router = createRouter({
  routeTree,
  defaultPreload: 'intent',
  defaultPreloadStaleTime: 0,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <I18nProvider>
      <ThemeProvider>
        <FontProvider>
          <DirectionProvider>
            <SessionProvider>
              <RouterProvider router={router} />
            </SessionProvider>
          </DirectionProvider>
        </FontProvider>
      </ThemeProvider>
    </I18nProvider>
  </StrictMode>,
)
