// ── 分时图组件 KLineChart.jsx ──
// Canvas 绘制个股分时价格线、均价线、成交量柱、MACD（DIF/DEA/柱），
// 价格线按相对昨收动态红涨绿跌并填充区域，支持容器自适应、hover 十字线。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import './KLineChart.css'

// ── 布局常量（逻辑像素）──
const axisL = 48
const plotL = axisL
const plotR = 10
const axisB = 16
const plotT = 8

// 配色（浅色底、强对比，避免浅色线刺眼）
const C = {
  bg: '#ffffff',
  grid: '#ececec',
  axisTxt: '#909399',
  prev: '#c0c4cc',
  cross: '#c0c4cc',
  dot: '#c0c4cc',
  volUp: '#f5222d',
  volDown: '#16a34a',
  avg: '#fa8c16',
  dif: '#d48806',
  dea: '#1677ff',
}
// 分时价格线上涨/下跌颜色（红涨绿跌）
const PRICE_UP = '#f5222d'
const PRICE_DOWN = '#16a34a'

// 格式化成交量：>=1亿 显「亿」、>=1万 显「万」，否则原值
function fmtVol(v) {
  const n = Number(v) || 0
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿'
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return String(n)
}
// 格式化成交额：>=1亿 显「亿」、>=1万 显「万」，否则原值
function fmtAmt(v) {
  const n = Number(v) || 0
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿'
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return String(n)
}

// 根据容器宽高推导画布视图高度（限制在 0.5×宽 ~ 440px 之间）
function computeViewH(w, h) {
  return Math.min(440, Math.max(h, Math.round(w * 0.5)))
}

// 在 canvas 上按坐标数组绘制连续折线（无点则直接返回）
function drawPolyline(ctx, coords) {
  if (!coords.length) return
  ctx.beginPath()
  ctx.moveTo(coords[0][0], coords[0][1])
  for (let i = 1; i < coords.length; i++) ctx.lineTo(coords[i][0], coords[i][1])
  ctx.stroke()
}

/**
 * 分时图组件
 * Canvas 绘制个股分时价格线、均价线、成交量柱、MACD（DIF/DEA/柱），
 * 价格线按相对昨收动态红涨绿跌并填充区域，支持容器自适应与 hover 十字线。
 * @param {{code:string, name?:string, height?:number, scale?:number, count?:number}} props
 * @returns {JSX.Element}
 */
export default function KLineChart({
  code,
  name = '',
  height = 240,
  scale = 1,
  count = 241,
}) {
  const [raw, setRaw] = useState([])
  const [prevClose, setPrevClose] = useState(0)
  const [dispName, setDispName] = useState(name)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [lastClose, setLastClose] = useState(0)
  const [last, setLast] = useState(0)
  const [viewW, setViewW] = useState(axisL + 320)
  const [hover, setHover] = useState(null)

  const wrapRef = useRef(null)    // 容器 DOM 引用（取可用宽度）
  const canvasRef = useRef(null)  // 画布 DOM 引用（绘制分时图）

  useEffect(() => {
    let cancelled = false
    async function load() {
      // 拉取指定股票的分时/分钟数据并缓存到 raw，触发重绘
      if (!code) return
      setLoading(true)
      setError('')
      try {
        const data = await api.fetchMinute(code, scale, count)
        const pts = data && Array.isArray(data.points) ? data.points : null
        if (!pts) {
          if (!cancelled) setError('分时数据格式异常')
          return
        }
        if (pts.length === 0) {
          if (!cancelled) {
            setRaw([])
            if (data && data.error) setError(data.error)
          }
          return
        }
        if (cancelled) return
        setRaw(pts)
        setPrevClose(Number(data.prev_close) || 0)
        if (data.name) setDispName(data.name)
      } catch (e) {
        if (!cancelled) setError(e && e.message ? e.message : '分时加载失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [code, scale, count])

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

  useEffect(() => {
    if (raw.length) {
      const lp = raw[raw.length - 1]
      setLastClose(lp.close)
      setLast(prevClose > 0 ? ((lp.close - prevClose) / prevClose) * 100 : 0)
    }
  }, [raw, prevClose])

  useEffect(() => {
    const cvs = canvasRef.current
    if (!cvs || raw.length === 0) return

    const dpr = window.devicePixelRatio || 1
    cvs.style.width = '100%'
    const contW = Math.max(1, Math.round(cvs.clientWidth || viewW))
    const viewH = computeViewH(contW, height)
    cvs.width = Math.round(contW * dpr)
    cvs.height = Math.round(viewH * dpr)
    cvs.style.height = viewH + 'px'
    const ctx = cvs.getContext('2d')
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, contW, viewH)
    ctx.fillStyle = C.bg
    ctx.fillRect(0, 0, contW, viewH)

    const innerH = viewH - plotT - axisB
    const priceH = Math.round(innerH * 0.62)
    const volH = Math.round(innerH * 0.20)
    const macdH = innerH - priceH - volH
    const priceBottom = plotT + priceH
    const volTop = priceBottom
    const volBottom = volTop + volH
    const macdTop = volBottom
    const macdZero = macdTop + macdH / 2
    const plotW = contW - plotL - plotR

    let min = Infinity, max = -Infinity
    for (const p of raw) {
      if (p.high > max) max = p.high
      if (p.low < min) min = p.low
    }
    let lo, hi
    if (prevClose > 0 && max >= 0 && min >= 0) {
      const span = Math.max(0.015, (max - min) / (2 * prevClose))
      const half = span * 1.15
      lo = Math.min(prevClose * (1 - half), min - (max - min) * 0.05)
      hi = Math.max(prevClose * (1 + half), max + (max - min) * 0.05)
    } else {
      const pad = (max - min) * 0.06 || 0.01
      lo = min - pad
      hi = max + pad
    }
    const priceY = (v) => plotT + (hi - v) / (hi - lo) * priceH
    const macdLineY = (v, maxAbs, half) => macdZero - (v / maxAbs) * half

    const n = raw.length
    const step = plotW / n
    const cxOf = (i) => plotL + step * i + step / 2

    const points = raw.map((p, i) => ({ i, raw: p, cx: cxOf(i), yClose: priceY(p.close) }))
    const priceCoords = points.map((p) => [p.cx, p.yClose])

    let cumAmt = 0, cumVol = 0
    const avgArr = new Array(n).fill(0)
    const avgCoords = []
    for (let i = 0; i < n; i++) {
      cumAmt += raw[i].amount || 0
      cumVol += raw[i].volume || 0
      if (cumVol <= 0) continue
      const avg = cumAmt / cumVol
      avgArr[i] = avg
      avgCoords.push([cxOf(i), priceY(avg)])
    }

    let maxV = 0
    for (const p of raw) if (p.volume > maxV) maxV = p.volume
    if (maxV <= 0) maxV = 1
    const vW = Math.max(1, step * 0.6)
    const volBars = raw.map((p, i) => {
      const cx = cxOf(i)
      const h = (p.volume / maxV) * volH
      const up = prevClose > 0 ? p.close >= prevClose : p.close >= p.open
      return { x: cx - vW / 2, w: vW, y: volBottom - h, h: Math.max(0.5, h), color: up ? C.volUp : C.volDown }
    })

    let maxAbs = 0.0001
    for (const p of raw) maxAbs = Math.max(maxAbs, Math.abs(p.bar), Math.abs(p.dif), Math.abs(p.dea))
    const half = macdH / 2
    const mW = Math.max(1, step * 0.55)
    const difCoords = [], deaCoords = []
    const macdBars = raw.map((p, i) => {
      const cx = cxOf(i)
      const b = p.bar
      const hgt = (Math.abs(b) / maxAbs) * half
      const y = b >= 0 ? macdZero - hgt : macdZero
      difCoords.push([cx, macdLineY(p.dif, maxAbs, half)])
      deaCoords.push([cx, macdLineY(p.dea, maxAbs, half)])
      return { x: cx - mW / 2, w: mW, y, h: Math.max(0.5, hgt), color: b >= 0 ? C.volUp : C.volDown }
    })

    ctx.lineWidth = 1
    ctx.font = '12px monospace'
    ctx.textBaseline = 'middle'

    for (let i = 0; i <= 4; i++) {
      const v = lo + (hi - lo) * i / 4
      const y = plotT + priceH - (v - lo) / (hi - lo) * priceH
      ctx.strokeStyle = C.grid
      ctx.setLineDash([3, 3])
      ctx.beginPath(); ctx.moveTo(plotL, y); ctx.lineTo(contW - plotR, y); ctx.stroke()
      ctx.setLineDash([])
      ctx.fillStyle = C.axisTxt
      ctx.textAlign = 'right'
      ctx.fillText(v.toFixed(2), plotL - 4, y)
    }

    const prevY = priceY(prevClose)
    ctx.strokeStyle = C.prev
    ctx.setLineDash([4, 3])
    ctx.beginPath(); ctx.moveTo(plotL, prevY); ctx.lineTo(contW - plotR, prevY); ctx.stroke()
    ctx.setLineDash([])
    ctx.fillStyle = C.prev
    ctx.textAlign = 'end'
    ctx.fillText(prevClose ? prevClose.toFixed(2) : '', contW - plotR, prevY - 8)

    if (priceCoords.length > 1) {
      const up = raw[n - 1].close >= prevClose
      const cc = up ? PRICE_UP : PRICE_DOWN
      const grad = ctx.createLinearGradient(0, plotT, 0, priceBottom)
      grad.addColorStop(0, up ? 'rgba(245,34,77,0.22)' : 'rgba(22,163,74,0.22)')
      grad.addColorStop(1, up ? 'rgba(245,34,77,0.02)' : 'rgba(22,163,74,0.02)')
      ctx.fillStyle = grad
      ctx.beginPath()
      ctx.moveTo(priceCoords[0][0], priceCoords[0][1])
      for (let i = 1; i < priceCoords.length; i++) ctx.lineTo(priceCoords[i][0], priceCoords[i][1])
      ctx.lineTo(priceCoords[n - 1][0], priceBottom)
      ctx.lineTo(priceCoords[0][0], priceBottom)
      ctx.closePath()
      ctx.fill()

      ctx.lineWidth = 1.6
      ctx.lineJoin = 'round'
      for (let i = 0; i < n - 1; i++) {
        ctx.strokeStyle = points[i + 1].raw.close >= prevClose ? PRICE_UP : PRICE_DOWN
        ctx.beginPath()
        ctx.moveTo(priceCoords[i][0], priceCoords[i][1])
        ctx.lineTo(priceCoords[i + 1][0], priceCoords[i + 1][1])
        ctx.stroke()
      }
      ctx.fillStyle = cc
      ctx.beginPath(); ctx.arc(priceCoords[n - 1][0], priceCoords[n - 1][1], 2.4, 0, Math.PI * 2); ctx.fill()
    }

    ctx.strokeStyle = C.avg
    ctx.lineWidth = 1
    drawPolyline(ctx, avgCoords)

    ctx.strokeStyle = C.grid
    ctx.lineWidth = 1
    ctx.beginPath(); ctx.moveTo(plotL, volTop); ctx.lineTo(contW - plotR, volTop); ctx.stroke()
    ctx.beginPath(); ctx.moveTo(plotL, macdTop); ctx.lineTo(contW - plotR, macdTop); ctx.stroke()
    ctx.strokeStyle = C.grid
    ctx.setLineDash([3, 3])
    ctx.beginPath(); ctx.moveTo(plotL, macdZero); ctx.lineTo(contW - plotR, macdZero); ctx.stroke()
    ctx.setLineDash([])

    for (const b of volBars) {
      ctx.fillStyle = b.color
      ctx.globalAlpha = 0.45
      ctx.fillRect(b.x, b.y, b.w, b.h)
    }
    ctx.globalAlpha = 1

    for (const b of macdBars) {
      ctx.fillStyle = b.color
      ctx.globalAlpha = 0.8
      ctx.fillRect(b.x, b.y, b.w, b.h)
    }
    ctx.globalAlpha = 1

    ctx.strokeStyle = C.dif
    ctx.lineWidth = 1
    drawPolyline(ctx, difCoords)
    ctx.strokeStyle = C.dea
    drawPolyline(ctx, deaCoords)

    ctx.fillStyle = C.axisTxt
    ctx.textAlign = 'center'
    const tcount = Math.min(6, n)
    for (let i = 0; i < tcount; i++) {
      const idx = Math.round((n - 1) * i / (tcount - 1 || 1))
      const p = points[idx]
      ctx.fillText((p.raw.time || '').slice(11, 16), p.cx, viewH - 5)
    }

    if (hover) {
      ctx.strokeStyle = C.cross
      ctx.lineWidth = 1
      ctx.beginPath(); ctx.moveTo(hover.x, plotT); ctx.lineTo(hover.x, viewH - axisB); ctx.stroke()
      ctx.beginPath(); ctx.moveTo(plotL, hover.y); ctx.lineTo(contW - plotR, hover.y); ctx.stroke()
      ctx.fillStyle = C.dot
      ctx.beginPath(); ctx.arc(hover.x, hover.y, 3, 0, Math.PI * 2); ctx.fill()

      const tipX = Math.min(Math.max(hover.x - 92, axisL), contW - 184)
      const tipW = 184, tipH = 92, tipY = 4
      ctx.fillStyle = '#ffffff'
      ctx.strokeStyle = '#d0d0d0'
      ctx.fillRect(tipX, tipY, tipW, tipH)
      ctx.strokeRect(tipX, tipY, tipW, tipH)
      ctx.textAlign = 'left'
      ctx.fillStyle = '#303133'
      ctx.fillText(hover.point.time || '', tipX + 8, tipY + 12)
      const upc = hover.delta >= 0 ? PRICE_UP : PRICE_DOWN
      ctx.fillStyle = upc
      ctx.fillText('价 ' + hover.point.close.toFixed(2), tipX + 8, tipY + 30)
      ctx.fillText('涨 ' + (hover.delta >= 0 ? '+' : '') + hover.pct.toFixed(2) + '%', tipX + 96, tipY + 30)
      ctx.fillStyle = '#606266'
      ctx.fillText('开 ' + hover.point.open.toFixed(2) + ' 高 ' + hover.point.high.toFixed(2) + ' 低 ' + hover.point.low.toFixed(2), tipX + 8, tipY + 48)
      ctx.fillText('量 ' + fmtVol(hover.point.volume) + ' · 额 ' + fmtAmt(hover.point.amount), tipX + 8, tipY + 66)
      ctx.fillText('DIF ' + hover.point.dif.toFixed(3) + ' DEA ' + hover.point.dea.toFixed(3) + ' BAR ' + hover.point.bar.toFixed(3), tipX + 8, tipY + 84)
    }
  }, [raw, prevClose, viewW, height, hover])

  function onMove(ev) {
    // 鼠标移动：按横向距离就近定位数据点，计算相对昨收的涨跌幅并显示十字光标信息
    if (raw.length === 0) return
    const rect = ev.currentTarget.getBoundingClientRect()
    const lx = ev.clientX - rect.left
    const contW = Math.max(1, Math.round(ev.currentTarget.clientWidth || viewW))

    const innerH = computeViewH(contW, height) - plotT - axisB
    const priceH = Math.round(innerH * 0.62)
    const volH = Math.round(innerH * 0.20)
    const macdH = innerH - priceH - volH

    let mn = Infinity, mx = -Infinity
    for (const p of raw) { if (p.high > mx) mx = p.high; if (p.low < mn) mn = p.low }
    let lo, hi
    if (prevClose > 0 && mx >= 0 && mn >= 0) {
      const span = Math.max(0.015, (mx - mn) / (2 * prevClose))
      const half = span * 1.15
      lo = Math.min(prevClose * (1 - half), mn - (mx - mn) * 0.05)
      hi = Math.max(prevClose * (1 + half), mx + (mx - mn) * 0.05)
    } else {
      const pad = (mx - mn) * 0.06 || 0.01
      lo = mn - pad; hi = mx + pad
    }
    const plotW = contW - plotL - plotR
    const step = plotW / raw.length
    const cxOf = (i) => plotL + step * i + step / 2
    const priceY = (v) => plotT + (hi - v) / (hi - lo) * priceH

    let best = null, bestDist = Infinity
    for (let i = 0; i < raw.length; i++) {
      const cx = cxOf(i)
      const d = Math.abs(cx - lx)
      if (d < bestDist) { bestDist = d; best = { i, raw: raw[i], cx, yClose: priceY(raw[i].close) } }
    }
    if (!best) return
    const pc = prevClose
    const delta = pc > 0 ? best.raw.close - pc : 0
    const pct = pc > 0 ? (delta / pc) * 100 : 0
    setHover({ x: best.cx, y: best.yClose, point: best.raw, delta, pct })
  }
  function onLeave() { setHover(null) } // 鼠标移出画布：清除十字光标信息

  const avgMemo = React.useMemo(() => {
    const arr = new Array(raw.length).fill(0)
    let cumAmt = 0, cumVol = 0
    for (let i = 0; i < raw.length; i++) {
      cumAmt += raw[i].amount || 0
      cumVol += raw[i].volume || 0
      if (cumVol <= 0) continue
      arr[i] = cumAmt / cumVol
    }
    return arr
  }, [raw])
  function avgAt(idx) { return avgMemo[idx] || 0 } // 取某点的分时均价（无则 0）

  return (
    <div className="kline-chart">
      <div className="kline-toolbar">
        <span className="kline-title">{dispName || code} · 分时</span>
        {lastClose ? (
          <span className="kline-summary">
            现价 <b className={last >= 0 ? 'up' : 'down'}>{lastClose.toFixed(2)}</b>
            涨跌 <b className={last >= 0 ? 'up' : 'down'}>{last >= 0 ? '+' : ''}{last.toFixed(2)}%</b>
          </span>
        ) : null}
        <button className="btn-refresh" disabled={loading} onClick={() => {
          setLoading(true)
          api.fetchMinute(code, scale, count).then((data) => {
            const pts = data && Array.isArray(data.points) ? data.points : null
            if (!pts) { setError('分时数据格式异常'); return }
            setRaw(pts); setPrevClose(Number(data.prev_close) || 0)
            if (data.name) setDispName(data.name)
            if (pts.length === 0 && data && data.error) setError(data.error)
          }).catch((e) => setError(e && e.message ? e.message : '分时加载失败')).finally(() => setLoading(false))
        }}>刷新</button>
      </div>

      {loading ? <div className="kline-state">加载中…</div> : null}
      {error ? <div className="kline-state">{error}</div> : null}
      {!loading && !error && raw.length === 0 ? <div className="kline-state">暂无分时数据</div> : null}

      {!loading && !error && raw.length > 0 ? (
        <div ref={wrapRef} className="kline-wrap">
          <canvas
            ref={canvasRef}
            onMouseMove={onMove}
            onMouseLeave={onLeave}
          />
        </div>
      ) : null}

      {raw.length > 0 ? (
        <div className="kline-legend">
          <span><i style={{ background: PRICE_UP }} />价格</span>
          <span><i style={{ background: C.avg }} />均价</span>
          <span><i style={{ background: C.prev }} />昨收</span>
          <span className="macd"><i style={{ background: C.dif }} />DIF</span>
          <span className="macd"><i style={{ background: C.dea }} />DEA</span>
        </div>
      ) : null}
    </div>
  )
}
