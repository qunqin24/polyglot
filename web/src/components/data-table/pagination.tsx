import { type Table } from '@tanstack/react-table'
import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useT } from '@/lib/i18n'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

/** At most five page buttons: 1 … 4 5 6 … 10. */
function pageNumbers(current: number, total: number): (number | '…')[] {
  if (total <= 5) return Array.from({ length: total }, (_, i) => i + 1)
  if (current <= 3) return [1, 2, 3, 4, '…', total]
  if (current >= total - 2) return [1, '…', total - 3, total - 2, total - 1, total]
  return [1, '…', current - 1, current, current + 1, '…', total]
}

export function DataTablePagination<TData>({
  table,
  className,
}: {
  table: Table<TData>
  className?: string
}) {
  const t = useT()
  const currentPage = table.getState().pagination.pageIndex + 1
  const totalPages = Math.max(1, table.getPageCount())

  return (
    <div className={cn('flex flex-wrap items-center justify-between gap-4 px-2', className)}>
      <div className='flex items-center gap-2'>
        <Select
          value={`${table.getState().pagination.pageSize}`}
          onValueChange={(value) => table.setPageSize(Number(value))}
        >
          <SelectTrigger size='sm' className='h-8 w-18'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent side='top'>
            {[10, 20, 50, 100].map((size) => (
              <SelectItem key={size} value={`${size}`}>
                {size}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className='hidden text-sm text-muted-foreground sm:block'>{t('table.rowsPerPage')}</p>
      </div>

      <div className='flex items-center gap-4'>
        <span className='text-sm text-muted-foreground'>
          {t('table.pageOf', { page: currentPage, total: totalPages })}
        </span>
        <div className='flex items-center gap-1'>
          <Button
            variant='outline'
            className='size-8 p-0 max-md:hidden'
            onClick={() => table.setPageIndex(0)}
            disabled={!table.getCanPreviousPage()}
            aria-label={t('table.firstPage')}
          >
            <ChevronsLeft />
          </Button>
          <Button
            variant='outline'
            className='size-8 p-0'
            onClick={() => table.previousPage()}
            disabled={!table.getCanPreviousPage()}
            aria-label={t('table.prevPage')}
          >
            <ChevronLeft />
          </Button>
          {pageNumbers(currentPage, totalPages).map((n, i) =>
            n === '…' ? (
              <span key={`gap-${i}`} className='px-1 text-sm text-muted-foreground'>
                …
              </span>
            ) : (
              <Button
                key={n}
                variant={currentPage === n ? 'default' : 'outline'}
                className='h-8 min-w-8 px-2'
                onClick={() => table.setPageIndex(n - 1)}
              >
                {n}
              </Button>
            )
          )}
          <Button
            variant='outline'
            className='size-8 p-0'
            onClick={() => table.nextPage()}
            disabled={!table.getCanNextPage()}
            aria-label={t('table.nextPage')}
          >
            <ChevronRight />
          </Button>
          <Button
            variant='outline'
            className='size-8 p-0 max-md:hidden'
            onClick={() => table.setPageIndex(table.getPageCount() - 1)}
            disabled={!table.getCanNextPage()}
            aria-label={t('table.lastPage')}
          >
            <ChevronsRight />
          </Button>
        </div>
      </div>
    </div>
  )
}
