// ── §4.2 人工回执前端入口的行为锁（2026-11-06）──
// 背景：POST /api/qmt/order-receipt 后端早已实现（internal/server/handlers_qmt_receipt.go），
// 但契约台账 scripts/contract_ledger.tsv:219 自标「无 UI 待补」，此前只能靠 curl 回填。
// 本用例锁住补上的前端入口三件事，判据全部来自后端 orderReceiptReq，不猜字段：
//   ① 表单**只**提交后端认的五个字段 signal_id/status/qty/price/note，
//      qty/price 是数字，**不带 user_id**（后端「收下但永不采信」，归属只由登录态决定）；
//   ② 成功后有可读提示（MessagePlugin.success 回显落账事实）；
//   ③ 失败时**不吞错**：后端中文 error 原样进 MessagePlugin.error，且不冒充成功。
// English: behavior locks for the manual order-receipt form; exact backend fields, success toast,
// and surfaced (never swallowed) failure message.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
// §DET-TIME 同款：接口全是 resolved promise 的替身，排空微任务即可，不依赖真实定时器。
import { settle } from './settle.js'

// TDesign 的 MessagePlugin 整体换成 spy：断言文案而不弹真 toast（与本仓撤单/对账用例同款）。
const mocks = vi.hoisted(() => ({
  message: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
}))
vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: mocks.message }
})

// 二次确认在测试里没有真对话框：直接放行（本页撤单/改判会调，本用例不触发但模块需可用）。
vi.mock('../ui.jsx', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, confirmDialog: async () => true }
})

// 整页 API 替身：挂载期只读拉取用合法 resolved 值，人工回执写成受控 spy。
// 未列出的函数即便被轮询调到也只是 undefined → 被各自 loader 的 try/catch 兜住，不拖垮挂载。
vi.mock('../api/index.js', () => ({
  getAccount: () => 'admin',
  isAdmin: () => true,
  isForbidden: (e) => !!(e && e.status === 403),
  fetchQMTConfig: vi.fn(async () => ({
    enabled: true, mode: 'manual', price_type: 'market', auto_sell: false, halted: false,
    gateway_url: 'http://127.0.0.1:18789', token_masked: '****',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
    known_strategies: [], strategies: [], strategy_amounts: {},
  })),
  fetchQMTState: vi.fn(async () => ({ enabled: true, mode: 'manual', tripped: false, gateway_url: 'http://127.0.0.1:18789' })),
  fetchQMTBroker: vi.fn(async () => ({ ok: true, broker: 'xt', xt_connected: true, queued_connected: false })),
  fetchQMTTrades: vi.fn(async () => ({})),
  fetchQMTOrders: vi.fn(async () => [
    { order_id: 'GW-9001', signal_id: 'SIG-9001', code: '600362.SH', side: '买入', price: 36.56, qty: 500, status: '已报' },
  ]),
  fetchQMTSettleHistory: vi.fn(async () => ({ ok: '1', diffs: [] })),
  fetchShortStatus: vi.fn(async () => ({ short_enabled: false })),
  fetchPaperState: vi.fn(async () => ({ short_book: { enabled: false } })),
  switchQMTBroker: vi.fn(async () => ({})),
  updateQMTConfig: vi.fn(async () => ({})),
  qmtCancel: vi.fn(async () => ({ ok: '1', status: '撤单中', order_id: 'GW-9001' })),
  // 本用例的主角：默认成功响应按后端 200 形状给，失败用例再 mockRejectedValueOnce 覆盖。
  submitOrderReceipt: vi.fn(async () => ({
    status: 'ok', signal_id: 'SIG-9001', order_id: 'GW-9001',
    before_status: '已报', after_status: '已成', fill_written: true, source: 'manual_receipt',
  })),
}))

import Quant from '../pages/Quant.jsx'
import * as api from '../api/index.js'
import { MemoryRouter } from 'react-router-dom' // §W-04 子页签读 URL query，测试必须有 Router 上下文

// 填表并提交：五个输入逐一按 placeholder / testid 定位，模拟运维照券商回单敲单。
// 默认值是一笔合法「已成」回执；各用例按需覆盖（如缺量价、换状态）。
function fillAndSubmit({ signal_id = 'SIG-9001', status = 'filled', qty = '500', price = '36.56', note = '券商App手敲' } = {}) {
  // signal_id：幂等锚点，取自「当日委托」卡
  fireEvent.change(screen.getByPlaceholderText('signal_id（必填，来自当日委托）'), { target: { value: signal_id } })
  // status：原生下拉，取值只能落在后端五档里
  fireEvent.change(screen.getByTestId('receipt-status'), { target: { value: status } })
  // qty / price：数字输入，提交前由页面转成 Number
  fireEvent.change(screen.getByPlaceholderText('成交数量 qty'), { target: { value: qty } })
  fireEvent.change(screen.getByPlaceholderText('成交价格 price'), { target: { value: price } })
  // note：可选备注
  fireEvent.change(screen.getByPlaceholderText('备注 note（可选）'), { target: { value: note } })
  // 提交：按钮文案是本卡唯一入口
  fireEvent.click(screen.getByRole('button', { name: '提交人工回执' }))
}

describe('§4.2 人工回执前端入口（Quant 页）', () => {
  beforeEach(() => {
    // 清调用历史但保留各 loader 的 resolved 实现；submitOrderReceipt 单独复位再给默认成功值，
    // 避免上一支用例的 mockRejectedValueOnce 残留串到下一支。
    vi.clearAllMocks()
    localStorage.clear()
    api.submitOrderReceipt.mockReset()
    api.submitOrderReceipt.mockResolvedValue({
      status: 'ok', signal_id: 'SIG-9001', order_id: 'GW-9001',
      before_status: '已报', after_status: '已成', fill_written: true, source: 'manual_receipt',
    })
  })

  afterEach(() => cleanup())

  it('表单按后端 orderReceiptReq 五字段提交：qty/price 为数字，且不带 user_id', async () => {
    render(<MemoryRouter><Quant /></MemoryRouter>)
    // 卡渲染出来才说明入口真的挂上了（不是只加了 api 函数）
    await waitFor(() => expect(screen.getByText('人工回执（桥断后手敲单回填）')).toBeInTheDocument())
    fillAndSubmit()
    await waitFor(() => expect(api.submitOrderReceipt).toHaveBeenCalledTimes(1))
    // 断言请求体：字段集合一个不多一个不少，值按后端契约（signal_id 去空白、量价是数字）。
    const body = api.submitOrderReceipt.mock.calls[0][0]
    expect(Object.keys(body).sort()).toEqual(['note', 'price', 'qty', 'signal_id', 'status'])
    expect(body.signal_id).toBe('SIG-9001')
    expect(body.status).toBe('filled')
    expect(body.qty).toBe(500)
    expect(body.price).toBe(36.56)
    expect(body.note).toBe('券商App手敲')
    // user_id 是后端「收下但永不采信」的字段，前端绝不伪造归属
    expect(body).not.toHaveProperty('user_id')
  })

  it('提交成功：MessagePlugin.success 回显落账事实，且不走失败提示', async () => {
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('人工回执（桥断后手敲单回填）')).toBeInTheDocument())
    fillAndSubmit()
    await waitFor(() => expect(mocks.message.success).toHaveBeenCalledTimes(1))
    // 成功文案要带上锚点与后端回显的 after_status，读得懂「哪一单、到了什么态」。
    const text = String(mocks.message.success.mock.calls[0][0])
    expect(text).toContain('人工回执已入账')
    expect(text).toContain('SIG-9001')
    expect(text).toContain('已成')
    expect(text).toContain('已落成交行')
    expect(mocks.message.error).not.toHaveBeenCalled()
  })

  it('提交失败：后端中文 error 原样进 MessagePlugin.error，绝不吞错、不冒充成功', async () => {
    // 模拟后端 404「查无此单」：request() 会把 {error:...} 的中文文案放进 e.message。
    api.submitOrderReceipt.mockRejectedValueOnce(new Error('本账号当日账里没有 signal_id=SIG-9001 的委托行'))
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('人工回执（桥断后手敲单回填）')).toBeInTheDocument())
    fillAndSubmit()
    await waitFor(() => expect(mocks.message.error).toHaveBeenCalledTimes(1))
    // 失败文案既要有统一前缀，也要把后端原话透出来（吞成「提交失败」四个字不算可读反馈）。
    const text = String(mocks.message.error.mock.calls[0][0])
    expect(text).toContain('人工回执失败')
    expect(text).toContain('没有 signal_id=SIG-9001 的委托行')
    expect(mocks.message.success).not.toHaveBeenCalled()
  })

  it('成交类回执缺量价：前端就地拦下发 warning，一个请求都不发（后端同样会 400）', async () => {
    render(<MemoryRouter><Quant /></MemoryRouter>)
    await waitFor(() => expect(screen.getByText('人工回执（桥断后手敲单回填）')).toBeInTheDocument())
    // filled 但 qty/price 留空：不发注定被后端拒的请求，也不许静默无反馈。
    fillAndSubmit({ qty: '', price: '' })
    await settle()
    expect(api.submitOrderReceipt).not.toHaveBeenCalled()
    expect(mocks.message.warning).toHaveBeenCalledTimes(1)
    expect(String(mocks.message.warning.mock.calls[0][0])).toContain('qty>0')
    expect(mocks.message.success).not.toHaveBeenCalled()
  })
})
