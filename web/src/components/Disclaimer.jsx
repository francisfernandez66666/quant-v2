// ── §F6 免责声明 Disclaimer.jsx ──
// 合规文案（UAT 4.1）：登录页底部 / 仪表盘页脚 / 咨询回答尾注三处引用同一份文案，集中管理避免各处漂移。
// `variant` 控制展示密度：login 完整成段，footer/inline 一行小字。
// English: §F6 compliance disclaimer (UAT 4.1) — one canonical text reused by the login footer, dashboard
// footer and consult answer tail so wording never drifts across places. `variant` tunes density.
import React from 'react'

export const DISCLAIMER_TEXT =
  '本平台为量化研究与交易辅助工具，所有信号、评分、资讯与 AI 回答均由模型自动生成，仅供研究参考，'
  + '不构成任何投资建议或买卖要约。证券市场有风险，据此操作风险自负，请独立判断并自行承担后果。'

/**
 * @param {{variant?: 'login'|'footer'|'inline'}} [props]
 */
export default function Disclaimer({ variant = 'inline' }) {
  if (variant === 'login') {
    return (
      <p data-testid="disclaimer-login" style={{ marginTop: 18, fontSize: 11, lineHeight: 1.6, color: 'var(--app-muted-2)', textAlign: 'center' }}>
        {DISCLAIMER_TEXT}
      </p>
    )
  }
  if (variant === 'footer') {
    return (
      <div data-testid="disclaimer-footer" className="muted" style={{ marginTop: 16, padding: '10px 4px', textAlign: 'center', fontSize: 11, lineHeight: 1.6 }}>
        {DISCLAIMER_TEXT}
      </div>
    )
  }
  return (
    <div data-testid="disclaimer-inline" style={{ marginTop: 8, fontSize: 11, color: 'var(--app-muted-2)' }}>
      ⚠ 以上为模型自动生成内容，仅供研究参考，不构成投资建议，据此操作风险自负。
    </div>
  )
}
