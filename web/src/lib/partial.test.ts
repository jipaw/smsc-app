import { describe, expect, it } from 'vitest'
import { isAllSent, partialBadge, partialExplain, partialRetryLabel, partialRetryWarning } from './partial'

describe('teks pesan parsial', () => {
  it('sebagian bagian terkirim', () => {
    const m = { parts: 4, parts_sent: 1 }
    expect(isAllSent(m)).toBe(false)
    expect(partialBadge(m)).toBe('Parsial 1/4')
    expect(partialExplain(m)).toContain('1 dari 4 bagian sudah terkirim')
    expect(partialRetryLabel(m)).toBe('Retry (bagian ganda)')
    expect(partialRetryWarning(m)).toContain('SEMUA bagian')
  })
  it('semua bagian terkirim: tidak menulis "sisanya tidak" dan memperingatkan kirim ulang LENGKAP', () => {
    const m = { parts: 3, parts_sent: 3 }
    expect(isAllSent(m)).toBe(true)
    expect(partialBadge(m)).toBe('Semua bagian terkirim (3/3)')
    expect(partialExplain(m)).not.toContain('sisanya tidak')
    expect(partialExplain(m)).toContain('SUDAH sampai utuh')
    expect(partialRetryWarning(m)).toContain('LENGKAP')
    expect(partialRetryLabel(m)).toBe('Retry (kirim ulang lengkap)')
  })
  it('batas: parts_sent null/0 bukan semua terkirim; parts_sent melebihi parts dianggap semua terkirim; parts 0 tidak', () => {
    expect(isAllSent({ parts: 2, parts_sent: null })).toBe(false)
    expect(isAllSent({ parts: 2, parts_sent: 0 })).toBe(false)
    expect(isAllSent({ parts: 2, parts_sent: 5 })).toBe(true)
    expect(isAllSent({ parts: 0, parts_sent: 0 })).toBe(false)
    expect(partialBadge({ parts: 2 })).toBe('Parsial 0/2')
  })
})
