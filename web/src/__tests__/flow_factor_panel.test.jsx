// ── §FLOW-FACTOR（2026-10-25）净流入因子 + §SIZING-EVO 开关：设置页表单行为锁 ──
//
// 这一批把「净流入 dynamic 因子」的**全部可配档位**搬到了设置页（方案 §2.8 最后一行：
// 「前端 设置页 sizing 块 → flow_factor 配置表单」）。落码时最容易犯的错不是渲染不好看，
// 而是**配置被静默吞掉**，所以这里钉的是载荷与回显的等值关系：
//
//   · F1 默认语义：`enabled/sell_enabled` 键缺失必须显示为**开**（后端 nil=开，判据是
//     `!== false`）；`evo.enabled/auto_promote` 键缺失必须显示为**关**（后端 nil=关，
//     判据是 `=== true`）。两侧判据写反都是静默事故：前者把默认开显示成关（管理员以为要
//     "打开"才生效，点保存反而多写一笔），后者把默认关显示成开（一次保存就启动常驻进化）。
//   · F2 负阈值不被吞：档位阈值天生带负号（`mild_out_pct=-1` 表示"流出 1% 以上"）。旧
//     `mapNums` 的 `n > 0` 判据会把负数当缺失而回落默认——用户把 -1 改成 -2.5、保存、
//     再读回来又变 -1。这条用例就是为了钉死这个回归。
//   · F3 写侧整块提交：`flow_factor` 必须整块（含 sell_scales 与两侧 bands）进载荷，
//     否则稀疏 merge 只写一半，剩下的一半永远停在出厂值。
//   · F4 档位顺序写反必须**拒绝保存**：`flowBandOf` 从强到弱逐个比，顺序反了永远命中第一档，
//     净流入 5% 与净流出 5% 会拿到同一个乘子——这是"不报错但静默错档"的形态，只能在前端拦。
//
// English: behaviour locks for the net-inflow factor form on the settings page — default-on
// semantics for the factor switches vs default-off for the evolution switch, signed band
// thresholds surviving the round-trip, the whole flow_factor block being submitted, and
// reversed band ordering being rejected before it can silently collapse every band.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/react'
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

// 后端 GET /api/config/regime 契约载荷。F2 用**非默认**的负阈值（-2.5）——只有偏离默认值
// 才能区分"回显了配置"与"回落了出厂默认"。
// English: the payload uses a NON-default negative threshold (-2.5) so that "echoed the config"
// and "fell back to factory default" are distinguishable.
function regimePayload() {
  return {
    enhance: { market_state_enabled: true },
    sizing: {
      enabled: true,
      budget_mode: 'dynamic',
      mode: 'power_mean',
      flow_factor: {
        enabled: false, // 显式关：F1 的对照组（缺键才是开）
        sell_enabled: true,
        buy_5d_scales: { strong_in: 1.2, mild_in: 1.05, neutral: 1.0, mild_out: 0.85, strong_out: 0.7 },
        buy_5d_bands: { strong_in_pct: 4, mild_in_pct: 1, mild_out_pct: -2.5, strong_out_pct: -6 },
        buy_today_scales: { strong_in: 1.05, mild_in: 1.025, neutral: 1.0, mild_out: 0.95, strong_out: 0.9 },
        buy_today_bands: { strong_in_pct: 3, mild_in_pct: 1, mild_out_pct: -1, strong_out_pct: -3 },
        sell_scales: { strong_out: 0.8, mild_out: 0.9, today_strong_out: 0.95, today_mild_out: 0.975 },
        sell_bands: { strong_out_pct: -3, mild_out_pct: -1 },
      },
      evo: { enabled: true, auto_promote: false },
    },
    macro_gate: {},
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

function labelRow(text) {
  const el = [...document.querySelectorAll('span')].find((n) => (n.textContent || '').trim() === text)
  if (!el) throw new Error(`未找到标签「${text}」`)
  return el.closest('div')
}

function switchByLabel(text) {
  const sw = labelRow(text).querySelector('[role="switch"]')
  if (!sw) throw new Error(`标签「${text}」所在行没有开关`)
  return sw
}

function numberByLabel(text) {
  const input = labelRow(text).querySelector('input')
  if (!input) throw new Error(`标签「${text}」所在行没有数字输入`)
  return input
}

// openFine 展开「精细参数」折叠区（净流入档位全部收在里面，不展开查不到）。
async function openFine() {
  render(<RegimeConfigPanel />)
  await settle()
  fireEvent.click(btn('展开精细参数'))
  await settle()
}

describe('§FLOW-FACTOR 设置页：默认语义 / 负阈值回显 / 整块提交 / 档位顺序拦截', () => {
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

  it('F1 开关默认语义：因子缺键=开、显式 false=关；evo 缺键=关', async () => {
    await openFine()
    // 载荷里显式写了 enabled:false → 显示关（对照组）
    expect(switchByLabel('买入侧净流入因子').getAttribute('aria-checked')).toBe('false')
    expect(switchByLabel('卖出侧净流出加权').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('启用仓位进化引擎').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('达标候选自动晋升').getAttribute('aria-checked')).toBe('false')
  })

  it('F1b 因子键完全缺失 → 两侧都显示为开（nil=开，判据必须是 !== false）', async () => {
    const p = regimePayload()
    delete p.sizing.flow_factor
    delete p.sizing.evo
    state.impl = { ...stubs(), fetchRegimeConfig: () => p }
    await openFine()
    expect(switchByLabel('买入侧净流入因子').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('卖出侧净流出加权').getAttribute('aria-checked')).toBe('true')
    // evo 相反：缺键=关
    expect(switchByLabel('启用仓位进化引擎').getAttribute('aria-checked')).toBe('false')
  })

  it('F2 负阈值回显不被吞：-2.5 读回来还是 -2.5（不是默认的 -1）', async () => {
    await openFine()
    expect(Number(numberByLabel('5日档位阈值 中流出').value)).toBe(-2.5)
    expect(Number(numberByLabel('5日档位阈值 强流出').value)).toBe(-6)
    expect(Number(numberByLabel('5日档位阈值 强流入').value)).toBe(4)
  })

  it('F3 写侧整块提交：负阈值与 sell_scales 一并进载荷', async () => {
    await openFine()
    fireEvent.change(numberByLabel('5日档位阈值 中流出'), { target: { value: '-3.5' } })
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(1)
    const body = api.setRegimeConfig.mock.calls[0][0]
    const ff = body.sizing.flow_factor
    expect(ff.buy_5d_bands.mild_out_pct).toBe(-3.5) // 改的键生效
    expect(ff.buy_5d_bands.strong_out_pct).toBe(-6) // 未改的键保持原值
    expect(ff.sell_scales.mild_out).toBe(0.9)
    expect(ff.sell_bands.strong_out_pct).toBe(-3)
    expect(typeof ff.buy_5d_bands.mild_out_pct).toBe('number') // 稀疏 merge 不收字符串
    expect(body.sizing.evo.enabled).toBe(true)
  })

  it('F4 档位顺序写反 → 拒绝保存、零 POST、点名「须严格递减」', async () => {
    await openFine()
    // 强流入 4 → -10：顺序被破坏（-10 不再大于中流入 1）
    fireEvent.change(numberByLabel('5日档位阈值 强流入'), { target: { value: '-10' } })
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(0)
    expect([...document.body.querySelectorAll('*')].some((n) => /须严格递减/.test(n.textContent || ''))).toBe(true)
  })

  // F5 R9 弃权率告警阈值：负数是**合法**语义（=关闭告警），必须原样回显、原样提交。
  // 这条钉的是 num() 的 `n > 0` 判据——用它读这个字段，用户填的 -1 会被静默改成 50（默认开）。
  // English: the R9 threshold accepts negatives (= disable); num()'s `n > 0` gate would silently
  // rewrite an explicit -1 into the default 50.
  it('F5 R9 阈值负数回显与提交都不被吞（-1 保持 -1，不回落 50）', async () => {
    const p = regimePayload()
    p.sizing.flow_factor.abstain_alert_pct = -1
    state.impl = { ...stubs(), fetchRegimeConfig: () => p }
    await openFine()
    expect(Number(numberByLabel('弃权率告警阈值（%）').value)).toBe(-1)
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(1)
    expect(api.setRegimeConfig.mock.calls[0][0].sizing.flow_factor.abstain_alert_pct).toBe(-1)
  })

  // F6 缺键 → 显示出厂 50（不是 0：0 在后端语义里也等于 50，但显示 0 会让运维以为"关掉了"）。
  // English: a missing key shows the factory 50 rather than 0.
  it('F6 R9 阈值缺键 → 回显出厂的 50', async () => {
    await openFine()
    expect(Number(numberByLabel('弃权率告警阈值（%）').value)).toBe(50)
  })
})
