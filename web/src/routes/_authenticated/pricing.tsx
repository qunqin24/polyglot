import { createFileRoute } from '@tanstack/react-router'
import { Pricing } from '@/pages/pricing'

export const Route = createFileRoute('/_authenticated/pricing')({
  component: Pricing,
})
