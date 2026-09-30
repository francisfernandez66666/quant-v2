// ── 备案页脚 IcpFooter.jsx ──
// 管局要求（2026-09，沪ICP备2026045551）：网站首页底部展示备案号，并链接到工信部首页（https://beian.miit.gov.cn/）。
// 公安备案要求（2026-09-30 通过，沪公网安备31011302009737号）：同一页脚展示警徽图标 + 公安备案号，
// 并链接到全国互联网安全管理服务平台（https://beian.mps.gov.cn/）的备案查询页。
// 登录页（未登录首页）与仪表盘页脚（登录后首页）两处引用同一组件，集中管理避免漂移。
// English: the ICP + public-security beian footer required by the regulators — shown at the bottom of
// the home pages (login page + dashboard footer), linking to beian.miit.gov.cn and beian.mps.gov.cn.
import React from 'react'

// ICP 备案号与工信部链接（管局合规固定文案，勿随意改动）
export const ICP_NUMBER = '沪ICP备2026045551'
export const ICP_URL = 'https://beian.miit.gov.cn/'

// 公安备案号（合规固定文案，与备案回执逐字一致；改这里即可，下面的深链自动跟着变）
export const POLICE_NUMBER = '沪公网安备31011302009737号'
// 深链的 code 参数就是备案号里的纯数字：从文案里抽出来，避免"两处字面量各改一半"的漂移形态
const POLICE_CODE = POLICE_NUMBER.replace(/\D/g, '')
// 公安备案查询页（平台首页为 beian.mps.gov.cn，带 code 参数可直达本条备案记录）
export const POLICE_URL = `https://beian.mps.gov.cn/#/query/webSearch?code=${POLICE_CODE}`
// 警徽图标：从 beian.mps.gov.cn 取官方资源后本地托管（web/public/，构建时随 dist 根一起产出/打包进 APK），
// 不热链对方站点——对方是哈希文件名 + SPA 兜底页，热链随时可能变成一张破图。
export const POLICE_EMBLEM = '/police-emblem.png'

/**
 * @param {{variant?: 'footer'|'login'}} [props]
 */
// 备案外链：备案号文案（公安备案额外带警徽图标）+ 新标签打开的备案平台链接，悬停下划线。
function BeianLink({ testid, href, label, icon }) {
  return (
    <a data-testid={testid} href={href} target="_blank" rel="noopener noreferrer"
      style={{ color: 'inherit', textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 3 }}
      onMouseOver={(e) => { e.currentTarget.style.textDecoration = 'underline' }}
      onMouseOut={(e) => { e.currentTarget.style.textDecoration = 'none' }}
    >
      {/* 图标只是装饰，合规判据是"文案 + 可点击外链"，图挂了不影响备案号展示 */}
      {icon ? <img src={icon} alt="" aria-hidden="true" style={{ width: 14, height: 14, display: 'block' }} /> : null}
      <span>{label}</span>
    </a>
  )
}

// 备案页脚：ICP 备案号 + 公安备案号两条外链（管局与公安双重合规）。
export default function IcpFooter({ variant = 'footer' }) {
  const style = variant === 'login'
    ? { marginTop: 10, fontSize: 11, textAlign: 'center', color: 'var(--app-muted-2)' }
    : { marginTop: 4, padding: '2px 0 6px', textAlign: 'center', fontSize: 11, color: 'var(--app-muted-2)' }
  return (
    <div data-testid="icp-footer" style={style}>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', justifyContent: 'center' }}>
        <BeianLink testid="icp-link" href={ICP_URL} label={ICP_NUMBER} />
        <span aria-hidden="true">|</span>
        <BeianLink testid="police-link" href={POLICE_URL} label={POLICE_NUMBER} icon={POLICE_EMBLEM} />
      </span>
    </div>
  )
}
