import type { components } from '../api/schema'

// Tipe diturunkan langsung dari contracts/openapi.yaml (npm run gen:api).
type S = components['schemas']
export type Device = S['Device']
export type Sim = S['Sim']
export type Message = S['Message']
export type MessageEvent = S['MessageEvent']
export type MessageStatus = S['MessageStatus']
export type Template = S['Template']
export type DispatchPolicy = S['DispatchPolicy']
export type DispatchPolicyInput = S['DispatchPolicyInput']
export type Enrollment = S['Enrollment']
export type ApiKey = S['ApiKey']
export type Suppression = S['Suppression']
export type AuditLog = S['AuditLog']
export type Role = S['Role']
export type Webhook = S['Webhook']
export type WebhookCreated = S['WebhookCreated']
export type WebhookDelivery = S['WebhookDelivery']
export type WebhookEvent = S['WebhookEvent']
export type RetentionSettings = S['RetentionSettings']
export type Campaign = S['Campaign']
export type CampaignPreview = S['CampaignPreview']
export type ImportResult = S['ImportResult']

export interface Me { id: string; email: string; role: Role; csrf_token: string; mfa_enabled?: boolean; mfa_setup_required?: boolean; has_password?: boolean; sso_linked?: boolean; sso_session?: boolean }
export interface AuthMethods { passkey_login: boolean; oidc: { enabled: boolean; name?: string } }
export interface SSOView { enabled: boolean; name?: string; issuer?: string; client_id?: string; redirect_url?: string; reachable?: boolean; reach_error?: string; auto_provision: boolean; default_role?: string; link_by_email: boolean; sync_roles: boolean; trust_idp_mfa: boolean; allowed_domains: string[]; linked_users: number }
export const SSO_ERRORS: Record<string, string> = {
  invalid_state: 'Sesi SSO tidak valid atau kedaluwarsa. Coba masuk lagi dari awal.', session_mismatch: 'Penautan SSO harus diselesaikan di sesi yang sama.',
  denied: 'Login SSO dibatalkan atau ditolak oleh penyedia identitas.', token: 'Verifikasi token dari penyedia identitas gagal.', unavailable: 'Penyedia identitas (IdP) tidak dapat dihubungi.',
  not_linked: 'Akun SSO ini belum tertaut ke pengguna. Minta admin, atau tautkan dari halaman Akun setelah masuk dengan password.',
  unverified_email: 'Penyedia identitas tidak memberi email yang terverifikasi.', domain: 'Domain email Anda tidak diizinkan untuk SSO di sini.',
  disabled: 'Akun ini dinonaktifkan.', conflict: 'Akun dengan email ini sudah ada.', link_conflict: 'Identitas SSO ini sudah tertaut ke akun lain, atau akun Anda sudah tertaut.', internal: 'Terjadi kesalahan di server.',
}
export interface MfaChallenge { mfa_required: true; mfa_token: string; expires_in?: number; methods?: ('webauthn' | 'totp' | 'recovery')[] }
export interface MfaStatus { enabled: boolean; totp_enabled: boolean; webauthn_credentials: number; enabled_at: string | null; recovery_codes_remaining: number }
export interface WebAuthnCred { id: string; name: string; created_at: string; last_used_at?: string | null; backed_up: boolean; passkey: boolean; transports: string[] }
export interface WebAuthnList { enabled: boolean; rp_id?: string; credentials: WebAuthnCred[] }
export interface SecuritySettings { require_2fa: 'off' | 'admin' | 'all'; passkey_login: boolean; users_missing_2fa: number }
export interface UserItem { id: string; email: string; role: Role; mfa_enabled?: boolean; has_password?: boolean; sso_linked?: boolean; disabled: boolean; created_at: string }
export interface Invitation { id: string; email: string; role: Role; status: 'pending' | 'accepted' | 'expired' | 'revoked'; created_by?: string | null; created_at: string; expires_at: string; accepted_at?: string | null; token?: string; invite_url?: string; email_status?: 'queued' | 'sent' | 'failed' | 'cancelled' | null; email_error?: string | null }
export interface EmailConfig { configured: boolean; host?: string; port?: number; mode?: string; from?: string; auth_enabled: boolean }
export interface MfaSetup { secret: string; otpauth_uri: string }
export interface Page<T> { items: T[]; next_cursor: string | null }
export interface Overview {
  devices: { online: number; degraded: number; offline: number }
  sims_active: number
  queue_depth: number
  today: { accepted: number; sent: number; delivered: number; failed: number }
  success_rate_24h: number
  paused: boolean
}
export interface Point { t: string; sent: number; delivered: number; failed: number }
export type MessageDetail = Message & { events: MessageEvent[] }
export interface SendResult {
  messages: Message[]
  rejected: { index: number; to: string; reason: string }[]
}
