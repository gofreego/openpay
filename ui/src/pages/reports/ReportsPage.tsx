import { useState } from 'react'
import { Box, Paper, Table, TableBody, TableCell, TableHead, TableRow, TextField, Typography } from '@mui/material'
import type { ProviderStats } from '../../apis/proto/openpay/v1/report'
import { ApiErrorAlert, DataTable, EntityLink, Money, PageHeader, Section } from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { reportService } from '../../services'

function firstOfMonth(): string {
  const d = new Date()
  return new Date(d.getFullYear(), d.getMonth(), 1).toISOString().slice(0, 10)
}

export function ReportsPage() {
  const { selectedProduct } = useConsole()
  const permissions = usePermissions()
  const [from, setFrom] = useState(firstOfMonth)
  const [to, setTo] = useState(() => new Date().toISOString().slice(0, 10))
  const productId = selectedProduct === 'all' ? undefined : selectedProduct
  const start = new Date(`${from}T00:00:00`).toISOString()
  const endDate = new Date(`${to}T00:00:00`)
  endDate.setDate(endDate.getDate() + 1)
  const end = endDate.toISOString()

  const pnl = useAsync(() => permissions.can(Perm.ledgerRead) ? reportService.pnl(start, end, productId) : Promise.resolve(null), [start, end, productId])
  const stats = useAsync(() => permissions.can(Perm.paymentsRead) ? reportService.providerStats(start, end, productId) : Promise.resolve(null), [start, end, productId])

  return (
    <>
      <PageHeader title="Reports" subtitle="Read straight off the ledger and payment records, so they agree with every statement." />
      <Box sx={{ display: 'flex', gap: 2, mb: 2 }}>
        <TextField size="small" type="date" label="From" value={from} onChange={(e) => setFrom(e.target.value)} InputLabelProps={{ shrink: true }} />
        <TextField size="small" type="date" label="To (inclusive)" value={to} onChange={(e) => setTo(e.target.value)} InputLabelProps={{ shrink: true }} />
      </Box>
      <Section title="Income and expense by product">
        {pnl.error && <ApiErrorAlert error={pnl.error} />}
        {(pnl.data?.products ?? []).map((p) => (
          <Paper key={p.productId + p.currency} variant="outlined" sx={{ mb: 2 }}>
            <Box sx={{ p: 2, display: 'flex', gap: 3, alignItems: 'baseline' }}>
              <Typography variant="subtitle1" sx={{ flex: 1 }}><EntityLink id={p.productId} label={p.productCode} /></Typography>
              <span>Income <Money amount={p.income} currency={p.currency} /></span>
              <span>Expense <Money amount={p.expense} currency={p.currency} /></span>
              <b>Net <Money amount={p.net} currency={p.currency} colored /></b>
            </Box>
            <Table size="small">
              <TableHead><TableRow><TableCell>Account</TableCell><TableCell align="right">Amount</TableCell></TableRow></TableHead>
              <TableBody>
                {p.lines.map((l) => (
                  <TableRow key={l.accountCode}>
                    <TableCell><code>{l.accountCode}</code></TableCell>
                    <TableCell align="right"><Money amount={l.amount} currency={p.currency} colored /></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Paper>
        ))}
      </Section>
      <Section title="Provider success">
        <DataTable<ProviderStats>
          rows={stats.data?.providers ?? []}
          rowKey={(s) => s.provider}
          loading={stats.loading}
          error={stats.error}
          emptyText="No payment attempts in this period"
          columns={[
            { key: 'provider', header: 'Provider', render: (s) => s.provider },
            { key: 'rate', header: 'Success', align: 'right', render: (s) => `${s.successRateBps / 100}%` },
            { key: 'attempts', header: 'Attempts', align: 'right', render: (s) => s.attempts },
            { key: 'captured', header: 'Captured', align: 'right', render: (s) => s.captured },
            { key: 'failed', header: 'Failed', align: 'right', render: (s) => s.failed },
            { key: 'expired', header: 'Expired', align: 'right', render: (s) => s.expired },
            { key: 'amount', header: 'Captured amount', align: 'right', render: (s) => <Money amount={s.capturedAmount} currency="INR" /> },
            { key: 'top', header: 'Top failures', render: (s) => s.topFailures.map((f) => `${f.code} ×${f.count}`).join(', ') },
          ]}
        />
      </Section>
    </>
  )
}
