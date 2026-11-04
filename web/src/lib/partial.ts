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

export interface AggPart { kind: 'partial' | 'allsent'; count: number; text: string }

/** Bagian ringkasan agregat: parsial sejati dan/atau gagal-tetapi-semua-bagian-terkirim (kosong bila tidak ada). */
export function overviewParts(partial: number | undefined, allSent: number | undefined): AggPart[] {
  const { strict, allSent: a } = aggregateParts(partial, allSent)
  const out: AggPart[] = []
  if (strict > 0) out.push({ kind: 'partial', count: strict, text: `${strict} gagal parsial (sebagian bagian sudah sampai)` })
  if (a > 0) out.push({ kind: 'allsent', count: a, text: `${a} gagal tetapi semua bagian terkirim` })
  return out
}

/** Catatan di kartu "Gagal hari ini" (teks polos; Overview memakai overviewParts agar tiap bagian bisa menjadi tautan). */
export function overviewNote(partial: number | undefined, allSent: number | undefined): string | undefined {
  const parts = overviewParts(partial, allSent)
  return parts.length ? `termasuk ${parts.map((x) => x.text).join(' dan ')}` : undefined
}

/** Bagian legenda progres campaign setelah "gagal X". */
export function campaignParts(partial: number | undefined, allSent: number | undefined): AggPart[] {
  const { strict, allSent: a } = aggregateParts(partial, allSent)
  const out: AggPart[] = []
  if (strict > 0) out.push({ kind: 'partial', count: strict, text: `parsial ${strict}` })
  if (a > 0) out.push({ kind: 'allsent', count: a, text: `semua bagian terkirim ${a}` })
  return out
}

/** Potongan legenda progres campaign setelah "gagal X" (teks polos). */
export function campaignFailedSuffix(partial: number | undefined, allSent: number | undefined): string {
  const parts = campaignParts(partial, allSent)
  return parts.length ? ` (${parts.map((x) => x.text).join(', ')})` : ''
}

/** Parameter query daftar transaksi untuk filter kegagalan parsial: parsial sejati = partial=true&all_sent=false. */
export function partialParams(kind: '' | 'partial' | 'allsent'): { partial: string; all_sent: string } {
  if (kind === 'partial') return { partial: 'true', all_sent: 'false' }
  if (kind === 'allsent') return { partial: '', all_sent: 'true' }
  return { partial: '', all_sent: '' }
}
