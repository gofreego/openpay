import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Checkbox, FormControlLabel, MenuItem, TextField } from '@mui/material'
import { LedgerAccountType, type LedgerAccount } from '../../apis/proto/openpay/v1/ledger'
import { DataTable, LedgerCheckBanner, Money, PageHeader, EntityLink } from '../../components'
import { useAsync, useConsole } from '../../hooks'
import { ledgerService } from '../../services'
import { enumLabel } from '../../utils/format'

const PAGE = 50

export function AccountsPage() {
  const navigate = useNavigate()
  const { selectedProduct, permissions } = useConsole()
  const [page, setPage] = useState(0)
  const [type, setType] = useState<LedgerAccountType | ''>('')
  const [codePrefix, setCodePrefix] = useState('')
  const [platformOnly, setPlatformOnly] = useState(false)
  const productId = selectedProduct === 'all' ? undefined : selectedProduct

  const accounts = useAsync(() => ledgerService.listAccounts({
    limit: PAGE, offset: page * PAGE, productId: platformOnly ? undefined : productId, platformOnly,
    type: type || undefined, codePrefix,
  }), [page, type, codePrefix, platformOnly, productId])

  return (
    <>
      <PageHeader title="Ledger accounts" subtitle="Read-only. Every balance is the sum of its postings; nothing here can be edited." />
      <LedgerCheckBanner />
      <DataTable<LedgerAccount>
        rows={accounts.data?.accounts ?? []}
        rowKey={(a) => a.id}
        loading={accounts.loading}
        error={accounts.error}
        onRowClick={(a) => navigate(`/ledger/accounts/${a.id}`)}
        filters={<>
          <TextField size="small" label="Code starts with" value={codePrefix} onChange={(e) => { setCodePrefix(e.target.value); setPage(0) }}
            placeholder="income:, psp:mock:" />
          <TextField size="small" select label="Type" value={type} sx={{ minWidth: 150 }} onChange={(e) => { setType(e.target.value as LedgerAccountType | ''); setPage(0) }}>
            <MenuItem value="">Any</MenuItem>
            {Object.values(LedgerAccountType).filter((t) => t !== LedgerAccountType.LEDGER_ACCOUNT_TYPE_UNSPECIFIED && t !== LedgerAccountType.UNRECOGNIZED)
              .map((t) => <MenuItem key={t} value={t}>{enumLabel(t, 'LEDGER_ACCOUNT_TYPE_')}</MenuItem>)}
          </TextField>
          {permissions.scopeAll && (
            <FormControlLabel control={<Checkbox checked={platformOnly} onChange={(_, v) => { setPlatformOnly(v); setPage(0) }} />}
              label="Platform accounts only" />
          )}
        </>}
        pagination={{ mode: 'offset', page, pageSize: PAGE, total: Number.parseInt(accounts.data?.total ?? '0', 10), onPageChange: setPage }}
        columns={[
          { key: 'code', header: 'Code', render: (a) => <code>{a.code}</code> },
          { key: 'type', header: 'Type', render: (a) => enumLabel(a.type, 'LEDGER_ACCOUNT_TYPE_') },
          { key: 'product', header: 'Product', render: (a) => a.productId ? <EntityLink id={a.productId} /> : 'Platform' },
          { key: 'balance', header: 'Balance', align: 'right', render: (a) => <Money amount={a.balance?.balance} currency={a.currency} colored /> },
        ]}
      />
    </>
  )
}
