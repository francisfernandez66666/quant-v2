// ── 价格/涨跌幅缺数渲染（全站单实现）──
// §F-19（2026-10-20）：自选表与个股详情抽屉必须用同一个渲染出口。
// 旧实现抽屉里另写一遍 `Number(showPrice).toFixed(2)`，且 <=0 时整段不渲染——
// 同一份行情在表里显示 ¥9.78、在抽屉里显示 9.78 或什么都不显示，客户无从判断哪个可信。
//
// 缺数判据（与 §P3-FE P8/P9 一致）：
//   price：0 不是合法现价 ⇒ 可解析且 >0 才算有数；
//   change_pct：0 是合法实测值（平盘）⇒ 只要可解析成有限数就算有数。
// 缺数统一 U+2014「—」且不着色；绝不再把“没读到”折成 0/¥0.00/+0.00%。
import React from 'react'

export const WL_NO_DATA_PLACEHOLDER = '—'

// 现价缺数判据：可解析且 >0 才认，其余（0/NaN/null/undefined）一律 null
export function priceOrNil(v) {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? n : null
}

// 涨跌幅缺数判据：null/空串/不可解析→null；0 与负值都是合法实测读数
export function pctOrNil(v) {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) ? n : null
}

/**
 * 现价单元格：有数显示 ¥xx.xx，缺数（含 0）显示占位符且不着色。
 * @param {{value: number|null|string}} props
 */
export function PriceCell({ value }) {
  const p = priceOrNil(value)
  return p == null
    ? <span style={{ color: 'var(--app-faint)' }} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
    : <span>¥{p.toFixed(2)}</span>
}

/**
 * 涨跌幅单元格：有数按红涨绿跌着色（0 是合法的平盘实测），缺数显示占位符且不着色。
 * @param {{value: number|null|string}} props
 */
export function PctCell({ value }) {
  const c = pctOrNil(value)
  return c == null
    ? <span style={{ color: 'var(--app-faint)' }} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
    : <span data-testid="wl-pct" style={{ color: c >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{c > 0 ? '+' : ''}{c.toFixed(2)}%</span>
}
