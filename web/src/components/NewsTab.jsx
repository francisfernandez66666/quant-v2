// ── 新闻 tab 组件 NewsTab.jsx ──
// §B6（2026-11-05 通知增强）消息中心新增「新闻」tab：利多且未命中持仓/自选的新闻
// 在这里落一行，**不占必推通道、也不混进信号流**。
//
// 三条纪律（都是本仓反复出事的形状，逐条写死在实现里）：
//  1. 只留当日：数据来自 `GET /api/alerts?kind=news`（同一路由加 query，零新路由），
//     跨交易日即清由**服务端消息中心既有的 day-key** 判，本组件不自算第二套日界线；
//     未读数的"已读到"标记按日存 localStorage，于是换日自动从 0 开始（键不同＝自然归零）。
//  2. 折叠必须可见：出站侧超预算时会在行里带一条「今日已折叠 N 条」，这里原样渲染，
//     不汇总、不省略——静默折叠等于把通知系统做成黑箱。
//  3. 空态不许假 0：读数没拿到（loading/error）时不画 "0 条"；拿到读数且确实为 0 才说
//     "今日暂无新闻"。"取不到"与"没有"是两件事（J0 零回包、自选卡 J-03 同族教训）。
import React, { useState, useEffect, useMemo, useCallback } from 'react'
import { Card, Tag, Button } from 'tdesign-react'
import * as api from '../api/index.js'
import { useStaleGuard } from '../utils/staleGuard.js'

// 新闻行的种类串（与后端 data.KindNews 同源；改这里必须同批改后端与读接口判据）
export const NEWS_KIND = 'news'
// 一次把当日新闻取全的量（分页在**客户端**做：未读数要按整日算，只拿一页会把未读算少）
export const NEWS_FETCH_SIZE = 500
// localStorage 键前缀：`newsTabLastSeen.<YYYY-MM-DD>` —— 按日一把键，换日即新键＝未读从满开始
export const LAST_SEEN_PREFIX = 'newsTabLastSeen'

/** 从 generated_at 取 YYYY-MM-DD（后端给的可能是 ISO 串，也可能已经是 '2026-11-05 10:20:00'）。 */
export function dayKeyOf(raw) {
  if (!raw) return ''
  const s = String(raw)
  const m = s.match(/(\d{4})-(\d{2})-(\d{2})/)
  return m ? `${m[1]}-${m[2]}-${m[3]}` : ''
}

/** 读当日"已读到"时刻；缺失返回 0（＝当日第一次打开，全部算未读）。 */
export function readLastSeen(dayKey) {
  if (!dayKey) return 0
  try {
    const v = Number(window.localStorage.getItem(`${LAST_SEEN_PREFIX}.${dayKey}`))
    return Number.isFinite(v) ? v : 0
  } catch (_) {
    // 隐私模式/配额坏掉都不许让页面崩：退化成"每次都按未读处理"
    return 0
  }
}

/** 写当日"已读到"时刻（epoch ms）。 */
export function writeLastSeen(dayKey, ts) {
  if (!dayKey) return
  try {
    window.localStorage.setItem(`${LAST_SEEN_PREFIX}.${dayKey}`, String(ts))
  } catch (_) {
    // 写不进去只是"下次进来还显示未读"，不影响数据正确性
  }
}

/** 时间戳解析：失败给 0，让该行在比较里排最后而不是冒充"最新"。 */
function tsOf(row) {
  const raw = row && (row.generated_at || row.time)
  if (!raw) return 0
  const t = new Date(String(raw).replace(' ', 'T')).getTime()
  return Number.isFinite(t) ? t : 0
}

/**
 * 新闻 tab：拉当日新闻行 → 客户端分页 → 未读数 → 折叠行原样可见。
 * @param {boolean} active 当前是否显示在本 tab（打开时把"已读到"推进到当刻，未读清零）
 * @param {(n: number) => void} onUnreadChange 把未读数回传给消息中心（页签角标用）
 */
export default function NewsTab({ active = true, pageSize = 20, onUnreadChange }) {
  const [rows, setRows] = useState([])
  // total 用 null 表示"还没有读数"：渲染侧据此区分「取不到」与「确实是 0 条」
  const [total, setTotal] = useState(null)
  const [error, setError] = useState('')
  const [page, setPage] = useState(1)
  const [seenAt, setSeenAt] = useState(0)
  const guard = useStaleGuard()

  // 读接口就是消息中心那条 GET /api/alerts，只多一个 kind=news（§5A.4：零新路由）
  const load = useCallback(async () => {
    const token = guard.begin()
    try {
      const res = await api.request(
        `/api/alerts?kind=${NEWS_KIND}&page=1&page_size=${NEWS_FETCH_SIZE}`,
        { timeout: 15000, withMeta: true },
      )
      if (guard.isStale(token)) return
      const list = Array.isArray(res && res.data) ? res.data : []
      // 响应头缺失（代理吞头）时退回数组长度：宁可总数偏小，也不画一个不存在的读数
      const h = res && res.headers
      const rawTotal = h && typeof h.get === 'function' ? Number(h.get('X-Total-Count')) : NaN
      setRows(list)
      setTotal(Number.isFinite(rawTotal) && rawTotal >= 0 ? rawTotal : list.length)
      setError('')
    } catch (e) {
      if (guard.isStale(token)) return
      setError(e && e.message ? String(e.message) : '新闻读取失败')
    }
  }, [guard])

  useEffect(() => {
    load()
    // 60s 兜底轮询：新闻是盘中持续到达的，不轮询就会停在打开那一刻
    const timer = setInterval(load, 60000)
    return () => clearInterval(timer)
  }, [load])

  // 当日 day-key 取自行内时间（服务端已按交易日日切清行，这里只是给未读标记配一把日键）
  const dayKey = useMemo(() => dayKeyOf(rows.length ? rows[0].generated_at : ''), [rows])

  // 打开本 tab 即把"已读到"推到当刻：未读归零只在**当天**生效，换日是新键＝重新计
  useEffect(() => {
    if (!active || !dayKey) return
    const ts = Date.now()
    writeLastSeen(dayKey, ts)
    setSeenAt(ts)
  }, [active, dayKey, rows.length])

  const unread = useMemo(() => {
    const mark = seenAt || readLastSeen(dayKey)
    if (!mark) return rows.length // 当日从没打开过＝全部未读（不许画 0 冒充看过）
    return rows.filter((r) => tsOf(r) > mark).length
  }, [rows, dayKey, seenAt])

  useEffect(() => {
    if (typeof onUnreadChange === 'function') onUnreadChange(unread)
  }, [unread, onUnreadChange])

  // 客户端分页：page 越界时钳制（与消息中心同姿势）
  const totalPages = Math.max(1, Math.ceil(rows.length / pageSize))
  const curPage = Math.min(Math.max(1, page), totalPages)
  const paged = rows.slice((curPage - 1) * pageSize, curPage * pageSize)

  return (
    <div>
      {/* 顶部读数条：未读 + 当日总条数。读数没拿到时整条不画（不画假 0） */}
      {total !== null && !error && (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 10, flexWrap: 'wrap' }}>
          <Tag theme={unread > 0 ? 'danger' : 'default'} size="small">未读 {unread}</Tag>
          <span style={{ fontSize: 12, color: 'var(--app-muted-2)' }}>当日 {total} 条 · 只留当日，切交易日即清</span>
        </div>
      )}

      {/* 三种状态分开渲染：加载失败 ≠ 今天没有新闻（这是 §M-9/J0 一族的老坑） */}
      {paged.map((r, i) => (
        <Card key={r.id || i} style={{ marginBottom: 8, borderLeft: `4px solid ${r.direction === '利空' ? 'var(--app-up)' : 'var(--app-down)'}` }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 6 }}>
            <Tag theme={String(r.title || '').startsWith('今日已折叠') ? 'warning' : 'primary'} size="small">
              {String(r.title || '').startsWith('今日已折叠') ? '降噪' : '新闻'}
            </Tag>
            <span style={{ fontFamily: 'monospace', color: 'var(--app-accent)' }}>{r.code}</span>
            <span style={{ fontSize: 13, color: 'var(--app-text-2)' }}>{r.name}</span>
            <span style={{ flex: 1 }} />
            <span style={{ fontSize: 12, color: 'var(--app-muted-2)' }}>{String(r.generated_at || r.time || '').replace('T', ' ').slice(0, 19)}</span>
          </div>
          <div style={{ fontSize: 14, fontWeight: 600 }}>{r.title}</div>
          <div style={{ fontSize: 13, color: 'var(--app-muted-2)', marginTop: 4, whiteSpace: 'pre-line' }}>{r.body}</div>
        </Card>
      ))}

      {/* 分页控件：只有真超过一页才出现（一页时摆控件是骗交互） */}
      {rows.length > pageSize && (
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 12, padding: '12px 0' }}>
          <Button size="small" variant="outline" disabled={curPage <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>上一页</Button>
          <span style={{ color: 'var(--app-muted-2)', fontSize: 13 }}>第 {curPage} / {totalPages} 页 · 共 {rows.length} 条</span>
          <Button size="small" variant="outline" disabled={curPage >= totalPages} onClick={() => setPage((p) => Math.min(totalPages, p + 1))}>下一页</Button>
        </div>
      )}

      {error ? (
        <div style={{ textAlign: 'center', padding: 40, color: 'var(--td-warning-color)', fontSize: 13 }}>
          ⚠ 新闻读取失败：{error}
          <div style={{ marginTop: 8 }}>
            <Button size="small" variant="outline" theme="warning" onClick={load}>重试</Button>
          </div>
          <div style={{ marginTop: 6, fontSize: 12 }}>这是「取不到」，不是「今天没有新闻」——两者不许互相冒充。</div>
        </div>
      ) : (
        total !== null && rows.length === 0 && (
          <div style={{ textAlign: 'center', padding: 60, color: 'var(--app-text-2)' }}>今日暂无新闻</div>
        )
      )}
    </div>
  )
}
