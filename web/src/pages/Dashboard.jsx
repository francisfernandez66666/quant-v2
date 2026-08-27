// ── 仪表盘页面 Dashboard.jsx ──
// 聚合展示首页核心数据：策略信号统计、热门个股、宏观日历、IPO、热门板块、
// 最新资讯、按战法胜率归因、数据源健康与实盘链路状态。
import React, { useState, useEffect, useMemo, useRef } from 'react'
import * as api from '../api/index.js'
import LogModal from '../components/LogModal.jsx'
import './Dashboard.css'

// 将时间戳格式化为相对时间（如 5s前 / 3m前 / 2h前）
function fmtAgo(ts) {
  if (!ts || String(ts).startsWith('0001')) return ''
  const sec = Math.floor((Date.now() - new Date(ts).getTime()) / 1000)
  if (!Number.isFinite(sec) || sec < 0) return ''
  if (sec < 60) return sec + 's前'
  if (sec < 3600) return Math.floor(sec / 60) + 'm前'
  return Math.floor(sec / 3600) + 'h前'
}

// 根据 IPO/上市日期计算倒计时或上市状态
function ipoCountdown(c) {
  const ds = c.listing_date || c.ipo_date
  if (!ds) return c.list_status === 'L' ? '已上市' : '即将上市'
  const t = new Date(+ds.slice(0, 4), +ds.slice(4, 6) - 1, +ds.slice(6, 8))
  const diff = Math.ceil((t - Date.now()) / 86400000)
  if (diff > 0) return `${diff}天后`
  if (diff === 0) return '📌今天'
  return `${-diff}天前`
}

// 将时间戳或 ISO 字符串统一格式化为 MM-DD HH:mm
function fmtNewsTime(dt) {
  if (dt === null || dt === undefined || dt === '') return ''
  const s = String(dt)
  if (/^\d+$/.test(s)) {
    const t = new Date(Number(s) * 1000)
    if (!isNaN(t.getTime())) {
      const mm = String(t.getMonth() + 1).padStart(2, '0')
      const dd = String(t.getDate()).padStart(2, '0')
      const hh = String(t.getHours()).padStart(2, '0')
      const mi = String(t.getMinutes()).padStart(2, '0')
      return `${mm}-${dd} ${hh}:${mi}`
    }
    return ''
  }
  return s.length >= 16 ? s.slice(5, 16) : s
}

// 格式化盈亏比，处理 Infinity、0 与空值
function fmtProfitFactor(pf) {
  if (pf === null || pf === undefined) return '--'
  if (pf === Infinity) return '∞'
  if (pf === 0) return '--'
  return pf.toFixed(2)
}

/**
 * 仪表盘页面组件
 * 聚合展示策略信号、热门个股、宏观日历、IPO、热门板块、数据源与引擎健康等。
 * @returns {JSX.Element}
 */
export default function Dashboard() {
  const [signals, setSignals] = useState([])
  const [status, setStatus] = useState({})
  const [newsItems, setNewsItems] = useState([])
  const [hotSectors, setHotSectors] = useState([])
  const [snapshotStocks, setSnapshotStocks] = useState([])
  const [snapshotTime, setSnapshotTime] = useState('')
  const [ipoCalendar, setIpoCalendar] = useState([])
  const [showLog, setShowLog] = useState(false)
  const [dataSourceHealth, setDataSourceHealth] = useState({})
  const [newsSourceHealth, setNewsSourceHealth] = useState({})
  const [engineHealth, setEngineHealth] = useState({})
  const [strategyStats, setStrategyStats] = useState({})
  const [qmtState, setQmtState] = useState(null)

  const timer = useRef(null)
  const qmtTimer = useRef(null)
  const sseUnsub = useRef(null)
  const visibilityHandler = useRef(null)

  const scanStats = useMemo(() => status.scan_stats || {}, [status])

  const strongCount = useMemo(() => signals.filter((s) => s.remind_level === 'strong').length, [signals])
  const observeCount = useMemo(() => signals.filter((s) => s.remind_level === 'observe').length, [signals])
  const muteCount = useMemo(() => signals.filter((s) => s.remind_level === 'mute').length, [signals])

  const calendarEvents = useMemo(
    () => newsItems.filter((n) => n.source === '宏观日历' || n.source === '政策反制'),
    [newsItems]
  )

  const qmtLine = useMemo(() => {
    const s = qmtState
    if (!s || !s.enabled) return ''
    const parts = []
    parts.push(s.last_probe_ok ? '●' : '○')
    if (s.last_latency_ms > 0) parts.push(s.last_latency_ms + 'ms')
    parts.push(s.mode === 'auto' ? '自动' : '手动')
    parts.push(s.tripped ? '⚠熔断' + (s.trip_reason ? ':' + s.trip_reason : '') : '正常')
    const ra = fmtAgo(s.last_report_at)
    if (ra) parts.push('回报' + ra)
    return parts.join(' ')
  }, [qmtState])

  // 加载实盘/QMT 状态（接口异常不阻断整页）
  async function loadQMT() {
    try { setQmtState(await api.fetchQMTState()) } catch (e) { /* 接口异常不影响整页 */ }
  }

  // 并行加载仪表盘所需的信号、状态、新闻、板块、快照、IPO 与战法统计
  async function load() {
    const [sigRes, stRes, newsRes, secRes, snapRes, ipoRes, dashRes] = await Promise.allSettled([
      api.fetchSignals(), api.fetchStatus(), api.fetchNews(true), api.fetchSectorHot(),
      api.fetchHotSnapshot(), api.fetchIPOCalendar(), api.fetchDashboard(),
    ])
    if (sigRes.status === 'fulfilled' && Array.isArray(sigRes.value)) setSignals(sigRes.value)
    if (stRes.status === 'fulfilled' && stRes.value) setStatus(stRes.value)
    if (newsRes.status === 'fulfilled' && Array.isArray(newsRes.value)) setNewsItems(newsRes.value)
    if (secRes.status === 'fulfilled' && Array.isArray(secRes.value)) setHotSectors(secRes.value)
    if (snapRes.status === 'fulfilled' && Array.isArray(snapRes.value) && snapRes.value.length) {
      setSnapshotStocks(snapRes.value)
      setSnapshotTime(new Date().toLocaleTimeString())
    }
    if (ipoRes.status === 'fulfilled' && Array.isArray(ipoRes.value)) setIpoCalendar(ipoRes.value)
    if (dashRes.status === 'fulfilled' && dashRes.value && dashRes.value.report_stats) {
      setStrategyStats(dashRes.value.report_stats.by_strategy || {})
    }
  }

  // SSE 推送到达时刷新仪表盘数据
  function handleSSE() {
    load()
  }

  // 页面挂载：加载数据、启动定时刷新、订阅 SSE、监听可见性变化；卸载时清理
  useEffect(() => {
    load()
    timer.current = setInterval(load, 5000)
    loadQMT()
    qmtTimer.current = setInterval(loadQMT, 15000)
    api.connectSSE()
    sseUnsub.current = api.onSSE(handleSSE)
    visibilityHandler.current = () => {
      if (document.hidden) {
        if (timer.current) { clearInterval(timer.current); timer.current = null }
        if (qmtTimer.current) { clearInterval(qmtTimer.current); qmtTimer.current = null }
      } else {
        if (!timer.current) {
          load()
          timer.current = setInterval(load, 5000)
        }
        if (!qmtTimer.current) {
          loadQMT()
          qmtTimer.current = setInterval(loadQMT, 15000)
        }
      }
    }
    document.addEventListener('visibilitychange', visibilityHandler.current)
    api.fetchDataSourceHealth().then((r) => setDataSourceHealth(r)).catch(() => {})
    api.fetchNewsSourceHealth().then((r) => setNewsSourceHealth(r)).catch(() => {})
    api.fetchEngineHealth().then((r) => setEngineHealth((r && r.engine) || r || {})).catch(() => {})
    return () => {
      if (timer.current) clearInterval(timer.current)
      if (qmtTimer.current) clearInterval(qmtTimer.current)
      if (visibilityHandler.current) document.removeEventListener('visibilitychange', visibilityHandler.current)
      if (sseUnsub.current) sseUnsub.current()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="dashboard">
      <button className="btn-log" onClick={() => setShowLog(true)}>📋 日志</button>
      <LogModal visible={showLog} onClose={() => setShowLog(false)} />

      <div className="stats-row">
        <div className="stat-card strong">
          <div className="stat-num">{strongCount}</div>
          <div className="stat-label">强信号</div>
        </div>
        <div className="stat-card observe">
          <div className="stat-num">{observeCount}</div>
          <div className="stat-label">观察中</div>
        </div>
        <div className="stat-card mute">
          <div className="stat-num">{muteCount}</div>
          <div className="stat-label">静默</div>
        </div>
        <div className="stat-card holding">
          <div className="stat-num">{scanStats.total_stocks || snapshotStocks.length || 0}</div>
          <div className="stat-label">监控个股</div>
        </div>
      </div>

      <div className="grid-2col">
        <div className="card">
          <div className="card-header">
            <span>🔥 热门个股 <span className="badge-live">LIVE</span></span>
            <span className="card-sub">{snapshotTime}</span>
          </div>
          {snapshotStocks.length ? (
            <div className="stock-table">
              <div className="st-header">
                <span className="st-code">代码</span>
                <span className="st-name">名称</span>
                <span className="st-sector">板块</span>
                <span className="st-price">现价</span>
                <span className="st-chg">涨跌</span>
              </div>
              <div className="st-body">
                {snapshotStocks.map((s) => (
                  <div key={s.code} className="st-row">
                    <span className="st-code">{s.code}</span>
                    <span className="st-name">{s.name}</span>
                    <span className="st-sector" title={s.sector_reason || ''}>{s.sector || '—'}</span>
                    <span className="st-price">¥{(s.price || 0).toFixed(2)}</span>
                    <span className={'st-chg ' + ((s.change_pct || 0) >= 0 ? 'up' : 'down')}>
                      {(s.change_pct || 0) > 0 ? '+' : ''}{(s.change_pct || 0).toFixed(2)}%
                    </span>
                  </div>
                ))}
              </div>
            </div>
          ) : (
            <div className="empty"><span className="loading-dot"></span> 等待行情数据...</div>
          )}
        </div>

        <div className="card">
          <div className="card-header">
            <span>最新动态</span>
            <span className="card-sub">{newsItems.length + hotSectors.length}条</span>
          </div>

          <div className="cal-section">
            <div className="section-label">📅 宏观日历</div>
            <div className="cal-scroll">
              {calendarEvents.map((c, i) => (
                <div key={'c' + i} className="cal-item">
                  <span className="cal-date">{c.datetime ? c.datetime.slice(5, 10) : ''}</span>
                  <span className="cal-title">{c.title}</span>
                </div>
              ))}
              {!calendarEvents.length && <div className="cal-empty">暂无日历事件</div>}
            </div>
          </div>

          <div className="section-divider"></div>

          <div className="cal-section">
            <div className="section-label">📋 IPO日历</div>
            <div className="cal-scroll">
              {ipoCalendar.map((c, i) => (
                <div key={'ipo' + i} className="cal-item">
                  <span className="cal-date">{c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : '')}</span>
                  <span className="cal-title">{c.name}（{c.code}）</span>
                  {c.issue_price && <span className="cal-price">¥{c.issue_price.toFixed(2)}</span>}
                  <span className={'cal-status ' + (c.list_status === 'L' ? 'cal-status-l' : 'cal-status-u')}>{ipoCountdown(c)}</span>
                </div>
              ))}
              {!ipoCalendar.length && <div className="cal-empty">暂无IPO日历</div>}
            </div>
          </div>

          <div className="section-divider"></div>

          {hotSectors.length > 0 && <div className="section-label">🔥 热门板块</div>}
          {hotSectors.length > 0 && (
            <div className="sec-scroll">
              {hotSectors.slice(0, 5).map((s, i) => (
                <div key={'s' + i} className="sec-row">
                  <span className="sec-pct">{(s.change_pct || 0) > 0 ? '+' : ''}{(s.change_pct || 0).toFixed(1)}%</span>
                  <span className="sec-name">{s.name}</span>
                  <span className="sec-inflow">净流入 {s.net_inflow ? (s.net_inflow / 1e8).toFixed(1) + '亿' : '—'}</span>
                </div>
              ))}
            </div>
          )}

          <div className="section-divider"></div>

          {newsItems.length > 0 && <div className="section-label">📰 资讯</div>}
          <div className="news-scroll">
            {newsItems.slice(0, 15).map((n, i) => (
              <div key={'n' + i} className="news-row">
                <div className="news-head">
                  <span className="news-time">{fmtNewsTime(n.datetime)}</span>
                  <span className="news-title-text">{n.title}</span>
                </div>
                <div className="news-tags-line">
                  {n.direction && (
                    <span className={'tag ' + (n.direction === '利好' ? 'tag-up' : n.direction === '利空' ? 'tag-down' : 'tag-neutral')}>{n.direction}</span>
                  )}
                  {n.impact_level && <span className={'tag tag-impact-' + n.impact_level}>{n.impact_level}影响</span>}
                  {n.sectors?.length && n.sectors.map((sec) => <span key={sec} className="sector-tag">{sec}</span>)}
                  {n.stocks?.length && n.stocks.map((stk) => <span key={stk} className="stock-tag">{stk}</span>)}
                </div>
              </div>
            ))}
          </div>
          {!newsItems.length && !hotSectors.length && !calendarEvents.length && (
            <div className="empty"><span className="loading-dot"></span> 等待数据...</div>
          )}
        </div>
      </div>

      {strategyStats && Object.keys(strategyStats).length > 0 && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="card-header">按战法胜率</div>
          <div className="strategy-table">
            <div className="stg-header">
              <span className="stg-strategy">战法</span>
              <span className="stg-num">样本</span>
              <span className="stg-num">已平仓</span>
              <span className="stg-num">胜率</span>
              <span className="stg-num">平均盈</span>
              <span className="stg-num">平均亏</span>
              <span className="stg-num">盈亏比</span>
              <span className="stg-num">持仓中</span>
            </div>
            {Object.entries(strategyStats).map(([name, s]) => (
              <div key={name} className="stg-row">
                <span className="stg-strategy">{s.strategy || name}</span>
                <span className="stg-num">{s.total}</span>
                <span className="stg-num">{s.closed}</span>
                <span className={'stg-num ' + (s.win_rate >= 50 ? 'up' : 'down')}>{(s.win_rate || 0).toFixed(1)}%</span>
                <span className="stg-num up">{(s.avg_win_pct || 0).toFixed(1)}%</span>
                <span className="stg-num down">{(s.avg_loss_pct || 0).toFixed(1)}%</span>
                <span className="stg-num">{fmtProfitFactor(s.profit_factor)}</span>
                <span className="stg-num">{s.holding}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="card" style={{ marginTop: 16 }}>
        <div className="card-header">系统</div>
        <div className="status-row-inline">
          <span>运行 {status.uptime || '-'}</span>
          <span>数据源：东财{dataSourceHealth.eastmoney ? '●' : '○'} 新浪{dataSourceHealth.sina ? '●' : '○'} 腾讯{dataSourceHealth.tencent ? '●' : '○'} 同花顺{dataSourceHealth.ths ? '●' : '○'}</span>
          <span>新闻：财联社{newsSourceHealth.cainanshe ? '●' : '○'} 同花顺{newsSourceHealth.kuaixun ? '●' : '○'} 新浪{newsSourceHealth.sina ? '●' : '○'}</span>
          <span>快照 {scanStats.total_stocks || 0}股 / {scanStats.hot_sector_count || 0}板块</span>
          <span>原始 {scanStats.raw_signals || 0} → 最终 {scanStats.final_signals || 0}</span>
          <span>流程引擎：新闻抓取{engineHealth.news_agent ? '●' : '○'} 策略引擎{engineHealth.strategy_engine ? '●' : '○'} 板块验证{engineHealth.sector_agent ? '●' : '○'} 战法扫描{engineHealth.combat_agent ? '●' : '○'} LLM{engineHealth.llm ? '●' : '○'} 同花顺{engineHealth.ths ? '●' : '○'} 聚合器{engineHealth.aggregator ? '●' : '○'}</span>
          {qmtLine && <span>实盘链路：{qmtLine}</span>}
        </div>
      </div>
    </div>
  )
}
