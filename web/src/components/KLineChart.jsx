// ── 分时图组件 KLineChart.jsx ──
// 基于 Canvas 绘制个股分时价格线、均价线、成交量柱、MACD（DIF/DEA/柱），
// 支持容器自适应、hover 十字线与数据提示。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import './KLineChart.css'

// ── 布局常量（逻辑单位 = 实际像素，与 Vue 版一致）──
const axisL = 50               // 左侧价格刻度宽度
const plotL = axisL            // 绘图区左边界
const plotR = 8                // 右留白
const axisB = 16               // 底部时间刻度高度
const plotT = 6                // 顶部留白

// 配色（与 Vue 源完全一致）
const difColor = '#f6b73c'
const deaColor = '#5b8ff9'
const avgColor = '#f0b90b'
const priceColor = '#7cd6ff'
const C = {
  bg: '#101828',
  grid: '#2a3a55',
  panel: '#24334d',
  axisTxt: '#7d8fab',
  prev: '#f0b90b',
  cross: '#f0b90b',
  dot: '#7cb3ff',
  volUp: '#e2432a',
  volDown: '#1fa747',
}

// 格式化成交量/委托量为中文单位
function fmtVol(v) {
  const n = Number(v) || 0
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿'
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return String(n)
}
// 格式化成交额为中文单位
function fmtAmt(v) {
  const n = Number(v) || 0
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿'
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return String(n)
}

// 在 canvas 上绘制折线
function drawPolyline(ctx, coords) {
  if (!coords.length) return
  ctx.beginPath()
  ctx.moveTo(coords[0][0], coords[0][1])
  for (let i = 1; i < coords.length; i++) ctx.lineTo(coords[i][0], coords[i][1])
  ctx.stroke()
}

/**
 * 分时图组件
 * @param {{code:string, name?:string, height?:number, scale?:number, count?:number}} props
 * @returns {JSX.Element}
 */
export default function KLineChart({
  code,
  name = '',
  height = 220,
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
  const [viewW, setViewW] = useState(axisL + 300)
  const [hover, setHover] = useState(null)

  const wrapRef = useRef(null)
  const canvasRef = useRef(null)

  // 拉取分时数据（code / scale / count 变化时重新加载）
  useEffect(() => {
    let cancelled = false
    async function load() {
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
          if (!cancelled) setRaw([])
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

  // 回填最新价 / 涨跌幅
  useEffect(() => {
    if (raw.length) {
      const lp = raw[raw.length - 1]
      setLastClose(lp.close)
      setLast(prevClose > 0 ? ((lp.close - prevClose) / prevClose) * 100 : 0)
    }
  }, [raw, prevClose])

  // 绘制（canvas，devicePixelRatio 缩放）
  useEffect(() => {
    const cvs = canvasRef.current
    if (!cvs || raw.length === 0) return

    const dpr = window.devicePixelRatio || 1
    const viewH = Math.max(height, Math.round(viewW * 0.55))
    cvs.width = Math.round(viewW * dpr)
    cvs.height = Math.round(viewH * dpr)
    cvs.style.width = viewW + 'px'
    cvs.style.height = viewH + 'px'
    const ctx = cvs.getContext('2d')
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, viewW, viewH)
    ctx.fillStyle = C.bg
    ctx.fillRect(0, 0, viewW, viewH)

    const innerH = viewH - plotT - axisB
    const macdH = Math.round(innerH * 0.32)
    const volH = Math.round(innerH * 0.18)
    const mainH = innerH - macdH - volH
    const priceBottom = plotT + mainH
    const volTop = priceBottom
    const volBottom = volTop + volH
    const macdTop = volBottom
    const macdZero = macdTop + macdH / 2
    const plotW = viewW - plotL - plotR

    // 价格区间（绕昨收对称，兜底防溢出）
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
    const priceY = (v) => plotT + (hi - v) / (hi - lo) * mainH
    const macdLineY = (v, maxAbs, half) => macdZero - (v / maxAbs) * half

    const n = raw.length
    const step = plotW / n
    const cxOf = (i) => plotL + step * i + step / 2

    // 分时点坐标
    const points = raw.map((p, i) => ({ i, raw: p, cx: cxOf(i), yClose: priceY(p.close) }))
    const priceCoords = points.map((p) => [p.cx, p.yClose])

    // 累计均价
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

    // 成交量柱
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

    // MACD 柱 + DIF/DEA 线
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
    ctx.font = '14px monospace'
    ctx.textBaseline = 'middle'

    // 价格区网格 + 左侧刻度
    for (let i = 0; i <= 4; i++) {
      const v = lo + (hi - lo) * i / 4
      const y = plotT + mainH - (v - lo) / (hi - lo) * mainH
      ctx.strokeStyle = C.grid
      ctx.setLineDash([3, 3])
      ctx.beginPath(); ctx.moveTo(plotL, y); ctx.lineTo(viewW - plotR, y); ctx.stroke()
      ctx.setLineDash([])
      ctx.fillStyle = C.axisTxt
      ctx.textAlign = 'right'
      ctx.fillText(v.toFixed(2), plotL - 4, y)
    }

    // 昨收参考线
    const prevY = priceY(prevClose)
    ctx.strokeStyle = C.prev
    ctx.setLineDash([4, 3])
    ctx.beginPath(); ctx.moveTo(plotL, prevY); ctx.lineTo(viewW - plotR, prevY); ctx.stroke()
    ctx.setLineDash([])
    ctx.fillStyle = C.prev
    ctx.textAlign = 'end'
    ctx.fillText(prevClose ? prevClose.toFixed(2) : '', viewW - plotR, prevY - 8)

    // 价格线 / 均价线
    ctx.strokeStyle = priceColor
    ctx.lineWidth = 1.4
    drawPolyline(ctx, priceCoords)
    ctx.strokeStyle = avgColor
    ctx.lineWidth = 1
    drawPolyline(ctx, avgCoords)

    // 成交量区 / MACD 区分隔线
    ctx.strokeStyle = C.panel
    ctx.lineWidth = 1
    ctx.beginPath(); ctx.moveTo(plotL, volTop); ctx.lineTo(viewW - plotR, volTop); ctx.stroke()
    ctx.beginPath(); ctx.moveTo(plotL, macdTop); ctx.lineTo(viewW - plotR, macdTop); ctx.stroke()
    // MACD 零轴
    ctx.strokeStyle = C.grid
    ctx.setLineDash([3, 3])
    ctx.beginPath(); ctx.moveTo(plotL, macdZero); ctx.lineTo(viewW - plotR, macdZero); ctx.stroke()
    ctx.setLineDash([])

    // 成交量柱
    for (const b of volBars) {
      ctx.fillStyle = b.color
      ctx.globalAlpha = 0.5
      ctx.fillRect(b.x, b.y, b.w, b.h)
    }
    ctx.globalAlpha = 1

    // MACD 柱
    for (const b of macdBars) {
      ctx.fillStyle = b.color
      ctx.globalAlpha = 0.8
      ctx.fillRect(b.x, b.y, b.w, b.h)
    }
    ctx.globalAlpha = 1

    // DIF / DEA 线
    ctx.strokeStyle = difColor
    ctx.lineWidth = 1
    drawPolyline(ctx, difCoords)
    ctx.strokeStyle = deaColor
    drawPolyline(ctx, deaCoords)

    // X 轴时间刻度（≤6 个）
    ctx.fillStyle = C.axisTxt
    ctx.textAlign = 'center'
    const tcount = Math.min(6, n)
    for (let i = 0; i < tcount; i++) {
      const idx = Math.round((n - 1) * i / (tcount - 1 || 1))
      const p = points[idx]
      ctx.fillText((p.raw.time || '').slice(11, 16), p.cx, viewH - 5)
    }

    // hover 十字线 + 提示
    if (hover) {
      ctx.strokeStyle = C.cross
      ctx.lineWidth = 1
      ctx.beginPath(); ctx.moveTo(hover.x, plotT); ctx.lineTo(hover.x, viewH - axisB); ctx.stroke()
      ctx.beginPath(); ctx.moveTo(plotL, hover.y); ctx.lineTo(viewW - plotR, hover.y); ctx.stroke()
      ctx.fillStyle = C.dot
      ctx.beginPath(); ctx.arc(hover.x, hover.y, 3, 0, Math.PI * 2); ctx.fill()

      const tipX = Math.min(Math.max(hover.x - 84, axisL), viewW - 168)
      const tipW = 168, tipH = 92, tipY = 4
      ctx.fillStyle = '#0a0f1c'
      ctx.strokeStyle = '#2e4161'
      ctx.fillRect(tipX, tipY, tipW, tipH)
      ctx.strokeRect(tipX, tipY, tipW, tipH)
      ctx.textAlign = 'left'
      ctx.fillStyle = '#e8f0fe'
      ctx.fillText(hover.point.time || '', tipX + 6, tipY + 12)
      const upc = hover.delta >= 0 ? '#ff6b5a' : '#3ddc84'
      ctx.fillStyle = upc
      ctx.fillText('价 ' + hover.point.close.toFixed(2), tipX + 6, tipY + 30)
      ctx.fillText('涨 ' + (hover.delta >= 0 ? '+' : '') + hover.pct.toFixed(2) + '%', tipX + 90, tipY + 30)
      ctx.fillStyle = '#e8f0fe'
      ctx.fillText('开 ' + hover.point.open.toFixed(2) + ' 高 ' + hover.point.high.toFixed(2) + ' 低 ' + hover.point.low.toFixed(2), tipX + 6, tipY + 48)
      ctx.fillText('量 ' + fmtVol(hover.point.volume) + ' · 额 ' + fmtAmt(hover.point.amount), tipX + 6, tipY + 66)
      ctx.fillText('DIF ' + hover.point.dif.toFixed(3) + ' DEA ' + hover.point.dea.toFixed(3) + ' BAR ' + hover.point.bar.toFixed(3), tipX + 6, tipY + 84)
    }
  }, [raw, prevClose, viewW, height, hover])

  function onMove(ev) {
    if (raw.length === 0) return
    const rect = ev.currentTarget.getBoundingClientRect()
    const lx = ev.clientX - rect.left

    const innerH = Math.max(height, Math.round(viewW * 0.55)) - plotT - axisB
    const macdH = Math.round(innerH * 0.32)
    const volH = Math.round(innerH * 0.18)
    const mainH = innerH - macdH - volH

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
    const plotW = viewW - plotL - plotR
    const step = plotW / raw.length
    const cxOf = (i) => plotL + step * i + step / 2
    const priceY = (v) => plotT + (hi - v) / (hi - lo) * mainH

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
    setHover({
      x: best.cx, y: best.yClose, point: best.raw, delta, pct,
      avg: avgAt(best.i),
      tipX: Math.min(Math.max(best.cx - 84, axisL), viewW - 168),
    })
  }
  function onLeave() { setHover(null) }

  // 累计均价（用于 hover 提示）
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
  function avgAt(idx) { return avgMemo[idx] || 0 }

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
          <span><i style={{ background: '#5b8ff9' }} />价格</span>
          <span><i style={{ background: '#f6b73c' }} />均价</span>
          <span><i style={{ background: '#f0b90b' }} />昨收</span>
          <span className="macd"><i style={{ background: '#f6b73c' }} />DIF</span>
          <span className="macd"><i style={{ background: '#5b8ff9' }} />DEA</span>
        </div>
      ) : null}
    </div>
  )
}
