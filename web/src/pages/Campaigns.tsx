import { ChangeEvent, DragEvent, useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { del, get, patch, post, qs, upload } from '../lib/api'
import { fmtTime, pct, rel } from '../lib/format'
import { analyze, variables } from '../lib/sms'
import { canOperate, useMe } from '../lib/session'
import type { Campaign, CampaignPreview, DispatchPolicy, ImportResult, Page, Point, Sim, Template, Device } from '../lib/types'
import { bucketSlots, campaignWindow } from '../lib/chart'
import { SeriesChart } from '../components/SeriesChart'
import { Confirm, Empty, ErrorBox, errMsg, Field, Loading, useToast } from '../components/ui'
import { Transactions } from './Transactions'

const CAMPAIGN_STATUS: Record<string, { label: string; tone: string; icon: string }> = {
  draft: { label: 'Draft', tone: 'muted', icon: '✎' }, scheduled: { label: 'Terjadwal', tone: 'info', icon: '⏰' }, running: { label: 'Berjalan', tone: 'ok', icon: '▶' },
  paused: { label: 'Dijeda', tone: 'warn', icon: '⏸' }, completed: { label: 'Selesai', tone: 'ok', icon: '✓' }, cancelled: { label: 'Dibatalkan', tone: 'muted', icon: '⊘' },
}
const PAUSE_HELP: Record<string, string> = {
  user: 'Anda menjeda campaign ini. Klik Lanjutkan untuk meneruskan pengiriman.',
  auto_failure_ratio: 'Dijeda otomatis karena terlalu banyak percobaan gagal akibat SIM/jaringan. Periksa sinyal, pulsa, dan status SIM, lalu Lanjutkan.',
  no_capacity: 'Dijeda otomatis karena tidak ada SIM online yang dapat mengirim. Hubungkan kembali HP, lalu Lanjutkan.',
}

export function CampaignBadge({ status }: { status: string }) {
  const s = CAMPAIGN_STATUS[status] ?? { label: status, tone: 'muted', icon: '•' }
  return <span className={`badge ${s.tone}`}><span aria-hidden="true">{s.icon}</span>{s.label}</span>
}

export function Progress({ c }: { c: Campaign }) {
  const k = c.counters
  const total = Math.max(1, k.total)
  const seg = (n: number) => ({ width: `${(n / total) * 100}%` })
  const done = k.delivered + k.sent + k.failed + k.cancelled + k.expired + k.unknown
  return (
    <div>
      <div className="pbar" role="progressbar" aria-valuemin={0} aria-valuemax={k.total} aria-valuenow={done} aria-label={`Progres ${done} dari ${k.total}`}>
        <i className="delivered" style={seg(k.delivered)} /><i className="sent" style={seg(k.sent)} /><i className="inflight" style={seg(k.in_flight)} />
        <i className="failed" style={seg(k.failed)} /><i className="other" style={seg(k.cancelled + k.expired + k.unknown)} />
      </div>
      <div className="legend muted" style={{ marginTop: 6 }}>
        <span>{done}/{k.total} selesai ({pct(done / total)})</span><span>sampai {k.delivered}</span><span>terkirim {k.sent}</span><span>proses {k.in_flight}</span>
        <span>antre {k.queued}</span><span>gagal {k.failed}{k.partial > 0 && ` (parsial ${k.partial})`}</span>{k.unknown > 0 && <span>tidak pasti {k.unknown}</span>}{k.expired > 0 && <span>kedaluwarsa {k.expired}</span>}{k.cancelled > 0 && <span>batal {k.cancelled}</span>}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------- daftar
export function Campaigns() {
  const me = useMe()
  const q = useQuery({ queryKey: ['campaigns'], queryFn: () => get<Page<Campaign>>('/campaigns?limit=100') })
  return (
    <div className="stack gap-lg">
      <div className="row spread"><h1>Campaigns</h1>{canOperate(me) && <Link className="btn primary" to="/campaigns/new" style={{ textDecoration: 'none' }}>+ Buat campaign</Link>}</div>
      {q.isLoading && <Loading />}<ErrorBox error={q.error} />
      {q.data && q.data.items.length === 0 && <Empty>Belum ada campaign. Buat campaign untuk mengirim pesan ke banyak penerima dengan variabel per penerima.</Empty>}
      <div className="stack">{q.data?.items.map((c) => (
        <div key={c.id} className="card stack">
          <div className="row spread">
            <div><Link to={c.status === 'draft' ? `/campaigns/${c.id}/edit` : `/campaigns/${c.id}`}><strong>{c.name}</strong></Link> <CampaignBadge status={c.status} />
              {c.pause_reason && <span className="hint"> · {c.pause_reason === 'user' ? 'dijeda pengguna' : c.pause_reason === 'no_capacity' ? 'tanpa SIM online' : 'auto-pause'}</span>}</div>
            <span className="muted">{c.status === 'scheduled' && c.schedule?.start_at ? `mulai ${fmtTime(c.schedule.start_at)}` : `dibuat ${rel(c.created_at)}`}</span>
          </div>
          {c.recipients.count > 0 ? <Progress c={c} /> : <span className="muted">Belum ada penerima</span>}
        </div>))}</div>
    </div>
  )
}

// ---------------------------------------------------------------- detail
export function CampaignDetail() {
  const { id } = useParams()
  const me = useMe()
  const qc = useQueryClient()
  const toast = useToast()
  const nav = useNavigate()
  const [tab, setTab] = useState<'pesan' | 'pengaturan'>('pesan')
  const [confirmCancel, setConfirmCancel] = useState(false)
  const q = useQuery({ queryKey: ['campaign', id], queryFn: () => get<Campaign>(`/campaigns/${id}`), refetchInterval: 5000 })
  const act = useMutation({
    mutationFn: (a: string) => post<Campaign>(`/campaigns/${id}/${a}`),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['campaign'] }); qc.invalidateQueries({ queryKey: ['campaigns'] }) }, onError: (e) => toast(errMsg(e), true),
  })
  if (q.isLoading) return <Loading />
  if (q.error) return <ErrorBox error={q.error} />
  const c = q.data!
  if (c.status === 'draft') { nav(`/campaigns/${c.id}/edit`, { replace: true }); return null }
  const op = canOperate(me)
  const live = ['running', 'paused', 'scheduled'].includes(c.status)
  return (
    <div className="stack gap-lg">
      <div className="row spread">
        <div><h1>{c.name}</h1><div className="muted"><CampaignBadge status={c.status} /> · {c.recipients.count} penerima · prioritas {c.priority}{c.started_at ? ` · mulai ${fmtTime(c.started_at)}` : ''}{c.finished_at ? ` · selesai ${fmtTime(c.finished_at)}` : ''}</div></div>
        {op && live && (
          <div className="row">
            {c.status === 'running' && <button onClick={() => act.mutate('pause')} disabled={act.isPending}>⏸ Jeda</button>}
            {c.status === 'paused' && <button className="primary" onClick={() => act.mutate('resume')} disabled={act.isPending}>▶ Lanjutkan</button>}
            <button className="danger" onClick={() => setConfirmCancel(true)}>Batalkan</button>
          </div>)}
      </div>
      {c.status === 'paused' && c.pause_reason && <div className="warnbox" role="alert"><strong>Campaign dijeda.</strong> {PAUSE_HELP[c.pause_reason] ?? ''}</div>}
      {c.status === 'scheduled' && <div className="okbox">Terjadwal mulai {fmtTime(c.schedule?.start_at)}. Pesan akan dibuat otomatis saat waktunya tiba.</div>}
      <div className="card stack">
        <Progress c={c} />
        {c.status === 'running' && c.estimated_finish_at && <div className="muted">Perkiraan selesai ± {fmtTime(c.estimated_finish_at)} (kasar; belum memperhitungkan jendela waktu kirim)</div>}
      </div>
      {c.counters.total > 0 && <CampaignSeries c={c} live={live} />}
      <div className="tabs" role="tablist">
        <button role="tab" aria-selected={tab === 'pesan'} onClick={() => setTab('pesan')}>Pesan</button>
        <button role="tab" aria-selected={tab === 'pengaturan'} onClick={() => setTab('pengaturan')}>Pengaturan</button>
      </div>
      {tab === 'pesan' ? <Transactions campaignId={c.id} /> : <LiveSettings c={c} editable={op && live} />}
      <Confirm open={confirmCancel} onClose={() => setConfirmCancel(false)} title="Batalkan campaign?" confirmWord="BATAL"
        text={`Pesan yang masih antre (${c.counters.queued}) akan dibatalkan. Pesan yang sedang dikirim oleh HP diselesaikan. Tindakan ini tidak dapat dibatalkan.`}
        onConfirm={() => { setConfirmCancel(false); act.mutate('cancel') }} />
    </div>
  )
}

function LiveSettings({ c, editable }: { c: Campaign; editable: boolean }) {
  const qc = useQueryClient()
  const toast = useToast()
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => get<{ items: DispatchPolicy[] }>('/dispatch-policies') })
  const sims = useQuery({ queryKey: ['sims'], queryFn: () => get<{ items: Sim[] }>('/sims') })
  const devices = useQuery({ queryKey: ['devices'], queryFn: () => get<Page<Device>>('/devices') })
  const [policyId, setPolicyId] = useState(c.policy_id ?? '')
  const [simIds, setSimIds] = useState<string[]>(c.sim_ids ?? [])
  const save = useMutation({
    mutationFn: () => patch(`/campaigns/${c.id}`, { policy_id: policyId || null, sim_ids: simIds }),
    onSuccess: () => { toast('Pengaturan disimpan; berlaku untuk pesan yang belum dikirim'); qc.invalidateQueries({ queryKey: ['campaign'] }) }, onError: (e) => toast(errMsg(e), true),
  })
  const dev = (id: string) => devices.data?.items.find((d) => d.id === id)?.name ?? ''
  return (
    <div className="card stack">
      <p className="muted">Saat campaign berjalan hanya policy dan pilihan SIM yang dapat diubah. Perubahan berlaku untuk pesan yang belum dikirim.</p>
      <Field label="Policy pengiriman"><select value={policyId} disabled={!editable} onChange={(e) => setPolicyId(e.target.value)}><option value="">Default</option>{policies.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
      <Field label="SIM yang dipakai (kosong = semua SIM aktif)">
        <div className="stack">{sims.data?.items.filter((s) => s.status === 'active').map((s) => (
          <label key={s.id} className="row"><input type="checkbox" disabled={!editable} checked={simIds.includes(s.id)} onChange={(e) => setSimIds((p) => e.target.checked ? [...p, s.id] : p.filter((x) => x !== s.id))} />{dev(s.device_id)} · SIM {s.slot_index + 1} {s.label || s.carrier_name || ''}</label>))}</div>
      </Field>
      <div className="muted">Auto-pause: {c.auto_pause.enabled ? `aktif — jeda bila > ${Math.round(c.auto_pause.max_failure_ratio * 100)}% dari ${c.auto_pause.window} percobaan terakhir gagal karena SIM/jaringan` : 'nonaktif'}</div>
      {editable && <div><button className="primary" onClick={() => save.mutate()} disabled={save.isPending}>Simpan</button></div>}
    </div>
  )
}

// ---------------------------------------------------------------- wizard
const STEPS = ['Detail', 'Penerima', 'Pesan', 'SIM & Policy', 'Review']

export function CampaignWizard() {
  const { id: routeId } = useParams()
  const [sp, setSp] = useSearchParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()
  const me = useMe()
  const [id, setId] = useState<string | undefined>(routeId)
  const step = Math.min(5, Math.max(1, Number(sp.get('step') || '1')))
  const goto = (n: number) => setSp({ step: String(n) }, { replace: true })
  const q = useQuery({ queryKey: ['campaign', id], queryFn: () => get<Campaign>(`/campaigns/${id}`), enabled: !!id })
  const c = q.data

  // form lokal
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [mode, setMode] = useState<'body' | 'template'>('body')
  const [body, setBody] = useState('')
  const [tplId, setTplId] = useState('')
  const [simIds, setSimIds] = useState<string[]>([])
  const [policyId, setPolicyId] = useState('')
  const [priority, setPriority] = useState(3)
  const [ap, setAp] = useState({ enabled: true, window: 50, max_failure_ratio: 0.5 })
  const [when, setWhen] = useState('')
  const [hydrated, setHydrated] = useState(false)
  const [varErr, setVarErr] = useState<string | null>(null)
  useEffect(() => {
    if (c && !hydrated) {
      setName(c.name); setDesc(c.description ?? ''); setBody(c.body ?? ''); setTplId(c.template_id ?? ''); setMode(c.template_id ? 'template' : 'body')
      setSimIds(c.sim_ids); setPolicyId(c.policy_id ?? ''); setPriority(c.priority); setAp({ ...c.auto_pause })
      setWhen(c.schedule?.start_at ? toLocalInput(c.schedule.start_at) : ''); setHydrated(true)
    }
  }, [c, hydrated])

  const templates = useQuery({ queryKey: ['templates'], queryFn: () => get<{ items: Template[] }>('/templates') })
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => get<{ items: DispatchPolicy[] }>('/dispatch-policies') })
  const sims = useQuery({ queryKey: ['sims'], queryFn: () => get<{ items: Sim[] }>('/sims') })
  const devices = useQuery({ queryKey: ['devices'], queryFn: () => get<Page<Device>>('/devices') })

  const refresh = () => { qc.invalidateQueries({ queryKey: ['campaign', id] }); qc.invalidateQueries({ queryKey: ['campaigns'] }) }
  const save = useMutation({
    mutationFn: async (fields: Record<string, unknown>) => {
      if (!id) { const created = await post<Campaign>('/campaigns', { name, description: desc || null, body: '{{name}}'.slice(0, 0) || 'Halo', ...fields }); setId(created.id); return created }
      return patch<Campaign>(`/campaigns/${id}`, fields)
    },
    onSuccess: (r) => { qc.setQueryData(['campaign', r.id], r); refresh() },
  })

  if (!canOperate(me)) return <p className="err">Peran Anda tidak dapat membuat campaign.</p>
  if (id && q.isLoading) return <Loading />
  if (c && c.status !== 'draft') { nav(`/campaigns/${c.id}`, { replace: true }); return null }

  const text = mode === 'template' ? (templates.data?.items.find((t) => t.id === tplId)?.body ?? '') : body
  const vars = variables(text)
  const columns = c?.recipients.columns ?? []
  const missingCols = vars.filter((v) => !columns.includes(v))
  const info = analyze(text)
  const next = async (fields?: Record<string, unknown>) => {
    try { if (fields) await save.mutateAsync(fields); goto(step + 1) } catch (e) { toast(errMsg(e), true) }
  }
  const bodyFields = () => (mode === 'template' ? { template_id: tplId, body: null } : { body, template_id: null })
  // simpan pesan, lalu periksa variabel terhadap SELURUH penerima (impor terjadi sebelum isi pesan diketahui)
  const nextFromMessage = async () => {
    try {
      await save.mutateAsync(bodyFields())
      const pv = await post<CampaignPreview>(`/campaigns/${id}/preview`, { sample: 1 })
      if (pv.missing_variables.length > 0) {
        setVarErr(pv.missing_variables.map((m) => `${m.recipients} penerima tidak memiliki {{${m.name}}}`).join('; '))
        return
      }
      setVarErr(null); goto(4)
    } catch (e) { toast(errMsg(e), true) }
  }
  const stepDone = (n: number) => n < step

  return (
    <div className="stack gap-lg">
      <div className="row spread"><h1>{c ? `Campaign: ${c.name}` : 'Campaign baru'}</h1>{id && <Link to="/campaigns">Simpan & keluar</Link>}</div>
      <ol className="steps" aria-label="Langkah wizard">{STEPS.map((s, i) => <li key={s} className={stepDone(i + 1) ? 'done' : ''} aria-current={step === i + 1 ? 'step' : undefined}>{s}</li>)}</ol>

      {step === 1 && (
        <div className="card stack">
          <Field label="Nama campaign"><input value={name} onChange={(e) => setName(e.target.value)} maxLength={120} autoFocus /></Field>
          <Field label="Deskripsi (opsional)"><input value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
          <ErrorBox error={save.error} />
          <div className="row" style={{ justifyContent: 'flex-end' }}><button className="primary" disabled={!name.trim() || save.isPending} onClick={() => next({ name, description: desc || null })}>Lanjut: Penerima →</button></div>
        </div>)}

      {step === 2 && id && <Recipients id={id} c={c} onChange={refresh} onBack={() => goto(1)} onNext={() => goto(3)} />}

      {step === 3 && (
        <div className="grid cols-2">
          <div className="card stack">
            <div className="row" role="radiogroup" aria-label="Sumber pesan">
              <label className="row"><input type="radio" name="mode" checked={mode === 'body'} onChange={() => setMode('body')} /> Tulis pesan</label>
              <label className="row"><input type="radio" name="mode" checked={mode === 'template'} onChange={() => setMode('template')} /> Pakai template</label>
            </div>
            {mode === 'body'
              ? <Field label="Isi pesan" hint={`Gunakan {{variabel}} sesuai kolom CSV. ${info.encoding === 'gsm7' ? 'GSM-7' : 'Unicode'} · ${info.units} unit · ${info.parts} segmen`}><textarea value={body} onChange={(e) => setBody(e.target.value)} maxLength={1600} aria-label="Isi pesan" /></Field>
              : <Field label="Template"><select value={tplId} onChange={(e) => setTplId(e.target.value)}><option value="">— pilih —</option>{templates.data?.items.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select></Field>}
            {mode === 'template' && text && <div className="card" style={{ whiteSpace: 'pre-wrap' }}>{text}</div>}
          </div>
          <div className="card stack">
            <h2>Variabel</h2>
            <div>Kolom penerima tersedia: {columns.length ? columns.map((x) => <span key={x} className="badge muted" style={{ marginRight: 4 }}>{x}</span>) : <span className="muted">tidak ada (hanya nomor)</span>}</div>
            <div>Dipakai pesan: {vars.length ? vars.map((x) => <span key={x} className={`badge ${columns.includes(x) ? 'ok' : 'bad'}`} style={{ marginRight: 4 }}>{`{{${x}}}`}</span>) : <span className="muted">tidak ada</span>}</div>
            {missingCols.length > 0 && <div className="err" role="alert">Kolom <strong>{missingCols.join(', ')}</strong> tidak ada pada data penerima. Kembali ke langkah Penerima dan impor ulang CSV dengan kolom tersebut.</div>}
            {varErr && <div className="err" role="alert"><strong>Data penerima belum lengkap:</strong> {varErr}. Hapus semua penerima di langkah Penerima, lalu impor ulang CSV yang sudah dilengkapi.<div style={{ marginTop: 6 }}><button onClick={() => { setVarErr(null); goto(2) }}>← Ke langkah Penerima</button></div></div>}
          </div>
          <div className="row" style={{ gridColumn: '1 / -1', justifyContent: 'space-between' }}>
            <button onClick={() => goto(2)}>← Penerima</button>
            <button className="primary" disabled={!text.trim() || missingCols.length > 0 || save.isPending} onClick={nextFromMessage}>Lanjut: SIM & Policy →</button>
          </div>
        </div>)}

      {step === 4 && (
        <div className="grid cols-2">
          <div className="card stack">
            <h2>Rute</h2>
            <Field label="SIM yang dipakai (kosong = semua SIM aktif)">
              <div className="stack">{sims.data?.items.filter((s) => s.status === 'active').map((s) => (
                <label key={s.id} className="row"><input type="checkbox" checked={simIds.includes(s.id)} onChange={(e) => setSimIds((p) => e.target.checked ? [...p, s.id] : p.filter((x) => x !== s.id))} />{devices.data?.items.find((d) => d.id === s.device_id)?.name} · SIM {s.slot_index + 1} {s.label || s.carrier_name || ''}</label>))}
                {sims.data?.items.length === 0 && <span className="muted">Belum ada SIM terdaftar.</span>}</div>
            </Field>
            <Field label="Policy pengiriman" hint="Menentukan jeda antar SMS, batas per menit/jam/hari, jendela waktu, retry. Tidak bisa melonggarkan batas aman SIM/device.">
              <select value={policyId} onChange={(e) => setPolicyId(e.target.value)}><option value="">Default</option>{policies.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}{p.is_default ? ' (default)' : ''}</option>)}</select>
            </Field>
            <Field label={`Prioritas: ${priority}`} hint="Campaign default 3; kirim cepat 5 sehingga tidak tertahan campaign besar."><input type="range" min={0} max={9} value={priority} onChange={(e) => setPriority(+e.target.value)} /></Field>
          </div>
          <div className="card stack">
            <h2>Auto-pause</h2>
            <label className="row"><input type="checkbox" checked={ap.enabled} onChange={(e) => setAp({ ...ap, enabled: e.target.checked })} /> Jeda otomatis bila banyak kegagalan SIM/jaringan</label>
            <div className="row">
              <Field label="Dievaluasi dari N percobaan terakhir"><input type="number" min={1} max={1000} value={ap.window} disabled={!ap.enabled} onChange={(e) => setAp({ ...ap, window: Math.max(1, +e.target.value) })} /></Field>
              <Field label="Ambang gagal (%)"><input type="number" min={0} max={100} value={Math.round(ap.max_failure_ratio * 100)} disabled={!ap.enabled} onChange={(e) => setAp({ ...ap, max_failure_ratio: Math.min(100, Math.max(0, +e.target.value)) / 100 })} /></Field>
            </div>
            <p className="hint">Hanya kegagalan akibat sinyal/radio/HP yang dihitung; nomor tujuan yang salah tidak memicu jeda.</p>
          </div>
          <div className="row" style={{ gridColumn: '1 / -1', justifyContent: 'space-between' }}>
            <button onClick={() => goto(3)}>← Pesan</button>
            <button className="primary" disabled={save.isPending} onClick={() => next({ sim_ids: simIds, policy_id: policyId || null, priority, auto_pause: ap })}>Lanjut: Review →</button>
          </div>
        </div>)}

      {step === 5 && id && <Review id={id} when={when} setWhen={setWhen} onBack={() => goto(4)} saveSchedule={(w) => save.mutateAsync({ schedule: { start_at: w ? new Date(w).toISOString() : null } })} onDone={(cid) => { refresh(); nav(`/campaigns/${cid}`) }} />}
    </div>
  )
}

function toLocalInput(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

function Recipients({ id, c, onChange, onBack, onNext }: { id: string; c?: Campaign; onChange: () => void; onBack: () => void; onNext: () => void }) {
  const toast = useToast()
  const [result, setResult] = useState<ImportResult | null>(null)
  const [paste, setPaste] = useState('')
  const [over, setOver] = useState(false)
  const imp = useMutation({
    mutationFn: (file: File) => { const f = new FormData(); f.append('file', file); return upload<ImportResult>(`/campaigns/${id}/recipients`, f) },
    onSuccess: (r) => { setResult(r); onChange(); toast(`${r.accepted} penerima diimpor`) }, onError: (e) => toast(errMsg(e), true),
  })
  const clear = useMutation({ mutationFn: () => del<{ removed: number }>(`/campaigns/${id}/recipients`), onSuccess: (r) => { setResult(null); onChange(); toast(`${r.removed} penerima dihapus`) }, onError: (e) => toast(errMsg(e), true) })
  const onFile = (e: ChangeEvent<HTMLInputElement>) => { const f = e.target.files?.[0]; if (f) imp.mutate(f); e.target.value = '' }
  const onDrop = (e: DragEvent) => { e.preventDefault(); setOver(false); const f = e.dataTransfer.files?.[0]; if (f) imp.mutate(f) }
  const sample = 'phone,name,amount,due\n08123456789,Budi,150000,5 Nov\n08129876543,Sari,90000,6 Nov\n'
  const count = c?.recipients.count ?? 0
  const invalidCsv = useMemo(() => result?.invalid.map((i) => `${i.row},${i.value},${i.reason}`).join('\n') ?? '', [result])
  return (
    <div className="stack">
      <div className="card stack">
        <div className={`dropzone ${over ? 'over' : ''}`} onDragOver={(e) => { e.preventDefault(); setOver(true) }} onDragLeave={() => setOver(false)} onDrop={onDrop}>
          <p>Seret berkas CSV ke sini, atau</p>
          <label className="btn" style={{ display: 'inline-block', cursor: 'pointer' }}>Pilih berkas CSV<input type="file" accept=".csv,text/csv" onChange={onFile} style={{ position: 'absolute', opacity: 0, width: 1, height: 1 }} aria-label="Pilih berkas CSV penerima" /></label>
          <p className="hint">Kolom wajib <span className="mono">phone</span>; kolom lain menjadi variabel pesan. UTF-8, pemisah , atau ;. Maks. 50.000 penerima.
            {' '}<a href={`data:text/csv;charset=utf-8,${encodeURIComponent(sample)}`} download="contoh-penerima.csv">Unduh contoh CSV</a></p>
        </div>
        <details><summary>Atau tempel isi CSV</summary>
          <Field label="Isi CSV (baris pertama = header)"><textarea value={paste} onChange={(e) => setPaste(e.target.value)} placeholder={sample} /></Field>
          <button disabled={!paste.trim() || imp.isPending} onClick={() => { imp.mutate(new File([paste], 'tempel.csv', { type: 'text/csv' })); setPaste('') }}>Impor teks</button></details>
        <ErrorBox error={imp.error} />
      </div>
      {result && (
        <div className="card stack" role="status">
          <h2>Hasil impor terakhir</h2>
          <div className="row"><span className="badge ok">{result.accepted} diterima</span><span className="badge muted">{result.duplicates} duplikat</span><span className="badge muted">{result.suppressed} di daftar blokir</span><span className={`badge ${result.invalid.length ? 'bad' : 'muted'}`}>{result.invalid.length} bermasalah</span></div>
          {result.invalid.length > 0 && (<>
            <table><thead><tr><th>Baris</th><th>Nilai</th><th>Masalah</th></tr></thead><tbody>{result.invalid.slice(0, 50).map((i, k) => <tr key={k}><td>{i.row}</td><td className="mono">{i.value}</td><td>{i.reason.startsWith('missing_variable:') ? `variabel {{${i.reason.split(':')[1]}}} kosong` : 'nomor tidak valid'}</td></tr>)}</tbody></table>
            {result.invalid.length > 50 && <p className="hint">Menampilkan 50 dari {result.invalid.length}.</p>}
            <a className="btn" href={`data:text/csv;charset=utf-8,${encodeURIComponent('row,value,reason\n' + invalidCsv)}`} download="baris-bermasalah.csv">Unduh daftar bermasalah</a></>)}
        </div>)}
      <div className="card row spread"><span><strong>{count}</strong> penerima tersimpan{c && c.recipients.columns.length > 0 && <> · variabel: {c.recipients.columns.join(', ')}</>}</span>
        {count > 0 && <button className="small danger" disabled={clear.isPending} onClick={() => window.confirm(`Hapus semua ${count} penerima? (untuk impor ulang)`) && clear.mutate()}>Hapus semua penerima</button>}</div>
      <div className="row" style={{ justifyContent: 'space-between' }}><button onClick={onBack}>← Detail</button><button className="primary" disabled={count === 0} onClick={onNext}>Lanjut: Pesan →</button></div>
    </div>
  )
}

function Review({ id, when, setWhen, onBack, saveSchedule, onDone }: { id: string; when: string; setWhen: (w: string) => void; onBack: () => void; saveSchedule: (w: string) => Promise<unknown>; onDone: (id: string) => void }) {
  const toast = useToast()
  const prev = useQuery({ queryKey: ['preview', id], queryFn: () => post<CampaignPreview>(`/campaigns/${id}/preview`, { sample: 3 }), staleTime: 0 })
  const camp = useQuery({ queryKey: ['campaign', id], queryFn: () => get<Campaign>(`/campaigns/${id}`) })
  const go = useMutation({
    mutationFn: async () => { await saveSchedule(when); return post<Campaign>(`/campaigns/${id}/start`) },
    onSuccess: (c) => { toast(c.status === 'scheduled' ? 'Campaign dijadwalkan' : 'Campaign dimulai'); onDone(c.id) },
  })
  const p = prev.data
  const c = camp.data
  const dur = p?.estimated_duration_seconds
  const durText = dur == null ? 'tidak dapat diperkirakan (tidak ada SIM online)' : dur < 90 ? `± ${dur} detik` : dur < 5400 ? `± ${Math.round(dur / 60)} menit` : `± ${(dur / 3600).toFixed(1)} jam`
  const blocked = !p || p.recipients === 0 || p.missing_variables.length > 0
  const future = when && new Date(when).getTime() > Date.now() + 1000
  return (
    <div className="stack">
      <div className="grid cols-2">
        <div className="card stack">
          <h2>Ringkasan</h2>
          {c && <dl style={{ margin: 0, display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 14px' }}>
            <dt className="muted">Nama</dt><dd style={{ margin: 0 }}>{c.name}</dd><dt className="muted">Penerima</dt><dd style={{ margin: 0 }}>{c.recipients.count}</dd>
            <dt className="muted">Prioritas</dt><dd style={{ margin: 0 }}>{c.priority}</dd><dt className="muted">SIM</dt><dd style={{ margin: 0 }}>{c.sim_ids.length ? `${c.sim_ids.length} dipilih` : 'semua SIM aktif'}</dd>
            <dt className="muted">Auto-pause</dt><dd style={{ margin: 0 }}>{c.auto_pause.enabled ? `ya (> ${Math.round(c.auto_pause.max_failure_ratio * 100)}% dari ${c.auto_pause.window})` : 'tidak'}</dd></dl>}
          {p && <><div>Segmen total ≈ <strong>{p.total_parts}</strong> · SIM online: <strong>{p.online_sims}</strong></div><div>Perkiraan durasi: <strong>{durText}</strong><div className="hint">Kasar; belum memperhitungkan jendela waktu kirim dan retry.</div></div></>}
          {p && p.missing_variables.length > 0 && <div className="err" role="alert">{p.missing_variables.map((m) => `${m.recipients} penerima tidak memiliki {{${m.name}}}`).join('; ')}. Perbaiki di langkah Penerima/Pesan.</div>}
        </div>
        <div className="card stack">
          <h2>Pratinjau pesan</h2>
          {prev.isLoading && <Loading what="pratinjau" />}<ErrorBox error={prev.error} />
          {p?.samples.map((s, i) => <div key={i} className="card"><div className="mono muted">{s.to} · {s.encoding === 'gsm7' ? 'GSM-7' : 'Unicode'} · {s.parts} segmen</div><div style={{ whiteSpace: 'pre-wrap' }}>{s.body}</div></div>)}
        </div>
      </div>
      <div className="card stack">
        <Field label="Jadwal (opsional)" hint="Kosongkan untuk mulai sekarang."><input type="datetime-local" value={when} onChange={(e) => setWhen(e.target.value)} /></Field>
        <ErrorBox error={go.error} />
        <div className="row" style={{ justifyContent: 'space-between' }}>
          <button onClick={onBack}>← SIM & Policy</button>
          <button className="primary" disabled={blocked || go.isPending} onClick={() => go.mutate()}>{go.isPending ? 'Memproses…' : future ? 'Jadwalkan' : 'Mulai sekarang'}</button>
        </div>
      </div>
    </div>
  )
}


/** Deret waktu pengiriman campaign: dari mulai (atau dibuat) sampai selesai (atau sekarang). */
function CampaignSeries({ c, live }: { c: Campaign; live: boolean }) {
  const start = new Date(c.started_at ?? c.created_at).getTime()
  const end = c.finished_at ? new Date(c.finished_at).getTime() : Date.now()
  const w = campaignWindow(start, end)
  const q = useQuery({
    queryKey: ['campaign-series', c.id, w.bucket, w.count, c.finished_at ?? 'live', c.counters.sent + c.counters.delivered + c.counters.failed],
    queryFn: () => get<{ points: Point[] }>('/stats/timeseries' + qs({ from: new Date(w.fromMs).toISOString(), to: new Date(w.toMs).toISOString(), bucket: w.bucket, campaign_id: c.id })),
    refetchInterval: live ? 15000 : false,
  })
  const slots = bucketSlots(q.data?.points ?? [], w.bucket, w.toMs - 1, w.count)
  const unit = { minute: 'menit', hour: 'jam', day: 'hari' }[w.bucket]
  return (
    <div className="card">
      <h2>Pengiriman campaign</h2>
      {q.isLoading ? <Loading /> : <SeriesChart slots={slots} bucket={w.bucket} title={`Grafik pengiriman campaign per ${unit}`} emptyText="Belum ada pesan terkirim atau gagal." leftLabel={w.truncated ? `${w.count} ${unit} terakhir` : fmtTime(slots[0].t)} rightLabel={c.finished_at ? fmtTime(c.finished_at) : 'sekarang'} />}
      <ErrorBox error={q.error} />
    </div>
  )
}
