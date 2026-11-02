// Klien HTTP tipis: cookie sesi (HttpOnly) + header CSRF untuk mutasi; error = problem+json (RFC 9457).
export class ApiError extends Error {
  constructor(public status: number, public type: string, public title: string, public detail?: string,
    public errors?: { field: string; message: string }[]) {
    super(detail || title)
  }
  /** Pesan siap tampil, termasuk error per-field. */
  get pretty(): string {
    const f = this.errors?.map((e) => `${e.field}: ${e.message}`).join('; ')
    return f ? `${this.title} (${f})` : this.detail ? `${this.title}: ${this.detail}` : this.title
  }
}

let csrf = ''
export const setCsrf = (t: string) => { csrf = t }

export async function api<T = unknown>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  const h: Record<string, string> = { Accept: 'application/json', ...headers }
  if (body !== undefined) h['Content-Type'] = 'application/json'
  if (csrf && method !== 'GET') h['X-CSRF-Token'] = csrf
  const res = await fetch('/api/v1' + path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' })
  if (res.status === 204) return undefined as T
  const ct = res.headers.get('content-type') || ''
  const data = ct.includes('json') ? await res.json() : await res.text()
  if (!res.ok) {
    const p = (typeof data === 'object' ? data : {}) as { type?: string; title?: string; detail?: string; errors?: { field: string; message: string }[] }
    throw new ApiError(res.status, p.type || 'error', p.title || res.statusText, p.detail, p.errors)
  }
  return data as T
}

export const get = <T,>(path: string) => api<T>('GET', path)
export const post = <T,>(path: string, body?: unknown, headers?: Record<string, string>) => api<T>('POST', path, body ?? {}, headers)
export const put = <T,>(path: string, body: unknown) => api<T>('PUT', path, body)
export const patch = <T,>(path: string, body: unknown) => api<T>('PATCH', path, body)
export const del = <T,>(path: string) => api<T>('DELETE', path)

export function qs(params: Record<string, string | number | boolean | undefined | null | string[]>): string {
  const u = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || (Array.isArray(v) && v.length === 0)) continue
    u.set(k, Array.isArray(v) ? v.join(',') : String(v))
  }
  const s = u.toString()
  return s ? '?' + s : ''
}

/** Unggah multipart (CSV). Content-Type diisi browser (boundary); CSRF tetap dikirim. */
export async function upload<T>(path: string, form: FormData): Promise<T> {
  const res = await fetch('/api/v1' + path, { method: 'POST', body: form, headers: csrf ? { 'X-CSRF-Token': csrf, Accept: 'application/json' } : { Accept: 'application/json' }, credentials: 'same-origin' })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, data.type || 'error', data.title || res.statusText, data.detail, data.errors)
  return data as T
}
