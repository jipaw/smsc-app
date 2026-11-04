/**
 * Teks untuk pesan gagal-parsial. `partial` di API berarti ada bagian yang sudah terkirim saat pesan dinyatakan gagal. Bila
 * hitungan yang disegarkan dari HP (fase 33) ternyata mencapai SEMUA bagian, pesan itu sebenarnya sudah sampai seluruhnya
 * walau ada galat: tulisan "sebagian" dan "sisanya tidak" menjadi salah, dan retry berarti mengirim ulang pesan LENGKAP.
 */
export interface PartialInfo { parts: number; parts_sent?: number | null }

export function isAllSent(m: PartialInfo): boolean {
  return m.parts_sent != null && m.parts > 0 && m.parts_sent >= m.parts
}

/** Teks lencana di samping status "Gagal". */
export function partialBadge(m: PartialInfo): string {
  return isAllSent(m) ? `Semua bagian terkirim (${m.parts_sent}/${m.parts})` : `Parsial ${m.parts_sent ?? 0}/${m.parts}`
}

/** Banner penjelasan di drawer detail. */
export function partialExplain(m: PartialInfo): string {
  return isAllSent(m)
    ? `Pesan dinyatakan gagal, tetapi HP melaporkan semua ${m.parts} bagian sudah terkirim ke penerima. Kemungkinan besar pesan SUDAH sampai utuh; tidak diantre ulang otomatis agar tidak terkirim dua kali.`
    : `Gagal parsial: ${m.parts_sent ?? 0} dari ${m.parts} bagian sudah terkirim ke penerima, sisanya tidak. Pesan tidak diantre ulang otomatis agar bagian yang sudah sampai tidak ganda.`
}

/** Pertanyaan konfirmasi sebelum retry. */
export function partialRetryWarning(m: PartialInfo): string {
  return isAllSent(m)
    ? `Semua ${m.parts} bagian pesan ini dilaporkan sudah terkirim. Retry mengirim ulang pesan LENGKAP sehingga penerima bisa menerimanya dua kali. Yakin?`
    : `${m.parts_sent ?? 0} dari ${m.parts} bagian pesan ini sudah terkirim. Retry mengirim ulang SEMUA bagian sehingga bagian itu ganda di penerima. Yakin?`
}

/** Teks tombol retry. */
export function partialRetryLabel(m: PartialInfo): string {
  return isAllSent(m) ? 'Retry (kirim ulang lengkap)' : 'Retry (bagian ganda)'
}

/**
 * Ringkasan agregat (Overview/campaign). API: partial ⊆ failed, all_sent ⊆ partial. Parsial sejati = partial − all_sent.
 * Mengembalikan potongan kalimat (kosong bila tidak ada).
 */
export function aggregateParts(partial: number | undefined, allSent: number | undefined): { strict: number; allSent: number } {
  const p = Math.max(0, partial ?? 0)
  const a = Math.max(0, Math.min(allSent ?? 0, p))
  return { strict: p - a, allSent: a }
}

/** Catatan di kartu "Gagal hari ini". */
export function overviewNote(partial: number | undefined, allSent: number | undefined): string | undefined {
  const { strict, allSent: a } = aggregateParts(partial, allSent)
  const parts: string[] = []
  if (strict > 0) parts.push(`${strict} gagal parsial (sebagian bagian sudah sampai)`)
  if (a > 0) parts.push(`${a} gagal tetapi semua bagian terkirim`)
  return parts.length ? `termasuk ${parts.join(' dan ')}` : undefined
}

/** Potongan legenda progres campaign setelah "gagal X". */
export function campaignFailedSuffix(partial: number | undefined, allSent: number | undefined): string {
  const { strict, allSent: a } = aggregateParts(partial, allSent)
  const parts: string[] = []
  if (strict > 0) parts.push(`parsial ${strict}`)
  if (a > 0) parts.push(`semua bagian terkirim ${a}`)
  return parts.length ? ` (${parts.join(', ')})` : ''
}
