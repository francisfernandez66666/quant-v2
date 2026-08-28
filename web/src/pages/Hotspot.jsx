// ── 热点页面 Hotspot.jsx ──
// 展示热点板块（含异动原因弹窗）、全市场个股评分排名、宏观日历、IPO日历、热点资讯。
// 全面使用 TDesign 组件：Card / Table / Dialog / Tag / Button，纯内联样式，无自定义 CSS。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Card, Table, Dialog, Tag, Button, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import { fetchSignalLogs, fetchStageRecords } from '../api/index.js'

// ── 工具函数 ──
// 截断异动原因为简短描述
function shortReason(r) {
  if (!r) return ''
  const idx = r.indexOf('，')
  return idx > 0 ? r.slice(0, idx) : r.slice(0, 18)
}

// 根据分数与阈值返回评分单元格颜色（红强 / 橙达标 / 灰偏低）
function scoreColorF(score, pass, strongMin) {
  if (!score || score <= 0) return '#555'
  if (score >= strongMin) return '#e34d59'
  if (pass) return '#FAAD14'
  return '#555'
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

  // 过滤非日历类资讯
  const newsItems = useMemo(() => news.filter((n) => n.source !== '宏观日历' && n.source !== '政策反制'), [news])
  // 过滤宏观日历与政策反制事件
  const calendarEvents = useMemo(() => news.filter((n) => n.source === '宏观日历' || n.source === '政策反制'), [news])

  // 根据上市日期计算 IPO 倒计时（相对今天的天数差，含「今天/已上市/即将上市」）
  function ipoCountdown(c) {
    const ds = c.listing_date || c.ipo_date
    if (!ds) return c.list_status === 'L' ? '已上市' : '即将上市'
    const t = new Date(+ds.slice(0, 4), +ds.slice(4, 6) - 1, +ds.slice(6, 8))
    const diff = Math.ceil((t - Date.now()) / 86400000) // 86400000 = 一天的毫秒数
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
          // 重新分析为异步任务，1.5s 后拉取一次最新结果
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
    timerRef.current = setInterval(load, 5000) // 每 5s 轮询刷新热点/评分/新闻/IPO
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
    { colKey: 'n_score', title: 'N≥60', width: 70, sorter: (a, b) => (a.n_score || 0) - (b.n_score || 0), cell: ({ row }) => <span style={{ color: scoreColorF(row.n_score, row.n_score >= 60, 80), fontWeight: 600 }}>{row.n_score > 0 ? row.n_score.toFixed(0) : '—'}</span> },
    { colKey: 'dragon_score', title: '龙≥60', width: 70, sorter: (a, b) => (a.dragon_score || 0) - (b.dragon_score || 0), cell: ({ row }) => <span style={{ color: scoreColorF(row.dragon_score, row.dragon_score >= 60, 80), fontWeight: 600 }}>{row.dragon_score > 0 ? row.dragon_score.toFixed(0) : '—'}</span> },
    { colKey: 'db_score', title: '凸≥60', width: 70, sorter: (a, b) => (a.db_score || 0) - (b.db_score || 0), cell: ({ row }) => <span style={{ color: scoreColorF(row.db_score, row.db_score >= 60, 80), fontWeight: 600 }}>{row.db_score > 0 ? row.db_score.toFixed(0) : '—'}</span> },
    { colKey: 'dr_score', title: '回≥60', width: 70, sorter: (a, b) => (a.dr_score || 0) - (b.dr_score || 0), cell: ({ row }) => <span style={{ color: scoreColorF(row.dr_score, row.dr_score >= 60, 80), fontWeight: 600 }}>{row.dr_score > 0 ? row.dr_score.toFixed(0) : '—'}</span> },
    { colKey: 'm_score', title: '量≥50', width: 70, sorter: (a, b) => (a.m_score || 0) - (b.m_score || 0), cell: ({ row }) => <span style={{ color: scoreColorF(row.m_score, row.m_score >= 50, 70), fontWeight: 600 }}>{row.m_score > 0 ? row.m_score.toFixed(0) : '—'}</span> },
  ]

  // 宏观日历列
  const calendarColumns = [
    { colKey: 'date', title: '日期', width: 90, cell: ({ row }) => <span style={{ color: '#888' }}>{row.date}</span> },
    { colKey: 'title', title: '事件', cell: ({ row }) => <span style={{ color: '#1a1a1a' }}>{row.title}</span> },
  ]
  const calendarData = calendarEvents.map((c, i) => ({ id: 'c' + i, date: c.datetime ? c.datetime.slice(5, 10) : '', title: c.title })) // 取 MM-DD 作为日期列

  // IPO 日历列
  const ipoColumns = [
    { colKey: 'date', title: '日期', width: 90, cell: ({ row }) => <span style={{ color: '#888' }}>{row.date}</span> },
    { colKey: 'name', title: '名称', width: 160, cell: ({ row }) => <span style={{ color: '#1a1a1a' }}>{row.name}（{row.code}）</span> },
    { colKey: 'price', title: '发行价', width: 90, cell: ({ row }) => row.issue_price ? <span style={{ color: '#4fc3f7' }}>¥{row.issue_price.toFixed(2)}</span> : null },
    { colKey: 'status', title: '状态', width: 100, cell: ({ row }) => <Tag size="small" theme={row.statusTheme}>{row.status}</Tag> },
  ]
  const ipoData = ipoCalendar.map((c, i) => ({ id: 'ipo' + i, date: c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : ''), name: c.name, code: c.code, issue_price: c.issue_price, status: ipoCountdown(c), statusTheme: c.list_status === 'L' ? 'default' : 'warning' }))

  // 资讯标签解析：根据情绪/方向/影响等级生成 TDesign Tag 的 {text, theme} 列表
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
    <div className="page">
      <Card>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <span>🔥 热点板块</span>
          <Button size="small" variant="outline" theme="primary" onClick={openLog}>📋 日志</Button>
        </div>
        {sectors.length ? (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 12 }}>
            {sectors.map((s) => (
              <Card key={s.code} bordered={false} style={{ cursor: 'pointer', background: '#eef4fc', border: '1px solid #eef0f3' }} onClick={() => setReasonTarget(s)}>
                <div style={{ fontSize: 14, fontWeight: 600, color: '#1a1a1a' }}>{s.name}</div>
                {s.reason && <div style={{ fontSize: 11, color: '#888', marginTop: 4, minHeight: 28, overflow: 'hidden' }}>{shortReason(s.reason)}</div>}
                <div style={{ fontSize: 16, fontWeight: 700, color: '#FAAD14', marginTop: 4 }}>{Math.round((s.score || 0) * 100)}分</div>
                <div className={(s.change_pct || 0) >= 0 ? 'up' : 'down'} style={{ fontWeight: 700, marginTop: 2 }}>
                  {(s.change_pct || 0) > 0 ? '+' : ''}{(s.change_pct || 0).toFixed(2)}%
                </div>
                <div style={{ fontSize: 11, color: '#777', marginTop: 6 }}>
                  {(s.d1 || 0) > 0 && <span style={{ display: 'inline-block', background: 'rgba(79,195,247,0.15)', color: '#4fc3f7', borderRadius: 4, padding: '1px 5px', marginRight: 6 }}>D1 {s.d1.toFixed(0)}</span>}
                  <span>涨停 {s.limitup_cnt || 0}</span>
                  <span style={{ marginLeft: 8 }}>流入 {s.net_inflow ? (s.net_inflow / 1e8).toFixed(1) + '亿' : '—'}</span>
                </div>
              </Card>
            ))}
          </div>
        ) : (
          <div className="muted" style={{ padding: 24, textAlign: 'center' }}>暂无热点板块数据</div>
        )}
      </Card>

      {/* 板块异动原因弹窗 */}
      <Dialog visible={!!reasonTarget} header={reasonTarget ? reasonTarget.name : ''} onClose={() => setReasonTarget(null)} confirmBtn="知道了" cancelBtn="">
        {reasonTarget && (
          <div>
            {/* 信息来源 / 归因来源标识：根据后端透传的 source 字段渲染 */}
            <div style={{ marginBottom: 14 }}>
              <div style={{ fontWeight: 600, marginBottom: 6 }}>信息来源</div>
              <div style={{ fontSize: 13 }}>
                {reasonTarget.source === 'llm' ? (
                  <span style={{ display: 'inline-block', background: 'rgba(0,168,112,0.15)', color: '#00a870', borderRadius: 4, padding: '2px 8px' }}>LLM 归因</span>
                ) : reasonTarget.source === 'ths' ? (
                  <span style={{ display: 'inline-block', background: 'rgba(250,173,20,0.15)', color: '#FAAD14', borderRadius: 4, padding: '2px 8px' }}>同花顺板块兜底</span>
                ) : (
                  <span style={{ display: 'inline-block', background: 'rgba(153,153,153,0.15)', color: '#999', borderRadius: 4, padding: '2px 8px' }}>未知来源</span>
                )}
              </div>
            </div>
            <div style={{ marginBottom: 14 }}>
              <div style={{ fontWeight: 600, marginBottom: 6 }}>板块异动原因</div>
              <div style={{ fontSize: 13, color: '#ccc', whiteSpace: 'pre-wrap' }}>{reasonTarget.reason_detail || reasonTarget.reason || '暂无'}</div>
            </div>
            {/* 触发新闻：有则渲染列表，无则给出友好空态提示，避免用户误以为功能损坏 */}
            <div>
              <div style={{ fontWeight: 600, marginBottom: 6 }}>触发新闻{reasonTarget.news_titles && reasonTarget.news_titles.length ? `（${reasonTarget.news_titles.length}条）` : ''}</div>
              {reasonTarget.news_titles && reasonTarget.news_titles.length ? (
                reasonTarget.news_titles.map((t, i) => (
                  <div key={i} style={{ display: 'flex', gap: 6, fontSize: 13, color: '#666666', padding: '2px 0' }}>
                    <span style={{ color: '#888' }}>{i + 1}.</span>
                    <span>{t}</span>
                  </div>
                ))
              ) : (
                <div style={{ fontSize: 13, color: '#888' }}>暂无关联新闻（来源未提供相关触发新闻）</div>
              )}
            </div>
          </div>
        )}
      </Dialog>

      {/* 运行日志弹窗 */}
      <Dialog visible={showLog} header="运行日志" onClose={() => setShowLog(false)} confirmBtn="关闭" cancelBtn="">
        <div>
          <div style={{ marginBottom: 14 }}>
            <div style={{ fontWeight: 600, marginBottom: 6 }}>信号批次 / 阶段记录</div>
            <pre style={{ fontSize: 12, color: '#ccc', background: '#eef4fc', borderRadius: 6, padding: 10, maxHeight: 180, overflow: 'auto', margin: 0 }}>{logSignal || '暂无'}</pre>
          </div>
          <div>
            <div style={{ fontWeight: 600, marginBottom: 6 }}>Stage 轮次记录</div>
            <pre style={{ fontSize: 12, color: '#ccc', background: '#eef4fc', borderRadius: 6, padding: 10, maxHeight: 180, overflow: 'auto', margin: 0 }}>{logStage || '暂无'}</pre>
          </div>
        </div>
      </Dialog>

      {/* 个股评分排名 */}
      <Card style={{ marginTop: 14 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <span>📊 个股评分排名</span>
          <span className="muted">N形≥60 / 龙头≥60 / 双凸≥60 / 回头≥60 / 动量≥50</span>
        </div>
        {evals.length ? (
          <Table
            data={sortedEvals}
            columns={evalColumns}
            rowKey="code"
            size="small"
            pagination={{ pageSize: 20, showJumper: true }}
          />
        ) : (
          <div className="muted" style={{ padding: 24, textAlign: 'center' }}>等待评估结果...</div>
        )}
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, fontSize: 11, color: '#888', marginTop: 10 }}>
          <span style={{ color: '#e34d59' }}>≥80 强势</span>
          <span style={{ color: '#FAAD14' }}>≥门槛 达标</span>
          <span style={{ color: '#555' }}>&lt;门槛 偏低</span>
          <span>|</span>
          <span>N形≥60操作, 龙头≥60买入/≥50观察, 双凸≥60买入/50-60观察, 回头≥60入场, 动量≥50关注</span>
          <span>|</span>
          <span>点击表头排序</span>
        </div>
      </Card>

      {/* 宏观日历 */}
      <Card style={{ marginTop: 14 }}>
        <div style={{ fontWeight: 600, marginBottom: 12 }}>📅 宏观日历</div>
        {calendarData.length ? (
          <Table data={calendarData} columns={calendarColumns} rowKey="id" size="small" pagination={false} maxHeight={200} />
        ) : (
          <div className="muted" style={{ padding: 16, textAlign: 'center' }}>暂无日历事件</div>
        )}
      </Card>

      {/* IPO 日历 */}
      <Card style={{ marginTop: 14 }}>
        <div style={{ fontWeight: 600, marginBottom: 12 }}>📋 IPO日历</div>
        {ipoData.length ? (
          <Table data={ipoData} columns={ipoColumns} rowKey="id" size="small" pagination={false} maxHeight={200} />
        ) : (
          <div className="muted" style={{ padding: 16, textAlign: 'center' }}>暂无IPO日历</div>
        )}
      </Card>

      {/* 热点资讯 */}
      <Card style={{ marginTop: 14 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <span>📰 热点资讯</span>
          <Button size="small" variant="outline" theme="primary" disabled={reanalyzing} onClick={onReanalyze}>
            {reanalyzing ? '补推中…' : '🔁 手动补推'}
          </Button>
        </div>
        {newsData.length ? (
          <Table data={newsData} columns={newsColumns} rowKey="id" size="small" pagination={false} maxHeight={400} />
        ) : (
          <div className="muted" style={{ padding: 16, textAlign: 'center' }}>暂无资讯</div>
        )}
      </Card>
    </div>
  )
}
