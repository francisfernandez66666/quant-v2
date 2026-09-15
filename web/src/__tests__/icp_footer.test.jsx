// IcpFooter 备案页脚测试：管局合规（沪ICP备2026045551）——
// 1) 渲染备案号文本；2) 外链指向工信部首页 beian.miit.gov.cn；3) 登录页/仪表盘两种 variant 都渲染。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import IcpFooter, { ICP_NUMBER, ICP_URL } from '../components/IcpFooter'

describe('IcpFooter 备案号页脚（管局合规）', () => {
  it('导出备案号与工信部链接常量', () => {
    expect(ICP_NUMBER).toBe('沪ICP备2026045551')
    expect(ICP_URL).toBe('https://beian.miit.gov.cn/')
  })

  it('footer variant 渲染备案号文本并链接工信部首页', () => {
    const { container } = render(<IcpFooter variant="footer" />)
    const link = screen.getByTestId('icp-footer').querySelector('a')
    expect(link).toBeTruthy()
    expect(link.textContent).toBe('沪ICP备2026045551')
    expect(link.getAttribute('href')).toBe('https://beian.miit.gov.cn/')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toBe('noopener noreferrer')
    expect(container.textContent).toContain('沪ICP备2026045551')
  })

  it('login variant 同样渲染备案号（登录首页合规）', () => {
    render(<IcpFooter variant="login" />)
    expect(screen.getByTestId('icp-footer').textContent).toContain('沪ICP备2026045551')
  })
})
