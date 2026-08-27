// ── 自选股页面 Watchlist.jsx ──
// 展示自选股多维评分（N形/龙头/双凸/龙回头/动量），支持添加/删除/排序、展开分时+盘口。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import KLineChart from '../components/KLineChart.jsx'
import DepthPanel from '../components/DepthPanel.jsx'
import './Watchlist.css'

const CACHE_KEY = 'wl_cache_v1'

// 将自选股列表持久化到 localStorage
function persistCache(stocks) {
  try { localStorage.setItem(CACHE_KEY, JSON.stringify(stocks)) } catch (_) {}
}
// 从 localStorage 读取自选股缓存
function loadCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr : []
  } catch (_) { return [] }
}

// 根据分数与阈值返回评分单元格的 CSS 类名
function scoreClass(score, pass, strongMin) {
  if (!score || score <= 0) return 'ev-score'
  if (score >= strongMin) return 'ev-score strong'
  if (pass) return 'ev-score pass'
  return 'ev-score'
}

// 根据多维度评分决定整行高亮样式（强势/关注/普通）
function rowClass(e) {
  const strong = (e.n_score || 0) >= 80 || (e.dragon_score || 0) >= 80 || (e.db_score || 0) >= 80 || (e.dr_score || 0) >= 80 || (e.m_score || 0) >= 70
  if (strong) return 'ev-row strong'
  const watch = (e.n_score || 0) >= 60 || (e.dragon_score || 0) >= 70 || (e.db_score || 0) >= 70 || (e.dr_score || 0) >= 60 || (e.m_score || 0) >= 50
  if (watch) return 'ev-row watch'
  return 'ev-row'
}

// 安全读取字段值，字符串默认空串，数字默认 0
function val(e, key) {
  const v = e[key]
  if (typeof v === 'string') return v || ''
  return v || 0
}

/**
 * 自选股页面组件
 * 展示多维评分、支持添加/删除/排序与展开分时/盘口。
 * @returns {JSX.Element}
 */
export default function Watchlist() {
  const [stocks, setStocks] = useState([])
  const [newCode, setNewCode] = useState('')
  const [sortKey, setSortKey] = useState('')
  const [sortDir, setSortDir] = useState(-1)
  const [adding, setAdding] = useState(false)
  const [feedback, setFeedback] = useState('')
  const [feedbackType, setFeedbackType] = useState('ok')
  const [sheetStock, setSheetStock] = useState(null)
  const [klineOpenCode, setKlineOpenCode] = useState('')
  const timer = useRef(null)

  // 初始化：读取缓存、加载数据、启动 30s 轮询
  useEffect(() => {
    setStocks(loadCache())
    load()
    timer.current = setInterval(load, 30000)
    return () => { if (timer.current) clearInterval(timer.current) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 自选股变动时持久化缓存
  useEffect(() => { persistCache(stocks) }, [stocks])

  // 顶部反馈提示，2.5s 后自动消失
  function showFeedback(msg, type) {
    setFeedback(msg)
    setFeedbackType(type || 'ok')
    setTimeout(() => setFeedback(''), 2500)
  }

  // 按当前排序键计算展示列表，无排序键时按最高维度分倒序
  const sortedEvals = (() => {
    const arr = [...stocks]
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
  })()

  // 切换排序键或排序方向
  function setSort(key) {
    if (sortKey === key) setSortDir((d) => d * -1)
    else { setSortKey(key); setSortDir(-1) }
  }
  function sortArrow(key) {
    if (sortKey !== key) return ''
    return sortDir === -1 ? ' ▼' : ' ▲'
  }

  // 加载自选行情、评估数据并合并快照信息
  async function load() {
    try {
      const st = await api.fetchStatus()
      const hasEmptyCode = stocks.some((s) => !s.code)
      if (!api.isTradingSession(st.session) && stocks.length && !hasEmptyCode) return
      api.setLastSession(st.session)
      const [snap, wl, ev] = await Promise.all([
        api.fetchSnapshot(), api.fetchWatchlist(), api.fetchEvaluations(),
      ])
      const wlStocks = (wl.stocks || []).map((c) => (typeof c === 'object' ? c : { code: c }))
      const codes = wlStocks.map((c) => c.code)
      if (!codes.length) { setStocks([]); return }
      const wlMap = {}
      wlStocks.forEach((c) => { wlMap[c.code] = c })
      const evMap = {}
      if (ev) ev.forEach((e) => { evMap[e.code] = e })
      const wlRow = (c) => {
        const code = typeof c === 'string' ? c : (c && c.code)
        return {
          code,
          name: wlMap[code]?.name || evMap[code]?.name || code,
          price: Number(wlMap[code]?.price) || 0,
          change_pct: Number(wlMap[code]?.change_pct) || 0,
          n_score: evMap[code]?.n_score || 0, n_pass: evMap[code]?.n_pass || false,
          dragon_score: evMap[code]?.dragon_score || 0, dragon_pass: evMap[code]?.dragon_pass || false,
          db_score: evMap[code]?.db_score || 0, db_pass: evMap[code]?.db_pass || false,
          dr_score: evMap[code]?.dr_score || 0, dr_pass: evMap[code]?.dr_pass || false,
          m_score: evMap[code]?.m_score || 0, m_pass: evMap[code]?.m_pass || false,
        }
      }
      let list
      if (snap && snap.length) {
        list = snap
          .filter((s) => codes.includes(s.code))
          .map((s) => {
            const base = wlRow(s.code)
            return {
              ...base,
              name: s.name || base.name,
              price: Number(s.price) || base.price,
              change_pct: Number(s.change_pct) ?? base.change_pct,
            }
          })
      } else if (ev && ev.length) {
        list = ev.filter((e) => codes.includes(e.code)).map((e) => wlRow(e.code))
      } else {
        list = []
      }
      const known = {}
      list.forEach((s) => { known[s.code] = true })
      for (const c of codes) {
        if (!known[c]) list.push(wlRow(c))
      }
      setStocks(list)
    } catch (_) { /* 保留旧数据 */ }
  }

  // 添加新自选股代码并立即同步后端
  async function add() {
    const code = newCode.trim()
    if (!code || adding) return
    setAdding(true)
    setFeedback('')
    try {
      const res = await api.addWatchlist(code)
      setNewCode('')
      if (res && res.stock) {
        const row = {
          code: res.stock.code || code,
          name: res.stock.name || code,
          price: res.stock.price || 0,
          change_pct: res.stock.change_pct || 0,
          n_score: 0, n_pass: false,
          dragon_score: 0, dragon_pass: false,
          db_score: 0, db_pass: false,
          dr_score: 0, dr_pass: false,
          m_score: 0, m_pass: false,
        }
        setStocks((prev) => [...prev.filter((s) => s.code !== row.code), row])
      } else if (!res || !res.duplicate) {
        setStocks((prev) => [...prev, { code, name: code, price: 0, change_pct: 0 }])
      }
      showFeedback('已添加 ' + code, 'ok')
    } catch (e) { showFeedback('添加失败: ' + e.message, 'err') }
    setAdding(false)
  }

  // 删除指定自选股
  async function remove(code) {
    try {
      await api.removeWatchlist(code)
      setStocks((prev) => prev.filter((s) => s.code !== code))
      showFeedback('已移除 ' + code, 'ok')
    } catch (e) { showFeedback('删除失败: ' + e.message, 'err') }
  }

  // 展开/收起指定代码的分时图
  function toggleKline(code) {
    setKlineOpenCode((c) => (c === code ? '' : code))
  }

  function onRowTap(e) {
    if (window.innerWidth > 768) return
    setSheetStock(e)
  }

  return (
    <div className="watchlist-page">
      <div className="page-header">
        <h2>自选股</h2>
        <div className="add-row">
          <input value={newCode} placeholder="输入代码 (如 000001)" onChange={(e) => setNewCode(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') add() }} disabled={adding} />
          <button onClick={add} className="btn-add" disabled={adding}>{adding ? '添加中…' : '添加'}</button>
          {feedback && <span className={'feedback ' + feedbackType}>{feedback}</span>}
        </div>
      </div>

      {stocks.length > 0 && (
        <div className="eval-table">
          <div className="ev-header">
            <span className="ev-code sortable" onClick={() => setSort('code')}>代码{sortArrow('code')}</span>
            <span className="ev-name sortable" onClick={() => setSort('name')}>名称{sortArrow('name')}</span>
            <span className="ev-price sortable" onClick={() => setSort('price')}>现价{sortArrow('price')}</span>
            <span className="ev-chg sortable" onClick={() => setSort('change_pct')}>涨跌{sortArrow('change_pct')}</span>
            <span className="ev-n sortable" onClick={() => setSort('n_score')} title="N形≥60可操作">N≥60{sortArrow('n_score')}</span>
            <span className="ev-dragon sortable" onClick={() => setSort('dragon_score')} title="龙头≥70买入,≥50观察">龙≥70{sortArrow('dragon_score')}</span>
            <span className="ev-db sortable" onClick={() => setSort('db_score')} title="双凸≥70买入,50-70观察">凸≥70{sortArrow('db_score')}</span>
            <span className="ev-dr sortable" onClick={() => setSort('dr_score')} title="龙回头≥60首次入场">回≥60{sortArrow('dr_score')}</span>
            <span className="ev-m sortable" onClick={() => setSort('m_score')} title="动量≥50值得看">量≥50{sortArrow('m_score')}</span>
            <span className="ev-k">K线</span>
            <span className="ev-act">操作</span>
          </div>
          <div className="ev-body">
            {sortedEvals.map((e) => (
              <div key={e.code} className="ev-row-group">
                <div className={rowClass(e)} onClick={() => onRowTap(e)}>
                  <span className="ev-code" data-label="代码">{e.code}</span>
                  <span className="ev-name" data-label="名称">{e.name || '-'}</span>
                  <span className="ev-price" data-label="现价">¥{(e.price || 0).toFixed(2)}</span>
                  <span className={'ev-chg ' + ((e.change_pct || 0) >= 0 ? 'up' : 'down')} data-label="涨跌">
                    {(e.change_pct || 0) > 0 ? '+' : ''}{(e.change_pct || 0).toFixed(2)}%
                  </span>
                  <span className={scoreClass(e.n_score, e.n_pass, 80)} data-label="N形">{e.n_score > 0 ? e.n_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.dragon_score, e.dragon_pass, 80)} data-label="龙头">{e.dragon_score > 0 ? e.dragon_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.db_score, e.db_pass, 80)} data-label="双凸">{e.db_score > 0 ? e.db_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.dr_score, e.dr_pass, 80)} data-label="回头">{e.dr_score > 0 ? e.dr_score.toFixed(0) : '—'}</span>
                  <span className={scoreClass(e.m_score, e.m_pass, 70)} data-label="动量">{e.m_score > 0 ? e.m_score.toFixed(0) : '—'}</span>
                  <span data-label="K线"><button className="btn-kline" onClick={(ev) => { ev.stopPropagation(); toggleKline(e.code) }} title={klineOpenCode === e.code ? '收起分时' : '展开分时'}>{klineOpenCode === e.code ? '收起' : '分时'}</button></span>
                  <span data-label="操作"><button className="btn-remove" onClick={(ev) => { ev.stopPropagation(); remove(e.code) }}>✕</button></span>
                </div>
                {klineOpenCode === e.code && (
                  <div className="ev-kline-row">
                    <div className="kline-flex">
                      <div className="kline-main"><KLineChart key={e.code} code={e.code} name={e.name} /></div>
                      <div className="depth-side"><DepthPanel code={e.code} name={e.name} /></div>
                    </div>
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}
      {stocks.length === 0 && <div className="empty">暂无自选股，输入代码添加</div>}

      <div className="legend">
        <span className="lg-strong">≥80 强势</span>
        <span className="lg-pass">≥门槛 达标</span>
        <span className="lg-low">&lt;门槛 偏低</span>
        <span className="lg-sep">|</span>
        <span className="lg-item">N形≥60操作, 龙头≥70买入/≥50观察, 双凸≥70买入/50-70观察, 回头≥60入场, 动量≥50关注</span>
        <span className="lg-sep">|</span>
        <span className="lg-item">点击表头排序</span>
      </div>

      {sheetStock && (
        <div className="sheet-overlay" onClick={() => setSheetStock(null)}>
          <div className="action-sheet" onClick={(e) => e.stopPropagation()}>
            <div className="sheet-title">{sheetStock.code} {sheetStock.name || ''}</div>
            <button className="sheet-btn" onClick={() => { toggleKline(sheetStock.code); setSheetStock(null) }}>
              {klineOpenCode === sheetStock.code ? '收起分时' : '展开分时'}
            </button>
            <button className="sheet-btn sheet-danger" onClick={() => { const c = sheetStock.code; setSheetStock(null); remove(c) }}>删除</button>
            <button className="sheet-btn sheet-cancel" onClick={() => setSheetStock(null)}>取消</button>
          </div>
        </div>
      )}
    </div>
  )
}
