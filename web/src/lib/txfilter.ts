/** Filter daftar transaksi yang disimpan di URL (bisa dibagikan/di-bookmark, bertahan saat muat ulang). Nilai dari URL tidak dipercaya: divalidasi. */
export const STATUSES = ['queued', 'assigned', 'sending', 'sent', 'delivered', 'failed', 'cancelled', 'expired', 'unknown']
export const SOURCES = ['dashboard', 'api', 'campaign']
export type PartialKind = '' | 'partial' | 'allsent'

export interface TxFilter { status: string[]; source: string; to: string; since: string; partial: PartialKind; campaign: string }
export const EMPTY_FILTER: TxFilter = { status: [], source: '', to: '', since: '', partial: '', campaign: '' }

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const LOCAL_DT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/

export function parseFilter(sp: URLSearchParams): TxFilter {
  const status = Array.from(new Set((sp.get('status') ?? '').split(',').filter((s) => STATUSES.includes(s))))
  const source = SOURCES.includes(sp.get('source') ?? '') ? (sp.get('source') as string) : ''
  const to = (sp.get('to') ?? '').replace(/[^+0-9]/g, '').slice(0, 20)
  const sinceRaw = sp.get('since') ?? ''
  const since = LOCAL_DT.test(sinceRaw) && !Number.isNaN(new Date(sinceRaw).getTime()) ? sinceRaw : ''
  const p = sp.get('parsial')
  const partial: PartialKind = p === 'partial' || p === 'allsent' ? p : ''
  const c = (sp.get('campaign') ?? '').toLowerCase()
  return { status, source, to, since, partial, campaign: UUID.test(c) ? c : '' }
}

/** Serialisasikan filter; parameter lain pada `base` (bukan milik filter) dipertahankan. Nilai kosong dihilangkan. */
export function toSearch(f: TxFilter, base?: URLSearchParams): URLSearchParams {
  const sp = new URLSearchParams(base)
  for (const k of ['status', 'source', 'to', 'since', 'parsial', 'campaign']) sp.delete(k)
  if (f.status.length) sp.set('status', f.status.join(','))
  if (f.source) sp.set('source', f.source)
  if (f.to) sp.set('to', f.to)
  if (f.since) sp.set('since', f.since)
  if (f.partial) sp.set('parsial', f.partial)
  if (f.campaign) sp.set('campaign', f.campaign)
  return sp
}

/** Tautan ke daftar transaksi dengan filter tertentu. */
export function filterLink(patch: Partial<TxFilter>): string {
  const s = toSearch({ ...EMPTY_FILTER, ...patch }).toString()
  return '/transactions' + (s ? `?${s}` : '')
}

/** Bentuk filter di API tampilan tersimpan (kunci sama; nilai kosong = ""). */
export interface ViewFilterDTO { status?: string[]; source?: string; to?: string; since?: string; partial?: string; campaign?: string }

export function toView(f: TxFilter): Required<ViewFilterDTO> {
  return { status: f.status, source: f.source, to: f.to, since: f.since, partial: f.partial, campaign: f.campaign }
}

/** Tampilan dari server → filter dashboard; lewat parseFilter agar nilai yang tak lagi valid dibuang dengan aturan yang sama dengan URL. */
export function fromView(v: ViewFilterDTO): TxFilter {
  const sp = new URLSearchParams()
  if (v.status?.length) sp.set('status', v.status.join(','))
  if (v.source) sp.set('source', v.source)
  if (v.to) sp.set('to', v.to)
  if (v.since) sp.set('since', v.since)
  if (v.partial) sp.set('parsial', v.partial)
  if (v.campaign) sp.set('campaign', v.campaign)
  return parseFilter(sp)
}

/** Dua filter sama (urutan status tidak berpengaruh). */
export function sameFilter(a: TxFilter, b: TxFilter): boolean {
  return [...a.status].sort().join() === [...b.status].sort().join() && a.source === b.source && a.to === b.to && a.since === b.since && a.partial === b.partial && a.campaign === b.campaign
}
