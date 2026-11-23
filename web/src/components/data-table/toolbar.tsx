import { type Table } from '@tanstack/react-table'
import { X } from 'lucide-react'
import { useT } from '@/lib/i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { DataTableFacetedFilter, type FacetOption } from './faceted-filter'
import { DataTableViewOptions } from './view-options'

type DataTableToolbarProps<TData> = {
  table: Table<TData>
  searchPlaceholder: string
  filters?: { columnId: string; title: string; options: FacetOption[] }[]
  /** Sits between the filters and the column menu, e.g. a result count. */
  children?: React.ReactNode
}

/** Search across the table's global filter, faceted column filters, reset and column visibility. */
export function DataTableToolbar<TData>({
  table,
  searchPlaceholder,
  filters = [],
  children,
}: DataTableToolbarProps<TData>) {
  const t = useT()
  const globalFilter: unknown = table.getState().globalFilter
  const search = typeof globalFilter === 'string' ? globalFilter : ''
  const isFiltered = table.getState().columnFilters.length > 0 || search !== ''

  return (
    <div className='flex flex-wrap items-center justify-between gap-2'>
      <div className='flex flex-1 flex-wrap items-center gap-2'>
        <Input
          placeholder={searchPlaceholder}
          value={search}
          onChange={(event) => table.setGlobalFilter(event.target.value)}
          className='h-8 w-full sm:w-56 lg:w-64'
        />
        {filters.map((filter) => {
          const column = table.getColumn(filter.columnId)
          if (!column) return null
          return (
            <DataTableFacetedFilter
              key={filter.columnId}
              column={column}
              title={filter.title}
              options={filter.options}
            />
          )
        })}
        {isFiltered && (
          <Button
            variant='ghost'
            onClick={() => {
              table.resetColumnFilters()
              table.setGlobalFilter('')
            }}
            className='h-8 px-2 lg:px-3'
          >
            {t('table.reset')}
            <X className='size-4' />
          </Button>
        )}
      </div>
      {children}
      <DataTableViewOptions table={table} />
    </div>
  )
}
