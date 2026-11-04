// external_brain_tag_20261104.test.jsx — §EXT-BRAIN-1104（2026-11-04）外脑档位标签的展示契约。
//
// 钉三件事，都是"11-05 断桥之后值班会不会盲操作"的直接对应：
//   ① 0/1 一律不渲染——健康态不许占地方（尤其 0=没配网关的纯纸面账号，
//      给它一块"外脑"标签等于凭空造一条没人能处置的告警）；
//   ② 2/3/4 三句必须各指各的处置方向（查读数来源 / 查网关与桥 / 这是 owner 自己的意图），
//      折成同一句"外脑异常"就是让人猜；同时每一句都自带"不再自动开新仓，卖出与纸面照旧"，
//      省掉"是不是整个账号停了、止损也不出了"这一轮追问；
//   ③ 未知值（5 / null / undefined / 非数字）退回"读数未知"，**绝不折成"正常"**
//      ——同 §0925EVE-W3-E「读不到不装已知」，也同契约读数那三态。
// 最后一条接线用例：仪表盘那一行确实按 /api/qmt/state 的字段分形（不在 util 里自证了就算接上）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import ExternalBrainTag from '../components/ExternalBrainTag.jsx'
import { externalBrainTier, EXTERNAL_BRAIN_TIER } from '../utils/liveBrain.js'

const TAIL_PHRASE = '不再自动开新仓'

describe('externalBrainTier 判档（0-4 与未知）', () => {
  it('档 0 不适用 / 档 1 正常：active=false 且不产文案（健康态不占地方）', () => {
    for (const code of [EXTERNAL_BRAIN_TIER.NA, EXTERNAL_BRAIN_TIER.NORMAL]) {
      const t = externalBrainTier(code)
      expect(t.active, `档 ${code} 不该渲染`).toBe(false)
      expect(t.text).toBe('')
    }
  })

  it('档 2 指向「柜台读数过期」，并给出查导出/手填持仓这条处置腿', () => {
    const t = externalBrainTier(2)
    expect(t.active).toBe(true)
    expect(t.text).toContain('柜台读数过期')
    expect(t.text).toContain('券商导出')
    expect(t.text).toContain('持仓资金')
    expect(t.theme).toBe('warning')
  })

  it('档 3 指向「通道断」，处置腿是网关进程与桥（与档 2 完全不同的一条路）', () => {
    const t = externalBrainTier(3)
    expect(t.active).toBe(true)
    expect(t.text).toContain('通道断')
    expect(t.text).toContain('网关进程')
    expect(t.theme).toBe('danger')
  })

  it('档 4 说清是 owner 显式关停的影子态，不是故障', () => {
    const t = externalBrainTier(4)
    expect(t.active).toBe(true)
    expect(t.text).toContain('显式影子态')
    expect(t.text).toContain('owner')
  })

  it('2/3/4 三句互不相同，且谁也不吃谁的关键词（折成一句＝让人盲操作）', () => {
    const texts = [2, 3, 4].map((c) => externalBrainTier(c).text)
    expect(new Set(texts).size, '三档文案必须各不相同').toBe(3)
    expect(texts[0]).not.toContain('通道断')
    expect(texts[1]).not.toContain('柜台读数过期')
    expect(texts[2]).not.toContain('柜台读数过期')
    expect(texts[2]).not.toContain('通道断')
  })

  it('2/3/4 每一句都自带「不再自动开新仓，卖出与纸面照旧」', () => {
    for (const code of [2, 3, 4]) {
      const text = externalBrainTier(code).text
      expect(text, `档 ${code} 少了结论`).toContain(TAIL_PHRASE)
      expect(text, `档 ${code} 少了"卖出与纸面照旧"`).toContain('卖出与纸面照旧')
    }
  })

  it('未知值（5 / -1 / null / undefined / NaN / 小数 / 非数字串）退回「读数未知」，不折成正常', () => {
    for (const bad of [5, -1, null, undefined, NaN, 1.5, Infinity, '', '  ', 'abc', {}, []]) {
      const t = externalBrainTier(bad)
      expect(t.active, `${String(bad)} 不许被当成健康`).toBe(true)
      expect(t.text).toContain('读数未知')
      expect(t.text).not.toContain('正常')
    }
  })

  it('纯整数字符串按数字认（JSON 里偶发成串时不该报未知）', () => {
    expect(externalBrainTier('3').text).toContain('通道断')
    expect(externalBrainTier('1').active).toBe(false)
  })
})

describe('ExternalBrainTag 渲染', () => {
  it('档 0/1 渲染空 DOM（一块标签都不留）', () => {
    for (const code of [0, 1]) {
      const { container } = render(<ExternalBrainTag code={code} />)
      expect(container).toBeEmptyDOMElement()
    }
  })

  it('档 3 渲染出通道断那句，且不带档 2 的字', () => {
    render(<ExternalBrainTag code={3} />)
    expect(screen.getByText(/外脑·通道断/)).toBeInTheDocument()
    expect(screen.queryByText(/柜台读数过期/)).not.toBeInTheDocument()
  })

  it('字段缺席（旧版引擎没发这个键）显示未知，而不是静默不渲染', () => {
    render(<ExternalBrainTag code={undefined} />)
    expect(screen.getByText(/读数未知/)).toBeInTheDocument()
  })
})

// ── 接线腿：仪表盘实盘链路那一行（/api/qmt/state 才是它读的账，不是别的端点）──
vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }
})

vi.mock('../api/index.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual,
    getAccount: () => 'admin',
    getToken: () => 'tk',
    getStoredServer: () => '',
    isAdmin: () => true,
    hasStrictPerm: () => true,
    getRole: () => 'admin',
    isForbidden: (e) => !!(e && e.status === 403),
    // 局部 mock 只覆盖 Dashboard 首屏真正会打的读面：漏一条就是 unhandled rejection 把整文件带红。
    fetchStatus: vi.fn(async () => ({ quote_source: 'sina', quote_age_sec: 2, in_trade_time: true, quote_feed: null })),
    fetchDashboard: vi.fn(async () => ({ signals: [], news_events: [], hot_sectors: [], final_signals: [] })),
    fetchSignals: vi.fn(async () => []),
    fetchNews: vi.fn(async () => []),
    fetchHealth: vi.fn(async () => ({ ok: true })),
    fetchAccount: vi.fn(async () => ({ total_asset: 100000, available: 50000 })),
    fetchQMTState: vi.fn(async () => null),
  }
})

import Dashboard from '../pages/Dashboard.jsx'
import * as apiMod from '../api/index.js'

// 除 external_brain 之外的字段一律取"通道正常"，这样屏上差异只可能来自档位本身。
const liveBase = { enabled: true, mode: 'auto', tripped: false, last_probe_ok: true, gateway_url: 'http://10.77.0.2:8789' }

describe('仪表盘实盘链路行的外脑标签（接线腿）', () => {
  beforeEach(() => { apiMod.fetchQMTState.mockResolvedValue(null) })
  afterEach(() => cleanup())

  it('档 3 在实盘链路那一行给出"通道断"', async () => {
    apiMod.fetchQMTState.mockResolvedValue({ ...liveBase, external_brain: 3 })
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    expect(await screen.findByText(/外脑·通道断/)).toBeInTheDocument()
  })

  it('档 4（qmt.enabled=false，实盘链路那行文本本身不渲染）仍要显示影子态', async () => {
    // 这条是"为什么不把标签挂进 qmtLine"的锁：qmtLine 的第一句就是 !s.enabled ⇒ 空串，
    // 标签若寄生在它上面，owner 主动关停反而变成屏上最没痕迹的一种状态。
    apiMod.fetchQMTState.mockResolvedValue({ ...liveBase, enabled: false, external_brain: 4 })
    render(<MemoryRouter><Dashboard /></MemoryRouter>)
    expect(await screen.findByText(/显式影子态/)).toBeInTheDocument()
    expect(screen.queryByText(/实盘链路/)).not.toBeInTheDocument()
  })

  it('档 0（纯纸面/没配网关）屏上不出现任何"外脑"字', async () => {
    apiMod.fetchQMTState.mockResolvedValue({ ...liveBase, external_brain: 0 })
    const { container } = render(<MemoryRouter><Dashboard /></MemoryRouter>)
    // 先确认快照真的落到页面了（否则"没有外脑字"可能是整条实盘腿都没渲染，假通过）。
    await screen.findByText(/实盘链路/)
    expect(container.textContent).not.toMatch(/外脑/)
  })
})
