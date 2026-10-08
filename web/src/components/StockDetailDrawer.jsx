// ── 全局个股详情抽屉 StockDetailDrawer.jsx ──
// §F3 结构性增强：一个组件收敛 Signals/Positions/Paper/MsgCenter/Watchlist 五处"点开个股"的需求，
// 替代此前各页各做各的移动端面板。内容 = 实时价（fetchStockLookup，5s 轮询）+ 分时/盘口（复用 MinuteView）
// + 所属信号 / 持仓 / 相关消息三段关联信息（由调用方传入原始列表，本组件按代码过滤，缺失段自动隐藏）。
// 桌面为右侧滑入浮层，窄屏（≤640px）转为底部抽屉；Esc / 遮罩 / 关闭按钮均可收起。
//
// English: the §F3 global stock-detail drawer — one component shared by Signals/Positions/Paper/MsgCenter/
// Watchlist to replace each page's ad-hoc mobile panel. Body = live price (fetchStockLookup, 5s poll) +
// intraday/depth (reuses MinuteView) + associated signals / positions / related messages (raw lists passed
// in by the host, filtered by code here; empty sections hide themselves). Right slide-over on desktop,
// bottom sheet on narrow screens (≤640px); closes on Esc / overlay click / close button.
import React, { useState, useEffect } from 'react'
import * as api from '../api/index.js'
// §P2-I（2026-10-06 修复批 波 6）抽屉自己的 5s 行情轮询同样在射程内：本组件被五页共用，
// 而 §P3-FE P4 又让涨幅也吃这份读数 ⇒ 旧响应后到时覆盖的不只是现价，而是整行「价 + 涨幅」，
// 用户看到的就是"价格跳回去了"。守卫实例必须由 useStaleGuard() 持有（跨渲染复用，见 hook 注释）。
import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）
import MinuteView from './MinuteView.jsx'

// codeEq 归一化比对：兼容 "600000" 与 "600000.SH/.SZ" 两种写法，任一前缀（6 位数字）相同即视为同一标的。
// English: tolerant code match — treats "600000" and "600000.SH" as the same symbol via its 6-digit prefix.
function codeEq(a, b) {
  if (!a || !b) return false
  // 代码取前 6 位数字做同股比对键
  const d = (x) => String(x).slice(0, 6)
  return String(a) === String(b) || d(a) === d(b)
}

// fmtPct 涨跌幅带符号（红涨绿跌色由调用处决定）。English: signed change% string.
function fmtPct(v) {
  const n = Number(v)
  if (!Number.isFinite(n)) return ''
  return (n >= 0 ? '+' : '') + n.toFixed(2) + '%'
}

// §P3-FE P4（涨幅同源，2026-10-06 修复批 波 6）——读数解算整块提到模块作用域并导出，
// 一是让单测能对「两次 lookup、涨幅跟着第二次变」做**等值**断言（而不是挂载后看个大概），
// 二是让门禁静态锁能钉住判据本身（判据写在组件 JSX 里的话，锁只能数颜色字符串，改错了判据看不见）。
//
// 缺陷本体：旧实现 `const rawChg = changePct` 用的是**打开抽屉那一刻**宿主表格传进来的冻结值，
// 而同一张卡头的现价每 5s 从 fetchStockLookup 刷新 ⇒ 抽屉里会出现「价格已经涨上去了、
// 涨幅还是开抽屉那一刻的数」的自相矛盾读数（AUDIT_20261005 P3 / Drawer:86）。
// 修法按推荐口径「涨幅与现价出自**同一份读数**」：
//   ① 同批 change_pct 可用就用它（后端取不到行情时 price 与 change_pct 一起回 0，见 handlers_fix.go:1838，
//      所以必须先看同批 price>0，不能只看 change_pct 是不是数）；
//   ② change_pct 缺席（老后端/降级）但同批 prev_close>0 时自己按同批价格算，仍是实时腿；
//   ③ 两条实时腿都没有，才回落 props 的冻结值，并在旁边明确标注「开抽屉时刻值」——
//      不再让用户把陈旧读数当成实时读数（P5 反证：改回只读 props 即红）。
// numOrNil：null/undefined/空串/非数一律当「没有读数」返回 null。旧写法 Number(v) 会把 null 折成 0，
// 于是「宿主没传涨幅」被渲染成 "+0.00%"——缺数冒充平盘与 §P2-F 降级链写平盘是同族，这里一并收掉。
// English: pure resolvers for the drawer reading row, exported so tests can assert exact values.

/**
 * 把「可能是缺数」的输入解成数字或 null（缺数绝不折叠成 0）。
 * @param {unknown} v 原始值（数字/字符串/null/undefined/空串）
 * @returns {number|null} 可解析则返回数字，否则 null
 */
export function numOrNil(v) {
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  return Number.isFinite(n) ? n : null
}

/**
 * 同批行情里的可信现价：lookup 缺体、price 不可解析或 price<=0（后端取不到行情时回 0）一律算没有读数。
 * @param {object|null} quote fetchStockLookup 的返回体
 * @returns {number|null}
 */
export function batchPrice(quote) {
  if (quote == null) return null
  const p = numOrNil(quote.price)
  return p !== null && p > 0 ? p : null
}

/**
 * 用同一批的 price/prev_close 自己算涨幅（%），与后端 change_pct 同为百分数口径。
 * @param {number|null} price 同批现价（必须 >0）
 * @param {number|null} prevClose 同批昨收（必须 >0）
 * @returns {number|null} 任一不可信则 null；可信则按后端 r2 口径收拢到 2 位小数
 */
export function deriveChg(price, prevClose) {
  if (price === null || prevClose === null) return null
  if (!(price > 0) || !(prevClose > 0)) return null
  return Math.round(((price - prevClose) / prevClose) * 100 * 100) / 100
}

/**
 * 解算抽屉头部的「价 / 涨幅 / 涨幅是否为冻结值 / 涨跌方向」四元读数。
 * 纯函数、无副作用，返回的 frozen 就是「开抽屉时刻值」标注的开关。
 * @param {object|null} quote fetchStockLookup 的返回体（null 表示还没拉到）
 * @param {number|string|null} propChangePct 宿主打开抽屉时传入的涨幅（冻结值，只作最后兜底）
 * @returns {{price: number|null, chg: number|null, live: boolean, frozen: boolean, up: boolean}}
 */
export function resolveDrawerChg(quote, propChangePct) {
  const price = batchPrice(quote)
  // 实时腿：同批 change_pct 优先，其次同批 price/prev_close 自算
  let liveChg = null
  if (price !== null && quote != null) {
    const fromField = numOrNil(quote.change_pct)
    liveChg = fromField !== null ? fromField : deriveChg(price, numOrNil(quote.prev_close))
  }
  const propChg = numOrNil(propChangePct)
  const chg = liveChg !== null ? liveChg : propChg
  return {
    price,
    chg,
    live: liveChg !== null,
    // frozen＝这一轮 lookup 没给出任何实时涨幅，屏上显示的是开抽屉那一刻的值
    frozen: liveChg === null && chg !== null,
    // 缺读数（chg===null）时方向不参与着色：旧写法无条件按 chgUp 染色会把「不知道涨跌」染成跌绿
    up: chg !== null && chg >= 0,
  }
}

// RelList 抽屉内关联列表（板块/概念/同行业个股）通用渲染子件。
function RelList({ items, render }) {
  return (
    <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 6 }}>
      {items.map((it, i) => (
        <li key={i} style={{ fontSize: 12, lineHeight: 1.5, padding: '6px 8px', background: 'var(--app-surface-2)', borderRadius: 6 }}>
          {render(it)}
        </li>
      ))}
    </ul>
  )
}

/**
 * 全局个股详情抽屉。
 * @param {object} props
 * @param {boolean} props.open 是否展开
 * @param {string} props.code 标的代码
 * @param {string} [props.name] 标的名称（未传则用 lookup 返回名兜底）
 * @param {number} [props.price] 初始现价（行数据已有则先显示，避免打开瞬间空白）
 * @param {number} [props.changePct] 初始涨跌幅%
 * @param {{signals?:Array,positions?:Array,messages?:Array}} [props.related] 关联原始列表（内部按 code 过滤）
 * @param {() => void} props.onClose 关闭回调
 */
export default function StockDetailDrawer({ open, code, name, price, changePct, related, onClose }) {
  const [quote, setQuote] = useState(null)
  // §P2-I：5s 轮询的代号守卫（切股/关抽屉重开时也各算一轮）
  const lookupGuard = useStaleGuard()

  useEffect(() => {
    if (!open || !code) { setQuote(null); return }
    let alive = true
    // 拉取行情快照回填抽屉头部
    const load = () => {
      // §P2-I：代号在 fetch 之前盖章；alive（组件卸载/换股）管的是"还要不要写"，
      // 本代号管的是"这轮是不是最新的一轮"——两个都过才写，缺一个就还有半边覆盖窗口。
      const token = lookupGuard.begin()
      api.fetchStockLookup(code)
        .then((r) => { if (alive && r && !lookupGuard.isStale(token)) setQuote(r) })
        .catch(() => { /* §P2-J 可吞：抽屉行情是旁证读数，失败保留上一份快照并在原价上标注，不打断宿主页面 */ })
    }
    load()
    const t = setInterval(load, 5000)
    return () => { alive = false; clearInterval(t) }
  }, [open, code])

  useEffect(() => {
    if (!open) return
    // Esc 关闭抽屉
    const h = (e) => { if (e.key === 'Escape' && onClose) onClose() }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [open, onClose])

  if (!open || !code) return null

  // 展示名：行情返回优先，回落入参名称/代码
  const showName = (quote && quote.name) || name || code
  // §P3-FE P4：价、涨幅、是否冻结、涨跌方向**一次解算**（判据见文件头的 resolveDrawerChg）——
  // 留在这里的只有「同批没价格时显示宿主传入的初始价」这一条展示兜底。
  const reading = resolveDrawerChg(quote, changePct)
  const showPrice = reading.price !== null ? reading.price : price
  const chg = reading.chg
  // chgFrozen＝本轮 lookup 没给出涨幅、当前显示的是开抽屉时刻的涨幅
  const chgFrozen = reading.frozen
  const chgUp = reading.up

  const rel = related || {}
  // 关联数据按本股代码过滤（信号/持仓/消息）
  const mySignals = (rel.signals || []).filter((s) => codeEq(s.code, code))
  const myPositions = (rel.positions || []).filter((p) => codeEq(p.code, code))
  const myMessages = (rel.messages || []).filter((m) => codeEq(m.code, code))

  // 抽屉骨架：全屏遮罩（点击关闭）+ 右侧滑入面板，面板内依次为头部行情、明细与关联信息各段（见下方 JSX）
  return (
    <div
      className="sdd-mask"
      data-testid="stock-detail-overlay"
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,0.45)',
        display: 'flex', justifyContent: 'flex-end',
      }}
    >
      <div
        className="sdd-panel"
        data-testid="stock-detail-panel"
        onClick={(e) => e.stopPropagation()}
        style={{
          width: 'min(520px, 100vw)', height: '100%', background: 'var(--app-surface)', display: 'flex', flexDirection: 'column',
          boxShadow: '-2px 0 12px rgba(0,0,0,0.18)', animation: 'sdd-slide-in .18s ease-out',
        }}
      >
        <style>{'@keyframes sdd-slide-in{from{transform:translateX(24px);opacity:.4}to{transform:none;opacity:1}}'
          + '@media(max-width:640px){.sdd-panel{width:100vw!important;height:auto!important;max-height:88vh;border-radius:14px 14px 0 0;margin-top:auto}'
          + '.sdd-mask{align-items:flex-end!important}}'}</style>
        {/* 头部：代码/名称/现价/涨跌幅 + 关闭 */}
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, padding: '14px 16px', borderBottom: '1px solid #eef0f3' }}>
          <span style={{ fontSize: 18, fontWeight: 700 }}>{showName}</span>
          <span style={{ fontSize: 12, color: 'var(--app-muted-2)' }}>{code}</span>
          {Number.isFinite(Number(showPrice)) && showPrice > 0 && (
            // §P3-FE P8/P9 同族：旧写法无条件按 chgUp 染色，而 chg 为 null（完全没有涨幅读数）时
            // chgUp=false ⇒ 现价被染成跌绿，把「不知道涨跌」显示成「在跌」。缺读数时走中性弱化色。
            <span data-testid="sdd-price" style={{ fontSize: 18, fontWeight: 700, color: chg == null ? 'var(--app-faint)' : (chgUp ? 'var(--app-up)' : 'var(--app-down)') }}>
              {Number(showPrice).toFixed(2)}
            </span>
          )}
          {chg != null && (
            <span data-testid="sdd-chg" style={{ fontSize: 13, fontWeight: 600, color: chgUp ? 'var(--app-up)' : 'var(--app-down)' }}>{fmtPct(chg)}</span>
          )}
          {/* §P3-FE P4：lookup 未给出涨幅时明确标注口径，不让冻结值冒充实时读数 */}
          {chgFrozen && (
            <span style={{ fontSize: 11, color: 'var(--app-faint)' }} data-testid="sdd-chg-frozen">（开抽屉时刻值）</span>
          )}
          <button aria-label="关闭" onClick={onClose}
            style={{ marginLeft: 'auto', border: 'none', background: 'transparent', fontSize: 20, lineHeight: 1, cursor: 'pointer', color: 'var(--app-text-2)' }}>×</button>
        </div>
        {/* 主体：分时/盘口 + 关联信息（可滚动） */}
        <div style={{ flex: 1, overflowY: 'auto', padding: 14 }}>
          <MinuteView code={code} name={showName} />

          {mySignals.length > 0 && (
            <section style={{ marginTop: 16 }}>
              <div style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>相关信号 <span style={{ color: 'var(--app-muted-2)', fontWeight: 400 }}>({mySignals.length})</span></div>
              <RelList items={mySignals} render={(s) => (
                <span>
                  <b>{s.action || (s.direction === '做空' ? '卖出' : '买入')}</b> · {s.strategy}
                  {s.confidence != null && <span style={{ color: 'var(--app-text-2)' }}> 置信度 {(Number(s.confidence) <= 1 ? Number(s.confidence) * 100 : Number(s.confidence)).toFixed(0)}%</span>}
                  {s.reason && <span style={{ color: 'var(--app-muted-2)' }}> {s.reason}</span>}
                </span>
              )} />
            </section>
          )}

          {myPositions.length > 0 && (
            <section style={{ marginTop: 16 }}>
              <div style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>我的持仓 <span style={{ color: 'var(--app-muted-2)', fontWeight: 400 }}>({myPositions.length})</span></div>
              <RelList items={myPositions} render={(p) => (
                <span>
                  {p.direction === '做空' ? '空' : '多'} {p.qty ?? p.quantity ?? ''} · 成本 {Number(p.cost ?? p.cost_price ?? p.entry_price ?? 0).toFixed(2)}
                  {(p.pnl != null || p.pnl_pct != null) && (
                    <span style={{ color: (p.pnl ?? p.pnl_pct) >= 0 ? 'var(--app-up)' : 'var(--app-down)' }}>
                      {p.pnl != null ? ` 浮盈 ${Number(p.pnl).toFixed(2)}` : ''}
                      {p.pnl_pct != null ? ` ${fmtPct(Number(p.pnl_pct))}` : ''}
                    </span>
                  )}
                </span>
              )} />
            </section>
          )}

          {myMessages.length > 0 && (
            <section style={{ marginTop: 16, marginBottom: 8 }}>
              <div style={{ fontSize: 13, fontWeight: 700, marginBottom: 6 }}>相关消息 <span style={{ color: 'var(--app-muted-2)', fontWeight: 400 }}>({myMessages.length})</span></div>
              <RelList items={myMessages.slice(0, 20)} render={(m) => (
                <span>
                  <b>{m.level || '消息'}</b> {m.time ? <span style={{ color: 'var(--app-muted-2)' }}>{m.time}</span> : null} {m.body || m.title || ''}
                </span>
              )} />
            </section>
          )}
        </div>
      </div>
    </div>
  )
}
