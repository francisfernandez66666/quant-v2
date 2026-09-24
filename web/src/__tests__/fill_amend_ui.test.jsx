// ── §FILL-AMEND（2026-09-23）成交勘误入口测试 fill_amend_ui.test.jsx ──
// 背景：09-22 一笔真实卖出被按错误方向落账 → 买入笔数/预算/回款/已实现盈亏四本账同时污染，
// 而系统里没有任何"人工把这笔改回来"的入口（只能等下一次自动重放，而那正是本次失灵的路径）。
// 本批补的是**只追加、不覆写**的逐笔勘误：提交=待批准影子条目（账不动）→ 批准=读取侧方向生效
// → 撤销=回到柜台原始方向，另配只读的账本守恒自检。这里锁住前端这条链的四个不变量：
//   ① 提交体只带 {fill_id,new_side,reason}，**绝不带锚点**（trade_id/order_id/price/qty）——
//      锚点必须由服务端从原始成交行读出，前端自报锚一旦写歪就是一条匹配不到成交的死勘误；
//   ② 三态台账可视且动作互斥（待批准才给「批准生效」，已生效才给「撤销」）；
//   ③ 空理由在前端就拦住，一次请求都不发（后端同口径 400，这里是镜像防线）；
//   ④ 守恒自检缺腿时如实显示「未检查 + 原因」，绝不渲染成"通过"（§M-8/§N-6：
//      把没数据报成成功是本仓反复出事的形态）。
// English: §FILL-AMEND frontend locks — the submit body carries no anchor, the three amendment
// states are mutually exclusive in the ledger, an empty reason never leaves the browser, and a
// skipped conservation leg is surfaced as "not checked" rather than "passed".
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
// §DET-TIME（2026-09-24）：本文件的等待一律走 settle()（排空微任务队列）而不是 waitFor/findBy，
// 理由与轮数取值见 settle.js 文件头。被测链路的接口全是 resolved promise 的 mock，不需要任何时钟。
import { settle } from './settle.js'
import * as api from '../api/index.js'
import FillAmendPanel from '../components/FillAmendPanel.jsx'

const ok = (body) => new Response(JSON.stringify(body), { status: 200 })

// ── ① API 封装层：请求体/URL 的形状 ──
describe('勘误 API 封装（§FILL-AMEND）', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('提交体只有 fill_id/new_side/reason，不带任何锚点字段', async () => {
    const f = vi.fn(async () => ok({ ok: '1' }))
    vi.stubGlobal('fetch', f)
    await api.createFillAmendment({
      fillId: 42, newSide: '卖出', reason: '网关日志 dispatch=卖出，回报误记买入',
      // 下面这些即便调用方误传也绝不能出现在请求体里：锚点是服务端的职责
      trade_id: 'T999', order_id: 'ORD999', price: 22.55, qty: 100,
    })
    expect(f).toHaveBeenCalledTimes(1)
    const url = f.mock.calls[0][0]
    const opts = f.mock.calls[0][1]
    expect(url).toContain('/api/qmt/fill-amendments')
    expect(opts.method).toBe('POST')
    const body = JSON.parse(opts.body)
    expect(body).toEqual({ fill_id: 42, new_side: '卖出', reason: '网关日志 dispatch=卖出，回报误记买入' })
    // 反证：锚点字段一个都不许泄漏进请求体
    for (const k of ['trade_id', 'order_id', 'price', 'qty', 'code', 'traded_at']) {
      expect(body[k]).toBeUndefined()
    }
    expect(url).not.toContain('T999')
  })

  it('批准/撤销各自打独立端点（生效是唯一动账动作，必须可分辨）', async () => {
    const calls = []
    vi.stubGlobal('fetch', vi.fn(async (url, opts) => { calls.push([url, opts.method]); return ok({ ok: '1' }) }))
    await api.applyFillAmendment(7)
    await api.revokeFillAmendment(7)
    expect(calls).toEqual([
      ['/api/qmt/fill-amendments/7/apply', 'POST'],
      ['/api/qmt/fill-amendments/7/revoke', 'POST'],
    ])
  })

  it('台账状态过滤：有值才拼参数，留空不传 status（留空=全部）', async () => {
    const urls = []
    vi.stubGlobal('fetch', vi.fn(async (url) => { urls.push(url); return ok({ ok: '1', amendments: [] }) }))
    await api.fetchFillAmendments('pending')
    await api.fetchFillAmendments('')
    expect(urls[0]).toContain('/api/qmt/fill-amendments?status=pending')
    expect(urls[1]).not.toContain('status=')
  })

  it('守恒自检：day 编码进查询串，留空则由后端按当日判定', async () => {
    const urls = []
    vi.stubGlobal('fetch', vi.fn(async (url) => { urls.push(url); return ok({ ok: '1', report: {} }) }))
    await api.fetchFillConservation('2026-09-22')
    await api.fetchFillConservation('')
    expect(urls[0]).toContain('/api/qmt/fills/conservation?day=2026-09-22')
    expect(urls[1]).not.toContain('?day=')
  })
})

// ── ②③④ 面板渲染：只 mock 网络出口，组件逻辑保持真实 ──
vi.mock('../api/index.js', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real }
})
// 二次确认对话框在本仓是真实 DOM（ui.jsx confirmDialog 返回 promise）：
// 这里只把它固定成"用户点了确认"，让断言聚焦在"点了确认之后必须打到哪个端点"。
// 刻意用普通函数而非 vi.fn：下面的 afterEach 是 clearAllMocks（只清调用记录），
// 若用 vi.fn 又误用 restoreAllMocks，会把实现整个抹掉、批准路径静默短路成假红。
vi.mock('../ui.jsx', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, confirmDialog: async () => true }
})

const PENDING = {
  id: 1, fill_id: 42, code: '603468.SH', trade_id: 'T1001', orig_side: '买入', new_side: '卖出',
  qty: 100, orig_amount: 2255, reason: '柜台回单为卖出', operator: 'admin', status: 'pending',
  created_at: '2026-09-23 10:00:00', applied_at: '', amend_key: 't:T1001',
}
const APPLIED = { ...PENDING, id: 2, status: 'applied', applied_at: '2026-09-23 10:05:00', amend_key: 't:T1002' }
const REVOKED = { ...PENDING, id: 3, status: 'revoked', amend_key: 't:T1003' }

describe('勘误面板台账与守恒自检（§FILL-AMEND）', () => {
  beforeEach(() => {
    // 默认桩：台账三条状态各一、守恒未跑
    api.fetchFillAmendments = vi.fn(async () => ({ ok: '1', amendments: [PENDING, APPLIED, REVOKED] }))
    api.fetchFillConservation = vi.fn(async () => ({ ok: '1', report: null }))
    api.createFillAmendment = vi.fn(async () => ({ ok: '1' }))
    api.applyFillAmendment = vi.fn(async () => ({ ok: '1' }))
    api.revokeFillAmendment = vi.fn(async () => ({ ok: '1' }))
  })
  afterEach(() => { vi.clearAllMocks() })

  it('三态各给各自的动作：待批准可批准、已生效可撤销、已撤销只归档', async () => {
    render(<FillAmendPanel fills={[]} />)
    await settle()
    expect(api.fetchFillAmendments).toHaveBeenCalled()
    expect(screen.getByText('待批准')).toBeInTheDocument()
    expect(screen.getByText('已生效')).toBeInTheDocument()
    expect(screen.getAllByText('已撤销').length).toBeGreaterThan(0)
    // 动作互斥：只有那一条 pending 有「批准生效」，只有 applied 有「撤销」
    expect(screen.getAllByText('批准生效')).toHaveLength(1)
    expect(screen.getAllByText('撤销')).toHaveLength(1)
    expect(screen.getByText('已归档')).toBeInTheDocument()
  })

  it('批准后回调 onChanged 并刷新台账（账目数字此刻才真的动）', async () => {
    const onChanged = vi.fn()
    render(<FillAmendPanel fills={[]} onChanged={onChanged} />)
    await settle()
    expect(api.fetchFillAmendments).toHaveBeenCalled()
    api.fetchFillAmendments.mockClear()
    fireEvent.click(screen.getByText('批准生效'))
    await settle()
    expect(api.applyFillAmendment).toHaveBeenCalledWith(1)
    expect(onChanged).toHaveBeenCalled()
    expect(api.fetchFillAmendments.mock.calls.length).toBeGreaterThan(0)
  })

  it('台账读取失败（如会话非 admin 的 403）：错误文案透出，不静默渲染成空台账', async () => {
    api.fetchFillAmendments = vi.fn(async () => { const e = new Error('无权限'); e.status = 403; throw e })
    render(<FillAmendPanel fills={[]} />)
    await settle()
    expect(screen.getByText(/无权限/)).toBeInTheDocument()
    expect(screen.queryByText('暂无勘误记录——只有取证确认柜台方向记错时才需要改判')).not.toBeInTheDocument()
  })

  it('改判对话框：空理由在前端拦住且零外发；填了才提交且只带三键', async () => {
    const target = {
      id: 42, code: '603468.SH', side: '买入', price: 22.55, qty: 100, amount: 2255,
      traded_at: '2026-09-22T14:01:02', order_id: 'ORD1', trade_id: 'T1001', amend_key: 't:T1001',
    }
    const onClose = vi.fn()
    render(<FillAmendPanel fills={[target]} target={target} onCloseTarget={onClose} />)
    await settle()
    const dialog = screen.getByText('改判成交 603468.SH')
    expect(dialog).toBeInTheDocument()
    // 反证：空理由点提交 → 不发请求
    fireEvent.click(screen.getByText('提交待批准勘误'))
    expect(api.createFillAmendment).not.toHaveBeenCalled()
    // 正例：填理由后提交，参数按 fill_id/新方向/理由三项出网
    fireEvent.change(screen.getByPlaceholderText('为什么要改这笔的方向'), { target: { value: '柜台回单为卖出' } })
    fireEvent.click(screen.getByText('提交待批准勘误'))
    await settle()
    expect(api.createFillAmendment).toHaveBeenCalledWith({ fillId: 42, newSide: '卖出', reason: '柜台回单为卖出' })
  })

  it('守恒自检：缺腿如实显示「未检查+原因」，不得渲染成通过', async () => {
    api.fetchFillConservation = vi.fn(async () => ({
      ok: '1',
      report: {
        user_id: 'u_1', day: '2026-09-22', applied_amendments: 1,
        positions_checked: 2, positions_ok: false,
        position_lines: [{ code: '603468.SH', replayed_qty: 0, book_qty: 100, diff: 100, note: '账上多：成交簿重放不含该笔买入' }],
        cash: { checked: false, skip_reason: '期初资金未配置' },
        ok: false,
      },
    }))
    render(<FillAmendPanel fills={[]} />)
    await settle()
    expect(api.fetchFillAmendments).toHaveBeenCalled()
    fireEvent.click(screen.getByText('只读自检'))
    await settle()
    expect(screen.getByText('存在差异（只报数，未动账）')).toBeInTheDocument()
    // 603468.SH 在本用例出现两处（台账待批准行 + 守恒差异行），按"至少两处"断言而非 getByText
    expect(screen.getAllByText('603468.SH').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('账上多：成交簿重放不含该笔买入')).toBeInTheDocument()
    expect(screen.getByText(/未检查（期初资金未配置）/)).toBeInTheDocument()
    // 反证：现金腿未检查时不得出现任何"通过"字样
    expect(screen.queryByText('守恒通过')).not.toBeInTheDocument()
  })

  it('守恒自检全绿：持仓一致且现金可比 → 结论为通过', async () => {
    api.fetchFillConservation = vi.fn(async () => ({
      ok: '1',
      report: {
        user_id: 'u_1', day: '2026-09-23', applied_amendments: 0,
        positions_checked: 1, positions_ok: true, position_lines: [],
        cash: { checked: true, initial_capital: 100000, buy_amount: 2255, sell_amount: 0, fee_total: 5, stamp_tax_total: 0, expected_cash: 97740, book_cash: 97740, diff: 0, net_spend: 2255 },
        ok: true,
      },
    }))
    render(<FillAmendPanel fills={[]} />)
    await settle()
    expect(api.fetchFillAmendments).toHaveBeenCalled()
    fireEvent.click(screen.getByText('只读自检'))
    await settle()
    expect(screen.getByText('守恒通过')).toBeInTheDocument()
    expect(screen.queryByText(/未检查/)).not.toBeInTheDocument()
  })
})
