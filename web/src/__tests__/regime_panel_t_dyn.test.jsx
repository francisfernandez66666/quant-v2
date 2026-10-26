// ── §DYN-T（2026-10-26）做 T 阈值在仓位面板上的行为锁 ──
//
// 钉四件事，全部围绕"稀疏提交"这一条，因为 T 段是本面板里唯一一段**出厂默认会随版本移动**的键：
//   · T1 没碰过 T 段 → body.sizing 里绝不能出现 `t` 键（把 WithTEffective 的回显原样 POST 回去，
//     等于把今天的出厂默认逐键钉成这个账号的显式配置，日后调默认再也传不下去——§1.7 的物化陷阱，
//     `mode`/`max_total_position_pct` 已经被它坑过一次）；
//   · T2 只改一个键 → 补丁里只有那一个键，其余一个都不带；
//   · T3 关掉总开关 → 补丁是 {enabled:false}，不是"整段消失"也不是"顺带把别的键一起写"；
//   · T4 越界/HHMM 非法 → 拒绝提交并且点名是哪一项（后端对越界是"忽略并回落默认"，
//     页面必须先把这件事说出来，否则管理员以为配好了，引擎其实在按出厂值跑）。
//
// English: behavior locks for the §DYN-T block in the sizing panel. The whole point is sparseness —
// an untouched T section must not appear in the payload at all, and a one-key edit must carry one key.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/react'
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

// 后端 GET 的 T 段是 WithTEffective() 填好的生效值（含出厂默认）——测试必须按这份真实形状给，
// 给"只有用户写过的键"会把稀疏提交这条锁测成假绿。
function regimePayload() {
  return {
    enhance: { market_state_enabled: true },
    sizing: {
      enabled: true,
      sell_boost_level: 'L2',
      breadth_missing_scale: 0.9,
      tier_scales: { Yellow: 0.6, Red: 0.3 },
      t: {
        enabled: true,
        mode: 'both',
        max_rounds_per_code_per_day: 1,
        intraday_cap_pct: 50,
        min_spread_pct: 2,
        force_unwind_at: '1450',
        last_open_at: '1400',
        vwap_dev_pct: 0.8,
        min_amplitude_pct: 2.5,
        near_extreme_pct: 0.3,
        limit_distance_pct: 1,
        open_cooling_min: 15,
        min_market_scale: 0.4,
        sell_env_min_market_scale: 0.2,
        market_plunge_pct: -2,
        intraday_sell_cap_total_pct: 20,
        stop_guard_pct: 1.5,
        loss_breaker_pct: 3,
        min_amount_wan: 3000,
        min_turnover_pct: 1.5,
        min_listed_days: 60,
        vwap_check_tol_pct: 0.1,
      },
    },
    macro_gate: {
      yellow_confidence: 0.9, red_confidence: 0.92, red_block_new_buy: false,
      emotion_enabled: true, block_n_shape: true, block_momentum: true,
    },
  }
}

function stubs() {
  return {
    fetchRegimeConfig: () => regimePayload(),
    setRegimeConfig: () => ({ status: 'ok' }),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const s = {}
  for (const name of Object.keys(stubs())) {
    s[name] = vi.fn((...args) => state.impl[name](...args))
  }
  return { ...actual, ...s }
})

import RegimeConfigPanel from '../components/RegimeConfigPanel.jsx'

function btn(text) {
  const el = [...document.querySelectorAll('button,div[type="button"]')].find(
    (b) => b.getAttribute('role') !== 'switch' && (b.textContent || '').trim() === text,
  )
  if (!el) throw new Error(`未找到「${text}」按钮`)
  return el
}

// 面板把 T 段收在"展开精细参数"之后，所以取控件前先展开一次。
async function openFine() {
  fireEvent.click(btn('展开精细参数'))
  await settle()
}

function switchByLabel(text) {
  const row = [...document.querySelectorAll('span')].find((n) => (n.textContent || '').trim() === text)?.closest('div')
  if (!row) throw new Error(`未找到标签「${text}」`)
  const sw = row.querySelector('[role="switch"]')
  if (!sw) throw new Error(`标签「${text}」所在行没有开关`)
  return sw
}

function inputByLabel(text) {
  const row = [...document.querySelectorAll('span')].find((n) => (n.textContent || '').trim() === text)?.closest('div')
  if (!row) throw new Error(`未找到标签「${text}」`)
  const input = row.querySelector('input')
  if (!input) throw new Error(`标签「${text}」所在行没有输入框`)
  return input
}

async function savedBody() {
  const api = await import('../api/index.js')
  const calls = api.setRegimeConfig.mock.calls
  if (!calls.length) throw new Error('没有发出保存请求')
  return calls[calls.length - 1][0]
}

describe('§DYN-T 做 T 阈值：稀疏提交 / 单键补丁 / 越界拒绝', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.impl = stubs()
    const api = await import('../api/index.js')
    for (const name of Object.keys(state.impl)) {
      if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
    }
  })
  afterEach(() => cleanup())

  it('T1 未触碰 T 段时，保存载荷里不得出现 t 键', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const body = await savedBody()
    expect(body.sizing).toBeTruthy()
    expect('t' in body.sizing).toBe(false)
  })

  it('T2 只改机动仓上限 → 补丁只带 intraday_cap_pct 这一个键', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    const input = inputByLabel('机动仓上限（占持仓%）')
    fireEvent.change(input, { target: { value: '35' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const body = await savedBody()
    expect(body.sizing.t).toEqual({ intraday_cap_pct: 35 })
  })

  it('T3 关掉做 T 总开关 → 补丁是 {enabled:false}（不得顺带写别的键）', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    fireEvent.click(switchByLabel('做 T 总开关'))
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const body = await savedBody()
    expect(body.sizing.t).toEqual({ enabled: false })
  })

  it('T4 越界与非法时刻：拒绝提交并在提示里点名', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    // 买回熔断 0.2 < 下限 1（后端会静默回落 3.0，页面必须先拦住）
    fireEvent.change(inputByLabel('买回熔断阈值（%）'), { target: { value: '0.2' } })
    // 最后开新腿时刻写成 9:45（3 位），后端按 4 位 HHMM 判格式会回落 1400
    fireEvent.change(inputByLabel('最后开新腿时刻'), { target: { value: '945' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(0)
    const text = document.body.textContent || ''
    expect(text).toContain('买回熔断阈值%（须 1~10）')
    expect(text).toContain('最后开新腿时刻（须为 4 位 HHMM）')
  })
})
