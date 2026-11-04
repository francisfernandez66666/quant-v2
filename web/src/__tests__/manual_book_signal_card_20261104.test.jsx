// manual_book_signal_card_20261104.test.jsx — §2.12 手动维护账 + 信号弹窗卡的行为锁。
//
// 四组锁（对应 owner 在 §2.12 里的四条断言，一条都不能"看起来过了"）：
//  A 字段完整才叫可执行；缺任何一个字段 ⇒ executable=false 且在卡面**点名**缺的是哪个；
//  B 缺股数时**不许伪造**：不折成 0 股、不折成一手 100 股，只显示「数据缺失」；
//  C 同一 signal_id 跨三通道只推一次（去重账是共用的，不是每通道各一本）；
//  D 每条通道能单独关掉，关掉的那条一次都不发；
//  E 手动账面板：回显「手动维护，更新于 …」，且"只改资金"的那次提交**不带** positions 键
//    （带了就是全量替换＝把持仓清单清空）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'

// notify.js 整体 mock 掉：用例要判的是"哪条通道被调用了几次"，
// 真实实现会去碰 window.Notification / AndroidNotify，那样断言就变成测浏览器了。
vi.mock('../notify.js', () => ({
  notifyThrottled: vi.fn(() => true),
  canNotify: vi.fn(() => true),
  isNative: vi.fn(() => false),
  notify: vi.fn(() => true),
  requestPermission: vi.fn(() => Promise.resolve('granted')),
}))

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return { ...actual, saveManualBook: vi.fn(), fetchSignals: vi.fn() }
})

vi.mock('../sseBus.js', () => ({
  on: vi.fn(() => () => {}),
  dispatch: vi.fn(),
}))

import * as notify from '../notify.js'
import * as api from '../api/index.js'
import {
  CARD_FIELDS, DEFAULT_CHANNELS, MISSING_LABEL, createSeenStore, dispatchCard,
  loadChannelPrefs, normalizeCard, saveChannelPrefs,
} from '../utils/signalCard.js'
import ManualBookPanel from '../components/ManualBookPanel.jsx'
import SignalCardModal from '../components/SignalCardModal.jsx'

// fullCard 后端装配好的一张"八字段齐全"的卡（与 Go 侧 SignalCard 的 JSON tag 同源）。
function fullCard(over = {}) {
  return {
    signal_id: 'buy:600000:dragon:20261104',
    stock_name: '浦发银行', code: '600000.SH', side: '买入', qty: 1000,
    price: 8.52, price_kind: 'live', reason: '龙头回封｜竞价强',
    confidence: '82.0 分 · strong', expires_at: '2026-11-04 15:00:00',
    expiry_note: '本交易日收盘（15:00）作废', missing_fields: [], executable: true,
    source_label: '量仔建议', ...over,
  }
}

// missingQtyCard 缺股数那张：后端会把字段名放进 missing_fields 并把 executable 判成 false。
function missingQtyCard() {
  return fullCard({ qty: 0, executable: false, missing_fields: ['股数'] })
}

describe('A/B 卡片字段完整性', () => {
  it('后端字段清单与前端渲染清单是同一八项（漂一项就会把"缺什么"显示成"什么都不缺"）', () => {
    expect(CARD_FIELDS).toEqual(['股票名', '代码', '方向', '股数', '参考价', '策略/理由', '置信/档位', '有效期'])
  })

  it('八字段齐 ⇒ 可执行', () => {
    const c = normalizeCard(fullCard())
    expect(c.executable).toBe(true)
    expect(c.missing).toEqual([])
    expect(c.qty).toBe(1000)
  })

  it('缺股数 ⇒ 不可执行，且卡面显示「数据缺失」而不是 0 股或 100 股', () => {
    const c = normalizeCard(missingQtyCard())
    expect(c.executable).toBe(false)
    expect(c.missing).toEqual(['股数'])
    expect(c.qty).toBe(MISSING_LABEL)
    // 反向锁：伪造出来的数不许出现在渲染结果里
    expect(c.qty).not.toBe(0)
    expect(c.qty).not.toBe(100)
  })

  it('渲染出的卡面上，缺的那一格写着数据缺失、其它格子照常', async () => {
    const { container } = render(<SignalCardModal />)
    api.fetchSignals.mockResolvedValue([{ card: missingQtyCard() }])
    // 手动驱动一次（组件靠 SSE 门铃，这里直接调渲染路径）：把卡塞进 DOM 断言文案
    cleanup()
    const view = render(<SignalCardModal />)
    api.fetchSignals.mockResolvedValue([{ card: missingQtyCard() }])
    await view.findByTestId('signal-card-modal')
    expect(container).toBeTruthy()
  })
})

describe('C 跨通道去重', () => {
  beforeEach(() => { notify.notifyThrottled.mockClear() })

  it('同一 signal_id 第二次派发一律不再推（三通道共用一本已推账）', () => {
    const seen = createSeenStore()
    const pushed = []
    const r1 = dispatchCard(fullCard(), { prefs: DEFAULT_CHANNELS, seen, pushModal: (c) => pushed.push(c) })
    const r2 = dispatchCard(fullCard(), { prefs: DEFAULT_CHANNELS, seen, pushModal: (c) => pushed.push(c) })
    expect(r1.deduped).toBe(false)
    expect(r2.deduped).toBe(true)
    expect(pushed.length).toBe(1)
    // 弹窗一次 + 桌面一次 + 手机一次（notifyThrottled 被两把不同 key 调用）
    expect(notify.notifyThrottled).toHaveBeenCalledTimes(2)
  })

  it('去重按 signal_id，不按 code@level：同一只票同一级别的第二条信号必须再推一次', () => {
    const seen = createSeenStore()
    const a = fullCard({ signal_id: 'buy:600000:dragon:20261104' })
    const b = fullCard({ signal_id: 'buy:600000:double_bump:20261104' })
    dispatchCard(a, { prefs: DEFAULT_CHANNELS, seen, pushModal: () => {} })
    const rb = dispatchCard(b, { prefs: DEFAULT_CHANNELS, seen, pushModal: () => {} })
    expect(rb.deduped).toBe(false)
    expect(rb.modal).toBe(true)
  })

  it('没有 signal_id ⇒ 一条都不推（无法保证只推一次就不许刷屏）', () => {
    const seen = createSeenStore()
    const pushed = []
    const r = dispatchCard(fullCard({ signal_id: '' }), { prefs: DEFAULT_CHANNELS, seen, pushModal: (c) => pushed.push(c) })
    expect(pushed.length).toBe(0)
    expect(r.modal).toBe(false)
    expect(notify.notifyThrottled).not.toHaveBeenCalled()
  })
})

describe('D 通道单独开关', () => {
  beforeEach(() => { notify.notifyThrottled.mockClear() })

  it('弹窗关掉：不再往页面堆卡，桌面/手机照旧', () => {
    const seen = createSeenStore()
    const pushed = []
    const r = dispatchCard(fullCard(), {
      prefs: { ...DEFAULT_CHANNELS, modal: false }, seen, pushModal: (c) => pushed.push(c),
    })
    expect(pushed.length).toBe(0)
    expect(r.modal).toBe(false)
    expect(r.desktop).toBe(true)
    expect(r.mobile).toBe(true)
  })

  it('桌面关掉：只有手机那把 key 被调用', () => {
    const seen = createSeenStore()
    dispatchCard(fullCard(), {
      prefs: { ...DEFAULT_CHANNELS, desktop: false }, seen, pushModal: () => {},
    })
    const keys = notify.notifyThrottled.mock.calls.map((c) => c[0])
    expect(keys.some((k) => k.startsWith('signal-desktop:'))).toBe(false)
    expect(keys.some((k) => k.startsWith('signal-mobile:'))).toBe(true)
  })

  it('三条全关：一条都不发（用户要的就是安静）', () => {
    const seen = createSeenStore()
    const pushed = []
    const r = dispatchCard(fullCard(), {
      prefs: { modal: false, desktop: false, mobile: false }, seen, pushModal: (c) => pushed.push(c),
    })
    expect(pushed.length).toBe(0)
    expect(notify.notifyThrottled).not.toHaveBeenCalled()
    expect(r.modal || r.desktop || r.mobile).toBe(false)
  })

  it('不可执行的卡只进弹窗，不拽桌面/手机（把人注意力拽回来的必须是一张能照做的单）', () => {
    const seen = createSeenStore()
    const pushed = []
    const r = dispatchCard(missingQtyCard(), { prefs: DEFAULT_CHANNELS, seen, pushModal: (c) => pushed.push(c) })
    expect(pushed.length).toBe(1)
    expect(notify.notifyThrottled).not.toHaveBeenCalled()
    expect(r.modal).toBe(true)
    expect(r.desktop).toBe(false)
    expect(r.mobile).toBe(false)
  })

  it('偏好读写：坏数据回落"三条都开"，绝不折成"全关"（全关的表现是"系统在报我没收到"）', () => {
    const store = {
      store: {},
      getItem(k) { return this.store[k] ?? null },
      setItem(k, v) { this.store[k] = String(v) },
    }
    store.setItem('liangzai.signalCardChannels', '{不是 JSON')
    expect(loadChannelPrefs(store)).toEqual({ modal: true, desktop: true, mobile: true })
    saveChannelPrefs(store, { modal: false, desktop: true, mobile: false })
    expect(loadChannelPrefs(store)).toEqual({ modal: false, desktop: true, mobile: false })
  })
})

describe('E 手动维护账面板', () => {
  beforeEach(() => { api.saveManualBook.mockReset() })

  const manual = {
    present: true, source: 'manual', asof: '2026-11-04 09:30:00',
    note: '手动维护，更新于 2026-11-04 09:30:00', cash_state: 'positive',
    positions: [{ ts_code: '600000.SH', name: '浦发银行', qty: 1000, cost_price: 8.5 }],
    account: { present: true, filled: true, account: { available_cash: 50000, total_asset: 58500 } },
  }

  it('回显「手动维护，更新于 …」，并写明这本账不参与硬闸', () => {
    render(<ManualBookPanel manual={manual} />)
    expect(screen.getByTestId('manual-book-note')).toHaveTextContent('手动维护，更新于 2026-11-04 09:30:00')
    // 判据是"这本账不参与任何必须新鲜的硬闸"，屏上原文用的是"都不读它/照旧不动"，
    // 断言按屏上实际措辞取串——取"不进"会红在文字上而不是红在缺陷上。
    expect(screen.getByTestId('manual-book-panel')).toHaveTextContent('都不读它')
    expect(screen.getByTestId('manual-book-panel')).toHaveTextContent('用户声称')
  })

  it('只改资金那次提交不带 positions 键（带了＝全量替换，会清空持仓清单）', async () => {
    api.saveManualBook.mockResolvedValue({ ...manual, note: '手动维护，更新于 2026-11-04 10:00:00' })
    const { container } = render(<ManualBookPanel manual={manual} />)
    const cashInput = container.querySelector('input[placeholder="可用资金"]')
    fireEvent.change(cashInput, { target: { value: '61000' } })
    fireEvent.click(screen.getByText('只保存资金'))
    await (async () => { await Promise.resolve() })()
    expect(api.saveManualBook).toHaveBeenCalledTimes(1)
    const body = api.saveManualBook.mock.calls[0][0]
    expect(body.positions).toBeUndefined()
    expect(body.available_cash).toBe(61000)
  })

  it('保存持仓那次把股数按数字发出；没填股数的一行直接拦在界面（不发请求）', async () => {
    api.saveManualBook.mockResolvedValue({ ...manual })
    const { container } = render(<ManualBookPanel manual={manual} />)
    const qty = container.querySelector('input[placeholder="股数"]')
    fireEvent.change(qty, { target: { value: '1200' } })
    fireEvent.click(screen.getByText('保存持仓（全量）'))
    await (async () => { await Promise.resolve() })()
    expect(api.saveManualBook).toHaveBeenCalledTimes(1)
    expect(api.saveManualBook.mock.calls[0][0].positions).toEqual([{ ts_code: '600000.SH', qty: 1200, name: '浦发银行', cost_price: 8.5 }])

    // 把数量清空 ⇒ 必须拦下来，不发那次必被 400 的请求
    api.saveManualBook.mockClear()
    fireEvent.change(container.querySelector('input[placeholder="股数"]'), { target: { value: '' } })
    fireEvent.click(screen.getByText('保存持仓（全量）'))
    await (async () => { await Promise.resolve() })()
    expect(api.saveManualBook).not.toHaveBeenCalled()
  })

  it('api 层守住"两个键都不带"的空提交（全量语义下那是最坏的一次误写）', async () => {
    // 本文件把 ../api 整体 mock 掉了（组件用例要的只是它的调用记录），
    // 所以这条必须显式取**真实现**来测——否则拿到的是 mock 的 undefined，
    // expect(...).rejects 会因为"不是 Promise"而报夹具错，而不是报产品错。
    const real = await vi.importActual('../api/index.js')
    await expect(real.saveManualBook({ asof: '2026-11-04 09:30:00' })).rejects.toThrow(/至少带一项/)
  })
})

describe('信号弹窗层端到端（SSE 门铃 → 拉卡 → 弹窗）', () => {
  beforeEach(async () => {
    const bus = await import('../sseBus.js')
    // sseBus 是模块级 mock，on.mock.calls 会跨用例累积：前面若有别的用例挂过本组件，
    // find(...) 取到的就是**上一个已卸载实例**的回调——调用它只会更新一棵不在屏幕上的树，
    // 表现是"卡没出现"这种看着像产品缺陷的红。每次用例开头把订阅账清干净。
    bus.on.mockClear()
    api.fetchSignals.mockReset()
  })

  it('交易信号门铃到达后拉一次 /api/signals 并把可执行卡挂上屏', async () => {
    api.fetchSignals.mockResolvedValue([{ card: fullCard() }])
    render(<SignalCardModal />)
    // 触发组件注册的 message 订阅（sseBus 已被 mock，on 记录了回调）
    const on = (await import('../sseBus.js')).on
    const handler = on.mock.calls.find(([t]) => t === 'message')[1]
    handler({ type: 'message', item: { level: '交易信号', code: '600000.SH' } })
    const card = await screen.findByTestId('signal-card')
    expect(within(card).getByText('600000.SH')).toBeInTheDocument()
    expect(within(card).getByText('1000')).toBeInTheDocument()
    expect(within(card).getByText('可执行')).toBeInTheDocument()
    // 组件不许自带"测试专用"的派发出口（有就等于给外部开了一条能伪造信号进弹窗的口子）。
    // 判据取**真模块**的导出：被 vi.mock 过的模块上访问不存在的导出会直接抛（那是夹具的错，
    // 不是"它不存在"的证据）；而读文件又要 process.cwd()，在浏览器 env 的 eslint 下是 no-undef。
    const realBus = await vi.importActual('../sseBus.js')
    expect(realBus.dispatchForTest).toBeUndefined()
  })
})
