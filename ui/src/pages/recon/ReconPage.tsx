import { useState } from 'react'
import { Alert, Box, Button, Card, CardContent, Chip, Grid, MenuItem, TextField, Typography } from '@mui/material'
import { useNotification } from '@gofreego/tsutils'
import { BreakAction, type ReconBreak, type Settlement } from '../../apis/proto/openpay/v1/recon'
import { ApiErrorAlert, ConfirmAction, DataTable, EntityLink, JournalPreview, Money, PageHeader, Section, StatusChip } from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { reconService } from '../../services'
import { dateTime } from '../../utils/format'
import { toApiError, type ApiError } from '../../utils/apiError'
import { Reasons } from '../../utils/reasons'
import { Money as M } from '../../utils/money'

const CLASSES = ['missing_in_ledger', 'missing_at_provider', 'amount_mismatch', 'fee_mismatch', 'duplicate']
const AGED_MS = 48 * 3_600_000

const CLASS_HELP: Record<string, string> = {
  missing_in_ledger: 'The provider settled money we have no record of. Sync the payment, then force-match.',
  missing_at_provider: 'We captured it; the provider has not settled it yet. Usually timing.',
  amount_mismatch: 'Settled amount differs from captured. Confirm with the provider.',
  fee_mismatch: 'The fee differs from what we expected. Finance should check the rate card.',
  duplicate: 'The same payment settled twice. Claim it back from the provider.',
}

export function ReconPage() {
  const permissions = usePermissions()
  const summary = useAsync(() => reconService.summary(), [])
  const [running, setRunning] = useState(false)
  const [runError, setRunError] = useState<ApiError | null>(null)
  const [version, setVersion] = useState(0)
  const s = summary.data?.summary

  const run = async () => {
    setRunning(true)
    setRunError(null)
    try {
      await reconService.runCycle()
      summary.reload()
      setVersion((v) => v + 1)
    } catch (err) {
      setRunError(toApiError(err))
    } finally {
      setRunning(false)
    }
  }

  return (
    <>
      <PageHeader title="Reconciliation" subtitle="Provider settlements matched to our ledger. Suspense should trend to zero; every break needs a decision."
        action={permissions.canPlatform(Perm.reconManage) && <Button variant="contained" disabled={running} onClick={() => void run()}>{running ? 'Running…' : 'Run cycle now'}</Button>} />
      {summary.error && <ApiErrorAlert error={summary.error} />}
      {runError && <ApiErrorAlert error={runError} title="Cycle failed" />}
      {s && (
        <Grid container spacing={2} sx={{ mb: 2 }}>
          <Grid size={{ xs: 12, md: 3 }}><Stat label="Open breaks" value={s.openBreaks} /></Grid>
          <Grid size={{ xs: 12, md: 3 }}><Stat label="Aged breaks (need a person)" value={s.agedBreaks} bad={s.agedBreaks !== '0'} /></Grid>
          <Grid size={{ xs: 12, md: 6 }}>
            <Card variant="outlined"><CardContent>
              <Typography variant="overline" color="text.secondary">Per provider</Typography>
              {Object.keys({ ...s.suspense, ...s.receivable }).map((p) => (
                <Typography key={p}>
                  <b>{p}</b> · suspense <Money amount={s.suspense[p]} currency="INR" colored /> · awaiting settlement <Money amount={s.receivable[p]} currency="INR" />
                </Typography>
              ))}
            </CardContent></Card>
          </Grid>
        </Grid>
      )}
      <Breaks version={version} onResolved={summary.reload} />
      <Settlements version={version} />
    </>
  )
}

function Stat({ label, value, bad }: { label: string; value: string; bad?: boolean }) {
  return (
    <Card variant="outlined" sx={{ borderColor: bad ? 'error.main' : undefined }}><CardContent>
      <Typography variant="overline" color="text.secondary">{label}</Typography>
      <Typography variant="h5" color={bad ? 'error' : undefined}>{value}</Typography>
    </CardContent></Card>
  )
}

function Breaks({ version, onResolved }: { version: number; onResolved: () => void }) {
  const permissions = usePermissions()
  const { showNotification } = useNotification()
  const [status, setStatus] = useState('open')
  const [classification, setClassification] = useState('')
  const breaks = useAsync(() => reconService.breaks({ status, limit: 100 }), [status, version])
  const [resolving, setResolving] = useState<{ brk: ReconBreak; action: BreakAction } | null>(null)
  const [paymentId, setPaymentId] = useState('')
  const all = breaks.data?.breaks ?? []
  const rows = classification ? all.filter((b) => b.classification === classification) : all
  const counts = Object.fromEntries(CLASSES.map((c) => [c, all.filter((b) => b.classification === c).length]))
  const canManage = permissions.canPlatform(Perm.reconManage)

  const titles: Record<string, string> = {
    [BreakAction.BREAK_ACTION_RESOLVE]: 'Resolve (explained, nothing to move)',
    [BreakAction.BREAK_ACTION_FORCE_MATCH]: 'Force-match to a payment',
    [BreakAction.BREAK_ACTION_WRITE_OFF]: 'Write off',
  }
  const preview = (b: ReconBreak, action: BreakAction) => {
    const from = b.classification === 'missing_at_provider' ? `psp:${b.provider}:receivable` : `psp:${b.provider}:suspense`
    if (action === BreakAction.BREAK_ACTION_FORCE_MATCH) {
      return <JournalPreview currency={b.currency} legs={[{ account: `psp:${b.provider}:suspense`, debit: b.amount }, { account: `psp:${b.provider}:receivable`, credit: b.amount }]} />
    }
    if (action === BreakAction.BREAK_ACTION_WRITE_OFF) {
      return <JournalPreview currency={b.currency} legs={[{ account: 'expense:reconciliation_writeoffs', debit: b.amount }, { account: from, credit: b.amount }]} />
    }
    return <Typography variant="body2">No money moves; the break is closed with your explanation.</Typography>
  }

  return (
    <Section title="Break queue">
      <Box sx={{ display: 'flex', gap: 1, mb: 1, flexWrap: 'wrap' }}>
        <Chip label={`All (${all.length})`} color={!classification ? 'primary' : 'default'} onClick={() => setClassification('')} />
        {CLASSES.map((c) => (
          <Chip key={c} label={`${c.replace(/_/g, ' ')} (${counts[c]})`} color={classification === c ? 'primary' : 'default'} onClick={() => setClassification(c)} />
        ))}
      </Box>
      {classification && <Alert severity="info" sx={{ mb: 1 }}>{CLASS_HELP[classification]}</Alert>}
      <DataTable<ReconBreak>
        rows={rows}
        rowKey={(b) => b.id}
        loading={breaks.loading}
        error={breaks.error}
        emptyText="No breaks — reconciled"
        filters={
          <TextField size="small" select label="Status" value={status} sx={{ minWidth: 160 }} onChange={(e) => setStatus(e.target.value)}>
            {['open', 'resolved', 'force_matched', 'written_off', ''].map((s) => <MenuItem key={s} value={s}>{s ? s.replace(/_/g, ' ') : 'Any'}</MenuItem>)}
          </TextField>
        }
        columns={[
          { key: 'age', header: 'Opened', render: (b) => {
            const aged = b.status === 'open' && b.createdAt && Date.now() - new Date(b.createdAt).getTime() > AGED_MS
            return <span style={{ color: aged ? 'red' : undefined, fontWeight: aged ? 700 : undefined }}>{dateTime(b.createdAt)}{aged ? ' · aged' : ''}</span>
          } },
          { key: 'class', header: 'Kind', render: (b) => b.classification.replace(/_/g, ' ') },
          { key: 'provider', header: 'Provider', render: (b) => b.provider },
          { key: 'amount', header: 'Amount', align: 'right', render: (b) => <Money amount={b.amount} currency={b.currency} /> },
          { key: 'payment', header: 'Payment', render: (b) => <EntityLink id={b.paymentId} /> },
          { key: 'detail', header: 'Detail', render: (b) => b.detail },
          { key: 'status', header: 'Status', render: (b) => b.status === 'open' ? <StatusChip status="open" /> : <>{b.status.replace(/_/g, ' ')} · {b.resolvedBy}: {b.note}</> },
          { key: 'act', header: '', align: 'right', render: (b) => canManage && b.status === 'open' && (
            <Box sx={{ display: 'flex', gap: 0.5, justifyContent: 'flex-end' }}>
              <Button size="small" onClick={() => setResolving({ brk: b, action: BreakAction.BREAK_ACTION_RESOLVE })}>Resolve</Button>
              <Button size="small" onClick={() => { setPaymentId(b.paymentId); setResolving({ brk: b, action: BreakAction.BREAK_ACTION_FORCE_MATCH }) }}>Match</Button>
              <Button size="small" color="error" onClick={() => setResolving({ brk: b, action: BreakAction.BREAK_ACTION_WRITE_OFF })}>Write off</Button>
            </Box>
          ) },
        ]}
      />
      <ConfirmAction
        open={resolving !== null}
        title={resolving ? titles[resolving.action] : ''}
        destructive={resolving?.action === BreakAction.BREAK_ACTION_WRITE_OFF}
        description={resolving && <>
          {resolving.brk.classification.replace(/_/g, ' ')} at {resolving.brk.provider}: {resolving.brk.detail}
          {resolving.action === BreakAction.BREAK_ACTION_FORCE_MATCH && (
            <TextField fullWidth sx={{ mt: 2 }} label="Payment the money belongs to" value={paymentId} onChange={(e) => setPaymentId(e.target.value.trim())}
              helperText="Verify with the provider first: a wrong match hides two errors behind one clean line." />
          )}
          {resolving.action === BreakAction.BREAK_ACTION_WRITE_OFF && (
            <Alert severity="warning" sx={{ mt: 2 }}>Money written off is lost and shows in the P&L. Only with finance's agreement.</Alert>
          )}
        </>}
        amount={resolving && resolving.action !== BreakAction.BREAK_ACTION_RESOLVE ? { minor: resolving.brk.amount, currency: resolving.brk.currency } : undefined}
        preview={resolving && preview(resolving.brk, resolving.action)}
        reasonCodes={[...Reasons.recon]}
        confirmLabel={resolving ? titles[resolving.action].split(' (')[0] : ''}
        onClose={() => setResolving(null)}
        onConfirm={async ({ reasonCode, note, idempotencyKey }) => {
          await reconService.resolve(resolving!.brk.id, {
            action: resolving!.action, reasonCode, note,
            paymentId: resolving!.action === BreakAction.BREAK_ACTION_FORCE_MATCH ? paymentId : undefined,
          }, idempotencyKey)
          showNotification('Break closed', 'success')
          breaks.reload()
          onResolved()
        }}
      />
    </Section>
  )
}

function Settlements({ version }: { version: number }) {
  const [provider, setProvider] = useState('')
  const settlements = useAsync(() => reconService.settlements({ provider: provider || undefined, limit: 50 }), [provider, version])
  return (
    <Section title="Settlements">
      <DataTable<Settlement>
        rows={settlements.data?.settlements ?? []}
        rowKey={(s) => s.id}
        loading={settlements.loading}
        error={settlements.error}
        emptyText="No settlements ingested yet"
        filters={<TextField size="small" label="Provider" value={provider} onChange={(e) => setProvider(e.target.value.trim())} />}
        columns={[
          { key: 'when', header: 'Settled', render: (s) => dateTime(s.settledAt) },
          { key: 'provider', header: 'Provider', render: (s) => `${s.provider} · ${s.providerSettlementId}` },
          { key: 'gross', header: 'Gross', align: 'right', render: (s) => <Money amount={s.gross} currency={s.currency} /> },
          { key: 'fees', header: 'Fees + tax', align: 'right', render: (s) => <Money amount={M.add(s.fees, s.feeTax)} currency={s.currency} /> },
          { key: 'net', header: 'Net to bank', align: 'right', render: (s) => <Money amount={s.net} currency={s.currency} /> },
          { key: 'items', header: 'Lines', align: 'right', render: (s) => s.itemCount },
          { key: 'bank', header: 'Bank ref', render: (s) => `${s.bank} · ${s.bankReference}` },
          { key: 'status', header: 'Status', render: (s) => <StatusChip status={s.status} /> },
        ]}
      />
    </Section>
  )
}
