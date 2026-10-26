// AnchorShadowCard.jsx — §ANCHOR-FIRST W2 影子对照表上屏（2026-10-26）。
//
// 只读卡片：把 /api/anchor-shadow 返回的"成本口径 vs 首买价锚"逐日/逐账本读数展示出来。
// W2 只对照不切硬；这张卡的全部价值是让 owner 在切硬前看到哪些票的止损/止盈线会移动。
// 缺文件/损坏文件必须显示明确空态，不许伪装成"没有差异"。
import React, { useEffect, useMemo, useState } from 'react'
import { Card, Table, Tag, Select } from 'tdesign-react'
import * as api from '../api/index.js'

const BOOK_ORDER = ['live', 'paper', 'report']
const BOOK_LABEL = { live: '实盘', paper: '纸面', report: '报告' }

function fmtPrice(v) {
  if (v === null || v === undefined) return '—'
  const n = Number(v)
  if (!Number.isFinite(n) || n <= 0) return '—'
  return n.toFixed(3)
}

function fmtPct(v) {
  if (v === null || v === undefined) return '—'
  const n = Number(v)
  if (!Number.isFinite(n)) return '—'
  return `${n >= 0 ? '+' : ''}${n.toFixed(2)}%`
}

export default function AnchorShadowCard() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [data, setData] = useState(null)
  const [selected, setSelected] = useState('')

  useEffect(() => {
    let alive = true
    api.fetchAnchorShadow(20)
      .then((d) => { if (alive) { setData(d); setError('') } })
      .catch((e) => { if (alive) { setData(null); setError(e && (e.message || String(e)) || '读取失败') } })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [])

  // 把接口的 days[].books 摊平成总账行：固定账本顺序，未知账本追加在后。
  const summaryRows = useMemo(() => {
    if (!data || data.state !== 'ok') return []
    const rows = []
    const days = Array.isArray(data.days) ? data.days : []
    for (const day of days) {
      const books = (day && day.books) || {}
      const keys = [...BOOK_ORDER.filter((k) => books[k]), ...Object.keys(books).filter((k) => !BOOK_ORDER.includes(k))]
      for (const book of keys) {
        const s = (books[book] && books[book].summary) || {}
        rows.push({
          key: `${day.day}|${book}`,
          day: day.day,
          book,
          bookLabel: BOOK_LABEL[book] || book,
          rows: Number(s.rows) || 0,
          shifted: Number(s.shifted) || 0,
          diverged: Number(s.diverged) || 0,
          unknown: Number(s.unknown) || 0,
        })
      }
    }
    return rows
  }, [data])

  useEffect(() => {
    if (!selected && summaryRows.length) setSelected(summaryRows[0].key)
    if (selected && summaryRows.length && !summaryRows.some((r) => r.key === selected)) setSelected(summaryRows[0].key)
  }, [summaryRows, selected])

  const detailRows = useMemo(() => {
    if (!selected || !data || data.state !== 'ok') return []
    const [day, book] = selected.split('|')
    const dayEntry = (data.days || []).find((d) => d.day === day)
    const entry = dayEntry && dayEntry.books ? dayEntry.books[book] : null
    const rows = (entry && Array.isArray(entry.rows)) ? entry.rows : []
    return rows.map((r, i) => ({ key: `${selected}|${r.code || ''}|${i}`, ...r }))
  }, [selected, data])

  const summaryColumns = [
    { colKey: 'day', title: '交易日', width: 110 },
    { colKey: 'bookLabel', title: '账本', width: 80 },
    { colKey: 'rows', title: '行数', width: 70 },
    { colKey: 'shifted', title: '线位移动', width: 90 },
    { colKey: 'diverged', title: '判线翻转', width: 90 },
    { colKey: 'unknown', title: '锚点未回填', width: 100 },
  ]

  const detailColumns = [
    { colKey: 'code', title: '代码', width: 100 },
    { colKey: 'name', title: '名称', width: 90, ellipsis: true },
    { colKey: 'cost_price', title: '摊薄成本', width: 90, cell: ({ row }) => <span>{fmtPrice(row.cost_price)}</span> },
    { colKey: 'anchor_price', title: '首买锚', width: 90, cell: ({ row }) => <span>{fmtPrice(row.anchor_price)}</span> },
    { colKey: 'stop_price_by_cost', title: '止损(成本)', width: 100, cell: ({ row }) => <span>{fmtPrice(row.stop_price_by_cost)}</span> },
    { colKey: 'stop_price_by_anchor', title: '止损(锚)', width: 100, cell: ({ row }) => <span>{fmtPrice(row.stop_price_by_anchor)}</span> },
    { colKey: 'line_by_cost', title: '判线(成本)', width: 100, cell: ({ row }) => <span>{row.line_by_cost || '—'}</span> },
    { colKey: 'line_by_anchor', title: '判线(锚)', width: 100, cell: ({ row }) => <span>{row.line_by_anchor || '—'}</span> },
    { colKey: 'pnl_by_cost', title: '涨跌(成本)', width: 100, cell: ({ row }) => <span>{fmtPct(row.pnl_by_cost)}</span> },
    { colKey: 'pnl_by_anchor', title: '涨跌(锚)', width: 100, cell: ({ row }) => <span>{fmtPct(row.pnl_by_anchor)}</span> },
    { colKey: 'line_changed', title: '切硬会变', width: 90, cell: ({ row }) => <Tag size="small" theme={row.line_changed ? 'warning' : 'default'}>{row.line_changed ? '会变' : '不变'}</Tag> },
    { colKey: 'anchor_known', title: '锚点', width: 90, cell: ({ row }) => <Tag size="small" theme={row.anchor_known ? 'success' : 'default'}>{row.anchor_known ? '已回填' : '未回填'}</Tag> },
  ]

  const state = data && data.state
  return (
    <Card
      style={{ marginTop: 16 }}
      title={(
        <span>
          影子对照（成本 vs 首买价锚）
          <Tag size="small" variant="light" style={{ marginLeft: 8 }}>W2 只对照不切硬</Tag>
        </span>
      )}
    >
      {loading && <div className="muted">加载中…</div>}
      {!loading && error && <div style={{ color: 'var(--app-danger, #d54941)' }}>读取失败：{error}</div>}
      {!loading && !error && state === 'missing' && (
        <p className="muted">{data.reason || '暂无影子对照数据（引擎在当日首轮裁决后生成）'}</p>
      )}
      {!loading && !error && state === 'corrupt' && (
        <p style={{ color: 'var(--app-warning, #e37318)' }}>{data.reason || '影子对照文件损坏（原文件未改动）'}</p>
      )}
      {!loading && !error && state === 'ok' && summaryRows.length === 0 && (
        <p className="muted">暂无有效裁决读数（空表不交；等当日首轮裁决后再看）。</p>
      )}
      {!loading && !error && state === 'ok' && summaryRows.length > 0 && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10, flexWrap: 'wrap' }}>
            <span className="muted">查看明细：</span>
            <Select value={selected} onChange={(v) => setSelected(v)} size="small" style={{ width: 260 }}>
              {summaryRows.map((r) => (
                <Select.Option key={r.key} value={r.key} label={`${r.day} / ${r.bookLabel}`} />
              ))}
            </Select>
            <span className="muted" style={{ fontSize: 12 }}>
              账本合计：{summaryRows.length} 组；当日只记首轮读数，逐票线位移动见下表。
            </span>
          </div>
          <Table data={summaryRows} columns={summaryColumns} rowKey="key" size="small" pagination={false} />
          <div style={{ marginTop: 12 }}>
            <Table data={detailRows} columns={detailColumns} rowKey="key" size="small" pagination={{ pageSize: 10, total: detailRows.length, showJumper: true, pageSizeOptions: [10, 20, 50] }} empty="该账本当日无对照行" />
          </div>
        </>
      )}
    </Card>
  )
}
