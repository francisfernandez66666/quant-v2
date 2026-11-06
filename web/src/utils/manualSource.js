// ── §2.12 手动账来源标注（2026-11-06，券商禁令/桥断后续批）：全站唯一文案源 ──
// 券商禁令生效、桥回传可能断后，持仓与可用资金由 owner 手工维护；量化交易读这本手动实盘
// 来算动态仓位与推送买卖。方案 §2.12 的硬边界：手动数据是**用户声称**，不是柜台证据。
// 凡是把它（或据它算出的买卖信号）呈现在屏上/推送里，必须自报两条身份：
//   ①「手动维护，更新于 <asof>」  ②「非柜台实时读数」
// 持仓 tab 的 ManualBookPanel 与买卖信号卡/推送都从这里取同一串——两处各抄一份，
// 迟早漂一个字，其中一处就会长得像桥回报，被人当成"刚核对过的持仓"去下真单。
// 本模块零依赖（不碰 notify / api / React），谁都能安全 import。

// 前缀与固定半句：屏上必须原样出现，别在别处另抄一份。
export const MANUAL_SOURCE_PREFIX = '手动维护，更新于 '
export const MANUAL_SOURCE_NONREALTIME = '非柜台实时读数'

// manualSourceMain 由 asof 拼出「手动维护，更新于 <asof>」。
// asof 缺位（还没维护 / 旧载荷没带）时只留「手动维护」半句，绝不伪造一个时刻——
// 「假时刻」比「没时刻」更危险：它会让这本账看起来刚被核对过。
export function manualSourceMain(asof) {
  const t = asof == null ? '' : String(asof).trim()
  return t ? MANUAL_SOURCE_PREFIX + t : '手动维护'
}

// signalManualSource 供**信号卡/推送**用：按「字段存在就显示、不存在就不显示」兼容后端
// 后续才加的 source/asof 两字段，绝不硬依赖（今天下发的卡没有这两个键 ⇒ 返回 null）。
//   · source 缺位（旧载荷）或不是 'manual' ⇒ 返回 null，卡面/推送一个字都不多画；
//   · source === 'manual' ⇒ 给出 { main, nonrealtime, text }；
//     asof 缺位时省掉时刻、两条身份句照旧（来源已知、时刻未知是中间态）；
// 只有"持仓/资金确实来自手动账"才标，柜台/网关来源不标——那本来就是桥回报，不是用户声称。
export function signalManualSource(card) {
  const c = card || {}
  if (c.source !== 'manual') return null
  const main = manualSourceMain(c.asof)
  return {
    main,
    nonrealtime: MANUAL_SOURCE_NONREALTIME,
    text: main + ' · ' + MANUAL_SOURCE_NONREALTIME,
  }
}
