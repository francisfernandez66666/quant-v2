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

// §C7（2026-10-22）行情来源四态：live / trigger / cost / none。
// kind 缺省时保持旧签名行为（只判值），避免破坏既有调用方；新页面必须传 kind。
const FAINT = { color: 'var(--app-faint)' }
const SUBTLE = { color: 'var(--app-muted-2)', fontSize: 11, marginLeft: 4 }

/**
 * 现价单元格：kind=live 显示 ¥xx.xx；trigger/cost 显示“—”并注明来源；none 显示“—”。
 * @param {{value: number|null|string, kind?: string, triggerPrice?: number|null|string}} props
 */
export function PriceCell({ value, kind, triggerPrice }) {
  // 兼容旧签名：没有来源字段时按旧口径（可解析且 >0 才算有数）。
  if (!kind) {
    const legacy = priceOrNil(value)
    return legacy == null
      ? <span style={FAINT} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
      : <span>¥{legacy.toFixed(2)}</span>
  }
  if (kind === 'live') {
    const p = priceOrNil(value)
    return p == null
      ? <span style={FAINT} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
      : <span data-testid="wl-price-live">¥{p.toFixed(2)}</span>
  }
  if (kind === 'trigger') {
    const t = priceOrNil(triggerPrice != null ? triggerPrice : value)
    return (
      <span data-testid="wl-price-trigger" style={FAINT}>
        {WL_NO_DATA_PLACEHOLDER}
        {t != null && <span style={SUBTLE}>触发价 ¥{t.toFixed(2)}</span>}
      </span>
    )
  }
  if (kind === 'cost') {
    return (
      <span data-testid="wl-price-cost" style={FAINT}>
        {WL_NO_DATA_PLACEHOLDER}
        <span style={SUBTLE}>按成本列示</span>
      </span>
    )
  }
  return <span style={FAINT} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
}

/**
 * 涨跌幅单元格：ok=false 表示读数不可用，显示“—”；ok=true 时 0 是合法平盘（显示 0.00%）。
 * ok 缺省时保持旧签名行为（只判值）；新页面必须传 ok。
 * @param {{value: number|null|string, ok?: boolean}} props
 */
export function PctCell({ value, ok }) {
  if (ok === false) {
    return <span style={FAINT} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
  }
  const c = pctOrNil(value)
  return c == null
    ? <span style={FAINT} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
    : <span data-testid="wl-pct" style={{ color: c >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{c > 0 ? '+' : ''}{c.toFixed(2)}%</span>
}
