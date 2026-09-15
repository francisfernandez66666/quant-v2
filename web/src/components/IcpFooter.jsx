// ── ICP 备案页脚 IcpFooter.jsx ──
// 管局要求（2026-09，沪ICP备2026045551）：网站首页底部展示备案号，
// 并链接到工信部首页（https://beian.miit.gov.cn/）。
// 登录页（未登录首页）与仪表盘页脚（登录后首页）两处引用同一组件，集中管理避免漂移。
// English: the ICP beian footer required by the regulator — shown at the bottom of the home pages
// (login page + dashboard footer), linking to beian.miit.gov.cn.
import React from 'react'

// 备案号与工信部链接（管局合规固定文案，勿随意改动）
export const ICP_NUMBER = '沪ICP备2026045551'
export const ICP_URL = 'https://beian.miit.gov.cn/'

/**
 * @param {{variant?: 'footer'|'login'}} [props]
 */
// ICP 备案号页脚：备案号文本 + 工信部首页外链（新标签打开）。
export default function IcpFooter({ variant = 'footer' }) {
  const style = variant === 'login'
    ? { marginTop: 10, fontSize: 11, textAlign: 'center', color: 'var(--app-muted-2)' }
    : { marginTop: 4, padding: '2px 0 6px', textAlign: 'center', fontSize: 11, color: 'var(--app-muted-2)' }
  return (
    <div data-testid="icp-footer" style={style}>
      <a href={ICP_URL} target="_blank" rel="noopener noreferrer"
        style={{ color: 'inherit', textDecoration: 'none' }}
        onMouseOver={(e) => { e.currentTarget.style.textDecoration = 'underline' }}
        onMouseOut={(e) => { e.currentTarget.style.textDecoration = 'none' }}
      >{ICP_NUMBER}</a>
    </div>
  )
}
