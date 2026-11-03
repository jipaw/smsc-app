import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { del, get, patch, post, put } from '../lib/api'
import { fmtTime } from '../lib/format'
import { isAdmin, useMe } from '../lib/session'
import type { ApiKey, AuditLog, DispatchPolicy, DispatchPolicyInput, Page, RetentionSettings, Role, SecuritySettings, Suppression, Invitation, EmailConfig, SSOView, ScimSettings, ScimToken, UserItem, Webhook, WebhookCreated, WebhookDelivery, WebhookEvent } from '../lib/types'
import { Empty, ErrorBox, errMsg, Field, Loading, Modal, useToast } from '../components/ui'

const TABS = ['Policy', 'Daftar blokir', 'API key', 'Webhook', 'Retensi', 'Keamanan', 'SSO', 'SCIM', 'Email', 'Pengguna', 'Audit'] as const

export function Settings() {
  const me = useMe()
  const [tab, setTab] = useState<(typeof TABS)[number]>('Policy')
  if (!isAdmin(me)) return <p className="err">Hanya admin yang dapat membuka pengaturan.</p>
  return (
    <div className="stack">
      <h1>Pengaturan</h1>
      <div className="tabs" role="tablist">{TABS.map((t) => <button key={t} role="tab" aria-selected={tab === t} onClick={() => setTab(t)}>{t}</button>)}</div>
      {tab === 'Policy' && <Policies />}{tab === 'Daftar blokir' && <Suppressions />}{tab === 'API key' && <ApiKeys />}{tab === 'Webhook' && <Webhooks />}{tab === 'Retensi' && <Retention />}{tab === 'Keamanan' && <Security />}{tab === 'SSO' && <SSOSettings />}{tab === 'SCIM' && <ScimSettingsTab />}{tab === 'Email' && <EmailSettings />}{tab === 'Pengguna' && <Users />}{tab === 'Audit' && <Audit />}
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
  const [grace, setGrace] = useState(3600)
  const rotate = useMutation({ mutationFn: (id: string) => post<ApiKey & { secret: string }>(`/api-keys/${id}/rotate`, { grace_seconds: grace }), onSuccess: (k) => { setSecret(k.secret); qc.invalidateQueries({ queryKey: ['apikeys'] }) } })
  return (
    <div className="stack">
      <div className="card stack"><Field label="Nama key"><input value={name} onChange={(e) => setName(e.target.value)} placeholder="sistem-billing" /></Field>
        <div className="row">{['messages:send', 'messages:read'].map((s) => <label key={s} className="row"><input type="checkbox" checked={scopes.includes(s)} onChange={(e) => setScopes((p) => e.target.checked ? [...p, s] : p.filter((x) => x !== s))} />{s}</label>)}</div>
        <ErrorBox error={create.error} /><div><button className="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Buat API key</button></div>
        <div className="row"><label className="row">Masa tumpang tindih saat memutar <select aria-label="Masa tumpang tindih secret lama" value={grace} onChange={(e) => setGrace(Number(e.target.value))}><option value={0}>langsung matikan</option><option value={3600}>1 jam</option><option value={86400}>24 jam</option><option value={604800}>7 hari</option></select></label></div>
        <ErrorBox error={rotate.error} />
        {secret && <div className="okbox" role="status">Salin sekarang, tidak akan ditampilkan lagi:<br /><span className="mono">{secret}</span> <button className="small" onClick={() => navigator.clipboard?.writeText(secret)}>Salin</button></div>}</div>
      {q.data && <div className="card"><table><thead><tr><th>Nama</th><th>Prefix</th><th>Scope</th><th>Terakhir dipakai</th><th>Rotasi</th><th /></tr></thead><tbody>{q.data.items.map((k) => <tr key={k.id}><td>{k.name}</td><td className="mono">{k.prefix}</td><td>{k.scopes.join(', ')}</td><td className="muted">{fmtTime(k.last_used_at)}</td><td className="muted">{k.rotated_at ? fmtTime(k.rotated_at) : '—'}{k.previous_valid_until && <div>secret lama sah s.d. {fmtTime(k.previous_valid_until)}</div>}</td><td className="right"><button className="small" disabled={rotate.isPending} onClick={() => window.confirm(`Putar secret key "${k.name}"? Secret baru tampil sekali; secret lama tetap sah selama masa tumpang tindih yang dipilih.`) && rotate.mutate(k.id)}>Putar</button> <button className="small danger" onClick={() => window.confirm(`Cabut key "${k.name}"?`) && rm.mutate(k.id)}>Cabut</button></td></tr>)}</tbody></table></div>}
    </div>
  )
}

function Users() {
  const qc = useQueryClient()
  const me = useMe()
  const toast = useToast()
  const [f, setF] = useState<{ email: string; role: Role; password: string }>({ email: '', role: 'operator', password: '' })
  const [pwFor, setPwFor] = useState<UserItem | null>(null)
  const [newPw, setNewPw] = useState('')
  const q = useQuery({ queryKey: ['users'], queryFn: () => get<{ items: UserItem[] }>('/users') })
  const refresh = () => qc.invalidateQueries({ queryKey: ['users'] })
  const reset = useMutation({ mutationFn: (id: string) => del(`/users/${id}/2fa`), onSuccess: refresh })
  const edit = useMutation({ mutationFn: (v: { id: string; body: { role?: Role; disabled?: boolean } }) => patch(`/users/${v.id}`, v.body), onSuccess: refresh, onError: (e) => toast(errMsg(e), true) })
  const setPw = useMutation({ mutationFn: () => post(`/users/${pwFor!.id}/password`, { new_password: newPw }), onSuccess: () => { toast('Password diganti; sesi pengguna dikeluarkan'); setPwFor(null); setNewPw('') } })
  const create = useMutation({ mutationFn: () => post('/users', f), onSuccess: () => { setF({ email: '', role: 'operator', password: '' }); refresh() } })
  const delUser = useMutation({ mutationFn: (id: string) => del(`/users/${id}`), onSuccess: () => { toast('Pengguna dihapus'); refresh() }, onError: (e) => toast(errMsg(e), true) })
  return (
    <div className="stack">
      <Invitations />
      <div className="card stack"><div className="grid cols-4"><Field label="Email"><input type="email" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Field>
        <Field label="Peran"><select value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as Role })}><option>admin</option><option>operator</option><option>viewer</option></select></Field>
        <Field label="Password" hint="min. 12 karakter"><input type="password" autoComplete="new-password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} /></Field></div>
        <ErrorBox error={create.error} /><div><button className="primary" disabled={create.isPending} onClick={() => create.mutate()}>Tambah pengguna</button></div></div>
      <div className="card scroll-x"><table><thead><tr><th>Email</th><th>Peran</th><th>Status</th><th>2FA</th><th /></tr></thead><tbody>{q.data?.items.map((u) => {
        const self = u.id === me.id
        return (
          <tr key={u.id}><td>{u.email}{self && <span className="muted"> (Anda)</span>}</td>
            <td>{self ? u.role : <select aria-label={`Peran ${u.email}`} value={u.role} onChange={(e) => edit.mutate({ id: u.id, body: { role: e.target.value as Role } })}><option>admin</option><option>operator</option><option>viewer</option></select>}</td>
            <td>{u.disabled ? 'nonaktif' : 'aktif'}{u.sso_linked && <span className="badge muted" title="Tertaut ke SSO"> SSO</span>}{u.scim_managed && <span className="badge muted" title="Diprovisi IdP lewat SCIM"> SCIM</span>}{u.has_password === false && <span className="muted" title="Hanya bisa masuk lewat SSO"> · tanpa password</span>}</td><td>{u.mfa_enabled ? 'aktif' : '—'}</td>
            <td className="right"><span className="row">
              {!self && <button className="small" onClick={() => { setPwFor(u); setNewPw('') }}>Set password</button>}
              {!self && <button className={`small${u.disabled ? '' : ' danger'}`} onClick={() => (u.disabled || window.confirm(`Nonaktifkan ${u.email}? Semua sesinya dikeluarkan.`)) && edit.mutate({ id: u.id, body: { disabled: !u.disabled } })}>{u.disabled ? 'Aktifkan' : 'Nonaktifkan'}</button>}
              {u.disabled && !self && <button className="small danger" onClick={() => window.confirm(`Hapus ${u.email} secara permanen? API key/webhook buatannya tetap ada; jejak audit tetap menyimpan email.`) && delUser.mutate(u.id)}>Hapus</button>}
              {u.mfa_enabled && <button className="small danger" onClick={() => window.confirm(`Reset 2FA ${u.email}? Semua sesinya dikeluarkan dan ia harus menyiapkan 2FA lagi.`) && reset.mutate(u.id)}>Reset 2FA</button>}</span></td></tr>)
      })}</tbody></table></div>
      <Modal open={!!pwFor} onClose={() => setPwFor(null)} title={`Set password — ${pwFor?.email ?? ''}`}>
        <div className="stack">
          <p className="muted">Semua sesi pengguna ini dikeluarkan dan kunci akunnya direset. Sampaikan password baru lewat jalur aman.</p>
          <Field label="Password baru" hint="min. 12 karakter"><input type="password" autoComplete="new-password" value={newPw} onChange={(e) => setNewPw(e.target.value)} /></Field>
          <ErrorBox error={setPw.error} />
          <div><button className="primary" disabled={newPw.length < 12 || setPw.isPending} onClick={() => setPw.mutate()}>Simpan password</button></div>
        </div>
      </Modal>
    </div>
  )
}

function Invitations() {
  const qc = useQueryClient()
  const toast = useToast()
  const [f, setF] = useState<{ email: string; role: Role; ttl: number; mail: boolean }>({ email: '', role: 'operator', ttl: 168, mail: false })
  const [made, setMade] = useState<Invitation | null>(null)
  const cfg = useQuery({ queryKey: ['email-config'], queryFn: () => get<EmailConfig>('/settings/email') })
  const mailOk = !!cfg.data?.configured
  const q = useQuery({ queryKey: ['invitations'], queryFn: () => get<{ items: Invitation[] }>('/invitations'),
    refetchInterval: (query) => (query.state.data?.items.some((i) => i.email_status === 'queued') ? 2000 : false) }) // pantau email yang masih antre
  const refresh = () => qc.invalidateQueries({ queryKey: ['invitations'] })
  const create = useMutation({ mutationFn: () => post<Invitation>('/invitations', { email: f.email, role: f.role, ttl_hours: f.ttl, send_email: f.mail && mailOk }), onSuccess: (v) => { setMade(v); setF({ ...f, email: '' }); refresh() } })
  const resend = useMutation({ mutationFn: (i: Invitation) => post<Invitation>('/invitations', { email: i.email, role: i.role, send_email: true }), onSuccess: (v) => { setMade(v); toast('Undangan baru dibuat dan dikirim ulang; tautan lama tidak berlaku'); refresh() }, onError: (e) => toast(errMsg(e), true) })
  const revoke = useMutation({ mutationFn: (id: string) => del(`/invitations/${id}`), onSuccess: refresh, onError: (e) => toast(errMsg(e), true) })
  return (
    <section className="card stack" aria-label="Undang pengguna">
      <h2>Undang pengguna</h2>
      <p className="muted">Membuat tautan sekali pakai; penerima menetapkan passwordnya sendiri. Sistem tidak mengirim email — bagikan tautannya lewat jalur yang aman.</p>
      <div className="grid cols-4">
        <Field label="Email penerima"><input type="email" value={f.email} onChange={(e) => setF({ ...f, email: e.target.value })} /></Field>
        <Field label="Peran"><select value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as Role })}><option>admin</option><option>operator</option><option>viewer</option></select></Field>
        <Field label="Berlaku"><select value={f.ttl} onChange={(e) => setF({ ...f, ttl: Number(e.target.value) })}><option value={24}>1 hari</option><option value={168}>7 hari</option><option value={720}>30 hari</option></select></Field>
      </div>
      <label className="row"><input type="checkbox" checked={f.mail && mailOk} disabled={!mailOk} onChange={(e) => setF({ ...f, mail: e.target.checked })} /> Kirim tautan ke email penerima{!mailOk && <span className="muted"> (SMTP belum dikonfigurasi di server — bagikan tautan secara manual)</span>}</label>
      <ErrorBox error={create.error} />
      <div><button className="primary" disabled={!f.email || create.isPending} onClick={() => create.mutate()}>Buat undangan</button></div>
      {made?.invite_url && <div className="okbox" role="status">Tautan undangan untuk {made.email} — salin sekarang, tidak akan ditampilkan lagi:<br /><span className="mono" data-testid="invite-url">{made.invite_url}</span> <button className="small" onClick={() => navigator.clipboard?.writeText(made.invite_url!)}>Salin</button></div>}
      {q.data && q.data.items.length > 0 && (
        <div className="scroll-x"><table><thead><tr><th>Email</th><th>Peran</th><th>Status</th><th>Email</th><th>Berlaku sampai</th><th /></tr></thead><tbody>
          {q.data.items.map((i) => (
            <tr key={i.id}><td>{i.email}</td><td>{i.role}</td><td>{{ pending: 'menunggu', accepted: 'diterima', expired: 'kedaluwarsa', revoked: 'dicabut' }[i.status]}</td><td title={i.email_error ?? undefined}>{i.email_status ? { queued: 'antre', sent: 'terkirim', failed: 'gagal', cancelled: 'dibatalkan' }[i.email_status] : '—'}</td><td className="muted">{fmtTime(i.expires_at)}</td>
              <td className="right"><span className="row">{mailOk && (i.status === 'pending' || i.status === 'expired') && <button className="small" disabled={resend.isPending} onClick={() => window.confirm(`Buat undangan baru untuk ${i.email} dan kirim ulang? Tautan lama tidak berlaku lagi.`) && resend.mutate(i)}>Kirim ulang</button>}{(i.status === 'pending' || i.status === 'expired') && <button className="small danger" onClick={() => revoke.mutate(i.id)}>Cabut</button>}</span></td></tr>))}
        </tbody></table></div>)}
    </section>
  )
}

function ScimSettingsTab() {
  const qc = useQueryClient()
  const toast = useToast()
  const cfg = useQuery({ queryKey: ['scim'], queryFn: () => get<ScimSettings>('/settings/scim') })
  const tokens = useQuery({ queryKey: ['scim-tokens'], queryFn: () => get<{ items: ScimToken[] }>('/scim-tokens'), enabled: !!cfg.data?.enabled })
  const [name, setName] = useState('')
  const [secret, setSecret] = useState('')
  const refresh = () => { qc.invalidateQueries({ queryKey: ['scim-tokens'] }); qc.invalidateQueries({ queryKey: ['scim'] }) }
  const create = useMutation({ mutationFn: () => post<ScimToken>('/scim-tokens', { name }), onSuccess: (t) => { setSecret(t.token ?? ''); setName(''); refresh() } })
  const revoke = useMutation({ mutationFn: (id: string) => del(`/scim-tokens/${id}`), onSuccess: () => { toast('Token SCIM dicabut'); refresh() } })
  if (cfg.isLoading) return <Loading what="pengaturan SCIM" />
  if (!cfg.data) return <ErrorBox error={cfg.error} />
  const c = cfg.data
  return (
    <div className="card stack" aria-label="Pengaturan SCIM">
      <h2>Provisioning SCIM 2.0</h2>
      {!c.enabled ? (
        <p className="muted">SCIM belum diaktifkan. Atur <span className="mono">SCIM_ENABLED=true</span> di server lalu restart. Dengan SCIM, IdP (Okta, Entra ID, Keycloak, dll.) membuat, mengubah, dan menonaktifkan pengguna di sini secara otomatis.</p>
      ) : (
        <>
          <dl><dt>SCIM base URL (isi di IdP)</dt><dd className="mono">{c.base_url}</dd><dt>Peran bawaan</dt><dd>{c.default_role}</dd>
            <dt>Boleh memberi peran admin</dt><dd>{c.allow_admin_role ? 'ya' : 'tidak (disarankan)'}</dd>
            <dt>Mengelola pengguna lokal yang sudah ada</dt><dd>{c.adopt_existing ? 'ya' : 'tidak — hanya pengguna yang dibuat SCIM'}</dd><dt>Pengguna terkelola</dt><dd>{c.managed_users}</dd></dl>
          <h3>Token</h3>
          <div className="row"><Field label="Nama token"><input value={name} maxLength={60} onChange={(e) => setName(e.target.value)} placeholder="okta-prod" /></Field>
            <button className="primary" disabled={!name || create.isPending} onClick={() => create.mutate()}>Buat token</button></div>
          <ErrorBox error={create.error} />
          {secret && <div className="okbox" role="status">Salin sekarang, tidak akan ditampilkan lagi:<br /><span className="mono" data-testid="scim-token">{secret}</span> <button className="small" onClick={() => navigator.clipboard?.writeText(secret)}>Salin</button></div>}
          {tokens.data && tokens.data.items.length > 0 && (
            <div className="scroll-x"><table><thead><tr><th>Nama</th><th>Dibuat</th><th>Terakhir dipakai</th><th /></tr></thead><tbody>
              {tokens.data.items.map((t) => <tr key={t.id}><td>{t.name}</td><td className="muted">{fmtTime(t.created_at)}</td><td className="muted">{t.last_used_at ? fmtTime(t.last_used_at) : '—'}</td>
                <td className="right"><button className="small danger" onClick={() => window.confirm(`Cabut token "${t.name}"? IdP yang memakainya berhenti menyinkronkan.`) && revoke.mutate(t.id)}>Cabut</button></td></tr>)}
            </tbody></table></div>)}
        </>
      )}
      <p className="muted">Pengguna SCIM tidak punya password dan masuk lewat SSO. Menonaktifkan di IdP (active=false) mencabut sesinya seketika. Profil lengkap: <span className="mono">contracts/scim.md</span>.</p>
    </div>
  )
}

function SSOSettings() {
  const [probe, setProbe] = useState(false)
  const q = useQuery({ queryKey: ['sso', probe], queryFn: () => get<SSOView>('/settings/sso' + (probe ? '?probe=1' : '')) })
  if (q.isLoading) return <Loading what="pengaturan SSO" />
  if (!q.data) return <ErrorBox error={q.error} />
  const c = q.data
  return (
    <div className="card stack" aria-label="Pengaturan SSO">
      <h2>Single sign-on (OpenID Connect)</h2>
      {!c.enabled ? (
        <p className="muted">SSO belum dikonfigurasi. Atur <span className="mono">OIDC_ISSUER</span>, <span className="mono">OIDC_CLIENT_ID</span> (dan <span className="mono">OIDC_CLIENT_SECRET</span>) di server lalu restart. Daftarkan redirect URI <span className="mono">PUBLIC_BASE_URL/api/v1/auth/oidc/callback</span> di IdP.</p>
      ) : (
        <>
          <dl><dt>Penyedia</dt><dd>{c.name} · <span className="mono">{c.issuer}</span></dd><dt>Client ID</dt><dd className="mono">{c.client_id}</dd>
            <dt>Redirect URI (daftarkan di IdP)</dt><dd className="mono">{c.redirect_url}</dd>
            <dt>Penyediaan otomatis</dt><dd>{c.auto_provision ? `ya (peran dasar: ${c.default_role})` : 'tidak — pengguna harus ditautkan dahulu'}</dd>
            <dt>Tautkan otomatis lewat email</dt><dd>{c.link_by_email ? 'ya (email_verified wajib)' : 'tidak (hanya lewat halaman Akun)'}</dd>
            <dt>Sinkron peran dari grup</dt><dd>{c.sync_roles ? 'ya' : 'tidak'}</dd><dt>SSO memenuhi kewajiban 2FA</dt><dd>{c.trust_idp_mfa ? 'ya (IdP menegakkan MFA)' : 'tidak'}</dd>
            <dt>Domain diizinkan</dt><dd>{c.allowed_domains.length ? c.allowed_domains.join(', ') : 'semua'}</dd><dt>Pengguna tertaut</dt><dd>{c.linked_users}</dd></dl>
          <div className="row"><button className="small" onClick={() => setProbe(true)}>Uji koneksi ke IdP</button>
            {c.reachable === true && <span className="badge ok">IdP terjangkau</span>}
            {c.reachable === false && <span className="err" role="alert">Tidak terjangkau: {c.reach_error}</span>}</div>
        </>
      )}
      <p className="muted">Konfigurasi berasal dari environment server; rahasia klien tidak pernah ditampilkan.</p>
    </div>
  )
}

function EmailSettings() {
  const toast = useToast()
  const q = useQuery({ queryKey: ['email-config'], queryFn: () => get<EmailConfig>('/settings/email') })
  const [to, setTo] = useState('')
  const test = useMutation({ mutationFn: () => post('/settings/email/test', { to }), onSuccess: () => toast('Email uji terkirim — periksa kotak masuk') })
  if (q.isLoading) return <Loading what="pengaturan email" />
  if (!q.data) return <ErrorBox error={q.error} />
  const c = q.data
  return (
    <div className="card stack" aria-label="Pengaturan email">
      <h2>Email (SMTP)</h2>
      {!c.configured ? (
        <p className="muted">SMTP belum dikonfigurasi. Atur <span className="mono">SMTP_HOST</span>, <span className="mono">SMTP_FROM</span> (dan <span className="mono">SMTP_USERNAME</span>/<span className="mono">SMTP_PASSWORD</span>) di server lalu restart. Sampai itu, tautan undangan dibagikan manual.</p>
      ) : (
        <>
          <dl><dt>Server</dt><dd className="mono">{c.host}:{c.port}</dd><dt>Enkripsi</dt><dd>{c.mode === 'none' ? 'TIDAK ADA (hanya untuk relay lokal tepercaya)' : c.mode === 'tls' ? 'TLS implisit' : 'STARTTLS'}</dd>
            <dt>Pengirim</dt><dd>{c.from}</dd><dt>Autentikasi</dt><dd>{c.auth_enabled ? 'ya' : 'tidak'}</dd></dl>
          <Field label="Kirim email uji ke"><input type="email" value={to} onChange={(e) => setTo(e.target.value)} placeholder="anda@contoh.id" /></Field>
          <ErrorBox error={test.error} />
          <div><button className="primary" disabled={!to || test.isPending} onClick={() => test.mutate()}>{test.isPending ? 'Mengirim…' : 'Kirim email uji'}</button></div>
        </>
      )}
      <p className="muted">Pengaturan SMTP berasal dari environment server dan tidak dapat diubah dari sini (kredensial tidak pernah ditampilkan).</p>
    </div>
  )
}

const LEVELS: [SecuritySettings['require_2fa'], string, string][] = [
  ['off', 'Opsional (default)', 'Setiap pengguna boleh mengaktifkan 2FA sendiri.'],
  ['admin', 'Wajib untuk admin', 'Admin tanpa 2FA hanya dapat membuka halaman Akun sampai 2FA aktif.'],
  ['all', 'Wajib untuk semua pengguna', 'Berlaku juga untuk operator dan viewer.'],
]

function Security() {
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({ queryKey: ['security'], queryFn: () => get<SecuritySettings>('/settings/security') })
  const save = useMutation({ mutationFn: (v: string) => put<SecuritySettings>('/settings/security', { require_2fa: v }), onSuccess: () => { toast('Kebijakan keamanan disimpan'); qc.invalidateQueries({ queryKey: ['security'] }) } })
  const savePk = useMutation({ mutationFn: (on: boolean) => put<SecuritySettings>('/settings/security', { passkey_login: on }), onSuccess: () => { toast('Kebijakan keamanan disimpan'); qc.invalidateQueries({ queryKey: ['security'] }); qc.invalidateQueries({ queryKey: ['auth-methods'] }) } })
  if (q.isLoading) return <Loading what="kebijakan keamanan" />
  if (!q.data) return <ErrorBox error={q.error} />
  return (
    <div className="card stack">
      <h2>Kewajiban 2FA</h2>
      <p className="muted">Akun yang belum punya 2FA tetap bisa masuk, tetapi hanya dapat menyiapkan 2FA. API key tidak terpengaruh. Anda sendiri harus sudah memakai 2FA untuk menyalakannya; bila ada yang terkunci keluar: <span className="mono">smscd admin reset-2fa</span>.</p>
      <fieldset className="stack" style={{ border: 0, padding: 0 }}><legend className="sr-only">Kebijakan</legend>
        {LEVELS.map(([v, l, d]) => (
          <label key={v} className="row" style={{ alignItems: 'flex-start' }}>
            <input type="radio" name="require2fa" checked={q.data!.require_2fa === v} disabled={save.isPending} onChange={() => save.mutate(v)} />
            <span><strong>{l}</strong><br /><span className="muted">{d}</span></span></label>))}
      </fieldset>
      <ErrorBox error={save.error} />
      <h2>Login tanpa password (passkey)</h2>
      <p className="muted">Pengguna dapat mendaftarkan passkey (butuh PIN/biometrik) dan masuk tanpa mengetik password maupun kode 2FA — passkey sudah menggabungkan kepemilikan perangkat dan verifikasi pengguna. Passkey tersinkron ke akun cloud (iCloud/Google) memperluas permukaan serangan ke akun tersebut; nyalakan hanya bila kebijakan organisasi mengizinkan. Mematikan menolak login passkey (kunci tersimpan tetap sah sebagai faktor kedua).</p>
      <label className="row"><input type="checkbox" checked={q.data.passkey_login} disabled={savePk.isPending} onChange={(e) => savePk.mutate(e.target.checked)} /> Izinkan login tanpa password dengan passkey</label>
      <ErrorBox error={savePk.error} />
      <div>{q.data.users_missing_2fa > 0 ? <span className="badge warn">{q.data.users_missing_2fa} pengguna dalam cakupan belum memakai 2FA</span> : <span className="badge ok">Semua pengguna dalam cakupan sudah memakai 2FA (atau kebijakan opsional)</span>}</div>
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

const EVENTS: WebhookEvent[] = ['message.sent', 'message.delivered', 'message.failed', 'campaign.completed', 'campaign.paused', 'device.offline']

function Webhooks() {
  const qc = useQueryClient()
  const toast = useToast()
  const [url, setUrl] = useState('')
  const [events, setEvents] = useState<WebhookEvent[]>(['message.delivered', 'message.failed'])
  const [secret, setSecret] = useState('')
  const [open, setOpen] = useState<Webhook | null>(null)
  const q = useQuery({ queryKey: ['webhooks'], queryFn: () => get<{ items: Webhook[] }>('/webhooks') })
  const refresh = () => qc.invalidateQueries({ queryKey: ['webhooks'] })
  const create = useMutation({ mutationFn: () => post<WebhookCreated>('/webhooks', { url, events }), onSuccess: (w) => { setSecret(w.signing_secret); setUrl(''); refresh() } })
  const toggle = useMutation({ mutationFn: (w: Webhook) => patch(`/webhooks/${w.id}`, { enabled: !w.enabled }), onSuccess: refresh })
  const rm = useMutation({ mutationFn: (id: string) => del(`/webhooks/${id}`), onSuccess: () => { setOpen(null); refresh() } })
  const [graceW, setGraceW] = useState(86400)
  const rotateW = useMutation({ mutationFn: (id: string) => post<WebhookCreated>(`/webhooks/${id}/rotate-secret`, { grace_seconds: graceW }), onSuccess: (w) => { setSecret(w.signing_secret); refresh() } })
  const test = useMutation({ mutationFn: (id: string) => post(`/webhooks/${id}/test`, {}), onSuccess: () => { toast('Event uji dikirim'); qc.invalidateQueries({ queryKey: ['deliveries'] }) } })
  return (
    <div className="stack">
      <div className="card stack">
        <Field label="URL penerima" hint="HTTP(S). Jaringan privat/tailnet diizinkan; loopback dan alamat metadata cloud ditolak.">
          <input type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://billing.example.ts.net/hooks/sms" />
        </Field>
        <fieldset className="row" style={{ border: 0, padding: 0 }}><legend className="muted">Event</legend>
          {EVENTS.map((ev) => <label key={ev} className="row"><input type="checkbox" checked={events.includes(ev)} onChange={(e) => setEvents((p) => e.target.checked ? [...p, ev] : p.filter((x) => x !== ev))} />{ev}</label>)}
        </fieldset>
        <ErrorBox error={create.error} /><ErrorBox error={rotateW.error} />
        <label className="row">Masa tumpang tindih saat memutar secret <select aria-label="Masa tumpang tindih secret webhook" value={graceW} onChange={(e) => setGraceW(Number(e.target.value))}><option value={0}>langsung ganti</option><option value={3600}>1 jam</option><option value={86400}>24 jam</option><option value={604800}>7 hari</option></select></label>
        <div><button className="primary" disabled={!url || events.length === 0 || create.isPending} onClick={() => create.mutate()}>Tambah webhook</button></div>
        {secret && <div className="okbox" role="status">Secret penandatangan — salin sekarang, tidak akan ditampilkan lagi:<br /><span className="mono">{secret}</span> <button className="small" onClick={() => navigator.clipboard?.writeText(secret)}>Salin</button></div>}
      </div>
      {q.isLoading && <Loading what="webhook" />}
      <ErrorBox error={q.error} />
      {q.data && q.data.items.length === 0 && <Empty>Belum ada webhook.</Empty>}
      {q.data && q.data.items.length > 0 && (
        <div className="card scroll-x"><table><thead><tr><th>URL</th><th>Event</th><th>Status</th><th /></tr></thead><tbody>
          {q.data.items.map((w) => (
            <tr key={w.id}><td className="mono">{w.url}</td><td>{w.events.join(', ')}</td><td>{w.enabled ? 'aktif' : 'nonaktif'}{w.previous_secret_valid_until && <div className="muted">dua tanda tangan s.d. {fmtTime(w.previous_secret_valid_until)}</div>}</td>
              <td className="right"><span className="row">
                <button className="small" onClick={() => setOpen(w)}>Riwayat</button>
                <button className="small" disabled={test.isPending} onClick={() => test.mutate(w.id)}>Uji</button>
                <button className="small" disabled={rotateW.isPending} onClick={() => window.confirm(`Putar secret penandatangan ${w.url}? Selama masa tumpang tindih, pengiriman membawa dua tanda tangan.`) && rotateW.mutate(w.id)}>Putar secret</button>
                <button className="small" onClick={() => toggle.mutate(w)}>{w.enabled ? 'Nonaktifkan' : 'Aktifkan'}</button>
                <button className="small danger" onClick={() => window.confirm(`Hapus webhook ${w.url}?`) && rm.mutate(w.id)}>Hapus</button></span></td></tr>))}
        </tbody></table></div>)}
      {open && <Deliveries webhook={open} onClose={() => setOpen(null)} />}
    </div>
  )
}

function Deliveries({ webhook, onClose }: { webhook: Webhook; onClose: () => void }) {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ['deliveries', webhook.id], queryFn: () => get<Page<WebhookDelivery>>(`/webhooks/${webhook.id}/deliveries?limit=50`), refetchInterval: 3000 })
  const retry = useMutation({ mutationFn: (id: string) => post(`/webhooks/${webhook.id}/deliveries/${id}/retry`, {}), onSuccess: () => qc.invalidateQueries({ queryKey: ['deliveries', webhook.id] }) })
  return (
    <Modal open onClose={onClose} title={`Riwayat pengiriman — ${webhook.url}`} wide>
      <ErrorBox error={q.error} /><ErrorBox error={retry.error} />
      {q.data && q.data.items.length === 0 && <Empty>Belum ada pengiriman.</Empty>}
      {q.data && q.data.items.length > 0 && (
        <div className="scroll-x"><table><thead><tr><th>Waktu</th><th>Event</th><th>Status</th><th>Percobaan</th><th>Respons</th><th /></tr></thead><tbody>
          {q.data.items.map((d) => (
            <tr key={d.id}><td className="muted">{fmtTime(d.created_at)}</td><td className="mono">{d.event}</td><td>{d.status}</td><td>{d.attempts}</td>
              <td>{d.last_status ?? d.last_error ?? '—'}</td>
              <td className="right">{d.status === 'failed' && <button className="small" disabled={retry.isPending} onClick={() => retry.mutate(d.id)}>Kirim ulang</button>}</td></tr>))}
        </tbody></table></div>)}
    </Modal>
  )
}

function Retention() {
  const qc = useQueryClient()
  const toast = useToast()
  const q = useQuery({ queryKey: ['retention'], queryFn: () => get<RetentionSettings>('/settings/retention') })
  const [body, setBody] = useState('')
  const [ev, setEv] = useState('')
  const [inv, setInv] = useState('')
  const save = useMutation({
    mutationFn: () => put<RetentionSettings>('/settings/retention', { body_retention_days: Number(body !== '' ? body : q.data?.body_retention_days), event_retention_days: Number(ev !== '' ? ev : q.data?.event_retention_days), invitation_retention_days: Number(inv !== '' ? inv : q.data?.invitation_retention_days) }),
    onSuccess: () => { toast('Retensi disimpan'); setBody(''); setEv(''); setInv(''); qc.invalidateQueries({ queryKey: ['retention'] }) },
  })
  if (q.isLoading) return <Loading what="pengaturan retensi" />
  if (!q.data) return <ErrorBox error={q.error} />
  const r = q.data.last_run
  return (
    <div className="stack">
      <div className="card stack">
        <p className="muted">Isi pesan yang sudah selesai dihapus setelah sekian hari (metadata tetap ada). 0 = hapus segera setelah selesai. Pembersihan berjalan otomatis tiap jam.</p>
        <div className="grid cols-4">
          <Field label="Simpan isi pesan (hari)" hint="0–3650"><input type="number" min={0} max={3650} value={body !== '' ? body : q.data.body_retention_days} onChange={(e) => setBody(e.target.value)} /></Field>
          <Field label="Simpan riwayat event (hari)" hint="1–3650"><input type="number" min={1} max={3650} value={ev !== '' ? ev : q.data.event_retention_days} onChange={(e) => setEv(e.target.value)} /></Field>
          <Field label="Simpan undangan selesai (hari)" hint="1–3650; yang masih tertunda tak dihapus"><input type="number" min={1} max={3650} value={inv !== '' ? inv : q.data.invitation_retention_days} onChange={(e) => setInv(e.target.value)} /></Field>
        </div>
        <ErrorBox error={save.error} />
        <div><button className="primary" disabled={save.isPending} onClick={() => save.mutate()}>Simpan</button></div>
      </div>
      <div className="card" aria-label="Pembersihan terakhir">
        <h2>Pembersihan terakhir</h2>
        {!r ? <p className="muted">Belum pernah berjalan.</p> : (
          <dl><dt>Waktu</dt><dd>{fmtTime(r.at)}</dd>
            <dt>Isi pesan dibersihkan</dt><dd>{r.bodies_purged}</dd><dt>Event dihapus</dt><dd>{r.events_deleted}</dd>
            <dt>Penerima campaign dianonimkan</dt><dd>{r.recipients_scrubbed}</dd><dt>Sesi dihapus</dt><dd>{r.sessions_deleted}</dd>
            <dt>Enrollment dihapus</dt><dd>{r.enrollments_deleted}</dd><dt>Riwayat webhook dihapus</dt><dd>{r.deliveries_deleted}</dd><dt>Undangan lama dihapus</dt><dd>{r.invitations_deleted}</dd></dl>)}
      </div>
    </div>
  )
}
