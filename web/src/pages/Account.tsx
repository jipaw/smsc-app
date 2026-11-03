import { FormEvent, useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import QRCode from 'qrcode'
import { get, post } from '../lib/api'
import { fmtTime } from '../lib/format'
import { useMe, useRefreshMe } from '../lib/session'
import type { MfaSetup, MfaStatus } from '../lib/types'
import { ErrorBox, Field, Loading, useToast } from '../components/ui'

export function Account() {
  const me = useMe()
  return (
    <div className="stack gap-lg">
      <h1>Akun</h1>
      <div className="muted">{me.email} · {me.role}</div>
      {me.mfa_setup_required && <div className="banner" role="alert">Kebijakan keamanan mewajibkan 2FA untuk akun Anda. Aktifkan di bawah ini untuk membuka dashboard.</div>}
      <Password />
      <TwoFactor />
    </div>
  )
}

function Password() {
  const toast = useToast()
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [again, setAgain] = useState('')
  const m = useMutation({
    mutationFn: () => post('/auth/password', { current_password: cur, new_password: next }),
    onSuccess: () => { toast('Password diganti. Sesi di perangkat lain dikeluarkan.'); setCur(''); setNext(''); setAgain('') },
  })
  const mismatch = again !== '' && again !== next
  const submit = (e: FormEvent) => { e.preventDefault(); if (!mismatch) m.mutate() }
  return (
    <form className="card stack" onSubmit={submit} aria-label="Ganti password">
      <h2>Ganti password</h2>
      <Field label="Password saat ini"><input type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} required /></Field>
      <Field label="Password baru" hint="Minimal 12 karakter."><input type="password" autoComplete="new-password" minLength={12} value={next} onChange={(e) => setNext(e.target.value)} required /></Field>
      <Field label="Ulangi password baru"><input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} required /></Field>
      {mismatch && <div className="err" role="alert">Password tidak sama.</div>}
      <ErrorBox error={m.error} />
      <div><button className="primary" disabled={m.isPending || mismatch || !cur || next.length < 12}>Ganti password</button></div>
    </form>
  )
}

function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const text = codes.join('\n')
  return (
    <div className="okbox stack" role="status">
      <strong>Simpan kode pemulihan ini sekarang — tidak akan ditampilkan lagi.</strong>
      <span className="muted">Tiap kode hanya berlaku sekali, untuk masuk bila HP authenticator hilang.</span>
      <ul className="mono" style={{ columns: 2, listStyle: 'none', padding: 0, margin: 0 }}>{codes.map((c) => <li key={c}>{c}</li>)}</ul>
      <div className="row">
        <button type="button" className="small" onClick={() => navigator.clipboard?.writeText(text)}>Salin</button>
        <a className="btn small" style={{ textDecoration: "none" }} download="smsc-recovery-codes.txt" href={'data:text/plain;charset=utf-8,' + encodeURIComponent(text + '\n')}>Unduh</a>
        <button type="button" className="small primary" onClick={onDone}>Sudah saya simpan</button>
      </div>
    </div>
  )
}

function TwoFactor() {
  const qc = useQueryClient()
  const toast = useToast()
  const refreshMe = useRefreshMe()
  const q = useQuery({ queryKey: ['mfa'], queryFn: () => get<MfaStatus>('/auth/2fa') })
  const [pw, setPw] = useState('')
  const [code, setCode] = useState('')
  const [setup, setSetup] = useState<MfaSetup | null>(null)
  const [qr, setQr] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const [mode, setMode] = useState<'' | 'disable' | 'regen'>('')
  useEffect(() => { if (setup) QRCode.toDataURL(setup.otpauth_uri, { margin: 1, width: 200 }).then(setQr); else setQr('') }, [setup])
  const refresh = () => { qc.invalidateQueries({ queryKey: ['mfa'] }); qc.invalidateQueries({ queryKey: ['users'] }) }
  const start = useMutation({ mutationFn: () => post<MfaSetup>('/auth/2fa/setup', { password: pw }), onSuccess: (s) => { setSetup(s); setPw('') } })
  const enable = useMutation({ mutationFn: () => post<{ recovery_codes: string[] }>('/auth/2fa/enable', { code }), onSuccess: (r) => { setCodes(r.recovery_codes); setSetup(null); setCode(''); refresh() } })
  const disable = useMutation({ mutationFn: () => post('/auth/2fa/disable', { password: pw, code }), onSuccess: () => { toast('2FA dinonaktifkan'); setMode(''); setPw(''); setCode(''); refresh() } })
  const regen = useMutation({ mutationFn: () => post<{ recovery_codes: string[] }>('/auth/2fa/recovery-codes', { password: pw, code }), onSuccess: (r) => { setCodes(r.recovery_codes); setMode(''); setPw(''); setCode(''); refresh() } })
  if (q.isLoading) return <Loading what="status 2FA" />
  if (!q.data) return <ErrorBox error={q.error} />
  const st = q.data
  return (
    <section className="card stack" aria-label="Verifikasi 2 langkah">
      <h2>Verifikasi 2 langkah (TOTP)</h2>
      {codes && <RecoveryCodes codes={codes} onDone={() => { setCodes(null); refreshMe() }} />}
      {!st.enabled && !setup && (
        <div className="stack">
          <p className="muted">Tambahkan lapisan kedua saat masuk dengan aplikasi authenticator (Google Authenticator, Aegis, 1Password, dll.). Disarankan untuk semua admin.</p>
          <Field label="Password" hint="Diminta ulang untuk keamanan."><input type="password" autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
          <ErrorBox error={start.error} />
          <div><button className="primary" disabled={!pw || start.isPending} onClick={() => start.mutate()}>Siapkan 2FA</button></div>
        </div>
      )}
      {!st.enabled && setup && (
        <div className="stack">
          <p>1. Pindai QR ini dengan aplikasi authenticator (atau masukkan kunci manual).</p>
          {qr && <img src={qr} alt="QR untuk aplikasi authenticator" width={200} height={200} />}
          <div>Kunci manual: <span className="mono" data-testid="totp-secret">{setup.secret}</span></div>
          <p>2. Masukkan kode 6 digit yang ditampilkan untuk mengonfirmasi.</p>
          <Field label="Kode 6 digit"><input inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} /></Field>
          <ErrorBox error={enable.error} />
          <div className="row"><button className="primary" disabled={code.length !== 6 || enable.isPending} onClick={() => enable.mutate()}>Aktifkan</button>
            <button className="small" onClick={() => { setSetup(null); setCode('') }}>Batal</button></div>
        </div>
      )}
      {st.enabled && (
        <div className="stack">
          <div><span className="badge ok">Aktif</span> sejak {fmtTime(st.enabled_at)} · sisa kode pemulihan: <strong>{st.recovery_codes_remaining}</strong>{st.recovery_codes_remaining <= 2 && <span className="err"> — hampir habis, terbitkan ulang.</span>}</div>
          {mode === '' && <div className="row"><button className="small" onClick={() => setMode('regen')}>Terbitkan ulang kode pemulihan</button><button className="small danger" onClick={() => setMode('disable')}>Nonaktifkan 2FA</button></div>}
          {mode !== '' && (
            <div className="stack">
              <p className="muted">{mode === 'disable' ? 'Nonaktifkan 2FA' : 'Terbitkan ulang kode pemulihan (kode lama hangus)'} — masukkan password dan kode saat ini.</p>
              <Field label="Password"><input type="password" autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
              <Field label="Kode TOTP atau kode pemulihan"><input autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} /></Field>
              <ErrorBox error={mode === 'disable' ? disable.error : regen.error} />
              <div className="row">
                <button className={mode === 'disable' ? 'danger' : 'primary'} disabled={!pw || !code || disable.isPending || regen.isPending} onClick={() => (mode === 'disable' ? disable : regen).mutate()}>Konfirmasi</button>
                <button className="small" onClick={() => { setMode(''); setPw(''); setCode('') }}>Batal</button>
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  )
}
