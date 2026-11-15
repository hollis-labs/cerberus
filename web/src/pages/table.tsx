import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@hollis-labs/design-components"
import type { ReactNode } from "react"

/** A plain table: headings, then one row per item. */
export function DataTable<T>({
  label,
  head,
  rows,
  row,
  empty,
}: {
  label: string
  head: string[]
  rows: T[]
  row: (item: T) => ReactNode[]
  empty: string
}) {
  if (rows.length === 0) return <p className="text-fg-muted">{empty}</p>
  return (
    <Table aria-label={label}>
      <TableHeader>
        <TableRow>
          {head.map((h) => (
            <TableHead key={h}>{h}</TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((item, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: rows have no stable id in every list
          <TableRow key={i}>
            {row(item).map((cell, j) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: cells are positional
              <TableCell key={j}>{cell}</TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

export const dash = (v: string | number | undefined | null) =>
  v === undefined || v === null || v === "" ? "-" : String(v)

/** One row's cells, in heading order. */
export const cells = (...c: ReactNode[]): ReactNode[] => c
