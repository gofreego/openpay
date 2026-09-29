import { describe, expect, it } from 'vitest'
import { labelOf, stateName, toneOf } from './statusTone'

describe('status tones', () => {
  it('strips enum prefixes, however many words the machine name has', () => {
    expect(stateName('PAYMENT_STATUS_CAPTURED')).toBe('captured')
    expect(stateName('WITHDRAWAL_STATUS_PENDING_APPROVAL')).toBe('pending_approval')
    expect(stateName('LEDGER_CHECK_STATUS_FAILED')).toBe('failed')
    expect(stateName('open')).toBe('open')
  })

  it('renders one state the same way everywhere', () => {
    expect(toneOf('PAYMENT_STATUS_CAPTURED')).toBe('success')
    expect(toneOf('captured')).toBe('success')
    expect(toneOf('WITHDRAWAL_STATUS_PENDING_APPROVAL')).toBe('warning')
    expect(toneOf('DISPUTE_STATUS_LOST')).toBe('error')
    expect(toneOf('something_new')).toBe('default')
    expect(labelOf('WITHDRAWAL_STATUS_PENDING_APPROVAL')).toBe('Pending approval')
  })
})
