// ── 成交勘误面板 FillAmendPanel.jsx（§FILL-AMEND 2026-09-23）──
// 页面位置：量化交易页「交易流水与整体盈亏」卡片下方，仅 admin 可见（上层 Quant.jsx 已按
//          adminMiddleware 的 403 渲染无权限面板，本组件不再做客户端角色判断——
//          角色判定的唯一事实源在后端，前端复制一套判定迟早和后端漂移）。
//
// 三件事：
//  1. 逐笔改判对话框：由成交行的「改判」按钮打开（target 属性传进来），只提交
//     {fill_id, new_side, reason}。**刻意不带锚点**（trade_id/order_id/价格/数量）：
//     锚点由服务端从原始成交行读出——让前端自报锚点，等于允许一个写歪的锚造出
//     一条永久匹配不到任何成交的"死勘误"（提交成功、账目永远不动，最难查的那种）。
//  2. 勘误台账：pending（影子态，账没动）/ applied（已生效）/ revoked（已撤销）三态可视，
//     批准与撤销是两个人工动作，批准是整条链路上**唯一**让账目数字变动的动作。
//  3. 守恒自检：只读报数（成交簿重放 vs 实盘持仓/现金账），发现差异只列线索、绝不平账。
//
// 为什么必须把 pending 显式画出来：09-22 那笔方向记错的成交，如果运维提交了改判却没批准，
// 页面上的钱还是老样子——没有"待批准"这一栏，人会以为改判没生效而反复提交（后端回 409 后
// 变成"改不动"的谜）。
import React, { useState, useEffect, useMemo, useCallback } from 'react'
import { Card, Table, Tag, Button, Input, Textarea, Dialog, Select } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast, confirmDialog } from '../ui.jsx'
import { fmtCNY2 } from '../utils'

// STATUS_LABEL 勘误状态 → 中文（与 store 的 pending/applied/revoked 一一对应，
// 出现未知状态时原样显示，避免后端加态而前端把行吞掉）。
const STATUS_LABEL = { pending: '待批准', applied: '已生效', revoked: '已撤销' }
// STATUS_THEME 状态标签配色：待批准=警告黄，已生效=绿，已撤销=灰
const STATUS_THEME = { pending: 'warning', applied: 'success', revoked: 'default' }

// 成交方向下拉项：只允许 买入/卖出 两个规范值（后端 ErrFillAmendmentSide 同口径），
// 不给自由文本是为了杜绝"改成第三种方向"这种根本不存在的账户语义。
const SIDE_OPTIONS = [
  { value: '买入', label: '买入' },
  { value: '卖出', label: '卖出' },
]

/**
 * 成交勘误面板。
 * @param {Array}  fills          当前流水表的成交行（来自 /api/qmt/trades，side 已是生效方向）
 * @param {Object} target         要改判的成交行（null=对话框关闭）
 * @param {Function} onCloseTarget 关闭改判对话框
 * @param {Function} onChanged    勘误状态变化后回调（让上层刷新流水/盈亏表，批准后数字会动）
 */
export default function FillAmendPanel({ fills = [], target = null, onCloseTarget, onChanged }) {
  const [rows, setRows] = useState([])
  const [statusFilter, setStatusFilter] = useState('')
  const [loadErr, setLoadErr] = useState('')
  const [busyId, setBusyId] = useState(0) // 正在处置的勘误 ID（禁用按钮防重复点击）

  // 改判对话框的表单态：newSide/reason 每次切换目标行都要重置，否则上一笔的理由会串到这一笔
  const [newSide, setNewSide] = useState('卖出')
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // 守恒自检：day 留空=后端按当日判定；report 为 null 表示尚未跑过
  const [consDay, setConsDay] = useState('')
  const [consReport, setConsReport] = useState(null)
  const [consBusy, setConsBusy] = useState(false)
  const [consErr, setConsErr] = useState('')

  const loadLedger = useCallback(async () => {
    try {
      const r = await api.fetchFillAmendments(statusFilter)
      setRows(Array.isArray(r && r.amendments) ? r.amendments : [])
      setLoadErr('')
    } catch (e) {
      // 错误文案原样透出：403（会话不是 admin）与 500（库异常）对运维是两件完全不同的事
      setLoadErr((e && e.message) || '勘误台账加载失败')
    }
  }, [statusFilter])

  useEffect(() => { loadLedger() }, [loadLedger])

  // 切换改判目标时重置表单：默认方向取"生效方向的反面"（改判的语义就是把这笔翻过来），
  // 用户仍可显式选回原方向——后端会以 ErrFillAmendmentSide 拒绝同向空改判。
  useEffect(() => {
    if (!target) return
    setNewSide(target.side === '买入' ? '卖出' : '买入')
    setReason('')
  }, [target])

  // pendingByKey：把"已提交、尚未批准"的影子勘误挂回成交行的索引。
  // 键用后端单点算好的 amend_key，绝不在 JS 里重算复合锚——浮点价格两边一旦格式不一致，
  // 勘误状态就会挂到错误的成交行上（这正是本仓 §ADJ-BASIS 那类"键算歪"事故的前端版）。
  // 已生效（applied）不需要这张表：流水行的 side/amended 字段本身就是视图给的结果。
  const pendingByKey = useMemo(() => {
    const m = new Map()
    for (const a of rows) if (a.status === 'pending' && a.amend_key) m.set(a.amend_key, a)
    return m
  }, [rows])

  // 当前流水里已被改判的行数（供头部提示）：只统计这一页可见的最近 100 笔，
  // 因为流水端点本身就截尾 100 笔——按全库统计会让人误以为"页面上看到的即全部"。
  const amendedVisible = useMemo(
    () => fills.filter((f) => f.amended || pendingByKey.has(f.amend_key)).length,
    [fills, pendingByKey],
  )

  async function submitAmendment() {
    if (!target) return
    const r = reason.trim()
    if (!r) { showToast('请填写改判理由（无留痕即无据可查）', 'warning'); return }
    setSubmitting(true)
    try {
      await api.createFillAmendment({ fillId: target.id, newSide, reason: r })
      showToast('改判已提交，状态=待批准（批准前不影响任何账目数字）', 'success')
      if (onCloseTarget) onCloseTarget()
      loadLedger()
    } catch (e) {
      // 409=这笔已有待批准/已生效的勘误：文案本身已含处置指引（先撤销原条目），直接透出
      showToast((e && e.message) || '改判提交失败', 'error')
    } finally {
      setSubmitting(false)
    }
  }

  async function transition(a, action) {
    const isApply = action === 'apply'
    const ok = await confirmDialog(
      isApply
        ? `确认让这笔改判生效？${a.code} ${a.orig_side} → ${a.new_side}（成交号 ${a.trade_id || '复合锚'}）。\n生效后买入/卖出笔数、回款与已实现盈亏会跟着重算；原始成交行不会被改写。`
        : `确认撤销这条勘误？${a.code} 的账目数字会回到柜台原始方向「${a.orig_side}」。`,
      isApply ? '批准勘误' : '撤销勘误',
    )
    if (!ok) return
    setBusyId(a.id)
    try {
      if (isApply) await api.applyFillAmendment(a.id)
      else await api.revokeFillAmendment(a.id)
      showToast(isApply ? '勘误已生效，账目已按新方向重算' : '勘误已撤销', 'success')
      loadLedger()
      if (onChanged) onChanged() // 批准后流水/盈亏数字变了，通知上层重拉
    } catch (e) {
      showToast((e && e.message) || '勘误处置失败', 'error')
      loadLedger() // 失败也要重拉：409 多半是"已被他人处置"，本地台账此刻已经旧了
    } finally {
      setBusyId(0)
    }
  }

  async function runConservation() {
    setConsBusy(true)
    setConsErr('')
    try {
      const r = await api.fetchFillConservation(consDay.trim())
      setConsReport(r && r.report ? r.report : null)
      if (!r || !r.report) setConsErr('后端未返回自检报告')
    } catch (e) {
      setConsReport(null)
      setConsErr((e && e.message) || '守恒自检失败')
    } finally {
      setConsBusy(false)
    }
  }

  const ledgerColumns = [
    { colKey: 'code', title: '代码', width: 90 },
    {
      colKey: 'side', title: '方向改判', width: 150,
      cell: ({ row }) => (
        <span style={{ fontSize: 12 }}>
          <span style={{ color: 'var(--app-text-2)' }}>{row.orig_side}</span>
          {' → '}
          <span style={{ color: row.new_side === '买入' ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 700 }}>{row.new_side}</span>
        </span>
      ),
    },
    {
      colKey: 'trade_id', title: '成交锚', width: 150,
      // 空 trade_id=旧行/交割单回灌，走复合锚；这里如实标出，便于对柜台回单
      cell: ({ row }) => <span style={{ fontSize: 11, fontFamily: 'monospace' }}>{row.trade_id || '复合锚'}</span>,
    },
    { colKey: 'qty', title: '数量', width: 80 },
    { colKey: 'orig_amount', title: '金额(未变)', width: 110, cell: ({ row }) => fmtCNY2(row.orig_amount) },
    { colKey: 'reason', title: '理由', cell: ({ row }) => <span title={row.reason}>{row.reason}</span> },
    { colKey: 'operator', title: '提交人', width: 100 },
    {
      colKey: 'status', title: '状态', width: 90,
      cell: ({ row }) => <Tag theme={STATUS_THEME[row.status] || 'default'} variant="light">{STATUS_LABEL[row.status] || row.status}</Tag>,
    },
    {
      colKey: 'time', title: '时间', width: 140,
      cell: ({ row }) => <span style={{ fontSize: 11 }}>{row.applied_at || row.created_at}</span>,
    },
    {
      colKey: 'op', title: '操作', width: 130,
      cell: ({ row }) => (
        <span style={{ display: 'inline-flex', gap: 6 }}>
          {row.status === 'pending' && (
            <Button size="extra-small" theme="primary" variant="outline" disabled={busyId === row.id} onClick={() => transition(row, 'apply')}>批准生效</Button>
          )}
          {row.status === 'applied' && (
            <Button size="extra-small" theme="warning" variant="outline" disabled={busyId === row.id} onClick={() => transition(row, 'revoke')}>撤销</Button>
          )}
          {row.status === 'revoked' && <span style={{ fontSize: 11, color: 'var(--app-muted-2)' }}>已归档</span>}
        </span>
      ),
    },
  ]

  const cash = consReport && consReport.cash
  return (
    <Card title="成交勘误与账本守恒" style={{ marginBottom: 14 }}>
      <div style={{ fontSize: 11, color: 'var(--app-text-2)', marginBottom: 10, lineHeight: 1.7 }}>
        人工改判只改写「这笔算买入还是卖出」的**读取侧方向**，成交金额、数量、柜台原始记录一字不动；
        提交=待批准（账没动），批准=数字重算（买入笔数闸/预算/回款/已实现盈亏同步），撤销=回到原始方向。
        每一步都留 opslog 审计。上方流水表点「改判」提交。
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8, flexWrap: 'wrap' }}>
        <span style={{ fontSize: 12, color: 'var(--app-text-2)' }}>
          勘误台账 {rows.length} 条{amendedVisible ? ` · 当前流水可见 ${amendedVisible} 笔已带改判` : ''}
        </span>
        <Select
          size="small" value={statusFilter} onChange={setStatusFilter} style={{ width: 140 }}
          options={[
            { value: '', label: '全部状态' },
            { value: 'pending', label: '只看待批准' },
            { value: 'applied', label: '只看已生效' },
            { value: 'revoked', label: '只看已撤销' },
          ]}
        />
        <Button size="small" variant="outline" onClick={loadLedger}>刷新</Button>
        {loadErr && <span style={{ fontSize: 12, color: 'var(--app-up)' }}>⚠ {loadErr}</span>}
      </div>

      {rows.length ? (
        <Table data={rows} columns={ledgerColumns} rowKey="id" size="small"
          pagination={{ pageSize: 8, total: rows.length }} />
      ) : (
        <div style={{ color: 'var(--app-text-2)', fontSize: 12, marginBottom: 4 }}>
          {loadErr ? '台账暂不可用' : '暂无勘误记录——只有取证确认柜台方向记错时才需要改判'}
        </div>
      )}

      {/* ── 守恒自检（只读）── */}
      <div style={{ marginTop: 16, paddingTop: 12, borderTop: '1px solid var(--app-border)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
          <span style={{ fontSize: 13, fontWeight: 700 }}>账本守恒自检</span>
          <Input size="small" value={consDay} onChange={setConsDay} placeholder="YYYY-MM-DD（留空=今日）" style={{ width: 190 }} />
          <Button size="small" theme="primary" variant="outline" loading={consBusy} onClick={runConservation}>只读自检</Button>
          <span style={{ fontSize: 11, color: 'var(--app-text-2)' }}>
            比对「成交簿重放」与「实盘持仓/现金账」；历史错账不会因改判自动平账，此处只列差异线索
          </span>
        </div>
        {consErr && <div style={{ fontSize: 12, color: 'var(--app-up)', marginBottom: 8 }}>⚠ {consErr}</div>}
        {consReport && (
          <div style={{ fontSize: 12, padding: '8px 10px', borderRadius: 6, border: '1px solid var(--app-border)' }}>
            <div style={{ marginBottom: 6 }}>
              截止 {consReport.day} · 生效勘误 {consReport.applied_amendments} 条 · 结论：
              <Tag theme={consReport.ok ? 'success' : 'danger'} variant="light" style={{ marginLeft: 6 }}>
                {consReport.ok ? '守恒通过' : '存在差异（只报数，未动账）'}
              </Tag>
            </div>
            <div style={{ marginBottom: 4 }}>
              持仓不变量：比对 {consReport.positions_checked} 只
              {consReport.positions_ok ? ' 全部一致' : ` · 不一致 ${consReport.position_lines.length} 条`}
            </div>
            {(consReport.position_lines || []).length ? (
              <Table
                data={consReport.position_lines} rowKey="code" size="small" pagination={false}
                columns={[
                  { colKey: 'code', title: '代码', width: 100 },
                  { colKey: 'replayed_qty', title: '重放量', width: 90 },
                  { colKey: 'book_qty', title: '账上量', width: 90 },
                  {
                    colKey: 'diff', title: '差额', width: 90,
                    cell: ({ row }) => <span style={{ color: row.diff > 0 ? 'var(--app-up)' : 'var(--app-down)' }}>{row.diff}</span>,
                  },
                  { colKey: 'note', title: '线索' },
                ]}
              />
            ) : null}
            {cash && (
              <div style={{ marginTop: 6, color: 'var(--app-text-2)' }}>
                {cash.checked ? (
                  <>
                    现金不变量：期初 {fmtCNY2(cash.initial_capital)} − 买入 {fmtCNY2(cash.buy_amount)} − 佣金 {fmtCNY2(cash.fee_total)}
                    {' '}+ 卖出 {fmtCNY2(cash.sell_amount)} − 印花税 {fmtCNY2(cash.stamp_tax_total)}
                    {' '}= 期望 {fmtCNY2(cash.expected_cash)}；账上 {fmtCNY2(cash.book_cash)}；偏差 {fmtCNY2(cash.diff)}
                  </>
                ) : (
                  // 缺腿就明说缺哪条：把"没数据"渲染成"通过"是本仓反复出事的形态（§M-8/§N-6）
                  <>现金不变量：未检查（{cash.skip_reason || '缺少可比基准'}）</>
                )}
              </div>
            )}
          </div>
        )}
        {!consReport && !consErr && !consBusy && (
          <div style={{ color: 'var(--app-text-2)', fontSize: 12 }}>尚未自检——批准勘误后建议跑一次，确认差异符合预期</div>
        )}
      </div>

      {/* ── 逐笔改判对话框 ── */}
      <Dialog
        visible={!!target}
        header={target ? `改判成交 ${target.code}` : '改判成交'}
        onClose={onCloseTarget}
        onConfirm={submitAmendment}
        confirmBtn={submitting ? '提交中…' : '提交待批准勘误'}
        width={520}
      >
        {target && (
          <div style={{ fontSize: 13 }}>
            {/* 原方向按 side 展示：side 已是生效方向，未勘误时它就是柜台原始方向 */}
            <div style={{ marginBottom: 8, color: 'var(--app-text-2)' }}>
              当前方向 <b style={{ color: target.side === '买入' ? 'var(--app-up)' : 'var(--app-down)' }}>{target.side}</b>
              {' · 价格 '}{target.price}{' · 数量 '}{target.qty}{' · 金额 '}{fmtCNY2(target.amount)}
              <br />
              成交时间 {target.traded_at} · 委托号 {target.order_id} · 成交号 {target.trade_id || '（柜台未给，按复合锚定位）'}
            </div>
            {target.amended && (
              <div style={{ marginBottom: 8, color: 'var(--app-warning, #e37318)' }}>
                这笔已有一条生效勘误（理由：{target.amend_reason || '—'}），需先在台账里撤销再重新提交。
              </div>
            )}
            <div style={{ marginBottom: 8 }}>
              <span style={{ display: 'inline-block', width: 80, color: 'var(--app-text-2)' }}>改判为</span>
              <Select value={newSide} onChange={setNewSide} options={SIDE_OPTIONS} style={{ width: 160 }} />
            </div>
            <div>
              <div style={{ color: 'var(--app-text-2)', marginBottom: 4 }}>
                理由（必填，进 opslog 审计；写清取证依据，例如「网关日志 dispatch=卖出、回报误记买入」）
              </div>
              <Textarea value={reason} onChange={setReason} autosize={{ minRows: 3, maxRows: 8 }}
                placeholder="为什么要改这笔的方向" />
            </div>
          </div>
        )}
      </Dialog>
    </Card>
  )
}
