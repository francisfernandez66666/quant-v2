// ── 持仓管理页面 Positions.jsx ──
// 纸面持仓（增删改/加减仓/改成本/清仓/批次明细） + 实盘持仓（QMT 网关对账 + 手动下单）。
// 收益展示：纸面总盈亏（已实现+浮动，可一键清零） / 实盘总盈亏（QMT trades 汇总优先，
// 汇总未回来时按持仓浮动兜底并挂「本地兜底」标记自报口径，§W7-G）；
// 建议回看：实盘持仓「建议」列由 SSE real_advice 实时推送 + 挂载 REST 回填（§F-6）点亮。
// §ADVICE-TACTICS（2026-11-05）：同一列再带「建议 N 股 ≈M 元（受限：x）」——数来自后端当日动态
// 仓位快照（signal_decisions），本页不算任何仓位；算不出的一格显示「未计算」，不显示 0。
// 战法开关做的是硬过滤，所以表下另起一行「另有 N 条因战法未启用被过滤」，载荷没带这个键时不渲染。
// 纯 TDesign 组件（Tabs / TabPanel / Card / Table / Dialog / Form / Input / InputNumber / Button / Tag），无自定义 CSS。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Tabs, Card, Table, Dialog, Form, Input, InputNumber, Button, Tag, MessagePlugin, DialogPlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import MinuteView from '../components/MinuteView.jsx'
import AnchorShadowCard from '../components/AnchorShadowCard.jsx' // §ANCHOR-FIRST W2 影子对照（只读）
import StockDetailDrawer from '../components/StockDetailDrawer.jsx'
// §MANUAL-BOOK-UNIFY（2026-11-06 裁决 1/2/4）：独立的手动维护面板已删除（方案 §6.1），
// 维护统一落进实仓 tab；「手动维护，更新于 <asof>」「非柜台实时读数」两串身份文案仍取自全站唯一源。
import { manualSourceMain, MANUAL_SOURCE_NONREALTIME } from '../utils/manualSource.js'
import { on } from '../sseBus.js'
// 2026-11-11 owner 需求：手动/自动维护改为前端显式开关，不再硬等系统判冻结/熔断。
// 自动＝跟随 book_source（柜台帧冻结时仍自动放开改账，避免按钮全灰）；手动＝强制改账态。
// English: explicit bookkeeping mode; "manual" always forces the adjust path, "auto"
// follows the server book source (with the frozen-frame safety fallback).
export const BOOK_MODE_KEY = 'positions_book_mode'
// 2026-11-11 owner 实锤：改账新增在手机上「明明填了代码却报请输入代码」——受控 Input 在
// 移动端 IME/自动填充下 onChange 可能不触发，confirm 读到空串。修复两件：确认时优先读
// ref（不受闭包/渲染时序影响），并接受 6 位纯数字码按板块自动补后缀（60→SH，00/30→SZ，4/8→BJ）。
// English: ref-backed read + suffix normalization for bare 6-digit codes.
export function normalizeManualCode(raw) {
  const c = String(raw || '').trim().toUpperCase()
  if (/^\d{6}\.(SH|SZ|BJ)$/.test(c)) return c
  if (/^\d{6}$/.test(c)) {
    if (c.startsWith('6')) return c + '.SH'
    if (c.startsWith('0') || c.startsWith('3')) return c + '.SZ'
    if (c.startsWith('4') || c.startsWith('8')) return c + '.BJ'
  }
  return c
}
export function resolveBookMode(bookMode, cashManual, posManual, counterFrozen) {
  return bookMode === 'manual' || !!(cashManual || posManual || counterFrozen)
}
import { createStaleGuard } from '../utils/staleGuard.js' // §M-10 轮询后到丢弃
import { usePolling } from '../utils/usePolling.js' // §N-09 统一可见性轮询生命周期
// §FIX-20261028-F09：读取失败按腿记账（共享 hook + 共享红条，testid 全站唯一）
import { useLoadLedger } from '../utils/loadLedger.js'
import LoadFailBanner from '../components/LoadFailBanner.jsx'
// §H-2（2026-09-22 修复批）：editBalanceSave 的失败分支调用 showToast，但导入区一直没有
// ui.jsx —— 保存可用资金一旦失败即抛 ReferenceError（异步未捕获、用户零提示）。补齐导入。
// English: §H-2 — showToast was called without being imported; the save-failure branch died
// with an uncaught ReferenceError (no toast at all). Import added.
import { showToast } from '../ui.jsx'
import { PriceCell, PctCell } from '../components/priceFormat.jsx' // §C7 行情来源契约（cost 不再冒充现价）
// §ADVICE-TACTICS（2026-11-05）：建议行的股数文案与"被战法开关滤了几条"文案的唯一来源
import { adviceSuggestText, adviceFilteredText, adviceRowLabel, tradeLevelSummaryText } from '../utils/adviceFormat.js'
import { canStrict } from '../utils/perm.js'

// 持仓与资金数据的 localStorage 缓存键
const CACHE_KEY = 'pos_cache_v1'

// 将持仓与资金缓存到 localStorage
function persistCache(holdings, balance) {
  // §P2-J 可吞：本地缓存写入失败（配额满/隐私模式）不影响主流程，下一次成功写入即覆盖。
  // 但仍留一行 console 诊断——静默吞错与"已知可吞"是两件事（§113 空吞负锁要求此处不得为空块）。
  try { localStorage.setItem(CACHE_KEY, JSON.stringify({ holdings, balance })) } catch (e) { console.warn('[positions] 缓存写入失败（可吞）:', e) }
}
// 从 localStorage 读取持仓与资金缓存
function loadCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    const d = raw ? JSON.parse(raw) : null
    if (d) {
      return { holdings: Array.isArray(d.holdings) ? d.holdings : [], balance: d.balance || 0 }
    }
  } catch (e) { console.warn('[positions] 缓存解析失败（按无缓存处理）:', e) } // §P2-J 可吞：随后回源取数，不打扰用户
  return { holdings: [], balance: 0 }
}

/**
 * 持仓管理页面组件
 * 管理纸面持仓（增删改/加减仓/改成本/清仓）与实盘 QMT 持仓对账/下单。
 * @returns {JSX.Element}
 */
export default function Positions() {
  // 初始化时读取本地缓存的持仓与资金作为初始值（断线/刷新后仍可见）
  const cache = loadCache()
  // 纸面持仓列表
  const [holdings, setHoldings] = useState(cache.holdings)
  // 已展开分时图的持仓代码集合
  const [klineOpen, setKlineOpen] = useState(new Set())
  // §F3 全局个股详情抽屉目标（{code,name}），null=关闭
  const [detail, setDetail] = useState(null)
  // 可用资金余额
  const [availableBalance, setAvailableBalance] = useState(cache.balance)
  // §H-2（2026-09-22 修复批）资金保存在途标记 + 服务端已确认余额：
  // 在途期间 persistCache 一律用确认值，乐观值/回滚前的脏值绝不进缓存。
  const balancePendingRef = useRef(false)
  const balanceConfirmedRef = useRef(cache.balance)
  // §0929POS-SAVE（09-29 全量审计批 P1-4）持仓腿照余额腿同姿势收口：
  // 旧 saveHoldings() 的 catch 是空实现、调用点不判成败、persistCache 无条件写——
  // 一次网络抖动＝后端没存住、页面显示已改、脏值活到下次刷新（:262 那句"绝不静默"的注释
  // 与代码口径相反，本轮按代码改注释）。余额腿在 §H-2 已修成正确形态，持仓腿是同族没一起收编。
  // 第二个更硬的坑（本轮读码撞出、报告未列）：旧 saveHoldings() 不带参数、内部读闭包里的
  // holdings，而调用点 confirmAdd 是「setHoldings(下一份表) → 立刻 await saveHoldings()」——
  // React 的状态更新要等下一次渲染才可见，闭包里读到的仍是**改动前**那份表；POST /api/holdings
  // 又是 full-replace 语义（后端 :1104 会把没出现在载荷里的本账号手动持仓直接删档），
  // 于是新增/编辑永远传不上去，60s 轮询（:646）再把界面打回服务端现值＝用户改动静默丢失。
  // 现在整表写只认**显式传入的下一份表**，闭包值不再作为写出口径（同锁见
  // __tests__/h_0929pos_save.test.jsx 的"未传参必红"反证）。
  // 三个量的分工：holdingsPendingRef＝整表写在途；holdingsConfirmedRef＝服务端确认过的那份
  // （缓存写的兜底基准）；dirtyCodesRef＋同名 state＝界面上尚未落库的代码集合
  // （ref 供轮询/写入口做判据，state 供表格渲染脏标记；size>0 即整表处于未落库态，
  // 不再另设 holdingsDirtyRef——同一事实留两个来源迟早分叉）。
  // English: §0929POS-SAVE — the holdings leg adopts the balance leg's guard trio. A second,
  // harder bug found while reading code (not in the audit report): the old parameterless
  // saveHoldings() transmitted the closure's pre-edit holdings, while POST /api/holdings is
  // full-replace and deletes any manual position absent from the payload — so every add/edit was
  // silently dropped and the 60s poll reverted the UI. Writes now take the intended table
  // explicitly; dirty codes are tracked in a ref (logic) plus state (rendering).
  const holdingsPendingRef = useRef(false)
  const holdingsConfirmedRef = useRef(cache.holdings)
  const dirtyCodesRef = useRef(new Set())
  // 脏行代码集合：dirtyCodesRef 供逻辑判据、dirtyCodes 供表格渲染（变更只经 setDirtyCodes 单入口）
  const [dirtyCodes, setDirtyCodesState] = useState(new Set())
  // §0929POS-SAVE：纸面持仓的服务端读数是否不可得（load 失败）。旧写法空 catch 直接不提，
  // 页面继续显示本地缓存却没有任何"这不是服务端现值"的交代。
  const [serverReadingStale, setServerReadingStale] = useState(false)
  // 新增/编辑持仓弹窗显隐
  const [showAdd, setShowAdd] = useState(false)
  // §E1 盈亏单轨（owner 裁决 2026-09-26）：显示偏移量不再存 localStorage（旧 'pnl_offset' 键
  // 换设备即丢、全程无痕），改由后端 /api/holdings 随汇总下发，校准动作走 POST /api/holdings/pnl-offset 入库留痕。
  // 偏移值本页只回显、不参与本地算式——总盈亏算式已收敛到后端 paperPnlTotals 一处。
  const [pnlOffset, setPnlOffset] = useState(0)
  // §E1：后端算好的纸面总盈亏（null=后端读数不可得，页头显示"—"，绝不本地兜底重算回两套账）
  const [totalPnl, setTotalPnl] = useState(null)
  // §E1：累计已实现盈亏不再单列 state——它已并入后端 total_pnl 算式（paperPnlTotals），
  // 前端只消费 total_pnl/pnl_offset 两个读数（原 [totalRealizedPnl, setTotalRealizedPnl] 删除）。

  // 当前账本标签：paper=纸面持仓，real=实盘持仓
  const [bookTab, setBookTab] = useState('paper')
  // QMT 网关状态（启用/模式/熔断/网关地址）
  const [qmtState, setQmtState] = useState({ enabled: false, mode: 'manual', tripped: false, gateway_url: '' })
  // 实盘持仓列表
  const [realPositions, setRealPositions] = useState([])
  // 实盘账户资产（广州 QMT 上报的可用资金/冻结/总值/市值）
  const [realAccount, setRealAccount] = useState(null)
  // §MANUAL-BOOK-UNIFY（2026-11-06 裁决 1/2）：book_source 是后端单点装配的"这一维用谁"
  //（{cash, positions, cash_asof, positions_asof, taken_over_at, manual_unavailable?}）。
  // 前端只透传、只按它分流（下单 vs 改账）与挂角标，**绝不**自己在页面上再判一次新鲜度——
  // 两处判一个迟早一处漂，漂的方向不是放行买入就是把手敲账悄悄换成旧柜台账。
  const [realBookSource, setRealBookSource] = useState(null)
  // 2026-11-11：资金维护补齐「总资产」显式输入（不填就不改，服务端绝不推算）
  const [manualSetTotalVal, setManualSetTotalVal] = useState('')
  // 维护模式显式开关（localStorage 记住选择；SSR/隐私模式下 localStorage 不可用也不炸）
  const [bookMode, setBookModeState] = useState(() => {
    try { return localStorage.getItem(BOOK_MODE_KEY) === 'manual' ? 'manual' : 'auto' } catch { return 'auto' }
  })
  function setBookMode(v) {
    setBookModeState(v)
    try { localStorage.setItem(BOOK_MODE_KEY, v) } catch { /* no-op */ }
  }
  // 实盘整体盈亏（/api/qmt/trades summary：realized/unrealized/total_pnl）——页头优先展示
  const [realTrades, setRealTrades] = useState(null)
  // 实盘持仓建议映射（ts_code -> 建议）
  const [realAdvices, setRealAdvices] = useState({})
  // §ADVICE-TACTICS A（2026-11-05）：这一轮被战法开关滤掉的加仓条数（null=读数未知，
  // 旧一轮载荷没带这个键时**不画 0**——"没滤"与"没报"是两件事）。
  const [adviceFiltered, setAdviceFiltered] = useState(null)
  // §TRADE-CARD（2026-11-05）本轮建议的**原始列表**（映射会按 ts_code 折叠，撤单行需要它）：
  // 撤行定位的是委托而不是持仓，被撤的建仓单往往根本没有持仓行 ⇒ 表格里没有落脚处。
  const [adviceRows, setAdviceRows] = useState([])
  // §F-6（20260917 缺陷修复批）建议映射统一入口：SSE 实时推送与挂载 REST 回填共用。
  // 把建议数组按 ts_code（缺省回退 code）归一成映射，保证两个来源写入同一份 state，
  // 覆盖顺序为「REST 先回填、SSE 后覆盖」。
  // §ADVICE-TACTICS B：映射**不再只留 5 个字段**——建议股数/金额/受限项/定权时刻都要带上，
  // 否则后端算好的数在到达浏览器的那一刻又被前端丢了一次（owner 抱怨的正是"屏上没有一个数
  // 是从我的持仓算出来的"）。缺席的字段保持缺席（不是补 0）。
  function applyAdviceMap(advices, filteredBySwitch) {
    const m = {}
    // §TRADE-CARD 新增四列：order_id（撤行定位到具体一笔委托）、trade_kind（做T 标注）、
    // cancel_kind / cancel_source（撤单形态与理由）。全部原样透传，缺席就保持缺席——
    // 补 0 或补默认话术都会让"后端没说"在屏上看上去像"后端说了没有"。
    for (const a of (advices || [])) {
      if (a && (a.ts_code || a.code)) {
        const key = a.ts_code || a.code
        m[key] = {
          action: a.action, label: a.label || a.action, ref_price: a.ref_price, reason: a.reason, level: a.level,
          suggest_qty: a.suggest_qty, suggest_amount: a.suggest_amount, capped_by: a.capped_by, sizing_at: a.sizing_at,
          order_id: a.order_id, trade_kind: a.trade_kind, cancel_kind: a.cancel_kind, cancel_source: a.cancel_source,
        }
      }
    }
    setRealAdvices(m)
    setAdviceRows(advices || [])
    // 载荷没带这个键 ⇒ 未知（null），不是 0。
    setAdviceFiltered(typeof filteredBySwitch === 'number' ? filteredBySwitch : null)
  }
  // 实盘是否启用
  const realEnabled = !!qmtState.enabled
  // 实盘网关是否熔断（熔断后禁止下单）
  const realTripped = !!qmtState.tripped
  // ── §MANUAL-BOOK-UNIFY 两维来源翻译层 ──
  // 三态：counter 可达 / manual 改账态 / unknown 读数未知。unknown 只在两种情况出现：
  // 旧一轮载荷没带 book_source，或后端 manual_view 读取失败（manual_unavailable）。
  // unknown **绝不**折成任何一个端（沿用 CarrierShadowPanel 那条纪律：读数未知不显示 0）——
  // 页头显示"读数未知"，操作列保持今天的下真单行为，绝不自动变成改账。
  function bookDimSource(dim) {
    const bs = realBookSource
    if (!bs || bs.manual_unavailable) return 'unknown'
    const v = bs[dim]
    if (v === 'counter' || v === 'manual') return v
    return 'unknown'
  }
  const cashSrc = bookDimSource('cash')
  const posSrc = bookDimSource('positions')
  const cashManual = cashSrc === 'manual'
  const posManual = posSrc === 'manual'
  // 柜台可达＝两维都标 counter 且**确有柜台读数证据**（asof 或持仓行时刻）——页头"柜台：可达"
  // 与操作列标题的唯一判据。
  // 2026-11-10 owner 需求（全用户手动持仓维护）：book_source 对柜台从未回报过的账号也缺省标
  // counter（SelectBookSources 的"手动不可用⇒保持柜台默认"语义，不改两维语义本身），但那不等于
  // "可达"——无物理机账号（facai2026 等）没有任何柜台读数，若照字面判可达，它们会永远停在
  // "柜台可达+无持仓"的死路上，进不了手动维护入口。可达必须有证据；没证据落 unknown 态。
  // English: "counter" labels without any asof/timestamp evidence are not reachability — a
  // machine-less account falls into the unknown state, which is where manual maintenance starts.
  const counterReachable = cashSrc === 'counter' && posSrc === 'counter' &&
    !!(realBookSource && (realBookSource.cash_asof || realBookSource.positions_asof || realBookSource.counter_frame_at))
  // 改账态＝**任一一维**确认为 manual（两维独立回落，资金新但持仓旧时也要能改账）。
  // 第三态 bookUnknown 既不改账也不报可达：读数未知时操作列保持今天的下真单行为，
  // 绝不把"点下单"静默变成"点记账"——那比按钮点不动更危险。
  // English: three states — both dimensions counter (reachable), any dimension manual (adjust
  // mode), or unknown (keep today's real-order behaviour; never silently switch to bookkeeping).
  // 2026-11-11 owner 需求：柜台帧已冻结（counter_frozen=true，页头挂"以券商 App 为准"）时，
  // 旧逻辑仍把两维标成 counter ⇒ 落进真单态，而网关熔断又把四个按钮全禁用，用户既不能
  // 下单也不能改账。冻结账本就不是可信柜台读数，此时一律进改账态（列标题变「改账」、
  // 四个动作只写 manual_* 不下单），这才是"回报通路重建中"唯一该开的口。
  // English: a frozen counter frame must open the bookkeeping path; otherwise the tripped
  // real-order buttons leave every manual maintenance action greyed out.
  const counterFrozen = !!realBookSource?.counter_frozen
  const manualMode = resolveBookMode(bookMode, cashManual, posManual, counterFrozen)
  const bookUnknown = !counterReachable && !manualMode
  // 当前实盘下单参数（持仓+方向）
  const [realAction, setRealAction] = useState(null)
  // 实盘下单价格/数量/策略
  const [realFormPrice, setRealFormPrice] = useState(0)
  const [realFormQty, setRealFormQty] = useState(0)
  const [realFormStrategy, setRealFormStrategy] = useState('')
  // 实盘下单提交中标记（防重复提交）
  const [realSubmitting, setRealSubmitting] = useState(false)
  // §MANUAL-BOOK-UNIFY 改账弹窗 state（柜台不可达·改账态专用）：四个动作（加仓/减仓/止盈/清仓）
  // 与「改账」新增共用一个 Dialog，op 与按钮一一对应（op ∈ new|add|reduce|tp|close）。
  // 弹窗 header 与确认按钮在改账态带「不下单」，与实盘下单弹窗刻意不同形——
  // 同形＝最容易把"记一笔账"点成"下一笔真单"的形状。
  const [manualAction, setManualAction] = useState(null) // {pos?, dir}
  const [manualFormPrice, setManualFormPrice] = useState(0)
  const [manualFormQty, setManualFormQty] = useState(0)
  const [manualFormCode, setManualFormCode] = useState('')
  const manualFormCodeRef = useRef('')
  const [manualFormName, setManualFormName] = useState('')
  const [manualSubmitting, setManualSubmitting] = useState(false)
  // 「直接改资金」小入口（裁决 3 的手续费/分红/出入金口子）：拆成独立的 state，
  // 免得与改持仓的动作混在同一张弹窗里互相预填。
  const [manualSetCashShow, setManualSetCashShow] = useState(false)
  const [manualSetCashVal, setManualSetCashVal] = useState('')
  // 实盘轮询由 §N-09 的 usePolling 统一管理（进入实盘标签且为 admin 时启用）
  // §M-10（2026-09-22 PM 批清扫）轮询请求代号守卫：纸面 load 与实盘 loadReal 各自独立代号，
  // 60s 轮询/SSE 触发/手动刷新交错时，旧请求的迟到响应整包丢弃（防数据倒挂）。
  const paperGuard = useRef(null)
  if (!paperGuard.current) paperGuard.current = createStaleGuard()
  const realGuard = useRef(null)
  if (!realGuard.current) realGuard.current = createStaleGuard()
  // 2026-11-10 owner 需求：state/trades 两腿的权限位探测结果（null=未探测；false=已吃 403，
  // 后续轮询直接跳过，避免每 60s 重复打一条必 403 的请求；true=有权限，照常拉）。
  const realPermsRef = useRef(null)

  // 编辑中的持仓下标（-1 表示新增）
  const [editingIdx, setEditingIdx] = useState(-1)
  // 新增/编辑表单：代码、成本、数量、查得名称、查得现价、止盈%、止损%
  const [formCode, setFormCode] = useState('')
  const [formCost, setFormCost] = useState(0)
  const [formQty, setFormQty] = useState(0)
  const [lookupName, setLookupName] = useState('')
  const [lookupPrice, setLookupPrice] = useState(0)
  // 止盈/止损百分比（默认 +8% / -5%）
  const [formTp, setFormTp] = useState(8)
  const [formSl, setFormSl] = useState(5)

  // 加减仓弹窗状态与表单（方向 add/sell、成交价/量、现价、目标持仓）
  const [showLot, setShowLot] = useState(false)
  // 改成本弹窗状态
  const [showCost, setShowCost] = useState(false)
  // 批次明细弹窗状态
  const [showLots, setShowLots] = useState(false)
  // 加减仓弹窗的目标持仓
  const [lotTarget, setLotTarget] = useState(null)
  // 改成本弹窗的目标持仓
  const [costTarget, setCostTarget] = useState(null)
  // 批次明细弹窗的目标持仓
  const [lotsTarget, setLotsTarget] = useState(null)
  // 加减仓方向（add=加仓 / sell=减仓）
  const [lotDir, setLotDir] = useState('add')
  // 加减仓表单：成交价、成交数量、该持仓最新现价、改成本表单新成本价
  const [lotFormPrice, setLotFormPrice] = useState(0)
  const [lotFormQty, setLotFormQty] = useState(0)
  const [lotCurrentPrice, setLotCurrentPrice] = useState(0)
  const [costFormPrice, setCostFormPrice] = useState(0)

  // 清仓弹窗状态与表单（清仓价、预览盈亏金额/比例、预览是否有效）
  const [showClose, setShowClose] = useState(false)
  // 清仓弹窗的目标持仓
  const [closeTarget, setCloseTarget] = useState(null)
  // 清仓价输入值
  const [closeFormPrice, setCloseFormPrice] = useState(0)
  // 清仓盈亏预览：金额与百分比
  const [closePnlAmount, setClosePnlAmount] = useState(0)
  const [closePnlPct, setClosePnlPct] = useState(0)
  // 预览是否有效（持仓与价格均合法才显示盈亏预览）
  const [closePreviewValid, setClosePreviewValid] = useState(false)

  // 可用资金编辑状态与输入值
  const [editingBalance, setEditingBalance] = useState(false)
  const [balanceInputVal, setBalanceInputVal] = useState(0)
  // 移动端操作面板对应的持仓
  const [sheetHolding, setSheetHolding] = useState(null)

  // §PERM-GATE 20260918：纸面持仓写操作（updateHoldings/addHoldingLot/setHoldingCost/
  // closeHolding/sellHoldingLot/holdings/balance）后端为 admin 守卫；成员点击必 403，
  // 前端按 admin 收敛写入口。
  // 2026-11-10 owner 需求（全用户手动持仓维护）：实盘账读端点已放宽为账号级 authMiddleware，
  // 实盘 tab 对所有登录用户渲染——手动增删改实盘持仓（manual_* 写面）不再要求任何权限位；
  // canTrading 继续只管运营专属腿（建议回填 advice、execute 下真单）。
  const canFundsWrite = canStrict('funds_admin')
  const canTrading = canStrict('trading_admin')
  // §FIX-20261028-F09：实盘三段拉取的失败台账（失败请名到腿、成功即销案、不清旧读数）
  const ledger = useLoadLedger()

  // §N-09：纸面/实盘两条轮询统一走 usePolling——后台暂停、恢复补一次、卸载清理。
  // 实盘只在当前标签为「实盘」时轮询（2026-11-10 owner 需求：所有登录用户都能维护实盘持仓，
  // 轮询判据从 canLive 收敛为标签本身；无权限的 state/trades 腿在 loadReal 内按 403 静默处理）。
  usePolling(load, 60000, bookTab === 'paper')
  usePolling(loadReal, 60000, bookTab === 'real')

  // 纸面持仓轮询定时器（30s）
  // 纸面/实盘轮询均交给 §N-09 的 usePolling，不再使用页面级定时器 ref
  // SSE 订阅取消函数
  const unsubSSE = useRef(null)

  // §E1：总盈亏 = 后端 /api/holdings 的 total_pnl（已实现 + 浮动 − 入库校准偏移），
  // 旧版此处前端逐持仓自算再减 localStorage 私有偏移（两套账的前端半边），已删除。

  // §F1 是否有实盘数据（持仓或账户上报存在）：有则页头展示实盘盈亏/可用资金，无才回落纸面
  const hasReal = useMemo(
    () => realPositions.length > 0 || (realAccount && (realAccount.updated_at || realAccount.total_asset > 0)),
    [realPositions, realAccount]
  )
  // 实盘可用资金：网关上报的 available_cash（无上报时保持 0，与实盘 tab 口径一致）
  const displayAvailable = hasReal
    ? (realAccount && realAccount.available_cash != null ? realAccount.available_cash : 0)
    : availableBalance
  // 实盘总盈亏：优先用 /api/qmt/trades 的 total_pnl（已实现+浮动）；未取到时按实盘持仓现价-成本×数量兜底
  //
  // §W7-G（2026-10-06 修复批 波 7，owner 裁决「不删兜底，只把口径说清 + 让兜底可见」）：
  // 这条兜底以前**只有一个数、没有身份**——网关成交汇总还没回来（首屏、/api/qmt/trades 失败、
  // 账号未连网关）时页头照样打出一个「实盘 总盈亏: ¥xxxx」，而它是前端逐持仓自算的
  // 「现价−成本×数量」，两条口径不是一回事：
  //   · 网关 total_pnl = 已实现 + 浮动（真成交流水，含已清仓的那段盈亏）；
  //   · 本地兜底      = 只有当前仍在持仓的浮动，已实现部分**不在里面**，
  //     且现价取的是持仓行自带的 cur_price（见本文件 curPrice：缺位或非正一律当 0，
  //     所以行情没到位时兜底数会偏低甚至为负，这不是 bug 而是"没数就别装成有数"的另一形态）。
  // 于是"看到的那个实盘盈亏"能不能和账本对得上，取决于一个页面上完全不可见的条件——
  // 同页下面 §E1 纸面腿写的是相反的纪律（「null 就显示"—"，绝不本地兜底重算，那又是两套账
  // 的老路」），两条取向并存却只有一条被标注，这本身就是缺陷（AUDIT_20261005 P2-新增，
  // FIX_PLAN_20261006 改判为设计张力：兜底留着有价值——首屏不该空着，但必须自报身份）。
  // 因此本批**保留兜底**，改成：兜底生效 ⇒ 派生出 pnlFallback，页头挂出「本地兜底」标记 +
  // 悬停说明口径差异；网关数一到就自动摘掉（真值胜出，不需要用户做任何事）。
  // English: §W7-G — the local fallback stays (owner ruling: annotate, don't delete), but it now
  // announces itself: pnlFallback marks "this number was recomputed in the browser (unrealized P&L
  // of held positions only), not the gateway's settled+floating total".
  const displayPnl = useMemo(() => {
    if (!hasReal) return totalPnl
    if (realTrades && realTrades.total_pnl != null) return realTrades.total_pnl
    let sum = 0
    for (const p of realPositions) {
      const price = curPrice(p) || 0
      sum += (price - (p.cost_price || 0)) * (p.qty || 0)
    }
    return sum
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasReal, realTrades, realPositions, totalPnl])
  // §W7-G 兜底身份：只在「有实盘数据、且网关成交汇总确实没给数」时成立。
  // 判据与 displayPnl 里的分支**同一条件同一次求值**（不是再猜一次），两条腿才会一起翻。
  const pnlFallback = useMemo(
    () => hasReal && !(realTrades && realTrades.total_pnl != null),
    [hasReal, realTrades]
  )
  // §E1 展示串单点：纸面总盈亏的格式化只算一次——页头回落形态与纸面 Tab 内嵌形态共用，
  // null（读数不可得）显示"—"，绝不本地兜底重算（那又是两套账的老路）。
  // 与上面实盘腿的关系（§W7-G 说清，别再留两种取向各写各的）：纸面账**没有**可信的第三方
  // 汇总可回落，所以那里"没数就是没数"；实盘账的权威数（网关 trades）在位时永远优先，
  // 兜底只负责"权威数还在路上"这一段的首屏可读性，并且必须带着「本地兜底」标记出现。
  const paperPnlShown = totalPnl == null ? '—' : `${totalPnl >= 0 ? '+' : ''}¥${totalPnl.toFixed(2)}`

  // 预览加减仓后该持仓的数量（加仓=现量+加量；减仓=现量-减量，无效时归零）
  const lotPreviewQty = useMemo(() => {
    const cur = Number(lotTarget?.quantity) || 0
    const add = Number(lotFormQty) || 0
    if (lotDir === 'add') return add > 0 ? cur + add : 0
    return add > 0 ? cur - add : 0
  }, [lotTarget, lotFormQty, lotDir])

  // 判断减仓数量是否超过当前持仓量（超卖时禁用确认并提示）
  const lotOverSell = useMemo(() => {
    if (lotDir !== 'sell') return false
    const cur = Number(lotTarget?.quantity) || 0
    const sell = Number(lotFormQty) || 0
    return sell > cur
  }, [lotTarget, lotFormQty, lotDir])

  // 加减仓确认按钮是否禁用：成交价或数量无效，或减仓超卖时禁用
  const fareCalcDisabled = useMemo(() => {
    const pr = Number(lotFormPrice) || 0
    const qt = Number(lotFormQty) || 0
    if (lotDir === 'add') return pr <= 0 || qt <= 0
    return pr <= 0 || qt <= 0 || lotOverSell
  }, [lotFormPrice, lotFormQty, lotDir, lotOverSell])

  // 预览加减仓后的加权平均成本（加仓按新量加权，减仓保持原成本）
  const lotPreviewCost = useMemo(() => {
    const cur = Number(lotTarget?.quantity) || 0
    const curCost = Number(lotTarget?.cost_price) || 0
    const add = Number(lotFormQty) || 0
    const addPrice = Number(lotFormPrice) || 0
    if (lotDir === 'add') {
      const total = cur + add
      if (total <= 0) return 0
      return (cur * curCost + add * addPrice) / total
    }
    const remain = cur - add
    if (remain <= 0) return 0
    return curCost
  }, [lotTarget, lotFormQty, lotFormPrice, lotDir])

  // 持仓与资金变动时持久化到 localStorage，供下次进入恢复
  // §H-2（2026-09-22 修复批）缓存写入口加守卫：可用资金保存请求在途时，乐观新值不落缓存，
  // 改按「服务端确认值」持久化——失败回滚后缓存里也绝不会残留错误余额（跨刷新污染根断）；
  // §0929POS-SAVE 同口径补到持仓腿：整表写在途或存在未落库脏行时，缓存写的是**服务端确认过
  // 的那份表**（旧值），绝不把还没存进后端的乐观行带到下次刷新——那是同一族"界面说存了、
  // 后端说没有"的跨刷新污染。落库成功后由 saveHoldings 自己补写一次权威值。
  // English: §H-2/§0929POS-SAVE — while a balance or full-table holdings write is in flight (or
  // any row is still dirty) the cache persists the server-confirmed snapshot, never the optimistic
  // value; a successful write persists the new authoritative table itself.
  useEffect(() => {
    const safeBalance = balancePendingRef.current ? balanceConfirmedRef.current : availableBalance
    const holdingsDirty = dirtyCodesRef.current.size > 0
    const safeHoldings = holdingsPendingRef.current || holdingsDirty ? holdingsConfirmedRef.current : holdings
    persistCache(safeHoldings, safeBalance)
  }, [holdings, availableBalance])

  // 「清零」按钮（§E1 单轨版）：不再本地记偏移，改调后端 POST /api/holdings/pnl-offset
  // 由服务端按自己的算式取整入账（append-only 留痕），成功后重新拉取汇总。
  // 403/网络失败弹窗提示，绝不静默——静默失败会让人以为已校准，回到旧的两套账。
  async function resetPnl() {
    try {
      await api.resetPaperPnlOffset()
      await load()
    } catch (e) {
      showToast(`盈亏校准失败（清零未生效）：${e?.message || e}`, 'error')
    }
  }

  // 加载纸面持仓、资金与已实现盈亏
  // §0929POS-SAVE 两处收口：① 空 catch 不再静默——读数拿不到时置 serverReadingStale，
  // 页头显式挂「服务端读数不可得」徽标（旧写法页面继续显示本地缓存、零提示，
  // 用户会把缓存现值当成服务端现值）；② 存在未落库脏行时**不整表覆盖**用户看到的
  // 那一行（服务端根本没有它 / 还是旧值），其余行照常按权威值刷新，避免一次轮询
  // 把失败的那笔编辑从界面上抹掉。脏行判据取 dirtyCodesRef（ref 不是 state：
  // 轮询回调持有的是挂载时的闭包，读 state 会读到过期集合）。
  // English: §0929POS-SAVE — the load path no longer swallows failures (a header badge marks the
  // reading as server-unavailable), and rows still pending a successful write are kept as shown
  // while every other row refreshes from the authoritative payload.
  async function load() {
    const token = paperGuard.current.begin() // §M-10 本轮代号
    try {
      const st = await api.fetchStatus()
      if (paperGuard.current.isStale(token)) return // 后到的旧轮次：整包丢弃
      api.setLastSession(st.session)
      const data = await api.fetchHoldings()
      if (paperGuard.current.isStale(token)) return
      if (data) {
        setHoldings(mergeOverDirtyRows(data.holdings || []))
        setAvailableBalance(data.available_balance || 0)
        // §H-2（2026-09-22 修复批）服务端回读即权威确认值，同步更新缓存写守卫的基准
        balanceConfirmedRef.current = data.available_balance || 0
        // §0929POS-SAVE 同理：这一包的持仓列表就是"服务端确认过的那份"，
        // 但脏行不在其中（那是尚未落库的本地编辑），确认基准必须与之同构，
        // 否则下一次缓存写入会把脏行连同旧值一起当成权威值落盘。
        holdingsConfirmedRef.current = mergeOverDirtyRows(data.holdings || [])
        // §E1：total_realized_pnl 不再单独入 state——后端 total_pnl 已含该腿（paperPnlTotals）。
        setTotalPnl(data.total_pnl == null ? null : data.total_pnl)
        setPnlOffset(data.pnl_offset || 0)
        setServerReadingStale(false)
      }
    } catch (_) {
      // §P2-J 可吞：不弹 toast（60s 轮询持续故障会每分钟刷屏），改为页头徽标常驻 stale
      // ——失败可见性由"服务端读数字样 + 徽标"承担，属于本页既有的显式降级口径。
      setServerReadingStale(true)
    }
  }

  // §0929POS-SAVE 轮询合并腿：以服务端权威列表为底，把仍在脏状态的行换回界面当前值。
  // 两条分支都要走到（各有专测）：服务端已有该股（编辑未落库→保留本地值）、
  // 服务端还没有该股（新增未落库→把本地行补回列表尾部，否则该行从界面上消失）。
  // English: overlay still-dirty rows on top of the authoritative list, covering both the
  // "server has it but older" and "server does not have it yet" shapes.
  function mergeOverDirtyRows(serverList) {
    // 快速路径也返回**新数组**而不是 serverList 本体（§0929POS-SAVE 写 T3 时撞出来的新依赖，
    // 静态锁 T7 钉住）：fetchHoldings 的数组若直接当界面状态，服务端对象就留在了 state 里，
    // 而 confirmAdd 是从 `holdings` 复制出下一份表的（不像 editHoldingQty 走 JSON 拷贝），
    // 之后任何就地改写都会连着改到"服务端读数基线"——正是本批要消灭的载荷与显示分叉。
    if (dirtyCodesRef.current.size === 0) return serverList.slice()
    const out = serverList.map((h) => (dirtyCodesRef.current.has(h.code) ? holdings.find((x) => x.code === h.code) || h : h))
    const onServer = new Set(serverList.map((h) => h.code))
    for (const h of holdings) {
      if (dirtyCodesRef.current.has(h.code) && !onServer.has(h.code)) out.push(h)
    }
    return out
  }

  // §0929POS-SAVE 脏标记单写入口：ref（判据）与 state（渲染）同源变更，不留分叉窗口
  // English: single mutator so the logic ref and the rendering state always move together
  function setDirtyCodes(codes) {
    const next = new Set(codes)
    dirtyCodesRef.current = next
    setDirtyCodesState(next)
  }

  // 将当前持仓整表同步到后端（POST /api/holdings，full-replace 语义）
  // §0929POS-SAVE 四点改口径（旧实现：无参读闭包 holdings + 空 catch + 无条件写缓存）：
  // ① 待传表由调用方**显式传入**，杜绝把"改动前那份"当成本次要保存的内容；
  // ② 返回布尔，调用点据此决定弹窗是否关闭（失败时绝不假装保存成功）；
  // ③ 在途期间缓存写走 holdingsPendingRef 守卫，成功才推进 holdingsConfirmedRef 并补写缓存；
  // ④ 失败弹 toast 并把本次涉及的代码标脏（行上带「未落库」标记），脏值不进 pos_cache_v1。
  // 不再随载荷上行 available_balance：§P1-11 起资金只能走 /api/holdings/balance 窄口径，
  // 后端在本端点显式丢弃该字段（旧载荷带它是"字段存在但无效"的假契约）。
  // English: §0929POS-SAVE — the full-table write takes the intended list explicitly, returns
  // success/failure, guards the localStorage cache with the in-flight flag, advances the confirmed
  // baseline only on success, and on failure raises a toast plus per-row dirty marks. The balance
  // field is no longer transmitted here because this endpoint drops it by design (§P1-11).
  async function saveHoldings(nextList, dirtyKey) {
    const list = (nextList || []).map(({ lots, ...rest }) => rest)
    holdingsPendingRef.current = true
    try {
      await api.updateHoldings({ holdings: list })
      holdingsConfirmedRef.current = nextList || []
      if (dirtyKey) setDirtyCodes([...dirtyCodesRef.current].filter((c) => c !== dirtyKey))
      persistCache(holdingsConfirmedRef.current, balanceConfirmedRef.current)
      return true
    } catch (e) {
      if (dirtyKey) setDirtyCodes([...dirtyCodesRef.current, dirtyKey])
      showToast(`持仓保存失败（后端未落库，页面显示的是未保存的修改）：${e && e.message ? e.message : e}`, 'error')
      return false
    } finally {
      holdingsPendingRef.current = false
    }
  }

  // 根据输入代码查询股票名称与现价
  async function onCodeInput(code) {
    const c = (code !== undefined ? code : formCode).trim()
    if (c.length < 5) { setLookupName(''); return }
    try {
      const data = await api.fetchStockLookup(c)
      if (data && data.name) { setLookupName(data.name); setLookupPrice(data.price || 0) }
      else { setLookupName('未找到'); setLookupPrice(0) }
    } catch (_) { setLookupName('') } // §P2-J 可吞：查名失败只影响输入框的名称提示，价格/数量照常提交
  }

  // 清空新增/编辑持仓表单（代码、成本、数量、查得名称/现价、止盈止损归零）
  function resetForm() {
    setFormCode(''); setFormCost(0); setFormQty(0); setLookupName(''); setLookupPrice(0)
  }

  // 确认新增或编辑持仓，并同步后端
  // §0929POS-SAVE：本函数原先是 setHoldings(...) 之后直接 await saveHoldings()，
  // 而 saveHoldings 读的是闭包里的旧 holdings（full-replace 端点于是把"改动前那份表"
  // 当成用户要保存的内容发出去，新增行从未上行、编辑行从未更新）。
  // 现在改成先纯函数算出 next 表，再"界面与后端同传一份"：setHoldings(next) 与
  // saveHoldings(next) 用的是同一个值，界面显示与请求载荷不可能分叉；
  // 保存失败时弹窗保持打开（用户填的东西不丢），行上留「未落库」脏标记。
  // English: §0929POS-SAVE — compute the next table once and feed the identical value to both
  // the UI and the backend, and keep the dialog open when the write fails.
  async function confirmAdd() {
    const code = formCode.trim()
    if (!code || !formCost || !formQty) { MessagePlugin.warning('请填写完整信息'); return }
    const item = {
      code,
      name: lookupName || code,
      quantity: formQty,
      cost_price: formCost,
      cur_price: lookupPrice || 0,
      pnl_pct: 0,
      change_pct: 0,
      take_profit_pct: formTp || 8,
      stop_loss_pct: formSl || 5,
    }
    // 编辑模式：替换原持仓的数量/成本/止盈止损；新增模式：追加到列表末尾
    let next
    if (editingIdx >= 0) {
      const cur = holdings[editingIdx]
      next = [...holdings]
      next[editingIdx] = { ...cur, quantity: formQty, cost_price: formCost, take_profit_pct: formTp, stop_loss_pct: formSl }
    } else {
      // §0929POS-SAVE 重试去重：同一代码已有行时按位置替换，不再追加。
      // 触发场景就是本批新增的那条失败恢复路径——上一次整表写失败后弹窗保持打开、
      // 界面上已经留着这条乐观行，用户直接再点一次「确定」。旧写法走 [...holdings, item]
      // 会让同一代码在表和载荷里各出现两次（后端按 code 归一只存一条，界面却显示两行），
      // 而 full-replace 载荷里的重复代码本身就是给后端留"两次更新同一条记录"的模糊语义。
      // English: §0929POS-SAVE — on retry after a failed write the optimistic row is already in
      // the table, so an "add" of the same code must replace it in place instead of appending a
      // duplicate code into a full-replace payload.
      const at = holdings.findIndex((x) => x.code === code)
      next = at >= 0 ? [...holdings.slice(0, at), item, ...holdings.slice(at + 1)] : [...holdings, item]
    }
    setHoldings(next)
    // 同步后端；失败则保留弹窗与脏标记，成功才关窗重置表单
    const ok = await saveHoldings(next, code)
    if (!ok) return
    setShowAdd(false)
    setEditingIdx(-1)
    resetForm()
  }

  // 用指定持仓填充编辑表单并打开编辑弹窗
  function editHolding(h) {
    setEditingIdx(holdings.indexOf(h))
    setFormCode(h.code)
    setFormCost(h.cost_price)
    setFormQty(h.quantity)
    setLookupName(h.name)
    setLookupPrice(h.cur_price)
    setFormTp(h.take_profit_pct || 8)
    setFormSl(h.stop_loss_pct || 5)
    setShowAdd(true)
  }

  // 打开加/减仓弹窗：预填现价并刷新该持仓最新价
  function openAddLot(h) {
    setLotTarget(h)
    setLotDir('add')
    setLotFormPrice(Number(h.cur_price) || 0)
    setLotFormQty(0)
    setLotCurrentPrice(Number(h.cur_price) || 0)
    setShowLot(true)
    refreshLotPrice(h.code)
  }
  // 按代码查询最新价并刷新加减仓弹窗的现价/成交价
  async function refreshLotPrice(code) {
    if (!code) return
    try {
      const data = await api.fetchStockLookup(code)
      if (data && data.price > 0) { setLotCurrentPrice(data.price); setLotFormPrice(data.price) }
    } catch (e) {
      // §P2-J 可吞：预填价取不到时用户仍可手输，提交时后端会再校验；显式清零预填值避免残留上一笔。
      setLotCurrentPrice(0)
      console.warn('[positions] 弹窗预填价不可得（可吞）:', e)
    }
  }
  // 确认加/减仓操作并同步后端
  async function confirmLot() {
    const t = lotTarget
    const price = Number(lotFormPrice)
    const qty = Number(lotFormQty)
    if (!t || price <= 0 || qty <= 0) { MessagePlugin.warning('请填写成交价与成交数量'); return }
    try {
      // 根据方向调用加仓/减仓接口，减仓全部时移除持仓
    if (lotDir === 'sell') {
        const res = await api.sellHoldingLot(t.code, price, qty)
        if (res && res.closed) {
          setHoldings((prev) => prev.filter((x) => x.code !== t.code))
          MessagePlugin.success(`已全部减仓 ${t.code} ${t.name}`)
        } else if (res && res.holding) { upsertHolding(res.holding) }
      } else {
        const res = await api.addHoldingLot(t.code, price, qty)
        if (res && res.holding) upsertHolding(res.holding)
      }
      setShowLot(false)
    } catch (e) { MessagePlugin.error((lotDir === 'sell' ? '减仓失败: ' : '加仓失败: ') + (e.message || '')) }
  }
  // 打开改成本弹窗并预填当前成本价
  function openSetCost(h) {
    setCostTarget(h); setCostFormPrice(Number(h.cost_price) || 0); setShowCost(true)
  }
  // 确认修改持仓成本价
  async function confirmSetCost() {
    const t = costTarget
    const price = Number(costFormPrice)
    if (!t || price <= 0) { MessagePlugin.warning('请输入有效的成本价'); return }
    try {
      const res = await api.setHoldingCost(t.code, price)
      if (res && res.holding) upsertHolding(res.holding)
      setShowCost(false)
    } catch (e) { MessagePlugin.error('更新成本失败: ' + (e ? e.message : '')) }
  }
  // 打开指定持仓的加仓批次明细弹窗
  function showLotsFor(h) { setLotsTarget(h); setShowLots(true) }
  // 新增或更新本地持仓数据
  function upsertHolding(h) {
    setHoldings((prev) => {
      const idx = prev.findIndex((x) => x.code === h.code)
      if (idx >= 0) { const next = [...prev]; next[idx] = h; return next }
      return [...prev, h]
    })
  }
  // 关闭新增/编辑持仓弹窗并清除编辑下标
  function closeAdd() { setShowAdd(false); setEditingIdx(-1) }
  // 打开「新增持仓」弹窗：重置为新增态并设默认止盈+8%/止损-5%
  function openAddNew() { setEditingIdx(-1); resetForm(); setFormTp(8); setFormSl(5); setShowAdd(true) }

  // 根据清仓价实时预览该持仓的盈亏金额与比例（校验价/量是否有效）
  function closePriceInput() {
    const t = closeTarget
    const price = Number(closeFormPrice)
    if (!t || price <= 0 || !t.quantity) { setClosePreviewValid(false); return }
    const qty = t.quantity || 1
    const cost = t.cost_price || 0
    setClosePnlAmount((price - cost) * qty)
    setClosePnlPct(cost > 0 ? (price - cost) / cost * 100 : 0)
    setClosePreviewValid(true)
  }
  // 打开清仓弹窗并预填现价，等待输入清仓价预览盈亏
  function openCloseHolding(h) {
    setCloseTarget(h); setCloseFormPrice(Number(h.cur_price) || 0); setClosePreviewValid(false); setShowClose(true)
  }
  // 确认清仓并移除本地持仓
  async function confirmCloseHolding() {
    const t = closeTarget
    const price = Number(closeFormPrice)
    if (!t || price <= 0) { MessagePlugin.warning('请输入有效的清仓价'); return }
    try {
      // 调用后端清仓接口，返回盈亏金额与百分比
      const res = await api.closeHolding(t.code, price)
      if (res && res.status === 'ok') {
        setHoldings((prev) => prev.filter((x) => x.code !== t.code))
        const amt = res.profit_amount || 0
        const pct = res.profit_pct || 0
        MessagePlugin.success(`已清仓 ${t.code} ${t.name}：盈亏 ${amt >= 0 ? '+' : ''}¥${amt.toFixed(2)}（${pct >= 0 ? '+' : ''}${pct.toFixed(2)}%）`)
      }
      setShowClose(false)
    } catch (e) { MessagePlugin.error('清仓失败: ' + (e.message || '')) }
  }

  // ── 实盘 ──
  // 切换纸面/实盘标签，进入实盘时启动轮询
  function switchBook(tab) {
    setBookTab(tab)
    if (tab === 'real') {
      loadReal()
    }
  }
  // 加载 QMT 状态与实盘持仓
  //
  // §FIX-20261028-F09（2026-10-29）：三段此前都是 `catch (_) {}`——请求失败后什么都不留，
  // 界面照常显示上一轮读数或空表。用户会把"读不到实盘"读成"实盘没有持仓"（本轮 UAT 用
  // page.route 阻断 /api/positions/real 复现：屏上零失败提示）。
  // 现在接仓里统一的"按腿记账"设施（useLoadLedger）：失败只点名那一腿、不清旧数据，
  // 成功即销案；页头用共享的 LoadFailBanner 渲染（testid=load-ledger 全站唯一）。
  async function loadReal() {
    // 2026-11-10 owner 需求（全用户手动持仓维护）：实盘账读三腿改为**所有登录用户**发起
    // （后端 GET /api/positions/real 已放宽为账号级 authMiddleware，按请求者取账）。
    // /api/qmt/state 与 /api/qmt/trades 仍是 trading_admin/quant_trade 守卫：无权限位成员
    // 对这两腿吃 403 时按"无实盘权限"静默处理（保持默认 qmtState，不进失败台账刷红条），
    // 不影响持仓/手动账主腿。运营专属腿（advice 建议回填、execute 下单）仍是 canTrading。
    // English: the real-book leg loads for every logged-in user (account-scoped backend); the
    // state/trades legs stay privileged and their 403s are treated as "no live perms", not failures.
    const token = realGuard.current.begin() // §M-10 本轮代号（三段拉取共用一次判定）
    const failMsg = (e) => (e && e.message ? String(e.message) : '请求失败')
    // state/trades 两腿是 trading_admin/quant_trade 守卫：已探测到无权限（realPermsRef=false）
    // 就不再发必 403 的请求；首次仍发一次以探测（有权限的账号照常拿到读数）。
    if (realPermsRef.current !== false) {
      try {
        const st = await api.fetchQMTState()
        if (st && !realGuard.current.isStale(token)) { setQmtState(st); ledger.clear('实盘状态') }
        realPermsRef.current = true
      } catch (e) {
        // 403＝该账号没有实盘状态读权限（无 quant_trade/trading_admin 位），不是故障：
        // 静默保持默认态（enabled=false ⇒ 熔断徽标不闪红条），只记一次已探测。
        if (api.isForbidden(e)) { realPermsRef.current = false }
        else ledger.mark('实盘状态', failMsg(e))
      }
    }
    try {
      const data = await api.fetchRealPositions()
      if (realGuard.current.isStale(token)) return
      if (data && Array.isArray(data.positions)) setRealPositions(data.positions)
      if (data && data.account) setRealAccount(data.account)
      // §MANUAL-BOOK-UNIFY：book_source（后端单点装配的"这一维用谁"）透传进 state，
      // 操作分流 / 页头状态 / 行角标 全部只认它。载荷没带该键 ⇒ null ＝ 读数未知
      //（前端就这样如实显示），绝不本地重新判一次新鲜度去猜。
      // 顺带把后端随响应下发的柜台帧读数（counter_frame_at/counter_frozen，§LEDGER-FROZEN-BANNER）
      // 并进同一份 state——它们是响应顶层键，不并进来页头的冻结横幅判不到。
      if (data && data.book_source) {
        setRealBookSource({
          ...data.book_source,
          counter_frame_at: data.counter_frame_at || '',
          counter_frozen: !!data.counter_frozen,
        })
      } else setRealBookSource(null)
      ledger.clear('实盘持仓')
    } catch (e) {
      ledger.mark('实盘持仓', failMsg(e))
    }
    // §F1 实盘整体盈亏（已实现+浮动）：页头优先展示，无实盘才回落纸面
    if (realPermsRef.current !== false) {
      try {
        const t = await api.fetchQMTTrades()
        if (realGuard.current.isStale(token)) return
        if (t && t.summary) setRealTrades(t.summary)
        ledger.clear('实盘成交')
      } catch (e) {
        if (!api.isForbidden(e)) ledger.mark('实盘成交', failMsg(e))
      }
    }
  }
  // 取实盘持仓的有效现价（无效价返回 0，避免展示脏数据）
  function curPrice(p) { return (p.cur_price && p.cur_price > 0) ? p.cur_price : 0 }
  // §N-02：实盘数据的"截至时刻"——持仓行与账户 updated_at 的最大值。
  // 为什么必须显式上屏：real_positions 可能停在很久以前，而页面照样有表格与金额，
  // 不标时刻就会让"几周前的对账"看起来和"今天刚回报"一模一样。
  function latestUpdatedAt() {
    let best = ''
    let bestT = -Infinity
    const push = (v) => {
      if (!v) return
      const t = Date.parse(v)
      if (Number.isFinite(t) && t > bestT) { bestT = t; best = v }
    }
    if (realAccount) push(realAccount.updated_at)
    realPositions.forEach((p) => push(p.updated_at))
    return best
  }
  // 计算实盘持仓盈亏百分比（成本或现价为空时返回 0）
  function realPnlPct(p) {
    if (!p.cost_price || p.cost_price <= 0 || !curPrice(p)) return 0
    return (curPrice(p) - p.cost_price) / p.cost_price * 100
  }
  // 按 ts_code 取出该实盘持仓对应的操作建议（无则 null）
  function adviceFor(tsCode) { return realAdvices[tsCode] || null }
  // 将实盘操作方向（add/reduce/tp/close）翻译为中文动作标签
  function realActionLabel(dir) { return ({ add: '加仓', reduce: '减仓', tp: '止盈', close: '清仓' })[dir] || dir }
  // 打开实盘下单确认弹窗：预填参考价与默认数量，若网关已熔断则禁止下单
  function openRealAction(p, dir) {
    if (realTripped) { MessagePlugin.warning('网关已熔断，暂停实盘下单'); return }
    // §F16 持仓不足一手禁减仓：A 股卖出必须整手（100 股）或清仓全卖，
    // qty<100 减仓会被后端 400 拒（"卖出不支持零股"）；提前把用户引到清仓。
    const p0 = p || {}
    if (dir === 'reduce' && (p0.qty || 0) < 100) {
      MessagePlugin.warning('持仓 ' + (p0.qty || 0) + ' 股不足一手，请用「清仓」全卖')
      return
    }
    setRealAction({ pos: p, dir })
    setRealFormPrice(curPrice(p) || p.cost_price || 0)
    // 默认数量：加仓 100 股（一手），减仓则为持仓量（不超过一手）
    setRealFormQty(dir === 'add' ? 100 : Math.min(100, p.qty || 0))
    setRealFormStrategy('')
  }
  // 提交实盘买入/卖出/止盈/清仓委托
  // §0926E2E-W1C（后端同步改造）：现价不可得时后端不再静默放行价格校验，而是 400 要求
  // 显式确认（confirm_no_quote）。这里承接该形态：报"无法获取实时现价"→ 弹二次确认，
  // 用户点「仍要下单」后带确认位**复用同一 client_id** 重发（幂等双保险：即便首请求
  // 已到达柜台，重试也只会命中幂等键返回 duplicate，不会重复下单）。
  async function confirmRealAction() {
    const a = realAction
    if (!a) return
    const qty = a.dir === 'close' ? (a.pos.qty || 0) : Math.round(Number(realFormQty) || 0)
    const price = Number(realFormPrice) || 0
    if (qty <= 0 || price <= 0) { MessagePlugin.warning('请输入有效的价格与数量'); return }
    // §F16 前端兜底：减仓/止盈方向必须 ≥100 且为整手，与后端 400 拒单同口径
    const sell = a.dir === 'reduce' || a.dir === 'tp' || a.dir === 'close'
    if (sell && a.dir !== 'close' && (qty < 100 || qty % 100 !== 0)) {
      MessagePlugin.warning('卖出必须整手（100 的倍数）；如需清仓请用清仓动作')
      return
    }
    setRealSubmitting(true)
    // 一次确认生成一次幂等键（§P2-15），行情确认重试复用同一键
    const clientID = (typeof crypto !== 'undefined' && crypto.randomUUID) ? crypto.randomUUID() : undefined
    const base = {
      code: a.pos.ts_code,
      side: sell ? '卖出' : '买入',
      action: realActionLabel(a.dir),
      qty,
      price,
      strategy: realFormStrategy,
      reason: 'manual:' + a.dir,
      client_id: clientID,
    }
    // 真正提交并处理成功收尾（成功提示+关弹窗+延迟刷新等回报）
    async function submit(payload) {
      const res = await api.executeRealAction(payload)
      MessagePlugin.success((sell ? '卖出' : '买入') + '委托已提交 ' + a.pos.ts_code + ' ' + qty + ' 股' + (res.order_id ? '（单号 ' + res.order_id + '）' : ''))
      setRealAction(null)
      // 委托提交后 2s 刷新实盘持仓，等待网关回报
      setTimeout(loadReal, 2000)
    }
    // 行情不可得的二次确认：Promise 化 DialogPlugin（确认=resolve 继续重发；取消/关闭=reject 静默收尾）
    function confirmNoQuote() {
      return new Promise((resolve, reject) => {
        DialogPlugin.confirm({
          header: '无法获取实时现价',
          body: `行情链路故障，委托价 ${price} 无法与现价核对偏离。请再次核对价格与方向；确需在此状态下下单（如紧急止损），点「仍要下单」，该操作会写入审计留痕。`,
          confirmBtn: '仍要下单',
          cancelBtn: '取消',
          onConfirm: () => { resolve() },
          onClose: () => { reject(new Error('__no_quote_cancel__')) },
        })
      })
    }
    try {
      try {
        await submit(base)
      } catch (e) {
        const msg = String((e && e.message) || '')
        // §P2-J 可吞（非吞错）：这里不是"吃掉错误"——非"无现价"类错误原样 throw 给调用方；
      // 只有"取不到实时现价"会走 confirmNoQuote() 弹二次确认，由用户决定是否仍要下单。
      if (!msg.includes('无法获取实时现价')) throw e
        await confirmNoQuote()
        await submit({ ...base, confirm_no_quote: true })
      }
    } catch (e) {
      const msg = String((e && e.message) || '')
      // 用户主动取消二次确认：静默收场（未下单，无需报错恐吓）
      if (msg.includes('__no_quote_cancel__')) MessagePlugin.info('已取消，未提交委托')
      else MessagePlugin.error('下单失败: ' + msg)
    } finally { setRealSubmitting(false) }
  }

  // ── §MANUAL-BOOK-UNIFY（2026-11-06 裁决 2/3/4）改账态操作 ──
  // 柜台不可达时四个按钮不再发真单，改走 POST /api/qmt/manual-positions 的 adjust（一次一条）：
  // 弹窗与确认按钮都带「不下单」，这是"记账"而不是"委托"——后端会在 op 词表上再拒一次，
  // 但把形状分开是第一道闸：真单弹窗有战法/no-quote 确认流，改账弹窗没有，两个态不该共用一张脸。
  // 打开改账弹窗（有持仓行的四个动作：加仓/减仓/止盈/清仓）
  function openManualAdjust(p, dir) {
    const p0 = p || {}
    // 卖出侧沿用柜台纪律（§F16 同口径）：不足一手的整行减不了，引到「清仓」整行归零。
    if (dir === 'reduce' && (p0.qty || 0) < 100) {
      MessagePlugin.warning('手动账 ' + (p0.qty || 0) + ' 股不足一手，请用「清仓」整行归零')
      return
    }
    setManualAction({ pos: p0, dir })
    setManualFormPrice(curPrice(p0) || p0.cost_price || 0)
    // 默认数量：加仓一手；减/止盈按当前持仓量与一手取小
    setManualFormQty(dir === 'add' ? 100 : Math.min(100, p0.qty || 0))
  }
  // 打开「改账」新增（op=new）：柜台从来没回报过的一码，手工记进出口（只在改账态出现）
  function openManualNew() {
    setManualAction({ pos: null, dir: 'new' })
    setManualFormCode('')
    manualFormCodeRef.current = ''
    setManualFormName('')
    setManualFormPrice(0)
    setManualFormQty(100)
  }
  // 打开「直接改资金」（op=set_cash，裁决 3 的手续费/分红/出入金口子）：
  // 用当前读到的可用资金预填——改的是**绝对值**，后端按"改后可用资金"整值落账并记流水。
  function openManualSetCash() {
    const cur = realAccount && realAccount.available_cash != null ? String(realAccount.available_cash) : ''
    setManualSetCashVal(cur)
    const curTotal = realAccount && realAccount.total_asset != null && Number(realAccount.total_asset) > 0
      ? String(realAccount.total_asset) : ''
    setManualSetTotalVal(curTotal)
    setManualSetCashShow(true)
  }
  async function confirmManualSetCash() {
    const v = Number(String(manualSetCashVal).replace(/[, ]/g, ''))
    if (!Number.isFinite(v) || v < 0) {
      MessagePlugin.error('可用资金没填或不是数字（"没填"与"填 0"是两回事：填 0 表示真的没有可用资金）')
      return
    }
    setManualSubmitting(true)
    try {
      const payload = { op: 'set_cash', available_cash: v }
      // 留空＝本次不改总值（服务端保留旧值）；填了才按人填的绝对值落库。
      const tv = String(manualSetTotalVal || '').trim()
      if (tv !== '') {
        const t = Number(tv.replace(/[, ]/g, ''))
        if (!Number.isFinite(t) || t < 0) { MessagePlugin.error('总资产不是有效数字（留空表示不改）'); return }
        payload.total_asset = t
      }
      await api.adjustManualBook(payload)
      MessagePlugin.success('已记入改账（不下单）：可用资金 ¥' + v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }))
      setManualSetCashShow(false)
      loadReal()
    } catch (e) {
      MessagePlugin.error('改账失败（未产生任何委托）：' + (e && e.message ? e.message : '请求失败'))
    } finally { setManualSubmitting(false) }
  }
  // 提交改账（op ∈ add|reduce|tp|close|new）：与真单语义互斥——本笔不触柜台、不产生委托。
  async function confirmManualAdjust() {
    const a = manualAction
    if (!a) return
    if (a.dir === 'new') {
      // ref 优先：移动端受控 Input 可能不触发 onChange，state 会是空串（owner 11:42 实锤）
      const code = normalizeManualCode(manualFormCodeRef.current || manualFormCode)
      if (!code) { MessagePlugin.warning('请输入代码：改账也必须挂在真实标的下，不留"不知道是谁的仓"'); return }
      const q = Math.round(Number(manualFormQty) || 0)
      const pr = Number(manualFormPrice) || 0
      if (q <= 0 || pr <= 0) { MessagePlugin.warning('请输入有效的价格与数量'); return }
      await submitManualAdjust({ op: 'new', ts_code: code, qty: q, price: pr })
      return
    }
    const qty = a.dir === 'close' ? (a.pos.qty || 0) : Math.round(Number(manualFormQty) || 0)
    const price = Number(manualFormPrice) || 0
    if (qty <= 0 || price <= 0) { MessagePlugin.warning('请输入有效的价格与数量'); return }
    const sell = a.dir === 'reduce' || a.dir === 'tp'
    // 卖出侧沿用柜台两条纪律（§F16 同口径）：非清仓方向的卖出必须整手；
    // 清仓走账上现量（后端 op=close 自取整行，qty 只是留痕）。
    if (sell && (qty < 100 || qty % 100 !== 0)) {
      MessagePlugin.warning('卖出必须整手（100 的倍数）；整行都不要请用「清仓」')
      return
    }
    await submitManualAdjust({ op: a.dir, ts_code: a.pos.ts_code, qty, price })
  }
  // 改账提交唯一出口：成功后重读实盘读数（book_source/接管时刻在服务端已更新），
  // 失败把"没产生委托"原样带上——那正是用户此刻最需要确认的事。
  async function submitManualAdjust(adjust) {
    setManualSubmitting(true)
    try {
      const res = await api.adjustManualBook(adjust)
      const adj = (res && res.adjust) || {}
      MessagePlugin.success('已改账（不下单）· ' + (adj.op || '') + ' ' + (adj.ts_code || '') +
        (adj.qty != null ? ' 改后持仓 ' + adj.qty + ' 股' : ''))
      setManualAction(null)
      loadReal()
    } catch (e) {
      MessagePlugin.error('改账失败（未产生任何委托）：' + (e && e.message ? e.message : '请求失败'))
    } finally { setManualSubmitting(false) }
  }

  // 展开/收起指定代码的分时图（维护已展开代码集合）
  function toggleKline(code) {
    setKlineOpen((prev) => { const next = new Set(prev); if (next.has(code)) next.delete(code); else next.add(code); return next })
  }
  // 桌面端点击行不响应；移动端点击行时打开底部持仓操作面板
  function onRowTap(h) {
    if (window.innerWidth > 768) return
    setSheetHolding(h)
  }

  // 可用资金编辑
  // 进入可用资金编辑态：用当前余额预填输入框
  function editBalanceStart() { setBalanceInputVal(availableBalance); setEditingBalance(true) }
  // 保存可用资金编辑结果：§P1-11（2026-09-15）改走窄口径 POST /api/holdings/balance——
  // 此前整表 saveHoldings() 是 full-replace 语义且后端显式丢弃 balance 字段，
  // 改资金既存不进、还会在并发下把手改持仓整体回写覆盖。
  async function editBalanceSave() {
    // §H-2（2026-09-22 修复批）保存失败三处收口：
    // ① showToast 已补导入（旧码此调用直接 ReferenceError，用户零提示）；
    // ② catch 内回滚乐观写到编辑前值（旧码不回滚，界面长期显示未保存成功的余额）；
    // ③ 在途/失败期间 persistCache 走 balancePendingRef 守卫（见挂载副作用），错误余额
    //    绝不写进 pos_cache_v1 —— 旧码 :231 的无条件持久化会把脏值带到下次刷新。
    // English: §H-2 — on save failure the optimistic write is rolled back, a real toast is
    // shown (showToast import was missing), and the dirty balance never reaches the
    // localStorage cache (guarded by balancePendingRef in the persist effect).
    const prev = availableBalance
    const next = balanceInputVal
    balancePendingRef.current = true
    setAvailableBalance(next); setEditingBalance(false)
    try {
      await api.updateHoldingsBalance(next)
      balanceConfirmedRef.current = next
    } catch (e) {
      balancePendingRef.current = false
      setAvailableBalance(prev) // 回滚到编辑前值
      showToast('保存可用资金失败：' + (e && e.message ? e.message : e), 'error')
    }
    balancePendingRef.current = false
  }
  // 取消可用资金编辑，放弃本次修改
  function editBalanceCancel() { setEditingBalance(false) }

  // 挂载时加载持仓、启动轮询并订阅 SSE 实盘建议；卸载时清理
  useEffect(() => {
    load()
    // §N-09：纸面轮询由 usePolling 管理，此处只做首轮加载与 SSE 订阅
    // 订阅 SSE 事件总线（App 单连接扇出）：处理实盘建议推送与 QMT/订单回报，触发对应刷新
    unsubSSE.current = on(['real_advice', 'qmt_report', 'real_order'], (msg) => {
      // 实盘操作建议：按 ts_code 汇总成建议映射（§ADVICE-TACTICS：一并带上被战法开关滤掉的条数）
      if (msg.type === 'real_advice' && Array.isArray(msg.advices)) {
        applyAdviceMap(msg.advices, msg.filtered_by_switch)
      } else if (msg.type === 'qmt_report' || msg.type === 'real_order') {
        // §M-13（2026-09-22 修复批）前端半：旧写法 `tripped: !!msg.tripped` 在后端
        // qmt_report 广播载荷缺 tripped 字段时被 !!undefined=false 命中，任意一发热报
        // 都把熔断徽标瞬清成「正常」，靠同行 loadReal 一个 RTT 才纠正回来（徽标闪烁，
        // 熔断拦截窗口内用户可能误判网关正常）。现只在载荷确实带该字段时才更新这一位，
        // 缺字段保持原值，由随后的 loadReal REST 权威刷新。
        // English: §M13 (frontend half) — only overwrite the tripped bit when the broadcast
        // actually carries it; a missing field must leave the badge untouched, not clear it.
        setQmtState((prev) => (msg.tripped === undefined ? prev : { ...prev, tripped: !!msg.tripped }))
        loadReal()
      }
    })
    // §F-6（20260917 缺陷修复批）挂载 REST 回填：SSE 断线超补发窗/页面重载后，
    // 先用服务端留存的最近一轮建议点亮"建议"列，后续仍由 SSE 实时覆盖。
    // §0929GATE-403：这条此前是本页唯一没走 admin 预过滤的实盘读——GET /api/positions/advice
    // 在 adminMiddleware 下（server.go:772），成员一挂载就吃一条 403 并被 catch 静默吞掉
    // （既灌 opslog 又让人不知道发生了什么）。与 loadReal() 的 §PERM-GATE 同口径早退。
    if (canTrading) {
      api.fetchRealAdvice().then((r) => {
        if (r && Array.isArray(r.advices) && r.advices.length) applyAdviceMap(r.advices, r.filtered_by_switch)
      }).catch(() => {}) // §P2-J 可吞：建议列是增强信息，失败时保留上一轮映射（§F-6 口径）
    }
    // 卸载时清理：轮询由 usePolling 负责，这里只取消 SSE 订阅
    return () => {
      if (unsubSSE.current) unsubSSE.current()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 纸面持仓表格列定义：代码、名称、数量、成本/现价、当日涨跌、持仓盈亏、
  // 信号标记、N形/龙头/动量评分、止盈止损、移动止盈、分时与操作按钮
  const paperColumns = [
    { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
    // §0929POS-SAVE：脏行（整表写失败后仍未落库）在名称列挂「未落库」标记。
    // 为什么挂在名称列：它是首屏必视列，且行内没有任何其他标记会与之抢位；
    // 只在这一列出现，避免同一事实在多列重复渲染后各自漂移。
    // English: rows whose last full-table write failed carry a "not persisted" tag in the name column.
    { colKey: 'name', title: '名称', width: 110, cell: ({ row }) => (dirtyCodes.has(row.code)
      ? <span><span style={{ color: 'var(--app-faint)' }}>{row.name}</span> <Tag theme="warning" size="small">未落库</Tag></span>
      : <span style={{ color: 'var(--app-faint)' }}>{row.name}</span>) },
    { colKey: 'quantity', title: '数量', width: 70, sorter: (a, b) => (a.quantity || 0) - (b.quantity || 0), cell: ({ row }) => row.quantity },
    { colKey: 'cost_price', title: '成本价', width: 80, sorter: (a, b) => (a.cost_price || 0) - (b.cost_price || 0), cell: ({ row }) => (row.cost_price != null ? '¥' + Number(row.cost_price).toFixed(2) : '-') },
    // §C7：现价/涨跌/盈亏按来源渲染。cost=无行情回退成本（不是现价）；缺数显示 —，不显示 0.00%。
    { colKey: 'cur_price', title: '现价', width: 130, sorter: (a, b) => (a.cur_price || 0) - (b.cur_price || 0), cell: ({ row }) => <PriceCell value={row.cur_price} kind={row.price_kind} /> },
    { colKey: 'change_pct', title: '当日涨跌', width: 90, sorter: (a, b) => (a.change_pct || 0) - (b.change_pct || 0), cell: ({ row }) => <PctCell value={row.change_pct} ok={row.change_ok} /> },
    // 持仓盈亏百分比列：红涨绿跌（§F1 可排序——盯盘最常用「按盈亏排序找雷/找赢家」）
    { colKey: 'pnl_pct', title: '持仓盈亏', width: 90, sorter: (a, b) => (a.pnl_pct || 0) - (b.pnl_pct || 0), cell: ({ row }) => <PctCell value={row.pnl_pct} ok={row.pnl_ok} /> },

    // 信号状态列：有策略信号时显示⚡
    { colKey: 'signal', title: '信号', width: 50, cell: ({ row }) => row.signal_active ? <span title="有策略信号">⚡</span> : <span style={{ color: 'var(--app-border)' }}>—</span> },

    // 战法评分列（N形/龙头/量能）：≥60 红色达标，≥50 黄色观察
    { colKey: 'n_score', title: 'N', width: 55, cell: ({ row }) => { const v = row.n_score || 0; const c = v >= 60 ? 'var(--app-up)' : v > 0 ? 'var(--td-warning-color)' : 'var(--app-text-2)'; return <span style={{ color: c, fontWeight: 600 }}>{v > 0 ? v.toFixed(0) : '—'}</span> } },
    { colKey: 'dragon_score', title: '龙', width: 55, cell: ({ row }) => { const v = row.dragon_score || 0; const c = v >= 60 ? 'var(--app-up)' : v >= 50 ? 'var(--td-warning-color)' : 'var(--app-text-2)'; return <span style={{ color: c, fontWeight: 600 }}>{v > 0 ? v.toFixed(0) : '—'}</span> } },
    { colKey: 'm_score', title: '量', width: 55, cell: ({ row }) => { const v = row.m_score || 0; const c = v >= 50 ? 'var(--td-warning-color)' : 'var(--app-text-2)'; return <span style={{ color: c, fontWeight: 600 }}>{v > 0 ? v.toFixed(0) : '—'}</span> } },

    // 止盈/止损百分比列
    { colKey: 'sl', title: '止盈/止损', width: 110, cell: ({ row }) => <span><span style={{ color: 'var(--app-up)' }}>+{(row.take_profit_pct || 8).toFixed(1)}%</span><span style={{ color: 'var(--app-border)' }}> / </span><span style={{ color: 'var(--app-down)' }}>-{(row.stop_loss_pct || 5).toFixed(1)}%</span></span> },

    // 移动止盈最高价列
    { colKey: 'highest', title: '移动止盈', width: 90, cell: ({ row }) => row.highest_price > 0 ? <span style={{ color: row.highest_price > (row.cost_price || 0) ? 'var(--app-up)' : '#b388ff' }}>¥{row.highest_price.toFixed(2)}</span> : '—' },

    // 分时图展开按钮列
    { colKey: 'kline', title: '分时', width: 70, cell: ({ row }) => <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); toggleKline(row.code) }}>{klineOpen.has(row.code) ? '收起' : '分时'}</Button> },

    // 操作列：加减仓/改成本/明细/编辑/清仓（写操作 admin 守卫，成员仅留只读明细/分时）
    { colKey: 'actions', title: '操作', width: 230, cell: ({ row }) => (
      <div style={{ display: 'flex', gap: 4, justifyContent: 'center', flexWrap: 'wrap' }}>
        {canFundsWrite && <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); openAddLot(row) }}>加减仓</Button>}
        {canFundsWrite && <Button size="small" variant="outline" theme="warning" onClick={(e) => { e.stopPropagation(); openSetCost(row) }}>改成本</Button>}
        <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); showLotsFor(row) }}>明细</Button>
        {canFundsWrite && <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); editHolding(row) }}>编辑</Button>}
        {canFundsWrite && <Button size="small" variant="outline" theme="danger" onClick={(e) => { e.stopPropagation(); openCloseHolding(row) }}>清仓</Button>}
      </div>
    ) },
  ]

  // 实盘持仓表格列定义：代码、名称、数量、成本/现价、持仓盈亏、最高价、建议标签与操作按钮
  const realColumns = [
    // §MANUAL-BOOK-UNIFY 行级来源角标：持仓维确认为手动来源时，每一行都自带"人敲的"身份。
    // 只挂在代码列（首屏必视列）；柜台来源与读数未知一律不挂——角标乱挂等于没有角标。
    // English: rows sourced from the manual book carry an explicit badge; counter/unknown rows do not.
    { colKey: 'ts_code', title: '代码', width: 120, cell: ({ row }) => (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        <span style={{ color: 'var(--app-accent)', fontFamily: 'monospace' }}>{row.ts_code}</span>
        {posSrc === 'manual' && (
          <Tag data-testid="manual-row-badge" theme="warning" size="small"
            title={manualSourceMain(realBookSource?.positions_asof) + ' · ' + MANUAL_SOURCE_NONREALTIME}>改账</Tag>
        )}
      </span>
    ) },
    { colKey: 'name', title: '名称', width: 90, cell: ({ row }) => <span style={{ color: 'var(--app-faint)' }}>{row.name}</span> },
    { colKey: 'qty', title: '数量', width: 70, sorter: (a, b) => (a.qty || 0) - (b.qty || 0), cell: ({ row }) => row.qty },
    { colKey: 'cost_price', title: '成本价', width: 90, sorter: (a, b) => (a.cost_price || 0) - (b.cost_price || 0), cell: ({ row }) => (row.cost_price != null ? '¥' + Number(row.cost_price).toFixed(3) : '-') },
    // §ANCHOR-FIRST（2026-10-24 裁决二）首买锚点=风险线基准（建仓即冻结，加仓摊薄不改写它），
    // 与左侧"成本价"（含费摊薄，盈亏口径）分两列并排——W3 切硬前 owner 要逐票看清两者的差。
    // 0/缺失显示 '—'（存量持仓尚未回填），不得回退成成本价，否则影子对照会被读成"两者相同"。
    // English: the frozen first-buy anchor beside the diluted cost; un-backfilled rows show '—'
    // rather than falling back to cost (which would fake "the two bases agree").
    { colKey: 'anchor_price', title: '首买锚点', width: 90, sorter: (a, b) => (a.anchor_price || 0) - (b.anchor_price || 0), cell: ({ row }) => (row.anchor_price > 0 ? '¥' + Number(row.anchor_price).toFixed(3) : '—') },
    { colKey: 'cur_price', title: '现价', width: 90, sorter: (a, b) => curPrice(a) - curPrice(b), cell: ({ row }) => curPrice(row) ? '¥' + curPrice(row).toFixed(2) : '—' },
    // §F1 实盘持仓盈亏按 realPnlPct 派生值排序（成本价×数量的浮盈率），与展示口径一致
    { colKey: 'pnl', title: '持仓盈亏', width: 90, sorter: (a, b) => realPnlPct(a) - realPnlPct(b), cell: ({ row }) => <span style={{ color: realPnlPct(row) >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{row.cost_price > 0 && curPrice(row) ? (realPnlPct(row) > 0 ? '+' : '') + realPnlPct(row).toFixed(2) + '%' : '—'}</span> },
    { colKey: 'highest_price', title: '最高价', width: 90, sorter: (a, b) => (a.highest_price || 0) - (b.highest_price || 0), cell: ({ row }) => <span>¥{row.highest_price != null ? Number(row.highest_price).toFixed(2) : '—'}</span> },
    // 建议列：动作标签（做T 的行自动带「做T·」前缀）+ §ADVICE-TACTICS B 的"量"那一行
    // （股数/金额/被哪道帽挡住，或「未计算」）。
    // 为什么单独一行而不是并进 Tag：Tag 里塞两个事实就没法一眼判出"这条建议要买多少"；
    // 为什么必须显式写「未计算」而不是留空：留空的格子会被读成"系统算过、算出来是 0"，
    // 而真实情况是当日定权快照里根本没有这一票的行。
    // 标签文案改走 adviceRowLabel 单点：此前直读后端给的 a.label，于是"做 T 要特殊标注"这条
    // 口径只能在后端每个生产者各写一遍——漏一个生产者就漏一类卡。
    // theme 表按**中文动作字面量**判：旧键是 add/reduce/tp 这套英文，而后端 Action 一直是中文，
    // 所以那五行配色从来没有生效过（所有标签一律 default 色）。这是顺手补真，不改任何判定。
    { colKey: 'advice', title: '建议', width: 170, cell: ({ row }) => { const a = adviceFor(row.ts_code); if (!a) return <span style={{ color: 'var(--app-border)' }}>—</span>; const theme = { 加仓: 'danger', 减仓: 'warning', 止盈: 'success', 止损: 'success', 撤单: 'warning', 格局: 'default', 持有: 'default' }[a.action] || 'default'; const suggest = adviceSuggestText(a); return (<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 2 }}><Tag data-testid="advice-tag" theme={theme} size="small">{adviceRowLabel(a)}</Tag>{suggest ? <span data-testid="advice-suggest" className="muted" style={{ fontSize: 12 }}>{suggest}</span> : null}</div>) } },
    // 实盘操作列：加仓/减仓/止盈/清仓。§MANUAL-BOOK-UNIFY（2026-11-06 裁决 2）**一个按钮两种含义**，
    // 判据只认后端单点装配的 book_source：改账态走 adjust（不下单），其余两态走 execute（今天行为）。
    // disabled={realTripped} 只在真单态保留：桥断那天熔断必然已开，若改账态还照旧禁用，
    // 四个按钮会一个都点不动——而那正是这本账唯一要被用上的时刻。
    // §MULTI-ACCT-B 批5：execute 未对 quant_trade 开放，按 canTrading 隐藏的口径不变。
    // English: same four buttons, two meanings — real orders while the counter is reachable,
    // book adjustments (no order) when it is not; unknown keeps today's real-order behaviour.
    // 2026-11-10 owner 需求（全用户手动持仓维护）：改账态（manualMode）对所有登录用户开放——
    // 这写的是"自己声称的账"（manual_*，账号级归属）；真单（柜台可达走 execute）仍是
    // trading_admin 专属，非管理员的柜台态保持 '—'（与既有 §MULTI-ACCT-B 口径一致）。
    { colKey: 'actions', title: manualMode ? '改账' : '操作', width: 200, cell: ({ row }) => {
      if (manualMode) {
        return (
          <div style={{ display: 'flex', gap: 4, justifyContent: 'center' }}>
            <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); openManualAdjust(row, 'add') }}>加仓</Button>
            <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); openManualAdjust(row, 'reduce') }}>减仓</Button>
            <Button size="small" variant="outline" theme="warning" onClick={(e) => { e.stopPropagation(); openManualAdjust(row, 'tp') }}>止盈</Button>
            <Button size="small" variant="outline" theme="danger" onClick={(e) => { e.stopPropagation(); openManualAdjust(row, 'close') }}>清仓</Button>
          </div>
        )
      }
      if (!canTrading) return <span style={{ color: 'var(--app-border)' }}>—</span>
      return (
        <div style={{ display: 'flex', gap: 4, justifyContent: 'center' }}>
          <Button size="small" variant="outline" theme="primary" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'add') }}>加仓</Button>
          <Button size="small" variant="outline" theme="primary" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'reduce') }}>减仓</Button>
          <Button size="small" variant="outline" theme="warning" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'tp') }}>止盈</Button>
          <Button size="small" variant="outline" theme="danger" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'close') }}>清仓</Button>
        </div>
      )
    } },
  ]

  return (
    <div className="page">
      {/* §FIX-20261028-F09：实盘读腿失败必须可见（否则"读不到"被画成"实盘无持仓"） */}
      <LoadFailBanner fails={ledger.fails} page="Positions" />
      <Card style={{ marginBottom: 16 }}>
        <div className="toolbar" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>持仓管理</h2>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            {hasReal ? (
              <>
                {/* 实盘盈亏与可用资金展示。
                    §W7-G：兜底生效时必须自报身份——「本地兜底」标记 + 悬停说明两条口径的差，
                    网关 trades 的 total_pnl 一到标记自动消失（真值胜出，用户不需要做任何事）。
                    标记用 data-testid="real-pnl-fallback" 暴露给 UAT/vitest：同一个数字旁边
                    有没有这一枚，就是这条腿生没生效的唯一判据（测试不许靠数颜色）。 */}
                <div className={displayPnl >= 0 ? 'up' : 'down'} style={{ fontWeight: 600 }}>
                  <span className="muted" style={{ fontSize: 12, marginRight: 4 }}>实盘</span>
                  总盈亏: <span data-testid="real-pnl-value">{displayPnl >= 0 ? '+' : ''}¥{displayPnl.toFixed(2)}</span>
                  {pnlFallback && (
                    <Tag
                      data-testid="real-pnl-fallback"
                      size="small"
                      variant="light"
                      theme="warning"
                      style={{ marginLeft: 6 }}
                      title="这个数是前端按「持仓现价−成本×数量」现算的浮动盈亏，只含仍在持仓的部分；网关成交汇总（/api/qmt/trades 的 total_pnl＝已实现＋浮动）还没回来。拿到真值后本标记自动消失。"
                    >
                      本地兜底
                    </Tag>
                  )}
                </div>
                <div style={{ fontWeight: 600 }}>可用资金: ¥{displayAvailable.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</div>
              </>
            ) : (
              <>
                {/* 纸面持仓页头：总盈亏（含清零按钮）+ 可用资金（可编辑）。
                    §E1：数值为后端算好的 total_pnl；null=读数不可得显示"—"（不本地兜底重算）。
                    清零仅管理员（写端点 admin 守卫，成员点了必 403，入口直接收敛）。
                    §F1 回落：实盘数据在位时页头让位给实盘盈亏，纸面汇总改在纸面 Tab 内嵌渲染
                    （testid 两处互斥同值，UAT §E1 展示腿不依赖栈内是否有实盘数据）。 */}
                <div data-testid="paper-pnl-summary" className={totalPnl == null || totalPnl >= 0 ? 'up' : 'down'} style={{ fontWeight: 600 }}>
                  总盈亏: {paperPnlShown}
                  {canFundsWrite && (
                    <Button size="small" variant="outline" theme="default" onClick={resetPnl}
                      title={`点击后按后端算式把总盈亏校准为 0（偏移 ¥${(pnlOffset || 0).toFixed(2)} → 当前总盈亏，入库留痕）`}
                      style={{ marginLeft: 8 }}>清零</Button>
                  )}
                </div>
                {!editingBalance
                  ? (canFundsWrite
                    ? <div onClick={editBalanceStart} style={{ cursor: 'pointer' }}>可用资金: ¥{availableBalance.toFixed(2)} ✏️</div>
                    : <div>可用资金: ¥{availableBalance.toFixed(2)}</div>)
                  : <InputNumber value={balanceInputVal} min={0} step={0.01} onBlur={editBalanceSave} onEnter={editBalanceSave} onChange={(v) => setBalanceInputVal(Number(v) || 0)} style={{ width: 160 }} autoFocus />}
              </>
            )}
            {/* §0929POS-SAVE 页头两枚状态徽标（放在三元之外：实盘数据在位与否都要看得见）。
                ① serverReadingStale：/api/holdings 读数拿不到时页面显示的其实是
                   pos_cache_v1 的本地缓存值，旧写法零提示（空 catch），这里必须说清
                   "这不是服务端现值"；② dirtyCodes.size：整表写失败后仍有未落库行，
                   配合行上的「未落库」标记给出总笔数。
                English: two header status badges — "server reading unavailable" (what you see is
                the local cache) and "N changes not persisted". */}
            {serverReadingStale && (
              <Tag data-testid="paper-reading-stale" theme="danger" size="small" title="最近一次持仓/资金读数拉取失败，页面显示的是本地缓存值">
                服务端读数不可得
              </Tag>
            )}
            {dirtyCodes.size > 0 && (
              <Tag data-testid="paper-dirty-count" theme="warning" size="small" title="这些持仓的修改没有保存进后端，请联网后重新提交">
                {dirtyCodes.size} 项未落库
              </Tag>
            )}
            {canFundsWrite && <Button theme="primary" onClick={openAddNew}>+ 新增持仓</Button>}
          </div>
        </div>
      </Card>

      <Tabs value={bookTab} onChange={(v) => switchBook(v)}>
        <Tabs.TabPanel value="paper" label="纸面持仓">
          {/* §E1 可达性补腿（2026-09-26）：实盘数据在位时页头被 §F1 回落让位给实盘盈亏，
              纸面「总盈亏+清零」若无此处则整页不可达——真实接了 QMT 的用户将永远点不到清零。
              仅 hasReal 时渲染（与页头回落形态互斥，页面无重复汇总）；数值仍是后端 total_pnl，
              算式与写端点都不另起炉灶。
              English: when real-account data is present the page header shows real PnL (§F1);
              this in-panel row keeps the paper summary and admin reset reachable in that state. */}
          {hasReal && (
            <div data-testid="paper-pnl-summary" style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
              <span className={totalPnl == null || totalPnl >= 0 ? 'up' : 'down'} style={{ fontWeight: 600 }}>
                <span className="muted" style={{ fontSize: 12, marginRight: 4 }}>纸面</span>
                总盈亏: {paperPnlShown}
              </span>
              {canFundsWrite && (
                <Button size="small" variant="outline" theme="default" onClick={resetPnl}
                  title="点击后按后端算式把纸面总盈亏校准为 0（偏移入库留痕）">清零</Button>
              )}
            </div>
          )}
          {/* 有持仓时渲染表格，无持仓时显示空态引导 */}
          {holdings.length > 0 ? (
            // 持仓表格：支持展开分时图、行点击打开操作面板
            <Card>
              {/* 持仓表格：数据绑定/列定义/行展开分时图 */}
              <div data-testid="paper-holdings-table">
              <Table
                data={holdings}
                columns={paperColumns}
                rowKey="code"
                size="small"
                // §N-11e：客户主路径显式空态，不再用 TDesign 默认「暂无数据」混淆加载失败
                empty="暂无纸面持仓"
                // §F5 长持仓分页（默认 20/页，可选 20/50/100）
                pagination={{ defaultPageSize: 20, pageSizeOptions: [20, 50, 100], showJumper: true }}
                // §F1 长持仓列表固定表头
                fixedHeader
                maxHeight="calc(100vh - 320px)"
                expandOnRowClick={false}
                expandedRowKeys={Array.from(klineOpen)}
                onExpandChange={(keys) => setKlineOpen(new Set(keys))}
                //  行展开渲染分时图 
                expandedRow={({ row }) => (
                  <MinuteView code={row.code} name={row.name} />
                )}
              />
              </div>
            </Card>
          ) : (
            // 无持仓空态：引导用户新增持仓
            <Card>
              <div style={{ padding: 24, textAlign: 'center' }}>
                <p className="muted">暂无持仓</p>
                <p className="muted">点击右上角「新增持仓」手动添加，或通过信号页确认买入自动更新</p>
              </div>
            </Card>
          )}

          {/* 图例说明：涨跌颜色/信号标记/止盈止损/评分阈值 */}
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', fontSize: 12, color: 'var(--app-muted)', marginTop: 12 }}>
            <span>当日涨跌红涨绿跌</span>
            <span style={{ color: 'var(--app-text-2)' }}>|</span>
            <span>持仓盈亏红赚绿亏</span>
            <span style={{ color: 'var(--app-text-2)' }}>|</span>
            <span>⚡ 有策略信号</span>
            <span style={{ color: 'var(--app-text-2)' }}>|</span>
            <span>止盈+8% / 止损-5%</span>
            <span style={{ color: 'var(--app-text-2)' }}>|</span>
            <span>N≥60可买 龙≥60买 量≥50关注</span>
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="real" label={realTripped ? '实盘持仓 !' : '实盘持仓'}>
          {/* §PERM-GATE 20260918：实盘读端点原为 admin 守卫，成员 403 曾被 loadReal 的 catch
              静默吞成"空表"——后经 §MULTI-ACCT-B 批5 放开到 canLive（trading_admin 或 quant_trade）。
              2026-11-10 owner 需求（全用户手动持仓维护）：读端点已放宽为账号级 authMiddleware
              （后端归属严格按 requestUserID，A 读不到 B 的账），实仓 tab 对**所有登录用户**渲染；
              无权限位的成员没有 state/trades 徽标读数（loadReal 内按 403 静默跳过），
              但可以读到自己账号的实盘/手动账并增删改。敏感写路径（execute 下真单等）仍 admin。 */}
          <>
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center', marginBottom: 12 }}>
            <span style={{ color: qmtState.enabled ? 'var(--app-down)' : 'var(--app-muted)' }}>{qmtState.enabled ? '已启用' : '未启用'}</span>
            <span className="muted">模式: {qmtState.mode || 'manual'}</span>
            <span style={{ color: qmtState.tripped ? 'var(--app-up)' : 'var(--app-down)' }}>熔断: {qmtState.tripped ? '已熔断' : '正常'}</span>
            {qmtState.gateway_url && <span className="muted">网关 {qmtState.gateway_url}</span>}
            {realAccount && (
              <span style={{ color: 'var(--app-accent)', fontWeight: 600 }}>
                可用资金 {realAccount.updated_at
                  ? '¥' + (realAccount.available_cash || 0).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
                  : '—（网关未上报）'}
              </span>
            )}
            {/* 实盘资产总值（网关上报时显示） */}
            {realAccount && realAccount.total_asset > 0 && (
              <span className="muted">总值 ¥{(realAccount.total_asset || 0).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</span>
            )}
            {/* §N-02：数据截至时刻（超过 4 天未更新转警示色） */}
            {(() => {
              const at = latestUpdatedAt()
              if (!at) return <span className="muted">持仓数据截至 —（网关未上报）</span>
              const ageDays = (Date.now() - Date.parse(at)) / 86400000
              const stale = Number.isFinite(ageDays) && ageDays > 4
              return (
                <span className={stale ? '' : 'muted'} style={stale ? { color: 'var(--td-warning-color)' } : undefined}>
                  持仓数据截至 {at}{stale ? '（超过 4 天未更新）' : ''}
                </span>
              )
            })()}
            {/* 2026-11-11 owner 需求：维护模式显式开关（手动=强制改账，不下单；自动=跟随读数）。 */}
            <span className="muted" style={{ marginLeft: 'auto' }}>维护模式</span>
            <Button size="small" data-testid="book-mode-auto" variant={bookMode === 'auto' ? 'base' : 'outline'} theme={bookMode === 'auto' ? 'primary' : 'default'} onClick={() => setBookMode('auto')}>自动</Button>
            <Button size="small" data-testid="book-mode-manual" variant={bookMode === 'manual' ? 'base' : 'outline'} theme={bookMode === 'manual' ? 'warning' : 'default'} onClick={() => setBookMode('manual')}>手动维护</Button>
            <Button size="small" variant="outline" theme="primary" onClick={loadReal}>刷新</Button>
          </div>

          {/* §MANUAL-BOOK-UNIFY（2026-11-06 裁决 2/4/5）可达性状态行 + 改账态两个入口。
              旧形态是实仓表格之外再挂一块独立的「手动维护账」面板（第二个录入口，难用），
              已按 owner 裁决删除：维护统一落在这张实仓表的四个动作上，冲突时柜台赢。
              状态行放在表格三元式之外 ⇒ 一张持仓都没有时（桥断当天新买的票还没进账）
              照样能看见"现在是改账态"，也照样点得到「改账·新增」。
              unknown 态一律显示"读数未知"这四个字，绝不把没读到的来源画成 0 或空表，
              也不许自动切改账（沿用 CarrierShadowPanel 那条纪律）。
              English: one status strip for the whole tab; adjust-only entries appear solely in
              adjust mode; an unknown source is labelled unknown, never rendered as zero. */}
          <div data-testid="real-book-source" style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', marginBottom: 12, padding: '8px 12px', borderRadius: 6, background: manualMode ? '#fff7e6' : 'transparent', border: manualMode ? '1px solid #ffd591' : '1px solid transparent' }}>
            {bookMode === 'manual' && (
              <>
                <Tag data-testid="book-mode-forced" theme="warning" size="small">维护模式：手动（强制改账）</Tag>
                <span className="muted">四个动作只写手动账，不下单；切回「自动」恢复跟随柜台读数</span>
              </>
            )}
            {/* §LEDGER-FROZEN-BANNER（2026-11-09 实锤后的诚实化）：柜台帧不属于今日 ⇒ 整表是冻结账，
                页头必须挂「以券商 App 为准」——冻结账长得跟真柜台一模一样，是最危险的静默降级。
                counter_frozen/counter_frame_at 由后端随响应下发（qmt.go counterFrameReadout）。 */}
            {realBookSource?.counter_frozen && (
              <>
                <Tag data-testid="counter-frozen" theme="danger" size="small">柜台数据冻结</Tag>
                <span className="muted" data-testid="counter-frozen-note">
                  最新柜台读数 {realBookSource?.counter_frame_at || '—'} · 回报通路重建中 · 持仓与资金以券商 App 为准
                </span>
              </>
            )}
            {!realBookSource?.counter_frozen && counterReachable ? (
              <>
                <Tag data-testid="counter-reachable" theme="success" size="small">柜台：可达</Tag>
                <span className="muted">持仓与可用资金以 QMT 柜台回报为准 · 回报于 {latestUpdatedAt() || '—'}</span>
              </>
            ) : manualMode ? (
              <>
                <Tag data-testid="counter-unreachable" theme="warning" size="small">柜台：不可达 · 改账态</Tag>
                <span className="muted" data-testid="manual-book-note">
                  {manualSourceMain(realBookSource?.positions_asof || realBookSource?.cash_asof)} · {MANUAL_SOURCE_NONREALTIME}
                </span>
                {realBookSource?.taken_over_at && <span className="muted">已被柜台接管于 {realBookSource.taken_over_at}</span>}
                <span className="muted">四个动作只改账（不下单）</span>
                <Button data-testid="manual-new-entry" size="small" variant="outline" theme="warning" onClick={openManualNew}>改账·新增</Button>
                <Button data-testid="manual-set-cash-entry" size="small" variant="outline" theme="default" onClick={openManualSetCash}>直接改资金</Button>
              </>
            ) : bookUnknown ? (
              <>
                <Tag data-testid="book-unknown" theme="danger" size="small">柜台：读数未知</Tag>
                <span className="muted">读数未知：未取得账本来源，操作列保持下真单；请刷新重试</span>
                {/* 2026-11-10 owner 需求（全用户手动持仓维护）：无物理机账号（柜台从未回报、
                    手动账也还没录）从这里开始手动维护——先录一笔，book_source 立即翻成改账态。 */}
                <Button data-testid="manual-new-entry" size="small" variant="outline" theme="warning" onClick={openManualNew}>改账·新增</Button>
                <Button data-testid="manual-set-cash-entry" size="small" variant="outline" theme="default" onClick={openManualSetCash}>直接改资金</Button>
              </>
            ) : null}
          </div>

          {/* §TRADE-CARD（2026-11-05）撤单提示条：这条**必须挂在持仓表格之外**——
              撤行定位的是委托，而被撤的建仓买单往往根本没有对应的持仓行（那只票还没买进来）。
              只把撤单塞进表格"建议"列的结果就是：系统撤了一笔单，屏上一个字都不显示。
              放在 ternary 之前 ⇒ 空持仓（一张建仓单都没成交）时这句话照样看得见。 */}
          {(() => {
            const txt = tradeLevelSummaryText(adviceRows)
            return txt ? <Card><div data-testid="trade-cancel-strip" style={{ padding: '10px 14px', fontSize: 13 }}>{txt}</div></Card> : null
          })()}

          {/* 实盘持仓表格或空态：有持仓渲染表格，无持仓显示启用状态 */}
          {!realPositions.length ? (
            <Card>
              <div style={{ padding: 24, textAlign: 'center' }}>
                <p className="muted">{realEnabled ? '暂无实盘持仓' : '实盘未启用（config.toml 中 qmt.enabled=true 并配置网关）'}</p>
                {realEnabled && <p className="muted">等待 QMT 网关回报 /api/qmt/report 推送持仓对账</p>}
              </div>
            </Card>
          ) : (
            <Card>
              <Table data={realPositions} columns={realColumns} rowKey="ts_code" size="small" pagination={{ defaultPageSize: 20, pageSizeOptions: [20, 50, 100] }} empty="暂无实盘持仓" />
              {/* §ADVICE-TACTICS A：战法开关做的是**硬过滤**（owner 裁决），所以"滤掉几条"必须自己上屏
                  ——不然屏上少了几条加仓建议与"今天本来就没有几条信号"在用户眼里是同一张脸。
                  读数未知（旧一轮载荷没带这个键）时整行不渲染，绝不显示「另有 0 条」。 */}
              {(() => {
                const txt = adviceFilteredText(adviceFiltered)
                return txt ? <div data-testid="advice-filtered" className="muted" style={{ marginTop: 8, fontSize: 12 }}>{txt}</div> : null
              })()}
            </Card>
          )}
          </>
        </Tabs.TabPanel>
      </Tabs>

      {/* §ANCHOR-FIRST W2：成本口径 vs 首买价锚的影子对照（只读，不参与任何处置） */}
      <AnchorShadowCard />

      {/* 移动端操作菜单 */}
      <Dialog visible={!!sheetHolding} header={(sheetHolding ? sheetHolding.code : '') + ' ' + (sheetHolding ? sheetHolding.name : '')} onClose={() => setSheetHolding(null)} footer={false}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <Button variant="outline" theme="primary" onClick={() => { if (sheetHolding) toggleKline(sheetHolding.code); setSheetHolding(null) }}>{sheetHolding && klineOpen.has(sheetHolding.code) ? '收起分时' : '展开分时'}</Button>
          {canFundsWrite && <Button variant="outline" theme="primary" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openAddLot(h) }}>加减仓</Button>}
          {canFundsWrite && <Button variant="outline" theme="warning" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openSetCost(h) }}>改成本</Button>}
          <Button variant="outline" theme="default" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) showLotsFor(h) }}>加仓明细</Button>
          {canFundsWrite && <Button variant="outline" theme="default" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) editHolding(h) }}>编辑持仓</Button>}
          {canFundsWrite && <Button variant="outline" theme="danger" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openCloseHolding(h) }}>清仓</Button>}
          <Button theme="default" onClick={() => setSheetHolding(null)}>取消</Button>
        </div>
      </Dialog>

      {/* 新增/编辑弹窗：代码查询/成本/数量/止盈止损 */}
      <Dialog visible={showAdd} header={editingIdx >= 0 ? '编辑持仓' : '新增持仓'} onClose={closeAdd} onConfirm={confirmAdd} confirmBtn="确定" cancelBtn="取消">
        <Form onSubmit={confirmAdd}>
          <Form.FormItem label="代码">
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <Input value={formCode} disabled={editingIdx >= 0} placeholder="输入代码" onChange={(v) => { setFormCode(v); onCodeInput(v) }} />
              {lookupName && <span className="muted">{lookupName} ¥{(lookupPrice || 0).toFixed(2)}</span>}
            </div>
          </Form.FormItem>
          <Form.FormItem label="成本价">
            <InputNumber value={formCost} min={0} step={0.001} placeholder="成本价" onChange={(v) => setFormCost(Number(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label="持股数">
            <InputNumber value={formQty} min={0} step={1} placeholder="持股数量" onChange={(v) => setFormQty(parseInt(v) || 0)} />
          </Form.FormItem>
          {/* 止盈止损参数：默认 +8%/-5%，用户可自定义 */}
          <Form.FormItem label="止盈%">
            <InputNumber value={formTp} step={0.1} placeholder="默认+8%" onChange={(v) => setFormTp(Number(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label="止损%">
            <InputNumber value={formSl} step={0.1} placeholder="默认-5%" onChange={(v) => setFormSl(Number(v) || 0)} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 加减仓弹窗：方向切换/当前持仓/现价/成交价/数量/预览加权成本 */}
      <Dialog visible={showLot} onClose={() => setShowLot(false)} confirmBtn={{ content: lotDir === 'add' ? '确定加仓' : '确定减仓', disabled: lotOverSell || fareCalcDisabled }} cancelBtn="取消" onConfirm={confirmLot}
        header={<span>加减仓 {lotTarget?.code} {lotTarget?.name}
          <span style={{ marginLeft: 12 }}>
            <Button size="small" variant={lotDir === 'add' ? 'outline' : 'outline'} theme={lotDir === 'add' ? 'danger' : 'default'} onClick={() => setLotDir('add')}>加仓</Button>
            <Button size="small" variant="outline" theme={lotDir === 'sell' ? 'danger' : 'default'} onClick={() => setLotDir('sell')} style={{ marginLeft: 4 }}>减仓</Button>
          </span>
        </span>}
      >
        <Form>
          {/* 当前持仓信息：数量/成本价 */}
          <Form.FormItem label="当前数量">
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <span>{lotTarget?.quantity}</span>
              <span className="muted">当前成本</span>
              <span>¥{lotTarget?.cost_price?.toFixed(2)}</span>
            </div>
          </Form.FormItem>
          {/* 现价展示与快捷填入按钮 */}
          <Form.FormItem label="现价">
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span>{lotCurrentPrice > 0 ? '¥' + lotCurrentPrice.toFixed(2) : '—'}</span>
              {lotCurrentPrice > 0 && <Button size="small" variant="outline" theme="primary" onClick={() => setLotFormPrice(lotCurrentPrice)}>按现价</Button>}
            </div>
          </Form.FormItem>
          {/* 成交价与成交数量输入 */}
          <Form.FormItem label={lotDir === 'add' ? '加仓价' : '减仓价'}>
            <InputNumber value={lotFormPrice} min={0} step={0.001} placeholder="成交价格（默认现价）" onChange={(v) => setLotFormPrice(Number(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label={lotDir === 'add' ? '加仓数量' : '减仓数量'}>
            <InputNumber value={lotFormQty} min={0} step={1} placeholder="成交数量" onChange={(v) => setLotFormQty(parseInt(v) || 0)} />
          </Form.FormItem>
          {/* 加减仓后预览：总股数/加权平均成本/超卖警告 */}
          {lotPreviewQty > 0 && (
            <div className="muted">
              {lotDir === 'add'
                ? <>加仓后：共 {lotPreviewQty} 股 / 平均成本 ¥{lotPreviewCost.toFixed(3)}</>
                : <span style={{ color: lotOverSell ? 'var(--app-up)' : 'var(--app-muted)' }}>
                    {lotOverSell ? '减仓数量超过持仓！' : `减仓后：剩余 ${lotPreviewQty} 股 / 平均成本 ¥${lotPreviewCost.toFixed(3)}`}
                  </span>}
            </div>
          )}
        </Form>
      </Dialog>

      {/* 改成本弹窗：输入新的成本价并确认 */}
      <Dialog visible={showCost} header={`更新成本 ${costTarget?.code} ${costTarget?.name}`} onClose={() => setShowCost(false)} onConfirm={confirmSetCost} confirmBtn="确定" cancelBtn="取消">
        <Form onSubmit={confirmSetCost}>
          <Form.FormItem label="目标成本">
            <InputNumber value={costFormPrice} min={0} step={0.001} placeholder="新的成本价" onChange={(v) => setCostFormPrice(Number(v) || 0)} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 清仓弹窗：输入清仓价并实时预览盈亏金额/比例 */}
      <Dialog visible={showClose} header={`清仓 ${closeTarget?.code} ${closeTarget?.name}`} onClose={() => setShowClose(false)} onConfirm={confirmCloseHolding} confirmBtn="确认清仓" cancelBtn="取消">
        <Form onSubmit={confirmCloseHolding}>
          <Form.FormItem label="当前持仓">
            <span>{closeTarget?.quantity} 股 / 成本 ¥{closeTarget?.cost_price?.toFixed(2)}</span>
          </Form.FormItem>
          <Form.FormItem label="清仓价">
            <InputNumber value={closeFormPrice} min={0} step={0.001} placeholder="清仓价格" onChange={(v) => { setCloseFormPrice(Number(v) || 0); closePriceInput() }} />
          </Form.FormItem>
          {/* 清仓盈亏预览：金额与百分比 */}
          {closePreviewValid && (
            <div className="muted">
              清仓盈亏：<span style={{ color: closePnlAmount >= 0 ? 'var(--app-up)' : 'var(--app-down)' }}>{closePnlAmount >= 0 ? '+' : ''}¥{closePnlAmount.toFixed(2)}</span>
              （{closePnlPct >= 0 ? '+' : ''}{closePnlPct.toFixed(2)}%）
            </div>
          )}
        </Form>
      </Dialog>

      {/* 批次明细弹窗：展示该持仓历次加仓的时间/价格/数量/金额明细 */}
      <Dialog visible={showLots && !!lotsTarget} header={`加仓明细 ${lotsTarget?.code} ${lotsTarget?.name}`} onClose={() => setShowLots(false)} onConfirm={() => setShowLots(false)} confirmBtn="关闭" cancelBtn="">
        <div>
          {/* 表头：时间/价格/数量/金额 四列 */}
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr', gap: 4, fontWeight: 600, fontSize: 13 }}>
            <span>时间</span><span>价格</span><span>数量</span><span>金额</span>
          </div>

          {/* 各批次加仓明细行 */}
          {(lotsTarget?.lots || []).map((lot, i) => (
            <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr', gap: 4, fontSize: 13, borderBottom: '1px solid #e7e7e7', padding: '4px 0' }}>
              <span className="muted">{(lot.at || '').replace('T', ' ').slice(0, 19)}</span>
              <span>¥{lot.price?.toFixed(3)}</span>
              <span>{lot.quantity}</span>
              <span>¥{(lot.price * lot.quantity).toFixed(2)}</span>
            </div>
          ))}
          <div style={{ marginTop: 8, fontSize: 13 }}>合计：{lotsTarget?.quantity} 股 / 平均成本 ¥{lotsTarget?.cost_price?.toFixed(3)}</div>
        </div>
      </Dialog>

      {/* 实盘下单确认弹窗：参考价/数量/战法/预估金额，网关熔断时禁用 */}
      <Dialog visible={!!realAction} header={`实盘${realAction ? realActionLabel(realAction.dir) : ''} ${realAction?.pos.ts_code} ${realAction?.pos.name}`} onClose={() => setRealAction(null)} onConfirm={confirmRealAction} confirmBtn={realSubmitting ? '下单中…' : '确认下单'} cancelBtn="取消">
        <Form onSubmit={confirmRealAction}>
          {/* 实盘持仓信息：数量/成本价 */}
          <Form.FormItem label="当前持仓">
            <span>{realAction?.pos.qty} 股 / 成本 ¥{realAction?.pos.cost_price?.toFixed(3)}</span>
          </Form.FormItem>
          {/* 参考价/数量/战法输入 */}
          <Form.FormItem label="参考价">
            <InputNumber value={realFormPrice} min={0} step={0.001} placeholder="成交参考价" onChange={(v) => setRealFormPrice(Number(v) || 0)} />
          </Form.FormItem>
           {/* §F16 卖出不支持零股：非清仓方向的卖出/减仓数量 min=100 强制一手起，
               与后端 qmt.go:317-327 校验同口径；清仓走 pos.qty 全量，输入框隐藏数量语义。 */}
           <Form.FormItem label={realAction?.dir === 'add' ? '加仓数量' : realAction?.dir === 'close' ? '清仓数量' : '减仓数量'}>
             <InputNumber
               value={realFormQty}
               min={realAction?.dir === 'close' ? 0 : 100}
               step={100}
               disabled={realAction?.dir === 'close'}
               placeholder={realAction?.dir === 'add' ? '股数（一手=100）' : realAction?.dir === 'close' ? '全部持仓 ' + (realAction?.pos?.qty || 0) + ' 股' : '股数（最少一手=100）'}
               onChange={(v) => setRealFormQty(parseInt(v) || 0)}
             />
           </Form.FormItem>
          <Form.FormItem label="战法">
            {/* §P3-UX 20260918：此字段是"归因战法"而非自由标签——买入侧若填写则必须是准入
                白名单内的战法（risk/gate.go checkWhitelist 兜底校验，填错会被拒单）；留空则
                不校验。原文案"（可选）"易被当作可随填的备注，误导成员。 */}
            <Input value={realFormStrategy} placeholder="归因战法（留空=不校验；买入须填准入白名单内战法，否则拒单）" onChange={(v) => setRealFormStrategy(v)} />
          </Form.FormItem>
          {/* 预估金额：数量×参考价 */}
          {realFormQty > 0 && realFormPrice > 0 && (
            <div className="muted">预估金额：¥{(realFormQty * realFormPrice).toFixed(2)}</div>
          )}
        </Form>
      </Dialog>

      {/* §MANUAL-BOOK-UNIFY 改账弹窗（桥断态专用）：与上面真单弹窗**刻意不同形**。
          header 与确认按钮都带「改账 · 不下单」；两个态的确认按钮文案不同
          （真单＝「确认下单」，改账＝「确认改账 · 不下单」）——同形就是
          "以为下了单其实只改了账"最容易发生的那一处。战法/归因字段改账态没有，也不该有。
          English: the adjust dialog never shares its shape with the real-order dialog, precisely
          so an operator cannot mistake bookkeeping for placing a broker order. */}
      <Dialog
        visible={!!manualAction}
        header={'改账 · 不下单｜' + ({ add: '加仓', reduce: '减仓', tp: '止盈', close: '清仓', new: '改账新增' }[manualAction?.dir] || '改账')
          + ' ' + (manualAction?.pos?.ts_code || manualFormCode || '')}
        onClose={() => setManualAction(null)}
        onConfirm={confirmManualAdjust}
        confirmBtn={manualSubmitting ? '改账中…' : '确认改账 · 不下单'}
        cancelBtn="取消"
      >
        <Form onSubmit={confirmManualAdjust}>
          <div data-testid="manual-adjust-dialog" className="muted" style={{ marginBottom: 8 }}>
            本笔只改量仔账本，不产生任何委托；柜台恢复后以柜台回报为准。
          </div>
          {manualAction?.dir === 'new' ? (
            <Form.FormItem label="代码">
              <Input value={manualFormCode} placeholder="输入代码（如 601958 或 600000.SH）"
                onChange={(v) => { manualFormCodeRef.current = String(v ?? ''); setManualFormCode(String(v ?? '')) }}
                onBlur={(v) => { if (v != null) { manualFormCodeRef.current = String(v ?? '') ; setManualFormCode(String(v ?? '')) } }} />
            </Form.FormItem>
          ) : (
            <Form.FormItem label="当前持仓">
              <span>{manualAction?.pos?.qty} 股 / 成本 ¥{manualAction?.pos?.cost_price?.toFixed(3)}</span>
            </Form.FormItem>
          )}
          <Form.FormItem label="成交价">
            <InputNumber value={manualFormPrice} min={0} step={0.001} placeholder="本笔成交价" onChange={(v) => setManualFormPrice(Number(v) || 0)} />
          </Form.FormItem>
          {manualAction?.dir !== 'close' && (
            <Form.FormItem label="数量">
              <InputNumber value={manualFormQty} min={100} step={100} placeholder="股数（一手=100）" onChange={(v) => setManualFormQty(parseInt(v) || 0)} />
            </Form.FormItem>
          )}
          <div className="muted">资金联动：买扣卖加（数量×成交价）；手续费不建模，分红/出入金请用「直接改资金」。</div>
        </Form>
      </Dialog>

      {/* 「直接改资金」（裁决 3 的手续费/分红/出入金口子）：改的是**改后可用资金**整值。
          与改持仓拆成两张弹窗，免得预填值互相污染；真 0 是合法值，没填才是没填。 */}
      <Dialog visible={manualSetCashShow} header="直接改可用资金 · 改账（不下单）"
        onClose={() => setManualSetCashShow(false)} onConfirm={confirmManualSetCash}
        confirmBtn={manualSubmitting ? '改账中…' : '确认改账 · 不下单'} cancelBtn="取消">
        <Form onSubmit={confirmManualSetCash}>
          <div data-testid="manual-set-cash-dialog" className="muted" style={{ marginBottom: 8 }}>
            改的是改后可用资金的整值；手续费/分红/出入金不建模，用这个口子兜平。填 0 表示真的没有可用资金。
          </div>
          <Form.FormItem label="可用资金">
            <InputNumber value={Number(manualSetCashVal) || 0} min={0} step={0.01} placeholder="改后可用资金"
              onChange={(v) => setManualSetCashVal(String(v ?? ''))} />
          </Form.FormItem>
          <Form.FormItem label="总资产（选填）">
            <InputNumber value={manualSetTotalVal === '' ? undefined : Number(manualSetTotalVal)} min={0} step={0.01}
              placeholder="留空＝不改；按券商 App 的总资产填" onChange={(v) => setManualSetTotalVal(v == null ? '' : String(v))} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* §F3 全局个股详情抽屉：代码点开，实时价 + 分时/盘口 + 该标的持仓 */}
      <StockDetailDrawer open={!!detail} code={detail?.code} name={detail?.name}
        related={{ positions: holdings }} onClose={() => setDetail(null)} />
    </div>
  )
}
