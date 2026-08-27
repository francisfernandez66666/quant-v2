// ── 策略信号页面 Signals.jsx ──
// 展示所有策略评级信号，支持按等级/战法筛选、查看 D1-D4 子维度评分、
// 确认买入/忽略操作、模拟买入归池、一键收藏自选、展开分时+盘口。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import KLineChart from '../components/KLineChart.jsx'
import DepthPanel from '../components/DepthPanel.jsx'
import './Signals.css'

const FILTERS = [
  { key: 'all', label: '全部' },
  { key: 'strong', label: '可开仓' },
  { key: 'observe', label: '观察' },
  { key: 'mute', label: '静默' },
]

// 取长描述的前缀或前 6 个字符，用于 D-pill 展示
function shortDesc(s) {
  if (!s) return ''
  const idx = s.indexOf(',')
  return idx > 0 ? s.slice(0, idx) : s.slice(0, 6)
}

// 构造 D1 维度标签（事件摘要 + 评分 + 拦截标记）
function d1Tag(s) {
  const label = s.d1_event || s.d1_reason || ''
  const base = shortDesc(label) || '事件'
  const score = s.d1_score ? s.d1_score.toFixed(0) : ''
  const blocked = s.d1_blocked ? '·拦' : ''
  return [base, score, blocked].filter(Boolean).join('')
}

/**
 * 策略信号页面组件
 * 展示策略评级信号，支持等级/战法筛选、D1-D4 维度展示、买入/忽略、模拟买入与日志。
 * @returns {JSX.Element}
 */
export default function Signals() {
  const [signals, setSignals] = useState([])
  const [klineOpen, setKlineOpen] = useState(new Set())
  const [activeFilter, setActiveFilter] = useState('all')
  const [activeStrategy, setActiveStrategy] = useState('all')
  const [showConfirm, setShowConfirm] = useState(false)
  const [showLog, setShowLog] = useState(false)
  const [logData, setLogData] = useState(null)
  const [sheetSignal, setSheetSignal] = useState(null)
  const [paperOn, setPaperOn] = useState(false)
  const [tradeTarget, setTradeTarget] = useState({})
  const [tradeAction, setTradeAction] = useState('')
  const timer = useRef(null)
  const visHandler = useRef(null)
  const unsubSSE = useRef(null)

  const strategyOptions = Array.from(new Set(signals.map((s) => s.strategy).filter(Boolean)))

  const filteredSignals = signals
    .filter((s) => (activeFilter !== 'all' ? s.remind_level === activeFilter : true))
    .filter((s) => (activeStrategy !== 'all' ? s.strategy === activeStrategy : true))
    .filter((s) => s.strategy !== '预期差')

  // 根据操作结果弹出成功/失败提示
  function showFeedback(msg, type) {
    showToast(msg, type === 'ok' ? 'success' : 'error')
  }

  // 判断信号是否来自研究战法库（有独立资金池）
  function hasStrategyPool(s) {
    if (!s) return false
    const id = s.strategy_id || ''
    if (id.startsWith('fac_') || id.startsWith('pat_')) return true
    return !!s.strategy_type
  }

  // 打开买入/忽略确认弹窗
  function confirmTrade(s, action) {
    setTradeTarget(s)
    setTradeAction(action)
    setShowConfirm(true)
  }

  // 执行买入或忽略操作并刷新信号列表
  async function doAction(action) {
    try {
      await api.actionSignal(tradeTarget.code, action)
      setShowConfirm(false)
      await load()
    } catch (e) {
      setShowConfirm(false)
      showToast('操作失败: ' + e.message, 'error')
    }
  }

  // 展开/收起指定代码的分时图
  function toggleKline(code) {
    setKlineOpen((prev) => {
      const next = new Set(prev)
      if (next.has(code)) next.delete(code)
      else next.add(code)
      return next
    })
  }

  // 移动端点击行时打开底部操作面板
  function onRowTap(s) {
    if (window.innerWidth > 768) return
    setSheetSignal(s)
  }

  // 将信号对应个股加入自选股
  async function collectToWatchlist(s) {
    try {
      await api.addWatchlist(s.code)
      showFeedback('已收藏 ' + s.code, 'ok')
    } catch (e) {
      showFeedback('收藏失败: ' + e.message, 'err')
    }
  }

  // 在模拟盘按指定价格/数量买入该信号个股
  async function paperBuy(s) {
    const priceStr = window.prompt('输入买入价（元，留空用实时价）：', s.price || s.close || '')
    const qtyStr = window.prompt('输入买入手数（1 手 = 100 股）：', '1')
    if (qtyStr === null || priceStr === null) return
    const qty = parseInt(qtyStr, 10)
    const price = parseFloat(priceStr)
    if (isNaN(qty) || qty <= 0) {
      window.alert('买入手数无效，请填写正整数')
      return
    }
    if (isNaN(price) || price <= 0) {
      if (!window.confirm(`确认模拟买入 ${s.code} ${s.name || ''} ${qty} 手？将按实时价成交。`)) return
    } else {
      if (!window.confirm(`确认模拟买入 ${s.code} ${s.name || ''} ${qty} 手 @${price.toFixed(2)}？`)) return
    }
    try {
      await api.buyPaperPosition(s.code, s.name || '', s.strategy || '', s.price || 0, isNaN(price) || price <= 0 ? 0 : price, qty, s.strategy_type || '', s.strategy_id || '')
      window.alert(`已模拟买入 ${s.code} ${qty} 手`)
    } catch (e) {
      window.alert(e.message || '模拟买入失败')
    }
  }

  // 加载当前策略信号列表
  async function load() {
    try { setSignals(await api.fetchSignals()) } catch (_) {}
  }

  // 探测模拟盘开关状态
  async function probePaper() {
    try { setPaperOn(!!(await api.fetchPaperState()).enabled) } catch (_) {}
  }

  // 打开信号批次日志弹窗并加载日志数据
  async function openLog() {
    setShowLog(true)
    try {
      const data = await api.fetchSignalLogs()
      setLogData(data)
    } catch (_) {
      setLogData(null)
    }
  }

  // SSE 新信号或扫描到达时刷新列表
  function handleSSE(msg) {
    if (msg.signal || msg.type === 'scan') load()
  }

  // 挂载时加载信号、探测模拟盘、启动轮询与 SSE；处理页面可见性变化
  useEffect(() => {
    load()
    probePaper()
    timer.current = setInterval(load, 5000)
    visHandler.current = () => {
      if (document.hidden) {
        if (timer.current) { clearInterval(timer.current); timer.current = null }
      } else if (!timer.current) {
        load()
        timer.current = setInterval(load, 5000)
      }
    }
    document.addEventListener('visibilitychange', visHandler.current)
    api.connectSSE()
    unsubSSE.current = api.onSSE(handleSSE)
    return () => {
      if (timer.current) clearInterval(timer.current)
      if (visHandler.current) document.removeEventListener('visibilitychange', visHandler.current)
      if (unsubSSE.current) unsubSSE.current()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="signals-page">
      <div className="page-header">
        <h2>策略信号</h2>
        <div className="filter-row">
          {FILTERS.map((f) => (
            <button key={f.key} className={'filter-btn' + (activeFilter === f.key ? ' active' : '')} onClick={() => setActiveFilter(f.key)}>
              {f.label}
            </button>
          ))}
          <select className="strategy-select" value={activeStrategy} onChange={(e) => setActiveStrategy(e.target.value)} title="按策略名称筛选">
            <option value="all">全部策略</option>
            {strategyOptions.map((st) => (
              <option key={st} value={st}>{st}</option>
            ))}
          </select>
          <button className="btn-log" onClick={openLog}>📋 日志</button>
        </div>
      </div>

      <div className="signals-table">
        <div className="table-header">
          <span className="col-code">代码</span>
          <span className="col-name">名称</span>
          <span className="col-price">现价/涨跌</span>
          <span className="col-strategy">策略</span>
          <span className="col-score">总分</span>
          <span className="col-level">等级</span>
          <span className="col-detail">D1/D2/D3/D4</span>
          <span className="col-kline">分时</span>
          <span className="col-action">操作</span>
        </div>
        {filteredSignals.map((s) => (
          <div key={s.code} className="table-row-group">
            <div className="table-row" onClick={() => onRowTap(s)}>
              <span className="col-code" data-label="代码">{s.code}</span>
              <span className="col-name" data-label="名称">{s.name || '-'}</span>
              <span className="col-price" data-label="现价/涨跌">
                <span className="px-price">¥{(s.price || 0).toFixed(2)}</span>
                <span className={'px-chg ' + ((s.change_pct || 0) >= 0 ? 'up' : 'down')}>
                  {(s.change_pct || 0) > 0 ? '+' : ''}{(s.change_pct || 0).toFixed(2)}%
                </span>
              </span>
              <span className="col-strategy" data-label="策略">{s.strategy}</span>
              <span className="col-score" data-label="总分">{s.total_score?.toFixed(0)}</span>
              <span className="col-level" data-label="等级">
                <span className={'tag ' + s.remind_level}>
                  {s.level === '交易' ? '交易' : s.level === '观望' ? '观望' : s.remind_level === 'strong' ? '可开仓' : s.remind_level === 'observe' ? '观察' : '静默'}
                </span>
              </span>
              <span className="col-detail" data-label="D1/D2/D3/D4">
                <span className="d-pill d1" title={'D1事件: ' + (s.d1_reason || s.d1_event || '无事件') + (s.d1_blocked ? '（负面拦截）' : '')}>
                  {s.d1_score && (s.d1_reason || s.d1_event)
                    ? <em>{d1Tag(s)}</em>
                    : <span className="d1-none">{s.d1 ? s.d1.toFixed(0) : '—'}</span>}
                </span>
                <span className="d-pill d2" title={'D2: ' + (s.d2_desc || '')}>
                  {s.d2 ? s.d2.toFixed(0) : '—'}{s.d2_desc && <em>{shortDesc(s.d2_desc)}</em>}
                </span>
                <span className="d-pill d3" title={'D3: ' + (s.d3_desc || '')}>
                  {s.d3 ? s.d3.toFixed(0) : '—'}{s.d3_desc && <em>{shortDesc(s.d3_desc)}</em>}
                </span>
                <span className="d-pill d4" title={'D4: ' + (s.d4_desc || '')}>
                  {s.d4 ? s.d4.toFixed(0) : '—'}{s.d4_desc && <em>{shortDesc(s.d4_desc)}</em>}
                </span>
              </span>
              <span className="col-kline" data-label="分时">
                <button className="btn-kline" onClick={(e) => { e.stopPropagation(); toggleKline(s.code) }} title={klineOpen.has(s.code) ? '收起分时' : '展开分时'}>
                  {klineOpen.has(s.code) ? '收起' : '分时'}
                </button>
              </span>
              <span className="col-action" data-label="操作">
                {s.can_open
                  ? <button className="btn-buy" onClick={(e) => { e.stopPropagation(); confirmTrade(s, 'buy') }}>买入</button>
                  : s.action === 'buy'
                    ? <button className="btn-ignore" onClick={(e) => { e.stopPropagation(); confirmTrade(s, 'ignore') }}>忽略</button>
                    : <span className="text-muted">—</span>}
                {paperOn && s.can_open && hasStrategyPool(s) && (
                  <button className="btn-paper" onClick={(e) => { e.stopPropagation(); paperBuy(s) }} title="模拟买入归入该信号所属战法资金池（非战法信号不可买）">模拟买入</button>
                )}
                {!s.can_open && s.action !== 'buy' && (
                  <button className="btn-collect" onClick={(e) => { e.stopPropagation(); collectToWatchlist(s) }}>收藏</button>
                )}
              </span>
            </div>
            {klineOpen.has(s.code) && (
              <div className="col-kline-row">
                <div className="kline-flex">
                  <div className="kline-main"><KLineChart code={s.code} name={s.name} /></div>
                  <div className="depth-side"><DepthPanel code={s.code} name={s.name} /></div>
                </div>
              </div>
            )}
          </div>
        ))}
        {filteredSignals.length === 0 && <div className="empty">暂无信号</div>}
      </div>

      {sheetSignal && (
        <div className="sheet-overlay" onClick={() => setSheetSignal(null)}>
          <div className="action-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-title">{sheetSignal.code} {sheetSignal.name || ''} · {sheetSignal.strategy}</div>
            {sheetSignal.can_open && <button className="sheet-btn sheet-danger" onClick={() => { const s = sheetSignal; setSheetSignal(null); confirmTrade(s, 'buy') }}>买入</button>}
            {sheetSignal.can_open && paperOn && hasStrategyPool(sheetSignal) && (
              <button className="sheet-btn sheet-paper" onClick={() => { const s = sheetSignal; setSheetSignal(null); paperBuy(s) }}>模拟买入</button>
            )}
            {sheetSignal.action === 'buy' && <button className="sheet-btn" onClick={() => { const s = sheetSignal; setSheetSignal(null); confirmTrade(s, 'ignore') }}>忽略</button>}
            {!sheetSignal.can_open && sheetSignal.action !== 'buy' && <button className="sheet-btn" onClick={() => { const s = sheetSignal; setSheetSignal(null); collectToWatchlist(s) }}>收藏</button>}
            <button className="sheet-btn" onClick={() => { toggleKline(sheetSignal.code); setSheetSignal(null) }}>{klineOpen.has(sheetSignal.code) ? '收起分时' : '展开分时'}</button>
            <button className="sheet-btn sheet-cancel" onClick={() => setSheetSignal(null)}>取消</button>
          </div>
        </div>
      )}

      {showConfirm && (
        <div className="modal-overlay">
          <div className="modal">
            <h3>确认交易</h3>
            <div className="modal-body">
              <p><strong>{tradeTarget.code}</strong> {tradeTarget.name}</p>
              <p>策略: {tradeTarget.strategy}</p>
              <p>总分: {tradeTarget.total_score?.toFixed(0)}</p>
              <p>价格: {tradeTarget.price ? '¥' + tradeTarget.price.toFixed(2) : '—'}</p>
            </div>
            <div className="modal-actions">
              <button className="btn-cancel" onClick={() => setShowConfirm(false)}>取消</button>
              {tradeAction === 'buy' && <button className="btn-buy" onClick={() => doAction('buy')}>确认买入</button>}
              {tradeAction === 'ignore' && <button className="btn-ignore" onClick={() => doAction('ignore')}>忽略</button>}
            </div>
          </div>
        </div>
      )}

      {showLog && (
        <div className="modal-overlay" onClick={() => setShowLog(false)}>
          <div className="modal log-modal" onClick={(e) => e.stopPropagation()}>
            <h3>扫描日志</h3>
            <div className="modal-body">
              {logData && Array.isArray(logData.batches) && logData.batches.length ? (
                <div className="log-section">
                  <h4>信号批次</h4>
                  {logData.batches.map((b, i) => (
                    <div className="log-item" key={i}>{JSON.stringify(b).slice(0, 200)}</div>
                  ))}
                </div>
              ) : (
                <p>暂无日志数据</p>
              )}
            </div>
            <div className="modal-actions">
              <button className="btn-cancel" onClick={() => setShowLog(false)}>关闭</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
