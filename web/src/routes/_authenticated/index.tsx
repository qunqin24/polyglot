import { createFileRoute } from '@tanstack/react-router'
import { Overview } from '@/pages/overview'

export const Route = createFileRoute('/_authenticated/')({
  component: Overview,
})
