import { Table, TableBody, TableCell, TableHead, TableRow } from '@mui/material'
import { Money } from './Money'

export interface PreviewLeg {
  account: string
  debit?: string
  credit?: string
}

/**
 * JournalPreview shows the postings an action will make before it is
 * submitted (plan.md U-D7): which accounts, which side, how much — and that
 * they balance.
 */
export function JournalPreview({ legs, currency }: { legs: PreviewLeg[]; currency: string }) {
  return (
    <Table size="small">
      <TableHead>
        <TableRow>
          <TableCell>Account</TableCell>
          <TableCell align="right">Debit</TableCell>
          <TableCell align="right">Credit</TableCell>
        </TableRow>
      </TableHead>
      <TableBody>
        {legs.map((l) => (
          <TableRow key={l.account + (l.debit ? 'd' : 'c')}>
            <TableCell><code>{l.account}</code></TableCell>
            <TableCell align="right">{l.debit && <Money amount={l.debit} currency={currency} />}</TableCell>
            <TableCell align="right">{l.credit && <Money amount={l.credit} currency={currency} />}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}
