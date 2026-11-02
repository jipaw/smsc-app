import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { del, get, post, put } from '../lib/api'
import { fmtTime } from '../lib/format'
import { isAdmin, useMe } from '../lib/session'
import type { ApiKey, AuditLog, DispatchPolicy, DispatchPolicyInput, Page, Role, Suppression } from '../lib/types'
import { Empty, ErrorBox, errMsg, Field, Loading, Modal, useToast } from '../components/ui'

const TABS = ['Policy', 'Daftar blokir', 'API key', 'Pengguna', 'Audit'] as const

export function Settings() {
  const me = useMe()
  const [tab, setTab] = useState<(typeof TABS)[number]>('Policy')
  if (!isAdmin(me)) return <p className="err">Hanya admin yang dapat membuka pengaturan.</p>
  return (
    <div className="stack">
      <h1>Pengaturan</h1>
      <div className="tabs" role="tablist">{TABS.map((t) => <button key={t} role="tab" aria-selected={tab === t} onClick={() => setTab(t)}>{t}</button>)}</div>
      {tab === 'Policy' && <Policies />}{tab === 'Daftar blokir' && <Suppressions />}{tab === 'API key' && <ApiKeys />}{tab === 'Pengguna' && <Users />}{tab === 'Audit' && <Audit />}
    </div>
  )
}

type NumField = 'min_delay_ms' | 'max_delay_ms' | 'burst_size' | 'burst_pause_ms' | 'max_inflight_per_sim' | 'max_attempts' | 'retry_backoff_ms' | 'message_ttl_seconds'
const NUM_FIELDS: [NumField, string, string][] = [
  ['min_delay_ms', 'Jeda minimum (ms)', 'Jeda acak antar SMS pada SIM yang sama. Mulai konservatif (≥ 3000).'],
  ['max_delay_ms', 'Jeda maksimum (ms)', 'Harus ≥ minimum.'], ['burst_size', 'Burst (jumlah SMS)', 'Setelah N SMS berturut-turut, istirahat.'],
  ['burst_pause_ms', 'Jeda burst (ms)', ''], ['max_inflight_per_sim', 'Konkurensi per SIM', 'Default 1.'],
  ['max_attempts', 'Maks. percobaan', '1–10'], ['retry_backoff_ms', 'Backoff retry (ms)', 'Minimal 1000.'], ['message_ttl_seconds', 'Masa berlaku pesan (detik)', 'Minimal 30.'],
]

function Policies() {
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({ queryKey: ['policies'], queryFn: () => get<{ items: DispatchPolicy[] }>('/dispatch-policies') })
  const [edit, setEdit] = useState<(DispatchPolicyInput & { id?: string; is_default?: boolean }) | null>(null)
  const save = useMutation({
    mutationFn: (p: DispatchPolicyInput & { id?: string }) => (p.id ? put(`/dispatch-policies/${p.id}`, p) : post('/dispatch-policies', p)),
    onSuccess: () => { toast('Policy disimpan'); setEdit(null); qc.invalidateQueries({ queryKey: ['policies'] }) },
  })
  const rm = useMutation({ mutationFn: (id: string) => del(`/dispatch-policies/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: ['policies'] }), onError: (e) => toast(errMsg(e), true) })
  const def = useMutation({ mutationFn: (id: string) => put('/settings/default-policy', { policy_id: id }), onSuccess: () => qc.invalidateQueries({ queryKey: ['policies'] }), onError: (e) => toast(errMsg(e), true) })
  const setNum = (k: NumField, v: string) => setEdit((p) => ({ ...p!, [k]: v === '' ? null : Number(v) }))
  const setRate = (k: 'per_minute' | 'per_hour' | 'per_day', v: string) => setEdit((p) => ({ ...p!, rate_limits: { ...(p!.rate_limits ?? {}), [k]: v === '' ? null : Number(v) } }))
  const win = edit?.send_window?.windows?.[0]
  const setWin = (patch: Partial<{ start: string; end: string }> | null) => setEdit((p) => {
    if (patch === null) return { ...p!, send_window: null }
    const cur = p!.send_window?.windows?.[0] ?? { days: [1, 2, 3, 4, 5, 6, 7], start: '08:00', end: '20:00' }
    return { ...p!, send_window: { timezone: p!.send_window?.timezone ?? 'Asia/Jakarta', windows: [{ ...cur, ...patch }] } }
  })
  return (
    <div className="stack">
      <div className="row spread"><p className="muted">Guardrail (global → device → SIM): nilai paling konservatif menang. Campaign/pesan tidak bisa melonggarkannya.</p><button className="primary" onClick={() => setEdit({ name: '' })}>+ Policy</button></div>
      {q.isLoading && <Loading />}<ErrorBox error={q.error} />
      <div className="card scroll-x"><table><thead><tr><th>Nama</th><th>Jeda</th><th>Rate (mnt/jam/hari)</th><th>Jendela</th><th /></tr></thead>
        <tbody>{q.data?.items.map((p) => (
          <tr key={p.id}><td>{p.name} {p.is_default && <span className="badge info">default</span>}</td>
            <td>{p.min_delay_ms ?? '—'}–{p.max_delay_ms ?? '—'} ms</td>
            <td>{p.rate_limits?.per_minute ?? '—'} / {p.rate_limits?.per_hour ?? '—'} / {p.rate_limits?.per_day ?? '—'}</td>
            <td>{p.send_window?.windows?.[0] ? `${p.send_window.windows[0].start}–${p.send_window.windows[0].end} ${p.send_window.timezone}` : '—'}</td>
            <td className="right"><span className="row" style={{ justifyContent: 'flex-end' }}>
              <button className="small" onClick={() => setEdit({ ...p })}>Ubah</button>
              {!p.is_default && <button className="small" onClick={() => def.mutate(p.id)}>Jadikan default</button>}
              {!p.is_default && <button className="small danger" onClick={() => window.confirm(`Hapus "${p.name}"?`) && rm.mutate(p.id)}>Hapus</button>}</span></td></tr>))}</tbody></table></div>
      <Modal open={!!edit} onClose={() => setEdit(null)} title={edit?.id ? 'Ubah policy' : 'Policy baru'} wide>
        {edit && (<div className="stack">
          <Field label="Nama"><input value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} /></Field>
          <div className="grid cols-4">{NUM_FIELDS.map(([k, l, h]) => <Field key={k} label={l} hint={h}><input type="number" value={edit[k] ?? ''} placeholder={edit.is_default ? '' : 'warisi'} onChange={(e) => setNum(k, e.target.value)} /></Field>)}</div>
          <div className="grid cols-4">{(['per_minute', 'per_hour', 'per_day'] as const).map((k) => <Field key={k} label={`Batas ${k.replace('per_', 'per ')}`} hint="kosong = tak dibatasi / warisi"><input type="number" value={edit.rate_limits?.[k] ?? ''} onChange={(e) => setRate(k, e.target.value)} /></Field>)}</div>
          <div className="row"><strong>Jendela kirim</strong>
            {win ? (<><input type="time" value={win.start} onChange={(e) => setWin({ start: e.target.value })} /> – <input type="time" value={win.end} onChange={(e) => setWin({ end: e.target.value })} /><span className="muted">{edit.send_window?.timezone}</span><button className="small" onClick={() => setWin(null)}>Hapus</button></>) : <button className="small" onClick={() => setWin({})}>Atur jendela</button>}
            <span className="hint">00:00–00:00 = sepanjang hari; mulai &gt; selesai = melewati tengah malam.</span></div>
          <ErrorBox error={save.error} />
          <div className="row" style={{ justifyContent: 'flex-end' }}><button onClick={() => setEdit(null)}>Batal</button><button className="primary" disabled={save.isPending} onClick={() => save.mutate(edit)}>Simpan</button></div>
        </div>)}
      </Modal>
    </div>
  )
}

function Suppressions() {
  const qc = useQueryClient()
  const toast = useToast()
  const [nums, setNums] = useState('')
  const q = useQuery({ queryKey: ['suppressions'], queryFn: () => get<Page<Suppression>>('/suppressions') })
  const add = useMutation({ mutationFn: () => post<{ added: number; invalid: string[] }>('/suppressions', { numbers: nums.split(/[\s,;]+/).filter(Boolean), reason: 'ditambahkan dari dashboard' }),
    onSuccess: (r) => { toast(`${r.added} ditambahkan${r.invalid?.length ? `, ${r.invalid.length} tidak valid` : ''}`); setNums(''); qc.invalidateQueries({ queryKey: ['suppressions'] }) } })
  const rm = useMutation({ mutationFn: (id: string) => del(`/suppressions/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: ['suppressions'] }) })
  return (
    <div className="stack">
      <div className="card stack"><Field label="Tambah nomor yang tidak boleh dikirimi (opt-out)"><textarea value={nums} onChange={(e) => setNums(e.target.value)} placeholder="08111222333" /></Field><ErrorBox error={add.error} /><div><button className="primary" disabled={!nums.trim() || add.isPending} onClick={() => add.mutate()}>Tambahkan</button></div></div>
      {q.data?.items.length === 0 && <Empty>Daftar blokir kosong.</Empty>}
      {q.data && q.data.items.length > 0 && <div className="card"><table><thead><tr><th>Nomor</th><th>Alasan</th><th>Dibuat</th><th /></tr></thead><tbody>{q.data.items.map((s) => <tr key={s.id}><td className="mono">{s.number}</td><td>{s.reason}</td><td className="muted">{fmtTime(s.created_at)}</td><td className="right"><button className="small danger" onClick={() => rm.mutate(s.id)}>Hapus</button></td></tr>)}</tbody></table></div>}
    </div>
  )
}

function ApiKeys() {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>(['messages:send', 'messages:read'])
  const [secret, setSecret] = useState('')
  const q = useQuery({ queryKey: ['apikeys'], queryFn: () => get<{ items: ApiKey[] }>('/api-keys') })
  const create = useMutation({ mutationFn: () => post<ApiKey & { secret: string }>('/api-keys', { name, scopes }), onSuccess: (k) => { setSecret(k.secret); setName(''); qc.invalidateQueries({ queryKey: ['apikeys'] }) } })
  const rm = useMutation({ mutationFn: (id: string) => del(`/api-keys/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: ['apikeys'] }) })
  return (
    <div className="stack">
      <div className="card stack"><Field label="Nama key"><input value={name} onChange={(e) => setName(e.target.value)} placeholder="sistem-billing" /></Field>
        <div className="row">{['messages:send', 'messages:read'].map((s) => <label key={s} className="row"><input type="checkbox" checked={scopes.includes(s)} onChange={(e) => setScopes((p) => e.target.checked ? [...p, s] : p.filter((x) => x !== s))} />{s}</label>)}</div>
        <ErrorBox error={create.error} /><div><button className="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Buat API key</button></div>
        {secret && <div className="okbox" role="status">Salin sekarang, tidak akan ditampilkan lagi:<br /><span className="mono">{secret}</span> <button className="small" onClick={() => navigator.clipboard?.writeText(secret)}>Salin</button></div>}</div>
      {q.data && <div className="card"><table><thead><tr><th>Nama</th><th>Prefix</th><th>Scope</th><th>Terakhir dipakai</th><th /></tr></thead><tbody>{q.data.items.map((k) => <tr key={k.id}><td>{k.name}</td><td className="mono">{k.prefix}</td><td>{k.scopes.join(', ')}</td><td className="muted">{fmtTime(k.last_used_at)}</td><td className="right"><button className="small danger" onClick={() => window.confirm(`Cabut key "${k.name}"?`) && rm.mutate(k.id)}>Cabut</button></td></tr>)}</tbody></table></div>}
    </div>
  )
}

function Users() {
  const qc = useQueryClient()
  const [f, setF] = useState<{ email: string; role: Role; password: string }>({ email: '', role: 'operator', password: '' })
  const q = useQuery({ queryKey: ['users'], queryFn: () => get<{ items: { id: string; email: string; role: string }[] }>('/users') })
  const create = useMutation({ mutationFn: () => post('/users', f), onSuccess: () => { setF({ email: '', role: 'operator', password: '' }); qc.invalidateQueries({ queryKey: ['users'] }) } })
  return (
    <div className="stack">
      <div className="card stack"><div className="grid cols-4"><Field label="Email"><input type="email" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Field>
        <Field label="Peran"><select value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as Role })}><option>admin</option><option>operator</option><option>viewer</option></select></Field>
        <Field label="Password" hint="min. 12 karakter"><input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field></div>
        <ErrorBox error={create.error} /><div><button className="primary" disabled={create.isPending} onClick={() => create.mutate()}>Tambah pengguna</button></div></div>
      <div className="card"><table><thead><tr><th>Email</th><th>Peran</th></tr></thead><tbody>{q.data?.items.map((u) => <tr key={u.id}><td>{u.email}</td><td>{u.role}</td></tr>)}</tbody></table></div>
    </div>
  )
}

function Audit() {
  const q = useQuery({ queryKey: ['audit'], queryFn: () => get<Page<AuditLog>>('/audit-logs?limit=100') })
  return (
    <div className="card scroll-x">{q.isLoading && <Loading />}<ErrorBox error={q.error} />
      <table><thead><tr><th>Waktu</th><th>Pelaku</th><th>Aksi</th><th>Target</th></tr></thead><tbody>{q.data?.items.map((a) => <tr key={a.id}><td className="muted">{fmtTime(a.at)}</td><td>{a.actor}</td><td className="mono">{a.action}</td><td className="mono">{a.target_type} {a.target_id?.slice(0, 8)}</td></tr>)}</tbody></table></div>
  )
}
