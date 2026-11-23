import { Link } from '@tanstack/react-router'
import { useT } from '@/lib/i18n'
import { initials } from '@/lib/utils'
import { useSession } from '@/lib/session'
import useDialogState from '@/hooks/use-dialog-state'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { SignOutDialog } from '@/components/sign-out-dialog'

export function ProfileDropdown() {
  const t = useT()
  const { me } = useSession()
  const [open, setOpen] = useDialogState()

  return (
    <>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button variant='ghost' className='relative h-8 w-8 rounded-full'>
            <Avatar className='h-8 w-8'>
              <AvatarFallback>{initials(me.username)}</AvatarFallback>
            </Avatar>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent className='w-56' align='end' forceMount>
          <DropdownMenuLabel className='font-normal'>
            <div className='flex flex-col gap-1.5'>
              <p className='text-sm leading-none font-medium'>{me.username}</p>
              <p className='text-xs leading-none text-muted-foreground'>v{me.version}</p>
            </div>
          </DropdownMenuLabel>
          <DropdownMenuItem asChild>
            <Link to='/settings'>{t('nav.settings')}</Link>
          </DropdownMenuItem>
          <DropdownMenuItem variant='destructive' onClick={() => setOpen(true)}>
            {t('common.signOut')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <SignOutDialog open={!!open} onOpenChange={setOpen} />
    </>
  )
}
