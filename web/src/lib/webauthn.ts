// Pembungkus WebAuthn: opsi JSON dari server (biner base64url) <-> ArrayBuffer API browser.
// Sengaja tidak memakai PublicKeyCredential.parse*FromJSON agar jalan di browser yang belum mendukungnya.

export const webauthnSupported = () => typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials

const toBuf = (s: string): ArrayBuffer => {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4)
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}
const toB64 = (b: ArrayBuffer | null | undefined): string | undefined => {
  if (!b) return undefined
  let s = ''
  for (const c of new Uint8Array(b)) s += String.fromCharCode(c)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

type Desc = { id: string; type: string; transports?: string[] }
type PK = Record<string, unknown> & { challenge: string; user?: { id: string }; excludeCredentials?: Desc[]; allowCredentials?: Desc[] }

const desc = (d: Desc) => ({ ...d, id: toBuf(d.id) })

/** Menjalankan navigator.credentials.create() dari opsi server dan menyerialisasi hasilnya untuk /register/finish. */
export async function createCredential(options: { publicKey: PK }): Promise<Record<string, unknown>> {
  const pk = options.publicKey
  const cred = (await navigator.credentials.create({
    publicKey: { ...pk, challenge: toBuf(pk.challenge), user: { ...(pk.user as object), id: toBuf(pk.user!.id) }, excludeCredentials: (pk.excludeCredentials ?? []).map(desc) } as unknown as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null
  if (!cred) throw new Error('Pembuatan kunci dibatalkan')
  const r = cred.response as AuthenticatorAttestationResponse
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type, authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    response: { clientDataJSON: toB64(r.clientDataJSON), attestationObject: toB64(r.attestationObject), transports: r.getTransports?.() ?? [] },
    clientExtensionResults: cred.getClientExtensionResults(),
  }
}

/** navigator.credentials.get() dari opsi server → bentuk untuk /login/webauthn/finish. */
export async function getCredential(options: { publicKey: PK }): Promise<Record<string, unknown>> {
  const pk = options.publicKey
  const cred = (await navigator.credentials.get({
    publicKey: { ...pk, challenge: toBuf(pk.challenge), allowCredentials: (pk.allowCredentials ?? []).map(desc) } as unknown as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null
  if (!cred) throw new Error('Verifikasi dibatalkan')
  const r = cred.response as AuthenticatorAssertionResponse
  return {
    id: cred.id, rawId: toB64(cred.rawId), type: cred.type, authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    response: { clientDataJSON: toB64(r.clientDataJSON), authenticatorData: toB64(r.authenticatorData), signature: toB64(r.signature), userHandle: toB64(r.userHandle) },
    clientExtensionResults: cred.getClientExtensionResults(),
  }
}

/** Pesan ramah untuk kesalahan DOM umum (dibatalkan, tidak ada kunci, dsb). */
export function waErrMsg(e: unknown): string {
  const n = (e as { name?: string })?.name
  if (n === 'NotAllowedError') return 'Dibatalkan, kedaluwarsa, atau tidak ada kunci yang cocok.'
  if (n === 'InvalidStateError') return 'Kunci ini sudah terdaftar di akun Anda.'
  if (n === 'SecurityError') return 'Ditolak browser: halaman harus HTTPS dengan nama domain yang sama dengan RP ID server.'
  return e instanceof Error ? e.message : String(e)
}
