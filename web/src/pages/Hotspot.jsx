// ── 热点页面 Hotspot.jsx ──
// 展示热点板块（含异动原因弹窗）、全市场个股评分排名、宏观日历、IPO日历、热点资讯
import React, { useState, useEffect, useRef, useMemo } from 'react'
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

// 根据多维度评分决定个股行高亮样式
function rowClass(e) {
  const strong = (e.n_score || 0) >= 80 || (e.dragon_score || 0) >= 80 || (e.db_score || 0) >= 80 || (e.dr_score || 0) >= 80 || (e.m_score || 0) >= 70
  if (strong) return 'ev-row strong'
  const watch = (e.n_score || 0) >= 60 || (e.dragon_score || 0) >= 60 || (e.db_score || 0) >= 60 || (e.dr_score || 0) >= 60 || (e.m_score || 0) >= 50
  if (watch) return 'ev-row watch'
  return 'ev-row'
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
    if (sortKey === key) {
      setSortDir(d => d * -1)
    } else {
      setSortKey(key)
      setSortDir(-1)
    }
  }

  // 返回当前排序方向的箭头字符
  function sortArrow(key) {
    if (sortKey !== key) return ''
    return sortDir === -1 ? ' ▼' : ' ▲'
  }

  // 安全读取字段值
  function val(e, key) {
    const v = e[key]
    if (typeof v === 'string') return v || ''
    return v || 0
  }

  // 过滤非日历类资讯
  const newsItems = useMemo(() => news.filter(n => n.source !== '宏观日历' && n.source !== '政策反制'), [news])
  // 过滤宏观日历与政策反制事件
  const calendarEvents = useMemo(() => news.filter(n => n.source === '宏观日历' || n.source === '政策反制'), [news])

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

  // 计算当前评分列表中的最高维度分，用于进度条比例
  const maxScore = useMemo(() => {
    let m = 0
    for (const e of evals) {
      const t = Math.max(e.n_score || 0, e.dragon_score || 0, e.db_score || 0, e.dr_score || 0, e.m_score || 0)
      if (t > m) m = t
    }
    return m || 100
  }, [evals])

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

  return (
    <div className="hotspot-page">
      <div className="card">
        <div className="card-header">
          <span>🔥 热点板块</span>
          <button className="btn-log" onClick={openLog}>📋 日志</button>
        </div>
        {sectors.length ? (
          <div className="sector-grid">
            {sectors.map((s) => (
              <div key={s.code} className="sector-card" onClick={() => setReasonTarget(s)}>
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
              </div>
            ))}
          </div>
        ) : (
          <div className="empty">暂无热点板块数据</div>
        )}
      </div>

      {reasonTarget && (
        <>
          <div className="modal-overlay" onClick={() => setReasonTarget(null)}></div>
          <div className="modal">
            <div className="modal-header">{reasonTarget.name}</div>
            <div className="modal-body">
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
            <button className="modal-close" onClick={() => setReasonTarget(null)}>知道了</button>
          </div>
        </>
      )}

      {showLog && (
        <>
          <div className="modal-overlay" onClick={() => setShowLog(false)}></div>
          <div className="modal" style={{ maxWidth: 480 }}>
            <div className="modal-header">运行日志</div>
            <div className="modal-body">
              <div className="modal-section">
                <div className="modal-subtitle">信号批次 / 阶段记录</div>
                <pre className="modal-reason" style={{ maxHeight: 180 }}>{logSignal || '暂无'}</pre>
              </div>
              <div className="modal-section">
                <div className="modal-subtitle">Stage 轮次记录</div>
                <pre className="modal-reason" style={{ maxHeight: 180 }}>{logStage || '暂无'}</pre>
              </div>
            </div>
            <button className="modal-close" onClick={() => setShowLog(false)}>关闭</button>
          </div>
        </>
      )}

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-header">
          <span>📊 个股评分排名</span>
          <span className="card-sub">N形≥60 / 龙头≥60 / 双凸≥60 / 回头≥60 / 动量≥50</span>
        </div>
        {evals.length ? (
          <div className="eval-table">
            <div className="ev-header">
              <span className="ev-code sortable" onClick={() => setSort('code')}>代码{sortArrow('code')}</span>
              <span className="ev-name sortable" onClick={() => setSort('name')}>名称{sortArrow('name')}</span>
              <span className="ev-price sortable" onClick={() => setSort('price')}>现价{sortArrow('price')}</span>
              <span className="ev-chg sortable" onClick={() => setSort('change_pct')}>涨跌{sortArrow('change_pct')}</span>
              <span className="ev-n sortable" onClick={() => setSort('n_score')} title="N形≥60可操作">N≥60{sortArrow('n_score')}</span>
              <span className="ev-dragon sortable" onClick={() => setSort('dragon_score')} title="龙头≥60买入,≥50观察">龙≥60{sortArrow('dragon_score')}</span>
              <span className="ev-db sortable" onClick={() => setSort('db_score')} title="双凸≥60买入,50-60观察">凸≥60{sortArrow('db_score')}</span>
              <span className="ev-dr sortable" onClick={() => setSort('dr_score')} title="龙回头≥60首次入场">回≥60{sortArrow('dr_score')}</span>
              <span className="ev-m sortable" onClick={() => setSort('m_score')} title="动量≥50值得看">量≥50{sortArrow('m_score')}</span>
            </div>
            <div className="ev-body">
              {sortedEvals.map((e) => (
                <div key={e.code} className={rowClass(e)}>
                  <span className="ev-code" data-label="代码">{e.code}</span>
                  <span className="ev-name" data-label="名称">{e.name || '-'}</span>
                  <span className="ev-price" data-label="现价">¥{(e.price || 0).toFixed(2)}</span>
                  <span className={['ev-chg', (e.change_pct || 0) >= 0 ? 'up' : 'down'].join(' ')} data-label="涨跌">
                    {(e.change_pct || 0) > 0 ? '+' : ''}{(e.change_pct || 0).toFixed(2)}%
                  </span>
                  <span className={scoreClass(e.n_score, e.n_pass, 80)} data-label="N形">{e.n_score > 0 ? e.n_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.dragon_score, e.dragon_pass, 80)} data-label="龙头">{e.dragon_score > 0 ? e.dragon_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.db_score, e.db_pass, 80)} data-label="双凸">{e.db_score > 0 ? e.db_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.dr_score, e.dr_pass, 80)} data-label="回头">{e.dr_score > 0 ? e.dr_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.m_score, e.m_pass, 70)} data-label="动量">{e.m_score > 0 ? e.m_score.toFixed(0) : '—'}</span>
                </div>
              ))}
            </div>
          </div>
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
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-header">📅 宏观日历</div>
        <div className="hs-cal-scroll">
          {calendarEvents.map((c, i) => (
            <div key={'c' + i} className="hs-cal-item">
              <span className="hs-cal-date">{c.datetime ? c.datetime.slice(5, 10) : ''}</span>
              <span className="hs-cal-title">{c.title}</span>
            </div>
          ))}
          {!calendarEvents.length && <div className="hs-cal-empty">暂无日历事件</div>}
        </div>
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-header">📋 IPO日历</div>
        <div className="hs-cal-scroll">
          {ipoCalendar.map((c, i) => (
            <div key={'ipo' + i} className="hs-cal-item">
              <span className="hs-cal-date">{c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : '')}</span>
              <span className="hs-cal-title">{c.name}（{c.code}）</span>
              {c.issue_price && <span className="cal-price">¥{c.issue_price.toFixed(2)}</span>}
              <span className={['cal-status', c.list_status === 'L' ? 'cal-status-l' : 'cal-status-u'].join(' ')}>{ipoCountdown(c)}</span>
            </div>
          ))}
          {!ipoCalendar.length && <div className="hs-cal-empty">暂无IPO日历</div>}
        </div>
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-header">
          📰 热点资讯
          <div className="hs-actions">
            <button className="btn-log" disabled={reanalyzing} onClick={onReanalyze}>
              {reanalyzing ? '补推中…' : '🔁 手动补推'}
            </button>
          </div>
        </div>
        {newsItems.length ? (
          <div className="hs-news-scroll">
            {newsItems.map((n, i) => (
              <div key={i} className="hs-news-item">
                <div className="hs-news-head">
                  <span className="hs-news-time">{fmtNewsTime(n.datetime)}</span>
                  <span className="hs-news-title">{n.title}</span>
                </div>
                <div className="hs-news-tags">
                  {n.sentiment && <span className={['tag', 'tag-sent-' + n.sentiment].join(' ')}>{n.sentiment}</span>}
                  {n.direction && <span className={['tag', n.direction === '利好' ? 'tag-up' : n.direction === '利空' ? 'tag-down' : 'tag-neutral'].join(' ')}>{n.direction}</span>}
                  {n.impact_level && <span className={['tag', 'tag-impact-' + n.impact_level].join(' ')}>{n.impact_level}影响</span>}
                  {n.sectors && n.sectors.map((sec) => <span key={sec} className="sector-tag">{sec}</span>)}
                  {n.stocks && n.stocks.map((stk) => <span key={stk} className="stock-tag">{stk}</span>)}
                  {!n.sentiment && !n.direction && !(n.sectors && n.sectors.length) && <span className="tag-placeholder"></span>}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="empty">暂无资讯</div>
        )}
      </div>
    </div>
  )
}
