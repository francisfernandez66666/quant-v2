// ── §HISQUOTE（2026-10-22）信号页「现价/涨跌」三态与缺数口径 ──
//
// 缺陷原文（台账 2026-10-15 第一条）：信号页取不到行情时被 `(row.change_pct || 0).toFixed(2)`
// 渲染成 `¥0.00 / 0.00%`，把「没有读数」伪造成「价格归零 + 平盘」。
// 后端 §N-12d 已给出 `price_kind = live / trigger / none`，但前端此前没有一条回归锁；
// 本用例把三种状态和「live 但涨跌幅缺失」第四种边角一起钉住：
//   · trigger → 破折号 + 「触发价 ¥xx.xx」（触发价不是现价）；
//   · none / price<=0（含旧载荷没带 price_kind）→ 破折号 + 「无行情」，绝不出现 ¥0.00；
//   · live + change_pct=0 → 合法平盘，必须显示 ¥10.00 / 0.00%（0 不能被当成缺失）；
//   · live + change_pct 缺失 → 价格照常、涨跌幅显示「—」（缺数≠平盘）。
//
// English: locks the signals table's quote states — trigger/none/zero-price render "—/无行情"
// (never ¥0.00 + 0.00%), a genuine flat quote still renders 0.00%, and a missing change pct
// renders "—" instead of a fake flat reading.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'

const { state } = vi.hoisted(() => ({ state: { impl: {} } }))

// 所有行共用的非行情字段：表头/四维/操作列都靠它们正常渲染。
const BASE = {
  strategy: '龙头首板', strategy_type: 'dragon', strategy_id: 'dragon-1',
  level: '交易', remind_level: 'strong', action: 'buy', total_score: 88, can_open: true,
  d1: 88, d2: 70, d3: 65, d4: 40,
  d1_desc: '贴板强度', d2_desc: '板块共振', d3_desc: '超额收益', d4_desc: '5日趋势',
}
const LIVE_UP = { ...BASE, code: '600001', name: '上涨股', price: 10.5, change_pct: 1.23, price_kind: 'live' }
const LIVE_FLAT = { ...BASE, code: '600002', name: '平盘股', price: 10, change_pct: 0, price_kind: 'live' }
// 老载荷/上游漏填：price_kind 缺失且 price=0，前端必须自己判成「无行情」。
const LEGACY_ZERO = { ...BASE, code: '600003', name: '旧载荷股', price: 0 }
const TRIGGER = { ...BASE, code: '600004', name: '触发价股', price: 30, price_kind: 'trigger' }
const NONE = { ...BASE, code: '600005', name: '无行情股', price: 0, price_kind: 'none' }
// live 但涨跌幅缺席：价格可用、涨跌幅只能显示「—」。
const LIVE_NO_CHG = { ...BASE, code: '600006', name: '缺涨幅股', price: 12, price_kind: 'live' }

function basePayloads(list) {
  return {
    isAdmin: () => false,
    getAccount: () => 'admin',
    fetchStatus: () => ({ session: 'test', signal_count: list.length }),
    setLastSession: () => {},
    connectSSE: () => {},
    fetchSignals: () => list,
    fetchPaperState: () => ({ enabled: false }),
    fetchShortStatus: () => ({ short_enabled: false }),
    fetchAlerts: () => [],
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(basePayloads([]))) {
    stubs[name] = vi.fn(async (...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import Signals from '../pages/Signals.jsx'

async function mount() {
  await act(async () => { render(<Signals />) })
  await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
}

describe('§HISQUOTE 信号页行情三态', () => {
  beforeEach(() => {
    state.impl = basePayloads([LIVE_UP, LIVE_FLAT, LEGACY_ZERO, TRIGGER, NONE, LIVE_NO_CHG])
  })
  afterEach(() => cleanup())

  it('S1 trigger / none / 旧载荷 price=0 → 破折号 + 恰当说明，绝不出现 ¥0.00', async () => {
    await mount()
    // 三行都走「无现价」分支：trigger、none、旧载荷 price=0
    expect(screen.getAllByTestId('sig-no-live-price').length).toBe(3)
    expect(screen.getAllByText('无行情').length).toBe(2)
    expect(screen.getByText('触发价 ¥30.00')).toBeTruthy()
    expect(document.body.textContent, '§HISQUOTE：缺行情不得渲染成 ¥0.00').not.toContain('¥0.00')
  })

  it('S2 真平盘必须显示 0.00%（0 是合法读数，不能被当缺失吞掉）', async () => {
    await mount()
    expect(screen.getByText('¥10.00')).toBeTruthy()
    // 全表只有一个 0.00%：来自平盘行；其余行分别是 +1.23% / 缺涨幅 —
    expect(screen.getAllByText('0.00%').length).toBe(1)
    expect(screen.getByText('+1.23%')).toBeTruthy()
  })

  it('S3 live 但 change_pct 缺失 → 涨跌幅显示「—」而不是 0.00%', async () => {
    await mount()
    const el = screen.getByTestId('sig-live-chg-empty')
    expect(el.textContent).toBe('—')
    // 缺涨幅行的价格照常显示（有行情只是没涨跌幅）
    expect(screen.getByText('¥12.00')).toBeTruthy()
  })

  it('S4 负向清零：全表不得出现「¥0.00」或第二个 0.00%', async () => {
    await mount()
    expect(document.body.textContent).not.toContain('¥0.00')
    expect(screen.getAllByText('0.00%').length, '§HISQUOTE：只有真平盘行能出现 0.00%').toBe(1)
  })
})
