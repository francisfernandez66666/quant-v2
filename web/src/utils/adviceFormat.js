// ── 实盘建议行的读数文案（§ADVICE-TACTICS 2026-11-05）──
// 单一真源的两句文案：
//   ① 建议行上的股数/金额（「建议 300 股 ≈3,150 元（受限：target）」），数来自后端
//      当日动态仓位快照（signal_decisions），前端**不做任何仓位算式**；
//   ② 「另有 N 条因战法未启用被过滤」——owner 选的是硬过滤，滤掉几条必须看得见，
//      否则"屏上少了 N 条"与"本来就没有 N 条"在页面上长得一模一样。
// 缺席机制：后端用 omitempty 让算不出的字段**根本不出现在 JSON 里**，
// 这里对应地判"键在不在、值是不是正数"，绝不把缺席折成 0
// （屏上一个 0 会被读成"系统建议买 0 股"，那是把"没算"报成"算了"）。
// English: single-source copy for the live advice row — suggested qty/amount copied from the
// backend's dynamic-sizing snapshot, and the tactics-switch filter count. Absent fields stay
// absent (rendered 未计算), never coerced into a 0 that reads as a real measurement.

// 买入方向的动作字面量（后端 PositionAdvice.Action）。
// 只有这一类卡片挂"建议股数"：卖出方向（止盈/止损/减仓）永不因战法开关被过滤，
// 也不该显示一个来自买入定权的数量——方向不同源，数字再同源也不能混着挂。
const ADVICE_ACTION_ADD = '加仓'

// groupThousands 千分位（自己实现而不是 toLocaleString：jsdom / Node 的 locale 不一致会让
// 同一句文案在测试与生产里长得不一样，那是"测试绿、屏上不是这句"的经典形状）。
function groupThousands(n) {
  const s = String(Math.round(n))
  return s.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

// adviceSuggestText 建议行的"量"那一格文案。
// 返回 null = 这一格不渲染（非买入方向的卡片、或整张卡根本没有建议）。
// 返回字符串 = 一定带得上「未计算」或具体股数之一——不许返回空串让读的人自己猜。
export function adviceSuggestText(advice) {
  if (!advice || advice.action !== ADVICE_ACTION_ADD) return null
  const qty = Number(advice.suggest_qty)
  const amount = Number(advice.suggest_amount)
  const capped = typeof advice.capped_by === 'string' ? advice.capped_by.trim() : ''
  const capNote = capped ? `（受限：${capped}）` : ''
  // 有量：股数必带，金额只在也拿到的时候带（两个读数各自缺席，不互相冒充）。
  if (Number.isFinite(qty) && qty > 0) {
    const amtNote = (Number.isFinite(amount) && amount > 0) ? ` ≈${groupThousands(amount)} 元` : ''
    return `建议 ${Math.round(qty)} 股${amtNote}${capNote}`
  }
  // 无量：只有"被某道帽挡住了"这一种缺席值得解释成受限项，其余一律「未计算」。
  return `未计算${capNote}`
}

// adviceFilteredText 战法开关过滤条数的文案。
// n 为非正数或拿不到（undefined/null/字符串——旧一轮载荷根本没带这个键）都返回 null：
// "读数未知"与"这条不存在"在页面上同样不该画出一个数字。
export function adviceFilteredText(n) {
  const v = Number(n)
  if (!Number.isFinite(v) || v <= 0) return null
  return `另有 ${Math.round(v)} 条因战法未启用被过滤`
}

// ── §TRADE-CARD（2026-11-05）交易级五动作（撤/留/加/减/走）的两处单点文案 ──
//
// 背景是 owner 的口径：这张卡完全拿"量化自动交易"的数据来做，做 T 的还要特殊标注。
// 于是标签与提示条各只有一个出处：后端给字段，这里给话术，页面只负责摆。
// 前端**不判**"该不该撤/该不该买"——判据住在 trading 与 engine，这里再判一次就是第二本账。

// 撤单一行的动作字面量（后端 trading.adviceActionCancel）。
export const ADVICE_ACTION_CANCEL = '撤单'
// 做 T 一腿的标记值（后端 trading.AdviceTradeKindIntradayT）。
export const ADVICE_TRADE_KIND_T = 'intraday_t'

// 撤单动作形态的中文话术（后端 trading.SweepAction.Kind 的三个值）。
// 认不出形态时**照原值回显**而不是猜一句"已撤"：撤单这件事说错方向就是让人以为敞口已经收掉了。
const CANCEL_KIND_TEXT = {
  requested: '撤单请求已发出，等待交易所确认',
  window: '判定要撤，但禁撤窗口内未发请求（这单仍挂在柜台上）',
  refused: '撤单请求被拒（这单仍挂在柜台上）',
}

// 撤单理由来源的中文话术（后端 trading.SweepSource*）。
const CANCEL_SOURCE_TEXT = {
  sweep: '超时撤单',
  signal_gone: '信号消失',
  close_sweep: '收盘清单',
  // §BUY-REPRICE（2026-11-05）：撤它是为了按新的市场参考价重挂，不是不想要这笔仓。
  vwap_reprice: 'VWAP 改价',
}

// adviceRowLabel 建议行的动作标签：做 T 的一律前缀「做T·」。
// owner 要的就是这一枚标注——同一只票同一天既做 T 又战略加仓是合法形态，
// 不标注就看不出哪条当天必须配平收摊、哪条不该动。
export function adviceRowLabel(advice) {
  if (!advice) return ''
  const action = String(advice.action || advice.label || '')
  return advice.trade_kind === ADVICE_TRADE_KIND_T ? `做T·${action}` : action
}

// cancelKindText 撤单形态话术（单点，卡片与提示条共用）。
export function cancelKindText(kind) {
  const key = String(kind || '')
  return CANCEL_KIND_TEXT[key] || (key ? `撤单形态：${key}` : '撤单形态未标注，请核对当日委托')
}

// cancelSourceText 撤单理由话术（单点）。
export function cancelSourceText(source) {
  const key = String(source || '')
  return CANCEL_SOURCE_TEXT[key] || (key ? key : '来源未标注')
}

// tradeLevelSummaryText 交易级提示条：把本轮建议列表里的**撤单行**归成一句人话。
//
// 为什么撤单要额外占一条横条，而不是只挂在表格的"建议"列里：
// 撤行定位的是**委托**，而建仓买入被撤时那只票根本还没有持仓行——
// 表格里没有那一行，"建议"列就永远没有机会显示它。屏上必须有第二个落点，
// 否则系统撤了一笔单而用户完全看不见。
// 返回 null = 本轮没有撤单动作（不渲染空条，也不画「0 笔」）。
export function tradeLevelSummaryText(advices) {
  const rows = (advices || []).filter((a) => a && String(a.action) === ADVICE_ACTION_CANCEL)
  if (!rows.length) return null
  // 按"还挂着没有"分两组：已发出（等确认）与仍在柜台上（窗口内未发 / 被拒）。
  const pending = rows.filter((a) => a.cancel_kind === 'window' || a.cancel_kind === 'refused')
  const sent = rows.length - pending.length
  const parts = []
  if (sent > 0) parts.push(`已发撤单请求 ${sent} 笔`)
  if (pending.length > 0) parts.push(`另有 ${pending.length} 笔仍未撤成`)
  // 理由分布按来源归组（同一句里只点出现在读数里的来源，缺席的不补 0）。
  const bySource = {}
  for (const a of rows) {
    const k = cancelSourceText(a.cancel_source)
    bySource[k] = (bySource[k] || 0) + 1
  }
  const reasonText = Object.keys(bySource).sort().map((k) => `${k} ${bySource[k]}`).join('、')
  return `今日撤单 ${rows.length} 笔（${parts.join('，')}）：${reasonText}`
}
