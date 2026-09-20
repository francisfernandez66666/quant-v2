// ── Markdown 渲染器测试 ──
// §CONSULT-UX(20260921)：咨询回复改为渲染 Markdown，验证结构可见（标题/列表）、行内格式、
// 数字原样保留，且不使用 dangerouslySetInnerHTML（杜绝 XSS）。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import Markdown from '../components/Markdown.jsx'

describe('Markdown 渲染器（咨询回复）', () => {
  it('## 标题渲染为 h3，### 渲染为 h4', () => {
    const { container } = render(<Markdown text={'## 结论\n### 细项'} />)
    expect(container.querySelector('h3')?.textContent).toBe('结论')
    expect(container.querySelector('h4')?.textContent).toBe('细项')
  })

  it('- 项目符号渲染为 ul/li', () => {
    const { container } = render(<Markdown text={'- 量价\n- 资金'} />)
    const ul = container.querySelector('ul')
    expect(ul).toBeTruthy()
    expect(ul.querySelectorAll('li')).toHaveLength(2)
  })

  it('1. 有序列表渲染为 ol/li', () => {
    const { container } = render(<Markdown text={'1. 第一\n2. 第二'} />)
    const ol = container.querySelector('ol')
    expect(ol).toBeTruthy()
    expect(ol.querySelectorAll('li')).toHaveLength(2)
  })

  it('**粗体** 与 *斜体* 正确解析', () => {
    const { container } = render(<Markdown text={'**重要** 和 *强调*'} />)
    expect(container.querySelector('strong')?.textContent).toBe('重要')
    expect(container.querySelector('em')?.textContent).toBe('强调')
  })

  it('数字与单位原样保留（不得被改写或吞掉）', () => {
    render(<Markdown text={'成交量 16119800股，主力净流入 -22200.00万元'} />)
    expect(screen.getByText(/16119800股/)).toBeInTheDocument()
    expect(screen.getByText(/-22200\.00万元/)).toBeInTheDocument()
  })

  it('不使用 dangerouslySetInnerHTML（XSS 安全）', () => {
    const { container } = render(<Markdown text={'<img src=x onerror=alert(1)> 普通文本'} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toContain('<img src=x onerror=alert(1)>')
  })
})
