// ── §N6-RISK（2026-10-21）风控与仓位面板行为锁 ──
//
// 这一批开关（市场状态机 / 动态仓位 / 卖出加权 / Red 应急阀 / 广度缺失降权）落码时是
// **先在引擎里生效、后在页面上可见**的：owner 裁决要求「全部可配 + 前端有开关」，而
// RegimeConfigPanel 是本批新写的组件，此前一条断言都没有。§DYN-SIZE 的开关一旦能被人
// 一键打开，页面就成了资金安全面，所以钉的是「写出去的载荷」而不是「渲染好不好看」：
//   · R1 读侧回显：GET 恰好一次，开关态与数字框值逐个等值（不是"有内容"）；
//   · R1b 缺载荷不显示 0：后端返回空对象时数字必须回落到出厂默认（阈值显示 0 会让管理员
//     以为风控被关掉，而引擎其实按 0.90/0.30 在跑——两侧读数不一致是这套面板唯一的存在理由）；
//   · R2 写侧键集逐键等值 + 数值必须是 number 不是字符串（稀疏 merge 会把类型不符整笔挡成 400）；
//   · R3 读取失败 → 红条 + 连保存按钮都不渲染 + 零 POST（拿一份不完整的载荷去 POST 就是 §CFGSMASH 的抹平事故）；
//   · R4 数字缺失/越界拒绝提交并点名缺失项；
//   · R5 默认语义：三个「默认开」的拦截位（情绪参与/N 形/动量）在键缺失时必须显示为开
//     （用的是 `!== false`，写成 `=== true` 就把默认开变成默认关——静默放松风控且页面看不出来）；
//   · R6 共用闸的文案锁：block_n_shape/block_momentum 同时被 Red 风险档与交割日事件读，
//     面板标签必须两处都点名（只写「Red 拦截」＝管理员一次点击关掉两道闸还以为只关了一道）；
//   · R7 保存成功后 dirty 标记清除（基线推进），否则用户永远以为还有未保存改动。
//
// English: behavior locks for the new §N6-RISK regime panel — read echo, exact write payload,
// load-failure save block, invalid-number refusal, default-on semantics for the three gate
// switches, the shared-gate label disclosure, and baseline advance after a successful save.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
// §DET-TIME：等 UI 一律用 settle()（排空微任务），不用 waitFor/findBy——邻居负载下真实时钟轮询必偶发红。
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

// 后端 GET /api/config/regime 契约载荷（键名逐键对齐 internal/config/config.go 的 JSON tag：
// enhance.market_state_enabled / sizing.{enabled,sell_boost_level,tier_scales,breadth_missing_scale}
// / macro_gate.{yellow_confidence,red_confidence,red_block_new_buy,emotion_enabled,block_n_shape,block_momentum}）
function regimePayload() {
  return {
    enhance: { market_state_enabled: true },
    sizing: {
      enabled: true,
      sell_boost_level: 'L2',
      breadth_missing_scale: 0.9,
      tier_scales: { Yellow: 0.6, Red: 0.3 },
    },
    macro_gate: {
      yellow_confidence: 0.9,
      red_confidence: 0.92,
      red_block_new_buy: false,
      emotion_enabled: true,
      block_n_shape: true,
      block_momentum: true,
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

const ERR500 = (msg) => Object.assign(new Error(msg), { status: 500 })

// 按文案取按钮：TDesign 的 disabled Button 会渲染成 <div type="button">（无 button role），
// 按 role 查询在禁用态会查不到而假失败——同族 §P2-H / §N-4 已踩过，沿用 DOM 取法。
function btn(text) {
  const el = [...document.querySelectorAll('button,div[type="button"]')].find(
    (b) => b.getAttribute('role') !== 'switch' && (b.textContent || '').trim() === text,
  )
  if (!el) throw new Error(`未找到「${text}」按钮`)
  return el
}
function btnDisabled(text) {
  const el = btn(text)
  return el.disabled === true || el.hasAttribute('disabled') || el.classList.contains('t-is-disabled')
}

// 按标签文案取同行的开关 / 数字输入：面板十来个控件全靠顺序取会随增删行漂移，
// 且"取错行还恰好值一样"是这类锁最典型的假绿来源，因此一律按 label 定位。
function labelRow(text) {
  const el = [...document.querySelectorAll('span')].find((n) => (n.textContent || '').trim() === text)
  if (!el) throw new Error(`未找到标签「${text}」`)
  return el.closest('div')
}
function switchByLabel(text) {
  const row = labelRow(text)
  const sw = row.querySelector('[role="switch"]')
  if (!sw) throw new Error(`标签「${text}」所在行没有开关`)
  return sw
}
function numberByLabel(text) {
  const row = labelRow(text)
  const input = row.querySelector('input')
  if (!input) throw new Error(`标签「${text}」所在行没有数字输入`)
  return input
}

describe('§N6-RISK 风控与仓位面板：读回显 / 写载荷 / 读失败禁保存 / 缺失拒绝 / 默认语义 / 共用闸文案', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.impl = stubs()
    // 模块级 vi.fn 在整个文件内是同一个实例，不清就把上一例的调用记到这一例头上：
    // R3/R4 两条「零调用」断言会因此恒红（组件其实没发请求）。同族口径照 §P2-H 补上。
    const api = await import('../api/index.js')
    for (const name of Object.keys(state.impl)) {
      if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
    }
  })
  afterEach(() => cleanup())

  // R1 读侧逐项回显：三个开关的 aria-checked、五个数字框的 value、下拉档位，全部等值断言。
  it('R1 挂载 → fetchRegimeConfig 调用一次，开关态与数字值逐项等值回显', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    const api = await import('../api/index.js')
    expect(api.fetchRegimeConfig.mock.calls.length).toBe(1)
    expect(switchByLabel('市场状态机（大盘走向）').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('动态仓位（买入缩放）').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('Red 禁止新开仓（应急）').getAttribute('aria-checked')).toBe('false')
    expect(switchByLabel('情绪相位参与风险档').getAttribute('aria-checked')).toBe('true')
    expect(numberByLabel('Yellow 置信度门槛').value).toBe('0.9')
    expect(numberByLabel('Red 置信度门槛').value).toBe('0.92')
    expect(numberByLabel('Yellow 金额权重').value).toBe('0.6')
    expect(numberByLabel('Red 金额权重').value).toBe('0.3')
    expect(numberByLabel('广度缺失降权').value).toBe('0.9')
    const sel = document.querySelector('select')
    expect(sel.value).toBe('L2')
  })

  // R1b 空载荷回落出厂默认：数字框一个都不许显示 0（0 会被读成"这档风控关了"）。
  it('R1b GET 返回空对象 → 数字回落到出厂默认而非 0', async () => {
    state.impl.fetchRegimeConfig = () => ({})
    render(<RegimeConfigPanel />)
    await settle()
    expect(numberByLabel('Yellow 置信度门槛').value).toBe('0.9')
    expect(numberByLabel('Red 置信度门槛').value).toBe('0.92')
    expect(numberByLabel('Yellow 金额权重').value).toBe('0.6')
    expect(numberByLabel('Red 金额权重').value).toBe('0.3')
    expect(numberByLabel('广度缺失降权').value).toBe('0.9')
    const values = [...document.querySelectorAll('input')].map((i) => i.value)
    expect(values, '§N6-RISK：缺失数字不得折叠成 0 显示').not.toContain('0')
  })

  // R2 写侧载荷逐键等值：只提交本面板负责的三段、数值是 number、档位是大写串。
  // 这一条同时挡两种坏法：多带键（稀疏 merge 会把没打算改的段一起覆盖）与类型漂移（字符串 → 后端 400）。
  it('R2 保存 → 请求体三段键集与后端契约逐键等值且类型正确', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(1)
    const body = api.setRegimeConfig.mock.calls[0][0]
    expect(Object.keys(body).sort()).toEqual(['enhance', 'macro_gate', 'sizing'])
    expect(Object.keys(body.enhance)).toEqual(['market_state_enabled'])
    expect(Object.keys(body.sizing).sort()).toEqual(['breadth_missing_scale', 'enabled', 'sell_boost_level', 'tier_scales'])
    expect(Object.keys(body.sizing.tier_scales).sort()).toEqual(['Red', 'Yellow'])
    expect(Object.keys(body.macro_gate).sort()).toEqual(
      ['block_momentum', 'block_n_shape', 'emotion_enabled', 'red_block_new_buy', 'red_confidence', 'yellow_confidence'].sort(),
    )
    expect(body.sizing.tier_scales.Red).toBe(0.3)
    expect(typeof body.sizing.tier_scales.Red).toBe('number')
    expect(body.macro_gate.yellow_confidence).toBe(0.9)
    expect(body.sizing.sell_boost_level).toBe('L2')
    expect(body.enhance.market_state_enabled).toBe(true)
    expect(body.macro_gate.red_block_new_buy).toBe(false)
  })

  // R3 读取失败 → 红条 + 零 POST。形态如实钉成「整表单不渲染、连保存按钮都不存在」而不是
  // 「按钮禁用」：组件在 error 分支把 form 置 null，`{form && ...}` 那段（含保存按钮）根本
  // 不挂载——比 disabled 更强，但断言写反会去查一个不存在的节点而假红（同族「死支别写断言」）。
  it('R3 GET 500 → 红条 + 无保存入口 + setRegimeConfig 零调用', async () => {
    state.impl.fetchRegimeConfig = () => { throw ERR500('regime 拉取失败') }
    render(<RegimeConfigPanel />)
    await settle()
    expect(screen.getByText(/读取失败，禁止保存/)).toBeInTheDocument()
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect([...document.querySelectorAll('button,div[type="button"]')]
      .some((b) => (b.textContent || '').trim() === '保存风控配置'), '§N6-RISK：error 态不得出现保存入口').toBe(false)
    // 重载按钮必须在（否则用户只能刷新整页，读失败被做成死路）
    expect([...document.querySelectorAll('button,div[type="button"]')]
      .some((b) => (b.textContent || '').trim() === '重载')).toBe(true)
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length, '§N6-RISK：error 态绝不发起保存').toBe(0)
    // 整表单不渲染（form 为 null）＝没有可被误提交的半成品
    expect(document.querySelector('select')).toBeNull()
  })

  // R4 数字越界/缺失拒绝提交并点名：清空 Red 金额权重后点保存必须零请求。
  it('R4 Red 金额权重清空 → 拒绝保存、点名缺失项、零请求', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    fireEvent.change(numberByLabel('Red 金额权重'), { target: { value: '' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length, '§N6-RISK：缺失权重不得折叠成 0 提交').toBe(0)
    expect([...document.body.querySelectorAll('*')].some((n) => /Red 金额权重/.test(n.textContent || ''))).toBe(true)
  })

  // R5 默认开的三把闸：键缺失时必须渲染为「开」。写成 === true 会静默变成默认关（放松风控），
  // 而页面还显示关——这是"降级了但报成功"那一族的页面版。
  it('R5 macro_gate 缺 block_n_shape/block_momentum/emotion_enabled → 三把闸默认开', async () => {
    const p = regimePayload()
    delete p.macro_gate.block_n_shape
    delete p.macro_gate.block_momentum
    delete p.macro_gate.emotion_enabled
    state.impl.fetchRegimeConfig = () => p
    render(<RegimeConfigPanel />)
    await settle()
    expect(switchByLabel('拦截 N 形超短（Red 档 + 交割日）').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('拦截动量观察（Red 档 + 交割日）').getAttribute('aria-checked')).toBe('true')
    expect(switchByLabel('情绪相位参与风险档').getAttribute('aria-checked')).toBe('true')
  })

  // R5b 显式 false 必须真的是关：与"缺键默认开"分开钉，否则 `!== false` 写成 `!== undefined` 也能过 R5。
  it('R5b macro_gate.block_n_shape=false → 开关渲染为关且保存回传 false', async () => {
    const p = regimePayload()
    p.macro_gate.block_n_shape = false
    state.impl.fetchRegimeConfig = () => p
    render(<RegimeConfigPanel />)
    await settle()
    expect(switchByLabel('拦截 N 形超短（Red 档 + 交割日）').getAttribute('aria-checked')).toBe('false')
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls[0][0].macro_gate.block_n_shape).toBe(false)
  })

  // R6 共用闸文案：这一把键同时被 Red 风险档与交割日事件读，标签只写「Red 拦截」＝一次点击
  // 关掉两道闸而界面只显示一道（现网唯一入口在本面板，故按文案钉住）。
  it('R6 N 形/动量拦截行必须点名「交割日」共用职责', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    const nRow = labelRow('拦截 N 形超短（Red 档 + 交割日）')
    expect(nRow.textContent).toContain('交割日')
    expect(nRow.textContent).toContain('同一开关管两件事')
    expect(labelRow('拦截动量观察（Red 档 + 交割日）').textContent).toContain('交割日')
  })

  // R7 卖出加权档位改动要进载荷；保存成功后 dirty 标记清除（基线推进）。
  it('R7 改档位→出现未保存标记，保存成功→标记清除且档位回传 L3', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    fireEvent.change(document.querySelector('select'), { target: { value: 'L3' } })
    await settle()
    expect(screen.getByText(/有未保存修改/)).toBeInTheDocument()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls[0][0].sizing.sell_boost_level).toBe('L3')
    expect(screen.queryByText(/有未保存修改/)).toBeNull()
  })

  // R7b 保存失败不得推进基线：否则红条消失、用户以为已保存，而引擎吃的还是旧值。
  it('R7b POST 失败 → 保留未保存标记（基线不推进）', async () => {
    state.impl.setRegimeConfig = () => { throw ERR500('落库失败') }
    render(<RegimeConfigPanel />)
    await settle()
    fireEvent.change(numberByLabel('Red 金额权重'), { target: { value: '0.2' } })
    await settle()
    expect(screen.getByText(/有未保存修改/)).toBeInTheDocument()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    expect(screen.getByText(/有未保存修改/).textContent).toBeTruthy()
    expect([...document.body.querySelectorAll('*')].some((n) => /保存失败/.test(n.textContent || ''))).toBe(true)
  })
})
