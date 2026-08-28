// ── 模拟盘页面 Paper.jsx ──
// Paper trading: account state, strategy pools, positions/fills/orders, equity curve,
// manual buy/trim/close, deposit, pool/cap config, pool reset, full liquidation.
import React, { useState, useEffect, useRef, useMemo } from 'react'
import {
  Button, Dialog, DialogPlugin, Table, Tag, Card, Form, InputNumber, Input, Select, Tabs,
} from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import MinuteView from '../components/MinuteView.jsx'

const UP = '#e34d59'   // 涨（A股习惯红）
const DOWN = '#00a870' // 跌（绿）
const clsColor = (c) => (c === 'up' ? UP : c === 'down' ? DOWN : undefined)

/**
 * 封装 TDesign 确认对话框为 Promise。
 * 弹出「警告」主题确认框，用户点击确认返回 true，关闭或取消返回 false。
 * @param {string} body 弹窗正文内容
 * @param {string} [header='确认'] 弹窗标题
 * @returns {Promise<boolean>} 用户确认结果
 */
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

/**
 * 将数值格式化为带千分位、固定两位小数的中文本地化字符串。
 * @param {number|string} v 原始数值（null/undefined 视为 0）
 * @returns {string} 例如 "1,234.56"
 */
// 格式化为两位小数的中文数字
const fmt = (v) => (v ?? 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
/**
 * 将时间戳格式化为 "MM-DD HH:mm:ss"（本地时区）。
 * 无法解析时回退为字符串切片（取第 5~16 位）。
 * @param {number|string} t 时间戳或日期字符串
 * @returns {string} 格式化后的时间，空值返回 "—"
 */
// 格式化为 MM-DD HH:mm:ss
function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d)) return String(t).slice(5, 16)
  const p2 = (n) => String(n).padStart(2, '0')
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`
}
/**
 * 根据盈亏数值返回涨跌标记，用于决定文字颜色。
 * @param {number} v 盈亏值
 * @returns {'up'|'down'} 非负返回 'up'，否则 'down'
 */
// 根据盈亏返回 up/down
function pnlCls(v) { return v >= 0 ? 'up' : 'down' }
/**
 * 计算买入成交价相对信号价的滑点百分比（仅买入且信号价有效时）。
 * @param {object} t 成交记录，需含 side/price/signal_price
 * @returns {string} 形如 "+1.23%" 或 "—"（非买入/无信号价）
 */
// 计算买入成交价相对信号价的滑点
function tradeSlippage(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return '—'
  const pct = (t.price - t.signal_price) / t.signal_price * 100
  return (pct >= 0 ? '+' : '') + pct.toFixed(2) + '%'
}
/**
 * 返回买入滑点的涨跌标记，用于滑点文字着色。
 * 成交价高于信号价视为成本增加（down/绿），低于则视为节省（up/红）。
 * @param {object} t 成交记录
 * @returns {''|'up'|'down'} 无有效信号价时返回空串
 */
function tradeSlippageCls(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return ''
  return t.price >= t.signal_price ? 'down' : 'up'
}
/**
 * 将资金池 key 翻译为中文展示标签。
 * 已知 key（龙头/双响炮/N形/龙回头/动量）映射为对应中文；
 * 以 fac_ / pat_ 开头的分别加「因子·」「形态·」前缀。
 * @param {string} k 资金池 key（空视为"其他/手动"）
 * @returns {string} 中文标签
 */
// 将资金池 key 翻译为中文标签
function poolLabel(k) {
  if (!k) return '其他/手动'
  const labels = { dragon: '龙头', double_bump: '双响炮', n_shape: 'N形', dragon_return: '龙回头', momentum: '动量' }
  if (labels[k]) return labels[k]
  if (/^fac_/.test(k)) return '因子·' + k
  if (/^pat_/.test(k)) return '形态·' + k
  return k
}
/**
 * 规范化资金池 key：空 key 统一归为 "__other__" 一类，便于按池筛选。
 * @param {string} k 原始 key
 * @returns {string} 规范化后的 key
 */
// 规范化资金池 key，空 key 归为一类
function normPoolKey(k) { return k || '__other__' }
/**
 * 将订单状态英文枚举翻译为中文文案。
 * @param {string} s filled/partial/rejected 之一
 * @returns {string} 中文状态文案
 */
// 订单状态中文
function orderStatusText(s) { return { filled: '全部成交', partial: '部分成交', rejected: '已拒绝' }[s] || s }
/**
 * 返回订单状态对应的 TDesign Tag 主题色。
 * @param {string} s 订单状态
 * @returns {string} TDesign 主题名（success/warning/danger/default）
 */
// 订单状态徽标主题
function orderStatusTheme(s) { return { filled: 'success', partial: 'warning', rejected: 'danger' }[s] || 'default' }
/**
 * 截断过长的订单说明文本，超过 18 字用省略号收尾（用于表格列展示）。
 * @param {string} r 原始说明
 * @returns {string} 截断后文本或"—"
 */
// 截断原因文本
function shortReason(r) { if (!r) return '—'; return r.length > 18 ? r.slice(0, 18) + '…' : r }

/**
 * 单指标统计卡片：上方小灰字标签 + 下方大号数值。
 * @param {string} label 卡片标题
 * @param {React.ReactNode} children 数值内容
 */
// 单卡统计
function StatCard({ label, children }) {
  return (
    <Card bordered style={{ flex: '1 1 180px', minWidth: 160 }}>
      <div style={{ fontSize: 12, color: '#888' }}>{label}</div>
      <div style={{ fontSize: 18, fontWeight: 600, marginTop: 4 }}>{children}</div>
    </Card>
  )
}

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

  const W = 900, H = 220 // 净值曲线 SVG 的逻辑尺寸（viewBox 坐标，非真实像素）
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
  // 净值曲线背景横向网格线：在画布 1/4、2/4、3/4 高度处（k=1,2,3）
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

  const posData = useMemo(() => filteredPositions.map((p) => ({ ...p, __key: p.code })), [filteredPositions])
  const tradeData = useMemo(() => filteredTrades.map((t, i) => ({ ...t, __key: 'trade_' + i, __idx: i })), [filteredTrades])
  const orderData = useMemo(() => filteredOrders.map((o, i) => ({ ...o, __key: o.id || ('o_' + i) })), [filteredOrders])

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

  /**
   * 打开加仓/减仓/清仓交易弹窗并回填标的与默认手数。
   * @param {object} p 持仓记录（含 code/name/strategy/mark/qty）
   * @param {'add'|'trim'|'close'} dir 交易方向
   */
  function openTrade(p, dir) {
    setTradeTarget(p); setTradeDir(dir)
    setTradeFormPrice(p.mark || 0)
    setTradeFormQty(dir === 'close' ? p.qty : 1)
    setTradeModal(true)
  }

  /**
   * 提交模拟盘加仓/减仓/清仓委托。
   * add：调用 buyPaperPosition；trim/close：调用 sellPaperPosition（close 传 0 表示全平）。
   * 成功后关闭弹窗并重新加载数据。
   * @returns {Promise<void>}
   */
  // 提交模拟盘加仓/减仓/清仓委托
  async function confirmTrade() {
    const p = tradeTarget
    if (!p) return
    const price = parseFloat(tradeFormPrice)
    const qty = parseInt(tradeFormQty, 10)
    if (tradeDir !== 'close' && (isNaN(qty) || qty <= 0)) { showToast('请输入有效的数量','warning'); return }
    try {
      if (tradeDir === 'add') {
        await api.buyPaperPosition(p.code, p.name || '', p.strategy || '', 0, price > 0 ? price : 0, qty)
        showToast(`已加仓 ${p.code} ${qty} 手`,'success')
      } else {
        await api.sellPaperPosition(p.code, price > 0 ? price : 0, tradeDir === 'close' ? 0 : qty)
        showToast(`已${tradeDir === 'close' ? '清仓' : '减仓'} ${p.code}`,'success')
      }
      setTradeModal(false)
      await load()
    } catch (e) { showToast(e.message || '操作失败','error') }
  }

  /**
   * 加载模拟盘全部数据：先取开关/账户/资金池状态，
   * 仅在 enabled 时再拉取持仓、成交、委托与净值曲线（各自 try/catch 互不阻断）。
   * @returns {Promise<void>}
   */
  // 加载模拟盘状态、持仓、成交、委托与净值曲线
  async function load() {
    try {
      const st = await api.fetchPaperState()
      setEnabled(!!st.enabled)
      setIsAdmin(!!st.is_admin)
      if (st.initial_capital > 0 && !initialCapital) setInitialCapital(String(st.initial_capital))
      if (st.max_positions !== undefined && !maxPos) setMaxPos(st.max_positions > 0 ? String(st.max_positions) : '0')
      setAppliedMax((st.max_positions !== undefined && st.max_positions > 0) ? st.max_positions : 0)
      setStats(st.stats || null)
      setPools(Array.isArray(st.strategy_pools) ? st.strategy_pools : [])
    } catch (_) {}
    if (!enabled) return
    try { setPositions(await api.fetchPaperPositions()) } catch (_) {}
    try { setTrades(await api.fetchPaperTrades()) } catch (_) {}
    try { setOrders(await api.fetchPaperOrders()) } catch (_) {}
    try { setEquity(await api.fetchPaperEquity()) } catch (_) {}
  }

  /**
   * 确认注入资金（增量计入现金，保留现有持仓/净值/成交）。
   * 调用 resetPaper 并回填初始资金与持仓上限，然后重新加载。
   * @returns {Promise<void>}
   */
  // 确认注入资金并更新持仓上限
  async function confirmDeposit() {
    const amt = parseFloat(depositAmount)
    if (!(amt > 0)) { showToast('请输入有效的注入金额','warning'); return }
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
    } catch (e) { showToast(e.message || '注入失败','error') }
  }

  /**
   * 清盘当前选中的分仓资金池：按最后估值价平仓该池全部持仓并回补池现金，
   * 清空该池累计涨跌幅；不影响其他池与全局净值/成交。
   * @returns {Promise<void>}
   */
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
    } catch (e) { showToast(e.message || '清盘失败','error') }
  }

  /**
   * 打开统一设置弹窗（资金分配/仓位上限/买入纪律），
   * 将各资金池现有 cash/max_pos/buy_rule 回填到表单状态，并默认选中首个有效策略池。
   */
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

  /**
   * 保存设置：根据当前标签页分别校验并提交
   * - alloc：各池资金额之和不得超过总现金；
   * - rules：逐池写入买入纪律（日限/冷却/最低分/日预算%）；
   * - caps：各池上限之和不得超过全局上限。
   * 失败以 Toast 提示，成功关闭弹窗并重新加载。
   * @returns {Promise<void>}
   */
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
      catch (e) { showToast(e.message || '保存失败','error') }
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
      catch (e) { showToast(e.message || '保存失败','error') }
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
    catch (e) { showToast(e.message || '保存失败','error') }
  }

  /**
   * 全局清盘重置：平仓全部持仓、清除成交日志与净值曲线。
   * 可携带 reset_to（重置后初始资金）与 max_positions（持仓上限）参数。
   * @returns {Promise<void>}
   */
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
    } catch (e) { showToast(e.message || '清盘失败','error') }
  }

  // 挂载时加载模拟盘数据并启动 15s 轮询
  useEffect(() => {
    load()
    timer.current = setInterval(load, 15000) // 每 15 秒轮询刷新模拟盘数据
    return () => { if (timer.current) clearInterval(timer.current) }
  }, [])

  // ── 列定义 ──
  const posColumns = [
    { colKey: 'code', title: '代码', width: 90 },
    { colKey: 'name', title: '名称', width: 100 },
    { colKey: 'time', title: '买入时间', width: 160, cell: ({ row }) => (
      <span title={'信号发出 ' + fmtTime(row.signal_at) + ' · 撮合成交 ' + fmtTime(row.filled_at)}>{fmtTime(row.filled_at || row.signal_at)}</span>
    ) },
    { colKey: 'qty', title: '数量', width: 70 },
    { colKey: 'cost', title: '成本价', width: 90, cell: ({ row }) => (row.cost_price || 0).toFixed(2) },
    { colKey: 'mark', title: '现价', width: 90, cell: ({ row }) => (row.mark || 0).toFixed(2) },
    { colKey: 'pnl', title: '浮盈', width: 100, cell: ({ row }) => <span style={{ color: row.pnl >= 0 ? UP : DOWN }}>{fmt(row.pnl)}</span> },
    { colKey: 'pnlPct', title: '浮盈%', width: 90, cell: ({ row }) => <span style={{ color: row.pnl >= 0 ? UP : DOWN }}>{fmt(row.pnl_pct)}%</span> },
    { colKey: 'slip', title: '滑点', width: 90, cell: ({ row }) => <span style={{ color: row.slippage_pct >= 0 ? UP : DOWN }}>{fmt(row.slippage_pct)}%</span> },
    { colKey: 'lat', title: '延迟', width: 70, cell: ({ row }) => row.latency_sec + 's' },
    { colKey: 'pool', title: '池', width: 100, cell: ({ row }) => <Tag>{poolLabel(row.strategy_type)}</Tag> },
    { colKey: 'kline', title: '分时', width: 80, cell: ({ row }) => (
      <Button size="small" variant="text" onClick={(e) => { e.stopPropagation(); toggleKline(row.__key) }}>
        {klineOpen.has(row.__key) ? '收起' : '分时'}
      </Button>
    ) },
    { colKey: 'ops', title: '操作', width: 200, cell: ({ row }) => (
      <div style={{ display: 'flex', gap: 6 }}>
        <Button size="small" onClick={(e) => { e.stopPropagation(); openTrade(row, 'add') }}>加仓</Button>
        <Button size="small" onClick={(e) => { e.stopPropagation(); openTrade(row, 'trim') }}>减仓</Button>
        <Button size="small" theme="danger" onClick={(e) => { e.stopPropagation(); openTrade(row, 'close') }}>清仓</Button>
      </div>
    ) },
  ]

  const tradeColumns = [
    { colKey: 'time', title: '时间', width: 160, cell: ({ row }) => fmtTime(row.time) },
    { colKey: 'side', title: '方向', width: 80, cell: ({ row }) => <Tag theme={row.side === 'buy' ? 'success' : 'danger'}>{row.side === 'buy' ? '买入' : '卖出'}</Tag> },
    { colKey: 'code', title: '代码', width: 90 },
    { colKey: 'name', title: '名称', width: 100 },
    { colKey: 'strategy', title: '战法', width: 100, cell: ({ row }) => <Tag>{row.strategy}</Tag> },
    { colKey: 'qty', title: '数量', width: 70 },
    { colKey: 'price', title: '价格', width: 90, cell: ({ row }) => (row.price || 0).toFixed(2) },
    { colKey: 'amount', title: '金额', width: 100, cell: ({ row }) => fmt(row.amount) },
    { colKey: 'slip', title: '滑点', width: 90, cell: ({ row }) => {
      const c = tradeSlippageCls(row)
      return <span style={c ? { color: clsColor(c) } : undefined}>{tradeSlippage(row)}</span>
    } },
    { colKey: 'lat', title: '延迟', width: 70, cell: ({ row }) => (row.side === 'buy' ? (row.latency_sec || 0) + 's' : '—') },
    { colKey: 'kline', title: '分时', width: 80, cell: ({ row }) => (
      <Button size="small" variant="text" onClick={(e) => { e.stopPropagation(); toggleKline(row.__key) }}>
        {klineOpen.has(row.__key) ? '收起' : '分时'}
      </Button>
    ) },
  ]

  const orderColumns = [
    { colKey: 'time', title: '时间', width: 160, cell: ({ row }) => fmtTime(row.created_at) },
    { colKey: 'side', title: '方向', width: 80, cell: ({ row }) => <Tag theme={row.side === 'buy' ? 'success' : 'danger'}>{row.side === 'buy' ? '买入' : '卖出'}</Tag> },
    { colKey: 'code', title: '代码', width: 90 },
    { colKey: 'name', title: '名称', width: 100 },
    { colKey: 'kind', title: '来源', width: 90, cell: ({ row }) => <Tag>{row.kind || '—'}</Tag> },
    { colKey: 'status', title: '状态', width: 90, cell: ({ row }) => <Tag theme={orderStatusTheme(row.status)}>{orderStatusText(row.status)}</Tag> },
    { colKey: 'qty', title: '数量', width: 70 },
    { colKey: 'price', title: '成交价', width: 90, cell: ({ row }) => (row.price ? row.price.toFixed(2) : '—') },
    { colKey: 'signal', title: '信号价', width: 90, cell: ({ row }) => (row.signal_price ? row.signal_price.toFixed(2) : '—') },
    { colKey: 'reason', title: '说明', width: 160, ellipsis: true, cell: ({ row }) => <span title={row.reason || ''}>{shortReason(row.reason)}</span> },
  ]

  function renderKline(params) {
    const row = params && params.row ? params.row : params
    return <MinuteView code={row.code} name={row.name} />
  }

  return (
    <div className="page">
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12, flexWrap: 'wrap', gap: 8 }}>
        <h2 style={{ fontSize: 18, fontWeight: 600 }}>模拟盘</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {isAdmin && <Tag theme="warning" style={{ cursor: 'default' }}>联动版</Tag>}
          <Tag theme={enabled ? 'success' : 'default'}>
            {enabled ? (isAdmin ? '自动撮合中' : '手动记账（静态）') : '未启用（rules.paper.enabled）'}
          </Tag>
          {enabled && <Tag>上限：{appliedMax > 0 ? appliedMax + ' 只' : '不设限'}</Tag>}
          <Button theme="primary" disabled={!enabled} onClick={() => setShowDepositModal(true)}>＋ 注入资金</Button>
          <Button disabled={!enabled} onClick={openSettingsModal}>⚙ 设置</Button>
          <Button theme="danger" disabled={!enabled} onClick={() => setShowResetModal(true)}>清盘</Button>
        </div>
      </div>

      {/* 注入资金弹窗 */}
      <Dialog
        visible={showDepositModal}
        header="注入资金"
        onClose={() => setShowDepositModal(false)}
        onConfirm={() => { confirmDeposit(); setShowDepositModal(false) }}
        confirmBtn="确认注入"
      >
        <Form layout="vertical">
          <Form.FormItem label="金额（元）">
            <InputNumber value={depositAmount} min={0} step={1000} placeholder="10000" onChange={(v) => setDepositAmount(v || 0)} style={{ width: 240 }} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 清盘弹窗 */}
      <Dialog
        visible={showResetModal}
        header="⚠ 清盘重置"
        onClose={() => setShowResetModal(false)}
        onConfirm={doResetV2}
        confirmBtn="确认清盘"
      >
        <div style={{ color: '#e6a23c', marginBottom: 8 }}>将平仓全部持仓、清除成交日志与净值曲线。</div>
        <Form layout="vertical">
          <Form.FormItem label="重置后初始资金">
            <InputNumber value={resetToCapital} min={0} step={10000} placeholder="默认 100000" onChange={(v) => setResetToCapital(v || 0)} style={{ width: 240 }} />
            <span style={{ fontSize: 12, color: '#888' }}>元（不填则按当前累计投入总额重置）</span>
          </Form.FormItem>
          <Form.FormItem label="持仓上限">
            <InputNumber value={resetMaxPos} min={0} step={1} placeholder="0=不设限" onChange={(v) => setResetMaxPos(v || 0)} style={{ width: 240 }} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 统一设置弹窗 */}
      <Dialog
        visible={settingsOpen}
        header="⚙ 设置"
        onClose={() => setSettingsOpen(false)}
        onConfirm={saveSettings}
        confirmBtn="保存"
        width={640}
      >
        <Tabs value={settingsTab} onChange={(v) => setSettingsTab(v)}>
          <Tabs.TabPanel value="alloc" label="资金分配">
            <div style={{ fontSize: 12, color: '#888', marginBottom: 8 }}>每池资金额（Σ ≈ 总现金守恒）。不影响仓位上限。</div>
            {pools.map((p) => (
              <Form.FormItem key={'sa-' + p.key} label={p.label}>
                <InputNumber
                  value={cfgAllocs[p.key]}
                  min={0} step={1000}
                  placeholder={'当前 ¥' + fmt(p.cash)}
                  onChange={(v) => setCfgAllocs({ ...cfgAllocs, [p.key]: v || 0 })}
                  style={{ width: 240 }}
                />
              </Form.FormItem>
            ))}
          </Tabs.TabPanel>
          <Tabs.TabPanel value="caps" label="仓位上限">
            <Form.FormItem label="全局持仓上限（0=不设限）">
              <InputNumber value={cfgMaxPos} min={0} step={1} placeholder="0=不设限" onChange={(v) => setCfgMaxPos(v || 0)} style={{ width: 240 }} />
            </Form.FormItem>
            <div style={{ fontSize: 12, color: '#888', margin: '8px 0' }}>每池持仓上限（0=不单独设限）。Σ ≤ 全局。不影响资金分配。</div>
            {pools.map((p) => (
              <Form.FormItem key={'sc-' + p.key} label={p.label}>
                <InputNumber
                  value={cfgCaps[p.key]}
                  min={0} step={1}
                  placeholder={p.max_pos > 0 ? '当前 ' + p.max_pos : '不单独设限'}
                  onChange={(v) => setCfgCaps({ ...cfgCaps, [p.key]: v || 0 })}
                  style={{ width: 240 }}
                />
              </Form.FormItem>
            ))}
          </Tabs.TabPanel>
          <Tabs.TabPanel value="rules" label="买入纪律">
            <div style={{ fontSize: 12, color: '#888', marginBottom: 8 }}>
              每池买入纪律：日限次数 / 冷却分钟 / 最低评分 / 日预算%。全 0 = 不设限；寻优审批会自动把门槛写入对应池的「最低评分」。
            </div>
            <Select value={cfgRuleSel} onChange={(v) => setCfgRuleSel(v)} style={{ width: 240, marginBottom: 8 }}>
              {pools.map((p) => <Select.Option key={'sel-' + p.key} value={p.key}>{p.label}</Select.Option>)}
            </Select>
            {cfgRules[cfgRuleSel] && (
              <div>
                <div style={{ marginBottom: 8 }}>
                  {poolLabel(cfgRuleSel)}
                  {poolCurrentRule(cfgRuleSel) && <span style={{ color: '#888' }}>（当前生效：{poolCurrentRuleText(cfgRuleSel)}）</span>}
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                  <Form.FormItem label="日限买">
                    <InputNumber value={cfgRules[cfgRuleSel].max_daily_buys} min={0} step={1} placeholder="0=不限" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], max_daily_buys: v || 0 } })} />
                  </Form.FormItem>
                  <Form.FormItem label="冷却(分)">
                    <InputNumber value={cfgRules[cfgRuleSel].cooldown_minutes} min={0} step={5} placeholder="0=不限" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], cooldown_minutes: v || 0 } })} />
                  </Form.FormItem>
                  <Form.FormItem label="最低分">
                    <InputNumber value={cfgRules[cfgRuleSel].min_score} min={0} max={100} step={1} placeholder="0=不过滤" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], min_score: v || 0 } })} />
                  </Form.FormItem>
                  <Form.FormItem label="日预算%">
                    <InputNumber value={cfgRules[cfgRuleSel].budget_pct_per_day} min={0} max={100} step={5} placeholder="0=不限" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], budget_pct_per_day: v || 0 } })} />
                  </Form.FormItem>
                </div>
              </div>
            )}
          </Tabs.TabPanel>
        </Tabs>
        {cfgWarn && <div style={{ color: '#e6a23c', marginTop: 8 }}>{cfgWarn}</div>}
      </Dialog>

      {/* 分仓资金池条 */}
      {enabled && pools.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <span style={{ color: '#888', fontSize: 13 }}>分仓资金池</span>
          <Tag
            style={{ cursor: 'pointer', background: activePool === null ? '#1d4ed8' : undefined, color: activePool === null ? '#ffffff' : undefined, borderColor: activePool === null ? '#1d4ed8' : undefined }}
            onClick={() => setActivePool(null)}
          >
            全部（{positions.length} 仓）
          </Tag>
          {pools.map((p) => {
            const key = normPoolKey(p.key)
            const active = activePool === key
            return (
              <Tag
                key={p.key}
                style={{ cursor: 'pointer', background: active ? '#1d4ed8' : undefined, color: active ? '#ffffff' : undefined, borderColor: active ? '#1d4ed8' : undefined }}
                onClick={() => setActivePool(active ? null : key)}
              >
                {p.label} <span style={{ color: p.return_pct >= 0 ? UP : DOWN }}>{(p.return_pct >= 0 ? '+' : '') + p.return_pct.toFixed(2)}%</span> · ¥{fmt(p.cash)} · {p.ratio_pct.toFixed(1)}%·{p.positions}仓
              </Tag>
            )
          })}
          {activePool !== null && (
            <Button size="small" theme="warning" disabled={!enabled} onClick={confirmPoolReset}>清盘本池</Button>
          )}
        </div>
      )}

      {/* 统计范围标签 */}
      {enabled && activePool !== null && (
        <div style={{ marginBottom: 8 }}><Tag>统计范围：{activePoolLabel}</Tag></div>
      )}

      {/* 绩效统计卡 */}
      {activeStats && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, marginBottom: 12 }}>
          <StatCard label="总资产">¥{fmt(activeStats.total_value)}</StatCard>
          <StatCard label="总收益">
            <span style={{ color: activeStats.total_return_pct >= 0 ? UP : DOWN }}>
              {(activeStats.total_return_pct >= 0 ? '+' : '') + activeStats.total_return_pct.toFixed(2)}%
            </span>
            <em style={{ fontSize: 12, color: '#888', fontStyle: 'normal' }}> 基于累计投入 ¥{fmt(activeStats.initial_capital)}</em>
          </StatCard>
          <StatCard label="当日收益">
            <span style={{ color: activeStats.today_return_pct >= 0 ? UP : DOWN }}>
              {(activeStats.today_return_pct >= 0 ? '+' : '') + activeStats.today_return_pct.toFixed(2)}%
            </span>
          </StatCard>
          <StatCard label="现金">¥{fmt(activeStats.cash)}</StatCard>
          <StatCard label="持仓市值 / 已实现盈亏">
            ¥{fmt(activeStats.market_value)}
            <em style={{ fontSize: 12, color: activeStats.realized_pnl >= 0 ? UP : DOWN, fontStyle: 'normal' }}>
              {' '}{(activeStats.realized_pnl >= 0 ? '+' : '')}¥{fmt(activeStats.realized_pnl)}
            </em>
          </StatCard>
          <StatCard label="已平仓胜率">{activeStats.win_rate_pct.toFixed(0)}% <em style={{ fontSize: 12, color: '#888', fontStyle: 'normal' }}>/ {activeStats.open_positions}仓</em></StatCard>
        </div>
      )}

      {/* 信号质量统计卡（仅联动版） */}
      {activeStats && isAdmin && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, marginBottom: 12 }}>
          <StatCard label="已撮合买入信号">{activeStats.filled_buys}</StatCard>
          <StatCard label="平均成交延迟">{activeStats.avg_latency_sec}s <em style={{ fontSize: 12, color: '#888', fontStyle: 'normal' }}>最大 {activeStats.max_latency_sec}s</em></StatCard>
          <StatCard label="平均滑点（成交 vs 信号价）">
            <span style={{ color: activeStats.avg_slippage_pct >= 0 ? UP : DOWN }}>
              {(activeStats.avg_slippage_pct >= 0 ? '+' : '') + activeStats.avg_slippage_pct.toFixed(2)}%
            </span>
          </StatCard>
          <StatCard label="滑点累计成本">
            <span style={{ color: activeStats.slippage_cost >= 0 ? UP : DOWN }}>
              {(activeStats.slippage_cost >= 0 ? '+' : '')}¥{fmt(activeStats.slippage_cost)}
            </span>
            <em style={{ fontSize: 12, color: '#888', fontStyle: 'normal' }}> 占初始 {activeStats.signal_amount_pct.toFixed(2)}%</em>
          </StatCard>
        </div>
      )}

      {/* 净值曲线 */}
      {isAdmin && (
        <Card title={<span>净值曲线 <em style={{ color: '#888', fontSize: 12, fontStyle: 'normal' }}>（{stats?.equity_curve_points || 0} 个交易日）</em></span>} style={{ marginBottom: 12 }}>
          {equity.length > 1 ? (
            <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ width: '100%', height: H }}>
              <polyline points={linePoints} fill="none" stroke="#FF4D4F" strokeWidth="2" />
              {gridLines.map((lvl) => <line key={lvl.y} x1="0" y1={lvl.y} x2={W} y2={lvl.y} style={{ stroke: '#eef0f3' }} />)}
            </svg>
          ) : <div className="muted" style={{ padding: 24, textAlign: 'center' }}>净值数据不足（自动撮合开启并产生成交后显示）</div>}
        </Card>
      )}

      {/* 持仓 / 成交 / 订单 */}
      <Tabs value={tab} onChange={(v) => setTab(v)} style={{ marginBottom: 8 }}>
        <Tabs.TabPanel value="positions" label={`当前持仓 (${filteredPositions.length})`}>
          <Card>
            {posData.length ? (
              <Table
                rowKey="__key"
                data={posData}
                columns={posColumns}
                expandedRow={renderKline}
                expandedRowKeys={[...klineOpen]}
                onExpandChange={(keys) => setKlineOpen(new Set(keys))}
                onRowClick={({ row }) => onRowTap(row)}
                bordered
                size="small"
              />
            ) : (
              <div className="muted" style={{ padding: 24, textAlign: 'center' }}>
                {isAdmin ? '暂无持仓（出现可开仓信号时按实时价自动买入）' : '暂无持仓（在信号页点「模拟买入」，或上方加仓/减仓管理已有持仓）'}
              </div>
            )}
          </Card>
        </Tabs.TabPanel>
        <Tabs.TabPanel value="trades" label={`成交日志 (${filteredTrades.length})`}>
          <Card>
            {tradeData.length ? (
              <Table
                rowKey="__key"
                data={tradeData}
                columns={tradeColumns}
                expandedRow={renderKline}
                expandedRowKeys={[...klineOpen]}
                onExpandChange={(keys) => setKlineOpen(new Set(keys))}
                onRowClick={({ row }) => onTradeTap(row, row.__idx)}
                bordered
                size="small"
              />
            ) : <div className="muted" style={{ padding: 24, textAlign: 'center' }}>暂无成交记录</div>}
          </Card>
        </Tabs.TabPanel>
        <Tabs.TabPanel value="orders" label={`订单 (${filteredOrders.length})`}>
          <Card>
            {orderData.length ? (
              <Table rowKey="__key" data={orderData} columns={orderColumns} bordered size="small" />
            ) : <div className="muted" style={{ padding: 24, textAlign: 'center' }}>暂无订单记录</div>}
          </Card>
        </Tabs.TabPanel>
      </Tabs>

      {/* 移动端：持仓行操作菜单 */}
      <Dialog visible={!!sheetPos} header={sheetPos ? sheetPos.code + ' ' + sheetPos.name : ''} onClose={() => setSheetPos(null)} footer={null}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <Button block onClick={sheetKline}>{sheetPos && klineOpen.has(sheetPos.code) ? '收起分时' : '展开分时'}</Button>
          <Button block onClick={() => sheetTrade('add')}>加仓</Button>
          <Button block onClick={() => sheetTrade('trim')}>减仓</Button>
          <Button block theme="danger" onClick={() => sheetTrade('close')}>清仓</Button>
          <Button block onClick={() => setSheetPos(null)}>取消</Button>
        </div>
      </Dialog>
      {/* 移动端：成交行操作菜单 */}
      <Dialog visible={!!sheetTradeRow} header={sheetTradeRow ? sheetTradeRow.code + ' ' + sheetTradeRow.name : ''} onClose={() => setSheetTradeRow(null)} footer={null}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <Button block onClick={sheetTradeKline}>
            {sheetTradeRow && klineOpen.has('trade_' + sheetTradeRow.idx) ? '收起分时' : '展开分时'}
          </Button>
          <Button block onClick={() => setSheetTradeRow(null)}>取消</Button>
        </div>
      </Dialog>

      {/* 交易弹窗：加仓 / 减仓 / 清仓 */}
      <Dialog
        visible={tradeModal}
        header={(tradeDir === 'add' ? '加仓' : tradeDir === 'trim' ? '减仓' : '清仓') + (tradeTarget ? ' ' + tradeTarget.code + ' ' + tradeTarget.name : '')}
        onClose={() => setTradeModal(false)}
        onConfirm={confirmTrade}
        confirmBtn={{ content: '确定', disabled: tradeOverSell, theme: tradeDir === 'close' ? 'danger' : 'primary' }}
      >
        <Form layout="vertical">
          <Form.FormItem label="当前持仓">
            <span>{tradeTarget?.qty} 股 / 成本 ¥{tradeTarget?.cost_price?.toFixed(2)}</span>
          </Form.FormItem>
          <Form.FormItem label="价格">
            <InputNumber value={tradeFormPrice} step={0.001} placeholder="成交价格（留空用实时价）" onChange={(v) => setTradeFormPrice(v || 0)} style={{ width: 240 }} />
          </Form.FormItem>
          <Form.FormItem label={tradeDir === 'add' ? '加仓手数' : tradeDir === 'trim' ? '减仓手数' : '清仓'}>
            {tradeDir !== 'close'
              ? <InputNumber value={tradeFormQty} step={1} placeholder="手数（1手=100股）" onChange={(v) => setTradeFormQty(v || 1)} style={{ width: 240 }} />
              : <span>{tradeTarget?.qty} 股（全部）</span>}
          </Form.FormItem>
          {tradeDir === 'trim' && tradePreviewQty > 0 && (
            <div style={{ color: '#e6a23c' }}>减仓后：剩余 {tradeTarget.qty - tradePreviewQty * 100} 股</div>
          )}
        </Form>
      </Dialog>
    </div>
  )
}
