import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { Alert, Button, MenuItem, TextField } from '@mui/material'
import { WalletScope, WalletTypeStatus, type WalletType } from '../../apis/proto/openpay/v1/wallet_type'
import { ApiErrorAlert, AuditPanel, ConfirmAction, EntityLink, KeyValue, Money, PageHeader, StatusChip } from '../../components'
import { Perm, useAsync, usePermissions } from '../../hooks'
import { walletTypeService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'
import { capabilitySummary } from './walletTypeSummary'
import { LimitsFields } from './LimitsFields'
import { limitsDraft, toLimits } from './limits'

export function WalletTypeDetailPage() {
  const { id = '' } = useParams()
  const wt = useAsync(() => walletTypeService.get(id), [id])
  const t = wt.data?.walletType
  if (wt.error) return <ApiErrorAlert error={wt.error} />
  if (!t) return null

  const limit = (v: string | undefined) => (!v || v === '0' ? 'No limit' : <Money amount={v} currency={t.currency} />)
  const c = t.capabilities
  return (
    <>
      <PageHeader title={`${t.name} (${t.code})`} subtitle={capabilitySummary(t)} action={<EditWalletType type={t} onSaved={wt.reload} />} />
      {c?.withdrawable && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          Cash-out enabled — approved by {t.withdrawalApproval?.approvedBy} on {dateTime(t.withdrawalApproval?.approvedAt)}
          {' '}(ref {t.withdrawalApproval?.reference}).
        </Alert>
      )}
      <KeyValue items={[
        ['Id', <code>{t.id}</code>],
        ['Belongs to', t.scope === WalletScope.WALLET_SCOPE_PLATFORM ? 'Platform' : <EntityLink id={t.productId} />],
        ['Status', <StatusChip status={t.status} />],
        ['Currency', t.currency],
        ['Capabilities', ['fundable', 'grantable', 'transferable', 'withdrawable', 'refundableToSource']
          .filter((k) => c?.[k as keyof typeof c]).join(', ') || 'none'],
        ['Expiry', `${enumLabel(t.expiryPolicy, 'EXPIRY_POLICY_')}${t.expiryDays ? ` · ${t.expiryDays} days` : ''}`],
        ['Maximum balance', limit(t.limits?.maxBalance)],
        ['Maximum per transaction', limit(t.limits?.maxTxnAmount)],
        ['Daily load limit', limit(t.limits?.dailyLoadLimit)],
        ...(c?.withdrawable ? [
          ['Minimum withdrawal', limit(t.limits?.minWithdrawalAmount)],
          ['Approval above', limit(t.limits?.withdrawalApprovalThreshold)],
          ['Daily withdrawal limit', limit(t.limits?.dailyWithdrawalLimit)],
        ] as [string, React.ReactNode][] : []),
        ['Created', dateTime(t.createdAt)],
      ]} />
      <AuditPanel resourceType="wallet_type" resourceId={t.id} />
    </>
  )
}

function EditWalletType({ type, onSaved }: { type: WalletType; onSaved: () => void }) {
  const permissions = usePermissions()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState(type.name)
  const [status, setStatus] = useState(type.status)
  const [limits, setLimits] = useState(limitsDraft(type.limits, type.currency))
  const [formError, setFormError] = useState<string | null>(null)
  if (!permissions.canPlatform(Perm.walletTypesWrite)) return null

  return (
    <>
      <Button variant="outlined" onClick={() => {
        setName(type.name); setStatus(type.status); setLimits(limitsDraft(type.limits, type.currency)); setOpen(true)
      }}>Edit</Button>
      <ConfirmAction
        open={open}
        title={`Edit ${type.code}`}
        description={<>
          Only the name, status and limits can change. Scope, currency and capabilities are fixed once wallets exist.
          <TextField fullWidth sx={{ mt: 2 }} label="Name" value={name} onChange={(e) => setName(e.target.value)} />
          <TextField fullWidth select sx={{ mt: 2 }} label="Status" value={status} onChange={(e) => setStatus(e.target.value as WalletTypeStatus)}
            helperText="Archived types open no new wallets; existing wallets keep working.">
            <MenuItem value={WalletTypeStatus.WALLET_TYPE_STATUS_ACTIVE}>Active</MenuItem>
            <MenuItem value={WalletTypeStatus.WALLET_TYPE_STATUS_ARCHIVED}>Archived</MenuItem>
          </TextField>
          <LimitsFields draft={limits} onChange={setLimits} withdrawable={!!type.capabilities?.withdrawable} currency={type.currency} />
          {formError && <Alert severity="error" sx={{ mt: 2 }}>{formError}</Alert>}
        </>}
        requireNote={false}
        confirmLabel="Save"
        onClose={() => setOpen(false)}
        onConfirm={async ({ idempotencyKey }) => {
          setFormError(null)
          let parsed
          try {
            parsed = toLimits(limits, type.currency)
          } catch (err) {
            setFormError((err as Error).message)
            throw err
          }
          await walletTypeService.update(type.id, { name, status, limits: parsed }, idempotencyKey)
          onSaved()
        }}
      />
    </>
  )
}
