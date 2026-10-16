// quantVerdicts.js — §SELLPOINT-UNIFY（2026-09-21）信号裁定留痕卡的展示映射（纯函数，供 vitest 回归锁）。
// 背景：卖出裁决（signalctl stage=sell_discipline）与买入准入裁定共用同一条留痕环
// （GET /api/signalctl/verdicts）。旧卡片把 verdict 写死为买入口径（block→拦截、其余→待确认），
// 卖出「处置（pass）」会被误显示成"待确认"。本文件按环节（stage）分层翻译：
// 卖出 pass=处置、hold=观察窗预警；买入 block=拦截（影子命中带"影子"前缀）、hold=待确认。
// English: stage-aware label mapping for the verdict audit card — sell pass/hold must not
// render under the buy-side "block/hold" vocabulary.
export const STAGE_SELL = 'sell_discipline'

// STAGE_SIZING §DYN-SIZE（2026-10-16）动态仓位硬拦环节：命中即把买入降级为 hold
// （情绪冰点 / 风险档 Red / 个股 LLM 利空且风险档≥Yellow / 连续止损熔断 / 长假前最后一日禁开仓）。
export const STAGE_SIZING = 'sizing'

// verdictDisplay 把一条裁定留痕翻译为 { 环节, 裁定文案, Tag 主题 }。
export function verdictDisplay(row) {
  const v = row && row.verdict
  if (row && row.stage === STAGE_SELL) {
    if (v === 'pass') return { stageLabel: '卖出', label: row.shadow ? '影子处置' : '处置', theme: 'danger' }
    if (v === 'hold') return { stageLabel: '卖出', label: '观察窗预警', theme: 'warning' }
    if (v === 'block') return { stageLabel: '卖出', label: '卖出拦截', theme: 'danger' }
    return { stageLabel: '卖出', label: v || '—', theme: 'default' }
  }
  // §DYN-SIZE 定权环节：与"待确认"区分开——待确认是持续性探针还没满窗，定权硬拦是风险因子命中。
  if (row && row.stage === STAGE_SIZING) {
    if (v === 'hold') return { stageLabel: '买入', label: '定权硬拦', theme: 'danger' }
    if (v === 'block') return { stageLabel: '买入', label: '定权拦截', theme: 'danger' }
    return { stageLabel: '买入', label: v === 'pass' ? '放行' : (v || '—'), theme: 'success' }
  }
  if (v === 'block') return { stageLabel: '买入', label: row && row.shadow ? '影子拦截' : '拦截', theme: 'danger' }
  if (v === 'hold') return { stageLabel: '买入', label: '待确认', theme: 'warning' }
  return { stageLabel: '买入', label: '放行', theme: 'success' }
}

// sizingDisplay §DYN-SIZE 把裁定的 buy_scale / sell_boost / sizing_reasons 翻译为 { 文案, 主题, 提示 }。
// 无缩放（未定权 / 中性 1.0）→ label 为空串（表格该列渲染「—」）。
// 提示串由命中的因子组成（如「情绪相位×0.50 · 大盘跌幅×0.70」），鼠标悬停可看为何缩/加。
// §DYN-SIZE P2 卖出加权：卖出裁决行挂 sell_boost（L1/L2/L3），该列显示「卖出加权 L2」，
// 悬停看触发条件（情绪相位=退潮 · 风险档=Red …）。
export function sizingDisplay(row) {
  const reasons = (row && row.sizing_reasons) || []
  const tip = reasons.join(' · ')
  const boost = (row && row.sell_boost) || ''
  if (boost) {
    const shadow = row && row.shadow ? '影子' : ''
    return { label: `${shadow}卖出加权 ${boost}`, theme: 'danger', tip }
  }
  const s = row && typeof row.buy_scale === 'number' ? row.buy_scale : 0
  if (!s || s === 1) return { label: '', theme: 'default', tip }
  const pct = Math.round(s * 100)
  if (s > 1) return { label: `加仓 ${pct}%`, theme: 'primary', tip }
  if (s >= 0.85) return { label: `微缩 ${pct}%`, theme: 'warning', tip }
  return { label: `缩仓 ${pct}%`, theme: 'warning', tip }
}
