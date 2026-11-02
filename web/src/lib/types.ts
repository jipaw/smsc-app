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
export type Campaign = S['Campaign']
export type CampaignPreview = S['CampaignPreview']
export type ImportResult = S['ImportResult']

export interface Me { id: string; email: string; role: Role; csrf_token: string }
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
