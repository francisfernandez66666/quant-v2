// ── 持仓管理页面 Positions.jsx ──
// 纸面持仓（增删改/加减仓/改成本/清仓/批次明细） + 实盘持仓（QMT 网关对账 + 手动下单）。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import KLineChart from '../components/KLineChart.jsx'
import DepthPanel from '../components/DepthPanel.jsx'
import './Positions.css'

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
  const [holdings, setHoldings] = useState(cache.holdings)
  const [klineOpen, setKlineOpen] = useState(new Set())
  const [availableBalance, setAvailableBalance] = useState(cache.balance)
  const [showAdd, setShowAdd] = useState(false)
  const [pnlOffset, setPnlOffset] = useState(parseFloat(localStorage.getItem('pnl_offset') || '0'))
  const [totalRealizedPnl, setTotalRealizedPnl] = useState(0)

  const [bookTab, setBookTab] = useState('paper')
  const [qmtState, setQmtState] = useState({ enabled: false, mode: 'manual', tripped: false, gateway_url: '' })
  const [realPositions, setRealPositions] = useState([])
  const [realAdvices, setRealAdvices] = useState({})
  const realEnabled = !!qmtState.enabled
  const realTripped = !!qmtState.tripped
  const [realAction, setRealAction] = useState(null)
  const [realFormPrice, setRealFormPrice] = useState(0)
  const [realFormQty, setRealFormQty] = useState(0)
  const [realFormStrategy, setRealFormStrategy] = useState('')
  const [realSubmitting, setRealSubmitting] = useState(false)
  const realTimer = useRef(null)

  const [editingIdx, setEditingIdx] = useState(-1)
  const [formCode, setFormCode] = useState('')
  const [formCost, setFormCost] = useState(0)
  const [formQty, setFormQty] = useState(0)
  const [lookupName, setLookupName] = useState('')
  const [lookupPrice, setLookupPrice] = useState(0)
  const [formTp, setFormTp] = useState(8)
  const [formSl, setFormSl] = useState(5)

  const [showLot, setShowLot] = useState(false)
  const [showCost, setShowCost] = useState(false)
  const [showLots, setShowLots] = useState(false)
  const [lotTarget, setLotTarget] = useState(null)
  const [costTarget, setCostTarget] = useState(null)
  const [lotsTarget, setLotsTarget] = useState(null)
  const [lotDir, setLotDir] = useState('add')
  const [lotFormPrice, setLotFormPrice] = useState(0)
  const [lotFormQty, setLotFormQty] = useState(0)
  const [lotCurrentPrice, setLotCurrentPrice] = useState(0)
  const [costFormPrice, setCostFormPrice] = useState(0)

  const [showClose, setShowClose] = useState(false)
  const [closeTarget, setCloseTarget] = useState(null)
  const [closeFormPrice, setCloseFormPrice] = useState(0)
  const [closePnlAmount, setClosePnlAmount] = useState(0)
  const [closePnlPct, setClosePnlPct] = useState(0)
  const [closePreviewValid, setClosePreviewValid] = useState(false)

  const [editingBalance, setEditingBalance] = useState(false)
  const [balanceInputVal, setBalanceInputVal] = useState(0)
  const [sheetHolding, setSheetHolding] = useState(null)

  const timer = useRef(null)
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

  // 判断当前价是否触及止盈或止损线
  function curReachedStop(h) {
    if (!h.cur_price || !h.stop_loss) return false
    return h.cur_price <= h.stop_loss || h.cur_price >= h.take_profit
  }
  // 根据涨跌/盈亏/信号/止损状态返回行样式
  function rowClass(h) {
    const chg = h.change_pct || 0
    const pnl = h.pnl_pct || 0
    if (h.signal_active) return 'table-row signal'
    if (chg >= 5 || pnl >= 8) return 'table-row strong'
    if (curReachedStop(h)) return 'table-row danger'
    if (chg >= 3 || pnl >= 5 || chg <= -3 || pnl <= -5) return 'table-row watch'
    return 'table-row'
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
  async function onCodeInput() {
    const code = formCode.trim()
    if (code.length < 5) { setLookupName(''); return }
    try {
      const data = await api.fetchStockLookup(code)
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
    if (!code || !formCost || !formQty) { window.alert('请填写完整信息'); return }
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
    if (!t || price <= 0 || qty <= 0) { window.alert('请填写成交价与成交数量'); return }
    try {
      if (lotDir === 'sell') {
        const res = await api.sellHoldingLot(t.code, price, qty)
        if (res && res.closed) {
          setHoldings((prev) => prev.filter((x) => x.code !== t.code))
          window.alert(`已全部减仓 ${t.code} ${t.name}`)
        } else if (res && res.holding) { upsertHolding(res.holding) }
      } else {
        const res = await api.addHoldingLot(t.code, price, qty)
        if (res && res.holding) upsertHolding(res.holding)
      }
      setShowLot(false)
    } catch (e) { window.alert((lotDir === 'sell' ? '减仓失败: ' : '加仓失败: ') + (e.message || '')) }
  }
  function openSetCost(h) {
    setCostTarget(h); setCostFormPrice(Number(h.cost_price) || 0); setShowCost(true)
  }
  // 确认修改持仓成本价
  async function confirmSetCost() {
    const t = costTarget
    const price = Number(costFormPrice)
    if (!t || price <= 0) { window.alert('请输入有效的成本价'); return }
    try {
      const res = await api.setHoldingCost(t.code, price)
      if (res && res.holding) upsertHolding(res.holding)
      setShowCost(false)
    } catch (e) { window.alert('更新成本失败: ' + (e.message || '')) }
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
    if (!t || price <= 0) { window.alert('请输入有效的清仓价'); return }
    try {
      const res = await api.closeHolding(t.code, price)
      if (res && res.status === 'ok') {
        setHoldings((prev) => prev.filter((x) => x.code !== t.code))
        const amt = res.profit_amount || 0
        const pct = res.profit_pct || 0
        window.alert(`已清仓 ${t.code} ${t.name}：清仓价 ¥${price.toFixed(2)}，盈亏 ${amt >= 0 ? '+' : ''}¥${amt.toFixed(2)}（${pct >= 0 ? '+' : ''}${pct.toFixed(2)}%）`)
      }
      setShowClose(false)
    } catch (e) { window.alert('清仓失败: ' + (e.message || '')) }
  }

  // ── 实盘 ──
  // 切换纸面/实盘标签，进入实盘时启动轮询
  function switchBook(tab) {
    setBookTab(tab)
    if (tab === 'real') {
      loadReal()
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
    } catch (_) {}
  }
  function realRowClass(p) {
    if (adviceFor(p.ts_code)) return 'table-row signal'
    if (p.cost_price > 0 && curPrice(p) && (p.cost_price - curPrice(p)) / p.cost_price <= -0.05) return 'table-row danger'
    return 'table-row'
  }
  function curPrice(p) { return (p.cur_price && p.cur_price > 0) ? p.cur_price : 0 }
  function realPnlPct(p) {
    if (!p.cost_price || p.cost_price <= 0 || !curPrice(p)) return 0
    return (curPrice(p) - p.cost_price) / p.cost_price * 100
  }
  function adviceFor(tsCode) { return realAdvices[tsCode] || null }
  function realActionLabel(dir) { return ({ add: '加仓', reduce: '减仓', tp: '止盈', close: '清仓' })[dir] || dir }
  function openRealAction(p, dir) {
    if (realTripped) { window.alert('网关已熔断，暂停实盘下单'); return }
    setRealAction({ pos: p, dir })
    setRealFormPrice(curPrice(p) || p.cost_price || 0)
    setRealFormQty(dir === 'add' ? 100 : Math.min(100, p.qty || 0))
    setRealFormStrategy('')
  }
  // 提交实盘买入/卖出/止盈/清仓委托
  async function confirmRealAction() {
    const a = realAction
    if (!a) return
    const qty = a.dir === 'close' ? (a.pos.qty || 0) : Math.round(Number(realFormQty) || 0)
    const price = Number(realFormPrice) || 0
    if (qty <= 0 || price <= 0) { window.alert('请输入有效的价格与数量'); return }
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
      window.alert((sell ? '卖出' : '买入') + '委托已提交 ' + a.pos.ts_code + ' ' + qty + ' 股' + (res.order_id ? '（单号 ' + res.order_id + '）' : ''))
      setRealAction(null)
      setTimeout(loadReal, 2000)
    } catch (e) { window.alert('下单失败: ' + (e.message || '')) }
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
    load(); timer.current = setInterval(load, 30000)
    unsubSSE.current = api.onSSE((msg) => {
      if (!msg || !msg.type) return
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

  return (
    <div className="positions-page">
      <div className="page-header">
        <h2>持仓管理</h2>
        <div className="header-right">
          <div className={'total-pnl ' + (totalPnl >= 0 ? 'up' : 'down')}>
            总盈亏: {totalPnl >= 0 ? '+' : ''}¥{totalPnl.toFixed(2)}
            <button className="btn-reset" onClick={resetPnl}>清零</button>
          </div>
          {!editingBalance
            ? <div className="balance" onClick={editBalanceStart}>可用资金: ¥{availableBalance.toFixed(2)} ✏️</div>
            : <div className="balance-editing">
                <input type="number" step="0.01" value={balanceInputVal} onChange={(e) => setBalanceInputVal(parseFloat(e.target.value) || 0)} onBlur={editBalanceSave} onKeyDown={(e) => { if (e.key === 'Enter') editBalanceSave(); if (e.key === 'Escape') editBalanceCancel() }} autoFocus />
              </div>}
          <button className="btn-add" onClick={openAddNew}>+ 新增持仓</button>
        </div>
      </div>

      <div className="book-tabs">
        <button className={'book-tab ' + (bookTab === 'paper' ? 'active' : '')} onClick={() => switchBook('paper')}>纸面持仓</button>
        <button className={'book-tab ' + (bookTab === 'real' ? 'active' : '')} onClick={() => switchBook('real')}>
          实盘持仓{realTripped && <span className="tripped-dot" title="网关断线熔断中">!</span>}
        </button>
      </div>

      {bookTab === 'paper' && (
        <>
          {holdings.length > 0 ? (
            <div className="positions-table">
              <div className="table-header">
                <span className="col-code">代码</span>
                <span className="col-name">名称</span>
                <span className="col-num">数量</span>
                <span className="col-price">成本价</span>
                <span className="col-price">现价</span>
                <span className="col-chg">当日涨跌</span>
                <span className="col-chg">持仓盈亏</span>
                <span className="col-sig" title="有策略信号">⚡</span>
                <span className="col-score" title="N形≥60可操作">N</span>
                <span className="col-score" title="龙头≥60买入">龙</span>
                <span className="col-score" title="动量≥50关注">量</span>
                <span className="col-sl">止盈/止损</span>
                <span className="col-sl" title="移动止盈基准（阶段最高价）">移动止盈</span>
                <span className="col-kline">分时</span>
                <span className="col-actions">操作</span>
              </div>
              {holdings.map((h) => (
                <div key={h.code} className="pos-row-group">
                  <div className={rowClass(h)} onClick={() => onRowTap(h)}>
                    <span className="col-code" data-label="代码">{h.code}</span>
                    <span className="col-name" data-label="名称">{h.name}</span>
                    <span className="col-num" data-label="数量">{h.quantity}</span>
                    <span className="col-price" data-label="成本价">{h.cost_price?.toFixed(2)}</span>
                    <span className="col-price" data-label="现价">{h.cur_price?.toFixed(2)}</span>
                    <span className={'col-chg ' + ((h.change_pct || 0) >= 0 ? 'up' : 'down')} data-label="当日涨跌">
                      {(h.change_pct || 0) > 0 ? '+' : ''}{(h.change_pct || 0).toFixed(2)}%
                    </span>
                    <span className={'col-chg ' + ((h.pnl_pct || 0) >= 0 ? 'up' : 'down')} data-label="持仓盈亏">
                      {(h.pnl_pct || 0) > 0 ? '+' : ''}{(h.pnl_pct || 0).toFixed(2)}%
                    </span>
                    {h.signal_active
                      ? <span className="col-sig" data-label="信号" title="有策略信号">⚡</span>
                      : <span className="col-sig dim" data-label="信号">—</span>}
                    <span className={'col-score ' + ((h.n_score || 0) >= 60 ? 'strong' : ((h.n_score || 0) > 0 ? 'watch' : ''))} data-label="N形">
                      {(h.n_score || 0) > 0 ? h.n_score.toFixed(0) : '—'}
                    </span>
                    <span className={'col-score ' + ((h.dragon_score || 0) >= 60 ? 'strong' : ((h.dragon_score || 0) >= 50 ? 'watch' : ''))} data-label="龙头">
                      {(h.dragon_score || 0) > 0 ? h.dragon_score.toFixed(0) : '—'}
                    </span>
                    <span className={'col-score ' + ((h.m_score || 0) >= 50 ? 'watch' : '')} data-label="动量">
                      {(h.m_score || 0) > 0 ? h.m_score.toFixed(0) : '—'}
                    </span>
                    <span className="col-sl" data-label="止盈/止损">
                      <span className="sl-tp">+{(h.take_profit_pct || 8).toFixed(1)}%</span>
                      <span className="sl-div">/</span>
                      <span className="sl-sel">-{(h.stop_loss_pct || 5).toFixed(1)}%</span>
                    </span>
                    <span className="col-sl" data-label="移动止盈">
                      {h.highest_price > 0
                        ? <span className={'sl-move ' + (h.highest_price > (h.cost_price || 0) ? 'up' : '')}>¥{h.highest_price.toFixed(2)}</span>
                        : '—'}
                    </span>
                    <span className="col-kline" data-label="分时">
                      <button className="btn-kline" onClick={(e) => { e.stopPropagation(); toggleKline(h.code) }} title={klineOpen.has(h.code) ? '收起分时' : '展开分时'}>{klineOpen.has(h.code) ? '收起' : '分时'}</button>
                    </span>
                    <span className="col-actions" data-label="操作">
                      <button className="btn-lot" onClick={(e) => { e.stopPropagation(); openAddLot(h) }}>加减仓</button>
                      <button className="btn-cost" onClick={(e) => { e.stopPropagation(); openSetCost(h) }}>改成本</button>
                      <button className="btn-edit" onClick={(e) => { e.stopPropagation(); showLotsFor(h) }}>明细</button>
                      <button className="btn-edit" onClick={(e) => { e.stopPropagation(); editHolding(h) }}>编辑</button>
                      <button className="btn-sell" onClick={(e) => { e.stopPropagation(); openCloseHolding(h) }}>清仓</button>
                    </span>
                  </div>
                  {klineOpen.has(h.code) && (
                    <div className="pos-kline-row">
                      <div className="kline-flex">
                        <div className="kline-main"><KLineChart code={h.code} name={h.name} /></div>
                        <div className="depth-side"><DepthPanel code={h.code} name={h.name} /></div>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <div className="empty">
              <p>暂无持仓</p>
              <p className="hint">点击右上角「新增持仓」手动添加，或通过信号页确认买入自动更新</p>
            </div>
          )}

          {sheetHolding && (
            <div className="sheet-overlay" onClick={() => setSheetHolding(null)}>
              <div className="action-sheet" onClick={(e) => e.stopPropagation()}>
                <div className="sheet-title">{sheetHolding.code} {sheetHolding.name}</div>
                <button className="sheet-btn" onClick={() => { toggleKline(sheetHolding.code); setSheetHolding(null) }}>{klineOpen.has(sheetHolding.code) ? '收起分时' : '展开分时'}</button>
                <button className="sheet-btn" onClick={() => { const h = sheetHolding; setSheetHolding(null); openAddLot(h) }}>加减仓</button>
                <button className="sheet-btn" onClick={() => { const h = sheetHolding; setSheetHolding(null); openSetCost(h) }}>改成本</button>
                <button className="sheet-btn" onClick={() => { const h = sheetHolding; setSheetHolding(null); showLotsFor(h) }}>加仓明细</button>
                <button className="sheet-btn" onClick={() => { const h = sheetHolding; setSheetHolding(null); editHolding(h) }}>编辑持仓</button>
                <button className="sheet-btn sheet-danger" onClick={() => { const h = sheetHolding; setSheetHolding(null); openCloseHolding(h) }}>清仓</button>
                <button className="sheet-btn sheet-cancel" onClick={() => setSheetHolding(null)}>取消</button>
              </div>
            </div>
          )}

          {/* 新增/编辑弹窗 */}
          {showAdd && (
            <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) closeAdd() }}>
              <div className="modal">
                <div className="modal-title">{editingIdx >= 0 ? '编辑持仓' : '新增持仓'}</div>
                <div className="form-row">
                  <label>代码</label>
                  <input value={formCode} placeholder="输入代码" onChange={(e) => setFormCode(e.target.value)} onInput={onCodeInput} disabled={editingIdx >= 0} />
                  {lookupName && <span className="lookup-result">{lookupName} ¥{lookupPrice?.toFixed(2)}</span>}
                </div>
                <div className="form-row">
                  <label>成本价</label>
                  <input type="number" step="0.001" value={formCost} onChange={(e) => setFormCost(parseFloat(e.target.value) || 0)} placeholder="成本价" />
                </div>
                <div className="form-row">
                  <label>持股数</label>
                  <input type="number" step="1" value={formQty} onChange={(e) => setFormQty(parseInt(e.target.value) || 0)} placeholder="持股数量" />
                </div>
                <div className="form-row">
                  <label>止盈%</label>
                  <input type="number" step="0.1" value={formTp} onChange={(e) => setFormTp(parseFloat(e.target.value) || 0)} placeholder="默认+8%" />
                </div>
                <div className="form-row">
                  <label>止损%</label>
                  <input type="number" step="0.1" value={formSl} onChange={(e) => setFormSl(parseFloat(e.target.value) || 0)} placeholder="默认-5%" />
                </div>
                <div className="modal-actions">
                  <button className="btn-cancel" onClick={closeAdd}>取消</button>
                  <button className="btn-confirm" onClick={confirmAdd}>确定</button>
                </div>
              </div>
            </div>
          )}

          {/* 加减仓弹窗 */}
          {showLot && (
            <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) setShowLot(false) }}>
              <div className="modal">
                <div className="modal-title">
                  加减仓 {lotTarget?.code} {lotTarget?.name}
                  <span className="lot-dir">
                    <button className={'dir-btn ' + (lotDir === 'add' ? 'active-add' : '')} onClick={() => setLotDir('add')}>加仓</button>
                    <button className={'dir-btn ' + (lotDir === 'sell' ? 'active-sell' : '')} onClick={() => setLotDir('sell')}>减仓</button>
                  </span>
                </div>
                <div className="form-row">
                  <label>当前数量</label>
                  <span className="static-val">{lotTarget?.quantity}</span>
                  <label style={{ width: 'auto' }}>当前成本</label>
                  <span className="static-val">¥{lotTarget?.cost_price?.toFixed(2)}</span>
                </div>
                <div className="form-row">
                  <label>现价</label>
                  <span className="static-val">{lotCurrentPrice > 0 ? '¥' + lotCurrentPrice.toFixed(2) : '—'}</span>
                  {lotCurrentPrice > 0 && <button className="btn-lot" style={{ marginLeft: 8 }} onClick={() => setLotFormPrice(lotCurrentPrice)}>按现价</button>}
                </div>
                <div className="form-row">
                  <label>{lotDir === 'add' ? '加仓价' : '减仓价'}</label>
                  <input type="number" step="0.001" value={lotFormPrice} onChange={(e) => setLotFormPrice(parseFloat(e.target.value) || 0)} placeholder="成交价格（默认现价）" />
                </div>
                <div className="form-row">
                  <label>{lotDir === 'add' ? '加仓数量' : '减仓数量'}</label>
                  <input type="number" step="1" value={lotFormQty} onChange={(e) => setLotFormQty(parseInt(e.target.value) || 0)} placeholder="成交数量" />
                </div>
                {lotPreviewQty > 0 && (
                  <div className="preview">
                    {lotDir === 'add'
                      ? <>加仓后：共 {lotPreviewQty} 股 / 平均成本 ¥{lotPreviewCost.toFixed(3)}</>
                      : <span className={lotOverSell ? 'over-sell' : ''}>
                          {lotOverSell ? '减仓数量超过持仓！' : `减仓后：剩余 ${lotPreviewQty} 股 / 平均成本 ¥${lotPreviewCost.toFixed(3)}`}
                        </span>}
                  </div>
                )}
                <div className="modal-actions">
                  <button className="btn-cancel" onClick={() => setShowLot(false)}>取消</button>
                  <button className={'btn-confirm ' + (lotDir === 'sell' ? 'btn-confirm-sell' : '')} onClick={confirmLot} disabled={lotOverSell || fareCalcDisabled}>
                    {lotDir === 'add' ? '确定加仓' : '确定减仓'}
                  </button>
                </div>
              </div>
            </div>
          )}

          {/* 改成本弹窗 */}
          {showCost && (
            <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) setShowCost(false) }}>
              <div className="modal">
                <div className="modal-title">更新成本 {costTarget?.code} {costTarget?.name}</div>
                <div className="form-row">
                  <label>目标成本</label>
                  <input type="number" step="0.001" value={costFormPrice} onChange={(e) => setCostFormPrice(parseFloat(e.target.value) || 0)} placeholder="新的成本价" />
                </div>
                <div className="modal-actions">
                  <button className="btn-cancel" onClick={() => setShowCost(false)}>取消</button>
                  <button className="btn-confirm" onClick={confirmSetCost}>确定</button>
                </div>
              </div>
            </div>
          )}

          {/* 清仓弹窗 */}
          {showClose && (
            <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) setShowClose(false) }}>
              <div className="modal">
                <div className="modal-title">清仓 {closeTarget?.code} {closeTarget?.name}</div>
                <div className="form-row">
                  <label>当前持仓</label>
                  <span className="static-val">{closeTarget?.quantity} 股 / 成本 ¥{closeTarget?.cost_price?.toFixed(2)}</span>
                </div>
                <div className="form-row">
                  <label>清仓价</label>
                  <input type="number" step="0.001" value={closeFormPrice} onChange={(e) => setCloseFormPrice(parseFloat(e.target.value) || 0)} onInput={closePriceInput} placeholder="清仓价格" />
                </div>
                {closePreviewValid && (
                  <div className="preview">
                    清仓盈亏：<span className={closePnlAmount >= 0 ? 'pnl-up' : 'pnl-down'}>{closePnlAmount >= 0 ? '+' : ''}¥{closePnlAmount.toFixed(2)}</span>
                    （{closePnlPct >= 0 ? '+' : ''}{closePnlPct.toFixed(2)}%）
                  </div>
                )}
                <div className="modal-actions">
                  <button className="btn-cancel" onClick={() => setShowClose(false)}>取消</button>
                  <button className="btn-confirm" onClick={confirmCloseHolding}>确认清仓</button>
                </div>
              </div>
            </div>
          )}

          {/* 批次明细弹窗 */}
          {showLots && lotsTarget && (
            <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) setShowLots(false) }}>
              <div className="modal wide">
                <div className="modal-title">加仓明细 {lotsTarget.code} {lotsTarget.name}</div>
                <div className="lots-table">
                  <div className="lots-header"><span>时间</span><span>价格</span><span>数量</span><span>金额</span></div>
                  {(lotsTarget.lots || []).map((lot, i) => (
                    <div className="lots-row" key={i}>
                      <span>{(lot.at || '').replace('T', ' ').slice(0, 19)}</span>
                      <span>¥{lot.price?.toFixed(3)}</span>
                      <span>{lot.quantity}</span>
                      <span>¥{(lot.price * lot.quantity).toFixed(2)}</span>
                    </div>
                  ))}
                  <div className="lots-footer">合计：{lotsTarget.quantity} 股 / 平均成本 ¥{lotsTarget.cost_price?.toFixed(3)}</div>
                </div>
                <div className="modal-actions">
                  <button className="btn-confirm" onClick={() => setShowLots(false)}>关闭</button>
                </div>
              </div>
            </div>
          )}

          <div className="legend">
            <span><span className="lg-dot up"></span>当日涨跌红涨绿跌</span>
            <span className="lg-sep">|</span>
            <span><span className="lg-dot warn"></span>持仓盈亏红赚绿亏</span>
            <span className="lg-sep">|</span>
            <span>⚡ 有策略信号</span>
            <span className="lg-sep">|</span>
            <span className="lg-item">止盈+8% / 止损-5%</span>
            <span className="lg-sep">|</span>
            <span>N≥60可买 龙≥60买 量≥50关注</span>
          </div>
        </>
      )}

      {bookTab === 'real' && (
        <div className="real-book">
          <div className="real-book-bar">
            <span className={'real-bar-item ' + (qmtState.enabled ? 'ok' : 'off')}>{qmtState.enabled ? '已启用' : '未启用'}</span>
            <span className="real-bar-item">模式: {qmtState.mode || 'manual'}</span>
            <span className={'real-bar-item ' + (qmtState.tripped ? 'bad' : 'ok')}>熔断: {qmtState.tripped ? '已熔断' : '正常'}</span>
            {qmtState.gateway_url && <span className="real-bar-item dim">网关 {qmtState.gateway_url}</span>}
            <button className="btn-refresh" onClick={loadReal} title="刷新实盘数据">刷新</button>
          </div>

          {!realPositions.length ? (
            <div className="real-empty">
              <p>{realEnabled ? '暂无实盘持仓' : '实盘未启用（config.toml 中 qmt.enabled=true 并配置网关）'}</p>
              {realEnabled && <p className="hint">等待 QMT 网关回报 /api/qmt/report 推送持仓对账</p>}
            </div>
          ) : (
            <div className="positions-table">
              <div className="table-header">
                <span className="col-code">代码</span>
                <span className="col-name">名称</span>
                <span className="col-num">数量</span>
                <span className="col-price">成本价</span>
                <span className="col-price">现价</span>
                <span className="col-chg">持仓盈亏</span>
                <span className="col-chg">最高价</span>
                <span className="col-sig">建议</span>
                <span className="col-actions">操作</span>
              </div>
              {realPositions.map((p) => (
                <div key={p.ts_code} className="pos-row-group">
                  <div className={'table-row ' + realRowClass(p)}>
                    <span className="col-code" data-label="代码">{p.ts_code}</span>
                    <span className="col-name" data-label="名称">{p.name}</span>
                    <span className="col-num" data-label="数量">{p.qty}</span>
                    <span className="col-price" data-label="成本价">{p.cost_price?.toFixed(3)}</span>
                    <span className="col-price" data-label="现价">{curPrice(p) ? '¥' + curPrice(p).toFixed(2) : '—'}</span>
                    <span className={'col-chg ' + (realPnlPct(p) >= 0 ? 'up' : 'down')} data-label="持仓盈亏">
                      {p.cost_price > 0 && curPrice(p) ? (realPnlPct(p) > 0 ? '+' : '') + realPnlPct(p).toFixed(2) + '%' : '—'}
                    </span>
                    <span className="col-chg" data-label="最高价">¥{p.highest_price?.toFixed(2) || '—'}</span>
                    <span className="col-sig" data-label="建议">
                      {adviceFor(p.ts_code)
                        ? <span className={'advice-badge ' + adviceFor(p.ts_code).action}>{adviceFor(p.ts_code).label}</span>
                        : <span className="dim">—</span>}
                    </span>
                    <span className="col-actions" data-label="操作">
                      <button className="btn-lot" onClick={() => openRealAction(p, 'add')} disabled={realTripped}>加仓</button>
                      <button className="btn-lot" onClick={() => openRealAction(p, 'reduce')} disabled={realTripped}>减仓</button>
                      <button className="btn-cost" onClick={() => openRealAction(p, 'tp')} disabled={realTripped}>止盈</button>
                      <button className="btn-sell" onClick={() => openRealAction(p, 'close')} disabled={realTripped}>清仓</button>
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}

          {realAction && (
            <div className="modal-overlay" onClick={(e) => { if (e.target === e.currentTarget) setRealAction(null) }}>
              <div className="modal">
                <div className="modal-title">实盘{realActionLabel(realAction.dir)} {realAction.pos.ts_code} {realAction.pos.name}</div>
                <div className="form-row">
                  <label>当前持仓</label>
                  <span className="static-val">{realAction.pos.qty} 股 / 成本 ¥{realAction.pos.cost_price?.toFixed(3)}</span>
                </div>
                <div className="form-row">
                  <label>参考价</label>
                  <input type="number" step="0.001" value={realFormPrice} onChange={(e) => setRealFormPrice(parseFloat(e.target.value) || 0)} placeholder="成交参考价" />
                </div>
                <div className="form-row">
                  <label>{realAction.dir === 'add' ? '加仓数量' : '数量'}</label>
                  <input type="number" step="100" value={realFormQty} onChange={(e) => setRealFormQty(parseInt(e.target.value) || 0)} placeholder={realAction.dir === 'add' ? '股数（一手=100）' : '股数'} />
                </div>
                <div className="form-row">
                  <label>战法</label>
                  <input value={realFormStrategy} onChange={(e) => setRealFormStrategy(e.target.value)} placeholder="策略名（可选）" />
                </div>
                {realFormQty > 0 && realFormPrice > 0 && (
                  <div className="preview">预估金额：¥{(realFormQty * realFormPrice).toFixed(2)}</div>
                )}
                <div className="modal-actions">
                  <button className="btn-cancel" onClick={() => setRealAction(null)}>取消</button>
                  <button className="btn-confirm" onClick={confirmRealAction} disabled={realSubmitting}>{realSubmitting ? '下单中…' : '确认下单'}</button>
                </div>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
