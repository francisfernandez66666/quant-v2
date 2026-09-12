// ── 盘口面板组件 DepthPanel.jsx ──
// Canvas 展示个股买卖盘口（最多十档）、现价涨跌与委比/封单等派生因子。
// 自动按实际档位数撑高，避免行距过密；无数据的档位自动隐藏。
import React, { useState, useEffect, useRef } from 'react'
import * as api from '../api/index.js'
import './DepthPanel.css'

// 盘口面板配色常量（文字/现价底/买卖盘/涨跌/数据源色）
import { subscribeTheme } from '../theme.js'
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

// refreshPalette 从当前主题的 --app-* 令牌刷新盘口 canvas 底色/文字色（语义涨跌色保持不变）。
// English: refreshPalette pulls the live theme's surface/text tokens into the depth canvas
// (semantic ask/bid/up/down colors stay fixed).
function refreshPalette() {
  if (typeof document === 'undefined' || typeof getComputedStyle !== 'function') return
  const s = getComputedStyle(document.documentElement)
  const g = (name, fb) => { const v = (s.getPropertyValue(name) || '').trim(); return v || fb }
  C.bg = g('--app-surface', C.bg)
  C.lv = g('--app-text-2', C.lv)
  C.vol = g('--app-muted', C.vol)
  C.nowBg = g('--app-surface-2', C.nowBg)
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
  // §F4 主题版本计数：主题切换时自增触发重绘。English: theme version counter; bumped on theme change to repaint.
  const [themeTick, setThemeTick] = useState(0)
  useEffect(() => subscribeTheme(() => setThemeTick((n) => n + 1)), [])

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
        // 请求后端盘口接口；响应含 bids 视为合法数据，同时回填盘口因子与股票名
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
        // 请求失败：展示后端错误信息或通用提示，避免面板空白无反馈
        if (!cancelled) setError(e && e.message ? e.message : '盘口加载失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    // 触发首次加载；组件卸载或 code 变更时置 cancelled，丢弃未完成的过期响应
    load()
    return () => { cancelled = true }
  }, [code])

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

  const pctText = (() => {
    // 现价相对昨收的涨跌幅文本（带 +/ 符号），数据缺失返回 '--'
    const p = ob.price || 0
    const pc = ob.prev_close || 0
    if (!p || !pc) return '--'
    // 涨跌幅百分比：(现价-昨收)/昨收×100
    const d = (p - pc) / pc * 100
    return (d >= 0 ? '+' : '') + d.toFixed(2) + '%'
  })()
  const nowCls = pctText.startsWith('+') ? 'up' : 'down'

  useEffect(() => {
    const cvs = canvasRef.current
    if (!cvs || !ob || !ob.bids || !ob.bids.length) return
    refreshPalette() // §F4 每帧同步当前主题令牌配色

    // 画布初始化：按 devicePixelRatio 高清适配
    const dpr = window.devicePixelRatio || 1
    cvs.style.width = '100%'
    const W = Math.max(1, Math.round(cvs.clientWidth || viewW))

    let levelCount = 0
    // 有效档位数：最大支持 10 档，按买卖两侧价格>0 的档位统计，无数据则按 5 档兜底
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
    // 行布局：上为卖盘（倒序）、中间现价行、下为买盘；画布高度随档位数自适应

    cvs.width = Math.round(W * dpr)
    cvs.height = Math.round(H * dpr)
    cvs.style.height = H + 'px'
    const ctx = cvs.getContext('2d')
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, W, H)
    ctx.fillStyle = C.bg
    ctx.fillRect(0, 0, W, H)

    const rows = []
    // 卖盘行：档位标签倒序（卖五…卖一），价格/量取自 asks 数组
    for (let i = 0; i < L; i++) {
      const label = L - i
      const idx = L - 1 - i
      const a = ob.asks ? ob.asks[idx] : null
      rows.push({ side: 'ask', lv: '卖' + label, price: a ? a.price : 0, vol: a ? a.volume : 0 })
    }
    rows.push({ now: true, lv: ob.name || code, price: ob.price || 0, volText: pctText })
    // 买盘行：档位标签正序（买一…买五/%d），价格/量取自 bids 数组
    for (let i = 0; i < L; i++) {
      const b = ob.bids ? ob.bids[i] : null
      rows.push({ side: 'bid', lv: '买' + (i + 1), price: b ? b.price : 0, vol: b ? b.volume : 0 })
    }

    // 量能最大值（用于量柱宽度归一化），避免除零
    let maxVol = 0
    for (const r of rows) if (r.vol > maxVol) maxVol = r.vol
    if (maxVol <= 0) maxVol = 1

    // 列布局：档位标签 | 价格 | 量柱，量柱区按最大量归一化
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
        // 现价行：浅灰底 + 名称 + 右侧现价与涨跌幅（红涨绿跌）
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
        // 买卖档行：量柱（买红/卖绿）+ 档位标签 + 价格 + 手数；价格色区分买卖侧
        if (r.vol > 0) {
          // 量柱宽度：按该档量 / 最大量归一化到量柱区宽度
          const bw = (r.vol / maxVol) * (volAreaRight - volAreaLeft)
          ctx.fillStyle = r.side === 'ask' ? 'rgba(22,163,74,0.14)' : 'rgba(245,34,77,0.14)'
          ctx.fillRect(volAreaLeft, y + 3, bw, rowH - 6)
        }
        ctx.fillStyle = C.lv
        ctx.textAlign = 'left'
        ctx.fillText(r.lv, col1, cy)
        // 价格文字：卖侧绿色、买侧红色（A 股红涨绿跌配色习惯）
        ctx.fillStyle = r.side === 'ask' ? C.ask : C.bid
        ctx.textAlign = 'left'
        ctx.fillText(fmtPrice(r.price), priceX, cy)
        ctx.fillStyle = C.vol
        ctx.textAlign = 'right'
        ctx.fillText(fmtVol(r.vol), volRight, cy)
      }
    })

    if (factors) {
      // 盘口因子区：委比/买卖量/封单/价差/覆盖度，每行一对「标签-数值」
      // 因子区起始 y：位于档位行之下留出 18px 间距
      let fy = topPad + (L * 2 + 1) * rowH + 18
      ctx.font = '12px monospace'
      // 数值统一转 Number：后端可能返回字符串，避免拼接与运算出错
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
        // 在盘口因子区绘制一行「标签-数值 × 2」：a/b 各含 label/val/color，行高自动下移
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
      // 逐对绘制因子：委比按正负红绿着色；封单红/绿区分买卖方向
      drawPair(
        // 委比 = (买盘总量-卖盘总量)/(买盘总量+卖盘总量)，衡量买卖盘力量对比
        { label: '委比', val: (F.bid_ask_ratio * 100).toFixed(1) + '%', color: F.bid_ask_ratio >= 0 ? C.up : C.down },
        // 买/卖量：买卖两侧挂单总量对比
        { label: '买/卖量', val: fmtVol(F.bid_vol) + '/' + fmtVol(F.ask_vol) }
      )
      // 封单量：买一/卖一位置的封单（涨停/跌停时意义最大）
      drawPair(
        { label: '买一封单', val: fmtVol(F.seal_bid), color: C.up },
        { label: '卖一封单', val: fmtVol(F.seal_ask), color: C.down }
      )
      // 价差：买卖一价差占现价比例（流动性）；覆盖：档位数据完整度
      drawPair(
        { label: '价差', val: F.spread_pct.toFixed(3) + '%' },
        { label: '覆盖', val: F.near_pct.toFixed(2) + '%' }
      )
    }
  }, [ob, factors, viewW, height, pctText, themeTick])

  return (
    <div className="depth-panel">
      {
        // 工具栏：股票名+「盘口」标题、行情时间与数据源标识、手动刷新按钮
      }
      <div className="depth-toolbar">
        <span className="depth-title">{ob.name || dispName || code} · 盘口</span>
        {ob.time ? (
          <span className="depth-time">
            {ob.time} {ob.source ? <i className="src">{ob.source}</i> : null}
          </span>
        ) : null}
        {
          // 刷新按钮：重新拉取盘口数据（复用加载态与错误态逻辑）
        }
        <button className="btn-refresh" disabled={loading} onClick={() => {
          setLoading(true); setError('')
          api.fetchDepth(code).then((data) => {
            if (data && data.bids) { setOb(data); setFactors(data.factors || null); if (data.name) setDispName(data.name) }
            else setError('盘口数据格式异常')
          }).catch((e) => setError(e && e.message ? e.message : '盘口加载失败')).finally(() => setLoading(false))
        }}>刷新</button>
      </div>

      {
        // 加载中 / 错误提示区
      }
      {loading ? <div className="depth-state">加载中…</div> : null}
      {error ? <div className="depth-state">{error}</div> : null}

      {
        // 盘口画布：仅在数据就绪且无错误时渲染
      }
      {!loading && !error && ob.bids && ob.bids.length ? (
        <div ref={wrapRef}>
          <canvas ref={canvasRef} />
        </div>
      ) : null}
    </div>
  )
}
