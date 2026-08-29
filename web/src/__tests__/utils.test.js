// ── 工具函数单元测试 utils.test.js ──
// 覆盖 web/src/utils.js 中各格式化函数的边界与正常值：
// fmtPct / fmtNum / fmtMoney / pnlClass / fmtTime / toStr。
// 重点验证 null/undefined/NaN/0 等边界返回 '-' 与正确的百分比/千分位格式。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { fmtPct, fmtNum, fmtMoney, pnlClass, fmtTime, toStr } from '../utils.js'

describe('fmtPct', () => {
  it('格式化正常小数（toFixed 精度）', () => {
    expect(fmtPct(0.1234)).toBe('0.12%')
    expect(fmtPct(0.5)).toBe('0.50%')
  })
  it('自定义精度', () => {
    expect(fmtPct(0.1, 0)).toBe('0%')
    expect(fmtPct(0.123, 4)).toBe('0.1230%')
  })
  it('null/undefined/NaN 返回 "-"', () => {
    expect(fmtPct(null)).toBe('-')
    expect(fmtPct(undefined)).toBe('-')
    expect(fmtPct(NaN)).toBe('-')
  })
  it('0 返回 0.00%', () => {
    expect(fmtPct(0)).toBe('0.00%')
  })
})

describe('fmtNum', () => {
  it('格式化正常数字', () => {
    expect(fmtNum(12.345)).toBe('12.35')
  })
  it('自定义精度', () => {
    expect(fmtNum(1, 0)).toBe('1')
  })
  it('null/undefined/NaN 返回 "-"', () => {
    expect(fmtNum(null)).toBe('-')
    expect(fmtNum(NaN)).toBe('-')
  })
})

describe('fmtMoney', () => {
  it('千分位格式化', () => {
    expect(fmtMoney(1234.56)).toBe('1,234.56')
  })
  it('大数字', () => {
    expect(fmtMoney(1234567.89)).toBe('1,234,567.89')
  })
  it('null/undefined/NaN 返回 "-"', () => {
    expect(fmtMoney(null)).toBe('-')
    expect(fmtMoney(NaN)).toBe('-')
  })
})

describe('pnlClass', () => {
  it('正数返回 pnl-up', () => {
    expect(pnlClass(100)).toBe('pnl-up')
  })
  it('负数返回 pnl-down', () => {
    expect(pnlClass(-50)).toBe('pnl-down')
  })
  it('0 返回 pnl-flat', () => {
    expect(pnlClass(0)).toBe('pnl-flat')
  })
  it('NaN 返回 pnl-flat', () => {
    expect(pnlClass(NaN)).toBe('pnl-flat')
  })
})

describe('fmtTime', () => {
  it('格式化时间戳', () => {
    const result = fmtTime('2026-08-29T10:30:00')
    expect(result).toMatch(/\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}/)
  })
  it('空值返回 "-"', () => {
    expect(fmtTime(null)).toBe('-')
    expect(fmtTime(undefined)).toBe('-')
    expect(fmtTime(0)).toBe('-')
    expect(fmtTime('')).toBe('-')
  })
  it('无效日期返回原始字符串', () => {
    expect(fmtTime('invalid')).toBe('invalid')
  })
})

describe('toStr', () => {
  it('null/undefined 返回空串', () => {
    expect(toStr(null)).toBe('')
    expect(toStr(undefined)).toBe('')
  })
  it('其他值转为字符串', () => {
    expect(toStr(123)).toBe('123')
    expect(toStr('hello')).toBe('hello')
    expect(toStr(true)).toBe('true')
  })
})
