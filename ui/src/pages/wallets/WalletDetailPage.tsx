import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { Alert, Box, Button, MenuItem, TextField } from '@mui/material'
import { useNotification } from '@gofreego/tsutils'
import { PostingDirection } from '../../apis/proto/openpay/v1/ledger'
import { WalletStatus, type Wallet } from '../../apis/proto/openpay/v1/wallet'
import {
  ApiErrorAlert, AuditPanel, ConfirmAction, EntityLink, JournalPreview, KeyValue, Money, PageHeader, Section, Statement, StatusChip,
} from '../../components'
import { Perm, useAsync, useConsole, usePermissions } from '../../hooks'
import { walletService } from '../../services'
import { Money as M } from '../../utils/money'
import { Reasons } from '../../utils/reasons'
import { dateTime } from '../../utils/format'

export function WalletDetailPage() {
  const { id = '' } = useParams()
  const [version, setVersion] = useState(0)
  const wallet = useAsync(() => walletService.get(id), [id, version])
  const w = wallet.data?.wallet
  if (wallet.error) return <ApiErrorAlert error={wallet.error} />
  if (!w) return null
  const moved = () => setVersion((v) => v + 1)

  return (
    <>
      <PageHeader
        title={`${w.walletTypeCode} wallet`}
        subtitle={<code>{w.id}</code>}
        action={<Box sx={{ display: 'flex', gap: 1 }}><GrantAction wallet={w} onDone={moved} /><AdjustAction wallet={w} onDone={moved} /></Box>}
      />
      {w.status === WalletStatus.WALLET_STATUS_FROZEN && (
        <Alert severity="error" sx={{ mb: 2 }}>Frozen: the customer's own operations are refused; adjustments still apply.</Alert>
      )}
      <KeyValue items={[
        ['Customer', <EntityLink id={w.customerId} />],
        ['Product', w.productId ? <EntityLink id={w.productId} /> : 'Platform'],
        ['Type', <EntityLink id={w.walletTypeId} label={w.walletTypeCode} />],
        ['Balance', <Money amount={w.balance?.balance} currency={w.currency} />],
        ['Held', <Money amount={w.balance?.held} currency={w.currency} />],
        ['Available', <Money amount={w.balance?.available} currency={w.currency} />],
        ['Status', <StatusChip status={w.status} />],
        ['Opened', dateTime(w.createdAt)],
      ]} />
      <Section title="Statement">
        <Statement
          currency={w.currency}
          version={version}
          load={async (cursor) => {
            const res = await walletService.statement(w.id, cursor || undefined)
            return { entries: res.entries, nextCursor: res.nextCursor }
          }}
          exportCsv={(from, to) => walletService.exportStatement(w.id, from, to)}
        />
      </Section>
      <AuditPanel resourceType="wallet" resourceId={w.id} />
    </>
  )
}

/** amountField reads a rupee amount; null while it is not a positive amount. */
function parsePositive(input: string, currency: string): bigint | null {
  try {
    const v = M.fromMajor(input, currency)
    return v > 0n ? v : null
  } catch {
    return null
  }
}

function productCode(wallet: Wallet, codes: { id: string; code: string }[], fundingId: string): string {
  return codes.find((p) => p.id === (wallet.productId || fundingId))?.code ?? '<product>'
}

function GrantAction({ wallet, onDone }: { wallet: Wallet; onDone: () => void }) {
  const permissions = usePermissions()
  const { products } = useConsole()
  const { showNotification } = useNotification()
  const [open, setOpen] = useState(false)
  const [amount, setAmount] = useState('')
  const [funding, setFunding] = useState('')
  if (!permissions.can(Perm.walletsGrant, wallet.productId || undefined)) return null

  const minor = parsePositive(amount, wallet.currency)
  const code = productCode(wallet, products, funding)
  return (
    <>
      <Button variant="outlined" onClick={() => { setAmount(''); setOpen(true) }}>Grant</Button>
      <ConfirmAction
        open={open}
        title="Grant value"
        description={<>
          Promotional value, paid for by the product's promotions budget. Only grantable wallet types accept it.
          <TextField fullWidth sx={{ mt: 2 }} label={`Amount (${wallet.currency})`} value={amount} onChange={(e) => setAmount(e.target.value)} />
          {!wallet.productId && (
            <TextField fullWidth select sx={{ mt: 2 }} label="Funded by" value={funding} onChange={(e) => setFunding(e.target.value)}>
              {products.map((p) => <MenuItem key={p.id} value={p.id}>{p.name}</MenuItem>)}
            </TextField>
          )}
        </>}
        amount={minor ? { minor: minor.toString(), currency: wallet.currency } : undefined}
        preview={minor && <JournalPreview currency={wallet.currency} legs={[
          { account: `expense:${code}:promotions`, debit: minor.toString() },
          { account: `wallet ${wallet.id}`, credit: minor.toString() },
        ]} />}
        reasonCodes={[...Reasons.grant]}
        confirmLabel="Grant"
        onClose={() => setOpen(false)}
        onConfirm={async ({ reasonCode, note, idempotencyKey }) => {
          if (!minor) throw new Error('Enter an amount')
          const res = await walletService.grant(wallet.id, {
            amount: minor.toString(), reasonCode, memo: note, fundingProductId: funding || undefined,
          }, idempotencyKey)
          showNotification(`Granted — journal ${res.journalId}`, 'success')
          onDone()
        }}
      />
    </>
  )
}

/**
 * AdjustAction is the correction path — including undoing a mistaken
 * adjustment, by adjusting the other way with reason error_correction.
 * Central ops only (plan.md U-D6); the server enforces it.
 */
function AdjustAction({ wallet, onDone }: { wallet: Wallet; onDone: () => void }) {
  const permissions = usePermissions()
  const { products } = useConsole()
  const { showNotification } = useNotification()
  const [open, setOpen] = useState(false)
  const [amount, setAmount] = useState('')
  const [direction, setDirection] = useState<PostingDirection>(PostingDirection.POSTING_DIRECTION_CREDIT)
  const [funding, setFunding] = useState('')
  if (!permissions.canPlatform(Perm.walletsAdjust)) return null

  const minor = parsePositive(amount, wallet.currency)
  const code = productCode(wallet, products, funding)
  const credit = direction === PostingDirection.POSTING_DIRECTION_CREDIT
  const expense = `expense:${code}:adjustments`
  const account = `wallet ${wallet.id}`
  return (
    <>
      <Button variant="outlined" color="warning" onClick={() => { setAmount(''); setOpen(true) }}>Adjust</Button>
      <ConfirmAction
        open={open}
        title="Adjust balance"
        destructive={!credit}
        description={<>
          Money no payment or grant explains. Every adjustment is audited with your name and reason; to undo one, adjust the
          other way with reason <i>error correction</i> and name the original journal in the note.
          <TextField fullWidth select sx={{ mt: 2 }} label="Direction" value={direction} onChange={(e) => setDirection(e.target.value as PostingDirection)}>
            <MenuItem value={PostingDirection.POSTING_DIRECTION_CREDIT}>Credit — add to the wallet</MenuItem>
            <MenuItem value={PostingDirection.POSTING_DIRECTION_DEBIT}>Debit — take from the wallet</MenuItem>
          </TextField>
          <TextField fullWidth sx={{ mt: 2 }} label={`Amount (${wallet.currency})`} value={amount} onChange={(e) => setAmount(e.target.value)} />
          {!wallet.productId && (
            <TextField fullWidth select sx={{ mt: 2 }} label="Adjustments account of" value={funding} onChange={(e) => setFunding(e.target.value)}>
              {products.map((p) => <MenuItem key={p.id} value={p.id}>{p.name}</MenuItem>)}
            </TextField>
          )}
        </>}
        amount={minor ? { minor: minor.toString(), currency: wallet.currency } : undefined}
        preview={minor && <JournalPreview currency={wallet.currency} legs={credit
          ? [{ account: expense, debit: minor.toString() }, { account, credit: minor.toString() }]
          : [{ account, debit: minor.toString() }, { account: expense, credit: minor.toString() }]} />}
        reasonCodes={[...Reasons.adjust]}
        confirmLabel={credit ? 'Credit wallet' : 'Debit wallet'}
        onClose={() => setOpen(false)}
        onConfirm={async ({ reasonCode, note, idempotencyKey }) => {
          if (!minor) throw new Error('Enter an amount')
          const res = await walletService.adjust(wallet.id, {
            amount: minor.toString(), direction, reasonCode, memo: note, productId: funding || undefined,
          }, idempotencyKey)
          showNotification(`Adjusted — journal ${res.journalId}`, 'success')
          onDone()
        }}
      />
    </>
  )
}
