// ── 模拟盘页面 Paper.jsx ──
// Paper trading: account state, strategy pools, positions/fills/orders, equity curve,
// manual buy/trim/close, deposit, pool/cap config, pool reset, full liquidation.
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { DialogPlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import './Paper.css'

// ── 本地分时图组件（替代 Vue KLineChart.vue）──
/**
 * 简版分时图组件
 * @param {{code:string, name:string}} props
 */
function KLineChart({ code, name }) {
  const [points, setPoints] = useState([])
  const [loading, setLoading] = useState(true)
  // 加载并绘制最近 241 个 1 分钟收盘价连线
  useEffect(() => {
    let alive = true
    setLoading(true)
    api.fetchMinute(code, 1, 241).then((d) => {
      if (alive) setPoints((d && d.points) || [])
    }).catch(() => { if (alive) setPoints([]) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [code])
  if (loading) return <div className="kline-loading">加载分时…</div>
  if (!points.length) return <div className="kline-loading">暂无分时数据</div>
  const W = 600, H = 180, pad = 8
  const vals = points.map((p) => p.close)
  const min = Math.min(...vals), max = Math.max(...vals)
  const range = max - min || 1
  const line = points.map((p, i) => {
    const x = pad + (i / (points.length - 1)) * (W - 2 * pad)
    const y = H - pad - ((p.close - min) / range) * (H - 2 * pad)
    return x.toFixed(1) + ',' + y.toFixed(1)
  }).join(' ')
  return (
    <svg className="equity-chart" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ width: '100%', height: 180 }}>
      <polyline points={line} fill="none" stroke="#FF4D4F" strokeWidth="1.5" />
    </svg>
  )
}

// ── 本地盘口面板组件（替代 Vue DepthPanel.vue）──
/**
 * 简版盘口面板组件
 * @param {{code:string, name:string}} props
 */
function DepthPanel({ code, name }) {
  const [depth, setDepth] = useState(null)
  const [loading, setLoading] = useState(true)
  // 加载买卖五档盘口
  useEffect(() => {
    let alive = true
    setLoading(true)
    api.fetchDepth(code).then((d) => { if (alive) setDepth(d) }).catch(() => { if (alive) setDepth(null) })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [code])
  if (loading) return <div className="kline-loading">加载盘口…</div>
  if (!depth) return <div className="kline-loading">暂无盘口数据</div>
  const bids = depth.bids || []
  const asks = depth.asks || []
  return (
    <div className="depth-panel">
      <div className="depth-title">{depth.name || code} 盘口</div>
      <div className="depth-cols">
        <div className="depth-side-col">
          <div className="depth-head">卖</div>
          {asks.slice(0, 5).map((a, i) => (
            <div className="depth-row" key={'a' + i}><span className="dn">{a.price?.toFixed(2)}</span><span className="dq">{a.volume}</span></div>
          ))}
        </div>
        <div className="depth-side-col">
          <div className="depth-head">买</div>
          {bids.slice(0, 5).map((b, i) => (
            <div className="depth-row" key={'b' + i}><span className="up">{b.price?.toFixed(2)}</span><span className="dq">{b.volume}</span></div>
          ))}
        </div>
      </div>
    </div>
  )
}

// 封装 tdesign 确认对话框为 Promise
function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    const d = DialogPlugin.confirm({
      header,
      body,
      theme: 'warning',
      onConfirm: () => { d.hide(); resolve(true) },
      onClose: () => { d.hide(); resolve(false) },
    })
  })
}

// 格式化为两位小数的中文数字
const fmt = (v) => (v ?? 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
// 格式化为 MM-DD HH:mm:ss
function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d)) return String(t).slice(5, 16)
  const p2 = (n) => String(n).padStart(2, '0')
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`
}
// 根据盈亏返回 CSS 类名
function pnlCls(v) { return v >= 0 ? 'up' : 'down' }
// 计算买入成交价相对信号价的滑点
function tradeSlippage(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return '—'
  const pct = (t.price - t.signal_price) / t.signal_price * 100
  return (pct >= 0 ? '+' : '') + pct.toFixed(2) + '%'
}
function tradeSlippageCls(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return ''
  return t.price >= t.signal_price ? 'down' : 'up'
}
// 将资金池 key 翻译为中文标签
function poolLabel(k) {
  if (!k) return '其他/手动'
  const labels = { dragon: '龙头', double_bump: '双响炮', n_shape: 'N形', dragon_return: '龙回头', momentum: '动量' }
  if (labels[k]) return labels[k]
  if (/^fac_/.test(k)) return '因子·' + k
  if (/^pat_/.test(k)) return '形态·' + k
  return k
}
// 规范化资金池 key，空 key 归为一类
function normPoolKey(k) { return k || '__other__' }
// 订单状态中文
function orderStatusText(s) { return { filled: '全部成交', partial: '部分成交', rejected: '已拒绝' }[s] || s }
// 订单状态样式类
function orderStatusCls(s) { return { filled: 'buy', partial: 'hold', rejected: 'sell' }[s] || 'hold' }
// 截断原因文本
function shortReason(r) { if (!r) return '—'; return r.length > 18 ? r.slice(0, 18) + '…' : r }

/**
 * 模拟盘页面组件
 * 展示账户状态、分仓资金池、持仓/成交/委托、净值曲线与资金配置。
 * @returns {JSX.Element}
 */
export default function Paper() {
  const [enabled, setEnabled] = useState(false)
  const [isAdmin, setIsAdmin] = useState(false)
  const [initialCapital, setInitialCapital] = useState('')
  const [maxPos, setMaxPos] = useState('')
  const [appliedMax, setAppliedMax] = useState(0)
  const [tab, setTab] = useState('positions')
  const [stats, setStats] = useState(null)
  const [positions, setPositions] = useState([])
  const [trades, setTrades] = useState([])
  const [orders, setOrders] = useState([])
  const [equity, setEquity] = useState([])
  const [pools, setPools] = useState([])
  const [activePool, setActivePool] = useState(null)

  const [showDepositModal, setShowDepositModal] = useState(false)
  const [showResetModal, setShowResetModal] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settingsTab, setSettingsTab] = useState('alloc')
  const [depositAmount, setDepositAmount] = useState(0)
  const [resetToCapital, setResetToCapital] = useState(0)
  const [resetMaxPos, setResetMaxPos] = useState(0)
  const [cfgMaxPos, setCfgMaxPos] = useState(0)
  const [cfgAllocs, setCfgAllocs] = useState({})
  const [cfgCaps, setCfgCaps] = useState({})
  const [cfgRules, setCfgRules] = useState({})
  const [cfgRuleSel, setCfgRuleSel] = useState('')
  const [cfgWarn, setCfgWarn] = useState('')

  const [klineOpen, setKlineOpen] = useState(new Set())
  const [sheetPos, setSheetPos] = useState(null)
  const [sheetTradeRow, setSheetTradeRow] = useState(null)
  const [tradeModal, setTradeModal] = useState(false)
  const [tradeDir, setTradeDir] = useState('add')
  const [tradeTarget, setTradeTarget] = useState(null)
  const [tradeFormPrice, setTradeFormPrice] = useState(0)
  const [tradeFormQty, setTradeFormQty] = useState(1)

  const W = 900, H = 220
  const timer = useRef(null)

  const tradePreviewQty = useMemo(() => {
    const q = parseInt(tradeFormQty, 10)
    return isNaN(q) || q <= 0 ? 0 : q
  }, [tradeFormQty])
  const tradeOverSell = useMemo(() =>
    tradeDir === 'trim' && tradeTarget && tradePreviewQty * 100 >= tradeTarget.qty, [tradeDir, tradePreviewQty, tradeTarget])

  const poolCurrentRule = (key) => {
    const p = pools.find((x) => x.key === key)
    return !!(p && p.buy_rule && (p.buy_rule.max_daily_buys || p.buy_rule.cooldown_minutes ||
      p.buy_rule.min_score || p.buy_rule.budget_pct_per_day))
  }
  const poolCurrentRuleText = (key) => {
    const p = pools.find((x) => x.key === key)
    const r = p && p.buy_rule
    if (!r) return ''
    return `限${r.max_daily_buys || '∞'}次/冷却${r.cooldown_minutes || 0}分/分≥${r.min_score || 0}/预算${r.budget_pct_per_day || 0}%`
  }

  const linePoints = useMemo(() => {
    if (equity.length < 2) return ''
    const pad = 10
    const vals = equity.map((p) => p.value)
    const min = Math.min(...vals), max = Math.max(...vals)
    const range = max - min || 1
    return equity.map((p, i) => {
      const x = pad + (i / (equity.length - 1)) * (W - 2 * pad)
      const y = H - pad - ((p.value - min) / range) * (H - 2 * pad)
      return x.toFixed(1) + ',' + y.toFixed(1)
    }).join(' ')
  }, [equity])
  const gridLines = useMemo(() => [1, 2, 3].map((k) => ({ y: (H / 4) * k })), [])

  const filteredPositions = useMemo(() => {
    if (activePool === null) return positions
    return positions.filter((p) => normPoolKey(p.strategy_type) === activePool)
  }, [positions, activePool])
  const filteredTrades = useMemo(() => {
    if (activePool === null) return trades
    return trades.filter((t) => normPoolKey(t.strategy_type) === activePool)
  }, [trades, activePool])
  const filteredOrders = useMemo(() => {
    if (activePool === null) return orders
    return orders.filter((o) => normPoolKey(o.strategy_type) === activePool)
  }, [orders, activePool])
  const activeStats = useMemo(() => {
    if (activePool === null) return stats
    const p = pools.find((p) => normPoolKey(p.key) === activePool)
    return (p && p.stats) || stats
  }, [activePool, pools, stats])
  const activePoolLabel = useMemo(() => {
    const p = pools.find((p) => normPoolKey(p.key) === activePool)
    return p ? p.label : ''
  }, [activePool, pools])

  function toggleKline(key) {
    setKlineOpen((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }
  function onRowTap(p) { if (window.innerWidth <= 768) setSheetPos(p) }
  function onTradeTap(t, i) { if (window.innerWidth <= 768) setSheetTradeRow({ ...t, idx: i }) }
  function sheetKline() {
    if (!sheetPos) return
    toggleKline(sheetPos.code); setSheetPos(null)
  }
  function sheetTrade(dir) {
    if (!sheetPos) return
    const p = sheetPos; setSheetPos(null); openTrade(p, dir)
  }
  function sheetTradeKline() {
    if (!sheetTradeRow) return
    toggleKline('trade_' + sheetTradeRow.idx); setSheetTradeRow(null)
  }

  function openTrade(p, dir) {
    setTradeTarget(p); setTradeDir(dir)
    setTradeFormPrice(p.mark || 0)
    setTradeFormQty(dir === 'close' ? p.qty : 1)
    setTradeModal(true)
  }

  // 提交模拟盘加仓/减仓/清仓委托
  async function confirmTrade() {
    const p = tradeTarget
    if (!p) return
    const price = parseFloat(tradeFormPrice)
    const qty = parseInt(tradeFormQty, 10)
    if (tradeDir !== 'close' && (isNaN(qty) || qty <= 0)) { window.alert('请输入有效的数量'); return }
    try {
      if (tradeDir === 'add') {
        await api.buyPaperPosition(p.code, p.name || '', p.strategy || '', 0, price > 0 ? price : 0, qty)
        window.alert(`已加仓 ${p.code} ${qty} 手`)
      } else {
        await api.sellPaperPosition(p.code, price > 0 ? price : 0, tradeDir === 'close' ? 0 : qty)
        window.alert(`已${tradeDir === 'close' ? '清仓' : '减仓'} ${p.code}`)
      }
      setTradeModal(false)
      await load()
    } catch (e) { window.alert(e.message || '操作失败') }
  }

  // 加载模拟盘状态、持仓、成交、委托与净值曲线
  async function load() {
    let en = false
    try {
      const st = await api.fetchPaperState()
      en = !!st.enabled
      setEnabled(en)
      setIsAdmin(!!st.is_admin)
      if (st.initial_capital > 0 && !initialCapital) setInitialCapital(String(st.initial_capital))
      if (st.max_positions !== undefined && !maxPos) setMaxPos(st.max_positions > 0 ? String(st.max_positions) : '0')
      setAppliedMax((st.max_positions !== undefined && st.max_positions > 0) ? st.max_positions : 0)
      setStats(st.stats || null)
      setPools(Array.isArray(st.strategy_pools) ? st.strategy_pools : [])
    } catch (_) {}
    if (!en) return
    try { setPositions(await api.fetchPaperPositions()) } catch (_) {}
    try { setTrades(await api.fetchPaperTrades()) } catch (_) {}
    try { setOrders(await api.fetchPaperOrders()) } catch (_) {}
    try { setEquity(await api.fetchPaperEquity()) } catch (_) {}
  }

  // 确认注入资金并更新持仓上限
  async function confirmDeposit() {
    const amt = parseFloat(depositAmount)
    if (!(amt > 0)) { window.alert('请输入有效的注入金额'); return }
    const mp = parseInt(maxPos, 10)
    const mpv = mp > 0 ? mp : 0
    const capHint = mpv > 0 ? '，持仓上限 ' + mpv + ' 只' : '（持仓上限不设限，由资金决定）'
    const ok = await confirmDialog('确认注入资金 ¥' + fmt(amt) + capHint + '？将增量计入现金，保留现有持仓/净值/成交记录。', '注入资金')
    if (!ok) return
    try {
      const res = await api.resetPaper(amt, mpv)
      setInitialCapital(String(res.initial_capital || (parseFloat(initialCapital) + amt)))
      setMaxPos(String(res.max_positions > 0 ? res.max_positions : 0))
      setAppliedMax(res.max_positions > 0 ? res.max_positions : 0)
      await load()
    } catch (e) { window.alert(e.message || '注入失败') }
  }

  // 清盘当前选中的分仓资金池
  async function confirmPoolReset() {
    if (activePool === null) return
    const label = activePoolLabel
    const count = filteredPositions.length
    const ok = await confirmDialog(
      `清盘「${label}」资金池？\n将按最后估值价平仓该池 ${count} 笔持仓（回补池现金），并清空该池累计涨跌幅表现。\n其他分仓资金池与全局净值/成交日志不受影响。`,
      '单池清盘'
    )
    if (!ok) return
    try {
      await api.resetPaperPool(activePool === '__other__' ? '' : activePool)
      await load()
    } catch (e) { window.alert(e.message || '清盘失败') }
  }

  // 打开资金分配/仓位上限/买入纪律设置弹窗并回填当前配置
  function openSettingsModal() {
    setCfgMaxPos(appliedMax > 0 ? appliedMax : 0)
    const allocs = {}, caps = {}, rules = {}
    pools.forEach((p) => {
      allocs[p.key] = p.cash
      caps[p.key] = p.max_pos || 0
      rules[p.key] = {
        max_daily_buys: (p.buy_rule && p.buy_rule.max_daily_buys) || 0,
        cooldown_minutes: (p.buy_rule && p.buy_rule.cooldown_minutes) || 0,
        min_score: (p.buy_rule && p.buy_rule.min_score) || 0,
        budget_pct_per_day: (p.buy_rule && p.buy_rule.budget_pct_per_day) || 0,
      }
    })
    setCfgAllocs(allocs); setCfgCaps(caps); setCfgRules(rules)
    const firstStrategy = pools.find((p) => p.key !== '')
    setCfgRuleSel(firstStrategy ? firstStrategy.key : (pools[0] ? pools[0].key : ''))
    setSettingsTab('alloc'); setCfgWarn('')
    setSettingsOpen(true)
  }

  // 保存资金分配、仓位上限或买入纪律配置
  async function saveSettings() {
    const totalCash = pools.reduce((s, p) => s + p.cash, 0)
    if (settingsTab === 'alloc') {
      const allocs = {}; let assigned = 0
      pools.forEach((p) => {
        const n = parseFloat(cfgAllocs[p.key])
        if (n > 0) { allocs[p.key] = n; assigned += n }
      })
      if (assigned > totalCash + 0.01) { setCfgWarn(`资金超额：Σ ¥${fmt(assigned)} > 总现金 ¥${fmt(totalCash)}`); return }
      try { await api.configPaperPools(null, null, allocs); setSettingsOpen(false); await load() }
      catch (e) { window.alert(e.message || '保存失败') }
      return
    }
    if (settingsTab === 'rules') {
      const rules = {}
      pools.forEach((p) => {
        const r = cfgRules[p.key] || {}
        rules[p.key] = {
          max_daily_buys: parseInt(r.max_daily_buys, 10) || 0,
          cooldown_minutes: parseInt(r.cooldown_minutes, 10) || 0,
          min_score: parseFloat(r.min_score) || 0,
          budget_pct_per_day: parseFloat(r.budget_pct_per_day) || 0,
        }
      })
      try { await api.configPaperPools(null, null, null, rules); setSettingsOpen(false); await load() }
      catch (e) { window.alert(e.message || '保存失败') }
      return
    }
    const caps = {}; let capSum = 0
    pools.forEach((p) => {
      const c = parseInt(cfgCaps[p.key], 10)
      if (c > 0) { caps[p.key] = c; capSum += c }
    })
    const gCap = parseInt(cfgMaxPos, 10)
    if (gCap > 0 && capSum > gCap) { setCfgWarn(`Σ池上限 ${capSum} > 全局 ${gCap}`); return }
    try { await api.configPaperPools(gCap, caps, null); setSettingsOpen(false); await load() }
    catch (e) { window.alert(e.message || '保存失败') }
  }

  // 清除自定义资金分配，恢复各池均分
  async function clearAllocs() {
    const ok = await confirmDialog('清除每池自定义资金并恢复均分？仓位上限不受影响。', '恢复均分')
    if (!ok) return
    try { await api.configPaperPools(null, {}, {}); await load() }
    catch (e) { window.alert(e.message || '操作失败') }
  }

  // 清盘重置：平仓全部持仓、清除成交日志与净值曲线
  async function doResetV2() {
    const ok = await confirmDialog('确认清盘？\n将平仓全部持仓、清除成交日志与净值曲线。', '清盘重置')
    if (!ok) return
    try {
      const body = {}
      if (resetToCapital > 0) body.reset_to = resetToCapital
      if (resetMaxPos > 0) body.max_positions = resetMaxPos
      await api.paperResetV2(body)
      setShowResetModal(false); setResetToCapital(0); setResetMaxPos(0)
      await load()
    } catch (e) { window.alert(e.message || '清盘失败') }
  }

  // 挂载时加载模拟盘数据并启动 15s 轮询
  useEffect(() => {
    load()
    timer.current = setInterval(load, 15000)
    return () => { if (timer.current) clearInterval(timer.current) }
  }, [])

  return (
    <div className="paper-page">
      <div className="page-header">
        <h2>模拟盘</h2>
        <div className="header-right">
          {isAdmin && <span className="admin-badge" title="admin 账户的模拟盘支持回测与自动化交易联动">联动版</span>}
          <span className={['enabled-badge', enabled ? 'on' : 'off'].join(' ')}>
            {enabled ? (isAdmin ? '自动撮合中' : '手动记账（静态）') : '未启用（rules.paper.enabled）'}
          </span>
          {enabled && (
            <span className="cap-badge" title="当前生效的持仓上限（经确认资金固化）">
              上限：{appliedMax > 0 ? appliedMax + ' 只' : '不设限'}
            </span>
          )}
          <button className="btn-confirm" disabled={!enabled} onClick={() => setShowDepositModal(true)} title="向模拟盘增量注入资金">＋ 注入资金</button>
          <button className="btn-config" disabled={!enabled} onClick={openSettingsModal} title="资金分配 / 仓位上限 / 恢复均分">⚙ 设置</button>
          <button className="btn-reset" disabled={!enabled} onClick={() => setShowResetModal(true)} title="清盘重置：平仓全部持仓并重置净值">清盘</button>
        </div>
      </div>

      {showDepositModal && (
        <div className="modal-overlay" onClick={() => setShowDepositModal(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-title">注入资金</div>
            <div className="form-row">
              <label>金额（元）</label>
              <input type="number" min="0" step="1000" placeholder="10000" value={depositAmount} onChange={(e) => setDepositAmount(parseFloat(e.target.value) || 0)} />
            </div>
            <div className="modal-actions">
              <button className="btn-cancel" onClick={() => setShowDepositModal(false)}>取消</button>
              <button className="btn-confirm" onClick={() => { confirmDeposit(); setShowDepositModal(false) }}>确认注入</button>
            </div>
          </div>
        </div>
      )}

      {showResetModal && (
        <div className="modal-overlay" onClick={() => setShowResetModal(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-title">⚠ 清盘重置</div>
            <div className="config-hint" style={{ color: '#e6a23c' }}>将平仓全部持仓、清除成交日志与净值曲线。</div>
            <div className="form-row">
              <label>重置后初始资金</label>
              <input type="number" min="0" step="10000" placeholder="默认 100000" value={resetToCapital} onChange={(e) => setResetToCapital(parseFloat(e.target.value) || 0)} />
              <span className="static-val">元</span>
            </div>
            <div className="config-hint">不填则按当前累计投入总额重置。</div>
            <div className="form-row">
              <label>持仓上限</label>
              <input type="number" min="0" step="1" placeholder="0=不设限" value={resetMaxPos} onChange={(e) => setResetMaxPos(parseFloat(e.target.value) || 0)} />
            </div>
            <div className="modal-actions">
              <button className="btn-cancel" onClick={() => setShowResetModal(false)}>取消</button>
              <button className="btn-reset" onClick={doResetV2}>确认清盘</button>
            </div>
          </div>
        </div>
      )}

      {settingsOpen && (
        <div className="modal-overlay" onClick={() => setSettingsOpen(false)}>
          <div className="modal pool-config-modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-title">⚙ 设置</div>
            <div className="settings-tabs">
              <button className={['tab', settingsTab === 'alloc' ? 'active' : ''].join(' ')} onClick={() => setSettingsTab('alloc')}>资金分配</button>
              <button className={['tab', settingsTab === 'caps' ? 'active' : ''].join(' ')} onClick={() => setSettingsTab('caps')}>仓位上限</button>
              <button className={['tab', settingsTab === 'rules' ? 'active' : ''].join(' ')} onClick={() => setSettingsTab('rules')}>买入纪律</button>
            </div>
            {settingsTab === 'alloc' && (
              <div>
                <div className="config-hint">每池资金额（Σ ≈ 总现金守恒）。不影响仓位上限。</div>
                {pools.map((p) => (
                  <div className="pool-config-row" key={'sa-' + p.key}>
                    <span className="pool-config-label">{p.label}</span>
                    <input type="number" min="0" step="1000" className="cfg-input"
                      placeholder={'当前 ¥' + fmt(p.cash)}
                      value={cfgAllocs[p.key] ?? ''}
                      onChange={(e) => setCfgAllocs({ ...cfgAllocs, [p.key]: parseFloat(e.target.value) || 0 })} />
                  </div>
                ))}
              </div>
            )}
            {settingsTab === 'caps' && (
              <div>
                <div className="form-row">
                  <label>全局持仓上限</label>
                  <input type="number" min="0" step="1" placeholder="0=不设限" value={cfgMaxPos} onChange={(e) => setCfgMaxPos(parseInt(e.target.value, 10) || 0)} />
                  <span className="static-val">（0=不设限）</span>
                </div>
                <div className="config-hint">每池持仓上限（0=不单独设限）。Σ ≤ 全局。不影响资金分配。</div>
                {pools.map((p) => (
                  <div className="pool-config-row" key={'sc-' + p.key}>
                    <span className="pool-config-label">{p.label}</span>
                    <input type="number" min="0" step="1" className="cfg-input cfg-cap"
                      placeholder={p.max_pos > 0 ? '当前 ' + p.max_pos : '不单独设限'}
                      value={cfgCaps[p.key] ?? ''}
                      onChange={(e) => setCfgCaps({ ...cfgCaps, [p.key]: parseInt(e.target.value, 10) || 0 })} />
                  </div>
                ))}
              </div>
            )}
            {settingsTab === 'rules' && (
              <div>
                <div className="config-hint">
                  每池买入纪律：日限次数 / 冷却分钟 / 最低评分 / 日预算%。全 0 = 不设限；
                  寻优审批会自动把门槛写入对应池的「最低评分」。
                </div>
                <div className="pool-rules-row">
                  <select className="cfg-select" value={cfgRuleSel} onChange={(e) => setCfgRuleSel(e.target.value)}>
                    {pools.map((p) => <option key={'sel-' + p.key} value={p.key}>{p.label}</option>)}
                  </select>
                  {cfgRules[cfgRuleSel] && (
                    <div>
                      <div className="pool-rules-head">{poolLabel(cfgRuleSel)}{poolCurrentRule(cfgRuleSel) && <span className="rules-now">（当前生效：{poolCurrentRuleText(cfgRuleSel)}）</span>}</div>
                      <div className="pool-rules-grid">
                        <label>日限买<input type="number" min="0" step="1" placeholder="0=不限" value={cfgRules[cfgRuleSel].max_daily_buys} onChange={(e) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], max_daily_buys: parseInt(e.target.value, 10) || 0 } })} /></label>
                        <label>冷却(分)<input type="number" min="0" step="5" placeholder="0=不限" value={cfgRules[cfgRuleSel].cooldown_minutes} onChange={(e) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], cooldown_minutes: parseInt(e.target.value, 10) || 0 } })} /></label>
                        <label>最低分<input type="number" min="0" max="100" step="1" placeholder="0=不过滤" value={cfgRules[cfgRuleSel].min_score} onChange={(e) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], min_score: parseFloat(e.target.value) || 0 } })} /></label>
                        <label>日预算%<input type="number" min="0" max="100" step="5" placeholder="0=不限" value={cfgRules[cfgRuleSel].budget_pct_per_day} onChange={(e) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], budget_pct_per_day: parseFloat(e.target.value) || 0 } })} /></label>
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}
            {cfgWarn && <div className="preview">{cfgWarn}</div>}
            <div className="modal-actions">
              <button className="btn-cancel" onClick={() => setSettingsOpen(false)}>取消</button>
              <button className="btn-confirm" onClick={saveSettings}>保存</button>
            </div>
          </div>
        </div>
      )}

      {enabled && pools.length > 0 && (
        <div className="pools-bar">
          <div className="pools-title">分仓资金池</div>
          <div className={['pool-chip', activePool === null ? 'active' : ''].join(' ')} onClick={() => setActivePool(null)}>
            <span className="pool-label">全部</span>
            <span className="pool-meta">{positions.length} 仓</span>
          </div>
          {pools.map((p) => (
            <div key={p.key}
              className={['pool-chip', activePool === normPoolKey(p.key) ? 'active' : '', !p.key ? 'other' : ''].join(' ')}
              title={p.key || '其他/手动'}
              onClick={() => setActivePool(activePool === normPoolKey(p.key) ? null : normPoolKey(p.key))}>
              <span className="pool-label">{p.label}</span>
              <span className={['pool-return', pnlCls(p.return_pct)].join(' ')}>
                {(p.return_pct >= 0 ? '+' : '') + p.return_pct.toFixed(2)}%
              </span>
              <span className="pool-cash">¥{fmt(p.cash)}</span>
              <span className="pool-meta">{p.ratio_pct.toFixed(1)}% · {p.positions} 仓</span>
            </div>
          ))}
          {activePool !== null && (
            <button className="btn-pool-reset" disabled={!enabled} onClick={confirmPoolReset}>清盘本池</button>
          )}
        </div>
      )}

      {enabled && activePool !== null && (
        <div className="stats-scope"><span className="stats-scope-tag">统计范围：{activePoolLabel}</span></div>
      )}
      {activeStats && (
        <div className="stats-grid">
          <div className="stat-card"><div className="stat-label">总资产</div><div className="stat-value">¥{fmt(activeStats.total_value)}</div></div>
          <div className="stat-card">
            <div className="stat-label">总收益</div>
            <div className={['stat-value', activeStats.total_return_pct >= 0 ? 'up' : 'down'].join(' ')}>
              {(activeStats.total_return_pct >= 0 ? '+' : '') + activeStats.total_return_pct.toFixed(2)}%
              <em className="sub">基于累计投入 ¥{fmt(activeStats.initial_capital)}</em>
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-label">当日收益</div>
            <div className={['stat-value', activeStats.today_return_pct >= 0 ? 'up' : 'down'].join(' ')}>
              {(activeStats.today_return_pct >= 0 ? '+' : '') + activeStats.today_return_pct.toFixed(2)}%
            </div>
          </div>
          <div className="stat-card"><div className="stat-label">现金</div><div className="stat-value">¥{fmt(activeStats.cash)}</div></div>
          <div className="stat-card">
            <div className="stat-label">持仓市值 / 已实现盈亏</div>
            <div className="stat-value">
              ¥{fmt(activeStats.market_value)}
              <em className={['sub', activeStats.realized_pnl >= 0 ? 'up' : 'down'].join(' ')}>
                {(activeStats.realized_pnl >= 0 ? '+' : '')}¥{fmt(activeStats.realized_pnl)}
              </em>
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-label">已平仓胜率</div>
            <div className="stat-value">{activeStats.win_rate_pct.toFixed(0)}% <em className="sub">/ {activeStats.open_positions}仓</em></div>
          </div>
        </div>
      )}

      {activeStats && isAdmin && (
        <div className="stats-grid quality">
          <div className="stat-card"><div className="stat-label">已撮合买入信号</div><div className="stat-value">{activeStats.filled_buys}</div></div>
          <div className="stat-card">
            <div className="stat-label">平均成交延迟</div>
            <div className="stat-value">{activeStats.avg_latency_sec}s <em className="sub">最大 {activeStats.max_latency_sec}s</em></div>
          </div>
          <div className="stat-card">
            <div className="stat-label">平均滑点（成交 vs 信号价）</div>
            <div className={['stat-value', activeStats.avg_slippage_pct >= 0 ? 'down' : 'up'].join(' ')}>
              {(activeStats.avg_slippage_pct >= 0 ? '+' : '') + activeStats.avg_slippage_pct.toFixed(2)}%
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-label">滑点累计成本</div>
            <div className={['stat-value', activeStats.slippage_cost >= 0 ? 'down' : 'up'].join(' ')}>
              {(activeStats.slippage_cost >= 0 ? '+' : '')}¥{fmt(activeStats.slippage_cost)}
              <em className="sub">占初始 {activeStats.signal_amount_pct.toFixed(2)}%</em>
            </div>
          </div>
        </div>
      )}

      {isAdmin && (
        <div className="panel">
          <div className="panel-title">净值曲线 <em className="sub">（{stats?.equity_curve_points || 0} 个交易日）</em></div>
          {equity.length > 1 ? (
            <svg className="equity-chart" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
              <polyline points={linePoints} fill="none" stroke="#FF4D4F" strokeWidth="2" />
              {gridLines.map((lvl) => <line key={lvl.y} x1="0" y1={lvl.y} x2={W} y2={lvl.y} className="grid-line" />)}
            </svg>
          ) : <div className="empty-hint">净值数据不足（自动撮合开启并产生成交后显示）</div>}
        </div>
      )}

      <div className="tabs">
        <button className={['tab', tab === 'positions' ? 'active' : ''].join(' ')} onClick={() => setTab('positions')}>
          当前持仓 <em className="sub">{filteredPositions.length} 只</em>
        </button>
        <button className={['tab', tab === 'trades' ? 'active' : ''].join(' ')} onClick={() => setTab('trades')}>
          成交日志 <em className="sub">{filteredTrades.length} 笔 · 近3月</em>
        </button>
        <button className={['tab', tab === 'orders' ? 'active' : ''].join(' ')} onClick={() => setTab('orders')}>
          订单 <em className="sub">{filteredOrders.length} 笔</em>
        </button>
      </div>

      {tab === 'positions' && (
        <div className="panel">
          <div className="panel-title">当前持仓 <em className="sub">{filteredPositions.length} 只</em></div>
          {filteredPositions.length ? (
            <div className="positions-table">
              <div className="table-header">
                <span className="col-code">代码</span><span className="col-name">名称</span>
                <span className="col-time">买入时间</span><span className="col-num">数量</span>
                <span className="col-price">成本价</span><span className="col-price">现价</span>
                <span className="col-chg">浮盈</span><span className="col-chg">浮盈%</span>
                <span className="col-chg">滑点</span><span className="col-num">延迟</span>
                <span className="col-pool">池</span><span className="col-kline">分时</span>
                <span className="col-actions">操作</span>
              </div>
              {filteredPositions.map((p) => (
                <div className="pos-row-group" key={p.code}>
                  <div className="table-row" onClick={() => onRowTap(p)}>
                    <span className="col-code" data-label="代码">{p.code}</span>
                    <span className="col-name" data-label="名称">{p.name}</span>
                    <span className="col-time" data-label="买入时间" title={'信号发出 ' + fmtTime(p.signal_at) + ' · 撮合成交 ' + fmtTime(p.filled_at)}>
                      {fmtTime(p.filled_at || p.signal_at)}
                    </span>
                    <span className="col-num" data-label="数量">{p.qty}</span>
                    <span className="col-price" data-label="成本价">{p.cost_price.toFixed(2)}</span>
                    <span className="col-price" data-label="现价">{(p.mark || 0).toFixed(2)}</span>
                    <span className={['col-chg', pnlCls(p.pnl)].join(' ')} data-label="浮盈">{fmt(p.pnl)}</span>
                    <span className={['col-chg', pnlCls(p.pnl)].join(' ')} data-label="浮盈%">{fmt(p.pnl_pct)}%</span>
                    <span className={['col-chg', pnlCls(p.slippage_pct)].join(' ')} data-label="滑点">{fmt(p.slippage_pct)}%</span>
                    <span className="col-num" data-label="延迟">{p.latency_sec}s</span>
                    <span className="col-pool" data-label="池"><span className="tag">{poolLabel(p.strategy_type)}</span></span>
                    <span className="col-kline" data-label="分时">
                      <button className="btn-kline" onClick={(e) => { e.stopPropagation(); toggleKline(p.code) }} title={klineOpen.has(p.code) ? '收起分时' : '展开分时'}>
                        {klineOpen.has(p.code) ? '收起' : '分时'}
                      </button>
                    </span>
                    <span className="col-actions" data-label="操作">
                      <button className="btn-lot" onClick={(e) => { e.stopPropagation(); openTrade(p, 'add') }}>加仓</button>
                      <button className="btn-cost" onClick={(e) => { e.stopPropagation(); openTrade(p, 'trim') }}>减仓</button>
                      <button className="btn-sell" onClick={(e) => { e.stopPropagation(); openTrade(p, 'close') }}>清仓</button>
                    </span>
                  </div>
                  {klineOpen.has(p.code) && (
                    <div className="pos-kline-row">
                      <div className="kline-flex">
                        <div className="kline-main"><KLineChart code={p.code} name={p.name} /></div>
                        <div className="depth-side"><DepthPanel code={p.code} name={p.name} /></div>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <div className="empty-hint">
              {isAdmin ? '暂无持仓（出现可开仓信号时按实时价自动买入）' : '暂无持仓（在信号页点「模拟买入」，或上方加仓/减仓管理已有持仓）'}
            </div>
          )}
        </div>
      )}

      {tab === 'trades' && (
        <div className="panel">
          <div className="panel-title">成交日志 <em className="sub">{filteredTrades.length} 笔 · 近3个月</em></div>
          {filteredTrades.length ? (
            <div className="positions-table">
              <div className="table-header">
                <span className="col-time">时间</span><span className="col-side">方向</span>
                <span className="col-code">代码</span><span className="col-name">名称</span>
                <span className="col-pool">战法</span><span className="col-num">数量</span>
                <span className="col-price">价格</span><span className="col-price">金额</span>
                <span className="col-chg">滑点</span><span className="col-num">延迟</span>
                <span className="col-kline">分时</span>
              </div>
              {filteredTrades.map((t, i) => (
                <div className="pos-row-group" key={i}>
                  <div className="table-row" onClick={() => onTradeTap(t, i)}>
                    <span className="col-time" data-label="时间">{fmtTime(t.time)}</span>
                    <span className="col-side" data-label="方向"><span className={['tag', t.side === 'buy' ? 'buy' : 'sell'].join(' ')}>{t.side === 'buy' ? '买入' : '卖出'}</span></span>
                    <span className="col-code" data-label="代码">{t.code}</span>
                    <span className="col-name" data-label="名称">{t.name}</span>
                    <span className="col-pool" data-label="战法"><span className="tag">{t.strategy}</span></span>
                    <span className="col-num" data-label="数量">{t.qty}</span>
                    <span className="col-price" data-label="价格">{t.price.toFixed(2)}</span>
                    <span className="col-price" data-label="金额">{fmt(t.amount)}</span>
                    <span className={['col-chg', tradeSlippageCls(t)].join(' ')} data-label="滑点">{tradeSlippage(t)}</span>
                    <span className="col-num" data-label="延迟">{t.side === 'buy' ? (t.latency_sec || 0) + 's' : '—'}</span>
                    <span className="col-kline" data-label="分时">
                      <button className="btn-kline" onClick={(e) => { e.stopPropagation(); toggleKline('trade_' + i) }}>{klineOpen.has('trade_' + i) ? '收起' : '分时'}</button>
                    </span>
                  </div>
                  {klineOpen.has('trade_' + i) && (
                    <div className="pos-kline-row">
                      <div className="kline-flex">
                        <div className="kline-main"><KLineChart code={t.code} name={t.name} /></div>
                        <div className="depth-side"><DepthPanel code={t.code} name={t.name} /></div>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
          ) : <div className="empty-hint">暂无成交记录</div>}
        </div>
      )}

      {tab === 'orders' && (
        <div className="panel">
          <div className="panel-title">订单记录 <em className="sub">{filteredOrders.length} 笔 · 含被拒留痕</em></div>
          {filteredOrders.length ? (
            <div className="positions-table">
              <div className="table-header">
                <span className="col-time">时间</span><span className="col-side">方向</span>
                <span className="col-code">代码</span><span className="col-name">名称</span>
                <span className="col-pool">来源</span><span className="col-num">状态</span>
                <span className="col-num">数量</span><span className="col-price">成交价</span>
                <span className="col-price">信号价</span><span className="col-name">说明</span>
              </div>
              {filteredOrders.map((o, i) => (
                <div className="table-row" key={o.id || i}>
                  <span className="col-time" data-label="时间">{fmtTime(o.created_at)}</span>
                  <span className="col-side" data-label="方向"><span className={['tag', o.side === 'buy' ? 'buy' : 'sell'].join(' ')}>{o.side === 'buy' ? '买入' : '卖出'}</span></span>
                  <span className="col-code" data-label="代码">{o.code}</span>
                  <span className="col-name" data-label="名称">{o.name}</span>
                  <span className="col-pool" data-label="来源"><span className="tag">{o.kind || '—'}</span></span>
                  <span className="col-num" data-label="状态"><span className={['tag', orderStatusCls(o.status)].join(' ')}>{orderStatusText(o.status)}</span></span>
                  <span className="col-num" data-label="数量">{o.qty || '—'}</span>
                  <span className="col-price" data-label="成交价">{o.price ? o.price.toFixed(2) : '—'}</span>
                  <span className="col-price" data-label="信号价">{o.signal_price ? o.signal_price.toFixed(2) : '—'}</span>
                  <span className="col-name" data-label="说明" title={o.reason || ''}>{shortReason(o.reason)}</span>
                </div>
              ))}
            </div>
          ) : <div className="empty-hint">暂无订单记录</div>}
        </div>
      )}

      {sheetPos && (
        <div className="sheet-overlay" onClick={() => setSheetPos(null)}>
          <div className="action-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-title">{sheetPos.code} {sheetPos.name}</div>
            <button className="sheet-btn" onClick={sheetKline}>{klineOpen.has(sheetPos.code) ? '收起分时' : '展开分时'}</button>
            <button className="sheet-btn" onClick={() => sheetTrade('add')}>加仓</button>
            <button className="sheet-btn" onClick={() => sheetTrade('trim')}>减仓</button>
            <button className="sheet-btn sheet-danger" onClick={() => sheetTrade('close')}>清仓</button>
            <button className="sheet-btn sheet-cancel" onClick={() => setSheetPos(null)}>取消</button>
          </div>
        </div>
      )}
      {sheetTradeRow && (
        <div className="sheet-overlay" onClick={() => setSheetTradeRow(null)}>
          <div className="action-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-title">{sheetTradeRow.code} {sheetTradeRow.name}</div>
            <button className="sheet-btn" onClick={sheetTradeKline}>
              {klineOpen.has('trade_' + sheetTradeRow.idx) ? '收起分时' : '展开分时'}
            </button>
            <button className="sheet-btn sheet-cancel" onClick={() => setSheetTradeRow(null)}>取消</button>
          </div>
        </div>
      )}

      {tradeModal && (
        <div className="modal-overlay" onClick={() => setTradeModal(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-title">
              {tradeDir === 'add' ? '加仓' : (tradeDir === 'trim' ? '减仓' : '清仓')}
              {tradeTarget?.code} {tradeTarget?.name}
            </div>
            <div className="form-row">
              <label>当前持仓</label>
              <span className="static-val">{tradeTarget?.qty} 股 / 成本 ¥{tradeTarget?.cost_price?.toFixed(2)}</span>
            </div>
            <div className="form-row">
              <label>价格</label>
              <input type="number" step="0.001" placeholder="成交价格（留空用实时价）" value={tradeFormPrice} onChange={(e) => setTradeFormPrice(parseFloat(e.target.value) || 0)} />
            </div>
            <div className="form-row">
              <label>{tradeDir === 'add' ? '加仓手数' : (tradeDir === 'trim' ? '减仓手数' : '清仓')}</label>
              {tradeDir !== 'close'
                ? <input type="number" step="1" placeholder="手数（1手=100股）" value={tradeFormQty} onChange={(e) => setTradeFormQty(parseInt(e.target.value, 10) || 1)} />
                : <span className="static-val">{tradeTarget?.qty} 股（全部）</span>}
            </div>
            {tradeDir === 'trim' && tradePreviewQty > 0 && (
              <div className="preview">减仓后：剩余 {tradeTarget.qty - tradePreviewQty * 100} 股</div>
            )}
            <div className="modal-actions">
              <button className="btn-cancel" onClick={() => setTradeModal(false)}>取消</button>
              <button className={['btn-confirm', tradeDir === 'close' ? 'btn-confirm-sell' : ''].join(' ')} onClick={confirmTrade} disabled={tradeOverSell}>确定</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
