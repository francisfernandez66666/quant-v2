// ── §P2-H（2026-10-06 修复批 波 6）全局 D1 面板行为锁 ──
//
// 缺陷本体：后端 GET/POST /api/config/d1 早已具备「稀疏 merge + 写前快照 + 字段级审计 +
// effective_source 回显」（§0929CFG-D1），而前端**一个调用点都没有**——本批开工前
// `grep -rn 'config/d1' web/src` 零命中。后果不是"少了个页面"这么轻：
//   · D1 软加成权重/门槛与事件规则表**直接参与战法打分**，却只能在页面外改（脚本/翻 auth.json）；
//   · 后端为自证接线专门加的 effective_source 没人显示，"页面写的值就是引擎吃的值"无法核对；
//   · 没有页面也就没有"读失败就别保存"的闸，而 §CFGSMASH/§0929CFG-D1 两次事故都正是
//     "拿一份不完整的载荷去 POST"造成的抹平。
// 本文件把面板钉成四件事：读侧全量回显（含生效账本）、写侧只提交三个已知键且规则行不夹带
// 本地字段、读失败禁保存（零 POST）、数字/文本缺失不得折叠成 0 提交、后端 ignored_keys 必须可见。
//
// English: §P2-H behavior locks for the new global D1 panel — the backend channel existed with
// sparse merge / snapshot / audit / effective_source, but had zero frontend callers. These cases
// pin the panel's read echo, exact write payload, load-failure save block, missing-field refusal,
// and visible ignored_keys.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
// §DET-TIME：等 UI 一律用 settle()（排空微任务），不用 waitFor/findBy——邻居负载下真实时钟轮询必偶发红。
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

// 后端 GET 契约载荷（字段名逐键对齐 internal/config/config.go 的 D1Config/D1Rule JSON tag）
function d1Payload() {
  return {
    rules: [
      { direction: '利好', score: 12, blocked: false },
      { direction: '立案调查', score: 30, blocked: true },
    ],
    boost_weight: 0.15,
    boost_threshold: 8,
    effective_source: 'global',
  }
}

function stubs() {
  return {
    fetchD1Config: () => d1Payload(),
    setD1Config: () => ({ status: 'ok', ignored_keys: [], effective_source: 'global', rules_count: 2 }),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const s = {}
  for (const name of Object.keys(stubs())) {
    // 同步包装：useState 初始化器里的同步调用不能拿到 Promise（本面板只有异步读取，仍按同族写法保持一致）
    s[name] = vi.fn((...args) => state.impl[name](...args))
  }
  return { ...actual, ...s }
})

import D1ConfigPanel from '../components/D1ConfigPanel.jsx'

const ERR500 = (msg) => Object.assign(new Error(msg), { status: 500 })

// 按文案取按钮：TDesign 的 disabled Button 会渲染成 <div type="button">（无 button role），
// 按 role 查询在禁用态会查不到而假失败——同族 §N-4 已踩过，这里沿用 DOM 取法。
function btn(text) {
  const el = [...document.querySelectorAll('button,div[type="button"]')].find((b) => (b.textContent || '').trim() === text)
  if (!el) throw new Error(`未找到「${text}」按钮`)
  return el
}
function btnDisabled(text) {
  const el = btn(text)
  return el.disabled === true || el.hasAttribute('disabled') || el.classList.contains('t-is-disabled')
}

describe('§P2-H 全局 D1 面板：读回显 / 写载荷 / 读失败禁保存 / 缺失拒绝 / ignored_keys 可见', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.impl = stubs()
    // 模块级 vi.fn 在**整个文件内是同一个实例**，不清就把上一例的调用记到这一例头上：
    // 本文件三条「零调用」断言（H3/H4）会因此恒红，而红的还是"看起来最像缺陷"的那种红
    // （组件其实没发请求）。同族 §N-4 用例已在 beforeEach 里 mockClear，照同一口径补上。
    const api = await import('../api/index.js')
    for (const name of Object.keys(state.impl)) {
      if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
    }
  })
  afterEach(() => cleanup())

  // H1 读侧全量回显：GET 恰好一次、规则条数与生效账本都渲染出来（effective_source 是 §0929CFG-D1
  // 专门加的自证字段，面板不显示它＝这条通道依旧无法核对，等于没修）。
  it('H1 挂载 → fetchD1Config 调用一次且规则条数/生效账本均回显', async () => {
    render(<D1ConfigPanel />)
    await settle()
    const api = await import('../api/index.js')
    expect(api.fetchD1Config.mock.calls.length).toBe(1)
    expect(screen.getByText('事件规则（2 条）')).toBeInTheDocument()
    expect(screen.getByText('全局')).toBeInTheDocument()
    // 两条规则的方向都要出现在输入框里（不是只数条数——条数对得上而内容丢失也是这类面板的典型坏法）
    const inputs = [...document.querySelectorAll('input')].map((i) => i.value)
    expect(inputs).toContain('利好')
    expect(inputs).toContain('立案调查')
  })

  // H1b 生效账本换值必须换文案：account 与 global 两种读数各自渲染（等值而不是"有内容"）。
  // 只测 global 的话，把 account 分支写错（例如恒显示"全局"）照样绿——而"页面吃的是账号覆盖"
  // 正是这套回显唯一要解决的问题。
  it('H1b effective_source=account → 渲染账号级覆盖提示而非「全局」', async () => {
    state.impl.fetchD1Config = () => ({ ...d1Payload(), effective_source: 'account' })
    render(<D1ConfigPanel />)
    await settle()
    expect(screen.getByText(/账号级覆盖/)).toBeInTheDocument()
    expect(screen.queryByText('全局')).toBeNull()
  })

  // H2 写侧载荷精确对账：只有三个已知键，规则行只有 direction/score/blocked 三键
  // （本地 React key 混进请求体＝前端契约漂移，后端会回 ignored_keys；这里断言它压根不该出现）。
  it('H2 保存 → 请求体键集与后端契约逐键等值，规则行不夹带本地字段', async () => {
    render(<D1ConfigPanel />)
    await settle()
    fireEvent.click(btn('保存 D1 配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setD1Config.mock.calls.length).toBe(1)
    const payload = api.setD1Config.mock.calls[0][0]
    expect(Object.keys(payload).sort()).toEqual(['boost_threshold', 'boost_weight', 'rules'])
    expect(payload.boost_weight).toBe(0.15)
    expect(payload.boost_threshold).toBe(8)
    expect(payload.rules.map((r) => Object.keys(r).sort())).toEqual([['blocked', 'direction', 'score'], ['blocked', 'direction', 'score']])
    expect(payload.rules[1]).toEqual({ direction: '立案调查', score: 30, blocked: true })
  })

  // H3 读取失败 → 红条「禁止保存」+ 按钮禁用 + 零 POST（旧形态是静默空表，点保存等于把规则清空）。
  it('H3 GET 500 → 红条 + 保存禁用 + setD1Config 零调用', async () => {
    state.impl.fetchD1Config = () => { throw ERR500('d1 拉取失败') }
    render(<D1ConfigPanel />)
    await settle()
    expect(screen.getByText(/D1 配置读取失败，禁止保存/)).toBeInTheDocument()
    expect(btnDisabled('保存 D1 配置')).toBe(true)
    // 再点一次（禁用态仍可能被脚本/回车触发）：绝不发请求
    fireEvent.click(btn('保存 D1 配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setD1Config.mock.calls.length, '§P2-H：error 态绝不发起保存').toBe(0)
  })

  // H4 缺失 ≠ 0：加成权重被清空后保存必须被拦下（写成 0 提交＝把加成关掉，且后端"显式传键一定更新"）
  it('H4 加成权重清空 → 拒绝保存并点名缺失字段，不发请求', async () => {
    render(<D1ConfigPanel />)
    await settle()
    // 清空第一个数字输入（软加成权重）
    const numInputs = [...document.querySelectorAll('input')].filter((i) => i.value === '0.15')
    expect(numInputs.length).toBe(1)
    fireEvent.change(numInputs[0], { target: { value: '' } })
    await settle()
    fireEvent.click(btn('保存 D1 配置'))
    await settle()
    const api = await import('../api/index.js')
    expect(api.setD1Config.mock.calls.length, '§P2-H：缺失字段必须被必填校验拦下').toBe(0)
  })

  // H5 契约漂移可见：后端回报 ignored_keys 时不能只 toast 成功——这正是 §0929CFG-D1 用来抓
  // "发错键名把配置抹平"的那只眼睛，咽掉它等于把自证字段关掉。
  it('H5 后端回 ignored_keys → 保存提示点名未知键（不静默报成功）', async () => {
    state.impl.setD1Config = () => ({ status: 'ok', ignored_keys: ['boost Wieght'], effective_source: 'global', rules_count: 2 })
    render(<D1ConfigPanel />)
    await settle()
    fireEvent.click(btn('保存 D1 配置'))
    await settle()
    // toast 文案由 MessagePlugin 渲染到 body，这里断言"未知键"这条警示确实走到（而不是成功分支）
    expect([...document.body.querySelectorAll('*')].some((n) => (n.textContent || '').includes('契约漂移'))).toBe(true)
  })

  // H6 反向确认（本文件自己的反证位）：正常路径保存成功必须写明规则条数（rules_count 回显），
  // 且成功后 dirty 标记消失（基线推进）——不推进的话用户会一直以为还有未保存改动。
  it('H6 保存成功 → 回显 rules_count 且未保存标记清除', async () => {
    render(<D1ConfigPanel />)
    await settle()
    // 先改一个值制造 dirty
    const w = [...document.querySelectorAll('input')].find((i) => i.value === '0.15')
    fireEvent.change(w, { target: { value: '0.2' } })
    await settle()
    expect(screen.getByText(/有未保存修改/)).toBeInTheDocument()
    fireEvent.click(btn('保存 D1 配置'))
    await settle()
    expect([...document.body.querySelectorAll('*')].some((n) => /规则 2 条/.test(n.textContent || ''))).toBe(true)
    expect(screen.queryByText(/有未保存修改/)).toBeNull()
  })
})
