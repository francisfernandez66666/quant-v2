// §MARKET_RISK_GATE F2 市场环境条 —— 全站唯一的"当前市场环境"展示位。
// 数据来自 SSE `score` 广播（后端 scoring_loop 每轮推送 emotion/market_state/max_pos_pct/risk_tier）。
// 三段：① 情绪相位（涨停池+真实涨跌广度纠偏后的六阶段，配色区分冷热）；② 市场状态机 bull/range/bear
// + 建议仓位档；③ 风险档红/黄徽标（B3 合成器接入前恒空，按空隐藏，不占位）。
// 任一维度缺失（盘前/接口失败/开关未开）即该段弃权隐藏，绝不显示假值或"?"占位误导。
// English: the F2 single source-of-truth environment bar fed by the SSE `score` broadcast. Three segments:
// emotion phase (limit-up pool corrected by real breadth), market state machine (bull/range/bear + suggested
// position cap), and a red/yellow risk-tier badge (hidden until the B3 synthesizer fills it). Each segment
// hides itself when its input is missing (pre-open / fetch failure / toggle off) — never a fake value or placeholder.
import React from 'react'
import { Tag } from 'tdesign-react'

// 情绪相位 → {颜色, 简述}（A股习惯：冰点最冷偏蓝、高潮最热偏红；退潮/背离为转弱信号）
const EMOTION_STYLE = {
  冰点: { color: '#1e6091', desc: '情绪冰点·观望为主' },
  启动: { color: '#2ba471', desc: '情绪启动' },
  发酵: { color: 'var(--td-warning-color)', desc: '情绪发酵·赚钱效应扩散' },
  高潮: { color: 'var(--app-up)', desc: '情绪高潮·注意兑现' },
  背离: { color: '#834ec2', desc: '指数与情绪背离·防回落' },
  退潮: { color: '#6b7785', desc: '情绪退潮·控制回撤' },
}

// 市场状态 → {颜色, 中文名}（bull 红/range 灰/bear 蓝，与 A股涨跌色一致）
const STATE_STYLE = {
  bull: { color: 'var(--app-up)', label: '牛市' },
  range: { color: 'var(--td-warning-color)', label: '震荡' },
  bear: { color: '#1e6091', label: '熊市' },
}

const RISK_STYLE = {
  Red: { color: 'var(--app-up)', bg: 'var(--app-warn-bg)', label: '系统性风险日·做多收紧' },
  Yellow: { color: 'var(--td-warning-color)', bg: '#fff3e0', label: '警惕日·买入从严' },
}

export default function MarketStatusBar({ env }) {
  if (!env) return null
  const emotion = (env.emotion || '').trim()
  const state = (env.marketState || '').trim()
  const risk = (env.riskTier || '').trim()
  const eStyle = emotion ? EMOTION_STYLE[emotion] : null
  const sStyle = state ? STATE_STYLE[state] : null
  const rStyle = risk ? RISK_STYLE[risk] : null
  const maxPos = Number(env.maxPosPct || 0)
  const reasons = Array.isArray(env.riskReasons) ? env.riskReasons : []

  // 三段全缺失（盘前未算 / 各开关未开）→ 整条隐藏，不留空条占位
  if (!eStyle && !sStyle && !rStyle) return null

  return (
    <div
      style={{
        display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 12,
        padding: '6px 16px', fontSize: 13, lineHeight: '20px',
        background: rStyle ? rStyle.bg : 'var(--app-surface-2)', borderBottom: '1px solid #e5e8ef',
      }}
    >
      {/* ① 风险档徽标（最醒目，红/黄；B3 接入前隐藏） */}
      {rStyle && (
        <Tag size="small" style={{ background: rStyle.color, color: '#fff', border: 'none', fontWeight: 600 }}>
          ⚠ 风险档 {risk}
        </Tag>
      )}
      {rStyle && <span style={{ color: rStyle.color, fontWeight: 600 }}>{rStyle.label}{reasons.length ? '：' + reasons.join('、') : ''}</span>}

      {/* ② 情绪相位 */}
      {eStyle && (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <i style={{ width: 8, height: 8, borderRadius: '50%', background: eStyle.color, display: 'inline-block' }} />
          <span style={{ color: eStyle.color, fontWeight: 600 }}>{emotion}</span>
          <span style={{ color: 'var(--app-text-2)' }}>{eStyle.desc}</span>
        </span>
      )}

      {/* ③ 市场状态 + 建议仓位档 */}
      {sStyle && (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <span style={{ color: sStyle.color, fontWeight: 600 }}>{sStyle.label}</span>
          {maxPos > 0 && <span style={{ color: 'var(--app-text-2)' }}>建议仓位≤{Math.round(maxPos * 100)}%</span>}
        </span>
      )}
    </div>
  )
}
