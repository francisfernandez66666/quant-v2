// ── 盘口面板组件 DepthPanel.jsx ──
// 展示个股买卖五档盘口、现价涨跌与委比/封单等派生因子，基于 Canvas 绘制。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import './DepthPanel.css'

// 配色（与 Vue 源完全一致）
const C = {
  bg: '#101828',
  lv: '#7d8fab',
  vol: '#b8c7dd',
  nowBg: '#16223a',
  now: '#e8f0fe',
  ask: '#3ddc84',   // 卖盘绿
  bid: '#ff6b5a',   // 买盘红
  up: '#ff6b5a',
  down: '#3ddc84',
  src: '#5b8ff9',
}

// 格式化价格，空值显示 --
function fmtPrice(v) {
  const n = Number(v) || 0
  return n ? n.toFixed(2) : '--'
}
// 格式化盘口量为中文单位
function fmtVol(v) {
  const n = Number(v) || 0
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return n ? String(Math.round(n)) : '--'
}

/**
 * 盘口面板组件
 * @param {{code:string, name?:string, height?:number}} props
 * @returns {JSX.Element}
 */
export default function DepthPanel({ code, name = '', height = 260 }) {
  const [ob, setOb] = useState({})
  const [factors, setFactors] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [dispName, setDispName] = useState(name)
  const [viewW, setViewW] = useState(320)

  const wrapRef = useRef(null)
  const canvasRef = useRef(null)

  // 拉取盘口（code 变化时重新加载）
  useEffect(() => {
    if (!code) return
    let cancelled = false
    async function load() {
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

  // 容器宽度自适应（ResizeObserver，退化轮询）
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

  // 现价相对昨收涨跌幅文本 + 颜色
  const pctText = (() => {
    const p = ob.price || 0
    const pc = ob.prev_close || 0
    if (!p || !pc) return '--'
    const d = (p - pc) / pc * 100
    return (d >= 0 ? '+' : '') + d.toFixed(2) + '%'
  })()
  const nowCls = pctText.startsWith('+') ? 'up' : 'down'

  // 绘制盘口阶梯（canvas，devicePixelRatio 缩放）
  useEffect(() => {
    const cvs = canvasRef.current
    if (!cvs || !ob || !ob.bids || !ob.bids.length) return

    const dpr = window.devicePixelRatio || 1
    const W = viewW
    const H = height
    cvs.width = Math.round(W * dpr)
    cvs.height = Math.round(H * dpr)
    cvs.style.width = W + 'px'
    cvs.style.height = H + 'px'
    const ctx = cvs.getContext('2d')
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, W, H)
    ctx.fillStyle = C.bg
    ctx.fillRect(0, 0, W, H)

    // 档位
    const levels = Number(ob.levels) || Math.min(ob.bids.length || 0, 5) || 5
    const showLevels = Math.min(levels, 10)

    // 构建行：卖 high→low ... 现价 ... 买 low→high
    const rows = []
    for (let i = 0; i < showLevels; i++) {
      const label = showLevels - i
      const idx = showLevels - 1 - i
      const a = ob.asks ? ob.asks[idx] : null
      rows.push({ lv: '卖' + label, price: a ? a.price : 0, vol: a ? a.volume : 0, color: C.ask, best: i === showLevels - 1 })
    }
    rows.push({ now: true, lv: ob.name || code, price: ob.price || 0, volText: pctText, color: nowCls })
    for (let i = 0; i < showLevels; i++) {
      const label = i + 1
      const b = ob.bids ? ob.bids[i] : null
      rows.push({ lv: '买' + label, price: b ? b.price : 0, vol: b ? b.volume : 0, color: C.bid, best: i === 0 })
    }

    const factorH = factors ? 78 : 0
    const rowH = (H - 8 - factorH) / rows.length
    const col1 = 6, col1W = 56
    const priceX = col1 + col1W
    const priceW = (W - priceX) / 2
    const volX = priceX + priceW

    ctx.font = '14px monospace'
    ctx.textBaseline = 'middle'

    rows.forEach((r, ri) => {
      const y = 4 + ri * rowH
      const cy = y + rowH / 2
      // 高亮最优档
      if (r.best) {
        ctx.fillStyle = 'rgba(255,255,255,0.04)'
        ctx.fillRect(col1, y, W - col1 * 2, rowH)
      }
      if (r.now) {
        ctx.fillStyle = C.nowBg
        ctx.fillRect(col1, y, W - col1 * 2, rowH)
        ctx.fillStyle = C.now
        ctx.textAlign = 'left'
        ctx.fillText(r.lv, col1, cy)
        const pcolor = r.color === 'up' ? C.up : C.down
        ctx.fillStyle = pcolor
        ctx.textAlign = 'right'
        ctx.fillText(fmtPrice(r.price), volX - 6, cy)
        ctx.fillStyle = C.vol
        ctx.fillText(r.volText, W - col1, cy)
      } else {
        ctx.fillStyle = C.lv
        ctx.textAlign = 'left'
        ctx.fillText(r.lv, col1, cy)
        ctx.fillStyle = r.color
        ctx.textAlign = 'right'
        ctx.fillText(fmtPrice(r.price), volX - 6, cy)
        ctx.fillStyle = C.vol
        ctx.fillText(fmtVol(r.vol), W - col1, cy)
      }
    })

    // 因子区
    if (factors) {
      let fy = H - factorH + 14
      ctx.font = '13px monospace'
      const labelColor = C.lv
      const drawRow = (items) => {
        let x = col1
        ctx.textAlign = 'left'
        items.forEach((it) => {
          ctx.fillStyle = labelColor
          ctx.fillText(it.label, x, fy)
          x += 52
          ctx.fillStyle = it.color || C.now
          ctx.fillText(it.val, x, fy)
          x += 70
        })
        fy += 22
      }
      const F = {
        bid_ask_ratio: Number(factors.bid_ask_ratio) || 0,
        bid_vol: Number(factors.bid_vol) || 0,
        ask_vol: Number(factors.ask_vol) || 0,
        seal_bid: Number(factors.seal_bid) || 0,
        seal_ask: Number(factors.seal_ask) || 0,
        spread_pct: Number(factors.spread_pct) || 0,
        near_pct: Number(factors.near_pct) || 0,
      }
      drawRow([
        { label: '委比', val: (F.bid_ask_ratio * 100).toFixed(1) + '%', color: F.bid_ask_ratio >= 0 ? C.up : C.down },
        { label: '买/卖量', val: fmtVol(F.bid_vol) + ' / ' + fmtVol(F.ask_vol) },
      ])
      drawRow([
        { label: '买一封单', val: fmtVol(F.seal_bid), color: C.up },
        { label: '卖一封单', val: fmtVol(F.seal_ask), color: C.down },
      ])
      drawRow([
        { label: '价差', val: F.spread_pct.toFixed(3) + '%' },
        { label: '覆盖', val: F.near_pct.toFixed(2) + '%' },
      ])
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
