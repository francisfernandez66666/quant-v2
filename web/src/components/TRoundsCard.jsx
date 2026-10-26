// TRoundsCard.jsx — §DYN-T 日内回转（做 T）的收益与留痕上屏（2026-10-26 批 4/9 收尾）。
//
// 为什么单独一张卡而不是并进绩效统计：六轮裁决 T6-5 明确「盈亏不计整仓、单独计算一个做 T 收益」。
// 做 T 的净额来自"两腿配对完成"，它既不是持仓浮盈的一部分（那些腿已经并回底仓），也不该被
// 加进总收益卡片——否则一次日内低吸会被同时记成"成本摊薄"和"赚了多少"，重复计一次。
//
// 两本账分开读：
//   · 纸面（/api/paper/t-rounds）：走引擎，唯一能看到"拦在哪一步"（step1~step9 分布）的账本；
//   · 实盘（/api/t-live/rounds）：读私有文件，state 三态必须原样呈现——
//     corrupt 绝不能渲染成"今天没做 T"（本项目反复吃亏的"降级报成功"）。
//
// English: standalone §DYN-T card. T P&L is deliberately NOT folded into the performance summary
// (ruling T6-5); paper and live books are fetched separately, and a corrupt live file is shown as
// corrupt rather than as "no rounds today".
import React, { useEffect, useMemo, useState } from 'react'
import { Card, Table, Tag, Tabs } from 'tdesign-react'
import * as api from '../api/index.js'

// 终态配色：done＝配对完成（唯一计收益的态）；converted＝未回补转战略减仓（仓位主动变更）；
// unwound＝半态收摊（不追买）；open＝一腿在途（还欠一条腿，钱冻着）。
const STATE_LABEL = { done: '已配对', converted: '转战略', unwound: '已收摊', open: '在途' }
const STATE_THEME = { done: 'success', converted: 'warning', unwound: 'default', open: 'primary' }

// STEP_LABEL 把"拦在哪一步"翻成一句人话（编号来自 paper/t_rounds.go 的 step0~step9 链）。
const STEP_LABEL = {
  step0: '前置：开关/时段/盲区',
  step1: '已有开腿，先配平',
  step2: '当日次数已用尽',
  step3: '底仓/机动仓形态不合',
  step4: '日内口径不给（振幅/偏离/极值/涨跌停）',
  step5: '发起点不对（现价没站对分时均线）',
  step6: '信号门槛没过（候选 B / 否决门）',
  step7: '环境门/总量帽/现金不足',
  step8: '流动性前置不过',
  step9: '锚点或成本口径不合',
}

function fmtNum(v, digits = 2) {
  const n = Number(v)
  if (!Number.isFinite(n)) return '—'
  return n.toFixed(digits)
}

function fmtTime(s) {
  if (!s) return '—'
  const t = String(s)
  // Go 侧是 RFC3339；只取"月-日 时:分:秒"，零值时间（0001-01-01）当作没发生。
  if (t.startsWith('0001-')) return '—'
  return t.slice(5, 19).replace('T', ' ')
}

export default function TRoundsCard() {
  const [tab, setTab] = useState('paper')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [paperView, setPaperView] = useState(null)
  const [liveView, setLiveView] = useState(null)

  useEffect(() => {
    let alive = true
    setLoading(true)
    // 两张账本并行取：任一面失败不能把另一面也拖成空态（后端读的是不同来源）。
    // 未导出的那一路要当"接口不可用"而不是直接崩：测试里的 api 是按用例拼的部分 mock，
    // 而 Vitest 对**未定义导出会在取值时直接抛错**（不是回 undefined），所以取函数本身就得包 try。
    // English: the api module is partially mocked in tests and Vitest throws on *reading* an
    // undefined export, so even resolving the function needs a try/catch.
    const call = (name, params) => {
      // 取值与调用都要包：部分 mock 下取未定义导出即抛（Vitest 的导出代理行为）；
      // 而桩函数直接 return 对象（不是 Promise）时 `.catch` 也不存在——
      // 少任何一层都会让整张卡在 render 里崩，而不是退回可读错误态。
      try {
        const fn = api[name]
        if (typeof fn !== 'function') return Promise.resolve(null)
        const p = fn(params)
        return p && typeof p.catch === 'function' ? p.catch(() => null) : Promise.resolve(p ?? null)
      } catch {
        return Promise.resolve(null)
      }
    }
    Promise.all([
      call('fetchPaperTRounds', { limit: 200 }),
      call('fetchLiveTRounds', { limit: 200 }),
    ])
      .then(([p, l]) => {
        if (!alive) return
        if (!p && !l) throw new Error('两张账本都读不到（模拟盘未启用或接口不可用）')
        setPaperView(p || null)
        setLiveView(l || null)
        setError('')
      })
      .catch((e) => {
        if (!alive) return
        setError(String((e && e.message) || e))
      })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [])

  const view = tab === 'live' ? liveView : paperView
  const rounds = useMemo(() => (view && Array.isArray(view.rounds) ? view.rounds : []), [view])
  const dayStats = (paperView && paperView.day_stats) || null

  const columns = [
    { colKey: 'code', title: '代码', width: 90 },
    {
      colKey: 'mode',
      title: '方向',
      width: 70,
      // 纸面用中文 mode（正T/反T），实盘用英文 dir（buy_first/sell_first）——同一列两种口径，
      // 因为两份账本的落盘字段本来就不是同一套字面量（改任何一边等于改契约）。
      cell: ({ row }) => <span>{row.mode || (row.dir === 'buy_first' ? '正T' : row.dir === 'sell_first' ? '反T' : '—')}</span>,
    },
    { colKey: 'state', title: '状态', width: 90, cell: ({ row }) => <Tag size="small" theme={STATE_THEME[row.state || row.status] || 'default'}>{STATE_LABEL[row.state || row.status] || row.state || row.status || '—'}</Tag> },
    { colKey: 'leg', title: '第一腿', width: 80, cell: ({ row }) => <span>{(row.leg === 'sell' || row.dir === 'sell_first') ? '先卖' : '先买'}</span> },
    { colKey: 'leg_qty', title: '腿量', width: 80, cell: ({ row }) => <span>{Number(row.leg_qty ?? row.qty) || 0}</span> },
    { colKey: 'leg_price', title: '腿价', width: 80, cell: ({ row }) => <span>{fmtNum(row.leg_price ?? row.buy_price ?? row.sell_price, 3)}</span> },
    { colKey: 'pair_price', title: '配对价', width: 80, cell: ({ row }) => <span>{fmtNum(row.pair_price ?? (row.state === 'done' || row.status === 'done' ? row.sell_price || row.buy_price : 0), 3)}</span> },
    {
      colKey: 't_pnl_net',
      title: '做T净额',
      width: 100,
      cell: ({ row }) => {
        const v = Number(row.t_pnl_net)
        if (!Number.isFinite(v) || v === 0) return <span>—</span>
        return <span style={{ color: v >= 0 ? 'var(--app-up)' : 'var(--app-down)' }}>{v.toFixed(2)}</span>
      },
    },
    { colKey: 'v', title: '分时均线', width: 90, cell: ({ row }) => <span>{fmtNum(row.v, 3)}</span> },
    { colKey: 'dev_pct', title: '偏离%', width: 80, cell: ({ row }) => <span>{fmtNum(row.dev_pct)}</span> },
    { colKey: 'frozen_cash', title: '冻结资金', width: 90, cell: ({ row }) => <span>{fmtNum(row.frozen_cash)}</span> },
    { colKey: 'used_signal_id', title: '消费信号', width: 120, ellipsis: true, cell: ({ row }) => <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{row.used_signal_id || '—'}</span> },
    { colKey: 'leg_at', title: '开腿时刻', width: 120, cell: ({ row }) => <span>{fmtTime(row.leg_at || row.opened_at)}</span> },
    { colKey: 'reason', title: '原因', ellipsis: true, cell: ({ row }) => <span>{row.reason || row.unwind_reason || '—'}</span> },
  ]

  const blockRows = useMemo(() => {
    const list = (paperView && Array.isArray(paperView.block_steps)) ? paperView.block_steps : []
    return list.map((b, i) => ({
      key: `${b.step}|${b.code}|${i}`,
      step: b.step,
      stepName: STEP_LABEL[b.step] || b.step,
      code: b.code || '—',
      n: Number(b.n) || 0,
    }))
  }, [paperView])

  const pnl = (view && view.pnl) || { net: 0, gross: 0, done_rounds: 0, fee_est_share: 0 }
  const liveState = liveView && liveView.state

  return (
    <Card style={{ marginTop: 16 }} title={(
      <span>
        日内回转（做 T）
        <Tag size="small" variant="light" style={{ marginLeft: 8 }}>收益独立口径，不计入整仓收益</Tag>
        {dayStats && dayStats.frozen_cash > 0 && (
          <Tag size="small" variant="light" theme="warning" style={{ marginLeft: 6 }}>在途冻结 {fmtNum(dayStats.frozen_cash)} 元</Tag>
        )}
      </span>
    )}
    >
      {loading && <div className="muted">加载中…</div>}
      {!loading && error && <div style={{ color: 'var(--app-danger, #d54941)' }}>读取失败：{error}</div>}
      {!loading && !error && (
        <>
          <Tabs value={tab} onChange={(v) => setTab(v)}>
            <Tabs.TabPanel value="paper" label="纸面账本" />
            <Tabs.TabPanel value="live" label="实盘账本" />
          </Tabs>
          {tab === 'live' && liveState === 'missing' && (
            <p className="muted">{(liveView && liveView.reason) || '该账号还没有实盘回转记录'}</p>
          )}
          {tab === 'live' && liveState === 'corrupt' && (
            <p style={{ color: 'var(--app-danger, #d54941)' }}>
              {(liveView && liveView.reason) || '实盘回转账本文件损坏（接口未改动原文件，需人工核对）'}
            </p>
          )}
          <div style={{ display: 'flex', gap: 18, flexWrap: 'wrap', margin: '10px 0' }}>
            <span>做 T 净额（累计 done 轮）：<b>{fmtNum(pnl.net)}</b> 元</span>
            <span>毛额（未扣费）：{fmtNum(pnl.gross)} 元</span>
            <span>完成轮数：{Number(pnl.done_rounds) || 0}</span>
            {Number(pnl.fee_est_share) > 0 && (
              <span style={{ color: 'var(--app-warning, #e37318)' }}>
                费用为估算入账的占比 {(Number(pnl.fee_est_share) * 100).toFixed(0)}%
              </span>
            )}
          </div>
          {/* 目标仓位一帧读数（§8.8 面板展示）：由账本引擎按下单同一条式子算出，接口原样搬出来。
              market_scale 缺失（=0）时必须显式说"本轮尚未定权"，不能显示 1.00——
              把"不知道"画成"满额目标"正是本批要消灭的那类假读数。 */}
          {tab === 'paper' && paperView && paperView.extra && paperView.extra.target && (
            <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', marginBottom: 10, fontSize: 13 }}>
              {Number(paperView.extra.target.market_scale) > 0 ? (
                <span>市场缩放 MS {fmtNum(paperView.extra.target.market_scale, 3)}（占比 {fmtNum(paperView.extra.target.ratio_used * 100, 1)}%）</span>
              ) : (
                <Tag size="small" theme="default">本轮尚未定权（MS 未注入，不显示中性 1.00）</Tag>
              )}
              <span>目标 {fmtNum(paperView.extra.target.target)} / 当前 {fmtNum(paperView.extra.target.cur_strat)} / 剩余 {fmtNum(paperView.extra.target.room)}</span>
              <span>T 额度 {fmtNum(paperView.extra.target.t_quota)} · 可用 T 现金 {fmtNum(paperView.extra.target.cash_t)}</span>
              <span>反T冻结 {fmtNum(paperView.extra.target.frozen_cash)}</span>
              {paperView.extra.target.missing_key_input && (
                <Tag size="small" theme="danger">关键读数缺失：目标封顶半仓且今日不做 T</Tag>
              )}
            </div>
          )}
          {tab === 'paper' && dayStats && (
            <div style={{ display: 'flex', gap: 18, flexWrap: 'wrap', marginBottom: 10 }}>
              <span className="muted">交易日 {dayStats.day}</span>
              <span>开腿 {dayStats.opened}</span>
              <span>配对 {dayStats.done}</span>
              <span>转战略 {dayStats.converted}</span>
              <span>收摊 {dayStats.unwound}</span>
              {dayStats.loss_breaker > 0 && (
                <Tag size="small" theme="danger">买回熔断 {dayStats.loss_breaker}</Tag>
              )}
              <span>反T卖腿市值 {fmtNum(dayStats.sell_notional)}</span>
              {/* 总量帽已用比例（分母＝min(总资产, 持仓市值)，六轮 T6-2）：只看市值不看比例，
                  就看不出"今天还能不能再高抛一次"这个真正约束开腿的量。 */}
              {paperView && paperView.extra && paperView.extra.sell_cap_value > 0 && (
                <span>
                  反T总量帽 {fmtNum(paperView.extra.sell_used_pct, 1)}%
                  <span className="muted" style={{ fontSize: 12 }}>
                    （帽 {fmtNum(paperView.extra.sell_cap_value)} 元 ＝ min(总资产, 持仓市值) {fmtNum(paperView.extra.sell_cap_denominator)} × {fmtNum(paperView.extra.sell_cap_pct, 0)}%）
                  </span>
                </span>
              )}
              <span>未回补转减仓股数 {Number(dayStats.converted_qty) || 0}</span>
              {(paperView && paperView.shadow) === true && <Tag size="small" variant="light">影子期：只留痕不执法</Tag>}
              {paperView && paperView.enabled === false && <Tag size="small" theme="default">做 T 开关：关</Tag>}
            </div>
          )}
          {/* 实盘账本处于 missing/corrupt 时**不渲染明细表**：损坏态旁边再挂一句"暂无实盘回转记录"
              等于同时说"读不到"和"确实没有"，后者是前者最容易伪装成的形态（本项目的招牌缺陷）。 */}
          {!(tab === 'live' && (liveState === 'corrupt' || liveState === 'missing')) && (
            <Table data={rounds} columns={columns} rowKey={(row, i) => row.id || `r${i}`} size="small"
              empty={tab === 'live' ? '暂无实盘回转记录' : '今日暂无回转（被拦分布见下表）'}
              pagination={{ pageSize: 10, total: rounds.length, showJumper: true, pageSizeOptions: [10, 20, 50] }} />
          )}
          {tab === 'paper' && (
            <div style={{ marginTop: 12 }}>
              <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>
                为什么没做 T（当日按拦截步骤计数；分布形状就是调参依据——step2 最多＝次数帽太紧，step4 最多＝行情不给振幅）
              </div>
              <Table data={blockRows} rowKey="key" size="small" empty="当日没有任何一次不开腿记录"
                columns={[
                  { colKey: 'step', title: '步骤', width: 80 },
                  { colKey: 'stepName', title: '拦在哪里', width: 260 },
                  { colKey: 'code', title: '代码', width: 100 },
                  { colKey: 'n', title: '次数', width: 80 },
                ]}
                pagination={blockRows.length > 10 ? { pageSize: 10, total: blockRows.length } : false} />
            </div>
          )}
        </>
      )}
    </Card>
  )
}
