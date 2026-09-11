// ── 回测增强前端测试 backtest_enhance.test.jsx ──
// §回测自动增强 D：ParetoChart 渲染（前沿点/冠军星/推荐解/空态）、
// BacktestConfigPanel 读写（api mock）、approveOptimization 携带推荐解参数体。
// 注意：面板 describe 放最前——jsdom 下先跑组件 render 用例再测异步加载 mock 会出现
// 状态卡滞（mock 工厂函数跨用例残留交互），面板先行可稳定复现加载链路。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import * as api from '../api/index.js'
import ParetoChart from '../components/ParetoChart.jsx'
import BacktestConfigPanel from '../components/BacktestConfigPanel.jsx'

vi.mock('../api/index.js', async (importOriginal) => {
  // 仅替换面板用的两个配置端点函数；approveOptimization 等保持真实实现以断言请求体
  const actual = await importOriginal()
  return { ...actual, fetchBacktestConfig: vi.fn(), saveBacktestConfig: vi.fn() }
})

// 构造一个前沿解点（paretoPointJSON 形状）
function mkPoint(tp, wr, pf, sharpe) {
  return {
    params: { take_profit_pct: tp, stop_loss_pct: 8, hold_days: 5, min_score: 60 },
    win_rate: wr, profit_factor: pf, sharpe, calmar: 1.2, trigger_count: 88, expectancy: 2.5,
  }
}

describe('BacktestConfigPanel', () => {
  it('读取配置：enabled=true 时展开滑点/流动性/Pareto 分区', async () => {
    api.fetchBacktestConfig.mockResolvedValueOnce({ config: { enabled: true, slippage: { base_bps: 3 } }, enabled: true })
    render(<BacktestConfigPanel />)
    expect(await screen.findByText('总开关')).toBeInTheDocument()
    expect(screen.getByText('滑点成本')).toBeInTheDocument()
    expect(screen.getByText('Pareto 多目标前沿')).toBeInTheDocument()
  })

  it('无配置记录（config=null）默认折叠、只显示总开关', async () => {
    api.fetchBacktestConfig.mockResolvedValueOnce({ config: null, enabled: false })
    render(<BacktestConfigPanel />)
    expect(await screen.findByText('总开关')).toBeInTheDocument()
    expect(screen.queryByText('滑点成本')).not.toBeInTheDocument()
  })

  it('保存 PUT 透传表单配置（默认关闭时 enabled=false）', async () => {
    api.fetchBacktestConfig.mockResolvedValueOnce({ config: { enabled: false }, enabled: false })
    api.saveBacktestConfig.mockResolvedValueOnce({ status: 'saved', enabled: false })
    render(<BacktestConfigPanel />)
    await screen.findByText('总开关')
    fireEvent.click(screen.getByText('保存回测增强配置'))
    await waitFor(() => expect(api.saveBacktestConfig).toHaveBeenCalled())
    const body = api.saveBacktestConfig.mock.calls[0][0]
    expect(body.enabled).toBe(false)
    expect(body.slippage).toBeTruthy()
  })
})

describe('ParetoChart', () => {
  it('空前沿显示降级文案', () => {
    render(<ParetoChart front={[]} />)
    expect(screen.getByText(/无 Pareto 前沿数据/)).toBeInTheDocument()
  })

  it('渲染前沿点、图例与冠军星标', () => {
    const champ = { ...mkPoint(20, 45, 1.8, 1.1) }
    const front = [mkPoint(10, 40, 1.6, 0.9), mkPoint(25, 52, 2.1, 1.4), champ]
    const rec = { ...mkPoint(15, 48, 2.4, 1.2) }
    const { container } = render(<ParetoChart front={front} champion={champ} recommended={rec}
      gates={{ min_win_rate: 30, min_profit_factor: 1 }} onPick={() => {}} />)
    expect(container.querySelectorAll('circle').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('★ 当前冠军')).toBeInTheDocument()
    expect(screen.getByText('● 推荐解')).toBeInTheDocument()
    expect(screen.getByText('● Pareto 前沿')).toBeInTheDocument()
    // 冠军以五角星 path 呈现
    expect(container.querySelectorAll('path').length).toBeGreaterThanOrEqual(1)
  })

  it('点击解触发 onPick 回调', async () => {
    const onPick = vi.fn()
    const front = [mkPoint(10, 40, 1.6, 0.9), mkPoint(25, 52, 2.1, 1.4)]
    const { container } = render(<ParetoChart front={front} onPick={onPick} />)
    fireEvent.click(container.querySelector('circle'))
    expect(onPick).toHaveBeenCalledTimes(1)
    expect(onPick.mock.calls[0][0].win_rate).toBe(40)
  })

  it('悬停显示指标明细条', () => {
    const front = [mkPoint(10, 40.5, 1.6, 0.9)]
    const { container } = render(<ParetoChart front={front} />)
    fireEvent.mouseEnter(container.querySelector('circle'))
    expect(screen.getByText(/胜率 40.5%/)).toBeInTheDocument()
  })
})

describe('api - approveOptimization 请求体', () => {
  let realFetch
  beforeEach(() => {
    realFetch = global.fetch
    global.fetch = vi.fn()
    localStorage.setItem('liangzai_token', 't')
  })
  afterEach(() => { global.fetch = realFetch; localStorage.clear() })

  function jsonReply(body) {
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) })
  }

  it('不带参数 = 无请求体（旧行为）', async () => {
    jsonReply({ status: 'ok' })
    await api.approveOptimization(7)
    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toContain('/api/research/optimizations/7/approve')
    expect(opts.method).toBe('POST')
    expect(opts.body).toBeUndefined()
  })

  it('携带推荐解参数体 {params}', async () => {
    jsonReply({ status: 'ok' })
    const p = { take_profit_pct: 15, stop_loss_pct: 6, hold_days: 4, min_score: 55 }
    await api.approveOptimization(7, p)
    const [, opts] = global.fetch.mock.calls[0]
    expect(JSON.parse(opts.body)).toEqual({ params: p })
  })
})
