// ── §W7-G（FIX_PLAN_20261006 波 7 / P2-新增，owner 裁决「标注口径 + 加可视标记，不删兜底」）──
//
// 被测缺陷：Positions 页头「实盘 总盈亏」在网关成交汇总（/api/qmt/trades 的 total_pnl＝已实现＋浮动）
// 还没回来时，用前端逐持仓自算的「现价−成本×数量」兜底。兜底本身有价值（首屏不空），
// 坏在**它没有身份**：页面上那个数看起来和权威数一模一样，而两条口径根本不是一回事
// ——本地值不含已实现盈亏，且现价缺位时按 0 计（偏低甚至为负）。
// 同页 §E1 的纸面腿写的却是相反纪律（null 就显示"—"、绝不本地重算），两种取向并存且只有一条被标注。
//
// 本文件四把尺子：
//   G1 兜底生效 ⇒ 挂出「本地兜底」标记，且页头数字确是本地自算值（证明标记跟着真分支走）；
//   G2 权威数到达 ⇒ 标记消失且数字换成网关值（真值胜出，不是恒挂标记）；
//   G3 无实盘数据 ⇒ 不出现该标记（纸面腿不许被这条改出第三种读数）；
//   G4 结构锁 ⇒ 兜底实现 + 派生 + 标记 + 口径文案四件都在位，且标记的悬停说明必须点名
//        「已实现/浮动」两条口径之差（标记沦为纯装饰＝本缺陷换个形态复活；
//        兜底被"统一口径"删掉＝owner 明令禁止的形态，见裁决「实装优先禁删除」）。
//
// 挂载形态照抄 h2_positions_balance.test.jsx：只替身 api 层，loadReal 由点击「实盘持仓」标签触发
// （页头那行读数在 Tabs 之外渲染，所以断言不依赖面板是否挂载）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

const { state } = vi.hoisted(() => ({
  state: {
    // 实盘持仓夹具：一条 100 股、成本 10、现价 12 ⇒ 本地兜底应算出 (12-10)*100 = 200
    positions: [],
    account: null,
    tradesSummary: null, // null＝网关成交汇总没回来（正是缺陷现场）
  },
}))

// api 层整体替身：只把页面真会拨的那几个函数换成受控夹具，其余（request 等）走 importActual，
// 免得「替身没定义」被读成「页面不调用它」。state 由下面的用例逐条喂：positions / account / tradesSummary 三格。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: [], available_balance: 100,
      total_realized_pnl: 0, total_unrealized_pnl: 0,
      pnl_offset: 0, total_pnl: 0,
    })),
    resetPaperPnlOffset: vi.fn(async () => ({ status: 'ok', pnl_offset: 0 })),
    updateHoldingsBalance: vi.fn(async () => ({ status: 'ok' })),
    updateHoldings: vi.fn(async () => ({ status: 'ok' })),
    fetchQMTState: vi.fn(async () => ({ enabled: true, tripped: false })),
    fetchRealPositions: vi.fn(async () => ({
      positions: state.positions, ...(state.account ? { account: state.account } : {}),
    })),
    // 页面按 `t.summary` 取成交汇总：无 summary＝total_pnl 不可得
    fetchQMTTrades: vi.fn(async () => (state.tradesSummary ? { summary: state.tradesSummary } : null)),
    fetchRealAdvice: vi.fn(async () => ({ advices: [] })),
  }
})

import Positions from '../pages/Positions.jsx'

// headerPnlText 页头那一行总盈亏文本（Tabs 之外，纸面/实盘共用一处 DOM）。
function headerPnlText() {
  const el = screen.getByText(/总盈亏/, { exact: false })
  return el.textContent || ''
}

// openRealBook 走真实交互路径触发 loadReal（点击「实盘持仓」标签）。
async function openRealBook() {
  fireEvent.click(await screen.findByText(/实盘持仓/))
  await waitFor(() => expect(state.positions.length === 0 || screen.queryAllByTestId('real-pnl-value').length > 0))
}

describe('§W7-G 实盘总盈亏本地兜底必须自报口径（标记在／权威数到就摘／兜底不许被删）', () => {
  beforeEach(() => {
    cleanup()
    document.querySelectorAll('.t-message').forEach((n) => n.remove())
    localStorage.clear()
    state.positions = [{ code: '600519.SH', name: '兜底用例', qty: 100, cost_price: 10, cur_price: 12 }]
    state.account = null
    state.tradesSummary = null
  })
  afterEach(() => {
    cleanup()
  })

  // G1：成交汇总没回来 ⇒ 数字是本地自算的 200，且必须挂着「本地兜底」
  it('G1 兜底生效：页头挂出「本地兜底」标记，数字确为本地自算值', async () => {
    render(<Positions />)
    await openRealBook()
    await screen.findByTestId('real-pnl-value')
    expect(screen.getByTestId('real-pnl-value').textContent, '本地兜底应算出 (12-10)*100=200')
      .toContain('200.00')
    const tag = await screen.findByTestId('real-pnl-fallback')
    expect(tag.textContent).toContain('本地兜底')
  })

  // G2：权威数到达（网关 total_pnl=888，与本地 200 不同）⇒ 数字换成 888 且标记自动摘掉。
  // 用两个互不相等的数是为了让"真值胜出"这件事无法靠"恒挂标记 + 恒显本地值"蒙过去。
  it('G2 网关成交汇总到达：标记消失且显示权威值', async () => {
    state.tradesSummary = { total_pnl: 888 }
    render(<Positions />)
    await openRealBook()
    await waitFor(() => expect(screen.getByTestId('real-pnl-value').textContent).toContain('888.00'))
    expect(screen.queryByTestId('real-pnl-fallback'), '§W7-G：权威数在位时兜底标记必须消失').not.toBeInTheDocument()
  })

  // G3：没有实盘数据 ⇒ 页头走纸面 §E1 口径，不许出现「本地兜底」标记
  it('G3 无实盘数据：不出现兜底标记（纸面腿不被这条改出第三种读数）', async () => {
    state.positions = []
    render(<Positions />)
    await screen.findByText(/可用资金: ¥/)
    expect(screen.queryByTestId('real-pnl-fallback')).not.toBeInTheDocument()
    expect(screen.queryByTestId('real-pnl-value'), 'hasReal=false 时页头走纸面读数').not.toBeInTheDocument()
  })

  // G4：结构锁 + 口径文案锁（源码级，防"下次重构顺手统一口径"把这条改回静默兜底）
  it('G4 源码四件在位，且标记的悬停说明点名两条口径之差', () => {
    // 计数一律在**剥掉注释之后**的源码上做：本仓锤过的同族自伤——说明文字里引用了被测锚点
    // （这条用例的文件头就把 testid 写进过注释），按原始源码计数会把"注释写了"当成"代码在位"，
    // 反过来删掉真代码而留着注释时锁照旧绿。
    const raw = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf-8')
    const src = raw.replace(/^[ \t]*\/\/.*$/gm, '').replace(/\{?\/\*[\s\S]*?\*\/\}?/g, '')
    // 夹具自检两条：原文要读到位（空串＝路径错，下面所有计数会全 0 而"看起来像锁"），
    // 且注释真要被我这套剥法剥掉（剥不干净＝本文件的剥法漏形态，计数锁会退回"注释也算在位"）。
    expect(raw.length, '夹具自检：Positions.jsx 要读到位（0＝路径/读取失败，下面的计数全 0 是假红不是真锁）')
      .toBeGreaterThan(1000)
    expect(src.length, '夹具自检：剥注释后的源码不能是空串').toBeGreaterThan(1000)
    expect(src.length).toBeLessThan(raw.length)
    // ① 兜底实现本体还在（owner 明令不删：删了＝改变现网显示行为，且 §E1 那种"没数就没数"
    //    在实盘侧并不成立——网关数在位时永远优先）
    const calcHits = (src.match(/\(price - \(p\.cost_price \|\| 0\)\) \* \(p\.qty \|\| 0\)/g) || []).length
    expect(calcHits, '§W7-G：本地兜底重算那条腿不许被删').toBe(1)
    // ② 身份派生恰一枚（判据与兜底分支同一条件；两枚＝两条时间线各说一套）
    const deriveHits = (src.match(/const pnlFallback = useMemo\(/g) || []).length
    expect(deriveHits, '§W7-G：兜底身份派生必须恰有一枚实现').toBe(1)
    // ③ 标记渲染恰一处 + 标记文案与 testid 固定（改名＝测试与 UAT 双双失明）
    const renderHits = (src.match(/\{pnlFallback && \(/g) || []).length
    expect(renderHits, '§W7-G：兜底标记的渲染点必须恰一处').toBe(1)
    expect((src.match(/本地兜底/g) || []).length, '标记文案「本地兜底」恰一枚（多枚＝另有一处读数要收编进本锁）').toBe(1)
    expect((src.match(/data-testid="real-pnl-fallback"/g) || []).length).toBe(1)
    expect((src.match(/data-testid="real-pnl-value"/g) || []).length, '页头实盘盈亏数值的锚点恰一枚').toBe(1)
    // ④ 口径要说清：标记的 title 必须同时点名"已实现"与"浮动"，否则标记只是装饰，
    //    而这条缺陷的本体就是"看不出这是哪个口径"。
    const tip = src.match(/title="(这个数是前端[^"]*)"/)
    expect(tip, '§W7-G：兜底标记必须带口径说明（title 文案）').not.toBeNull()
    expect(tip[1]).toContain('已实现')
    expect(tip[1]).toContain('浮动')
    expect(tip[1], '说明里要写清"网关汇总到了标记自动消失"，否则用户会以为要手工刷新')
      .toContain('自动消失')
  })
})
