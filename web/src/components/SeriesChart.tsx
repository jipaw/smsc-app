import type { Point } from '../lib/types'
import { chartMax, splitFailed, type Bucket } from '../lib/chart'
import { fmtTime } from '../lib/format'

const UNIT: Record<Bucket, string> = { minute: 'menit', hour: 'jam', day: 'hari' }

/** Batang bertumpuk: gagal penuh, gagal parsial (berpola), sampai, terkirim-menunggu laporan. Dipakai Overview dan halaman campaign. */
export function SeriesChart({ slots, bucket, title, emptyText, leftLabel, rightLabel }: {
  slots: Point[]; bucket: Bucket; title: string; emptyText: string; leftLabel: string; rightLabel: string
}) {
  const total = slots.reduce((a, p) => a + p.sent + p.failed, 0)
  const partialTotal = slots.reduce((a, p) => a + splitFailed(p).partial, 0)
  if (total === 0) return <p className="muted">{emptyText}</p>
  const max = chartMax(slots)
  return (
    <div>
      <div className="bars" role="img" aria-label={`${title}: ${total} pesan${partialTotal ? `, ${partialTotal} di antaranya gagal parsial` : ''}`}>
        {slots.map((p) => {
          const { full, partial: part } = splitFailed(p) // parsial adalah bagian dari gagal: digambar terpisah, tidak dihitung dua kali
          return (
            <div className="b" key={p.t} title={`${fmtTime(p.t)} (per ${UNIT[bucket]}): terkirim ${p.sent}, sampai ${p.delivered}, gagal ${p.failed}${part ? ` (parsial ${part})` : ''}`}>
              <i className="f" style={{ height: `${(full / max) * 100}%` }} />
              <i className="p" style={{ height: `${(part / max) * 100}%` }} />
              <i className="d" style={{ height: `${(p.delivered / max) * 100}%` }} />
              <i className="s" style={{ height: `${(Math.max(0, p.sent - p.delivered) / max) * 100}%` }} />
            </div>
          )
        })}
      </div>
      <div className="row spread hint" style={{ marginTop: 4 }}><span>{leftLabel}</span><span>{rightLabel}</span></div>
      <div className="row hint" style={{ marginTop: 6 }}>
        <span><i style={{ color: 'var(--ok)' }}>■</i> sampai</span><span><i style={{ color: 'var(--ok)', opacity: .6 }}>■</i> terkirim (menunggu laporan)</span><span><i style={{ color: 'var(--bad)' }}>■</i> gagal</span>
        {partialTotal > 0 && <span><i className="swatch-partial" aria-hidden="true" /> gagal parsial ({partialTotal})</span>}
      </div>
    </div>
  )
}
