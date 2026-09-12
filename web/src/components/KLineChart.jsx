// ── 分时图组件 KLineChart.jsx ──
// Canvas 绘制个股分时价格线、均价线、成交量柱、MACD（DIF/DEA/柱），
// 价格线按相对昨收动态红涨绿跌并填充区域，支持容器自适应、hover 十字线。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import './KLineChart.css'

// ── 布局常量（逻辑像素）──
const axisL = 48 // 左侧坐标轴宽
const plotL = axisL // 绘图区左边距（=坐标轴宽）
const plotR = 10 // 绘图区右边距
const axisB = 16 // 底部坐标轴高
const plotT = 8 // 绘图区上边距

// 配色令牌：canvas 不能用 CSS var()，故运行时从 :root 的 --app-chart-* 读取（refreshPalette）。
// 初值为浅色基线；组件挂载/主题切换时刷新为当前主题值，保证深浅色下图表配色与全站一致。
// English: canvas can't use CSS var(), so colors are read at runtime from --app-chart-* tokens
// via refreshPalette(); defaults are the light baseline, refreshed on mount and theme change.
import { subscribeTheme } from '../theme.js'
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
  tipBg: '#ffffff',
  tipBorder: '#d0d0d0',
  tipText: '#303133',
  tipSub: '#606266',
}
// 分时价格线上涨/下跌颜色（红涨绿跌）
let PRICE_UP = '#f5222d'
let PRICE_DOWN = '#16a34a' // 下跌绿

// refreshPalette 从当前主题的 CSS 自定义属性刷新配色（无 document 时保持默认）。
// English: refreshPalette reads the live theme's custom properties into the canvas palette.
function refreshPalette() {
  if (typeof document === 'undefined' || typeof getComputedStyle !== 'function') return
  const s = getComputedStyle(document.documentElement)
  const g = (name, fb) => { const v = (s.getPropertyValue(name) || '').trim(); return v || fb }
  C.bg = g('--app-chart-bg', C.bg)
  C.grid = g('--app-chart-grid', C.grid)
  C.axisTxt = g('--app-chart-axis', C.axisTxt)
  C.prev = g('--app-chart-line', C.prev)
  C.cross = g('--app-chart-line', C.cross)
  C.dot = g('--app-chart-line', C.dot)
  C.avg = g('--app-chart-avg', C.avg)
  C.dif = g('--app-chart-dif', C.dif)
  C.dea = g('--app-chart-dea', C.dea)
  C.volUp = g('--app-chart-vol-up', C.volUp)
  C.volDown = g('--app-chart-vol-down', C.volDown)
  C.tipBg = g('--app-chart-tip-bg', C.tipBg)
  C.tipBorder = g('--app-chart-tip-border', C.tipBorder)
  C.tipText = g('--app-chart-tip-text', C.tipText)
  C.tipSub = g('--app-chart-tip-sub', C.tipSub)
  PRICE_UP = g('--app-chart-vol-up', PRICE_UP)
  PRICE_DOWN = g('--app-chart-vol-down', PRICE_DOWN)
}


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
  // 分时原始数据点数组（后端返回的 points）
  const [raw, setRaw] = useState([])
  // 昨收盘价：红涨绿跌着色与涨跌幅计算的基准
  const [prevClose, setPrevClose] = useState(0)
  // 展示名：优先用后端返回的名称，否则用外部传入或代码
  const [dispName, setDispName] = useState(name)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  // 最新收盘价与相对昨收的涨跌幅（工具栏汇总展示）
  const [lastClose, setLastClose] = useState(0)
  const [last, setLast] = useState(0)
  // 容器宽度（画布自适应重绘）与 hover 十字光标状态
  const [viewW, setViewW] = useState(axisL + 320)
  const [hover, setHover] = useState(null)
  // §F4 主题版本计数：订阅主题变化并自增，作为重绘依赖让 canvas 用新令牌配色刷新。
  const [themeTick, setThemeTick] = useState(0)
  useEffect(() => subscribeTheme(() => setThemeTick((n) => n + 1)), [])

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
        // 请求分时接口；响应 points 非数组视为格式异常
        const data = await api.fetchMinute(code, scale, count)
        const pts = data && Array.isArray(data.points) ? data.points : null
        if (!pts) {
          if (!cancelled) setError('分时数据格式异常')
          return
        }
        // 空数据：清空画布数据源；后端附带 error 文案时一并展示
        if (pts.length === 0) {
          if (!cancelled) {
            setRaw([])
            if (data && data.error) setError(data.error)
          }
          return
        }
        if (cancelled) return
        // 回填点位、昨收与展示名，触发画布重绘
        setRaw(pts)
        setPrevClose(Number(data.prev_close) || 0)
        if (data.name) setDispName(data.name)
      } catch (e) {
        if (!cancelled) setError(e && e.message ? e.message : '分时加载失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    // 触发首次加载；卸载或 code/scale/count 变更时置 cancelled，丢弃过期响应
    load()
    return () => { cancelled = true }
  }, [code, scale, count])

  useEffect(() => {
    // 容器宽度监听：画布按容器实际宽度重绘（无 ResizeObserver 时退化为 500ms 轮询）
    if (!wrapRef.current) return
    setViewW(wrapRef.current.clientWidth)
    // 优先使用 ResizeObserver 监听容器尺寸变化
    if (typeof ResizeObserver !== 'undefined') {
      const ro = new ResizeObserver(() => {
        if (wrapRef.current) setViewW(wrapRef.current.clientWidth)
      })
      ro.observe(wrapRef.current)
      return () => ro.disconnect()
    } else {
      // 兜底：不支持 ResizeObserver 的环境改用 500ms 轮询容器宽度
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
    refreshPalette() // §F4 每帧同步当前主题配色令牌（含首帧与主题切换后重绘）

    // 画布初始化：按 devicePixelRatio 高清适配、清屏并填充底色
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

    // 纵向分区：上中下分别为 价格区(62%) / 成交量区(20%) / MACD 区
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
    // 价格轴范围：以昨收为中心取对称区间（带 15% 余量），保证红涨绿跌基准一致；无昨收则按高低点加边距
    let lo, hi
    if (prevClose > 0 && max >= 0 && min >= 0) {
      const span = Math.max(0.015, (max - min) / (2 * prevClose))
      const half = span * 1.15
      lo = Math.min(prevClose * (1 - half), min - (max - min) * 0.05)
      hi = Math.max(prevClose * (1 + half), max + (max - min) * 0.05)
    } else {
      // 无昨收基准：按高低点 ±6% 加安全边距，留出坐标刻度空间
      const pad = (max - min) * 0.06 || 0.01
      lo = min - pad
      hi = max + pad
    }
    // 价格/MACD 纵坐标线性映射（像素↔数值）：值在上界映射顶、下界映射底
    const priceY = (v) => plotT + (hi - v) / (hi - lo) * priceH
    const macdLineY = (v, maxAbs, half) => macdZero - (v / maxAbs) * half

    const n = raw.length
    const step = plotW / n
    // 横坐标：第 i 根 K 线柱中点（左边界 + 步长×i + 半柱宽）
    const cxOf = (i) => plotL + step * i + step / 2

    const points = raw.map((p, i) => ({ i, raw: p, cx: cxOf(i), yClose: priceY(p.close) }))
    const priceCoords = points.map((p) => [p.cx, p.yClose])

    // 分时均价线：累计成交额 / 累计成交量（前复权价口径）
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
    // 成交量柱：按最大量归一化高度，红涨绿跌着色（相对昨收或相对开盘）
    const volBars = raw.map((p, i) => {
      const cx = cxOf(i)
      // 柱高 = 成交量/池内最大量 × 区域高度
      const h = (p.volume / maxV) * volH
      const up = prevClose > 0 ? p.close >= prevClose : p.close >= p.open
      return { x: cx - vW / 2, w: vW, y: volBottom - h, h: Math.max(0.5, h), color: up ? C.volUp : C.volDown }
    })

    let maxAbs = 0.0001
    for (const p of raw) maxAbs = Math.max(maxAbs, Math.abs(p.bar), Math.abs(p.dif), Math.abs(p.dea))
    const half = macdH / 2
    const mW = Math.max(1, step * 0.55)
    // MACD 柱（红正绿负）+ DIF/DEA 折线数据点，按最大绝对值归一化到 MACD 区
    const difCoords = [], deaCoords = []
    const macdBars = raw.map((p, i) => {
      const cx = cxOf(i)
      const b = p.bar
      // 柱高按最大绝对值归一化至半区高度，正负分绘上下半区
      const hgt = (Math.abs(b) / maxAbs) * half
      const y = b >= 0 ? macdZero - hgt : macdZero
      difCoords.push([cx, macdLineY(p.dif, maxAbs, half)])
      deaCoords.push([cx, macdLineY(p.dea, maxAbs, half)])
      return { x: cx - mW / 2, w: mW, y, h: Math.max(0.5, hgt), color: b >= 0 ? C.volUp : C.volDown }
    })

    ctx.lineWidth = 1
    ctx.font = '12px monospace'
    ctx.textBaseline = 'middle'

    // 价格区横向 5 等分网格线 + 左侧坐标刻度
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

    // 昨收基准虚线（红涨绿跌的分界线）
    const prevY = priceY(prevClose)
    ctx.strokeStyle = C.prev
    ctx.setLineDash([4, 3])
    ctx.beginPath(); ctx.moveTo(plotL, prevY); ctx.lineTo(contW - plotR, prevY); ctx.stroke()
    ctx.setLineDash([])
    ctx.fillStyle = C.prev
    ctx.textAlign = 'end'
    ctx.fillText(prevClose ? prevClose.toFixed(2) : '', contW - plotR, prevY - 8)

    // 分时价格线：整体渐变填充到价格区底 + 逐段按红涨绿跌着色 + 末点高亮
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

    // 成交量区/MACD 区分隔线 + MACD 零轴虚线
    ctx.strokeStyle = C.grid
    ctx.lineWidth = 1
    ctx.beginPath(); ctx.moveTo(plotL, volTop); ctx.lineTo(contW - plotR, volTop); ctx.stroke()
    ctx.beginPath(); ctx.moveTo(plotL, macdTop); ctx.lineTo(contW - plotR, macdTop); ctx.stroke()
    ctx.strokeStyle = C.grid
    ctx.setLineDash([3, 3])
    ctx.beginPath(); ctx.moveTo(plotL, macdZero); ctx.lineTo(contW - plotR, macdZero); ctx.stroke()
    ctx.setLineDash([])

    // 成交量柱批量绘制（半透明红涨绿跌）
    for (const b of volBars) {
      ctx.fillStyle = b.color
      ctx.globalAlpha = 0.45
      ctx.fillRect(b.x, b.y, b.w, b.h)
    }
    ctx.globalAlpha = 1

    // MACD 柱批量绘制（红正绿负）
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

    // 底部时间轴刻度（最多 6 个，取分时时间 HH:mm）
    ctx.fillStyle = C.axisTxt
    ctx.textAlign = 'center'
    const tcount = Math.min(6, n)
    for (let i = 0; i < tcount; i++) {
      const idx = Math.round((n - 1) * i / (tcount - 1 || 1))
      const p = points[idx]
      ctx.fillText((p.raw.time || '').slice(11, 16), p.cx, viewH - 5)
    }

    // hover 十字光标：竖/横参考线 + 定位点 + 信息气泡（时间/价/涨跌幅/开高低/量额/MACD）
    if (hover) {
      ctx.strokeStyle = C.cross
      ctx.lineWidth = 1
      ctx.beginPath(); ctx.moveTo(hover.x, plotT); ctx.lineTo(hover.x, viewH - axisB); ctx.stroke()
      ctx.beginPath(); ctx.moveTo(plotL, hover.y); ctx.lineTo(contW - plotR, hover.y); ctx.stroke()
      ctx.fillStyle = C.dot
      ctx.beginPath(); ctx.arc(hover.x, hover.y, 3, 0, Math.PI * 2); ctx.fill()

      const tipX = Math.min(Math.max(hover.x - 92, axisL), contW - 184)
      // 信息气泡：184×92 固定尺寸白底描边卡片，紧贴顶部
      const tipW = 184, tipH = 92, tipY = 4
      ctx.fillStyle = C.tipBg
      ctx.strokeStyle = C.tipBorder
      ctx.fillRect(tipX, tipY, tipW, tipH)
      ctx.strokeRect(tipX, tipY, tipW, tipH)
      ctx.textAlign = 'left'
      ctx.fillStyle = C.tipText
      ctx.fillText(hover.point.time || '', tipX + 8, tipY + 12)
      // 行2：现价与涨跌幅（按相对昨收正负着色）
      const upc = hover.delta >= 0 ? PRICE_UP : PRICE_DOWN
      ctx.fillStyle = upc
      ctx.fillText('价 ' + hover.point.close.toFixed(2), tipX + 8, tipY + 30)
      ctx.fillText('涨 ' + (hover.delta >= 0 ? '+' : '') + hover.pct.toFixed(2) + '%', tipX + 96, tipY + 30)
      // 行3：开/高/低；行4：量/额；行5：MACD 三值（DIF/DEA/BAR）
      ctx.fillStyle = C.tipSub
      ctx.fillText('开 ' + hover.point.open.toFixed(2) + ' 高 ' + hover.point.high.toFixed(2) + ' 低 ' + hover.point.low.toFixed(2), tipX + 8, tipY + 48)
      ctx.fillText('量 ' + fmtVol(hover.point.volume) + ' · 额 ' + fmtAmt(hover.point.amount), tipX + 8, tipY + 66)
      ctx.fillText('DIF ' + hover.point.dif.toFixed(3) + ' DEA ' + hover.point.dea.toFixed(3) + ' BAR ' + hover.point.bar.toFixed(3), tipX + 8, tipY + 84)
    }
  }, [raw, prevClose, viewW, height, hover, themeTick])

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
      // 无昨收基准：按极值 ±6% 加安全边距（十字光标坐标范围）
      const pad = (mx - mn) * 0.06 || 0.01
      lo = mn - pad; hi = mx + pad
    }
    const plotW = contW - plotL - plotR
    const step = plotW / raw.length
    // 十字光标探测用横/纵线性坐标映射（与绘制共用同一套像素基准）
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
      {
        // 顶部工具栏：股票名+「分时」标题、现价/涨跌摘要、手动刷新按钮
      }
      <div className="kline-toolbar">
        <span className="kline-title">{dispName || code} · 分时</span>
        {
          // 现价与涨跌幅：红涨绿跌着色（仅拿到最新价后展示）
        }
        {lastClose ? (
          <span className="kline-summary">
            现价 <b className={last >= 0 ? 'up' : 'down'}>{lastClose.toFixed(2)}</b>
            涨跌 <b className={last >= 0 ? 'up' : 'down'}>{last >= 0 ? '+' : ''}{last.toFixed(2)}%</b>
          </span>
        ) : null}
        {
          // 刷新按钮：重新拉取分时数据并回填状态
        }
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

      {
        // 加载中 / 错误 / 空数据提示区
      }
      {loading ? <div className="kline-state">加载中…</div> : null}
      {error ? <div className="kline-state">{error}</div> : null}
      {!loading && !error && raw.length === 0 ? <div className="kline-state">暂无分时数据</div> : null}

      {
        // 分时画布：数据就绪时渲染，支持 hover 十字光标
      }
      {!loading && !error && raw.length > 0 ? (
        <div ref={wrapRef} className="kline-wrap">
          <canvas
            ref={canvasRef}
            onMouseMove={onMove}
            onMouseLeave={onLeave}
          />
        </div>
      ) : null}

      {
        // 图例：价格线 / 均价线 / 昨收 / DIF / DEA 的颜色说明
      }
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
