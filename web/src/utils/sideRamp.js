// sideRamp.js — D7（2026-11-25）动态仓位分时释放的**唯一文案源**。
//
// 为什么单开一个纯函数而不是写在卡片里：目标仓位那行的 hint 已经有 MS，
// 再加一句释放档位就会变成两处各拼一句话（一处说「35%」、另一处说「熊市上午」），
// 读的人无法判断它们是不是同一件事。数值只认后端给的 `target_ramp`，
// 前端**不持有任何档位阈值**（熊市上午 0.30、下午 0.50、牛市上午 0.70、满额 1.00 全住在 carrier_knobs.go）。
//
// 关闭时（`ramp_off`）与满额后（`ramp_full`）一律返回空串 ⇒ 卡片不渲染这一行：
// 这两种状态下 D7 对目标仓位没有约束，写一句话只会让人以为仓位还被压着。
//
// English: the single copy source for D7's time-of-day release readout. Values come
// from the frame (`target_ramp`); this file owns no thresholds.

const AM_SUFFIX = '，13:00 无坏信号升至下午档，14:00 满额';
const PM_SUFFIX = '，14:00 满额';

/**
 * sideRampHint 把资金帧的 target_ramp / target_ramp_src 拼成一行小字。
 * @param {object|null|undefined} funds /api/funds/today 的 live.funds
 * @returns {string} 空串＝不渲染（D7 关闭 / 已满额 / 读数缺失）
 */
/** rampModeLabel 只给档位名，不加时间线后缀；供"时段开放比例"那行复用。 */
export function rampModeLabel(src) {
  const s = String(src || '')
  if (s === 'ramp_bear_am') return '熊市上午档'
  if (s === 'ramp_bear_pm') return '熊市下午档'
  if (s === 'ramp_bull_am') return '牛市上午档'
  if (s === 'ramp_bull_pm') return '牛市下午档'
  return '满额档'
}

export function sideRampHint(funds) {
  const src = String((funds && funds.target_ramp_src) || '');
  const ramp = Number(funds && funds.target_ramp);
  // 只有四档「正在压制」的格子才出文案：bull/bear × am/pm。
  if (!/^ramp_(bull|bear)_(am|pm)$/.test(src)) return '';
  if (!Number.isFinite(ramp) || ramp <= 0 || ramp > 1) return '';
  const pct = Math.round(ramp * 100);
  const mode = src.indexOf('_bull_') >= 0 ? '牛市' : '熊市';
  const half = src.endsWith('_am') ? '上午' : '下午';
  const tail = src.endsWith('_am') ? AM_SUFFIX : PM_SUFFIX;
  return `今日按 ${pct}% 释放（${mode}${half}档）${tail}`;
}
