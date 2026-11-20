// ── 资金流 / 量能读数卡 FlowFactorCard.jsx ──（2026-11-20，§FLOW-QUADRANT 展示面）
//
// 首页仪表盘上的市场级读数卡，只展示、不参与任何判定：
//   左：云端净流入——flow_factor_daily.market_flow_pct（净流入 ÷ 当日成交额 %）
//       与其归一强度 market_delta ∈[-1,1]，外加当日已烘入的板块读数行数；
//   右：量能状态——放量 / 缩量 / 平量（由 r_yest、r_ma5 判出），并列出两个比值本身；
//   脚：三项 delta 合成的金额乘子预览（±20% 封顶），弃权时明说"不参与"。
//
// 三条展示纪律（与后端 flow_status.go 一一对应）：
//   ① 缺数不画 0：market_known / vol_known 为 false 时显示「今日无读数（弃权）」，
//      绝不写 0.00% 或「平量」——那会让人以为这条腿真的读过并且是中性；
//   ② 量态文案只有一个映射表（VOL_STATE_LABEL），阈值判定全在后端 signalctl.VolStateOf；
//   ③ 合成乘子直接用后端下发的 blend，不在前端再算一次（否则就是第二份公式）。
// English: dashboard card for the cloud net-inflow and volume-state readings. Display only;
// missing values render as an explicit abstain, never as 0.0% / "flat".
import React from 'react'
import { Card, Tag } from 'tdesign-react'

// 后端 signalctl 的量态枚举 → 文案与配色。键必须与 Go 侧 VolState* 常量逐字一致。
// 放量用「涨色」（与 blend 的正向强度同向），缩量用「跌色」，平量走中性灰。
export const VOL_STATE_LABEL = {
  surge: { text: '放量', theme: 'danger' },
  shrink: { text: '缩量', theme: 'success' },
  neutral: { text: '平量', theme: 'default' },
}

// volStateLabel 量态 → 文案；未知/缺失回落「—」（绝不猜成平量）。
export function volStateLabel(state) {
  const hit = VOL_STATE_LABEL[state]
  return hit ? hit.text : '—'
}

// fmtSignedPct 百分比带符号两位小数；非有限值（null/NaN/undefined）→ null 交给调用方显示占位。
// 为什么单独抽出来：`(v || 0).toFixed(2)` 会把 null 画成 0.00%——正是本卡要避免的那种"假中性"。
export function fmtSignedPct(v) {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  if (!Number.isFinite(n)) return null
  return `${n > 0 ? '+' : ''}${n.toFixed(2)}%`
}

// fmtRatio 量比两位小数（1.00 表示与基准持平）；非有限值 → null。
export function fmtRatio(v) {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  if (!Number.isFinite(n)) return null
  return n.toFixed(2)
}

// deltaTone 强度 → 颜色变量（正=涨色、负=跌色、0/缺失=中性灰）。
// 注意 0 是**有效平量**（参与合乘，权重 1/3 贡献 0），与"弃权"不同——弃权由 *_known 决定。
function deltaTone(v) {
  if (v === null || v === undefined) return 'var(--app-muted)'
  const n = Number(v)
  if (!Number.isFinite(n) || n === 0) return 'var(--app-muted)'
  return n > 0 ? 'var(--app-up)' : 'var(--app-down)'
}

// 一行小指标：名称 + 值 + 可选提示（值缺失时由调用方传占位符，绝不在此补 0）
function Metric({ label, value, tone, hint }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <span className="muted" style={{ fontSize: 12 }}>{label}</span>
      <span style={{ fontSize: 20, fontWeight: 700, color: tone || 'inherit' }}>{value}</span>
      {hint ? <span className="muted" style={{ fontSize: 11 }}>{hint}</span> : null}
    </div>
  )
}

/**
 * 资金流 / 量能读数卡。
 * @param {{flow?: object}} props flow = GET /api/status 的 flow 子对象（可能尚未取到）
 */
export default function FlowFactorCard({ flow }) {
  // 三种"不画数"的形态各自成句，绝不合并成同一句：
  //   ① flow 缺省 = 接口还没返回；② available=false = 服务端读数库没接线；
  //   ③ known=false = 接线了但今天还没烘出任何一行。
  let body
  if (!flow) {
    body = (
      <div className="muted" data-testid="flow-unavailable" style={{ fontSize: 13 }}>
        资金流读数尚未取到（接口未返回）
      </div>
    )
  } else if (flow.available === false) {
    body = (
      <div className="muted" data-testid="flow-unavailable" style={{ fontSize: 13 }}>
        资金流读数未接入（服务端未挂载读数库）
      </div>
    )
  } else if (!flow.known) {
    body = (
      <>
        <div className="muted" data-testid="flow-abstain" style={{ fontSize: 13 }}>
          今日暂无资金流/量能读数（弃权：不参与金额乘子，买卖判定按原口径）
        </div>
        <div className="muted" style={{ fontSize: 11, marginTop: 4 }}>读数日 {flow.day || '—'}</div>
      </>
    )
  } else {
    const pct = fmtSignedPct(flow.market_pct)
    const marketDelta = Number.isFinite(Number(flow.market_delta)) && flow.market_delta !== null
      ? Number(flow.market_delta) : null
    const rYest = fmtRatio(flow.r_yest)
    const rMa5 = fmtRatio(flow.r_ma5)
    const vol = VOL_STATE_LABEL[flow.vol_state] || null
    const blend = Number.isFinite(Number(flow.blend)) && flow.blend !== null ? Number(flow.blend) : null
    const blendPct = blend === null ? null : (blend - 1) * 100
    const sectorCount = Number.isFinite(Number(flow.sector_count)) && flow.sector_count !== null
      ? Number(flow.sector_count) : null
    body = (
      <>
        <div style={{ display: 'flex', gap: 28, flexWrap: 'wrap' }}>
          {/* 左：云端净流入（主力净额 ÷ 当日成交额，3% 满档归一到 ±1） */}
          <div data-testid="flow-market" style={{ flex: '1 1 200px' }}>
            {flow.market_known ? (
              <div style={{ display: 'flex', gap: 24, alignItems: 'flex-end', flexWrap: 'wrap' }}>
                <Metric
                  label="净流入（云端，占成交额）"
                  value={pct === null ? '—' : pct}
                  tone={deltaTone(marketDelta)}
                  hint={marketDelta === null ? '强度读数缺失' : `归一强度 ${marketDelta > 0 ? '+' : ''}${marketDelta.toFixed(2)}`}
                />
              </div>
            ) : (
              <div className="muted" data-testid="flow-market-abstain" style={{ fontSize: 13 }}>
                净流入：今日无读数（弃权）
              </div>
            )}
            <div className="muted" style={{ fontSize: 11, marginTop: 6 }}>
              板块读数 {sectorCount === null ? '—' : `${sectorCount} 个`}
              {sectorCount === 0 ? '（板块逐票腿今日无数据，弃权）' : ''}
            </div>
            {/* §FLOW-SECTOR-AGG（owner 2026-11-20）：板块聚合两列。
                缺数一律显示"弃权"，绝不画 0 —— 0 在资金流语境里是"板块中性"，是另一个意思。 */}
            <div className="muted" data-testid="flow-sector-agg" style={{ fontSize: 11, marginTop: 4 }}>
              {flow.sector_known ? (
                <>
                  板块合计净流入 {fmtSignedPct(flow.sector_pct) ?? '—'}，等权强度{' '}
                  {Number.isFinite(Number(flow.sector_delta))
                    ? `${Number(flow.sector_delta) > 0 ? '+' : ''}${Number(flow.sector_delta).toFixed(2)}`
                    : '—'}
                </>
              ) : (
                <>板块合计：今日无读数（弃权）</>
              )}
            </div>
          </div>

          {/* 右：量能状态（放量/缩量/平量由 r_yest 与 r_ma5 双闸判出，阈值在后端） */}
          <div data-testid="flow-volume" style={{ flex: '1 1 200px' }}>
            {flow.vol_known ? (
              <div style={{ display: 'flex', gap: 24, alignItems: 'flex-end', flexWrap: 'wrap' }}>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                  <span className="muted" style={{ fontSize: 12 }}>量能（相对昨日 / 近五日均量）</span>
                  <span style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <Tag theme={vol ? vol.theme : 'default'} size="medium" data-testid="flow-vol-state">
                      {vol ? vol.text : '—'}
                    </Tag>
                    <span className="muted" style={{ fontSize: 12 }}>
                      昨日 ×{rYest === null ? '—' : rYest} · 五日均 ×{rMa5 === null ? '—' : rMa5}
                    </span>
                  </span>
                </div>
              </div>
            ) : (
              <div className="muted" data-testid="flow-volume-abstain" style={{ fontSize: 13 }}>
                量能：今日无读数（弃权）
              </div>
            )}
          </div>
        </div>

        {/* 脚：合成金额乘子（后端已算好，前端不重算）。弃权时明说"不参与"，不显示 ×1.00。 */}
        <div className="muted" style={{ fontSize: 12, marginTop: 12 }} data-testid="flow-blend">
          {flow.blend_ok && blend !== null ? (
            <span>
              合成金额乘子 <b style={{ color: deltaTone(blendPct) }}>×{blend.toFixed(2)}</b>
              （{blendPct > 0 ? '+' : ''}{blendPct.toFixed(1)}%，±20% 封顶；缺项按有效腿折算）
            </span>
          ) : (
            <span>合成金额乘子：弃权（三条腿今日都无读数，金额按原口径）</span>
          )}
          <span style={{ marginLeft: 8 }}>· 读数日 {flow.day || '—'}</span>
        </div>
      </>
    )
  }

  return (
    <Card title="市场资金流与量能">
      <div data-testid="flow-factor-card">{body}</div>
    </Card>
  )
}
