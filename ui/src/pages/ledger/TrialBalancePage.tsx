import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Alert, Box, Checkbox, FormControlLabel, TextField } from '@mui/material'
import type { TrialBalanceLine } from '../../apis/proto/openpay/v1/ledger'
import { DataTable, LedgerCheckBanner, Money, PageHeader } from '../../components'
import { useAsync, useConsole } from '../../hooks'
import { ledgerService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'
import { Money as M } from '../../utils/money'

export function TrialBalancePage() {
  const navigate = useNavigate()
  const { selectedProduct } = useConsole()
  const [asOf, setAsOf] = useState('')
  const [includeEmpty, setIncludeEmpty] = useState(false)
  const productId = selectedProduct === 'all' ? undefined : selectedProduct
  const tb = useAsync(() => ledgerService.trialBalance({
    productId, includeEmpty, asOf: asOf ? new Date(asOf).toISOString() : undefined,
  }), [productId, includeEmpty, asOf])

  return (
    <>
      <PageHeader title="Trial balance" help="trial-balance" subtitle={`Every account's debits and credits${tb.data?.asOf ? ` as of ${dateTime(tb.data.asOf)}` : ''}. Each currency must net to zero.`} />
      <LedgerCheckBanner />
      {tb.data?.totals.map((t) => M.parse(t.difference) !== 0n && (
        <Alert key={t.currency} severity="error" sx={{ mb: 2 }}>
          {t.currency} does not balance: debits and credits differ by <Money amount={t.difference} currency={t.currency} />.
        </Alert>
      ))}
      <Box sx={{ display: 'flex', gap: 3, mb: 2 }}>
        {tb.data?.totals.map((t) => (
          <Box key={t.currency}>
            {t.currency}: debits <Money amount={t.debits} currency={t.currency} /> · credits <Money amount={t.credits} currency={t.currency} />
          </Box>
        ))}
      </Box>
      <DataTable<TrialBalanceLine>
        rows={tb.data?.lines ?? []}
        rowKey={(l) => l.accountId}
        loading={tb.loading}
        error={tb.error}
        onRowClick={(l) => navigate(`/ledger/accounts/${l.accountId}`)}
        filters={<>
          <TextField size="small" type="datetime-local" label="As of" value={asOf} onChange={(e) => setAsOf(e.target.value)} InputLabelProps={{ shrink: true }} />
          <FormControlLabel control={<Checkbox checked={includeEmpty} onChange={(_, v) => setIncludeEmpty(v)} />} label="Include empty accounts" />
        </>}
        columns={[
          { key: 'code', header: 'Account', render: (l) => <code>{l.accountCode}</code> },
          { key: 'type', header: 'Type', render: (l) => enumLabel(l.type, 'LEDGER_ACCOUNT_TYPE_') },
          { key: 'debits', header: 'Debits', align: 'right', render: (l) => <Money amount={l.debits} currency={l.currency} /> },
          { key: 'credits', header: 'Credits', align: 'right', render: (l) => <Money amount={l.credits} currency={l.currency} /> },
          { key: 'balance', header: 'Balance', align: 'right', render: (l) => <Money amount={l.balance} currency={l.currency} colored /> },
        ]}
      />
    </>
  )
}
