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
    const sel = document.querySelector('[data-testid="sell-boost"]')
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
    expect(Object.keys(body).sort()).toEqual(['concept', 'enhance', 'macro_gate', 'sizing'])
    expect(Object.keys(body.enhance).sort()).toEqual(['market_state_enabled', 'risk_pool_shadow', 'tech_filter'])
    expect(Object.keys(body.concept).sort()).toEqual(['enabled', 'max_staleness_hours', 'min_coverage_pct', 'shadow'])
    // §SIZING-FINE（2026-10-22）：面板现在负责 sizing 的**全量**键（精细参数折叠区也在内），
    // 逐键等值锁住键集——多一个未定义键会被后端稀疏 merge 写进配置，少一个键等于某组精细参数失踪。
    expect(Object.keys(body.sizing).sort()).toEqual([
      'bear_sl_tighten_pt', 'breadth_missing_scale', 'budget_mode', 'cap', 'emotion_scales', 'enabled', 'floor',
      'group_floors', 'holiday_block_new', 'holiday_scales', 'index_scale_thresholds', 'kelly_lambda', 'kelly_min_prob',
      'llm_bearish_scale', 'macro_event_day_scale', 'macro_lead_curve', 'macro_lead_days',
      'macro_window_scales', 'max_drawdown_pct', 'max_total_position_pct', 'mode', 'power_p', 'sell_boost_level', 'shadow',
      'sl_offset_cap', 'sl_offset_min_index_drop', 'sl_offset_per_pct', 'sl_offset_risk_budget_keep',
      'state_scales', 'stoploss_guard_count', 'stoploss_guard_window_days', 'tier_scales', 'tp_offset_cap',
      'tp_offset_per_pct', 'vol_clamp_hi', 'vol_clamp_lo', 'vol_target_pct',
    ].sort())
    // §DYN-GLOBAL（2026-10-24）：缺键必须落在**新**出厂默认 dynamic。此前锁的是 legacy，
    // 而 legacy 只剩账号级退路——面板把缺键写成 legacy，等于管理员点一次"保存"就把账号
    // 从动态预算踢回旧闸门，且页面上看不出自己刚做了这件事。
    expect(body.sizing.budget_mode).toBe('dynamic')
    expect(body.sizing.mode).toBe('product')
    expect(body.sizing.power_p).toBe(0.5)
    expect(body.sizing.group_floors).toEqual({ market: 0.45, stock: 0.35 })
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
    // 精细参数缺键时必须按出厂默认落 payload（不是 undefined/0）：后端 WithDefaults 同样兜底，
    // 但页面若把缺键写成 0，稀疏 merge 会真的把 0 写进配置，等于把闸门关掉。
    // §DYN-GLOBAL：影子的出厂默认改成关（缩放真下发），缺键必须落 false——落 true 等于
    // 一次保存就把动态缩放按回"只留痕"。
    expect(body.sizing.shadow).toBe(false)
    expect(body.sizing.floor).toBe(0.15)
    expect(typeof body.sizing.floor).toBe('number')
    expect(body.sizing.emotion_scales['退潮']).toBe(0.5)
    expect(body.sizing.macro_lead_curve).toEqual([0.9, 0.85, 0.8, 0.7, 0.5])
    expect(body.sizing.sl_offset_risk_budget_keep).toBe(true)
    expect(body.sizing.holiday_block_new).toBe(false)
  })

  // R2b §DYN-SIZE-v3：预算/合成口径与 p=0/分组 floor 必须原样写出（p=0=几何平均，不能被折叠成 0.5）。
  it('R2b 写侧：dynamic/power_mean/p=0/分组 floor 逐键回传', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    fireEvent.change(document.querySelector('[data-testid="budget-mode"]'), { target: { value: 'dynamic' } })
    fireEvent.change(document.querySelector('[data-testid="sizing-mode"]'), { target: { value: 'power_mean' } })
    fireEvent.change(numberByLabel('幂平均指数 p'), { target: { value: '0' } })
    const floorInputs = labelRow('分组 floor').querySelectorAll('input')
    fireEvent.change(floorInputs[0], { target: { value: '0.5' } })
    fireEvent.change(floorInputs[1], { target: { value: '0.4' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(1)
    const sz = api.setRegimeConfig.mock.calls[0][0].sizing
    expect(sz.budget_mode).toBe('dynamic')
    expect(sz.mode).toBe('power_mean')
    expect(sz.power_p).toBe(0)
    expect(sz.group_floors).toEqual({ market: 0.5, stock: 0.4 })
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
    fireEvent.change(document.querySelector('[data-testid="sell-boost"]'), { target: { value: 'L3' } })
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

  // ── §SIZING-FINE（2026-10-22）精细参数折叠区 ──
  // 这一批把 SizingConfig 的 40 余个精细参数接到页面。锁的重点不是"渲染了行"，而是：
  // 读侧显示后端**实际生效值**（不是写死默认）、写侧逐键进 payload、越界/曲线长度不符拒绝保存。

  function expandFine() {
    fireEvent.click(btn('展开精细参数'))
  }

  it('R8 精细参数读侧：后端非默认值逐项回显（含曲线文本与三个开关）', async () => {
    const p = regimePayload()
    Object.assign(p.sizing, {
      floor: 0.2, cap: 1.1, kelly_lambda: 0.4, kelly_min_prob: 0.55,
      vol_target_pct: 2.5, vol_clamp_lo: 0.6, vol_clamp_hi: 1.4,
      emotion_scales: { 退潮: 0.45, 背离: 0.65, 高潮: 0.75, 发酵: 1.0, 启动: 1.0 },
      state_scales: { bull: 1.0, range: 0.65, bear: 0.35 },
      macro_window_scales: { high: 0.45, medium: 0.75, low: 1.0 },
      macro_lead_days: 3, macro_lead_curve: [0.8, 0.6, 0.4], macro_event_day_scale: 0.25,
      holiday_scales: { weekend: 0.8, small1: 0.65, small2: 0.8, long1: 0.35, long2: 0.55, long3: 0.7 },
      holiday_block_new: true,
      index_scale_thresholds: { '-1': 1.0, '-2': 0.6, deep: 0.3 },
      llm_bearish_scale: 0.4, max_total_position_pct: 0.75, max_drawdown_pct: 0.08,
      stoploss_guard_count: 3, stoploss_guard_window_days: 2,
      sl_offset_per_pct: 2.5, sl_offset_cap: 3.5, tp_offset_per_pct: 2.5, tp_offset_cap: 4.5,
      sl_offset_min_index_drop: 0.8, bear_sl_tighten_pt: 1.2,
      sl_offset_risk_budget_keep: false,
      shadow: false,
    })
    state.impl.fetchRegimeConfig = () => p
    render(<RegimeConfigPanel />)
    await settle()
    expandFine()
    await settle()
    expect(switchByLabel('影子模式（只留痕不下发）').getAttribute('aria-checked')).toBe('false')
    expect(numberByLabel('仓位缩放地板').value).toBe('0.2')
    expect(numberByLabel('仓位缩放上限').value).toBe('1.1')
    expect(numberByLabel('Kelly 系数').value).toBe('0.4')
    expect(numberByLabel('Kelly 最小优势概率').value).toBe('0.55')
    expect(numberByLabel('波动率目标').value).toBe('2.5')
    expect(numberByLabel('波动率乘子下限').value).toBe('0.6')
    expect(numberByLabel('波动率乘子上限').value).toBe('1.4')
    expect(numberByLabel('情绪「退潮」权重').value).toBe('0.45')
    expect(numberByLabel('状态 bear 权重').value).toBe('0.35')
    expect(numberByLabel('宏观影响 high 权重').value).toBe('0.45')
    expect(numberByLabel('事件前瞻交易日数').value).toBe('3')
    expect(numberByLabel('前瞻曲线（逗号分隔）').value).toBe('0.80, 0.60, 0.40')
    expect(numberByLabel('事件当日缩放').value).toBe('0.25')
    expect(numberByLabel('节前权重 long1').value).toBe('0.35')
    expect(switchByLabel('长假前最后一日禁新开仓').getAttribute('aria-checked')).toBe('true')
    expect(numberByLabel('大盘档位权重 deep').value).toBe('0.3')
    expect(numberByLabel('LLM 利空缩放').value).toBe('0.4')
    expect(numberByLabel('总仓位上限').value).toBe('0.75')
    expect(numberByLabel('权益回撤熔断比例').value).toBe('0.08')
    expect(numberByLabel('连续止损熔断次数').value).toBe('3')
    expect(numberByLabel('连续止损回看窗').value).toBe('2')
    expect(numberByLabel('止损放宽系数').value).toBe('2.5')
    expect(numberByLabel('止损放宽上限').value).toBe('3.5')
    expect(numberByLabel('止盈上浮系数').value).toBe('2.5')
    expect(numberByLabel('止盈上浮上限').value).toBe('4.5')
    expect(numberByLabel('止损放宽触发跌幅').value).toBe('0.8')
    expect(numberByLabel('趋势破位止损收紧').value).toBe('1.2')
    expect(switchByLabel('放宽止损同比例缩仓').getAttribute('aria-checked')).toBe('false')
  })

  it('R9 精细参数写侧：改动逐键进 payload 且类型正确（数值/整数/数组/布尔）', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    expandFine()
    await settle()
    fireEvent.change(numberByLabel('仓位缩放地板'), { target: { value: '0.25' } })
    fireEvent.change(numberByLabel('Kelly 系数'), { target: { value: '0.35' } })
    fireEvent.change(numberByLabel('情绪「退潮」权重'), { target: { value: '0.4' } })
    fireEvent.change(numberByLabel('事件前瞻交易日数'), { target: { value: '3' } })
    fireEvent.change(numberByLabel('前瞻曲线（逗号分隔）'), { target: { value: '0.8，0.6 0.4' } })
    fireEvent.change(numberByLabel('连续止损熔断次数'), { target: { value: '6' } })
    fireEvent.click(switchByLabel('长假前最后一日禁新开仓'))
    fireEvent.click(switchByLabel('放宽止损同比例缩仓'))
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length).toBe(1)
    const sz = api.setRegimeConfig.mock.calls[0][0].sizing
    expect(sz.floor).toBe(0.25)
    expect(sz.kelly_lambda).toBe(0.35)
    expect(sz.emotion_scales['退潮']).toBe(0.4)
    expect(sz.macro_lead_days).toBe(3)
    expect(sz.macro_lead_curve).toEqual([0.8, 0.6, 0.4])
    expect(sz.stoploss_guard_count).toBe(6)
    expect(Number.isInteger(sz.stoploss_guard_count)).toBe(true)
    expect(sz.holiday_block_new).toBe(true)
    expect(sz.sl_offset_risk_budget_keep).toBe(false)
    expect(sz.cap).toBe(1.15)
    expect(typeof sz.cap).toBe('number')
  })

  it('R10 前瞻曲线个数与交易日数不符 → 拒绝保存、点名、零请求', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    expandFine()
    await settle()
    // 默认曲线 5 项，把前瞻交易日数改成 4 → 曲线必须同步改成 4 项才允许保存
    fireEvent.change(numberByLabel('事件前瞻交易日数'), { target: { value: '4' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length, '§SIZING-FINE：曲线长度不符不得提交').toBe(0)
    expect(document.body.textContent).toContain('以下参数无效')
    expect(document.body.textContent).toContain('前瞻曲线')
  })

  it('R11 缩放地板 ≥ 上限 → 拒绝保存并点名', async () => {
    render(<RegimeConfigPanel />)
    await settle()
    expandFine()
    await settle()
    fireEvent.change(numberByLabel('仓位缩放地板'), { target: { value: '1.2' } })
    await settle()
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setRegimeConfig.mock.calls.length, '§SIZING-FINE：地板≥上限不得提交').toBe(0)
    expect(document.body.textContent).toContain('地板须小于上限')
  })

  it('R12 空 sizing 载荷 → 精细参数全部回落出厂默认（无 0/空输入），保存也不把 0 写进配置', async () => {
    state.impl.fetchRegimeConfig = () => ({ sizing: {} })
    render(<RegimeConfigPanel />)
    await settle()
    expandFine()
    await settle()
    // §DYN-GLOBAL（2026-10-24）三把交付键的出厂默认全部反转：enabled=开、budget_mode=dynamic、
    // shadow=关。空载荷必须按**新**默认回显——读成旧默认（关/legacy/影子）会让管理员以为动态
    // 仓位没生效而去"打开"它，实际引擎早就在按动态缩放下单。
    expect(switchByLabel('动态仓位（买入缩放）').getAttribute('aria-checked')).toBe('true')
    expect(document.querySelector('[data-testid="budget-mode"]').value).toBe('dynamic')
    expect(switchByLabel('影子模式（只留痕不下发）').getAttribute('aria-checked')).toBe('false')
    expect(numberByLabel('仓位缩放地板').value).toBe('0.15')
    expect(numberByLabel('情绪「退潮」权重').value).toBe('0.5')
    expect(numberByLabel('事件前瞻交易日数').value).toBe('5')
    expect(numberByLabel('前瞻曲线（逗号分隔）').value).toBe('0.90, 0.85, 0.80, 0.70, 0.50')
    expect(numberByLabel('节前权重 long1').value).toBe('0.4')
    expect(numberByLabel('大盘档位权重 deep').value).toBe('0.4')
    expect(numberByLabel('连续止损熔断次数').value).toBe('4')
    expect(switchByLabel('长假前最后一日禁新开仓').getAttribute('aria-checked')).toBe('false')
    expect(switchByLabel('放宽止损同比例缩仓').getAttribute('aria-checked')).toBe('true')
    // §C4 的 KDJ J 上限 0 是合法“关闭”语义，不属于“缺键折叠成 0”，单独排除。
    const kdjInput = numberByLabel('KDJ J 上限')
    const vals = [...document.querySelectorAll('input')]
      .filter((i) => i !== kdjInput)
      .map((i) => i.value)
    expect(vals.filter((v) => v === '0' || v === ''), '§SIZING-FINE：缺键不得折叠成 0/空').toEqual([])
    fireEvent.click(btn('保存风控配置'))
    await settle()
    const api = await import('../api/index.js')
    const sz = api.setRegimeConfig.mock.calls[0][0].sizing
    // §DYN-GLOBAL：空载荷点保存，写出去的必须是新出厂默认（dynamic + 非影子 + enabled 开），
    // 否则这次保存本身就把账号退回旧口径。
    expect(sz.shadow).toBe(false)
    expect(sz.budget_mode).toBe('dynamic')
    expect(sz.enabled).toBe(true)
    expect(sz.floor).toBe(0.15)
    expect(sz.emotion_scales['退潮']).toBe(0.5)
    expect(sz.macro_lead_curve).toEqual([0.9, 0.85, 0.8, 0.7, 0.5])
    expect(sz.sl_offset_risk_budget_keep).toBe(true)
  })
})
