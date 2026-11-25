// dynamicPosition.js — 当前时段「已占用动态仓位」百分比的唯一计算源（2026-11-25 owner 裁决）。
//
// 口径：**当前战略仓位 ÷ 当前动态目标仓位**（不是占总资产）。
//   · 当前战略仓位 = 底仓 + 在途买单 − 正T买腿 + 反T卖腿，来自资金帧 `cur_strat`；
//   · 当前动态目标 = 总资产 × min(MS×TM, 天花板) × ramp，来自资金帧 `target`。
// 因此读数回答的是「本时段批给动态仓位的额度用了多少」：100% 正好用满，
// 超过 100% 表示现有战略仓位已经超出本时段目标，需要靠卖出/T 腿回收，而不是继续加仓。
//
// 为什么不放 0：分母缺失/非法时返回 null，调用方画「—」；0% 是"当前真的一分钱没占"，
// 和"没有这一帧/还没定权"是两件事——这是本仓资金卡一贯的缺数纪律。
//
// English: the single source of truth for "how much of the current dynamic budget is used".
// Returns null (not 0) when either leg is missing or invalid.

/**
 * dynamicUsedPct 当前已占用动态仓位百分比（数值，单位 %）。
 * @param {object|null|undefined} funds /api/funds/today 的 live.funds
 * @returns {number|null} null=弃权（缺帧/缺数/非法/目标≤0），否则为 cur_strat/target×100
 */
export function dynamicUsedPct(funds) {
  if (!funds || typeof funds !== 'object') return null
  const cur = Number(funds.cur_strat)
  const target = Number(funds.target)
  if (!Number.isFinite(cur) || !Number.isFinite(target) || target <= 0) return null
  return (cur / target) * 100
}

/**
 * formatDynamicUsedPct 展示层文案：一位小数 + %；缺数画「—」。
 * 不在这里夹到 100%：超买就是超买，夹掉会把"已经超额"伪装成"刚好用满"。
 */
export function formatDynamicUsedPct(funds) {
  const pct = dynamicUsedPct(funds)
  return pct === null ? '—' : `${pct.toFixed(1)}%`
}

const num2 = (v) => v.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })

/**
 * formatDynamicUsedProcess 把算式本身也摆出来：当前战略 ÷ 本时段目标 = 百分比。
 * 只给结果会让人怀疑分母选错（占总资产还是占目标）；把两腿都画出来，
 * 使用者当场就能复核"171.4% 是 60000/35000 来的，不是 60000/100000"。
 */
export function formatDynamicUsedProcess(funds) {
  if (!funds || typeof funds !== 'object') return '—'
  const cur = Number(funds.cur_strat)
  const target = Number(funds.target)
  const pct = dynamicUsedPct(funds)
  if (pct === null) return '—'
  return `${num2(cur)} ÷ ${num2(target)} = ${pct.toFixed(1)}%`
}

/**
 * formatTargetProcess 展示动态目标是怎么来的（2026-11-25 owner 追加）。
 *
 *	target = 总资产 × min(MS × TM, 天花板) × 分时 ramp
 *
 * 四个因子必须与后端 fundsTargetCurRoomWithScales 同源（同帧下发）：
 *   total_asset / market_scale / target_multiplier / target_ceiling / target_ramp。
 * 任一因子缺失或非法 ⇒ 画「—」，绝不用 1.0 猜——那会把"没数据"伪装成"没有缩放"。
 */
export function formatTargetProcess(funds) {
  if (!funds || typeof funds !== 'object') return '—'
  const total = Number(funds.total_asset)
  const ms = Number(funds.market_scale)
  const tm = Number(funds.target_multiplier)
  const cap = Number(funds.target_ceiling)
  const ramp = Number(funds.target_ramp)
  if (![total, ms, tm, cap, ramp].every((v) => Number.isFinite(v)) || total <= 0 || cap <= 0) return '—'
  const eff = Math.min(ms * tm, cap)
  return `${num2(total)} × min(${ms.toFixed(3)} × ${tm.toFixed(2)}, ${cap.toFixed(2)}) × ${ramp.toFixed(2)} = ${num2(total * eff * ramp)}`
}
