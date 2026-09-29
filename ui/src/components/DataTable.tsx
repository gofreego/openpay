import type { ReactNode } from 'react'
import {
  Box, Button, CircularProgress, Paper, Table, TableBody, TableCell, TableContainer,
  TableHead, TablePagination, TableRow, Typography,
} from '@mui/material'
import DownloadIcon from '@mui/icons-material/Download'
import NavigateBeforeIcon from '@mui/icons-material/NavigateBefore'
import NavigateNextIcon from '@mui/icons-material/NavigateNext'
import type { ApiError } from '../utils/apiError'
import { ApiErrorAlert } from './ApiErrorAlert'

export interface Column<T> {
  key: string
  header: string
  render: (row: T) => ReactNode
  align?: 'left' | 'right' | 'center'
  width?: number | string
}

/**
 * Pagination as the server does it: limit/offset for listings, an opaque
 * cursor for statements. Either way the server pages; the table never holds
 * more than one page.
 */
export type Pagination =
  | { mode: 'offset'; page: number; pageSize: number; total: number; onPageChange: (page: number) => void; onPageSizeChange?: (size: number) => void }
  | { mode: 'cursor'; hasPrevious: boolean; hasNext: boolean; onPrevious: () => void; onNext: () => void }

interface DataTableProps<T> {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  loading?: boolean
  error?: ApiError | null
  /** What an empty result means here, e.g. "No payments in this period". */
  emptyText?: string
  /** Filters for this table, rendered above it. */
  filters?: ReactNode
  pagination?: Pagination
  onRowClick?: (row: T) => void
  /** Offers an export button; the screen decides what exporting means. */
  onExport?: () => void
}

export function DataTable<T>({
  columns, rows, rowKey, loading = false, error = null, emptyText = 'Nothing here yet',
  filters, pagination, onRowClick, onExport,
}: DataTableProps<T>) {
  return (
    <Paper variant="outlined">
      {(filters || onExport) && (
        <Box sx={{ display: 'flex', gap: 2, p: 2, alignItems: 'center', flexWrap: 'wrap' }}>
          <Box sx={{ display: 'flex', gap: 2, flex: 1, flexWrap: 'wrap' }}>{filters}</Box>
          {onExport && (
            <Button size="small" startIcon={<DownloadIcon />} onClick={onExport} disabled={loading}>
              Export CSV
            </Button>
          )}
        </Box>
      )}
      {error && <Box sx={{ p: 2 }}><ApiErrorAlert error={error} /></Box>}
      <TableContainer>
        <Table size="small">
          <TableHead>
            <TableRow>
              {columns.map((c) => (
                <TableCell key={c.key} align={c.align} sx={{ width: c.width, fontWeight: 600 }}>{c.header}</TableCell>
              ))}
            </TableRow>
          </TableHead>
          <TableBody>
            {loading && (
              <TableRow>
                <TableCell colSpan={columns.length} align="center" sx={{ py: 4 }}><CircularProgress size={24} /></TableCell>
              </TableRow>
            )}
            {!loading && !error && rows.length === 0 && (
              <TableRow>
                <TableCell colSpan={columns.length} align="center" sx={{ py: 4 }}>
                  <Typography color="text.secondary">{emptyText}</Typography>
                </TableCell>
              </TableRow>
            )}
            {!loading && rows.map((row) => (
              <TableRow key={rowKey(row)} hover={!!onRowClick} onClick={onRowClick ? () => onRowClick(row) : undefined}
                sx={{ cursor: onRowClick ? 'pointer' : undefined }}>
                {columns.map((c) => <TableCell key={c.key} align={c.align}>{c.render(row)}</TableCell>)}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
      {pagination?.mode === 'offset' && (
        <TablePagination
          component="div"
          count={pagination.total}
          page={pagination.page}
          rowsPerPage={pagination.pageSize}
          onPageChange={(_, page) => pagination.onPageChange(page)}
          rowsPerPageOptions={pagination.onPageSizeChange ? [10, 25, 50, 100] : [pagination.pageSize]}
          onRowsPerPageChange={(e) => pagination.onPageSizeChange?.(Number.parseInt(e.target.value, 10))}
        />
      )}
      {pagination?.mode === 'cursor' && (
        <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: 1, p: 1 }}>
          <Button size="small" startIcon={<NavigateBeforeIcon />} disabled={!pagination.hasPrevious || loading} onClick={pagination.onPrevious}>
            Newer
          </Button>
          <Button size="small" endIcon={<NavigateNextIcon />} disabled={!pagination.hasNext || loading} onClick={pagination.onNext}>
            Older
          </Button>
        </Box>
      )}
    </Paper>
  )
}
