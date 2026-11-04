import type { Point } from './types'

export type Bucket = 'minute' | 'hour' | 'day'
export const BUCKET_MS: Record<Bucket, number> = { minute: 60_000, hour: 3600_000, day: 86_400_000 }
const BUCKET_CAP: Record<Bucket, number> = { minute: 60, hour: 72, day: 60 } // maksimum batang per grafik

/**
 * Susun `count` slot berurutan berakhir di bucket yang memuat `endMs`. Titik dari server dijumlahkan ke slot yang MEMUATNYA
 * (bukan dicocokkan persis), sehingga tetap benar bila zona waktu database menggeser batas bucket (mis. hari).
 */
export function bucketSlots(points: Point[], bucket: Bucket, endMs: number, count: number): Point[] {
  const ms = BUCKET_MS[bucket]
  const end = Math.floor(endMs / ms) * ms
  const slots: Point[] = Array.from({ length: count }, (_, i) => ({
    t: new Date(end - (count - 1 - i) * ms).toISOString(), sent: 0, delivered: 0, failed: 0, partial: 0,
  }))
  const first = end - (count - 1) * ms
  for (const p of points) {
    const i = Math.floor((new Date(p.t).getTime() - first) / ms)
    if (i < 0 || i >= count) continue
    const s = slots[i]
    s.sent += p.sent; s.delivered += p.delivered; s.failed += p.failed; s.partial = (s.partial ?? 0) + (p.partial ?? 0)
  }
  return slots
}

/** 24 slot jam terakhir (selalu 24 kolom, jam tanpa data = kosong) agar skala batang konsisten. */
export function hourSlots(points: Point[], now: number = Date.now()): Point[] {
  return bucketSlots(points, 'hour', now, 24)
}

/** Pilih bucket menurut rentang waktu campaign: <= 1 jam per menit, <= 3 hari per jam, selebihnya per hari. */
export function pickBucket(spanMs: number): Bucket {
  if (spanMs <= 3600_000) return 'minute'
  if (spanMs <= 72 * 3600_000) return 'hour'
  return 'day'
}

export interface SeriesWindow { bucket: Bucket; count: number; fromMs: number; toMs: number; truncated: boolean }

/** Jendela deret waktu untuk campaign: dari mulai (atau dibuat) sampai selesai (atau sekarang), dibatasi BUCKET_CAP batang terakhir. */
export function campaignWindow(startMs: number, endMs: number): SeriesWindow {
  const span = Math.max(endMs - startMs, BUCKET_MS.minute)
  const bucket = pickBucket(span)
  const ms = BUCKET_MS[bucket]
  const wanted = Math.ceil(span / ms) + 1
  const count = Math.min(wanted, BUCKET_CAP[bucket])
  const end = Math.floor(endMs / ms) * ms
  return { bucket, count, fromMs: end - (count - 1) * ms, toMs: end + ms, truncated: wanted > count }
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
