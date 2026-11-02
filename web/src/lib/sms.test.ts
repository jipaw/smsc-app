import { describe, expect, it } from 'vitest'
import { analyze, parseRecipients, variables } from './sms'

// Kasus yang sama dengan server/internal/sms/sms_test.go (T-SEG-01/02/03)
describe('analyze', () => {
  it('GSM-7: 160->1, 161->2, 306->2, 307->3', () => {
    for (const [n, want] of [[1, 1], [160, 1], [161, 2], [306, 2], [307, 3]] as const) {
      const r = analyze('a'.repeat(n))
      expect(r.encoding).toBe('gsm7')
      expect(r.parts).toBe(want)
    }
  })
  it('UCS-2: 70->1, 71->2, 134->2, 135->3', () => {
    for (const [n, want] of [[1, 1], [70, 1], [71, 2], [134, 2], [135, 3]] as const) {
      const r = analyze('ä'.repeat(n - 1) + 'ж')
      expect(r.encoding).toBe('ucs2')
      expect(r.parts).toBe(want)
    }
  })
  it('extension GSM = 2 septet, emoji = 2 unit UTF-16', () => {
    expect(analyze('{}')).toMatchObject({ encoding: 'gsm7', units: 4 })
    expect(analyze('€'.repeat(81)).parts).toBe(2)
    expect(analyze('hi 😀')).toMatchObject({ encoding: 'ucs2', units: 5 })
  })
})

describe('util', () => {
  it('parseRecipients memisahkan baris/koma dan membuang kosong', () => {
    expect(parseRecipients('0812\n0813, 0814;\n\n')).toEqual(['0812', '0813', '0814'])
  })
  it('variables unik berurutan', () => {
    expect(variables('{{a}} {{ b }} {{a}}')).toEqual(['a', 'b'])
  })
})
