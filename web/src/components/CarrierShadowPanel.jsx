// CarrierShadowPanel.jsx — §CARRIER-SHADOW（2026-11-05）载体影子对照的读数面板。
//
// 这块屏只回答 owner 那枚「武装」按钮前面的一个问题：**每枚移植规则与引擎对照跑到第几天了**。
// 数据来自 GET /api/qmt/state 的 carrier_shadow 键（真源 internal/server/carrier_shadow.go，
// 那张表里的四种状态是唯一口径，本文件只按值渲染，判据文案在 utils/carrierShadow.js 单点）。
//
// 三条不许被顺手优化掉的边界：
//  ① known=false ⇒ 整块只显示「读数未知」，**绝不显示 0 天或空表**——
//     "0/5" 会被读成"跑了但一条都没命中"，而真相是我们不知道（missing≠empty，
//     与 J0「零回包不算健康」、carrier 面板那条「不许出现已是最新」同族）；
//  ② 三种"零"必须各显示各的（未上报 / 零命中 / 分歧）：把它们折成一个数字，
//     5 日窗口就会被一串静默日自己凑满，而这正是这枚开关存在的理由；
//  ③ 纯读数：本组件不发任何请求、不含任何"去武装/去启用"的动作——
//     要不要按下那枚按钮是 owner 的决定，我们只负责把证据摆全（§OBS-GUARD 同取向）。
// English: read-only shadow-comparison panel; unknown is never rendered as zero days, and the
// three different kinds of "zero" stay three separate columns.
import React from 'react'
import { Tag } from 'tdesign-react'
import { shadowDayText, windowProgress, kindRow } from '../utils/carrierShadow.js'

// DayCells 把一类规则的窗口逐日摊成一排格子：一眼能看出"缺的是哪天、缺的是哪一种"。
function DayCells({ days }) {
  return (
    <span style={{ display: 'inline-flex', gap: 4, flexWrap: 'wrap' }}>
      {(days || []).map((d) => {
        const copy = shadowDayText(d.state)
        return (
          <Tag key={d.day} size="small" variant="light-outline" theme={copy.theme}
               title={`${d.day} ${copy.text}（载体 ${d.carrier_rows || 0} 条 / 引擎认 ${d.engine_agreed || 0} 条 / 该日总报 ${d.reported_rows || 0} 行）`}>
            {String(d.day).slice(-4)}·{d.state}
          </Tag>
        )
      })}
    </span>
  )
}

export default function CarrierShadowPanel({ shadow }) {
  const progress = windowProgress(shadow)
  if (!progress.known) {
    return (
      <div style={{ marginTop: 6 }}>
        <Tag size="small" variant="light-outline" theme="warning">影子对照：{progress.text}</Tag>
      </div>
    )
  }
  const rows = progress.kinds.map(kindRow)
  return (
    <div style={{ marginTop: 6, display: 'flex', flexDirection: 'column', gap: 4 }}>
      <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <Tag size="small" variant="light-outline" theme="primary">
          影子对照（截至 {shadow.as_of_day}，窗口 {shadow.window_days} 个交易日）
        </Tag>
        <span>{progress.text}</span>
        {/* 整根腿有没有在报数，看这一列：它把"载体没跑"与"跑了没命中"分开（缺这行就得靠猜） */}
        <span>已上报 {shadow.days_reported || 0} 天 / 需要 {shadow.required_days || 5} 天</span>
      </div>
      {rows.length === 0 ? (
        <div>
          <Tag size="small" variant="light-outline" theme="default">
            还没有任何一类规则的影子判定（这是"没数据"，不是"一致"）
          </Tag>
        </div>
      ) : (
        rows.map((r) => (
          <div key={r.kind} style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
            <Tag size="small" variant="light-outline" theme={r.filled ? 'success' : 'default'}>
              {r.filled ? '窗口已满' : '窗口未满'}
            </Tag>
            <span>{r.text}</span>
            <DayCells days={progress.kinds.find((k) => k.kind === r.kind).days} />
          </div>
        ))
      )}
    </div>
  )
}
