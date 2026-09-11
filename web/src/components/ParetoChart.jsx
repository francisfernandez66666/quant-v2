// ── Pareto 前沿散点图 ParetoChart.jsx ──
// §回测自动增强 D：SVG 手绘四维 Pareto 前沿可视化（不引图表库依赖，与 KLineChart 风格一致）。
// 数据源 = 寻优冠军行 grid_json.pareto（后端 C 模块捎带落库）：
//   X 轴胜率% · Y 轴盈亏比 · 点半径映射夏普；前沿=●（蓝），冠军=★（金），推荐解=●（红）。
// 点可点击弹出参数明细；无 pareto 键的旧任务由调用方降级为 champion 展示（不渲染本组件）。
import React, { useMemo, useState } from 'react'

// 布局常量（viewBox 逻辑像素）
const W = 560
const H = 340
const PAD_L = 52
const PAD_R = 16
const PAD_T = 14
const PAD_B = 40

// 数值格式（保留有效位）
function fmt(v, d = 2) {
  const n = Number(v)
  return Number.isFinite(n) ? n.toFixed(d) : '-'
}

// 轴范围：0 起、向上取整到"好看"的刻度步
function axisMax(vals, min) {
  const vs = vals.filter((v) => Number.isFinite(v))
  let m = vs.length ? Math.max(...vs, min || 0) : (min || 1)
  m = Math.max(m * 1.08, (min || 0) + 1e-9)
  return m
}

/**
 * @param {Object}   props
 * @param {Array}    props.front       前沿点集（grid_json.pareto.front）
 * @param {Object}   props.champion    冠军行（含 params/win_rate/profit_factor/sharpe）
 * @param {Object}   props.recommended 推荐解（可为 null，红点标注）
 * @param {Object}   props.gates       硬门槛（画门槛参考线）
 * @param {Function} props.onPick      点选回调（解对象，含 params 与指标）
 */
export default function ParetoChart({ front = [], champion, recommended, gates, onPick }) {
  const [hover, setHover] = useState(null) // 悬停点索引（tooltip）

  // 归一化点集：附加唯一键与角色标记（champion/recommended 若不在前沿集中则补画）
  const pts = useMemo(() => {
    const keyOf = (r) => [r?.params?.take_profit_pct, r?.params?.stop_loss_pct,
      r?.params?.hold_days, r?.params?.min_score].join('|')
    const list = (front || []).map((r, i) => ({ ...r, _k: keyOf(r) || 'p' + i, _role: 'front' }))
    const ck = keyOf(champion)
    const hitC = list.find((r) => r._k === ck)
    if (hitC) hitC._role = 'champion'
    else if (champion) list.push({ ...champion, _k: 'champ', _role: 'champion' })
    const rk = keyOf(recommended)
    const hitR = list.find((r) => r._k === rk)
    if (hitR) hitR._role = hitR._role === 'champion' ? 'champ_rec' : 'recommended'
    else if (recommended) list.push({ ...recommended, _k: 'rec', _role: 'recommended' })
    return list
  }, [front, champion, recommended])

  // 比例尺（含半径映射：sharpe → 3.5~9px）
  const scale = useMemo(() => {
    const xs = pts.map((p) => Number(p.win_rate))
    const ys = pts.map((p) => Number(p.profit_factor))
    const rs = pts.map((p) => Number(p.sharpe) || 0)
    const xMax = axisMax(xs, 10)
    const yMax = axisMax(ys, 1)
    const rMin = Math.min(...rs, 0); const rMax = Math.max(...rs, 1)
    const px = (v) => PAD_L + (v / xMax) * (W - PAD_L - PAD_R)
    const py = (v) => H - PAD_B - (v / yMax) * (H - PAD_B - PAD_T)
    const pr = (v) => 3.5 + ((v - rMin) / Math.max(rMax - rMin, 1e-9)) * 5.5
    return { xMax, yMax, px, py, pr }
  }, [pts])

  if (!pts.length) {
    return <div style={{ color: '#888', fontSize: 12, padding: 8 }}>本任务无 Pareto 前沿数据（旧任务产物或寻优时未启用）。</div>
  }

  // 门槛参考线（gates 提供时画虚线框，达标区为右上）
  const gl = gates && Number.isFinite(Number(gates.min_win_rate)) ? Number(gates.min_win_rate) : null
  const gp = gates && Number.isFinite(Number(gates.min_profit_factor)) ? Number(gates.min_profit_factor) : null

  // 简单刻度（5 档）
  const ticks = (max) => [0, 1, 2, 3, 4].map((i) => (max / 4) * i)

  const colorOf = (p) => (p._role === 'recommended' || p._role === 'champ_rec') ? '#e34d59'
    : p._role === 'champion' ? '#b8860b' : '#4f7cff'

  return (
    <div style={{ position: 'relative' }}>
      <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', maxWidth: W, display: 'block' }}>
        {/* 网格与坐标轴 */}
        {ticks(scale.yMax).map((v, i) => (
          <g key={'y' + i}>
            <line x1={PAD_L} x2={W - PAD_R} y1={scale.py(v)} y2={scale.py(v)} stroke="#ececec" strokeWidth="1" />
            <text x={PAD_L - 6} y={scale.py(v) + 3} fontSize="10" fill="#909399" textAnchor="end">{fmt(v, 1)}</text>
          </g>
        ))}
        {ticks(scale.xMax).map((v, i) => (
          <text key={'x' + i} x={scale.px(v)} y={H - PAD_B + 14} fontSize="10" fill="#909399" textAnchor="middle">{fmt(v, 0)}</text>
        ))}
        <line x1={PAD_L} x2={PAD_L} y1={PAD_T} y2={H - PAD_B} stroke="#c0c4cc" />
        <line x1={PAD_L} x2={W - PAD_R} y1={H - PAD_B} y2={H - PAD_B} stroke="#c0c4cc" />
        {/* 门槛参考线（达标区=右上） */}
        {gl !== null && <line x1={scale.px(gl)} x2={scale.px(gl)} y1={PAD_T} y2={H - PAD_B} stroke="#e6a23c" strokeDasharray="4 3" />}
        {gp !== null && <line x1={PAD_L} x2={W - PAD_R} y1={scale.py(gp)} y2={scale.py(gp)} stroke="#e6a23c" strokeDasharray="4 3" />}
        {/* 轴标题 */}
        <text x={(PAD_L + W - PAD_R) / 2} y={H - 8} fontSize="11" fill="#606266" textAnchor="middle">胜率 %</text>
        <text x={12} y={(PAD_T + H - PAD_B) / 2} fontSize="11" fill="#606266" textAnchor="middle" transform={`rotate(-90 12 ${(PAD_T + H - PAD_B) / 2})`}>盈亏比</text>
        {/* 前沿点（半径=夏普）；冠军画五角星 */}
        {pts.map((p, i) => {
          const cx = scale.px(Number(p.win_rate) || 0)
          const cy = scale.py(Number(p.profit_factor) || 0)
          const r = scale.pr(Number(p.sharpe) || 0)
          const fill = colorOf(p)
          if (p._role === 'champion' || p._role === 'champ_rec') {
            const star = starPath(cx, cy, r + 3)
            return <path key={p._k} d={star} fill={p._role === 'champ_rec' ? '#e34d59' : fill} stroke="#fff" strokeWidth="0.8" style={{ cursor: 'pointer' }}
              onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} onClick={() => onPick && onPick(p)} />
          }
          return <circle key={p._k} cx={cx} cy={cy} r={r} fill={fill} fillOpacity={p._role === 'recommended' ? 0.95 : 0.55}
            stroke={fill} strokeWidth="1" style={{ cursor: 'pointer' }}
            onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} onClick={() => onPick && onPick(p)} />
        })}
      </svg>
      {/* 悬停信息条 */}
      {hover !== null && pts[hover] && (
        <div style={{ position: 'absolute', left: 8, top: 4, fontSize: 11, background: 'rgba(255,255,255,.92)', border: '1px solid #eee', borderRadius: 4, padding: '2px 8px', pointerEvents: 'none' }}>
          {roleLabel(pts[hover]._role)} 胜率 {fmt(pts[hover].win_rate, 1)}% · 盈亏比 {fmt(pts[hover].profit_factor)}
          {' · 夏普 '}{fmt(pts[hover].sharpe)} · 卡玛 {fmt(pts[hover].calmar)} · 样本 {pts[hover].trigger_count ?? '-'}
        </div>
      )}
      {/* 图例 */}
      <div style={{ fontSize: 11, color: '#888', marginTop: 2 }}>
        <span style={{ color: '#b8860b' }}>★ 当前冠军</span>
        <span style={{ marginLeft: 12, color: '#4f7cff' }}>● Pareto 前沿</span>
        <span style={{ marginLeft: 12, color: '#e34d59' }}>● 推荐解</span>
        <span style={{ marginLeft: 12 }}>点半径 ∝ 夏普；橙色虚线 = 硬门槛</span>
      </div>
    </div>
  )
}

// 角色中文名
function roleLabel(role) {
  return { front: '前沿解', champion: '冠军', recommended: '推荐解', champ_rec: '冠军=推荐解' }[role] || '解'
}

// 五角星 path（外接圆半径 R）
function starPath(cx, cy, R) {
  const pts = []
  for (let i = 0; i < 10; i++) {
    const ang = -Math.PI / 2 + (Math.PI / 5) * i
    const r = i % 2 === 0 ? R : R * 0.42
    pts.push((i === 0 ? 'M' : 'L') + (cx + r * Math.cos(ang)).toFixed(1) + ' ' + (cy + r * Math.sin(ang)).toFixed(1))
  }
  return pts.join(' ') + ' Z'
}
