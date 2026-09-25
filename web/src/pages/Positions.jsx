// ── 持仓管理页面 Positions.jsx ──
// 纸面持仓（增删改/加减仓/改成本/清仓/批次明细） + 实盘持仓（QMT 网关对账 + 手动下单）。
// 收益展示：纸面总盈亏（已实现+浮动，可一键清零） / 实盘总盈亏（QMT trades 汇总优先）；
// 建议回看：实盘持仓「建议」列由 SSE real_advice 实时推送 + 挂载 REST 回填（§F-6）点亮。
// 纯 TDesign 组件（Tabs / TabPanel / Card / Table / Dialog / Form / Input / InputNumber / Button / Tag），无自定义 CSS。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Tabs, Card, Table, Dialog, Form, Input, InputNumber, Button, Tag, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import MinuteView from '../components/MinuteView.jsx'
import StockDetailDrawer from '../components/StockDetailDrawer.jsx'
import { on } from '../sseBus.js'
import { createStaleGuard } from '../utils/staleGuard.js' // §M-10 轮询后到丢弃
// §H-2（2026-09-22 修复批）：editBalanceSave 的失败分支调用 showToast，但导入区一直没有
// ui.jsx —— 保存可用资金一旦失败即抛 ReferenceError（异步未捕获、用户零提示）。补齐导入。
// English: §H-2 — showToast was called without being imported; the save-failure branch died
// with an uncaught ReferenceError (no toast at all). Import added.
import { showToast } from '../ui.jsx'

// 持仓与资金数据的 localStorage 缓存键
const CACHE_KEY = 'pos_cache_v1'

// 将持仓与资金缓存到 localStorage
function persistCache(holdings, balance) {
  try { localStorage.setItem(CACHE_KEY, JSON.stringify({ holdings, balance })) } catch (_) {}
}
// 从 localStorage 读取持仓与资金缓存
function loadCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    const d = raw ? JSON.parse(raw) : null
    if (d) {
      return { holdings: Array.isArray(d.holdings) ? d.holdings : [], balance: d.balance || 0 }
    }
  } catch (_) {}
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
  // 实盘整体盈亏（/api/qmt/trades summary：realized/unrealized/total_pnl）——页头优先展示
  const [realTrades, setRealTrades] = useState(null)
  // 实盘持仓建议映射（ts_code -> 建议）
  const [realAdvices, setRealAdvices] = useState({})
  // §F-6（20260917 缺陷修复批）建议映射统一入口：SSE 实时推送与挂载 REST 回填共用。
  // 把建议数组按 ts_code（缺省回退 code）归一成 {code: {action,label,ref_price,reason,level}} 映射，
  // 保证两个来源写入同一份 state，覆盖顺序为「REST 先回填、SSE 后覆盖」。
  function applyAdviceMap(advices) {
    const m = {}
    for (const a of (advices || [])) {
      if (a && (a.ts_code || a.code)) {
        const key = a.ts_code || a.code
        m[key] = { action: a.action, label: a.label || a.action, ref_price: a.ref_price, reason: a.reason, level: a.level }
      }
    }
    setRealAdvices(m)
  }
  // 实盘是否启用
  const realEnabled = !!qmtState.enabled
  // 实盘网关是否熔断（熔断后禁止下单）
  const realTripped = !!qmtState.tripped
  // 当前实盘下单参数（持仓+方向）
  const [realAction, setRealAction] = useState(null)
  // 实盘下单价格/数量/策略
  const [realFormPrice, setRealFormPrice] = useState(0)
  const [realFormQty, setRealFormQty] = useState(0)
  const [realFormStrategy, setRealFormStrategy] = useState('')
  // 实盘下单提交中标记（防重复提交）
  const [realSubmitting, setRealSubmitting] = useState(false)
  // 实盘轮询定时器（进入实盘标签时启动）
  const realTimer = useRef(null)
  // §M-10（2026-09-22 PM 批清扫）轮询请求代号守卫：纸面 load 与实盘 loadReal 各自独立代号，
  // 60s 轮询/SSE 触发/手动刷新交错时，旧请求的迟到响应整包丢弃（防数据倒挂）。
  const paperGuard = useRef(null)
  if (!paperGuard.current) paperGuard.current = createStaleGuard()
  const realGuard = useRef(null)
  if (!realGuard.current) realGuard.current = createStaleGuard()

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
  // closeHolding/sellHoldingLot/holdings/balance）与实盘读（/api/positions/real 系）后端均为
  // admin 守卫（server.go:629-634/660-662）；成员点击必 403。前端按 admin 收敛写入口、
  // 实盘 tab 显示无权限面板（此前 403 被 loadReal 的 catch 静默吞成"空表"，误导为无持仓）。
  const admin = api.isAdmin()

  // 纸面持仓轮询定时器（30s）
  const timer = useRef(null)
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
  // §E1 展示串单点：纸面总盈亏的格式化只算一次——页头回落形态与纸面 Tab 内嵌形态共用，
  // null（读数不可得）显示"—"，绝不本地兜底重算（那又是两套账的老路）。
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
  // 持仓变化仍照常随写。
  // English: §H-2 — while a balance save is in flight the optimistic value is never written to
  // the localStorage cache; the last server-confirmed balance is persisted instead, so a failed
  // save can no longer poison pos_cache_v1 across refreshes.
  useEffect(() => {
    const safeBalance = balancePendingRef.current ? balanceConfirmedRef.current : availableBalance
    persistCache(holdings, safeBalance)
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
  async function load() {
    const token = paperGuard.current.begin() // §M-10 本轮代号
    try {
      const st = await api.fetchStatus()
      if (paperGuard.current.isStale(token)) return // 后到的旧轮次：整包丢弃
      api.setLastSession(st.session)
      const data = await api.fetchHoldings()
      if (paperGuard.current.isStale(token)) return
      if (data) {
        setHoldings(data.holdings || [])
        setAvailableBalance(data.available_balance || 0)
        // §H-2（2026-09-22 修复批）服务端回读即权威确认值，同步更新缓存写守卫的基准
        balanceConfirmedRef.current = data.available_balance || 0
        // §E1：total_realized_pnl 不再单独入 state——后端 total_pnl 已含该腿（paperPnlTotals）。
        setTotalPnl(data.total_pnl == null ? null : data.total_pnl)
        setPnlOffset(data.pnl_offset || 0)
      }
    } catch (_) {}
  }

  // 将当前持仓与资金同步到后端
  async function saveHoldings() {
    try {
      const list = holdings.map(({ lots, ...rest }) => rest)
      await api.updateHoldings({ holdings: list, available_balance: availableBalance })
    } catch (_) {}
  }

  // 根据输入代码查询股票名称与现价
  async function onCodeInput(code) {
    const c = (code !== undefined ? code : formCode).trim()
    if (c.length < 5) { setLookupName(''); return }
    try {
      const data = await api.fetchStockLookup(c)
      if (data && data.name) { setLookupName(data.name); setLookupPrice(data.price || 0) }
      else { setLookupName('未找到'); setLookupPrice(0) }
    } catch (_) { setLookupName('') }
  }

  // 清空新增/编辑持仓表单（代码、成本、数量、查得名称/现价、止盈止损归零）
  function resetForm() {
    setFormCode(''); setFormCost(0); setFormQty(0); setLookupName(''); setLookupPrice(0)
  }

  // 确认新增或编辑持仓，并同步后端
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
    if (editingIdx >= 0) {
      setHoldings((prev) => {
        const next = [...prev]
        const cur = next[editingIdx]
        next[editingIdx] = { ...cur, quantity: formQty, cost_price: formCost, take_profit_pct: formTp, stop_loss_pct: formSl }
        return next
      })
    } else {
      setHoldings((prev) => [...prev, item])
    }
    // 同步后端、关闭弹窗、重置表单
    await saveHoldings()
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
    } catch (_) {}
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
      // 进入实盘标签时启动 30s 轮询对账
      if (!realTimer.current) realTimer.current = setInterval(loadReal, 60000) // §F5 实盘持仓兜底 60s（回报走 SSE）
    } else if (realTimer.current) {
      clearInterval(realTimer.current); realTimer.current = null
    }
  }
  // 加载 QMT 状态与实盘持仓
  async function loadReal() {
    if (!admin) return // §PERM-GATE：成员无实盘读权限（admin 守卫），不发起必 403 的请求
    const token = realGuard.current.begin() // §M-10 本轮代号（三段拉取共用一次判定）
    try { const st = await api.fetchQMTState(); if (st && !realGuard.current.isStale(token)) setQmtState(st) } catch (_) {}
    try {
      const data = await api.fetchRealPositions()
      if (realGuard.current.isStale(token)) return
      if (data && Array.isArray(data.positions)) setRealPositions(data.positions)
      if (data && data.account) setRealAccount(data.account)
    } catch (_) {}
    // §F1 实盘整体盈亏（已实现+浮动）：页头优先展示，无实盘才回落纸面
    try {
      const t = await api.fetchQMTTrades()
      if (realGuard.current.isStale(token)) return
      if (t && t.summary) setRealTrades(t.summary)
    } catch (_) {}
  }
  // 取实盘持仓的有效现价（无效价返回 0，避免展示脏数据）
  function curPrice(p) { return (p.cur_price && p.cur_price > 0) ? p.cur_price : 0 }
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
    // 构造实盘下单请求参数并提交
    try {
      const res = await api.executeRealAction({
        code: a.pos.ts_code,
        side: sell ? '卖出' : '买入',
        action: realActionLabel(a.dir),
        qty,
        price,
        strategy: realFormStrategy,
        reason: 'manual:' + a.dir,
      })
      MessagePlugin.success((sell ? '卖出' : '买入') + '委托已提交 ' + a.pos.ts_code + ' ' + qty + ' 股' + (res.order_id ? '（单号 ' + res.order_id + '）' : ''))
      setRealAction(null)
      // 委托提交后 2s 刷新实盘持仓，等待网关回报
      setTimeout(loadReal, 2000)
    } catch (e) { MessagePlugin.error('下单失败: ' + (e.message || '')) }
    finally { setRealSubmitting(false) }
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
    load(); timer.current = setInterval(load, 60000) // §F5 纸面持仓兜底轮询降为 60s
    // 订阅 SSE 事件总线（App 单连接扇出）：处理实盘建议推送与 QMT/订单回报，触发对应刷新
    unsubSSE.current = on(['real_advice', 'qmt_report', 'real_order'], (msg) => {
      // 实盘操作建议：按 ts_code 汇总成建议映射
      if (msg.type === 'real_advice' && Array.isArray(msg.advices)) {
        applyAdviceMap(msg.advices)
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
    api.fetchRealAdvice().then((r) => {
      if (r && Array.isArray(r.advices) && r.advices.length) applyAdviceMap(r.advices)
    }).catch(() => {})
    // 卸载时清理：纸面轮询/实盘轮询/SSE订阅
    return () => {
      if (timer.current) clearInterval(timer.current)
      if (realTimer.current) clearInterval(realTimer.current)
      if (unsubSSE.current) unsubSSE.current()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 纸面持仓表格列定义：代码、名称、数量、成本/现价、当日涨跌、持仓盈亏、
  // 信号标记、N形/龙头/动量评分、止盈止损、移动止盈、分时与操作按钮
  const paperColumns = [
    { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
    { colKey: 'name', title: '名称', width: 90, cell: ({ row }) => <span style={{ color: 'var(--app-faint)' }}>{row.name}</span> },
    { colKey: 'quantity', title: '数量', width: 70, sorter: (a, b) => (a.quantity || 0) - (b.quantity || 0), cell: ({ row }) => row.quantity },
    { colKey: 'cost_price', title: '成本价', width: 80, sorter: (a, b) => (a.cost_price || 0) - (b.cost_price || 0), cell: ({ row }) => (row.cost_price != null ? '¥' + Number(row.cost_price).toFixed(2) : '-') },
    { colKey: 'cur_price', title: '现价', width: 80, sorter: (a, b) => (a.cur_price || 0) - (b.cur_price || 0), cell: ({ row }) => (row.cur_price != null ? '¥' + Number(row.cur_price).toFixed(2) : '-') },
    { colKey: 'change_pct', title: '当日涨跌', width: 90, sorter: (a, b) => (a.change_pct || 0) - (b.change_pct || 0), cell: ({ row }) => <span style={{ color: (row.change_pct || 0) >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%</span> },
    // 持仓盈亏百分比列：红涨绿跌（§F1 可排序——盯盘最常用「按盈亏排序找雷/找赢家」）
    { colKey: 'pnl_pct', title: '持仓盈亏', width: 90, sorter: (a, b) => (a.pnl_pct || 0) - (b.pnl_pct || 0), cell: ({ row }) => <span style={{ color: (row.pnl_pct || 0) >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{(row.pnl_pct || 0) > 0 ? '+' : ''}{(row.pnl_pct || 0).toFixed(2)}%</span> },

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
        {admin && <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); openAddLot(row) }}>加减仓</Button>}
        {admin && <Button size="small" variant="outline" theme="warning" onClick={(e) => { e.stopPropagation(); openSetCost(row) }}>改成本</Button>}
        <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); showLotsFor(row) }}>明细</Button>
        {admin && <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); editHolding(row) }}>编辑</Button>}
        {admin && <Button size="small" variant="outline" theme="danger" onClick={(e) => { e.stopPropagation(); openCloseHolding(row) }}>清仓</Button>}
      </div>
    ) },
  ]

  // 实盘持仓表格列定义：代码、名称、数量、成本/现价、持仓盈亏、最高价、建议标签与操作按钮
  const realColumns = [
    { colKey: 'ts_code', title: '代码', width: 90, cell: ({ row }) => <span style={{ color: 'var(--app-accent)', fontFamily: 'monospace' }}>{row.ts_code}</span> },
    { colKey: 'name', title: '名称', width: 90, cell: ({ row }) => <span style={{ color: 'var(--app-faint)' }}>{row.name}</span> },
    { colKey: 'qty', title: '数量', width: 70, sorter: (a, b) => (a.qty || 0) - (b.qty || 0), cell: ({ row }) => row.qty },
    { colKey: 'cost_price', title: '成本价', width: 90, sorter: (a, b) => (a.cost_price || 0) - (b.cost_price || 0), cell: ({ row }) => (row.cost_price != null ? '¥' + Number(row.cost_price).toFixed(3) : '-') },
    { colKey: 'cur_price', title: '现价', width: 90, sorter: (a, b) => curPrice(a) - curPrice(b), cell: ({ row }) => curPrice(row) ? '¥' + curPrice(row).toFixed(2) : '—' },
    // §F1 实盘持仓盈亏按 realPnlPct 派生值排序（成本价×数量的浮盈率），与展示口径一致
    { colKey: 'pnl', title: '持仓盈亏', width: 90, sorter: (a, b) => realPnlPct(a) - realPnlPct(b), cell: ({ row }) => <span style={{ color: realPnlPct(row) >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{row.cost_price > 0 && curPrice(row) ? (realPnlPct(row) > 0 ? '+' : '') + realPnlPct(row).toFixed(2) + '%' : '—'}</span> },
    { colKey: 'highest_price', title: '最高价', width: 90, sorter: (a, b) => (a.highest_price || 0) - (b.highest_price || 0), cell: ({ row }) => <span>¥{row.highest_price != null ? Number(row.highest_price).toFixed(2) : '—'}</span> },
    { colKey: 'advice', title: '建议', width: 80, cell: ({ row }) => { const a = adviceFor(row.ts_code); if (!a) return <span style={{ color: 'var(--app-border)' }}>—</span>; const theme = { add: 'danger', reduce: 'warning', tp: 'success', close: 'success', hold: 'default' }[a.action] || 'default'; return <Tag theme={theme} size="small">{a.label}</Tag> } },
    //  实盘操作列：加仓/减仓/止盈/清仓（熔断时禁用） 
    { colKey: 'actions', title: '操作', width: 200, cell: ({ row }) => (
      <div style={{ display: 'flex', gap: 4, justifyContent: 'center' }}>
        <Button size="small" variant="outline" theme="primary" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'add') }}>加仓</Button>
        <Button size="small" variant="outline" theme="primary" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'reduce') }}>减仓</Button>
        <Button size="small" variant="outline" theme="warning" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'tp') }}>止盈</Button>
        <Button size="small" variant="outline" theme="danger" disabled={realTripped} onClick={(e) => { e.stopPropagation(); openRealAction(row, 'close') }}>清仓</Button>
      </div>
    ) },
  ]

  return (
    <div className="page">
      <Card style={{ marginBottom: 16 }}>
        <div className="toolbar" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>持仓管理</h2>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            {hasReal ? (
              <>
                {/* 实盘盈亏与可用资金展示 */}
                <div className={displayPnl >= 0 ? 'up' : 'down'} style={{ fontWeight: 600 }}>
                  <span className="muted" style={{ fontSize: 12, marginRight: 4 }}>实盘</span>
                  总盈亏: {displayPnl >= 0 ? '+' : ''}¥{displayPnl.toFixed(2)}
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
                  {admin && (
                    <Button size="small" variant="outline" theme="default" onClick={resetPnl}
                      title={`点击后按后端算式把总盈亏校准为 0（偏移 ¥${(pnlOffset || 0).toFixed(2)} → 当前总盈亏，入库留痕）`}
                      style={{ marginLeft: 8 }}>清零</Button>
                  )}
                </div>
                {!editingBalance
                  ? (admin
                    ? <div onClick={editBalanceStart} style={{ cursor: 'pointer' }}>可用资金: ¥{availableBalance.toFixed(2)} ✏️</div>
                    : <div>可用资金: ¥{availableBalance.toFixed(2)}</div>)
                  : <InputNumber value={balanceInputVal} min={0} step={0.01} onBlur={editBalanceSave} onEnter={editBalanceSave} onChange={(v) => setBalanceInputVal(Number(v) || 0)} style={{ width: 160 }} autoFocus />}
              </>
            )}
            {admin && <Button theme="primary" onClick={openAddNew}>+ 新增持仓</Button>}
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
              {admin && (
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
              <Table
                data={holdings}
                columns={paperColumns}
                rowKey="code"
                size="small"
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
          {/* §PERM-GATE 20260918：实盘读端点均 admin 守卫（server.go:660-662），成员 403 曾被
              loadReal 的 catch 静默吞成"空表"——改为显式无权限面板，与 Quant 页姿势一致 */}
          {!admin ? (
            <Card>
              <div style={{ padding: 18, borderRadius: 8, background: '#fff7e6', border: '1px solid #ffd591', color: 'var(--td-warning-color)', fontSize: 13 }}>
                🔒 无权限访问实盘持仓：当前登录「{api.getAccount() || '未知'}」为普通用户，实盘账本仅管理员账号可见。
              </div>
            </Card>
          ) : (
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
            <Button size="small" variant="outline" theme="primary" onClick={loadReal} style={{ marginLeft: 'auto' }}>刷新</Button>
          </div>

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
              <Table data={realPositions} columns={realColumns} rowKey="ts_code" size="small" pagination={{ defaultPageSize: 20, pageSizeOptions: [20, 50, 100] }} />
            </Card>
          )}
          </>
          )}
        </Tabs.TabPanel>
      </Tabs>

      {/* 移动端操作菜单 */}
      <Dialog visible={!!sheetHolding} header={(sheetHolding ? sheetHolding.code : '') + ' ' + (sheetHolding ? sheetHolding.name : '')} onClose={() => setSheetHolding(null)} footer={false}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <Button variant="outline" theme="primary" onClick={() => { if (sheetHolding) toggleKline(sheetHolding.code); setSheetHolding(null) }}>{sheetHolding && klineOpen.has(sheetHolding.code) ? '收起分时' : '展开分时'}</Button>
          {admin && <Button variant="outline" theme="primary" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openAddLot(h) }}>加减仓</Button>}
          {admin && <Button variant="outline" theme="warning" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openSetCost(h) }}>改成本</Button>}
          <Button variant="outline" theme="default" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) showLotsFor(h) }}>加仓明细</Button>
          {admin && <Button variant="outline" theme="default" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) editHolding(h) }}>编辑持仓</Button>}
          {admin && <Button variant="outline" theme="danger" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openCloseHolding(h) }}>清仓</Button>}
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

      {/* §F3 全局个股详情抽屉：代码点开，实时价 + 分时/盘口 + 该标的持仓 */}
      <StockDetailDrawer open={!!detail} code={detail?.code} name={detail?.name}
        related={{ positions: holdings }} onClose={() => setDetail(null)} />
    </div>
  )
}
