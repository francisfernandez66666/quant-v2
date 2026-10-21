// ── §HISQUOTE / §LOGCROSS（2026-10-22）信号日志弹窗的历史批次能力 ──
//
// 台账 2026-10-15 第二、三条：
//   ② 历史信号没有涨跌幅：本批后端在信号生成时冻结 change_pct/prev_close；前端弹窗此前完全不
//      展示涨跌幅。锁形：有值显示 ±x.xx%、**字段缺失显示「—」**（旧批次不回填实时价，也不许
//      折叠成 0.00% 冒充平盘）、真 0 必须显示 0.00%。
//   ③ 战法筛选不跨批次：旧实现 `if (!q) return []` 让战法筛选只在「当前批次下拉」内生效，
//      用户必须一批一批手动切换才能看别的批次。锁形：只选战法（关键词为空）也必须聚合全部已
//      加载批次，且批次下拉隐藏（此时页面展示的是聚合视图）；关键词 + 战法仍要取交集。
//
// 另锁住两个配套改动：批次标签带月日（跨日后只报时分秒无法辨认）、回溯天数下拉把 days 传给
// 接口（默认 5，选 10 后必须以 10 再请求）。
//
// English: locks for the signal-log modal — frozen change pct ("—" when absent, 0.00% only when
// genuinely flat), strategy-only cross-batch aggregation, date-aware batch labels, and the
// lookback-days selector forwarding `days` to the API.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: { impl: {} } }))

const TODAY = new Date(2026, 9, 22, 10, 5, 0).getTime()
const YESTERDAY = new Date(2026, 9, 21, 14, 30, 0).getTime()
const TWO_DAYS = new Date(2026, 9, 20, 9, 15, 0).getTime()

// 三条批次（后端按最新在前返回）：今日批 3 条、昨日批 1 条、前日批 1 条。
// 今日批里刻意混入一条**没有 change_pct** 的旧口径信号，锁「缺数显示 —」。
function records() {
  return [
    {
      process_time: TODAY,
      raw_count: 12,
      signals: [
        { id: 'a', code: '600001', name: '今日龙头A', strategy: '龙头首板', direction: '做多', action: 'buy', confidence: 0.8, price: 10.5, change_pct: 1.23, sector: '半导体', reason: '贴板' },
        { id: 'b', code: '600002', name: '今日动量B', strategy: '动量观察', direction: '提醒', action: 'watch', confidence: 0.6, price: 20, sector: '机器人', reason: '放量' },
        { id: 'c', code: '600003', name: '今日平盘C', strategy: '龙头首板', direction: '做多', action: 'buy', confidence: 0.7, price: 8, change_pct: 0, sector: '电力', reason: '平盘' },
      ],
    },
    {
      process_time: YESTERDAY,
      raw_count: 8,
      signals: [
        { id: 'd', code: '600004', name: '昨日龙头D', strategy: '龙头首板', direction: '做多', action: 'buy', confidence: 0.75, price: 15, change_pct: -2.1, sector: '军工', reason: '昨日信号' },
      ],
    },
    {
      process_time: TWO_DAYS,
      raw_count: 5,
      signals: [
        { id: 'e', code: '600005', name: '前日N形E', strategy: 'N形超短', direction: '做多', action: 'buy', confidence: 0.66, price: 9, change_pct: 0.5, sector: '通信', reason: '前日信号' },
      ],
    },
  ]
}

function basePayloads() {
  return {
    fetchStageRecords: () => [],
    fetchLLMDebug: () => ({}),
    fetchSignalLogs: (days) => records(),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(basePayloads())) {
    stubs[name] = vi.fn((...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import LogModal from '../components/LogModal.jsx'

function sigSearchInput() {
  return document.querySelector('.log-search')
}
function strategySelect() {
  return document.querySelector('select[title^="按战法策略筛选"]')
}
function daysSelect() {
  return document.querySelector('select[title^="信号批次回溯范围"]')
}

async function openSignalTab() {
  render(<LogModal visible onClose={() => {}} />)
  await settle()
  fireEvent.click(screen.getByText('信号批次'))
  await settle()
}

// 与组件同口径的日期标签（MM-DD HH:mm:ss），用于断言批次下拉带月日。
function dateLabel(t) {
  const d = new Date(t)
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`
}

describe('§HISQUOTE/§LOGCROSS 信号日志弹窗', () => {
  beforeEach(() => {
    state.impl = basePayloads()
  })
  afterEach(() => cleanup())

  it('L1 单批次涨跌幅：+1.23% / 真平盘 0.00% / 旧信号缺字段显示「—」', async () => {
    await openSignalTab()
    expect(screen.getByText('+1.23%')).toBeTruthy()
    // 平盘是合法读数：必须真的显示 0.00%，不能因为"0 像缺失"被吞
    expect(screen.getByText('0.00%')).toBeTruthy()
    // 旧口径信号没有 change_pct：显示破折号，绝不允许 +0.00% 冒充平盘
    const empty = screen.getByTestId('sig-log-chg-empty')
    expect(empty.textContent).toBe('—')
    expect(document.body.textContent).not.toContain('+0.00%')
    // 负涨跌幅保留符号
    fireEvent.change(document.querySelector('.log-select'), { target: { value: '1' } })
    await settle()
    expect(screen.getByText('-2.10%')).toBeTruthy()
  })

  it('L2 只选战法（关键词为空）→ 聚合全部批次，批次下拉隐藏', async () => {
    await openSignalTab()
    // 筛选前有批次下拉（.log-select）；筛选后展示聚合视图，它必须隐藏
    expect(document.querySelectorAll('.log-select').length).toBe(1)
    fireEvent.change(strategySelect(), { target: { value: '龙头首板' } })
    await settle()
    // 今日 2 条 + 昨日 1 条 = 3 条、跨 2 个批次；前日 N 形被战法过滤掉
    expect(screen.getByText(/战法「龙头首板」共 3 条信号，跨 2 个批次/)).toBeTruthy()
    expect(screen.getByText('昨日龙头D')).toBeTruthy()
    expect(screen.queryByText('前日N形E')).toBeNull()
    expect(screen.queryByText('今日动量B')).toBeNull()
    expect(document.querySelectorAll('.log-select').length, '§LOGCROSS：聚合视图不显示单批次下拉').toBe(0)
  })

  it('L3 关键词 + 战法取交集；关键词无命中给明确空态', async () => {
    await openSignalTab()
    fireEvent.change(strategySelect(), { target: { value: '龙头首板' } })
    await settle()
    fireEvent.change(sigSearchInput(), { target: { value: '昨日' } })
    await settle()
    expect(screen.getByText(/共 1 条信号命中，跨 1 个批次/)).toBeTruthy()
    expect(screen.getByText('昨日龙头D')).toBeTruthy()
    expect(screen.queryByText('今日龙头A')).toBeNull()
    fireEvent.change(sigSearchInput(), { target: { value: '不存在的关键词' } })
    await settle()
    expect(screen.getByText(/未找到匹配项/)).toBeTruthy()
  })

  it('L4 回溯天数下拉：初始 5 天，选 10 后以 days=10 再请求', async () => {
    await openSignalTab()
    const api = await import('../api/index.js')
    const initial = api.fetchSignalLogs.mock.calls.map((c) => c[0])
    expect(initial.length).toBeGreaterThanOrEqual(1)
    expect(initial.every((d) => d === 5), `初始应全部按 5 天请求，实际 ${JSON.stringify(initial)}`).toBe(true)
    fireEvent.change(daysSelect(), { target: { value: '10' } })
    await settle()
    const days = api.fetchSignalLogs.mock.calls.map((c) => c[0])
    expect(days.filter((d) => d === 10).length, '§LOGCROSS：选 10 天后必须按 10 请求').toBe(1)
  })

  it('L5 批次标签带月日：跨天后仅时分秒无法辨认', async () => {
    await openSignalTab()
    const label = dateLabel(TODAY)
    // 最新批次排最前，标签编号 = 批次数（3）
    expect(screen.getByText(new RegExp(`^批次 3 · ${label}（`))).toBeTruthy()
  })
})
