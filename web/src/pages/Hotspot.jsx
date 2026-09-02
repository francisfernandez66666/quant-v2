// ── 热点页面 Hotspot.jsx ──
// 展示热点板块（含异动原因弹窗）、全市场个股评分排名、宏观日历、IPO日历、热点资讯。
// 全面使用 TDesign 组件：Card / Table / Dialog / Tag / Button，纯内联样式，无自定义 CSS。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Card, Table, Dialog, Tag, Button, Select, MessagePlugin } from 'tdesign-react'
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
  const [logSignals, setLogSignals] = useState([])
  const [logStages, setLogStages] = useState([])
  // 日志批次选择：'all'=平铺全部；数字=只看该轮批次（下拉框按批次区分）
  const [logBatch, setLogBatch] = useState('all')
  const [reanalyzing, setReanalyzing] = useState(false)

  const [sortKey, setSortKey] = useState('')
  const [sortDir, setSortDir] = useState(-1)

  const loadingRef = useRef(false)      // 加载互斥标记（防止重复请求）
  const timerRef = useRef(null)         // 轮询定时器句柄
  const unsubSSERef = useRef(null)      // SSE 取消订阅函数引用
  const visHandlerRef = useRef(null)    // 页面可见性事件处理器引用

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

  // 资讯按标题建索引，供板块弹窗匹配「原文」
  // 按标题建索引：标题→新闻原文，供板块弹窗精确匹配关联新闻
  const newsByTitle = useMemo(() => {
    const m = {} // 标题→新闻原文的索引表
    for (const n of news) if (n.title) m[n.title] = n
    return m
  }, [news])
  // 按标题（精确→模糊包含）查找关联新闻原文
  function findNews(title) {
    if (!title) return null
    if (newsByTitle[title]) return newsByTitle[title]
    const t = title.replace(/\s/g, '')
    for (const k of Object.keys(newsByTitle)) {
      const kk = k.replace(/\s/g, '')
      if (kk && (kk.includes(t) || t.includes(kk))) return newsByTitle[k]
    }
    return null
  }

  // 按板块名反查资讯库原文：板块归因的 news_titles 与资讯库标题常不一致（LLM 改写/截断），
  // 标题匹配失败时退而展示「资讯库中 sectors 命中本板块」的原文，保证点开能溯源看到原文（修复：点开啥也没有）。
  // English: when title matching fails, surface news whose `sectors` contains this sector name.
  // 按板块名建索引：板块名→命中该板块的全部新闻原文，用于标题匹配失败时的兜底溯源
  const newsBySector = useMemo(() => {
    const m = {} // 板块名→新闻数组的索引表
    for (const n of news) {
      for (const sec of (n.sectors || [])) {
        if (!sec) continue
        if (!m[sec]) m[sec] = []
        m[sec].push(n)
      }
    }
    return m
  }, [news])
  // 按板块名反查资讯库原文：返回所有 sectors 命中该板块的新闻（去重），供异动原因弹窗溯源展示
  function findNewsBySector(name) {
    if (!name) return []
    const out = []
    const seen = new Set()
    for (const k of Object.keys(newsBySector)) {
      if (!k) continue
      if (k === name || k.includes(name) || name.includes(k)) {
        for (const n of newsBySector[k]) {
          if (!seen.has(n.title)) { seen.add(n.title); out.push(n) }
        }
      }
    }
    return out
  }

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

  // 将时间戳或字符串统一格式化为 YYYY-MM-DD HH:mm:ss（用于日志/弹窗时间）
  function fmtLogTime(dt) {
    if (dt === null || dt === undefined || dt === '') return ''
    const s = String(dt)
    const d = /^\d+$/.test(s) ? new Date(Number(s) * 1000) : new Date(s)
    if (isNaN(d.getTime())) return s
    const p = (n) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
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
    setLogBatch('all')
    try {
      const sl = await fetchSignalLogs()
      setLogSignals(Array.isArray(sl) ? sl : [])
    } catch (_) { setLogSignals([]) }
    try {
      const st = await fetchStageRecords()
      setLogStages(Array.isArray(st) ? st : [])
    } catch (_) { setLogStages([]) }
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
  // 将 IPO 日历转为表格行数据并附加上市倒计时状态与标签主题
  const ipoData = ipoCalendar.map((c, i) => ({ id: 'ipo' + i, date: c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : ''), name: c.name, code: c.code, issue_price: c.issue_price, status: ipoCountdown(c), statusTheme: c.list_status === 'L' ? 'default' : 'warning' }))

  // 资讯标签解析：根据情绪/方向/影响等级生成 TDesign Tag 的 {text, theme} 列表
  // 归一影响级别：后端部分来源可能下发英文 high/medium/low，统一成中文 高/中/低
  function normImpact(v) {
    if (!v) return ''
    const m = { high: '高', medium: '中', low: '低' }
    return m[String(v).toLowerCase()] || v
  }
  // 将单条资讯解析为标签列表（情绪/方向/影响级别），供热点资讯表格渲染彩色 Tag
  function newsTags(n) {
    const tags = []
    // sentiment 与 direction 常相等（后端 sentiment 复用了 direction 值），仅在不同时展示避免重复；
    // 颜色映射同时兼容「正面/负面」与「利好/利空」两种取值
    if (n.sentiment && n.sentiment !== n.direction) {
      const theme = (n.sentiment === '正面' || n.sentiment === '利好') ? 'success' : (n.sentiment === '负面' || n.sentiment === '利空') ? 'danger' : 'default'
      tags.push({ text: n.sentiment, theme })
    }
    if (n.direction) { const theme = n.direction === '利好' ? 'success' : n.direction === '利空' ? 'danger' : 'default'; tags.push({ text: n.direction, theme }) }
    const il = normImpact(n.impact_level)
    if (il) { const theme = il === '高' ? 'warning' : il === '中' ? 'primary' : 'default'; tags.push({ text: il + '影响', theme }) }
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
  // 将资讯列表转成表格行数据（格式化时间并解析情绪/方向/影响标签、板块、个股）
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
              <Card key={s.code} bordered={false} style={{ background: '#eef4fc', border: '1px solid #eef0f3' }}>
                <div onClick={() => setReasonTarget(s)} style={{ cursor: 'pointer' }}>
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
              <div style={{ fontSize: 13, color: '#333', whiteSpace: 'pre-wrap', lineHeight: 1.6 }}>{reasonTarget.reason_detail || reasonTarget.reason || '暂无'}</div>
            </div>
            {/* 触发新闻：优先用后端直接溯源的 news_items（含正文），标题二次匹配失败时按板块名兜底 */}
            <div>
              {(() => {
                const items = reasonTarget.news_items && reasonTarget.news_items.length ? reasonTarget.news_items : null
                if (items) {
                  return (
                    <div>
                      <div style={{ fontWeight: 600, marginBottom: 6 }}>触发新闻（{items.length}条）</div>
                      {items.map((art, i) => (
                        <div key={i} style={{ marginBottom: 12, borderLeft: '3px solid #4fc3f7', paddingLeft: 10 }}>
                          <div style={{ fontSize: 13, fontWeight: 600, color: '#1a1a1a' }}>{i + 1}. {art.title}</div>
                          <div style={{ fontSize: 12, color: '#555', marginTop: 4, whiteSpace: 'pre-wrap', lineHeight: 1.6 }}>{art.content || '（该资讯暂未收录正文）'}</div>
                        </div>
                      ))}
                    </div>
                  )
                }
                // 兜底：旧逻辑（标题匹配 + 板块名反查）
                const titles = reasonTarget.news_titles || []
                return (
                  <div>
                    <div style={{ fontWeight: 600, marginBottom: 6 }}>触发新闻{titles.length ? `（${titles.length}条）` : ''}</div>
                    {titles.length ? (
                      titles.map((t, i) => {
                        const art = findNews(t)
                        return (
                          <div key={i} style={{ marginBottom: 12, borderLeft: '3px solid #4fc3f7', paddingLeft: 10 }}>
                            <div style={{ fontSize: 13, fontWeight: 600, color: '#1a1a1a' }}>{i + 1}. {t}</div>
                            {art ? (
                              <div style={{ fontSize: 12, color: '#555', marginTop: 4, whiteSpace: 'pre-wrap', lineHeight: 1.6 }}>{art.content || '（资讯库无正文）'}</div>
                            ) : (
                              <div style={{ fontSize: 12, color: '#999', marginTop: 4 }}>（未在资讯库匹配到原文）</div>
                            )}
                          </div>
                        )
                      })
                    ) : (
                      <div style={{ fontSize: 13, color: '#888' }}>暂无关联新闻（来源未提供相关触发新闻）</div>
                    )}
                    {titles.length > 0 && !titles.every((t) => findNews(t)) ? (
                      (() => {
                        const related = findNewsBySector(reasonTarget.name)
                        if (!related.length) return null
                        return (
                          <div style={{ marginTop: 10 }}>
                            <div style={{ fontWeight: 600, marginBottom: 6, color: '#1d4ed8' }}>板块相关原文（按板块名匹配，{related.length}条）</div>
                            {related.map((art, i) => (
                              <div key={'r' + i} style={{ marginBottom: 12, borderLeft: '3px solid #FAAD14', paddingLeft: 10 }}>
                                <div style={{ fontSize: 13, fontWeight: 600, color: '#1a1a1a' }}>{art.title}</div>
                                <div style={{ fontSize: 12, color: '#555', marginTop: 4, whiteSpace: 'pre-wrap', lineHeight: 1.6 }}>{art.content || '（资讯库无正文）'}</div>
                              </div>
                            ))}
                          </div>
                        )
                      })()
                    ) : null}
                  </div>
                )
              })()}
            </div>
          </div>
        )}
      </Dialog>

      {/* 运行日志弹窗：将结构化日志渲染为人人可读的摘要，避免原始 JSON 对非技术人员不可读。
          批次下拉框：按轮次（批次）区分，默认平铺全部；选定某轮只展示该批次信号+分析。 */}
      <Dialog visible={showLog} header="运行日志" onClose={() => setShowLog(false)} confirmBtn="关闭" cancelBtn="">
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
            <span style={{ fontSize: 13, color: '#888' }}>按批次查看</span>
            <Select
              value={logBatch}
              onChange={(v) => setLogBatch(v)}
              size="small"
              style={{ width: 280 }}
              options={[
                { label: `全部批次（信号 ${logSignals.length} 轮 / 分析 ${logStages.length} 轮）`, value: 'all' },
                ...Array.from({ length: Math.max(logSignals.length, logStages.length) }, (_, i) => {
                  const t = (logSignals[i] && logSignals[i].process_time) || (logStages[i] && logStages[i].process_time) || ''
                  return { label: `第 ${i + 1} 轮 · ${fmtLogTime(t)}`, value: String(i) }
                }),
              ]}
            />
          </div>

          {logBatch !== 'all' ? (() => {
            const bi = Number(logBatch)
            const log = logSignals[bi]
            const d = logStages[bi]
            return (
              <div>
                <div style={{ fontWeight: 600, marginBottom: 6 }}>第 {bi + 1} 轮 · 信号批次</div>
                {!log ? (
                  <div style={{ fontSize: 13, color: '#888', marginBottom: 12 }}>该批次暂无信号日志</div>
                ) : (
                  <div style={{ marginBottom: 16, border: '1px solid #eef0f3', borderRadius: 6, padding: 10 }}>
                    <div style={{ fontSize: 12, color: '#888', marginBottom: 6 }}>
                      {fmtLogTime(log.process_time)} · 扫描新闻 {log.raw_count || 0} 条 · 产出信号 {log.signals ? log.signals.length : 0} 个
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                      {(log.signals || []).map((sg, j) => (
                        <div key={j} style={{ fontSize: 12, background: '#f7f9fc', borderRadius: 4, padding: '6px 8px' }}>
                          <span style={{ fontFamily: 'monospace', color: '#1a1a1a', fontWeight: 600 }}>{sg.code} {sg.name}</span>
                          <span style={{ marginLeft: 6, color: sg.direction === '做空' ? '#e34d59' : '#00a870' }}>{sg.direction}</span>
                          <span style={{ marginLeft: 6, color: '#1d4ed8' }}>{sg.action}</span>
                          <span style={{ marginLeft: 6, color: '#666' }}>{sg.strategy}</span>
                          {sg.price > 0 && <span style={{ marginLeft: 6, color: '#666' }}>触发价 ¥{sg.price}</span>}
                          {sg.reason && <div style={{ color: '#888', marginTop: 3 }}>原因：{sg.reason}</div>}
                        </div>
                      ))}
                    </div>
                  </div>
                )}
                <div style={{ fontWeight: 600, marginBottom: 6 }}>第 {bi + 1} 轮 · 新闻分析</div>
                {!d ? (
                  <div style={{ fontSize: 13, color: '#888' }}>该批次暂无阶段记录</div>
                ) : (
                  <div style={{ border: '1px solid #eef0f3', borderRadius: 6, padding: 10 }}>
                    <div style={{ fontSize: 12, color: '#888', marginBottom: 6 }}>
                      {fmtLogTime(d.process_time)} · 初筛模式 {d.stage1_mode || '-'}：原始 {d.raw_count || 0} 条 → 命中 {d.selected_count || 0} 条
                    </div>
                    <div style={{ fontSize: 12, color: '#333', fontWeight: 600, marginBottom: 4 }}>命中事件：</div>
                    {(d.stage2_events || []).map((ev, j) => (
                      <div key={j} style={{ fontSize: 12, borderLeft: '3px solid #FAAD14', paddingLeft: 8, margin: '4px 0', color: '#555' }}>
                        <span style={{ fontWeight: 600, color: '#1a1a1a' }}>{ev.title}</span>
                        <span style={{ marginLeft: 6, color: ev.direction === '利好' ? '#00a870' : ev.direction === '利空' ? '#e34d59' : '#888' }}>{ev.direction}</span>
                        {(ev.sectors || []).length > 0 && <span style={{ marginLeft: 6, color: '#4fc3f7' }}>{ev.sectors.join(' / ')}</span>}
                      </div>
                    ))}
                    {(!d.stage2_events || d.stage2_events.length === 0) && (
                      <div style={{ fontSize: 12, color: '#999' }}>本轮无命中事件（原始标题 {d.raw_titles ? d.raw_titles.length : 0} 条未通过筛选）</div>
                    )}
                  </div>
                )}
              </div>
            )
          })() : (
            <div>
              <div style={{ marginBottom: 14 }}>
                <div style={{ fontWeight: 600, marginBottom: 6 }}>信号批次（共 {logSignals.length} 轮）</div>
                {logSignals.length === 0 && <div style={{ fontSize: 13, color: '#888' }}>暂无信号日志</div>}
                {logSignals.map((log, i) => (
                  <div key={i} style={{ marginBottom: 12, border: '1px solid #eef0f3', borderRadius: 6, padding: 10 }}>
                    <div style={{ fontSize: 12, color: '#888', marginBottom: 6 }}>
                      第 {i + 1} 轮 · {fmtLogTime(log.process_time)} · 扫描新闻 {log.raw_count || 0} 条 · 产出信号 {log.signals ? log.signals.length : 0} 个
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                      {(log.signals || []).map((sg, j) => (
                        <div key={j} style={{ fontSize: 12, background: '#f7f9fc', borderRadius: 4, padding: '6px 8px' }}>
                          <span style={{ fontFamily: 'monospace', color: '#1a1a1a', fontWeight: 600 }}>{sg.code} {sg.name}</span>
                          <span style={{ marginLeft: 6, color: sg.direction === '做空' ? '#e34d59' : '#00a870' }}>{sg.direction}</span>
                          <span style={{ marginLeft: 6, color: '#1d4ed8' }}>{sg.action}</span>
                          <span style={{ marginLeft: 6, color: '#666' }}>{sg.strategy}</span>
                          {sg.price > 0 && <span style={{ marginLeft: 6, color: '#666' }}>触发价 ¥{sg.price}</span>}
                          {sg.reason && <div style={{ color: '#888', marginTop: 3 }}>原因：{sg.reason}</div>}
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
              <div>
                <div style={{ fontWeight: 600, marginBottom: 6 }}>新闻分析轮次（共 {logStages.length} 轮）</div>
                {logStages.length === 0 && <div style={{ fontSize: 13, color: '#888' }}>暂无阶段记录</div>}
                {logStages.map((d, i) => (
                  <div key={i} style={{ marginBottom: 12, border: '1px solid #eef0f3', borderRadius: 6, padding: 10 }}>
                    <div style={{ fontSize: 12, color: '#888', marginBottom: 6 }}>
                      第 {i + 1} 轮 · {fmtLogTime(d.process_time)} · 初筛模式 {d.stage1_mode || '-'}：原始 {d.raw_count || 0} 条 → 命中 {d.selected_count || 0} 条
                    </div>
                    <div style={{ fontSize: 12, color: '#333', fontWeight: 600, marginBottom: 4 }}>命中事件：</div>
                    {(d.stage2_events || []).map((ev, j) => (
                      <div key={j} style={{ fontSize: 12, borderLeft: '3px solid #FAAD14', paddingLeft: 8, margin: '4px 0', color: '#555' }}>
                        <span style={{ fontWeight: 600, color: '#1a1a1a' }}>{ev.title}</span>
                        <span style={{ marginLeft: 6, color: ev.direction === '利好' ? '#00a870' : ev.direction === '利空' ? '#e34d59' : '#888' }}>{ev.direction}</span>
                        {(ev.sectors || []).length > 0 && <span style={{ marginLeft: 6, color: '#4fc3f7' }}>{ev.sectors.join(' / ')}</span>}
                      </div>
                    ))}
                    {(!d.stage2_events || d.stage2_events.length === 0) && (
                      <div style={{ fontSize: 12, color: '#999' }}>本轮无命中事件（原始标题 {d.raw_titles ? d.raw_titles.length : 0} 条未通过筛选）</div>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
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
            // §FIX-20260902 补 total=长度：不传 total 时 tdesign 分页显示「共 0 条」且下一页不可用
            pagination={{ pageSize: 20, showJumper: true, total: sortedEvals.length }}
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
