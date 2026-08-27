// ── 量化交易页 Quant.jsx ──
// Quant trading page: live-trading link status, master switch / execution mode,
// position discipline, and strategy whitelist. 30s trades + 10s link polling.
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Button, Switch, Input, DialogPlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import './Quant.css'

const KNOWN_STRATEGIES = [
  { value: 'dragon', label: '龙头战法 Dragon' },
  { value: 'double_bump', label: '双响炮 DoubleBump' },
  { value: 'n_shape', label: 'N形超短 NShape' },
  { value: 'dragon_return', label: '龙回头(中线) DragonReturn' },
]

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

export default function Quant() {
  const [state, setState] = useState(null)
  const [form, setForm] = useState({
    enabled: false, mode: 'manual', price_type: 'market', auto_sell: false,
    gateway_url: '', token_masked: '',
    fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
    daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
  })
  const [tokenInput, setTokenInput] = useState('')
  const [knownStrategies, setKnownStrategies] = useState([...KNOWN_STRATEGIES.map((s) => s.value)])
  const [strategyOn, setStrategyOn] = useState({})
  const [strategyDirty, setStrategyDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [trades, setTrades] = useState(null)
  const [amountsInput, setAmountsInput] = useState({})

  const stateTimer = useRef(null)
  const tradesTimer = useRef(null)

  const strategyRows = useMemo(() => KNOWN_STRATEGIES.filter((s) => knownStrategies.includes(s.value)), [knownStrategies])
  const allStrategyOn = useMemo(() => strategyRows.every((s) => strategyOn[s.value]), [strategyRows, strategyOn])
  const strategyHint = useMemo(() => {
    const onCount = strategyRows.filter((s) => strategyOn[s.value]).length
    if (allStrategyOn) return '当前：全部允许'
    return `当前：${onCount}/${strategyRows.length} 允许进入实盘`
  }, [strategyRows, strategyOn, allStrategyOn])

  function fmtMoney(v) {
    const n = Number(v) || 0
    return (n > 0 ? '+' : '') + n.toFixed(2)
  }
  function fmtAgo(ts) {
    if (!ts) return ''
    const sec = Math.floor((Date.now() - ts * 1000) / 1000)
    if (sec < 60) return sec + '秒前'
    if (sec < 3600) return Math.floor(sec / 60) + '分前'
    return Math.floor(sec / 3600) + '小时前'
  }

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
    if (Array.isArray(c.known_strategies) && c.known_strategies.length) setKnownStrategies(c.known_strategies)
    const wl = Array.isArray(c.strategies) ? c.strategies : []
    const on = {}
    knownStrategies.forEach((v) => { on[v] = wl.length === 0 || wl.includes(v) })
    setStrategyOn(on)
    const sa = c.strategy_amounts || {}
    const ai = {}
    knownStrategies.forEach((v) => { ai[v] = sa[v] ?? '' })
    setAmountsInput(ai)
    setStrategyDirty(false)
  }

  async function loadTrades() {
    try {
      const t = await api.fetchQMTTrades()
      if (t && t.summary) setTrades(t)
    } catch (_) {}
  }

  async function loadState() {
    try { setState(await api.fetchQMTState()) } catch (_) {}
  }

  function markStrategyDirty() { setStrategyDirty(true) }

  async function patch(fields, okTip) {
    setSaving(true)
    try {
      await api.updateQMTConfig(fields)
      showToast(okTip || '已保存', 'success')
      await loadConfig()
    } catch (e) {
      showToast('保存失败：' + (e && e.message ? e.message : e), 'error')
    } finally {
      setSaving(false)
    }
  }

  async function saveSwitches(v) {
    const next = v === undefined ? form.enabled : v
    if (next && !(await confirmDialog('确认启用实盘链路？启用后将按下方参数向广州网关传递真实交易指令。', '启用实盘链路'))) {
      setForm({ ...form, enabled: false })
      return
    }
    await patch({ enabled: next }, next ? '实盘链路已启用' : '实盘链路已停用')
  }

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

  async function saveCaps() {
    await patch({
      max_positions: form.max_positions, fixed_amount: form.fixed_amount,
      initial_capital: form.initial_capital, daily_max_buys: form.daily_max_buys,
      daily_budget_amount: form.daily_budget_amount,
    }, '仓位纪律已保存')
  }

  async function saveStrategies() {
    const values = strategyRows.filter((s) => strategyOn[s.value]).map((s) => s.value)
    const amounts = {}
    for (const v of knownStrategies) {
      const n = parseFloat(amountsInput[v])
      if (!Number.isNaN(n) && n > 0) amounts[v] = n
    }
    return patch(
      { strategies: allStrategyOn ? [] : values, strategy_amounts: amounts },
      '战法开关与仓位已保存',
    )
  }

  useEffect(() => {
    loadState()
    stateTimer.current = setInterval(loadState, 10000)
    loadTrades()
    tradesTimer.current = setInterval(loadTrades, 30000)
    loadConfig().catch((e) => showToast('加载实盘配置失败：' + (e && e.message ? e.message : e), 'error'))
    return () => {
      if (stateTimer.current) clearInterval(stateTimer.current)
      if (tradesTimer.current) clearInterval(tradesTimer.current)
    }
  }, [])

  return (
    <div className="quant-page">
      <div className="page-title">📈 量化交易</div>
      <div className="page-sub">实盘链路参数、仓位纪律与战法白名单（保存后约 5 秒热加载生效）</div>

      <div className="card link-card">
        {state && state.enabled ? (
          <>
            <span className={['dot', state.last_probe_ok ? 'ok' : 'bad'].join(' ')}>●</span>
            <span>首尔 ↔ 广州</span>
            <span className="mono">{state.gateway_url}</span>
            {state.last_latency_ms > 0 && <span className="pill">{state.last_latency_ms}ms</span>}
            <span className={['pill', state.tripped ? 'warn' : 'good'].join(' ')}>
              {state.tripped ? '熔断:' + (state.trip_reason || '未知') : '正常'}
            </span>
            <span>{state.mode === 'auto' ? '自动' : '手动'}</span>
            {fmtAgo(state.last_report_at) && <span>回报{fmtAgo(state.last_report_at)}</span>}
          </>
        ) : (
          <>
            <span className="dot bad">●</span><span>实盘链路未启用</span>
            <span className="hint">打开下方「总开关」并配置网关地址后开始互通</span>
          </>
        )}
      </div>

      <div className="card">
        <div className="card-header">总开关与执行方式</div>
        <div className="form-row switch-row">
          <label className="lbl">实盘总开关</label>
          <Switch value={form.enabled} onChange={(v) => { setForm({ ...form, enabled: v }); saveSwitches(v) }} />
          <span className="hint">关闭后引擎不再向网关传递任何信号/建议（纸面盘不受影响）</span>
        </div>
        <div className="form-row">
          <label className="lbl">执行模式</label>
          <button className={['seg', form.mode === 'manual' ? 'on' : ''].join(' ')} onClick={() => setForm({ ...form, mode: 'manual' })}>手动确认</button>
          <button className={['seg', form.mode === 'auto' ? 'on' : ''].join(' ')} onClick={() => setForm({ ...form, mode: 'auto' })}>全自动</button>
          <span className="hint">手动=每单前端确认；自动=信号直接下单</span>
        </div>
        <div className="form-row">
          <label className="lbl">委托价格</label>
          <button className={['seg', form.price_type === 'market' ? 'on' : ''].join(' ')} onClick={() => setForm({ ...form, price_type: 'market' })}>对手价</button>
          <button className={['seg', form.price_type === 'limit' ? 'on' : ''].join(' ')} onClick={() => setForm({ ...form, price_type: 'limit' })}>限价</button>
        </div>
        <div className="form-row switch-row">
          <label className="lbl">自动卖出</label>
          <Switch value={form.auto_sell} onChange={(v) => setForm({ ...form, auto_sell: v })} />
          <span className="hint">自动模式下止损/清仓级建议自动全仓卖出；止盈/减仓保持提醒</span>
        </div>
        <div className="form-row">
          <label className="lbl">心跳超时(秒)</label>
          <Input className="inp short" type="number" value={form.miss_heartbeat_sec} min={30} max={3600}
            onChange={(v) => setForm({ ...form, miss_heartbeat_sec: parseInt(v, 10) })} />
          <span className="hint">连续失联超过该值触发熔断暂停下单（30-3600）</span>
        </div>
        <div className="form-row">
          <label className="lbl">网关地址</label>
          <Input className="inp wide mono" value={form.gateway_url} placeholder="http://81.71.69.17:8789"
            onChange={(v) => setForm({ ...form, gateway_url: v })} />
        </div>
        <div className="form-row">
          <label className="lbl">鉴权Token</label>
          <Input className="inp wide mono" type="password" value={tokenInput} placeholder={form.token_masked || '未设置'}
            onChange={(v) => setTokenInput(v)} />
          <span className="hint">显示为脱敏形态；留空表示保持原值不变</span>
        </div>
        <div className="save-bar">
          <Button theme="primary" onClick={saveExec} loading={saving}>保存执行参数</Button>
        </div>
      </div>

      <div className="card">
        <div className="card-header">仓位纪律</div>
        <div className="form-grid">
          <div className="field"><label>最大持仓数</label><Input className="inp" type="number" value={form.max_positions} min={1} max={50} onChange={(v) => setForm({ ...form, max_positions: parseInt(v, 10) })} /><span className="hint">1-50，双端校验</span></div>
          <div className="field"><label>单票金额(元)</label><Input className="inp" type="number" value={form.fixed_amount} min={0} step={500} onChange={(v) => setForm({ ...form, fixed_amount: parseFloat(v) })} /><span className="hint">每次买入投入金额</span></div>
          <div className="field"><label>初始资金(元)</label><Input className="inp" type="number" value={form.initial_capital} min={0} step={10000} onChange={(v) => setForm({ ...form, initial_capital: parseFloat(v) })} /><span className="hint">用于仓位约束预检</span></div>
          <div className="field"><label>单日买入笔数上限</label><Input className="inp" type="number" value={form.daily_max_buys} min={0} onChange={(v) => setForm({ ...form, daily_max_buys: parseInt(v, 10) })} /><span className="hint">0=不设限，防信号风暴</span></div>
          <div className="field"><label>单日买入预算(元)</label><Input className="inp" type="number" value={form.daily_budget_amount} min={0} step={10000} onChange={(v) => setForm({ ...form, daily_budget_amount: parseFloat(v) })} /><span className="hint">0=不设限，超出拒绝新买入</span></div>
        </div>
        <div className="save-bar">
          <Button theme="primary" onClick={saveCaps} loading={saving}>保存仓位纪律</Button>
        </div>
      </div>

      <div className="card">
        <div className="card-header">战法开关
          <span className="card-sub">关闭的战法信号不会进入实盘链路（模拟盘不受影响）；全部开启 = 不设白名单</span>
        </div>
        {strategyRows.map((s) => (
          <div className="strategy-row" key={s.value}>
            <div>
              <div className="st-name">{s.label}</div>
              <div className="st-code">{s.value}</div>
            </div>
            <div className="st-amount">
              <Input className="inp short" type="number" min={0} step={500} value={amountsInput[s.value] ?? ''} placeholder="全局"
                onChange={(v) => setAmountsInput({ ...amountsInput, [s.value]: v })} />
              <span className="hint">元/次</span>
            </div>
            <Switch value={!!strategyOn[s.value]} onChange={(v) => { setStrategyOn({ ...strategyOn, [s.value]: v }); markStrategyDirty() }} />
          </div>
        ))}
        <div className="save-bar">
          <span className="hint">{strategyHint} · 仓位留空/0 = 使用全局单票金额</span>
          <Button theme="primary" disabled={!strategyDirty || saving} onClick={saveStrategies}>
            {saving ? '保存中…' : (strategyDirty ? '保存战法开关 *' : '已同步')}
          </Button>
        </div>
      </div>

      <div className="card">
        <div className="card-header">交易流水与整体盈亏
          <span className="card-sub">已实现=加权成本重放；浮动=市值-成本×数量；30s 刷新</span>
        </div>
        {trades && trades.summary ? (
          <>
            <div className="chips">
              <div className={['chip', (trades.summary.total_pnl || 0) >= 0 ? 'up' : 'down'].join(' ')}>
                <div className="chip-num">{fmtMoney(trades.summary.total_pnl)}</div>
                <div className="chip-lbl">总盈亏</div>
              </div>
              <div className="chip">
                <div className={['chip-num', (trades.summary.realized_pnl || 0) >= 0 ? 'up' : 'down'].join(' ')}>{fmtMoney(trades.summary.realized_pnl)}</div>
                <div className="chip-lbl">已实现</div>
              </div>
              <div className="chip">
                <div className={['chip-num', (trades.summary.unrealized_pnl || 0) >= 0 ? 'up' : 'down'].join(' ')}>{fmtMoney(trades.summary.unrealized_pnl)}</div>
                <div className="chip-lbl">浮动盈亏</div>
              </div>
              <div className="chip"><div className="chip-num">{trades.summary.trade_count}</div><div className="chip-lbl">成交笔数</div></div>
              <div className="chip"><div className="chip-num">{trades.summary.wins}胜 / {trades.summary.losses}负</div><div className="chip-lbl">卖出胜负</div></div>
            </div>

            {(trades.by_strategy || []).length ? (
              <table className="tbl">
                <thead><tr><th>战法</th><th>买入额</th><th>卖出额</th><th>已实现盈亏</th><th>笔数</th></tr></thead>
                <tbody>
                  {trades.by_strategy.map((st) => (
                    <tr key={st.strategy}>
                      <td>{st.strategy}</td><td>{st.buys}</td><td>{st.sells}</td>
                      <td className={st.realized_pnl >= 0 ? 'up' : 'down'}>{fmtMoney(st.realized_pnl)}</td>
                      <td>{st.trade_count}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : <p className="hint empty-tip">暂无成交——实盘成交后此处出现按战法归因的盈亏统计（飞轮回流数据源）</p>}

            {(trades.fills || []).length ? (
              <table className="tbl fills-tbl">
                <thead><tr><th>时间</th><th>代码</th><th>方向</th><th>价格</th><th>数量</th><th>金额</th><th>战法</th></tr></thead>
                <tbody>
                  {trades.fills.map((f, i) => (
                    <tr key={f.order_id + f.traded_at + i}>
                      <td className="mono">{(f.traded_at || '').replace('T', ' ').slice(5, 19)}</td>
                      <td className="mono">{f.code}</td>
                      <td className={f.side === '买入' ? 'up' : 'down'}>{f.side}</td>
                      <td>{f.price}</td><td>{f.qty}</td><td>{f.amount}</td>
                      <td className="mono dim">{f.strategy}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : null}
          </>
        ) : <p className="hint">加载交易流水…</p>}
      </div>
    </div>
  )
}
