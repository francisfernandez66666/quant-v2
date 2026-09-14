// EmotionReview.jsx — §市场情绪回看页（情绪面板 C 档，2026-09-13）
//
// 双 Y 轴历史回看：**涨停家数柱（左轴）+ 模拟盘账户净值折线（右轴）+ 情绪相位色带（底部条）**，
// 区间可选（30/60/120/250 交易日），回答"上次冰点买入的仓位现在什么水平"。
//
// 数据源：
//   - GET /api/market/emotion/history?days=N → 每日 emotion/limit_up_count/ladder_height
//   - GET /api/paper/equity → [{date:YYYY-MM-DD, value}] 账户净值序列（admin + 模拟盘开启时有）
// 两序列按日期对齐（ISO 日期键），净值缺日时折线断开处直连（个人工具，不做插值假装精确）。
// 渲染零依赖：原生 SVG（与 Paper.jsx 净值曲线同风格），hover 用 <title> 原生 tooltip。
// English: emotion review page — limit-up bars (left axis) + paper equity line (right axis)
// + sentiment phase ribbon at the bottom; range selectable up to ~1 trading year. Pure SVG,
// aligned by ISO date keys, no chart library.
import React, { useEffect, useMemo, useState } from 'react'
import { Card, Button } from 'tdesign-react'
import * as api from '../api/index.js'

// 六相位配色（与 MarketStatusBar / SentimentCard 同表）
const EMOTION_COLOR = {
  冰点: '#1e6091', 启动: '#2ba471', 发酵: '#e8a317',
  高潮: '#e5484d', 背离: '#834ec2', 退潮: '#6b7785',
}
const RANGES = [30, 60, 120, 250]
const W = 1000, H = 320, RIBBON_H = 18, PAD_B = 44, PAD_T = 16, PAD_L = 46, PAD_R = 60

// 情绪回看页（§C 档）：涨停柱 + 净值折线 + 相位色带三合一，区间 30/60/120/250 交易日。
export default function EmotionReview() {
  const [days, setDays] = useState(120)
  const [series, setSeries] = useState([])      // 情绪日历史（YYYY-MM-DD 升序）
  const [equity, setEquity] = useState([])      // 净值点 [{date,value}]
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [hover, setHover] = useState(null)      // 悬停的日期索引（信息条显示）

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError('')
    // 两路并发：情绪历史必拉；净值失败静默（无模拟盘/非 admin 时右轴隐藏）
    Promise.all([
      api.fetchEmotionHistory(days).then((r) => (Array.isArray(r && r.series) ? r.series : [])).catch(() => []),
      api.fetchPaperEquity().then((r) => (Array.isArray(r) ? r : [])).catch(() => []),
    ]).then(([hist, eq]) => {
      if (!alive) return
      setSeries(hist)
      setEquity(eq)
      setLoading(false)
    }).catch((e) => {
      if (!alive) return
      setError((e && e.message) || '加载失败')
      setLoading(false)
    })
    return () => { alive = false }
  }, [days])

  // 净值按情绪历史日期窗口过滤 + 对齐（ISO 键）
  const aligned = useMemo(() => {
    if (!series.length) return { eqByDate: {}, eqMin: 0, eqMax: 0, hasEq: false }
    const from = series[0].date
    const to = series[series.length - 1].date
    const eqPts = equity.filter((p) => p && p.date >= from && p.date <= to && Number(p.value) > 0)
    if (eqPts.length < 2) return { eqByDate: {}, eqMin: 0, eqMax: 0, hasEq: false }
    const eqByDate = {}
    let lo = Infinity, hi = -Infinity
    for (const p of eqPts) { eqByDate[p.date] = p.value; if (p.value < lo) lo = p.value; if (p.value > hi) hi = p.value }
    return { eqByDate, eqMin: lo, eqMax: hi, hasEq: true }
  }, [series, equity])

  // 图内坐标换算（SVG viewBox 空间；日期等距铺开——周末缺口由色带宽度吸收）
  const geo = useMemo(() => {
    if (!series.length) return null
    const n = series.length
    const maxLU = Math.max(...series.map((d) => d.limit_up_count || 0), 10)
    // 柱状图步宽
    const step = (W - PAD_L - PAD_R) / n
    const bars = series.map((d, i) => {
      const x = PAD_L + i * step
      // 涨停数柱高（按序列最大值归一）
      const bh = ((d.limit_up_count || 0) / maxLU) * (H - PAD_T - PAD_B - 8)
      return { x, w: Math.max(step - 1.5, 1), y: H - PAD_B - bh, h: bh, lu: d.limit_up_count || 0, color: EMOTION_COLOR[d.emotion] || '#999' }
    })
    // 净值折线：右轴缩放；缺日直连
    let eqLine = ''
    if (aligned.hasEq) {
      const pts = series.filter((d) => aligned.eqByDate[d.date] != null)
      const lo = aligned.eqMin, hi = aligned.eqMax, span = hi - lo || 1
      eqLine = pts.map((d, k) => {
        const i = series.indexOf(d)
        const x = PAD_L + i * step + step / 2
        const y = H - PAD_B - ((aligned.eqByDate[d.date] - lo) / span) * (H - PAD_T - PAD_B - 8)
        return (k === 0 ? 'M' : 'L') + x.toFixed(1) + ' ' + y.toFixed(1)
      }).join(' ')
    }
    return { n, step, maxLU, bars, eqLine }
  }, [series, aligned])

  // 相位图例（当前区间各相位天数）
  const phaseCount = useMemo(() => {
    const m = {}
    for (const d of series) m[d.emotion] = (m[d.emotion] || 0) + 1
    return m
  }, [series])

  const hoverDay = hover != null && series[hover] ? series[hover] : null

  return (
    <div className="page">
      {/* TDesign Card 头部右侧插槽名是 actions（非 headerRightContent，后者不渲染） */}
      <Card title="市场情绪回看" actions={
        // 区间切换按钮组
        <div style={{ display: 'flex', gap: 6 }}>
          {RANGES.map((r) => (
            <Button key={r} size="small" variant={days === r ? 'base' : 'outline'} theme={days === r ? 'primary' : 'default'} onClick={() => setDays(r)}>{r}日</Button>
          ))}
        </div>
      }>
        {error && <div style={{ color: 'var(--app-warn-text)', fontSize: 13 }}>{error}</div>}
        {loading && <div className="muted" style={{ fontSize: 13 }}>加载 {days} 日情绪与净值…</div>}
        {!loading && !error && !series.length && (
          <div className="muted" style={{ padding: 24, textAlign: 'center' }}>
            暂无情绪留痕数据——引擎在交易时段跑过评分循环后每日落一条 market_risk_daily
          </div>
        )}
        {!loading && geo && (
          <div>
            {/* 信息条：悬停日期 → 相位/涨停/连板/净值 */}
            <div style={{ height: 22, fontSize: 12, color: 'var(--app-muted)' }}>
              {hoverDay ? (
                <>
                  <span style={{ fontWeight: 600, color: EMOTION_COLOR[hoverDay.emotion] || 'inherit' }}>{hoverDay.date} {hoverDay.emotion || '—'}</span>
                  {' · 涨停 '}<b style={{ color: 'var(--app-up)' }}>{hoverDay.limit_up_count}</b>
                  {' · 最高连板 '}{hoverDay.ladder_height}
                  {aligned.hasEq && aligned.eqByDate[hoverDay.date] != null && (
                    <>{' · 净值 '}<b style={{ color: 'var(--app-accent)' }}>¥{Number(aligned.eqByDate[hoverDay.date]).toLocaleString()}</b></>
                  )}
                </>
              ) : '悬停柱子看当日明细'}
            </div>
            <svg
              viewBox={`0 0 ${W} ${H}`}
              style={{ width: '100%', height: 380, display: 'block' }}
              onMouseLeave={() => setHover(null)}
            >
              {/* 左轴网格（涨停家数）：4 条水平参考线 */}
              {[0.25, 0.5, 0.75, 1].map((f) => {
                const y = H - PAD_B - f * (H - PAD_T - PAD_B - 8)
                return (
                  <g key={f}>
                    <line x1={PAD_L} y1={y} x2={W - PAD_R} y2={y} stroke="var(--app-divider)" strokeDasharray="3 4" />
                    <text x={PAD_L - 6} y={y + 4} textAnchor="end" fontSize="10" fill="var(--app-muted)">{Math.round(geo.maxLU * f)}</text>
                  </g>
                )
              })}
              {/* 涨停柱：按当日情绪相位着色 */}
              {geo.bars.map((b, i) => (
                <rect
                  key={i} x={b.x} y={b.y} width={b.w} height={b.h} fill={b.color} opacity={0.82}
                  onMouseEnter={() => setHover(i)}
                />
              ))}
              {/* 净值折线（右轴）+ 左右轴刻度 */}
              {geo.eqLine && (
                <path d={geo.eqLine} fill="none" stroke="var(--app-accent)" strokeWidth="2" opacity="0.9" />
              )}
              {aligned.hasEq && [aligned.eqMin, (aligned.eqMin + aligned.eqMax) / 2, aligned.eqMax].map((v, k) => {
                const y = H - PAD_B - (k / 2) * (H - PAD_T - PAD_B - 8)
                return <text key={k} x={W - PAD_R + 6} y={y + 4} fontSize="10" fill="var(--app-accent)">{Math.round(v / 1000)}k</text>
              })}
              {/* 底部情绪相位色带（每日一格） */}
              {geo.bars.map((b, i) => (
                <rect key={'r' + i} x={b.x} y={H - PAD_B + 6} width={b.w + 1.5} height={RIBBON_H}
                  fill={b.color} onMouseEnter={() => setHover(i)}>
                  <title>{series[i].date + ' ' + series[i].emotion}</title>
                </rect>
              ))}
              {/* X 轴日期刻度（首/中/尾三点） */}
              {[0, Math.floor(geo.n / 2), geo.n - 1].map((i) => (
                <text key={i} x={PAD_L + i * geo.step + geo.step / 2} y={H - 6} textAnchor="middle" fontSize="10" fill="var(--app-muted)">
                  {(series[i]?.date || '').slice(5)}
                </text>
              ))}
            </svg>
            {/* 图例：相位 → 区间内天数 */}
            <div style={{ display: 'flex', gap: 14, flexWrap: 'wrap', marginTop: 10, fontSize: 12 }}>
              {Object.entries(phaseCount).sort((a, b) => b[1] - a[1]).map(([ph, cnt]) => (
                <span key={ph}>
                  <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: EMOTION_COLOR[ph] || '#999', marginRight: 5 }} />
                  {ph || '未标注'} ×{cnt}
                </span>
              ))}
              {aligned.hasEq && <span style={{ color: 'var(--app-accent)' }}>— 账户净值（右轴，¥千）</span>}
              {!aligned.hasEq && <span className="muted">净值线：需模拟盘开启且有 ≥2 个快照</span>}
            </div>
          </div>
        )}
      </Card>
    </div>
  )
}
