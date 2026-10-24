// RegimeConfigPanel.jsx — §N6-RISK（2026-10-21）信号生成层的风控与仓位开关。
//
// 这一组开关决定「宏观 / 大盘 / 风险情绪」如何进入信号生成：
//   · enhance.market_state_enabled —— 市场状态机是否参与风险档与仓位（大盘走向维度）；
//   · sizing.enabled / sell_boost_level —— 买入动态仓位缩放、卖出加权档位（L2=确认窗减半+取消延持）；
//   · macro_gate.* —— Yellow/Red 置信度门槛、Red 禁新开仓（应急阀）、情绪参与、N 形/动量硬拦；
//     注意 block_n_shape / block_momentum 是**一把闸管两处**：applyRiskTier（Red 风险档）与
//     applyMacroGate（期货交割日事件）读同一对键，本面板是它们唯一的页面入口，文案必须把两处
//     都点名——只写「Red 拦截」会让管理员以为关掉只影响风险档（§N6-RISK 复核时锤出，属既有
//     耦合、非本批引入：Generalize 自 §0925EVE 的 applyMacroGate 时就共用了键）。
//   · sizing.tier_scales / breadth_missing_scale —— 风险档金额权重与广度缺失降权。
//
// §SIZING-FINE（2026-10-22）：把 `SizingConfig` 剩余的**精细参数**全部搬到页面（owner 要求
// 「精细参数做到前端」）。此前只有四个金额类权重可调，Kelly / 波动率 / 情绪与状态映射 /
// 宏观前瞻曲线 / 节前分级 / 大盘跌幅档 / 连续止损熔断 / 止损止盈偏移都只能改配置文件。
// 精细参数收在「展开精细参数」折叠区里：默认不展开，避免主面板一眼三十行；展开后按合成链路分组。
// 读取侧一律按 `GetSizingConfig` 的**补齐默认后**的真值展示（后端 WithDefaults），缺失才回落
// 出厂默认——数字缺失折叠成 0 会让管理员以为风控被关掉。
//
// 纪律与战法/D1 面板一致：读取失败=禁止保存（红条 + 重载）；数字缺失不折叠成 0；
// 保存走稀疏 merge（只提交本面板负责的键）。执行层不读这些开关——它们只影响信号长什么样。
//
// English: admin panel for the signal-generation risk switches plus every fine-grained SizingConfig
// knob (collapsible). Load failure disables saving; numbers never silently fold to 0; writes are
// sparse merges of the keys owned here, and reads show the defaults-filled effective values.
import React, { useCallback, useEffect, useState } from 'react'
import { Button, Card, InputNumber, Tag } from 'tdesign-react'
import ToggleSw from './ToggleSw'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

const rowStyle = { display: 'flex', alignItems: 'center', gap: 12, padding: '7px 0', flexWrap: 'wrap' }
const labelStyle = { width: 230, flexShrink: 0, color: 'var(--app-muted-2)', fontSize: 13 }
const hintStyle = { color: 'var(--app-muted-2)', fontSize: 12 }
const groupStyle = { margin: '16px 0 2px', fontSize: 12, fontWeight: 600, color: 'var(--app-text-2)' }

// 出厂默认：与 internal/config/config.go::DefaultSizingConfig 逐键对齐。
// 只在后端载荷缺键时兜底；正常情况下后端已经 WithDefaults，页面显示的就是引擎实际生效值。
const DEFAULT_EMOTION = { 退潮: 0.50, 背离: 0.60, 高潮: 0.80, 发酵: 1.00, 启动: 1.00 }
const DEFAULT_STATE = { bull: 1.00, range: 0.70, bear: 0.40 }
const DEFAULT_MACRO_WINDOW = { high: 0.50, medium: 0.80, low: 1.00 }
const DEFAULT_LEAD_CURVE = [0.90, 0.85, 0.80, 0.70, 0.50]
const DEFAULT_HOLIDAY = { weekend: 0.85, small1: 0.70, small2: 0.85, long1: 0.40, long2: 0.60, long3: 0.75 }
const DEFAULT_INDEX = { '-1': 1.00, '-2': 0.70, deep: 0.40 }
const DEFAULTS = {
  floor: 0.15,
  cap: 1.15,
  kellyLambda: 0.5,
  kellyMinProb: 0.52,
  volTarget: 2.0,
  volLo: 0.5,
  volHi: 1.5,
  leadDays: 5,
  eventDay: 0.30,
  llmBearish: 0.5,
  maxPos: 0.80,
  maxDD: 0.10,
  guardCount: 4,
  guardWindow: 1,
  slPerPct: 3.0,
  slCap: 4.0,
  tpPerPct: 3.0,
  tpCap: 5.0,
  slMinDrop: 1.0,
  bearTighten: 1.0,
}

function num(v, fallback) {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? n : fallback
}

function intNum(v, fallback) {
  const n = Number(v)
  return Number.isInteger(n) && n > 0 ? n : fallback
}

// mapNums 按出厂默认的键集读映射：只覆盖「有正有限值」的键，其余保留默认。
// 后端 WithDefaults 保证键齐全；这里兜底旧载荷/空载荷，避免界面出现 0 或 undefined。
function mapNums(src, defaults) {
  const out = { ...defaults }
  if (src && typeof src === 'object') {
    for (const k of Object.keys(defaults)) {
      const n = Number(src[k])
      if (Number.isFinite(n) && n > 0) out[k] = n
    }
  }
  return out
}

// parseCurve 解析前瞻曲线文本：逗号/中文逗号/空白都当分隔符，逐项转数字。
function parseCurve(text) {
  return String(text == null ? '' : text)
    .split(/[,，\s]+/)
    .filter((s) => s !== '')
    .map(Number)
}

// curveText 把曲线数组转成输入框文本（两位小数，逗号分隔）。
function curveText(arr) {
  const list = Array.isArray(arr) && arr.length ? arr : DEFAULT_LEAD_CURVE
  return list.map((v) => Number(num(v, 0)).toFixed(2)).join(', ')
}

// toNum 输入框取值：空串/null 必须变成 NaN（缺失），不能折算成 0——0 是「真关掉」的语义。
function toNum(v) {
  if (v === null || v === undefined || v === '') return NaN
  return Number(v)
}

// NumRow 精细参数行：标签 + 数字输入 + 说明。测试按标签文案定位本行（不按顺序）。
function NumRow({ label, hint, value, min = 0.01, max = 2, step = 0.01, precision, onChange, testId }) {
  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <InputNumber
        data-testid={testId}
        min={min}
        max={max}
        step={step}
        precision={precision}
        value={value}
        onChange={(v) => onChange(toNum(v))}
      />
      {hint && <span style={hintStyle}>{hint}</span>}
    </div>
  )
}

// SwitchRow 精细参数开关行：标签 + 开关 + 说明。
function SwitchRow({ label, hint, checked, onChange }) {
  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <ToggleSw checked={checked} onChange={onChange} />
      {hint && <span style={hintStyle}>{hint}</span>}
    </div>
  )
}

/** 风险与仓位设置面板（仅管理员挂载，与 /api/config/regime 的 adminMiddleware 同门槛）。 */
export default function RegimeConfigPanel() {
  const [state, setState] = useState('loading') // loading / loaded / error
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [baseline, setBaseline] = useState('')
  const [form, setForm] = useState(null)
  const [showFine, setShowFine] = useState(false) // 精细参数折叠区
  // §A0 账号级 sizing：accountId 为空 = 当前登录账号；admin 选择器可代配他人。
  const [accountId, setAccountId] = useState('')
  const [accounts, setAccounts] = useState([])
  const [hasOverride, setHasOverride] = useState(false)

  const set = useCallback((patch) => setForm((f) => ({ ...f, ...patch })), [])

  async function load(id = accountId) {
    setState('loading')
    setError('')
    try {
      const cfg = await api.fetchRegimeConfig(id || undefined)
      if (!cfg || typeof cfg !== 'object') throw new Error('返回载荷为空/非对象')
      const enh = cfg.enhance || {}
      const sz = cfg.sizing || {}
      const mg = cfg.macro_gate || {}
      const tiers = sz.tier_scales || {}
      const tech = enh.tech_filter || {}
      const conc = cfg.concept || {}
      const next = {
        marketState: !!enh.market_state_enabled,
        // §DYN-GLOBAL：动态仓位现在出厂即开，账号级 enabled 只是退路。键缺失必须读成"开"
        // （与后端 WithDefaults 同口径），写成 `!!sz.enabled` 会把没写过该键的存量账号显示成关，
        // 而引擎其实按开在跑——两侧读数不一致会让管理员以为要"打开"才生效，点了保存反而多写一笔。
        sizingOn: sz.enabled !== false,
        // §DYN-GLOBAL：预算口径出厂即 dynamic，legacy 只剩账号级退路。这里的兜底值必须和后端
        // 默认一致（键缺失=没写过=按 dynamic 跑），否则面板显示 legacy、实际下单走 dynamic。
        // §DYN-SIZE-v3：预算口径与合成口径（账号级）。
        budgetMode: String(sz.budget_mode || 'dynamic').toLowerCase(),
        sizingMode: String(sz.mode || 'product').toLowerCase(),
        powerP: num(sz.power_p, 0.5),
        groupMarket: num((sz.group_floors || {}).market, 0.45),
        groupStock: num((sz.group_floors || {}).stock, 0.35),
        sellBoost: String(sz.sell_boost_level || 'L2').toUpperCase(),
        yellowConf: num(mg.yellow_confidence, 0.90),
        redConf: num(mg.red_confidence, 0.92),
        yellowWeight: num(tiers.Yellow, 0.60),
        redWeight: num(tiers.Red, 0.30),
        redBlockNewBuy: mg.red_block_new_buy === true,
        breadthMissing: num(sz.breadth_missing_scale, 0.90),
        emotionOn: mg.emotion_enabled !== false,
        blockN: mg.block_n_shape !== false,
        blockMomentum: mg.block_momentum !== false,
        // ── 精细参数（SizingConfig 全量） ──
        // §DYN-GLOBAL：影子出厂默认关（缩放真下发）。键缺失必须读成"不影子"，写成
        // `!== false` 会把缺键显示成影子开、保存时再把 shadow=true 真写进配置，
        // 等于用一次点击把刚上线的动态缩放重新按回只留痕。
        shadow: sz.shadow === true,
        floor: num(sz.floor, DEFAULTS.floor),
        cap: num(sz.cap, DEFAULTS.cap),
        kellyLambda: num(sz.kelly_lambda, DEFAULTS.kellyLambda),
        kellyMinProb: num(sz.kelly_min_prob, DEFAULTS.kellyMinProb),
        volTarget: num(sz.vol_target_pct, DEFAULTS.volTarget),
        volLo: num(sz.vol_clamp_lo, DEFAULTS.volLo),
        volHi: num(sz.vol_clamp_hi, DEFAULTS.volHi),
        emotion: mapNums(sz.emotion_scales, DEFAULT_EMOTION),
        stateScales: mapNums(sz.state_scales, DEFAULT_STATE),
        macroWindow: mapNums(sz.macro_window_scales, DEFAULT_MACRO_WINDOW),
        leadDays: intNum(sz.macro_lead_days, DEFAULTS.leadDays),
        leadCurve: curveText(sz.macro_lead_curve),
        eventDay: num(sz.macro_event_day_scale, DEFAULTS.eventDay),
        holiday: mapNums(sz.holiday_scales, DEFAULT_HOLIDAY),
        holidayBlockNew: sz.holiday_block_new === true,
        indexScales: mapNums(sz.index_scale_thresholds, DEFAULT_INDEX),
        llmBearish: num(sz.llm_bearish_scale, DEFAULTS.llmBearish),
        maxPos: num(sz.max_total_position_pct, DEFAULTS.maxPos),
        maxDD: num(sz.max_drawdown_pct, DEFAULTS.maxDD),
        guardCount: intNum(sz.stoploss_guard_count, DEFAULTS.guardCount),
        guardWindow: intNum(sz.stoploss_guard_window_days, DEFAULTS.guardWindow),
        slPerPct: num(sz.sl_offset_per_pct, DEFAULTS.slPerPct),
        slCap: num(sz.sl_offset_cap, DEFAULTS.slCap),
        tpPerPct: num(sz.tp_offset_per_pct, DEFAULTS.tpPerPct),
        tpCap: num(sz.tp_offset_cap, DEFAULTS.tpCap),
        slMinDrop: num(sz.sl_offset_min_index_drop, DEFAULTS.slMinDrop),
        bearTighten: num(sz.bear_sl_tighten_pt, DEFAULTS.bearTighten),
        riskBudgetKeep: sz.sl_offset_risk_budget_keep !== false,
        // §C4 / §C6 / §C1-V2（系统级）
        techKdjMax: num(tech.kdj_j_max, 0),
        techMa: String(tech.ma_filter || 'off'),
        techShadow: tech.shadow !== false,
        riskPoolShadow: enh.risk_pool_shadow !== false,
        conceptEnabled: conc.enabled === true,
        conceptShadow: conc.shadow !== false,
        conceptCoverage: num(conc.min_coverage_pct, 95),
        conceptStaleness: intNum(conc.max_staleness_hours, 48),
      }
      setForm(next)
      setBaseline(JSON.stringify(next))
      setHasOverride(cfg.sizing_has_override === true)
      setState('loaded')
    } catch (e) {
      setForm(null)
      setBaseline('')
      setError(String((e && e.message) || e))
      setState('error')
    }
  }

  useEffect(() => { load('') }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // 账号选择器数据：平台/租户 admin 会成功；普通账号 403 时静默忽略，面板退回“当前账号”。
  useEffect(() => {
    let alive = true
    // 测试里 api 是部分 mock：Vitest 对未定义导出会在“取值”时直接抛错，故必须 try/catch，
    // 不能只写 typeof 判断。取不到就把选择器留在“当前账号”。
    let loadUsers = null
    try { loadUsers = api.fetchAdminUsers } catch { loadUsers = null }
    if (typeof loadUsers !== 'function') return undefined
    loadUsers()
      .then((res) => {
        if (!alive) return
        const list = (res && Array.isArray(res.users)) ? res.users : []
        setAccounts(list.filter((u) => u && u.id))
      })
      .catch(() => { /* 非管理员：无账号列表，保留当前账号 */ })
    return () => { alive = false }
  }, [])

  const dirty = form && baseline && JSON.stringify(form) !== baseline

  // setMap 更新嵌套映射中的单个键（情绪/状态/节前/大盘档位共用）。
  function setMap(key, k, v) {
    setForm((f) => ({ ...f, [key]: { ...f[key], [k]: v } }))
  }

  // validate 返回无效项名称清单。分三档：比例 (0,1]、正数 (0,hi]、整数区间。
  // 缺失（NaN，来自清空输入框）与 0 都算无效——0 在这些字段里意味着闸门失效/仓位归零。
  function validate(f) {
    const bad = []
    const finite = (v) => Number.isFinite(Number(v))
    const ratio = (name, v) => { if (!finite(v) || !(Number(v) > 0 && Number(v) <= 1)) bad.push(name) }
    const pos = (name, v, hi) => { if (!finite(v) || !(Number(v) > 0 && Number(v) <= hi)) bad.push(name) }
    const intIn = (name, v, lo, hi) => {
      if (!Number.isInteger(Number(v)) || Number(v) < lo || Number(v) > hi) bad.push(name)
    }
    const mapAll = (prefix, m) => {
      for (const k of Object.keys(m)) pos(`${prefix}「${k}」`, m[k], 2)
    }

    ratio('Yellow 置信度', f.yellowConf)
    ratio('Red 置信度', f.redConf)
    ratio('Yellow 金额权重', f.yellowWeight)
    ratio('Red 金额权重', f.redWeight)
    ratio('广度缺失降权', f.breadthMissing)
    // §DYN-SIZE-v3：p 允许 0（几何平均）与负值，范围 [-10,10]；分组 floor 0~1。
    if (!Number.isFinite(Number(f.powerP)) || Number(f.powerP) < -10 || Number(f.powerP) > 10) bad.push('幂平均指数 p')
    ratio('市场组 floor', f.groupMarket)
    ratio('个股组 floor', f.groupStock)

    pos('仓位缩放地板', f.floor, 5)
    pos('仓位缩放上限', f.cap, 5)
    if (!(Number(f.floor) < Number(f.cap))) bad.push('地板须小于上限')
    pos('Kelly 系数', f.kellyLambda, 1)
    pos('Kelly 最小优势概率', f.kellyMinProb, 1)
    pos('波动率目标', f.volTarget, 100)
    pos('波动率乘子下限', f.volLo, 5)
    pos('波动率乘子上限', f.volHi, 5)
    if (!(Number(f.volLo) <= Number(f.volHi))) bad.push('波动率下限须≤上限')

    mapAll('情绪权重', f.emotion)
    mapAll('状态权重', f.stateScales)
    mapAll('宏观影响期权重', f.macroWindow)

    const leadDays = Number(f.leadDays)
    const leadDaysOK = Number.isInteger(leadDays) && leadDays >= 1 && leadDays <= 20
    if (!leadDaysOK) bad.push('事件前瞻交易日数')
    const curve = parseCurve(f.leadCurve)
    if (!leadDaysOK || curve.length !== leadDays || !curve.every((v) => Number.isFinite(v) && v > 0 && v <= 2)) {
      bad.push('前瞻曲线（个数须等于前瞻交易日数）')
    }
    pos('事件当日缩放', f.eventDay, 2)

    mapAll('节前权重', f.holiday)
    mapAll('大盘档位权重', f.indexScales)

    pos('LLM 利空缩放', f.llmBearish, 2)
    ratio('总仓位上限', f.maxPos)
    ratio('权益回撤熔断比例', f.maxDD)
    intIn('连续止损熔断次数', f.guardCount, 1, 100)
    intIn('连续止损回看窗', f.guardWindow, 1, 20)
    pos('止损放宽系数', f.slPerPct, 50)
    pos('止损放宽上限', f.slCap, 50)
    pos('止盈上浮系数', f.tpPerPct, 50)
    pos('止盈上浮上限', f.tpCap, 50)
    pos('止损放宽触发跌幅', f.slMinDrop, 20)
    pos('趋势破位止损收紧', f.bearTighten, 50)
    // §C4：0=关；>0 时上限 200。
    const kdj = Number(f.techKdjMax)
    if (!Number.isFinite(kdj) || kdj < 0 || kdj > 200) bad.push('KDJ J 上限')
    // §C1-V2：0=恢复默认 95；允许 0，其余 0-100。
    const cov = Number(f.conceptCoverage)
    if (!Number.isFinite(cov) || cov < 0 || cov > 100) bad.push('概念覆盖率门槛')
    const stale = Number(f.conceptStaleness)
    if (!Number.isInteger(stale) || stale < 0 || stale > 720) bad.push('概念数据最大陈旧小时')
    return bad
  }

  async function save() {
    if (state !== 'loaded' || !form) return
    const bad = validate(form)
    if (bad.length) {
      showToast(`以下参数无效（缺失或越界），禁止保存：${bad.join('、')}`, 'error')
      return
    }
    setSaving(true)
    try {
      const body = {
        enhance: {
          market_state_enabled: !!form.marketState,
          // §C4/§C6 系统级增强开关
          risk_pool_shadow: !!form.riskPoolShadow,
          tech_filter: {
            kdj_j_max: Number(form.techKdjMax),
            ma_filter: form.techMa,
            shadow: !!form.techShadow,
          },
        },
        concept: {
          enabled: !!form.conceptEnabled,
          shadow: !!form.conceptShadow,
          min_coverage_pct: Number(form.conceptCoverage),
          max_staleness_hours: Number(form.conceptStaleness),
        },
        sizing: {
          enabled: !!form.sizingOn,
          // §DYN-SIZE-v3：dynamic=动态仓位是唯一预算决策者；power_mean=分组幂平均。
          budget_mode: form.budgetMode === 'dynamic' ? 'dynamic' : 'legacy',
          mode: form.sizingMode === 'power_mean' ? 'power_mean' : 'product',
          power_p: Number(form.powerP),
          group_floors: { market: Number(form.groupMarket), stock: Number(form.groupStock) },
          shadow: !!form.shadow,
          sell_boost_level: form.sellBoost,
          breadth_missing_scale: Number(form.breadthMissing),
          tier_scales: { Yellow: Number(form.yellowWeight), Red: Number(form.redWeight) },
          floor: Number(form.floor),
          cap: Number(form.cap),
          kelly_lambda: Number(form.kellyLambda),
          kelly_min_prob: Number(form.kellyMinProb),
          vol_target_pct: Number(form.volTarget),
          vol_clamp_lo: Number(form.volLo),
          vol_clamp_hi: Number(form.volHi),
          emotion_scales: { ...form.emotion },
          state_scales: { ...form.stateScales },
          macro_window_scales: { ...form.macroWindow },
          macro_lead_days: Number(form.leadDays),
          macro_lead_curve: parseCurve(form.leadCurve),
          macro_event_day_scale: Number(form.eventDay),
          holiday_scales: { ...form.holiday },
          holiday_block_new: !!form.holidayBlockNew,
          index_scale_thresholds: { ...form.indexScales },
          llm_bearish_scale: Number(form.llmBearish),
          max_total_position_pct: Number(form.maxPos),
          max_drawdown_pct: Number(form.maxDD),
          stoploss_guard_count: Number(form.guardCount),
          stoploss_guard_window_days: Number(form.guardWindow),
          sl_offset_per_pct: Number(form.slPerPct),
          sl_offset_cap: Number(form.slCap),
          tp_offset_per_pct: Number(form.tpPerPct),
          tp_offset_cap: Number(form.tpCap),
          sl_offset_min_index_drop: Number(form.slMinDrop),
          bear_sl_tighten_pt: Number(form.bearTighten),
          sl_offset_risk_budget_keep: !!form.riskBudgetKeep,
        },
        macro_gate: {
          yellow_confidence: Number(form.yellowConf),
          red_confidence: Number(form.redConf),
          red_block_new_buy: !!form.redBlockNewBuy,
          emotion_enabled: !!form.emotionOn,
          block_n_shape: !!form.blockN,
          block_momentum: !!form.blockMomentum,
        },
      }
      await api.setRegimeConfig(body, accountId || undefined)
      setBaseline(JSON.stringify(form))
      setHasOverride(true)
      showToast('风控与仓位配置已保存，热更新即时生效', 'success')
    } catch (e) {
      showToast('保存失败: ' + String((e && e.message) || e), 'error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card title="风控与仓位（信号生成层）" style={{ marginBottom: 16 }}>
      {state === 'error' && (
        <div
          role="alert"
          style={{
            margin: '0 0 10px', padding: '8px 10px', borderRadius: 4, fontSize: 12,
            background: 'var(--app-danger-bg, #fdecee)', border: '1px solid var(--app-border, #e0e0e0)',
          }}
        >
          <div style={{ fontWeight: 600 }}>⛔ 读取失败，禁止保存</div>
          <div style={{ marginTop: 4 }}>{error || '风控配置读取失败（原因未知），已禁止保存以免覆盖他人配置。'}</div>
        </div>
      )}
      <div style={{ ...rowStyle, gap: 8 }}>
        <span className="muted" style={{ fontSize: 12 }}>
          这些开关只影响**信号怎么生成**（买入降权 / 卖出加权）；执行层只执行最终动作。
          {state === 'loading' && ' 读取中…'}
        </span>
        {state === 'error' && <Button variant="outline" size="small" onClick={load}>重载</Button>}
        {dirty && <span style={{ color: 'var(--app-warn-text)', fontSize: 12 }}>● 有未保存修改</span>}
      </div>

      {form && (
        <>
          {/* §A0 账号选择器：admin 可代配任意账号；普通账号只看到自己的配置。 */}
          <div style={rowStyle}>
            <span style={labelStyle}>配置账号</span>
            <select
              data-testid="account-select"
              value={accountId}
              onChange={(e) => { const id = e.target.value; setAccountId(id); load(id) }}
              disabled={accounts.length === 0}
              style={{ minWidth: 220, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="">当前登录账号</option>
              {accounts.map((u) => (
                <option key={u.id} value={u.id}>{u.username || u.id}</option>
              ))}
            </select>
            <span style={hintStyle}>仓位参数按账号独立；市场状态/风险档门槛为系统级（仅管理员可改）</span>
          </div>
          {state === 'loaded' && !hasOverride && (
            <div style={{ ...hintStyle, margin: '0 0 6px' }}>
              该账号尚未保存过专用仓位配置，当前继承旧账号/全局值；首次保存后将物化为账号独立配置。
            </div>
          )}
          <div style={rowStyle}>
            <span style={labelStyle}>市场状态机（大盘走向）</span>
            <Tag theme="primary" variant="light" size="small">系统级</Tag>
            <ToggleSw checked={form.marketState} onChange={(v) => set({ marketState: v })} />
            <span style={hintStyle}>开启后 bull/range/bear 参与风险档与仓位；广度缺失按下方系数降权</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>动态仓位（买入缩放）</span>
            <Tag theme="success" variant="light" size="small">账号级</Tag>
            <ToggleSw checked={form.sizingOn} onChange={(v) => set({ sizingOn: v })} />
            <span style={hintStyle}>按市场状态、风险档、宏观窗口、波动率等合成买入仓位倍数</span>
          </div>
          {form.sizingOn && form.shadow && (
            <div
              role="status"
              style={{
                margin: '0 0 10px', padding: '8px 10px', borderRadius: 4, fontSize: 12,
                background: 'var(--app-warn-bg, #fff7e6)', border: '1px solid var(--app-border, #e0e0e0)',
              }}
            >
              当前为影子模式：只留痕，不改变下单金额。要真正生效请关闭「影子模式」。
            </div>
          )}
          <div style={rowStyle}>
            <span style={labelStyle}>预算口径</span>
            <select
              data-testid="budget-mode"
              value={form.budgetMode}
              onChange={(e) => set({ budgetMode: e.target.value })}
              style={{ minWidth: 220, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="dynamic">dynamic 动态仓位唯一预算（v3，默认）</option>
              <option value="legacy">legacy 旧固定金额/战法分配/每日闸（账号级退路）</option>
            </select>
            <span style={hintStyle}>dynamic（默认）：总资产（模拟盘=池权益）× 缩放，再减到行业/单票 35% 额度；旧金额与次数闸不再拦（改成 legacy 即按账号退回旧口径）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>合成口径</span>
            <select
              data-testid="sizing-mode"
              value={form.sizingMode}
              onChange={(e) => set({ sizingMode: e.target.value })}
              style={{ minWidth: 220, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="product">product 旧连乘（逐字回归）</option>
              <option value="power_mean">power_mean 分组幂平均（v3）</option>
            </select>
            <span style={hintStyle}>power_mean：市场组/个股组各自幂平均，再组间合成，避免多因子过度连乘</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>幂平均指数 p</span>
            <InputNumber data-testid="power-p" min={-10} max={10} step={0.1} value={form.powerP} onChange={(v) => set({ powerP: toNum(v) })} />
            <span style={hintStyle}>0.5=更宽容；0=几何平均；仅 power_mean 生效</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>分组 floor</span>
            <InputNumber data-testid="group-market-floor" min={0.01} max={1} step={0.01} value={form.groupMarket} onChange={(v) => set({ groupMarket: toNum(v) })} />
            <span style={hintStyle}>市场组（默认 0.45）</span>
            <InputNumber data-testid="group-stock-floor" min={0.01} max={1} step={0.01} value={form.groupStock} onChange={(v) => set({ groupStock: toNum(v) })} />
            <span style={hintStyle}>个股组（默认 0.35）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>卖出加权档位</span>
            <select
              data-testid="sell-boost"
              value={form.sellBoost}
              onChange={(e) => set({ sellBoost: e.target.value })}
              style={{ minWidth: 200, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="OFF">off（不启用）</option>
              <option value="L1">L1 只标注</option>
              <option value="L2">L2 加快确认（推荐）</option>
              <option value="L3">L3 trim→close（激进）</option>
            </select>
            <Tag theme="warning" variant="light">L2：确认窗减半 + 取消延持 + Bear 单源</Tag>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Yellow 置信度门槛</span>
            <InputNumber min={0.01} max={1} step={0.01} value={form.yellowConf} onChange={(v) => set({ yellowConf: toNum(v) })} />
            <span style={hintStyle}>非 N 形/非龙头买入低于此值降级 watch（默认 0.90）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Red 置信度门槛</span>
            <InputNumber min={0.01} max={1} step={0.01} value={form.redConf} onChange={(v) => set({ redConf: toNum(v) })} />
            <span style={hintStyle}>默认 0.92；龙头豁免该门槛（由金额权重收束）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Yellow 金额权重</span>
            <InputNumber min={0.01} max={1} step={0.05} value={form.yellowWeight} onChange={(v) => set({ yellowWeight: toNum(v) })} />
            <span style={hintStyle}>风险档 Yellow 的买入仓位乘子（默认 0.60）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Red 金额权重</span>
            <InputNumber min={0.01} max={1} step={0.05} value={form.redWeight} onChange={(v) => set({ redWeight: toNum(v) })} />
            <span style={hintStyle}>默认 0.30（收束而非一刀切）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Red 禁止新开仓（应急）</span>
            <ToggleSw checked={form.redBlockNewBuy} onChange={(v) => set({ redBlockNewBuy: v })} />
            <span style={hintStyle}>默认关；打开后 Red 日全部买入在信号层降级 watch（含龙头）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>广度缺失降权</span>
            <InputNumber min={0.01} max={1} step={0.05} value={form.breadthMissing} onChange={(v) => set({ breadthMissing: toNum(v) })} />
            <span style={hintStyle}>涨跌家数缺失时的仓位乘子（默认 0.90），数据恢复自动回真实广度</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>情绪相位参与风险档</span>
            <ToggleSw checked={form.emotionOn} onChange={(v) => set({ emotionOn: v })} />
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>拦截 N 形超短（Red 档 + 交割日）</span>
            <ToggleSw checked={form.blockN} onChange={(v) => set({ blockN: v })} />
            <span style={hintStyle}>默认开。同一开关管两件事：Red 风险档日 N 形买入一律降级 watch，以及宏观事件（期货交割日）拦截 N 形超短——关掉两边一起关</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>拦截动量观察（Red 档 + 交割日）</span>
            <ToggleSw checked={form.blockMomentum} onChange={(v) => set({ blockMomentum: v })} />
            <span style={hintStyle}>默认开。同上：Red 日不再新增动量 watch，交割日事件拦截也走这一把闸</span>
          </div>

          {/*
            精细参数折叠区：SizingConfig 除上面四个金额权重以外的全部字段。
            默认收起；展开后按合成链路分组（边界 → Kelly/波动率 → 情绪状态宏观 → 前瞻 → 节前大盘 → 利空与上限 → 熔断与偏移）。
          */}
          <div style={{ ...rowStyle, gap: 8, borderTop: '1px solid var(--app-border, #eee)', marginTop: 6 }}>
            <Button variant="outline" size="small" onClick={() => setShowFine((v) => !v)}>
              {showFine ? '收起精细参数' : '展开精细参数'}
            </Button>
            <span style={hintStyle}>合成边界 / Kelly / 波动率 / 情绪状态 / 宏观前瞻 / 节前与大盘 / 止损熔断与偏移</span>
          </div>

          {showFine && (
            <>
              <div style={groupStyle}>合成边界与影子模式</div>
              <SwitchRow
                label="影子模式（只留痕不下发）"
                hint="默认开；灰度时保持开启，合成结果只写日志，不改变实际下单金额"
                checked={form.shadow}
                onChange={(v) => set({ shadow: v })}
              />
              <NumRow label="仓位缩放地板" hint="任何利空叠加也不会低于该值（默认 0.15）" value={form.floor} max={5} step={0.01} onChange={(v) => set({ floor: v })} />
              <NumRow label="仓位缩放上限" hint="进攻档的硬顶（默认 1.15），须大于地板" value={form.cap} max={5} step={0.01} onChange={(v) => set({ cap: v })} />

              <div style={groupStyle}>Kelly 与波动率</div>
              <NumRow label="Kelly 系数" hint="Fractional Kelly 的 λ（默认 0.5 = 半凯利）" value={form.kellyLambda} max={1} step={0.05} onChange={(v) => set({ kellyLambda: v })} />
              <NumRow label="Kelly 最小优势概率" hint="置信度低于此值视为无优势，固定 0.6 缩放（默认 0.52）" value={form.kellyMinProb} max={1} step={0.01} onChange={(v) => set({ kellyMinProb: v })} />
              <NumRow label="波动率目标" hint="ATR 占现价的目标百分比（默认 2.0）：低波动放大、高波动缩小" value={form.volTarget} max={100} step={0.1} onChange={(v) => set({ volTarget: v })} />
              <NumRow label="波动率乘子下限" hint="默认 0.5" value={form.volLo} max={5} step={0.05} onChange={(v) => set({ volLo: v })} />
              <NumRow label="波动率乘子上限" hint="默认 1.5，须不小于下限" value={form.volHi} max={5} step={0.05} onChange={(v) => set({ volHi: v })} />

              <div style={groupStyle}>情绪相位 / 市场状态 / 宏观影响期</div>
              {Object.keys(DEFAULT_EMOTION).map((k) => (
                <NumRow
                  key={'emo-' + k}
                  label={`情绪「${k}」权重`}
                  hint={`默认 ${DEFAULT_EMOTION[k].toFixed(2)}（冰点走硬拦，不进本表）`}
                  value={form.emotion[k]}
                  onChange={(v) => setMap('emotion', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_STATE).map((k) => (
                <NumRow
                  key={'state-' + k}
                  label={`状态 ${k} 权重`}
                  hint={`默认 ${DEFAULT_STATE[k].toFixed(2)}；状态机关闭时该因子弃权`}
                  value={form.stateScales[k]}
                  onChange={(v) => setMap('stateScales', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_MACRO_WINDOW).map((k) => (
                <NumRow
                  key={'mw-' + k}
                  label={`宏观影响 ${k} 权重`}
                  hint={`默认 ${DEFAULT_MACRO_WINDOW[k].toFixed(2)}（high=影响最大期）`}
                  value={form.macroWindow[k]}
                  onChange={(v) => setMap('macroWindow', k, v)}
                />
              ))}

              <div style={groupStyle}>宏观事件前瞻（按交易日）</div>
              <NumRow
                label="事件前瞻交易日数"
                hint="事件前 N 个交易日起渐进收仓（默认 5，整数）"
                value={form.leadDays}
                min={1}
                max={20}
                step={1}
                precision={0}
                onChange={(v) => set({ leadDays: v })}
              />
              <div style={rowStyle}>
                <span style={labelStyle}>前瞻曲线（逗号分隔）</span>
                <input
                  value={form.leadCurve}
                  onChange={(e) => set({ leadCurve: e.target.value })}
                  style={{ minWidth: 300, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
                />
                <span style={hintStyle}>下标 0 = 距事件最远；个数必须等于前瞻交易日数（默认 0.90, 0.85, 0.80, 0.70, 0.50）</span>
              </div>
              <NumRow label="事件当日缩放" hint="T 日缩放，与曲线尾部相接（默认 0.30）" value={form.eventDay} max={2} step={0.05} onChange={(v) => set({ eventDay: v })} />

              <div style={groupStyle}>节前分级与大盘跌幅档</div>
              {Object.keys(DEFAULT_HOLIDAY).map((k) => (
                <NumRow
                  key={'hol-' + k}
                  label={`节前权重 ${k}`}
                  hint={`默认 ${DEFAULT_HOLIDAY[k].toFixed(2)}（small1=小长假前1日，long1=长假前1日）；日历未加载时因子弃权`}
                  value={form.holiday[k]}
                  onChange={(v) => setMap('holiday', k, v)}
                />
              ))}
              <SwitchRow
                label="长假前最后一日禁新开仓"
                hint="默认关；打开后长假前最后一个交易日禁止新开仓（硬拦）"
                checked={form.holidayBlockNew}
                onChange={(v) => set({ holidayBlockNew: v })}
              />
              {Object.keys(DEFAULT_INDEX).map((k) => (
                <NumRow
                  key={'idx-' + k}
                  label={`大盘档位权重 ${k}`}
                  hint={`默认 ${DEFAULT_INDEX[k].toFixed(2)}（-1=跌1%档，-2=跌2%档，deep=≤-2%深跌档）`}
                  value={form.indexScales[k]}
                  onChange={(v) => setMap('indexScales', k, v)}
                />
              ))}

              <div style={groupStyle}>利空与仓位上限</div>
              <NumRow label="LLM 利空缩放" hint="个股 LLM 利空时的买入乘子（默认 0.5）" value={form.llmBearish} max={2} step={0.05} onChange={(v) => set({ llmBearish: v })} />
              <NumRow label="总仓位上限" hint="账户总仓位比例上限（默认 0.80）；参与上限类取 min" value={form.maxPos} max={1} step={0.05} onChange={(v) => set({ maxPos: v })} />
              <NumRow label="权益回撤熔断比例" hint="回撤达此比例按 risk_overlay 缩仓（默认 0.10）" value={form.maxDD} max={1} step={0.01} onChange={(v) => set({ maxDD: v })} />

              <div style={groupStyle}>连续止损熔断与止损/止盈偏移</div>
              <NumRow label="连续止损熔断次数" hint="回看窗内止损达该次数即硬拦新开仓（默认 4，整数）" value={form.guardCount} min={1} max={100} step={1} precision={0} onChange={(v) => set({ guardCount: v })} />
              <NumRow label="连续止损回看窗" hint="交易日数（默认 1，整数）" value={form.guardWindow} min={1} max={20} step={1} precision={0} onChange={(v) => set({ guardWindow: v })} />
              <NumRow label="止损放宽系数" hint="大盘每跌 1% 止损下浮的 pt（默认 3.0）" value={form.slPerPct} max={50} step={0.1} onChange={(v) => set({ slPerPct: v })} />
              <NumRow label="止损放宽上限" hint="止损下浮硬顶 pt（默认 4.0）" value={form.slCap} max={50} step={0.1} onChange={(v) => set({ slCap: v })} />
              <NumRow label="止盈上浮系数" hint="大盘每跌 1% 止盈上浮的 pt（默认 3.0）" value={form.tpPerPct} max={50} step={0.1} onChange={(v) => set({ tpPerPct: v })} />
              <NumRow label="止盈上浮上限" hint="止盈上浮硬顶 pt（默认 5.0）" value={form.tpCap} max={50} step={0.1} onChange={(v) => set({ tpCap: v })} />
              <NumRow label="止损放宽触发跌幅" hint="大盘跌幅达到该值才放宽止损（默认 1.0%）" value={form.slMinDrop} max={20} step={0.1} onChange={(v) => set({ slMinDrop: v })} />
              <NumRow label="趋势破位止损收紧" hint="趋势下跌时止损收紧 pt（默认 1.0）" value={form.bearTighten} max={50} step={0.1} onChange={(v) => set({ bearTighten: v })} />
              <SwitchRow
                label="放宽止损同比例缩仓"
                hint="默认开（强制）：止损 6%→9% 时仓位 ×6/9，避免放宽止损放大单笔亏损"
                checked={form.riskBudgetKeep}
                onChange={(v) => set({ riskBudgetKeep: v })}
              />

              <div style={groupStyle}>技术过滤 / 风险池 / 概念库（系统级）</div>
              <NumRow
                label="KDJ J 上限"
                hint="0=关；J 高于该值不新开仓（目标 90）。先影子统计误杀率。"
                testId="tech-kdj-max"
                value={form.techKdjMax}
                min={0}
                max={200}
                step={1}
                precision={0}
                onChange={(v) => set({ techKdjMax: v })}
              />
              <div style={rowStyle}>
                <span style={labelStyle}>长均线过滤</span>
                <select
                  data-testid="tech-ma-filter"
                  value={form.techMa}
                  onChange={(e) => set({ techMa: e.target.value })}
                  style={{ minWidth: 200, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
                >
                  <option value="off">off（关闭）</option>
                  <option value="ma20">收盘低于 MA20 不买</option>
                  <option value="ma60">收盘低于 MA60 不买</option>
                  <option value="ma120_250">收盘低于 MA120/MA250 不买</option>
                </select>
                <span style={hintStyle}>日K不足时该条件弃权，不误拦</span>
              </div>
              <SwitchRow
                label="技术过滤影子模式"
                hint="默认开：只记录“本应拦截”，不改裁定"
                checked={form.techShadow}
                onChange={(v) => set({ techShadow: v })}
              />
              <SwitchRow
                label="风险池影子读数"
                hint="默认开：只记录跌停/炸板家数与 would-change，不改风险档"
                checked={form.riskPoolShadow}
                onChange={(v) => set({ riskPoolShadow: v })}
              />
              <SwitchRow
                label="概念库自动同步/敞口"
                hint="覆盖率达门槛且数据未过期后再开；敞口上限在执行层面板配置"
                checked={form.conceptEnabled}
                onChange={(v) => set({ conceptEnabled: v })}
              />
              <SwitchRow
                label="概念敞口影子模式"
                hint="默认开；先跑影子一周统计误杀率"
                checked={form.conceptShadow}
                onChange={(v) => set({ conceptShadow: v })}
              />
              <NumRow
                label="概念覆盖率门槛 %"
                hint="0=恢复默认 95%；低于该值概念敞口闸弃权"
                value={form.conceptCoverage}
                min={0}
                max={100}
                step={1}
                precision={0}
                onChange={(v) => set({ conceptCoverage: v })}
              />
              <NumRow
                label="概念数据最大陈旧 小时"
                hint="0=恢复默认 48h；超过该时长概念数据不可信"
                value={form.conceptStaleness}
                min={0}
                max={720}
                step={1}
                precision={0}
                onChange={(v) => set({ conceptStaleness: v })}
              />
            </>
          )}

          <div style={{ ...rowStyle, gap: 8 }}>
            <Button theme="primary" onClick={save} loading={saving} disabled={state !== 'loaded'}>保存风控配置</Button>
            <Button variant="outline" onClick={load} disabled={state === 'loading' || saving}>重载</Button>
          </div>
        </>
      )}
    </Card>
  )
}
