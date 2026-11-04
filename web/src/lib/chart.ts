import type { Point } from './types'

/** 24 slot jam terakhir (selalu 24 kolom, jam tanpa data = kosong) agar skala batang konsisten. */
export function hourSlots(points: Point[], now: number = Date.now()): Point[] {
  const hour = 3600_000
  const end = Math.floor(now / hour) * hour
  const byT = new Map(points.map((p) => [Math.floor(new Date(p.t).getTime() / hour) * hour, p]))
  return Array.from({ length: 24 }, (_, i) => {
    const t = end - (23 - i) * hour
    return byT.get(t) ?? { t: new Date(t).toISOString(), sent: 0, delivered: 0, failed: 0, partial: 0 }
  })
}

/** Gagal parsial adalah BAGIAN dari gagal: kembalikan {gagal penuh, parsial} yang jumlahnya = failed (parsial dibatasi ≤ failed). */
export function splitFailed(p: Pick<Point, 'failed' | 'partial'>): { full: number; partial: number } {
  const partial = Math.max(0, Math.min(p.partial ?? 0, p.failed))
  return { full: p.failed - partial, partial }
}

/** Tinggi tumpukan terbesar (skala batang): max(sent, delivered) + failed; minimal 1. */
export function chartMax(slots: Point[]): number {
  return Math.max(1, ...slots.map((p) => Math.max(p.sent, p.delivered) + p.failed))
}
