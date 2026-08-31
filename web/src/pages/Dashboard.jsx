// ── 仪表盘页面 Dashboard.jsx ──
// 聚合展示首页核心数据：策略信号统计、热门个股、宏观日历、IPO、热门板块、
// 最新资讯、按战法胜率归因、数据源健康与实盘链路状态。
// 使用 TDesign React 组件（Card / Table / Tag / Button / Dialog）。
import React, { useState, useEffect, useMemo, useRef } from 'react'
import { Card, Table, Tag, Button } from 'tdesign-react'
import * as api from '../api/index.js'
import LogModal from '../components/LogModal.jsx'

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

// 涨跌百分比配色（红涨绿跌）
function chgColor(v) {
  return (v || 0) >= 0 ? '#e34d59' : '#00a870'
}

/**
 * 仪表盘页面组件
 * 聚合展示策略信号、热门个股、宏观日历、IPO、热门板块、数据源与引擎健康等。
 * @returns {JSX.Element}
 */
export default function Dashboard() {
  // 策略信号列表（来自后端扫描结果）
  const [signals, setSignals] = useState([])
  // 引擎/扫描整体状态（含 uptime、scan_stats 等）
  const [status, setStatus] = useState({})
  // 最新资讯与宏观日历事件
  const [newsItems, setNewsItems] = useState([])
  // 热门板块列表
  const [hotSectors, setHotSectors] = useState([])
  // 热门个股实时快照
  const [snapshotStocks, setSnapshotStocks] = useState([])
  // 快照拉取时刻（仅用于展示）
  const [snapshotTime, setSnapshotTime] = useState('')
  // IPO / 上市日历
  const [ipoCalendar, setIpoCalendar] = useState([])
  // 日志弹窗显隐
  const [showLog, setShowLog] = useState(false)
  // 各行情数据源健康状态（东财/新浪/腾讯/同花顺）
  const [dataSourceHealth, setDataSourceHealth] = useState({})
  // 各新闻数据源健康状态（财联社/同花顺/新浪）
  const [newsSourceHealth, setNewsSourceHealth] = useState({})
  // 流程引擎各模块健康状态
  const [engineHealth, setEngineHealth] = useState({})
  // 按战法统计的胜率/盈亏比等归因数据
  const [strategyStats, setStrategyStats] = useState({})
  // 实盘/QMT 链路状态
  const [qmtState, setQmtState] = useState(null)

  // 主数据刷新定时器（每 5s 轮询）
  const timer = useRef(null)
  // QMT 状态刷新定时器（每 15s 轮询）
  const qmtTimer = useRef(null)
  // SSE 订阅取消函数
  const sseUnsub = useRef(null)
  // 页面可见性变化处理函数引用
  const visibilityHandler = useRef(null)

  const scanStats = useMemo(() => status.scan_stats || {}, [status])

  const strongCount = useMemo(() => signals.filter((s) => s.remind_level === 'strong').length, [signals])
  const observeCount = useMemo(() => signals.filter((s) => s.remind_level === 'observe').length, [signals])
  const muteCount = useMemo(() => signals.filter((s) => s.remind_level === 'mute').length, [signals])

  const calendarEvents = useMemo(
    () => newsItems.filter((n) => n.source === '宏观日历' || n.source === '政策反制'),
    [newsItems]
  )

  const strategyRows = useMemo(
    () => Object.entries(strategyStats || {}).map(([name, s]) => ({ name, ...s })),
    [strategyStats]
  )

  const qmtLine = useMemo(() => {
    const s = qmtState
    if (!s || !s.enabled) return ''
    const parts = []
    parts.push(s.last_probe_ok ? '●' : '○')
    parts.push(s.mode === 'auto' ? '自动' : '手动')
    parts.push(s.tripped ? '⚠熔断' + (s.trip_reason ? ':' + s.trip_reason : '') : '正常')
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
    // 主数据每 5s 轮询刷新
    timer.current = setInterval(load, 5000)
    loadQMT()
    // QMT 链路状态每 15s 轮询刷新
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
          // 恢复页面后重启主数据 5s 轮询
          timer.current = setInterval(load, 5000)
        }
        if (!qmtTimer.current) {
          loadQMT()
          // 恢复页面后重启 QMT 15s 轮询
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

  // 热点个股表格列定义：代码、名称、板块（含原因 title 悬浮）、现价、涨跌幅
  const stockColumns = [
    { colKey: 'code', title: '代码', width: 90 },
    { colKey: 'name', title: '名称', width: 100 },
    { colKey: 'sector', title: '板块', ellipsis: true, cell: ({ row }) => <span title={row.sector_reason || ''}>{row.sector || '—'}</span> },
    { colKey: 'price', title: '现价', width: 90, cell: ({ row }) => '¥' + (row.price || 0).toFixed(2) },
    { colKey: 'change_pct', title: '涨跌', width: 100, cell: ({ row }) => (
      <span style={{ color: chgColor(row.change_pct) }}>{(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%</span>
    ) },
  ]

  // 按战法归因表格列定义：战法、样本数、已平仓、胜率、平均盈亏、盈亏比、持仓中
  const strategyColumns = [
    { colKey: 'strategy', title: '战法', width: 140, cell: ({ row }) => row.strategy || row.name },
    { colKey: 'total', title: '样本', width: 70 },
    { colKey: 'closed', title: '已平仓', width: 80 },
    { colKey: 'win_rate', title: '胜率', width: 80, cell: ({ row }) => (
      <span style={{ color: (row.win_rate || 0) >= 50 ? '#e34d59' : '#00a870' }}>{(row.win_rate || 0).toFixed(1)}%</span>
    ) },
    { colKey: 'avg_win_pct', title: '平均盈', width: 80, cell: ({ row }) => <span style={{ color: '#e34d59' }}>{(row.avg_win_pct || 0).toFixed(1)}%</span> },
    { colKey: 'avg_loss_pct', title: '平均亏', width: 80, cell: ({ row }) => <span style={{ color: '#00a870' }}>{(row.avg_loss_pct || 0).toFixed(1)}%</span> },
    { colKey: 'profit_factor', title: '盈亏比', width: 80, cell: ({ row }) => fmtProfitFactor(row.profit_factor) },
    { colKey: 'holding', title: '持仓中', width: 70 },
  ]

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <Button theme="default" variant="outline" onClick={() => setShowLog(true)}>📋 日志</Button>
      </div>

      <div style={{ display: 'flex', gap: 12, marginBottom: 16, flexWrap: 'wrap' }}>
        {[
          { n: strongCount, l: '强信号', c: '#e34d59' },
          { n: observeCount, l: '观察中', c: '#faad14' },
          { n: muteCount, l: '静默', c: '#888' },
          { n: (scanStats.total_stocks || snapshotStocks.length || 0), l: '监控个股', c: '#0052d9' },
        ].map((s) => (
          <Card key={s.l} style={{ flex: '1 1 150px' }}>
            <div style={{ fontSize: 28, fontWeight: 700, color: s.c }}>{s.n}</div>
            <div className="muted">{s.l}</div>
          </Card>
        ))}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginBottom: 16 }}>
        <Card title={<span>🔥 热门个股 <Tag theme="warning" size="small">LIVE</Tag></span>}>
          {snapshotStocks.length ? (
            <Table data={snapshotStocks} columns={stockColumns} rowKey="code" size="small" pagination={false} />
          ) : (
            <div className="muted" style={{ padding: 24, textAlign: 'center' }}>等待行情数据...</div>
          )}
          <div className="muted" style={{ marginTop: 8 }}>{snapshotTime}</div>
        </Card>

        <Card title="最新动态">
          <SectionLabel>📅 宏观日历</SectionLabel>
          {calendarEvents.length ? calendarEvents.map((c, i) => (
            <div key={'c' + i} style={{ display: 'flex', gap: 10, padding: '4px 0', fontSize: 13 }}>
              <span className="muted">{c.datetime ? c.datetime.slice(5, 10) : ''}</span>
              <span>{c.title}</span>
            </div>
          )) : <div className="muted">暂无日历事件</div>}

          <Divider />

          <SectionLabel>📋 IPO日历</SectionLabel>
          {ipoCalendar.length ? ipoCalendar.map((c, i) => (
            <div key={'ipo' + i} style={{ display: 'flex', gap: 8, padding: '4px 0', fontSize: 13, alignItems: 'center' }}>
              <span className="muted">{c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : '')}</span>
              <span>{c.name}（{c.code}）</span>
              {c.issue_price && <span>¥{c.issue_price.toFixed(2)}</span>}
              <Tag size="small" theme={c.list_status === 'L' ? 'success' : 'default'} style={{ marginLeft: 'auto' }}>{ipoCountdown(c)}</Tag>
            </div>
          )) : <div className="muted">暂无IPO日历</div>}

          <Divider />

          {hotSectors.length > 0 && <SectionLabel>🔥 热门板块</SectionLabel>}
          {hotSectors.length > 0 && hotSectors.slice(0, 5).map((s, i) => (
            <div key={'s' + i} style={{ display: 'flex', gap: 10, padding: '4px 0', fontSize: 13, alignItems: 'center' }}>
              <span style={{ color: chgColor(s.change_pct), width: 64 }}>{(s.change_pct || 0) > 0 ? '+' : ''}{(s.change_pct || 0).toFixed(1)}%</span>
              <span>{s.name}</span>
              <span className="muted" style={{ marginLeft: 'auto' }}>净流入 {s.net_inflow ? (s.net_inflow / 1e8).toFixed(1) + '亿' : '—'}</span>
            </div>
          ))}

          <Divider />

          {newsItems.length > 0 && <SectionLabel>📰 资讯</SectionLabel>}
          {newsItems.slice(0, 15).map((n, i) => (
            <div key={'n' + i} style={{ padding: '6px 0', borderBottom: '1px solid #e7e7e7' }}>
              <div style={{ display: 'flex', gap: 8, fontSize: 13 }}>
                <span className="muted">{fmtNewsTime(n.datetime)}</span>
                <span>{n.title}</span>
              </div>
              <div style={{ display: 'flex', gap: 6, marginTop: 4, flexWrap: 'wrap' }}>
                {n.direction && <Tag theme={n.direction === '利好' ? 'success' : n.direction === '利空' ? 'danger' : 'default'} size="small">{n.direction}</Tag>}
                {n.impact_level && <Tag size="small" variant="light">{n.impact_level}影响</Tag>}
                {n.sectors?.length && n.sectors.map((sec) => <Tag key={sec} size="small" theme="primary" variant="light">{sec}</Tag>)}
                {n.stocks?.length && n.stocks.map((stk) => <Tag key={stk} size="small" theme="warning" variant="light">{stk}</Tag>)}
              </div>
            </div>
          ))}
          {!newsItems.length && !hotSectors.length && !calendarEvents.length && (
            <div className="muted" style={{ padding: 16, textAlign: 'center' }}>等待数据...</div>
          )}
        </Card>
      </div>

      {strategyRows.length > 0 && (
        <Card title="按战法胜率" style={{ marginBottom: 16 }}>
          <Table data={strategyRows} columns={strategyColumns} rowKey="name" size="small" pagination={false} />
        </Card>
      )}

      <Card title="系统">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 13 }}>
          <span>运行 {status.uptime || '-'}</span>
          <span>数据源：东财{dataSourceHealth.eastmoney ? '●' : '○'} 新浪{dataSourceHealth.sina ? '●' : '○'} 腾讯{dataSourceHealth.tencent ? '●' : '○'} 同花顺{dataSourceHealth.ths ? '●' : '○'}</span>
          <span>新闻：财联社{newsSourceHealth.cainanshe ? '●' : '○'} 同花顺{newsSourceHealth.kuaixun ? '●' : '○'} 新浪{newsSourceHealth.sina ? '●' : '○'}</span>
          <span>快照 {scanStats.total_stocks || 0}股 / {scanStats.hot_sector_count || 0}板块</span>
          <span>原始 {scanStats.raw_signals || 0} → 最终 {scanStats.final_signals || 0}</span>
          <span>流程引擎：新闻抓取{engineHealth.news_agent ? '●' : '○'} 策略引擎{engineHealth.strategy_engine ? '●' : '○'} 板块验证{engineHealth.sector_agent ? '●' : '○'} 战法扫描{engineHealth.combat_agent ? '●' : '○'} LLM{engineHealth.llm ? '●' : '○'} 同花顺{engineHealth.ths ? '●' : '○'} 聚合器{engineHealth.aggregator ? '●' : '○'}</span>
          {qmtLine && <span>实盘链路：{qmtLine}</span>}
        </div>
      </Card>

      <LogModal visible={showLog} onClose={() => setShowLog(false)} />
    </div>
  )
}

// 板块小标题
function SectionLabel({ children }) {
  return <div style={{ fontWeight: 600, margin: '8px 0 4px', fontSize: 13 }}>{children}</div>
}
// 分隔线
function Divider() {
  return <div style={{ height: 1, background: '#e7e7e7', margin: '10px 0' }} />
}
