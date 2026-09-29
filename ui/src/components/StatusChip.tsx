import { Chip } from '@mui/material'
import { labelOf, toneOf } from './statusTone'

/**
 * StatusChip renders any OpenPay status — the enum name or the plain state —
 * with the same colour and label on every screen.
 */
export function StatusChip({ status, size = 'small' }: { status: string; size?: 'small' | 'medium' }) {
  const tone = toneOf(status)
  return (
    <Chip
      size={size}
      label={labelOf(status)}
      color={tone === 'default' ? 'default' : tone}
      variant={tone === 'default' ? 'outlined' : 'filled'}
    />
  )
}
