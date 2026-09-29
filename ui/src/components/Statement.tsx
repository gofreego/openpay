import { useState } from 'react'
import { Box, Button, TextField } from '@mui/material'
import DownloadIcon from '@mui/icons-material/Download'
import type { StatementEntry } from '../apis/proto/openpay/v1/ledger'
import { useAsync } from '../hooks/useAsync'
import { dateTime } from '../utils/format'
import { toApiError, type ApiError } from '../utils/apiError'
import { DataTable } from './DataTable'
import { EntityLink } from './EntityLink'
import { Money } from './Money'
import { ApiErrorAlert } from './ApiErrorAlert'

interface StatementProps {
  currency: string
  /** Loads one page, newest first, from a cursor ("" for the newest). */
  load: (cursor: string) => Promise<{ entries: StatementEntry[]; nextCursor: string }>
  /** Downloads [from, to) as CSV, server-generated. */
  exportCsv: (from: string, to: string) => Promise<void>
  /** Changes when the underlying balance may have moved, to refetch. */
  version?: number
}

/**
 * Statement pages an account's postings newest first by the server's cursor,
 * with the running balance after each — the trail from a balance to the
 * journals that made it.
 */
export function Statement({ currency, load, exportCsv, version = 0 }: StatementProps) {
  // Cursors of the pages before the current one, for "Newer".
  const [stack, setStack] = useState<string[]>([])
  const [cursor, setCursor] = useState('')
  const page = useAsync(() => load(cursor), [cursor, version])

  // Default export period: the last 30 days.
  const [from, setFrom] = useState(() => new Date(Date.now() - 30 * 86_400_000).toISOString().slice(0, 10))
  const [to, setTo] = useState(() => new Date().toISOString().slice(0, 10))
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<ApiError | null>(null)

  const download = async () => {
    setExporting(true)
    setExportError(null)
    try {
      // The period is half-open; include the whole "to" day.
      const end = new Date(`${to}T00:00:00`)
      end.setDate(end.getDate() + 1)
      await exportCsv(new Date(`${from}T00:00:00`).toISOString(), end.toISOString())
    } catch (err) {
      setExportError(toApiError(err))
    } finally {
      setExporting(false)
    }
  }

  return (
    <>
      <DataTable<StatementEntry>
        rows={page.data?.entries ?? []}
        rowKey={(e) => `${e.journalId}:${e.direction}:${e.amount}`}
        loading={page.loading}
        error={page.error}
        emptyText="No movements"
        filters={
          <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
            <TextField size="small" type="date" label="From" value={from} onChange={(e) => setFrom(e.target.value)} InputLabelProps={{ shrink: true }} />
            <TextField size="small" type="date" label="To" value={to} onChange={(e) => setTo(e.target.value)} InputLabelProps={{ shrink: true }} />
            <Button size="small" startIcon={<DownloadIcon />} onClick={() => void download()} disabled={exporting}>
              {exporting ? 'Preparing…' : 'Export CSV'}
            </Button>
          </Box>
        }
        pagination={{
          mode: 'cursor',
          hasPrevious: stack.length > 0,
          hasNext: !!page.data?.nextCursor,
          onPrevious: () => { setCursor(stack[stack.length - 1]); setStack(stack.slice(0, -1)) },
          onNext: () => { setStack([...stack, cursor]); setCursor(page.data?.nextCursor ?? '') },
        }}
        columns={[
          { key: 'when', header: 'Posted', render: (e) => dateTime(e.postedAt) },
          { key: 'kind', header: 'Kind', render: (e) => e.journalKind },
          { key: 'memo', header: 'Memo', render: (e) => e.memo || e.journalExternalId },
          { key: 'journal', header: 'Journal', render: (e) => <EntityLink id={e.journalId} /> },
          { key: 'change', header: 'Change', align: 'right', render: (e) => <Money amount={e.change} currency={currency} signed colored /> },
          { key: 'balance', header: 'Balance after', align: 'right', render: (e) => <Money amount={e.balanceAfter} currency={currency} /> },
        ]}
      />
      {exportError && <Box sx={{ mt: 1 }}><ApiErrorAlert error={exportError} title="Export failed" /></Box>}
    </>
  )
}
