// ── 热点页面 Hotspot.jsx ──
// 展示热点板块（含异动原因弹窗）、全市场个股评分排名、宏观日历、IPO日历、热点资讯。
// 全面使用 TDesign 组件：Card / Table / Dialog / Tag / Button。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Card, Table, Dialog, Tag, Button, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import { fetchSignalLogs, fetchStageRecords } from '../api/index.js'
import './Hotspot.css'

// ── 工具函数 ──
// 截断异动原因为简短描述
function shortReason(r) {
  if (!r) return ''
  const idx = r.indexOf('，')
  return idx > 0 ? r.slice(0, idx) : r.slice(0, 18)
}

// 根据分数与阈值返回评分单元格 CSS 类
function scoreClass(score, pass, strongMin) {
  if (!score || score <= 0) return 'ev-score'
  if (score >= strongMin) return 'ev-score strong'
  if (pass) return 'ev-score pass'
  return 'ev-score'
}
function scoreColor(cls) {
  if (cls.indexOf('strong') >= 0) return '#e34d59'
  if (cls.indexOf('pass') >= 0) return '#FAAD14'
  return '#555'
}

// 根据多维度评分决定个股行高亮样式
function rowClass(e) {
  const strong = (e.n_score || 0) >= 80 || (e.dragon_score || 0) >= 80 || (e.db_score || 0) >= 80 || (e.dr_score || 0) >= 80 || (e.m_score || 0) >= 70
  if (strong) return 'ev-row strong'
  const watch = (e.n_score || 0) >= 60 || (e.dragon_score || 0) >= 60 || (e.db_score || 0) >= 60 || (e.dr_score || 0) >= 60 || (e.m_score || 0) >= 50
  if (watch) return 'ev-row watch'
  return 'ev-row'
}

// 安全读取字段值
function val(e, key) {
  const v = e[key]
  if (typeof v === 'string') return v || ''
  return v || 0
}

/**
 * 热点页面组件
 * 展示热点板块、全市场个股评分排名、宏观日历、IPO 日历与热点资讯。
 * @returns {JSX.Element}
 */
export default function Hotspot() {
  const [sectors, setSectors] = useState([])
  const [evals, setEvals] = useState([])
  const [news, setNews] = useState([])
  const [ipoCalendar, setIpoCalendar] = useState([])
  const [reasonTarget, setReasonTarget] = useState(null)
  const [showLog, setShowLog] = useState(false)
  const [logSignal, setLogSignal] = useState('')
  const [logStage, setLogStage] = useState('')
  const [reanalyzing, setReanalyzing] = useState(false)

  const [sortKey, setSortKey] = useState('')
  const [sortDir, setSortDir] = useState(-1)

  const loadingRef = useRef(false)
  const timerRef = useRef(null)
  const unsubSSERef = useRef(null)
  const visHandlerRef = useRef(null)

  // 用 ref 保存最新评分数据，避免轮询闭包引用旧值
  const evalsRef = useRef([])
  useEffect(() => { evalsRef.current = evals }, [evals])

  // 切换排序键或方向
  function setSort(key) {
    if (sortKey === key) setSortDir((d) => d * -1)
    else { setSortKey(key); setSortDir(-1) }
  }
  // 返回当前排序方向的箭头字符
  function sortArrow(key) {
    if (sortKey !== key) return ''
    return sortDir === -1 ? ' ▼' : ' ▲'
  }

  // 过滤非日历类资讯
  const newsItems = useMemo(() => news.filter((n) => n.source !== '宏观日历' && n.source !== '政策反制'), [news])
  // 过滤宏观日历与政策反制事件
  const calendarEvents = useMemo(() => news.filter((n) => n.source === '宏观日历' || n.source === '政策反制'), [news])

  // 根据上市日期计算 IPO 倒计时
  function ipoCountdown(c) {
    const ds = c.listing_date || c.ipo_date
    if (!ds) return c.list_status === 'L' ? '已上市' : '即将上市'
    const t = new Date(+ds.slice(0, 4), +ds.slice(4, 6) - 1, +ds.slice(6, 8))
    const diff = Math.ceil((t - Date.now()) / 86400000)
    if (diff > 0) return `${diff}天后`
    if (diff === 0) return '📌今天'
    return `${-diff}天前`
  }

  // 将时间戳或字符串统一格式化为 MM-DD HH:mm
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

  // 按排序键计算展示列表，默认按最高维度分倒序
  const sortedEvals = useMemo(() => {
    if (!evals || !evals.length) return []
    const arr = [...evals]
    const sk = sortKey
    if (!sk) {
      return arr.sort((a, b) => {
        const sa = Math.max(a.n_score || 0, a.dragon_score || 0, a.db_score || 0, a.dr_score || 0, a.m_score || 0)
        const sb = Math.max(b.n_score || 0, b.dragon_score || 0, b.db_score || 0, b.dr_score || 0, b.m_score || 0)
        return sb - sa
      })
    }
    const dir = sortDir
    return arr.sort((a, b) => {
      const va = val(a, sk)
      const vb = val(b, sk)
      if (typeof va === 'string') return va.localeCompare(vb) * dir
      return (va - vb) * dir
    })
  }, [evals, sortKey, sortDir])

  // 加载热点板块、个股评分、新闻与 IPO 日历
  async function load() {
    if (loadingRef.current) return
    loadingRef.current = true
    try {
      try {
        const st = await api.fetchStatus()
        api.setLastSession(st.session)
        if (api.isTradingSession(st.session) || !evalsRef.current.length) {
          try {
            const e = await api.fetchEvaluations()
            if (e) setEvals(e)
          } catch (_) {}
        }
      } catch (_) {}
      let fromRecords = false
      try {
        const recs = await api.fetchSectorHotRecords()
        if (Array.isArray(recs) && recs.length) {
          setSectors(recs[0].sectors || [])
          fromRecords = true
        }
      } catch (_) {}
      if (!fromRecords) {
        try {
          const s = await api.fetchSectorHot()
          if (s) setSectors(s)
        } catch (_) {}
      }
      try {
        const n = await api.fetchNews(true)
        if (n) setNews(n)
      } catch (_) {}
      try {
        const ipo = await api.fetchIPOCalendar()
        if (ipo) setIpoCalendar(ipo)
      } catch (_) {}
    } finally {
      loadingRef.current = false
    }
  }

  // SSE 推送到达时刷新热点数据
  function handleSSE() { load() }

  // 触发新闻重新分析
  async function onReanalyze() {
    if (reanalyzing) return
    setReanalyzing(true)
    try {
      const res = await api.reanalyzeNews()
      if (res && res.accepted !== false) {
        setTimeout(() => load(), 1500)
      }
    } catch (_) {} finally {
      setReanalyzing(false)
    }
  }

  // 打开日志弹窗并加载信号与 Stage 日志
  async function openLog() {
    setShowLog(true)
    try {
      const sl = await fetchSignalLogs()
      setLogSignal(typeof sl === 'string' ? sl : JSON.stringify(sl, null, 2))
    } catch (_) { setLogSignal('') }
    try {
      const st = await fetchStageRecords()
      setLogStage(typeof st === 'string' ? st : JSON.stringify(st, null, 2))
    } catch (_) { setLogStage('') }
  }

  // 挂载时加载数据、启动轮询与 SSE；处理页面可见性变化；卸载时清理
  useEffect(() => {
    load()
    timerRef.current = setInterval(load, 5000)
    visHandlerRef.current = () => {
      if (document.hidden) {
        if (timerRef.current) { clearInterval(timerRef.current); timerRef.current = null }
      } else {
        if (!timerRef.current) {
          load()
          timerRef.current = setInterval(load, 5000)
        }
      }
    }
    document.addEventListener('visibilitychange', visHandlerRef.current)
    api.connectSSE()
    unsubSSERef.current = api.onSSE(handleSSE)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
      if (visHandlerRef.current) document.removeEventListener('visibilitychange', visHandlerRef.current)
      if (unsubSSERef.current) { unsubSSERef.current(); unsubSSERef.current = null }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 板块卡片网格列
  const sectorColumns = [
    { colKey: 'name', title: '板块', width: 120 },
    { colKey: 'reason', title: '异动原因', width: 200, ellipsis: true, cell: ({ row }) => row.reason ? <span style={{ color: '#4fc3f7' }}>{shortReason(row.reason)}</span> : null },
    { colKey: 'score', title: '评分', width: 80, cell: ({ row }) => <span style={{ color: '#FAAD14' }}>{Math.round((row.score || 0) * 100)}分</span> },
    { colKey: 'change_pct', title: '涨幅', width: 90, cell: ({ row }) => <span style={{ color: (row.change_pct || 0) >= 0 ? '#e34d59' : '#00a870', fontWeight: 700 }}>{(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%</span> },
    { colKey: 'meta', title: '涨停/流入', width: 180, cell: ({ row }) => (
      <span style={{ color: '#666', fontSize: 13 }}>
        {row.d1 > 0 && <Tag size="small" style={{ marginRight: 6 }}>D1 {row.d1.toFixed(0)}</Tag>}
        <span>涨停 {row.limitup_cnt || 0}</span>
        <span style={{ marginLeft: 8 }}>流入 {row.net_inflow ? (row.net_inflow / 1e8).toFixed(1) + '亿' : '—'}</span>
      </span>
    ) },
  ]

  // 个股评分排名列
  const evalColumns = [
    { colKey: 'code', title: '代码', width: 90, sorter: (a, b) => (a.code || '').localeCompare(b.code || ''), cell: ({ row }) => <span style={{ color: '#4fc3f7', fontFamily: 'monospace' }}>{row.code}</span> },
    { colKey: 'name', title: '名称', width: 90, sorter: (a, b) => (a.name || '').localeCompare(b.name || ''), cell: ({ row }) => <span style={{ color: '#ccc' }}>{row.name || '-'}</span> },
    { colKey: 'price', title: '现价', width: 90, sorter: (a, b) => (a.price || 0) - (b.price || 0), cell: ({ row }) => '¥' + (row.price || 0).toFixed(2) },
    { colKey: 'change_pct', title: '涨跌', width: 100, sorter: (a, b) => (a.change_pct || 0) - (b.change_pct || 0), cell: ({ row }) => <span style={{ color: (row.change_pct || 0) >= 0 ? '#e34d59' : '#00a870', fontWeight: 600 }}>{(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%</span> },
    { colKey: 'n_score', title: 'N≥60', width: 70, sorter: (a, b) => (a.n_score || 0) - (b.n_score || 0), cell: ({ row }) => { const c = scoreClass(row.n_score, row.n_score >= 60, 80); return <span style={{ color: scoreColor(c), fontWeight: 600 }}>{row.n_score > 0 ? row.n_score.toFixed(0) : '—'}</span> } },
    { colKey: 'dragon_score', title: '龙≥60', width: 70, sorter: (a, b) => (a.dragon_score || 0) - (b.dragon_score || 0), cell: ({ row }) => { const c = scoreClass(row.dragon_score, row.dragon_score >= 60, 80); return <span style={{ color: scoreColor(c), fontWeight: 600 }}>{row.dragon_score > 0 ? row.dragon_score.toFixed(0) : '—'}</span> } },
    { colKey: 'db_score', title: '凸≥60', width: 70, sorter: (a, b) => (a.db_score || 0) - (b.db_score || 0), cell: ({ row }) => { const c = scoreClass(row.db_score, row.db_score >= 60, 80); return <span style={{ color: scoreColor(c), fontWeight: 600 }}>{row.db_score > 0 ? row.db_score.toFixed(0) : '—'}</span> } },
    { colKey: 'dr_score', title: '回≥60', width: 70, sorter: (a, b) => (a.dr_score || 0) - (b.dr_score || 0), cell: ({ row }) => { const c = scoreClass(row.dr_score, row.dr_score >= 60, 80); return <span style={{ color: scoreColor(c), fontWeight: 600 }}>{row.dr_score > 0 ? row.dr_score.toFixed(0) : '—'}</span> } },
    { colKey: 'm_score', title: '量≥50', width: 70, sorter: (a, b) => (a.m_score || 0) - (b.m_score || 0), cell: ({ row }) => { const c = scoreClass(row.m_score, row.m_score >= 50, 70); return <span style={{ color: scoreColor(c), fontWeight: 600 }}>{row.m_score > 0 ? row.m_score.toFixed(0) : '—'}</span> } },
  ]

  // 宏观日历列
  const calendarColumns = [
    { colKey: 'date', title: '日期', width: 90, cell: ({ row }) => <span style={{ color: '#888' }}>{row.date}</span> },
    { colKey: 'title', title: '事件', cell: ({ row }) => <span style={{ color: '#e0e0e0' }}>{row.title}</span> },
  ]
  const calendarData = calendarEvents.map((c, i) => ({ id: 'c' + i, date: c.datetime ? c.datetime.slice(5, 10) : '', title: c.title }))

  // IPO 日历列
  const ipoColumns = [
    { colKey: 'date', title: '日期', width: 90, cell: ({ row }) => <span style={{ color: '#888' }}>{row.date}</span> },
    { colKey: 'name', title: '名称', width: 160, cell: ({ row }) => <span style={{ color: '#e0e0e0' }}>{row.name}（{row.code}）</span> },
    { colKey: 'price', title: '发行价', width: 90, cell: ({ row }) => row.issue_price ? <span style={{ color: '#4fc3f7' }}>¥{row.issue_price.toFixed(2)}</span> : null },
    { colKey: 'status', title: '状态', width: 100, cell: ({ row }) => <Tag size="small" theme={row.statusTheme}>{row.status}</Tag> },
  ]
  const ipoData = ipoCalendar.map((c, i) => ({ id: 'ipo' + i, date: c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : ''), name: c.name, code: c.code, issue_price: c.issue_price, status: ipoCountdown(c), statusTheme: c.list_status === 'L' ? 'default' : 'warning' }))

  // 资讯标签解析
  function newsTags(n) {
    const tags = []
    if (n.sentiment) { const theme = n.sentiment === '正面' ? 'success' : n.sentiment === '负面' ? 'danger' : 'default'; tags.push({ text: n.sentiment, theme }) }
    if (n.direction) { const theme = n.direction === '利好' ? 'success' : n.direction === '利空' ? 'danger' : 'default'; tags.push({ text: n.direction, theme }) }
    if (n.impact_level) { const theme = n.impact_level === '高' ? 'warning' : n.impact_level === '中' ? 'primary' : 'default'; tags.push({ text: n.impact_level + '影响', theme }) }
    return tags
  }
  // 资讯列
  const newsColumns = [
    { colKey: 'time', title: '时间', width: 100, cell: ({ row }) => <span style={{ color: '#888' }}>{row.time}</span> },
    { colKey: 'title', title: '标题', cell: ({ row }) => <span style={{ color: '#ccc' }}>{row.title}</span> },
    { colKey: 'tags', title: '标签', width: 280, cell: ({ row }) => (
      <span style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
        {row.tags.map((t, i) => <Tag key={i} size="small" theme={t.theme}>{t.text}</Tag>)}
        {(row.sectors || []).map((sec) => <Tag key={'s' + sec} size="small" theme="primary" variant="light">{sec}</Tag>)}
        {(row.stocks || []).map((stk) => <Tag key={'k' + stk} size="small" theme="warning" variant="light">{stk}</Tag>)}
      </span>
    ) },
  ]
  const newsData = newsItems.map((n, i) => ({ id: 'n' + i, time: fmtNewsTime(n.datetime), title: n.title, tags: newsTags(n), sectors: n.sectors, stocks: n.stocks }))

  return (
    <div className="hotspot-page">
      <Card>
        <div className="card-header" style={{ display: 'flex', justifyContent: 'space-between' }}>
          <span>🔥 热点板块</span>
          <Button size="small" variant="outline" theme="primary" onClick={openLog}>📋 日志</Button>
        </div>
        {sectors.length ? (
          <div className="sector-grid">
            {sectors.map((s) => (
              <Card key={s.code} className="sector-card" bordered={false} style={{ cursor: 'pointer' }} onClick={() => setReasonTarget(s)}>
                <div className="sec-name">{s.name}</div>
                {s.reason && <div className="sec-reason">{shortReason(s.reason)}</div>}
                <div className="sec-score">{Math.round((s.score || 0) * 100)}分</div>
                <div className={['sec-pct', (s.change_pct || 0) >= 0 ? 'up' : 'down'].join(' ')}>
                  {(s.change_pct || 0) > 0 ? '+' : ''}{(s.change_pct || 0).toFixed(2)}%
                </div>
                <div className="sec-meta">
                  {(s.d1 || 0) > 0 && <span className="d1-badge">D1 {s.d1.toFixed(0)}</span>}
                  <span>涨停 {s.limitup_cnt || 0}</span>
                  <span>流入 {s.net_inflow ? (s.net_inflow / 1e8).toFixed(1) + '亿' : '—'}</span>
                </div>
              </Card>
            ))}
          </div>
        ) : (
          <div className="empty">暂无热点板块数据</div>
        )}
      </Card>

      {/* 板块异动原因弹窗 */}
      <Dialog visible={!!reasonTarget} header={reasonTarget ? reasonTarget.name : ''} onClose={() => setReasonTarget(null)} confirmBtn="知道了" cancelBtn="">
        {reasonTarget && (
          <div>
            <div className="modal-section">
              <div className="modal-subtitle">板块异动原因</div>
              <div className="modal-reason">{reasonTarget.reason_detail || reasonTarget.reason || '暂无'}</div>
            </div>
            {reasonTarget.news_titles && reasonTarget.news_titles.length ? (
              <div className="modal-section">
                <div className="modal-subtitle">触发新闻（{reasonTarget.news_titles.length}条）</div>
                {reasonTarget.news_titles.map((t, i) => (
                  <div key={i} className="modal-news-item">
                    <span className="modal-news-idx">{i + 1}.</span>
                    <span className="modal-news-title">{t}</span>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        )}
      </Dialog>

      {/* 运行日志弹窗 */}
      <Dialog visible={showLog} header="运行日志" onClose={() => setShowLog(false)} confirmBtn="关闭" cancelBtn="">
        <div>
          <div className="modal-section">
            <div className="modal-subtitle">信号批次 / 阶段记录</div>
            <pre className="modal-reason" style={{ maxHeight: 180 }}>{logSignal || '暂无'}</pre>
          </div>
          <div className="modal-section">
            <div className="modal-subtitle">Stage 轮次记录</div>
            <pre className="modal-reason" style={{ maxHeight: 180 }}>{logStage || '暂无'}</pre>
          </div>
        </div>
      </Dialog>

      {/* 个股评分排名 */}
      <Card style={{ marginTop: 14 }}>
        <div className="card-header" style={{ display: 'flex', justifyContent: 'space-between' }}>
          <span>📊 个股评分排名</span>
          <span className="card-sub">N形≥60 / 龙头≥60 / 双凸≥60 / 回头≥60 / 动量≥50</span>
        </div>
        {evals.length ? (
          <Table
            data={sortedEvals}
            columns={evalColumns}
            rowKey="code"
            size="small"
            pagination={{ pageSize: 20, showJumper: true }}
            rowClassName={({ row }) => rowClass(row)}
          />
        ) : (
          <div className="empty"><span className="loading-dot"></span> 等待评估结果...</div>
        )}
        <div className="legend">
          <span className="lg-strong">≥80 强势</span>
          <span className="lg-pass">≥门槛 达标</span>
          <span className="lg-low">&lt;门槛 偏低</span>
          <span className="lg-sep">|</span>
          <span className="lg-item">N形≥60操作, 龙头≥60买入/≥50观察, 双凸≥60买入/50-60观察, 回头≥60入场, 动量≥50关注</span>
          <span className="lg-sep">|</span>
          <span className="lg-item">点击表头排序</span>
        </div>
      </Card>

      {/* 宏观日历 */}
      <Card style={{ marginTop: 14 }}>
        <div className="card-header">📅 宏观日历</div>
        {calendarData.length ? (
          <Table data={calendarData} columns={calendarColumns} rowKey="id" size="small" pagination={false} maxHeight={200} />
        ) : (
          <div className="hs-cal-empty">暂无日历事件</div>
        )}
      </Card>

      {/* IPO 日历 */}
      <Card style={{ marginTop: 14 }}>
        <div className="card-header">📋 IPO日历</div>
        {ipoData.length ? (
          <Table data={ipoData} columns={ipoColumns} rowKey="id" size="small" pagination={false} maxHeight={200} />
        ) : (
          <div className="hs-cal-empty">暂无IPO日历</div>
        )}
      </Card>

      {/* 热点资讯 */}
      <Card style={{ marginTop: 14 }}>
        <div className="card-header" style={{ display: 'flex', justifyContent: 'space-between' }}>
          <span>📰 热点资讯</span>
          <Button size="small" variant="outline" theme="primary" disabled={reanalyzing} onClick={onReanalyze}>
            {reanalyzing ? '补推中…' : '🔁 手动补推'}
          </Button>
        </div>
        {newsData.length ? (
          <Table data={newsData} columns={newsColumns} rowKey="id" size="small" pagination={false} maxHeight={400} />
        ) : (
          <div className="empty">暂无资讯</div>
        )}
      </Card>
    </div>
  )
}
