// ── 自动研究页面 Research.jsx ──
// 研究候选审批、战法库管理、回测任务中心、参数寻优与资金池纪律配置。
import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import './Research.css'

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
  const [cfgSweep, setCfgSweep] = useState({})
  const [cfgRule, setCfgRule] = useState({})
  const [expandedStrategy, setExpandedStrategy] = useState('')
  const strategyRefs = useRef({})
  const [showMetricHelp, setShowMetricHelp] = useState(false)
  const [adviceOpen, setAdviceOpen] = useState(0)

  const canApprove = api.hasPerm('research_approve')

  const tabs = [
    { key: 'candidates', label: '待审批候选' },
    { key: 'library', label: '战法库' },
    { key: 'backtests', label: '回测' },
    { key: 'settings', label: '设置' },
  ]
  const builtinPatterns = [
    { id: 'double_bump', name: '双响炮' },
    { id: 'dragon', name: '龙头' },
    { id: 'dragon_return', name: '龙回头' },
    { id: 'n_shape', name: 'N形（日K近似）' },
  ]

  // 轮询 / 计时器
  const backtestPollers = useRef({})
  const libPollTimer = useRef(null)
  const pollTimer = useRef(null)

  const setStrategyRef = (key) => (el) => { if (el) strategyRefs.current[key] = el }

  // ===== 通用工具 =====
  function pct(v) {
    if (v === null || v === undefined || isNaN(v)) return 0
    return Math.min(100, Math.round(v * 100))
  }
  function fmtRows(v) {
    if (v === null || v === undefined || isNaN(v)) return '-'
    return Number(v).toLocaleString('zh-CN')
  }
  function fmt(v) {
    if (v === null || v === undefined || isNaN(v)) return '-'
    return Number(v).toFixed(4)
  }
  function signClass(v) {
    if (v === null || v === undefined || isNaN(v)) return ''
    return Number(v) >= 0 ? 'pos' : 'neg'
  }
  function fmtNum(v, digits) {
    if (v === null || v === undefined || isNaN(v)) return '-'
    const d = digits === undefined ? 0 : digits
    return Number(v).toFixed(d)
  }

  // ===== 因子元数据 =====
  const factorMeta = useRef({})
  // 加载因子中英文名称等元数据，用于渲染战法条件
  async function loadFactorMeta() {
    try {
      const res = await api.fetchResearchFactors()
      if (res && Array.isArray(res.factors)) {
        for (const f of res.factors) {
          if (f && f.id) factorMeta.current[f.id] = f
        }
      }
    } catch (_) {}
  }
  function factorName(id) {
    const m = factorMeta.current[id]
    return (m && m.name) ? m.name : id
  }

  function weightList(c) {
    try {
      const w = JSON.parse(c.weights || '{}')
      const wm = (w && typeof w === 'object' && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights)) ? w.weights : w
      if (!wm || typeof wm !== 'object') return []
      const entries = Object.entries(wm).filter(([, v]) => typeof v === 'number' && isFinite(v))
      return entries.sort((a, b) => b[1] - a[1])
    } catch (_) { return [] }
  }
  function depthSummary(c) {
    try { return JSON.parse(c.weights || '{}') } catch (_) { return {} }
  }
  function kindLabel(k) {
    const m = { factor: '因子战法', pattern: '形态战法', weights: '权重优化', depth: '盘口扫描' }
    return m[k] || k
  }
  function factorDirs(c) {
    try {
      const w = JSON.parse(c.weights || '{}')
      if (w && typeof w === 'object' && w.directions && typeof w.directions === 'object') return w.directions
    } catch (_) {}
    return {}
  }
  function factorRule(c) {
    const dirs = factorDirs(c)
    const wm = {}
    try {
      const w = JSON.parse(c.weights || '{}')
      const weightsObj = (w && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights)) ? w.weights : w
      Object.assign(wm, weightsObj)
    } catch (_) {}
    return Object.entries(wm)
      .filter(([, v]) => typeof v === 'number' && isFinite(v))
      .map(([id, weight]) => ({ id, label: factorName(id), weight, dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1 }))
      .sort((a, b) => b.weight - a.weight)
  }
  function parseReason(c, key) {
    const reason = c.reason || ''
    const pats = {
      '样本内IR': /样本内IR=(-?\d+\.?\d*)/,
      '样本外IR': /样本外IR=(-?\d+\.?\d*)/,
      反推超额: /反推超额=(-?\d+\.?\d*)/,
    }
    const m = reason.match(pats[key])
    return m ? parseFloat(m[1]) : null
  }
  function fmtPct(v) {
    if (v === null || v === undefined || isNaN(v)) return '-'
    const s = (v * 100).toFixed(1)
    return (v >= 0 ? '+' : '') + s + '%'
  }
  function verdict(c) {
    const insample = parseReason(c, '样本内IR')
    const outsample = parseReason(c, '样本外IR')
    const gen = parseReason(c, '反推超额')
    const thr = 0.3
    const passed = (insample !== null ? insample >= thr : true) &&
                   (outsample !== null ? outsample >= thr : true) &&
                   (gen !== null ? gen > 0 : true)
    if (passed) return { ok: true, text: '电脑用两段互不相干的历史行情分别验证过，这条规律都能跑赢，不是碰运气。' }
    return { ok: false, text: '这条规律在验证中没站稳，不建议直接拿来实盘。' }
  }
  function plainLines(c) {
    const insample = parseReason(c, '样本内IR')
    const outsample = parseReason(c, '样本外IR')
    const gen = parseReason(c, '反推超额')
    const lines = []
    if (insample !== null) lines.push('先拿前半段历史行情回放：这套打分的选股效果明显（稳定度 ' + fmt(insample) + '，越高越靠谱）。')
    if (outsample !== null) lines.push('再拿一段完全没参与挑规律的行情回放：效果仍然明显（稳定度 ' + fmt(outsample) + '）。这一步是防止规律只对老数据灵、换市场就失灵。')
    if (gen !== null) lines.push('最后对比「按这套规律选出的股票」和「随便买」：选的比平均多赚 ' + fmtPct(gen) + '，说明规律确实挑得出好股票。')
    if (lines.length === 0) lines.push(/通过护栏/.test(c.reason || '') ? '电脑验证通过，这条规律可以试试。' : '这条规律未通过验证。')
    return lines
  }
  function btTested(c) {
    if (typeof c.backtest_done === 'boolean') return c.backtest_done
    return c.avg_excess !== 0
  }
  function btPct(id) {
    const p = backtestProgress[id]
    if (!p) return '0%'
    const n = parseInt(p, 10)
    return (isNaN(n) ? 0 : Math.max(0, Math.min(100, n))) + '%'
  }

  // ===== 进度 / 候选 =====
  // 加载研究任务当前进度
  async function loadProgress() {
    try {
      const p = await api.fetchResearchProgress()
      if (p) setProgress(p)
    } catch (e) { console.error('Research 进度加载失败', e) }
  }
  // 根据状态筛选加载研究候选列表
  async function loadData() {
    setLoading(true)
    try {
      const res = await api.fetchResearchCandidates(statusFilter || '')
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
  // 同时加载研究进度与候选数据
  async function loadAll() {
    setLoading(true)
    await loadProgress()
    await loadData()
    setLoading(false)
  }
  function statusLabel(s) {
    const m = { proposed: '待审批', approved: '已审批', applied: '已应用', rejected: '已驳回' }
    return m[s] || s
  }
  async function doApprove(c) {
    try {
      await api.approveResearchCandidate(c.id)
      c.status = 'applied'
      alert('候选 #' + c.id + ' 已审批并应用')
    } catch (e) { alert('审批失败: ' + (e.message || e)) }
  }
  async function doReject(c) {
    try {
      await api.rejectResearchCandidate(c.id)
      c.status = 'rejected'
      alert('候选 #' + c.id + ' 已驳回')
    } catch (e) { alert('驳回失败: ' + (e.message || e)) }
  }

  // ===== 战法库 =====
  function ruleFactors(s) {
    const dirs = s.directions || {}
    const wm = s.weights || {}
    return Object.entries(wm)
      .filter(([, v]) => typeof v === 'number' && isFinite(v))
      .map(([id, weight]) => ({ id, label: factorName(id), weight, dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1 }))
      .sort((a, b) => b.weight - a.weight)
  }
  function condLabel(c) {
    const name = factorName(c.factor || '')
    const min = (c.min !== undefined && c.min !== null) ? c.min : '-∞'
    const max = (c.max !== undefined && c.max !== null) ? c.max : '+∞'
    return name + ' ∈ [' + min + ', ' + max + ')'
  }
  async function loadLibrary() {
    setLoadingLibrary(true)
    try {
      const res = await api.fetchResearchLibrary()
      if (res && Array.isArray(res.library)) setLibrary(res.library)
    } catch (e) { console.error('战法库加载失败', e) }
    finally { setLoadingLibrary(false) }
  }
  async function toggleLibrary(s) {
    try {
      await api.setResearchLibraryEnabled(s.id, !s.enabled)
      s.enabled = !s.enabled
      alert('战法 ' + s.name + (s.enabled ? ' 已启用（已注入 8a/8b 实盘）' : ' 已停用'))
    } catch (e) { alert('操作失败: ' + (e.message || e)) }
  }
  async function removeLibrary(s) {
    if (!confirm('确定删除战法 ' + s.name + ' ？删除后不再注入 8a/8b 实盘。')) return
    try {
      await api.deleteResearchLibrary(s.id)
      setLibrary((l) => l.filter((x) => x.id !== s.id))
      alert('战法 ' + s.name + ' 已删除')
    } catch (e) { alert('删除失败: ' + (e.message || e)) }
  }
  function startRename(s) {
    setEditingName((e) => ({ ...e, [s.id]: true }))
    setNameDraft((d) => ({ ...d, [s.id]: s.name }))
  }
  async function saveName(s) {
    const name = (nameDraft[s.id] || '').trim()
    setEditingName((e) => ({ ...e, [s.id]: false }))
    if (!name || name === s.name) return
    try {
      await api.renameResearchLibrary(s.id, name)
      s.name = name
      alert('战法已重命名为 ' + name)
    } catch (e) { alert('改名失败: ' + (e.message || e)) }
  }

  // ===== 回测开关 =====
  async function loadBacktestToggle() {
    try {
      const res = await api.fetchBacktestToggle()
      if (res && typeof res.enabled === 'boolean') setBacktestEnabled(res.enabled)
    } catch (e) { console.error('加载回测开关失败', e) }
  }
  async function saveBacktestToggle() {
    try {
      await api.setBacktestToggle(backtestEnabled)
      alert('全量回测全局开关已' + (backtestEnabled ? '开启' : '关闭'))
    } catch (e) {
      alert('保存失败: ' + (e.message || e))
      setBacktestEnabled(!backtestEnabled)
    }
  }

  // ===== 单候选回测 + 轮询 =====
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
      alert('回测启动失败: ' + (e.message || e))
      return
    }
    pollBacktest(c)
  }
  async function doCancelBacktest(id) {
    if (!confirm('取消候选 #' + id + ' 的回测？（已算完的事件保留缓存，续跑只算剩余）')) return
    try { await api.cancelBacktest(id); loadBacktests() } catch (e) { alert('取消失败: ' + (e.message || e)) }
  }
  async function doPauseBacktest(id) {
    try { await api.pauseBacktest(id); loadBacktests() } catch (e) { alert('暂停失败: ' + (e.message || e)) }
  }
  async function doResumeBacktest(id) {
    try { await api.resumeBacktest(id); loadBacktests() } catch (e) { alert('恢复失败: ' + (e.message || e)) }
  }

  // ===== 寻优 =====
  function optObjectiveLabel(o) {
    return { profitfactor: '盈亏比', profitFactor: '盈亏比', winrate: '胜率', winRate: '胜率', avgwin: '平均盈利', avgWin: '平均盈利', expectancy: '期望收益' }[o] || (o || '-')
  }
  async function loadOptimizations() {
    if (loadingOpts) return
    setLoadingOpts(true)
    try {
      const res = await api.fetchOptimizations()
      setOptTasks(res.optimizations || [])
    } catch (e) { console.warn('寻优结果加载失败', e) }
    finally { setLoadingOpts(false) }
  }
  async function startOptimize() {
    if (!confirm('发起全库战法参数寻优？\n目标：' + optObjectiveLabel(optObjective) + '，盘后窗口执行，完成后排名出现在本页。')) return
    try {
      await api.enqueueOptimize({ objective: optObjective })
      setOptLaunching(true)
      alert('已加入研究队列——进度可在「回测」tab 查看，完成后回到「优化结果」刷新。')
    } catch (e) { alert('发起失败: ' + (e.message || e)) }
  }
  async function approveOpt(r) {
    const msg = '把参数应用到「' + r.strategy + '」？\n止盈线 ' + fmtNum(r.params.take_profit_pct) + '% · 止损线 ' +
      fmtNum(r.params.stop_loss_pct) + '% · 兜底 ' + fmtNum(r.params.hold_days) + ' 天' +
      ((r.params || {}).min_score ? ' · 门槛 ' + fmtNum(r.params.min_score) : '') + '\n审批后立即热重载生效。'
    if (!confirm(msg)) return
    try { await api.approveOptimization(r.id); r.status = 'approved' } catch (e) { alert('入库失败: ' + (e.message || e)) }
  }
  async function rejectOpt(r) {
    try { await api.rejectOptimization(r.id); r.status = 'rejected' } catch (e) { alert('操作失败: ' + (e.message || e)) }
  }

  const optStrategies = useMemo(() => {
    if (!optTasks.length) return []
    const rows = optTasks[0].results || []
    return rows.map((r) => ({ key: r.strategy_kind || r.strategy, label: r.strategy, bestExp: (r.expectancy !== undefined && r.expectancy !== null) ? Number(r.expectancy) : null }))
  }, [optTasks])
  const optCur = useMemo(() => {
    for (const t of optTasks) {
      const hit = (t.results || []).find((r) => (r.strategy_kind || r.strategy) === optSelected)
      if (hit) return { ...hit, task_id: t.task_id }
    }
    return null
  }, [optTasks, optSelected])
  const optCurHeat = useMemo(() => {
    const empty = { tps: [], sls: [], map: {} }
    if (!optCur || !optCur.grid_json) return empty
    try {
      const extra = JSON.parse(optCur.grid_json)
      const cells = extra.grid || []
      const tps = [...new Set(cells.map((c) => Number(c.tp)))].sort((a, b) => a - b)
      const sls = [...new Set(cells.map((c) => Number(c.sl)))].sort((a, b) => a - b)
      const map = {}
      cells.forEach((c) => { map[c.tp + '|' + c.sl] = c.expectancy })
      return { tps, sls, map }
    } catch { return empty }
  }, [optCur])
  function heatVal(tp, sl) {
    const v = optCurHeat.map[tp + '|' + sl]
    return (v === undefined || v === null) ? null : v
  }
  function heatColor(exp) {
    if (exp === null || exp === undefined || isNaN(Number(exp))) return 'transparent'
    const clamped = Math.max(-5, Math.min(5, exp))
    const alpha = 0.12 + Math.abs(clamped) / 5 * 0.55
    return clamped >= 0 ? `rgba(34,197,94,${alpha.toFixed(2)})` : `rgba(239,68,68,${alpha.toFixed(2)})`
  }
  const optCurBatches = useMemo(() => {
    if (!optCur || !optCur.grid_json) return []
    try { return JSON.parse(optCur.grid_json).batches || [] } catch { return [] }
  }, [optCur])
  const optCurPoolKey = useMemo(() => {
    if (!optCur) return ''
    if ((optCur.strategy_kind || '').startsWith('fac_')) return 'factor'
    if ((optCur.strategy_kind || '').startsWith('pat_')) return 'pattern'
    return ({ '双响炮': 'double_bump', '龙头': 'dragon', 'N形': 'n_shape', '龙回头': 'dragon_return' })[optCur.strategy] || ''
  }, [optCur])
  const sweepComboEstimate = useMemo(() => {
    const c = cfgSweep
    const nF = (f, t, s) => (s > 0 && t >= f) ? Math.round((t - f) / s) + 1 : 1
    const nI = (f, t, s) => (s > 0 && t >= f) ? Math.floor((t - f) / s) + 1 : 1
    return nF(+c.tp_from || 0, +c.tp_to || 0, +c.tp_step || 0) *
           nF(+c.sl_from || 0, +c.sl_to || 0, +c.sl_step || 0) *
           nI(+c.hold_from || 0, +c.hold_to || 0, +c.hold_step || 0) *
           nF(+c.score_from || 0, +c.score_to || 0, +c.score_step || 0)
  }, [cfgSweep])
  async function toggleDrawer() {
    if (optDrawerOpen) { setOptDrawerOpen(false); return }
    const def = { tp_from: 5, tp_to: 30, tp_step: 5, sl_from: 3, sl_to: 15, sl_step: 3, hold_from: 2, hold_to: 30, hold_step: 2, score_from: 40, score_to: 95, score_step: 5 }
    setCfgSweep(def)
    try {
      const res = await api.fetchSweepPools()
      const mine = (res.pools || []).find((p) => p.strategy === (optCur ? optCur.strategy : ''))
      if (mine) setCfgSweep({ ...def, ...mine, tp_from: mine.tp_from, tp_to: mine.tp_to, tp_step: mine.tp_step, sl_from: mine.sl_from, sl_to: mine.sl_to, sl_step: mine.sl_step, hold_from: mine.hold_from, hold_to: mine.hold_to, hold_step: mine.hold_step, score_from: mine.score_from, score_to: mine.score_to, score_step: mine.score_step })
    } catch {}
    setCfgRule({ max_daily_buys: 0, cooldown_minutes: 0, min_score: 0, budget_pct_per_day: 0 })
    try {
      const ps = await api.fetchPaperState()
      const pk = optCurPoolKey
      const pool = ((ps.strategy_pools) || []).find((p) => p.key === pk)
      if (pool && pool.buy_rule) setCfgRule({ max_daily_buys: pool.buy_rule.max_daily_buys || 0, cooldown_minutes: pool.buy_rule.cooldown_minutes || 0, min_score: pool.buy_rule.min_score || 0, budget_pct_per_day: pool.buy_rule.budget_pct_per_day || 0 })
    } catch {}
    setOptDrawerOpen(true)
  }
  async function saveSweepPool() {
    if (!optCur) return
    try {
      const res = await api.saveSweepPool({ strategy: optCur.strategy, ...cfgSweep })
      alert(`参数池已保存——预估 ${Number(res.combos).toLocaleString()} 组合`)
    } catch (e) { alert('保存失败: ' + (e.message || e)) }
  }
  async function savePoolDiscipline() {
    const pk = optCurPoolKey
    if (!pk) { alert('未知战法无法映射资金池'); return }
    const r = cfgRule
    const rule = { max_daily_buys: parseInt(r.max_daily_buys, 10) || 0, cooldown_minutes: parseInt(r.cooldown_minutes, 10) || 0, min_score: parseFloat(r.min_score) || 0, budget_pct_per_day: parseFloat(r.budget_pct_per_day) || 0 }
    try {
      await api.configPaperPools(null, null, null, { [pk]: rule })
      alert('池纪律已保存并即时生效')
    } catch (e) { alert('保存失败: ' + (e.message || e)) }
  }

  // ===== 回测任务中心 =====
  function toggleAdvice(id) { setAdviceOpen((a) => (a === id ? 0 : id)) }
  function jobParams(j) {
    if (!j.params_json) return ''
    try {
      const p = JSON.parse(j.params_json)
      const parts = []
      if (p.start) parts.push(p.start + '~' + (p.end || '今'))
      if (p.top_k) parts.push('每次选 ' + p.top_k + ' 只')
      if (p.min_stocks) parts.push('最少样本 ' + p.min_stocks)
      if (p.maxstocks) parts.push('池子 ' + p.maxstocks + ' 只')
      return parts.join(' · ')
    } catch { return '' }
  }
  function metricAdvices(j) {
    const out = []
    const lines = (j.result_text || '').split('\n')
    for (const line of lines) {
      const m = line.match(/^【(.+?)】胜率 ([\d.]+)% 盈亏比 ([\d.]+) 触发 (\d+) 持仓 ([\d.]+)天/)
      if (!m) continue
      const [, name, wrS, pfS, nS, holdS] = m
      const wr = parseFloat(wrS), pf = parseFloat(pfS), n = parseInt(nS, 10), hold = parseFloat(holdS)
      const ev = wr / 100 * pf - (1 - wr / 100)
      const tips = []
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
    const n = parseInt(String(id || '').replace(/^[a-z]+_/, ''), 10)
    return Number.isFinite(n) ? n : -1
  }
  function latestLibJob(sk, num) {
    let best = null
    for (const j of backtestJobs) {
      if (j.kind !== 'library' || (j.strategy_kind || '') !== sk) continue
      if (num >= 0 && j.candidate_id !== num) continue
      if (!best || (j.id || 0) > (best.id || 0)) best = j
    }
    return best
  }
  function summarizeJob(j) {
    if (!j) return '未回测'
    if (j.status === 'running') return '回测中 ' + jobPct(j)
    if (j.status === 'queued') return '排队中·盘后执行'
    if (j.status === 'interrupted') return '已中断·可续跑'
    if (j.status === 'error') return '回测失败'
    const line = (j.result_text || '').split('\n')[0] || ''
    return line.replace(/^【[^】]*】/, '').trim() || '已完成'
  }
  function queueHint(j) {
    const ahead = backtestJobs.filter((x) => x.status === 'running' || (x.status === 'queued' && (x.id || 0) < (j.id || 0))).length
    return ahead > 0 ? `排队中：前方还有 ${ahead} 个任务，将按优先级依次执行` : '已加入队列，将在非交易时段自动执行（交易日盘后起 / 非交易日全天；绝不进入盘中）'
  }
  function libraryJobLabel(j) {
    if (j.candidate_id === 0) return '夜间全量回放'
    return builtinLabel(j.candidate_id)
  }
  async function rerunLibrary(j) {
    const sk = j.strategy_kind
    const id = ['double_bump', 'dragon', 'dragon_return', 'n_shape'].includes(sk) ? sk : (sk === 'factor' ? 'fac_' : 'pat_') + j.candidate_id
    try { await api.backtestLibraryRule(id, {}); startLibPoll(); await loadBacktests() } catch (e) { alert('发起失败: ' + (e.message || e)) }
  }
  function builtinLabel(num) {
    const m = { 901: '双响炮', 902: '龙头', 903: '龙回头', 904: 'N形' }
    return m[num] || ('规则 ' + num)
  }
  function selectStrategy(key) {
    setExpandedStrategy((e) => (e === key ? '' : key))
    const el = strategyRefs.current[key]
    if (expandedStrategy !== key && el && el.scrollIntoView) el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }
  async function doLibraryBacktest(s) {
    if (!confirm(`回测战法「${s.name || s.id}」？（历史日K回放，结果进「回测」tab）`)) return
    try {
      await api.backtestLibraryRule(s.id, { start: btStart.trim(), end: btEnd.trim() })
      setActiveTab('backtests')
      await loadBacktests()
      startLibPoll()
    } catch (e) { alert('发起失败: ' + (e.message || e)) }
  }
  function startLibPoll() {
    if (libPollTimer.current) return
    libPollTimer.current = setInterval(async () => {
      await loadBacktests()
      const busy = backtestJobs.some((j) => j.kind === 'library' && (j.status === 'running' || j.status === 'paused' || j.status === 'queued'))
      if (!busy) { clearInterval(libPollTimer.current); libPollTimer.current = null }
    }, 5000)
  }
  function pollBacktest(c) {
    const id = c.id
    if (backtestPollers.current[id]) return
    backtestPollers.current[id] = setInterval(async () => {
      try {
        const j = await api.fetchBacktestStatus(id)
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
          alert('候选 #' + id + ' 回测完成，回测超额 ' + (j.avg_excess !== undefined ? (j.avg_excess * 100).toFixed(2) + '%' : '0%'))
          loadLibrary()
        } else if (j.status === 'error') {
          clearPoll(id)
          setBacktestLoading((b) => ({ ...b, [id]: false }))
          setBacktestState((b) => ({ ...b, [id]: 'error' }))
          setBacktestProgress((p) => ({ ...p, [id]: null }))
          syncJobIntoList({ status: 'error', candidate_id: id, progress: '100%', error: j.error })
          alert('候选 #' + id + ' 回测失败: ' + (j.error || ''))
        }
      } catch (e) {}
    }, 5000)
  }
  function clearPoll(id) {
    if (backtestPollers.current[id]) { clearInterval(backtestPollers.current[id]); delete backtestPollers.current[id] }
  }
  async function restoreRunningBacktests() {
    try {
      const res = await api.fetchRunningBacktests()
      const jobs = (res && res.jobs) || []
      for (const j of jobs) {
        if (j.kind !== 'candidate') continue
        if (j.status !== 'running' && j.status !== 'queued') continue
        const cand = candidates.find((x) => x.id === j.candidate_id)
        const c = cand || { id: j.candidate_id, kind: 'factor' }
        setBacktestLoading((b) => ({ ...b, [c.id]: true }))
        setBacktestState((b) => ({ ...b, [c.id]: 'running' }))
        if (j.progress) setBacktestProgress((p) => ({ ...p, [c.id]: j.progress }))
        pollBacktest(c)
      }
    } catch (e) { console.error('恢复运行中回测任务失败', e) }
  }
  async function loadBacktests() {
    setBtLoading(true)
    try {
      const res = await api.fetchAllBacktests()
      if (res && Array.isArray(res.jobs)) {
        const seen = new Set()
        setBacktestJobs(res.jobs.filter((j) => {
          const k = (j.kind || 'candidate') + ':' + j.candidate_id
          if (seen.has(k)) return false
          seen.add(k)
          return true
        }))
      }
    } catch (e) { console.error('加载回测任务失败', e) }
    finally { setBtLoading(false) }
  }
  function syncJobIntoList(j) {
    setBacktestJobs((jobs) => {
      const jk = j.kind || 'candidate'
      const idx = jobs.findIndex((x) => (x.kind || 'candidate') === jk && x.candidate_id === j.candidate_id)
      const merged = { ...(idx >= 0 ? jobs[idx] : { id: j.task_id }), ...j, kind: jk }
      if (idx >= 0) { const copy = jobs.slice(); copy[idx] = merged; return copy }
      return [merged, ...jobs]
    })
  }
  function doBacktestById(id) {
    const c = candidates.find((x) => x.id === id)
    if (c) doBacktest(c)
    else alert('候选 #' + id + ' 不存在（请先刷新候选列表）')
  }
  function btStatusLabel(s) {
    const m = { running: '运行中', paused: '已暂停', done: '已完成', error: '失败', interrupted: '已中断', queued: '排队中·盘后执行', preempted: '已中断' }
    return m[s] || s
  }
  function ctrlId(j) {
    return j.kind === 'library' ? 1000000000 + j.candidate_id : j.candidate_id
  }
  function jobPct(j) {
    const p = j.progress || '0%'
    const n = parseInt(p, 10)
    return (isNaN(n) ? 0 : Math.max(0, Math.min(100, n))) + '%'
  }
  const btCandidates = useMemo(() => candidates.filter((c) => (c.kind === 'factor' || c.kind === 'pattern') && c.status === 'proposed'), [candidates])

  // 分组
  const libGroupFactors = useMemo(() => library.filter((s) => s.kind !== 'pattern'), [library])
  const libGroupPatterns = useMemo(() => library.filter((s) => s.kind === 'pattern'), [library])
  const ruleGroups = useMemo(() => [
    { key: 'factor', title: '因子战法', items: libGroupFactors },
    { key: 'pattern', title: '形态战法', items: libGroupPatterns },
  ].filter((g) => g.items.length), [libGroupFactors, libGroupPatterns])

  // ===== 生命周期 =====
  function startPolling() {
    if (pollTimer.current) return
    pollTimer.current = setInterval(loadProgress, 30000)
  }
  function stopPolling() {
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

  // 兜底渲染错误
  if (renderError) {
    return (
      <div className="research-page">
        <div style={{ background: '#fef2f2', color: '#b91c1c', border: '1px solid #fecaca', borderRadius: 6, padding: '8px 12px', marginBottom: 8, fontSize: 12 }}>
          页面渲染出错（不影响其他标签页）：<code>{renderError}</code>
          <button className="btn-refresh" style={{ marginLeft: 8 }} onClick={() => setRenderError('')}>关闭</button>
        </div>
      </div>
    )
  }

  return (
    <div className="research-page">
      <div className="page-header">
        <h2>自动研究</h2>
        <div className="header-right">
          <select value={statusFilter} className="status-filter" onChange={(e) => { setStatusFilter(e.target.value); loadData() }}>
            <option value="">全部</option>
            <option value="proposed">待审批</option>
            <option value="applied">已应用</option>
            <option value="approved">已审批</option>
            <option value="rejected">已驳回</option>
          </select>
          <button className="btn-refresh" onClick={loadAll} disabled={loading}>
            {loading ? '加载中...' : '刷新'}
          </button>
        </div>
      </div>

      <div className="research-tabs">
        {tabs.map((t) => (
          <button key={t.key} className={'tab' + (activeTab === t.key ? ' active' : '')} onClick={() => setActiveTab(t.key)}>{t.label}</button>
        ))}
      </div>

      {activeTab === 'candidates' && (
        <>
          <div className="research-tabs" style={{ marginBottom: 8 }}>
            <button className={'tab' + (candSubTab === 'patterns' ? ' active' : '')} onClick={() => setCandSubTab('patterns')}>形态候选</button>
            <button className={'tab' + (candSubTab === 'optimize' ? ' active' : '')} onClick={() => { setCandSubTab('optimize'); loadOptimizations() }}>优化结果</button>
          </div>

          <div style={{ display: candSubTab === 'patterns' ? 'block' : 'none' }}>
            {progress && (
              <div className="progress-panel">
                <div className="progress-title">研究处理进度
                  {progress.data_source && <span className="tag status-applied" style={{ marginLeft: 8, fontSize: 11 }} title={'回测/研究取数主源：' + progress.data_source}>数据源: {progress.data_source}</span>}
                </div>
                <div className="progress-grid">
                  <div className="progress-item">
                    <div className="progress-label">数据准备度（近一年有行情 / 全市场）</div>
                    <div className="progress-bar"><div className="progress-fill" style={{ width: pct(progress.ready_pct) + '%' }}></div></div>
                    <div className="progress-meta">{progress.ready_stocks} / {progress.stocks} 只（{pct(progress.ready_pct)}%）</div>
                  </div>
                  <div className="progress-item">
                    <div className="progress-label">日线数据</div>
                    <div className="progress-meta">{fmtRows(progress.daily_rows)} 行</div>
                  </div>
                  <div className="progress-item">
                    <div className="progress-label">财务指标</div>
                    <div className="progress-meta">{fmtRows(progress.fin_rows)} 行</div>
                  </div>
                  <div className="progress-item">
                    <div className="progress-label">研究候选</div>
                    <div className="progress-meta">
                      <span className="meta-chip">{progress.candidates} 条</span>
                      {progress.applied && <span className="meta-chip">已应用 {progress.applied}</span>}
                      {progress.proposed && <span className="meta-chip">待审批 {progress.proposed}</span>}
                    </div>
                  </div>
                </div>
              </div>
            )}
          </div>

          <div style={{ display: candSubTab === 'optimize' ? 'block' : 'none' }} className="library-panel">
            <div className="library-header">
              <div className="library-title">参数寻优中心<span style={{ fontSize: 12, color: '#888' }}>（每战法独立寻优池：止盈×止损×持仓×门槛 步进网格，批内选优+批间 PK）</span></div>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <select value={optObjective} className="bt-select" style={{ width: 'auto', padding: '4px 8px' }} onChange={(e) => setOptObjective(e.target.value)}>
                  <option value="profitFactor">目标：盈亏比</option>
                  <option value="winRate">目标：胜率</option>
                  <option value="avgWin">目标：平均盈利</option>
                  <option value="expectancy">目标：期望收益</option>
                </select>
                <button className="btn-backtest" onClick={startOptimize} disabled={optLaunching}>{optLaunching ? '已入队…' : '⚙ 发起全库寻优'}</button>
                <button className="btn-refresh" onClick={loadOptimizations}>刷新</button>
              </div>
            </div>

            {optStrategies.length > 0 && (
              <div className="opt-chips">
                {optStrategies.map((s) => (
                  <button key={s.key} className={'tab opt-chip' + (optSelected === s.key ? ' active' : '')} onClick={() => setOptSelected(s.key)}>
                    {s.label}
                    <em style={(s.bestExp ?? 0) >= 0 ? { color: '#22c55e' } : { color: '#ef4444' }}>
                      {s.bestExp !== null ? ((s.bestExp >= 0 ? '+' : '') + fmtNum(s.bestExp, 2) + '%') : ''}
                    </em>
                  </button>
                ))}
              </div>
            )}

            {loadingOpts && <div className="empty">加载中...</div>}
            {!loadingOpts && !optCur && <div className="empty">暂无寻优结果——点右上「发起全库寻优」，任务进研究队列，完成后排名自动落库到这里。</div>}

            {!loadingOpts && optCur && (
              <>
                <div className="opt-champion">
                  <div className="oc-head">
                    <span className="oc-name">{optCur.strategy}</span>
                    {!optCur.strategy_kind && <span className="tag tag-kind kind-factor">内置</span>}
                    {optCur.status === 'approved' && <span className="tag" style={{ background: '#22c55e', color: '#fff' }}>已应用</span>}
                    {optCur.status === 'rejected' && <span className="tag" style={{ background: '#666', color: '#fff' }}>已淘汰</span>}
                    {optCur.status !== 'approved' && optCur.status !== 'rejected' && <span style={{ flex: 1 }}></span>}
                    {optCur.status === 'pending' && (
                      <>
                        {optCur.strategy_kind
                          ? <button className="btn-toggle" onClick={() => approveOpt(optCur)}>加入战法库</button>
                          : <button className="btn-toggle" onClick={() => approveOpt(optCur)} title="写入该战法的止盈线/止损线/持仓天（config 热生效），门槛同步下发对应资金池纪律">应用参数</button>}
                        <button className="btn-reject" style={{ marginLeft: 6 }} onClick={() => rejectOpt(optCur)}>淘汰</button>
                      </>
                    )}
                    <button className="btn-toggle" style={{ marginLeft: 'auto' }} onClick={toggleDrawer}>⚙ 参数池 / 池纪律</button>
                  </div>
                  <div className="oc-grid">
                    <div className="oc-item"><label>止盈线</label><b>{fmtNum((optCur.params || {}).take_profit_pct)}%</b></div>
                    <div className="oc-item"><label>止损线</label><b>{fmtNum((optCur.params || {}).stop_loss_pct)}%</b></div>
                    <div className="oc-item"><label>持仓天数</label><b>{fmtNum((optCur.params || {}).hold_days)}天</b></div>
                    <div className="oc-item"><label>门槛分数</label><b>{(optCur.params || {}).min_score ? fmtNum(optCur.params.min_score) : '—'}</b></div>
                    <div className="oc-item"><label>胜率</label><b>{fmtNum(optCur.win_rate, 1)}%</b></div>
                    <div className="oc-item"><label>盈亏比</label><b>{fmtNum(optCur.profit_factor, 2)}</b></div>
                    <div className="oc-item"><label>期望收益</label><b style={optCur.expectancy >= 0 ? { color: '#22c55e' } : { color: '#ef4444' }}>{fmtNum(optCur.expectancy, 2)}%</b></div>
                    <div className="oc-item" title="冠军参数注入该战法真实出场逻辑后的整库回放数字（与实盘同口径）">
                      <label>实盘复核</label>
                      {optCur.verify_expectancy !== undefined
                        ? <b>{fmtNum(optCur.verify_win_rate, 1)}% / {fmtNum(optCur.verify_profit_factor, 2)} / {fmtNum(optCur.verify_expectancy, 2)}%</b>
                        : <b>—</b>}
                    </div>
                    {optCur.pool_stats && (
                      <div className="oc-item" title="对应模拟盘资金池实测">
                        <label>模拟盘实测</label>
                        <b>{fmtNum(optCur.pool_stats.win_rate_pct, 1)}% / {optCur.pool_stats.expectancy >= 0 ? '+' : ''}{fmtNum(optCur.pool_stats.expectancy, 2)}% / {optCur.pool_stats.filled_buys}笔</b>
                      </div>
                    )}
                  </div>
                  <div className="oc-note">出场规则：盈利达止盈线即时卖 + 亏损达止损线即时卖 + 超持仓天兜底离场；门槛仅对有连续入场分的战法生效。</div>
                </div>

                <div className="lib-group-title" style={{ marginTop: 10 }}>止盈×止损 热力网格<span style={{ fontSize: 11, color: '#888' }}>（格值 %：该格跨持仓/门槛最优期望；点击格高亮）</span></div>
                {optCurHeat.tps.length ? (
                  <div style={{ overflowX: 'auto' }}>
                    <table className="heat-table">
                      <thead>
                        <tr><th>止盈\止损</th>{optCurHeat.sls.map((sl) => <th key={'h' + sl}>{fmtNum(sl)}%</th>)}</tr>
                      </thead>
                      <tbody>
                        {optCurHeat.tps.map((tp) => (
                          <tr key={'r' + tp}>
                            <th>{fmtNum(tp)}%</th>
                            {optCurHeat.sls.map((sl) => (
                              <td key={'c' + tp + '-' + sl} style={{ background: heatColor(heatVal(tp, sl)) }} title={'止盈' + tp + '% / 止损' + sl + '%'}>
                                {heatVal(tp, sl) !== null ? fmtNum(heatVal(tp, sl), 2) : '—'}
                              </td>
                            ))}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : <div className="empty" style={{ padding: 8 }}>本行无网格数据（旧任务产物，重新寻优后生成）。</div>}

                {optCurBatches.length > 0 && (
                  <details style={{ marginTop: 8 }}>
                    <summary style={{ cursor: 'pointer', fontSize: 12, color: '#888' }}>批次冠军明细（{optCurBatches.length} 批淘汰赛过程）</summary>
                    <table className="opt-table" style={{ marginTop: 6 }}>
                      <thead><tr><th>批</th><th>止盈%</th><th>止损%</th><th>持仓天</th><th>门槛</th><th>目标值</th></tr></thead>
                      <tbody>
                        {optCurBatches.map((b) => (
                          <tr key={'b' + b.batch}>
                            <td>#{b.batch}</td><td>{fmtNum(b.tp)}</td><td>{fmtNum(b.sl)}</td><td>{fmtNum(b.hold_days)}</td><td>{fmtNum(b.min_score)}</td><td>{fmtNum(b.objective, 3)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </details>
                )}

                {optDrawerOpen && (
                  <div className="opt-drawer">
                    <div className="lib-group-title">该战法寻优参数池（步进搜索空间，保存后下次寻优生效）</div>
                    <div className="drawer-grid4">
                      <label>止盈起<input type="number" step="1" value={cfgSweep.tp_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, tp_from: +e.target.value }))} /></label>
                      <label>止盈终<input type="number" step="1" value={cfgSweep.tp_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, tp_to: +e.target.value }))} /></label>
                      <label>步长<input type="number" step="1" min="0.5" value={cfgSweep.tp_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, tp_step: +e.target.value }))} /></label>
                    </div>
                    <div className="drawer-grid4">
                      <label>止损起<input type="number" step="1" value={cfgSweep.sl_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, sl_from: +e.target.value }))} /></label>
                      <label>止损终<input type="number" step="1" value={cfgSweep.sl_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, sl_to: +e.target.value }))} /></label>
                      <label>步长<input type="number" step="1" min="0.5" value={cfgSweep.sl_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, sl_step: +e.target.value }))} /></label>
                    </div>
                    <div className="drawer-grid4">
                      <label>持仓起(天)<input type="number" step="1" value={cfgSweep.hold_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, hold_from: +e.target.value }))} /></label>
                      <label>持仓终(天)<input type="number" step="1" value={cfgSweep.hold_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, hold_to: +e.target.value }))} /></label>
                      <label>步长(天)<input type="number" step="1" min="1" value={cfgSweep.hold_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, hold_step: +e.target.value }))} /></label>
                    </div>
                    <div className="drawer-grid4">
                      <label>门槛起<input type="number" step="5" value={cfgSweep.score_from ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, score_from: +e.target.value }))} /></label>
                      <label>门槛终<input type="number" step="5" value={cfgSweep.score_to ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, score_to: +e.target.value }))} /></label>
                      <label>步长<input type="number" step="1" min="1" value={cfgSweep.score_step ?? 0} onChange={(e) => setCfgSweep((c) => ({ ...c, score_step: +e.target.value }))} /></label>
                    </div>
                    <div style={{ fontSize: 12, margin: '6px 0' }}>
                      预估组合数 <b>{sweepComboEstimate.toLocaleString()}</b>（引擎按 ≤5000/批 分批全量模拟后批冠军 PK）
                      {sweepComboEstimate > 100000 && <span style={{ color: '#ef4444' }}>超上限 100000，请放宽步长</span>}
                    </div>
                    <button className="btn-confirm" onClick={saveSweepPool}>保存参数池</button>

                    <div className="lib-group-title" style={{ marginTop: 14 }}>对应资金池买入纪律（模拟盘实时生效）</div>
                    <div className="drawer-grid4">
                      <label>日限买<input type="number" min="0" step="1" value={cfgRule.max_daily_buys ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, max_daily_buys: +e.target.value }))} /></label>
                      <label>冷却(分)<input type="number" min="0" step="5" value={cfgRule.cooldown_minutes ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, cooldown_minutes: +e.target.value }))} /></label>
                      <label>最低分<input type="number" min="0" max="100" step="1" value={cfgRule.min_score ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, min_score: +e.target.value }))} /></label>
                      <label>日预算%<input type="number" min="0" max="100" step="5" value={cfgRule.budget_pct_per_day ?? 0} onChange={(e) => setCfgRule((c) => ({ ...c, budget_pct_per_day: +e.target.value }))} /></label>
                    </div>
                    <div style={{ fontSize: 11, color: '#888', margin: '4px 0' }}>
                      目标池：<b>{optCurPoolKey || '（未知战法不下发）'}</b>；全零=清除该池纪律。寻优审批会自动把门槛写入此处的「最低评分」。
                    </div>
                    <button className="btn-confirm" onClick={savePoolDiscipline}>保存池纪律</button>
                  </div>
                )}
              </>
            )}
          </div>

          {noDB && <div className="empty">研究库未接入（需后端开启 B5 研究闭环）</div>}
          {!noDB && candidates.length === 0 && <div className="empty">暂无候选，先在命令行跑 research optimize 产出</div>}

          {candidates.length > 0 && (
            <div className="candidate-list">
              {candidates.map((c) => (
                <div key={c.id} className="candidate-card">
                  <div className="cand-header">
                    <span className="cand-id">#{c.id}</span>
                    <span className={'tag tag-kind kind-' + c.kind}>{kindLabel(c.kind)}</span>
                    <span className={'tag status-' + c.status}>{statusLabel(c.status)}</span>
                    <span className="cand-time">{c.created_at}</span>
                  </div>

                  {c.kind === 'factor' ? (
                    <>
                      <div className="block-title">这条战法在做什么</div>
                      <div className="factors-row">
                        {factorRule(c).map((f) => (
                          <div key={f.id} className="factor-chip">
                            <span className={'dir-badge ' + (f.dir < 0 ? 'short' : 'long')}>{f.dir < 0 ? '看空' : '看多'}</span>
                            <span className="factor-name">{f.label}</span>
                            <span className="factor-id">{f.id}</span>
                            <span className="factor-weight">{f.weight.toFixed(2)}权重</span>
                          </div>
                        ))}
                      </div>
                      <div className="factor-desc">
                        玩法：每天给所有股票按上面 {factorRule(c).length} 个指标打分，分数最高的前一批会被标记为「值得买」，赌它们接下来 {c.horizon} 个交易日能涨。
                        {factorRule(c).some((f) => f.dir < 0) && (
                          <span>注意：带「看空」的指标是反着用的——这项数值越高，反而越说明不该买。</span>
                        )}
                      </div>

                      <div className="block-title">这条规律靠谱吗？（电脑验证过）</div>
                      <div className="verify-plain">
                        <div className="plain-summary">
                          <span className={'plain-badge ' + (verdict(c).ok ? 'good' : 'bad')}>{verdict(c).ok ? '✅ 可以试试' : '⚠️ 建议别用'}</span>
                          <span className="plain-text">{verdict(c).text}</span>
                        </div>
                        {plainLines(c).map((l, i) => (
                          <div key={i} className="plain-line">
                            <span className="plain-num">{i + 1}.</span>
                            <span className="plain-body">{l}</span>
                          </div>
                        ))}
                      </div>

                      <details className="detail-block">
                        <summary>想看具体数字？展开</summary>
                        <div className="detail-row"><span className="d-label">样本内测试</span><span className="d-value">前一段历史回放：IR {fmt(parseReason(c, '样本内IR'))}</span></div>
                        <div className="detail-row"><span className="d-label">样本外测试</span><span className="d-value">另一段没用过的历史回放：IR {fmt(parseReason(c, '样本外IR'))}</span></div>
                        <div className="detail-row"><span className="d-label">反推超额</span><span className="d-value">高分股比全市场平均多赚 {fmtPct(parseReason(c, '反推超额'))}</span></div>
                        <div className="detail-row"><span className="d-label">全样本 IR</span><span className="d-value">{fmt(c.ir)}（参考）</span></div>
                        <div className="detail-row"><span className="d-label">全样本 IC</span><span className="d-value">{fmt(c.ic_mean)}（参考）</span></div>
                        <div className="detail-row"><span className="d-label">全链路回测</span><span className="d-value">{btTested(c) ? (c.backtest_result_text || fmt(c.avg_excess)) : '未测'}</span></div>
                      </details>
                    </>
                  ) : (
                    <>
                      <div className="metric-row">
                        <div className="metric"><span className="metric-label">IR</span><span className={'metric-value ' + signClass(c.ir)}>{fmt(c.ir)}</span></div>
                        <div className="metric"><span className="metric-label">IC</span><span className={'metric-value ' + signClass(c.ic_mean)}>{fmt(c.ic_mean)}</span></div>
                        <div className="metric"><span className="metric-label">回测超额</span><span className={'metric-value ' + signClass(c.avg_excess)}>{fmt(c.avg_excess)}</span></div>
                        <div className="metric"><span className="metric-label">前瞻天数</span><span className="metric-value">{c.horizon}</span></div>
                      </div>
                      {weightList(c).length > 0 && (
                        <div className="weights-row">
                          {weightList(c).map((w) => (
                            <span key={w[0]} className="weight-chip">
                              <span className="weight-fid">{w[0]}</span>
                              <span className="weight-val">{w[1].toFixed(3)}</span>
                            </span>
                          ))}
                        </div>
                      )}
                    </>
                  )}

                  {c.kind === 'depth' && (
                    <div className="depth-block">
                      {Object.entries(depthSummary(c)).map(([code, s]) => (
                        <div key={code} className="depth-stock">
                          <span className="depth-code">{code}</span>
                          <span className="depth-touch">买1 {s.bid1} / 卖1 {s.ask1}</span>
                          {(s.orders || []).map((o) => (
                            <span key={o.level + o.kind} className={'order-chip ' + (o.kind === 'support' ? 'support' : 'resistance')}>
                              {o.kind === 'support' ? '托' : '压'}单 档{o.level} {o.price} / {o.volume}手 ({(o.share_pct * 100).toFixed(0)}%)
                            </span>
                          ))}
                        </div>
                      ))}
                    </div>
                  )}

                  {canApprove && c.status === 'proposed' && (
                    <div className="cand-actions">
                      <button className="btn-approve" onClick={() => doApprove(c)}>审批并应用</button>
                      <button className="btn-reject" onClick={() => doReject(c)}>驳回</button>
                      {c.kind === 'factor' && (
                        <button className="btn-backtest" disabled={backtestLoading[c.id]} onClick={() => doBacktest(c)}>
                          {backtestLoading[c.id] ? '回测中...' : (c.avg_excess ? '重新回测' : '全量回测')}
                        </button>
                      )}
                      {backtestLoading[c.id] && (
                        <div className="bt-progress">
                          <div className="bt-progress-bar"><div className="bt-progress-fill" style={{ width: btPct(c.id) }}></div></div>
                          <span className="bt-progress-label">全链路回测 {backtestProgress[c.id] || '0%'}</span>
                        </div>
                      )}
                      {backtestResult[c.id] && <span className={'bt-result ' + signClass(backtestResult[c.id])}>回测超额 {fmt(backtestResult[c.id])}</span>}
                    </div>
                  )}
                  {!canApprove && c.status === 'proposed' && <div className="no-perm">无审批权限（需管理员授予 research_approve）</div>}
                </div>
              ))}
            </div>
          )}
        </>
      )}

      {activeTab === 'library' && (
        <div className="library-panel">
          <div className="library-header">
            <div className="library-title">战法库（已应用因子战法）</div>
            <div style={{ display: 'flex', gap: 8 }}>
              <button className="btn-backtest" onClick={() => { setActiveTab('candidates'); setCandSubTab('optimize'); loadOptimizations() }}>⚙ 参数寻优</button>
              <button className="btn-refresh" onClick={loadLibrary} disabled={loadingLibrary}>{loadingLibrary ? '加载中...' : '刷新'}</button>
            </div>
          </div>

          <div className="builtin-cards">
            {builtinPatterns.map((b) => (
              <div key={'card-' + b.id} className={'bcard' + (expandedStrategy === 'bt:' + b.id ? ' active' : '')} onClick={() => selectStrategy('bt:' + b.id)}>
                <div className="bcard-name">{b.name}</div>
                <div className="bcard-sub">{summarizeJob(latestLibJob(b.id, -1))}</div>
              </div>
            ))}
          </div>

          <div className="lib-group-title">内置形态战法（{builtinPatterns.length}）</div>
          <div className="library-list">
            {builtinPatterns.map((b) => (
              <div key={'bt-' + b.id} ref={setStrategyRef('bt:' + b.id)} className="library-card lib-builtin">
                <div className="lib-head">
                  <span className="lib-name">{b.name}</span>
                  <span className="tag tag-kind kind-factor">内置</span>
                  <span className="tag status-applied">实盘常驻</span>
                  <span className="lib-time">最新: {summarizeJob(latestLibJob(b.id, -1))}</span>
                </div>
                <div className="lib-actions">
                  <button className="btn-backtest" onClick={() => doLibraryBacktest(b)}>回测此战法</button>
                  <button className="btn-toggle" onClick={() => selectStrategy('bt:' + b.id)}>
                    {expandedStrategy === 'bt:' + b.id ? '收起详情' : '详情'}
                  </button>
                </div>
                {expandedStrategy === 'bt:' + b.id && (
                  <div className="lib-detail">
                    {!latestLibJob(b.id, -1) && <div className="lib-detail-text dim">尚未回测——点「回测此战法」发起历史日K回放（结果进「回测」tab）</div>}
                    {latestLibJob(b.id, -1) && (
                      <>
                        {['running', 'queued', 'interrupted'].includes(latestLibJob(b.id, -1).status) ? (
                          <div className="bt-progress">
                            <div className="bt-progress-bar"><div className="bt-progress-fill" style={{ width: jobPct(latestLibJob(b.id, -1)) }}></div></div>
                            <span className="bt-progress-label">{jobPct(latestLibJob(b.id, -1))}</span>
                          </div>
                        ) : latestLibJob(b.id, -1).result_text ? (
                          <pre className="lib-detail-text">{latestLibJob(b.id, -1).result_text}</pre>
                        ) : latestLibJob(b.id, -1).status === 'error' ? (
                          <div className="lib-detail-text dim">上次回测失败：{latestLibJob(b.id, -1).error}</div>
                        ) : (
                          <div className="lib-detail-text dim">暂无已完成回测报告</div>
                        )}
                      </>
                    )}
                  </div>
                )}
              </div>
            ))}
          </div>

          {ruleGroups.length === 0 && <div className="empty">暂无已应用因子/形态战法。审批通过的候选会自动加入战法库并注入实盘。</div>}
          {ruleGroups.map((g) => (
            <div key={'g-' + g.key}>
              <div className="lib-group-title">{g.title}（{g.items.length}）</div>
              <div className="library-list">
                {g.items.map((s) => (
                  <div key={g.key + ':' + s.id} className="library-card">
                    <div className="lib-head">
                      {editingName[s.id] ? (
                        <input
                          value={nameDraft[s.id] || ''}
                          onChange={(e) => setNameDraft((d) => ({ ...d, [s.id]: e.target.value }))}
                          className="name-input"
                          onKeyDown={(e) => { if (e.key === 'Enter') saveName(s) }}
                          onBlur={() => saveName(s)}
                        />
                      ) : (
                        <span className="lib-name">{s.name}</span>
                      )}
                      {canApprove && !editingName[s.id] && <button className="btn-rename" onClick={() => startRename(s)}>改名</button>}
                      <span className={'tag tag-kind ' + (s.kind === 'pattern' ? 'kind-pattern' : 'kind-factor')}>{s.kind === 'pattern' ? '形态' : '因子'}</span>
                      <span className={'tag ' + (s.enabled ? 'status-applied' : 'status-rejected')}>{s.enabled ? '已启用' : '已停用'}</span>
                      <span className="lib-id">{s.id}</span>
                      <span className="lib-time">{s.applied_at}｜最新: {summarizeJob(latestLibJob(s.kind, ruleNum(s.id)))}</span>
                    </div>
                    <div className="lib-factors">
                      {s.kind === 'pattern' ? (
                        (s.conds || []).map((cc, i) => <span key={i} className="cond-chip">{condLabel(cc)}</span>)
                      ) : (
                        ruleFactors(s).map((f) => (
                          <span key={f.id} className="factor-chip">
                            <span className={'dir-badge ' + (f.dir < 0 ? 'short' : 'long')}>{f.dir < 0 ? '看空' : '看多'}</span>
                            <span className="factor-name">{f.label}</span>
                            <span className="factor-id">{f.id}</span>
                          </span>
                        ))
                      )}
                    </div>
                    <div className="lib-stats">
                      <span className="stat">信号 <b>{s.signal_count}</b></span>
                      <span className="stat">胜 <b className="pos">{s.win}</b></span>
                      <span className="stat">负 <b className="neg">{s.loss}</b></span>
                      <span className="stat">累计前向收益 <b className={s.cum_return >= 0 ? 'pos' : 'neg'}>{fmtPctGlobal(s.cum_return)}</b></span>
                      <span className="stat">回测超额 <b className={signClass(s.excess)}>{fmt(s.excess)}</b></span>
                    </div>
                    {s.kind === 'factor' && (
                      <div className="lib-verify">
                        <div className="lib-verify-title">这条规律靠谱吗？（电脑验证过）</div>
                        <div className="lib-verify-help">
                          <div><b>样本内 IR</b>：训练期里"每天按这个规律选股的排名"与"未来收益排名"的稳定相关强度。数值越稳定越好，&gt;0.5 算强。</div>
                          <div><b>样本外 IR</b>：把数据藏起一段（模型没见过的验证期）再做同样检验——这是最关键的数字：&gt;0.3 及格；若明显低于样本内，说明规律可能只是"背答案"（过拟合）。</div>
                          <div><b>反推超额</b>：故意反着做这条规律的历史收益超额。反向也赚钱=规律方向可疑；反向大亏=规律方向可信。</div>
                        </div>
                        <div className="lib-verify-row">
                          <span className="v-label">前瞻</span><span className="v-value">{s.horizon} 个交易日</span>
                          <span className="v-label">样本内 IR</span><span className="v-value">{fmt(parseReason(s, '样本内IR'))}</span>
                          <span className="v-label">样本外 IR</span><span className="v-value">{fmt(parseReason(s, '样本外IR'))}</span>
                          <span className="v-label">反推超额</span><span className="v-value">{fmtPctGlobal(parseReason(s, '反推超额'))}</span>
                          <span className="v-label">全样本 IR</span><span className="v-value">{fmt(s.ir)}</span>
                          <span className="v-label">全样本 IC</span><span className="v-value">{fmt(s.ic_mean)}</span>
                          <span className="v-label">全链路回测</span>
                          <span className={'v-value ' + (s.backtest_done ? (s.avg_excess >= 0 ? 'pos' : 'neg') : 'dim')}>{s.backtest_done ? fmt(s.avg_excess) : '未测'}</span>
                        </div>
                      </div>
                    )}
                    {canApprove && (
                      <div className="lib-actions">
                        <button className="btn-toggle" onClick={() => toggleLibrary(s)}>{s.enabled ? '停用' : '启用'}</button>
                        <button className="btn-backtest" onClick={() => doLibraryBacktest(s)}>回测此战法</button>
                        <button className="btn-toggle" onClick={() => selectStrategy('rule:' + s.id)}>
                          {expandedStrategy === 'rule:' + s.id ? '收起详情' : '详情'}
                        </button>
                        <button className="btn-reject" onClick={() => removeLibrary(s)}>删除</button>
                      </div>
                    )}
                    {expandedStrategy === 'rule:' + s.id && (
                      <div className="lib-detail">
                        {latestLibJob(s.kind, ruleNum(s.id)) && latestLibJob(s.kind, ruleNum(s.id)).result_text ? (
                          <pre className="lib-detail-text">{latestLibJob(s.kind, ruleNum(s.id)).result_text}</pre>
                        ) : (
                          <div className="lib-detail-text dim">暂无回测报告——点「回测此战法」发起</div>
                        )}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      {activeTab === 'settings' && (
        <div className="settings-panel">
          <div className="settings-title">研究调度设置</div>
          <div className="setting-row">
            <div className="setting-info">
              <div className="setting-label">全量回测全局开关</div>
              <div className="setting-desc">开启后，夜间自动研究在发现因子候选后会追加一次 B4 全链路回测（回填回测超额）；关闭则只做发现、不做回测，省时省 CPU。</div>
            </div>
            <label className="switch">
              <input type="checkbox" checked={backtestEnabled} onChange={(e) => { setBacktestEnabled(e.target.checked); saveBacktestToggle() }} />
              <span className="slider"></span>
            </label>
            <span className="setting-state">{backtestEnabled ? '已开启' : '已关闭'}</span>
          </div>
          <div className="setting-hint">配置写入 rules.scheduler.nightly.backtest_enabled，quant-research 服务 30s 内热生效。</div>
        </div>
      )}

      {activeTab === 'backtests' && (
        <div className="bt-center">
          <div className="bt-add">
            <div className="bt-add-title">发起 / 重跑全量回测</div>
            <div className="bt-add-row bt-params-row">
              <label className="bt-param">开始 <input value={btStart} onChange={(e) => setBtStart(e.target.value)} className="bt-input" placeholder="20230801" /></label>
              <label className="bt-param">结束 <input value={btEnd} onChange={(e) => setBtEnd(e.target.value)} className="bt-input" placeholder="留空=今天" /></label>
              <label className="bt-param">选股数 <input value={btTopK} onChange={(e) => setBtTopK(e.target.value)} className="bt-input bt-input-sm" placeholder="5" /></label>
              <label className="bt-param">最小样本 <input value={btMinStocks} onChange={(e) => setBtMinStocks(e.target.value)} className="bt-input bt-input-sm" placeholder="10" /></label>
            </div>
            <div className="bt-add-row">
              <select value={btPickId} className="bt-select" disabled={btLoading} onChange={(e) => setBtPickId(+e.target.value)}>
                <option value={0} disabled>选择待审批因子候选</option>
                {btCandidates.map((c) => (
                  <option key={c.id} value={c.id}>#{c.id} {c.kind === 'pattern' ? '形态战法' : '因子战法'}（{c.kind === 'pattern' ? ('触发 ' + (c.triggers ?? '-')) : ('IC ' + fmt(c.ic_mean) + '，IR ' + fmt(c.ir))}）</option>
                ))}
              </select>
              <button className="btn-backtest" disabled={btPickId === 0 || backtestLoading[btPickId]} onClick={() => doBacktestById(btPickId)}>
                {btPickId !== 0 && backtestLoading[btPickId] ? '回测中...' : '发起全量回测'}
              </button>
              <button className="btn-refresh" onClick={loadBacktests} disabled={btLoading}>{btLoading ? '加载中...' : '刷新列表'}</button>
            </div>
            <div className="bt-add-row" style={{ marginTop: 8 }}>
              <span style={{ fontSize: 12, color: '#888' }}>内置形态战法：</span>
              {builtinPatterns.map((b) => (
                <button key={'bt-' + b.id} className="btn-backtest" style={{ marginRight: 8 }} onClick={() => doLibraryBacktest(b)}>回测·{b.name}</button>
              ))}
            </div>
            <div className="bt-add-hint">
              任务统一走研究队列：手动回测为高优先级，夜间自动研究为低优先级；高优先级到来会自动让路（被抢占任务断点续跑）。所有任务仅在盘后窗口执行——盘中提交会显示"排队中·盘后执行"。断点持久化，中断/重启后重跑只计算剩余事件；页面刷新后排队/运行中任务自动恢复轮询，可暂停/取消。
            </div>
          </div>

          <div className="bt-metrics-help">
            <button className="btn-toggle" onClick={() => setShowMetricHelp(!showMetricHelp)}>
              {showMetricHelp ? '收起指标说明' : '📖 这些指标是什么意思？（点开看解释）'}
            </button>
            {showMetricHelp && (
              <div className="lib-verify-help" style={{ marginTop: 6 }}>
                <div><b>触发信号数</b>：历史区间里该战法一共发出过多少次买入机会。太少(&lt;50)说明统计意义弱。</div>
                <div><b>胜率</b>：赚钱笔数占比。短线战法 40%~50% 就不错——靠盈亏比赚钱，不必追求高胜率。</div>
                <div><b>盈亏比</b>：平均每笔赚的 ÷ 平均每笔亏的。<b>&gt;1 才有意义</b>，&gt;1.2 算优秀；配合胜率的期望=胜率×均盈−(1−胜率)×均亏。</div>
                <div><b>平均持仓天数</b>：资金占用效率。超短 1~3 天、波段 5~15 天；过长说明退出不果断。</div>
                <div><b>回测超额</b>：相对基准(指数)的超额收益（B4 全链路口径），&gt;0 跑赢大盘。</div>
                <div style={{ color: '#0f172a' }}><b>怎么用</b>：单条结果只是"体检报告"——切到「待审批候选 → 优化结果」跑全库参数寻优，系统自动试出最优止盈/持仓/门槛组合并可一键应用。</div>
              </div>
            )}
          </div>

          {backtestJobs.length === 0 && <div className="empty">暂无回测任务。选择上方候选发起全量回测，或等待夜间调度器产出。</div>}
          {backtestJobs.length > 0 && (
            <div className="bt-list">
              {backtestJobs.map((j) => (
                <div key={(j.kind || 'candidate') + ':' + j.candidate_id} className={'bt-card bt-' + j.status}>
                  {jobParams(j) && <div className="bt-params" style={{ fontSize: 11, color: '#64748b', marginTop: 2 }} title="开始/结束日期；选股数=每个事件买几只；最小样本=当日最少多少只股票有数据才构成事件">{jobParams(j)}{j.strategy_kind && <span style={{ marginLeft: 6, color: '#94a3b8' }}>({j.strategy_kind})</span>}</div>}
                  <div className="bt-head">
                    <span className={'tag tag-kind ' + (j.kind === 'nightly' ? '' : (j.kind === 'library' ? 'kind-pattern' : 'kind-factor'))}>
                      {j.kind === 'nightly' ? '夜间全量' : (j.kind === 'library' ? '战法库' : '单候选')}
                    </span>
                    {j.kind === 'candidate' && <span className="bt-cand">候选 #{j.candidate_id}</span>}
                    {j.kind === 'library' && <span className="bt-cand">{libraryJobLabel(j)}</span>}
                    <span className={'tag bt-status status-' + (j.status === 'done' ? 'applied' : (j.status === 'error' ? 'rejected' : 'proposed'))}>{btStatusLabel(j.status)}</span>
                    <span className="bt-time">{j.started_at || ''}{j.finished_at && ' → ' + j.finished_at}</span>
                  </div>
                  {(j.status === 'running' || j.status === 'paused' || j.status === 'queued') && (
                    <div className="bt-progress">
                      <div className="bt-progress-bar"><div className="bt-progress-fill" style={{ width: jobPct(j) }}></div></div>
                      <span className="bt-progress-label">{jobPct(j)}</span>
                    </div>
                  )}
                  {j.status === 'queued' && <div className="bt-error" style={{ color: '#888' }}>{queueHint(j)}</div>}
                  {j.status === 'done' && j.kind === 'library' && <div className="bt-result bt-lib-result" style={{ whiteSpace: 'pre-wrap' }}>{j.result_text}</div>}
                  {j.status === 'done' && j.kind === 'library' && metricAdvices(j).length > 0 && (
                    <div style={{ marginTop: 6 }}>
                      <button className="btn-toggle" style={{ fontSize: 11 }} onClick={() => toggleAdvice(j.id)}>
                        {adviceOpen === j.id ? '收起改进建议' : '💡 改进建议（怎么调这条战法）'}
                      </button>
                      {adviceOpen === j.id && (
                        <div className="lib-verify-help" style={{ marginTop: 6 }}>
                          {metricAdvices(j).map((a, ai) => <div key={ai} style={{ marginBottom: 4 }}>{a}</div>)}
                          <div style={{ color: '#0f172a', marginTop: 4 }}>
                            ▶ 下一步：切到「待审批候选 → 优化结果」发起全库参数寻优，系统网格搜索最优止盈%/持仓天/入场门槛并排名，选中后「加入战法库」即热生效。
                          </div>
                        </div>
                      )}
                    </div>
                  )}
                  {j.status === 'done' && j.kind !== 'library' && (
                    <div className="bt-result">
                      回测超额 <b className={signClass(j.avg_excess)}>{fmt(j.avg_excess)}</b>
                      {j.result_text && j.result_text.includes('期望') && (
                        <em style={{ marginLeft: 8, fontSize: 11, color: '#64748b' }}>
                          {j.result_text.match(/【[^】]*】.*?期望 ([+\-][\d.]+%)/)?.[1] || ''} 期望
                        </em>
                      )}
                    </div>
                  )}
                  {j.status === 'error' && <div className="bt-error">{j.error}</div>}
                  {j.status === 'interrupted' && <div className="bt-error">{j.error || '任务中断，可重新发起续跑（断点缓存仍有效，重跑只计算剩余事件）'}</div>}
                  {canApprove && (j.kind === 'candidate' || j.kind === 'library') && (
                    <div className="bt-actions">
                      {j.status === 'running' && <button className="btn-backtest bt-ctl" onClick={() => doPauseBacktest(ctrlId(j))}>暂停</button>}
                      {j.status === 'paused' && <button className="btn-backtest bt-ctl" onClick={() => doResumeBacktest(ctrlId(j))}>继续</button>}
                      {(j.status === 'running' || j.status === 'paused' || j.status === 'queued') && <button className="btn-backtest bt-ctl bt-danger" onClick={() => doCancelBacktest(ctrlId(j))}>取消</button>}
                      {j.kind === 'candidate' && j.status !== 'queued' && (
                        <button className="btn-backtest" disabled={backtestLoading[j.candidate_id]} onClick={() => doBacktestById(j.candidate_id)}>
                          {j.status === 'interrupted' ? '续跑（断点续传）' : (backtestLoading[j.candidate_id] ? '回测中...' : '重新回测')}
                        </button>
                      )}
                      {j.kind === 'library' && j.strategy_kind && j.status !== 'queued' && (
                        <button className="btn-backtest" onClick={() => rerunLibrary(j)}>
                          {j.status === 'interrupted' ? '重跑（断点续传）' : '重新回测'}
                        </button>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
