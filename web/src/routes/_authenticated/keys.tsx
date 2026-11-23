import { createFileRoute } from '@tanstack/react-router'
import { Keys } from '@/pages/keys'

export const Route = createFileRoute('/_authenticated/keys')({
  component: Keys,
})
