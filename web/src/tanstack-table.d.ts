import '@tanstack/react-table'

declare module '@tanstack/react-table' {
  // The generics must match the library's declaration to merge with it.
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  interface ColumnMeta<TData, TValue> {
    /** Applied to both th and td. */
    className?: string
    tdClassName?: string
    thClassName?: string
    /** Translated column name, shown in the column visibility menu. */
    title?: string
  }
}
