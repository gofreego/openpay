import { Box } from '@mui/material'
import { Money as M } from '../utils/money'

interface MoneyProps {
  /** Minor units, as the API sends them. */
  amount: string | bigint | undefined
  currency: string
  /** Show a leading + on positive amounts (a statement's change column). */
  signed?: boolean
  /** Colour negatives red. */
  colored?: boolean
}

/**
 * Money is the only way the console shows an amount (plan.md U-D4):
 * tabular figures, the currency's full decimals, BigInt all the way down.
 */
export function Money({ amount, currency, signed = false, colored = false }: MoneyProps) {
  const value = M.parse(amount)
  const text = M.format(value, currency)
  return (
    <Box
      component="span"
      sx={{
        fontVariantNumeric: 'tabular-nums',
        whiteSpace: 'nowrap',
        color: colored && value < 0n ? 'error.main' : undefined,
      }}
    >
      {signed && value > 0n ? `+${text}` : text}
    </Box>
  )
}
