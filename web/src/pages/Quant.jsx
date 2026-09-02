// ── 量化交易页 Quant.jsx ──
// 页面用途：实盘链路总开关、执行方式、仓位纪律与战法白名单的统一管理界面。
// 主要功能：查看首尔↔广州链路状态/熔断；配置总开关、执行模式、委托价格、心跳超时、网关与 Token；
//          设定最大持仓/单票金额/预算等仓位纪律；按战法开关实盘准入并展示交易流水与归因盈亏。
//          定时轮询：链路状态 10s 一次、交易流水 30s 一次；配置修改提交后待交易时段生效。
// 使用 TDesign React 组件（Card / Form / Input / Button / Tag / Table）。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import ToggleSw from '../components/ToggleSw'
import { Card, Form, Input, Button, Tag, Table, MessagePlugin, DialogPlugin } from 'tdesign-react'
import * as api from '../api/index.js'

// 战法分组标签：form=内置形态战法、factor=因子战法、pattern=形态自动发现战法。
// 后端 /api/config/qmt 的 known_strategies 为 [{id,name,kind}]；因子/形态战法审批注入后自动出现。
// 战法类型展示名映射：形态（内置）/因子/形态自动发现，用于标签渲染
const KIND_LABELS = {
  form: '形态战法（内置）',
  factor: '因子战法',
  pattern: '形态自动发现战法',
}
// 战法类型展示顺序：按 内置→因子→自动发现 排列 tab
const KIND_ORDER = ['form', 'factor', 'pattern']

// 本地缓存键：切换 tab 时先显示上次成功加载的配置，避免开关/参数瞬间跳回默认值。
// 键含当前账号名（多账号隔离），防止 A 账号的表单缓存串给 B 账号显示。
const STORAGE_QMT_FORM_BASE = 'liangzai_qmt_form'

// cachedFormKey 返回当前账号专属的缓存键（多账号隔离：拼接账号名）。
function cachedFormKey() {
  const acc = (typeof api.getAccount === 'function' && api.getAccount()) || ''
  return acc ? STORAGE_QMT_FORM_BASE + ':' + acc : STORAGE_QMT_FORM_BASE
}

// readCachedForm 读取本地缓存的实盘表单配置；无缓存时回退旧键并返回 null。
function readCachedForm() {
  try {
    const raw = localStorage.getItem(cachedFormKey()) || localStorage.getItem(STORAGE_QMT_FORM_BASE)
    if (raw) return JSON.parse(raw)
  } catch (_) {}
  return null
}

// writeCachedForm 把实盘表单配置写入本地缓存（当前账号专属键）。
function writeCachedForm(form) {
  try {
    localStorage.setItem(cachedFormKey(), JSON.stringify(form))
  } catch (_) {}
}

// 涨跌配色（红涨绿跌）：盈亏 >=0 用红色，<0 用绿色
function pnlColor(v) {
  return (v || 0) >= 0 ? '#e34d59' : '#00a870'
}

// 通用确认弹窗：返回 Promise<boolean>，用户确认 resolve(true)、关闭/取消 resolve(false)。
// 必须只 resolve 一次：TDesign 的 d.hide() 会同步触发 onClose，若 onConfirm 先 d.hide() 再 resolve(true)，
// onClose 的 resolve(false) 会先一步生效（Promise 首次 resolve 即定型），导致"确认"被当作"取消"——
// 表现为实盘总开关点了启用却存不进、瞬间弹回关闭。这里用 done 守卫保证首次 resolve 的值生效。
// English: resolve-once guard. d.hide() synchronously fires onClose; without the guard a confirm could
// resolve false (treated as cancel), so enabling the master switch would never persist.
function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    let done = false
    const finish = (val) => {
      if (done) return
      done = true
      d.hide()
      resolve(val)
    }
    const d = DialogPlugin.confirm({
      header,
      body,
      theme: 'warning',
      onConfirm: () => finish(true),
      onClose: () => finish(false),
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
  const cachedForm = readCachedForm()
  const [form, setForm] = useState(cachedForm || {
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
  // 配置加载失败提示：加载失败时表单停留在本地缓存值，若无提示用户会误把
  // 缓存当成服务器真实状态，误以为"开关被自动关闭"。显式告警消除歧义。
  const [loadErr, setLoadErr] = useState('')
  // 后端鉴权拒绝（403）：量化交易仅管理员可访问，后端据此决定，前端只负责展示。
  // English: backend denied (403) — quant trading is admin-only; the frontend just renders the denial.
  const [forbidden, setForbidden] = useState(false)
  // 是否已从服务器同步完成：首屏先显示「同步中」占位，避免把本地缓存的旧值
  // 误当成服务器真实状态（用户反馈「开关刷新后变回关闭」多源于此闪烁）。
  // English: whether config has synced from server; show a placeholder first paint
  // so a stale cached value can never be mistaken for the server's truth.
  const [syncing, setSyncing] = useState(true)

  const stateTimer = useRef(null)  // 链路状态轮询定时器
  const tradesTimer = useRef(null) // 交易流水轮询定时器

  // 按 kind 分组（form → factor → pattern），便于分别展示"形态战法 / 因子战法"
  const strategyGroups = useMemo(() => {
    const g = { form: [], factor: [], pattern: [] }
    strategyList.forEach((s) => {
      const k = g[s.kind] ? s.kind : 'form'
      g[k].push(s)
    })
    return KIND_ORDER.map((kind) => ({ kind, label: KIND_LABELS[kind] || kind, items: g[kind] })).filter((x) => x.items.length)
  }, [strategyList])
  // 全部战法是否都处于开启状态（全部开启时白名单传空数组表示不设限）
  const allStrategyOn = useMemo(() => strategyList.length > 0 && strategyList.every((s) => strategyOn[s.id]), [strategyList, strategyOn])
  // 生成当前战法开关状态的提示文案（全部允许 / 已开启数量）
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
  // 拉取实盘配置并回填表单/战法开关/自定义金额；白名单为空数组时默认全部开启。
  // 失败时置 loadErr 告警（页面顶部显示），并向上抛出由调用方决定是否 toast。
  async function loadConfig() {
    setSyncing(true)
    try {
      const c = await api.fetchQMTConfig()
      setLoadErr('')
      const nextForm = {
        enabled: !!c.enabled, mode: c.mode || 'manual', price_type: c.price_type || 'market',
        auto_sell: !!c.auto_sell, gateway_url: c.gateway_url || '', token_masked: c.token_masked || '',
        fixed_amount: c.fixed_amount ?? 10000, max_positions: c.max_positions ?? 10,
        initial_capital: c.initial_capital ?? 100000,
        daily_max_buys: c.daily_max_buys ?? 20, daily_budget_amount: c.daily_budget_amount ?? 100000,
        miss_heartbeat_sec: c.miss_heartbeat_sec ?? 120,
      }
      setForm(nextForm)
      writeCachedForm(nextForm)
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
      setSyncing(false)
    } catch (e) {
      // 后端返回 403（无权限）时，直接展示「无权限」面板，不再走缓存兜底提示。
      if (e && e.message && e.message.indexOf('无权限') >= 0) {
        setForbidden(true)
        setSyncing(false)
        return
      }
      setLoadErr('实盘配置加载失败（' + (e && e.message ? e.message : '网络异常') + '）——下方开关显示的是本机缓存，不代表服务器真实状态')
      setSyncing(false)
      throw e
    }
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

  // 通用保存：回写指定字段到后端，成功提示并重新拉取配置。
  // 失败时除提示外，还回滚到后端真实配置——否则界面显示与实际不一致，
  // 刷新/切tab重新挂载后配置"跳回"，造成开关丢了的现象。
  async function patch(fields, okTip) {
    setSaving(true)
    try {
      await api.updateQMTConfig(fields)
      MessagePlugin.success(okTip || '已保存')
      await loadConfig()
    } catch (e) {
      MessagePlugin.error('保存失败：' + (e && e.message ? e.message : e))
      try { await loadConfig() } catch (_) {}
    } finally {
      setSaving(false)
    }
  }

  // 切换执行模式：点击立即保存到后端（全自动需二次确认），不再依赖"保存"按钮
  async function saveMode(mode) {
    if (saving) return
    if (mode === 'auto' && !(await confirmDialog('确认切换为「全自动」？信号将不经人工确认直接下单（受熔断/纪律约束）。', '切换全自动'))) return
    setForm((f) => ({ ...f, mode }))
    await patch({ mode }, mode === 'auto' ? '已切换为全自动' : '已切换为手动确认')
  }

  // 切换委托价格：点击立即保存到后端
  async function savePriceType(priceType) {
    if (saving) return
    setForm((f) => ({ ...f, price_type: priceType }))
    await patch({ price_type: priceType }, '委托价格已保存')
  }

  // 切换自动卖出：点击立即保存到后端
  async function saveAutoSell(v) {
    if (saving) return
    setForm((f) => ({ ...f, auto_sell: v }))
    await patch({ auto_sell: v }, v ? '已开启自动卖出' : '已关闭自动卖出')
  }

  // 保存实盘总开关：直接保存（不再用阻塞式二次确认，避免“开关一拨就弹回关闭”）。
  // 启用后下发真实交易指令属高危操作，故用非阻塞告警提示用户确认网关地址正确。
  // English: save immediately (no blocking confirm that could snap the switch back);
  // a non-blocking warning reminds the admin that enabling routes real orders to the gateway.
  async function saveSwitches(v) {
    if (saving) return
    await patch(
      { enabled: v },
      v
        ? '实盘链路启用已提交：将按下方参数向广州网关下发真实交易指令，待交易时段生效'
        : '实盘链路停用已提交，待交易时段生效',
    )
  }

  // 保存网关连接参数（网关地址/心跳超时/Token）；模式与价格已改为点击即存，不再随此保存
  async function saveExec() {
    const fields = {
      gateway_url: form.gateway_url, miss_heartbeat_sec: form.miss_heartbeat_sec,
    }
    if (tokenInput) fields.token = tokenInput
    await patch(fields, '网关连接参数已保存')
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
      <div style={{ fontSize: 12, color: '#888', marginBottom: 14 }}>实盘链路参数、仓位纪律与战法白名单（修改提交后，待下一交易时段自动生效）</div>

      {loadErr && (
        <div style={{ marginBottom: 12, padding: '8px 12px', borderRadius: 6, background: '#fdecea', border: '1px solid #f5c6c2', color: '#b71c1c', fontSize: 12 }}>
          ⚠️ {loadErr}
        </div>
      )}

      {forbidden && (
        <div style={{ marginBottom: 12, padding: '18px 16px', borderRadius: 8, background: '#fff7e6', border: '1px solid #ffd591', color: '#ad6800', fontSize: 13 }}>
          🔒 无权限访问量化交易：当前登录「{api.getAccount() || '未知'}」为普通用户，该页面仅管理员账号可操作。请使用管理员账号（用户名 admin）登录后再进行管理。
        </div>
      )}

      <Card style={{ marginBottom: 14 }}>
        {state && state.enabled ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: state.last_probe_ok ? '#00a870' : '#666' }}>●</span>
            <span>本机网关（实盘闭环，无两地互通）</span>
            <span style={{ fontFamily: 'monospace', color: '#4fc3f7' }}>{state.gateway_url}</span>
            <Tag size="small" theme={state.tripped ? 'danger' : 'success'}>
              {state.tripped ? '熔断:' + (state.trip_reason || '未知') : '正常'}
            </Tag>
            <span>{state.mode === 'auto' ? '自动' : '手动'}</span>
          </div>
        ) : state ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: form.enabled ? '#e6a23c' : '#666' }}>●</span>
            {form.enabled ? (
              <>
                <span>实盘链路已配置启用（修改已提交，待交易时段生效）</span>
                <span style={{ color: '#666', fontSize: 11 }}>配置变更在下一交易时段自动生效；休市/非交易时段不下发，生效后此处转为实时状态</span>
              </>
            ) : (
              <>
                <span>实盘链路未启用</span>
                <span style={{ color: '#666', fontSize: 11 }}>打开下方「总开关」并配置网关地址后开始互通</span>
              </>
            )}
          </div>
        ) : (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: '#999' }}>●</span>
            <span style={{ color: '#666' }}>链路状态加载中（若长期停留请检查网络/重新登录）</span>
          </div>
        )}
      </Card>

      <Card title="总开关与执行方式" style={{ marginBottom: 14 }}>
        <Form labelWidth={120} labelAlign="right">
          <Form.FormItem label="实盘总开关">
            {syncing ? (
              <span style={{ fontSize: 13, color: '#999' }}>同步中…</span>
            ) : (
              <ToggleSw checked={form.enabled} onChange={(v) => { setForm({ ...form, enabled: v }); saveSwitches(v) }} />
            )}
            <span style={{ color: loadErr ? '#b71c1c' : '#00a870', fontSize: 11, marginLeft: 10 }}>
              {loadErr ? '⚠ 未同步服务器（下方为本地缓存，非真实状态）' : '已同步服务器 ✓'}
            </span>
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10, display: 'block', marginTop: 4 }}>
              关闭后引擎不再向网关传递任何信号/建议（纸面盘不受影响）
            </span>
          </Form.FormItem>
          <Form.FormItem label="执行模式">
            <span style={segBtn(form.mode === 'manual')} onClick={() => saveMode('manual')}>手动确认</span>
            <span style={segBtn(form.mode === 'auto')} onClick={() => saveMode('auto')}>全自动</span>
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>点击立即生效并保存；手动=每单前端确认；自动=信号直接下单</span>
          </Form.FormItem>
          <Form.FormItem label="委托价格">
            <span style={segBtn(form.price_type === 'market')} onClick={() => savePriceType('market')}>对手价</span>
            <span style={segBtn(form.price_type === 'limit')} onClick={() => savePriceType('limit')}>限价</span>
            <span style={{ color: '#666', fontSize: 11, marginLeft: 10 }}>点击立即生效并保存</span>
          </Form.FormItem>
          <Form.FormItem label="自动卖出">
            <ToggleSw checked={form.auto_sell} onChange={(v) => { setForm({ ...form, auto_sell: v }); saveAutoSell(v) }} />
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
            <Button theme="primary" onClick={saveExec} loading={saving}>保存网关参数</Button>
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
                  <ToggleSw checked={!!strategyOn[s.id]} onChange={(v) => { setStrategyOn({ ...strategyOn, [s.id]: v }); markStrategyDirty() }} />
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
                // §FIX-20260902 补 total=长度：tdesign 未传 total 时分页默认 0 → 页脚「共 0 条」且无法翻页
                pagination={{ pageSize: 10, showJumper: true, total: trades.fills.length }} />
            ) : null}
          </>
        ) : (
          <div style={{ color: '#666', fontSize: 13 }}>加载交易流水…</div>
        )}
      </Card>
    </div>
  )
}
