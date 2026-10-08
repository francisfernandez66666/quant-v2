// ── §P2-J O1/O2 + §P3-FE P12/P13（2026-10-06 修复批 波 6）Paper 页「四腿吞错」与「胜率取整」──
//
// 两条缺陷都在 Paper 页，形态不同但都属于「屏上的数不等于账本里的数」：
//  · O1（AUDIT_20261005 P2-J）：持仓/成交/委托/净值四条数据腿各写一句 `catch (_) {}`。
//    独立容错本身是对的（一条腿挂不该拖黑整页），坏在失败之后**什么都不留**：
//    于是持仓取数失败时页面显示的还是上一轮那张表、却毫无标注，用户拿旧持仓做新决策。
//    主腿 fetchPaperState 更是外层 catch 一吞，整页读数停在上一轮而屏上看不出来。
//  · P12（AUDIT_20261005 / Paper:1006）：胜率 `toFixed(0)` 把 99.6% 显示成 100%，
//    零亏损被显示成"全胜"——§0929 ⑧ 已裁决「亚单位不取整」，这一格是当时漏改的。
//
// 断言的层次（每层各拦一种假绿）：
//  1) **逐腿**断言可见错误态（不是只断"有红条"）：四条腿的区块角标各有一枚独立 testid，
//     任一处改回吞错就少一枚角标——这就是 O1 要求的"逐个反证"；
//  2) 独立性反证：只坏一条腿时其余三条照常渲染（否则"整页红"会被误当成修好了）；
//  3) O2 矛盾读数：失败时 KPI 卡与表内合计**必须来自同一份上一轮读数**并同时被标注
//     （断言"旧行仍在 + mismatch 标注仍在"，而不是断"数字变了"——数字不该变）；
//  4) 胜率格按字符串等值断言，旁边配一枚"旧口径 toFixed(0) 必给出 100%"的反证；
//  5) 静态派生锁：catch 吞错形态与 toFixed(0) 在四腿路径上 0 命中（行为腿看不见的复制粘贴残留）。
//
// English: wave-6 locks for the Paper page — each of the four data legs must surface its own
// visible failure state (previously swallowed by `catch (_) {}`), failed legs must keep the last
// good reading *and* label it, and the win-rate cell must stop rounding 99.6% up to 100%.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act, fireEvent } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
// §DET-TIME：等 UI 一律用 settle()；需要触发 60s 兜底轮询的用例改用 vi.useFakeTimers()（§M-10 先例）。
import { settle } from './settle.js'

const HERE = path.dirname(fileURLToPath(import.meta.url))

const POS = {
  code: '600000.SH', name: '浦发银行', strategy: 'N形', strategy_type: '',
  qty: 500, cost_price: 9.5, mark: 9.8, pnl: 150, pnl_pct: 3.16,
  slippage_pct: 0, latency_sec: 2, signal_at: '2026-09-18 09:35:00', filled_at: '2026-09-18 09:35:02',
}

// 桩名必须来自**静态函数**：mock factory 在本文件 import 页面时就执行，那时 state.impl 还是空的
// （同族 §M-10 的 okPayloads() 就是这个原因）。
function paperStubs() {
  return {
    getAccount: () => 'admin',
    isForbidden: (e) => !!(e && e.status === 403),
    fetchPaperState: () => paperState(),
    fetchPaperPositions: () => [POS],
    fetchPaperTrades: () => [{ code: '600000.SH', name: '浦发银行', side: 'buy', qty: 500, price: 9.5, amount: 4750, time: '2026-09-18 09:35:02' }],
    fetchPaperOrders: () => [{ id: 'o1', code: '600000.SH', name: '浦发银行', side: 'buy', qty: 500, price: 9.5, status: 'filled', time: '2026-09-18 09:35:01' }],
    fetchPaperEquity: () => [{ date: '2026-09-18', value: 99900 }],
    fetchPaperStrategies: () => ({ strategies: [], known_strategies: [], blacklist: [], shadow_blacklist: true }),
    fetchPaperConfig: () => ({ enabled: true, engine_enabled: true, auto_sell: true }),
    fetchPaperSelfCheck: () => ({ enabled: true, is_admin: true }),
    fetchPaperResearchReports: () => ({ reports: [], count: 0 }),
    fetchShortStatus: () => ({ short_enabled: false }),
    buyPaperPosition: () => ({}),
    sellPaperPosition: () => ({}),
  }
}

// 账本载荷（含绩效统计）；胜率一格由用例改 win_rate_pct，其余保持能渲染出 KPI 卡的最小值
function paperState(winRate) {
  return {
    is_admin: true, enabled: true, initial_capital: 100000, max_positions: 5,
    stats: {
      initial_capital: 100000, cash: 95000, market_value: 4900, total_value: 99900,
      total_return_pct: -0.1, today_return_pct: 0, realized_pnl: -12.34, open_positions: 1,
      win_rate_pct: winRate === undefined ? 99.6 : winRate,
      filled_buys: 1, avg_slippage_pct: 0, avg_latency_sec: 2, max_latency_sec: 2,
      slippage_cost: 0, signal_amount_pct: 0,
    },
    strategy_pools: [], pools: {}, short_book: { enabled: false },
  }
}

const { state } = vi.hoisted(() => ({ state: { impl: {} } }))
// 包装必须是**同步透传**（vi.fn((...a) => state.impl[n](...a))），不能写成 async：
// 本桩集里混着同步助手（isForbidden/getAccount），一旦统一加 async，
// `{forbidden && …}` 拿到的就是 Promise 对象 ⇒ React 直接报「Objects are not valid as a child」
// （首跑实录四条同因红）。异步语义由桩实现自己返回 Promise 决定，与包装层无关（同族 §P2-H 的写法）。
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(paperStubs())) {
    stubs[name] = vi.fn((...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import Paper from '../pages/Paper.jsx'

// 让某几条腿**拒绝**（其余照常成功）——按腿名单点名，避免"整页坏"与"单腿坏"两种形态混成一份桩。
// 用 Promise.reject 而不是同步 throw：真实 fetch 层就是返回被拒的 promise，
// 同步 throw 会额外测到"页面在 await 之前就抛"这条不存在的路径。
function rejectLeg(name) {
  return () => Promise.reject(Object.assign(new Error(name + ' 读取失败'), { status: 500 }))
}
function failLegs(names) {
  for (const n of names) state.impl[n] = rejectLeg(n)
}

// 剥注释：静态派生锁只看代码行（文件头说明里引用的旧写法不能被当成一处实现）
function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((l) => {
      const i = l.indexOf('//')
      return i >= 0 ? l.slice(0, i) : l
    })
    .join('\n')
}
function readSrc(rel) {
  return stripComments(fs.readFileSync(path.join(HERE, '..', rel), 'utf8'))
}

// 切到指定 Tab 面板（TDesign 的 Tabs 只渲染当前面板 ⇒ 面板内的角标/表格要先切过去才在 DOM 里）。
// 页签按 label 文案取：label 形如「成交日志 (0)」，正则必须带count \(，
// 否则红条/空态文案里同样出现「成交日志」这几个字 ⇒ getByText 会报 multiple（O1 首跑实录）。
function switchTab(labelRe) {
  const el = screen.getByText(labelRe)
  fireEvent.click(el)
}

describe('§P3-FE P12/P13 胜率格不取整', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    state.impl = {}
    for (const k of Object.keys(paperStubs())) state.impl[k] = paperStubs()[k]
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  // P12 99.6% 必须原样到一位小数，且屏上不得出现取整后的 100%
  it('P12 win_rate_pct=99.6 → 渲染 99.6%，不渲染取整后的 100%', async () => {
    render(<Paper />)
    await settle()
    const cell = screen.getByTestId('paper-win-rate')
    expect(cell.textContent).toContain('99.6%')
    expect(cell.textContent, '§P3-FE P12：亚单位不许取整（99.6→100 会显示成全胜）').not.toContain('100%')
    // 副读数「/ 1仓」仍在同一格：证明改的是主读数而不是整格被换成缺数占位
    expect(cell.textContent).toContain('/ 1仓')
  })

  // P13 反证：旧口径 (99.6).toFixed(0)+'%' 恰好就是 '100%'——
  // 这条不是测实现，是钉住"旧口径与交付口径必须不同"，防止有人把断言改成 toEqual('100%')。
  it('P13 旧口径 toFixed(0) 与同源实现给出不同读数（反证基线）', async () => {
    const legacy = Number(99.6).toFixed(0) + '%'
    expect(legacy).toBe('100%')
    render(<Paper />)
    await settle()
    expect(screen.getByTestId('paper-win-rate').textContent).not.toContain(legacy)
  })

  // P13b 缺数不冒充读数：win_rate_pct 非数（老后端没这个键）时显示 '—'，而不是 0.0%
  it('P13b win_rate_pct 缺失/非数 → 显示占位符而不是 0.0%', async () => {
    state.impl.fetchPaperState = () => {
      const s = paperState()
      delete s.stats.win_rate_pct
      return s
    }
    render(<Paper />)
    await settle()
    const cell = screen.getByTestId('paper-win-rate')
    expect(cell.textContent).toContain('—')
    expect(cell.textContent, '§P3-FE：缺读数不许折叠成 0.0%（0% 是"全亏"的合法读数）').not.toContain('0.0%')
  })

  // P13c 合法 0 必须还能显示 0.0%（把缺数与真零混淆是 §P2-F 同族，判据要两头都钉）
  it('P13c win_rate_pct=0 → 显示 0.0%（真零不是缺数）', async () => {
    state.impl.fetchPaperState = () => paperState(0)
    render(<Paper />)
    await settle()
    expect(screen.getByTestId('paper-win-rate').textContent).toContain('0.0%')
  })

  // P13d 静态锁：胜率格走 toFixed(1)，四腿/KPI 读数额外不许有 toFixed(0)（亚单位取整的形态源头）
  it('P13d 胜率判据唯一出口 + 取整形态 0 命中', () => {
    const src = readSrc('pages/Paper.jsx')
    expect((src.match(/Number\(activeStats\.win_rate_pct\)\.toFixed\(1\)/g) || []).length).toBe(1)
    expect((src.match(/win_rate_pct[^;]{0,40}\.toFixed\(0\)/g) || []).length, '§P3-FE P12：胜率取整形态不得复活').toBe(0)
    expect((src.match(/data-testid="paper-win-rate"/g) || []).length).toBe(1)
  })
})

describe('§P2-J O1/O2 Paper 四腿失败可见 + 不矛盾读数', () => {
  beforeEach(() => {
    cleanup()
    localStorage.clear()
    state.impl = {}
    for (const k of Object.keys(paperStubs())) state.impl[k] = paperStubs()[k]
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  // O1 四条腿同时 500：四枚区块角标**各自**在位 + 红条逐一点名 + KPI 矛盾标注在位。
  // 只断红条的话，把某一处改回 catch (_) {} 照样绿（红条由共用台账驱动，看不出少了一枚角标）。
  // 持仓/成交/委托三块在 Tabs 里，TDesign 只挂载**当前面板** ⇒ 角标必须跟着读数走（切页才可见），
  // 这既是 DOM 事实也是正确的产品语义：读数旁边的标注不该由"哪一页默认展开"决定。
  it('O1 持仓/成交/委托/净值四腿各 500 → 四处各自可见 + 红条点名四条', async () => {
    failLegs(['fetchPaperPositions', 'fetchPaperTrades', 'fetchPaperOrders', 'fetchPaperEquity'])
    render(<Paper />)
    await settle()
    // 净值曲线区块在 Tabs 之外，首屏就在
    expect(screen.getByTestId('paper-leg-fail-净值曲线').textContent).toContain('读取失败')
    // 当前面板＝持仓
    expect(screen.getByTestId('paper-leg-fail-持仓').textContent).toContain('读取失败')
    // 逐个切到成交/委托面板再断角标（每枚独立，缺一枚即红＝O1 要求的"逐个反证"）
    switchTab(/成交日志 \(/)
    await settle()
    expect(screen.getByTestId('paper-leg-fail-成交').textContent).toContain('读取失败')
    switchTab(/订单 \(/)
    await settle()
    expect(screen.getByTestId('paper-leg-fail-委托').textContent).toContain('读取失败')

    const banner = screen.getByTestId('load-ledger')
    for (const leg of ['持仓', '成交', '委托', '净值曲线']) {
      expect(banner.textContent).toContain(leg)
    }
    // 四腿全坏的屏上：KPI 与表内合计都还是上一轮读数，必须同时挂上矛盾标注（O2 的可见半边）
    expect(screen.getByTestId('paper-kpi-mismatch').textContent).toContain('上一轮读数')
    expect(banner.getAttribute('data-page')).toBe('Paper')
  })

  // O1b 主腿失败：外层 catch 原先静默吞（整页停在上一轮而看不出来），现按「账户状态」记账。
  // 覆盖面账（不吹成四枚角标齐平）：账户状态腿的读数是**上方整排 KPI 卡**，没有单一区块头，
  // 所以它的"读数旁边可见"由 paper-kpi-mismatch 承担（紧贴绩效卡），红条再点名一次；
  // 断言只按这两处等值，而不是硬造一枚不存在于产品语义里的角标。
  it('O1b fetchPaperState 拒绝 → 红条点名账户状态 + 绩效卡旁矛盾标注在位', async () => {
    state.impl.fetchPaperState = rejectLeg('fetchPaperState')
    render(<Paper />)
    await settle()
    expect(screen.getByTestId('load-ledger').textContent).toContain('账户状态')
    const mismatch = screen.getByTestId('paper-kpi-mismatch')
    expect(mismatch.textContent).toContain('账户状态（含绩效统计）读取失败')
    expect(mismatch.textContent).toContain('绩效卡是上一轮读数')
  })

  // O1c 独立性反证：只坏持仓时，其余三条腿照常渲染（"整页红"不是修复目标）
  it('O1c 只坏持仓 → 仅该腿有角标，其余三腿读数照常上屏', async () => {
    state.impl.fetchPaperPositions = rejectLeg('fetchPaperPositions')
    render(<Paper />)
    await settle()
    expect(screen.getByTestId('paper-leg-fail-持仓')).toBeInTheDocument()
    for (const leg of ['成交', '委托', '净值曲线']) {
      expect(screen.queryByTestId('paper-leg-fail-' + leg), '未失败的腿不许挂角标：' + leg).toBeNull()
    }
    expect(screen.getByTestId('load-ledger').textContent).toContain('持仓')
    // 成交/委托两条腿的数据仍在（各切到对应面板断一行读数）
    switchTab(/成交日志 \(/)
    await settle()
    expect(screen.getByText('600000.SH')).toBeInTheDocument()
    switchTab(/订单 \(/)
    await settle()
    expect(screen.getByText('600000.SH')).toBeInTheDocument()
  })

  // O1d 契约漂移也算这条腿没读到：后端把数组改成对象时不许静默写进表格
  it('O1d 持仓返回非数组（契约漂移） → 该腿记名失败且保留空表口径', async () => {
    state.impl.fetchPaperPositions = () => ({ data: [] })
    render(<Paper />)
    await settle()
    const badge = screen.getByTestId('paper-leg-fail-持仓')
    expect(badge).toBeInTheDocument()
    expect(badge.title).toContain('契约漂移')
  })

  // O2 失败**不清空**既有读数 + 读数旁边有标注：先成功一轮，再让 60s 兜底轮询里四条数据腿全失败，
  // 断言上一轮那张持仓行仍在、KPI 主读数未变，且矛盾标注同时出现。
  // 这条拦的是两种"看起来更干净"的错法：把旧值抹掉（读取失败冒充今天没数据）、
  // 或只留旧值不标注（用户拿旧持仓做新决策）。
  // 故意**不**让账户状态腿一起失败：主腿失败时 load() 在四腿之前就 return（见 Paper.jsx:474 外层 catch），
  // 那种形态由 O1b 单独覆盖；这里要测的是"KPI 是本轮新读数、三张表是上一轮旧读数"这对真矛盾。
  it('O2 第二轮四腿全失败 → 上一轮读数保留 + KPI 矛盾标注点名三条表腿', async () => {
    vi.useFakeTimers()
    render(<Paper />)
    await settle()
    // 第一轮成功：持仓行与 KPI 都来自本轮读数，此时不该有失败标注
    expect(screen.queryByTestId('load-ledger')).toBeNull()
    expect(screen.getByText('600000.SH')).toBeInTheDocument()
    expect(screen.getByTestId('paper-win-rate').textContent).toContain('99.6%')

    failLegs(['fetchPaperPositions', 'fetchPaperTrades', 'fetchPaperOrders', 'fetchPaperEquity'])
    // 触发 §F5 兜底轮询（默认 60s）走第二轮
    await act(async () => { vi.advanceTimersByTime(60000) })
    await settle()

    // 读数仍在（失败不清空），KPI 仍来自本轮成功的状态腿
    expect(screen.getByText('600000.SH'), '§P2-J：失败腿不得抹掉上一轮读数').toBeInTheDocument()
    expect(screen.getByTestId('paper-win-rate').textContent).toContain('99.6%')
    // 标注：红条点名 + KPI 矛盾说明同时在场（缺任一处就是"矛盾数字"）
    expect(screen.getByTestId('load-ledger')).toBeInTheDocument()
    const mismatch = screen.getByTestId('paper-kpi-mismatch')
    expect(mismatch.textContent).toContain('持仓表是上一轮读数')
    expect(mismatch.textContent).toContain('成交日志是上一轮读数')
    expect(mismatch.textContent).toContain('委托记录是上一轮读数')
    // 主腿本轮成功 ⇒ 矛盾标注不许顺带把账户状态也点名（点名错了等于把好的读数说成旧的）
    expect(mismatch.textContent).not.toContain('账户状态')
  })

  // O2b 成功一轮后自动销案：第二轮全部成功时红条与角标必须整体消失
  // （只加不减的台账会变成"永久红"，下一次评审就会被当成噪声而整条忽略）
  it('O2b 第二轮全部成功 → 台账销案，红条与角标整体消失', async () => {
    vi.useFakeTimers()
    render(<Paper />)
    await settle()
    failLegs(['fetchPaperPositions'])
    await act(async () => { vi.advanceTimersByTime(60000) })
    await settle()
    expect(screen.getByTestId('paper-leg-fail-持仓')).toBeInTheDocument()
    // 恢复成功后再走一轮：这一轮 clearLoadFail 应把案销掉
    state.impl.fetchPaperPositions = () => [POS]
    await act(async () => { vi.advanceTimersByTime(60000) })
    await settle()
    expect(screen.queryByTestId('load-ledger')).toBeNull()
    expect(screen.queryByTestId('paper-leg-fail-持仓')).toBeNull()
    expect(screen.queryByTestId('paper-kpi-mismatch')).toBeNull()
  })

  // O1e 静态派生锁：四腿路径上不再有无事可做的吞错 catch（吞错形态＝本条缺陷本体）
  it('O1e 四腿吞错形态 0 命中 + loadPaperLeg 判据唯一出口', () => {
    const src = readSrc('pages/Paper.jsx')
    // 旧写法 `catch (_) {}`（体为空）在整页 0 命中
    expect((src.match(/catch\s*\(\s*_?\s*\)\s*\{\s*\}/g) || []).length).toBe(0)
    expect((src.match(/function loadPaperLeg\(/g) || []).length).toBe(1)
    // 四条腿共用同一枚封装（写成四份复制粘贴＝下一批分叉的起点）
    expect((src.match(/await loadPaperLeg\(token, '/g) || []).length).toBe(4)
    // 台账与红条各只接一次（重复实例＝两处两套账，销案永远对不齐）
    expect((src.match(/useLoadLedger\(\)/g) || []).length).toBe(1)
    expect((src.match(/<LoadFailBanner /g) || []).length).toBe(1)
  })
})
