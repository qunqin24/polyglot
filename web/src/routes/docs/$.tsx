import { createFileRoute } from '@tanstack/react-router'
import { lazy, Suspense } from 'react'
import { LoadingGate, SkeletonLine } from '@/components/kit/skeletons'

// Docs are public and heavy (Markdown + every bundled document), so they load
// on demand and never weigh down the admin bundle.
const DocsPage = lazy(() => import('@/pages/docs'))

export const Route = createFileRoute('/docs/$')({
  component: () => (
    <Suspense fallback={<DocsSkeleton />}>
      <DocsPage />
    </Suspense>
  ),
})

// The shape of a document page: a title, then paragraphs of text lines.
function DocsSkeleton() {
  return (
    <div className='mx-auto w-full max-w-3xl px-6 py-10'>
      <LoadingGate
        loading
        skeleton={
          <div className='space-y-6'>
            <SkeletonLine className='h-6 w-1/3' />
            {[0, 1, 2].map((p) => (
              <div key={p} className='space-y-2.5'>
                <SkeletonLine className='w-full' />
                <SkeletonLine className='w-11/12' />
                <SkeletonLine className='w-3/4' />
              </div>
            ))}
          </div>
        }
      >
        {null}
      </LoadingGate>
    </div>
  )
}
