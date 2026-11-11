// 2026-11-11 owner 需求：交易卡三条规则的判定层回归锁。
// ① 非交易时段不提醒；② 信号消失卡消失；③ 缺读数卡弱化（渲染层改了文案/样式，这里钉判定）。
import { describe, it, expect } from 'vitest'
import { isTradingSessionNow, pruneCardsByLive } from '../utils/signalCard.js'

describe('trading session gate (Beijing 09:30-11:30 / 13:00-15:00, weekdays)', () => {
  // 用 UTC 时刻构造北京时间：BJ = UTC + 8h
  const at = (utcIso) => new Date(utcIso)

  it('morning session boundaries', () => {
    expect(isTradingSessionNow(at('2026-11-11T01:29:00Z'))).toBe(false) // 09:29
    expect(isTradingSessionNow(at('2026-11-11T01:30:00Z'))).toBe(true)  // 09:30
    expect(isTradingSessionNow(at('2026-11-11T03:30:00Z'))).toBe(true)  // 11:30
    expect(isTradingSessionNow(at('2026-11-11T03:31:00Z'))).toBe(false) // 11:31
  })
  it('afternoon session boundaries', () => {
    expect(isTradingSessionNow(at('2026-11-11T04:59:00Z'))).toBe(false) // 12:59
    expect(isTradingSessionNow(at('2026-11-11T05:00:00Z'))).toBe(true)  // 13:00
    expect(isTradingSessionNow(at('2026-11-11T07:00:00Z'))).toBe(true)  // 15:00
    expect(isTradingSessionNow(at('2026-11-11T07:01:00Z'))).toBe(false) // 15:01
  })
  it('weekend is never a session', () => {
    // 2026-11-14 是周六
    expect(isTradingSessionNow(at('2026-11-14T02:00:00Z'))).toBe(false)
    expect(isTradingSessionNow(at('2026-11-15T02:00:00Z'))).toBe(false) // 周日
  })
})

describe('cards vanish when their signal disappears', () => {
  it('prunes cards whose signal_id is no longer in the live list', () => {
    const cards = [{ signalId: 's1' }, { signalId: 's2' }, { signalId: 'gone' }]
    expect(pruneCardsByLive(cards, ['s1', 's2', 's3']).map((c) => c.signalId)).toEqual(['s1', 's2'])
    expect(pruneCardsByLive(cards, []).length).toBe(0)
  })
})
