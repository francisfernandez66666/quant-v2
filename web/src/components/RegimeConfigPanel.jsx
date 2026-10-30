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
// §DYN-T（2026-10-26）：做 T（日内回转）的全部阈值键进入本面板（方案 §8.8 的 22 个键），
// 并且是**唯一**的页面入口。T 段比其他键多一条纪律——提交必须按键级稀疏：GET 回显走的是
// `WithTEffective()`（把出厂默认填成生效值，为的是让面板显示"引擎真在按多少跑"），
// 若把这份回显原样 POST 回去，今天的出厂默认就变成该账号的显式配置，日后调默认再也传不下去。
// 因此 T 段只提交与首屏基线不同的键（见 tEffectiveToPatch），其余 sizing 键仍走整块提交。
// English: the §DYN-T block is submitted key-by-key against the first-screen baseline, because the
// GET echo carries effective (default-filled) values and round-tripping them would pin today's
// defaults as this account's explicit config.
//
// English: admin panel for the signal-generation risk switches plus every fine-grained SizingConfig
// knob (collapsible). Load failure disables saving; numbers never silently fold to 0; writes are
// sparse merges of the keys owned here, and reads show the defaults-filled effective values.
import React, { useCallback, useEffect, useState } from 'react'
import { Button, Card, Input, InputNumber, Tag } from 'tdesign-react'
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
// §FLOW-FACTOR（2026-10-25）：净流入 dynamic 因子的出厂档位，与
// internal/config/sizing_flow.go::DefaultFlowFactorConfig 逐键对齐。改这里必须同步改后端，
// 否则页面显示的"默认"与引擎实际生效值会分叉。
// English: factory bands for the net-inflow factor — must stay byte-identical to
// DefaultFlowFactorConfig in the backend.
const DEFAULT_FLOW_B5 = { strong_in: 1.10, mild_in: 1.05, neutral: 1.00, mild_out: 0.85, strong_out: 0.70 }
const DEFAULT_FLOW_BT = { strong_in: 1.05, mild_in: 1.025, neutral: 1.00, mild_out: 0.95, strong_out: 0.90 }
const DEFAULT_FLOW_BANDS = { strong_in_pct: 3, mild_in_pct: 1, mild_out_pct: -1, strong_out_pct: -3 }
const DEFAULT_FLOW_SELL = { strong_out: 0.85, mild_out: 0.95, today_strong_out: 0.95, today_mild_out: 0.975 }
const DEFAULT_FLOW_SELL_BANDS = { strong_out_pct: -3, mild_out_pct: -1 }
// R9 弃权率告警阈值出厂值（%），与后端 config.DefaultFlowAbstainAlertPct 同值。
// English: R9 abstain-alert threshold default, mirroring the backend constant.
const DEFAULT_FLOW_ABSTAIN_PCT = 50
// 档位键 → 中文标签（页面与 tooltip 共用，防拼写漂移）。
// English: band key → Chinese label, shared by the rows and their hints.
const FLOW_BAND_LABEL = { strong_in: '强流入', mild_in: '中流入', neutral: '中性', mild_out: '中流出', strong_out: '强流出' }
const FLOW_SELL_LABEL = {
  strong_out: '5日强流出',
  mild_out: '5日中流出',
  today_strong_out: '当日强流出',
  today_mild_out: '当日中流出',
  strong_out_pct: '5日强流出',
  mild_out_pct: '5日中流出',
}

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
  // §DYN-SIZE v4：`max_total_position_pct` 的语义从"逐笔缩放乘子"改成"目标仓位天花板"，
  // 出厂值随之从 0.80 抬到 1.00（旧值作为逐笔乘子在半仓时恒等于 1.0、永不约束，正是"一笔打满"
  // 的根因之一）。这里的兜底值必须与后端 DefaultSizingConfig 同值，否则面板显示一个数、引擎跑另一个数。
  maxPos: 1.00,
  perTrade: 0.10,
  perTradeMax: 0.30,
  // §T-CLUSTER：相关性簇帽（%，0=关）与相关性阈值。与 internal/config 的
  // defaultClusterCapPct / ClusterRhoOf 同源，改这里必须同步改后端。
  clusterCap: 40,
  clusterRho: 0.70,
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

// signedNum 允许负数/0 的数值读取。
// 为什么不能复用 num()：num() 的 `n > 0` 判据把 0 与负数一律当"没写"回落默认——对"负数=关闭"
// 这类语义（R9 告警阈值）会把用户的显式关闭静默改回默认开，是 §FLOW-FACTOR 档位阈值同族的坑。
// English: reads signed numbers; num()'s `n > 0` gate would silently turn an explicit "negative =
// disabled" back into the default.
function signedNum(v, fallback) {
  const n = Number(v)
  return Number.isFinite(n) ? n : fallback
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

// mapSigned 与 mapNums 同构，但**允许负数与 0**。
//
// 净流入率档位阈值天生带负号（strong_out_pct = -3 表示"流出 3% 以上"），用 mapNums 的
// `n > 0` 判据会把负数当成"缺失"而静默回落默认值——管理员把 -3 改成 -5、保存、再读回来
// 又变回 -3，配置被悄悄丢掉且毫无提示。凡是带符号的档位一律走本函数。
// English: like mapNums but accepts negatives/zero — band thresholds are inherently signed,
// and the `n > 0` test would silently drop them (user edits -3 → -5, saves, reads back -3).
function mapSigned(src, defaults) {
  const out = { ...defaults }
  if (src && typeof src === 'object') {
    for (const k of Object.keys(defaults)) {
      const n = Number(src[k])
      if (Number.isFinite(n)) out[k] = n
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
function NumRow({ label, hint, value, min = 0.01, max = 2, step = 0.01, precision, onChange, testId, disabled }) {
  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <InputNumber
        data-testid={testId}
        min={min}
        max={max}
        step={step}
        precision={precision}
        // §W-06-R4：兼容键那一行必须真的禁用。只写 <NumRow disabled> 而不往下传，
        // 屏上看起来是灰的（靠 label 文案），实际还能改——正是"改了没反应"那个坑。
        disabled={!!disabled}
        value={value}
        onChange={(v) => onChange(toNum(v))}
      />
      {hint && <span style={hintStyle}>{hint}</span>}
    </div>
  )
}

// StrRow 文本参数行（HHMM 时刻这类"必须逐字符核对"的值）。
// 为什么不用 InputNumber：1450 写进数字框会被显示成 1450/1,450 之类，且 0945 这种
// 前导零时刻一旦丢掉零就变成 945，后端按"4 位 HHMM"判格式直接回落默认——
// 用户看到的效果是"我明明配了 9:45，程序却在按 14:00 跑"。文本框原样传字符串最诚实。
// English: plain text for HHMM values — a numeric box drops leading zeros (0945→945) and the
// backend then silently falls back to the default.
function StrRow({ label, hint, value, onChange, testId }) {
  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <Input data-testid={testId} value={value} maxLength={4} placeholder="HHMM"
        onChange={(v) => onChange(String(v ?? '').trim())} style={{ width: 110 }} />
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

// tEffectiveToPatch 生成 §DYN-T 的**稀疏提交补丁**：只带与首屏基线不同的键。
//
// 为什么必须稀疏：GET 回显的是 WithTEffective 填好的生效值（含出厂默认），把它原样 POST 回去
// 等于把今天的出厂默认逐键写成这个账号的显式配置——日后调默认（比如把 min_spread 从 2.0 提到 2.5）
// 对这个账号永远不生效，正是 §1.7 里 `mode`/`max_total_position_pct` 被物化坑过的那一次。
// 参数 base 是首屏归一化后的表单值（键名与 form 同为 camelCase），value 按 wire 键名输出。
// 数字比较统一走 Number + 有限性判断：`0` 与 `false` 在这里都是合法配置，不能用 `||` 短路掉。
// English: emits only keys the operator actually changed (compared against the first-screen form
// values); round-tripping the effective echo would pin today's factory defaults as this account's
// explicit config — the §1.7 materialization trap.
function tEffectiveToPatch(base, form) {
  // [wire 键名, 表单字段名, 取值器]
  const spec = [
    ['enabled', 'tOn', (f) => !!f.tOn],
    ['mode', 'tMode', (f) => f.tMode],
    ['max_rounds_per_code_per_day', 'tMaxRounds', (f) => f.tMaxRounds],
    ['intraday_cap_pct', 'tCapPct', (f) => f.tCapPct],
    ['min_spread_pct', 'tMinSpread', (f) => f.tMinSpread],
    ['force_unwind_at', 'tForceUnwind', (f) => f.tForceUnwind],
    ['last_open_at', 'tLastOpen', (f) => f.tLastOpen],
    ['vwap_dev_pct', 'tVwapDev', (f) => f.tVwapDev],
    ['min_amplitude_pct', 'tMinAmp', (f) => f.tMinAmp],
    ['near_extreme_pct', 'tNearExtreme', (f) => f.tNearExtreme],
    ['limit_distance_pct', 'tLimitDist', (f) => f.tLimitDist],
    ['open_cooling_min', 'tCooling', (f) => f.tCooling],
    ['min_market_scale', 'tMinMS', (f) => f.tMinMS],
    ['sell_env_min_market_scale', 'tSellEnvMS', (f) => f.tSellEnvMS],
    ['market_plunge_pct', 'tPlunge', (f) => f.tPlunge],
    ['intraday_sell_cap_total_pct', 'tSellCapTotal', (f) => f.tSellCapTotal],
    ['stop_guard_pct', 'tStopGuard', (f) => f.tStopGuard],
    ['loss_breaker_pct', 'tLossBreaker', (f) => f.tLossBreaker],
    // §W-06-R4 / §W-09 三个新键（round_stop_pct / vol_ratio_max / regime_gate_enabled）。
    ['round_stop_pct', 'tRoundStop', (f) => f.tRoundStop],
    ['vol_ratio_max', 'tVolRatioMax', (f) => f.tVolRatioMax],
    ['regime_gate_enabled', 'tRegimeGate', (f) => !!f.tRegimeGate],
    ['min_amount_wan', 'tMinAmount', (f) => f.tMinAmount],
    ['min_turnover_pct', 'tMinTurnover', (f) => f.tMinTurnover],
    ['min_listed_days', 'tMinListed', (f) => f.tMinListed],
    ['vwap_check_tol_pct', 'tVwapTol', (f) => f.tVwapTol],
  ]
  const same = (a, b) => {
    if (typeof a === 'boolean' || typeof b === 'boolean') return !!a === !!b
    const na = Number(a), nb = Number(b)
    if (Number.isFinite(na) && Number.isFinite(nb)) return na === nb
    return String(a) === String(b)
  }
  const out = {}
  for (const [wire, field, read] of spec) {
    const next = read(form)
    if (base[field] === undefined || !same(base[field], next)) out[wire] = next
  }
  return out
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
  // §DYN-T 稀疏提交基线：GET 回显的 T 段生效值（含出厂默认）。补丁只带与它不同的键。
  // English: the effective T echo — the patch carries only keys that differ from it.
  const [tBaseline, setTBaseline] = useState({})

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
        // §DYN-SIZE v4 新增：单笔基准仓位（占总资产比例）。缺键时后端补 0.10，这里同口径。
        perTrade: num(sz.per_trade_position_pct, DEFAULTS.perTrade),
        perTradeMax: num(sz.per_trade_position_max_pct, DEFAULTS.perTradeMax),
        // §T-CLUSTER：cluster_cap_pct 是**三态**（缺键=出厂 40；显式 0=关闭），
        // 所以必须用 signedNum——num() 的 n>0 判据会把"显式关闭"读回 40，
        // 等于每次打开面板都把 owner 关掉的簇帽又打开。
        clusterCap: signedNum(sz.cluster_cap_pct, DEFAULTS.clusterCap),
        clusterRho: num(sz.cluster_rho, DEFAULTS.clusterRho),
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
        // ── §FLOW-FACTOR 净流入因子（2026-10-25） ──
        // 开关与后端同口径：键缺失 = 没写过 = 按**开**跑（nil=开），所以判据是 `!== false`
        // 而不是 `=== true`——写成后者会把存量账号显示成"关"，引擎却在按开算。
        // English: switches follow the backend's nil=on semantics (`!== false`, not `=== true`).
        flowOn: ((sz.flow_factor || {}).enabled) !== false,
        flowSellOn: ((sz.flow_factor || {}).sell_enabled) !== false,
        flowB5: mapNums((sz.flow_factor || {}).buy_5d_scales, DEFAULT_FLOW_B5),
        flowB5Bands: mapSigned((sz.flow_factor || {}).buy_5d_bands, DEFAULT_FLOW_BANDS),
        flowBT: mapNums((sz.flow_factor || {}).buy_today_scales, DEFAULT_FLOW_BT),
        flowBTBands: mapSigned((sz.flow_factor || {}).buy_today_bands, DEFAULT_FLOW_BANDS),
        flowSell: mapNums((sz.flow_factor || {}).sell_scales, DEFAULT_FLOW_SELL),
        flowSellBands: mapSigned((sz.flow_factor || {}).sell_bands, DEFAULT_FLOW_SELL_BANDS),
        // R9 弃权率告警阈值（%）：0/缺键=后端取出厂 50；负数=关闭告警（只留每轮诊断行）。
        // 用 mapSigned 的口径读——负数是合法值，不能被 num() 的 n>0 判据吞掉。
        // English: R9 abstain-alert threshold; negative is legal (disables), so read it signed.
        flowAbstainPct: signedNum((sz.flow_factor || {}).abstain_alert_pct, DEFAULT_FLOW_ABSTAIN_PCT),
        // §SIZING-EVO（2026-10-25）：自进化回路开关。出厂 **nil=关**（与因子默认开相反：
        // 进化是新增常驻行为，必须显式开），故这里判据是 `=== true`。
        // English: evolution switch defaults OFF (nil=off), unlike the factors — hence `=== true`.
        evoOn: ((sz.evo || {}).enabled) === true,
        evoAutoPromote: ((sz.evo || {}).auto_promote) === true,
        // ── §DYN-T 日内回转（做 T）全部阈值键（方案 §8.8） ──
        // 读的是 GET 回显里的**生效值**（后端 WithTEffective 已把出厂默认填进来），
        // 提交走稀疏口径：只有与回显不同的键才进 body——把回显原样 POST 回来会把今天的
        // 出厂默认物化成该账号的显式配置，日后改默认再也传不下去（§1.7 的物化陷阱）。
        // English: the GET echoes effective values; the POST is sparse, so untouched keys never
        // get materialized into this account and later default changes still propagate.
        tOn: (sz.t || {}).enabled !== false,
        tMode: String((sz.t || {}).mode || 'both').toLowerCase(),
        tMaxRounds: intNum((sz.t || {}).max_rounds_per_code_per_day, 1),
        tCapPct: num((sz.t || {}).intraday_cap_pct, 50),
        tMinSpread: num((sz.t || {}).min_spread_pct, 2.0),
        tForceUnwind: String((sz.t || {}).force_unwind_at || '1450'),
        tLastOpen: String((sz.t || {}).last_open_at || '1400'),
        tVwapDev: num((sz.t || {}).vwap_dev_pct, 0.8),
        tMinAmp: num((sz.t || {}).min_amplitude_pct, 2.5),
        tNearExtreme: num((sz.t || {}).near_extreme_pct, 0.3),
        tLimitDist: num((sz.t || {}).limit_distance_pct, 1.0),
        tCooling: num((sz.t || {}).open_cooling_min, 15),
        tMinMS: signedNum((sz.t || {}).min_market_scale, 0.40),
        tSellEnvMS: signedNum((sz.t || {}).sell_env_min_market_scale, 0.20),
        tPlunge: signedNum((sz.t || {}).market_plunge_pct, -2.0),
        tSellCapTotal: num((sz.t || {}).intraday_sell_cap_total_pct, 20),
        tStopGuard: num((sz.t || {}).stop_guard_pct, 1.5),
        tLossBreaker: num((sz.t || {}).loss_breaker_pct, 3.0),
        // §W-06-R4：本笔 T 止损（价差率%）。默认 2.0 的理由是**赔率**——min_spread_pct 也是 2.0，
        // 一笔好 T 赚 2.0、一笔坏 T 亏 2.5 的赔率是 1:1.25，打平需胜率 55.6%（长期负期望）。
        tRoundStop: num((sz.t || {}).round_stop_pct, 2.0),
        // §W-09：放量门。0 合法＝不设该条（后端 TVolRatioMaxOf 专门留了这个语义）。
        tVolRatioMax: num((sz.t || {}).vol_ratio_max, 1.8),
        // §W-09：T 行情门总开关，默认**开**（nil⇒true，后端用 *bool 表达"没配"与"配了 false"）。
        tRegimeGate: (sz.t || {}).regime_gate_enabled !== false,
        tMinAmount: signedNum((sz.t || {}).min_amount_wan, 3000),
        tMinTurnover: signedNum((sz.t || {}).min_turnover_pct, 1.5),
        tMinListed: signedNum((sz.t || {}).min_listed_days, 60),
        tVwapTol: num((sz.t || {}).vwap_check_tol_pct, 0.1),
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
      // 稀疏提交基线＝**首屏归一化后的表单值**（不是 raw cfg.sizing.t）：
      // 后端回显已经把出厂默认填进 T 段，raw 与归一值本来等价，但用 raw 时"键在回显里缺失"
      // 会被判成"改动"并把出厂默认写进账号——那正是 §1.7 的物化陷阱。用首屏表单值当基线，
      // owner 没碰过的键永远不进补丁，改过又改回来的键也不进（值相同＝没改）。
      setTBaseline(next)
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
    ratio('单笔基准仓位', f.perTrade)
    ratio('单笔上限', f.perTradeMax)
    // §B0：区间反了（上限 < 下限）会让后端把它们收敛到同值，等于静默改了 owner 的配置，
    // 所以在面板就拦下——这是资金参数，不接受"保存后才发现生效值不是填的值"。
    if (Number(f.perTradeMax) < Number(f.perTrade)) bad.push('单笔上限(必须 ≥ 单笔基准)')
    // §T-CLUSTER：簇帽 0=关，>0 时按百分比；相关性阈值必须落在 (0,1]——
    // ρ=0 会把每只票都判成"与谁都不同簇"，簇帽直接失效且毫无提示。
    const ccap = Number(f.clusterCap)
    if (!Number.isFinite(ccap) || ccap < 0 || ccap > 100) bad.push('相关性簇帽(0=关，0~100)')
    const crho = Number(f.clusterRho)
    if (!Number.isFinite(crho) || crho <= 0 || crho > 1) bad.push('相关性阈值(0~1)')
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
    // §FLOW-FACTOR：档位阈值必须**严格递减**（strong_in > mild_in > mild_out > strong_out）。
    // 顺序写反的后果不是报错而是静默错档：flowBandOf 从强到弱逐个比，写反了永远命中第一档，
    // 于是"净流出 5%"和"净流入 5%"拿到同一个乘子——必须在保存前拦住。
    // English: band thresholds must strictly decrease; a reversed pair silently collapses every
    // reading into the first matching band, so it is rejected before saving.
    const bandOK = (m, label) => {
      const v = ['strong_in_pct', 'mild_in_pct', 'mild_out_pct', 'strong_out_pct'].map((k) => Number(m[k]))
      if (v.some((x) => !Number.isFinite(x))) return bad.push(`${label}档位阈值`)
      if (!(v[0] > v[1] && v[1] > v[2] && v[2] > v[3])) bad.push(`${label}档位阈值（须严格递减）`)
      return undefined
    }
    bandOK(f.flowB5Bands, '5日净流入')
    bandOK(f.flowBTBands, '当日净流入')
    const sb = Number(f.flowSellBands.strong_out_pct)
    const mb = Number(f.flowSellBands.mild_out_pct)
    if (!Number.isFinite(sb) || !Number.isFinite(mb) || !(sb < mb)) bad.push('卖出净流出档位阈值（须 strong_out < mild_out）')
    // 卖出权重是"确认窗时长乘子"，>1 会**延长**确认窗（与净流出加速离场的初衷相反），故限 (0,1]。
    // English: sell weights shorten the confirm window, so >1 is nonsense — clamp to (0,1].
    for (const [k, v] of Object.entries(f.flowSell)) {
      const n = Number(v)
      if (!Number.isFinite(n) || n <= 0 || n > 1) bad.push(`卖出权重 ${k}`)
    }
    for (const [k, v] of [...Object.entries(f.flowB5), ...Object.entries(f.flowBT)]) {
      const n = Number(v)
      if (!Number.isFinite(n) || n < 0 || n > 3) bad.push(`净流入档位乘子 ${k}`)
    }
    // R9 阈值：>100 无意义（弃权率不可能超过 100%）；负数合法（=关闭），只挡非数与非有限值。
    const ap = Number(f.flowAbstainPct)
    if (!Number.isFinite(ap) || ap > 100) bad.push('弃权率告警阈值（须 ≤100，负数=关闭）')

    // ── §DYN-T 做 T 阈值：区间与后端 tClamp 逐键对齐（方案 §8.8）──
    // 后端对越界值是"忽略并回落默认 + TFallbacks 上报"，前端在提交前就挡住：
    // 让 owner 看到"这一项没进去"比让他以为配好了却在每轮被悄悄回落更诚实。
    const tPos = (name, v, lo, hi) => {
      const n = Number(v)
      if (!Number.isFinite(n) || n < lo || n > hi) bad.push(`${name}（须 ${lo}~${hi}）`)
    }
    const tHHMM = (name, v) => {
      const s = String(v || '').trim()
      if (!/^\d{4}$/.test(s)) { bad.push(`${name}（须为 4 位 HHMM）`); return }
      const hh = Number(s.slice(0, 2)), mm = Number(s.slice(2))
      if (hh < 0 || hh > 23 || mm < 0 || mm > 59) bad.push(`${name}（HHMM 非法）`)
    }
    tPos('单码单日回转上限', f.tMaxRounds, 1, 5)
    tPos('机动仓上限%', f.tCapPct, 5, 100)
    tPos('最小价差%', f.tMinSpread, 0.2, 10)
    tPos('现价偏离分时均线%', f.tVwapDev, 0.3, 5)
    tPos('日内振幅下限%', f.tMinAmp, 1, 10)
    tPos('近极值带%', f.tNearExtreme, 0.1, 2)
    tPos('距涨跌停距离%', f.tLimitDist, 0.5, 5)
    tPos('开盘冷静期(分钟)', f.tCooling, 10, 60)
    tPos('正T环境门 MS', f.tMinMS, 0, 1)
    tPos('反T环境门 MS', f.tSellEnvMS, 0, 1)
    tPos('当日反T卖腿市值帽%', f.tSellCapTotal, 5, 50)
    tPos('反T否决门距离%', f.tStopGuard, 0.5, 5)
    tPos('买回熔断阈值%', f.tLossBreaker, 1, 10)
    tPos('本笔T止损%', f.tRoundStop, 0.5, 5)
    // 量比门允许 0（＝不设该条），所以不走 tPos 的正数假设。
    if (Number(f.tVolRatioMax) !== 0) tPos('放量门量比', f.tVolRatioMax, 1, 6)
    tPos('分时均线校验容差%', f.tVwapTol, 0.05, 1)
    // 三个"0 合法（＝不设该条）"的流动性门与大盘急跌停门用允许 0/负值的判据，不走 tPos 的正数假设。
    tPos('流动性成交额下限(万)', f.tMinAmount, 0, 1000000)
    tPos('流动性换手率下限%', f.tMinTurnover, 0, 25)
    tPos('流动性上市天数下限', f.tMinListed, 0, 3650)
    const pl = Number(f.tPlunge)
    if (!Number.isFinite(pl) || pl > 0 || pl < -10) bad.push('大盘急跌停阈值（须为 -10~0，0=不设门）')
    tHHMM('尾盘强制配平时刻', f.tForceUnwind)
    tHHMM('最后开新腿时刻', f.tLastOpen)
    // 尾盘必须晚于最后开腿，否则会出现"刚开腿就被强制配平"的一腿过夜窗口。
    if (!(Number(f.tLastOpen) < Number(f.tForceUnwind))) bad.push('最后开新腿时刻须早于尾盘强制配平时刻')
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
          // §DYN-SIZE v4：单笔基准仓位（旧模型没有这个数，中性 1.0 时单笔可用满权益）。
          per_trade_position_pct: Number(form.perTrade),
          per_trade_position_max_pct: Number(form.perTradeMax),
          cluster_cap_pct: Number(form.clusterCap),
          cluster_rho: Number(form.clusterRho),
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
          // §FLOW-FACTOR（2026-10-25）：整块提交（后端 WithDefaults 会补齐缺键）。
          // 档位阈值用 mapSigned 读回来的值原样回写，负数不丢。
          // English: submit the whole flow_factor block; signed band thresholds round-trip as-is.
          flow_factor: {
            enabled: !!form.flowOn,
            sell_enabled: !!form.flowSellOn,
            ratio_basis: 'float_mcap',
            buy_5d_scales: { ...form.flowB5 },
            buy_5d_bands: { ...form.flowB5Bands },
            buy_today_scales: { ...form.flowBT },
            buy_today_bands: { ...form.flowBTBands },
            sell_scales: { ...form.flowSell },
            sell_bands: { ...form.flowSellBands },
            // R9：弃权率告警阈值（%）。负数=关闭告警，原样提交，不在前端"纠正"成 0/默认。
            abstain_alert_pct: Number(form.flowAbstainPct),
          },
          // §SIZING-EVO：只提交开关两键，其余（search_bounds/schedule/min_samples）留配置层，
          // 避免页面一次保存把围栏覆盖掉。
          // English: only the two switches; search bounds and cadence stay config-file owned.
          evo: { enabled: !!form.evoOn, auto_promote: !!form.evoAutoPromote },
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
      // §DYN-T 稀疏补丁：只把 owner 真改过的 T 键写进 sizing.t（空对象就整段不发，
      // 后端 mergeStructPatch 对缺席的键保持原值，出厂默认也因此还能继续传下去）。
      const tPatch = tEffectiveToPatch(tBaseline, form)
      if (Object.keys(tPatch).length > 0) body.sizing.t = tPatch
      await api.setRegimeConfig(body, accountId || undefined)
      setBaseline(JSON.stringify(form))
      setTBaseline(form) // 补丁已生效，基线随之推进到当前表单（否则下次保存会把同一批键再发一遍）
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

              <div style={groupStyle}>净流入因子（§FLOW-FACTOR，2026-10-25）</div>
              <SwitchRow
                label="买入侧净流入因子"
                hint="默认开。关掉等于两个买入因子整体弃权（与「没有这一维」逐字一致，不当中性）"
                checked={form.flowOn}
                onChange={(v) => set({ flowOn: v })}
              />
              <SwitchRow
                label="卖出侧净流出加权"
                hint="默认开。净流出只缩短卖出确认窗时长（不缩量），可与 L1/L2/L3 档位叠加"
                checked={form.flowSellOn}
                onChange={(v) => set({ flowSellOn: v })}
              />
              {Object.keys(DEFAULT_FLOW_B5).map((k) => (
                <NumRow
                  key={'f5-' + k}
                  label={`5日净流入 ${FLOW_BAND_LABEL[k]}`}
                  hint={`比值=近5日主力净流入/流通市值；默认 ${DEFAULT_FLOW_B5[k]}`}
                  value={form.flowB5[k]}
                  min={0}
                  max={3}
                  onChange={(v) => setMap('flowB5', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_FLOW_BANDS).map((k) => (
                <NumRow
                  key={'f5b-' + k}
                  label={`5日档位阈值 ${FLOW_BAND_LABEL[k.replace('_pct', '')]}`}
                  hint={`单位 %（可负）；默认 ${DEFAULT_FLOW_BANDS[k]}，须严格递减`}
                  value={form.flowB5Bands[k]}
                  min={-100}
                  max={100}
                  step={0.5}
                  onChange={(v) => setMap('flowB5Bands', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_FLOW_BT).map((k) => (
                <NumRow
                  key={'ft-' + k}
                  label={`当日净流入 ${FLOW_BAND_LABEL[k]}`}
                  hint={`实时值，档位幅度为 5 日的一半（进行中值噪音大）；默认 ${DEFAULT_FLOW_BT[k]}`}
                  value={form.flowBT[k]}
                  min={0}
                  max={3}
                  onChange={(v) => setMap('flowBT', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_FLOW_BANDS).map((k) => (
                <NumRow
                  key={'ftb-' + k}
                  label={`当日档位阈值 ${FLOW_BAND_LABEL[k.replace('_pct', '')]}`}
                  hint={`单位 %（可负）；默认 ${DEFAULT_FLOW_BANDS[k]}，须严格递减`}
                  value={form.flowBTBands[k]}
                  min={-100}
                  max={100}
                  step={0.5}
                  onChange={(v) => setMap('flowBTBands', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_FLOW_SELL).map((k) => (
                <NumRow
                  key={'fs-' + k}
                  label={`卖出加权 ${FLOW_SELL_LABEL[k]}`}
                  hint={`确认窗时长乘子（0,1]，<1 表示提前放行；默认 ${DEFAULT_FLOW_SELL[k]}`}
                  value={form.flowSell[k]}
                  min={0.01}
                  max={1}
                  step={0.005}
                  onChange={(v) => setMap('flowSell', k, v)}
                />
              ))}
              {Object.keys(DEFAULT_FLOW_SELL_BANDS).map((k) => (
                <NumRow
                  key={'fsb-' + k}
                  label={`卖出触发阈值 ${FLOW_SELL_LABEL[k.replace('_pct', '')]}`}
                  hint={`单位 %（须为负）；默认 ${DEFAULT_FLOW_SELL_BANDS[k]}`}
                  value={form.flowSellBands[k]}
                  min={-100}
                  max={0}
                  step={0.5}
                  onChange={(v) => setMap('flowSellBands', k, v)}
                />
              ))}
              <NumRow
                label="弃权率告警阈值（%）"
                hint="单轮买入信号里某一维拿不到数的占比达到该值即写 opslog 告警（同一原因 30 分钟一条）；默认 50；填负数=关闭告警"
                value={form.flowAbstainPct}
                min={-100}
                max={100}
                step={5}
                onChange={(v) => set({ flowAbstainPct: v })}
              />

              <div style={groupStyle}>自进化回路（§SIZING-EVO，出厂关）</div>
              <SwitchRow
                label="启用仓位进化引擎"
                hint="默认关。开启后周度跑 L1 寻优、夜间跑 L2 灰度/降级；关掉即冻结回默认配置（零行为变更）"
                checked={form.evoOn}
                onChange={(v) => set({ evoOn: v })}
              />
              <SwitchRow
                label="达标候选自动晋升"
                hint="默认关。关=只产候选+留痕，需人工在「自动研究 → 仓位进化」审阅后放行"
                checked={form.evoAutoPromote}
                onChange={(v) => set({ evoAutoPromote: v })}
              />

              <div style={groupStyle}>日内回转（§DYN-T 做 T，出厂开；阈值全部可配）</div>
              <SwitchRow
                label="做 T 总开关"
                hint="默认开（2026-10-26 六轮裁决：实盘与模拟盘同时生效）。关掉＝两本账当轮都不开腿，是一键止血位"
                checked={form.tOn}
                onChange={(v) => set({ tOn: v })}
              />
              <div style={rowStyle}>
                <span style={labelStyle}>允许方向</span>
                <select
                  data-testid="t-mode"
                  value={form.tMode}
                  onChange={(e) => set({ tMode: e.target.value })}
                  style={{ minWidth: 220, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
                >
                  <option value="both">both 正反都做（默认）</option>
                  <option value="buy_first">buy_first 只做正 T（先买后卖）</option>
                  <option value="sell_first">sell_first 只做反 T（先卖后买）</option>
                </select>
                <span style={hintStyle}>认不出的取值按"都不做"处理（资金路径宁可不做）</span>
              </div>
              <NumRow label="单码单日回转上限" hint="默认 1（灰度期一票一次）；硬顶 5。一次＝两腿都成交" value={form.tMaxRounds} min={1} max={5} step={1} precision={0} onChange={(v) => set({ tMaxRounds: v })} />
              <NumRow label="机动仓上限（占持仓%）" hint="默认 50（六轮把 30 提到 50）" value={form.tCapPct} min={5} max={100} step={1} onChange={(v) => set({ tCapPct: v })} />
              <NumRow label="最小价差（%）" hint="默认 2.0：第二腿要有利的变动达到该幅度才配对（两方向共用）" value={form.tMinSpread} min={0.2} max={10} step={0.1} onChange={(v) => set({ tMinSpread: v })} />
              <StrRow label="尾盘强制配平时刻" hint="默认 1450。正 T 买腿到点无条件卖出等量底仓（敞口过夜要许可，减敞口不要）" value={form.tForceUnwind} onChange={(v) => set({ tForceUnwind: v })} />
              <StrRow label="最后开新腿时刻" hint="默认 1400；晚于 1445 一律钳到 1445。必须早于尾盘配平时刻" value={form.tLastOpen} onChange={(v) => set({ tLastOpen: v })} />
              <NumRow label="现价偏离分时均线（%）" hint="默认 0.8：发起点门——正 T 要跌到均线下方、反 T 要站上均线上方达到该幅度" value={form.tVwapDev} min={0.3} max={5} step={0.1} onChange={(v) => set({ tVwapDev: v })} />
              <NumRow label="日内振幅下限（%）" hint="默认 2.5：振幅不够就没有配对空间，宁可不开腿" value={form.tMinAmp} min={1} max={10} step={0.1} onChange={(v) => set({ tMinAmp: v })} />
              <NumRow label="近极值带（%）" hint={`默认 0.3：贴着日内高/低点不开腿（下限 0.1，防「贴边也敢开」）`} value={form.tNearExtreme} min={0.1} max={2} step={0.05} onChange={(v) => set({ tNearExtreme: v })} />
              <NumRow label="距涨/跌停最小距离（%）" hint="默认 1.0：太靠近涨跌停成交不确定，两方向都不开" value={form.tLimitDist} min={0.5} max={5} step={0.1} onChange={(v) => set({ tLimitDist: v })} />
              <NumRow label="开盘冷静期（分钟）" hint="默认 15：早盘样本太少，日内口径（均线/振幅/极值）这时不可信" value={form.tCooling} min={10} max={60} step={1} onChange={(v) => set({ tCooling: v })} />
              <NumRow label="正 T 环境门 MS" hint="默认 0.40：市场组缩放低于此值不开买腿；填 0＝不设门" value={form.tMinMS} min={0} max={1} step={0.05} onChange={(v) => set({ tMinMS: v })} />
              <NumRow label="反 T 环境门 MS" hint="默认 0.20（六轮：极端弱势就是不做 T）。高于正 T 门时后端按两者较小值生效" value={form.tSellEnvMS} min={0} max={1} step={0.05} onChange={(v) => set({ tSellEnvMS: v })} />
              <NumRow label="大盘急跌停正 T（%）" hint="默认 -2.0：大盘跌到该幅度当天不开买腿；填 0＝不设该门" value={form.tPlunge} min={-10} max={0} step={0.1} onChange={(v) => set({ tPlunge: v })} />
              <NumRow label="当日反 T 卖腿市值帽（%）" hint="默认 20，分母是 min(总资产, 持仓市值)（五轮 T5-2 改的口径）" value={form.tSellCapTotal} min={5} max={50} step={1} onChange={(v) => set({ tSellCapTotal: v })} />
              <NumRow label="反 T 否决门距离（%）" hint="默认 1.5：距止盈/止损线小于该幅度就不做高抛（让位给信号执行）" value={form.tStopGuard} min={0.5} max={5} step={0.1} onChange={(v) => set({ tStopGuard: v })} />
              {/* §W-06-R4：旧键已并入「本笔 T 止损」，判定侧不再读它。
                  输入框 disabled + hint 写明"已并入"——不给 disable 就是留一个"改了没反应"的坑，
                  那比直接删掉更糟（用户以为自己在调止损）。 */}
              <NumRow disabled label="买回熔断阈值（%）——已并入「本笔 T 止损」，此键仅兼容历史配置"
                hint="默认 3.0（历史值）。2026-10-29 起判定侧统一读 round_stop_pct（2.0，价差率口径）；本键保留只为老配置反序列化不丢字段，改它不再影响任何判定"
                value={form.tLossBreaker} min={1} max={10} step={0.1} onChange={(v) => set({ tLossBreaker: v })} />
              <NumRow label="本笔 T 止损（价差率%，不含费用）"
                hint="默认 2.0：这一笔的价差率跌破 −2.0% 就跳过价差门立即配平（尾盘买回熔断也读它）。口径是**纯价差**，真实到手还要再减约 0.05~0.1% 的费用；定 2.0 而不是 2.5 是为了赔率——min_spread_pct 也是 2.0，赚 2.0 亏 2.5 的赔率要 55.6% 胜率才打平"
                value={form.tRoundStop} min={0.5} max={5} step={0.1} onChange={(v) => set({ tRoundStop: v })} />
              <NumRow label="放量门：折算量比上限"
                hint="默认 1.8（本仓既有分档的「显著放量」线；1.2 会把温和放量也拦掉，盘整市里大半时间开不了腿）。填 0＝不设该条。量比按已流逝交易分钟折算全天，早盘也能测出放量"
                value={form.tVolRatioMax} min={0} max={6} step={0.1} onChange={(v) => set({ tVolRatioMax: v })} />
              {/* §W-09：开关必须带标签。原先写的是 <ToggleSw label=…>，而 ToggleSw 不接 label，
                  屏上只剩下面那段说明——用户看不出这个开关管的是哪一件事，也没法用标签把它找出来。 */}
              <SwitchRow label="T 行情门（牛/熊关闭做 T）"
                hint="默认**开**：只认盘整（range）行情，牛/熊一律不开新腿，已开的腿立即配平回到开腿前。它与「市场状态机」那个全局开关是两件事——后者一开会让全账户仓位乘 state_scales（震荡 ×0.70、熊 ×0.40），所以 T 用自己的开关，不动那个。读数超过 15 分钟没更新时本门**弃权**（不会静默把 T 关掉）"
                checked={!!form.tRegimeGate}
                onChange={(v) => set({ tRegimeGate: !!v })} />
              <NumRow label="流动性：成交额下限（万元）" hint={`默认 3000；填 0＝不设该条。读数拿不到按「不过」处理`} value={form.tMinAmount} min={0} max={1000000} step={100} precision={0} onChange={(v) => set({ tMinAmount: v })} />
              <NumRow label="流动性：换手率下限（%）" hint="默认 1.5；填 0＝不设该条" value={form.tMinTurnover} min={0} max={25} step={0.1} onChange={(v) => set({ tMinTurnover: v })} />
              <NumRow label="流动性：上市天数下限" hint="默认 60；填 0＝不设该条（次新日内波动不具可比性）" value={form.tMinListed} min={0} max={3650} step={1} precision={0} onChange={(v) => set({ tMinListed: v })} />
              <NumRow label="分时均线校验容差（%）" hint="默认 0.1：主源与分钟链复算差过该值即视为读数不可信，本轮不开腿" value={form.tVwapTol} min={0.05} max={1} step={0.05} onChange={(v) => set({ tVwapTol: v })} />
              <div style={hintStyle}>
                越界值后端不会报错，而是**忽略该键并回落出厂值**，同时经 TFallbacks 逐轮上报——
                这里的区间与后端 tClamp 一一对应，提交前先挡住，避免"配了但没生效"。
              </div>

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
              <NumRow label="总仓位上限" hint="目标仓位天花板（默认 1.00，v4 起不再逐笔相乘）：目标仓位＝基准×min(市场缩放, 本值)" value={form.maxPos} max={1} step={0.05} onChange={(v) => set({ maxPos: v })} />
              <NumRow label="单笔基准仓位" hint="默认 0.10（占总资产）：这是**下限**。生效单笔占比 = 下限 + (上限−下限)×(市场缩放×个股缩放)，硬夹在两者之间" value={form.perTrade} max={1} step={0.01} onChange={(v) => set({ perTrade: v })} />
              <NumRow label="单笔上限" hint="默认 0.30：因子越好单笔越大（§B0，替代过去固定 10% 切出的碎单）。两者都不得超过 0.50（单笔不过半仓）" value={form.perTradeMax} max={0.5} step={0.01} onChange={(v) => set({ perTradeMax: v })} />
              <NumRow
                label="相关性簇帽"
                hint="默认 40（%，0=关）：同簇（ρ≥阈值 **或** 同行业）持仓合计不超过总资产该比例。与其他帽**取小不叠乘**——40% 簇帽 + 30% 行业帽 ⇒ 可用 30%，不是 12%。极端行情下它会主动减少可买票数，这是设计意图"
                value={form.clusterCap}
                max={100}
                step={5}
                onChange={(v) => set({ clusterCap: v })}
              />
              <NumRow
                label="相关性阈值"
                hint="默认 0.70：60 个交易日日收益 Pearson ρ 达到该值即判同簇（与「同行业」双轨合并，取严）。样本不足 40 天按同簇处理（fail-closed）"
                value={form.clusterRho}
                max={1}
                step={0.05}
                onChange={(v) => set({ clusterRho: v })}
              />
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
