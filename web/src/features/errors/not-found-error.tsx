import { useNavigate, useRouter } from '@tanstack/react-router'
import { useT } from '@/lib/i18n'
import { Button } from '@/components/ui/button'

export function NotFoundError() {
  const t = useT()
  const navigate = useNavigate()
  const { history } = useRouter()
  return (
    <div className='h-svh'>
      <div className='m-auto flex h-full w-full flex-col items-center justify-center gap-2'>
        <h1 className='text-[7rem] leading-tight font-bold'>404</h1>
        <span className='font-medium'>{t('error.notFoundTitle')}</span>
        <p className='text-center text-muted-foreground'>{t('error.notFoundBody')}</p>
        <div className='mt-6 flex gap-4'>
          <Button variant='outline' onClick={() => history.go(-1)}>
            {t('error.goBack')}
          </Button>
          <Button onClick={() => void navigate({ to: '/' })}>{t('error.home')}</Button>
        </div>
      </div>
    </div>
  )
}
