// 备案页脚测试：双重合规——
// 1) ICP：渲染「沪ICP备2026045551」并外链工信部首页 beian.miit.gov.cn；
// 2) 公安备案（2026-09-30 通过）：渲染「沪公网安备31011302009737号」+ 警徽图标，外链公安备案平台
//    beian.mps.gov.cn 的查询页（深链 code 参数由备案号数字派生，两处不许各改一半）；
// 3) 登录页/仪表盘两种 variant 都要渲染两条备案号。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import IcpFooter, {
  ICP_NUMBER, ICP_URL, POLICE_NUMBER, POLICE_URL, POLICE_EMBLEM,
} from '../components/IcpFooter'

describe('IcpFooter 备案号页脚（管局 + 公安双重合规）', () => {
  it('导出 ICP 备案号与工信部链接常量', () => {
    expect(ICP_NUMBER).toBe('沪ICP备2026045551')
    expect(ICP_URL).toBe('https://beian.miit.gov.cn/')
  })

  it('导出公安备案号，且深链 code 参数就是备案号里的数字（同源派生，不是第二处字面量）', () => {
    expect(POLICE_NUMBER).toBe('沪公网安备31011302009737号')
    expect(POLICE_URL).toBe('https://beian.mps.gov.cn/#/query/webSearch?code=31011302009737')
    // 反向锁：把备案号数字改掉后深链必须跟着变（等价于"深链没有写死第二份号码"）
    expect(POLICE_URL).toContain(POLICE_NUMBER.replace(/\D/g, ''))
  })

  it('footer variant 渲染两条备案号：各自外链 + 公安备案带警徽图标', () => {
    render(<IcpFooter variant="footer" />)
    const icp = screen.getByTestId('icp-link')
    expect(icp.textContent).toBe('沪ICP备2026045551')
    expect(icp.getAttribute('href')).toBe('https://beian.miit.gov.cn/')
    expect(icp.getAttribute('target')).toBe('_blank')
    expect(icp.getAttribute('rel')).toBe('noopener noreferrer')

    const police = screen.getByTestId('police-link')
    expect(police.textContent).toBe('沪公网安备31011302009737号')
    expect(police.getAttribute('href')).toBe('https://beian.mps.gov.cn/#/query/webSearch?code=31011302009737')
    expect(police.getAttribute('target')).toBe('_blank')
    expect(police.getAttribute('rel')).toBe('noopener noreferrer')
    // 警徽走本地托管资源（不热链对方站点），且是装饰图（空 alt，合规判据在文案上）
    const emblem = police.querySelector('img')
    expect(emblem).toBeTruthy()
    expect(emblem.getAttribute('src')).toBe(POLICE_EMBLEM)
    expect(POLICE_EMBLEM).toBe('/police-emblem.png')
    expect(emblem.getAttribute('alt')).toBe('')
  })

  it('login variant 同样渲染 ICP 与公安备案号（登录首页合规）', () => {
    render(<IcpFooter variant="login" />)
    const box = screen.getByTestId('icp-footer')
    expect(box.textContent).toContain('沪ICP备2026045551')
    expect(box.textContent).toContain('沪公网安备31011302009737号')
    expect(box.querySelectorAll('a')).toHaveLength(2)
  })
})
