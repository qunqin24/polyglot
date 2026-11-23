import { createFileRoute } from '@tanstack/react-router'
import { Logs } from '@/pages/logs'

export const Route = createFileRoute('/_authenticated/logs')({
  component: Logs,
})
