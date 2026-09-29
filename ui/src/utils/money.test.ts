import { describe, expect, it } from 'vitest'
import { add, allocate, format, fromMajor, parse, sub, toMajor } from './money'

describe('parse', () => {
  it('reads int64 strings exactly, beyond float precision', () => {
    // 2^63 - 1: a float64 would round this to ...5808.
    expect(parse('9223372036854775807')).toBe(9223372036854775807n)
    expect(parse('-5')).toBe(-5n)
    expect(parse(undefined)).toBe(0n)
  })

  it('refuses anything that is not minor units', () => {
    expect(() => parse('12.50')).toThrow()
    expect(() => parse('1e3')).toThrow()
  })
})

describe('arithmetic', () => {
  it('does not drift like floats do', () => {
    expect(add('10', '20')).toBe(30n) // 0.10 + 0.20 is exactly 0.30
    expect(sub('100', '250')).toBe(-150n)
  })

  it('allocates without losing or inventing a unit', () => {
    const parts = allocate('100', [1, 1, 1])
    expect(parts).toEqual([34n, 33n, 33n])
    expect(parts.reduce((s, p) => s + p, 0n)).toBe(100n)

    const big = allocate('9223372036854775807', [1, 2])
    expect(big.reduce((s, p) => s + p, 0n)).toBe(9223372036854775807n)

    expect(allocate('-100', [1, 1, 1])).toEqual([-34n, -33n, -33n])
    expect(allocate('5', [0, 1])).toEqual([0n, 5n])
    expect(() => allocate('5', [0, 0])).toThrow()
  })
})

describe('toMajor and fromMajor', () => {
  it('zero-pads minor units', () => {
    expect(toMajor('5', 'INR')).toBe('0.05')
    expect(toMajor('50', 'INR')).toBe('0.50')
    expect(toMajor('0', 'INR')).toBe('0.00')
    expect(toMajor('-7', 'INR')).toBe('-0.07')
    expect(toMajor('123456', 'INR')).toBe('1234.56')
    expect(toMajor('500', 'JPY')).toBe('500')
  })

  it('reads what people type, exactly', () => {
    expect(fromMajor('1,234.5', 'INR')).toBe(123450n)
    expect(fromMajor('₹ 10', 'INR')).toBe(1000n)
    expect(fromMajor('0.07', 'INR')).toBe(7n)
    expect(fromMajor('-2.00', 'INR')).toBe(-200n)
    expect(fromMajor('92233720368547758.07', 'INR')).toBe(9223372036854775807n)
  })

  it('refuses rather than rounds', () => {
    expect(() => fromMajor('10.005', 'INR')).toThrow()
    expect(() => fromMajor('ten', 'INR')).toThrow()
    expect(() => fromMajor('', 'INR')).toThrow()
  })

  it('round-trips', () => {
    for (const minor of ['0', '1', '99', '100', '-12345', '9223372036854775807']) {
      expect(fromMajor(toMajor(minor, 'INR'), 'INR')).toBe(BigInt(minor))
    }
  })
})

describe('format', () => {
  it('uses Indian grouping and full decimals for INR', () => {
    expect(format('12345670', 'INR')).toBe('₹1,23,456.70')
    expect(format('0', 'INR')).toBe('₹0.00')
    expect(format('-500', 'INR')).toBe('-₹5.00')
  })

  it('formats values a float cannot hold', () => {
    expect(format('9223372036854775807', 'INR')).toBe('₹92,23,37,20,36,85,47,758.07')
  })
})
