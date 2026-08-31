// ── 自动研究页面 Research.jsx ──
// 研究候选审批、战法库管理、回测任务中心、参数寻优与资金池纪律配置。
// 全量使用 TDesign React 组件（Card / Table / Tag / Button / Dialog / Tabs / Select / Input）。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import {
  Card, Table, Tag, Button, Dialog, DialogPlugin, Select, Input, InputNumber,
  Tabs, MessagePlugin,
} from 'tdesign-react'
import ToggleSw from '../components/ToggleSw'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

// 封装 tdesign 确认对话框为 Promise（与 Admin/Paper 保持一致）；用户确认时 resolve(true)，关闭时 resolve(false)
function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    const d = DialogPlugin.confirm({
    // 常量 d：局部定义
      header,
      body,
      theme: 'warning',
      onConfirm: () => { d.hide(); resolve(true) },
      onClose: () => { d.hide(); resolve(false) },
    })
  })
}

// 将小数格式化为带百分号的字符串（如 12.34%）
function fmtPctGlobal(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  return Number(v).toFixed(2) + '%'
}

/**
 * 自动研究页面组件
 * 管理研究候选审批、战法库、回测任务、参数寻优与研究设置。
 * @returns {JSX.Element}
 */
export default function Research() {
  const [loading, setLoading] = useState(false)
  const [candidates, setCandidates] = useState([])
  const [noDB, setNoDB] = useState(false)
  const [statusFilter, setStatusFilter] = useState('')
  const [progress, setProgress] = useState(null)
  const [library, setLibrary] = useState([])
  const [loadingLibrary, setLoadingLibrary] = useState(false)
  const [backtestLoading, setBacktestLoading] = useState({})
  const [backtestState, setBacktestState] = useState({})
  const [backtestResult, setBacktestResult] = useState({})
  const [backtestProgress, setBacktestProgress] = useState({})
  const [backtestJobs, setBacktestJobs] = useState([])
  const [btLoading, setBtLoading] = useState(false)
  const [btPickId, setBtPickId] = useState(0)
  const [btStart, setBtStart] = useState('')
  const [btEnd, setBtEnd] = useState('')
  const [btTopK, setBtTopK] = useState('')
  const [btMinStocks, setBtMinStocks] = useState('')
  const [activeTab, setActiveTab] = useState('candidates')
  const [renderError, setRenderError] = useState('')
  const [editingName, setEditingName] = useState({})
  const [nameDraft, setNameDraft] = useState({})
  const [backtestEnabled, setBacktestEnabled] = useState(false)

  const [candSubTab, setCandSubTab] = useState('patterns')
  const [optObjective, setOptObjective] = useState('profitFactor')
  const [optTasks, setOptTasks] = useState([])
  const [loadingOpts, setLoadingOpts] = useState(false)
  const [optLaunching, setOptLaunching] = useState(false)
  const [optSelected, setOptSelected] = useState('')
  const [optDrawerOpen, setOptDrawerOpen] = useState(false)
  const [schedStatus, setSchedStatus] = useState(null) // 研究调度可见性快照（为何卡排队）
  // 任务运行日志弹窗（前端直接看 researchd 落盘的 task_<id>.log，免去 SSH）
  const [logOpen, setLogOpen] = useState(false)
  const [logId, setLogId] = useState(0)
  const [logContent, setLogContent] = useState('')
  const [logExists, setLogExists] = useState(true)
  const [logLoading, setLogLoading] = useState(false)
  const logTimer = useRef(null)
  // 常量 logTimer：局部定义
  const [cfgSweep, setCfgSweep] = useState({})
  const [cfgRule, setCfgRule] = useState({})
  const [expandedStrategy, setExpandedStrategy] = useState('')
  const strategyRefs = useRef({})
  // 常量 strategyRefs：局部定义
  const [showMetricHelp, setShowMetricHelp] = useState(false)
  const [adviceOpen, setAdviceOpen] = useState(0)

  const canApprove = api.hasPerm('research_approve')
  // 常量 canApprove：局部定义

  const tabs = [
  // 顶部主导航标签定义（待审批候选 / 战法库 / 回测 / 设置）
    { value: 'candidates', label: '待审批候选' },
    { value: 'library', label: '战法库' },
    { value: 'backtests', label: '回测' },
    { value: 'settings', label: '设置' },
  ]
  const builtinPatterns = [
  // 内置形态战法枚举（双响炮/龙头/龙回头/N形），用于回测与战法库归类
    { id: 'double_bump', name: '双响炮' },
    { id: 'dragon', name: '龙头' },
    { id: 'dragon_return', name: '龙回头' },
    { id: 'n_shape', name: 'N形（日K近似）' },
  ]

  // 轮询 / 计时器：backtestPollers 按候选 id 缓存单候选回测轮询；libPollTimer 为战法库回测轮询；pollTimer 为研究进度轮询
  const backtestPollers = useRef({})
  const libPollTimer = useRef(null)
  // 常量 libPollTimer：局部定义
  const pollTimer = useRef(null)
  // 常量 pollTimer：局部定义

  const setStrategyRef = (key) => (el) => { if (el) strategyRefs.current[key] = el }
  // 生成设置各战法 ref 的回调，便于滚动定位到具体战法配置项

  // ===== 通用工具 =====
  function pct(v) {
    if (v === null || v === undefined || isNaN(v)) return 0
    return Math.min(100, Math.round(v * 100))
  }
  function fmtRows(v) {
  // 格式化计数：千分位展示样本/持仓等整数
    if (v === null || v === undefined || isNaN(v)) return '-'
    return Number(v).toLocaleString('zh-CN')
  }
  function fmt(v) {
  // 格式化数值：保留 4 位小数（因子/权重精度用）
    if (v === null || v === undefined || isNaN(v)) return '-'
    return Number(v).toFixed(4)
  }
  function signClass(v) {
  // 根据正负返回 pos/neg 样式类，用于盈亏着色
    if (v === null || v === undefined || isNaN(v)) return ''
    return Number(v) >= 0 ? 'pos' : 'neg'
  }
  function signColor(v) {
  // 根据正负返回红/绿颜色（红涨绿跌）
    if (v === null || v === undefined || isNaN(v)) return '#aaa'
    return Number(v) >= 0 ? '#e34d59' : '#00a870'
  }
  function fmtNum(v, digits) {
  // 格式化数值：指定小数位，空值返回占位符
    if (v === null || v === undefined || isNaN(v)) return '-'
    const d = digits === undefined ? 0 : digits
    // 常量 d：局部定义
    return Number(v).toFixed(d)
  }

  // ===== 因子元数据 =====
  const factorMeta = useRef({})
  async function loadFactorMeta() {
  // 加载全部因子元数据（id/名称/分类/说明）供战法规则渲染
    try {
      const res = await api.fetchResearchFactors()
      // 常量 res：局部定义
      if (res && Array.isArray(res.factors)) {
        for (const f of res.factors) {
          if (f && f.id) factorMeta.current[f.id] = f
        }
      }
    } catch (_) {}
  }
  function factorName(id) {
  // 将因子 id 转换为中文可读名称
    const m = factorMeta.current[id]
    // 常量 m：局部定义
    return (m && m.name) ? m.name : id
  }

  function weightList(c) {
  // 解析战法权重对象为可读的「因子名×权重」列表
    try {
      const w = JSON.parse(c.weights || '{}')
      // 常量 w：局部定义
      const wm = (w && typeof w === 'object' && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights)) ? w.weights : w
      // 常量 wm：局部定义
      if (!wm || typeof wm !== 'object') return []
      const entries = Object.entries(wm).filter(([, v]) => typeof v === 'number' && isFinite(v))
      // 常量 entries：局部定义
      return entries.sort((a, b) => b[1] - a[1])
    } catch (_) { return [] }
  }
  function depthSummary(c) {
  // 汇总盘口扫描深度指标为简要文案
    try { return JSON.parse(c.weights || '{}') } catch (_) { return {} }
  }
  function kindLabel(k) {
  // 将研究类型（factor/pattern/weights/depth）翻译为中文标签
    const m = { factor: '因子战法', pattern: '形态战法', weights: '权重优化', depth: '盘口扫描' }
    // 常量 m：局部定义
    return m[k] || k
  }
  function factorDirs(c) {
  // 解析因子方向（多/空）为可读文案
    try {
      const w = JSON.parse(c.weights || '{}')
      // 常量 w：局部定义
      if (w && typeof w === 'object' && w.directions && typeof w.directions === 'object') return w.directions
    } catch (_) {}
    return {}
  }
  function factorRule(c) {
  // 将因子规则对象渲染为可读的中文规则描述
    const dirs = factorDirs(c)
    // 常量 dirs：局部定义
    const wm = {}
    // 常量 wm：局部定义
    try {
      const w = JSON.parse(c.weights || '{}')
      // 常量 w：局部定义
      const weightsObj = (w && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights)) ? w.weights : w
      // 常量 weightsObj：局部定义
      Object.assign(wm, weightsObj)
    } catch (_) {}
    return Object.entries(wm)
      .filter(([, v]) => typeof v === 'number' && isFinite(v))
      .map(([id, weight]) => ({ id, label: factorName(id), weight, dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1 }))
      .sort((a, b) => b.weight - a.weight)
  }
  function parseReason(c, key) {
  // 解析命中原因字段，拆出模式/方向/阈值等子项
    const reason = c.reason || ''
    // 常量 reason：局部定义
    const pats = {
    // 常量 pats：局部定义
      '样本内IR': /样本内IR=(-?\d+\.?\d*)/,
      '样本外IR': /样本外IR=(-?\d+\.?\d*)/,
      反推超额: /反推超额=(-?\d+\.?\d*)/,
    }
    const m = reason.match(pats[key])
    // 常量 m：局部定义
    return m ? parseFloat(m[1]) : null
  }
  function fmtPct(v) {
  // 将 0~1 比率格式化为百分比字符串
    if (v === null || v === undefined || isNaN(v)) return '-'
    const s = (v * 100).toFixed(1)
    // 常量 s：局部定义
    return (v >= 0 ? '+' : '') + s + '%'
  }
  function verdict(c) {
  // 根据样本内/样本外阈值判定战法回测结论（通过/失败）
    const insample = parseReason(c, '样本内IR')
    // 常量 insample：局部定义
    const outsample = parseReason(c, '样本外IR')
    // 常量 outsample：局部定义
    const gen = parseReason(c, '反推超额')
    // 常量 gen：局部定义
    const thr = 0.3
    // 常量 thr：局部定义
    const passed = (insample !== null ? insample >= thr : true) &&
    // 常量 passed：局部定义
                   (outsample !== null ? outsample >= thr : true) &&
                   (gen !== null ? gen > 0 : true)
    if (passed) return { ok: true, text: '电脑用两段互不相干的历史行情分别验证过，这条规律都能跑赢，不是碰运气。' }
    return { ok: false, text: '这条规律在验证中没站稳，不建议直接拿来实盘。' }
  }
  function plainLines(c) {
  // 将候选理由多行文本拆分为纯文本行数组
    const insample = parseReason(c, '样本内IR')
    // 常量 insample：局部定义
    const outsample = parseReason(c, '样本外IR')
    // 常量 outsample：局部定义
    const gen = parseReason(c, '反推超额')
    // 常量 gen：局部定义
    const lines = []
    // 常量 lines：局部定义
    if (insample !== null) lines.push('先拿前半段历史行情回放：这套打分的选股效果明显（稳定度 ' + fmt(insample) + '，越高越靠谱）。')
    if (outsample !== null) lines.push('再拿一段完全没参与挑规律的行情回放：效果仍然明显（稳定度 ' + fmt(outsample) + '）。这一步是防止规律只对老数据灵、换市场就失灵。')
    if (gen !== null) lines.push('最后对比「按这套规律选出的股票」和「随便买」：选的比平均多赚 ' + fmtPct(gen) + '，说明规律确实挑得出好股票。')
    if (lines.length === 0) lines.push(/通过护栏/.test(c.reason || '') ? '电脑验证通过，这条规律可以试试。' : '这条规律未通过验证。')
    return lines
  }
  function btTested(c) {
  // 判断该候选是否已跑过回测
    if (typeof c.backtest_done === 'boolean') return c.backtest_done
    return c.avg_excess !== 0
  }
  function btPct(id) {
  // 根据回测结果计算并显示回测进度百分比
    const p = backtestProgress[id]
    // 常量 p：局部定义
    if (!p) return '0%'
    const n = parseInt(p, 10)
    // 常量 n：局部定义
    return (isNaN(n) ? 0 : Math.max(0, Math.min(100, n))) + '%'
  }

  // ===== 进度 / 候选 =====
  // 拉取研究处理进度（数据准备度、日线/财务行数、候选计数），更新进度卡片
  async function loadProgress() {
    try {
      const p = await api.fetchResearchProgress()
      // 常量 p：局部定义
      if (p) setProgress(p)
    } catch (e) { console.error('Research 进度加载失败', e) }
  }
  // 按当前状态筛选拉取研究候选列表；研究库未接入时进入 noDB 空状态
  async function loadData() {
    setLoading(true)
    try {
      const res = await api.fetchResearchCandidates(statusFilter || '')
      // 常量 res：局部定义
      if (res && Array.isArray(res.candidates)) {
        setCandidates(res.candidates)
        setNoDB(false)
      }
    } catch (e) {
      if (e && e.message && e.message.indexOf('研究库未接入') >= 0) {
        setNoDB(true)
        setCandidates([])
      } else { console.error('Research 加载失败', e) }
    } finally { setLoading(false) }
  }
  async function loadAll() {
  // 并行加载候选/战法库/回测/进度等全部研究数据
    setLoading(true)
    await loadProgress()
    await loadData()
    await loadSchedStatus()
    setLoading(false)
  }
  // 读取研究调度可见性快照：让前端直接展示"为何卡排队"（禁用/交易时段/内存闸门/槽位占用）。
  async function loadSchedStatus() {
    try {
      const res = await api.getSchedulerStatus()
      // 常量 res：局部定义
      setSchedStatus(res || null)
    } catch (e) { setSchedStatus(null) }
  }
  // 打开任务运行日志弹窗：拉取 task_<id>.log，运行中的任务每 4s 自动刷新。
  async function openLog(id) {
    setLogId(id); setLogOpen(true); setLogLoading(true)
    try {
      const res = await api.getResearchTaskLog(id)
      // 常量 res：局部定义
      setLogExists(res && res.exists !== false)
      setLogContent((res && res.log) || '')
    } catch (e) {
      setLogExists(false); setLogContent('')
    } finally { setLogLoading(false) }
    if (logTimer.current) clearInterval(logTimer.current)
    logTimer.current = setInterval(async () => {
      try {
        const res = await api.getResearchTaskLog(id)
        // 常量 res：局部定义
        setLogExists(res && res.exists !== false)
        setLogContent((res && res.log) || '')
      } catch (e) { /* 静默，关闭时清理 */ }
    }, 4000)
  }
  function closeLog() {
  // 关闭任务运行日志弹窗并停止轮询
    if (logTimer.current) { clearInterval(logTimer.current); logTimer.current = null }
    setLogOpen(false)
  }
  function statusLabel(s) {
  // 将候选审批状态翻译为中文标签
    const m = { proposed: '待审批', approved: '已审批', applied: '已应用', rejected: '已驳回' }
    // 常量 m：局部定义
    return m[s] || s
  }
  // 审批并通过接口应用某条研究候选（写回后端并热更新状态），权限不足时回退
  async function doApprove(c) {
    try {
      await api.approveResearchCandidate(c.id)
      c.status = 'applied'
      showToast('候选 #' + c.id + ' 已审批并应用', 'success')
    } catch (e) { showToast('审批失败: ' + (e.message || e), 'error') }
  }
  async function doReject(c) {
  // 驳回某研究候选（调用后端拒绝接口）
    try {
      await api.rejectResearchCandidate(c.id)
      c.status = 'rejected'
      showToast('候选 #' + c.id + ' 已驳回', 'success')
    } catch (e) { showToast('驳回失败: ' + (e.message || e), 'error') }
  }

  // ===== 战法库 =====
  function ruleFactors(s) {
    const dirs = s.directions || {}
    // 常量 dirs：局部定义
    const wm = s.weights || {}
    // 常量 wm：局部定义
    return Object.entries(wm)
      .filter(([, v]) => typeof v === 'number' && isFinite(v))
      .map(([id, weight]) => ({ id, label: factorName(id), weight, dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1 }))
      .sort((a, b) => b.weight - a.weight)
  }
  function condLabel(c) {
  // 将扫参条件（min/max/step）翻译为中文可读文案
    const name = factorName(c.factor || '')
    // 常量 name：局部定义
    const min = (c.min !== undefined && c.min !== null) ? c.min : '-∞'
    // 常量 min：局部定义
    const max = (c.max !== undefined && c.max !== null) ? c.max : '+∞'
    // 常量 max：局部定义
    return name + ' ∈ [' + min + ', ' + max + ')'
  }
  async function loadLibrary() {
  // 加载战法库列表与生效状态
    setLoadingLibrary(true)
    try {
      const res = await api.fetchResearchLibrary()
      // 常量 res：局部定义
      if (res && Array.isArray(res.library)) setLibrary(res.library)
    } catch (e) { console.error('战法库加载失败', e) }
    finally { setLoadingLibrary(false) }
  }
  async function toggleLibrary(s) {
  // 启用/禁用某条战法库记录
    try {
      await api.setResearchLibraryEnabled(s.id, !s.enabled)
      s.enabled = !s.enabled
      showToast('战法 ' + s.name + (s.enabled ? ' 已启用（已注入 8a/8b 实盘）' : ' 已停用'), 'success')
    } catch (e) { showToast('操作失败: ' + (e.message || e), 'error') }
  }
  async function removeLibrary(s) {
  // 删除某条战法库记录
    if (!(await confirmDialog('确定删除战法 ' + s.name + ' ？删除后不再注入 8a/8b 实盘。'))) return
    try {
      await api.deleteResearchLibrary(s.id)
      setLibrary((l) => l.filter((x) => x.id !== s.id))
      showToast(s.name + ' 已删除', 'success')
    } catch (e) { showToast('删除失败: ' + (e.message || e), 'error') }
  }
  function startRename(s) {
  // 进入某战法库的改名编辑态
    setEditingName((e) => ({ ...e, [s.id]: true }))
    setNameDraft((d) => ({ ...d, [s.id]: s.name }))
  }
  async function saveName(s) {
  // 保存某战法库记录的新名称
    const name = (nameDraft[s.id] || '').trim()
    // 常量 name：局部定义
    setEditingName((e) => ({ ...e, [s.id]: false }))
    if (!name || name === s.name) return
    try {
      await api.renameResearchLibrary(s.id, name)
      s.name = name
      showToast('战法已重命名为 ' + name, 'success')
    } catch (e) { showToast('改名失败: ' + (e.message || e), 'error') }
  }

  // ===== 回测开关 =====
  async function loadBacktestToggle() {
    try {
      const res = await api.fetchBacktestToggle()
      // 常量 res：局部定义
      if (res && typeof res.enabled === 'boolean') setBacktestEnabled(res.enabled)
    } catch (e) { console.error('加载回测开关失败', e) }
  }
  async function saveBacktestToggle(val) {
  // 保存全量回测全局开关状态
    try {
      await api.setBacktestToggle(val)
      showToast('全量回测全局开关已' + (val ? '开启' : '关闭'), 'success')
    } catch (e) {
      showToast('保存失败: ' + (e.message || e), 'error')
      setBacktestEnabled(!val)
    }
  }

  // ===== 单候选回测 + 轮询 =====
  // 对单条候选发起全链路回测，并启动进度轮询；接口调用携带开始/结束日期、选股数与最小样本数
  async function doBacktest(c) {
    if (backtestPollers.current[c.id]) return
    setBacktestLoading((b) => ({ ...b, [c.id]: true }))
    setBacktestState((b) => ({ ...b, [c.id]: 'running' }))
    try {
      await api.backtestResearchCandidate(c.id, {
        start: btStart.trim(),
        end: btEnd.trim(),
        top_k: parseInt(btTopK, 10) || 0,
        min_stocks: parseInt(btMinStocks, 10) || 0,
      })
    } catch (e) {
      setBacktestLoading((b) => ({ ...b, [c.id]: false }))
      setBacktestState((b) => ({ ...b, [c.id]: 'error' }))
      showToast('回测启动失败: ' + (e.message || e), 'error')
      return
    }
    pollBacktest(c)
  }
  async function doCancelBacktest(id) {
  // 取消（中断/置失效）某回测任务
    if (!(await confirmDialog('取消候选 #' + id + ' 的回测？（已算完的事件保留缓存，续跑只算剩余）'))) return
    try { await api.cancelBacktest(id); loadBacktests() } catch (e) { showToast('取消失败: ' + (e.message || e), 'error') }
  }
  async function doPauseBacktest(id) {
  // 暂停运行中的回测子进程（SIGSTOP）
    try { await api.pauseBacktest(id); loadBacktests() } catch (e) { showToast('暂停失败: ' + (e.message || e), 'error') }
  }
  async function doResumeBacktest(id) {
  // 恢复已暂停的回测（SIGCONT）
    try { await api.resumeBacktest(id); loadBacktests() } catch (e) { showToast('恢复失败: ' + (e.message || e), 'error') }
  }

  // ===== 寻优 =====
  function optObjectiveLabel(o) {
    return { profitfactor: '盈亏比', profitFactor: '盈亏比', winrate: '胜率', winRate: '胜率', avgwin: '平均盈利', avgWin: '平均盈利', expectancy: '期望收益' }[o] || (o || '-')
  }
  async function loadOptimizations() {
  // 加载参数寻优任务列表
    if (loadingOpts) return
    setLoadingOpts(true)
    try {
      const res = await api.fetchOptimizations()
      // 常量 res：局部定义
      setOptTasks(res.optimizations || [])
    } catch (e) { console.warn('寻优结果加载失败', e) }
    finally { setLoadingOpts(false) }
  }
  // 发起全库战法参数寻优，提交目标函数后进入研究队列（盘后窗口执行）
  async function startOptimize() {
    if (!(await confirmDialog('发起全库战法参数寻优？\n目标：' + optObjectiveLabel(optObjective) + '，盘后窗口执行，完成后排名出现在本页。'))) return
    try {
      await api.enqueueOptimize({ objective: optObjective })
      setOptLaunching(true)
      showToast('已加入研究队列——进度可在「回测」tab 查看，完成后回到「优化结果」刷新。', 'success')
    } catch (e) { showToast('发起失败: ' + (e.message || e), 'error') }
  }
  async function approveOpt(r) {
  // 审批通过某条寻优排名（写入 applied 配置并热加载）
    const msg = '把参数应用到「' + r.strategy + '」？\n止盈线 ' + fmtNum(r.params.take_profit_pct) + '% · 止损线 ' +
    // 常量 msg：局部定义
      fmtNum(r.params.stop_loss_pct) + '% · 兜底 ' + fmtNum(r.params.hold_days) + ' 天' +
      ((r.params || {}).min_score ? ' · 门槛 ' + fmtNum(r.params.min_score) : '') + '\n审批后立即热重载生效。'
    if (!(await confirmDialog(msg))) return
    try { await api.approveOptimization(r.id); r.status = 'approved'; showToast('已应用参数', 'success') } catch (e) { showToast('入库失败: ' + (e.message || e), 'error') }
  }
  async function rejectOpt(r) {
  // 淘汰某条寻优排名
    try { await api.rejectOptimization(r.id); r.status = 'rejected'; showToast('已淘汰', 'success') } catch (e) { showToast('操作失败: ' + (e.message || e), 'error') }
  }

  const optStrategies = useMemo(() => {
  // 常量 optStrategies：局部定义
    if (!optTasks.length) return []
    // §寻优修复：此前只取 optTasks[0].results——只显示最新任务的排名。现跨全部任务按战法聚合，
    // 同一战法保留最新任务中的冠军行（bestExp 最大），避免"只显示一个战法"或重复按钮。
    // English: was optTasks[0].results only (latest task). Now aggregates across all tasks per strategy,
    // keeping the best champion row (max expectancy) so every strategy shows up once.
    const byKey = new Map()
    for (const t of optTasks) {
      const rows = t.results || []
      // 常量 rows：局部定义
      for (const r of rows) {
        const key = r.strategy_kind || r.strategy
        const exp = (r.expectancy !== undefined && r.expectancy !== null) ? Number(r.expectancy) : null
        const prev = byKey.get(key)
        if (!prev || (exp !== null && (prev.bestExp === null || exp > prev.bestExp))) {
          byKey.set(key, { key, label: r.strategy, bestExp: exp })
        }
      }
    }
    return [...byKey.values()]
  }, [optTasks])
  const optCur = useMemo(() => {
  // 常量 optCur：局部定义
    for (const t of optTasks) {
      const hit = (t.results || []).find((r) => (r.strategy_kind || r.strategy) === optSelected)
      // 常量 hit：局部定义
      if (hit) return { ...hit, task_id: t.task_id }
    }
    return null
  }, [optTasks, optSelected])
  const optCurHeat = useMemo(() => {
  // 常量 optCurHeat：局部定义
    const empty = { tps: [], sls: [], map: {} }
    // 常量 empty：局部定义
    if (!optCur || !optCur.grid_json) return empty
    try {
      const extra = JSON.parse(optCur.grid_json)
      // 常量 extra：局部定义
      const cells = extra.grid || []
      // 常量 cells：局部定义
      const tps = [...new Set(cells.map((c) => Number(c.tp)))].sort((a, b) => a - b)
      // 常量 tps：局部定义
      const sls = [...new Set(cells.map((c) => Number(c.sl)))].sort((a, b) => a - b)
      // 常量 sls：局部定义
      const map = {}
      // 常量 map：局部定义
      cells.forEach((c) => { map[c.tp + '|' + c.sl] = c.expectancy })
      return { tps, sls, map }
    } catch { return empty }
  }, [optCur])
  function heatVal(tp, sl) {
  // 计算止盈/止损组合在热力图中的热度期望值
    const v = optCurHeat.map[tp + '|' + sl]
    // 常量 v：局部定义
    return (v === undefined || v === null) ? null : v
  }
  function heatColor(exp) {
  // 根据热度期望值返回热力图配色
    if (exp === null || exp === undefined || isNaN(Number(exp))) return 'transparent'
    const clamped = Math.max(-5, Math.min(5, exp))
    // 常量 clamped：局部定义
    const alpha = 0.12 + Math.abs(clamped) / 5 * 0.55
    // 常量 alpha：局部定义
    return clamped >= 0 ? `rgba(34,197,94,${alpha.toFixed(2)})` : `rgba(239,68,68,${alpha.toFixed(2)})`
  }
  const optCurBatches = useMemo(() => {
  // 常量 optCurBatches：局部定义
    if (!optCur || !optCur.grid_json) return []
    try { return JSON.parse(optCur.grid_json).batches || [] } catch { return [] }
  }, [optCur])
  const optCurPoolKey = useMemo(() => {
  // 常量 optCurPoolKey：局部定义
    if (!optCur) return ''
    if ((optCur.strategy_kind || '').startsWith('fac_')) return 'factor'
    if ((optCur.strategy_kind || '').startsWith('pat_')) return 'pattern'
    return ({ '双响炮': 'double_bump', '龙头': 'dragon', 'N形': 'n_shape', '龙回头': 'dragon_return' })[optCur.strategy] || ''
  }, [optCur])
  const sweepComboEstimate = useMemo(() => {
  // 常量 sweepComboEstimate：局部定义
    const c = cfgSweep
    // 常量 c：局部定义
    const nF = (f, t, s) => (s > 0 && t >= f) ? Math.round((t - f) / s) + 1 : 1
    // 常量 nF：局部定义
    const nI = (f, t, s) => (s > 0 && t >= f) ? Math.floor((t - f) / s) + 1 : 1
    // 常量 nI：局部定义
    return nF(+c.tp_from || 0, +c.tp_to || 0, +c.tp_step || 0) *
           nF(+c.sl_from || 0, +c.sl_to || 0, +c.sl_step || 0) *
           nI(+c.hold_from || 0, +c.hold_to || 0, +c.hold_step || 0) *
           nF(+c.score_from || 0, +c.score_to || 0, +c.score_step || 0)
  }, [cfgSweep])
  async function toggleDrawer() {
  // 打开/关闭参数寻优抽屉，初始化默认扫参范围
    if (optDrawerOpen) { setOptDrawerOpen(false); return }
    const def = { tp_from: 5, tp_to: 30, tp_step: 5, sl_from: 3, sl_to: 15, sl_step: 3, hold_from: 2, hold_to: 30, hold_step: 2, score_from: 40, score_to: 95, score_step: 5 }
    // 常量 def：局部定义
    setCfgSweep(def)
    try {
      const res = await api.fetchSweepPools()
      // 常量 res：局部定义
      const mine = (res.pools || []).find((p) => p.strategy === (optCur ? optCur.strategy : ''))
      // 常量 mine：局部定义
      if (mine) setCfgSweep({ ...def, ...mine, tp_from: mine.tp_from, tp_to: mine.tp_to, tp_step: mine.tp_step, sl_from: mine.sl_from, sl_to: mine.sl_to, sl_step: mine.sl_step, hold_from: mine.hold_from, hold_to: mine.hold_to, hold_step: mine.hold_step, score_from: mine.score_from, score_to: mine.score_to, score_step: mine.score_step })
    } catch {}
    setCfgRule({ max_daily_buys: 0, cooldown_minutes: 0, min_score: 0, budget_pct_per_day: 0 })
    try {
      const ps = await api.fetchPaperState()
      // 常量 ps：局部定义
      const pk = optCurPoolKey
      // 常量 pk：局部定义
      const pool = ((ps.strategy_pools) || []).find((p) => p.key === pk)
      // 常量 pool：局部定义
      if (pool && pool.buy_rule) setCfgRule({ max_daily_buys: pool.buy_rule.max_daily_buys || 0, cooldown_minutes: pool.buy_rule.cooldown_minutes || 0, min_score: pool.buy_rule.min_score || 0, budget_pct_per_day: pool.buy_rule.budget_pct_per_day || 0 })
    } catch {}
    setOptDrawerOpen(true)
  }
  async function saveSweepPool() {
  // 保存单战法四维步进搜索空间（服务端校验组合数护栏）
    if (!optCur) return
    try {
      const res = await api.saveSweepPool({ strategy: optCur.strategy, ...cfgSweep })
      // 常量 res：局部定义
      showToast(`参数池已保存——预估 ${Number(res.combos).toLocaleString()} 组合`, 'success')
    } catch (e) { showToast('保存失败: ' + (e.message || e), 'error') }
  }
  async function savePoolDiscipline() {
  // 保存分仓池买入纪律（日限/冷却/最低分/日预算%）
    const pk = optCurPoolKey
    // 常量 pk：局部定义
    if (!pk) { showToast('未知战法无法映射资金池', 'warning'); return }
    const r = cfgRule
    // 常量 r：局部定义
    const rule = { max_daily_buys: parseInt(r.max_daily_buys, 10) || 0, cooldown_minutes: parseInt(r.cooldown_minutes, 10) || 0, min_score: parseFloat(r.min_score) || 0, budget_pct_per_day: parseFloat(r.budget_pct_per_day) || 0 }
    // 常量 rule：局部定义
    try {
      await api.configPaperPools(null, null, null, { [pk]: rule })
      showToast('池纪律已保存并即时生效', 'success')
    } catch (e) { showToast('保存失败: ' + (e.message || e), 'error') }
  }

  // ===== 回测任务中心 =====
  function toggleAdvice(id) { setAdviceOpen((a) => (a === id ? 0 : id)) }
  function jobParams(j) {
  // 拼接回测任务的展示参数（起止/止盈/止损/持仓天数等；优化类展示寻优目标）
    if (!j.params_json) return ''
    try {
      const p = JSON.parse(j.params_json)
      // 常量 p：局部定义
      const parts = []
      // 常量 parts：局部定义
      if (j.strategy_kind === 'optimize' && p.objective) parts.push('寻优目标·' + p.objective)
      if (p.start) parts.push(p.start + '~' + (p.end || '今'))
      if (p.top_k) parts.push('每次选 ' + p.top_k + ' 只')
      if (p.min_stocks) parts.push('最少样本 ' + p.min_stocks)
      if (p.maxstocks) parts.push('池子 ' + p.maxstocks + ' 只')
      return parts.join(' · ')
    } catch { return '' }
  }
  function initiatingRule(j) {
  // 回测任务的「发起规则」：标识是哪类规则/配置触发了本次回测（因子/形态/参数寻优/候选/夜间调度）
    const sk = j.strategy_kind || ''
    if (sk === 'optimize') {
      let obj = ''
      try { const p = JSON.parse(j.params_json || '{}'); if (p.objective) obj = '·' + p.objective } catch { /* 忽略解析失败 */ }
      return '参数寻优' + obj
    }
    if (sk.startsWith('fac_')) return '因子战法·' + (libraryJobLabel(j) || sk)
    if (sk.startsWith('pat_')) return '形态战法·' + (libraryJobLabel(j) || sk)
    if (j.kind === 'library') return libraryJobLabel(j) || '战法库'
    if (j.kind === 'candidate') return '候选 #' + j.candidate_id
    if (j.kind === 'nightly') return '夜间全量调度'
    return j.kind || '-'
  }
  function metricAdvices(j) {
  // 从回测结果文本解析出指标改进建议列表
    const out = []
    // 常量 out：局部定义
    const lines = (j.result_text || '').split('\n')
    // 常量 lines：局部定义
    for (const line of lines) {
      const m = line.match(/^【(.+?)】胜率 ([\d.]+)% 盈亏比 ([\d.]+) 触发 (\d+) 持仓 ([\d.]+)天/)
      // 常量 m：局部定义
      if (!m) continue
      const [, name, wrS, pfS, nS, holdS] = m
      const wr = parseFloat(wrS), pf = parseFloat(pfS), n = parseInt(nS, 10), hold = parseFloat(holdS)
      // 常量 wr：局部定义
      const ev = wr / 100 * pf - (1 - wr / 100)
      // 常量 ev：局部定义
      const tips = []
      // 常量 tips：局部定义
      if (n < 50) tips.push('触发仅 ' + n + ' 次，统计意义弱——扩大回测区间再下结论')
      if (wr < 40) tips.push('胜率 ' + wr.toFixed(1) + '% 偏低：提高入场门槛（寻优里选门槛80）或叠加情绪/D1 过滤，宁缺毋滥')
      if (pf < 1.0) tips.push('盈亏比 ' + pf.toFixed(2) + ' <1：平均亏损吃掉平均盈利——收紧止损（更早认错）或让利润奔跑（放宽止盈）')
      else if (pf >= 1.2 && wr >= 45) tips.push('胜率与盈亏比均衡，具备小仓位实盘验证价值')
      if (hold > 10) tips.push('平均持仓 ' + hold.toFixed(1) + ' 天偏长：缩短最大持仓天数，提高资金周转')
      if (ev > 0.05 && tips.length === 0) tips.push('各项健康：期望为正且无短板——保持参数，控制单笔仓位即可')
      out.push('【' + name + '】' + (tips.length ? tips.join('；') : '暂无明显短板'))
    }
    return out
  }
  function ruleNum(id) {
  // 根据战法 id 反推内置规则编号
    const n = parseInt(String(id || '').replace(/^[a-z]+_/, ''), 10)
    // 常量 n：局部定义
    return Number.isFinite(n) ? n : -1
  }
  function latestLibJob(sk, num) {
  // 取某战法最近一次回测任务
    let best = null
    for (const j of backtestJobs) {
      if (j.kind !== 'library' || (j.strategy_kind || '') !== sk) continue
      if (num >= 0 && j.candidate_id !== num) continue
      if (!best || (j.id || 0) > (best.id || 0)) best = j
    }
    return best
  }
  function summarizeJob(j) {
  // 将回测任务结果摘要为单行可读文案
    if (!j) return '未回测'
    if (j.status === 'running') return '回测中 ' + jobPct(j)
    if (j.status === 'queued') return '排队中·盘后执行'
    if (j.status === 'interrupted') return '已中断·可续跑'
    if (j.status === 'error') return '回测失败'
    const line = (j.result_text || '').split('\n')[0] || ''
    // 常量 line：局部定义
    return line.replace(/^【[^】]*】/, '').trim() || '已完成'
  }
  function queueHint(j) {
  // 根据任务状态生成排队原因提示
    const ahead = backtestJobs.filter((x) => x.status === 'running' || (x.status === 'queued' && (x.id || 0) < (j.id || 0))).length
    // 常量 ahead：局部定义
    return ahead > 0 ? `排队中：前方还有 ${ahead} 个任务，将按优先级依次执行` : '已加入队列，将在非交易时段自动执行（交易日盘后起 / 非交易日全天；绝不进入盘中）'
  }
  function libraryJobLabel(j) {
  // 生成战法库回测任务的中文标签
    if (j.candidate_id === 0) return '夜间全量回放'
    return builtinLabel(j.candidate_id)
  }
  async function rerunLibrary(j) {
  // 对战法库规则重新发起一次回测
    const sk = j.strategy_kind
    // 常量 sk：局部定义
    const id = ['double_bump', 'dragon', 'dragon_return', 'n_shape'].includes(sk) ? sk : (sk === 'factor' ? 'fac_' : 'pat_') + j.candidate_id
    // 常量 id：局部定义
    try { await api.backtestLibraryRule(id, { quality: true }); startLibPoll(); await loadBacktests() } catch (e) { showToast('发起失败: ' + (e.message || e), 'error') }
  }
  function builtinLabel(num) {
  // 将内置规则编号转换为战法中文名
    const m = { 901: '双响炮', 902: '龙头', 903: '龙回头', 904: 'N形', 990: '全库参数寻优' }
    // 常量 m：局部定义
    return m[num] || ('规则 ' + num)
  }
  function selectStrategy(key) {
  // 选中某战法进入参数寻优配置
    setExpandedStrategy((e) => (e === key ? '' : key))
    const el = strategyRefs.current[key]
    // 常量 el：局部定义
    if (expandedStrategy !== key && el && el.scrollIntoView) el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }
  async function doLibraryBacktest(s) {
  // 对指定战法库记录发起回测任务
    if (!(await confirmDialog(`回测战法「${s.name || s.id}」？（历史日K回放，结果进「回测」tab）`))) return
    try {
      await api.backtestLibraryRule(s.id, { start: btStart.trim(), end: btEnd.trim(), quality: true })
      setActiveTab('backtests')
      await loadBacktests()
      startLibPoll()
    } catch (e) { showToast('发起失败: ' + (e.message || e), 'error') }
  }
  function startLibPoll() {
  // 启动战法库回测轮询（定时刷新任务状态）
    if (libPollTimer.current) return
    // 每 5000ms = 5 秒轮询一次战法库回测任务，直至无运行中任务时停止
    libPollTimer.current = setInterval(async () => {
      await loadBacktests()
      const busy = backtestJobs.some((j) => j.kind === 'library' && (j.status === 'running' || j.status === 'paused' || j.status === 'queued'))
      // 常量 busy：局部定义
      if (!busy) { clearInterval(libPollTimer.current); libPollTimer.current = null }
    }, 5000)
  }
  function pollBacktest(c) {
  // 轮询单候选回测任务进度并刷新结果
    const id = c.id
    // 常量 id：局部定义
    if (backtestPollers.current[id]) return
    // 每 5000ms = 5 秒轮询一次单候选回测进度与结果，完成时回填超额收益并刷新战法库
    backtestPollers.current[id] = setInterval(async () => {
      try {
        const j = await api.fetchBacktestStatus(id)
        // 常量 j：局部定义
        if (j.progress) {
          setBacktestProgress((p) => ({ ...p, [id]: j.progress }))
          syncJobIntoList(j)
        }
        if (j.status === 'done') {
          clearPoll(id)
          setBacktestLoading((b) => ({ ...b, [id]: false }))
          setBacktestState((b) => ({ ...b, [id]: 'done' }))
          setBacktestProgress((p) => ({ ...p, [id]: null }))
          setBacktestResult((r) => ({ ...r, [id]: j.avg_excess }))
          c.avg_excess = j.avg_excess
          syncJobIntoList({ status: 'done', candidate_id: id, progress: '100%', avg_excess: j.avg_excess })
          showToast('候选 #' + id + ' 回测完成，回测超额 ' + (j.avg_excess !== undefined ? (j.avg_excess * 100).toFixed(2) + '%' : '0%'), 'success')
          loadLibrary()
        } else if (j.status === 'error') {
          clearPoll(id)
          setBacktestLoading((b) => ({ ...b, [id]: false }))
          setBacktestState((b) => ({ ...b, [id]: 'error' }))
          setBacktestProgress((p) => ({ ...p, [id]: null }))
          syncJobIntoList({ status: 'error', candidate_id: id, progress: '100%', error: j.error })
          showToast('候选 #' + id + ' 回测失败: ' + (j.error || ''), 'error')
        }
      } catch (e) {}
    }, 5000)
  }
  function clearPoll(id) {
  // 清除指定候选的回测轮询定时器
    if (backtestPollers.current[id]) { clearInterval(backtestPollers.current[id]); delete backtestPollers.current[id] }
  }
  async function restoreRunningBacktests() {
  // 页面刷新后恢复仍在运行的回测轮询
    try {
      const res = await api.fetchRunningBacktests()
      // 常量 res：局部定义
      const jobs = (res && res.jobs) || []
      // 常量 jobs：局部定义
      for (const j of jobs) {
        if (j.kind !== 'candidate') continue
        if (j.status !== 'running' && j.status !== 'queued') continue
        const cand = candidates.find((x) => x.id === j.candidate_id)
        // 常量 cand：局部定义
        const c = cand || { id: j.candidate_id, kind: 'factor' }
        // 常量 c：局部定义
        setBacktestLoading((b) => ({ ...b, [c.id]: true }))
        setBacktestState((b) => ({ ...b, [c.id]: 'running' }))
        if (j.progress) setBacktestProgress((p) => ({ ...p, [c.id]: j.progress }))
        pollBacktest(c)
      }
    } catch (e) { console.error('恢复运行中回测任务失败', e) }
  }
  // 拉取全部回测任务并去重（按 kind:candidate_id），用于回测任务中心表格展示
  async function loadBacktests() {
    setBtLoading(true)
    try {
      const res = await api.fetchAllBacktests()
      // 常量 res：局部定义
      if (res && Array.isArray(res.jobs)) {
        const seen = new Set()
        // 常量 seen：局部定义
        setBacktestJobs(res.jobs.filter((j) => {
          const k = (j.kind || 'candidate') + ':' + j.candidate_id
          // 常量 k：局部定义
          if (seen.has(k)) return false
          seen.add(k)
          return true
        }))
      }
    } catch (e) { console.error('加载回测任务失败', e) }
    finally { setBtLoading(false) }
  }
  function syncJobIntoList(j) {
  // 将回测任务状态合并进任务列表（更新或追加）
    setBacktestJobs((jobs) => {
      const jk = j.kind || 'candidate'
      // 常量 jk：局部定义
      const idx = jobs.findIndex((x) => (x.kind || 'candidate') === jk && x.candidate_id === j.candidate_id)
      // 常量 idx：局部定义
      const merged = { ...(idx >= 0 ? jobs[idx] : { id: j.task_id }), ...j, kind: jk }
      // 常量 merged：局部定义
      if (idx >= 0) { const copy = jobs.slice(); copy[idx] = merged; return copy }
      return [merged, ...jobs]
    })
  }
  function doBacktestById(id) {
  // 根据任务 id 查询并展示回测详情
    const c = candidates.find((x) => x.id === id)
    // 常量 c：局部定义
    if (c) doBacktest(c)
    else showToast('候选 #' + id + ' 不存在（请先刷新候选列表）', 'warning')
  }
  function btStatusLabel(s) {
  // 将回测任务状态翻译为中文标签
    const m = { running: '运行中', paused: '已暂停', done: '已完成', error: '失败', interrupted: '已中断', queued: '排队中·盘后执行', preempted: '已中断' }
    // 常量 m：局部定义
    return m[s] || s
  }
  function ctrlId(j) {
  // 生成回测任务控制的唯一键
    return j.kind === 'library' ? 1000000000 + j.candidate_id : j.candidate_id
  }
  function jobPct(j) {
  // 计算回测任务整体进度百分比
    const p = j.progress || '0%'
    // 常量 p：局部定义
    const n = parseInt(p, 10)
    // 常量 n：局部定义
    return (isNaN(n) ? 0 : Math.max(0, Math.min(100, n))) + '%'
  }
  const btCandidates = useMemo(() => candidates.filter((c) => (c.kind === 'factor' || c.kind === 'pattern') && c.status === 'proposed'), [candidates])
  // 常量 btCandidates：局部定义

  // 分组
  const libGroupFactors = useMemo(() => library.filter((s) => s.kind !== 'pattern'), [library])
  const libGroupPatterns = useMemo(() => library.filter((s) => s.kind === 'pattern'), [library])
  // 常量 libGroupPatterns：局部定义
  const ruleGroups = useMemo(() => [
  // 常量 ruleGroups：局部定义
    { key: 'factor', title: '因子战法', items: libGroupFactors },
    { key: 'pattern', title: '形态战法', items: libGroupPatterns },
  ].filter((g) => g.items.length), [libGroupFactors, libGroupPatterns])

  // 回测任务表列（使用 Table 承载数据网格）
  const btColumns = useMemo(() => [
    { colKey: 'kind', title: '类型', width: 90, cell: ({ row }) => {
      const k = row.kind === 'nightly' ? '' : (row.kind === 'library' ? 'kind-pattern' : 'kind-factor')
      // 常量 k：局部定义
      const label = row.kind === 'nightly' ? '夜间全量' : (row.kind === 'library' ? '战法库' : '单候选')
      // 常量 label：局部定义
      return <Tag theme={row.kind === 'nightly' ? 'warning' : (row.kind === 'library' ? 'primary' : 'default')}>{label}</Tag>
    } },
    { colKey: 'candidate', title: '对象', width: 120, cell: ({ row }) => {
      if (row.kind === 'candidate') return <span>#{row.candidate_id}</span>
      if (row.kind === 'library') return <span>{libraryJobLabel(row)}</span>
      return <span>夜间全量</span>
    } },
    { colKey: 'status', title: '状态', width: 110, cell: ({ row }) => {
      const st = row.status
      // 常量 st：局部定义
      const theme = st === 'done' ? 'success' : st === 'error' ? 'danger' : st === 'queued' ? 'warning' : st === 'running' ? 'primary' : 'default'
      // 常量 theme：局部定义
      return <Tag theme={theme}>{btStatusLabel(st)}</Tag>
    } },
    { colKey: 'started_at', title: '发起时间', width: 150, cell: ({ row }) => <span style={{ fontSize: 12, color: '#bbb' }}>{row.started_at || (row.status === 'queued' ? '排队中' : (row.finished_at || '-'))}</span> },
    { colKey: 'params', title: '参数', minWidth: 160, cell: ({ row }) => <span style={{ fontSize: 12, color: '#aaa' }}>{jobParams(row) || '-'}</span> },
    { colKey: 'rule', title: '发起规则', minWidth: 140, cell: ({ row }) => <span style={{ fontSize: 12, color: '#aaa' }}>{initiatingRule(row)}</span> },
    { colKey: 'progress', title: '进度', width: 140, cell: ({ row }) => {
      if (row.status === 'running' || row.status === 'paused' || row.status === 'queued') {
        return (
          <div>
            <div style={{ height: 6, background: '#e7e7e7', borderRadius: 3, overflow: 'hidden' }}>
              <div style={{ width: jobPct(row), height: '100%', background: '#4caf50' }} />
            </div>
            <span style={{ fontSize: 11, color: '#aaa' }}>{jobPct(row)}</span>
          </div>
        )
      }
      if (row.status === 'done' && row.kind !== 'library') {
        return <span style={{ color: signClass(row.avg_excess) === 'pos' ? '#00a870' : '#e34d59' }}>{fmt(row.avg_excess)}</span>
      }
      return <span style={{ color: '#aaa' }}>{row.error ? '失败' : '-'}</span>
    } },
    { colKey: 'actions', title: '操作', width: 180, cell: ({ row }) => {
      if (!canApprove) return null
      if (row.kind !== 'candidate' && row.kind !== 'library') return null
      return (
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {row.status === 'running' && <Button size="small" theme="warning" onClick={() => doPauseBacktest(ctrlId(row))}>暂停</Button>}
          {row.status === 'paused' && <Button size="small" theme="warning" onClick={() => doResumeBacktest(ctrlId(row))}>继续</Button>}
          {(row.status === 'running' || row.status === 'paused' || row.status === 'queued') &&
            <Button size="small" theme="danger" onClick={() => doCancelBacktest(ctrlId(row))}>取消</Button>}
          {row.kind === 'candidate' && row.status !== 'queued' && row.status !== 'running' && row.status !== 'paused' &&
            <Button size="small" onClick={() => doBacktestById(row.candidate_id)}>{row.status === 'interrupted' ? '续跑' : '重新回测'}</Button>}
          {row.kind === 'library' && row.strategy_kind && row.status !== 'queued' && row.status !== 'running' && row.status !== 'paused' &&
            <Button size="small" onClick={() => rerunLibrary(row)}>{row.status === 'interrupted' ? '重跑' : '重新回测'}</Button>}
          {row.id > 0 && <Button size="small" variant="outline" onClick={() => openLog(row.id)}>日志</Button>}
        </div>
      )
    } },
  ], [backtestJobs, canApprove])

  // 寻优热力网格表列
  const heatColumns = useMemo(() => {
    const cols = [{ colKey: 'tp', title: '止盈\\止损', width: 100, fixed: 'left', cell: ({ row }) => <b>{fmtNum(row.tp)}%</b> }]
    // 常量 cols：局部定义
    optCurHeat.sls.forEach((sl) => {
      cols.push({ colKey: 'sl_' + sl, title: fmtNum(sl) + '%', cell: ({ row }) => {
        const v = row['sl_' + sl]
        // 常量 v：局部定义
        return <span style={{ background: heatColor(v), padding: '2px 6px', borderRadius: 3, display: 'inline-block' }}>{heatVal(row.tp, sl) !== null ? fmtNum(heatVal(row.tp, sl), 2) : '—'}</span>
      } })
    })
    return cols
  }, [optCurHeat])
  const heatData = useMemo(() => {
  // 常量 heatData：局部定义
    return optCurHeat.tps.map((tp) => {
      const r = { tp }
      // 常量 r：局部定义
      optCurHeat.sls.forEach((sl) => { r['sl_' + sl] = heatVal(tp, sl) })
      return r
    })
  }, [optCurHeat])

  // ===== 生命周期 =====
  // 启动研究进度定时轮询（每 30000ms = 30 秒刷新一次处理进度）
  function startPolling() {
    if (pollTimer.current) return
    // 每 30 秒同时刷新「研究进度」与「回测任务列表」：回测任务列表此前未纳入轮询，
    // 导致任务排队 → 运行 → 完成的状态变化在前端不刷新（长时间停在「排队中」）。
    pollTimer.current = setInterval(() => { loadProgress(); loadBacktests() }, 30000)
  }
  function stopPolling() {
  // 停止全部研究轮询定时器（组件卸载时清理）
    if (pollTimer.current) { clearInterval(pollTimer.current); pollTimer.current = null }
  }

  useEffect(() => {
    loadFactorMeta()
    loadAll()
    loadLibrary()
    loadBacktestToggle()
    startPolling()
    loadBacktests()
    restoreRunningBacktests()
    return () => {
      stopPolling()
      if (libPollTimer.current) { clearInterval(libPollTimer.current); libPollTimer.current = null }
      Object.keys(backtestPollers.current).forEach(clearPoll)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // ===== 渲染片段 =====
  // 渲染单条研究候选卡片：展示战法构成、电脑验证结论、关键指标与审批/回测操作按钮
  function renderCandidate(c) {
    return (
      <Card key={c.id} style={{ marginBottom: 12 }} title={<span>#{c.id} <Tag theme="primary">{kindLabel(c.kind)}</Tag> <Tag theme={c.status === 'proposed' ? 'warning' : 'success'}>{statusLabel(c.status)}</Tag> <span style={{ fontSize: 12, color: '#888' }}>{c.created_at}</span></span>}>
        {c.kind === 'factor' ? (
           <div>
            <div style={{ fontWeight: 600, margin: '6px 0', fontSize: 13 }}>这条战法在做什么</div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, margin: '4px 0' }}>
              {factorRule(c).map((f) => (
                <Tag key={f.id} style={{ margin: '2px 4px' }}>
                  <span style={{ color: f.dir < 0 ? '#00a870' : '#e34d59' }}>{f.dir < 0 ? '看空' : '看多'}</span> {f.label} {f.weight.toFixed(2)}
                </Tag>
              ))}
            </div>
            <div style={{ fontSize: 13, color: '#666666', lineHeight: 1.7, margin: '4px 0' }}>
              玩法：每天给所有股票按上面 {factorRule(c).length} 个指标打分，分数最高的前一批会被标记为「值得买」，赌它们接下来 {c.horizon} 个交易日能涨。
              {factorRule(c).some((f) => f.dir < 0) && <span> 注意：带「看空」的指标是反着用的——这项数值越高，反而越说明不该买。</span>}
            </div>
            <div style={{ fontWeight: 600, margin: '6px 0', fontSize: 13 }}>这条规律靠谱吗？（电脑验证过）</div>
            <div style={{ margin: '6px 0' }}>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <Tag theme={verdict(c).ok ? 'success' : 'danger'}>{verdict(c).ok ? '✅ 可以试试' : '⚠️ 建议别用'}</Tag>
                <span style={{ fontSize: 13, color: '#666666' }}>{verdict(c).text}</span>
              </div>
              {plainLines(c).map((l, i) => (
                <div key={i} style={{ display: 'flex', gap: 6, fontSize: 13, color: '#666666', margin: '4px 0' }}><span style={{ color: '#888' }}>{i + 1}.</span><span>{l}</span></div>
              ))}
            </div>
            <details style={{ border: '1px solid #e7e7e7', borderRadius: 6, padding: 8, marginTop: 6 }}>
              <summary style={{ cursor: 'pointer' }}>想看具体数字？展开</summary>
              <div style={{ display: 'flex', gap: 8, fontSize: 13, margin: '4px 0' }}><span style={{ color: '#888', minWidth: 90 }}>样本内测试</span><span>前一段历史回放：IR {fmt(parseReason(c, '样本内IR'))}</span></div>
              <div style={{ display: 'flex', gap: 8, fontSize: 13, margin: '4px 0' }}><span style={{ color: '#888', minWidth: 90 }}>样本外测试</span><span>另一段没用过的历史回放：IR {fmt(parseReason(c, '样本外IR'))}</span></div>
              <div style={{ display: 'flex', gap: 8, fontSize: 13, margin: '4px 0' }}><span style={{ color: '#888', minWidth: 90 }}>反推超额</span><span>高分股比全市场平均多赚 {fmtPct(parseReason(c, '反推超额'))}</span></div>
              <div style={{ display: 'flex', gap: 8, fontSize: 13, margin: '4px 0' }}><span style={{ color: '#888', minWidth: 90 }}>全样本 IR</span><span>{fmt(c.ir)}（参考）</span></div>
              <div style={{ display: 'flex', gap: 8, fontSize: 13, margin: '4px 0' }}><span style={{ color: '#888', minWidth: 90 }}>全样本 IC</span><span>{fmt(c.ic_mean)}（参考）</span></div>
              <div style={{ display: 'flex', gap: 8, fontSize: 13, margin: '4px 0' }}><span style={{ color: '#888', minWidth: 90 }}>全链路回测</span><span>{btTested(c) ? (c.backtest_result_text || fmt(c.avg_excess)) : '未测'}</span></div>
            </details>
          </div>
        ) : (
          <div>
            <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
              <div style={{ display: 'flex', gap: 6 }}><span style={{ color: '#888', fontSize: 13 }}>IR</span><span style={{ color: signColor(c.ir), fontWeight: 600 }}>{fmt(c.ir)}</span></div>
              <div style={{ display: 'flex', gap: 6 }}><span style={{ color: '#888', fontSize: 13 }}>IC</span><span style={{ color: signColor(c.ic_mean), fontWeight: 600 }}>{fmt(c.ic_mean)}</span></div>
              <div style={{ display: 'flex', gap: 6 }}><span style={{ color: '#888', fontSize: 13 }}>回测超额</span><span style={{ color: signColor(c.avg_excess), fontWeight: 600 }}>{fmt(c.avg_excess)}</span></div>
              <div style={{ display: 'flex', gap: 6 }}><span style={{ color: '#888', fontSize: 13 }}>前瞻天数</span><span style={{ fontWeight: 600 }}>{c.horizon}</span></div>
            </div>
            {weightList(c).length > 0 && (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, margin: '6px 0' }}>
                {weightList(c).map((w) => (
                  <Tag key={w[0]} style={{ margin: '2px 4px' }}>{w[0]} {w[1].toFixed(3)}</Tag>
                ))}
              </div>
            )}
          </div>
        )}

        {c.kind === 'depth' && (
          <div style={{ marginTop: 8 }}>
            {Object.entries(depthSummary(c)).map(([code, s]) => (
              <div key={code} style={{ margin: '4px 0' }}>
                <span style={{ fontWeight: 600 }}>{code}</span>
                <span className="muted" style={{ marginLeft: 8 }}>买1 {s.bid1} / 卖1 {s.ask1}</span>
                {(s.orders || []).map((o) => (
                  <Tag key={o.level + o.kind} theme={o.kind === 'support' ? 'success' : 'danger'} style={{ margin: '2px' }}>
                    {o.kind === 'support' ? '托' : '压'}单 档{o.level} {o.price} / {o.volume}手 ({(o.share_pct * 100).toFixed(0)}%)
                  </Tag>
                ))}
              </div>
            ))}
          </div>
        )}

        {canApprove && c.status === 'proposed' && (
          <div style={{ marginTop: 10, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            <Button theme="primary" size="small" onClick={() => doApprove(c)}>审批并应用</Button>
            <Button theme="danger" size="small" onClick={() => doReject(c)}>驳回</Button>
            {c.kind === 'factor' && (
              <Button theme="warning" size="small" loading={!!backtestLoading[c.id]} onClick={() => doBacktest(c)}>
                {backtestLoading[c.id] ? '回测中...' : (c.avg_excess ? '重新回测' : '全量回测')}
              </Button>
            )}
            {backtestLoading[c.id] && (
              <div style={{ flex: 1, minWidth: 160 }}>
                <div style={{ height: 6, background: '#e7e7e7', borderRadius: 3, overflow: 'hidden' }}>
                  <div style={{ width: btPct(c.id), height: '100%', background: '#4caf50' }} />
                </div>
                <span style={{ fontSize: 11, color: '#aaa' }}>全链路回测 {backtestProgress[c.id] || '0%'}</span>
              </div>
            )}
            {backtestResult[c.id] && <span style={{ color: signClass(backtestResult[c.id]) === 'pos' ? '#00a870' : '#e34d59' }}>回测超额 {fmt(backtestResult[c.id])}</span>}
          </div>
        )}
        {!canApprove && c.status === 'proposed' && <div style={{ marginTop: 10, color: '#888', fontSize: 12 }}>无审批权限（需管理员授予 research_approve）</div>}
      </Card>
    )
  }

  // 渲染战法库分组（因子战法 / 形态战法）：展示每个战法的名称、条件、收益统计与启停/回测/删除操作
  function renderLibraryGroup(g) {
    return (
      <div key={g.key} style={{ marginBottom: 16 }}>
        <div style={{ fontWeight: 600, margin: '8px 0', fontSize: 14 }}>{g.title}（{g.items.length}）</div>
        {g.items.map((s) => {
          const expanded = expandedStrategy === 'rule:' + s.id
          // 常量 expanded：局部定义
          return (
            <Card key={s.id} style={{ marginBottom: 10 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                {editingName[s.id]
                  ? <Input value={nameDraft[s.id]} onChange={(v) => setNameDraft((d) => ({ ...d, [s.id]: v }))} onEnter={saveName(s)} onBlur={saveName(s)} style={{ width: 160 }} />
                  : <b>{s.name}</b>}
                {canApprove && !editingName[s.id] && <Button size="small" variant="text" theme="primary" onClick={() => startRename(s)}>改名</Button>}
                <Tag theme={s.kind === 'pattern' ? 'primary' : 'default'}>{s.kind === 'pattern' ? '形态' : '因子'}</Tag>
                <Tag theme={s.enabled ? 'success' : 'default'}>{s.enabled ? '已启用' : '已停用'}</Tag>
                <span style={{ fontSize: 11, color: '#777', marginLeft: 'auto' }}>{s.id}｜{s.applied_at}</span>
              </div>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginTop: 6 }}>
                {s.kind === 'pattern'
                  ? (s.conds || []).map((c, i) => <Tag key={i} style={{ margin: '2px' }}>{condLabel(c)}</Tag>)
                  : ruleFactors(s).map((f) => (
                    <Tag key={f.id} style={{ margin: '2px' }}>
                      <span style={{ color: f.dir < 0 ? '#00a870' : '#e34d59' }}>{f.dir < 0 ? '看空' : '看多'}</span> {f.label}
                    </Tag>
                  ))}
              </div>
              <div style={{ fontSize: 12, color: '#aaa', marginTop: 6 }}>
                <span>信号 <b>{s.signal_count}</b></span>
                <span style={{ marginLeft: 8 }}>胜 <b style={{ color: '#00a870' }}>{s.win}</b></span>
                <span style={{ marginLeft: 8 }}>负 <b style={{ color: '#e34d59' }}>{s.loss}</b></span>
                <span style={{ marginLeft: 8 }}>累计前向收益 <b style={{ color: s.cum_return >= 0 ? '#00a870' : '#e34d59' }}>{fmtPct(s.cum_return)}</b></span>
              </div>
              {canApprove && (
                <div style={{ marginTop: 8, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  <Button size="small" theme="primary" variant="outline" onClick={() => toggleLibrary(s)}>{s.enabled ? '停用' : '启用'}</Button>
                  <Button size="small" theme="warning" onClick={() => doLibraryBacktest(s)}>回测此战法</Button>
                  <Button size="small" variant="outline" onClick={() => selectStrategy('rule:' + s.id)}>{expanded ? '收起详情' : '详情'}</Button>
                  <Button size="small" theme="danger" onClick={() => removeLibrary(s)}>删除</Button>
                </div>
              )}
              {expanded && (
                <pre style={{ marginTop: 8, fontSize: 12, color: '#ccc', whiteSpace: 'pre-wrap' }}>
                  {latestLibJob(s.kind, ruleNum(s.id)) && latestLibJob(s.kind, ruleNum(s.id)).result_text
                    ? latestLibJob(s.kind, ruleNum(s.id)).result_text
                    : '暂无回测报告——点「回测此战法」发起'}
                </pre>
              )}
            </Card>
          )
        })}
      </div>
    )
  }

  // ===== 兜底渲染错误 =====
  if (renderError) {
    return (
      <div className="page">
        <div style={{ background: '#fef2f2', color: '#b91c1c', border: '1px solid #fecaca', borderRadius: 6, padding: '8px 12px', marginBottom: 8, fontSize: 12 }}>
          页面渲染出错（不影响其他标签页）：<code>{renderError}</code>
          <Button style={{ marginLeft: 8 }} size="small" onClick={() => setRenderError('')}>关闭</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12, flexWrap: 'wrap', gap: 8 }}>
        <h2>自动研究</h2>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <Select
            value={statusFilter}
            onChange={(v) => { setStatusFilter(v); loadData() }}
            style={{ width: 140 }}
            options={[
              { label: '全部', value: '' },
              { label: '待审批', value: 'proposed' },
              { label: '已应用', value: 'applied' },
              { label: '已审批', value: 'approved' },
              { label: '已驳回', value: 'rejected' },
            ]}
          />
          <Button theme="default" variant="outline" loading={loading} onClick={loadAll}>刷新</Button>
        </div>
      </div>

      {schedStatus && (
        <div style={{
          background: (schedStatus.reason || '').includes('拦截') || (schedStatus.reason || '').includes('禁用') ? '#fef2f2'
            : schedStatus.busy ? '#eff6ff' : '#f0fdf4',
          color: (schedStatus.reason || '').includes('拦截') || (schedStatus.reason || '').includes('禁用') ? '#b91c1c'
            : schedStatus.busy ? '#1d4ed8' : '#15803d',
          border: '1px solid',
          borderColor: (schedStatus.reason || '').includes('拦截') || (schedStatus.reason || '').includes('禁用') ? '#fecaca'
            : schedStatus.busy ? '#bfdbfe' : '#bbf7d0',
          borderRadius: 6, padding: '8px 12px', marginBottom: 12, fontSize: 13
        }}>
          <b>研究调度状态</b>：{schedStatus.reason}
          {(backtestJobs || []).filter((j) => j.status === 'queued' || j.status === 'running').length > 0 && (
            <span style={{ color: '#1d4ed8', marginLeft: 8 }}>
              （回测队列：{(backtestJobs || []).filter((j) => j.status === 'queued' || j.status === 'running').length} 个任务排队/执行中）
            </span>
          )}
          <span style={{ color: '#888', marginLeft: 8 }}>
            （北京时间 {schedStatus.beijing_now} · 交易时段={schedStatus.in_trading_window ? '是' : '否'} · 内存可用 {schedStatus.mem_avail_mb}MB · 闸门={schedStatus.mem_gate_open ? '开' : '关'} · 槽位={schedStatus.busy ? '占用' : '空闲'}）
          </span>
        </div>
      )}

      <Tabs value={activeTab} onChange={(v) => setActiveTab(v)}>
        <Tabs.TabPanel value="candidates" label="待审批候选">
          <div style={{ marginTop: 12 }}>
              <div style={{ marginBottom: 8, display: 'flex', gap: 8 }}>
              <Button variant={candSubTab === 'patterns' ? 'base' : 'outline'} size="small" onClick={() => setCandSubTab('patterns')}>形态候选</Button>
              <Button variant={candSubTab === 'optimize' ? 'base' : 'outline'} size="small" onClick={() => { setCandSubTab('optimize'); loadOptimizations() }}>优化结果</Button>
            </div>

            {candSubTab === 'patterns' && (
              <div>
                {progress && (
                  <Card style={{ marginBottom: 12 }} title={<span>研究处理进度 {progress.db_attached && <Tag theme="success" size="small">数据源: {progress.db_attached}</Tag>}</span>}>
                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill,minmax(200px,1fr))', gap: 12 }}>
                      <div>
                        <div style={{ fontSize: 12, color: '#888' }}>数据准备度（近一年有行情 / 全市场）</div>
                        <div style={{ height: 8, background: '#eef0f3', borderRadius: 4, overflow: 'hidden', marginTop: 4 }}>
                          <div style={{ width: pct(progress.ready_pct) + '%', height: '100%', background: 'linear-gradient(90deg,#4caf50,#64b5f6)' }} />
                        </div>
                        <div style={{ fontSize: 12, color: '#aaa', marginTop: 4 }}>{progress.ready_stocks} / {progress.stocks} 只（{pct(progress.ready_pct)}%）</div>
                      </div>
                      <div><div style={{ fontSize: 12, color: '#888' }}>日线数据</div><div style={{ fontSize: 12, color: '#aaa', marginTop: 4 }}>{fmtRows(progress.daily_rows)} 行</div></div>
                      <div><div style={{ fontSize: 12, color: '#888' }}>财务指标</div><div style={{ fontSize: 12, color: '#aaa', marginTop: 4 }}>{fmtRows(progress.fin_rows)} 行</div></div>
                      <div>
                        <div style={{ fontSize: 12, color: '#888' }}>研究候选</div>
                        <div style={{ fontSize: 12, color: '#aaa', marginTop: 4 }}>
                          <Tag style={{ margin: '0 2px' }}>{progress.candidates} 条</Tag>
                          {progress.applied && <Tag theme="success" size="small" style={{ margin: '0 2px' }}>已应用 {progress.applied}</Tag>}
                          {progress.proposed && <Tag theme="warning" size="small" style={{ margin: '0 2px' }}>待审批 {progress.proposed}</Tag>}
                        </div>
                      </div>
                    </div>
                  </Card>
                )}

                {noDB && <Card>研究库未接入（需后端开启 B5 研究闭环）</Card>}
                {!noDB && candidates.length === 0 && <Card>暂无候选，先在命令行跑 research optimize 产出</Card>}
                {candidates.map(renderCandidate)}
              </div>
            )}

            {candSubTab === 'optimize' && (
              <Card>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
                    <div style={{ fontWeight: 600 }}>参数寻优中心<span style={{ fontSize: 12, color: '#888' }}>（每战法独立寻优池：止盈×止损×持仓×门槛 步进网格，批内选优+批间 PK）</span></div>
                  <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                    <Select value={optObjective} onChange={(v) => setOptObjective(v)} style={{ width: 150 }} options={[
                      { label: '目标：盈亏比', value: 'profitFactor' },
                      { label: '目标：胜率', value: 'winRate' },
                      { label: '目标：平均盈利', value: 'avgWin' },
                      { label: '目标：期望收益', value: 'expectancy' },
                    ]} />
                    <Button theme="warning" loading={optLaunching} onClick={startOptimize}>{optLaunching ? '已入队…' : '⚙ 发起全库寻优'}</Button>
                    <Button theme="default" variant="outline" onClick={loadOptimizations}>刷新</Button>
                  </div>
                </div>

                {optStrategies.length > 0 && (
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', margin: '8px 0' }}>
                    {optStrategies.map((s) => (
                      <Button key={s.key} variant={optSelected === s.key ? 'base' : 'outline'} size="small" onClick={() => setOptSelected(s.key)}>
                        {s.label}
                        <span style={(s.bestExp ?? 0) >= 0 ? { color: '#00a870' } : { color: '#e34d59' }}>
                          {s.bestExp !== null ? ((s.bestExp >= 0 ? '+' : '') + fmtNum(s.bestExp, 2) + '%') : ''}
                        </span>
                      </Button>
                    ))}
                  </div>
                )}

                {loadingOpts && <div style={{ padding: 20, textAlign: 'center', color: '#888' }}>加载中...</div>}
                {!loadingOpts && !optCur && <Card>暂无寻优结果——点右上「发起全库寻优」，任务进研究队列，完成后排名自动落库到这里。</Card>}

                {!loadingOpts && optCur && (
                  <div style={{ marginTop: 8 }}>
                    <Card title={<span style={{ fontWeight: 600 }}>{optCur.strategy} {!optCur.strategy_kind && <Tag theme="default">内置</Tag>} {optCur.status === 'approved' && <Tag theme="success">已应用</Tag>} {optCur.status === 'rejected' && <Tag theme="default">已淘汰</Tag>}</span>}>
                      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 8 }}>
                        {optCur.status === 'pending' && (
                          <>
                            {optCur.strategy_kind
                              ? <Button size="small" theme="primary" onClick={() => approveOpt(optCur)}>加入战法库</Button>
                              : <Button size="small" theme="primary" onClick={() => approveOpt(optCur)} title="写入该战法的止盈线/止损线/持仓天（config 热生效），门槛同步下发对应资金池纪律">应用参数</Button>}
                            <Button size="small" theme="danger" onClick={() => rejectOpt(optCur)}>淘汰</Button>
                          </>
                        )}
                        <Button size="small" variant="outline" style={{ marginLeft: 'auto' }} onClick={toggleDrawer}>⚙ 参数池 / 池纪律</Button>
                      </div>
                      {optCur.params ? (
                      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill,minmax(120px,1fr))', gap: 8 }}>
                        <div><label>止盈线</label><b>{fmtNum((optCur.params || {}).take_profit_pct)}%</b></div>
                        <div><label>止损线</label><b>{fmtNum((optCur.params || {}).stop_loss_pct)}%</b></div>
                        <div><label>持仓天数</label><b>{fmtNum((optCur.params || {}).hold_days)}天</b></div>
                        <div><label>门槛分数</label><b>{(optCur.params || {}).min_score ? fmtNum(optCur.params.min_score) : '—'}</b></div>
                        <div><label>胜率</label><b>{fmtNum(optCur.win_rate, 1)}%</b></div>
                        <div><label>盈亏比</label><b>{fmtNum(optCur.profit_factor, 2)}</b></div>
                        <div><label>期望收益</label><b style={optCur.expectancy >= 0 ? { color: '#00a870' } : { color: '#e34d59' }}>{fmtNum(optCur.expectancy, 2)}%</b></div>
                        <div><label>实盘复核</label><b>{optCur.win_rate !== undefined ? fmtNum(optCur.win_rate, 1) + '% / ' + fmtNum(optCur.profit_factor, 2) + ' / ' + fmtNum(optCur.expectancy, 2) + '%' : '—'}</b></div>
                        {optCur.pool_stats && <div><label>模拟盘实测</label><b>{fmtNum(optCur.pool_stats.win_rate_pct, 1)}% / {optCur.pool_stats.expectancy >= 0 ? '+' : ''}{fmtNum(optCur.pool_stats.expectancy, 2)}% / {optCur.pool_stats.filled_buys}笔</b></div>}
                      </div>
                      ) : (
                        <div style={{ color: '#e34d59', padding: '4px 0' }}>窗口内未触发买入信号（无有效组合），该战法本轮无可优化参数。</div>
                      )}
                    </Card>

                    <div style={{ marginTop: 10, fontWeight: 600, fontSize: 14 }}>止盈×止损 热力网格<span style={{ fontSize: 11, color: '#888' }}>（格值 %：该格跨持仓/门槛最优期望；点击格高亮）</span></div>
                    {optCurHeat.tps.length ? (
                      <Table data={heatData} columns={heatColumns} rowKey="tp" size="small" pagination={false} />
                    ) : <Card style={{ padding: 8 }}>本行无网格数据（旧任务产物，重新寻优后生成）。</Card>}

                    {optCurBatches.length > 0 && (
                      <details style={{ marginTop: 8 }}>
                        <summary style={{ cursor: 'pointer', fontSize: 12, color: '#888' }}>批次冠军明细（{optCurBatches.length} 批淘汰赛过程）</summary>
                        <Table
                          size="small"
                          pagination={false}
                          rowKey="batch"
                          data={optCurBatches.map((b) => ({ ...b }))}
                          columns={[
                            { colKey: 'batch', title: '批', cell: ({ row }) => '#' + row.batch },
                            { colKey: 'tp', title: '止盈%', cell: ({ row }) => fmtNum(row.tp) },
                            { colKey: 'sl', title: '止损%', cell: ({ row }) => fmtNum(row.sl) },
                            { colKey: 'hold_days', title: '持仓天', cell: ({ row }) => fmtNum(row.hold_days) },
                            { colKey: 'min_score', title: '门槛', cell: ({ row }) => fmtNum(row.min_score) },
                            { colKey: 'objective', title: '目标值', cell: ({ row }) => fmtNum(row.objective, 3) },
                          ]}
                        />
                      </details>
                    )}
                  </div>
                )}
              </Card>
            )}
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="library" label="战法库">
          <div style={{ marginTop: 12 }}>
            <Card title="战法库（已应用因子战法）" style={{ marginBottom: 12 }}>
              <div style={{ display: 'flex', gap: 8 }}>
                <Button theme="warning" size="small" onClick={() => { setActiveTab('candidates'); setCandSubTab('optimize'); loadOptimizations() }}>⚙ 参数寻优</Button>
                <Button theme="default" variant="outline" size="small" loading={loadingLibrary} onClick={loadLibrary}>刷新</Button>
              </div>
            </Card>

            <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', margin: '8px 0' }}>
              {builtinPatterns.map((b) => (
                <Card key={'card-' + b.id} style={{ flex: '1 1 140px', borderColor: expandedStrategy === 'bt:' + b.id ? '#4c8dff' : undefined }}>
                  <div onClick={() => selectStrategy('bt:' + b.id)} style={{ cursor: 'pointer' }}>
                  <div style={{ fontWeight: 700, fontSize: 13 }}>{b.name}</div>
                  <div style={{ fontSize: 12, color: '#888' }}>{summarizeJob(latestLibJob(b.id, -1))}</div>
                  </div>
                </Card>
              ))}
            </div>

            <div style={{ fontWeight: 600, margin: '8px 0', fontSize: 14 }}>内置形态战法（{builtinPatterns.length}）</div>
            {builtinPatterns.map((b) => {
              const expanded = expandedStrategy === 'bt:' + b.id
              // 常量 expanded：局部定义
              const job = latestLibJob(b.id, -1)
              // 常量 job：局部定义
              return (
                <Card key={'bt-' + b.id} ref={setStrategyRef('bt:' + b.id)} style={{ marginBottom: 10 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
                    <b style={{ fontSize: 13, fontWeight: 600 }}>{b.name}</b>
                    <Tag>内置</Tag>
                    <Tag theme="success">实盘常驻</Tag>
                    <span style={{ fontSize: 11, color: '#777', marginLeft: 'auto' }}>最新: {summarizeJob(job)}</span>
                  </div>
                  <div style={{ display: 'flex', gap: 8, marginTop: 6, flexWrap: 'wrap' }}>
                    <Button size="small" theme="warning" onClick={() => doLibraryBacktest(b)}>回测此战法</Button>
                    <Button size="small" variant="outline" onClick={() => selectStrategy('bt:' + b.id)}>{expanded ? '收起详情' : '详情'}</Button>
                  </div>
                  {expanded && (
                    <div style={{ marginTop: 8 }}>
                      {!job && <div style={{ fontSize: 12, color: '#888' }}>尚未回测——点「回测此战法」发起历史日K回放（结果进「回测」tab）</div>}
                      {job && job.status === 'running' && <div>回测中 {jobPct(job)}</div>}
                      {job && job.result_text && <pre style={{ fontSize: 12, color: '#ccc', whiteSpace: 'pre-wrap' }}>{job.result_text}</pre>}
                      {job && job.status === 'error' && <div style={{ color: '#e34d59' }}>上次回测失败：{job.error}</div>}
                    </div>
                  )}
                </Card>
              )
            })}

            {ruleGroups.length === 0 && <Card>暂无已应用因子/形态战法。审批通过的候选会自动加入战法库并注入实盘。</Card>}
            {ruleGroups.map(renderLibraryGroup)}
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="backtests" label="回测">
          <div style={{ marginTop: 12 }}>
            <Card title="发起 / 重跑全量回测" style={{ marginBottom: 12 }}>
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                <Input value={btStart} onChange={(v) => setBtStart(v)} placeholder="开始 20230801" style={{ width: 130 }} />
                <Input value={btEnd} onChange={(v) => setBtEnd(v)} placeholder="结束(留空=今天)" style={{ width: 150 }} />
                <Input value={btTopK} onChange={(v) => setBtTopK(v)} placeholder="选股数" style={{ width: 90 }} />
                <Input value={btMinStocks} onChange={(v) => setBtMinStocks(v)} placeholder="最小样本" style={{ width: 100 }} />
              </div>
              <div style={{ display: 'flex', gap: 8, marginTop: 8, alignItems: 'center', flexWrap: 'wrap' }}>
                <Select value={btPickId} onChange={(v) => setBtPickId(v)} disabled={btLoading} style={{ width: 260 }} options={[
                  { label: '选择待审批因子候选', value: 0, disabled: true },
                  ...btCandidates.map((c) => ({ label: `#${c.id} ${c.kind === 'pattern' ? '形态战法' : '因子战法'}（${c.kind === 'pattern' ? ('触发 ' + (c.triggers ?? '规则')) : ('IC ' + fmt(c.ic_mean) + '，IR ' + fmt(c.ir))}）`, value: c.id })),
                ]} />
                <Button theme="warning" disabled={btPickId === 0} onClick={() => doBacktestById(btPickId)}>发起全量回测</Button>
                <Button theme="default" variant="outline" loading={btLoading} onClick={loadBacktests}>刷新列表</Button>
              </div>
              <div style={{ marginTop: 8, fontSize: 12, color: '#888' }}>
                任务统一走研究队列：手动回测为高优先级，夜间自动研究为低优先级；高优先级到来会自动让路（被抢占任务断点续跑）。所有任务仅在盘后窗口执行。
              </div>
            </Card>

            <Card title="指标说明">
              <Button size="small" variant="text" theme="primary" onClick={() => setShowMetricHelp(!showMetricHelp)}>{showMetricHelp ? '收起指标说明' : '📖 这些指标是什么意思？（点开看解释）'}</Button>
              {showMetricHelp && (
                <div style={{ fontSize: 12, color: '#aaa', marginTop: 8, lineHeight: 1.8 }}>
                  <div><b>触发信号数</b>：历史区间里该战法一共发出过多少次买入机会。太少(&lt;50)说明统计意义弱。</div>
                  <div><b>胜率</b>：赚钱笔数占比。短线战法 40%~50% 就不错——靠盈亏比赚钱。</div>
                  <div><b>盈亏比</b>：平均每笔赚的 ÷ 平均每笔亏的。&gt;1 才有意义，&gt;1.2 算优秀。</div>
                  <div><b>平均持仓天数</b>：资金占用效率。</div>
                  <div><b>回测超额</b>：相对基准(指数)的超额收益（B4 全链路口径），&gt;0 跑赢大盘。</div>
                </div>
              )}
            </Card>

            {backtestJobs.length === 0 && <Card style={{ marginTop: 12 }}>暂无回测任务。选择上方候选发起全量回测，或等待夜间调度器产出。</Card>}
            {backtestJobs.length > 0 && (
              <Card style={{ marginTop: 12 }} title="回测任务">
                <Table data={backtestJobs} columns={btColumns} rowKey={(r) => (r.kind || 'candidate') + ':' + r.candidate_id} size="small" pagination={{ pageSize: 10, showJumper: true }} />
              </Card>
            )}
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="settings" label="设置">
          <div style={{ marginTop: 12 }}>
            <Card title="研究调度设置">
              <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 13, fontWeight: 600 }}>全量回测全局开关</div>
                  <div style={{ fontSize: 12, color: '#888', marginTop: 4, lineHeight: 1.5 }}>开启后，夜间自动研究在发现因子候选后会追加一次 B4 全链路回测（回填回测超额）；关闭则只做发现、不做回测，省时省 CPU。</div>
                </div>
                <ToggleSw checked={backtestEnabled} onChange={(v) => { setBacktestEnabled(v); saveBacktestToggle(v) }} />
                <Tag theme={backtestEnabled ? 'success' : 'default'}>{backtestEnabled ? '已开启' : '已关闭'}</Tag>
              </div>
            </Card>
          </div>
        </Tabs.TabPanel>
      </Tabs>

      {/* 寻优参数池 / 池纪律 设置弹窗 */}
      <Dialog visible={optDrawerOpen} onClose={() => setOptDrawerOpen(false)} header="参数池 / 池纪律" onConfirm={undefined}>
        <div style={{ fontWeight: 600, margin: '8px 0', fontSize: 14 }}>该战法寻优参数池（步进搜索空间，保存后下次寻优生效）</div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 8 }}>
          <label>止盈起<input type="number" step="1" value={cfgSweep.tp_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, tp_from: +e.target.value }))} /></label>
          <label>止盈终<input type="number" step="1" value={cfgSweep.tp_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, tp_to: +e.target.value }))} /></label>
          <label>步长<input type="number" step="1" min="0.5" value={cfgSweep.tp_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, tp_step: +e.target.value }))} /></label>
          <label>止损起<input type="number" step="1" value={cfgSweep.sl_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, sl_from: +e.target.value }))} /></label>
          <label>止损终<input type="number" step="1" value={cfgSweep.sl_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, sl_to: +e.target.value }))} /></label>
          <label>步长<input type="number" step="1" min="0.5" value={cfgSweep.sl_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, sl_step: +e.target.value }))} /></label>
          <label>持仓起(天)<input type="number" step="1" value={cfgSweep.hold_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, hold_from: +e.target.value }))} /></label>
          <label>持仓终(天)<input type="number" step="1" value={cfgSweep.hold_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, hold_to: +e.target.value }))} /></label>
          <label>步长(天)<input type="number" step="1" min="1" value={cfgSweep.hold_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, hold_step: +e.target.value }))} /></label>
          <label>门槛起<input type="number" step="5" value={cfgSweep.score_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, score_from: +e.target.value }))} /></label>
          <label>门槛终<input type="number" step="5" value={cfgSweep.score_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, score_to: +e.target.value }))} /></label>
          <label>步长<input type="number" step="1" min="1" value={cfgSweep.score_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, score_step: +e.target.value }))} /></label>
        </div>
        <div style={{ fontSize: 12, margin: '6px 0' }}>
          预估组合数 <b>{sweepComboEstimate.toLocaleString()}</b>（引擎按 ≤5000 组合/批 分批全量模拟后批冠军 PK）
          {sweepComboEstimate > 100000 && <span style={{ color: '#e34d59' }}>超上限 100000，请放宽步长</span>}
        </div>
        <Button theme="primary" onClick={saveSweepPool}>保存参数池</Button>

        <div style={{ marginTop: 14, fontWeight: 600, fontSize: 14 }}>对应资金池买入纪律（模拟盘实时生效）</div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4,1fr)', gap: 8 }}>
          <label>日限买<input type="number" min="0" step="1" value={cfgRule.max_daily_buys ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, max_daily_buys: +e.target.value }))} /></label>
          <label>冷却(分)<input type="number" min="0" step="5" value={cfgRule.cooldown_minutes ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, cooldown_minutes: +e.target.value }))} /></label>
          <label>最低分<input type="number" min="0" max="100" step="1" value={cfgRule.min_score ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, min_score: +e.target.value }))} /></label>
          <label>日预算%<input type="number" min="0" max="100" step="5" value={cfgRule.budget_pct_per_day ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, budget_pct_per_day: +e.target.value }))} /></label>
        </div>
        <div style={{ fontSize: 11, color: '#888', margin: '4px 0' }}>
          目标池：<b>{optCurPoolKey || '（未知战法不下发）'}</b>；全零=清除该池纪律。
        </div>
        <Button theme="primary" onClick={savePoolDiscipline}>保存池纪律</Button>
        <div style={{ marginTop: 12, textAlign: 'right' }}>
          <Button theme="default" onClick={() => setOptDrawerOpen(false)}>关闭</Button>
        </div>
      </Dialog>

      {/* 任务运行日志弹窗：前端直接查看 researchd 落盘的 task_<id>.log，免去 SSH 翻服务器 */}
      <Dialog visible={logOpen} onClose={closeLog} header={'任务运行日志 #' + logId} onConfirm={undefined}>
        {logLoading && <div style={{ fontSize: 12, color: '#888' }}>加载中…</div>}
        {!logLoading && !logExists && (
          <div style={{ fontSize: 13, color: '#888' }}>暂无日志（任务尚未执行，或 researchd 未写入 task_logs）。</div>
        )}
        {!logLoading && logExists && (
          <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', maxHeight: 420, overflow: 'auto', background: '#0d1117', color: '#c9d1d9', padding: 12, borderRadius: 6, fontSize: 12, lineHeight: 1.6 }}>
            {logContent || '（日志为空）'}
          </pre>
        )}
        <div style={{ fontSize: 11, color: '#888', marginTop: 6 }}>运行中任务每 4 秒自动刷新；关闭弹窗停止刷新。</div>
        <div style={{ marginTop: 12, textAlign: 'right' }}>
          <Button theme="default" onClick={closeLog}>关闭</Button>
        </div>
      </Dialog>
    </div>
  )
}
