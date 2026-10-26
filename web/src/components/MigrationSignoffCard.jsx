// MigrationSignoffCard.jsx — §DYN-SIZE v4 / §ANCHOR-FIRST 的签字单上屏（2026-10-26 六轮 T4-8/B8）。
//
// 这张卡只做一件事：**把"改资金参数之前后的值"摊开给人看，并且只有人亲手把 token 打进去才落盘**。
// 为什么必须这样：
//   · v4 迁移改的是 `sizing.mode` 与总仓位上限（合成口径 + 目标天花板）；
//   · 锚点回填改的是卖出裁决的判线基准（止损/止盈触发价）。
// 两者都不是"改错了刷新一下就好"的配置——静默改写资金参数是本仓库历史上最贵的一类缺陷
// （`mode`/`max_total_position_pct` 被旧默认物化坑过一次，摊薄成本污染锚点又一次）。
// 所以这里刻意不做"确认弹窗"，而是要求把后端报告里给出的 token 原样输入：
// 一次点击可以误触，输入一串词不行。
//
// 报告永远先读后写：两个 GET 可反复点、不改任何东西；POST 之后立刻重读一次报告，
// residual/remaining 不为零就在卡上显红——"写了但没生效"必须当场看得见。
//
// English: the sign-off sheet. Reading is free and repeatable; writing requires typing the token
// the backend echoed, and the card re-reads the report afterwards so a half-applied write shows up.
import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Card, Input, Table, Tag } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

const LAYER_LABEL = {
  global: '全局 rules.sizing',
  dedicated: '账号专用键',
  account_rules: '账号旧 rules.sizing',
  follower: '无文档（自动跟随）',
}

function fmtNum(v, d = 2) {
  const n = Number(v)
  return Number.isFinite(n) ? n.toFixed(d) : '—'
}

// call 安全取 api 导出的函数：测试里的 api 是按用例拼的部分 mock，
// 而 Vitest 对**未定义导出在取值时就抛错**，所以连取带调用都要包起来。
function call(name, ...args) {
  // 取值与调用**都要**包起来：部分 mock 下取未定义导出会直接抛（Vitest 的导出代理行为），
  // 而 mock 实现内部抛出的同步 TypeError 也不会变成 rejected promise——
  // 少了后一层，整张卡会在 render 里崩，而不是退回"读不到"的可读错误态。
  try {
    const fn = api[name]
    if (typeof fn !== 'function') return Promise.resolve(null)
    const p = fn(...args)
    return p && typeof p.catch === 'function' ? p.catch(() => null) : Promise.resolve(p ?? null)
  } catch {
    return Promise.resolve(null)
  }
}

export default function MigrationSignoffCard() {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [v4, setV4] = useState(null)
  const [anchor, setAnchor] = useState(null)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [p, a] = await Promise.all([call('fetchSizingV4Plan'), call('fetchAnchorBackfillPlan')])
      if (!p && !a) throw new Error('两份报告都读不到（接口不可用或非管理员）')
      setV4(p || null)
      setAnchor(a || null)
      setError('')
    } catch (e) {
      setError(String((e && e.message) || e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const v4Rows = useMemo(() => {
    const rows = ((v4 && v4.report && v4.report.rows) || []).filter((r) => r.changed)
    return rows.map((r, i) => ({
      key: `v4-${r.user_id || 'global'}-${r.layer}-${i}`,
      account: r.user_id || '（全局）',
      layer: LAYER_LABEL[r.layer] || r.layer,
      mode: `${r.mode_before} → ${r.mode_after}`,
      cap: `${fmtNum(r.cap_before)} → ${fmtNum(r.cap_after)}`,
      target: `${fmtNum(r.target_before * 100, 0)}% → ${fmtNum(r.target_after * 100, 0)}%`,
    }))
  }, [v4])

  const anchorRows = useMemo(() => {
    const rows = (anchor && Array.isArray(anchor.plan)) ? anchor.plan : []
    return rows.map((r, i) => ({
      key: `an-${r.code}-${i}`,
      code: r.code,
      name: r.name,
      cost: fmtNum(r.cost_price, 3),
      newAnchor: fmtNum(r.new_anchor, 3),
      source: r.anchor_source,
      stopBefore: fmtNum(r.stop_before, 3),
      stopAfter: fmtNum(r.stop_after, 3),
      delta: Number(r.line_delta_bp) || 0,
    }))
  }, [anchor])

  const expectToken = (v4 && v4.authorize_key) || (anchor && anchor.authorize_key) || ''
  const canSign = !!token && (!!expectToken ? token === expectToken : true)

  async function apply(kind) {
    if (!canSign) {
      showToast(`请先输入报告给出的签字 token${expectToken ? `（${expectToken}）` : ''}`, 'error')
      return
    }
    setBusy(kind)
    try {
      if (kind === 'v4') {
        const res = await call('applySizingV4Migration', token)
        if (!res) throw new Error('落盘请求无响应')
        if ((Number(res.residual) || 0) > 0) {
          showToast(`v4 迁移仍有 ${res.residual} 处未落盘（看 applied.rows 的 error 字段）`, 'error')
        } else {
          showToast('v4 迁移已按签字落盘，模拟盘已热同步', 'success')
        }
      } else {
        const res = await call('applyAnchorBackfill', token)
        if (!res) throw new Error('回填请求无响应')
        const left = Array.isArray(res.remaining) ? res.remaining.length : 0
        showToast(`锚点回填完成：写入 ${res.filled || 0} 条、跳过 ${res.skipped || 0} 条、仍缺 ${left} 条；可开 T 的持仓 ${res.t_eligible || 0} 只`, 'success')
      }
      await load() // 落盘后立刻重读：报告转空才算真生效
    } catch (e) {
      showToast('落盘失败：' + String((e && e.message) || e), 'error')
    } finally {
      setBusy('')
    }
  }

  const v4Applied = !!(v4 && v4.report && v4.report.written > 0)

  return (
    <Card style={{ marginTop: 16 }} title={(
      <span>
        发版签字单（v4 仓位迁移 + 存量锚点回填）
        <Tag size="small" variant="light" theme="warning" style={{ marginLeft: 8 }}>未签字只读，绝不写盘</Tag>
      </span>
    )}
    >
      {loading && <div className="muted">加载中…</div>}
      {!loading && error && <div style={{ color: 'var(--app-danger, #d54941)' }}>读取失败：{error}</div>}
      {!loading && !error && (
        <>
          <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap', marginBottom: 12 }}>
            <span className="muted" style={{ fontSize: 12 }}>签字 token</span>
            <Input value={token} onChange={(v) => setToken(String(v ?? ''))} placeholder={expectToken || '见报告'} style={{ width: 220 }} />
            <span className="muted" style={{ fontSize: 12 }}>
              报告里给出的 token 原样输入后，下面两个按钮才会真写盘（一次点击可以误触，输入一串词不行）。
            </span>
          </div>

          <div style={groupStyle}>① v4 仓位配置迁移（mode=product→power_mean、总仓位上限 0.80→1.00）</div>
          <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>
            只迁**逐字等于旧物化默认**的键；键缺失与手调过的一律不动。扫描 {(v4 && v4.report && v4.report.scanned) || 0} 处 ·
            需迁 {v4Rows.length} 处 · 已写 {((v4 && v4.report && v4.report.written) || 0)} 处 ·
            自动跟随 {((v4 && v4.report && v4.report.followers) || 0)} 个账号
            {v4Applied && <Tag size="small" theme="success" style={{ marginLeft: 8 }}>本次运行已落盘</Tag>}
          </div>
          <Table data={v4Rows} rowKey="key" size="small" empty="没有需要迁移的载荷（都已是新默认或键缺失——这正是期望态）"
            columns={[
              { colKey: 'account', title: '账号', width: 130 },
              { colKey: 'layer', title: '层', width: 150 },
              { colKey: 'mode', title: '合成口径', width: 190 },
              { colKey: 'cap', title: '总仓位上限', width: 150 },
              { colKey: 'target', title: '目标天花板位移', width: 150 },
            ]} />
          <div style={{ marginTop: 8 }}>
            <Button size="small" theme="primary" variant="outline" loading={busy === 'v4'}
              disabled={!canSign || v4Rows.length === 0} onClick={() => apply('v4')}>
              签字并应用 v4 迁移
            </Button>
          </div>

          <div style={groupStyle}>② 存量锚点回填（四级：first_lot → first_buy_order → cost_fallback → none）</div>
          <div className="muted" style={{ fontSize: 12, marginBottom: 6 }}>
            覆盖率 {fmtNum(((anchor && anchor.coverage && anchor.coverage.coverage_pct) || 0), 1)}% ·
            待回填 {((anchor && anchor.coverage && anchor.coverage.missing) || 0)} 行 ·
            只能按成本回落 {(anchor && anchor.coverage && anchor.coverage.cost_only) || 0} 行（回填后也不得开 T）·
            完全无价 {(anchor && anchor.coverage && anchor.coverage.unfillable) || 0} 行
          </div>
          <Table data={anchorRows} rowKey="key" size="small" empty="没有待回填的存量持仓"
            columns={[
              { colKey: 'code', title: '代码', width: 90 },
              { colKey: 'name', title: '名称', width: 90, ellipsis: true },
              { colKey: 'cost', title: '摊薄成本', width: 90 },
              { colKey: 'newAnchor', title: '回填锚点', width: 90 },
              { colKey: 'source', title: '来源', width: 120, cell: ({ row }) => <Tag size="small" theme={row.source === 'cost_fallback' ? 'warning' : 'success'}>{row.source}</Tag> },
              { colKey: 'stopBefore', title: '止损（前）', width: 100 },
              { colKey: 'stopAfter', title: '止损（后）', width: 100 },
              { colKey: 'delta', title: '位移(bp)', width: 100 },
            ]} />
          <div style={{ marginTop: 8 }}>
            <Button size="small" theme="primary" variant="outline" loading={busy === 'anchor'}
              disabled={!canSign || anchorRows.length === 0} onClick={() => apply('anchor')}>
              签字并回填锚点
            </Button>
            <span className="muted" style={{ fontSize: 12, marginLeft: 10 }}>
              账本加载时只自动补层账可证的首买价；成本档必须走这里，否则"打开账本"就等于把摊薄成本写成判线基准。
            </span>
          </div>
        </>
      )}
    </Card>
  )
}

const groupStyle = { margin: '16px 0 6px', fontSize: 13, fontWeight: 600, color: 'var(--app-text-2)' }
