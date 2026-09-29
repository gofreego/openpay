import { useParams } from 'react-router-dom'
import { ApiErrorAlert, EntityLink, KeyValue, LedgerCheckBanner, Money, PageHeader, Section, Statement } from '../../components'
import { useAsync } from '../../hooks'
import { ledgerService } from '../../services'
import { dateTime, enumLabel } from '../../utils/format'

export function AccountPage() {
  const { id = '' } = useParams()
  const account = useAsync(() => ledgerService.getAccount(id), [id])
  const a = account.data?.account
  if (account.error) return <ApiErrorAlert error={account.error} />
  if (!a) return null
  return (
    <>
      <PageHeader title={a.code} subtitle={<code>{a.id}</code>} />
      <LedgerCheckBanner />
      <KeyValue items={[
        ['Type', enumLabel(a.type, 'LEDGER_ACCOUNT_TYPE_')],
        ['Product', a.productId ? <EntityLink id={a.productId} /> : 'Platform'],
        ['Owner', `${a.ownerKind}${a.ownerId ? ` · ${a.ownerId}` : ''}`],
        ['Balance', <Money amount={a.balance?.balance} currency={a.currency} colored />],
        ['Held', <Money amount={a.balance?.held} currency={a.currency} />],
        ['Available', <Money amount={a.balance?.available} currency={a.currency} />],
        ['May go negative', a.allowNegative ? 'Yes' : 'No'],
        ['Status', a.status],
        ['Opened', dateTime(a.createdAt)],
      ]} />
      <Section title="Statement">
        <Statement
          currency={a.currency}
          load={async (cursor) => {
            const res = await ledgerService.statement(a.id, cursor || undefined)
            return { entries: res.entries, nextCursor: res.nextCursor }
          }}
          exportCsv={(from, to) => ledgerService.exportStatement(a.id, from, to)}
        />
      </Section>
    </>
  )
}
