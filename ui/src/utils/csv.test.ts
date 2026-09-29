import { describe, expect, it } from 'vitest'
import { toCsv } from './csv'

describe('toCsv', () => {
  it('quotes what CSV needs quoted', () => {
    expect(toCsv(['a', 'b'], [['x,y', 'say "hi"']])).toBe('a,b\n"x,y","say ""hi"""')
  })

  it('neutralises spreadsheet formulas', () => {
    expect(toCsv(['memo'], [['=HYPERLINK("http://evil.test")'], ['-1+1'], ['plain']]))
      .toBe('memo\n"\'=HYPERLINK(""http://evil.test"")"\n\'-1+1\nplain')
  })
})
