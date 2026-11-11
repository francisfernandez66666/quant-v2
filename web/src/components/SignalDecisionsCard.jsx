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

// 2026-11-11 owner 需求：决策子表表头点击也必须能按数字大小排序。此前该表一列 sorter
// 都没有，点击表头无反应，30s 轮询又整体替换列表，看起来就是"乱序"。与 Signals.jsx 同一套
// 数值解析口径：格式化串/缺值不产生 NaN（NaN 会让 JS sort 把整表当相等，保持引擎原序）。
// English: numeric-aware comparators for the decisions table; unparsable values sink.
function decisionNum(v) {
  if (v == null || v === '') return null
  if (typeof v === 'number') return Number.isFinite(v) ? v : null
  const cleaned = String(v).replace(/[^0-9.+\-]/g, '')
  if (!cleaned) return null   // 纯占位符（—/-/空）清洗后为空串，不能当 0
  const n = Number(cleaned)
  return Number.isFinite(n) ? n : null
}
function decisionNumSorter(key) {
  return (a, b) => {
    const x = decisionNum(a[key]); const y = decisionNum(b[key])
    if (x == null && y == null) return String(a.code || '').localeCompare(String(b.code || ''))
    if (x == null) return 1
    if (y == null) return -1
    if (x === y) return String(a.code || '').localeCompare(String(b.code || ''))
    return x - y
  }
}
function decisionStrSorter(key) {
  return (a, b) => String(a[key] == null ? '' : a[key]).localeCompare(String(b[key] == null ? '' : b[key]), 'zh-CN')
}

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
  // 受控排序：渲染层自行排序（TDesign 受控 data 下内部排序会被覆盖）。
  const [sort, setSort] = useState(null)
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
  const dcSorters = {
    at: decisionStrSorter('at'), channel: decisionStrSorter('channel'),
    code: decisionStrSorter('code'), strategy_key: decisionStrSorter('strategy_key'),
    stage: decisionStrSorter('stage'), scale: decisionNumSorter('market_scale'),
    target: decisionNumSorter('target'), rooms: decisionNumSorter('industry_room'),
    qty: decisionNumSorter('qty'), reason: decisionStrSorter('reason'),
  }
  const dActive = Array.isArray(sort) ? sort[0] : sort
  const dBy = dActive && (dActive.sortBy || dActive.colKey)
  let displayItems = items
  if (dBy && dcSorters[dBy]) {
    const cmp = dcSorters[dBy]
    displayItems = [...items].sort(dActive.descending ? (a, b) => cmp(b, a) : cmp)
  }
  const columns = [
    { sorter: decisionStrSorter('at'), colKey: 'at', title: '计算时刻', width: 110, cell: ({ row }) => <span style={{ fontSize: 12 }}>{String(row.at || '').replace('T', ' ').slice(11, 19) || '—'}</span> },
    // §FIX-20261028-F19a：通道列（实盘自动 / 手动直发 / 纸面）。此前三个通道在屏上无法区分，
    // 客户看到一行无法判断"这是引擎算的还是我手点的"。manual 行的定权列显示「—」（不伪造 MS/SS）。
    { sorter: decisionStrSorter('channel'), colKey: 'channel', title: '通道', width: 80, cell: ({ row }) => {
      const map = { live: ['实盘', 'primary'], manual: ['手动', 'warning'], paper: ['纸面', 'default'] }
      const [label, theme] = map[row.channel] || ['实盘', 'primary']
      return <Tag size="small" theme={theme} variant="light">{label}</Tag>
    } },
    { sorter: decisionStrSorter('code'), colKey: 'code', title: '代码', width: 95 },
    { sorter: decisionStrSorter('strategy_key'), colKey: 'strategy_key', title: '战法', width: 90, ellipsis: true, cell: ({ row }) => <span style={{ fontSize: 12 }}>{row.strategy_key || '—'}</span> },
    { sorter: decisionStrSorter('stage'), colKey: 'stage', title: '阶段', width: 90, cell: ({ row }) => stageTag(row.stage) },
    {
      colKey: 'scale', title: '定权 MS / SS / 单笔', width: 150, sorter: decisionNumSorter('market_scale'),
      cell: ({ row }) => (
        <span style={{ fontSize: 12 }}>
          {/* §FIX-20261028-F19a：手动直发不经动态预算 ⇒ 这三列对它没有意义，显示「—」
              而不是 0.000/0.0%（0 会被读成"算出来是 0"）。
              §W-02（B5）：纸面行没有个股缩放（StockScale 恒 0，表里留 0 并由 MissingKeyInput 标），
              同样显示「—」——0.000 会被读成"个股因子算出来是 0"，那是假读数。 */}
          {row.channel === 'manual' || (row.channel === 'paper' && !(Number(row.stock_scale) > 0))
            ? '—'
            : `${Number(row.market_scale || 0).toFixed(3)} / ${Number(row.stock_scale || 0).toFixed(3)} / ${(Number(row.per_trade_pct || 0) * 100).toFixed(1)}%`}
        </span>
      ),
    },
    {
      colKey: 'target', title: '目标 / 当前 / 剩余', width: 170, sorter: decisionNumSorter('target'),
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{`${money(row.target)} / ${money(row.cur_strat)} / ${money(row.room)}`}</span>,
    },
    {
      colKey: 'rooms', title: '额度余量 行业/单票/簇', width: 160, sorter: decisionNumSorter('industry_room'),
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{`${money(row.industry_room)} / ${money(row.stock_room)} / ${money(row.cluster_room)}`}</span>,
    },
    {
      colKey: 'qty', title: '数量 / 金额', width: 120, sorter: decisionNumSorter('qty'),
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{`${Number(row.qty) || 0} 股 / ${money(row.notional)}`}</span>,
    },
    {
      colKey: 'reason', title: '受限项 / 原因', ellipsis: true, sorter: decisionStrSorter('reason'),
      cell: ({ row }) => {
        // §P1-3（2026-10-30）：在途买单占用单独点名。事故当天这里只有 target，
        // 看不出 3.6 万是被两笔卡在「撤单中」的买单占着的。
        const pending = row.pending_note ? `在途占用：${row.pending_note}` : ''
        const txt = [row.reason, pending].filter(Boolean).join('；') || row.capped_by || '—'
        return (
          <span title={txt} style={{ fontSize: 12 }}>
            {row.capped_by ? <Tag size="small" variant="light" style={{ marginRight: 4 }}>{row.capped_by}</Tag> : null}
            <span title={txt}>{txt}</span>
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
        data={displayItems}
        columns={columns}
        sort={sort}
        onSortChange={(val) => setSort(val || null)}
        size="small"
        bordered
        empty="今日尚无决策快照（自动买入评估未触发；手动直发单会以「手动」通道出现）"
      />
    </Card>
  )
}
