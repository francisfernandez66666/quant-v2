// SignalDecisionsCard.jsx — 今日「动态仓位计算过程」上屏（§DECISION-TODAY，2026-10-28）。
//
// 页面职责：把 /api/signals/decisions 的当日快照按“计算链”摊开——
//   因子拆分（MS/SS/单笔占比）→ 账户目标层（总资产/现金/目标/当前/剩余）→
//   三道额度帽（行业/单票/簇）→ 结果（数量/金额/受限项/原因）。
// 被拦/未下单的信号同样列出来（stage=skipped）：这样“今天为什么没买这只票”不靠翻日志。
//
// 口径纪律：
//   1) 每日重新定权：接口只回当日；新交易日没有快照时明说“今日尚未定权”；
//   2) 缺读数不伪造成 0/中性：market_scale=0 显示“尚未定权”，missing_key_input 明确标红；
//   3) 非 admin 访问 403 时整卡不渲染（该接口是 admin 观测面）。
import React, { useEffect, useState } from 'react'
import { Card, Table, Tag } from 'tdesign-react'
import * as api from '../api/index.js'

/** 金额：千分位两位小数；非数字显示“—”。 */
function money(v) {
  const n = Number(v)
  if (!Number.isFinite(n)) return '—'
  return n.toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: 0 })
}

/** 阶段标签：placed/evaluated 视为参与执行，skipped 为被拦，rejected 为下单被拒。 */
function stageTag(stage) {
  switch (stage) {
    case 'placed':
    case 'evaluated':
      return <Tag size="small" theme="success">参与执行</Tag>
    case 'rejected':
      return <Tag size="small" theme="danger">下单被拒</Tag>
    case 'skipped':
      return <Tag size="small" theme="warning">被拦</Tag>
    default:
      return <Tag size="small" theme="default">{stage || '—'}</Tag>
  }
}

export default function SignalDecisionsCard() {
  const [data, setData] = useState(null)
  const [error, setError] = useState('')
  const [forbidden, setForbidden] = useState(false)

  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const res = await api.fetchSignalDecisions()
        if (!alive) return
        setData(res)
        setError('')
      } catch (e) {
        if (!alive) return
        if (api.isForbidden(e)) {
          setForbidden(true)
          return
        }
        setError(e && e.message ? e.message : '读取失败')
      }
    }
    load()
    const timer = setInterval(load, 30000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [])

  if (forbidden) return null

  const items = (data && Array.isArray(data.items)) ? data.items : []
  const columns = [
    { colKey: 'at', title: '计算时刻', width: 110, cell: ({ row }) => <span style={{ fontSize: 12 }}>{String(row.at || '').replace('T', ' ').slice(11, 19) || '—'}</span> },
    { colKey: 'code', title: '代码', width: 95 },
    { colKey: 'strategy_key', title: '战法', width: 90, ellipsis: true, cell: ({ row }) => <span style={{ fontSize: 12 }}>{row.strategy_key || '—'}</span> },
    { colKey: 'stage', title: '阶段', width: 90, cell: ({ row }) => stageTag(row.stage) },
    {
      colKey: 'scale', title: '定权 MS / SS / 单笔', width: 150,
      cell: ({ row }) => (
        <span style={{ fontSize: 12 }}>
          {`${Number(row.market_scale || 0).toFixed(3)} / ${Number(row.stock_scale || 0).toFixed(3)} / ${(Number(row.per_trade_pct || 0) * 100).toFixed(1)}%`}
        </span>
      ),
    },
    {
      colKey: 'target', title: '目标 / 当前 / 剩余', width: 170,
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{`${money(row.target)} / ${money(row.cur_strat)} / ${money(row.room)}`}</span>,
    },
    {
      colKey: 'rooms', title: '额度余量 行业/单票/簇', width: 160,
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{`${money(row.industry_room)} / ${money(row.stock_room)} / ${money(row.cluster_room)}`}</span>,
    },
    {
      colKey: 'qty', title: '数量 / 金额', width: 120,
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{`${Number(row.qty) || 0} 股 / ${money(row.notional)}`}</span>,
    },
    {
      colKey: 'reason', title: '受限项 / 原因', ellipsis: true,
      cell: ({ row }) => {
        const txt = row.reason || row.capped_by || '—'
        return (
          <span title={txt} style={{ fontSize: 12 }}>
            {row.capped_by ? <Tag size="small" variant="light" style={{ marginRight: 4 }}>{row.capped_by}</Tag> : null}
            <span title={row.reason || ''}>{row.reason || (row.capped_by ? '' : '—')}</span>
          </span>
        )
      },
    },
  ]

  // 缺读数/关键输入红标：一行里只要 missing_key_input 就明确提示（目标封顶半仓且今日不做 T）。
  const anyMissing = items.some((x) => x.missing_key_input)

  return (
    <Card title={(
      <span>
        今日动态仓位（信号仓）
        <Tag size="small" variant="light" style={{ marginLeft: 8 }}>每日重新定权，不累计</Tag>
        {anyMissing ? <Tag size="small" theme="warning" style={{ marginLeft: 6 }}>关键读数缺失</Tag> : null}
      </span>
    )} style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 8 }}>
        数据日 {data ? data.day : '—'} · 共 {items.length} 条（含被拦/未下单）
        {data && items.length === 0 ? ' · 今日尚未定权（引擎还没有产生动态仓位快照）' : ''}
      </div>
      {error ? <div style={{ color: 'var(--td-warning-color)', fontSize: 12, marginBottom: 8 }}>⚠ {error}</div> : null}
      <Table
        rowKey={(row) => `${row.code}|${row.strategy_key}|${row.signal_id || ''}`}
        data={items}
        columns={columns}
        size="small"
        bordered
        empty="今日尚未定权（或该账号今天还没有买入评估）"
      />
    </Card>
  )
}
