// 2026-11-11 owner 需求：数值列排序必须按数字大小，缺值/格式化串不得把整表拖成乱序。
// English: numeric sort regression locks (formatted strings, nulls, mixed shapes).
import { describe, it, expect } from 'vitest'
import { sorterNum, sorterStr, numVal } from '../pages/Signals.jsx'
import { resolveBookMode, normalizeManualCode } from '../pages/Positions.jsx'

describe('signal numeric sorting', () => {
  it('sorts by numeric value, not lexicographic', () => {
    const rows = [
      { code: 'A', total_score: 9 },
      { code: 'B', total_score: 100 },
      { code: 'C', total_score: 20 },
    ]
    expect([...rows].sort(sorterNum('total_score')).map((r) => r.code)).toEqual(['A', 'C', 'B'])
    expect([...rows].sort((a, b) => sorterNum('total_score')(b, a)).map((r) => r.code)).toEqual(['B', 'C', 'A'])
  })

  it('parses formatted strings and sinks unparsable/missing values', () => {
    expect(numVal('¥22.68')).toBe(22.68)
    expect(numVal('89.5%')).toBe(89.5)
    expect(numVal('—')).toBe(null)
    expect(numVal(null)).toBe(null)
    const rows = [
      { code: 'A', price: '—' },
      { code: 'B', price: '¥9.5' },
      { code: 'C', price: 30 },
      { code: 'D', price: null },
    ]
    expect([...rows].sort(sorterNum('price')).map((r) => r.code)).toEqual(['B', 'C', 'A', 'D'])
  })

  it('string sorter tolerates null fields', () => {
    const rows = [{ name: null }, { name: '甲' }, { name: '乙' }]
    expect(() => [...rows].sort(sorterStr('name'))).not.toThrow()
  })
  it('normalizes bare 6-digit codes to exchange suffixes', () => {
    expect(normalizeManualCode('601958')).toBe('601958.SH')
    expect(normalizeManualCode('002631')).toBe('002631.SZ')
    expect(normalizeManualCode('300750')).toBe('300750.SZ')
    expect(normalizeManualCode('430047')).toBe('430047.BJ')
    expect(normalizeManualCode('600000.SH')).toBe('600000.SH')
    expect(normalizeManualCode(' 601958 ')).toBe('601958.SH')
    expect(normalizeManualCode('12ab')).toBe('12AB')  // 非法形态原样透传给后端拒
  })
})

describe('book maintenance mode (explicit switch)', () => {
  it('manual forces adjust mode regardless of counter state', () => {
    expect(resolveBookMode('manual', false, false, false)).toBe(true)
    expect(resolveBookMode('manual', true, true, true)).toBe(true)
  })
  it('auto follows book source, frozen frame still opens adjust', () => {
    expect(resolveBookMode('auto', false, false, false)).toBe(false)
    expect(resolveBookMode('auto', true, false, false)).toBe(true)
    expect(resolveBookMode('auto', false, false, true)).toBe(true)
  })
})
