import { useParams } from 'react-router-dom'
import { Alert, Paper, Table, TableBody, TableCell, TableHead, TableRow } from '@mui/material'
import { PostingDirection } from '../../apis/proto/openpay/v1/ledger'
import { ApiErrorAlert, EntityLink, KeyValue, Money, PageHeader, Section } from '../../components'
import { useAsync } from '../../hooks'
import { ledgerService } from '../../services'
import { dateTime } from '../../utils/format'
import { Money as M } from '../../utils/money'

/**
 * JournalPage shows one journal's postings as debits and credits that
 * visibly sum to the same figure, with links to what caused it and to what
 * it reverses.
 */
export function JournalPage() {
  const { id = '' } = useParams()
  const journal = useAsync(() => ledgerService.journal(id), [id])
  const j = journal.data?.journal
  if (journal.error) return <ApiErrorAlert error={journal.error} />
  if (!j) return null

  const debit = (d: PostingDirection) => d === PostingDirection.POSTING_DIRECTION_DEBIT
  const currencies = [...new Set(j.postings.map((p) => p.currency))]
  const totals = currencies.map((c) => ({
    currency: c,
    debits: M.add(...j.postings.filter((p) => p.currency === c && debit(p.direction)).map((p) => p.amount)),
    credits: M.add(...j.postings.filter((p) => p.currency === c && !debit(p.direction)).map((p) => p.amount)),
  }))
  const balanced = totals.every((t) => t.debits === t.credits)
  const source = j.sourceKind && j.sourceId ? `${j.sourceKind} ${j.sourceId}` : ''

  return (
    <>
      <PageHeader title={`Journal · ${j.kind}`} subtitle={<code>{j.id}</code>} />
      {!balanced && <Alert severity="error" sx={{ mb: 2 }}>This journal does not balance. That should be impossible — raise a ledger-drift incident.</Alert>}
      <KeyValue items={[
        ['External id', <code>{j.externalId}</code>],
        ['Kind', j.kind],
        ['Product', j.productId ? <EntityLink id={j.productId} /> : 'Platform'],
        ['Caused by', source ? <EntityLink id={j.sourceId} label={source} /> : '—'],
        ['Reverses', j.reversesJournalId ? <EntityLink id={j.reversesJournalId} /> : '—'],
        ['Memo', j.memo],
        ['Posted', dateTime(j.postedAt)],
      ]} />
      <Section title="Postings">
        <Paper variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Account</TableCell>
                <TableCell align="right">Debit</TableCell>
                <TableCell align="right">Credit</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {j.postings.map((p, i) => (
                <TableRow key={i}>
                  <TableCell><EntityLink id={p.accountId} label={p.accountCode} /></TableCell>
                  <TableCell align="right">{debit(p.direction) && <Money amount={p.amount} currency={p.currency} />}</TableCell>
                  <TableCell align="right">{!debit(p.direction) && <Money amount={p.amount} currency={p.currency} />}</TableCell>
                </TableRow>
              ))}
              {totals.map((t) => (
                <TableRow key={t.currency} sx={{ '& td': { fontWeight: 700, borderTop: 2 } }}>
                  <TableCell>Total {t.currency} {balanced ? '· balanced' : ''}</TableCell>
                  <TableCell align="right"><Money amount={t.debits} currency={t.currency} /></TableCell>
                  <TableCell align="right"><Money amount={t.credits} currency={t.currency} /></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Paper>
      </Section>
    </>
  )
}
