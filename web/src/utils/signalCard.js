// ── §2.12 信号卡（2026-11-04，外脑化 B1' 批）：三通道派发 + signal_id 去重 + 通道开关 ──
//
// 为什么单独一个纯逻辑模块（不塞进组件）：这张卡真正的风险全在判定上——
//   · "缺股数"被折成 0 股或 100 股 ⇒ 下一笔没人算过的单；
//   · 去重键算错 ⇒ 同一条信号在页面弹两次、手机弹两次（把真告警训练成噪音，本仓反复栽过）；
//   · 通道开关读错默认值 ⇒ 要么三通道全哑（用户以为系统在报），要么全响（刷屏）。
// 这三条都必须能在 vitest 里钉住，所以判定与渲染分开：组件只渲染这里给出的结论。
//
// 三通道（owner 2026-11-03 裁决 Q1：全要）：
//   ① modal   —— 页内弹窗卡（可堆叠、可标记已处理/忽略；最可靠，页面在就看得见）
//   ② desktop —— 桌面系统通知（浏览器 Notification API，经 notify.js 走）
//   ③ mobile  —— 手机推送（APK 原生桥 window.AndroidNotify，同样经 notify.js；
//                 服务端那条 jpush/ntfy/webhook 通道**沿用现有告警链**，本批不新开告警规则）
// 三条都是"同一 signal_id 只推一次"的同一本账：seen 集合按 signal_id 记，
// 不按通道记——分通道记就会退化成"每条通道各一次"。
import { notifyThrottled } from '../notify.js'

// 2026-11-11 owner 需求三条的判定层（纯函数，vitest 可钉）：
//   ① 非交易时段不提醒（周一至周五 09:30–11:30 / 13:00–15:00，北京时间）；
//   ② 信号消失 ⇒ 提醒卡消失（调用方用 pruneCardsByLive 按当轮信号 id 裁剪卡堆叠）。
// English: trading-session gate + live-signal pruning, both injectable for tests.
export function isTradingSessionNow(now = new Date()) {
  const bj = new Date(now.getTime() + 8 * 3600 * 1000)
  const day = bj.getUTCDay()
  if (day === 0 || day === 6) return false
  const mins = bj.getUTCHours() * 60 + bj.getUTCMinutes()
  return (mins >= 570 && mins <= 690) || (mins >= 780 && mins <= 900)
}
export function pruneCardsByLive(cards, liveIds) {
  const live = new Set(liveIds)
  return (cards || []).filter((c) => live.has(c.signalId))
}

// 卡片八字段（与后端 internal/server/signal_card.go 的 CardField* 常量逐字对齐）。
// 后端那份是判据源，这份是渲染源；两边漂一个字，"缺什么"就会显示成"什么都不缺"。
// 由 signal_card_20261104.test.jsx 用同一份清单对撞（后端常量已在 Go 用例里锁住）。
export const CARD_FIELDS = [
  '股票名', '代码', '方向', '股数', '参考价', '策略/理由', '置信/档位', '有效期',
]

// MISSING_LABEL 与后端 CardMissingLabel 同串。
export const MISSING_LABEL = '数据缺失'

// CHANNELS 通道开关的键名（也是 localStorage 里的键空间前缀）。
export const CHANNELS = { modal: 'modal', desktop: 'desktop', mobile: 'mobile' }

// DEFAULT_CHANNELS 出厂缺省＝三条都开。
// 为什么默认全开而不是默认只开弹窗：owner 的裁决是"三通道全要"，
// 缺省关两通道的话，升级当天会表现为"手机上没收到"——那是最难查的一类"没报"。
export const DEFAULT_CHANNELS = { modal: true, desktop: true, mobile: true }

// seenKey 去重键：**只用 signal_id**。
// 为什么不带 code/level（notify.js 那侧的 throttle key 是 code@level）：
// 那是"通知节流"的维度，不是"一条信号"的身份；带上就会让同一条信号因为级别文字变了一格
// 而再弹一次。signal_id 缺失时后端已给兜底键（signalIDForCard），这里不再自己造第二个键——
// 两处造键＝两套身份，正是分裂去重的诞生方式。
export function seenKey(card) {
  const id = card && card.signal_id ? String(card.signal_id) : ''
  return id
}

// normalizeCard 把后端下发的 card 归一成渲染用形态，并**如实**补齐缺失字段的展示值。
// 关键约束：缺的字段一律显示 MISSING_LABEL，绝不用 0 / '—' / 空串渲染——
// 0 股与"没有数量读数"、¥0.00 与"没有价"在卡面上必须是两回事。
export function normalizeCard(card) {
  const src = card || {}
  const missing = Array.isArray(src.missing_fields) ? src.missing_fields : []
  const has = (field) => !missing.includes(field)
  return {
    signalId: seenKey(src),
    stockName: has('股票名') ? src.stock_name : MISSING_LABEL,
    code: has('代码') ? src.code : MISSING_LABEL,
    side: has('方向') ? src.side : MISSING_LABEL,
    // 股数：没有读数就是 MISSING_LABEL，不折 0、不折 100（一手）
    qty: has('股数') ? src.qty : MISSING_LABEL,
    price: has('参考价') ? src.price : MISSING_LABEL,
    priceKind: src.price_kind || '',
    reason: has('策略/理由') ? src.reason : MISSING_LABEL,
    confidence: has('置信/档位') ? src.confidence : MISSING_LABEL,
    expiresAt: has('有效期') ? src.expires_at : MISSING_LABEL,
    expiryNote: src.expiry_note || '',
    sourceLabel: src.source_label || '量仔建议',
    missing,
    // executable 由后端判（一处判定），前端只跟随；缺任一字段 ⇒ 不可执行（§2.12 字段缺一不可）
    executable: src.executable === true,
  }
}

// isExecutable 可执行判定的唯一入口（渲染与派发都问它，不许两处各判一遍）。
export function isExecutable(card) {
  return normalizeCard(card).executable === true
}

// createSeenStore 一条"已推过"账本。
// 为什么按自然日分键（而不是一个无限增长的集合）：signal_id 里带交易日，
// 跨日不复活、不累计——与本仓资金帧/决策快照的 day-keyed 口径一致；
// 而单一集合会让长期运行的前端把几千个键背在内存里（notify.js 的 throttle map 同理有 1000 上限）。
export function createSeenStore({ maxKeys = 2000 } = {}) {
  let seen = new Map()
  return {
    // has 这条信号是否已经推过（任意一条通道推过即算已推）
    has(key) {
      return key !== '' && seen.has(key)
    },
    // mark 记下这条已推过；返回是否新记（false=之前就有，即重放）
    mark(key) {
      if (key === '') return false
      if (seen.has(key)) return false
      seen.set(key, Date.now())
      if (seen.size > maxKeys) {
        // 淘汰最旧的一半：整表清空会让"上午推过的信号下午重弹"，那比多占内存更糟
        const cut = Math.floor(maxKeys / 2)
        const oldest = [...seen.entries()].sort((a, b) => a[1] - b[1]).slice(0, seen.size - cut)
        oldest.forEach(([k]) => seen.delete(k))
      }
      return true
    },
    size() {
      return seen.size
    },
    reset() {
      seen = new Map()
    },
  }
}

// loadChannelPrefs / saveChannelPrefs 通道偏好落设备本地（每个用户的手机/桌面各一套开关）。
// 为什么放本地而不是账号级服务端配置：开关的是"这台设备怎么打扰我"，
// 放服务端会让同一账号的手机与桌面被迫用同一档；而且本批刻意不扩写面（零新增路由）。
export function loadChannelPrefs(storage) {
  const out = { ...DEFAULT_CHANNELS }
  try {
    const raw = storage && storage.getItem ? storage.getItem('liangzai.signalCardChannels') : null
    if (!raw) return out
    const parsed = JSON.parse(raw) || {}
    Object.keys(out).forEach((k) => {
      if (typeof parsed[k] === 'boolean') out[k] = parsed[k]
    })
  } catch (_) {
    // 读坏＝回落缺省（三条都开），绝不把"解析失败"折成"全关"——
    // 全关的表现是"系统在报但我没收到"，那是最难查的一类没报。
  }
  return out
}

export function saveChannelPrefs(storage, prefs) {
  try {
    if (storage && storage.setItem) storage.setItem('liangzai.signalCardChannels', JSON.stringify(prefs))
  } catch (_) {
    // 存不进去（隐私模式/配额）不影响本次会话内的开关生效
  }
}

// cardTitle / cardBody 通知文案（桌面与手机共用一套，两处各写会漂）。
// 正文里**必须带股数或缺失标记**：通知是唯一可能在"没打开页面"的情况下被照着下单的东西，
// 它在文案上省略了"这笔没有数量"，就等于把不可执行的卡发成了可执行的卡。
export function cardTitle(c) {
  return '量仔 交易信号 · ' + (c.side === MISSING_LABEL ? '方向待定' : c.side)
}

export function cardBody(c) {
  const qty = c.qty === MISSING_LABEL ? MISSING_LABEL : c.qty + ' 股'
  const price = c.price === MISSING_LABEL ? MISSING_LABEL : '¥' + c.price
  return [c.code, c.stockName, qty, price, c.reason].join(' · ') +
    (c.executable ? '' : ' ｜不可执行（' + c.missing.join('、') + '）')
}

// dispatchCard 把一张卡派发到三条通道，返回每条通道的实际结果（供测试与"为什么没响"排查）。
// 语义：
//   · 同一 signal_id 只推一次——**跨通道共用一本已推账**；
//   · 每通道单独开关；开关关掉的通道不留痕（不记已推），这样"先关弹窗后开弹窗"时
//     那条还开着的通道不会因为"曾被另一条通道视为已推"而漏掉……
//     等等，这条推理要写清：不记已推的代价是同一条信号稍后可能在弹窗再出现一次，
//     而"漏推一条真信号"的代价远高于"多看见一次"——所以选择宁可重复也不漏。
//     但**已经推过的**（账里有键）一律不再推，这是去重的本体。
//   · 不可执行的卡仍然进弹窗（必须看得见"它缺了什么"），但**不进**桌面/手机通知：
//     把人从别的注意力里拽回来的东西必须是一张能直接照做的单，这是 owner 定"缺股数＝不可执行"的用意。
export function dispatchCard(card, { prefs, seen, notify = notifyThrottled, pushModal } = {}) {
  const c = normalizeCard(card)
  const key = c.signalId
  const p = prefs || { ...DEFAULT_CHANNELS }
  const res = { modal: false, desktop: false, mobile: false, deduped: false }
  if (!key) {
    // 没有去重键＝无法保证"只推一次"，宁可不推也不许刷屏（本仓反复的教训：刷屏会把真告警训练成噪音）
    return res
  }
  if (seen && seen.has(key)) {
    res.deduped = true
    return res
  }
  let pushed = false
  if (p[CHANNELS.modal]) {
    if (pushModal) pushModal(c)
    pushed = true
    res.modal = true
  }
  if (p[CHANNELS.desktop] && c.executable) {
    res.desktop = notify('signal-desktop:' + key, cardTitle(c), cardBody(c)) === true
    pushed = pushed || res.desktop
  }
  if (p[CHANNELS.mobile] && c.executable) {
    res.mobile = notify('signal-mobile:' + key, cardTitle(c), cardBody(c)) === true
    pushed = pushed || res.mobile
  }
  if (pushed && seen) seen.mark(key)
  return res
}
