// ── 模拟盘页面 Paper.jsx ──
// Paper trading: account state, strategy pools, positions/fills/orders, equity curve,
// manual buy/trim/close, deposit, pool/cap config, pool reset, full liquidation.
// 【页面职责概述】模拟盘（纸面交易）页面，主要包含五大块：
//   1) 账户总览：绩效统计卡（总资产/收益/现金/胜率/滑点成本）与净值曲线（SVG 折线）；
//   2) 分仓资金池：按池筛选持仓/成交/订单，展示各池收益与现金，支持单池清盘；
//   3) 三张数据表：当前持仓 / 成交日志 / 订单记录，行可展开分时+盘口视图（MinuteView）；
//   4) 手动交易：加仓/减仓/清仓弹窗、注入资金、全局清盘重置、引擎自检诊断；
//   5) 配置管理：设置弹窗（资金分配/仓位上限/撮合设置/战法开关/买入纪律）与融券做空池管理。
//   数据刷新策略：挂载加载一次 + SSE 事件（message/scan）驱动 + useSseRefresh 兜底轮询（§F5）；
//   权限：页面仅管理员可操作（后端 403 时前端展示无权限面板），普通用户为只读手动记账视图。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import {
  Button, Dialog, Table, Tag, Card, Form, InputNumber, Input, Select, Tabs, Checkbox,
} from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast, confirmDialog } from '../ui.jsx'
import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）
import { useLoadLedger } from '../utils/loadLedger.js' // §P2-J 读取失败按腿记账（共用台账）
import LoadFailBanner from '../components/LoadFailBanner.jsx' // §P2-J 台账红条（单实现，testid 只有一处）
import MinuteView from '../components/MinuteView.jsx'
import StockDetailDrawer from '../components/StockDetailDrawer.jsx'
import useSseRefresh from '../useSseRefresh.js'

const UP = 'var(--app-up)'   // 涨（A股习惯红）
const DOWN = 'var(--app-down)' // 跌（绿）
// 将 up/down 涨跌标记映射为对应的红/绿颜色常量
const clsColor = (c) => (c === 'up' ? UP : c === 'down' ? DOWN : undefined)

/**
 * 将数值格式化为带千分位、固定两位小数的中文本地化字符串。
 * @param {number|string} v 原始数值（null/undefined 视为 0）
 * @returns {string} 例如 "1,234.56"
 */
// 格式化为两位小数的中文数字
const fmt = (v) => (v ?? 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
/**
 * 将时间戳格式化为 "MM-DD HH:mm:ss"（本地时区）。
 * 无法解析时回退为字符串切片（取第 5~16 位）。
 * @param {number|string} t 时间戳或日期字符串
 * @returns {string} 格式化后的时间，空值返回 "—"
 */
// 格式化为 MM-DD HH:mm:ss
function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d)) return String(t).slice(5, 16)
  // 两位补零：月/日/时/分/秒统一两位展示
  const p2 = (n) => String(n).padStart(2, '0')
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`
}
/**
 * 根据盈亏数值返回涨跌标记，用于决定文字颜色。
 * @param {number} v 盈亏值
 * @returns {'up'|'down'} 非负返回 'up'，否则 'down'
 */
// 根据盈亏返回 up/down
function pnlCls(v) { return v >= 0 ? 'up' : 'down' }
/**
 * 计算买入成交价相对信号价的滑点百分比（仅买入且信号价有效时）。
 * @param {object} t 成交记录，需含 side/price/signal_price
 * @returns {string} 形如 "+1.23%" 或 "—"（非买入/无信号价）
 */
// 计算买入成交价相对信号价的滑点
function tradeSlippage(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return '—'
  // 滑点百分比：(成交价-信号价)/信号价×100
  const pct = (t.price - t.signal_price) / t.signal_price * 100
  return (pct >= 0 ? '+' : '') + pct.toFixed(2) + '%'
}
/**
 * 返回买入滑点的涨跌标记，用于滑点文字着色。
 * 成交价高于信号价视为成本增加（down/绿），低于则视为节省（up/红）。
 * @param {object} t 成交记录
 * @returns {''|'up'|'down'} 无有效信号价时返回空串
 */
// 买入滑点标记类名：成交价劣于信号价时高亮
function tradeSlippageCls(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return ''
  return t.price >= t.signal_price ? 'down' : 'up'
}
/**
 * 将资金池 key 翻译为中文展示标签。
 * 已知 key（龙头/双响炮/N形/龙回头/动量）映射为对应中文；
 * 以 fac_ / pat_ 开头的分别加「因子·」「形态·」前缀。
 * @param {string} k 资金池 key（空视为"其他/手动"）
 * @returns {string} 中文标签
 */
// 将资金池 key 翻译为中文标签
function poolLabel(k) {
  if (!k) return '其他/手动'
  const labels = { dragon: '龙头', double_bump: '双响炮', n_shape: 'N形', dragon_return: '龙回头', momentum: '动量' }
  if (labels[k]) return labels[k]
  if (/^fac_/.test(k)) return '因子·' + k
  if (/^pat_/.test(k)) return '形态·' + k
  return k
}
/**
 * 规范化资金池 key：空 key 统一归为 "__other__" 一类，便于按池筛选。
 * @param {string} k 原始 key
 * @returns {string} 规范化后的 key
 */
// 规范化资金池 key，空 key 归为一类
function normPoolKey(k) { return k || '__other__' }
/**
 * 将订单状态英文枚举翻译为中文文案。
 * @param {string} s filled/partial/rejected 之一
 * @returns {string} 中文状态文案
 */
// 订单状态中文
function orderStatusText(s) { return { filled: '全部成交', partial: '部分成交', rejected: '已拒绝' }[s] || s }
/**
 * 返回订单状态对应的 TDesign Tag 主题色。
 * @param {string} s 订单状态
 * @returns {string} TDesign 主题名（success/warning/danger/default）
 */
// 订单状态徽标主题
function orderStatusTheme(s) { return { filled: 'success', partial: 'warning', rejected: 'danger' }[s] || 'default' }
/**
 * 截断过长的订单说明文本，超过 18 字用省略号收尾（用于表格列展示）。
 * @param {string} r 原始说明
 * @returns {string} 截断后文本或"—"
 */
// 截断原因文本
function shortReason(r) { if (!r) return '—'; return r.length > 18 ? r.slice(0, 18) + '…' : r }

/**
 * 单指标统计卡片：上方小灰字标签 + 下方大号数值。
 * @param {string} label 卡片标题
 * @param {React.ReactNode} children 数值内容
 */
// 单卡统计
function StatCard({ label, children }) {
  return (
    <Card bordered style={{ flex: '1 1 180px', minWidth: 160 }}>
      <div style={{ fontSize: 12, color: 'var(--app-muted)' }}>{label}</div>
      <div style={{ fontSize: 18, fontWeight: 600, marginTop: 4 }}>{children}</div>
    </Card>
  )
}

/**
 * 模拟盘页面组件
 * 展示账户状态、分仓资金池、持仓/成交/委托、净值曲线与资金配置。
 * @returns {JSX.Element}
 */
export default function Paper() {
  // 模拟盘总开关（后端 rules.paper.enabled），随 load() 每次刷新更新
  const [enabled, setEnabled] = useState(false)
  // 当前账号是否管理员：联动版标签、统计卡可见性、买回平仓等权限点
  const [isAdmin, setIsAdmin] = useState(false)
  // 后端鉴权拒绝（403）：模拟盘仅管理员可访问，后端据此决定，前端只负责展示。
  const [forbidden, setForbidden] = useState(false)
  // 初始资金输入草稿（注入资金弹窗回填用，仅在为空时从后端回填一次）
  const [initialCapital, setInitialCapital] = useState('')
  // 持仓上限输入草稿（与初始资金一起在注入资金时提交）
  const [maxPos, setMaxPos] = useState('')
  // 已生效的全局持仓上限（0=不设限），设置弹窗回填与页头标签展示用
  const [appliedMax, setAppliedMax] = useState(0)
  // 主内容区 Tabs：positions=当前持仓 / trades=成交日志 / orders=订单
  const [tab, setTab] = useState('positions')
  // §SHORT-4 融券做空卡数据（short_book.enabled=false 时整卡隐藏，决策⑤）
  const [shortBook, setShortBook] = useState(null)
  // 全局账户统计（总资产/收益/胜率/滑点成本等），来自 fetchPaperState().stats
  const [stats, setStats] = useState(null)
  // 持仓列表（api.fetchPaperPositions）
  const [positions, setPositions] = useState([])
  // 成交记录列表（api.fetchPaperTrades）
  const [trades, setTrades] = useState([])
  // 委托记录列表（api.fetchPaperOrders）
  const [orders, setOrders] = useState([])
  // 净值曲线点位数组（api.fetchPaperEquity，元素形如 {value}）
  const [equity, setEquity] = useState([])
  // 分仓资金池列表（key/label/cash/max_pos/buy_rule/stats/return_pct 等）
  const [pools, setPools] = useState([])
  // 当前选中资金池的规范化 key（null=全部；'__other__'=其他/手动）
  const [activePool, setActivePool] = useState(null)

  // §P2-I 轮询/事件后到丢弃：本页有 SSE（message/scan）+ useSseRefresh 兜底轮询 + 十几处写操作后
  // 立即 load()，多条腿并行时旧响应后到的覆盖风险最高，故与其余页面统一走同一 hook。
  const loadGuard = useStaleGuard()
  // §P2-J 读取失败台账（持仓/成交/委托/净值/账户状态五腿，按腿记名、成功即销案）
  const { fails: loadFails, mark: markLoadFail, clear: clearLoadFail } = useLoadLedger()
  // §P2-J 区块角标：台账里点到本腿的名字就在对应卡片/页签上再显一次——
  // 页顶红条只说「哪几条腿坏了」，而用户此刻可能正盯着持仓表；失败态必须出现在**读数的旁边**，
  // 否则「上一轮持仓」看起来就是一份正常读数（O1 要求四处各自可见）。
  function legFailFlag(legName, note) {
    // 本腿失败时在区块头部渲染红色小字角标，成功/未拉取时返回 null
    // note：口径文案。列表/卡片类腿失败后显示的是上一轮读数；配置表单类腿失败后没有可用表单，
    // 两套语义不能共用一句（把「不可用」写成「显示上一轮读数」＝让空表单冒充旧数据）。
    if (!loadFails[legName]) return null
    return (
      <span
        style={{ color: 'var(--app-up)', fontSize: 12, marginLeft: 8 }}
        data-testid={'paper-leg-fail-' + legName}
        title={loadFails[legName]}
      >{note || '读取失败 · 显示上一轮读数'}</span>
    )
  }

  // 注入资金弹窗开关
  const [showDepositModal, setShowDepositModal] = useState(false)
  // 清盘重置弹窗开关
  const [showResetModal, setShowResetModal] = useState(false)
  // 统一设置弹窗开关（资金分配/仓位上限/撮合设置/战法开关/买入纪律）
  const [settingsOpen, setSettingsOpen] = useState(false)
  // 设置弹窗当前标签页：alloc/caps/engine/strategies/rules
  const [settingsTab, setSettingsTab] = useState('alloc')
  // §SIGNAL_CONTROLLER 模拟盘战法开关（白名单）：known=后端全集，stratOn=勾选映射，stratList=当前已列名集合
  const [stratKnown, setStratKnown] = useState([])
  // 各战法勾选映射 {id: 是否允许}（战法开关标签页表单数据）
  const [stratOn, setStratOn] = useState({})
  // 黑名单观察期标记：true 时战法开关页提示「命中只记录不拦截」
  const [stratShadow, setStratShadow] = useState(true)
  // 注入资金表单金额（元）
  const [depositAmount, setDepositAmount] = useState(0)
  // 清盘重置表单：重置后初始资金（0=按当前累计投入总额）
  const [resetToCapital, setResetToCapital] = useState(0)
  // 清盘重置表单：重置后持仓上限（0=不设限）
  const [resetMaxPos, setResetMaxPos] = useState(0)
  // 设置弹窗-全局持仓上限草稿（caps 标签页）
  const [cfgMaxPos, setCfgMaxPos] = useState(0)
  // 设置弹窗-各池资金分配草稿 {poolKey: 金额}（alloc 标签页）
  const [cfgAllocs, setCfgAllocs] = useState({})
  // 设置弹窗-各池持仓上限草稿 {poolKey: 数量}（caps 标签页）
  const [cfgCaps, setCfgCaps] = useState({})
  // 设置弹窗-各池买入纪律草稿 {poolKey: {max_daily_buys/cooldown_minutes/min_score/budget_pct_per_day}}
  const [cfgRules, setCfgRules] = useState({})
  // 设置弹窗-买入纪律当前选中的资金池 key
  const [cfgRuleSel, setCfgRuleSel] = useState('')
  // 设置弹窗表单校验告警文案（资金超额/上限超额/白名单全空等）
  const [cfgWarn, setCfgWarn] = useState('')
  // §F-4（20260917 缺陷修复批）撮合设置：账户级模拟盘参数（总开关/自动卖出/单笔资金/做空池），
  // openSettingsModal 拉取回填，保存走 POST /api/paper/config（改后即热生效，不再需要重启）。
  const [engCfg, setEngCfg] = useState(null)

  // 自检诊断结果数据（开关/管理员/引擎路径/持仓成交文件状态/池分布）
  const [selfCheck, setSelfCheck] = useState(null)
  // 自检结果弹窗开关
  const [selfCheckOpen, setSelfCheckOpen] = useState(false)
  // 自检请求进行中（按钮 loading，防重复提交）
  const [selfCheckLoading, setSelfCheckLoading] = useState(false)

  // §D-1（GAP_VERIFY_20260917_PM）夜间信号质量报告：researchd 每晚落库 paper_research_reports，
  // 此前有写无读；本卡补读端展示（日期列表→选中展开 trades/attribution 关键行）。
  const [nReports, setNReports] = useState([])
  const [nReportsOpen, setNReportsOpen] = useState(false)
  const [nReportsLoading, setNReportsLoading] = useState(false)
  const [nReportSel, setNReportSel] = useState(-1)

  // 已展开分时图的行 key 集合（持仓行用 code，成交行用 trade_序号）
  const [klineOpen, setKlineOpen] = useState(new Set())
  // §F3 全局个股详情抽屉目标（{code,name}），null=关闭
  const [detail, setDetail] = useState(null)
  // 移动端持仓行底部操作面板目标（null=关闭）
  const [sheetPos, setSheetPos] = useState(null)
  // 移动端成交行底部操作面板目标（含 idx 便于展开对应分时）
  const [sheetTradeRow, setSheetTradeRow] = useState(null)
  // 手动交易弹窗（加仓/减仓/清仓共用）开关
  const [tradeModal, setTradeModal] = useState(false)
  // 交易方向：add=加仓 / trim=减仓 / close=清仓
  const [tradeDir, setTradeDir] = useState('add')
  // 交易目标持仓记录
  const [tradeTarget, setTradeTarget] = useState(null)
  // 交易表单委托价（0=留空，后端按实时价撮合）
  const [tradeFormPrice, setTradeFormPrice] = useState(0)
  // 交易表单手数（1手=100股；清仓时不可编辑、固定全部持仓）
  const [tradeFormQty, setTradeFormQty] = useState(1)

  const W = 900, H = 220 // 净值曲线 SVG 的逻辑尺寸（viewBox 坐标，非真实像素）
  const timer = useRef(null) // §F5 预留（轮询已由 SSE + useSseRefresh 兜底接管）


  // 解析交易弹窗中输入的手数（无效或非正整数则归零，用于预览/校验）
  const tradePreviewQty = useMemo(() => {
    const q = parseInt(tradeFormQty, 10)
    return isNaN(q) || q <= 0 ? 0 : q
  }, [tradeFormQty])
  // 减仓手数×100 是否超过当前持仓股数（超卖时禁用确认）
  // §P2-15（2026-09-15）：原 `>=` 把"全部清仓"也当成超卖禁用——与 Positions.jsx:171 的
  // `sell > cur` 口径不一致，用户在模拟盘无法一键清掉整只持仓。改为严格大于。
  const tradeOverSell = useMemo(() =>
    tradeDir === 'trim' && tradeTarget && tradePreviewQty * 100 > tradeTarget.qty, [tradeDir, tradePreviewQty, tradeTarget])

  // 判断指定资金池是否已配置买入纪律（日限/冷却/最低分/日预算任一非空即视为已配）
  const poolCurrentRule = (key) => {
    const p = pools.find((x) => x.key === key)
    return !!(p && p.buy_rule && (p.buy_rule.max_daily_buys || p.buy_rule.cooldown_minutes ||
      p.buy_rule.min_score || p.buy_rule.budget_pct_per_day))
  }
  // 将指定资金池的买入纪律序列化为可读文案（日限/冷却/最低分/日预算%）
  const poolCurrentRuleText = (key) => {
    const p = pools.find((x) => x.key === key)
    const r = p && p.buy_rule
    if (!r) return ''
    return `限${r.max_daily_buys || '∞'}次/冷却${r.cooldown_minutes || 0}分/分≥${r.min_score || 0}/预算${r.budget_pct_per_day || 0}%`
  }

  // 将净值序列归一化映射为 SVG 折线坐标点（viewBox 尺寸 W×H）
  const linePoints = useMemo(() => {
    if (equity.length < 2) return ''
    const pad = 10
    const vals = equity.map((p) => p.value)
    const min = Math.min(...vals), max = Math.max(...vals)
    const range = max - min || 1
    return equity.map((p, i) => {
      const x = pad + (i / (equity.length - 1)) * (W - 2 * pad)
      const y = H - pad - ((p.value - min) / range) * (H - 2 * pad)
      return x.toFixed(1) + ',' + y.toFixed(1)
    }).join(' ')
  }, [equity])
  // 净值曲线背景横向网格线：在画布 1/4、2/4、3/4 高度处（k=1,2,3）
  const gridLines = useMemo(() => [1, 2, 3].map((k) => ({ y: (H / 4) * k })), [])

  // 按当前选中的资金池过滤持仓列表
  const filteredPositions = useMemo(() => {
    if (activePool === null) return positions
    return positions.filter((p) => normPoolKey(p.strategy_type) === activePool)
  }, [positions, activePool])
  // 按当前选中的资金池过滤成交记录
  const filteredTrades = useMemo(() => {
    if (activePool === null) return trades
    return trades.filter((t) => normPoolKey(t.strategy_type) === activePool)
  }, [trades, activePool])
  // 按当前选中的资金池过滤订单
  const filteredOrders = useMemo(() => {
    if (activePool === null) return orders
    return orders.filter((o) => normPoolKey(o.strategy_type) === activePool)
  }, [orders, activePool])
  // 取当前选中资金池的统计（未选中时回退到全局统计）
  const activeStats = useMemo(() => {
    if (activePool === null) return stats
    const p = pools.find((p) => normPoolKey(p.key) === activePool)
    return (p && p.stats) || stats
  }, [activePool, pools, stats])
  // 取当前选中资金池的中文标签
  const activePoolLabel = useMemo(() => {
    const p = pools.find((p) => normPoolKey(p.key) === activePool)
    return p ? p.label : ''
  }, [activePool, pools])

  // 为持仓行补充稳定行 key（供表格展开/分时图追踪）
  const posData = useMemo(() => filteredPositions.map((p) => ({ ...p, __key: p.code })), [filteredPositions])
  // 为成交行补充稳定行 key 与序号
  const tradeData = useMemo(() => filteredTrades.map((t, i) => ({ ...t, __key: 'trade_' + i, __idx: i })), [filteredTrades])
  // 为订单行补充稳定行 key
  const orderData = useMemo(() => filteredOrders.map((o, i) => ({ ...o, __key: o.id || ('o_' + i) })), [filteredOrders])

  // 展开/收起指定持仓代码的分时图（维护已展开 key 集合）
  function toggleKline(key) {
    setKlineOpen((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key); else next.add(key)
      return next
    })
  }
  // 移动端点击持仓行时打开底部持仓操作面板（桌面端不响应）
  function onRowTap(p) { if (window.innerWidth <= 768) setSheetPos(p) }
  // 移动端点击成交行时打开底部成交操作面板（带索引，便于展开对应分时）
  function onTradeTap(t, i) { if (window.innerWidth <= 768) setSheetTradeRow({ ...t, idx: i }) }
  // 移动端面板：展开当前选中持仓的分时图并关闭面板
  function sheetKline() {
    if (!sheetPos) return
    toggleKline(sheetPos.code); setSheetPos(null)
  }
  // 移动端面板：对当前选中持仓打开加仓/减仓/清仓交易弹窗
  function sheetTrade(dir) {
    if (!sheetPos) return
    const p = sheetPos; setSheetPos(null); openTrade(p, dir)
  }
  // 移动端面板：展开当前选中成交所属标的的分时图并关闭面板
  function sheetTradeKline() {
    if (!sheetTradeRow) return
    toggleKline('trade_' + sheetTradeRow.idx); setSheetTradeRow(null)
  }

  /**
   * 打开加仓/减仓/清仓交易弹窗并回填标的与默认手数。
   * @param {object} p 持仓记录（含 code/name/strategy/mark/qty）
   * @param {'add'|'trim'|'close'} dir 交易方向
   */
  // 打开模拟盘交易弹窗（指定持仓与方向）
  function openTrade(p, dir) {
    setTradeTarget(p); setTradeDir(dir)
    setTradeFormPrice(p.mark || 0)
    // close 时持仓 qty 为股数，表单口径是手——回填换算成整手展示（不足一手按 1 手）
    setTradeFormQty(dir === 'close' ? Math.max(1, Math.round((p.qty || 0) / 100)) : 1)
    setTradeModal(true)
  }

  /**
   * 提交模拟盘加仓/减仓/清仓委托。
   * add：调用 buyPaperPosition；trim/close：调用 sellPaperPosition（close 传 0 表示全平）。
   * 成功后关闭弹窗并重新加载数据。
   * @returns {Promise<void>}
   */
  // 提交模拟盘加仓/减仓/清仓委托
  // §FIX-1(20260919) 手/股单位收敛：弹窗输入与后端契约解耦——表单是手数（1手=100股），
  // API qty 一律为股数（引擎按股记账，见 internal/paper/paper_test.go 口径注释），提交前在此唯一换算点 ×100。
  async function confirmTrade() {
    const p = tradeTarget
    if (!p) return
    const price = parseFloat(tradeFormPrice)
    const qty = parseInt(tradeFormQty, 10)
    if (tradeDir !== 'close' && (isNaN(qty) || qty <= 0)) { showToast('请输入有效的数量','warning'); return }
    const shares = qty * 100 // 手→股；close 不走此值（传 0=全平）
    try {
      if (tradeDir === 'add') {
        await api.buyPaperPosition(p.code, p.name || '', p.strategy || '', 0, price > 0 ? price : 0, shares)
        showToast(`已加仓 ${p.code} ${qty} 手`,'success')
      } else {
        await api.sellPaperPosition(p.code, price > 0 ? price : 0, tradeDir === 'close' ? 0 : shares)
        showToast(`已${tradeDir === 'close' ? '清仓' : '减仓'} ${p.code}`,'success')
      }
      setTradeModal(false)
      await load()
    } catch (e) { showToast(e.message || '操作失败','error') }
  }

  /**
   * 加载模拟盘全部数据：先取开关/账户/资金池状态（账户状态腿），
   * 仅在 enabled 时再依次拉取持仓、成交、委托与净值曲线四条数据腿。
   * 四腿经 loadPaperLeg 统一封装：互不阻断（一腿失败不影响其余），但**失败必须按腿记名进台账**
   * （§P2-J，旧形态是四句 `catch (_) {}` 吞掉后什么都不留）；整轮受 §P2-I 代号守卫保护，
   * 后到的旧轮次响应不再覆盖新读数也不再销新轮的案。
   * @returns {Promise<void>}
   */
  // 加载模拟盘状态、持仓、成交、委托与净值曲线
  async function load() {
    // §P2-I：本轮代号在发起任何请求前盖章；下面每个 await 回来后才写 state，旧轮次整体丢弃
    // （本页原本就漏在守卫外：SSE 事件密集时段多条腿并行重拉，后到的旧持仓表会把刚成交的新表盖回去）。
    const token = loadGuard.begin()
    try {
      const st = await api.fetchPaperState()
      if (loadGuard.isStale(token)) return // §P2-I 后到的旧轮次：状态与五路数据一起作废
      clearLoadFail('账户状态')
      // 注意：开关状态必须用本次拉取到的 st.enabled 判断，不能用组件 state 的 enabled——
      // 首屏 enabled 初始为 false，且 setInterval(load) 捕获的是首屏闭包，若用 state 判断会
      // 永远走到「未启用」提前返回，导致持仓/成交/订单/净值曲线（在 return 之后才拉取）永远为空
      // （实录：分仓池显示 20 仓但「全部」页为空）。
      // English: decide the early-return from the freshly-fetched st.enabled, never from the
      // stale `enabled` state — otherwise positions/trades/orders/equity are never fetched.
      const en = !!st.enabled
      setEnabled(en)
      setIsAdmin(!!st.is_admin)
      if (st.initial_capital > 0 && !initialCapital) setInitialCapital(String(st.initial_capital))
      if (st.max_positions !== undefined && !maxPos) setMaxPos(st.max_positions > 0 ? String(st.max_positions) : '0')
      setAppliedMax((st.max_positions !== undefined && st.max_positions > 0) ? st.max_positions : 0)
      setStats(st.stats || null)
      setPools(Array.isArray(st.strategy_pools) ? st.strategy_pools : [])
      setShortBook(st.short_book || null)
      if (!en) {
        // 关闭后清空持仓/成交/订单/净值，避免残留旧数据让用户误以为仍有持仓
        setPositions([])
        setTrades([])
        setOrders([])
        setEquity([])
        // 开关已关：四条数据腿本轮不再拉取，台账里的旧失败案一并销掉，
        // 否则红条会一直挂着「持仓读取失败」，而实际是「本来就没有模拟盘数据」
        clearLoadFail('持仓'); clearLoadFail('成交'); clearLoadFail('委托'); clearLoadFail('净值曲线')
        return
      }
      // §P2-J（AUDIT_20261005 P2-J / FIX_PLAN_20261006 波 6）四腿独立容错——
      // 旧形态是四条各写一句 `try { setX(await api.fetch…()) } catch (_) {}`：
      // 独立容错的方向是对的（一条腿挂不该拖黑整页），坏在**失败之后什么都不留**，
      // 于是持仓拉取失败时页面显示的是上一轮那张表（或空表）却毫无标注，
      // 用户会拿旧持仓做新决策。现每腿失败按名记账（页顶红条 + 对应区块角标各点名一次），
      // 成功即销案，失败**不清空既有读数**（把旧值抹掉只会让「读取失败」冒充「今天真没数据」，
      // 与 §0929 ④「保存失败可见」、§M-9 已修族同一姿势）。
      await loadPaperLeg(token, '持仓', api.fetchPaperPositions, setPositions)
      await loadPaperLeg(token, '成交', api.fetchPaperTrades, setTrades)
      await loadPaperLeg(token, '委托', api.fetchPaperOrders, setOrders)
      await loadPaperLeg(token, '净值曲线', api.fetchPaperEquity, setEquity)
    } catch (e) {
      // §M13/§A5：后端 403 时展示「无权限」面板，不再静默兜底。
      // 判定改为状态码 api.isForbidden(e)——旧写法 e.message.indexOf('无权限') 只认
      // adminMiddleware 的中文文案，对 permMiddleware 的英文 "no permission: <perm>" 会漏判。
      if (api.isForbidden(e)) { setForbidden(true); return }
      // §P2-J 主腿（状态）原先在非 403 时同样什么都不留——外层 catch 走到这里就等于
      // 「整页读数停在上一轮」，必须可见，否则四腿的台账做得再细也挡不住主腿静默失效。
      markLoadFail('账户状态', (e && (e.message || String(e))) || '未知错误')
    }
  }

  /**
   * 单条数据腿的读取-记账封装（§P2-J 四腿共用一条实现，避免四份复制粘贴的分叉）。
   * @param {number|string} token 本轮守卫代号（loadGuard.begin() 的返回）
   * @param {string} legName 台账里的腿名（红条与区块角标点名的就是它）
   * @param {() => Promise<Array>} fetcher 该腿的取数函数
   * @param {(v: Array) => void} setter 该腿的 state 写入函数
   * @returns {Promise<void>}
   */
  async function loadPaperLeg(token, legName, fetcher, setter) {
    try {
      const data = await fetcher()
      // 后到的旧轮次：既不写 state 也不动台账（台账由本轮自己的成功/失败负责）
      if (loadGuard.isStale(token)) return
      if (Array.isArray(data)) { setter(data); clearLoadFail(legName) }
      // 结构漂移（后端改了载荷形状）也算这条腿没读到：写进去会让表格 render 抛错、
      // 静默吞掉则又是「旧读数冒充新读数」，所以按失败记账并保留上一轮。
      else markLoadFail(legName, '返回结构不是数组（契约漂移）')
    } catch (e) {
      markLoadFail(legName, (e && (e.message || String(e))) || '未知错误')
    }
  }

  // 触发模拟盘自检诊断（持仓/成交/订单/净值一致性），结果弹窗展示
  async function runSelfCheck() {
    setSelfCheckLoading(true)
    try {
      const res = await api.fetchPaperSelfCheck()
      setSelfCheck(res)
      setSelfCheckOpen(true)
    } catch (e) {
      showToast(e && e.message ? e.message : '自检失败')
    } finally {
      setSelfCheckLoading(false)
    }
  }

  // §D-1 拉取夜间信号质量报告历史（admin 端点；403 时静默——页面本身普通用户只读）
  async function openNightlyReports() {
    setNReportsLoading(true)
    try {
      const res = await api.fetchPaperResearchReports(30)
      setNReports(Array.isArray(res.reports) ? res.reports : [])
      setNReportSel(0)
      setNReportsOpen(true)
    } catch (e) {
      showToast(e && e.message ? e.message : '夜间报告拉取失败')
    } finally {
      setNReportsLoading(false)
    }
  }

  /**
   * 确认注入资金（增量计入现金，保留现有持仓/净值/成交）。
   * 调用 resetPaper 并回填初始资金与持仓上限，然后重新加载。
   * @returns {Promise<void>}
   */
  // 确认注入资金并更新持仓上限
  async function confirmDeposit() {
    const amt = parseFloat(depositAmount)
    if (!(amt > 0)) { showToast('请输入有效的注入金额','warning'); return }
    const mp = parseInt(maxPos, 10)
    const mpv = mp > 0 ? mp : 0
    const capHint = mpv > 0 ? '，持仓上限 ' + mpv + ' 只' : '（持仓上限不设限，由资金决定）'
    const ok = await confirmDialog('确认注入资金 ¥' + fmt(amt) + capHint + '？将增量计入现金，保留现有持仓/净值/成交记录。', '注入资金')
    if (!ok) return
    try {
      const res = await api.resetPaper(amt, mpv)
      setInitialCapital(String(res.initial_capital || (parseFloat(initialCapital) + amt)))
      setMaxPos(String(res.max_positions > 0 ? res.max_positions : 0))
      setAppliedMax(res.max_positions > 0 ? res.max_positions : 0)
      await load()
    } catch (e) { showToast(e.message || '注入失败','error') }
  }

  /**
   * 清盘当前选中的分仓资金池：按最后估值价平仓该池全部持仓并回补池现金，
   * 清空该池累计涨跌幅；不影响其他池与全局净值/成交。
   * @returns {Promise<void>}
   */
  // 清盘当前选中的分仓资金池
  async function confirmPoolReset() {
    if (activePool === null) return
    const label = activePoolLabel
    const count = filteredPositions.length
    const ok = await confirmDialog(
      `清盘「${label}」资金池？\n将按最后估值价平仓该池 ${count} 笔持仓（回补池现金），并清空该池累计涨跌幅表现。\n其他分仓资金池与全局净值/成交日志不受影响。`,
      '单池清盘'
    )
    if (!ok) return
    try {
      await api.resetPaperPool(activePool === '__other__' ? '' : activePool)
      await load()
    } catch (e) { showToast(e.message || '清盘失败','error') }
  }

  /**
   * 打开统一设置弹窗（资金分配/仓位上限/买入纪律），
   * 将各资金池现有 cash/max_pos/buy_rule 回填到表单状态，并默认选中首个有效策略池。
   */
  // 打开资金分配/仓位上限/买入纪律设置弹窗并回填当前配置
  function openSettingsModal() {
    // §P2-J 战法开关清单腿：旧写法 `.catch(() => {})` 把失败吞成「列表空白」，
    // 而空白在标签页里与「后端确实没给出 known_strategies」长得一模一样（§0929 ④「未落库不得脏缓存」同族）。
    // 现按腿进台账：页顶红条点名 + 标签页头部角标，成功即销案。
    api.fetchPaperStrategies().then((r) => {
      const known = Array.isArray(r.known_strategies) ? r.known_strategies : []
      // 常量 known：局部定义
      const wl = Array.isArray(r.strategies) ? r.strategies : []
      // 常量 wl：局部定义
      const on = {}
      // 常量 on：局部定义
      known.forEach((v) => { on[v.id] = wl.length === 0 ? v.id !== 'momentum' : wl.includes(v.id) })
      setStratKnown(known); setStratOn(on); setStratShadow(!!r.shadow_blacklist)
      clearLoadFail('战法开关清单')
    }).catch((e) => markLoadFail('战法开关清单', (e && e.message) || '未知错误'))
    // §F-4 拉取撮合配置回填"撮合设置"标签页（失败置 null，标签页内显示加载失败占位）
    // §P2-J：占位只说明"这格没数据"，还需要在页顶台账留一笔，否则从持仓表切过来的人看不到失败面
    api.fetchPaperConfig()
      .then((c) => { setEngCfg(c); clearLoadFail('撮合配置') })
      .catch((e) => { setEngCfg(null); markLoadFail('撮合配置', (e && e.message) || '未知错误') })
    setCfgMaxPos(appliedMax > 0 ? appliedMax : 0)
    const allocs = {}, caps = {}, rules = {}
    pools.forEach((p) => {
      allocs[p.key] = p.cash
      caps[p.key] = p.max_pos || 0
      rules[p.key] = {
        max_daily_buys: (p.buy_rule && p.buy_rule.max_daily_buys) || 0,
        cooldown_minutes: (p.buy_rule && p.buy_rule.cooldown_minutes) || 0,
        min_score: (p.buy_rule && p.buy_rule.min_score) || 0,
        budget_pct_per_day: (p.buy_rule && p.buy_rule.budget_pct_per_day) || 0,
      }
    })
    setCfgAllocs(allocs); setCfgCaps(caps); setCfgRules(rules)
    const firstStrategy = pools.find((p) => p.key !== '')
    setCfgRuleSel(firstStrategy ? firstStrategy.key : (pools[0] ? pools[0].key : ''))
    setSettingsTab('alloc'); setCfgWarn('')
    setSettingsOpen(true)
  }

  /**
   * 保存设置：根据当前标签页分别校验并提交
   * - alloc：各池资金额之和不得超过总现金；
   * - rules：逐池写入买入纪律（日限/冷却/最低分/日预算%）；
   * - caps：各池上限之和不得超过全局上限。
   * 失败以 Toast 提示，成功关闭弹窗并重新加载。
   * @returns {Promise<void>}
   */
  // 保存资金分配、仓位上限或买入纪律配置
  async function saveSettings() {
    const totalCash = pools.reduce((s, p) => s + p.cash, 0)

    // 资金分配保存：校验各池分配总额不超过总现金
    if (settingsTab === 'alloc') {
      const allocs = {}; let assigned = 0
      pools.forEach((p) => {
        const n = parseFloat(cfgAllocs[p.key])
        if (n > 0) { allocs[p.key] = n; assigned += n }
      })
      if (assigned > totalCash + 0.01) { setCfgWarn(`资金超额：Σ ¥${fmt(assigned)} > 总现金 ¥${fmt(totalCash)}`); return }
      try { await api.configPaperPools(null, null, allocs); setSettingsOpen(false); await load() }
      catch (e) { showToast(e.message || '保存失败','error') }
      return
    }

    // 买入纪律保存：解析每池日买笔数/冷却/最低分/预算占比
    if (settingsTab === 'rules') {
      const rules = {}
      pools.forEach((p) => {
        const r = cfgRules[p.key] || {}
        rules[p.key] = {
          max_daily_buys: parseInt(r.max_daily_buys, 10) || 0,
          cooldown_minutes: parseInt(r.cooldown_minutes, 10) || 0,
          min_score: parseFloat(r.min_score) || 0,
          budget_pct_per_day: parseFloat(r.budget_pct_per_day) || 0,
        }
      })
      try { await api.configPaperPools(null, null, null, rules); setSettingsOpen(false); await load() }
      catch (e) { showToast(e.message || '保存失败','error') }
      return
    }
    // §F-2（20260917 缺陷修复批）战法开关保存：旧实现缺本分支——在"战法开关"标签页点保存
    // 会落到下方仓位上限(caps)兜底分支，勾选静默丢失且意外提交 caps 草稿。
    // 后端契约：只传 strategies，blacklist 省略=保持原值（setPaperStrategiesReq 指针字段语义）。
    // 全不勾等价清空白名单=恢复默认全集（后端语义），易误操作，故至少保留一项。
    if (settingsTab === 'strategies') {
      const onList = Object.keys(stratOn).filter((k) => stratOn[k])
      if (onList.length === 0) { setCfgWarn('至少保留一个战法：全部取消=清空白名单，后端将恢复默认全集（动量除外）'); return }
      try {
        await api.updatePaperStrategies(onList)
        setSettingsOpen(false)
        showToast('战法准入已更新（' + onList.length + ' 项开启）', 'success')
        await load()
      } catch (e) { showToast(e.message || '保存失败', 'error') }
      return
    }
    // §F-4 撮合设置保存：只提交账户级参数（分池上限/资金仍走各自标签页），后端热生效。
    if (settingsTab === 'engine') {
      if (!engCfg) { showToast('撮合配置未加载，请重开设置弹窗', 'warning'); return }
      try {
        const res = await api.updatePaperConfig({
          enabled: !!engCfg.enabled,
          auto_sell: !!engCfg.auto_sell,
          fixed_amount: Number(engCfg.fixed_amount) || 0,
          short_capital: Number(engCfg.short_capital) || 0,
        })
        setSettingsOpen(false)
        showToast(res && res.engine_enabled ? '撮合配置已保存，模拟盘即时生效' : '撮合配置已保存（模拟盘当前为关闭态）', 'success')
        await load()
      } catch (e) { showToast(e.message || '保存失败', 'error') }
      return
    }
    const caps = {}; let capSum = 0
    pools.forEach((p) => {
      const c = parseInt(cfgCaps[p.key], 10)
      if (c > 0) { caps[p.key] = c; capSum += c }
    })
    const gCap = parseInt(cfgMaxPos, 10)
    if (gCap > 0 && capSum > gCap) { setCfgWarn(`Σ池上限 ${capSum} > 全局 ${gCap}`); return }
    try { await api.configPaperPools(gCap, caps, null); setSettingsOpen(false); await load() }
    catch (e) { showToast(e.message || '保存失败','error') }
  }

  /**
   * 全局清盘重置：平仓全部持仓、清除成交日志与净值曲线。
   * 可携带 reset_to（重置后初始资金）与 max_positions（持仓上限）参数。
   * @returns {Promise<void>}
   */
  // 清盘重置：平仓全部持仓、清除成交日志与净值曲线
  async function doResetV2() {
    const ok = await confirmDialog('确认清盘？\n将平仓全部持仓、清除成交日志与净值曲线。', '清盘重置')
    if (!ok) return
    try {
      const body = {}
      if (resetToCapital > 0) body.reset_to = resetToCapital
      if (resetMaxPos > 0) body.max_positions = resetMaxPos
      await api.paperResetV2(body)
      setShowResetModal(false); setResetToCapital(0); setResetMaxPos(0)
      await load()
    } catch (e) { showToast(e.message || '清盘失败','error') }
  }

  // 挂载时加载模拟盘数据；§F5 刷新由 SSE 事件驱动 + 60s 兜底（原 15s 高频轮询）
  // 依赖数组 []：本 effect 仅在首次挂载执行一次；后续刷新不靠其重跑，
  // 而由 SSE 事件与兜底轮询直接调用 load()（load 内部用的是首屏闭包，
  // 因此开关判断必须依赖新拉取的 st.enabled，见 load 内注释）。
  useEffect(() => { load() }, [])
  // 订阅 message/scan 两类 SSE 事件：事件到达即调用 load() 全量重拉，组件卸载自动解除订阅。
  // §UAT-D2 原订阅的 'tick' 后端从未广播（死订阅），移除
  // §M13（2026-09-22 修复批 K）：enabled 绑 !forbidden——成员账号 403 后 60s 兜底轮询与
  // SSE 驱动的整页重拉一并停用（/api/paper/state 在 adminMiddleware 下，继续打只会刷 403 噪声）。
  useSseRefresh(['message', 'scan'], load, { enabled: !forbidden }) // §UAT-D2 原订阅的 'tick' 后端从未广播（死订阅），移除

  // ── 列定义 ──
  // 模拟盘持仓表格列定义：代码/名称/买卖时间/数量/成本/现价/浮盈/滑点/延迟/资金池/分时/操作
  const posColumns = [
    // 代码列：点击打开全局个股详情抽屉（stopPropagation 避免触发行点击/展开）
    { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
    { colKey: 'name', title: '名称', width: 100 },
    // 买入时间列：优先展示撮合成交时间，悬浮可见「信号发出 → 撮合成交」完整时间线
    { colKey: 'time', title: '买入时间', width: 160, cell: ({ row }) => (
      <span title={'信号发出 ' + fmtTime(row.signal_at) + ' · 撮合成交 ' + fmtTime(row.filled_at)}>{fmtTime(row.filled_at || row.signal_at)}</span>
    ) },
    { colKey: 'qty', title: '数量', width: 70 },
    { colKey: 'cost', title: '成本价', width: 90, cell: ({ row }) => (row.cost_price || 0).toFixed(2) },
    { colKey: 'mark', title: '现价', width: 90, cell: ({ row }) => (row.mark || 0).toFixed(2) },
    // 浮盈 / 浮盈% / 滑点 三列：均按正负红涨绿跌着色
    { colKey: 'pnl', title: '浮盈', width: 100, cell: ({ row }) => <span style={{ color: row.pnl >= 0 ? UP : DOWN }}>{fmt(row.pnl)}</span> },
    { colKey: 'pnlPct', title: '浮盈%', width: 90, cell: ({ row }) => <span style={{ color: row.pnl >= 0 ? UP : DOWN }}>{fmt(row.pnl_pct)}%</span> },
    { colKey: 'slip', title: '滑点', width: 90, cell: ({ row }) => <span style={{ color: row.slippage_pct >= 0 ? UP : DOWN }}>{fmt(row.slippage_pct)}%</span> },
    { colKey: 'lat', title: '延迟', width: 70, cell: ({ row }) => row.latency_sec + 's' },
    // 池列：资金池 key 翻译为中文标签
    { colKey: 'pool', title: '池', width: 100, cell: ({ row }) => <Tag>{poolLabel(row.strategy_type)}</Tag> },
    // 分时列：切换该持仓行展开分时+盘口视图
    { colKey: 'kline', title: '分时', width: 80, cell: ({ row }) => (
      <Button size="small" variant="text" onClick={(e) => { e.stopPropagation(); toggleKline(row.__key) }}>
        {klineOpen.has(row.__key) ? '收起' : '分时'}
      </Button>
    ) },
    // 操作列：加仓/减仓/清仓，打开对应方向的手动交易弹窗
    { colKey: 'ops', title: '操作', width: 200, cell: ({ row }) => (
      <div style={{ display: 'flex', gap: 6 }}>
        <Button size="small" onClick={(e) => { e.stopPropagation(); openTrade(row, 'add') }}>加仓</Button>
        <Button size="small" onClick={(e) => { e.stopPropagation(); openTrade(row, 'trim') }}>减仓</Button>
        <Button size="small" theme="danger" onClick={(e) => { e.stopPropagation(); openTrade(row, 'close') }}>清仓</Button>
      </div>
    ) },
  ]

  // 成交记录表列定义：时间、方向、代码/名称、战法、数量、价格、金额、滑点、延迟与分时
  const tradeColumns = [
    { colKey: 'time', title: '时间', width: 160, cell: ({ row }) => fmtTime(row.time) },
    { colKey: 'side', title: '方向', width: 80, cell: ({ row }) => <Tag theme={row.side === 'buy' ? 'success' : 'danger'}>{row.side === 'buy' ? '买入' : '卖出'}</Tag> },
    { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
    { colKey: 'name', title: '名称', width: 100 },
    // 战法列：成交归属战法标签
    { colKey: 'strategy', title: '战法', width: 100, cell: ({ row }) => <Tag>{row.strategy}</Tag> },
    { colKey: 'qty', title: '数量', width: 70 },
    { colKey: 'price', title: '价格', width: 90, cell: ({ row }) => (row.price || 0).toFixed(2) },
    { colKey: 'amount', title: '金额', width: 100, cell: ({ row }) => fmt(row.amount) },
    // 滑点列：仅买入且信号价有效时展示；成交价劣于信号价=成本增加（绿），优则节省（红）
    { colKey: 'slip', title: '滑点', width: 90, cell: ({ row }) => {
      const c = tradeSlippageCls(row)
      return <span style={c ? { color: clsColor(c) } : undefined}>{tradeSlippage(row)}</span>
    } },
    // 延迟列：仅买入方向有信号→成交撮合延迟，卖出显示"—"
    { colKey: 'lat', title: '延迟', width: 70, cell: ({ row }) => (row.side === 'buy' ? (row.latency_sec || 0) + 's' : '—') },
    // 分时列：展开该成交标的的分时+盘口视图
    { colKey: 'kline', title: '分时', width: 80, cell: ({ row }) => (
      <Button size="small" variant="text" onClick={(e) => { e.stopPropagation(); toggleKline(row.__key) }}>
        {klineOpen.has(row.__key) ? '收起' : '分时'}
      </Button>
    ) },
  ]

  // 委托生命周期表列定义：时间、方向、代码/名称、来源、状态、量价、信号价与说明
  const orderColumns = [
    { colKey: 'time', title: '时间', width: 160, cell: ({ row }) => fmtTime(row.created_at) },
    { colKey: 'side', title: '方向', width: 80, cell: ({ row }) => <Tag theme={row.side === 'buy' ? 'success' : 'danger'}>{row.side === 'buy' ? '买入' : '卖出'}</Tag> },
    { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
    { colKey: 'name', title: '名称', width: 100 },
    // 战法列：产生委托的战法（手动单为空显示"—"）
    { colKey: 'strategy', title: '战法', width: 110, cell: ({ row }) => <Tag>{row.strategy || '—'}</Tag> },
    // 来源列：委托产生渠道（战法信号/手动操作），空显示"—"
    { colKey: 'kind', title: '来源', width: 90, cell: ({ row }) => <Tag>{row.kind || '—'}</Tag> },
    // 状态列：中文文案 + 主题色（全部成交绿/部分成交黄/已拒绝红）
    { colKey: 'status', title: '状态', width: 90, cell: ({ row }) => <Tag theme={orderStatusTheme(row.status)}>{orderStatusText(row.status)}</Tag> },
    { colKey: 'qty', title: '数量', width: 70 },
    { colKey: 'price', title: '成交价', width: 90, cell: ({ row }) => (row.price ? row.price.toFixed(2) : '—') },
    { colKey: 'signal', title: '信号价', width: 90, cell: ({ row }) => (row.signal_price ? row.signal_price.toFixed(2) : '—') },
    // 说明列：悬浮展示完整原因，超 18 字截断显示
    { colKey: 'reason', title: '说明', width: 160, ellipsis: true, cell: ({ row }) => <span title={row.reason || ''}>{shortReason(row.reason)}</span> },
  ]

  // 委托/持仓表格的展开行渲染器：返回该标的分时+盘口组合视图（MinuteView）
  function renderKline(params) {
    const row = params && params.row ? params.row : params
    return <MinuteView code={row.code} name={row.name} />
  }

  return (
    <div className="page">
      {/* 页头：标题 + 运行状态标签组 + 管理操作按钮组 */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12, flexWrap: 'wrap', gap: 8 }}>
        <h2 style={{ fontSize: 18, fontWeight: 600 }}>模拟盘</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {/* 联动版标记：仅管理员可见（自动撮合 + 自动估值模式） */}
          {isAdmin && <Tag theme="warning" style={{ cursor: 'default' }}>联动版</Tag>}
          {/* 运行状态标签：管理员=自动撮合中，普通用户=手动记账（静态），未启用提示配置键 */}
          <Tag theme={enabled ? 'success' : 'default'}>
            {enabled ? (isAdmin ? '自动撮合中' : '手动记账（静态）') : '未启用（rules.paper.enabled）'}
          </Tag>
          {/* 持仓上限标签：仅启用时展示（0=不设限） */}
          {enabled && <Tag>上限：{appliedMax > 0 ? appliedMax + ' 只' : '不设限'}</Tag>}
          {/* 注入资金入口：未启用时禁用 */}
          <Button theme="primary" disabled={!enabled} onClick={() => setShowDepositModal(true)}>＋ 注入资金</Button>
          {/* §F-4：未启用时管理员也要能打开设置（否则总开关永远无法从 UI 打开——先有鸡问题）；
              普通用户未启用时维持禁用（其无写权限）。 */}
          <Button disabled={!enabled && !isAdmin} onClick={openSettingsModal}>⚙ 设置</Button>
          {/* 自检诊断入口：拉取引擎快照一致性结果并弹窗展示 */}
          <Button theme="default" loading={selfCheckLoading} onClick={runSelfCheck}>🔍 自检</Button>
          {/* §D-1 夜间信号质量报告入口（researchd 每晚落库，admin-only 端点） */}
          <Button theme="default" loading={nReportsLoading} onClick={openNightlyReports}>📊 夜间报告</Button>
          {/* 全局清盘入口：未启用时禁用 */}
          <Button theme="danger" disabled={!enabled} onClick={() => setShowResetModal(true)}>清盘</Button>
        </div>
      </div>

      {/* 403 无权限提示面板：普通账号访问时展示，引导改用管理员登录 */}
      {forbidden && (
        <div style={{ marginBottom: 12, padding: '18px 16px', borderRadius: 8, background: '#fff7e6', border: '1px solid #ffd591', color: 'var(--td-warning-color)', fontSize: 13 }}>
          🔒 无权限访问模拟盘：当前登录「{api.getAccount() || '未知'}」为普通用户，该页面仅管理员账号可操作。请使用管理员账号（用户名 admin）登录后再进行管理。
        </div>
      )}

      {/* §P2-J 读取失败台账红条（与其余页面共用一枚实现/一个 testid，见 components/LoadFailBanner.jsx） */}
      <LoadFailBanner fails={loadFails} page="Paper" />

      {/* 注入资金弹窗 */}
      <Dialog
        visible={showDepositModal}
        header="注入资金"
        onClose={() => setShowDepositModal(false)}
        onConfirm={() => { confirmDeposit(); setShowDepositModal(false) }}
        confirmBtn="确认注入"
      >
        <Form layout="vertical">
          {/* 注入金额输入：增量计入现金，不影响现有持仓/净值/成交 */}
          <Form.FormItem label="金额（元）">
            <InputNumber value={depositAmount} min={0} step={1000} placeholder="10000" onChange={(v) => setDepositAmount(v || 0)} style={{ width: 240 }} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 清盘弹窗 */}
      <Dialog
        visible={showResetModal}
        header="⚠ 清盘重置"
        onClose={() => setShowResetModal(false)}
        onConfirm={doResetV2}
        confirmBtn="确认清盘"
      >
        <div style={{ color: 'var(--td-warning-color)', marginBottom: 8 }}>将平仓全部持仓、清除成交日志与净值曲线。</div>
        <Form layout="vertical">
          {/* 重置参数：初始资金（留空=按当前累计投入）与持仓上限（0=不设限） */}
          <Form.FormItem label="重置后初始资金">
            <InputNumber value={resetToCapital} min={0} step={10000} placeholder="默认 100000" onChange={(v) => setResetToCapital(v || 0)} style={{ width: 240 }} />
            <span style={{ fontSize: 12, color: 'var(--app-muted)' }}>元（不填则按当前累计投入总额重置）</span>
          </Form.FormItem>
          <Form.FormItem label="持仓上限">
            <InputNumber value={resetMaxPos} min={0} step={1} placeholder="0=不设限" onChange={(v) => setResetMaxPos(v || 0)} style={{ width: 240 }} />
          </Form.FormItem>
        </Form>
      </Dialog>

      {/* 统一设置弹窗 */}
      <Dialog
        visible={settingsOpen}
        header="⚙ 设置"
        onClose={() => setSettingsOpen(false)}
        onConfirm={saveSettings}
        confirmBtn="保存"
        width={640}
      >
        {/* 设置弹窗标签页：资金分配 / 仓位上限 / 买入纪律三个子面板 */}
        <Tabs value={settingsTab} onChange={(v) => setSettingsTab(v)}>
          {/* 资金分配标签页：逐池调整资金额度，确保各池资金之和不超过总现金 */}
          <Tabs.TabPanel value="alloc" label="资金分配">
            <div style={{ fontSize: 12, color: 'var(--app-muted)', marginBottom: 8 }}>每池资金额（Σ ≈ 总现金守恒）。不影响仓位上限。</div>
            {pools.map((p) => (
              <Form.FormItem key={'sa-' + p.key} label={p.label}>
                <InputNumber
                  value={cfgAllocs[p.key]}
                  min={0} step={1000}
                  placeholder={'当前 ¥' + fmt(p.cash)}
                  onChange={(v) => setCfgAllocs({ ...cfgAllocs, [p.key]: v || 0 })}
                  style={{ width: 240 }}
                />
              </Form.FormItem>
            ))}
          </Tabs.TabPanel>
          {/* 仓位上限标签页：设置全局持仓上限与各池独立上限，Σ 不得超全局 */}
          <Tabs.TabPanel value="caps" label="仓位上限">
            <Form.FormItem label="全局持仓上限（0=不设限）">
              <InputNumber value={cfgMaxPos} min={0} step={1} placeholder="0=不设限" onChange={(v) => setCfgMaxPos(v || 0)} style={{ width: 240 }} />
            </Form.FormItem>
            <div style={{ fontSize: 12, color: 'var(--app-muted)', margin: '8px 0' }}>每池持仓上限（0=不单独设限）。Σ ≤ 全局。不影响资金分配。</div>
            {pools.map((p) => (
              <Form.FormItem key={'sc-' + p.key} label={p.label}>
                <InputNumber
                  value={cfgCaps[p.key]}
                  min={0} step={1}
                  placeholder={p.max_pos > 0 ? '当前 ' + p.max_pos : '不单独设限'}
                  onChange={(v) => setCfgCaps({ ...cfgCaps, [p.key]: v || 0 })}
                  style={{ width: 240 }}
                />
              </Form.FormItem>
            ))}
          </Tabs.TabPanel>
          {/* §F-4 撮合设置标签页：账户级模拟盘参数（总开关/自动卖出/单笔资金/做空池预算）。
              旧缺陷：这些参数只能手改 config.json 并重启进程才生效（无端点、热同步函数死代码）。 */}
          <Tabs.TabPanel value="engine" label="撮合设置">
            {/* §P2-J 角标与红条分工：红条列「哪几条腿坏了」，本角标把失败态放到正在读的表单旁边。
                旧写法只有 engCfg 为 null 时的占位文案，而占位文案不点名原因（未知错误被写成一句通用提示），
                且 legs 失败后仍可能有上一轮读数时根本走不到占位分支 ⇒ 读数旁边必须独立显式。 */}
            {legFailFlag('撮合配置', '读取失败 · 表单不可回填')}
            {!engCfg ? (
              <div style={{ padding: '6px 2px', color: 'var(--app-text-2)', fontSize: 12 }}>撮合配置加载失败，请关闭弹窗重试</div>
            ) : (
              <>
                <div style={{ fontSize: 12, color: 'var(--app-muted)', marginBottom: 8 }}>
                  账户级撮合参数，保存后立即生效（无需重启）。总开关关闭时买入信号只提醒、不撮合。
                </div>
                {/* §F-4（20260917）：这些表单项不用 Form.FormItem 包裹——tdesign-react(v1.18)的
                    FormItem 在脱离 <Form> 时会把无 name 子控件的受控 checked/value 强制改写为
                    formValue(undefined)，导致勾选态/数值回填全部丢失（e2e 实锤：表格外 same-props
                    checkbox 勾选正确、FormItem 内恒 false）。与「战法开关」tab 同构用纯 div 布局。 */}
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 0' }}>
                  <span>模拟盘总开关</span>
                  <Checkbox checked={!!engCfg.enabled} onChange={(v) => setEngCfg({ ...engCfg, enabled: !!v })}>{'启用自动撮合'}</Checkbox>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 0' }}>
                  <span>自动卖出</span>
                  <Checkbox checked={!!engCfg.auto_sell} onChange={(v) => setEngCfg({ ...engCfg, auto_sell: !!v })}>{'止盈止损/清仓告警自动平仓'}</Checkbox>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 0' }}>
                  <span>每票固定买入资金（元）</span>
                  <InputNumber value={engCfg.fixed_amount} min={0} step={1000} onChange={(v) => setEngCfg({ ...engCfg, fixed_amount: v || 0 })} style={{ width: 240 }} />
                </div>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 0' }}>
                  <span>做空池预算（元，0=关闭做空侧）</span>
                  <InputNumber value={engCfg.short_capital} min={0} step={10000} onChange={(v) => setEngCfg({ ...engCfg, short_capital: v || 0 })} style={{ width: 240 }} />
                </div>
              </>
            )}
          </Tabs.TabPanel>
          {/* §SIGNAL_CONTROLLER 战法开关标签页：模拟盘买入准入白名单（与实盘量化页开关同构语义） */}
          <Tabs.TabPanel value="strategies" label="战法开关">
            {/* §P2-J 战法开关清单腿失败：stratKnown 不被改写 ⇒ 这里显示的仍是上一轮勾选，
                不标注就会被当成后端当前白名单（保存按钮还会把这轮旧勾选写回后端）。 */}
            {legFailFlag('战法开关清单')}
            <div style={{ fontSize: 12, color: 'var(--app-muted)', marginBottom: 8 }}>
              只有打开的战法产生的买入信号会被模拟盘撮合；关闭的战法信号仅提示不建仓。
              动量战法永不在默认全集内——需在此显式开启。卖出/止盈止损不受开关限制（不拦退出）。
              {stratShadow ? '（黑名单观察期：命中只记录不拦截）' : ''}
            </div>
            {stratKnown.map((v) => (
              <div key={'st-' + v.id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '6px 0' }}>
                <span>{v.name} <span style={{ color: 'var(--app-muted)', fontSize: 12 }}>({v.id})</span></span>
                <Checkbox checked={!!stratOn[v.id]} onChange={(c) => setStratOn({ ...stratOn, [v.id]: c })}>{'允许'}</Checkbox>
              </div>
            ))}
          </Tabs.TabPanel>
          {/* 买入纪律标签页：逐池配置日限次数/冷却/最低评分/日预算%，控制买入频率 */}
          <Tabs.TabPanel value="rules" label="买入纪律">
            <div style={{ fontSize: 12, color: 'var(--app-muted)', marginBottom: 8 }}>
              每池买入纪律：日限次数 / 冷却分钟 / 最低评分 / 日预算%。全 0 = 不设限；寻优审批会自动把门槛写入对应池的「最低评分」。
            </div>
            <Select value={cfgRuleSel} onChange={(v) => setCfgRuleSel(v)} style={{ width: 240, marginBottom: 8 }}>
              {pools.map((p) => <Select.Option key={'sel-' + p.key} value={p.key}>{p.label}</Select.Option>)}
            </Select>
            {cfgRules[cfgRuleSel] && (
              <div>
                <div style={{ marginBottom: 8 }}>
                  {poolLabel(cfgRuleSel)}
                  {poolCurrentRule(cfgRuleSel) && <span style={{ color: 'var(--app-muted)' }}>（当前生效：{poolCurrentRuleText(cfgRuleSel)}）</span>}
                </div>
                {/* 买入纪律四字段网格：日限买 / 冷却分钟 / 最低评分 / 日预算百分比 */}
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                  <Form.FormItem label="日限买">
                    <InputNumber value={cfgRules[cfgRuleSel].max_daily_buys} min={0} step={1} placeholder="0=不限" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], max_daily_buys: v || 0 } })} />
                  </Form.FormItem>
                  <Form.FormItem label="冷却(分)">
                    <InputNumber value={cfgRules[cfgRuleSel].cooldown_minutes} min={0} step={5} placeholder="0=不限" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], cooldown_minutes: v || 0 } })} />
                  </Form.FormItem>
                  <Form.FormItem label="最低分">
                    <InputNumber value={cfgRules[cfgRuleSel].min_score} min={0} max={100} step={1} placeholder="0=不过滤" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], min_score: v || 0 } })} />
                  </Form.FormItem>
                  <Form.FormItem label="日预算%">
                    <InputNumber value={cfgRules[cfgRuleSel].budget_pct_per_day} min={0} max={100} step={5} placeholder="0=不限" onChange={(v) => setCfgRules({ ...cfgRules, [cfgRuleSel]: { ...cfgRules[cfgRuleSel], budget_pct_per_day: v || 0 } })} />
                  </Form.FormItem>
                </div>
              </div>
            )}
          </Tabs.TabPanel>
        </Tabs>
        {/* 设置表单校验告警行：资金超额/上限超额/白名单全空等提示 */}
        {cfgWarn && <div style={{ color: 'var(--td-warning-color)', marginTop: 8 }}>{cfgWarn}</div>}
      </Dialog>

      {/* 分仓资金池条 */}
      {enabled && pools.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, marginBottom: 12 }}>
          <span style={{ color: 'var(--app-muted)', fontSize: 13 }}>分仓资金池</span>
          {/* 「全部」标签：清空池筛选，展示全账户持仓总数 */}
          <Tag
            style={{ cursor: 'pointer', background: activePool === null ? 'var(--td-brand-color)' : undefined, color: activePool === null ? '#ffffff' : undefined, borderColor: activePool === null ? 'var(--td-brand-color)' : undefined }}
            onClick={() => setActivePool(null)}
          >
            全部（{positions.length} 仓）
          </Tag>
          {/* 逐池标签：累计涨跌幅（红涨绿跌）/剩余现金/资金占比/仓位数；点击选中，再点取消 */}
          {pools.map((p) => {
            const key = normPoolKey(p.key)
            const active = activePool === key
            return (
              <Tag
                key={p.key}
                style={{ cursor: 'pointer', background: active ? 'var(--td-brand-color)' : undefined, color: active ? '#ffffff' : undefined, borderColor: active ? 'var(--td-brand-color)' : undefined }}
                onClick={() => setActivePool(active ? null : key)}
              >
                {p.label} <span style={{ color: p.return_pct >= 0 ? UP : DOWN }}>{(p.return_pct >= 0 ? '+' : '') + p.return_pct.toFixed(2)}%</span> · ¥{fmt(p.cash)} · {p.ratio_pct.toFixed(1)}%·{p.positions}仓
              </Tag>
            )
          })}
          {/* 选中池时显示「清盘本池」：仅平仓该池持仓并回补池现金，不影响其他池 */}
          {activePool !== null && (
            <Button size="small" theme="warning" disabled={!enabled} onClick={confirmPoolReset}>清盘本池</Button>
          )}
        </div>
      )}

      {/* 统计范围标签 */}
      {enabled && activePool !== null && (
        <div style={{ marginBottom: 8 }}><Tag>统计范围：{activePoolLabel}</Tag></div>
      )}

      {/* 绩效统计卡（§安全 F2，2026-08-29）：含自动估值的总资产/市值/已实现盈亏，仅管理员可见；
          普通用户纯手动记账视图隐藏，避免泄露自动估值口径。 */}
      {activeStats && isAdmin && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, marginBottom: 12 }}>
          <StatCard label="总资产">¥{fmt(activeStats.total_value)}</StatCard>
          <StatCard label="总收益">
            <span style={{ color: activeStats.total_return_pct >= 0 ? UP : DOWN }}>
              {(activeStats.total_return_pct >= 0 ? '+' : '') + activeStats.total_return_pct.toFixed(2)}%
            </span>
            <em style={{ fontSize: 12, color: 'var(--app-muted)', fontStyle: 'normal' }}> 基于累计投入 ¥{fmt(activeStats.initial_capital)}</em>
          </StatCard>
          <StatCard label="当日收益">
            <span style={{ color: activeStats.today_return_pct >= 0 ? UP : DOWN }}>
              {(activeStats.today_return_pct >= 0 ? '+' : '') + activeStats.today_return_pct.toFixed(2)}%
            </span>
          </StatCard>
          <StatCard label="现金">¥{fmt(activeStats.cash)}</StatCard>
          <StatCard label="持仓市值 / 已实现盈亏">
            ¥{fmt(activeStats.market_value)}
            <em style={{ fontSize: 12, color: activeStats.realized_pnl >= 0 ? UP : DOWN, fontStyle: 'normal' }}>
              {' '}{(activeStats.realized_pnl >= 0 ? '+' : '')}¥{fmt(activeStats.realized_pnl)}
            </em>
          </StatCard>
          {/* §P3-FE P12/P13（§0929 ⑧「亚单位不取整」同口径）：胜率原先 toFixed(0)，
              99.6% 显示成 100%、0.4% 显示成 0%——胜率是把「几胜几负」折成一个数的判据，
              取整后 100% 会让人以为零亏损。现保留一位小数；win_rate_pct 非数（老读数/缺字段）
              如实显示「—」而不是崩在 .toFixed 上。
              整格套一枚 data-testid（不是只框数字）：用例要同时断主读数与副读数「/ N仓」还在同一格里，
              否则把整格换成缺数占位也能骗过只数数字的断言。 */}
          <StatCard label="已平仓胜率">
            <span data-testid="paper-win-rate">
              {Number.isFinite(Number(activeStats.win_rate_pct)) ? Number(activeStats.win_rate_pct).toFixed(1) + '%' : '—'}
              {' '}<em style={{ fontSize: 12, color: 'var(--app-muted)', fontStyle: 'normal' }}>/ {activeStats.open_positions}仓</em>
            </span>
          </StatCard>
        </div>
      )}

      {/* §P2-J O2：KPI 卡（来自账户状态腿的服务端现值）与下方三张表（各自一条腿）不同源时，
          必须在读数旁边讲明白「哪一侧是上一轮」，否则用户看到市值 12 万、表内合计 11 万只会怀疑账坏了。
          账户状态腿自己失败也走这一条（那时上方 KPI 卡整块是上一轮的 stats）。 */}
      {(loadFails['持仓'] || loadFails['成交'] || loadFails['委托'] || loadFails['账户状态']) && (
        <div style={{ marginBottom: 8, fontSize: 12, color: 'var(--app-up)' }} data-testid="paper-kpi-mismatch">
          ⚠ 上方绩效卡与下方列表**不是同一轮读数**：
          {loadFails['账户状态'] ? '账户状态（含绩效统计）读取失败，绩效卡是上一轮读数；' : ''}
          {loadFails['持仓'] ? '持仓表是上一轮读数；' : ''}
          {loadFails['成交'] ? '成交日志是上一轮读数；' : ''}
          {loadFails['委托'] ? '委托记录是上一轮读数；' : ''}
          两边数字不一致属预期，请先修复链路再对账。
        </div>
      )}

      {/* 信号质量统计卡（仅联动版） */}
      {activeStats && isAdmin && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, marginBottom: 12 }}>
          <StatCard label="已撮合买入信号">{activeStats.filled_buys}</StatCard>
          <StatCard label="平均成交延迟">{activeStats.avg_latency_sec}s <em style={{ fontSize: 12, color: 'var(--app-muted)', fontStyle: 'normal' }}>最大 {activeStats.max_latency_sec}s</em></StatCard>
          <StatCard label="平均滑点（成交 vs 信号价）">
            <span style={{ color: activeStats.avg_slippage_pct >= 0 ? UP : DOWN }}>
              {(activeStats.avg_slippage_pct >= 0 ? '+' : '') + activeStats.avg_slippage_pct.toFixed(2)}%
            </span>
          </StatCard>
          <StatCard label="滑点累计成本">
            <span style={{ color: activeStats.slippage_cost >= 0 ? UP : DOWN }}>
              {(activeStats.slippage_cost >= 0 ? '+' : '')}¥{fmt(activeStats.slippage_cost)}
            </span>
            <em style={{ fontSize: 12, color: 'var(--app-muted)', fontStyle: 'normal' }}> 占初始 {activeStats.signal_amount_pct.toFixed(2)}%</em>
          </StatCard>
        </div>
      )}

      {/* 净值曲线 */}
      {isAdmin && (
        <Card title={<span>净值曲线 <em style={{ color: 'var(--app-muted)', fontSize: 12, fontStyle: 'normal' }}>（{stats?.equity_curve_points || 0} 个交易日）</em></span>} style={{ marginBottom: 12 }}>
          {/* §P2-J O1：净值腿失败角标（曲线本身是上一轮点位） */}
          {legFailFlag('净值曲线')}
          {equity.length > 1 ? (
            <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" style={{ width: '100%', height: H }}>
              {/* 净值折线主体：linePoints 由 equity 序列归一化映射到 viewBox 坐标 */}
              <polyline points={linePoints} fill="none" stroke="#FF4D4F" strokeWidth="2" />
              {/* 三条水平参考网格线（画布 1/4、2/4、3/4 高度） */}
              {gridLines.map((lvl) => <line key={lvl.y} x1="0" y1={lvl.y} x2={W} y2={lvl.y} style={{ stroke: 'var(--app-divider)' }} />)}
            </svg>
          ) : <div className="muted" style={{ padding: 24, textAlign: 'center' }}>{loadFails['净值曲线'] ? '净值读取失败，且本地没有上一轮点位' : '净值数据不足（自动撮合开启并产生成交后显示）'}</div>}
        </Card>
      )}

      {/* §SHORT-4 融券做空卡：做空池启用时显示（负持仓/担保/利息/权益 + 手动买回） */}
      {shortBook?.enabled && (
        <Card title={<span>融券做空 <em style={{ color: 'var(--app-muted)', fontSize: 12, fontStyle: 'normal' }}>（独立做空池 · 做空战法信号自动开仓 · T+1 可平）</em></span>} style={{ marginBottom: 12 }}>
          {/* 做空池权益指标卡组：权益/可用现金/冻结保证金/浮动与已实现盈亏/累计融券利息 */}
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, marginBottom: 8 }}>
            <StatCard label="做空权益">¥{fmt(shortBook.equity)}</StatCard>
            <StatCard label="池内可用现金">¥{fmt(shortBook.cash)}</StatCard>
            <StatCard label="冻结保证金">¥{fmt(shortBook.margin_used)}</StatCard>
            <StatCard label="浮动盈亏">
              <span style={{ color: shortBook.floating_pnl >= 0 ? UP : DOWN }}>
                {(shortBook.floating_pnl >= 0 ? '+' : '')}¥{fmt(shortBook.floating_pnl)}
              </span>
            </StatCard>
            <StatCard label="已实现盈亏">
              <span style={{ color: shortBook.realized >= 0 ? UP : DOWN }}>
                {(shortBook.realized >= 0 ? '+' : '')}¥{fmt(shortBook.realized)}
              </span>
            </StatCard>
            <StatCard label="已计融券利息">¥{fmt(shortBook.fee_accrued)}</StatCard>
          </div>
          {/* 空头持仓表：欠券数/开仓价/现价/浮动盈亏（§E1：盈亏与百分比均为后端 float_pnl/float_pnl_pct 直读） */}
          {(shortBook.positions || []).length ? (
            <Table rowKey="code" size="small" data={shortBook.positions}
              columns={[
                { colKey: 'code', title: '代码', width: 90, cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
                { colKey: 'name', title: '名称', width: 100 },
                { colKey: 'strategy', title: '触发战法', width: 120, cell: ({ row }) => <Tag theme="danger" size="small">{row.strategy}</Tag> },
                { colKey: 'qty', title: '欠券数', width: 80 },
                { colKey: 'open_price', title: '开仓价', width: 90, cell: ({ row }) => row.open_price?.toFixed(2) },
                { colKey: 'mark', title: '现价', width: 90, cell: ({ row }) => row.mark?.toFixed(2) },
                { colKey: 'float', title: '浮动盈亏', width: 130, cell: ({ row }) => {
                  // §E1 盈亏单轨：直接展示后端算好的 float_pnl/float_pnl_pct
                  // （ShortPosition 快照出口侧由 FloatPnl()/FloatPnlPct() 统一重算）。
                  // 旧版此处前端自写「(开仓价−现价)×数量−费用」是第二套账，公式一改两侧漂移。
                  const pnl = row.float_pnl ?? 0
                  const pct = row.float_pnl_pct ?? 0
                  return (
                    <span style={{ color: pnl >= 0 ? UP : DOWN }}>
                      {(pnl >= 0 ? '+' : '')}¥{fmt(pnl)} <em style={{ fontStyle: 'normal', fontSize: 12 }}>({pct.toFixed(1)}%)</em>
                    </span>
                  )
                } },
                { colKey: 'fee', title: '已计息', width: 80, cell: ({ row }) => fmt(row.fee_accrued || 0) },
                // 操作列：手动买回平仓（qty 传 0=全额买回），仅管理员可操作
                { colKey: 'op', title: '操作', width: 90, cell: ({ row }) => (
                  <Button size="small" variant="outline" theme="primary" disabled={!isAdmin}
                    onClick={async () => {
                      try { const r = await api.shortCoverPaper(row.code, 0); showToast(`买回成交，实现盈亏 ${r.realized >= 0 ? '+' : ''}\u00a5${fmt(r.realized)}`, 'success'); load() }
                      catch (e) { showToast(e.message || '买回失败', 'error') }
                    }}>买回平仓</Button>
                ) },
              ]} />
          ) : <div className="muted" style={{ padding: 8 }}>暂无融券空头（做空战法信号触发后自动开仓）</div>}
        </Card>
      )}

      {/* 持仓 / 成交 / 订单 */}
      <Tabs value={tab} onChange={(v) => setTab(v)} style={{ marginBottom: 8 }}>
        {/* 当前持仓面板：模拟盘持仓列表，含分时图展开行 */}
        <Tabs.TabPanel value="positions" label={`当前持仓 (${filteredPositions.length})`}>
          <Card>
            {/* §P2-J O1：本腿失败角标（与页顶红条同源，读数的旁边必须可见） */}
            {legFailFlag('持仓')}
            {/* 持仓表格：代码/名称/数量/成本/现价/盈亏/战法评分/止盈止损/移动止盈 */}
            {posData.length ? (
              <Table
                rowKey="__key"
                data={posData}
                columns={posColumns}
                //  行展开渲染分时图 
                expandedRow={renderKline}
                expandedRowKeys={[...klineOpen]}
                onExpandChange={(keys) => setKlineOpen(new Set(keys))}
                // 移动端整行点击打开底部操作面板（桌面端不响应）
                onRowClick={({ row }) => onRowTap(row)}
                bordered
                size="small"
                // §F1 长列表固定表头
                fixedHeader
                maxHeight="calc(100vh - 360px)"
              />
            ) : (
              <div className="muted" style={{ padding: 24, textAlign: 'center' }}>
                {/* §P2-J O2：读取失败＋表内为空时不能说「暂无持仓」——那是把「没读到」讲成「真的没有」 */}
                {loadFails['持仓'] ? '持仓读取失败，且本地没有上一轮读数（请先修复链路，再看此处列表）' : (isAdmin ? '暂无持仓（出现可开仓信号时按实时价自动买入）' : '暂无持仓（在信号页点「模拟买入」，或上方加仓/减仓管理已有持仓）')}
              </div>
            )}
          </Card>
        </Tabs.TabPanel>
        {/* 成交日志面板：模拟盘成交记录 */}
        <Tabs.TabPanel value="trades" label={`成交日志 (${filteredTrades.length})`}>
          <Card>
            {/* §P2-J O1：本腿失败角标 */}
            {legFailFlag('成交')}
            {/* 成交表格：代码/方向/价格/数量/时间/状态 */}
            {tradeData.length ? (
              <Table
                rowKey="__key"
                data={tradeData}
                columns={tradeColumns}
                expandedRow={renderKline}
                expandedRowKeys={[...klineOpen]}
                onExpandChange={(keys) => setKlineOpen(new Set(keys))}
                // 移动端整行点击打开底部操作面板（带序号便于展开对应分时）
                onRowClick={({ row }) => onTradeTap(row, row.__idx)}
                bordered
                size="small"
                // §F1 成交日志随时间无限增长：固定表头 + 分页，避免整页超长滚动
                fixedHeader
                maxHeight="calc(100vh - 360px)"
                pagination={{ defaultPageSize: 20, showJumper: true, pageSizeOptions: [20, 50, 100] }}
              />
            ) : <div className="muted" style={{ padding: 24, textAlign: 'center' }}>{loadFails['成交'] ? '成交日志读取失败，且本地没有上一轮读数' : '暂无成交记录'}</div>}
          </Card>
        </Tabs.TabPanel>
        {/* 订单记录面板：展示所有模拟交易订单 */}
        <Tabs.TabPanel value="orders" label={`订单 (${filteredOrders.length})`}>
          <Card>
            {/* §P2-J O1：本腿失败角标 */}
            {legFailFlag('委托')}
            {/* 订单表格：代码/方向/价格/数量/时间/状态 */}
            {orderData.length ? (
              <Table rowKey="__key" data={orderData} columns={orderColumns} bordered size="small"
                // §F1 订单记录固定表头 + 分页
                fixedHeader maxHeight="calc(100vh - 360px)"
                pagination={{ defaultPageSize: 20, showJumper: true, pageSizeOptions: [20, 50, 100] }} />
            ) : <div className="muted" style={{ padding: 24, textAlign: 'center' }}>{loadFails['委托'] ? '委托记录读取失败，且本地没有上一轮读数' : '暂无订单记录'}</div>}
          </Card>
        </Tabs.TabPanel>
      </Tabs>

      {/* 移动端：持仓行操作菜单 */}
      <Dialog visible={!!sheetPos} header={sheetPos ? sheetPos.code + ' ' + sheetPos.name : ''} onClose={() => setSheetPos(null)} footer={null}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {/* 操作按钮组：详情抽屉 / 分时切换 / 加仓 / 减仓 / 清仓 / 取消 */}
          <Button block theme="primary" onClick={() => { const p = sheetPos; setSheetPos(null); if (p) setDetail({ code: p.code, name: p.name }) }}>详情</Button>
          <Button block onClick={sheetKline}>{sheetPos && klineOpen.has(sheetPos.code) ? '收起分时' : '展开分时'}</Button>
          <Button block onClick={() => sheetTrade('add')}>加仓</Button>
          <Button block onClick={() => sheetTrade('trim')}>减仓</Button>
          <Button block theme="danger" onClick={() => sheetTrade('close')}>清仓</Button>
          <Button block onClick={() => setSheetPos(null)}>取消</Button>
        </div>
      </Dialog>
      {/* 移动端：成交行操作菜单 */}
      <Dialog visible={!!sheetTradeRow} header={sheetTradeRow ? sheetTradeRow.code + ' ' + sheetTradeRow.name : ''} onClose={() => setSheetTradeRow(null)} footer={null}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <Button block onClick={sheetTradeKline}>
            {sheetTradeRow && klineOpen.has('trade_' + sheetTradeRow.idx) ? '收起分时' : '展开分时'}
          </Button>
          <Button block onClick={() => setSheetTradeRow(null)}>取消</Button>
        </div>
      </Dialog>

      {/* 交易弹窗：加仓 / 减仓 / 清仓 */}
      <Dialog
        visible={tradeModal}
        header={(tradeDir === 'add' ? '加仓' : tradeDir === 'trim' ? '减仓' : '清仓') + (tradeTarget ? ' ' + tradeTarget.code + ' ' + tradeTarget.name : '')}
        onClose={() => setTradeModal(false)}
        onConfirm={confirmTrade}
        // 确认按钮：减仓超卖(tradeOverSell)时禁用；清仓按钮用危险色
        confirmBtn={{ content: '确定', disabled: tradeOverSell, theme: tradeDir === 'close' ? 'danger' : 'primary' }}
      >
        <Form layout="vertical">
          {/* 当前持仓回显：股数与成本价 */}
          <Form.FormItem label="当前持仓">
            <span>{tradeTarget?.qty} 股 / 成本 ¥{tradeTarget?.cost_price?.toFixed(2)}</span>
          </Form.FormItem>
          {/* 委托价输入：留空(0)则后端按实时价撮合 */}
          <Form.FormItem label="价格">
            <InputNumber value={tradeFormPrice} step={0.001} placeholder="成交价格（留空用实时价）" onChange={(v) => setTradeFormPrice(v || 0)} style={{ width: 240 }} />
          </Form.FormItem>
          {/* 手数输入：加/减仓可编辑（1手=100股），清仓固定为全部持仓 */}
          <Form.FormItem label={tradeDir === 'add' ? '加仓手数' : tradeDir === 'trim' ? '减仓手数' : '清仓'}>
            {tradeDir !== 'close'
              ? <InputNumber value={tradeFormQty} step={1} placeholder="手数（1手=100股）" onChange={(v) => setTradeFormQty(v || 1)} style={{ width: 240 }} />
              : <span>{tradeTarget?.qty} 股（全部）</span>}
          </Form.FormItem>
          {/* 减仓预览：按手数×100 实时计算减仓后剩余股数 */}
          {tradeDir === 'trim' && tradePreviewQty > 0 && (
            <div style={{ color: 'var(--td-warning-color)' }}>减仓后：剩余 {tradeTarget.qty - tradePreviewQty * 100} 股</div>
          )}
        </Form>
      </Dialog>

      {/* 模拟盘自检弹窗 */}
      {/* 模拟盘自检弹窗：展示开关/管理员/引擎路径/持仓/成交/文件状态等诊断信息 */}
      <Dialog visible={selfCheckOpen} header="模拟盘自检" onClose={() => setSelfCheckOpen(false)} footer={null} width={640}>
        {selfCheck && (
          <div style={{ fontSize: 13, lineHeight: 1.9 }}>
            {selfCheck.note && <div style={{ color: 'var(--td-warning-color)', marginBottom: 8 }}>{selfCheck.note}</div>}
            {/* 诊断信息表格：开关/管理员/引擎路径/成交/持仓/文件状态等关键指标 */}
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <tbody>
                {[
                  ['开关(enabled)', selfCheck.enabled],
                  ['是否管理员', selfCheck.is_admin],
                  ['引擎快照路径', selfCheck.engine_path || '—'],
                  ['是否发生过成交(has_filled)', selfCheck.has_filled],
                  ['当前持仓数', selfCheck.positions],
                  ['成交记录数', selfCheck.trades],
                  ['订单记录数', selfCheck.orders],
                  ['净值点数', selfCheck.equity_points],
                  ['资金池数', selfCheck.pools],
                  ['paper.json 存在', selfCheck.file_exists],
                  ['文件大小(byte)', selfCheck.file_size ?? '—'],
                  ['文件修改时间', selfCheck.file_mtime || '—'],
                  ['文件错误', selfCheck.file_error || '—'],
                ].map(([k, v]) => (
                  <tr key={k} style={{ borderBottom: '1px solid #eee' }}>
                    <td style={{ color: 'var(--app-text-2)', padding: '4px 8px', width: 200 }}>{k}</td>
                    <td style={{ padding: '4px 8px', fontFamily: 'monospace', wordBreak: 'break-all' }}>{String(v)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {/* 各资金池持仓分布表：展示每个战法资金池的持仓数与可用现金 */}
            {Array.isArray(selfCheck.pool_detail) && selfCheck.pool_detail.length > 0 && (
              <div style={{ marginTop: 12 }}>
                <div style={{ fontWeight: 600, marginBottom: 4 }}>各资金池持仓分布</div>
                <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                  <thead>
                    <tr style={{ color: 'var(--app-text-2)', textAlign: 'left' }}>
                      <th style={{ padding: '4px 8px' }}>池(key)</th>
                      <th style={{ padding: '4px 8px' }}>名称</th>
                      <th style={{ padding: '4px 8px' }}>持仓数</th>
                      <th style={{ padding: '4px 8px' }}>可用现金</th>
                    </tr>
                  </thead>
                  <tbody>
                    {selfCheck.pool_detail.map((p, i) => (
                      <tr key={i} style={{ borderBottom: '1px solid #eee' }}>
                        <td style={{ padding: '4px 8px', fontFamily: 'monospace' }}>{p.strategy}</td>
                        <td style={{ padding: '4px 8px' }}>{p.name}</td>
                        <td style={{ padding: '4px 8px' }}>{p.positions}</td>
                        <td style={{ padding: '4px 8px', fontFamily: 'monospace' }}>{fmt(p.cash)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            <div style={{ marginTop: 10, color: 'var(--app-muted-2)' }}>提示：若「当前持仓数」&gt;0 但前端「全部」页为空，多半是前端拉取逻辑问题（已修复）；若持仓与文件都为空，则确属无数据。</div>
          </div>
        )}
      </Dialog>

      {/* §D-1（GAP_VERIFY_20260917_PM）夜间信号质量报告弹窗：researchd 每晚 paper-research 落库的
          trades 聚合 / 归因 / 情绪相位（旧数据行 summary 解析失败时降级显示原文）。 */}
      <Dialog visible={nReportsOpen} header="夜间信号质量报告（researchd）" onClose={() => setNReportsOpen(false)} footer={null} width={720}>
        {nReports.length === 0 ? (
          <div style={{ color: 'var(--app-muted-2)', fontSize: 13, padding: '12px 0' }}>暂无报告——researchd 夜间任务跑过 paper-research 步骤后生成。</div>
        ) : (
          <div style={{ fontSize: 13 }}>
            {/* 日期选择条：倒序报告列表，点选切换正文 */}
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 10 }}>
              {nReports.map((r, i) => (
                <Tag key={r.date + '_' + i} theme={i === nReportSel ? 'primary' : 'default'}
                  style={{ cursor: 'pointer' }} onClick={() => setNReportSel(i)}>{r.date}</Tag>
              ))}
            </div>
            {(() => {
              const cur = nReports[nReportSel] || nReports[0]
              const s = cur && cur.summary
              if (!s) return <div style={{ wordBreak: 'break-all', fontFamily: 'monospace', color: 'var(--app-text-2)' }}>{cur?.summary_text || '（正文缺失）'}</div>
              return (
                <div>
                  <div style={{ color: 'var(--app-muted-2)', marginBottom: 8 }}>生成于 {String(s.generated_at || cur.created_at || '—')}</div>
                  {/* 成交聚合表：按战法池类型+方向（笔数/金额/均价/滑点/延迟） */}
                  <div style={{ fontWeight: 600, marginBottom: 4 }}>成交聚合（战法×方向）</div>
                  {Array.isArray(s.trades) && s.trades.length > 0 ? (
                    <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: 12 }}>
                      <thead>
                        <tr style={{ color: 'var(--app-text-2)', textAlign: 'left' }}>
                          {['战法', '方向', '笔数', '金额', '均价', '滑点%', '延迟s'].map((h) => <th key={h} style={{ padding: '3px 6px' }}>{h}</th>)}
                        </tr>
                      </thead>
                      <tbody>
                        {s.trades.map((t, i) => (
                          <tr key={i} style={{ borderBottom: '1px solid #eee' }}>
                            <td style={{ padding: '3px 6px' }}>{t.strategy_type || '其他/手动'}</td>
                            <td style={{ padding: '3px 6px' }}>{t.side === 'sell' ? '卖出' : t.side === 'buy' ? '买入' : t.side}</td>
                            <td style={{ padding: '3px 6px' }}>{t.count}</td>
                            <td style={{ padding: '3px 6px', fontFamily: 'monospace' }}>{fmt(t.total_amount)}</td>
                            <td style={{ padding: '3px 6px', fontFamily: 'monospace' }}>{Number(t.avg_price || 0).toFixed(2)}</td>
                            <td style={{ padding: '3px 6px', fontFamily: 'monospace', color: (t.avg_slippage || 0) > 0 ? 'var(--app-up)' : 'var(--app-down)' }}>{(t.avg_slippage || 0).toFixed(3)}</td>
                            <td style={{ padding: '3px 6px', fontFamily: 'monospace' }}>{(t.avg_latency || 0).toFixed(1)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  ) : <div style={{ color: 'var(--app-muted-2)', marginBottom: 12 }}>无成交分组（研究库窗口内 paper_trades 为空）。</div>}
                  {/* 归因表：用户+战法的信号→成交承接质量（admin 全账号视角） */}
                  {Array.isArray(s.attribution) && s.attribution.length > 0 && (
                    <>
                      <div style={{ fontWeight: 600, marginBottom: 4 }}>信号承接归因</div>
                      <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: 12 }}>
                        <thead>
                          <tr style={{ color: 'var(--app-text-2)', textAlign: 'left' }}>
                            {['账号', '战法', '笔数', '买/卖', '滑点%', '延迟s'].map((h) => <th key={h} style={{ padding: '3px 6px' }}>{h}</th>)}
                          </tr>
                        </thead>
                        <tbody>
                          {/* §0929GATE-CONTRACT（FIX_PLAN_20260929 ⑨-3）这里的 a.xxx ?? a.Xxx 两态兜底
                              不是防御性冗余，而是契约本身：报告正文的 attribution 是原样透传容器
                              （store.PaperAttribution 无 json tag ⇒ PascalCase），而同族报告的其他键是
                              snake_case。删掉任一态都会让另一代报告行整列显示 undefined。
                              English: both key forms are read on purpose — this container's keys follow
                              the producing side (PascalCase) while the rest of the report is snake_case. */}
                          {s.attribution.map((a, i) => (
                            <tr key={i} style={{ borderBottom: '1px solid #eee' }}>
                              <td style={{ padding: '3px 6px', fontFamily: 'monospace' }}>{a.user_id || a.UserID}</td>
                              <td style={{ padding: '3px 6px' }}>{a.strategy || a.Strategy}</td>
                              <td style={{ padding: '3px 6px' }}>{a.count ?? a.Count}</td>
                              <td style={{ padding: '3px 6px' }}>{a.buy_count ?? a.BuyCount}/{a.sell_count ?? a.SellCount}</td>
                              <td style={{ padding: '3px 6px', fontFamily: 'monospace' }}>{Number(a.avg_slippage ?? a.AvgSlippage ?? 0).toFixed(3)}</td>
                              <td style={{ padding: '3px 6px', fontFamily: 'monospace' }}>{Number(a.avg_latency ?? a.AvgLatency ?? 0).toFixed(1)}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </>
                  )}
                  {/* 情绪相位锚点：最近阶段 + 分布天数 */}
                  {s.emotion && (s.emotion.last_phase || s.emotion.phase_days) && (
                    <div style={{ color: 'var(--app-text-2)' }}>
                      情绪相位（近 {s.emotion.days} 交易日）：最近 <b>{String(s.emotion.last_phase || '—')}</b>
                      {s.emotion.phase_days && Object.entries(s.emotion.phase_days).map(([p, n]) => ` ${p} ${n}天`).join('')}
                    </div>
                  )}
                </div>
              )
            })()}
          </div>
        )}
      </Dialog>

      {/* §F3 全局个股详情抽屉：代码点开，实时价 + 分时/盘口 + 该标的持仓 */}
      <StockDetailDrawer open={!!detail} code={detail?.code} name={detail?.name}
        related={{ positions }} onClose={() => setDetail(null)} />
    </div>
  )
}
