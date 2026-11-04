import { describe, expect, it } from 'vitest'
import { chartMax, hourSlots, splitFailed } from './chart'

describe('splitFailed', () => {
  it('memisahkan parsial dari gagal tanpa menghitung dua kali', () => {
    expect(splitFailed({ failed: 5, partial: 2 })).toEqual({ full: 3, partial: 2, allSent: 0 })
    expect(splitFailed({ failed: 5, partial: 0 })).toEqual({ full: 5, partial: 0, allSent: 0 })
  })
  it('semua-terkirim adalah bagian dari parsial: tiga segmen berjumlah = failed', () => {
    expect(splitFailed({ failed: 6, partial: 3, all_sent: 1 })).toEqual({ full: 3, partial: 2, allSent: 1 })
    expect(splitFailed({ failed: 6, partial: 3, all_sent: 9 })).toEqual({ full: 3, partial: 0, allSent: 3 }) // dibatasi ≤ parsial
    const r = splitFailed({ failed: 7, partial: 4, all_sent: 2 })
    expect(r.full + r.partial + r.allSent).toBe(7)
  })
  it('parsial kosong/tidak ada = 0; parsial melebihi gagal dibatasi; negatif ditolak', () => {
    expect(splitFailed({ failed: 4 })).toEqual({ full: 4, partial: 0, allSent: 0 })
    expect(splitFailed({ failed: 1, partial: 9 })).toEqual({ full: 0, partial: 1, allSent: 0 })
    expect(splitFailed({ failed: 3, partial: -2 })).toEqual({ full: 3, partial: 0, allSent: 0 })
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

import { bucketSlots, campaignWindow, pickBucket } from './chart'

describe('pickBucket / campaignWindow', () => {
  it('memilih bucket menurut rentang', () => {
    expect(pickBucket(30 * 60_000)).toBe('minute')
    expect(pickBucket(3600_000)).toBe('minute')
    expect(pickBucket(3 * 3600_000)).toBe('hour')
    expect(pickBucket(72 * 3600_000)).toBe('hour')
    expect(pickBucket(73 * 3600_000)).toBe('day')
  })
  it('jendela campaign pendek: per menit, tidak melebihi batas, berakhir di bucket akhir', () => {
    const end = Date.parse('2026-11-04T10:30:20Z')
    const w = campaignWindow(end - 10 * 60_000, end)
    expect(w.bucket).toBe('minute')
    expect(w.count).toBe(11)
    expect(w.truncated).toBe(false)
    expect(w.toMs).toBe(Date.parse('2026-11-04T10:31:00Z'))
    expect(w.fromMs).toBe(Date.parse('2026-11-04T10:20:00Z'))
  })
  it('campaign sangat panjang dipotong ke batas batang dan ditandai truncated', () => {
    const end = Date.parse('2026-11-04T10:30:00Z')
    const w = campaignWindow(end - 400 * 86_400_000, end)
    expect(w.bucket).toBe('day')
    expect(w.count).toBe(60)
    expect(w.truncated).toBe(true)
  })
  it('campaign seketika (mulai=selesai) tetap menghasilkan minimal 2 slot menit', () => {
    const t = Date.parse('2026-11-04T10:30:00Z')
    expect(campaignWindow(t, t).count).toBeGreaterThanOrEqual(2)
  })
})

describe('bucketSlots dengan zona waktu database yang menggeser bucket hari', () => {
  it('titik jatuh ke slot yang memuatnya dan dijumlahkan, titik di luar jendela diabaikan', () => {
    const end = Date.parse('2026-11-04T10:00:00Z')
    // bucket hari DB berbasis WIB (UTC+7): mulai 17:00Z hari sebelumnya
    const pts = [
      { t: '2026-11-03T17:00:00Z', sent: 2, delivered: 1, failed: 1, partial: 1 },
      { t: '2026-11-05T17:00:00Z', sent: 9, delivered: 9, failed: 0, partial: 0 }, // di luar jendela (setelah slot terakhir)
      { t: '2026-10-01T17:00:00Z', sent: 5, delivered: 5, failed: 0, partial: 0 }, // sebelum jendela
    ]
    const s = bucketSlots(pts, 'day', end, 3)
    expect(s).toHaveLength(3)
    const sum = s.reduce((a, p) => ({ sent: a.sent + p.sent, partial: a.partial + (p.partial ?? 0) }), { sent: 0, partial: 0 })
    expect(sum).toEqual({ sent: 2, partial: 1 })
    expect(s[1]).toMatchObject({ t: '2026-11-03T00:00:00.000Z', sent: 2, partial: 1 }) // 17:00Z masuk hari 11-03 (bukan dibulatkan ke hari berikutnya)
  })
})
