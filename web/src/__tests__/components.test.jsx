// ── 组件单元测试 components.test.jsx ──
// 验证通用组件的错误兜底、受控开关与空/加载占位：
// ErrorBoundary（正常渲染 / 捕获错误 / 自定义 fallback 节点与函数）、
// ToggleSw（aria 状态 / 点击回调 / disabled 禁用）、Loading / Empty 占位渲染。
// 使用 @testing-library/react 的 render / screen 进行断言。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import ErrorBoundary from '../components/ErrorBoundary.jsx'
import ToggleSw from '../components/ToggleSw.jsx'
import { Loading, Empty } from '../ui.jsx'

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
