// ── §T-CLUSTER（2026-10-27）相关性簇帽在仓位面板上的行为锁 ──
//
// 钉三件事，全部围绕 `cluster_cap_pct` 的**三态**语义（缺键=出厂 40；显式 0=关闭）：
//   · C1 键缺失时面板必须回显出厂 40 / 0.70（与后端 ClusterCapPctOf 同口径）；
//   · C2 后端明确回了 0（owner 关掉簇帽）⇒ 面板必须显示 0，不许被 num() 的 n>0 判据
//     读回 40 —— 那会让"打开面板→保存"这个无害动作把簇帽重新打开，属于静默改资金参数；
//   · C3 ρ=0 或越界 ⇒ 拒绝提交并点名（ρ=0 会让每只票都判成"与谁都不同簇"，簇帽静默失效）。
//
// English: behavior locks for the §T-CLUSTER rows in the sizing panel, focused on the tri-state
// `cluster_cap_pct` (absent = 40, explicit 0 = disabled) and on rejecting a degenerate rho.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/react'
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

// regimePayload 造 GET /api/config/regime 的最小载荷；cluster 两键由 override 决定带不带。
// English: minimal regime payload; the cluster keys are controlled by `override`.
function regimePayload(override = {}) {
  return {
    enhance: { market_state_enabled: true },
    sizing: {
      enabled: true,
      sell_boost_level: 'L2',
      breadth_missing_scale: 0.9,
      tier_scales: { Yellow: 0.6, Red: 0.3 },
      ...override,
    },
    macro_gate: {
      yellow_confidence: 0.9, red_confidence: 0.92, red_block_new_buy: false,
      emotion_enabled: true, block_n_shape: true, block_momentum: true,
    },
  }
}

function stubs() {
  return {
    fetchRegimeConfig: () => regimePayload(state.payload || {}),
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

// 面板把仓位细项收在"展开精细参数"之后，取控件前先展开一次。
async function openFine() {
  fireEvent.click(btn('展开精细参数'))
  await settle()
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

describe('§T-CLUSTER 相关性簇帽：三态回显 / 显式关闭 / 越界拒绝', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.payload = {}
    state.impl = stubs()
    const api = await import('../api/index.js')
    for (const name of Object.keys(state.impl)) {
      if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
    }
  })
  afterEach(() => cleanup())

  it('C1 键缺失时回显出厂 40 / 0.70，保存载荷带上这两个键', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    expect(Number(inputByLabel('相关性簇帽').value)).toBe(40)
    expect(Number(inputByLabel('相关性阈值').value)).toBe(0.70)
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const body = await savedBody()
    expect(body.sizing.cluster_cap_pct).toBe(40)
    expect(body.sizing.cluster_rho).toBe(0.7)
  })

  it('C2 后端回显显式 0 ⇒ 面板必须显示 0（不许读回 40）', async () => {
    state.payload = { cluster_cap_pct: 0, cluster_rho: 0.8 }
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    // 这条是反证：把 signedNum 换成 num()（n>0 判据）⇒ 这里会读到 40，本用例必红。
    expect(Number(inputByLabel('相关性簇帽').value)).toBe(0)
    expect(Number(inputByLabel('相关性阈值').value)).toBe(0.8)
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const body = await savedBody()
    expect(body.sizing.cluster_cap_pct).toBe(0)
  })

  it('C3 ρ=0 与簇帽越界 ⇒ 拒绝提交并点名', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    await openFine()
    // ρ=0：每只票都会被判成"与谁都不同簇"，簇帽静默失效 —— 必须在面板就拦下。
    fireEvent.change(inputByLabel('相关性阈值'), { target: { value: '0' } })
    fireEvent.change(inputByLabel('相关性簇帽'), { target: { value: '120' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(0)
    const text = document.body.textContent || ''
    expect(text).toContain('相关性阈值(0~1)')
    expect(text).toContain('相关性簇帽(0=关，0~100)')
  })
})
