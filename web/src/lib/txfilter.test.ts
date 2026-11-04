import { describe, expect, it } from 'vitest'
import { EMPTY_FILTER, filterLink, parseFilter, toSearch } from './txfilter'

const sp = (s: string) => new URLSearchParams(s)
const CAMP = '0192f3c0-7a1e-7c3e-9a55-000000000064'

describe('parseFilter', () => {
  it('URL kosong = filter kosong', () => expect(parseFilter(sp(''))).toEqual(EMPTY_FILTER))
  it('membaca semua parameter yang valid', () => {
    expect(parseFilter(sp(`status=failed,unknown&source=api&to=%2B62812&since=2026-11-04T08:30&parsial=allsent&campaign=${CAMP}`))).toEqual({
      status: ['failed', 'unknown'], source: 'api', to: '+62812', since: '2026-11-04T08:30', partial: 'allsent', campaign: CAMP,
    })
  })
  it('nilai tak valid dibuang (URL tidak dipercaya)', () => {
    const f = parseFilter(sp(`status=failed,hack,failed&source=ftp&to=%2B62abc<script>&since=besok&parsial=semua&campaign=bukan-uuid`))
    expect(f).toEqual({ status: ['failed'], source: '', to: '+62', since: '', partial: '', campaign: '' })
  })
  it('tanggal mustahil ditolak dan nomor dipotong 20 karakter', () => {
    expect(parseFilter(sp('since=2026-13-45T99:99')).since).toBe('')
    expect(parseFilter(sp('to=' + '1'.repeat(40))).to).toHaveLength(20)
  })
  it('uuid campaign huruf besar dinormalkan', () => expect(parseFilter(sp(`campaign=${CAMP.toUpperCase()}`)).campaign).toBe(CAMP))
})

describe('toSearch / filterLink', () => {
  it('nilai kosong dihilangkan dan bolak-balik konsisten', () => {
    const f = { ...EMPTY_FILTER, status: ['failed'], partial: 'partial' as const, to: '+62' }
    const s = toSearch(f)
    expect(s.get('source')).toBeNull()
    expect(parseFilter(s)).toEqual(f)
  })
  it('parameter milik halaman lain dipertahankan, parameter filter diganti', () => {
    const s = toSearch({ ...EMPTY_FILTER, source: 'api' }, sp('tab=x&status=failed&source=sdk'))
    expect(s.get('tab')).toBe('x')
    expect(s.get('status')).toBeNull()
    expect(s.get('source')).toBe('api')
  })
  it('filterLink membangun tautan ke /transactions', () => {
    expect(filterLink({})).toBe('/transactions')
    expect(filterLink({ partial: 'partial', campaign: CAMP })).toBe(`/transactions?parsial=partial&campaign=${CAMP}`)
  })
})

import { fromView, sameFilter, toView } from './txfilter'

describe('tampilan tersimpan', () => {
  it('toView/fromView bolak-balik', () => {
    const f = { ...EMPTY_FILTER, status: ['failed'], partial: 'allsent' as const, to: '+62', campaign: CAMP }
    expect(fromView(toView(f))).toEqual(f)
  })
  it('fromView membuang nilai yang tak lagi valid dengan aturan URL', () => {
    expect(fromView({ status: ['failed', 'aneh'], source: 'ftp', partial: 'x', campaign: 'bukan' })).toEqual({ ...EMPTY_FILTER, status: ['failed'] })
  })
  it('sameFilter tidak peka urutan status', () => {
    expect(sameFilter({ ...EMPTY_FILTER, status: ['a', 'b'] as string[] }, { ...EMPTY_FILTER, status: ['b', 'a'] })).toBe(true)
    expect(sameFilter(EMPTY_FILTER, { ...EMPTY_FILTER, to: '+6' })).toBe(false)
  })
})
