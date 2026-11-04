import { describe, expect, it } from 'vitest'
import { reconnectDelay } from './sse'

describe('reconnectDelay', () => {
  it('backoff eksponensial dengan batas 30 dtk', () => {
    expect([0, 1, 2, 3, 4].map(reconnectDelay)).toEqual([1000, 2000, 4000, 8000, 16000])
    expect(reconnectDelay(5)).toBe(30000)
    expect(reconnectDelay(50)).toBe(30000)
  })
  it('nilai negatif diperlakukan sebagai percobaan pertama', () => {
    expect(reconnectDelay(-3)).toBe(1000)
  })
})
