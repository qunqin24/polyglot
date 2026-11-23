import { type Table } from '@tanstack/react-table'
import { X } from 'lucide-react'
import { useT } from '@/lib/i18n'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'

/** A floating toolbar shown while rows are selected. Escape clears the selection. */
export function DataTableBulkActions<TData>({
  table,
  children,
}: {
  table: Table<TData>
  children: React.ReactNode
}) {
  const t = useT()
  const count = table.getFilteredSelectedRowModel().rows.length
  if (count === 0) return null

  return (
    <div
      role='toolbar'
      aria-label={t('table.selectedCount', { count })}
      onKeyDown={(e) => {
        if (e.key === 'Escape') table.resetRowSelection()
      }}
      className='fixed bottom-6 left-1/2 z-50 -translate-x-1/2'
    >
      <div className='flex items-center gap-x-2 rounded-xl border bg-background/95 p-2 shadow-xl backdrop-blur-lg'>
        <Button
          variant='outline'
          size='icon'
          onClick={() => table.resetRowSelection()}
          className='size-6 rounded-full'
          aria-label={t('table.clearSelection')}
          title={t('table.clearSelection')}
        >
          <X />
        </Button>
        <Separator className='h-5' orientation='vertical' />
        <div className='flex items-center gap-x-1.5 text-sm' aria-live='polite'>
          <Badge className='min-w-8 rounded-lg'>{count}</Badge>
          <span className='hidden sm:inline'>{t('table.selected')}</span>
        </div>
        <Separator className='h-5' orientation='vertical' />
        {children}
      </div>
    </div>
  )
}
