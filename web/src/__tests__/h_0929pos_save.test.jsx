// ── §0929POS-SAVE（09-29 全量审计批 P1-4 + 本轮读码新撞缺陷）Positions 整表持仓写收口 ──
//
// 两条缺陷是同族的，只是第二条更硬：
//  ① 报告已列（P1-4）：saveHoldings 的 catch 是空实现、调用点不判成败、persistCache 无条件写
//     ——一次网络抖动＝后端没存住、页面显示已改、脏值活到下次刷新；
//  ② 报告未列（本轮读码撞出）：旧 saveHoldings() 不带参数，内部读**闭包里的 holdings**，
//     而调用点 confirmAdd 是 `setHoldings(下一份表)` 后立刻 `await saveHoldings()`。
//     React 的状态要到下一次渲染才可见，闭包读到的还是**改动前**那份表；
//     POST /api/holdings 又是 full-replace（后端 handlers_fix.go:1104 会把没出现在载荷里的
//     本账号手动持仓直接删档），于是新增行从未上行、编辑行从未更新，
//     60s 轮询（Positions.jsx:646）再把界面打回服务端现值＝**用户改动静默丢失**。
// 修法：整表写只认显式传入的下一份表；返回布尔；在途/脏值不落缓存；失败 toast + 行标脏
//       + 弹窗保持打开；轮询合并时脏行不被服务端旧值覆盖。
//
// 本文件的锁按"每条都能被摘掉守卫反证"设计：
//  T1 等值锁（载荷里真有新行 + 不再夹带 available_balance）——回退成读闭包即红；
//  T2 失败三件套（toast / 缓存不脏 / 弹窗不关）——摘掉 pending 守卫或 catch 即红；
//  T3 脏行存活锁（服务端还没有这一行）——merge 腿漏掉"补回列表尾部"分支即红；
//  T4 脏行覆盖锁（服务端有这一行但是旧值）——merge 腿漏掉"换回本地值"分支即红；
//  T5 成功清脏锁（再存一次成功后标记消失）——把清除条件写反即红；
//  T6 静态负向锁：无参 saveHoldings() 与整表载荷带 available_balance 都不得复活。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// api 替身的可变夹具：saveCalls 记录**实际上行的载荷**（本批缺陷全在载荷口径上，
// 断言只有钉在这里才拦得住"界面改了但后端没收到"）。
const { state, toasts } = vi.hoisted(() => ({
  state: {
    balance: 100,
    holdings: [],          // 服务端现值（fetchHoldings 的读数源）
    totalPnl: 0,
    pnlOffset: 0,
    saveErr: null,         // 非 null 时 updateHoldings 抛错（模拟整表写失败）
    saveCalls: [],         // 每次整表写的载荷快照
    resetCalls: 0,         // 清零点击次数（本文件用它当"手动触发一次 load()"的把手）
  },
  // toast 记账本：产品代码真的调用 MessagePlugin 才算"用户看得见失败"，判据钉在调用与文案上
  toasts: [],
}))

// §0929POS-SAVE 脚手架自伤修复（09-29 门禁实录）：本文件原先在 beforeEach 里
// 手工 `document.querySelectorAll('.t-message').forEach(n => n.remove())` 清 toast 残影，
// 而 TDesign 每条 toast 是挂在 document.body 上的**独立 React root**——手工摘走容器后，
// 它自己的自动关窗（duration 定时器）到点时 React 再去 removeChild 就抛
// NotFoundError（"The node to be removed is not a child of this node"）。
// 表现形态最难缠：用例全过、进程仍非零退出（Test Files 54 passed / Errors 1），
// 隔离 3 跑 1 红＝随机门禁红。改法不是放宽判据，而是把 toast 这层换成替身记账：
// 组件仍用真的 TDesign（只替 MessagePlugin 一个导出），断言从"DOM 里恰好有个 .t-message"
// 抬成"产品代码确实调了 error 且文案带'后端未落库'"，跨用例残影由 toasts 每条重置兜住。
vi.mock('tdesign-react', async (importOriginal) => {
  const actual = await importOriginal()
  const rec = (arg) => {
    toasts.push(typeof arg === 'string' ? arg : String((arg && arg.content) || arg))
    return Promise.resolve({ close() {}, destroy() {} })
  }
  return {
    ...actual,
    MessagePlugin: Object.assign(rec, {
      ...actual.MessagePlugin,
      open: rec, success: rec, error: rec, warning: rec, info: rec, loading: rec,
      closeAll: () => { toasts.length = 0 },
    }),
  }
})

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return {
    ...actual,
    isAdmin: vi.fn(() => true),
    fetchStatus: vi.fn(async () => ({ session: 'test' })),
    setLastSession: vi.fn(),
    fetchHoldings: vi.fn(async () => ({
      holdings: state.holdings, available_balance: state.balance,
      pnl_offset: state.pnlOffset, total_pnl: state.totalPnl,
    })),
    // 整表写替身：先记账再判失败；成功才把服务端现值推进（与后端 full-replace 语义同构）
    updateHoldings: vi.fn(async (payload) => {
      state.saveCalls.push(JSON.parse(JSON.stringify(payload)))
      if (state.saveErr) throw state.saveErr
      state.holdings = payload.holdings
      return { status: 'ok' }
    }),
    resetPaperPnlOffset: vi.fn(async () => {
      state.resetCalls += 1
      state.totalPnl = 0
      return { status: 'ok', pnl_offset: 0 }
    }),
    updateHoldingsBalance: vi.fn(async (v) => { state.balance = v; return { status: 'ok' } }),
    fetchStockLookup: vi.fn(async (c) => ({ name: '测试票' + c.slice(-3), price: 11 })),
    fetchQMTState: vi.fn(async () => ({ enabled: false })),
    fetchRealPositions: vi.fn(async () => ({ positions: [] })),
    fetchQMTTrades: vi.fn(async () => null),
    fetchRealAdvice: vi.fn(async () => ({ advices: [] })),
  }
})

import Positions from '../pages/Positions.jsx'

// 弹窗内按 placeholder 定位输入框并赋值（TDesign InputNumber 的 input 同选择器可用）
async function fill(placeholder, value) {
  const el = await waitFor(() => {
    const e = document.querySelector(`input[placeholder="${placeholder}"]`)
    if (!e) throw new Error(`弹窗输入框未出现：${placeholder}`)
    return e
  }, { timeout: 5000 })
  fireEvent.change(el, { target: { value: String(value) } })
}

// 新增/编辑持仓弹窗是否真的开着（按"这确实是那个持仓弹窗"的结构特征认，不靠文案子串：
// 表单项 label 是「持股数」而 placeholder 才是「持股数量」，按文本匹配会认错节点→判据恒假）。
// 为什么不用文本判定存在：TDesign Dialog 关闭后节点仍留在 DOM（.t-dialog__ctx 只把 display 置
// none，探针实测），按「新增持仓」文案判存在会把已关的弹窗也算成开着——判据必须落在可见性上。
// English: visibility-based probe keyed on the dialog's own code input, not on label text.
function addDialogOpen() {
  const ctxs = [...document.querySelectorAll('.t-dialog__ctx')].filter((n) => n.querySelector('input[placeholder="输入代码"]'))
  expect(ctxs.length, '§0929POS-SAVE：持仓弹窗节点必须能找到（找不到说明判据失效，本文件全部弹窗锁会变成假绿）').toBeGreaterThan(0)
  return getComputedStyle(ctxs[ctxs.length - 1]).display !== 'none'
}

// 走"新增持仓"这条真实交互链：点页头按钮 → 填代码/成本/数量 → 点确定（=confirmAdd）
async function addHolding(code, cost, qty) {
  fireEvent.click(await screen.findByText('+ 新增持仓'))
  await fill('输入代码', code)
  await fill('成本价', cost)
  await fill('持股数量', qty)
  fireEvent.click(await screen.findByRole('button', { name: '确定' }))
}

// 点「编辑」打开某只持仓的编辑弹窗，改数量后确定（编辑腿与新增腿共用同一次整表写）
async function editHoldingQty(code, qty) {
  const row = (await screen.findByText(code)).closest('tr')
  fireEvent.click(within(row).getByText('编辑'))
  await fill('持股数量', qty)
  fireEvent.click(await screen.findByRole('button', { name: '确定' }))
}
// 行内按钮定位不引 testing-library 的 within（本文件只用到 querySelector 级别的就近查找）
function within(row) {
  return { getByText: (t) => [...row.querySelectorAll('button')].find((b) => b.textContent.includes(t)) }
}

describe('§0929POS-SAVE Positions 整表持仓写（载荷等值 + 失败不撒谎 + 脏行不被轮询抹掉）', () => {
  beforeEach(() => {
    cleanup()
    // toast 不再走 DOM 清场（见文件头 vi.mock('tdesign-react') 那条自伤说明）：
    // 替身把每条 toast 记进 toasts，逐条用例开跑前清空即可，body 上不会留任何残影节点。
    toasts.length = 0
    localStorage.clear()
    state.balance = 100
    state.holdings = []
    state.totalPnl = 0
    state.pnlOffset = 0
    state.saveErr = null
    state.saveCalls = []
    state.resetCalls = 0
    // 预置与后端一致的干净缓存：T2 断言"失败后它仍干净"才有对照组
    localStorage.setItem('pos_cache_v1', JSON.stringify({ holdings: [], balance: 100 }))
  })
  afterEach(() => { state.saveErr = null; cleanup() })

  // T1 核心等值锁：新增一条持仓后，**上行载荷里必须有这一条**（旧码传的是改动前那份表）。
  // 反向锁：同一次载荷不得再夹带 available_balance——§P1-11 起资金只走窄口径，
  // 整表端点显式丢弃该字段（带它就是"字段存在但无效"的假契约）。
  it('T1 新增持仓：载荷真带上新行，且不再夹带 available_balance', async () => {
    render(<Positions />)
    await screen.findByText(/可用资金: ¥100\.00/)
    await addHolding('600998', 10, 200)

    await waitFor(() => expect(state.saveCalls.length).toBe(1), { timeout: 5000 })
    const payload = state.saveCalls[0]
    const row = (payload.holdings || []).find((h) => h.code === '600998')
    expect(row, '§0929POS-SAVE：整表写必须携带本次新增行（旧码读闭包旧值＝静默丢改动）').toBeTruthy()
    expect(row.quantity).toBe(200)
    expect(row.cost_price).toBe(10)
    expect('available_balance' in payload, '§0929POS-SAVE：整表端点会丢弃 available_balance，前端不得再上行').toBe(false)
    // 成功路径不得留脏：无「未落库」行标记、无页头计数徽标、弹窗已关（按可见性判，见 addDialogOpen）
    expect(screen.queryByText('未落库')).not.toBeInTheDocument()
    expect(screen.queryByTestId('paper-dirty-count')).not.toBeInTheDocument()
    await waitFor(() => expect(addDialogOpen(), '§0929POS-SAVE：落库成功必须关弹窗').toBe(false), { timeout: 5000 })
  })

  // T2 失败三件套：toast 可见 + 脏行标记 + **乐观值不进 pos_cache_v1** + 弹窗不关（改动不丢）。
  // 摘掉 holdingsPendingRef 守卫或把 catch 改回空实现，本条必红。
  it('T2 整表写失败：toast + 行标脏 + 缓存不写脏 + 弹窗保持打开', async () => {
    state.saveErr = Object.assign(new Error('服务端 500：持仓保存失败'), { status: 500 })
    render(<Positions />)
    await screen.findByText(/可用资金: ¥100\.00/)
    await addHolding('600997', 10, 300)

    await waitFor(() => expect(toasts.join('\n'), '§0929POS-SAVE：保存失败必须让用户看见（旧码空 catch 零提示）')
      .toMatch(/持仓保存失败（后端未落库/), { timeout: 5000 })
    await screen.findByText('未落库', {}, { timeout: 5000 })
    const badge = screen.getByTestId('paper-dirty-count')
    expect(badge.textContent).toContain('1 项未落库')
    expect(screen.getByText('新增持仓'), '§0929POS-SAVE：失败不得关弹窗（否则用户填的东西直接丢）').toBeInTheDocument()
    // 等 React 把这一次的状态变更全部落定再判可见性：紧贴 toast 判会把"正在关窗"读成"还开着"
    // （反证 R4 实测：摘掉 `if (!ok) return` 后只有 T5 变红，本条被这个时序窗口放过去了）。
    await new Promise((r) => setTimeout(r, 400))
    expect(addDialogOpen(), '§0929POS-SAVE：失败必须留着弹窗让用户原地重试').toBe(true)
    expect(document.querySelector('input[placeholder="输入代码"]').value, '§0929POS-SAVE：失败不得重置表单')
      .toBe('600997')
    await waitFor(() => {
      const raw = String(localStorage.getItem('pos_cache_v1') || '')
      expect(raw, '§0929POS-SAVE：未落库的乐观行绝不能进缓存（跨刷新污染）').not.toContain('600997')
    })
  })

  // T3 脏行存活锁（服务端**还没有**这一行）：失败后再拉一次服务端读数（这里用「清零」
  // 这条确实会 await load() 的路径触发），界面必须仍显示那笔未落库的新增行。
  // 旧写法（load 无条件整表覆盖）会把失败行直接抹掉。
  it('T3 失败后轮询：服务端缺这一行时脏行仍在列表里', async () => {
    state.saveErr = new Error('500')
    render(<Positions />)
    await screen.findByText(/可用资金: ¥100\.00/)
    await addHolding('600996', 10, 400)
    await screen.findByText('未落库', {}, { timeout: 5000 })
    state.saveErr = null // 只让上一次写失败；随后的读服务端仍是空表
    fireEvent.click(await screen.findByText('清零'))
    await waitFor(() => expect(state.resetCalls).toBe(1), { timeout: 5000 })
    // 脏行必须留着，且标记不消失（它确实还没落库）
    await screen.findByText('600996', {}, { timeout: 5000 })
    expect(screen.getByText('未落库')).toBeInTheDocument()
  })

  // T4 脏行覆盖锁（服务端**有这一行但是旧值**）：把已有持仓 100 股改成 500 股失败后，
  // 重载读数（服务端仍 100）必须让界面继续显示 500（那才是用户看到的未落库态），
  // 而不是被服务端旧值覆盖回 100。
  it('T4 编辑失败后重载：脏行保留用户编辑值，不被服务端旧值覆盖', async () => {
    state.holdings = [{ code: '600999', name: '旧值票', quantity: 100, cost_price: 10, cur_price: 11 }]
    state.saveErr = new Error('500')
    render(<Positions />)
    await screen.findByText('600999', {}, { timeout: 5000 })
    await editHoldingQty('600999', 500)
    await screen.findByText('未落库', {}, { timeout: 5000 })
    state.saveErr = null
    fireEvent.click(await screen.findByText('清零'))
    await waitFor(() => expect(state.resetCalls).toBe(1), { timeout: 5000 })
    const row = (await screen.findByText('600999')).closest('tr')
    expect([...row.querySelectorAll('td')].map((td) => td.textContent)).toContain('500')
    expect(screen.getByText('未落库'), '§0929POS-SAVE：脏行仍处未落库态，标记不得随重载消失')
      .toBeInTheDocument()
  })

  // T5 成功清脏锁 + 重试去重锁：失败后弹窗保持打开（用户原地再点一次「确定」就是恢复路径）。
  // 这一条钉两件事：① 再存成功后「未落库」标记与页头计数必须消失、缓存写入新行；
  // ② 重试不得把同一代码追加成两行（旧写法 [...holdings, item] 会让表与载荷各出现一次重复代码，
  //    后端按 code 归一只存一条，界面却显示两行——full-replace 载荷里带重复代码也是模糊语义）。
  it('T5 原地重试成功：脏标记消失、缓存落新行，且同代码不出两行', async () => {
    state.saveErr = new Error('500')
    render(<Positions />)
    await screen.findByText(/可用资金: ¥100\.00/)
    await addHolding('600995', 10, 600)
    await screen.findByText('未落库', {}, { timeout: 5000 })
    state.saveErr = null
    // 弹窗此时仍开着、三个字段仍带着上一次的值 → 直接再点确定（不清表单、不重开弹窗）
    fireEvent.click(screen.getByRole('button', { name: '确定' }))
    await waitFor(() => expect(state.saveCalls.length).toBe(2), { timeout: 5000 })
    const codes = state.saveCalls[1].holdings.map((h) => h.code)
    expect(codes.filter((c) => c === '600995').length, '§0929POS-SAVE：重试不得在整表载荷里塞重复代码')
      .toBe(1)
    expect(screen.queryByText('未落库'), '§0929POS-SAVE：落库成功后脏标记必须清').not.toBeInTheDocument()
    expect(screen.queryByTestId('paper-dirty-count')).not.toBeInTheDocument()
    await waitFor(() => expect(addDialogOpen()).toBe(false), { timeout: 5000 })
    // 界面上同代码也只有一行（重复行会让 findByText 直接抛"multiple elements"）
    expect(await screen.findByText('600995')).toBeInTheDocument()
    await waitFor(() => {
      expect(String(localStorage.getItem('pos_cache_v1') || '')).toContain('600995')
    })
  })

  // T6 静态负向锁：本批修的是"调用形态"，形态本身要钉住（跑测环境之外再兜一层）。
  it('T6 静态锁：无参 saveHoldings() 与整表夹带余额均不得复活', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf8')
    const code = src.split('\n').filter((l) => !l.trim().startsWith('//')).join('\n')
    expect(code, '§0929POS-SAVE：saveHoldings() 无参调用＝读闭包旧值，必须显式传下一份表')
      .not.toMatch(/saveHoldings\(\s*\)/)
    expect(code, '§0929POS-SAVE：整表端点丢弃 available_balance，前端不得再随它上行')
      .not.toMatch(/available_balance:\s*availableBalance/)
    // 载荷构造点必须仍然存在（防止有人把整表写腿整段删掉来"满足"上面的负向锁）
    expect(code).toMatch(/api\.updateHoldings\(\{\s*holdings:/)
  })

  // T7 静态锁（本文件写 T3 时撞出来的新依赖，已落码）：mergeOverDirtyRows 的"无脏行快速路径"
  // 不能是 `return serverList`——fetchHoldings 的数组直接当界面状态、服务端对象留在 state 里，
  // 于是 confirmAdd 从 `holdings` 复制出来的下一份表就与 state 同源，任何就地改写会连着改到
  // "服务端读数基线"（正是 §0929POS-SAVE 要消灭的载荷与显示分叉）。
  // 这条比看上去重要：把它改成深拷贝会连挂 T3/T4 行为锁，说明判据落在了真被消费的口径上。
  // English: §0929POS-SAVE — locks the array-copy fast path of mergeOverDirtyRows: a later
  // optimization returning serverList itself would put server objects into state and let
  // confirmAdd's copied table alias them.
  it('T7 静态锁：mergeOverDirtyRows 无脏行分支必须返回副本而非 serverList 本体', () => {
    const src = fs.readFileSync(path.join(HERE, '..', 'pages', 'Positions.jsx'), 'utf8')
    const fn = src.slice(src.indexOf('function mergeOverDirtyRows'))
    const body = fn.slice(0, fn.indexOf('\n  }\n') + 4)
    expect(body).toMatch(/return serverList\.slice\(\)/)
    expect(body).not.toMatch(/return serverList\s*$/)
  })
})
