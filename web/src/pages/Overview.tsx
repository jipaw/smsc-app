import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { get, put, qs } from '../lib/api'
import { fmtTime, pct, rel, ERROR_LABEL } from '../lib/format'
import { isAdmin, useMe } from '../lib/session'
import type { Device, Overview as Ov, Page, Point } from '../lib/types'
import { Badge, ErrorBox, errMsg, Loading, useToast, Confirm } from '../components/ui'
import { useState } from 'react'

function Stat({ label, value, tone }: { label: string; value: string | number; tone?: string }) {
  return <div className="card stat"><div className="l">{label}</div><div className="v" style={tone ? { color: `var(--${tone})` } : undefined}>{value}</div></div>
}

/** 24 slot jam terakhir (selalu 24 kolom, jam tanpa data = kosong) agar skala batang konsisten. */
function hourSlots(points: Point[]): Point[] {
  const hour = 3600_000
  const end = Math.floor(Date.now() / hour) * hour
  const byT = new Map(points.map((p) => [Math.floor(new Date(p.t).getTime() / hour) * hour, p]))
  return Array.from({ length: 24 }, (_, i) => {
    const t = end - (23 - i) * hour
    return byT.get(t) ?? { t: new Date(t).toISOString(), sent: 0, delivered: 0, failed: 0 }
  })
}

function Chart({ points }: { points: Point[] }) {
  const slots = hourSlots(points)
  const total = slots.reduce((a, p) => a + p.sent + p.failed, 0)
  if (total === 0) return <p className="muted">Belum ada pengiriman pada 24 jam terakhir.</p>
  const max = Math.max(1, ...slots.map((p) => Math.max(p.sent, p.delivered) + p.failed))
  return (
    <div>
      <div className="bars" role="img" aria-label={`Grafik pengiriman per jam selama 24 jam terakhir: ${total} pesan`}>
        {slots.map((p) => (
          <div className="b" key={p.t} title={`${fmtTime(p.t)}: terkirim ${p.sent}, sampai ${p.delivered}, gagal ${p.failed}`}>
            <i className="f" style={{ height: `${(p.failed / max) * 100}%` }} />
            <i className="d" style={{ height: `${(p.delivered / max) * 100}%` }} />
            <i className="s" style={{ height: `${(Math.max(0, p.sent - p.delivered) / max) * 100}%` }} />
          </div>
        ))}
      </div>
      <div className="row spread hint" style={{ marginTop: 4 }}><span>24 jam lalu</span><span>sekarang</span></div>
      <div className="row hint" style={{ marginTop: 6 }}>
        <span><i style={{ color: 'var(--ok)' }}>■</i> sampai</span><span><i style={{ color: 'var(--ok)', opacity: .6 }}>■</i> terkirim (menunggu laporan)</span><span><i style={{ color: 'var(--bad)' }}>■</i> gagal</span>
      </div>
    </div>
  )
}

export function Overview() {
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  const [confirm, setConfirm] = useState(false)
  const ov = useQuery({ queryKey: ['overview'], queryFn: () => get<Ov>('/stats/overview') })
  const series = useQuery({
    queryKey: ['series'],
    queryFn: () => { const to = new Date(Date.now() + 3600_000); const from = new Date(Date.now() - 24 * 3600_000); return get<{ points: Point[] }>('/stats/timeseries' + qs({ from: from.toISOString(), to: to.toISOString(), bucket: 'hour' })) },
  })
  const devices = useQuery({ queryKey: ['devices'], queryFn: () => get<Page<Device>>('/devices') })
  const pause = useMutation({
    mutationFn: (paused: boolean) => put('/settings/dispatch-pause', { paused }),
    onSuccess: () => { qc.invalidateQueries(); toast('Status pengiriman diperbarui') }, onError: (e) => toast(errMsg(e), true),
  })
  if (ov.isLoading) return <Loading />
  if (ov.error) return <ErrorBox error={ov.error} />
  const o = ov.data!
  const attention = (devices.data?.items ?? []).filter((d) => d.status !== 'online' && d.status !== 'revoked' || d.sims.some((s) => s.status === 'no_service'))
  return (
    <div className="stack gap-lg">
      <div className="row spread"><h1>Overview</h1>
        {isAdmin(me) && !o.paused && <button className="danger" onClick={() => setConfirm(true)}>⏸ Pause semua pengiriman</button>}
      </div>
      <div className="grid cols-4">
        <Stat label="Device online" value={o.devices.online} tone="ok" />
        <Stat label="Terganggu / offline" value={`${o.devices.degraded} / ${o.devices.offline}`} tone={o.devices.degraded + o.devices.offline > 0 ? 'warn' : undefined} />
        <Stat label="SIM aktif" value={o.sims_active} />
        <Stat label="Antrean" value={o.queue_depth} />
        <Stat label="Terkirim hari ini" value={o.today.sent} />
        <Stat label="Sampai hari ini" value={o.today.delivered} tone="ok" />
        <Stat label="Gagal hari ini" value={o.today.failed} tone={o.today.failed ? 'bad' : undefined} />
        <Stat label="Sukses 24 jam" value={pct(o.success_rate_24h)} />
      </div>
      <div className="card"><h2>Pengiriman 24 jam terakhir</h2>{series.data ? <Chart points={series.data.points} /> : <Loading />}</div>
      <div className="card">
        <h2>Perlu perhatian</h2>
        {attention.length === 0 ? <p className="muted">Semua device dan SIM normal.</p> : (
          <table><tbody>{attention.map((d) => (
            <tr key={d.id}>
              <td><Link to={`/devices/${d.id}`}>{d.name}</Link></td>
              <td><Badge status={d.status} /></td>
              <td className="muted">{d.status_reason ? (ERROR_LABEL[d.status_reason] ?? d.status_reason) : ''} {d.sims.filter((s) => s.status === 'no_service').map((s) => `SIM ${s.slot_index + 1} tanpa sinyal`).join(', ')}</td>
              <td className="muted right">{rel(d.last_seen_at)}</td>
            </tr>))}</tbody></table>
        )}
      </div>
      <Confirm open={confirm} onClose={() => setConfirm(false)} title="Pause semua pengiriman?" confirmWord="PAUSE"
        text="Dispatcher berhenti melepas pesan baru ke semua HP. Yang sedang dikirim diselesaikan. Lanjutkan kapan saja."
        onConfirm={() => { setConfirm(false); pause.mutate(true) }} />
    </div>
  )
}
