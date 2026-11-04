import { describe, expect, it } from 'vitest'
import { chartMax, hourSlots, splitFailed } from './chart'

describe('splitFailed', () => {
  it('memisahkan parsial dari gagal tanpa menghitung dua kali', () => {
    expect(splitFailed({ failed: 5, partial: 2 })).toEqual({ full: 3, partial: 2 })
    expect(splitFailed({ failed: 5, partial: 0 })).toEqual({ full: 5, partial: 0 })
  })
  it('parsial kosong/tidak ada = 0; parsial melebihi gagal dibatasi; negatif ditolak', () => {
    expect(splitFailed({ failed: 4 })).toEqual({ full: 4, partial: 0 })
    expect(splitFailed({ failed: 1, partial: 9 })).toEqual({ full: 0, partial: 1 })
    expect(splitFailed({ failed: 3, partial: -2 })).toEqual({ full: 3, partial: 0 })
  })
})

describe('hourSlots / chartMax', () => {
  const now = Date.parse('2026-11-04T10:30:00Z')
  it('selalu 24 slot dan jam kosong berisi nol termasuk partial', () => {
    const s = hourSlots([], now)
    expect(s).toHaveLength(24)
    expect(s.every((p) => p.sent === 0 && p.failed === 0 && p.partial === 0)).toBe(true)
    expect(s[23].t).toBe('2026-11-04T10:00:00.000Z')
  })
  it('titik dengan partial dipetakan ke slot jamnya', () => {
    const s = hourSlots([{ t: '2026-11-04T09:00:00Z', sent: 4, delivered: 2, failed: 3, partial: 1 }], now)
    expect(s[22]).toMatchObject({ failed: 3, partial: 1 })
    expect(chartMax(s)).toBe(7) // max(4,2)+3: parsial tidak menambah tinggi
  })
})
