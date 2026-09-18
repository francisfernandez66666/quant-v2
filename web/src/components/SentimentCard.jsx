// SentimentCard.jsx — §Dashboard 情绪面板 A（2026-09-13）
//
// 顶部三栏情绪卡：
//   左：当前情绪相位徽章 + 判定时间戳（来自 SSE `score` 事件，与 MarketStatusBar 同源）
//   中：最近 30 交易日情绪色带（横向条带，每日一段；hover 显示日期 + 相位 + 涨停家数）
//   右：核心指标——最新一日的涨停家数 / 最高连板 / 建议仓位（max_pos_pct）
//
// 数据源：GET /api/market/emotion/history?days=30 + SSE `score` 增量刷新左栏。
// 与 MarketStatusBar 关系：状态条只显当前相位一行，本卡加"多日趋势 + 关键指标"两层信息。
// English: Dashboard sentiment card — current phase badge (live via SSE) + 30-day phase ribbon
// + core indicators (limit-up count / max ladder / suggested position).
import React, { useEffect, useMemo, useState } from 'react'
import { Card } from 'tdesign-react'
import { useNavigate } from 'react-router-dom'
import * as api from '../api/index.js'
import { on } from '../sseBus.js'

// 情绪相位 → 配色（与 MarketStatusBar.jsx:14-22 保持视觉一致；A 股习惯冰点冷蓝→高潮热红）
const EMOTION_STYLE = {
  冰点: '#1e6091',
  启动: '#2ba471',
  发酵: '#e8a317',
  高潮: '#e5484d',
  背离: '#834ec2',
  退潮: '#6b7785',
}
// emotionColor 相位名 → 配色（未知/空相位回落灰色变量）。
function emotionColor(name) {
  const k = (name || '').trim()
  return EMOTION_STYLE[k] || 'var(--app-muted-2)'
}

export default function SentimentCard() {
  // 跳转情绪回看页（§C 档）用
  const nav = useNavigate()
  // 30 日历史色带（升序，最后一项=最近一交易日）
  const [series, setSeries] = useState([])
  // 当前实时相位（SSE score 推送更新）
  const [live, setLive] = useState({ emotion: '', market_state: '', max_pos_pct: 0, risk_tier: '', ts: '' })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  // §B 情绪×战法矩阵（懒加载：折叠区展开时才拉，默认收起不占首屏）
  const [matrixOpen, setMatrixOpen] = useState(false)
  const [matrix, setMatrix] = useState(null)
  const [matrixErr, setMatrixErr] = useState('')

  // 展开/收起情绪×战法矩阵，首次展开时拉数据
  function toggleMatrix() {
    const next = !matrixOpen
    setMatrixOpen(next)
    if (next && matrix == null && !matrixErr) {
      api.fetchEmotionStrategyMatrix()
        .then((r) => setMatrix(Array.isArray(r && r.rows) ? r.rows : []))
        .catch((e) => setMatrixErr((e && e.message) || '矩阵加载失败'))
    }
  }

  // 挂载时拉最近 30 日情绪序列；mounted 守卫防止请求回来时组件已卸载还 setState
  useEffect(() => {
    let mounted = true
    api.fetchEmotionHistory(30).then((r) => {
      if (!mounted) return
      setSeries(Array.isArray(r && r.series) ? r.series : [])
      setLoading(false)
    }).catch((e) => {
      if (!mounted) return
      setError((e && e.message) || '加载失败')
      setLoading(false)
    })
    return () => { mounted = false }
  }, [])

  // SSE score 事件更新实时相位（后端 scoring_loop 每轮推）
  useEffect(() => {
    const off = on(['score'], (msg) => {
      if (!msg) return
      setLive({
        emotion: msg.emotion || '',
        market_state: msg.market_state || '',
        max_pos_pct: msg.max_pos_pct || 0,
        risk_tier: msg.risk_tier || '',
        ts: msg.time || '',
      })
    })
    return () => { if (typeof off === 'function') off() }
  }, [])

  // 最近一日（series 尾项）作为"关键指标"来源；SSE 事件不含家数明细，日终快照兜底
  const last = useMemo(() => series.length ? series[series.length - 1] : null, [series])
  const curEmotion = live.emotion || (last && last.emotion) || ''
  const curColor = emotionColor(curEmotion)
  const ribbon = series.map((d) => ({ date: (d.date || '').slice(5), color: emotionColor(d.emotion), count: d.limit_up_count || 0, name: d.emotion || '—' }))

  return (
    <Card title="市场情绪" actions={
      // 右上角：数据源时间戳（实时用 SSE ts，无则用最近日快照 date）+ 回看页入口（§C 档）
      <span style={{ fontSize: 12, color: 'var(--app-muted)' }}>
        {live.ts ? 'SSE ' + live.ts : (last ? '日终 ' + last.date : '')}
        <a role="button" onClick={() => nav('/emotion')}
          style={{ marginLeft: 10, color: 'var(--app-accent)', cursor: 'pointer', textDecoration: 'underline' }}>
          回看全年
        </a>
      </span>
    } style={{ marginBottom: 12 }}>
      {error && <div style={{ color: 'var(--app-warn-text)', fontSize: 13 }}>{error}</div>}
      {loading && !error && <div className="muted" style={{ fontSize: 13 }}>加载情绪历史…</div>}
      {!loading && !error && (
        <div style={{ display: 'grid', gridTemplateColumns: '160px 1fr 260px', gap: 16, alignItems: 'center' }}>
          {/* 左：当前相位徽章 + 判定时间 */}
          <div>
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 8, padding: '4px 10px', borderRadius: 4, background: 'var(--app-surface-2)' }}>
              <span style={{ width: 10, height: 10, borderRadius: '50%', background: curColor, display: 'inline-block' }} />
              <span style={{ fontWeight: 600, fontSize: 16, color: curColor }}>{curEmotion || '—'}</span>
              {live.market_state && <span style={{ fontSize: 11, color: 'var(--app-muted)' }}>{live.market_state}</span>}
            </div>
            <div style={{ fontSize: 11, color: 'var(--app-muted)', marginTop: 6 }}>
              {live.risk_tier ? '风险档 ' + live.risk_tier : '等待 SSE 推送'}
            </div>
          </div>

          {/* 中：30 日色带 */}
          <div>
            <div style={{ display: 'flex', height: 24, borderRadius: 4, overflow: 'hidden', border: '1px solid var(--app-border)' }}>
              {ribbon.length === 0 && <div className="muted" style={{ fontSize: 12, padding: '4px 8px' }}>无历史</div>}
              {ribbon.map((c, i) => (
                <div
                  key={i}
                  title={c.date + ' · ' + c.name + ' · 涨停 ' + c.count}
                  style={{ flex: 1, background: c.color, opacity: i === ribbon.length - 1 ? 1 : 0.72 }}
                />
              ))}
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: 'var(--app-muted)', marginTop: 4 }}>
              <span>{ribbon[0]?.date || ''}</span>
              <span>{ribbon[ribbon.length - 1]?.date || ''}</span>
            </div>
          </div>

          {/* 右：核心指标 */}
          <div style={{ display: 'flex', gap: 16, justifyContent: 'flex-end' }}>
            <div style={{ textAlign: 'right' }}>
              <div className="muted" style={{ fontSize: 11 }}>涨停家数</div>
              <div style={{ fontSize: 16, fontWeight: 600, color: 'var(--app-up)' }}>{last ? last.limit_up_count : '—'}</div>
            </div>
            <div style={{ textAlign: 'right' }}>
              <div className="muted" style={{ fontSize: 11 }}>最高连板</div>
              <div style={{ fontSize: 16, fontWeight: 600 }}>{last ? last.ladder_height : '—'}</div>
            </div>
            <div style={{ textAlign: 'right' }}>
              <div className="muted" style={{ fontSize: 11 }}>建议仓位</div>
              <div style={{ fontSize: 16, fontWeight: 600, color: 'var(--app-accent)' }}>
                {live.max_pos_pct ? (live.max_pos_pct * 100).toFixed(0) + '%' : (last && last.max_pos_pct ? (last.max_pos_pct * 100).toFixed(0) + '%' : '—')}
              </div>
            </div>
          </div>
        </div>
      )}
      {/* §B 情绪×战法矩阵：折叠区，展开时懒加载；行=候选战法，列=六相位，格值=事件均超额 */}
      {!loading && !error && (
        <div style={{ marginTop: 10 }}>
          <a
            role="button"
            style={{ fontSize: 12, color: 'var(--app-accent)', cursor: 'pointer' }}
            onClick={toggleMatrix}
          >
            {matrixOpen ? '▾ 收起情绪×战法矩阵' : '▸ 看情绪×战法矩阵（历史分相回测）'}
          </a>
          {matrixOpen && matrixErr && <div style={{ fontSize: 12, color: 'var(--app-warn-text)', marginTop: 6 }}>{matrixErr}</div>}
          {matrixOpen && matrix == null && !matrixErr && <div className="muted" style={{ fontSize: 12, marginTop: 6 }}>加载矩阵…</div>}
          {matrixOpen && matrix != null && (
            matrix.length === 0 ? (
              <div className="muted" style={{ fontSize: 12, marginTop: 6 }}>
                暂无可分相的回测数据——先在「自动研究」对候选发起回测，回测断点会按情绪相位聚成这张矩阵
              </div>
            ) : (
              <div style={{ overflowX: 'auto', marginTop: 8 }}>
                <table style={{ borderCollapse: 'collapse', fontSize: 12, width: '100%' }}>
                  <thead>
                    <tr>
                      <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--app-muted)', fontWeight: 500 }}>战法/候选</th>
                      {['冰点', '启动', '发酵', '高潮', '退潮'].map((p) => (
                        <th key={p} style={{ padding: '4px 8px', fontWeight: 600, color: emotionColor(p) }}>{p}</th>
                      ))}
                      <th style={{ padding: '4px 8px', color: 'var(--app-muted)', fontWeight: 500 }}>事件</th>
                    </tr>
                  </thead>
                  <tbody>
                    {matrix.map((row) => (
                      <tr key={row.candidate_id} style={{ borderTop: '1px solid var(--app-border)' }}>
                        <td style={{ padding: '4px 8px', maxWidth: 220, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={row.name}>
                          {row.name}
                          <span className="muted" style={{ marginLeft: 6, fontSize: 11 }}>{row.kind}</span>
                        </td>
                        {['冰点', '启动', '发酵', '高潮', '退潮'].map((p) => {
                          // 行内定位当前阶段的单元格
                          const cell = (row.cells || []).find((c) => c.phase === p)
                          if (!cell) return <td key={p} style={{ padding: '4px 8px', textAlign: 'center', color: 'var(--app-muted-2)' }}>—</td>
                          // 格值：主 horizon（最右列）平均超额；hover 出全 horizon + 命中率
                          // 后端 hit_rate 为 0-1 比例（chain.go:465 wins/n），展示乘 100
                          const hz = (row.horizons || []).slice(-1)[0]
                          const v = cell.avg_excess && cell.avg_excess[hz]
                          // hover 提示：各持仓周期平均超额一览
                          const tip = (row.horizons || []).map((h) => 'H' + h + ': ' + fmtPct(cell.avg_excess && cell.avg_excess[h]) + ' /命中 ' + (cell.hit_rate && cell.hit_rate[h] != null ? (cell.hit_rate[h] * 100).toFixed(0) + '%' : '—')).join('\n')
                          return (
                            <td
                              key={p}
                              title={tip + '\n事件 ' + cell.events + (cell.thin ? '（样本<' + (matrixMinEvents) + '，参考性弱）' : '')}
                              style={{
                                padding: '4px 8px', textAlign: 'center',
                                color: v >= 0 ? 'var(--app-up)' : 'var(--app-down)',
                                fontWeight: cell.thin ? 400 : 600,
                                opacity: cell.thin ? 0.5 : 1,
                              }}
                            >
                              {fmtPct(v)}
                            </td>
                          )
                        })}
                        <td style={{ padding: '4px 8px', textAlign: 'center', color: 'var(--app-muted)' }}>{row.total_events}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          )}
        </div>
      )}
    </Card>
  )
}

// fmtPct 格值格式化：undefined→—，数字→±x.x%
function fmtPct(v) {
  if (v === undefined || v === null || isNaN(v)) return '—'
  return (v >= 0 ? '+' : '') + Number(v).toFixed(1) + '%'
}

// 与后端 EmotionMatrixRowMinEvents 同值的展示常量（后端改阈值时同步）
const matrixMinEvents = 20
