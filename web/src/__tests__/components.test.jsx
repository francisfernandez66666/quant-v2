// ── 组件单元测试 components.test.jsx ──
// 验证通用组件的错误兜底、受控开关与空/加载占位：
// ErrorBoundary（正常渲染 / 捕获错误 / 自定义 fallback 节点与函数）、
// ToggleSw（aria 状态 / 点击回调 / disabled 禁用）、Loading / Empty 占位渲染。
// 使用 @testing-library/react 的 render / screen 进行断言。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import ErrorBoundary from '../components/ErrorBoundary.jsx'
import ToggleSw from '../components/ToggleSw.jsx'
import { Loading, Empty } from '../ui.jsx'
import MarketStatusBar from '../components/MarketStatusBar.jsx'

// §F3 抽屉测试替身：分钟图/盘口依赖网络请求，桩掉以隔离抽屉自身逻辑；行情 lookup 走受控 mock。
// English: test doubles for the drawer — MinuteView (network charts) is stubbed; fetchStockLookup is controlled.
vi.mock('../components/MinuteView.jsx', () => ({ default: ({ code }) => <div data-testid="minuteview-stub">{code}</div> }))
vi.mock('../api/index.js', () => ({ fetchStockLookup: vi.fn(() => Promise.resolve({ code: '600000', name: '浦发银行', price: 8.88 })) }))
import StockDetailDrawer from '../components/StockDetailDrawer.jsx'

describe('ErrorBoundary', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('正常渲染子组件', () => {
    render(
      <ErrorBoundary>
        <div>正常内容</div>
      </ErrorBoundary>
    )
    expect(screen.getByText('正常内容')).toBeInTheDocument()
  })

  it('捕获错误后渲染兜底 UI', () => {
    // 抛错子组件：验证 ErrorBoundary 捕获并展示兜底
    const Throw = () => {
      throw new Error('测试错误')
    }
    render(
      <ErrorBoundary>
        <Throw />
      </ErrorBoundary>
    )
    expect(screen.getByText('页面出错了，请刷新')).toBeInTheDocument()
    expect(screen.getByText('测试错误')).toBeInTheDocument()
  })

  it('自定义 fallback 为 React 节点', () => {
    // 抛错子组件：验证 fallback 节点形态
    const Throw = () => {
      throw new Error('测试错误')
    }
    render(
      <ErrorBoundary fallback={<div>自定义错误页</div>}>
        <Throw />
      </ErrorBoundary>
    )
    expect(screen.getByText('自定义错误页')).toBeInTheDocument()
  })

  it('自定义 fallback 为函数', () => {
    // 抛错子组件：验证 fallback 函数接收 err 渲染
    const Throw = () => {
      throw new Error('函数错误')
    }
    render(
      <ErrorBoundary fallback={(err) => <div>错误信息: {err.message}</div>}>
        <Throw />
      </ErrorBoundary>
    )
    expect(screen.getByText('错误信息: 函数错误')).toBeInTheDocument()
  })
})

describe('ToggleSw', () => {
  it('渲染开关按钮', () => {
    const onChange = vi.fn()
    render(<ToggleSw checked={false} onChange={onChange} />)
    const btn = screen.getByRole('switch')
    expect(btn).toBeInTheDocument()
    expect(btn).toHaveAttribute('aria-checked', 'false')
  })

  it('checked 为 true 时 aria-checked 为 true', () => {
    const onChange = vi.fn()
    render(<ToggleSw checked={true} onChange={onChange} />)
    expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'true')
  })

  it('点击回调 onChange', () => {
    const onChange = vi.fn()
    render(<ToggleSw checked={false} onChange={onChange} />)
    screen.getByRole('switch').click()
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('disabled 时点击不回调', () => {
    const onChange = vi.fn()
    render(<ToggleSw checked={false} onChange={onChange} disabled />)
    screen.getByRole('switch').click()
    expect(onChange).not.toHaveBeenCalled()
  })

  it('disabled 时有 disabled 属性', () => {
    render(<ToggleSw checked={false} onChange={vi.fn()} disabled />)
    expect(screen.getByRole('switch')).toBeDisabled()
  })
})

describe('Loading', () => {
  it('渲染默认加载文本', () => {
    render(<Loading />)
    expect(screen.getByText('加载中...')).toBeInTheDocument()
  })

  it('渲染自定义文本', () => {
    render(<Loading text="正在加载数据..." />)
    expect(screen.getByText('正在加载数据...')).toBeInTheDocument()
  })
})

describe('Empty', () => {
  it('渲染默认空态文本', () => {
    render(<Empty />)
    expect(screen.getByText('暂无数据')).toBeInTheDocument()
  })

  it('渲染自定义文本', () => {
    render(<Empty text="暂无搜索结果" />)
    expect(screen.getByText('暂无搜索结果')).toBeInTheDocument()
  })
})

describe('MarketStatusBar (§F2 市场环境条)', () => {
  it('三段全空时整条隐藏（盘前/接口失败不占位）', () => {
    const { container } = render(<MarketStatusBar env={{ emotion: '', marketState: '', maxPosPct: 0, riskTier: '' }} />)
    expect(container).toBeEmptyDOMElement()
  })
  it('渲染情绪相位', () => {
    render(<MarketStatusBar env={{ emotion: '退潮', marketState: '', maxPosPct: 0, riskTier: '' }} />)
    expect(screen.getByText('退潮')).toBeInTheDocument()
  })
  it('渲染市场状态 + 仓位档', () => {
    render(<MarketStatusBar env={{ emotion: '', marketState: 'bear', maxPosPct: 0.15, riskTier: '' }} />)
    expect(screen.getByText('熊市')).toBeInTheDocument()
    expect(screen.getByText('建议仓位≤15%')).toBeInTheDocument()
  })
  it('风险档徽标接入后展示（Red）', () => {
    render(<MarketStatusBar env={{ emotion: '冰点', marketState: 'bear', maxPosPct: 0.15, riskTier: 'Red' }} />)
    expect(screen.getByText(/系统性风险日/)).toBeInTheDocument()
  })
  it('未知情绪相位不显假值，仅隐藏该段', () => {
    const { container } = render(<MarketStatusBar env={{ emotion: '乱码相位', marketState: '', maxPosPct: 0, riskTier: '' }} />)
    expect(container).toBeEmptyDOMElement()
  })
})

describe('StockDetailDrawer (§F3 全局个股详情抽屉)', () => {
  it('关闭/无代码时不渲染', () => {
    const { container } = render(<StockDetailDrawer open={false} code="600000" onClose={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })
  it('展开时渲染代码 + 分时图 + 头部', async () => {
    render(<StockDetailDrawer open code="600000" name="浦发" onClose={() => {}} />)
    expect(screen.getByTestId('stock-detail-panel')).toBeInTheDocument()
    expect(screen.getAllByText('600000').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByTestId('minuteview-stub')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('浦发银行')).toBeInTheDocument())
  })
  it('关联列表按代码过滤：只显本标的，缺失段隐藏', () => {
    render(<StockDetailDrawer open code="600000" related={{
      signals: [{ code: '600000.SH', strategy: 'N形', action: '买入', confidence: 0.93 }, { code: '000001', strategy: '打板' }],
      messages: [{ code: '600000', level: '交易信号', body: '现价8.8' }],
    }} onClose={() => {}} />)
    expect(screen.getByText(/N形/)).toBeInTheDocument()
    expect(screen.queryByText('打板')).not.toBeInTheDocument()
    expect(screen.getByText('现价8.8')).toBeInTheDocument()
    expect(screen.queryByText('我的持仓')).not.toBeInTheDocument()
  })
  it('点关闭回调 onClose', () => {
    const onClose = vi.fn()
    render(<StockDetailDrawer open code="600000" onClose={onClose} />)
    screen.getByLabelText('关闭').click()
    expect(onClose).toHaveBeenCalled()
  })
})
