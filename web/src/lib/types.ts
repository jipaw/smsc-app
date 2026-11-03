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

export interface Me { id: string; email: string; role: Role; csrf_token: string; mfa_enabled?: boolean; mfa_setup_required?: boolean }
export interface MfaChallenge { mfa_required: true; mfa_token: string; expires_in?: number }
export interface MfaStatus { enabled: boolean; enabled_at: string | null; recovery_codes_remaining: number }
export interface SecuritySettings { require_2fa: 'off' | 'admin' | 'all'; users_missing_2fa: number }
export interface UserItem { id: string; email: string; role: Role; mfa_enabled?: boolean; disabled: boolean; created_at: string }
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
