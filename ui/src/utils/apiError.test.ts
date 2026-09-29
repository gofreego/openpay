import { describe, expect, it } from 'vitest'
import { describeError, toApiError } from './apiError'

describe('toApiError', () => {
  it('reads the OpenPay error body', () => {
    const err = Object.assign(new Error('HTTP Error: Bad Request'), {
      status: 412,
      data: { code: 'insufficient_balance', message: 'wallet is short', request_id: 'req_1' },
    })
    expect(toApiError(err)).toEqual({ code: 'insufficient_balance', message: 'wallet is short', requestId: 'req_1', status: 412 })
    expect(describeError(err)).toBe('wallet is short (insufficient_balance, req_1)')
  })

  it('says a timeout may have been applied', () => {
    expect(toApiError(new Error('Request timeout')).code).toBe('timeout')
  })

  it('copes with a body that is not ours', () => {
    const err = Object.assign(new Error('HTTP Error: Bad Gateway'), { status: 502, data: { code: 502, message: '<html>' } })
    expect(toApiError(err).code).toBe('http_502')
  })
})
