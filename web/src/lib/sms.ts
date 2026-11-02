// Penghitung encoding/segmen SMS - SAMA dengan server/internal/sms (spec 04 §9):
// GSM-7 160/153, UCS-2 70/67; karakter extension GSM = 2 septet; di luar BMP (emoji) = 2 unit UTF-16.
const GSM_BASIC = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
const GSM_EXT = '^{}\\[~]|€\f'
const basic = new Set([...GSM_BASIC])
const ext = new Set([...GSM_EXT])

export interface SmsInfo { encoding: 'gsm7' | 'ucs2'; units: number; parts: number; limitSingle: number }

export function analyze(body: string): SmsInfo {
  let septets = 0
  let gsm = true
  for (const ch of body) {
    if (basic.has(ch)) septets += 1
    else if (ext.has(ch)) septets += 2
    else { gsm = false; break }
  }
  if (gsm) return { encoding: 'gsm7', units: septets, parts: parts(septets, 160, 153), limitSingle: 160 }
  const units = body.length // length JS = jumlah unit UTF-16 (emoji = 2)
  return { encoding: 'ucs2', units, parts: parts(units, 70, 67), limitSingle: 70 }
}

function parts(n: number, single: number, multi: number): number {
  return n <= single ? 1 : Math.ceil(n / multi)
}

/** Normalisasi ringan untuk pratinjau; server tetap otoritatif (libphonenumber). */
export function parseRecipients(text: string): string[] {
  return text.split(/[\n,;]+/).map((s) => s.trim()).filter(Boolean)
}

export function variables(tpl: string): string[] {
  return [...new Set([...tpl.matchAll(/\{\{\s*([A-Za-z0-9_]+)\s*\}\}/g)].map((m) => m[1]))]
}
