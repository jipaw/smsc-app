import { useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { get, post, qs } from '../lib/api'
import { ERROR_LABEL, fmtTime, rel } from '../lib/format'
import { canOperate, useMe } from '../lib/session'
import { STATUSES, SOURCES, parseFilter, toSearch, EMPTY_FILTER, type TxFilter } from '../lib/txfilter'
import { partialParams, partialBadge, partialExplain, partialRetryLabel, partialRetryWarning } from '../lib/partial'
import type { Message, MessageDetail, Page } from '../lib/types'
import { Badge, Empty, ErrorBox, errMsg, Loading, Modal, useToast } from '../components/ui'
import { SavedViews } from '../components/SavedViews'


export function Transactions({ campaignId }: { campaignId?: string } = {}) {
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  // filter disimpan di URL (bisa dibagikan, bertahan saat muat ulang); setiap perubahan mengganti entri riwayat agar tombol Back tidak tertimbun
  const [sp, setSp] = useSearchParams()
  const f = parseFilter(sp)
  const setF = (patch: Partial<TxFilter>) => setSp(toSearch({ ...f, ...patch }, sp), { replace: true })
  const { status, source, to, since: from, partial: partialKind } = f
  const effCampaign = campaignId ?? (f.campaign || undefined)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [open, setOpen] = useState<string | null>(null)
  const seen = useRef<Set<string>>(new Set())
  const [fresh, setFresh] = useState<Set<string>>(new Set())

  const filters = { status, source, to: to.trim(), created_from: from ? new Date(from).toISOString() : '', campaign_id: effCampaign, ...partialParams(partialKind) }
  const q = useInfiniteQuery({
    queryKey: ['messages', filters],
    queryFn: ({ pageParam }) => get<Page<Message>>('/messages' + qs({ ...filters, limit: 50, cursor: pageParam })),
    initialPageParam: '' as string, getNextPageParam: (l) => l.next_cursor ?? undefined,
  })
  const rows = q.data?.pages.flatMap((p) => p.items) ?? []

  // sorot baris baru/berubah (SSE meng-invalidate; bandingkan status)
  const lastStatus = useRef<Map<string, string>>(new Map())
  useEffect(() => {
    const ch = new Set<string>()
    for (const m of rows) { const k = lastStatus.current.get(m.id); if (k !== undefined && k !== m.status) ch.add(m.id); lastStatus.current.set(m.id, m.status) }
    seen.current = new Set(rows.map((r) => r.id))
    if (ch.size) { setFresh(ch); const t = window.setTimeout(() => setFresh(new Set()), 2000); return () => window.clearTimeout(t) }
  }, [rows.map((r) => r.id + r.status).join(',')]) // eslint-disable-line react-hooks/exhaustive-deps

  const bulk = useMutation({
    mutationFn: async (kind: 'retry' | 'cancel') => {
      const ids = rows.filter((r) => sel.has(r.id) && (kind === 'retry' ? ['failed', 'expired'].includes(r.status) && !r.partial : ['queued', 'assigned'].includes(r.status))).map((r) => r.id)
      const res = await Promise.allSettled(ids.map((id) => post(`/messages/${id}/${kind}`)))
      return { ok: res.filter((x) => x.status === 'fulfilled').length, total: ids.length }
    },
    onSuccess: (r) => { toast(`${r.ok}/${r.total} berhasil`); setSel(new Set()); qc.invalidateQueries({ queryKey: ['messages'] }) },
  })
  const toggle = (id: string) => setSel((s) => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n })
  const exportUrl = '/api/v1/messages/export' + qs({ ...filters })
  const op = canOperate(me)
  const retryable = rows.filter((r) => sel.has(r.id) && ['failed', 'expired'].includes(r.status) && !r.partial).length
  const cancellable = rows.filter((r) => sel.has(r.id) && ['queued', 'assigned'].includes(r.status)).length

  return (
    <div className="stack gap-lg">
      <div className="row spread">{campaignId ? <h2>Pesan campaign</h2> : <h1>Transaksi</h1>}{op && <a className="btn" href={exportUrl} download>Ekspor CSV</a>}</div>
      <div className="card stack">
        {!campaignId && f.campaign && <div className="row" role="status"><span className="badge info">Campaign {f.campaign.slice(0, 8)}…</span><Link to={`/campaigns/${f.campaign}`}>Buka campaign</Link><button className="small" onClick={() => setF({ campaign: '' })}>Hapus filter campaign</button></div>}
        {!campaignId && <SavedViews filter={f} apply={(nf) => setSp(toSearch(nf, sp), { replace: true })} />}
        <div className="row" role="group" aria-label="Filter status">{STATUSES.map((s) => (
          <label key={s} className="row" style={{ gap: 4 }}><input type="checkbox" checked={status.includes(s)} onChange={(e) => setF({ status: e.target.checked ? [...status, s] : status.filter((x) => x !== s) })} /><Badge status={s} /></label>))}</div>
        <div className="row">
          <input placeholder="Cari nomor (+62…)" value={to} onChange={(e) => setF({ to: e.target.value })} aria-label="Cari nomor" />
          <select value={source} onChange={(e) => setF({ source: e.target.value })} aria-label="Sumber"><option value="">Semua sumber</option>{SOURCES.map((s) => <option key={s}>{s}</option>)}</select>
          <select value={partialKind} onChange={(e) => setF({ partial: e.target.value as TxFilter['partial'] })} aria-label="Kegagalan parsial">
            <option value="">Semua pesan</option><option value="partial">Hanya gagal parsial</option><option value="allsent">Hanya gagal tetapi semua bagian terkirim</option></select>
          <label className="row">Sejak <input type="datetime-local" value={from} onChange={(e) => setF({ since: e.target.value })} /></label>
          <button className="small" onClick={() => setF({ ...EMPTY_FILTER })}>Reset</button>
        </div>
        {op && sel.size > 0 && <div className="row"><strong>{sel.size} dipilih</strong>
          <button className="small" disabled={!retryable || bulk.isPending} onClick={() => bulk.mutate('retry')}>Retry ({retryable})</button>
          <button className="small" disabled={!cancellable || bulk.isPending} onClick={() => bulk.mutate('cancel')}>Batalkan ({cancellable})</button>
          <span className="hint">Pesan “tidak pasti” dan “gagal parsial” hanya dapat di-retry satu per satu (risiko SMS/bagian ganda).</span></div>}
      </div>
      {q.isLoading && <Loading />}
      <ErrorBox error={q.error} />
      {q.data && rows.length === 0 && <Empty>{campaignId && !status.length && !to.trim() && !source && !from ? 'Belum ada pesan: pesan campaign dibuat bertahap setelah campaign berjalan.' : 'Tidak ada transaksi yang cocok dengan filter.'}</Empty>}
      {rows.length > 0 && (
        <div className="card scroll-x"><table className="cards">
          <thead><tr>{op && <th><span className="sr-only">Pilih</span></th>}<th>Waktu</th><th>Nomor</th><th>Isi</th><th>Sumber</th><th>Status</th><th>Try</th><th>Keterangan</th></tr></thead>
          <tbody>{rows.map((m) => (
            <tr key={m.id} className={`click ${fresh.has(m.id) ? 'flash' : ''}`} onClick={() => setOpen(m.id)}>
              {op && <td onClick={(e) => e.stopPropagation()}><input type="checkbox" checked={sel.has(m.id)} onChange={() => toggle(m.id)} aria-label={`Pilih ${m.to}`} /></td>}
              <td data-l="Waktu" title={fmtTime(m.created_at)}>{rel(m.created_at)}</td>
              <td data-l="Nomor" className="mono">{m.to}</td>
              <td data-l="Isi" style={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={m.body ?? ''}>{m.body}</td>
              <td data-l="Sumber">{m.source}</td><td data-l="Status"><Badge status={m.status} />{m.partial && <> <Badge status="partial" label={partialBadge(m)} /></>}</td><td data-l="Try">{m.attempt}/{m.max_attempts}</td>
              <td data-l="Keterangan" className="muted">{m.error_code ? ERROR_LABEL[m.error_code] ?? m.error_code : ''}</td>
            </tr>))}</tbody>
        </table>
          {q.hasNextPage && <div style={{ textAlign: 'center', padding: 10 }}><button onClick={() => q.fetchNextPage()} disabled={q.isFetchingNextPage}>{q.isFetchingNextPage ? 'Memuat…' : 'Muat lebih banyak'}</button></div>}
        </div>
      )}
      <Detail id={open} onClose={() => setOpen(null)} />
    </div>
  )
}

function Detail({ id, onClose }: { id: string | null; onClose: () => void }) {
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({ queryKey: ['message', id], queryFn: () => get<MessageDetail>(`/messages/${id}`), enabled: !!id })
  const act = useMutation({
    mutationFn: ({ kind, body }: { kind: string; body?: object }) => post(`/messages/${id}/${kind}`, body),
    onSuccess: () => { toast('Berhasil'); qc.invalidateQueries() }, onError: (e) => toast(errMsg(e), true),
  })
  const m = q.data
  const op = canOperate(me)
  return (
    <Modal open={!!id} onClose={onClose} title="Detail transaksi" drawer>
      {q.isLoading && <Loading />}<ErrorBox error={q.error} />
      {m && (<>
        <div className="row spread"><span className="mono">{m.to}</span><Badge status={m.status} /></div>
        <div className="card" style={{ whiteSpace: 'pre-wrap' }}>{m.body}</div>
        <div className="muted">{m.encoding?.toUpperCase()} · {m.parts} segmen · prioritas {m.priority} · percobaan {m.attempt}/{m.max_attempts}</div>
        {m.partial && <div className="err" role="status">{partialExplain(m)}</div>}
        {m.error_code && <div className="err">{ERROR_LABEL[m.error_code] ?? m.error_code} <span className="mono">({m.error_code})</span>{m.status === 'unknown' && <div>HP terputus saat mengirim; hasil tidak pasti. Tunggu HP kembali online (status dikoreksi otomatis) sebelum retry.</div>}</div>}
        {op && <div className="row">
          {['failed', 'expired'].includes(m.status) && !m.partial && <button onClick={() => act.mutate({ kind: 'retry', body: {} })}>Retry</button>}
          {m.partial && <button className="danger" onClick={() => window.confirm(partialRetryWarning(m)) && act.mutate({ kind: 'retry', body: { confirm_duplicate_risk: true } })}>{partialRetryLabel(m)}</button>}
          {m.status === 'unknown' && <button className="danger" onClick={() => window.confirm('Retry pesan tidak pasti dapat mengirim SMS ganda. Yakin?') && act.mutate({ kind: 'retry', body: { confirm_duplicate_risk: true } })}>Retry (risiko ganda)</button>}
          {['queued', 'assigned'].includes(m.status) && <button onClick={() => act.mutate({ kind: 'cancel' })}>Batalkan</button>}
        </div>}
        <h3>Timeline</h3>
        <ol className="timeline">{m.events.map((e, i) => (
          <li key={e.id}><strong>{e.type}</strong> <span className="muted">· {e.actor} · {fmtTime(e.at)}{i > 0 && ` (+${((new Date(e.at).getTime() - new Date(m.events[i - 1].at).getTime()) / 1000).toFixed(1)}s)`}</span>
            {e.error_code && <div className="hint">{e.error_code}{e.data && 'android_result_code' in e.data ? ` · android=${String(e.data.android_result_code)}` : ''}</div>}</li>))}</ol>
        <div className="hint mono">id {m.id}</div>
      </>)}
    </Modal>
  )
}
