// ── §2.12 手动账来源标注（2026-11-06，券商禁令/桥断后续批）：全站唯一文案源 ──
// 券商禁令生效、桥回传可能断后，持仓与可用资金由 owner 手工维护；量化交易读这本手动实盘
// 来算动态仓位与推送买卖。方案 §2.12 的硬边界：手动数据是**用户声称**，不是柜台证据。
// 凡是把它（或据它算出的买卖信号）呈现在屏上/推送里，必须自报两条身份：
//   ①「手动维护，更新于 <asof>」  ②「非柜台实时读数」
// §MANUAL-BOOK-UNIFY（2026-11-06 裁决 2）：独立的「手动维护账」面板已删除，维护统一落进
// 实仓 tab 的四个动作。现在**实仓改账态状态行 / 行级来源角标**与买卖信号卡/推送同取这一串；
// 两处各抄一份，迟早漂一个字，其中一处就会长得像桥回报，被人当成"刚核对过的持仓"去下真单。
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

// ── 跨日携带（owner 2026-11-18 裁决）：手动账跨日不再失效，但**必须自报是哪天的账** ──
// 为什么这两句要单独成一档而不并进 MANUAL_SOURCE_PREFIX：那串的语义是"我什么时候录的"
// （身份句），这一档的语义是"这个数**不是**今天的"（新鲜度警告）。两者的处置动作不同：
// 前者告诉你这本账的出处，后者告诉你要不要先核对一遍再去下单。
// 说成"过期"也不对——它是**可用**的（判据在 store.manualAsofUsable，跨日携带 + 14 天上限），
// 说成过期会让人以为决策没用它；说成"今天刚核对"则会让人按旧数字下手。
export const MANUAL_CARRIED_PREFIX = '非今日'

// manualCarriedDays 计算 asof 距 now 的自然日数。
// 不可解析/越界一律返回 null —— **绝不返回 0**：0 在屏上等于"今天"，那是伪造新鲜度。
export function manualCarriedDays(asof, now = new Date()) {
  const t = asof == null ? '' : String(asof).trim()
  if (t.length < 10) return null
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(t)
  if (!m) return null
  const at = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))
  if (Number.isNaN(at)) return null
  const ref = now instanceof Date ? now : new Date(now)
  if (Number.isNaN(ref.getTime())) return null
  const today = Date.UTC(ref.getFullYear(), ref.getMonth(), ref.getDate())
  const days = Math.round((today - at) / 86400000)
  if (!Number.isFinite(days)) return null
  return days
}

// manualCarriedNote 拼「非今日 · 已 N 天未更新」；天数不可得时只留「非今日」半句
// （沿用 manualSourceMain 那条纪律：没时刻就说没时刻，不编一个数字）。
// days <= 0 时也只留前半句：同一自然日内录的账不是"跨日"，后端也不会给 carried。
export function manualCarriedNote(asof, now = new Date()) {
  const n = manualCarriedDays(asof, now)
  if (n == null || n <= 0) return MANUAL_CARRIED_PREFIX
  return MANUAL_CARRIED_PREFIX + ' · 已 ' + n + ' 天未更新'
}
