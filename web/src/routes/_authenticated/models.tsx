import { createFileRoute } from '@tanstack/react-router'
import { Models } from '@/pages/models'

export const Route = createFileRoute('/_authenticated/models')({
  component: Models,
})
