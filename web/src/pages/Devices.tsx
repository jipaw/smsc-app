import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from 'react-router-dom'
import QRCode from 'qrcode'
import { del, get, patch, post } from '../lib/api'
import { fmtTime, rel, ERROR_LABEL } from '../lib/format'
import { isAdmin, useMe } from '../lib/session'
import type { Device, Enrollment, Page, Sim } from '../lib/types'
import { Badge, Confirm, Empty, ErrorBox, errMsg, Field, Loading, Modal, useToast } from '../components/ui'

function AddDevice({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [enr, setEnr] = useState<Enrollment | null>(null)
  const [qr, setQr] = useState('')
  const [left, setLeft] = useState(0)
  const [err, setErr] = useState<unknown>(null)
  const baseline = useRef<Set<string>>(new Set())
  const [done, setDone] = useState<string | null>(null)
  const devices = useQuery({ queryKey: ['devices'], queryFn: () => get<Page<Device>>('/devices'), enabled: open })

  useEffect(() => { if (!open) { setEnr(null); setQr(''); setName(''); setErr(null); setDone(null) } }, [open])
  useEffect(() => {
    if (!enr) return
    QRCode.toDataURL(enr.enroll_url, { margin: 1, width: 240 }).then(setQr)
    const tick = () => setLeft(Math.max(0, Math.round((new Date(enr.expires_at).getTime() - Date.now()) / 1000)))
    tick() // isi segera; tanpa ini sempat tampil "Kedaluwarsa" selama tick pertama
    const t = window.setInterval(tick, 500)
    return () => window.clearInterval(t)
  }, [enr])
  // deteksi device baru lewat SSE -> invalidasi 'devices'
  useEffect(() => {
    if (!enr || done) return
    const fresh = (devices.data?.items ?? []).find((d) => !baseline.current.has(d.id) && d.status !== 'pending')
    if (fresh) setDone(fresh.name)
  }, [devices.data, enr, done])

  const create = useMutation({
    mutationFn: () => post<Enrollment>('/device-enrollments', { device_name: name || undefined }),
    onSuccess: (e) => { baseline.current = new Set((devices.data?.items ?? []).map((d) => d.id)); setEnr(e); qc.invalidateQueries({ queryKey: ['devices'] }) },
    onError: setErr,
  })
  return (
    <Modal open={open} onClose={onClose} title="Tambah device">
      {!enr ? (
        <>
          <Field label="Nama device (opsional)"><input value={name} onChange={(e) => setName(e.target.value)} placeholder="HP Gateway 1" /></Field>
          <ErrorBox error={err} />
          <div className="row" style={{ justifyContent: 'flex-end' }}><button className="primary" onClick={() => create.mutate()} disabled={create.isPending}>Buat QR</button></div>
        </>
      ) : done ? (
        <div className="okbox" role="status">✓ Device “{done}” terhubung. Lihat SIM-nya di daftar device.</div>
      ) : (
        <>
          <div className="qr">{qr ? <img src={qr} width={240} height={240} alt="QR enrollment untuk aplikasi SMSC" /> : <Loading what="QR" />}</div>
          <div className="stack" style={{ textAlign: 'center' }}>
            <div>Kode manual: <strong className="mono" style={{ fontSize: 18 }}>{enr.short_code}</strong></div>
            <div className={left === 0 ? 'err' : 'muted'}>{left === 0 ? 'Kedaluwarsa. Buat ulang.' : `Berlaku ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`} · sekali pakai</div>
            {enr.tailnet_auth_key ? (
              <>
                <div className="okbox">QR ini memuat <strong>auth key Tailscale sekali pakai</strong>: aplikasi bergabung ke tailnet sendiri (tanpa aplikasi Tailscale). Jangan bagikan QR ini.</div>
                <ol className="muted" style={{ textAlign: 'left', margin: 0 }}>
                  <li>Pasang aplikasi SMSC (build tailnet ter-embed) dan beri izin</li><li>Scan QR ini</li>
                </ol>
              </>
            ) : (
              <ol className="muted" style={{ textAlign: 'left', margin: 0 }}>
                <li>Login Tailscale di HP</li><li>Pasang aplikasi SMSC dan beri izin</li><li>Scan QR ini (atau ketik kode manual)</li>
              </ol>
            )}
            <div className="muted" role="status">○ Menunggu perangkat…</div>
          </div>
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <button onClick={() => { setEnr(null); setDone(null) }}>Buat ulang</button>
            <button className="small" onClick={() => navigator.clipboard?.writeText(enr.enroll_url)}>Salin tautan</button>
          </div>
        </>
      )}
    </Modal>
  )
}

export function Devices() {
  const me = useMe()
  const [add, setAdd] = useState(false)
  const q = useQuery({ queryKey: ['devices'], queryFn: () => get<Page<Device>>('/devices') })
  return (
    <div className="stack gap-lg">
      <div className="row spread"><h1>Devices</h1>{isAdmin(me) && <button className="primary" onClick={() => setAdd(true)}>+ Tambah device</button>}</div>
      {q.isLoading && <Loading />}
      <ErrorBox error={q.error} />
      {q.data && q.data.items.length === 0 && <Empty>Belum ada device. Klik “Tambah device” untuk membuat QR enrollment.</Empty>}
      {q.data && q.data.items.length > 0 && (
        <div className="card scroll-x"><table className="cards">
          <thead><tr><th>Nama</th><th>Status</th><th>Baterai</th><th>Jaringan</th><th>SIM</th><th>Versi</th><th>Terakhir</th></tr></thead>
          <tbody>{q.data.items.map((d) => (
            <tr key={d.id}>
              <td data-l="Nama"><Link to={`/devices/${d.id}`}>{d.name}</Link><div className="muted">{d.manufacturer} {d.model}</div></td>
              <td data-l="Status"><Badge status={d.status} />{d.status_reason && <div className="hint">{ERROR_LABEL[d.status_reason] ?? d.status_reason}</div>}</td>
              <td data-l="Baterai">{d.battery_level != null ? `${d.battery_level}%${d.charging ? ' ⚡' : ''}` : '-'}</td>
              <td data-l="Jaringan">{d.network_type ?? '-'}{d.tailnet && <div><span className="badge ok" title={`Node tailnet terverifikasi server (WhoIs): ${d.tailnet.node_name} · ${d.tailnet.tags.join(', ')} · ${fmtTime(d.tailnet.verified_at)}`}>tailnet ✓</span></div>}</td>
              <td data-l="SIM">{d.sims.map((s) => <div key={s.id}>{s.slot_index + 1}: {s.label || s.carrier_name || '-'} <Badge status={s.status} /></div>)}</td>
              <td data-l="Versi" className="mono">{d.app_version ?? '-'}</td>
              <td data-l="Terakhir" className="muted">{rel(d.last_seen_at)}</td>
            </tr>))}</tbody>
        </table></div>
      )}
      <AddDevice open={add} onClose={() => setAdd(false)} />
    </div>
  )
}

const ACTIONS: [string, string][] = [['ping', 'Ping'], ['refresh_sims', 'Refresh SIM'], ['restart_service', 'Restart service'], ['upload_logs', 'Ambil log'], ['rotate_credential', 'Putar kredensial']]

export function DeviceDetail() {
  const { id } = useParams()
  const me = useMe()
  const nav = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const [revoke, setRevoke] = useState(false)
  const q = useQuery({ queryKey: ['device', id], queryFn: () => get<Device>(`/devices/${id}`) })
  const cmd = useMutation({ mutationFn: (action: string) => post(`/devices/${id}/commands`, { action }), onSuccess: () => toast('Perintah dikirim'), onError: (e) => toast(errMsg(e), true) })
  const upd = useMutation({ mutationFn: (b: object) => patch(`/devices/${id}`, b), onSuccess: () => qc.invalidateQueries(), onError: (e) => toast(errMsg(e), true) })
  const rev = useMutation({ mutationFn: () => del(`/devices/${id}`), onSuccess: () => { toast('Device dicabut'); qc.invalidateQueries(); nav('/devices') }, onError: (e) => toast(errMsg(e), true) })
  if (q.isLoading) return <Loading />
  if (q.error) return <ErrorBox error={q.error} />
  const d = q.data!
  const caps = (d.capabilities ?? {}) as Record<string, boolean>
  const admin = isAdmin(me)
  const capRows: [string, string, string][] = [
    ['battery_optimization_ignored', 'Pengecualian optimasi baterai', 'Tanpa ini HP bisa memutus koneksi saat layar mati.'],
    ['tailscale_vpn_active', 'Tailscale aktif', 'Koneksi ke server lewat tailnet.'],
    ['os_sms_limit_unlocked', 'Batas kirim OS tidak berlaku', 'Jika tidak, Android dapat membatasi ±25–30 SMS per 30 menit per HP.'],
    ['default_sms_app', 'Aplikasi SMS default', 'Opsional; dapat memengaruhi batas OS.'],
  ]
  return (
    <div className="stack gap-lg">
      <div className="row spread"><div><h1>{d.name}</h1><div className="muted">{d.manufacturer} {d.model} · Android {d.android_version} · app {d.app_version}</div></div><Badge status={d.status} /></div>
      <div className="grid cols-2">
        <div className="card stack">
          <h2>Kesehatan</h2>
          <div>Terakhir terlihat: {fmtTime(d.last_seen_at)} ({rel(d.last_seen_at)})</div>
          <div>Baterai: {d.battery_level ?? '-'}% {d.charging ? '(mengisi)' : ''} · Jaringan: {d.network_type ?? '-'}</div>
          <div>Identitas tailnet: {d.tailnet
            ? <><span className="badge ok">terverifikasi server</span> <span className="mono">{d.tailnet.node_name}</span> · {d.tailnet.tags.join(', ') || 'tanpa tag'} · {rel(d.tailnet.verified_at)}</>
            : <span className="muted">belum terverifikasi (bukan lewat tailnet ter-embed smscd)</span>}</div>
          {d.status_reason && <div className="badge warn">{ERROR_LABEL[d.status_reason] ?? d.status_reason}</div>}
          <h3>Capability</h3>
          <ul style={{ margin: 0, paddingLeft: 18 }}>{capRows.map(([k, l, h]) => <li key={k}>{caps[k] ? '✓' : '✗'} {l}{!caps[k] && <div className="hint">{h}</div>}</li>)}</ul>
        </div>
        <div className="card stack">
          <h2>Tindakan</h2>
          {admin ? (<>
            <div className="row">{ACTIONS.map(([a, l]) => <button key={a} className="small" disabled={d.status === 'offline' || cmd.isPending} onClick={() => (a !== 'rotate_credential' || window.confirm('Putar kredensial HP ini? Secret lama tetap berlaku 60 detik setelah HP mengonfirmasi.')) && cmd.mutate(a)}>{l}</button>)}</div>
            <label className="row"><input type="checkbox" checked={d.enabled} onChange={(e) => upd.mutate({ enabled: e.target.checked })} /> Device aktif (menerima pesan baru)</label>
            <div><button className="danger" onClick={() => setRevoke(true)}>Cabut device</button></div>
          </>) : <p className="muted">Hanya admin yang dapat mengubah device.</p>}
        </div>
      </div>
      <div className="card scroll-x">
        <h2>SIM</h2>
        <table><thead><tr><th>Slot</th><th>Operator</th><th>Label</th><th>Sinyal</th><th>Status</th><th>Hari ini</th><th>Berikutnya</th><th>Aktif</th></tr></thead>
          <tbody>{d.sims.map((s) => <SimRow key={s.id} s={s} admin={admin} />)}</tbody></table>
      </div>
      <Confirm open={revoke} onClose={() => setRevoke(false)} title="Cabut device?" confirmWord={d.name}
        text="HP langsung terputus dan harus didaftarkan ulang. Pesan antre tidak hilang; yang sedang dikirim menjadi 'tidak pasti'."
        onConfirm={() => { setRevoke(false); rev.mutate() }} />
    </div>
  )
}

function SimRow({ s, admin }: { s: Sim; admin: boolean }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [label, setLabel] = useState(s.label ?? '')
  const upd = useMutation({ mutationFn: (b: object) => patch(`/sims/${s.id}`, b), onSuccess: () => qc.invalidateQueries(), onError: (e) => toast(errMsg(e), true) })
  const next = useMemo(() => (s.next_eligible_at && new Date(s.next_eligible_at) > new Date() ? fmtTime(s.next_eligible_at) : 'siap'), [s.next_eligible_at])
  return (
    <tr>
      <td>{s.slot_index + 1}{s.is_esim ? ' (eSIM)' : ''}</td><td>{s.carrier_name ?? '-'}</td>
      <td>{admin ? <input value={label} onChange={(e) => setLabel(e.target.value)} onBlur={() => label !== (s.label ?? '') && upd.mutate({ label })} aria-label={`Label SIM ${s.slot_index + 1}`} /> : s.label}</td>
      <td>{s.signal_level != null ? '▂▄▆█'.slice(0, Math.max(1, s.signal_level)) + ` (${s.signal_level}/4)` : '-'}</td>
      <td><Badge status={s.status} /></td>
      <td className="muted">{s.today.sent}✓ {s.today.delivered}✓✓ {s.today.failed}✗</td><td className="muted">{next}</td>
      <td><input type="checkbox" checked={s.enabled} disabled={!admin} onChange={(e) => upd.mutate({ enabled: e.target.checked })} aria-label={`SIM ${s.slot_index + 1} aktif`} /></td>
    </tr>
  )
}

export function Sims() {
  const q = useQuery({ queryKey: ['sims'], queryFn: () => get<{ items: Sim[] }>('/sims') })
  const devs = useQuery({ queryKey: ['devices'], queryFn: () => get<Page<Device>>('/devices') })
  const me = useMe()
  const name = (id: string) => devs.data?.items.find((d) => d.id === id)?.name ?? id.slice(0, 8)
  return (
    <div className="stack gap-lg"><h1>SIM</h1>
      {q.isLoading && <Loading />}<ErrorBox error={q.error} />
      {q.data && q.data.items.length === 0 && <Empty>Belum ada SIM. SIM muncul otomatis setelah device terdaftar.</Empty>}
      {q.data && q.data.items.length > 0 && <div className="card scroll-x"><table><thead><tr><th>Device</th><th>Slot</th><th>Operator</th><th>Label</th><th>Sinyal</th><th>Status</th><th>Hari ini</th><th>Berikutnya</th><th>Aktif</th></tr></thead>
        <tbody>{q.data.items.map((s) => <SimRowFull key={s.id} s={s} dev={name(s.device_id)} admin={isAdmin(me)} />)}</tbody></table></div>}
    </div>
  )
}

function SimRowFull({ s, dev, admin }: { s: Sim; dev: string; admin: boolean }) {
  return <><tr><td>{dev}</td><td>{s.slot_index + 1}</td><td>{s.carrier_name ?? '-'}</td><td>{s.label ?? ''}</td><td>{s.signal_level ?? '-'}</td><td><Badge status={s.status} /></td>
    <td className="muted">{s.today.sent}✓ {s.today.delivered}✓✓ {s.today.failed}✗</td><td className="muted">{s.next_eligible_at && new Date(s.next_eligible_at) > new Date() ? fmtTime(s.next_eligible_at) : 'siap'}</td>
    <td><SimToggle s={s} admin={admin} /></td></tr></>
}

function SimToggle({ s, admin }: { s: Sim; admin: boolean }) {
  const qc = useQueryClient()
  const toast = useToast()
  const upd = useMutation({ mutationFn: (b: object) => patch(`/sims/${s.id}`, b), onSuccess: () => qc.invalidateQueries(), onError: (e) => toast(errMsg(e), true) })
  return <input type="checkbox" checked={s.enabled} disabled={!admin} onChange={(e) => upd.mutate({ enabled: e.target.checked })} aria-label={`SIM ${s.slot_index + 1} aktif`} />
}
