// FundsOverviewCard.jsx — 当日「信号仓 / T 仓」资金一帧上屏（§FUNDS-TODAY，2026-10-28）。
//
// 页面职责：把 /api/funds/today 返回的两本钱一次摆清楚——
//   · 信号仓：券商可用/冻结/总资产/市值 + 目标/当前/剩余（MS 未注入时显示“尚未定权”）；
//   · T 仓：T 额度、可用 T 现金、反 T 冻结、在途正/反 T 腿市值、当日 T 已实现净额；
//   · 纸面：同一结构（来自 TargetReadout），与实盘并列但互不混算。
//
// 口径纪律（写在组件里是为了防止后人“顺手补一个 0”）：
//   1) 每日清零：接口只回当日；state=pending 表示新交易日首帧还没写下，显示“今日尚未定权”，
//      **绝不**拿昨日数字顶上；
//   2) 实盘 cash_t 与纸面 cash_t 公式不同（实盘扣战略剩余空间，纸面不扣），这里各自展示事实值
//      并标注差异，不在前端重算第二套；
//   3) 资金快照时刻 cash_at 一并显示：金额没变 ≠ 快照没更新；
//   4) 两半各自带取数时刻：at＝信号仓侧、t_at＝T 侧。T 侧没跑过时**保持上一帧**并标出
//      “未更新”，绝不把 0 画成“T 仓真的是空的”（§FUNDS-TODAY 方案 §7.1）；
//   5) 非 admin 访问 403 时整卡不渲染（该接口是 admin 观测面，不在普通成员页制造噪音）。
import React, { useEffect, useState } from 'react'
import { Card, Tag } from 'tdesign-react'
import * as api from '../api/index.js'

/** 金额：千分位 + 两位小数；非法/缺失显示“—”。 */
function money(v) {
  const n = Number(v)
  if (!Number.isFinite(n)) return '—'
  return n.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

/** 比例：转百分比一位小数（0 是合法读数，不特殊处理）。 */
function pct(v) {
  const n = Number(v)
  if (!Number.isFinite(n)) return '—'
  return (n * 100).toFixed(1) + '%'
}

/** 时刻串 → “HH:MM:SS”；空/非法返回空串。 */
function hhmmss(v) {
  const s = String(v || '').replace('T', ' ')
  return s.length >= 19 ? s.slice(11, 19) : s.slice(0, 8)
}

/**
 * T 侧读数的新鲜度：返回 'fresh' | 'stale' | 'idle'。
 *
 * - idle：T 侧从没跑过（t_at 为空）——做 T 停用/当天没做 T，不是"零额度"；
 * - stale：t_at 比本帧的取数时刻旧超过 60s——T 循环可能停了，数字不能当现状看；
 * - fresh：T 侧刚更新过。
 */
function tSideFreshness(funds, frameAt) {
  const tAt = String((funds && funds.t_at) || '')
  if (!tAt) return 'idle'
  const t = Date.parse(tAt)
  const base = Date.parse(String(frameAt || ''))
  if (!Number.isFinite(t)) return 'stale'
  if (!Number.isFinite(base)) return 'fresh'
  return base - t > 60 * 1000 ? 'stale' : 'fresh'
}

/** 行渲染（左标签右数值），空值统一“—”。 */
function Row({ label, children, hint }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, padding: '3px 0' }}>
      <span style={{ color: 'var(--app-text-2)', fontSize: 12 }}>
        {label}
        {hint ? <span style={{ marginLeft: 4, color: 'var(--app-muted-2)', fontSize: 11 }}>{hint}</span> : null}
      </span>
      <span style={{ fontSize: 12, fontVariantNumeric: 'tabular-nums' }}>{children}</span>
    </div>
  )
}

/** 一列（信号仓或 T 仓）；children 由调用方按数据源组装。 */
function Column({ title, tags, children }) {
  return (
    <div style={{ flex: '1 1 320px', minWidth: 280, border: '1px solid var(--app-border-2, #e5e6eb)', borderRadius: 6, padding: '8px 12px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
        <b style={{ fontSize: 13 }}>{title}</b>
        {tags}
      </div>
      {children}
    </div>
  )
}

export default function FundsOverviewCard() {
  const [data, setData] = useState(null)
  const [error, setError] = useState('')
  const [forbidden, setForbidden] = useState(false)

  // 10s 轮询：与 T 循环同频；页面切走后清理定时器（不留下幽灵请求）。
  useEffect(() => {
    let alive = true
    const load = async () => {
      try {
        const res = await api.fetchFundsToday()
        if (!alive) return
        setData(res)
        setError('')
      } catch (e) {
        if (!alive) return
        if (api.isForbidden(e)) {
          setForbidden(true) // admin 观测面：普通成员静默隐藏，不制造 403 噪音
          return
        }
        setError(e && e.message ? e.message : '读取失败')
      }
    }
    load()
    const timer = setInterval(load, 10000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [])

  if (forbidden) return null

  const live = (data && data.live) || {}
  const funds = live.funds || null
  const account = live.account || null
  const paper = (data && data.paper) || null
  const paperR = (paper && paper.readout) || null
  const pending = live.state === 'pending' || (!funds && live.state !== 'ok')
  const corrupt = live.state === 'corrupt'
  const liveTag = corrupt
    ? <Tag size="small" theme="danger">实盘账本损坏</Tag>
    : pending
      ? <Tag size="small" theme="warning">今日尚未定权</Tag>
      : null

  // 统一取数：funds 帧优先；帧缺失时用账户兜底（此时 target/room/T 侧显示“—”）。
  const cash = funds ? funds.cash : (account ? account.cash : null)
  const frozen = funds ? funds.frozen_cash : (account ? account.frozen_cash : null)
  const total = funds ? funds.total_asset : (account ? account.total_asset : null)
  const mv = funds ? funds.market_value : (account ? account.market_value : null)
  const cashAt = (funds && funds.cash_at) || (account && account.cash_at) || ''
  // T 侧新鲜度：T 循环没跑过（idle）与跑了但已停（stale）都**不是**“T 仓为零”。
  const tFresh = tSideFreshness(funds, data && data.at)

  return (
    <Card title={(
      <span>
        今日资金（信号仓 / T 仓）
        <Tag size="small" variant="light" style={{ marginLeft: 8 }}>每日重新定权，不累计</Tag>
      </span>
    )} style={{ marginTop: 16 }}>
      <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 8 }}>
        数据日 {data ? data.day : '—'}
        {funds && funds.at ? ` · 信号仓取数 ${hhmmss(funds.at)}` : ''}
        {data && data.at ? ` · 接口时刻 ${String(data.at).replace('T', ' ').slice(5, 19)}` : ''}
        {cashAt ? ` · 券商资金快照 ${String(cashAt).slice(5, 19)}` : ''}
        {/* §FIX-20261028-F07（2026-10-29）：pending 时优先显示**接口给的原因**（live.reason），
            没有才回落到固定句。缺陷本体：后端已经分好 pending/missing 与具体原因，界面却用
            一句固定的"属正常"顶替 ⇒ 运维在屏上读不到真因（现网 pending 与本地 missing 同形）。 */}
        {pending ? ` · ${live.reason || '引擎尚未写下今日资金帧（新交易日首帧前属正常）'}` : ''}
      </div>
      {error ? <div style={{ color: 'var(--td-warning-color)', fontSize: 12 }}>⚠ {error}</div> : null}
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <Column title="信号仓（实盘）" tags={liveTag}>
          {live.state === 'corrupt'
            ? <div style={{ fontSize: 12, color: 'var(--app-danger, #d54941)' }}>{live.reason || '实盘回转账本损坏'}</div>
            : (
              <>
                <Row label="可用资金">{money(cash)}</Row>
                <Row label="冻结资金">{money(frozen)}</Row>
                <Row label="总资产">{money(total)}</Row>
                <Row label="持仓市值">{money(mv)}</Row>
                <Row label="目标仓位" hint={funds && Number(funds.market_scale) > 0 ? `MS ${Number(funds.market_scale).toFixed(3)}` : 'MS 未注入'}>
                  {funds ? money(funds.target) : '—'}
                </Row>
                <Row label="当前战略仓位" hint="底仓 + 在途买单 − 正T买腿 + 反T卖腿">{funds ? money(funds.cur_strat) : '—'}</Row>
                {/* §P1-3（2026-10-30）：把「战略当前」拆开显示。事故当天卡片只有一个总数，
                    操作人看不出差额是持仓还是卡在柜台上的买单。 */}
                <Row label="底仓市值">{funds ? money(funds.market_value) : '—'}</Row>
                <Row label="在途买单" hint={funds && funds.pending_buy_note ? funds.pending_buy_note : '占住战略剩余空间'}>
                  {funds
                    ? `${money(funds.pending_buy_amount)}${Number(funds.pending_buy_n) > 0 ? `（${Number(funds.pending_buy_n)} 笔）` : ''}`
                    : '—'}
                </Row>
                <Row label="剩余空间">{funds ? money(funds.room) : '—'}</Row>
                {funds && funds.missing_key_input
                  ? <Tag size="small" theme="warning">关键读数缺失：目标封顶半仓且今日不做 T</Tag>
                  : null}
              </>
            )}
        </Column>
        <Column title="T 仓（实盘）" tags={<>
          {funds && Number(funds.open_legs) > 0
            ? <Tag size="small" theme="warning">在途 {Number(funds.open_legs)} 条腿</Tag>
            : null}
          {tFresh === 'idle'
            ? <Tag size="small" theme="default">T 侧未运行</Tag>
            : null}
          {tFresh === 'stale'
            ? <Tag size="small" theme="warning">T 侧读数未更新</Tag>
            : null}
        </>}>
          {/* §P1-3（2026-10-30）：T 额度 = 总资产 − 目标仓位，是"战略让出来的空间"，
              不是"今天做过 T"。旧文案让用户以为系统凭空给了一笔 T 额度。 */}
          <Row label="T 可动用额度" hint="战略让出的空间，不是已做 T">
            {funds ? money(funds.t_quota) : '—'}
          </Row>
          {funds && Number(funds.done_rounds) === 0
            ? <div style={{ fontSize: 12, color: 'var(--app-text-secondary, #888)' }}>今日未做 T，该额度为战略未占用的空间</div>
            : null}
          <Row label="可用 T 现金" hint="已扣战略剩余空间">{funds ? money(funds.cash_t) : '—'}</Row>
          <Row label="反 T 冻结">{funds ? money(funds.t_frozen_cash) : '—'}</Row>
          <Row label="在途正 T 买腿">{funds ? money(funds.open_buy_legs) : '—'}</Row>
          <Row label="在途反 T 卖腿">{funds ? money(funds.open_sell_legs) : '—'}</Row>
          <Row label="完成轮数">{funds ? Number(funds.done_rounds) || 0 : '—'}</Row>
          <Row label="当日 T 净额" hint="仅 done 轮，独立口径">{funds ? money(funds.pnl_net) : '—'}</Row>
          <Row label="T 侧取数时刻">{funds && funds.t_at ? hhmmss(funds.t_at) : '—'}</Row>
        </Column>
        <Column title="纸面（对照）" tags={paper && paper.available ? null : <Tag size="small" theme="default">未接入</Tag>}>
          {paper && paper.available && paperR ? (
            <>
              <Row label="现金">{money(paperR.cash)}</Row>
              <Row label="持仓市值">{money(paperR.market_value)}</Row>
              <Row label="目标 / 当前 / 剩余">
                {`${money(paperR.target)} / ${money(paperR.cur_strat)} / ${money(paperR.room)}`}
              </Row>
              <Row label="单笔上限">{money(paperR.per_trade)}</Row>
              <Row label="T 额度 / 可用 T 现金">
                {`${money(paperR.t_quota)} / ${money(paperR.cash_t)}`}
              </Row>
              <Row label="反 T 冻结">{money(paperR.frozen_cash)}</Row>
              <Row label="在途腿（买/卖）">
                {`${money(paperR.open_buy_legs)} / ${money(paperR.open_sell_legs)}`}
              </Row>
              <Row label="目标占比" hint="min(MS, 天花板)">{pct(paperR.ratio_used)}</Row>
            </>
          ) : (
            <div style={{ fontSize: 12, color: 'var(--app-text-2)' }}>
              {(paper && paper.reason) || '模拟盘引擎未接入'}
            </div>
          )}
        </Column>
      </div>
      <div style={{ fontSize: 11, color: 'var(--app-muted-2)', marginTop: 8 }}>
        注：实盘「可用 T 现金」已扣战略剩余空间，纸面口径不扣；两列各展示各自引擎的事实值，不在前端重算。
      </div>
    </Card>
  )
}
