import { useT } from '@/lib/i18n'
import { useSessionState } from '@/lib/session'
import { ConfirmDialog } from '@/components/kit/dialog'

interface SignOutDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function SignOutDialog({ open, onOpenChange }: SignOutDialogProps) {
  const t = useT()
  const { signOut } = useSessionState()

  // The authenticated route sees the session end and redirects to sign-in,
  // keeping the current page as the place to come back to.
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('shell.signOutTitle')}
      description={t('shell.signOutConfirm')}
      confirmLabel={t('common.signOut')}
      onConfirm={signOut}
    />
  )
}
