// ── 量化交易页 Quant.jsx ──
// 页面用途：实盘链路总开关、执行方式、仓位纪律与战法白名单的统一管理界面。
// 主要功能：查看广州单机实盘链路状态/熔断；配置总开关、执行模式、委托价格、心跳超时、网关与 Token；
//          设定最大持仓/单票金额/预算等仓位纪律；按战法开关实盘准入并展示交易流水与归因盈亏。
//          §U-2（2026-09-14）：链路状态卡内置 kill-switch 紧急停止按钮、当日委托卡支持逐笔撤单、
//          §0925EVE-W3-G（2026-09-25）：新增「待核对委托」卡——网关第三态人工改判（released/settled）
//          的产品化出口（GET /api/qmt/pending-review + POST /api/qmt/order-confirm，均 admin+审计）。
//          日终结算卡一键三方对账并回看差异历史——三者后端早已就绪，本轮补上前端入口。
//          刷新策略（§0926E2E-17A 收编）：SSE 事件（qmt_report/real_order/qmt_halt/
//          settlement_diff/positions_clear_guard）驱动即时刷新 + 四类定时器 60s 兜底轮询
//          （对齐 §F5 统一口径，替代原 链路状态/当日委托 10s、交易流水 30s 高频轮）；
//          配置修改提交后待交易时段生效。
//          §M13（2026-09-22 修复批 K）：权限判定改走 api.isForbidden()（HTTP 状态码），
//          且任一 admin 端点回 403 时立即停掉全部轮询定时器——成员停在本页不再持续刷 403 灌 opslog。
// 使用 TDesign React 组件（Card / Form / Input / Button / Tag / Table）。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import ToggleSw from '../components/ToggleSw'
import FillAmendPanel from '../components/FillAmendPanel'
import { Card, Form, Input, Button, Tag, Table, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
// §0926E2E-17A：接入 §F5 事件总线，实盘链路 SSE 事件驱动本页即时刷新
import { on as sseOn } from '../sseBus.js'
import { confirmDialog } from '../ui.jsx'
import { fmtCNY2 } from '../utils'
import { verdictDisplay } from './quantVerdicts.js'

// 战法分组标签：form=内置形态战法、factor=因子战法、pattern=形态自动发现战法。
// 后端 /api/config/qmt 的 known_strategies 为 [{id,name,kind}]；因子/形态战法审批注入后自动出现。
// 战法类型展示名映射：形态（内置）/因子/形态自动发现，用于标签渲染
const KIND_LABELS = {
  form: '形态战法（内置）',
  factor: '因子战法',
  pattern: '形态自动发现战法',
}
// 战法类型展示顺序：按 内置→因子→自动发现 排列 tab
const KIND_ORDER = ['form', 'factor', 'pattern']

// 本地缓存键：切换 tab 时先显示上次成功加载的配置，避免开关/参数瞬间跳回默认值。
// 键含当前账号名（多账号隔离），防止 A 账号的表单缓存串给 B 账号显示。
const STORAGE_QMT_FORM_BASE = 'liangzai_qmt_form'

// cachedFormKey 返回当前账号专属的缓存键（多账号隔离：拼接账号名）。
function cachedFormKey() {
  const acc = (typeof api.getAccount === 'function' && api.getAccount()) || ''
  return acc ? STORAGE_QMT_FORM_BASE + ':' + acc : STORAGE_QMT_FORM_BASE
}

// readCachedForm 读取当前账号本地缓存的实盘表单配置。
// §F22 修复：不再回退到无账号后缀的旧全局键 —— 那会让切换账号时 B 账号看到 A 账号缓存的
// gateway_url/token（D5 漂移场景下的隐私/安全事故）。旧键仅一次性清理，读时不采纳。
// English: F22 — never fall back to the un-scoped legacy key; account B must not see A's cached
// gateway/token. The legacy global key is purged on read.
function readCachedForm() {
  try {
    // 一次性清理旧全局键（迁移遗留），避免下一个账号读到
    const legacy = localStorage.getItem(STORAGE_QMT_FORM_BASE)
    if (legacy) localStorage.removeItem(STORAGE_QMT_FORM_BASE)
    const raw = localStorage.getItem(cachedFormKey())
    if (raw) return JSON.parse(raw)
  } catch (_) {}
  return null
}

// writeCachedForm 把实盘表单配置写入本地缓存（当前账号专属键）。
function writeCachedForm(form) {
  try {
    localStorage.setItem(cachedFormKey(), JSON.stringify(form))
  } catch (_) {}
}

// 涨跌配色（红涨绿跌）：盈亏 >=0 用红色，<0 用绿色
function pnlColor(v) {
  return (v || 0) >= 0 ? 'var(--app-up)' : 'var(--app-down)'
}

// §0929FILL-NAME 成交流水名称列的两个常量（放模块作用域：负锁要能在不挂载组件的情况下读死口径）
// FILL_NAME_BATCH 单批问后端的代码数上限（后端 /api/stock/names 同值拒超限；
// 流水最多 100 笔去重后远小于此，留上限只为防"以后有人把别的清单也塞进来"）。
const FILL_NAME_BATCH = 200
// fillNameKey 名称映射的唯一键形态：去空白 + 大写归一。
// 写入侧（问完后端合并进 map）与读取侧（表格单元格取数）必须走同一个函数——
// 两处各写一份归一化，迟早一份大写一份小写，名称列就变成"有时有名有时没名"的哑故障。
// English: the single normalization used by both the write and read side of the name map;
// two hand-written variants would silently desynchronize and make the column flicker.
function fillNameKey(code) {
  return String(code || '').trim().toUpperCase()
}


/**
 * 量化交易主页面组件。
 * state：链路运行状态（enabled / 心跳 / 延迟 / 熔断等）。
 * form：实盘链路与仓位纪律的可编辑表单数据（保存时整体回写后端）。
 * tokenInput：鉴权 Token 明文输入，留空表示沿用原值。
 * knownStrategies：后端已知的全部战法标识列表。
 * strategyOn：各战法是否允许进入实盘的开关映射。
 * strategyDirty：战法开关是否被改动（控制保存按钮可用性）。
 * saving：保存中标志（禁用按钮、防重复提交）。
 * trades：交易流水与整体/分战法盈亏汇总。
 * amountsInput：各战法自定义单票金额（留空则用全局 fixed_amount）。
 * 副作用：挂载时拉取配置/状态/流水，并以定时器轮询刷新。
 */
export default function Quant() {
  // 链路运行状态（启用态/模式/心跳/延迟/熔断等），10s 轮询刷新
  const [state, setState] = useState(null)
  // §QMT-DUAL 网关 active 通道与双路径状态（broker: xt=miniQMT兼容 / queued=QMT桥兜底）
  const [broker, setBroker] = useState(null)
  // 通道切换请求进行中（按钮 loading，防重复提交）
  const [switchingBroker, setSwitchingBroker] = useState(false)
  // 首屏表单初值：优先取本账号 localStorage 缓存，无缓存时用内置默认值（避免开关闪回默认）
  const cachedForm = readCachedForm()
  // 实盘配置表单数据（按分组整体回写后端；用户编辑必须走 setFormUser 置脏位）
  const [form, setForm] = useState(cachedForm || {
    enabled: false, mode: 'manual', price_type: 'market', auto_sell: false,
    gateway_url: '', token_masked: '',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
    max_order_amount: 0,
  })

  // §UAT-D3（2026-09-16）修复「loadConfig 晚到回填冲掉用户输入」竞态：
  // 旧实现 fetchQMTConfig() 响应落地即无条件 setForm(nextForm)——挂载期慢响应 / 保存后刷新
  // 都可能把用户刚敲进单笔金额帽/预算的字符覆盖掉（uat_full.spec.mjs:109-111 曾自注「未修项」，
  // 测试被迫用服务端权威 toPass 重试兜底）。现约定：用户编辑一律走 setFormUser 置脏位，
  // loadConfig 检测到脏位则跳过表单覆盖（halted 安全态仍照常回显），重新进页/刷新即恢复权威值。
  // English: mark user edits; a late GET /api/config/qmt response must never clobber typed input.
  const userEditedRef = useRef(false)
  function setFormUser(updater) {
    userEditedRef.current = true
    setForm(updater)
  }

  // 通用 UI 状态
  // 鉴权 Token 明文输入（留空=沿用原值）
  const [tokenInput, setTokenInput] = useState('')
  // 后端已知战法列表（归一化为 {id,name,kind}）
  const [strategyList, setStrategyList] = useState([])
  // 各战法实盘准入开关映射 {id: bool}
  const [strategyOn, setStrategyOn] = useState({})
  // 战法开关是否被改动（控制保存按钮可用性）
  const [strategyDirty, setStrategyDirty] = useState(false)
  // §SHORT-4 做空战法状态：全局做空开关 + 模拟盘融券池开设标记
  const [shortEnabled, setShortEnabled] = useState(false)
  const [shortPoolOn, setShortPoolOn] = useState(false)
  const [saving, setSaving] = useState(false)
  // 交易流水数据（summary 汇总 + by_strategy 分战法 + fills 成交明细），30s 轮询刷新
  const [trades, setTrades] = useState(null)
  // §0929FILL-NAME 成交流水的「名称」旁证映射 {代码(大写)→股票名}。
  // 账本里没有这个名字（fills 表无 name 列），它是前端按代码向本地股票池表问出来的**展示旁证**：
  // 只进单元格渲染，绝不进提交体、锚点或任何账目计算（幂等锚仍是 (order_id,traded_at,price,qty)+trade_id）。
  const [fillNames, setFillNames] = useState({})
  // 已经问过的代码集合（含"问过但库里没名字"的）：代码→名称在本系统内是稳定事实，
  // 问过就不再重复问，防 60s 轮询把同一批代码反复打后端。
  const fillNameTriedRef = useRef(null)
  // §FILL-AMEND（2026-09-23）逐笔改判的目标成交行（null=对话框关闭）。
  // 状态放在本页、面板组件持有提交/台账/守恒的全部动作：流水表要点「改判」才能把行传进对话框，
  // 而台账与账目重算的刷新又得回过来调 loadTrades——两边都需要一个共同持有者。
  const [amendTarget, setAmendTarget] = useState(null)
  // §U-2（2026-09-14）运维三件套前端入口：kill-switch 紧急停止 / 当日委托手动撤单 / 交割单对账。
  // 此前这些能力后端端点齐备但页面零入口（撤单尤其因缺 order_id 列表而无从挂按钮）。
  // English: §U-2 frontend entries for the three ops capabilities that previously had backend
  // endpoints but no UI — kill-switch halt, manual cancel (now fed by the orders list), settlement.
  const [orders, setOrders] = useState(null)        // 当日委托（含 order_id / status）
  // §M-9（2026-09-22 修复批）委托列表 error 独立态：旧实现 loadOrders 失败既不 setOrders
  // 也不记错误，:939 的分支永远停在「加载委托列表…」——错误被伪装成永久加载态。
  // English: §M-9 — a failed orders fetch sets ordersError so the card shows a retryable
  // error instead of hanging on the loading placeholder forever.
  const [ordersError, setOrdersError] = useState('')
  // §0925EVE-W3-G（FIX_PLAN ⑫ C3）第三态「待核对委托」面板：网关对「已交给通道、结算结果不明」
  // 的委托保留「待核对」占位，此前只能 curl 打网关 /admin/status + /admin/order-confirm 收敛。
  // 三态分离沿用 §M-9 教训：pendingReview==null 且 pendingReviewError 非空 =「查询失败」
  // （失败态必须可见）；pendingReview 为空数组 =「确实没有待核对单」——两者绝不混渲染。
  // English: §0925EVE-W3-G — the third-state pending-review panel; read failure and an
  // genuinely empty list are separate visible states (never an empty array masking an error).
  const [pendingReview, setPendingReview] = useState(null)
  const [pendingReviewError, setPendingReviewError] = useState('')
  const [pendingReviewMeta, setPendingReviewMeta] = useState(null) // {unresolved_count, truncated, gateway_ts}
  const [confirmBusy, setConfirmBusy] = useState(false)            // 人工改判请求中（防连点双击改判）
  // §SIGNAL_CONTROLLER 信号裁定留痕（实盘/模拟盘两通道 hold/block 与原因，30s 随流水刷新）
  const [verdicts, setVerdicts] = useState([])
  // §F-5（20260917 缺陷修复批）风控闸口状态（下单前 12 道闸的当日命中与开关，30s 随流水刷新）
  const [riskGates, setRiskGates] = useState(null)
  const [killBusy, setKillBusy] = useState(false)   // kill-switch 请求中
  const [settleBusy, setSettleBusy] = useState(false)
  const [settle, setSettle] = useState(null)        // 最近一次对账结果/历史
  const [halted, setHalted] = useState(false)       // kill-switch 当前态（来自 config.halted）
  const ordersTimer = useRef(null)                  // 委托列表轮询定时器
  // §0925EVE-W3-G 待核对清单轮询定时器（30s：第三态收敛是人工作业，不需要 10s 实时性，
  // 且每次都要打网关 /admin/status，降频避免给跨网链路加常驻负载）
  const pendingTimer = useRef(null)

  // 战法自定义金额输入（按 strategyId → 金额）
  const [amountsInput, setAmountsInput] = useState({})
  // 配置加载失败提示：加载失败时表单停留在本地缓存值，若无提示用户会误把
  // 缓存当成服务器真实状态，误以为"开关被自动关闭"。显式告警消除歧义。
  const [loadErr, setLoadErr] = useState('')
  // 后端鉴权拒绝（403）：量化交易仅管理员可访问，后端据此决定，前端只负责展示。
  // English: backend denied (403) — quant trading is admin-only; the frontend just renders the denial.
  const [forbidden, setForbidden] = useState(false)
  // §0929GATE-403：「刷新身份并重试」按钮的在途态（只防连点重复请求，不参与权限判定）。
  const [retryBusy, setRetryBusy] = useState(false)
  // 是否已从服务器同步完成：首屏先显示「同步中」占位，避免把本地缓存的旧值
  // 误当成服务器真实状态（用户反馈「开关刷新后变回关闭」多源于此闪烁）。
  // English: whether config has synced from server; show a placeholder first paint
  // so a stale cached value can never be mistaken for the server's truth.
  const [syncing, setSyncing] = useState(true)

  const stateTimer = useRef(null)  // 链路状态轮询定时器
  const tradesTimer = useRef(null) // 交易流水轮询定时器
  // §0926E2E-17A：实盘链路 SSE 事件的取消订阅句柄（挂载注册、stopPolling/卸载回收）
  const sseQmtUnsub = useRef(null)

  // §M-6（2026-09-22 修复批）轮询/在飞链失效标志：stopPolling() 过去只 clearInterval，
  // 管不住已经跑在半路的 promise 链——Quant.jsx loadTrades 是「trades → verdicts → risk/gates」
  // 串行链，403 落地时链尾 fetchRiskGates 照发；React.StrictMode（main.jsx:19）双挂载更让
  // 两条链各漏一发（本轮唯一红 MP-3 的代码半）。现在 stopPolling 同时置位本标志，
  // 链上每一步 await 前后都查它，置位后整条链早退。
  // English: §M-6 — clearInterval alone cannot stop in-flight promise chains; the mount effect
  // resets this flag (StrictMode double-invoke), stopPolling sets it, and every chain step
  // checks it before firing the next request.
  const pollingDeadRef = useRef(false)

  // 按 kind 分组（form → factor → pattern），便于分别展示"形态战法 / 因子战法"
  const strategyGroups = useMemo(() => {
    const g = { form: [], factor: [], pattern: [] }
    strategyList.forEach((s) => {
      const k = g[s.kind] ? s.kind : 'form'
      g[k].push(s)
    })
    return KIND_ORDER.map((kind) => ({ kind, label: KIND_LABELS[kind] || kind, items: g[kind] })).filter((x) => x.items.length)
  }, [strategyList])
  // 全部战法是否都处于开启状态（全部开启时白名单传空数组表示不设限）
  const allStrategyOn = useMemo(() => strategyList.length > 0 && strategyList.every((s) => strategyOn[s.id]), [strategyList, strategyOn])
  // 生成当前战法开关状态的提示文案（全部允许 / 已开启数量）
  const strategyHint = useMemo(() => {
    const onCount = strategyList.filter((s) => strategyOn[s.id]).length
    if (allStrategyOn) return '当前：全部允许'
    return `当前：${onCount}/${strategyList.length} 允许进入实盘`
  }, [strategyList, strategyOn, allStrategyOn])

  // 金额格式化：正数补 + 号并保留两位小数
  function fmtMoney(v) {
    const n = Number(v) || 0
    return (n > 0 ? '+' : '') + n.toFixed(2)
  }
  // §M13（FIX_PLAN_20260922，2026-09-22 修复批 K）轮询止血三件套 ──────────────────
  // 缺陷原文：本页所有端点（/api/config/qmt、/api/qmt/state|orders|trades|broker|settle、
  //   /api/risk/gates）都挂在 adminMiddleware 上；成员账号首屏拿到 403、页面渲染「无权限」面板，
  //   但挂载副作用里起的 stateTimer(10s)/ordersTimer(10s)/tradesTimer(30s) 继续跑——
  //   每 10 秒三发 403 灌进 opslog，审计面被噪声淹没（M13 的"降级不停摆"半边）。
  // stopPolling 幂等清除全部轮询定时器（定时器句柄置 null，重复调用安全）。
  // §M-6（2026-09-22 修复批）同时置位 pollingDeadRef——clearInterval 只能挡「下一次定时触发」，
  // 挡不住已在飞的 promise 链；标志位让链上未执行的步骤全部早退（含 StrictMode 双挂载的第二条链）。
  function stopPolling() {
    pollingDeadRef.current = true
    for (const ref of [stateTimer, ordersTimer, tradesTimer, pendingTimer]) {
      if (ref && ref.current) {
        clearInterval(ref.current)
        ref.current = null
      }
    }
    // §0926E2E-17A：SSE 事件刷新同样是取数触发源——止血（403/卸载）时必须一并退订，
    // 否则成员账号仍会因服务端事件回调去拉 admin 端点，绕开 §M13 的止血语义。
    if (sseQmtUnsub.current) { sseQmtUnsub.current(); sseQmtUnsub.current = null }
  }
  // noteForbidden §M13/§A5：任一 admin 端点回 403 即认定当前会话无权限——停轮询 + 落无权限面板。
  // 判定一律走 api.isForbidden(e)（HTTP 状态码），不再用 e.message.indexOf('无权限')：
  // adminMiddleware 回中文「无权限」、permMiddleware 回英文 "no permission: <perm>"，
  // 按文案匹配对英文 403 必然漏判（E2E MP-2 用例即锁这组差异）。
  // §M-6 补充：非 403（500/503 等）在这里返回 false，调用方照常走各自降级分支——
  // 「服务异常」绝不能被分流成「无权限」（fetchShortStatus/fetchPaperState 挂载拉取同此口径）。
  // English: §M13 — any 403 from the admin endpoints halts all polling and renders the
  // forbidden panel; detection is status-code based (api.isForbidden), never message-text based.
  function noteForbidden(e) {
    if (!api.isForbidden(e)) return false
    stopPolling()
    setForbidden(true)
    return true
  }

  // ── §0929GATE-403（FIX_PLAN_20260929 ⑨-1）成员首屏 403 风暴预过滤 ──────────────
  // 锤实的现状：成员停在「实盘交易」页首屏连吃 9 条 403——/api/qmt/state、/api/qmt/orders、
  // /api/qmt/trades、/api/qmt/broker、/api/qmt/settle/history、/api/qmt/pending-review、
  // /api/config/qmt、/api/risk/gates、/api/qmt/fill-amendments(+conservation)。
  // 后端判得**对**（实盘面本就不对普通用户开放），缺的是前端明知道会被拒还照样拨：
  // 既脏了 opslog（§M13 当年正是为这条上止血），又让成员看到一片"加载失败"而不是一句"无权"。
  //
  // 判据来源＝/api/auth/me 写进本地缓存的角色（App 每次登录/刷新都会写 api.refreshMe()）。
  // 两条刻意边界，防把这条"减负"改成"新故障"：
  //   ① **后端仍是唯一裁决**。本地缓存只决定"要不要发这个请求"，不决定权限本身；
  //      因此无权限面板上保留「刷新身份并重试」出口——refreshMe 后若已升为管理员，
  //      本页照常加载。否则"角色刚被提升、缓存仍是旧值"会把管理员永久锁在门外。
  //   ② **只预过滤只读端点**。写端点（保存/撤单/勘误/开关）一处不动，它们的入口早由
  //      §0926E2E-17d 灰化处理，尺寸与判据都留在那条锁里。
  //
  // English: members no longer dial the admin-only read endpoints they will be refused by;
  // the backend stays the sole authority (the local role cache only gates whether we send),
  // which is why the denial panel keeps a "refresh identity and retry" escape hatch, and why
  // write-side entries are untouched (already greyed out by §0926E2E-17d).
  function adminReadsAllowed() {
    return api.isAdmin()
  }

  // startAdminReads 拉起本页全部 admin-only 只读链路：首轮取数 + 60s 兜底轮询 + SSE 事件即时刷新。
  // 幂等：先 stopPolling 再重建，「刷新身份并重试」重复点击不会叠出两套定时器（§M13 同族）。
  function startAdminReads() {
    stopPolling()
    pollingDeadRef.current = false
    loadState()
    stateTimer.current = setInterval(loadState, 60000)
    // §U-2 当日委托：60s 兜底；在途单状态推进的即时性走 SSE qmt_report/real_order。
    loadOrders()
    ordersTimer.current = setInterval(loadOrders, 60000)
    // §0925EVE-W3-G 待核对清单：60s（第三态人工改判本属低频作业，§0926E2E-17A 统一口径）
    loadPendingReview()
    pendingTimer.current = setInterval(loadPendingReview, 60000)
    loadSettleHistory()
    loadBroker()
    loadTrades()
    // 交易流水兜底轮询：60s；成交回报到达即经 SSE qmt_report 刷新
    tradesTimer.current = setInterval(loadTrades, 60000)
    // §0926E2E-17A：订阅实盘链路 SSE 事件（后端早已对当前账号定向广播）。
    // 回调里再核一次角色：成员会话即便收到事件也不得借回调去拉 admin 端点。
    sseQmtUnsub.current = sseOn(
      ['qmt_report', 'real_order', 'qmt_halt', 'settlement_diff', 'positions_clear_guard'],
      () => {
        if (pollingDeadRef.current || !adminReadsAllowed()) return // §M13/§0929GATE-403
        loadState(); loadOrders(); loadPendingReview(); loadTrades()
      },
    )
    loadConfig().catch((e) => {
      // §0929GATE-403：403 走止血分流（不弹"加载失败"误导成员），其余错误照旧 toast
      if (!noteForbidden(e)) MessagePlugin.error('加载实盘配置失败：' + (e && e.message ? e.message : e))
    })
  }

  // retryAsAdmin 「刷新身份并重试」：向服务器重新确认角色（缓存可能是旧值），升管理员即拉起取数链。
  // 这里刻意不"乐观地点亮页面"——只有 /api/auth/me 真回 admin 才解除预过滤。
  async function retryAsAdmin() {
    if (retryBusy) return
    setRetryBusy(true)
    try {
      const me = await api.refreshMe()
      if (me && me.role === 'admin') {
        setForbidden(false)
        startAdminReads()
        MessagePlugin.success('身份已刷新为管理员，正在加载实盘数据')
      } else {
        MessagePlugin.warning('服务器确认当前账号仍为普通用户，实盘数据不开放')
      }
    } catch (e) {
      MessagePlugin.error('身份刷新失败：' + (e && e.message ? e.message : e))
    } finally {
      setRetryBusy(false)
    }
  }

  // 拉取实盘配置并回填表单/战法开关/自定义金额；白名单为空数组时默认全部开启。
  // 失败时置 loadErr 告警（页面顶部显示），并向上抛出由调用方决定是否 toast。
  // §UAT-D3 force=true：保存成功后的主动刷新（用户输入即服务端权威值，绕过脏守卫）。
  async function loadConfig(force) {
    setSyncing(true)
    try {
      const c = await api.fetchQMTConfig()
      setLoadErr('')
      // 以服务端值为准组装表单快照（缺省字段回退默认值），待脏守卫判定后整体回填
      const nextForm = {
        enabled: !!c.enabled, mode: c.mode || 'manual', price_type: c.price_type || 'market',
        auto_sell: !!c.auto_sell, gateway_url: c.gateway_url || '', token_masked: c.token_masked || '',
        fixed_amount: c.fixed_amount ?? 10000, max_positions: c.max_positions ?? 10,
        initial_capital: c.initial_capital ?? 100000,
        daily_max_buys: c.daily_max_buys ?? 20, daily_budget_amount: c.daily_budget_amount ?? 100000,
        miss_heartbeat_sec: c.miss_heartbeat_sec ?? 120,
        max_order_amount: c.max_order_amount ?? 0,
      }
      // §UAT-D3 脏守卫：用户已编辑表单时，晚到的 GET 响应不得覆盖输入（见上方 userEditedRef 注释）。
      // 合法全量回填时机：首次加载 / 保存成功或失败后的刷新（patch 内显式 force）。
      if (!force && userEditedRef.current) {
        setHalted(!!c.halted) // 安全态与本地编辑无关，照常回显
        setSyncing(false)
        return
      }
      userEditedRef.current = false
      setForm(nextForm)
      writeCachedForm(nextForm)
      // §U-2 kill-switch 当前态随配置回显（config.halted），供紧急停止按钮显示"置位/解除"
      setHalted(!!c.halted)
      // 后端 known_strategies 可能为对象数组 [{id,name,kind}]（新）或纯 ID 数组（旧），统一归一
      let list = []
      if (Array.isArray(c.known_strategies)) {
        list = c.known_strategies.map((x) => {
          if (typeof x === 'string') return { id: x, name: x, kind: 'form' }
          return { id: x.id, name: x.name || x.id, kind: x.kind || 'form' }
        })
      }
      setStrategyList(list)

      // 初始化策略开关状态：若后端无已启用列表则默认全开，否则按列表匹配。
      // §20260917 严格开关：动量（momentum）不在"默认全开"之列——旧配置的空白名单
      // 语义是"内置四形态+库规则全部允许"，动量当时根本没有开关入口；现它必须显式
      // 出现在白名单里才算开启（后端空白名单同样不放行动量，双端口径一致）。
      const wl = Array.isArray(c.strategies) ? c.strategies : []
      const on = {}
      list.forEach((v) => { on[v.id] = wl.length === 0 ? v.id !== 'momentum' : wl.includes(v.id) })
      setStrategyOn(on)

      // 初始化策略金额输入框：从配置读取已有金额，未设置的留空
      const sa = c.strategy_amounts || {}
      const ai = {}
      list.forEach((v) => { ai[v.id] = sa[v.id] ?? '' })
      setAmountsInput(ai)
      setStrategyDirty(false)
      setSyncing(false)
    } catch (e) {
      // §M13/§A5：后端 403（无权限）时展示「无权限」面板并停掉全部轮询；
      // 判定改为状态码（api.isForbidden），旧写法 e.message.indexOf('无权限') 对
      // permMiddleware 的英文 "no permission: xxx" 会漏判——这里同时是文案解耦点。
      if (noteForbidden(e)) {
        setSyncing(false)
        return
      }
      setLoadErr('实盘配置加载失败（' + (e && e.message ? e.message : '网络异常') + '）——下方开关显示的是本机缓存，不代表服务器真实状态')
      setSyncing(false)
      throw e
    }
  }

  // 拉取交易流水（含汇总与分战法/成交流水），仅在返回合法时更新
  // §M-6（2026-09-22 修复批）串行链每步执行前查 pollingDeadRef：本函数是
  // 「fetchQMTTrades → fetchSignalVerdicts → fetchRiskGates」三步 await 链，
  // 旧实现 403 停轮询后在飞的链仍会走到链尾发出 /api/risk/gates（双挂载 ×2 发，MP-3 真漏网点）。
  async function loadTrades() {
    if (pollingDeadRef.current) return // §M-6 链入口即失效（forbidden/卸载后不再发起任何一步）
    try {
      const t = await api.fetchQMTTrades()
      if (pollingDeadRef.current) return // §M-6 上一步 await 期间 403 落地 → 链尾禁发
      if (t && t.summary) setTrades(t)
      // §0929FILL-NAME 流水到账后按代码补名称旁证（fire-and-forget：名称列不许拖慢流水渲染）
      resolveFillNames(((t && t.fills) || []).map((f) => f && f.code))
    } catch (e) {
      noteForbidden(e) // §M13：403 即停轮询（此端点在 adminMiddleware 下）
      if (pollingDeadRef.current) return
    }
    try {
      const v = await api.fetchSignalVerdicts(50)
      if (pollingDeadRef.current) return // §M-6
      if (v && Array.isArray(v.verdicts)) setVerdicts(v.verdicts)
    } catch (_) {
      if (pollingDeadRef.current) return // §M-6：verdicts 失败不再连带放行链尾 admin 端点
    }
    // §F-5 风控闸口状态（非 admin/无实盘账本时后端 403/503，静默降级不显示卡片）
    // §M13：其中 403 不再"静默"——它是权限判定信号，必须参与停轮询；503 等其他错误仍降级。
    try {
      const g = await api.fetchRiskGates()
      if (pollingDeadRef.current) return // §M-6：链尾响应落地时已失效则不再回写 state
      if (g && Array.isArray(g.gates)) setRiskGates(g)
    } catch (e) { noteForbidden(e) }
  }

  // §0929FILL-NAME 按成交代码补「名称」旁证映射（只问没问过的代码，一批一次请求）。
  // 三条纪律：
  //   ① 问过即记档（含"库里没有名字"的代码）——代码→名称在本系统内是稳定事实，
  //      60s 轮询不许反复为同一批代码打后端；
  //   ② 查询失败**静默**降级：名称列继续显示「—」，不弹窗、不调 noteForbidden、不停轮询。
  //      这条是展示旁证，它的故障不许牵动实盘主链路的数据面（§M13 的止血只认真权限信号）；
  //   ③ 返回的名称只合并进渲染用的 map，绝不写进任何提交体/定位锚（勘误入口仍只带 fill_id）。
  // English: side-evidence name resolution — ask once per code, degrade silently, and never let
  // the resolved names leak into any submit body or identity anchor.
  async function resolveFillNames(codes) {
    if (!fillNameTriedRef.current) fillNameTriedRef.current = new Set()
    const tried = fillNameTriedRef.current
    // 归一 + 去重 + 剔除问过的（fillNameKey 是写入/读取共用的唯一形态）
    const fresh = []
    const seen = new Set()
    for (const c of codes) {
      const key = fillNameKey(c)
      if (!key || tried.has(key) || seen.has(key)) continue
      seen.add(key)
      fresh.push(key)
    }
    if (fresh.length === 0) return
    // 单批上限：只把**真正发出去**的代码记档，超限的留到下一轮（记档而未发＝名称永久失踪）
    const batch = fresh.slice(0, FILL_NAME_BATCH)
    batch.forEach((k) => tried.add(k))
    try {
      const res = await api.fetchStockNames(batch)
      if (pollingDeadRef.current) return // 卸载/止血后不回写 state（§M-6 同口径）
      const map = res && res.names ? res.names : {}
      const merged = {}
      Object.keys(map).forEach((k) => {
        const key = fillNameKey(k)
        const v = map[k]
        if (key && v) merged[key] = v
      })
      if (Object.keys(merged).length === 0) return
      setFillNames((prev) => ({ ...prev, ...merged }))
    } catch (_) {
      // 旁证通道故障：名称列保持「—」，主链路数据照常展示（见上方纪律②）
    }
  }

  // 拉取链路运行状态（心跳/延迟/熔断等）
  async function loadState() {
    // §M13：/api/qmt/state 在 adminMiddleware 下，成员 403 要能触发停轮询（10s 定时器的主要噪声源）
    try { setState(await api.fetchQMTState()) } catch (e) { noteForbidden(e) }
  }

  // §QMT-DUAL 拉取网关 active 通道与双路径在线态（broker/xt_connected/queued_connected）
  async function loadBroker() {
    try { setBroker(await api.fetchQMTBroker()) } catch (e) { noteForbidden(e) } // §M13 同上
  }

  // §U-2 拉取当日委托列表（撤单按钮的数据源，含 order_id 与状态）；10s 随链路状态轮询
  async function loadOrders() {
    try {
      const o = await api.fetchQMTOrders()
      if (pollingDeadRef.current) return // §M-6：失效后不回写
      setOrders(Array.isArray(o) ? o : [])
      setOrdersError('') // §M-9 成功即清错误态
    } catch (e) {
      // 无实盘库时 503：静默降级（保留上次列表，不打断页面）；403 则落无权限面板并停轮询（§M13）
      if (!noteForbidden(e)) {
        // §M-9（2026-09-22 修复批）非 403 的失败也要留痕：列表尚空时渲染可重试错误，
        // 不再让「加载委托列表…」无限转圈冒充加载态。
        setOrdersError(e && e.message ? String(e.message) : '委托列表加载失败（网络/服务异常）')
      }
    }
  }

  // §U-2 kill-switch 紧急停止/解除：置位前二次确认（撤销一切在途未成交委托 + 拒绝新单），
  // 走专用 /api/qmt/halt（立即生效，不入开关队列）；成功后刷新配置与委托列表。
  async function toggleKillSwitch() {
    if (killBusy) return
    const engage = !halted
    const msg = engage
      ? '确认紧急停止（kill-switch）？\n将立即拒绝一切新下单（自动+手动），并撤销全部在途未成交委托。'
      : '确认解除紧急停止？\n解除后恢复正常下单（熔断仍按健康探测独立生效）。'
    if (!(await confirmDialog(msg, engage ? '紧急停止确认' : '解除停止确认'))) return
    setKillBusy(true)
    try {
      const r = await api.qmtHalt(engage)
      // §0925EVE-A2（2026-09-25）：kill-switch 的响应新增 failed 撤单失败明细数组。
      // 部分失败后端仍回 200（紧急停止动作本身已生效），但"撤销 N 笔"再也不能冒充
      // "全部撤干净"——有未撤成的单就换 warning 文案点名笔数与单号清单，操作者当场可决策。
      // 全成功时后端保证 failed 是空数组（非 null），这里仍防御性判一次，兼容旧版响应体。
      const failed = engage && r && Array.isArray(r.failed) ? r.failed : []
      if (failed.length) {
        const ids = failed.map((f) => (f && f.order_id ? f.order_id : '-')).join('、')
        MessagePlugin.warning(`已置位紧急停止，同步撤销 ${r.cancelled != null ? r.cancelled : 0} 笔，但 ${failed.length} 笔未撤成（${ids}）——仍在途，请人工处置`)
      } else {
        MessagePlugin.success(engage ? `已置位紧急停止，同步撤销 ${r && r.cancelled != null ? r.cancelled : 0} 笔在途委托` : '紧急停止已解除')
      }
      await loadConfig()
      await loadOrders()
    } catch (e) {
      MessagePlugin.error('操作失败：' + (e && e.message ? e.message : e))
    } finally {
      setKillBusy(false)
    }
  }

  // §U-2 手动撤单：仅未成交委托（已报/部成/部成待撤等）可撤；终态由后端 409 拒绝并如实回显。
  async function cancelOrder(orderId) {
    if (!(await confirmDialog(`确认撤单 ${orderId}？\n已成交/已撤/废单将返回错误。`, '撤单确认'))) return
    try {
      await api.qmtCancel(orderId)
      MessagePlugin.success('撤单请求已提交')
      await loadOrders()
    } catch (e) {
      MessagePlugin.error('撤单失败：' + (e && e.message ? e.message : e))
    }
  }

  // §0925EVE-W3-G 拉取第三态「待核对」清单（透传网关 /admin/status unresolved_orders）。
  // 失败分流与 loadOrders 的 §M-9 口径一致：403 走 noteForbidden（停轮询落无权限面板）；
  // 其余失败（502 网关读失败 / 503 未接入）记入 pendingReviewError——**可见的失败态**，
  // 绝不清空清单冒充「没有待核对单」（清单为空是有资金安全含义的断言）。
  async function loadPendingReview() {
    try {
      const r = await api.qmtPendingReview()
      if (pollingDeadRef.current) return // §M-6：失效后不回写
      setPendingReview(Array.isArray(r.orders) ? r.orders : [])
      setPendingReviewMeta({
        count: r.unresolved_count != null ? r.unresolved_count : (Array.isArray(r.orders) ? r.orders.length : 0),
        truncated: !!r.truncated,
        gateway_ts: r.gateway_ts || '',
      })
      setPendingReviewError('') // 成功即清错误态
    } catch (e) {
      if (!noteForbidden(e)) {
        setPendingReviewError(e && e.message ? String(e.message) : '待核对清单查询失败（网络/服务异常）')
      }
    }
  }

  // §0925EVE-W3-G 人工改判一条待核对委托：released=柜台确无此单（删占位解锁）/
  // settled=柜台有此单（占位改写正常终态）。confirmDialog 二次确认后转发网关；
  // 后端每次尝试都落 opslog 审计行（特权人工改判），本操作永不重发订单。
  async function confirmPendingOrder(row, decision) {
    if (confirmBusy) return
    const anchor = row.signal_id || '(缺锚点)'
    const msg = decision === 'released'
      ? `人工改判「柜台确无此单」→ 删除待核对占位、解锁该信号（不重发任何订单）。\n\n信号: ${anchor}\n委托: ${row.code || '-'} ${row.side || '-'} ${row.qty || 0} 股\n创建: ${row.created_at || '-'}\n\n前提：你已在券商/柜台侧核实该委托不存在。此操作将记入审计日志。确认执行？`
      : `人工改判「柜台已有此单」→ 待核对占位改写为正常终态（默认已撤，不重发）。\n\n信号: ${anchor}\n委托: ${row.code || '-'} ${row.side || '-'} ${row.qty || 0} 股\n创建: ${row.created_at || '-'}\n\n前提：你已在券商/柜台侧核实该委托存在。此操作将记入审计日志。确认执行？`
    if (!(await confirmDialog(msg, '待核对委托人工改判'))) return
    setConfirmBusy(true)
    try {
      const r = await api.qmtOrderConfirm({ wire_ref: row.signal_id, decision })
      MessagePlugin.success(decision === 'released'
        ? `已删除待核对占位并解锁信号 ${anchor}（可重新下单）`
        : `占位已转正常终态（${(r && r.status) || '已撤'}），同信号再下单走幂等返回`)
      await loadPendingReview()
      await loadOrders() // 委托卡同步刷新：settled 改写的是同一批账本行
    } catch (e) {
      // 网关拒绝/传输失败都如实回显（改判没有发生），不静默、不重试
      MessagePlugin.error('人工改判失败：' + (e && e.message ? e.message : e))
    } finally {
      setConfirmBusy(false)
    }
  }

  // §U-2/§WS-B 交割单三方对账（report_only 仅比对）：拉券商交割单↔本地账本，差异落库并告警。
  async function runSettle(mode = 'report_only') {
    if (settleBusy) return
    setSettleBusy(true)
    try {
      const r = await api.qmtSettle({ mode })
      setSettle({ result: r && r.diff ? r.diff : null, ok: true, mode })
      MessagePlugin.success('对账完成')
      await loadSettleHistory()
    } catch (e) {
      MessagePlugin.error('对账失败：' + (e && e.message ? e.message : e))
    } finally {
      setSettleBusy(false)
    }
  }

  // 对账历史（最近差异快照）
  async function loadSettleHistory() {
    try {
      const h = await api.fetchQMTSettleHistory()
      if (h && Array.isArray(h.diffs)) setSettle((s) => ({ ...(s || {}), history: h.diffs }))
    } catch (e) { noteForbidden(e) } // §M13：/api/qmt/settle/history 亦在 adminMiddleware 下
  }

  // §QMT-DUAL 切换网关 active 通道（xt=miniQMT兼容主路径 / queued=QMT内置桥兜底）。
  // 切换执行路径属高危操作：二次确认后调用后端（仅 admin），成功后刷新状态。
  async function switchBrokerTo(target) {
    if (switchingBroker) return
    if (broker && broker.broker === target) return  // 已在该通道，无需切换
    const targetName = target === 'queued' ? 'QMT桥(兜底)' : 'miniQMT(兼容)'
    if (!(await confirmDialog(
      `确认将实盘执行路径切换为「${targetName}」？\n切换后新订单将走该通道；切回需再次手动操作。`,
      '切换执行通道',
    ))) return
    setSwitchingBroker(true)
    try {
      // 调用后端切换接口（仅 admin），成功后刷新状态
      await api.switchQMTBroker(target)
      MessagePlugin.success(`已切换为${targetName}`)
      await loadBroker()
    } catch (e) {
      MessagePlugin.error('切换失败：' + (e && e.message ? e.message : e))
    } finally {
      setSwitchingBroker(false)
    }
  }

  // 标记战法开关被改动
  function markStrategyDirty() { setStrategyDirty(true) }

  // 通用保存：回写指定字段到后端，成功提示并重新拉取配置。
  // 失败时除提示外，还回滚到后端真实配置——否则界面显示与实际不一致，
  // 刷新/切tab重新挂载后配置"跳回"，造成开关丢了的现象。
  async function patch(fields, okTip) {
    // §0926E2E-17d：成员会话（forbidden 已由 403 锤实）不再向 admin 写端点发起保存——
    // 控件同步已灰化，这里是逻辑层的第二道闸（防键盘可达/将来漏标的调用点）。
    if (forbidden) { MessagePlugin.warning(ADMIN_ONLY_HINT); return }
    setSaving(true)
    try {
      await api.updateQMTConfig(fields)
      MessagePlugin.success(okTip || '已保存')
      await loadConfig(true) // §UAT-D3 保存成功=用户输入即服务端权威值，强制回填（清脏位）
    } catch (e) {
      MessagePlugin.error('保存失败：' + (e && e.message ? e.message : e))
      try { await loadConfig() } catch (_) {}
    } finally {
      setSaving(false)
    }
  }

  // 切换执行模式：点击立即保存到后端（全自动需二次确认），不再依赖"保存"按钮
  async function saveMode(mode) {
    if (saving) return
    if (mode === 'auto' && !(await confirmDialog('确认切换为「全自动」？信号将不经人工确认直接下单（受熔断/纪律约束）。', '切换全自动'))) return
    setFormUser((f) => ({ ...f, mode }))
    await patch({ mode }, mode === 'auto' ? '已切换为全自动' : '已切换为手动确认')
  }

  // 切换委托价格：点击立即保存到后端
  async function savePriceType(priceType) {
    if (saving) return
    setFormUser((f) => ({ ...f, price_type: priceType }))
    await patch({ price_type: priceType }, '委托价格已保存')
  }

  // 切换自动卖出：点击立即保存到后端
  async function saveAutoSell(v) {
    if (saving) return
    setFormUser((f) => ({ ...f, auto_sell: v }))
    await patch({ auto_sell: v }, v ? '已开启自动卖出' : '已关闭自动卖出')
  }

  // 保存实盘总开关：直接保存（不再用阻塞式二次确认，避免“开关一拨就弹回关闭”）。
  // 启用后下发真实交易指令属高危操作，故用非阻塞告警提示用户确认网关地址正确。
  // English: save immediately (no blocking confirm that could snap the switch back);
  // a non-blocking warning reminds the admin that enabling routes real orders to the gateway.
  async function saveSwitches(v) {
    if (saving) return
    await patch(
      { enabled: v },
      v
        ? '实盘链路启用已提交：将按下方参数向广州网关下发真实交易指令，待交易时段生效'
        : '实盘链路停用已提交，待交易时段生效',
    )
  }

  // 保存网关连接参数（网关地址/心跳超时/Token）；模式与价格已改为点击即存，不再随此保存
  async function saveExec() {
    const fields = {
      gateway_url: form.gateway_url, miss_heartbeat_sec: form.miss_heartbeat_sec,
    }
    if (tokenInput) fields.token = tokenInput
    await patch(fields, '网关连接参数已保存')
  }

  // 保存仓位纪律（最大持仓/单票金额/初始资金/日买笔数上限/日预算/单笔金额绝对帽）
  async function saveCaps() {
    await patch({
      max_positions: form.max_positions, fixed_amount: form.fixed_amount,
      initial_capital: form.initial_capital, daily_max_buys: form.daily_max_buys,
      daily_budget_amount: form.daily_budget_amount, max_order_amount: form.max_order_amount,
    }, '仓位纪律已保存')
  }

  // 保存战法白名单与自定义金额：全部开启时传空数组表示不设白名单（含因子/形态战法）
  async function saveStrategies() {
    const values = strategyList.filter((s) => strategyOn[s.id]).map((s) => s.id)
    const amounts = {}
    for (const v of strategyList) {
      const n = parseFloat(amountsInput[v.id])
      if (!Number.isNaN(n) && n > 0) amounts[v.id] = n
    }
    return patch(
      { strategies: allStrategyOn ? [] : values, strategy_amounts: amounts },
      '战法开关与仓位已保存',
    )
  }

  // 挂载副作用：依赖数组 []——仅在首次挂载执行一次，后续刷新由下方兜底定时器 + SSE 事件
  // 双通道驱动（四类定时器 60s 兜底、qmt_report 等事件即时刷新，§0926E2E-17A）；
  // 卸载时统一清除，避免内存泄漏与重复请求。
  // 配置加载失败在调用处 catch 提示，不阻塞轮询。
  // §M13 补充：forbidden 语义由 noteForbidden() 在任一 admin 端点回 403 时触发——
  // 它会调用与卸载清理同一个 stopPolling()，所以"成员停在页面被 403 灌 opslog"这条路被掐断；
  // 定时器句柄统一在这里赋值，回调里的清理只认句柄，不存在"清了旧的留下新的"竞态。
  useEffect(() => {
    // §M-6（2026-09-22 修复批）StrictMode 双挂载兼容：React 18 dev 下 effect 会
    // mount→cleanup→remount 同实例跑一遍，cleanup 走 stopPolling 把 pollingDeadRef 置了 true；
    // 第二次挂载必须在这里复位，否则整页轮询被自己的止血标志锁死。
    pollingDeadRef.current = false
    // §SHORT-4 做空开关与融券池状态探测（开关与模拟盘 short_book.enabled）
    // §M-6（2026-09-22 修复批）：两个挂载期拉取的 catch(()=>{}) 改为参与 noteForbidden 判定——
    // 403 是权限信号（应停轮询落无权限面板），旧实现直接吞掉；非 403（500/503）仍静默降级，
    // noteForbidden 内按状态码分流，绝不会被服务异常误判成无权限。
    // English: §M-6 — the swallowed catches now feed noteForbidden (status-code based), so a 403
    // halts polling; any non-403 error still degrades silently and cannot fake "forbidden".
    // 这两条挂在 authMiddleware 下（成员同样可读），因此**不参与** §0929GATE-403 的预过滤。
    api.fetchShortStatus().then((r) => { if (!pollingDeadRef.current) setShortEnabled(!!r.short_enabled) }).catch((e) => { noteForbidden(e) })
    api.fetchPaperState().then((r) => { if (!pollingDeadRef.current) setShortPoolOn(!!(r.short_book && r.short_book.enabled)) }).catch((e) => { noteForbidden(e) })
    // §0929GATE-403：admin-only 只读链路按角色决定是否拉起（成员直接落无权限面板，零请求）。
    // 判据只有这一个入口（startAdminReads），不在定时器回调里各写一份——两处判据分叉
    // 就是"有时加载有时不加载"的哑故障形态。
    if (adminReadsAllowed()) {
      startAdminReads()
    } else {
      setForbidden(true)
    }
    // 卸载时清除定时器，防止内存泄漏与重复请求（§M13：与 forbidden 停用共用同一清理函数）
    return () => { stopPolling() }
  }, [])

  // 执行模式 / 委托价格 的分段切换按钮样式工厂：active 为当前选中项（高饱和蓝，避免选中态过浅）
  const segBtn = (active) => ({
    marginRight: 8,
    padding: '5px 14px', borderRadius: 6, fontSize: 13, cursor: 'pointer',
    border: active ? '1px solid #1d4ed8' : '1px solid #cfd9ec',
    color: active ? '#ffffff' : 'var(--app-text-2)',
    background: active ? 'var(--td-brand-color)' : 'transparent',
    fontWeight: active ? 600 : 400,
  })

  // §0926E2E-17d 成员可见 admin 入口灰化：本页写侧端点全在 adminMiddleware 下，
  // 成员会话（noteForbidden 已被任一 403 置位 forbidden）此前仍能看到可点的开关与保存按钮——
  // 点了必 403（§M13 会止血，但"能点却必失败"本身就是缺陷，审计报告 §五-17 灰化条点名）。
  // 灰化≠删除：入口尺寸原样保留（读面板照常），只是按已锤实的 403 证据禁用并给出 tooltip 原因；
  // 管理员会话 forbidden 恒 false，一切行为与改前逐字节一致。
  const ADMIN_ONLY_HINT = '仅管理员可操作：当前账号对这些端点无权限（403）'
  const forbiddenHintProps = forbidden
    ? { disabled: true, title: ADMIN_ONLY_HINT, style: { opacity: 0.5, cursor: 'not-allowed' } }
    : {}
  // segBtnForbidden 给分段切换（span 形态按钮）用的样式补丁：禁指针+半透明，onClick 侧另有早退守卫。
  const segBtnForbiddenPatch = forbidden ? { pointerEvents: 'none', opacity: 0.5 } : {}

  // 分战法盈亏表列定义：realized_pnl 按涨跌配色渲染
  const byStrategyColumns = [
    { colKey: 'strategy', title: '战法', width: 140 },
    { colKey: 'buys', title: '买入额', width: 110 },
    { colKey: 'sells', title: '卖出额', width: 110 },
    {
      colKey: 'realized_pnl', title: '已实现盈亏', width: 120,
      cell: ({ row }) => <span style={{ color: pnlColor(row.realized_pnl) }}>{fmtMoney(row.realized_pnl)}</span>,
    },
    { colKey: 'trade_count', title: '笔数', width: 80 },
  ]

  // 成交流水表列定义：time 去掉 ISO 的 T 并截取至秒；side 买入红/卖出绿
  // §FILL-AMEND（2026-09-23）两处扩展：
  //   ① 方向列在已改判的行上额外标「原 X」——不标的话，运维按柜台回单核对时会看到
  //      "页面卖 / 回单买"却找不到差异来源，最容易二次改错；
  //   ② 末列「改判」是逐笔勘误入口（只提交待批准影子条目，批准才动账，见 FillAmendPanel）。
  const fillsColumns = [
    { colKey: 'time', title: '时间', width: 130, cell: ({ row }) => (row.traded_at || '').replace('T', ' ').slice(5, 19) },
    { colKey: 'code', title: '代码', width: 90 },
    // §0929FILL-NAME 名称列：账本（fills 表）本就没有股票名，这一列是前端按代码向本地股票池
    // 表问出来的**展示旁证**，只为让人工核对柜台回单时不必再拿代码反查名字。
    // 三条硬边界（与 store 侧负锁同源，改一处必须同时改另一处）：
    //   ① 名称不参与幂等锚（(order_id,traded_at,price,qty)+trade_id）——券商名称是自由文本，
    //      当锚用就会重演 §M4：同笔回报因两处名字不一致而判不出重复，账上多记一笔；
    //   ② 不参与任何金额/方向计算，也不进勘误提交体（提交仍只带 fill_id，锚由服务端从原始行读出）；
    //   ③ 查不到就显示「—」，不给空串、不猜——「没有名字」和"系统藏了名字"必须长得不一样。
    {
      colKey: 'name', title: '名称', width: 100,
      cell: ({ row }) => <span style={{ color: 'var(--app-faint)' }}>{fillNames[fillNameKey(row.code)] || '—'}</span>,
    },
    {
      colKey: 'side', title: '方向', width: 120,
      cell: ({ row }) => (
        <span>
          <span style={{ color: row.side === '买入' ? 'var(--app-up)' : 'var(--app-down)' }}>{row.side}</span>
          {row.amended && row.orig_side && row.orig_side !== row.side ? (
            <span style={{ fontSize: 11, color: 'var(--app-text-2)', marginLeft: 4 }} title={`勘误理由：${row.amend_reason || '—'}`}>原{row.orig_side}</span>
          ) : null}
        </span>
      ),
    },
    { colKey: 'price', title: '价格', width: 90 },
    { colKey: 'qty', title: '数量', width: 80 },
    { colKey: 'amount', title: '金额', width: 100, cell: ({ row }) => fmtCNY2(row.amount) },
    { colKey: 'strategy', title: '战法', width: 140, cell: ({ row }) => <span style={{ color: 'var(--app-text-2)' }}>{row.strategy}</span> },
    {
      colKey: 'amend', title: '操作', width: 90,
      cell: ({ row }) => (
        // 无 id 的行不给入口：改判只能按 fills.id 定位，自报锚点会造出匹配不到成交的死勘误
        row.id ? (
          <Button size="extra-small" variant="outline" onClick={() => setAmendTarget(row)}>
            {row.amended ? '已改判' : '改判'}
          </Button>
        ) : <span style={{ fontSize: 11, color: 'var(--app-muted-2)' }}>—</span>
      ),
    },
  ]

  // 渲染"总开关与执行方式"表单内容：总开关、执行模式、委托价格、自动卖出、心跳超时、网关地址、Token
  function renderExecForm() {
    // 实盘总开关：启用/停用实盘链路
    const masterSwitch = (
      <Form.FormItem label="实盘总开关">
        {syncing ? (
          <span style={{ fontSize: 13, color: 'var(--app-muted-2)' }}>同步中…</span>
        ) : (
          <ToggleSw checked={form.enabled} disabled={forbidden} onChange={(v) => { setFormUser({ ...form, enabled: v }); saveSwitches(v) }} />
        )}
        {/* 同步状态指示：红=加载失败（当前展示本地缓存），绿=已同步服务器 */}
        <span style={{ color: loadErr ? 'var(--app-up)' : 'var(--app-down)', fontSize: 11, marginLeft: 10 }}>
          {loadErr ? '⚠ 未同步服务器（下方为本地缓存，非真实状态）' : '已同步服务器 ✓'}
        </span>
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10, display: 'block', marginTop: 4 }}>
          关闭后引擎不再向网关传递任何信号/建议（纸面盘不受影响）
        </span>
      </Form.FormItem>
    )
    // 执行模式：手动确认（每单前端确认）或全自动（信号直接下单）
    const execMode = (
      <Form.FormItem label="执行模式">
        <span style={{ ...segBtn(form.mode === 'manual'), ...segBtnForbiddenPatch }} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => saveMode('manual')}>手动确认</span>
        <span style={{ ...segBtn(form.mode === 'auto'), ...segBtnForbiddenPatch }} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => saveMode('auto')}>全自动</span>
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10 }}>点击立即生效并保存；手动=每单前端确认；自动=信号直接下单</span>
      </Form.FormItem>
    )
    // 委托价格：对手价（市价）或限价
    const priceType = (
      <Form.FormItem label="委托价格">
        <span style={{ ...segBtn(form.price_type === 'market'), ...segBtnForbiddenPatch }} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => savePriceType('market')}>对手价</span>
        <span style={{ ...segBtn(form.price_type === 'limit'), ...segBtnForbiddenPatch }} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => savePriceType('limit')}>限价</span>
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10 }}>点击立即生效并保存</span>
      </Form.FormItem>
    )
    // 自动卖出：自动模式下止损/清仓级建议自动全仓卖出
    const autoSell = (
      <Form.FormItem label="自动卖出">
        <ToggleSw checked={form.auto_sell} disabled={forbidden} onChange={(v) => { setFormUser({ ...form, auto_sell: v }); saveAutoSell(v) }} />
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10 }}>自动模式下止损/清仓级建议自动全仓卖出；止盈/减仓保持提醒</span>
      </Form.FormItem>
    )
    // §SHORT-4 做空战法自动卖出状态（决策②⑤）：开关与顶部全局做空开关同源；
    // 状态行提示触发条件（全自动+自动卖出）与模拟盘融券池开设情况。
    // English: §SHORT-4 bear-tactic auto-sell status — same source as the global short toggle;
    // shows the live execution gate (auto mode + auto_sell) and the paper margin-short pool state.
    const shortTactics = (
      <Form.FormItem label="做空战法">
        <ToggleSw checked={shortEnabled} onChange={async (v) => {
          try {
            const r = await api.toggleShort(v)
            setShortEnabled(!!r.short_enabled)
            MessagePlugin.success(r.short_enabled ? '做空已开启' : '做空已关闭')
          } catch (_) { MessagePlugin.error('做空开关切换失败') }
        }} />
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10 }}>
          持有个股命中做空战法（高位滞涨/放量破位/龙头断板/利好兑现砸盘）自动卖出
          {form.mode === 'auto' && form.auto_sell ? '（当前满足：全自动+自动卖出）' : '（需「全自动 + 自动卖出」才代执行，否则 P1 强提醒手动处理）'}
          {shortPoolOn ? '；模拟盘融券池已开设' : ''}
        </span>
      </Form.FormItem>
    )
    // 心跳超时：连续失联超过该值触发熔断暂停下单（30-3600秒）
    const heartbeat = (
      <Form.FormItem label="心跳超时(秒)">
        <Input style={{ width: 140 }} type="number" value={form.miss_heartbeat_sec} min={30} max={3600}
          onChange={(v) => setFormUser({ ...form, miss_heartbeat_sec: parseInt(v, 10) })} />
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10 }}>连续失联超过该值触发熔断暂停下单（30-3600）</span>
      </Form.FormItem>
    )
    // 网关地址配置
    const gatewayUrl = (
      <Form.FormItem label="网关地址">
        <Input style={{ flex: 1, minWidth: 240 }} value={form.gateway_url} placeholder="http://81.71.69.17:8789"
          onChange={(v) => setFormUser({ ...form, gateway_url: v })} />
      </Form.FormItem>
    )
    // 鉴权Token配置：显示为脱敏形态，留空保持原值
    const tokenConfig = (
      <Form.FormItem label="鉴权Token">
        <Input style={{ flex: 1, minWidth: 240 }} type="password" value={tokenInput} placeholder={form.token_masked || '未设置'}
          onChange={(v) => setTokenInput(v)} />
        <span style={{ color: 'var(--app-text-2)', fontSize: 11, marginLeft: 10 }}>显示为脱敏形态；留空表示保持原值不变</span>
      </Form.FormItem>
    )
    // 保存按钮
    const saveBtn = (
      <Form.FormItem>
        <Button theme="primary" onClick={saveExec} loading={saving} {...forbiddenHintProps}>保存网关参数</Button>
      </Form.FormItem>
    )
    return (
      <Form labelWidth={120} labelAlign="right">
        {/* 表单字段：主开关/执行模式/价格类型/自动卖出/心跳/网关URL/Token/保存 */}
        {masterSwitch}
        {execMode}
        {priceType}
        {autoSell}
        {shortTactics}
        {heartbeat}
        {gatewayUrl}
        {tokenConfig}
        {saveBtn}
      </Form>
    )
  }

  // 渲染仓位纪律参数网格：最大持仓数、单票金额、初始资金、日买笔数上限、日预算
  function renderPositionDiscipline() {
    // 最大持仓数：1-50，双端校验
    const maxPos = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, color: 'var(--app-faint)' }}>最大持仓数</label>
        <Input type="number" value={form.max_positions} min={1} max={50} onChange={(v) => setFormUser({ ...form, max_positions: parseInt(v, 10) })} />
        <span style={{ fontSize: 10, color: 'var(--app-text-2)' }}>1-50，双端校验</span>
      </div>
    )
    // 单票金额：每次买入投入金额
    const fixedAmt = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, color: 'var(--app-faint)' }}>单票金额(元)</label>
        <Input type="number" value={form.fixed_amount} min={0} step={500} onChange={(v) => setFormUser({ ...form, fixed_amount: parseFloat(v) })} />
        <span style={{ fontSize: 10, color: 'var(--app-text-2)' }}>每次买入投入金额</span>
      </div>
    )
    // 初始资金：用于仓位约束预检
    const initCap = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, color: 'var(--app-faint)' }}>初始资金(元)</label>
        <Input type="number" value={form.initial_capital} min={0} step={10000} onChange={(v) => setFormUser({ ...form, initial_capital: parseFloat(v) })} />
        <span style={{ fontSize: 10, color: 'var(--app-text-2)' }}>用于仓位约束预检</span>
      </div>
    )
    // 单日买入笔数上限：0=不设限，按当日【已成交】笔数计（2026-09-18 口径修正：未成交/被废的报单不占额度）
    const dailyBuys = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, color: 'var(--app-faint)' }}>单日买入笔数上限</label>
        <Input type="number" value={form.daily_max_buys} min={0} onChange={(v) => setFormUser({ ...form, daily_max_buys: parseInt(v, 10) })} />
        <span style={{ fontSize: 10, color: 'var(--app-text-2)' }}>0=不设限，按当日已成交笔数计（未成交的报单不占额度）</span>
      </div>
    )
    // 单日买入预算：0=不设限，超出拒绝新买入
    const dailyBudget = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, color: 'var(--app-faint)' }}>单日买入预算(元)</label>
        <Input type="number" value={form.daily_budget_amount} min={0} step={10000} onChange={(v) => setFormUser({ ...form, daily_budget_amount: parseFloat(v) })} />
        <span style={{ fontSize: 10, color: 'var(--app-text-2)' }}>0=不设限，超出拒绝新买入</span>
      </div>
    )
    // §AUDIT-PM 2026-09-15 单笔金额绝对帽：0=关，手动/自动单统一封顶（保存即生效，不等开关队列）
    const maxOrderAmt = (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, color: 'var(--app-faint)' }}>单笔金额绝对帽(元)</label>
        <Input type="number" value={form.max_order_amount} min={0} step={10000} onChange={(v) => setFormUser({ ...form, max_order_amount: parseFloat(v) })} />
        <span style={{ fontSize: 10, color: 'var(--app-text-2)' }}>0=关；买卖双向封顶，防胖手误</span>
      </div>
    )
    return (
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 12 }}>
        {/* 仓位纪律字段：最大持仓/单票金额/初始资金/日买笔数/日预算/单笔绝对帽 */}
        {maxPos}
        {fixedAmt}
        {initCap}
        {dailyBuys}
        {dailyBudget}
        {maxOrderAmt}
      </div>
    )
  }

  // 渲染战法开关列表：按类型分组，每项含名称、ID、自定义金额、准入开关
  function renderStrategyGroups() {
    if (strategyGroups.length === 0) {
      return <div style={{ color: 'var(--app-text-2)', fontSize: 13, padding: '8px 2px' }}>暂无可用战法</div>
    }
    // 渲染单个战法组：标题 + 战法项列表
    function renderGroup(grp) {
      // 战法组标题：蓝色左边框 + 类型名称
      const groupTitle = (
        <div style={{ fontSize: 13, fontWeight: 700, color: 'var(--td-brand-color)', margin: '4px 0 6px', paddingLeft: 4, borderLeft: '3px solid #1d4ed8' }}>
          {grp.label}
        </div>
      )
      // 渲染单个战法项：名称+ID、自定义金额输入、准入开关
      function renderItem(s) {
        return (
          <div key={s.id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, padding: '9px 4px', borderBottom: '1px solid #ededed' }}>
            {/* 战法名称+ID */}
            <div>
              <div style={{ fontSize: 13, color: 'var(--app-text)' }}>{s.name}</div>
              <div style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--app-text-2)', marginTop: 2 }}>{s.id}</div>
            </div>
            {/* 金额输入+开关：自定义单次金额，开关控制准入状态 */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flex: 1, justifyContent: 'flex-end' }}>
              <Input style={{ width: 100 }} type="number" min={0} step={500} value={amountsInput[s.id] ?? ''} placeholder="全局"
                onChange={(v) => setAmountsInput({ ...amountsInput, [s.id]: v })} />
              <span style={{ fontSize: 11, color: 'var(--app-text-2)' }}>元/次</span>
              <ToggleSw checked={!!strategyOn[s.id]} onChange={(v) => { setStrategyOn({ ...strategyOn, [s.id]: v }); markStrategyDirty() }} />
            </div>
          </div>
        )
      }
      return (
        <div key={grp.kind} style={{ marginBottom: 14 }}>
          {groupTitle}
          {/* 渲染该分组下所有策略项（含开关/阈值/手续费配置） */}
          {grp.items.map(renderItem)}
        </div>
      )
    }
    return strategyGroups.map(renderGroup)
  }

  // 渲染盈亏汇总指标卡：总盈亏、已实现、浮动盈亏、成交笔数、胜负统计
  function renderSummaryCards() {
    // 总盈亏指标卡
    const totalPnl = (
      <div style={{ background: 'var(--app-surface-2)', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
        <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: pnlColor(trades.summary.total_pnl) }}>{fmtMoney(trades.summary.total_pnl)}</div>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginTop: 3 }}>总盈亏</div>
      </div>
    )
    // 已实现盈亏指标卡
    const realizedPnl = (
      <div style={{ background: 'var(--app-surface-2)', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
        <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: pnlColor(trades.summary.realized_pnl) }}>{fmtMoney(trades.summary.realized_pnl)}</div>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginTop: 3 }}>已实现</div>
      </div>
    )
    // 浮动盈亏指标卡
    const unrealizedPnl = (
      <div style={{ background: 'var(--app-surface-2)', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
        <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: pnlColor(trades.summary.unrealized_pnl) }}>{fmtMoney(trades.summary.unrealized_pnl)}</div>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginTop: 3 }}>浮动盈亏</div>
      </div>
    )
    // 成交笔数指标卡
    const tradeCount = (
      <div style={{ background: 'var(--app-surface-2)', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
        <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: 'var(--app-text)' }}>{trades.summary.trade_count}</div>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginTop: 3 }}>成交笔数</div>
      </div>
    )
    // 卖出胜负统计卡
    const winLoss = (
      <div style={{ background: 'var(--app-surface-2)', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
        <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: 'var(--app-text)' }}>{trades.summary.wins}胜 / {trades.summary.losses}负</div>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginTop: 3 }}>卖出胜负</div>
      </div>
    )
    return (
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))', gap: 10, marginBottom: 12 }}>
        {totalPnl}
        {realizedPnl}
        {unrealizedPnl}
        {tradeCount}
        {winLoss}
      </div>
    )
  }

  /* 渲染链路状态卡：实盘启用态/运行模式、网关地址、熔断、下行探测心跳、上行回报新鲜度、
     §QMT-DUAL 执行路径（xt=miniQMT兼容 / queued=QMT桥兜底）与二次确认切换。数据来自 10s 轮询的 state 与 broker。
     English: render the link-status card — enabled/mode, gateway, circuit-breaker, downlink probe,
     uplink report freshness, and the §QMT-DUAL active path with a guarded (confirm-in-switchBrokerTo) switch. */
  function renderChainStatusCard() {
    // 首轮轮询未到达：state 为 null，显示占位避免读取未定义字段
    if (!state) {
      return (
        <Card title="链路状态" style={{ marginBottom: 14 }}>
          <span style={{ fontSize: 13, color: 'var(--app-muted-2)' }}>链路状态加载中…（§0926E2E-17A：SSE 事件驱动 + 60s 兜底轮询）</span>
        </Card>
      )
    }
    // 单行信息条目：固定宽标签 + 值（用函数返回 JSX，避免在渲染函数内定义组件导致子树反复重挂载）
    const row = (k, node) => (
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '7px 0', borderBottom: '1px solid var(--app-border)', fontSize: 13 }}>
        <span style={{ width: 92, flexShrink: 0, color: 'var(--app-text-2)' }}>{k}</span>
        <span style={{ flex: 1, minWidth: 0, wordBreak: 'break-all' }}>{node}</span>
      </div>
    )
    // 运行模式中文标签（auto/manual，未知值原样展示）
    const modeLabel = state.mode === 'auto' ? '全自动' : (state.mode === 'manual' ? '手动确认' : (state.mode || '—'))
    // 熔断：已熔断展示红 Tag + 原因/时间，正常展示绿 Tag
    const breaker = state.tripped ? (
      <>
        <Tag theme="danger">已熔断</Tag>
        <span style={{ fontSize: 12, color: 'var(--app-up)', marginLeft: 8 }}>
          {state.trip_reason || '原因未知'}{state.trip_at ? `（${state.trip_at}）` : ''}
        </span>
      </>
    ) : <Tag theme="success">正常</Tag>
    // 下行探测（quant→gateway 连通性）：在线态 + 延迟 + 最近探测时间。
    // 零值时间戳（0001-…/缺失）= 引擎自启动还没探过测（探测只在连续竞价窗口跑，
    // 见 scoring_loop pushRealAdvice 的 IsContinuousTrade 门——桥心跳由 QMT tick 驱动，
    // 盘前/竞价/午休静默属正常，计入失联会每天误熔）——休市属正常，
    // 不能报"失联"（与真断线告警混淆）。
    const probeNever = !state.last_probe_at || String(state.last_probe_at).startsWith('0001-')
    const probe = (
      <span style={{ fontSize: 12 }}>
        {probeNever
          ? <Tag theme="default" title="下行探测仅在连续竞价时段（9:30-11:30 / 13:00-14:57）运行；休市/刚重启后无探测记录属正常">休市未探测</Tag>
          : (state.last_probe_ok ? <Tag theme="success">连通</Tag> : <Tag theme="danger">失联</Tag>)}
        {!probeNever && typeof state.last_latency_ms === 'number' ? <span style={{ marginLeft: 8 }}>延迟 {state.last_latency_ms}ms</span> : null}
        {!probeNever && state.last_probe_at ? <span style={{ marginLeft: 8, color: 'var(--app-text-2)' }}>· {state.last_probe_at}</span> : null}
      </span>
    )
    // 上行回报（gateway→quant 心跳/成交回执）新鲜度
    const report = state.last_report_at ? (
      <span style={{ fontSize: 12 }}>
        最近 {state.last_report_at}
        {state.last_report_kind ? <span style={{ color: 'var(--app-text-2)' }}>（{state.last_report_kind}）</span> : null}
      </span>
    ) : <span style={{ fontSize: 12, color: 'var(--app-text-2)' }}>暂无回报（非交易时段属正常）</span>
    // §QMT-DUAL 执行路径：双通道在线态 + active 高亮 + 切换按钮（switchBrokerTo 内含二次确认，防误切）
    const active = broker && broker.broker === 'queued' ? 'queued' : 'xt'
    const path = (
      <span style={{ fontSize: 12, display: 'inline-flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <Tag theme={active === 'xt' ? 'primary' : 'default'}>miniQMT兼容{broker && broker.xt_connected ? ' ●' : ' ○'}</Tag>
        <Tag theme={active === 'queued' ? 'primary' : 'default'}>QMT桥兜底{broker && broker.queued_connected ? ' ●' : ' ○'}</Tag>
        <Button size="xs" variant="outline" theme="warning" loading={switchingBroker} disabled={active === 'xt' || forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => switchBrokerTo('xt')}>切到 miniQMT</Button>
        <Button size="xs" variant="outline" theme="warning" loading={switchingBroker} disabled={active === 'queued' || forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => switchBrokerTo('queued')}>切到 QMT桥</Button>
        <span style={{ color: 'var(--app-text-2)' }}>当前：{active === 'queued' ? 'QMT桥兜底' : 'miniQMT兼容'}</span>
      </span>
    )
    return (
      <Card title="链路状态" style={{ marginBottom: 14 }}>
        {/* 实盘链路行：启用态 + 运行模式 */}
        {row('实盘链路', <>
          {state.enabled ? <Tag theme="success">已启用</Tag> : <Tag theme="default">未启用</Tag>}
          <span style={{ fontSize: 12, color: 'var(--app-text-2)', marginLeft: 8 }}>模式：{modeLabel}</span>
        </>)}
        {/* 网关地址行：广州单机网关 URL */}
        {row('网关地址', state.gateway_url || '—')}
        {/* 熔断行：健康探测触发的自动熔断（与 kill-switch 人工紧急停止相互独立） */}
        {row('熔断', breaker)}
        {/* §M12-A（2026-09-22）资金三态横幅：口径不可得（对账回报超30分钟/未接账本）时自动买入
            已 fail-close 暂停——不提示的话运维侧只会看到"没有买入"，容易误判成没有信号。
            English: §M12-A banner — auto-buy is fail-closed while the cash basis is stale/unknown. */}
        {state.cash_stale ? row('可用资金', <span style={{ fontSize: 12 }}>
          <Tag theme="warning">口径不可得</Tag>
          <span style={{ marginLeft: 8, color: 'var(--app-up)' }}>
            对账回报过期，自动买入已暂停（手动下单不受影响）；最近原始值 {Number(state.cash || 0).toFixed(2)}
          </span>
        </span>) : null}
        {/* §U-2 kill-switch（人工紧急停止）状态与入口：置位=拒绝一切新单+撤销在途委托，立即生效 */}
        {row('紧急停止', <>
          {halted ? <Tag theme="danger">已置位</Tag> : <Tag theme="success">未启用</Tag>}
          <Button
            size="xs" variant="outline"
            theme={halted ? 'success' : 'danger'}
            loading={killBusy}
            disabled={forbidden}
            title={forbidden ? ADMIN_ONLY_HINT : undefined}
            onClick={toggleKillSwitch}
            style={{ marginLeft: 10, ...(forbidden ? { opacity: 0.5, cursor: 'not-allowed' } : {}) }}
          >
            {halted ? '解除停止' : '紧急停止'}
          </Button>
          <span style={{ fontSize: 11, color: 'var(--app-text-2)', marginLeft: 8 }}>
            {halted ? '当前拒绝一切下单（自动+手动）' : '立即拒绝一切新下单并撤销在途未成交委托'}
          </span>
        </>)}
        {row('下行探测', probe)}
        {row('上行回报', report)}
        {row('执行路径', path)}
      </Card>
    )
  }

  // §U-2 委托可撤状态集（未成交/在途形态）；终态（已成/已撤/部撤/废单/发送失败）不显示撤单按钮
  const CANCELABLE = new Set(['已报', '部成', '已报待撤', '部成待撤'])
  // 委托状态 Tag 配色：终态绿/灰，在途蓝
  const orderTagTheme = (st) => (
    st === '已成' ? 'success' : (st === '已撤' || st === '部撤' || st === '废单' || st === '发送失败') ? 'danger' : 'primary'
  )

  /* §U-2 渲染"当日委托"卡：order_id/代码/方向/价格/数量/状态 + 未成交行的撤单按钮。
     数据 10s 轮询（与链路状态同频）；成交推进依赖网关回报（order 事件单调状态机 §R4-4）。 */
  function renderOrdersCard() {
    const cols = [
      { colKey: 'order_id', title: '委托号', width: 120, cell: ({ row }) => <span style={{ fontSize: 12 }}>{row.order_id}</span> },
      { colKey: 'code', title: '代码', width: 100 },
      { colKey: 'side', title: '方向', width: 70, cell: ({ row }) => <span style={{ color: row.side === '买入' ? 'var(--app-up)' : 'var(--app-down)' }}>{row.side}</span> },
      { colKey: 'price', title: '价格', width: 80 },
      { colKey: 'qty', title: '数量', width: 70 },
      { colKey: 'status', title: '状态', width: 90, cell: ({ row }) => <Tag size="small" theme={orderTagTheme(row.status)}>{row.status}</Tag> },
      { colKey: 'created_at', title: '时间', width: 130, cell: ({ row }) => <span style={{ fontSize: 12 }}>{(row.created_at || '').replace('T', ' ').slice(5, 19)}</span> },
      {
        // 操作列：仅在可撤状态（CANCELABLE）显示撤单按钮，终态显示占位"—"
        colKey: 'op', title: '操作', width: 90,
        cell: ({ row }) => (CANCELABLE.has(row.status)
          ? <Button size="xs" variant="outline" theme="danger" disabled={forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => cancelOrder(row.order_id)}>撤单</Button>
          : <span style={{ color: 'var(--app-muted-2)', fontSize: 12 }}>—</span>),
      },
    ]
    return (
      <Card title="当日委托" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 10 }}>状态由网关回报单调推进（已报→部成/已成/已撤）；未成交委托可撤，10s 刷新</div>
        {/* §M-9（2026-09-22 修复批）三态分离：loading（orders==null 且无错误）/
            error（可重试提示，不再无限「加载中」）/ 空（今日暂无委托）/ 有数据（表格）。 */}
        {orders == null && ordersError ? (
          <div style={{ color: 'var(--td-warning-color)', fontSize: 13, padding: '6px 2px' }}>
            ⚠ {ordersError}
            <Button size="xs" variant="outline" theme="warning" style={{ marginLeft: 10 }} onClick={loadOrders}>重试</Button>
          </div>
        ) : orders == null ? (
          <div style={{ color: 'var(--app-text-2)', fontSize: 13 }}>加载委托列表…</div>
        ) : orders.length ? (
          <Table data={orders} columns={cols} rowKey="order_id" size="small"
            pagination={{ pageSize: 10, showJumper: true, total: orders.length }} />
        ) : (
          <div style={{ padding: '8px 2px', color: 'var(--app-text-2)', fontSize: 13 }}>今日暂无实盘委托</div>
        )}
      </Card>
    )
  }

  /* §0925EVE-W3-G（FIX_PLAN ⑫ C3）渲染"待核对委托"卡：网关第三态（已交通道、结算结果不明）
     的人工收敛面板。每行两个处置按钮（柜台无此单=released / 柜台有此单=settled），
     confirmDialog 二次确认后走 POST /api/qmt/order-confirm（后端落 opslog 审计）。
     四态渲染严格分离（§M-9 同族口径）：失败态可见 ≠ 空态"确实没有" ≠ 加载 ≠ 有单。 */
  function renderPendingReviewCard() {
    const cols = [
      { colKey: 'signal_id', title: '信号锚点(signal_id)', width: 230, cell: ({ row }) => <span style={{ fontSize: 12, wordBreak: 'break-all' }} title={row.signal_id}>{row.signal_id}</span> },
      { colKey: 'code', title: '代码', width: 100 },
      { colKey: 'side', title: '方向', width: 70, cell: ({ row }) => <span style={{ color: row.side === '买入' ? 'var(--app-up)' : 'var(--app-down)' }}>{row.side}</span> },
      { colKey: 'qty', title: '数量', width: 70 },
      { colKey: 'created_at', title: '创建时间', width: 130, cell: ({ row }) => <span style={{ fontSize: 12 }}>{(row.created_at || '').replace('T', ' ').slice(5, 19)}</span> },
      {
        // 取证位：仍在派发队列 = 桥迟早回报、可能自动收敛；不在 = 只能人工核对柜台后改判
        colKey: 'dispatch_in_flight', title: '派发队列', width: 110,
        cell: ({ row }) => (row.dispatch_in_flight
          ? <Tag size="small" theme="warning">仍在队列</Tag>
          : <Tag size="small" theme="danger">已离队·须核柜台</Tag>),
      },
      {
        colKey: 'op', title: '人工处置', width: 220,
        cell: ({ row }) => (
          <div style={{ display: 'flex', gap: 6 }}>
            <Button size="xs" variant="outline" theme="danger" disabled={confirmBusy || forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => confirmPendingOrder(row, 'released')}>柜台无此单</Button>
            <Button size="xs" variant="outline" theme="primary" disabled={confirmBusy || forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => confirmPendingOrder(row, 'settled')}>柜台有此单</Button>
          </div>
        ),
      },
    ]
    return (
      <Card title="待核对委托（第三态人工收敛）" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 10 }}>
          网关对「已交给通道、结算结果不明」的委托保留待核对占位（宁拒不双卖）；须先到券商柜台侧核实，
          再逐笔人工改判。改判不重发任何订单，每次尝试均记入审计日志。30s 刷新。
        </div>
        {pendingReview == null && pendingReviewError ? (
          // 失败态必须可见：查询失败 ≠ 没有待核对单——绝不空数组冒充（§M-9 委托卡同族教训）
          <div style={{ color: 'var(--td-warning-color)', fontSize: 13, padding: '6px 2px' }}>
            ⚠ 待核对清单查询失败：{pendingReviewError}
            <Button size="xs" variant="outline" theme="warning" style={{ marginLeft: 10 }} onClick={loadPendingReview}>重试</Button>
          </div>
        ) : pendingReview == null ? (
          <div style={{ color: 'var(--app-text-2)', fontSize: 13 }}>加载待核对清单…</div>
        ) : pendingReview.length ? (
          <>
            <Table data={pendingReview} columns={cols} rowKey="signal_id" size="small"
              pagination={{ pageSize: 10, showJumper: true, total: pendingReview.length }} />
            {pendingReviewMeta && pendingReviewMeta.truncated && (
              // 网关单页最多回 20 条：计数>清单长度时明说"仍有未展示行"，防"页面空=风险小"误读
              <div style={{ marginTop: 6, fontSize: 12, color: 'var(--td-warning-color)' }}>
                ⚠ 全量待核对 {pendingReviewMeta.count} 笔，本清单仅前 {pendingReview.length} 条（网关单页上限 20），请用 curl /admin/status 核对余量或逐轮处置
              </div>
            )}
          </>
        ) : (
          // 空态只在「读成功且确实为 0」时渲染——与上面的失败态互斥
          <div style={{ padding: '8px 2px', color: 'var(--app-text-2)', fontSize: 13 }}>
            无待核对委托（网关确认 unresolved=0{pendingReviewMeta && pendingReviewMeta.gateway_ts ? ' · 网关采样 ' + pendingReviewMeta.gateway_ts : ''}）
          </div>
        )}
      </Card>
    )
  }

  /* §U-2/§WS-B 渲染"日终结算"卡：一键三方对账（券商交割单↔本地账本）+ 最近差异历史。
     report_only 仅比对不落补记；差异非空时后端已 P1 告警。 */
  function renderSettleCard() {
    const diff = settle && settle.result
    return (
      <Card title="日终结算对账" style={{ marginBottom: 14 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>
          <Button size="small" theme="primary" variant="outline" loading={settleBusy} disabled={forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={() => runSettle('report_only')}>立即对账</Button>
          <span style={{ fontSize: 11, color: 'var(--app-text-2)' }}>拉券商交割单与本地委托/成交三方比对，差异自动 P1 告警（休市/盘后运行最佳）</span>
        </div>
        {/* 最近一次对账结果摘要：本地缺失/多余/不符笔数 + 费用差与现金差 */}
        {diff && (
          <div style={{ fontSize: 12, marginBottom: 10, padding: '8px 10px', borderRadius: 6, border: '1px solid var(--app-border)', background: 'var(--app-bg-2, transparent)' }}>
            最近对账 {diff.day || '—'}：本地缺失 {(diff.missing_in_local || []).length} · 本地多余 {(diff.extra_in_local || []).length} · 不符 {(diff.mismatch || []).length} · 费用差 {(diff.fee_diff || 0).toFixed(2)} · 现金差 {(diff.cash_diff || 0).toFixed(2)}
          </div>
        )}
        {settle && Array.isArray(settle.history) && settle.history.length ? (
          <Table
            data={settle.history} rowKey={(r) => r.day + r.mode} size="small" pagination={false}
            columns={[
              { colKey: 'day', title: '交易日', width: 110 },
              { colKey: 'missing', title: '本地缺失', width: 90, cell: ({ row }) => (row.missing_in_local || []).length },
              { colKey: 'extra', title: '本地多余', width: 90, cell: ({ row }) => (row.extra_in_local || []).length },
              { colKey: 'mismatch', title: '不符', width: 70, cell: ({ row }) => (row.mismatch || []).length },
              { colKey: 'fee_diff', title: '费用差', width: 90, cell: ({ row }) => <span style={{ color: pnlColor(-(row.fee_diff || 0)) }}>{(row.fee_diff || 0).toFixed(2)}</span> },
              { colKey: 'mode', title: '模式', width: 110 },
            ]}
          />
        ) : (
          <div style={{ color: 'var(--app-text-2)', fontSize: 12 }}>暂无对账记录——收盘后点「立即对账」建立日结留痕</div>
        )}
      </Card>
    )
  }

  /* 量化交易页面主渲染：链路状态卡 → 总开关与执行方式 → 仓位纪律 → 战法开关 → 交易流水 */
  return (
    <div className="page">
      {/* 页面标题与说明 */}
      <div style={{ fontSize: 20, fontWeight: 700, marginBottom: 4 }}>📈 量化交易</div>
      <div style={{ fontSize: 12, color: 'var(--app-muted)', marginBottom: 14 }}>实盘链路参数、仓位纪律与战法白名单（修改提交后，待下一交易时段自动生效）</div>

      {/* 配置加载失败警告：提示用户当前显示的是本地缓存值 */}
      {loadErr && (
        <div style={{ marginBottom: 12, padding: '8px 12px', borderRadius: 6, background: 'var(--app-warn-bg)', border: '1px solid var(--app-warn-border)', color: 'var(--app-warn-text)', fontSize: 12 }}>
          ⚠️ {loadErr}
        </div>
      )}

      {/* 无权限访问提示：普通用户无法操作量化交易页面 */}
      {forbidden && (
        <div style={{ marginBottom: 12, padding: '18px 16px', borderRadius: 8, background: '#fff7e6', border: '1px solid #ffd591', color: 'var(--td-warning-color)', fontSize: 13 }}>
          {/* §0929GATE-403：首句「无权限访问量化交易」是既有验收锚（Playwright uat_full 的 tester 腿 +
              m13/w4d/w5a 三组 vitest 锁都按它定位），改写会把已收口的链路判成回归；
              新增的「实盘数据未拉取」后半句才说清这次的语义变化——预过滤后成员会话一条 admin
              请求都没发过，这里既不是"加载失败"也不是一次真实的 403 往返。 */}
          <div>🔒 无权限访问量化交易：实盘数据未拉取。当前登录「{api.getAccount() || '未知'}」为普通用户，
            该页面仅管理员账号可见；本页已按角色跳过实盘只读接口（未发出注定被 403 的请求，也不再刷"加载失败"）。</div>
          {/* 出口一：角色缓存可能是旧值（刚被提升/刚换登录），向后端重确认后照常加载——
              后端仍是唯一裁决，这里只是让本地预过滤有可撤销的余地，不是前端自授权限。
            出口二：换管理员账号，原文案保留该指引。 */}
          <div style={{ marginTop: 10, display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <Button size="small" variant="outline" loading={retryBusy} onClick={retryAsAdmin}>刷新身份并重试</Button>
            <span style={{ fontSize: 12, color: 'var(--app-text-2)' }}>若刚被提升为管理员可点左侧按钮；否则请使用管理员账号（用户名 admin）登录后再进行管理。</span>
          </div>
        </div>
      )}

      {/* 链路状态卡片：显示网关地址/熔断状态/紧急停止(kill-switch)/运行模式/执行路径切换 */}
      {renderChainStatusCard()}

      {/* §U-2 当日委托卡：撤单按钮的宿主（order_id 数据源） */}
      {renderOrdersCard()}

      {/* §0925EVE-W3-G 待核对委托卡：第三态人工改判入口（网关 /admin/* 的产品化出口） */}
      {renderPendingReviewCard()}

      {/* 总开关与执行方式卡片：实盘开关、执行模式、委托价格、自动卖出等配置 */}
      <Card title="总开关与执行方式" style={{ marginBottom: 14 }}>
        {renderExecForm()}
      </Card>

      {/* 仓位纪律卡片：最大持仓数、单票金额、初始资金、日买笔数上限、日预算 */}
      <Card title="仓位纪律" style={{ marginBottom: 14 }}>
        {renderPositionDiscipline()}
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 10 }}>
          <Button theme="primary" onClick={saveCaps} loading={saving} {...forbiddenHintProps}>保存仓位纪律</Button>
        </div>
      </Card>

      {/* 战法开关卡片：按类型分组展示各战法的准入开关与自定义金额 */}
      <Card title="战法开关" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 12 }}>关闭的战法信号不会进入实盘链路；「全部开启」= 默认全集（内置四形态+已审批库规则），动量战法需在此显式开启。因子/形态战法需先在「自动研究」页审批应用后才会出现在此处。模拟盘撮合的独立开关在「模拟盘 → 设置 → 战法开关」。</div>
        {renderStrategyGroups()}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, justifyContent: 'flex-end', marginTop: 10 }}>
          <span style={{ fontSize: 11, color: 'var(--app-text-2)' }}>{strategyHint} · 仓位留空/0 = 使用全局单票金额</span>
          <Button theme="primary" disabled={!strategyDirty || saving || forbidden} title={forbidden ? ADMIN_ONLY_HINT : undefined} onClick={saveStrategies}>
            {saving ? '保存中…' : (strategyDirty ? '保存战法开关 *' : '已同步')}
          </Button>
        </div>
      </Card>

      {/* §SIGNAL_CONTROLLER 信号裁定留痕卡：为何"提醒了却没成交"——白名单/黑名单/持续性确认窗的结构化答案。
          §SELLPOINT-UNIFY（2026-09-21）：卖出统一裁决（stage=sell_discipline）与买入共用本环，
          文案按环节翻译（处置/观察窗预警 vs 拦截/待确认），映射抽到 quantVerdicts.js 由 vitest 锁死。 */}
      <Card title="信号裁定留痕" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 8 }}>
          信号控制器对买入信号的拦截与观察记录（白名单/个股·板块黑名单/持续性确认窗），以及卖出纪律裁决留痕
          （触线进窗=观察窗预警、窗结算处置=卖出「处置」行；shadow 灰度期处置行不执行只投影）；消息中心对应条目带 ⛔ 标注。
        </div>
        {verdicts && verdicts.length ? (
          <Table data={verdicts} rowKey={(r) => String(r.at) + r.channel + r.code + r.verdict} size="small" pagination={{ pageSize: 10, total: verdicts.length }}
            columns={[
              { colKey: 'at', title: '时间', width: 150, cell: ({ row }) => (row.at || '').slice(11, 19) },
              { colKey: 'stage', title: '环节', width: 70, cell: ({ row }) => {
                  const d = verdictDisplay(row)
                  return <Tag theme={d.stageLabel === '卖出' ? 'primary' : 'default'} variant="light-outline">{d.stageLabel}</Tag>
                } },
              { colKey: 'channel', title: '通道', width: 70, cell: ({ row }) => (row.channel === 'live' ? '实盘' : '模拟') },
              { colKey: 'verdict', title: '裁定', width: 110, cell: ({ row }) => {
                  const d = verdictDisplay(row)
                  return <Tag theme={d.theme}>{d.label}</Tag>
                } },
              { colKey: 'code', title: '代码', width: 90 },
              { colKey: 'strategy', title: '战法', width: 120 },
              { colKey: 'reason', title: '原因', cell: ({ row }) => <span title={row.reason}>{row.reason}</span> },
            ]}
          />
        ) : (
          <div style={{ padding: '6px 2px', color: 'var(--app-text-2)', fontSize: 12 }}>暂无拦截/待确认/卖出裁决记录——买卖信号都直接过了裁定</div>
        )}
      </Card>

      {/* §F-5（20260917 缺陷修复批）风控闸口卡：下单前 risk.Gate 12 道闸的当日命中明细与开关态。
          旧缺口：GET /api/risk/gates 有数据面无消费方——闸拦了什么只有 opslog 可查，UI 不可见。 */}
      {riskGates && (
        <Card title="风控闸口状态" style={{ marginBottom: 14 }}>
          <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 8 }}>
            下单前最后防线（ST/黑名单/单笔帽/T+1/涨跌停/陈旧价/日亏/集中度/买法纪律/战法准入/持仓数）当日命中记录 · {riskGates.day} {riskGates.time}
          </div>
          {/* §0926E2E-4B 缺配置告警条：六闸默认关是 owner 裁决（出厂开闸会在存量账号产生新拒单），
              但"一条都没配"的裸奔状态必须一屏可见。只信后端 gates_config_unset（生效配置判定），
              不在前端复制判定逻辑（口径漂移先例：白名单显示名比对失配）。 */}
          {riskGates.gates_config_unset && (
            <div style={{ background: 'rgba(227,119,0,0.12)', border: '1px solid rgba(227,119,0,0.45)', color: 'var(--app-text-1)', borderRadius: 4, padding: '8px 10px', marginBottom: 8, fontSize: 12 }}>
              ⚠ 该账号六道默认关风控闸（日亏熔断/单票集中度/陈旧价/涨停追买/单笔金额帽/价格复核）全部未配置——
              新委托只受常开硬闸（跌停追卖/黑名单/笔数预算等）约束。建议按 docs/RUNBOOK_QMT_DAILY.md §4.3 评估开闸；保持默认关同样是有效决策，此条仅确认你知情。
            </div>
          )}
          {/* 闸口开关状态标签组：any_enabled 为主开关（蓝/灰），其余开=绿、关=灰 */}
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 8 }}>
            {Object.entries(riskGates.switches || {}).map(([k, v]) => (
              <Tag key={k} theme={k === 'any_enabled' ? (v ? 'primary' : 'default') : (v ? 'success' : 'default')} variant="light-outline">
                {k}{v ? ' 开' : ' 关'}
              </Tag>
            ))}
          </div>
          {/* 兼容后端历史 null 值：gates 为 null 时按空数组处理，避免 TypeError 拖垮整页渲染 */}
          {(riskGates.gates || []).length ? (
            <Table data={(riskGates.gates || [])} rowKey={(r) => r.user_id + r.gate} size="small" pagination={{ pageSize: 8, total: (riskGates.gates || []).length }}
              columns={[
                { colKey: 'gate', title: '闸口', width: 150 },
                { colKey: 'hits', title: '当日命中', width: 90, cell: ({ row }) => <Tag theme="danger" variant="light">{row.hits}</Tag> },
                { colKey: 'user_id', title: '账号', width: 130 },
                { colKey: 'last_reason', title: '最近原因', cell: ({ row }) => <span title={row.last_reason}>{row.last_reason}</span> },
                { colKey: 'updated_at', title: '更新', width: 150, cell: ({ row }) => (row.updated_at || '').slice(5, 16) },
              ]}
            />
          ) : (
            <div style={{ padding: '6px 2px', color: 'var(--app-text-2)', fontSize: 12 }}>今日暂无风控闸命中——所有实盘委托都通过了下单前闸门</div>
          )}
        </Card>
      )}

      {/* 交易流水与整体盈亏卡片：汇总指标 + 分战法盈亏表 + 成交流水表 */}
      <Card title="交易流水与整体盈亏" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 12 }}>已实现=加权成本重放；浮动=市值-成本×数量；30s 刷新</div>
        {trades && trades.summary ? (
          <>
            {renderSummaryCards()}
            {/* 分战法盈亏统计表：各战法的买入额/卖出额/已实现盈亏/笔数 */}
            {(trades.by_strategy || []).length ? (
              <div style={{ marginBottom: 12 }}>
                <Table data={trades.by_strategy} columns={byStrategyColumns} rowKey="strategy" size="small" pagination={false} />
              </div>
            ) : (
              <div style={{ padding: '8px 2px', color: 'var(--app-text-2)', fontSize: 13 }}>暂无成交——实盘成交后此处出现按战法归因的盈亏统计（飞轮回流数据源）</div>
            )}
            {/* 成交流水明细表：时间、代码、名称（§0929FILL-NAME 前端旁证）、方向、价格、数量、金额、战法 */}
            {(trades.fills || []).length ? (
              <Table data={trades.fills} columns={fillsColumns} rowKey="order_id" size="small"
                // §FIX-20260902 补 total=长度：tdesign 未传 total 时分页默认 0 → 页脚「共 0 条」且无法翻页
                pagination={{ pageSize: 10, showJumper: true, total: trades.fills.length }} />
            ) : null}
          </>
        ) : (
          <div style={{ color: 'var(--app-text-2)', fontSize: 13 }}>加载交易流水…</div>
        )}
      </Card>

      {/* §FILL-AMEND（2026-09-23）成交勘误台账 + 账本守恒自检（均 admin-only）。
          §0929GATE-403：这两个端点此前由本面板无条件挂载即拨，成员即使看到无权限面板也会先吃两条 403；
          现按 adminReadsAllowed() 传结论，判据仍只有那一个入口，组件内不再自行查角色。 */}
      <FillAmendPanel fills={(trades && trades.fills) || []} target={amendTarget}
        onCloseTarget={() => setAmendTarget(null)} onChanged={loadTrades}
        canQuery={adminReadsAllowed()} />

      {/* §U-2/§WS-B 日终结算对账卡（三方比对 + 差异历史） */}
      {renderSettleCard()}
    </div>
  )
}
