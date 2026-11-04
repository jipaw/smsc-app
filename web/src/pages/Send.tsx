import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { get, post } from '../lib/api'
import { analyze, parseRecipients } from '../lib/sms'
import type { DispatchPolicy, Sim, Template, SendResult, Device, Quota } from '../lib/types'
import { ErrorBox, Field, useToast } from '../components/ui'
import { canOperate, useMe } from '../lib/session'

export function Send() {
  const me = useMe()
  const toast = useToast()
  const [to, setTo] = useState('')
  const [body, setBody] = useState('')
  const [tplId, setTplId] = useState('')
  const [policyId, setPolicyId] = useState('')
  const [simIds, setSimIds] = useState<string[]>([])
  const [when, setWhen] = useState('')
  const [priority, setPriority] = useState(5)
  const [result, setResult] = useState<SendResult | null>(null)
  const templates = useQuery({ queryKey: ['templates'], queryFn: () => get<{ items: Template[] }>('/templates') })
  const policies = useQuery({ queryKey: ['policies'], queryFn: () => get<{ items: DispatchPolicy[] }>('/dispatch-policies') })
  const sims = useQuery({ queryKey: ['sims'], queryFn: () => get<{ items: Sim[] }>('/sims') })
  const devices = useQuery({ queryKey: ['devices'], queryFn: () => get<{ items: Device[] }>('/devices') })
  const quota = useQuery({ queryKey: ['quota'], queryFn: () => get<Quota>('/quota'), enabled: canOperate(me) })
  const qc = useQueryClient()

  const list = useMemo(() => parseRecipients(to), [to])
  const tpl = templates.data?.items.find((t) => t.id === tplId)
  const text = tpl ? tpl.body : body
  const info = useMemo(() => analyze(text), [text])
  const needsVars = (tpl?.variables.length ?? 0) > 0 || /\{\{\s*\w+\s*\}\}/.test(text)

  const send = useMutation({
    mutationFn: () => post<SendResult>('/messages', {
      recipients: list.map((n) => ({ to: n })),
      ...(tpl ? { template_id: tpl.id } : { body }),
      ...(policyId ? { policy_id: policyId } : {}), ...(simIds.length ? { sim_ids: simIds } : {}),
      ...(when ? { not_before: new Date(when).toISOString() } : {}), priority,
    }),
    onSuccess: (r) => { setResult(r); toast(`${r.messages.length} pesan diantrekan`) },
    onSettled: () => qc.invalidateQueries({ queryKey: ['quota'] }),
  })
  if (!canOperate(me)) return <p className="err">Peran Anda tidak dapat mengirim SMS.</p>
  const devName = (id: string) => devices.data?.items.find((d) => d.id === id)?.name ?? ''
  const valid = list.length > 0 && list.length <= 1000 && text.trim().length > 0 && !needsVars

  return (
    <div className="stack gap-lg">
      <h1>Kirim SMS</h1>
      <div className="grid cols-2">
        <div className="card stack">
          <Field label={`Penerima (${list.length})`} hint="Satu nomor per baris atau pisahkan dengan koma. Maks. 1.000. Format 08xx otomatis menjadi +62.">
            <textarea value={to} onChange={(e) => setTo(e.target.value)} placeholder={'08123456789\n08129998877'} aria-label="Penerima" />
          </Field>
          <Field label="Template (opsional)">
            <select value={tplId} onChange={(e) => setTplId(e.target.value)}><option value="">— tulis manual —</option>{templates.data?.items.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select>
          </Field>
          <Field label="Pesan" hint={`${info.encoding === 'gsm7' ? 'GSM-7' : 'Unicode (UCS-2)'} · ${info.units} ${info.encoding === 'gsm7' ? 'septet' : 'karakter'} · ${info.parts} segmen${info.encoding === 'ucs2' ? ' — ada karakter khusus/emoji, batas 70 per segmen' : ''}`}>
            <textarea value={tpl ? tpl.body : body} readOnly={!!tpl} onChange={(e) => setBody(e.target.value)} maxLength={1600} aria-label="Isi pesan" />
          </Field>
          {needsVars && <div className="err">Pesan memuat variabel {'{{…}}'}. Pengiriman cepat belum mengisi variabel per penerima; gunakan teks tanpa variabel (campaign mendukungnya di fase 2).</div>}
        </div>
        <div className="card stack">
          <h2>Rute & waktu</h2>
          <Field label="SIM (kosong = semua SIM aktif)">
            <div className="stack">{sims.data?.items.filter((s) => s.status === 'active').map((s) => (
              <label key={s.id} className="row"><input type="checkbox" checked={simIds.includes(s.id)} onChange={(e) => setSimIds((p) => e.target.checked ? [...p, s.id] : p.filter((x) => x !== s.id))} />
                {devName(s.device_id)} · SIM {s.slot_index + 1} {s.label || s.carrier_name || ''}</label>))}
              {sims.data?.items.length === 0 && <span className="muted">Belum ada SIM terdaftar.</span>}</div>
          </Field>
          <Field label="Policy pengiriman"><select value={policyId} onChange={(e) => setPolicyId(e.target.value)}><option value="">Default</option>{policies.data?.items.map((p) => <option key={p.id} value={p.id}>{p.name}{p.is_default ? ' (default)' : ''}</option>)}</select></Field>
          <Field label="Jadwal (opsional)"><input type="datetime-local" value={when} onChange={(e) => setWhen(e.target.value)} /></Field>
          <Field label={`Prioritas: ${priority}`} hint="Kirim cepat default 5; campaign 3."><input type="range" min={0} max={9} value={priority} onChange={(e) => setPriority(+e.target.value)} /></Field>
          <div className="okbox">{list.length} penerima × {info.parts} segmen = {list.length * info.parts} segmen</div>
          {quota.data && quota.data.remaining != null && (
            <div className={list.length * info.parts > quota.data.remaining ? 'err' : 'muted'} role="status">
              Kuota harian Anda: sisa {quota.data.remaining} dari {quota.data.effective} segmen
              {list.length * info.parts > quota.data.remaining && ' — pengiriman ini melebihi sisa kuota dan akan ditolak.'}
            </div>
          )}
          <ErrorBox error={send.error} />
          <button className="primary" disabled={!valid || send.isPending} onClick={() => send.mutate()}>{send.isPending ? 'Mengirim…' : `Kirim ke ${list.length} nomor`}</button>
        </div>
      </div>
      {result && (
        <div className="card stack" role="status">
          <h2>Hasil</h2>
          <div className="okbox">{result.messages.length} pesan masuk antrean. <Link to="/transactions">Lihat di Transaksi →</Link></div>
          {result.rejected.length > 0 && <div className="err"><strong>{result.rejected.length} ditolak:</strong><ul>{result.rejected.map((r) => <li key={r.index}>baris {r.index + 1}: <span className="mono">{r.to}</span> — {{ invalid_number: 'nomor tidak valid', suppressed: 'ada di daftar blokir', duplicate_client_ref: 'client_ref ganda', missing_variable: 'variabel template hilang', destination_not_allowed: 'tujuan tidak diizinkan' }[r.reason] ?? r.reason}</li>)}</ul></div>}
        </div>
      )}
    </div>
  )
}
