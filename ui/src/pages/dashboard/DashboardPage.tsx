import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { Box, Card, CardActionArea, CardContent, Grid, Typography } from '@mui/material'
import { LedgerCheckStatus } from '../../apis/proto/openpay/v1/ledger'
import { PaymentStatus } from '../../apis/proto/openpay/v1/payment'
import { WithdrawalStatus } from '../../apis/proto/openpay/v1/withdrawal'
import { DisputeStatus } from '../../apis/proto/openpay/v1/payment'
import { Money, PageHeader } from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import {
  disputeService, ledgerService, paymentService, reconService, reportService, walletService, withdrawalService,
} from '../../services'
import { dateTime, isoDaysAgo } from '../../utils/format'
import { Money as M } from '../../utils/money'

type Tone = 'ok' | 'warn' | 'bad' | undefined

function Kpi({ label, value, hint, tone, to }: { label: string; value: ReactNode; hint?: ReactNode; tone?: Tone; to?: string }) {
  const navigate = useNavigate()
  const color = tone === 'bad' ? 'error.main' : tone === 'warn' ? 'warning.main' : tone === 'ok' ? 'success.main' : undefined
  const body = (
    <CardContent>
      <Typography variant="overline" color="text.secondary">{label}</Typography>
      <Typography variant="h5" sx={{ color }}>{value}</Typography>
      {hint && <Typography variant="body2" color="text.secondary">{hint}</Typography>}
    </CardContent>
  )
  return (
    <Card variant="outlined" sx={{ height: '100%', borderColor: color }}>
      {to ? <CardActionArea sx={{ height: '100%' }} onClick={() => navigate(to)}>{body}</CardActionArea> : body}
    </Card>
  )
}

/**
 * DashboardPage answers "is payments healthy right now?" at a glance: each
 * card is green, amber or red, and opens the screen to act on it.
 */
export function DashboardPage() {
  const { permissions, selectedProduct } = useConsole()
  const p = usePermissions()
  const productId = selectedProduct === 'all' ? undefined : selectedProduct
  const now = new Date().toISOString()

  const stats = useAsync(() => p.can(Perm.paymentsRead) ? reportService.providerStats(isoDaysAgo(1 / 24), now, productId) : Promise.resolve(null), [productId])
  const stuck = useAsync(() => p.can(Perm.paymentsRead) ? paymentService.list({ status: PaymentStatus.PAYMENT_STATUS_PENDING, productId, limit: 1 }) : Promise.resolve(null), [productId])
  const float = useAsync(() => p.can(Perm.walletsRead) ? walletService.floatHeld() : Promise.resolve(null), [])
  const recon = useAsync(() => p.canPlatform(Perm.reconRead) ? reconService.summary() : Promise.resolve(null), [])
  const checks = useAsync(() => p.can(Perm.ledgerRead) ? ledgerService.checkRuns(1) : Promise.resolve(null), [])
  const approvals = useAsync(() => p.can(Perm.withdrawalsRead) ? withdrawalService.list({ status: WithdrawalStatus.WITHDRAWAL_STATUS_PENDING_APPROVAL, limit: 100 }) : Promise.resolve(null), [])
  const disputes = useAsync(() => p.can(Perm.paymentsRead) ? disputeService.list({ status: DisputeStatus.DISPUTE_STATUS_OPEN, limit: 100 }) : Promise.resolve(null), [])

  const lastCheck = checks.data?.runs[0]
  const floatTotal = float.data?.lines.filter((l) => !productId || l.productId === productId).map((l) => l.amount) ?? []
  const suspense = recon.data?.summary ? M.add(...Object.values(recon.data.summary.suspense)) : 0n
  const stuckCount = Number.parseInt(stuck.data?.total ?? '0', 10)

  return (
    <>
      <PageHeader title="OpenPay" help="dashboard" subtitle={`Health right now${permissions.scopeAll && !productId ? ', across every product' : ''}.`} />
      <Grid container spacing={2}>
        {(stats.data?.providers ?? []).map((s) => {
          const finished = ['captured', 'failed', 'expired', 'cancelled'].reduce((n, k) => n + Number.parseInt(s[k as 'captured'], 10), 0)
          const rate = s.successRateBps / 100
          return (
            <Grid key={s.provider} size={{ xs: 12, sm: 6, md: 3 }}>
              <Kpi label={`${s.provider} success, last hour`} to="/providers"
                value={finished ? `${rate}%` : '—'}
                tone={!finished ? undefined : rate >= 90 ? 'ok' : rate >= 70 ? 'warn' : 'bad'}
                hint={`${finished} finished · ${s.open} open${s.topFailures[0] ? ` · top failure ${s.topFailures[0].code}` : ''}`} />
            </Grid>
          )
        })}
        {stuck.data && (
          <Grid size={{ xs: 12, sm: 6, md: 3 }}>
            <Kpi label="Payments pending" value={stuckCount} tone={stuckCount > 0 ? 'warn' : 'ok'} to="/payments?status=PAYMENT_STATUS_PENDING"
              hint="The poller settles these within minutes; old ones need a sync" />
          </Grid>
        )}
        {lastCheck && (
          <Grid size={{ xs: 12, sm: 6, md: 3 }}>
            <Kpi label="Ledger" to="/ledger/checks"
              value={lastCheck.status === LedgerCheckStatus.LEDGER_CHECK_STATUS_OK ? 'Balanced' : lastCheck.status === LedgerCheckStatus.LEDGER_CHECK_STATUS_DRIFT ? `Drift: ${lastCheck.findings.length}` : 'Check failed'}
              tone={lastCheck.status === LedgerCheckStatus.LEDGER_CHECK_STATUS_OK ? 'ok' : 'bad'}
              hint={`Checked ${dateTime(lastCheck.finishedAt ?? lastCheck.startedAt)}`} />
          </Grid>
        )}
        {float.data && (
          <Grid size={{ xs: 12, sm: 6, md: 3 }}>
            <Kpi label="Customer money held" value={<Money amount={M.add(...floatTotal)} currency="INR" />}
              hint="Balances of fundable wallets — what we owe customers" />
          </Grid>
        )}
        {recon.data?.summary && (
          <>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <Kpi label="Suspense" value={<Money amount={suspense} currency="INR" />} tone={suspense === 0n ? 'ok' : 'warn'} to="/recon"
                hint="Provider money not yet matched; should trend to zero" />
            </Grid>
            <Grid size={{ xs: 12, sm: 6, md: 3 }}>
              <Kpi label="Recon breaks" value={recon.data.summary.openBreaks} to="/recon"
                tone={recon.data.summary.agedBreaks !== '0' ? 'bad' : recon.data.summary.openBreaks !== '0' ? 'warn' : 'ok'}
                hint={`${recon.data.summary.agedBreaks} aged past 48h`} />
            </Grid>
          </>
        )}
        {approvals.data && (
          <Grid size={{ xs: 12, sm: 6, md: 3 }}>
            <Kpi label="Withdrawals awaiting approval" value={approvals.data.withdrawals.length} to="/withdrawals"
              tone={approvals.data.withdrawals.length ? 'warn' : 'ok'} />
          </Grid>
        )}
        {disputes.data && (
          <Grid size={{ xs: 12, sm: 6, md: 3 }}>
            <Kpi label="Open disputes" value={disputes.data.disputes.length} to="/disputes"
              tone={disputes.data.disputes.length ? 'warn' : 'ok'} hint="Waiting for our evidence" />
          </Grid>
        )}
      </Grid>
      <Box sx={{ mt: 2 }}>
        <Typography variant="body2" color="text.secondary">Figures load live; open a card for the detail and the actions.</Typography>
      </Box>
    </>
  )
}
