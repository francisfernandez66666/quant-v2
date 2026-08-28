// ── 持仓管理页面 Positions.jsx ──
// 纸面持仓（增删改/加减仓/改成本/清仓/批次明细） + 实盘持仓（QMT 网关对账 + 手动下单）。
// 纯 TDesign 组件（Tabs / TabPanel / Card / Table / Dialog / Form / Input / InputNumber / Button / Tag），无自定义 CSS。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Tabs, Card, Table, Dialog, Form, Input, InputNumber, Button, Tag, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import MinuteView from '../components/MinuteView.jsx'

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
  const cache = loadCache()
  // 纸面持仓列表
  const [holdings, setHoldings] = useState(cache.holdings)
  // 已展开分时图的持仓代码集合
  const [klineOpen, setKlineOpen] = useState(new Set())
  // 可用资金余额
  const [availableBalance, setAvailableBalance] = useState(cache.balance)
  // 新增/编辑持仓弹窗显隐
  const [showAdd, setShowAdd] = useState(false)
  // 盈亏显示偏移量（用于「清零」显示，持久化到 localStorage）
  const [pnlOffset, setPnlOffset] = useState(parseFloat(localStorage.getItem('pnl_offset') || '0'))
  // 累计已实现盈亏
  const [totalRealizedPnl, setTotalRealizedPnl] = useState(0)

  // 当前账本标签：paper=纸面持仓，real=实盘持仓
  const [bookTab, setBookTab] = useState('paper')
  // QMT 网关状态（启用/模式/熔断/网关地址）
  const [qmtState, setQmtState] = useState({ enabled: false, mode: 'manual', tripped: false, gateway_url: '' })
  // 实盘持仓列表
  const [realPositions, setRealPositions] = useState([])
  // 实盘账户资产（广州 QMT 上报的可用资金/冻结/总值/市值）
  const [realAccount, setRealAccount] = useState(null)
  // 实盘持仓建议映射（ts_code -> 建议）
  const [realAdvices, setRealAdvices] = useState({})
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
  // 实盘下单提交中标记
  const [realSubmitting, setRealSubmitting] = useState(false)
  // 实盘轮询定时器（30s）
  const realTimer = useRef(null)

  // 编辑中的持仓下标（-1 表示新增）
  const [editingIdx, setEditingIdx] = useState(-1)
  // 新增/编辑表单：代码、成本、数量、查得名称、查得现价、止盈%、止损%
  const [formCode, setFormCode] = useState('')
  const [formCost, setFormCost] = useState(0)
  const [formQty, setFormQty] = useState(0)
  const [lookupName, setLookupName] = useState('')
  const [lookupPrice, setLookupPrice] = useState(0)
  const [formTp, setFormTp] = useState(8)
  const [formSl, setFormSl] = useState(5)

  // 加减仓弹窗状态与表单（方向 add/sell、成交价/量、现价、目标持仓）
  const [showLot, setShowLot] = useState(false)
  // 改成本弹窗状态
  const [showCost, setShowCost] = useState(false)
  // 批次明细弹窗状态
  const [showLots, setShowLots] = useState(false)
  const [lotTarget, setLotTarget] = useState(null)
  const [costTarget, setCostTarget] = useState(null)
  const [lotsTarget, setLotsTarget] = useState(null)
  const [lotDir, setLotDir] = useState('add')
  const [lotFormPrice, setLotFormPrice] = useState(0)
  const [lotFormQty, setLotFormQty] = useState(0)
  const [lotCurrentPrice, setLotCurrentPrice] = useState(0)
  const [costFormPrice, setCostFormPrice] = useState(0)

  // 清仓弹窗状态与表单（清仓价、预览盈亏金额/比例、预览是否有效）
  const [showClose, setShowClose] = useState(false)
  const [closeTarget, setCloseTarget] = useState(null)
  const [closeFormPrice, setCloseFormPrice] = useState(0)
  const [closePnlAmount, setClosePnlAmount] = useState(0)
  const [closePnlPct, setClosePnlPct] = useState(0)
  const [closePreviewValid, setClosePreviewValid] = useState(false)

  // 可用资金编辑状态与输入值
  const [editingBalance, setEditingBalance] = useState(false)
  const [balanceInputVal, setBalanceInputVal] = useState(0)
  // 移动端操作面板对应的持仓
  const [sheetHolding, setSheetHolding] = useState(null)

  // 纸面持仓轮询定时器（30s）
  const timer = useRef(null)
  // SSE 订阅取消函数
  const unsubSSE = useRef(null)

  const totalPnl = useMemo(() => {
    let sum = totalRealizedPnl
    for (const h of holdings) {
      const qty = h.quantity || 1
      const cost = h.cost_price || 0
      const cur = h.cur_price || 0
      sum += (cur - cost) * qty
    }
    return sum - pnlOffset
  }, [holdings, totalRealizedPnl, pnlOffset])

  const lotPreviewQty = useMemo(() => {
    const cur = Number(lotTarget?.quantity) || 0
    const add = Number(lotFormQty) || 0
    if (lotDir === 'add') return add > 0 ? cur + add : 0
    return add > 0 ? cur - add : 0
  }, [lotTarget, lotFormQty, lotDir])

  const lotOverSell = useMemo(() => {
    if (lotDir !== 'sell') return false
    const cur = Number(lotTarget?.quantity) || 0
    const sell = Number(lotFormQty) || 0
    return sell > cur
  }, [lotTarget, lotFormQty, lotDir])

  const fareCalcDisabled = useMemo(() => {
    const pr = Number(lotFormPrice) || 0
    const qt = Number(lotFormQty) || 0
    if (lotDir === 'add') return pr <= 0 || qt <= 0
    return pr <= 0 || qt <= 0 || lotOverSell
  }, [lotFormPrice, lotFormQty, lotDir, lotOverSell])

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

  useEffect(() => { persistCache(holdings, availableBalance) }, [holdings, availableBalance])

  // 以当前总盈亏为基准设置偏移量，实现「清零」显示
  function resetPnl() {
    let off = totalRealizedPnl
    for (const h of holdings) {
      const qty = h.quantity || 1
      const cost = h.cost_price || 0
      const cur = h.cur_price || 0
      off += (cur - cost) * qty
    }
    setPnlOffset(off)
    localStorage.setItem('pnl_offset', off.toString())
  }

  // 加载纸面持仓、资金与已实现盈亏
  async function load() {
    try {
      const st = await api.fetchStatus()
      api.setLastSession(st.session)
      const data = await api.fetchHoldings()
      if (data) {
        setHoldings(data.holdings || [])
        setAvailableBalance(data.available_balance || 0)
        setTotalRealizedPnl(data.total_realized_pnl || 0)
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
    await saveHoldings()
    setShowAdd(false)
    setEditingIdx(-1)
    resetForm()
  }

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

  function openAddLot(h) {
    setLotTarget(h)
    setLotDir('add')
    setLotFormPrice(Number(h.cur_price) || 0)
    setLotFormQty(0)
    setLotCurrentPrice(Number(h.cur_price) || 0)
    setShowLot(true)
    refreshLotPrice(h.code)
  }
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
  function showLotsFor(h) { setLotsTarget(h); setShowLots(true) }
  // 新增或更新本地持仓数据
  function upsertHolding(h) {
    setHoldings((prev) => {
      const idx = prev.findIndex((x) => x.code === h.code)
      if (idx >= 0) { const next = [...prev]; next[idx] = h; return next }
      return [...prev, h]
    })
  }
  function closeAdd() { setShowAdd(false); setEditingIdx(-1) }
  function openAddNew() { setEditingIdx(-1); resetForm(); setFormTp(8); setFormSl(5); setShowAdd(true) }

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
  function openCloseHolding(h) {
    setCloseTarget(h); setCloseFormPrice(Number(h.cur_price) || 0); setClosePreviewValid(false); setShowClose(true)
  }
  // 确认清仓并移除本地持仓
  async function confirmCloseHolding() {
    const t = closeTarget
    const price = Number(closeFormPrice)
    if (!t || price <= 0) { MessagePlugin.warning('请输入有效的清仓价'); return }
    try {
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
      if (!realTimer.current) realTimer.current = setInterval(loadReal, 30000)
    } else if (realTimer.current) {
      clearInterval(realTimer.current); realTimer.current = null
    }
  }
  // 加载 QMT 状态与实盘持仓
  async function loadReal() {
    try { const st = await api.fetchQMTState(); if (st) setQmtState(st) } catch (_) {}
    try {
      const data = await api.fetchRealPositions()
      if (data && Array.isArray(data.positions)) setRealPositions(data.positions)
      if (data && data.account) setRealAccount(data.account)
    } catch (_) {}
  }
  function curPrice(p) { return (p.cur_price && p.cur_price > 0) ? p.cur_price : 0 }
  function realPnlPct(p) {
    if (!p.cost_price || p.cost_price <= 0 || !curPrice(p)) return 0
    return (curPrice(p) - p.cost_price) / p.cost_price * 100
  }
  function adviceFor(tsCode) { return realAdvices[tsCode] || null }
  function realActionLabel(dir) { return ({ add: '加仓', reduce: '减仓', tp: '止盈', close: '清仓' })[dir] || dir }
  function openRealAction(p, dir) {
    if (realTripped) { MessagePlugin.warning('网关已熔断，暂停实盘下单'); return }
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
    const sell = a.dir === 'reduce' || a.dir === 'tp' || a.dir === 'close'
    setRealSubmitting(true)
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

  function toggleKline(code) {
    setKlineOpen((prev) => { const next = new Set(prev); if (next.has(code)) next.delete(code); else next.add(code); return next })
  }
  function onRowTap(h) {
    if (window.innerWidth > 768) return
    setSheetHolding(h)
  }

  // 可用资金编辑
  function editBalanceStart() { setBalanceInputVal(availableBalance); setEditingBalance(true) }
  function editBalanceSave() {
    setAvailableBalance(balanceInputVal); setEditingBalance(false); saveHoldings()
  }
  function editBalanceCancel() { setEditingBalance(false) }

  // 挂载时加载持仓、启动轮询并订阅 SSE 实盘建议；卸载时清理
  useEffect(() => {
    load(); timer.current = setInterval(load, 30000) // 每 30s 轮询纸面持仓
    // 订阅 SSE：处理实盘建议推送与 QMT 回报/订单回报，触发对应刷新
    unsubSSE.current = api.onSSE((msg) => {
      if (!msg || !msg.type) return
      // 实盘操作建议：按 ts_code 汇总成建议映射
      if (msg.type === 'real_advice' && Array.isArray(msg.advices)) {
        const m = {}
        for (const a of msg.advices) {
          if (a && (a.ts_code || a.code)) {
            const key = a.ts_code || a.code
            m[key] = { action: a.action, label: a.label || a.action, ref_price: a.ref_price, reason: a.reason, level: a.level }
          }
        }
        setRealAdvices(m)
      } else if (msg.type === 'qmt_report' || msg.type === 'real_order') {
        setQmtState((prev) => ({ ...prev, tripped: !!msg.tripped }))
        loadReal()
      }
    })
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
    { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span style={{ color: '#4fc3f7', fontFamily: 'monospace' }}>{row.code}</span> },
    { colKey: 'name', title: '名称', width: 90, cell: ({ row }) => <span style={{ color: '#ccc' }}>{row.name}</span> },
    { colKey: 'quantity', title: '数量', width: 70, cell: ({ row }) => row.quantity },
    { colKey: 'cost_price', title: '成本价', width: 80, cell: ({ row }) => (row.cost_price != null ? '¥' + Number(row.cost_price).toFixed(2) : '-') },
    { colKey: 'cur_price', title: '现价', width: 80, cell: ({ row }) => (row.cur_price != null ? '¥' + Number(row.cur_price).toFixed(2) : '-') },
    { colKey: 'change_pct', title: '当日涨跌', width: 90, cell: ({ row }) => <span style={{ color: (row.change_pct || 0) >= 0 ? '#e34d59' : '#00a870', fontWeight: 600 }}>{(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%</span> },
    { colKey: 'pnl_pct', title: '持仓盈亏', width: 90, cell: ({ row }) => <span style={{ color: (row.pnl_pct || 0) >= 0 ? '#e34d59' : '#00a870', fontWeight: 600 }}>{(row.pnl_pct || 0) > 0 ? '+' : ''}{(row.pnl_pct || 0).toFixed(2)}%</span> },
    { colKey: 'signal', title: '信号', width: 50, cell: ({ row }) => row.signal_active ? <span title="有策略信号">⚡</span> : <span style={{ color: '#e7e7e7' }}>—</span> },
    { colKey: 'n_score', title: 'N', width: 55, cell: ({ row }) => { const v = row.n_score || 0; const c = v >= 60 ? '#e34d59' : v > 0 ? '#FAAD14' : '#555'; return <span style={{ color: c, fontWeight: 600 }}>{v > 0 ? v.toFixed(0) : '—'}</span> } },
    { colKey: 'dragon_score', title: '龙', width: 55, cell: ({ row }) => { const v = row.dragon_score || 0; const c = v >= 60 ? '#e34d59' : v >= 50 ? '#FAAD14' : '#555'; return <span style={{ color: c, fontWeight: 600 }}>{v > 0 ? v.toFixed(0) : '—'}</span> } },
    { colKey: 'm_score', title: '量', width: 55, cell: ({ row }) => { const v = row.m_score || 0; const c = v >= 50 ? '#FAAD14' : '#555'; return <span style={{ color: c, fontWeight: 600 }}>{v > 0 ? v.toFixed(0) : '—'}</span> } },
    { colKey: 'sl', title: '止盈/止损', width: 110, cell: ({ row }) => <span><span style={{ color: '#e34d59' }}>+{(row.take_profit_pct || 8).toFixed(1)}%</span><span style={{ color: '#e7e7e7' }}> / </span><span style={{ color: '#00a870' }}>-{(row.stop_loss_pct || 5).toFixed(1)}%</span></span> },
    { colKey: 'highest', title: '移动止盈', width: 90, cell: ({ row }) => row.highest_price > 0 ? <span style={{ color: row.highest_price > (row.cost_price || 0) ? '#e34d59' : '#b388ff' }}>¥{row.highest_price.toFixed(2)}</span> : '—' },
    { colKey: 'kline', title: '分时', width: 70, cell: ({ row }) => <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); toggleKline(row.code) }}>{klineOpen.has(row.code) ? '收起' : '分时'}</Button> },
    { colKey: 'actions', title: '操作', width: 230, cell: ({ row }) => (
      <div style={{ display: 'flex', gap: 4, justifyContent: 'center', flexWrap: 'wrap' }}>
        <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); openAddLot(row) }}>加减仓</Button>
        <Button size="small" variant="outline" theme="warning" onClick={(e) => { e.stopPropagation(); openSetCost(row) }}>改成本</Button>
        <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); showLotsFor(row) }}>明细</Button>
        <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); editHolding(row) }}>编辑</Button>
        <Button size="small" variant="outline" theme="danger" onClick={(e) => { e.stopPropagation(); openCloseHolding(row) }}>清仓</Button>
      </div>
    ) },
  ]

  // 实盘持仓表格列定义：代码、名称、数量、成本/现价、持仓盈亏、最高价、建议标签与操作按钮
  const realColumns = [
    { colKey: 'ts_code', title: '代码', width: 90, cell: ({ row }) => <span style={{ color: '#4fc3f7', fontFamily: 'monospace' }}>{row.ts_code}</span> },
    { colKey: 'name', title: '名称', width: 90, cell: ({ row }) => <span style={{ color: '#ccc' }}>{row.name}</span> },
    { colKey: 'qty', title: '数量', width: 70, cell: ({ row }) => row.qty },
    { colKey: 'cost_price', title: '成本价', width: 90, cell: ({ row }) => (row.cost_price != null ? '¥' + Number(row.cost_price).toFixed(3) : '-') },
    { colKey: 'cur_price', title: '现价', width: 90, cell: ({ row }) => curPrice(row) ? '¥' + curPrice(row).toFixed(2) : '—' },
    { colKey: 'pnl', title: '持仓盈亏', width: 90, cell: ({ row }) => <span style={{ color: realPnlPct(row) >= 0 ? '#e34d59' : '#00a870', fontWeight: 600 }}>{row.cost_price > 0 && curPrice(row) ? (realPnlPct(row) > 0 ? '+' : '') + realPnlPct(row).toFixed(2) + '%' : '—'}</span> },
    { colKey: 'highest_price', title: '最高价', width: 90, cell: ({ row }) => <span>¥{row.highest_price != null ? Number(row.highest_price).toFixed(2) : '—'}</span> },
    { colKey: 'advice', title: '建议', width: 80, cell: ({ row }) => { const a = adviceFor(row.ts_code); if (!a) return <span style={{ color: '#e7e7e7' }}>—</span>; const theme = { add: 'danger', reduce: 'warning', tp: 'success', close: 'success', hold: 'default' }[a.action] || 'default'; return <Tag theme={theme} size="small">{a.label}</Tag> } },
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
            <div className={totalPnl >= 0 ? 'up' : 'down'} style={{ fontWeight: 600 }}>
              总盈亏: {totalPnl >= 0 ? '+' : ''}¥{totalPnl.toFixed(2)}
              <Button size="small" variant="outline" theme="default" onClick={resetPnl} style={{ marginLeft: 8 }}>清零</Button>
            </div>
            {!editingBalance
              ? <div onClick={editBalanceStart} style={{ cursor: 'pointer' }}>可用资金: ¥{availableBalance.toFixed(2)} ✏️</div>
              : <InputNumber value={balanceInputVal} min={0} step={0.01} onBlur={editBalanceSave} onEnter={editBalanceSave} onChange={(v) => setBalanceInputVal(Number(v) || 0)} style={{ width: 160 }} autoFocus />}
            <Button theme="primary" onClick={openAddNew}>+ 新增持仓</Button>
          </div>
        </div>
      </Card>

      <Tabs value={bookTab} onChange={(v) => switchBook(v)}>
        <Tabs.TabPanel value="paper" label="纸面持仓">
          {holdings.length > 0 ? (
            <Card>
              <Table
                data={holdings}
                columns={paperColumns}
                rowKey="code"
                size="small"
                pagination={false}
                expandOnRowClick={false}
                expandedRowKeys={Array.from(klineOpen)}
                onExpandChange={(keys) => setKlineOpen(new Set(keys))}
                expandedRow={({ row }) => (
                  <MinuteView code={row.code} name={row.name} />
                )}
              />
            </Card>
          ) : (
            <Card>
              <div style={{ padding: 24, textAlign: 'center' }}>
                <p className="muted">暂无持仓</p>
                <p className="muted">点击右上角「新增持仓」手动添加，或通过信号页确认买入自动更新</p>
              </div>
            </Card>
          )}

          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', fontSize: 12, color: '#888', marginTop: 12 }}>
            <span>当日涨跌红涨绿跌</span>
            <span style={{ color: '#555' }}>|</span>
            <span>持仓盈亏红赚绿亏</span>
            <span style={{ color: '#555' }}>|</span>
            <span>⚡ 有策略信号</span>
            <span style={{ color: '#555' }}>|</span>
            <span>止盈+8% / 止损-5%</span>
            <span style={{ color: '#555' }}>|</span>
            <span>N≥60可买 龙≥60买 量≥50关注</span>
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="real" label={realTripped ? '实盘持仓 !' : '实盘持仓'}>
          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center', marginBottom: 12 }}>
            <span style={{ color: qmtState.enabled ? '#00a870' : '#888' }}>{qmtState.enabled ? '已启用' : '未启用'}</span>
            <span className="muted">模式: {qmtState.mode || 'manual'}</span>
            <span style={{ color: qmtState.tripped ? '#e34d59' : '#00a870' }}>熔断: {qmtState.tripped ? '已熔断' : '正常'}</span>
            {qmtState.gateway_url && <span className="muted">网关 {qmtState.gateway_url}</span>}
            {realAccount && (
              <span style={{ color: '#4fc3f7', fontWeight: 600 }}>
                可用资金 ¥{(realAccount.available_cash || 0).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}
              </span>
            )}
            {realAccount && realAccount.total_asset > 0 && (
              <span className="muted">总值 ¥{(realAccount.total_asset || 0).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}</span>
            )}
            <Button size="small" variant="outline" theme="primary" onClick={loadReal} style={{ marginLeft: 'auto' }}>刷新</Button>
          </div>

          {!realPositions.length ? (
            <Card>
              <div style={{ padding: 24, textAlign: 'center' }}>
                <p className="muted">{realEnabled ? '暂无实盘持仓' : '实盘未启用（config.toml 中 qmt.enabled=true 并配置网关）'}</p>
                {realEnabled && <p className="muted">等待 QMT 网关回报 /api/qmt/report 推送持仓对账</p>}
              </div>
            </Card>
          ) : (
            <Card>
              <Table data={realPositions} columns={realColumns} rowKey="ts_code" size="small" pagination={false} />
            </Card>
          )}
        </Tabs.TabPanel>
      </Tabs>

      {/* 移动端操作菜单 */}
      <Dialog visible={!!sheetHolding} header={(sheetHolding ? sheetHolding.code : '') + ' ' + (sheetHolding ? sheetHolding.name : '')} onClose={() => setSheetHolding(null)} footer={false}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <Button variant="outline" theme="primary" onClick={() => { if (sheetHolding) toggleKline(sheetHolding.code); setSheetHolding(null) }}>{sheetHolding && klineOpen.has(sheetHolding.code) ? '收起分时' : '展开分时'}</Button>
          <Button variant="outline" theme="primary" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openAddLot(h) }}>加减仓</Button>
          <Button variant="outline" theme="warning" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openSetCost(h) }}>改成本</Button>
          <Button variant="outline" theme="default" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) showLotsFor(h) }}>加仓明细</Button>
          <Button variant="outline" theme="default" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) editHolding(h) }}>编辑持仓</Button>
          <Button variant="outline" theme="danger" onClick={() => { const h = sheetHolding; setSheetHolding(null); if (h) openCloseHolding(h) }}>清仓</Button>
          <Button theme="default" onClick={() => setSheetHolding(null)}>取消</Button>
        </div>
      </Dialog>

      {/* 新增/编辑弹窗 */}
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
          <Form.FormItem label="止盈%">
            <InputNumber value={formTp} step={0.1} placeholder="默认+8%" onChange={(v) => setFormTp(Number(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label="止损%">
            <InputNumber value={formSl} step={0.1} placeholder="默认-5%" onChange={(v) => setFormSl(Number(v) || 0)} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 加减仓弹窗 */}
      <Dialog visible={showLot} onClose={() => setShowLot(false)} confirmBtn={{ content: lotDir === 'add' ? '确定加仓' : '确定减仓', disabled: lotOverSell || fareCalcDisabled }} cancelBtn="取消" onConfirm={confirmLot}
        header={<span>加减仓 {lotTarget?.code} {lotTarget?.name}
          <span style={{ marginLeft: 12 }}>
            <Button size="small" variant={lotDir === 'add' ? 'outline' : 'outline'} theme={lotDir === 'add' ? 'danger' : 'default'} onClick={() => setLotDir('add')}>加仓</Button>
            <Button size="small" variant="outline" theme={lotDir === 'sell' ? 'danger' : 'default'} onClick={() => setLotDir('sell')} style={{ marginLeft: 4 }}>减仓</Button>
          </span>
        </span>}
      >
        <Form>
          <Form.FormItem label="当前数量">
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <span>{lotTarget?.quantity}</span>
              <span className="muted">当前成本</span>
              <span>¥{lotTarget?.cost_price?.toFixed(2)}</span>
            </div>
          </Form.FormItem>
          <Form.FormItem label="现价">
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span>{lotCurrentPrice > 0 ? '¥' + lotCurrentPrice.toFixed(2) : '—'}</span>
              {lotCurrentPrice > 0 && <Button size="small" variant="outline" theme="primary" onClick={() => setLotFormPrice(lotCurrentPrice)}>按现价</Button>}
            </div>
          </Form.FormItem>
          <Form.FormItem label={lotDir === 'add' ? '加仓价' : '减仓价'}>
            <InputNumber value={lotFormPrice} min={0} step={0.001} placeholder="成交价格（默认现价）" onChange={(v) => setLotFormPrice(Number(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label={lotDir === 'add' ? '加仓数量' : '减仓数量'}>
            <InputNumber value={lotFormQty} min={0} step={1} placeholder="成交数量" onChange={(v) => setLotFormQty(parseInt(v) || 0)} />
          </Form.FormItem>
          {lotPreviewQty > 0 && (
            <div className="muted">
              {lotDir === 'add'
                ? <>加仓后：共 {lotPreviewQty} 股 / 平均成本 ¥{lotPreviewCost.toFixed(3)}</>
                : <span style={{ color: lotOverSell ? '#e34d59' : '#888' }}>
                    {lotOverSell ? '减仓数量超过持仓！' : `减仓后：剩余 ${lotPreviewQty} 股 / 平均成本 ¥${lotPreviewCost.toFixed(3)}`}
                  </span>}
            </div>
          )}
        </Form>
      </Dialog>

      {/* 改成本弹窗 */}
      <Dialog visible={showCost} header={`更新成本 ${costTarget?.code} ${costTarget?.name}`} onClose={() => setShowCost(false)} onConfirm={confirmSetCost} confirmBtn="确定" cancelBtn="取消">
        <Form onSubmit={confirmSetCost}>
          <Form.FormItem label="目标成本">
            <InputNumber value={costFormPrice} min={0} step={0.001} placeholder="新的成本价" onChange={(v) => setCostFormPrice(Number(v) || 0)} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 清仓弹窗 */}
      <Dialog visible={showClose} header={`清仓 ${closeTarget?.code} ${closeTarget?.name}`} onClose={() => setShowClose(false)} onConfirm={confirmCloseHolding} confirmBtn="确认清仓" cancelBtn="取消">
        <Form onSubmit={confirmCloseHolding}>
          <Form.FormItem label="当前持仓">
            <span>{closeTarget?.quantity} 股 / 成本 ¥{closeTarget?.cost_price?.toFixed(2)}</span>
          </Form.FormItem>
          <Form.FormItem label="清仓价">
            <InputNumber value={closeFormPrice} min={0} step={0.001} placeholder="清仓价格" onChange={(v) => { setCloseFormPrice(Number(v) || 0); closePriceInput() }} />
          </Form.FormItem>
          {closePreviewValid && (
            <div className="muted">
              清仓盈亏：<span style={{ color: closePnlAmount >= 0 ? '#e34d59' : '#00a870' }}>{closePnlAmount >= 0 ? '+' : ''}¥{closePnlAmount.toFixed(2)}</span>
              （{closePnlPct >= 0 ? '+' : ''}{closePnlPct.toFixed(2)}%）
            </div>
          )}
        </Form>
      </Dialog>

      {/* 批次明细弹窗 */}
      <Dialog visible={showLots && !!lotsTarget} header={`加仓明细 ${lotsTarget?.code} ${lotsTarget?.name}`} onClose={() => setShowLots(false)} onConfirm={() => setShowLots(false)} confirmBtn="关闭" cancelBtn="">
        <div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr', gap: 4, fontWeight: 600, fontSize: 13 }}>
            <span>时间</span><span>价格</span><span>数量</span><span>金额</span>
          </div>
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

      {/* 实盘下单确认弹窗 */}
      <Dialog visible={!!realAction} header={`实盘${realAction ? realActionLabel(realAction.dir) : ''} ${realAction?.pos.ts_code} ${realAction?.pos.name}`} onClose={() => setRealAction(null)} onConfirm={confirmRealAction} confirmBtn={realSubmitting ? '下单中…' : '确认下单'} cancelBtn="取消">
        <Form onSubmit={confirmRealAction}>
          <Form.FormItem label="当前持仓">
            <span>{realAction?.pos.qty} 股 / 成本 ¥{realAction?.pos.cost_price?.toFixed(3)}</span>
          </Form.FormItem>
          <Form.FormItem label="参考价">
            <InputNumber value={realFormPrice} min={0} step={0.001} placeholder="成交参考价" onChange={(v) => setRealFormPrice(Number(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label={realAction?.dir === 'add' ? '加仓数量' : '数量'}>
            <InputNumber value={realFormQty} min={0} step={100} placeholder={realAction?.dir === 'add' ? '股数（一手=100）' : '股数'} onChange={(v) => setRealFormQty(parseInt(v) || 0)} />
          </Form.FormItem>
          <Form.FormItem label="战法">
            <Input value={realFormStrategy} placeholder="策略名（可选）" onChange={(v) => setRealFormStrategy(v)} />
          </Form.FormItem>
          {realFormQty > 0 && realFormPrice > 0 && (
            <div className="muted">预估金额：¥{(realFormQty * realFormPrice).toFixed(2)}</div>
          )}
        </Form>
      </Dialog>
    </div>
  )
}
