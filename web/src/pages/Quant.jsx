// ── 量化交易页 Quant.jsx ──
// 页面用途：实盘链路总开关、执行方式、仓位纪律与战法白名单的统一管理界面。
// 主要功能：查看首尔↔广州链路状态/熔断；配置总开关、执行模式、委托价格、心跳超时、网关与 Token；
//          设定最大持仓/单票金额/预算等仓位纪律；按战法开关实盘准入并展示交易流水与归因盈亏。
//          定时轮询：链路状态 10s 一次、交易流水 30s 一次；保存后约 5s 热加载生效。
// 使用 TDesign React 组件（Card / Form / Input / Switch / Button / Tag / Table）。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Card, Form, Input, Switch, Button, Tag, Table, MessagePlugin, DialogPlugin } from 'tdesign-react'
import * as api from '../api/index.js'

// 战法分组标签：form=内置形态战法、factor=因子战法、pattern=形态自动发现战法。
// 后端 /api/config/qmt 的 known_strategies 为 [{id,name,kind}]；因子/形态战法审批注入后自动出现。
const KIND_LABELS = {
  form: '形态战法（内置）',
  factor: '因子战法',
  pattern: '形态自动发现战法',
}
const KIND_ORDER = ['form', 'factor', 'pattern']

// 涨跌配色（红涨绿跌）：盈亏 >=0 用红色，<0 用绿色
function pnlColor(v) {
  return (v || 0) >= 0 ? '#e34d59' : '#00a870'
}

// 通用确认弹窗：返回 Promise<boolean>，用户确认 resolve(true)、关闭 resolve(false)
function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    const d = DialogPlugin.confirm({
      header,
      body,
      theme: 'warning',
      onConfirm: () => { d.hide(); resolve(true) },
      onClose: () => { d.hide(); resolve(false) },
    })
  })
}

/**
 * 量化交易主页面组件。
 * state：链路运行状态（enabled / 心跳 / 延迟 / 熔断等）。
 * form：实盘链路与仓位纪律的可编辑表单数据（保存时整体回写后端）。
 * tokenInput：鉴权 Token 明文输入，留空表示沿用原值。
 * knownStrategies：后端已知的全部战法标识列表。
 * strategyOn：各战法是否允许进入实盘的开关映射。
 * strategyDirty：战法开关是否被改动（控制保存按钮可用性）。
 * saving：保存中标志（禁用按钮、防重复提交）。
 * trades：交易流水与整体/分战法盈亏汇总。
 * amountsInput：各战法自定义单票金额（留空则用全局 fixed_amount）。
 * 副作用：挂载时拉取配置/状态/流水，并以定时器轮询刷新。
 */
export default function Quant() {
  const [state, setState] = useState(null)
  const [form, setForm] = useState({
    enabled: false, mode: 'manual', price_type: 'market', auto_sell: false,
    gateway_url: '', token_masked: '',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
  })
  const [tokenInput, setTokenInput] = useState('')
  const [strategyList, setStrategyList] = useState([])
  const [strategyOn, setStrategyOn] = useState({})
  const [strategyDirty, setStrategyDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [trades, setTrades] = useState(null)
  const [amountsInput, setAmountsInput] = useState({})

  const stateTimer = useRef(null)
  const tradesTimer = useRef(null)

  // 按 kind 分组（form → factor → pattern），便于分别展示"形态战法 / 因子战法"
  const strategyGroups = useMemo(() => {
    const g = { form: [], factor: [], pattern: [] }
    strategyList.forEach((s) => {
      const k = g[s.kind] ? s.kind : 'form'
      g[k].push(s)
    })
    return KIND_ORDER.map((kind) => ({ kind, label: KIND_LABELS[kind] || kind, items: g[kind] })).filter((x) => x.items.length)
  }, [strategyList])
  const allStrategyOn = useMemo(() => strategyList.length > 0 && strategyList.every((s) => strategyOn[s.id]), [strategyList, strategyOn])
  const strategyHint = useMemo(() => {
    const onCount = strategyList.filter((s) => strategyOn[s.id]).length
    if (allStrategyOn) return '当前：全部允许'
    return `当前：${onCount}/${strategyList.length} 允许进入实盘`
  }, [strategyList, strategyOn, allStrategyOn])

  // 金额格式化：正数补 + 号并保留两位小数
  function fmtMoney(v) {
    const n = Number(v) || 0
    return (n > 0 ? '+' : '') + n.toFixed(2)
  }
  // 将秒级时间戳格式化为「x秒前 / x分前 / x小时前」
  function fmtAgo(ts) {
    if (!ts) return ''
    const sec = Math.floor((Date.now() - ts * 1000) / 1000)
    if (sec < 60) return sec + '秒前'
    if (sec < 3600) return Math.floor(sec / 60) + '分前'
    return Math.floor(sec / 3600) + '小时前'
  }

  // 拉取实盘配置并回填表单/战法开关/自定义金额；白名单为空数组时默认全部开启
  async function loadConfig() {
    const c = await api.fetchQMTConfig()
    setForm({
      enabled: !!c.enabled, mode: c.mode || 'manual', price_type: c.price_type || 'market',
      auto_sell: !!c.auto_sell, gateway_url: c.gateway_url || '', token_masked: c.token_masked || '',
      fixed_amount: c.fixed_amount ?? 10000, max_positions: c.max_positions ?? 10,
      initial_capital: c.initial_capital ?? 100000,
      daily_max_buys: c.daily_max_buys ?? 20, daily_budget_amount: c.daily_budget_amount ?? 100000,
      miss_heartbeat_sec: c.miss_heartbeat_sec ?? 120,
    })
    // 后端 known_strategies 可能为对象数组 [{id,name,kind}]（新）或纯 ID 数组（旧），统一归一
    let list = []
    if (Array.isArray(c.known_strategies)) {
      list = c.known_strategies.map((x) => {
        if (typeof x === 'string') return { id: x, name: x, kind: 'form' }
        return { id: x.id, name: x.name || x.id, kind: x.kind || 'form' }
      })
    }
    setStrategyList(list)
    const wl = Array.isArray(c.strategies) ? c.strategies : []
    const on = {}
    list.forEach((v) => { on[v.id] = wl.length === 0 || wl.includes(v.id) })
    setStrategyOn(on)
    const sa = c.strategy_amounts || {}
    const ai = {}
    list.forEach((v) => { ai[v.id] = sa[v.id] ?? '' })
    setAmountsInput(ai)
    setStrategyDirty(false)
  }

  // 拉取交易流水（含汇总与分战法/成交流水），仅在返回合法时更新
  async function loadTrades() {
    try {
      const t = await api.fetchQMTTrades()
      if (t && t.summary) setTrades(t)
    } catch (_) {}
  }

  // 拉取链路运行状态（心跳/延迟/熔断等）
  async function loadState() {
    try { setState(await api.fetchQMTState()) } catch (_) {}
  }

  // 标记战法开关被改动
  function markStrategyDirty() { setStrategyDirty(true) }

  // 通用保存：回写指定字段到后端，成功提示并重新拉取配置，失败提示错误
  async function patch(fields, okTip) {
    setSaving(true)
    try {
      await api.updateQMTConfig(fields)
      MessagePlugin.success(okTip || '已保存')
      await loadConfig()
    } catch (e) {
      MessagePlugin.error('保存失败：' + (e && e.message ? e.message : e))
    } finally {
      setSaving(false)
    }
  }

  // 保存实盘总开关：启用前需二次确认（将向网关下发真实交易指令）
  async function saveSwitches(v) {
    const next = v === undefined ? form.enabled : v
    if (next && !(await confirmDialog('确认启用实盘链路？启用后将按下方参数向广州网关传递真实交易指令。', '启用实盘链路'))) {
      setForm({ ...form, enabled: false })
      return
    }
    await patch({ enabled: next }, next ? '实盘链路已启用' : '实盘链路已停用')
  }

  // 保存执行参数（模式/价格/自动卖出/网关/心跳/Token），切换全自动前需确认
  async function saveExec() {
    if (form.mode === 'auto' && !(await confirmDialog('确认切换为「全自动」？信号将不经人工确认直接下单（受熔断/纪律约束）。', '切换全自动'))) {
      setForm({ ...form, mode: 'manual' })
      return
    }
    const fields = {
      mode: form.mode, price_type: form.price_type, auto_sell: form.auto_sell,
      gateway_url: form.gateway_url, miss_heartbeat_sec: form.miss_heartbeat_sec,
    }
    if (tokenInput) fields.token = tokenInput
    await patch(fields, '执行参数已保存')
  }

  // 保存仓位纪律（最大持仓/单票金额/初始资金/日买笔数上限/日预算）
  async function saveCaps() {
    await patch({
      max_positions: form.max_positions, fixed_amount: form.fixed_amount,
      initial_capital: form.initial_capital, daily_max_buys: form.daily_max_buys,
      daily_budget_amount: form.daily_budget_amount,
    }, '仓位纪律已保存')
  }

  // 保存战法白名单与自定义金额：全部开启时传空数组表示不设白名单（含因子/形态战法）
  async function saveStrategies() {
    const values = strategyList.filter((s) => strategyOn[s.id]).map((s) => s.id)
    const amounts = {}
    for (const v of strategyList) {
      const n = parseFloat(amountsInput[v.id])
      if (!Number.isNaN(n) && n > 0) amounts[v.id] = n
    }
    return patch(
      { strategies: allStrategyOn ? [] : values, strategy_amounts: amounts },
      '战法开关与仓位已保存',
    )
  }

  useEffect(() => {
    loadState()
    // 链路状态每 10s 轮询一次（心跳/延迟/熔断实时性要求高）
    stateTimer.current = setInterval(loadState, 10000)
    loadTrades()
    // 交易流水每 30s 轮询一次（成交频率低，降低刷新压力）
    tradesTimer.current = setInterval(loadTrades, 30000)
    loadConfig().catch((e) => MessagePlugin.error('加载实盘配置失败：' + (e && e.message ? e.message : e)))
    // 卸载时清除两个定时器，防止内存泄漏与重复请求
    return () => {
      if (stateTimer.current) clearInterval(stateTimer.current)
      if (tradesTimer.current) clearInterval(tradesTimer.current)
    }
  }, [])

  // 执行模式 / 委托价格 的分段切换按钮样式工厂：active 为当前选中项（高饱和蓝，避免选中态过浅）
  const segBtn = (active) => ({
    marginRight: 8,
    padding: '5px 14px', borderRadius: 6, fontSize: 13, cursor: 'pointer',
    border: active ? '1px solid #1d4ed8' : '1px solid #cfd9ec',
    color: active ? '#ffffff' : '#5a6b86',
    background: active ? '#1d4ed8' : 'transparent',
    fontWeight: active ? 600 : 400,
  })

  // 分战法盈亏表列定义：realized_pnl 按涨跌配色渲染
  const byStrategyColumns = [
    { colKey: 'strategy', title: '战法', width: 140 },
    { colKey: 'buys', title: '买入额', width: 110 },
    { colKey: 'sells', title: '卖出额', width: 110 },
    {
      colKey: 'realized_pnl', title: '已实现盈亏', width: 120,
      cell: ({ row }) => <span style={{ color: pnlColor(row.realized_pnl) }}>{fmtMoney(row.realized_pnl)}</span>,
    },
    { colKey: 'trade_count', title: '笔数', width: 80 },
  ]

  // 成交流水表列定义：time 去掉 ISO 的 T 并截取至秒；side 买入红/卖出绿
  const fillsColumns = [
    { colKey: 'time', title: '时间', width: 130, cell: ({ row }) => (row.traded_at || '').replace('T', ' ').slice(5, 19) },
    { colKey: 'code', title: '代码', width: 90 },
    {
      colKey: 'side', title: '方向', width: 80,
      cell: ({ row }) => <span style={{ color: row.side === '买入' ? '#e34d59' : '#00a870' }}>{row.side}</span>,
    },
    { colKey: 'price', title: '价格', width: 90 },
    { colKey: 'qty', title: '数量', width: 80 },
    { colKey: 'amount', title: '金额', width: 100 },
    { colKey: 'strategy', title: '战法', width: 140, cell: ({ row }) => <span style={{ color: '#666' }}>{row.strategy}</span> },
  ]

  return (
    <div className="page">
      <div style={{ fontSize: 20, fontWeight: 700, marginBottom: 4 }}>📈 量化交易</div>
      <div style={{ fontSize: 12, color: '#888', marginBottom: 14 }}>实盘链路参数、仓位纪律与战法白名单（保存后约 5 秒热加载生效）</div>

      <Card style={{ marginBottom: 14 }}>
        {state && state.enabled ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: state.last_probe_ok ? '#00a870' : '#666' }}>●</span>
            <span>首尔 ↔ 广州</span>
            <span style={{ fontFamily: 'monospace', color: '#4fc3f7' }}>{state.gateway_url}</span>
            {state.last_latency_ms > 0 && <Tag size="small"> {state.last_latency_ms}ms</Tag>}
            <Tag size="small" theme={state.tripped ? 'danger' : 'success'}>
              {state.tripped ? '熔断:' + (state.trip_reason || '未知') : '正常'}
            </Tag>
            <span>{state.mode === 'auto' ? '自动' : '手动'}</span>
            {fmtAgo(state.last_report_at) && <span>回报{fmtAgo(state.last_report_at)}</span>}
          </div>
        ) : (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: '#666' }}>●</span>
            <span>实盘链路未启用</span>
            <span style={{ color: '#666', fontSize: 11 }}>打开下方「总开关」并配置网关地址后开始互通</span>
          </div>
        )}
      </Card>

      <Card title="总开关与执行方式" style={{ marginBottom: 14 }}>
        <Form labelWidth={120} labelAlign="right">
          <Form.FormItem label="实盘总开关">
            <Switch value={form.enabled} onChange={(v) => { setForm({ ...form, enabled: v }); saveSwitches(v) }} />
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>关闭后引擎不再向网关传递任何信号/建议（纸面盘不受影响）</span>
          </Form.FormItem>
          <Form.FormItem label="执行模式">
            <span style={segBtn(form.mode === 'manual')} onClick={() => setForm({ ...form, mode: 'manual' })}>手动确认</span>
            <span style={segBtn(form.mode === 'auto')} onClick={() => setForm({ ...form, mode: 'auto' })}>全自动</span>
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>手动=每单前端确认；自动=信号直接下单</span>
          </Form.FormItem>
          <Form.FormItem label="委托价格">
            <span style={segBtn(form.price_type === 'market')} onClick={() => setForm({ ...form, price_type: 'market' })}>对手价</span>
            <span style={segBtn(form.price_type === 'limit')} onClick={() => setForm({ ...form, price_type: 'limit' })}>限价</span>
          </Form.FormItem>
          <Form.FormItem label="自动卖出">
            <Switch value={form.auto_sell} onChange={(v) => setForm({ ...form, auto_sell: v })} />
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>自动模式下止损/清仓级建议自动全仓卖出；止盈/减仓保持提醒</span>
          </Form.FormItem>
          <Form.FormItem label="心跳超时(秒)">
            <Input style={{ width: 140 }} type="number" value={form.miss_heartbeat_sec} min={30} max={3600}
              onChange={(v) => setForm({ ...form, miss_heartbeat_sec: parseInt(v, 10) })} />
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>连续失联超过该值触发熔断暂停下单（30-3600）</span>
          </Form.FormItem>
          <Form.FormItem label="网关地址">
            <Input style={{ flex: 1, minWidth: 240 }} value={form.gateway_url} placeholder="http://81.71.69.17:8789"
              onChange={(v) => setForm({ ...form, gateway_url: v })} />
          </Form.FormItem>
          <Form.FormItem label="鉴权Token">
            <Input style={{ flex: 1, minWidth: 240 }} type="password" value={tokenInput} placeholder={form.token_masked || '未设置'}
              onChange={(v) => setTokenInput(v)} />
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>显示为脱敏形态；留空表示保持原值不变</span>
          </Form.FormItem>
          <Form.FormItem>
            <Button theme="primary" onClick={saveExec} loading={saving}>保存执行参数</Button>
          </Form.FormItem>
        </Form>
      </Card>

      <Card title="仓位纪律" style={{ marginBottom: 14 }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 12 }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <label style={{ fontSize: 12, color: '#aaa' }}>最大持仓数</label>
            <Input type="number" value={form.max_positions} min={1} max={50} onChange={(v) => setForm({ ...form, max_positions: parseInt(v, 10) })} />
            <span style={{ fontSize: 10, color: '#666' }}>1-50，双端校验</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <label style={{ fontSize: 12, color: '#aaa' }}>单票金额(元)</label>
            <Input type="number" value={form.fixed_amount} min={0} step={500} onChange={(v) => setForm({ ...form, fixed_amount: parseFloat(v) })} />
            <span style={{ fontSize: 10, color: '#666' }}>每次买入投入金额</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <label style={{ fontSize: 12, color: '#aaa' }}>初始资金(元)</label>
            <Input type="number" value={form.initial_capital} min={0} step={10000} onChange={(v) => setForm({ ...form, initial_capital: parseFloat(v) })} />
            <span style={{ fontSize: 10, color: '#666' }}>用于仓位约束预检</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <label style={{ fontSize: 12, color: '#aaa' }}>单日买入笔数上限</label>
            <Input type="number" value={form.daily_max_buys} min={0} onChange={(v) => setForm({ ...form, daily_max_buys: parseInt(v, 10) })} />
            <span style={{ fontSize: 10, color: '#666' }}>0=不设限，防信号风暴</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <label style={{ fontSize: 12, color: '#aaa' }}>单日买入预算(元)</label>
            <Input type="number" value={form.daily_budget_amount} min={0} step={10000} onChange={(v) => setForm({ ...form, daily_budget_amount: parseFloat(v) })} />
            <span style={{ fontSize: 10, color: '#666' }}>0=不设限，超出拒绝新买入</span>
          </div>
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 10 }}>
          <Button theme="primary" onClick={saveCaps} loading={saving}>保存仓位纪律</Button>
        </div>
      </Card>

      <Card title="战法开关" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: '#666', marginBottom: 12 }}>关闭的战法信号不会进入实盘链路（模拟盘不受影响）；全部开启 = 不设白名单。因子/形态战法需先在「自动研究」页审批应用后才会出现在此处。</div>
        {strategyGroups.length === 0 ? (
          <div style={{ color: '#666', fontSize: 13, padding: '8px 2px' }}>暂无可用战法</div>
        ) : strategyGroups.map((grp) => (
          <div key={grp.kind} style={{ marginBottom: 14 }}>
            <div style={{ fontSize: 13, fontWeight: 700, color: '#1d4ed8', margin: '4px 0 6px', paddingLeft: 4, borderLeft: '3px solid #1d4ed8' }}>
              {grp.label}
            </div>
            {grp.items.map((s) => (
              <div key={s.id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, padding: '9px 4px', borderBottom: '1px solid #ededed' }}>
                <div>
                  <div style={{ fontSize: 13, color: '#333' }}>{s.name}</div>
                  <div style={{ fontFamily: 'monospace', fontSize: 11, color: '#555', marginTop: 2 }}>{s.id}</div>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, flex: 1, justifyContent: 'flex-end' }}>
                  <Input style={{ width: 100 }} type="number" min={0} step={500} value={amountsInput[s.id] ?? ''} placeholder="全局"
                    onChange={(v) => setAmountsInput({ ...amountsInput, [s.id]: v })} />
                  <span style={{ fontSize: 11, color: '#666' }}>元/次</span>
                  <Switch value={!!strategyOn[s.id]} onChange={(v) => { setStrategyOn({ ...strategyOn, [s.id]: v }); markStrategyDirty() }} />
                </div>
              </div>
            ))}
          </div>
        ))}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, justifyContent: 'flex-end', marginTop: 10 }}>
          <span style={{ fontSize: 11, color: '#666' }}>{strategyHint} · 仓位留空/0 = 使用全局单票金额</span>
          <Button theme="primary" disabled={!strategyDirty || saving} onClick={saveStrategies}>
            {saving ? '保存中…' : (strategyDirty ? '保存战法开关 *' : '已同步')}
          </Button>
        </div>
      </Card>

      <Card title="交易流水与整体盈亏" style={{ marginBottom: 14 }}>
        <div style={{ fontSize: 11, color: '#666', marginBottom: 12 }}>已实现=加权成本重放；浮动=市值-成本×数量；30s 刷新</div>
        {trades && trades.summary ? (
          <>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))', gap: 10, marginBottom: 12 }}>
              <div style={{ background: '#eef4fc', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
                <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: pnlColor(trades.summary.total_pnl) }}>{fmtMoney(trades.summary.total_pnl)}</div>
                <div style={{ fontSize: 11, color: '#777', marginTop: 3 }}>总盈亏</div>
              </div>
              <div style={{ background: '#eef4fc', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
                <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: pnlColor(trades.summary.realized_pnl) }}>{fmtMoney(trades.summary.realized_pnl)}</div>
                <div style={{ fontSize: 11, color: '#777', marginTop: 3 }}>已实现</div>
              </div>
              <div style={{ background: '#eef4fc', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
                <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: pnlColor(trades.summary.unrealized_pnl) }}>{fmtMoney(trades.summary.unrealized_pnl)}</div>
                <div style={{ fontSize: 11, color: '#777', marginTop: 3 }}>浮动盈亏</div>
              </div>
              <div style={{ background: '#eef4fc', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
                <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: '#1a1a1a' }}>{trades.summary.trade_count}</div>
                <div style={{ fontSize: 11, color: '#777', marginTop: 3 }}>成交笔数</div>
              </div>
              <div style={{ background: '#eef4fc', borderRadius: 8, padding: 10, textAlign: 'center', border: '1px solid #eef0f3' }}>
                <div style={{ fontSize: 18, fontWeight: 700, fontFamily: 'monospace', color: '#1a1a1a' }}>{trades.summary.wins}胜 / {trades.summary.losses}负</div>
                <div style={{ fontSize: 11, color: '#777', marginTop: 3 }}>卖出胜负</div>
              </div>
            </div>

            {(trades.by_strategy || []).length ? (
              <div style={{ marginBottom: 12 }}>
                <Table data={trades.by_strategy} columns={byStrategyColumns} rowKey="strategy" size="small" pagination={false} />
              </div>
            ) : (
              <div style={{ padding: '8px 2px', color: '#666', fontSize: 13 }}>暂无成交——实盘成交后此处出现按战法归因的盈亏统计（飞轮回流数据源）</div>
            )}

            {(trades.fills || []).length ? (
              <Table data={trades.fills} columns={fillsColumns} rowKey="order_id" size="small"
                pagination={{ pageSize: 10, showJumper: true }} />
            ) : null}
          </>
        ) : (
          <div style={{ color: '#666', fontSize: 13 }}>加载交易流水…</div>
        )}
      </Card>
    </div>
  )
}
