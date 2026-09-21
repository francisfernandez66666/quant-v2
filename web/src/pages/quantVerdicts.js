// quantVerdicts.js — §SELLPOINT-UNIFY（2026-09-21）信号裁定留痕卡的展示映射（纯函数，供 vitest 回归锁）。
// 背景：卖出裁决（signalctl stage=sell_discipline）与买入准入裁定共用同一条留痕环
// （GET /api/signalctl/verdicts）。旧卡片把 verdict 写死为买入口径（block→拦截、其余→待确认），
// 卖出「处置（pass）」会被误显示成"待确认"。本文件按环节（stage）分层翻译：
// 卖出 pass=处置、hold=观察窗预警；买入 block=拦截（影子命中带"影子"前缀）、hold=待确认。
// English: stage-aware label mapping for the verdict audit card — sell pass/hold must not
// render under the buy-side "block/hold" vocabulary.
export const STAGE_SELL = 'sell_discipline'

// verdictDisplay 把一条裁定留痕翻译为 { 环节, 裁定文案, Tag 主题 }。
export function verdictDisplay(row) {
  const v = row && row.verdict
  if (row && row.stage === STAGE_SELL) {
    if (v === 'pass') return { stageLabel: '卖出', label: row.shadow ? '影子处置' : '处置', theme: 'danger' }
    if (v === 'hold') return { stageLabel: '卖出', label: '观察窗预警', theme: 'warning' }
    if (v === 'block') return { stageLabel: '卖出', label: '卖出拦截', theme: 'danger' }
    return { stageLabel: '卖出', label: v || '—', theme: 'default' }
  }
  if (v === 'block') return { stageLabel: '买入', label: row && row.shadow ? '影子拦截' : '拦截', theme: 'danger' }
  if (v === 'hold') return { stageLabel: '买入', label: '待确认', theme: 'warning' }
  return { stageLabel: '买入', label: '放行', theme: 'success' }
}
