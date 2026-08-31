// ── 盘口面板组件 DepthPanel.jsx ──
// Canvas 展示个股买卖盘口（最多十档）、现价涨跌与委比/封单等派生因子。
// 自动按实际档位数撑高，避免行距过密；无数据的档位自动隐藏。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import './DepthPanel.css'

// 盘口面板配色常量（文字/现价底/买卖盘/涨跌/数据源色）
const C = {
  bg: '#ffffff',
  lv: '#606266',
  vol: '#909399',
  nowBg: '#f5f7fa',
  ask: '#16a34a',
  bid: '#f5222d',
  up: '#f5222d',
  down: '#16a34a',
  src: '#1677ff',
}

// 格式化价格：保留两位小数，空值返回占位符 '--'
function fmtPrice(v) {
  const n = Number(v) || 0
  return n ? n.toFixed(2) : '--'
}
// 格式化成交量：>=1万 时换算为「万」并保留 1 位小数，空值返回 '--'
function fmtVol(v) {
  const n = Number(v) || 0
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return n ? String(Math.round(n)) : '--'
}

/**
 * 盘口面板组件
 * 展示个股买卖盘口（最多十档）、现价涨跌与委比/封单等派生因子。
 * 自动按实际档位数撑高，无数据的档位自动隐藏。
 * @param {{code:string, name?:string, height?:number}} props
 * @returns {JSX.Element}
 */
export default function DepthPanel({ code, name = '', height = 260 }) {
  const [ob, setOb] = useState({})
  const [factors, setFactors] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [dispName, setDispName] = useState(name)
  const [viewW, setViewW] = useState(300)

  const wrapRef = useRef(null)    // 容器 DOM 引用（取可用宽度）
  const canvasRef = useRef(null)  // 画布 DOM 引用（绘制盘口图）

  useEffect(() => {
    if (!code) return
    let cancelled = false
    async function load() {
      // 拉取指定股票的实时盘口（买卖五档/十档 + 盘口因子），更新状态
      setLoading(true)
      setError('')
      try {
        const data = await api.fetchDepth(code)
        if (data && data.bids) {
          if (cancelled) return
          setOb(data)
          setFactors(data.factors || null)
          if (data.name) setDispName(data.name)
        } else {
          if (!cancelled) setError('盘口数据格式异常')
        }
      } catch (e) {
        if (!cancelled) setError(e && e.message ? e.message : '盘口加载失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [code])

  useEffect(() => {
    if (!wrapRef.current) return
    setViewW(wrapRef.current.clientWidth)
    if (typeof ResizeObserver !== 'undefined') {
      const ro = new ResizeObserver(() => {
        if (wrapRef.current) setViewW(wrapRef.current.clientWidth)
      })
      ro.observe(wrapRef.current)
      return () => ro.disconnect()
    } else {
      const t = setInterval(() => {
        if (wrapRef.current) {
          const w = wrapRef.current.clientWidth
          setViewW((p) => (p === w ? p : w))
        }
      }, 500)
      return () => clearInterval(t)
    }
  }, [])

  const pctText = (() => {
    const p = ob.price || 0
    const pc = ob.prev_close || 0
    if (!p || !pc) return '--'
    const d = (p - pc) / pc * 100
    return (d >= 0 ? '+' : '') + d.toFixed(2) + '%'
  })()
  const nowCls = pctText.startsWith('+') ? 'up' : 'down'

  useEffect(() => {
    const cvs = canvasRef.current
    if (!cvs || !ob || !ob.bids || !ob.bids.length) return

    const dpr = window.devicePixelRatio || 1
    cvs.style.width = '100%'
    const W = Math.max(1, Math.round(cvs.clientWidth || viewW))

    let levelCount = 0
    const maxL = Math.min(ob.bids.length, 10)
    for (let i = 0; i < maxL; i++) {
      const b = ob.bids[i], a = ob.asks ? ob.asks[i] : null
      if ((b && b.price > 0) || (a && a.price > 0)) levelCount = i + 1
    }
    if (levelCount === 0) levelCount = 5
    const L = levelCount

    const rowH = 22
    const topPad = 4, botPad = 4
    const factorH = factors ? 66 : 0
    const H = topPad + (L * 2 + 1) * rowH + (factors ? factorH + 6 : 0) + botPad

    cvs.width = Math.round(W * dpr)
    cvs.height = Math.round(H * dpr)
    cvs.style.height = H + 'px'
    const ctx = cvs.getContext('2d')
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, W, H)
    ctx.fillStyle = C.bg
    ctx.fillRect(0, 0, W, H)

    const rows = []
    for (let i = 0; i < L; i++) {
      const label = L - i
      const idx = L - 1 - i
      const a = ob.asks ? ob.asks[idx] : null
      rows.push({ side: 'ask', lv: '卖' + label, price: a ? a.price : 0, vol: a ? a.volume : 0 })
    }
    rows.push({ now: true, lv: ob.name || code, price: ob.price || 0, volText: pctText })
    for (let i = 0; i < L; i++) {
      const b = ob.bids ? ob.bids[i] : null
      rows.push({ side: 'bid', lv: '买' + (i + 1), price: b ? b.price : 0, vol: b ? b.volume : 0 })
    }

    let maxVol = 0
    for (const r of rows) if (r.vol > maxVol) maxVol = r.vol
    if (maxVol <= 0) maxVol = 1

    const col1 = 8
    const labelW = 44
    const priceX = col1 + labelW
    const priceW = 70
    const volRight = W - col1
    const volAreaLeft = priceX + priceW + 6
    const volAreaRight = volRight

    ctx.font = '13px monospace'
    ctx.textBaseline = 'middle'

    rows.forEach((r, ri) => {
      const y = topPad + ri * rowH
      const cy = y + rowH / 2
      if (r.now) {
        ctx.fillStyle = C.nowBg
        ctx.fillRect(col1, y, W - col1 * 2, rowH)
        ctx.fillStyle = C.lv
        ctx.textAlign = 'left'
        ctx.fillText(r.lv, col1, cy)
        const pcolor = r.volText && r.volText.startsWith('+') ? C.up : C.down
        ctx.fillStyle = pcolor
        ctx.textAlign = 'right'
        ctx.fillText(fmtPrice(r.price), volRight, cy)
        ctx.fillStyle = pcolor
        ctx.fillText(r.volText, col1 + 120, cy)
      } else {
        if (r.vol > 0) {
          const bw = (r.vol / maxVol) * (volAreaRight - volAreaLeft)
          ctx.fillStyle = r.side === 'ask' ? 'rgba(22,163,74,0.14)' : 'rgba(245,34,77,0.14)'
          ctx.fillRect(volAreaLeft, y + 3, bw, rowH - 6)
        }
        ctx.fillStyle = C.lv
        ctx.textAlign = 'left'
        ctx.fillText(r.lv, col1, cy)
        ctx.fillStyle = r.side === 'ask' ? C.ask : C.bid
        ctx.textAlign = 'left'
        ctx.fillText(fmtPrice(r.price), priceX, cy)
        ctx.fillStyle = C.vol
        ctx.textAlign = 'right'
        ctx.fillText(fmtVol(r.vol), volRight, cy)
      }
    })

    if (factors) {
      let fy = topPad + (L * 2 + 1) * rowH + 18
      ctx.font = '12px monospace'
      const F = {
        bid_ask_ratio: Number(factors.bid_ask_ratio) || 0,
        bid_vol: Number(factors.bid_vol) || 0,
        ask_vol: Number(factors.ask_vol) || 0,
        seal_bid: Number(factors.seal_bid) || 0,
        seal_ask: Number(factors.seal_ask) || 0,
        spread_pct: Number(factors.spread_pct) || 0,
        near_pct: Number(factors.near_pct) || 0,
      }
      const drawPair = (a, b) => {
        ctx.textAlign = 'left'
        ctx.fillStyle = C.lv
        ctx.fillText(a.label, col1, fy)
        ctx.fillStyle = a.color || C.lv
        ctx.fillText(a.val, col1 + 44, fy)
        ctx.fillStyle = C.lv
        ctx.fillText(b.label, col1 + 150, fy)
        ctx.fillStyle = b.color || C.lv
        ctx.fillText(b.val, col1 + 194, fy)
        fy += 22
      }
      drawPair(
        { label: '委比', val: (F.bid_ask_ratio * 100).toFixed(1) + '%', color: F.bid_ask_ratio >= 0 ? C.up : C.down },
        { label: '买/卖量', val: fmtVol(F.bid_vol) + '/' + fmtVol(F.ask_vol) }
      )
      drawPair(
        { label: '买一封单', val: fmtVol(F.seal_bid), color: C.up },
        { label: '卖一封单', val: fmtVol(F.seal_ask), color: C.down }
      )
      drawPair(
        { label: '价差', val: F.spread_pct.toFixed(3) + '%' },
        { label: '覆盖', val: F.near_pct.toFixed(2) + '%' }
      )
    }
  }, [ob, factors, viewW, height, pctText])

  return (
    <div className="depth-panel">
      <div className="depth-toolbar">
        <span className="depth-title">{ob.name || dispName || code} · 盘口</span>
        {ob.time ? (
          <span className="depth-time">
            {ob.time} {ob.source ? <i className="src">{ob.source}</i> : null}
          </span>
        ) : null}
        <button className="btn-refresh" disabled={loading} onClick={() => {
          setLoading(true); setError('')
          api.fetchDepth(code).then((data) => {
            if (data && data.bids) { setOb(data); setFactors(data.factors || null); if (data.name) setDispName(data.name) }
            else setError('盘口数据格式异常')
          }).catch((e) => setError(e && e.message ? e.message : '盘口加载失败')).finally(() => setLoading(false))
        }}>刷新</button>
      </div>

      {loading ? <div className="depth-state">加载中…</div> : null}
      {error ? <div className="depth-state">{error}</div> : null}

      {!loading && !error && ob.bids && ob.bids.length ? (
        <div ref={wrapRef}>
          <canvas ref={canvasRef} />
        </div>
      ) : null}
    </div>
  )
}
