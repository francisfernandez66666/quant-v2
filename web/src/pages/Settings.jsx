// ── 设置页面 Settings.jsx ──
// 服务器连接、通知、账户信息、LLM 配置、五大战法参数、资讯显示开关、系统信息
// 使用 TDesign React 组件（Card / Input / InputNumber / Switch / Button / Tag / Textarea）。
import React, { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Card, Input, InputNumber, Button, Tag, Textarea, Dialog } from 'tdesign-react'
import ToggleSw from '../components/ToggleSw'
import * as api from '../api/index.js'
import { requestPermission, notify as sendNotify } from '../notify.js'
import { showToast } from '../ui.jsx'

// 五大战法参数分组定义：每个 group 含标题与字段列表（k=后端字段名, label=展示名, step=步进, type=控件类型, hint=悬浮说明）
const strategyGroups = [
  {
    // ── 龙头战法：打分因子权重 + 卖出风控/止盈参数 ──
    key: 'dragon', title: '龙头战法（权重合计≤1）',
    fields: [
      // 打分四因子权重（合计≤1，决定信号排序）
      { k: 'f1_seal_weight', label: 'F1 首封权重', step: 0.05 },
      { k: 'f2_resonance_weight', label: 'F2 共振权重', step: 0.05 },
      { k: 'f3_premium_weight', label: 'F3 溢价权重', step: 0.05 },
      { k: 'f4_rs_weight', label: 'F4 强度权重', step: 0.05 },
      // 卖出风控与止盈参数（回撤/炸板分级减仓、走弱判定、止盈）
      { k: 'pullback_max_pct', label: '最大回撤%', step: 0.01 },
      { k: 'breaker_sell_half_pct', label: '炸板减半%', step: 0.01 },
      { k: 'breaker_sell_all_pct', label: '炸板清仓%', step: 0.01 },
      { k: 'buy_pullback_sell_half_pct', label: '买入回撤减半%', step: 0.01 },
      { k: 'buy_pullback_sell_all_pct', label: '买入回撤清仓%', step: 0.01 },
      { k: 'buy_day_close_below', label: '买入日收盘低于%', step: 0.01 },
      { k: 'next_open_if_below', label: '次日开盘低于%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 1 },
    ],
  },
  {
    // ── 双响炮战法：两段放量突破的量比门槛与评分权重 ──
    key: 'double_bump', title: '双响炮战法',
    fields: [
      { k: 'first_break_volume_multiple', label: '一突量比', step: 0.1 },
      { k: 'second_break_volume_multiple', label: '二突量比', step: 0.1 },
      { k: 'adjust_vol_ratio_max', label: '调整量比上限', step: 0.5 },
      { k: 'position_weight', label: '调整深度权重', step: 0.05 },
      { k: 'ma_weight', label: '均线权重', step: 0.05 },
      { k: 'volume_weight', label: '量能权重', step: 0.05 },
      { k: 'double_bump_take_profit_pct', label: '止盈%', step: 0.01 },
    ],
  },
  {
    // ── N 形战法：形态门槛与固定止损 ──
    key: 'n_shape', title: 'N 形战法',
    fields: [
      { k: 'n_pattern_score_threshold', label: 'N 形态分阈值', step: 1 },
      { k: 'hard_stop_loss', label: '硬止损%', step: 0.01 },
    ],
  },
  {
    // ── 龙头回头战法：回调低吸的止盈止损与分批目标 ──
    key: 'dragon_return', title: '龙回头战法',
    fields: [
      { k: 'stop_loss_pct', label: '止损%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 0.01 },
      { k: 'max_hold_days', label: '最长持仓天数', step: 1 },
      { k: 'target1_multiplier', label: '目标1倍数', step: 0.05 },
      { k: 'target2_multiplier', label: '目标2倍数', step: 0.05 },
      { k: 'trailing_drawback', label: '移动止损回撤%', step: 0.01 },
    ],
  },
  {
    // ── 动量分模型：三因子权重 + 动量闸门（信号过滤开关）──
    // 注：动量"能不能交易"不在此页——与其他战法同权恒产信号，交易准入由「量化交易」页
    // 战法开关（实盘白名单）与模拟盘战法开关统一裁决（§SIGNAL_CONTROLLER）。
    key: 'momentum', title: '动量分权重（合计建议=100）',
    fields: [
      { k: 'volume_price_weight', label: '量价权重', step: 5 },
      { k: 'macd_weight', label: 'MACD权重', step: 5 },
      { k: 'trend_weight', label: '走势权重', step: 5 },
      { k: 'momentum_gate_enabled', label: '动量提升才提醒', type: 'switch', hint: '开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响' },
      { k: 'momentum_delta_tol', label: '回落容忍差(分)', step: 1, hint: '动量分相对上一轮回落 ≤ 该值仍视为提升；设为0表示需严格不回落' },
    ],
  },
]

// 生成空的战法参数字典（五大战法占位空对象），用于初始化/合并后端配置
const emptyStrategy = () => ({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} })

const rowStyle = { display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' } // 设置项行布局
const labelStyle = { width: 160, flexShrink: 0, color: 'var(--app-muted-2)', fontSize: 13 } // 设置项标签样式

/**
 * 设置页面组件
 * 管理服务器地址、通知、LLM 配置、五大战法参数与资讯显示开关。
 * @returns {JSX.Element}
 */
export default function Settings() {
  // §A5（20260918 审计批）：admin 数据首拉 403（本地角色缓存伪冒/会话中途被降权）统一跳 /403
  const navigate = useNavigate()
  const [serverUrl, setServerUrl] = useState(api.getStoredServer() || '')
  const [serverOnline, setServerOnline] = useState(false)

  const [account] = useState(api.getAccount())
  // §NATIVEAUTH（2026-09-22 C批）：改走 api.getToken() 统一读取点，不再直读 localStorage 'liangzai_token'
  // §NATIVEAUTH (2026-09-22 batch C): read through api.getToken() instead of touching localStorage directly —
  // 原生桥路径下 token 已迁 EncryptedSharedPreferences，直读会显示为空；getToken() 内含原生优先+迁移逻辑。
  // (with the native bridge the token lives in EncryptedSharedPreferences and a direct read would show empty;
  //  getToken() handles native-first lookup plus legacy migration). 该行仍是展示当前 token 的诊断行，逻辑不变。
  //  This line remains a diagnostic display of the current token; behavior unchanged.
  const [token] = useState(api.getToken() || '')

  const [llmApiUrl, setLlmApiUrl] = useState('')
  const [llmApiKeys, setLlmApiKeys] = useState('')
  const [llmModel, setLlmModel] = useState('')
  const [llmClassifierModel, setLlmClassifierModel] = useState('')
  const [llmBatchConcurrency, setLlmBatchConcurrency] = useState(4)
  const [llmD1MaxTokens, setLlmD1MaxTokens] = useState(2048)
  const [llmConfigured, setLlmConfigured] = useState(false)
  const [llmSaving, setLlmSaving] = useState(false)
  // llmProbing / llmRolling：测试连接与回滚各自的进行态（与保存互不阻塞）
  const [llmProbing, setLlmProbing] = useState(false)
  const [llmRolling, setLlmRolling] = useState(false)
  // llmForce 强制应用：跳过"探测未通过则拒绝"的保护。默认关——盘中误存一把坏 key 会把
  // 整条 LLM 链路（新闻归因/D1/咨询）连同落库配置一起打坏，且重启也救不回来。
  const [llmForce, setLlmForce] = useState(false)
  // llmNote 结果回报 { kind, head, text, probes }：保存/探测/回滚到底生效了没有、哪把 key 为什么没生效。
  const [llmNote, setLlmNote] = useState(null)

  const [strategyCfg, setStrategyCfg] = useState(emptyStrategy())
  const [strategySaving, setStrategySaving] = useState(false)
  // §N-4（2026-09-22 傍晚批 §CFGSMASH）战法参数加载三态：loading / loaded / error。
  // 旧实现 `catch (_) {}` 把加载失败静默吞掉 → 表单落在空对象上 → 数字缺失被 `?? 0` 渲染成 0
  // → 一次整份保存把五套战法阈值清零落库。现在 error 态**禁用保存 + 红条警示**，
  // 数字缺失渲染为空（不把「缺失」和「0」混同）+ 保存前必填校验。
  // English: §N-4 — tri-state loader for the tactics form; load-failed disables saving
  // (red banner), missing numbers render empty instead of 0, and save validates all fields.
  const [strategyLoadState, setStrategyLoadState] = useState('loading')
  const [strategyLoadError, setStrategyLoadError] = useState('')
  // §中-6 乐观锁基线：GET /api/config/strategy 回传的 updated_at，保存时原样带上；
  // 服务端版本已前进 → 409（banner 变「版本冲突」，必须重载后才能再存，禁止盲覆盖）。
  const [strategyVersion, setStrategyVersion] = useState('')
  const [strategyConflict, setStrategyConflict] = useState(false)
  // §F33 修复：保存前 dirty tracking 基线（LLM/战法配置分别），load 完成时同步，
  // 保存成功后重置。dirty=true 时按钮右上角亮小圆点 + 页面关闭前 beforeunload 拦截。
  // English: F33 — baseline snapshots for LLM/strategy configs; dirty indicator + unload guard
  // replace the previous silent "unsaved edits lost on route change" behavior.
  const [llmBaseline, setLlmBaseline] = useState(null)
  const [strategyBaseline, setStrategyBaseline] = useState(null)

  const [newsShowAll, setNewsShowAll] = useState(false)

  // §D-3（GAP_VERIFY_20260917_PM）配置历史/回滚卡：接上早已就绪但零消费的两组端点
  // （GET /api/config/history[?diff=TS] + POST /api/config/rollback 规则快照；
  //   GET /api/research/strategies/snapshots + POST /api/research/strategies/rollback 战法参数）。
  // admin-only：写侧端点是 adminMiddleware，成员看到卡也无意义（列表虽 auth 可读，统一隐藏避免误点）。
  const [histRuleSnaps, setHistRuleSnaps] = useState([])
  const [histStratSnaps, setHistStratSnaps] = useState([])
  const [histDiff, setHistDiff] = useState(null) // {snapshot_ts, diff} | null
  const [histRollback, setHistRollback] = useState(null) // {kind:'rules'|'strategy', ts} | null（确认弹窗目标）
  const [histLoading, setHistLoading] = useState(false)
  const histAdmin = api.getRole() === 'admin'

  // 拉取两类快照列表（进卡/回滚后刷新）
  async function loadHist() {
    if (!histAdmin) return
    setHistLoading(true)
    try {
      const [r, s] = await Promise.all([api.fetchConfigHistory(), api.fetchStrategySnapshots().catch(() => ({ snapshots: [] }))])
      setHistRuleSnaps(Array.isArray(r.snapshots) ? r.snapshots : [])
      setHistStratSnaps(Array.isArray(s.snapshots) ? s.snapshots : [])
    } catch (e) {
      showToast('配置历史拉取失败: ' + (e.message || ''), 'error')
    } finally {
      setHistLoading(false)
    }
  }
  useEffect(() => { loadHist() }, [histAdmin]) // eslint-disable-line react-hooks/exhaustive-deps

  // 点开某条规则快照的 diff（与当前生效配置的逐行差异文本）
  async function openHistDiff(ts) {
    if (histDiff && histDiff.snapshot_ts === ts) { setHistDiff(null); return }
    try { setHistDiff(await api.fetchConfigHistory(ts)) } catch (e) { showToast('diff 拉取失败: ' + (e.message || ''), 'error') }
  }

  // 确认弹窗→执行回滚（rules=config.json 原子恢复；strategy=参数快照恢复），成功后刷新列表
  async function confirmHistRollback() {
    if (!histRollback) return
    try {
      if (histRollback.kind === 'rules') await api.rollbackConfig(histRollback.ts)
      else await api.rollbackStrategyParams(histRollback.ts)
      showToast(`已回滚到快照 ${histRollback.ts}`, 'success')
      setHistRollback(null)
      await loadHist()
    } catch (e) {
      showToast('回滚失败: ' + (e.message || ''), 'error')
    }
  }

  // 加载战法参数（初始化与「重载」按钮共用同一份口径，避免两处各写一遍而分叉）。
  // §N-4：失败不再 `catch(_) {}` 静默——置 error 态禁用保存，并把原因显示在红条里。
  async function loadStrategyCfg() {
    setStrategyLoadState('loading')
    setStrategyLoadError('')
    setStrategyConflict(false)
    try {
      const sc = await api.fetchStrategyConfig()
      if (!sc || typeof sc !== 'object') throw new Error('返回载荷为空/非对象')
      const next = emptyStrategy()
      for (const group of strategyGroups) {
        const src = sc[group.key]
        if (src) Object.assign(next[group.key], src)
      }
      setStrategyCfg(next)
      setStrategyBaseline(JSON.parse(JSON.stringify(next))) // §F33 深拷贝基线
      setStrategyVersion(typeof sc.updated_at === 'string' ? sc.updated_at : '') // §中-6 版本基线
      setStrategyLoadState('loaded')
    } catch (e) {
      if (api.isForbidden(e)) { navigate('/403'); return } // §A5 同口径：权威角色非管理员跳 403
      setStrategyLoadError(String((e && e.message) || e))
      setStrategyLoadState('error')
    }
  }

  // 保存战法参数配置
  // §N-4 三重闸：① 非 loaded 态禁存（按钮已禁用，这里再兜一道，防回车/脚本触发）；
  // ② 必填校验——数字字段缺失（undefined/null/NaN）不得折叠成 0 提交，列出缺失项拒绝保存；
  // ③ 请求体带 §中-6 的 updated_at 基线，409=他人已先落一步 → 红条+禁止覆盖，只给「重载」。
  async function saveStrategy() {
    if (strategyLoadState !== 'loaded') return
    const missing = []
    for (const group of strategyGroups) {
      for (const f of group.fields) {
        if (f.type === 'switch') continue // 布尔开关无「缺失=0」歧义，缺省即 false 明示
        const v = strategyCfg[group.key][f.k]
        if (v === undefined || v === null || v === '' || !Number.isFinite(Number(v))) {
          missing.push(`${group.title}·${f.label}`)
        }
      }
    }
    if (missing.length) {
      showToast(`以下战法参数缺失，禁止保存（缺失≠0，请补齐或点「重载」）：${missing.slice(0, 5).join('、')}${missing.length > 5 ? ` 等 ${missing.length} 项` : ''}`, 'error')
      return
    }
    setStrategySaving(true)
    try {
      // 后端（§N-4 起）为稀疏 merge：body 未出现的键保留库中原值；updated_at 仅作版本比对，
      // 服务端统一盖新戳并回传——保存成功后把基线推进，防自我冲突（自己刚写的又被判为他人）。
      const res = await api.setStrategyConfig({ ...JSON.parse(JSON.stringify(strategyCfg)), updated_at: strategyVersion || undefined })
      if (res && typeof res.updated_at === 'string') setStrategyVersion(res.updated_at)
      setStrategyBaseline(JSON.parse(JSON.stringify(strategyCfg))) // §F33 基线跟随
      showToast('战法参数已保存，热更新即时生效', 'success')
    } catch (e) {
      if (e && e.status === 409) {
        // §中-6 冲突：绝不自动重放（后写覆盖前写正是本缺陷要杀的行为），强制人工重载比对。
        setStrategyLoadError('战法参数已被其他管理员更新（版本冲突），已禁止本次覆盖保存；请点「重载」读取最新参数后重新修改。')
        setStrategyLoadState('error')
        setStrategyConflict(true)
        showToast('保存被拒绝：参数版本冲突，请重载后重试', 'error')
      } else {
        showToast('保存失败: ' + (e.message || '未知错误'), 'error')
      }
    }
    setStrategySaving(false)
  }

  // 切换「显示全部资讯」开关
  async function toggleNewsShowAll(next) {
    const val = typeof next === 'boolean' ? next : newsShowAll
    try {
      const res = await api.toggleNewsShowAll(val)
      if (res && typeof res.news_show_all === 'boolean') setNewsShowAll(res.news_show_all)
    } catch (e) {
      setNewsShowAll(v => !v)
      showToast('切换失败: ' + (e.message || '未知错误'), 'error')
    }
  }

  // 保存服务器地址到本地存储
  function saveServer() {
    api.setStoredServer(serverUrl)
    showToast('服务器地址已保存', 'success')
  }

  // 请求浏览器通知权限并发送测试通知
  function requestNotify() {
    requestPermission().then(perm => {
      if (perm === 'granted') {
        sendNotify('量仔', '通知授权成功')
        showToast('通知授权成功', 'success')
      } else {
        showToast('通知被拒绝，请在系统设置中开启通知', 'warning')
      }
    })
  }

  // 播放测试音效，验证浏览器音频提醒可用
  function playTest() {
    try {
      const ctx = new (window.AudioContext || window.webkitAudioContext)()
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.connect(gain); gain.connect(ctx.destination)
      osc.frequency.value = 660; osc.type = 'sine'
      gain.gain.value = 0.1; osc.start(); osc.stop(ctx.currentTime + 0.2)
    } catch (_) {}
  }

  // 保存 LLM API 地址、Key、模型与并发配置
  //
  // 2026-09-18 重写：此前无论后端是否真的生效都弹"已保存并热生效"，而热更新存在两条真实的
  // 失败路径（配置被探测判定为不可用 → 拒绝；无法判定 → 采用但未验证）。UI 说"已生效"而
  // 实际没生效，正是"改了没生效"体感的来源。现在按后端回报如实展示，并把逐把 key 的结论摆出来。
  async function saveLLM() {
    setLlmSaving(true)
    setLlmNote(null)
    try {
      const resp = await api.setLLMConfig(llmPayload({ force: llmForce }))
      const res = resp?.result || {}
      // 只有后端确认 applied 才更新基线与"已配置"标记（否则页面显示与运行时会不一致）
      if (res.applied) {
        setLlmConfigured(true)
        setLlmForce(false)
        // §F33 保存成功后基线跟随当前值，dirty 归零
        setLlmBaseline({
          api_url: llmApiUrl, model: llmModel, classifier_model: llmClassifierModel,
          batch_concurrency: llmBatchConcurrency, d1_max_tokens: llmD1MaxTokens, api_keys: llmApiKeys,
        })
      }
      setLlmNote(llmNoteFromResult(res, res.warning ? '已生效，但有保留意见' : '已生效且验证通过'))
      if (res.warning) {
        showToast('LLM 配置已生效，但有保留意见（详见下方说明）', 'warning')
      } else {
        showToast('LLM 配置已热生效并验证通过', 'success')
      }
    } catch (e) {
      // 409 = 探测未通过被拒绝：运行时与磁盘**都没有动**，当前可用配置仍在跑。
      setLlmNote({ kind: 'error', head: '未生效（探测未通过，已保留当前可用配置）', text: e.message || '未知错误' })
      showToast('LLM 配置未生效：' + (e.message || '未知错误'), 'error')
    }
    setLlmSaving(false)
  }

  // 测试连接：只探测、不改任何状态。盘中排查"现在到底能不能用"的第一动作。
  async function probeLLM() {
    setLlmProbing(true)
    setLlmNote(null)
    try {
      const resp = await api.probeLLMConfig(llmPayload())
      const res = resp?.result || {}
      setLlmNote(llmNoteFromResult(res, res.verified ? '连通正常（配置可用）' : '未通过：当前填写的配置无法确认可用'))
      showToast(res.verified ? 'LLM 连接正常' : 'LLM 连接未通过（详见下方说明）', res.verified ? 'success' : 'warning')
    } catch (e) {
      setLlmNote({ kind: 'error', head: '探测失败', text: e.message || '未知错误' })
      showToast('探测失败: ' + (e.message || '未知错误'), 'error')
    }
    setLlmProbing(false)
  }

  // 回滚到上一个**已验证可用**的配置：热更新翻车（例如强制应用了不可用的配置）时的兜底动作，
  // 不必重启、不必回忆上次填了什么。回滚后回读表单，保证页面显示与运行时一致。
  async function rollbackLLM() {
    setLlmRolling(true)
    setLlmNote(null)
    try {
      const resp = await api.rollbackLLMConfig()
      const res = resp?.result || {}
      setLlmNote(llmNoteFromResult(res, '已回滚到上一个可用配置'))
      showToast('已回滚到上一个可用配置', 'success')
      try {
        const cfg = await api.fetchLLMConfig()
        if (cfg) applyLLMCfgToForm(cfg)
      } catch (_) {}
    } catch (e) {
      setLlmNote({ kind: 'error', head: '回滚失败', text: e.message || '未知错误' })
      showToast('回滚失败: ' + (e.message || '未知错误'), 'error')
    }
    setLlmRolling(false)
  }

  // llmPayload 组装提交体；force 只在显式勾选时带上（默认走"探测未通过则拒绝"的保护）。
  function llmPayload(extra = {}) {
    return {
      // 多 Key 支持：按换行或逗号拆分并去除空白，过滤空串
      api_keys: llmApiKeys.split(/[\n,]/).map(s => s.trim()).filter(Boolean),
      api_url: llmApiUrl,
      model: llmModel,
      classifier_model: llmClassifierModel,
      batch_concurrency: llmBatchConcurrency,
      d1_max_tokens: llmD1MaxTokens,
      ...extra,
    }
  }

  // llmNoteFromResult 把后端的 result 渲染成可读回报：结论 + 逐把 key 的原因 + 保留意见。
  // 逐把列出是刻意的：用户要拿着"第几把 key 为什么不行"去改输入框的对应行。
  function llmNoteFromResult(res, head) {
    const lines = []
    const probes = Array.isArray(res?.probes) ? res.probes : []
    probes.forEach((p) => {
      const status = p.status ? `HTTP ${p.status}` : '无响应'
      const detail = p.detail ? ` — ${p.detail}` : ''
      lines.push(`第 ${p.index + 1} 把：${probeKindLabel(p.kind)}（${status}）${detail}`)
    })
    if (typeof res?.effective_keys === 'number') {
      // 「被剔除」只数**确凿不可用**的（密钥无效/模型或地址不存在/欠费/地址不是 API 端点）；
      // 探测超时、上游 5xx 这类"未能判定"的 key 是**保留**在池里与配置里的，不算剔除
      // （§2026-09-20：否则用户会以为探测报红的 key 被删了）。
      lines.push(`生效密钥 ${res.effective_keys} 把；确认不可用已剔除 ${res.dropped_keys || 0} 把`)
    }
    if (res?.api_url) lines.push(`地址 ${res.api_url}；模型 ${res.model || '（默认）'}`)
    const kind = res?.rejected ? 'error' : (res?.warning ? 'warn' : 'success')
    return { kind, head, text: res?.warning || '', lines }
  }

  // probeKindLabel 探测结论的中文名（与后端 llm.ProbeKind 对齐，仅用于展示）。
  function probeKindLabel(kind) {
    const map = {
      ok: '可用', auth: '密钥无效/无权限', model: '地址或模型不可用', quota: '余额/额度不足',
      rate_limited: '被限流（密钥有效）', bad_request: '请求被拒（未能验证）',
      server: '供应商故障（未能验证）', network: '网络不可达（未能验证）', no_key: '未提供密钥',
      // §P0 2026-09-20：地址不是 API 端点（典型：填了供应商网页控制台域名，被 307 跳到登录页）。
      not_endpoint: '地址不是 API 端点（上游返回网页）',
      // §2026-09-20：后端探测总预算（默认 90s）用尽，这把没来得及判定——
      // **不是**"密钥坏了"，所以文案必须与 network（网络不可达）区分开。
      budget: '未完成判定（探测超时预算用尽，不代表密钥不可用）',
    }
    return map[kind] || kind || '未知'
  }

  // applyLLMCfgToForm 把后端 LLM 配置回填到表单（初始化与回滚后共用同一份口径）。
  function applyLLMCfgToForm(cfg) {
    setLlmApiUrl(cfg.api_url || '')
    setLlmModel(cfg.model || '')
    setLlmClassifierModel(cfg.classifier_model || '')
    if (cfg.batch_concurrency > 0) setLlmBatchConcurrency(cfg.batch_concurrency)
    if (cfg.d1_max_tokens > 0) setLlmD1MaxTokens(cfg.d1_max_tokens)
    // 多 Key 场景：数组按换行合并为一段文本；兼容旧版单 api_key 字段
    let keys = ''
    if (Array.isArray(cfg.api_keys) && cfg.api_keys.length) {
      keys = cfg.api_keys.join('\n')
    } else if (cfg.api_key) {
      keys = cfg.api_key
    }
    setLlmApiKeys(keys)
    // 已配置判定：有 Key 或有地址即视为已配置
    setLlmConfigured(!!(keys || cfg.api_url))
    // §F33 基线同步：与上面 set* 一一对应，用于计算 dirty
    setLlmBaseline({
      api_url: cfg.api_url || '', model: cfg.model || '',
      classifier_model: cfg.classifier_model || '',
      batch_concurrency: cfg.batch_concurrency > 0 ? cfg.batch_concurrency : 4,
      d1_max_tokens: cfg.d1_max_tokens > 0 ? cfg.d1_max_tokens : 2048,
      api_keys: keys,
    })
  }

  // 更新指定战法分组中的某个参数字段
  function setStrategyField(group, field, value) {
    setStrategyCfg(prev => ({
      ...prev,
      [group]: { ...prev[group], [field]: value },
    }))
  }

  // 初始化：检测服务器在线状态并加载 LLM/战法/资讯开关配置
  useEffect(() => {
    ;(async () => {
      // 1) 服务器连通性探测：仅设置在线状态标记
      try {
        await api.fetchStatus()
        setServerOnline(true)
      } catch (_) { setServerOnline(false) }
      // 2) 读取 LLM 配置回填到表单
      try {
        const cfg = await api.fetchLLMConfig()
        // 回填口径统一走 applyLLMCfgToForm（与回滚后回读共用，避免两处各写一遍而分叉）
        if (cfg) applyLLMCfgToForm(cfg)
      } catch (e) {
        // §A5：首个 admin 数据端点即 403=服务端权威角色非管理员，跳统一 403 页
        if (api.isForbidden(e)) navigate('/403')
      }
      // 3) 读取战法参数（§N-4 三态加载，统一走 loadStrategyCfg：失败=禁保存+红条）
      await loadStrategyCfg()
      // 4) 读取"显示全部资讯"开关状态
      try {
        const ns = await api.fetchNewsShowAllStatus()
        if (ns && typeof ns.news_show_all === 'boolean') setNewsShowAll(ns.news_show_all)
      } catch (_) {}
    })()
  }, [])

  // §F33 修复：dirty 计算 + beforeunload 拦截 + 保存按钮未保存标记
  // 基线未加载完成时视为不 dirty（避免首帧误闪）
  const llmDirty = llmBaseline != null && (
    llmApiUrl !== llmBaseline.api_url ||
    llmModel !== llmBaseline.model ||
    llmClassifierModel !== llmBaseline.classifier_model ||
    llmBatchConcurrency !== llmBaseline.batch_concurrency ||
    llmD1MaxTokens !== llmBaseline.d1_max_tokens ||
    llmApiKeys !== llmBaseline.api_keys
  )
  const strategyDirty = strategyBaseline != null && JSON.stringify(strategyCfg) !== JSON.stringify(strategyBaseline)
  const anyDirty = llmDirty || strategyDirty
  useEffect(() => {
    if (!anyDirty) return
    // beforeunload 拦截：存在未保存修改时提示浏览器挽留
    const h = (e) => { e.preventDefault(); e.returnValue = '' }
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [anyDirty])

  // 按字段类型渲染控件：switch 类型用 Switch，其余用 InputNumber（默认步进 1）
  const renderField = (group, f) => {
    // 开关型字段：渲染 ToggleSw 布尔控件
    if (f.type === 'switch') {
      return (
        <ToggleSw
          checked={!!strategyCfg[group.key][f.k]}
          onChange={(v) => setStrategyField(group.key, f.k, v)}
        />
      )
    }
    // 数字型字段：渲染 InputNumber（默认步进 1，列式布局）
    // §N-4：旧写法 `?? 0` 把「后端没给这个键」渲染成 0——缺失与真实 0 混同，
    // 用户没碰过它也会以 0 提交（配合后端旧全量替换=静默清零）。现缺失渲染为**空**，
    // 由保存前必填校验兜底；真实存 0 的字段后端回 0，0 ?? undefined=0，不受影响。
    // English: §N-4 — a missing key now renders EMPTY (validated as required on save)
    // instead of 0; an actually-stored 0 still renders 0.
    return (
      <InputNumber
        value={strategyCfg[group.key][f.k] ?? undefined}
        onChange={(v) => setStrategyField(group.key, f.k, v)}
        step={f.step || 1}
        theme="column"
        style={{ width: 200 }}
      />
    )
  }

  return (
    <div className="page">
      <SectionLabel>设置</SectionLabel>

      <Card title="服务器连接" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>服务器地址</span>
          <Input value={serverUrl} onChange={(v) => setServerUrl(v)} placeholder="http://localhost:8080" style={{ width: 280 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>连接状态</span>
          <Tag theme={serverOnline ? 'success' : 'default'} variant="light">
            {serverOnline ? '已连接' : '离线'}
          </Tag>
        </div>
        <Button theme="primary" onClick={saveServer}>保存</Button>
      </Card>

      <Card title="通知设置" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>浏览器通知</span>
          <Button theme="default" variant="outline" onClick={requestNotify}>授权并测试</Button>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>声音提醒</span>
          <Button theme="default" variant="outline" onClick={playTest}>测试声音</Button>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>macOS 通知</span>
          <Tag theme="success" variant="light">后台自动发送</Tag>
        </div>
      </Card>

      <Card title="账户信息" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>账号</span>
          <span style={{ color: 'var(--app-text)' }}>{account}</span>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>令牌</span>
          <Tag theme="default" variant="light">
            {token ? token.slice(0, 20) + '...' : '未登录'}
          </Tag>
        </div>
      </Card>

      <Card title="LLM 配置" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>API URL</span>
          <Input value={llmApiUrl} onChange={(v) => setLlmApiUrl(v)} placeholder="https://api.siliconflow.cn/v1/chat/completions" style={{ width: 280 }} />
        </div>
        <div style={{ ...rowStyle, alignItems: 'flex-start' }}>
          <span style={labelStyle} />
          <span style={{ fontSize: 10, color: 'var(--app-text-2)', maxWidth: 280 }}>
            填供应商文档里的 base URL（如 https://api.siliconflow.cn/v1）也可以，会自动补上
            /chat/completions；已带完整路径的原样使用。
          </span>
        </div>
        {
          /* API Key 文本域：每行一个 Key，多 Key 后端轮询分发。
             2026-09-18：补脱敏回显提示——回读是掩码（sk-…1234），保持掩码=沿用库中原值；
             要换 Key 必须整框替换，掩码与新 Key 并存时掩码那一槽位仍指向旧 Key（曾导致
             "改了 Key 却不生效"的误判）。 */
        }
        <div style={rowStyle}>
          <span style={labelStyle}>API Key(s)</span>
          <Textarea value={llmApiKeys} onChange={(v) => setLlmApiKeys(v)} placeholder="sk-...&#10;sk-...（每行一个，多个则轮询分发）" autosize={{ minRows: 4, maxRows: 8 }} style={{ width: 280 }} />
        </div>
        <div style={{ ...rowStyle, alignItems: 'flex-start' }}>
          <span style={labelStyle} />
          <span style={{ fontSize: 10, color: 'var(--app-text-2)', maxWidth: 280 }}>
            显示的是脱敏值（如 sk-…1234）：保持不动＝沿用库中已存的 Key。
            要换 Key 请<strong>整框替换</strong>（掩码与新 Key 同行并存时，掩码那一行仍指向旧 Key）。
          </span>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>模型</span>
          <Input value={llmModel} onChange={(v) => setLlmModel(v)} placeholder="gpt-4o-mini" style={{ width: 280 }} />
        </div>
        {
          /* 分类专用模型：留空则回退主模型 */
        }
        <div style={rowStyle}>
          <span style={labelStyle}>分类专用模型</span>
          <Input value={llmClassifierModel} onChange={(v) => setLlmClassifierModel(v)} placeholder="留空则用主模型" style={{ width: 280 }} />
        </div>
        {
          /* 归因批并发度：批量归因请求的并发上限（1-16） */
        }
        <div style={rowStyle}>
          <span style={labelStyle}>归因批并发度</span>
          <InputNumber value={llmBatchConcurrency} onChange={(v) => setLlmBatchConcurrency(v)} min={1} max={16} style={{ width: 200 }} />
        </div>
        {
          /* D1 推理上限：单条 D1 评分推理的最大 tokens */
        }
        <div style={rowStyle}>
          <span style={labelStyle}>D1推理上限</span>
          <InputNumber value={llmD1MaxTokens} onChange={(v) => setLlmD1MaxTokens(v)} min={512} max={4096} step={256} style={{ width: 200 }} />
          <span style={{ ...labelStyle, color: 'var(--app-muted-2)', marginLeft: 8, fontSize: 12 }}>tokens（默认 2048，D1 评分推理长度）</span>
        </div>
        {
          /* 配置状态展示与保存按钮 */
        }
        <div style={rowStyle}>
          <span style={labelStyle}>状态</span>
          <Tag theme={llmConfigured ? 'success' : 'default'} variant="light">
            {llmConfigured ? '已配置' : '未配置（降级为关键词过滤）'}
          </Tag>
        </div>
        {/* §F33 dirty=true 时右侧圆点+文字提示，避免"改了忘保存切页丢" */}
        {/* 三个动作：保存（探测→切换→落库）/ 测试连接（只探测）/ 回滚（回到上个已验证可用配置） */}
        <div style={{ ...rowStyle, flexWrap: 'wrap', gap: 8 }}>
          <Button theme="primary" onClick={saveLLM} loading={llmSaving}>保存</Button>
          <Button variant="outline" onClick={probeLLM} loading={llmProbing}>
            {llmProbing ? '探测中…' : '测试连接'}
          </Button>
          <Button variant="outline" onClick={rollbackLLM} loading={llmRolling}>回滚到上一个可用配置</Button>
          {llmDirty && <span style={{ marginLeft: 8, color: 'var(--app-warn-text)', fontSize: 12 }}>● 有未保存修改</span>}
          {/* 探测是**真出网**逐把打供应商（单把最长 45s，密钥多时要分波），必然要点时间。
              不提示出来，用户会以为卡死而反复点；后端总预算是硬上界（见 api 里 LLM_PROBE_TIMEOUT）。 */}
          {llmProbing && (
            <div style={{ width: '100%', color: 'var(--app-text-2)', fontSize: 12, marginTop: 4 }}>
              正在逐把探测：每把一次真实最小调用，最多约 1 分钟（不动不是死机，请勿重复点击）
            </div>
          )}
        </div>
        {
          /* 强制应用开关：只在探测明确判定配置不可用、但用户确信是环境问题时才打开。
             默认关闭——误存一把坏 key 会同时打坏运行时与落库配置，且重启也救不回来。 */
        }
        <div style={{ ...rowStyle, alignItems: 'flex-start' }}>
          <span style={labelStyle}>强制应用</span>
          <ToggleSw checked={llmForce} onChange={setLlmForce} />
          <span style={{ fontSize: 10, color: 'var(--app-text-2)', maxWidth: 280, marginLeft: 8 }}>
            默认关闭。开启后即使探测判定配置不可用也照样切换并落库（用于供应商抖动、本机代理不通
            等与配置无关的失败）。启用前请先看清下方逐把结论——翻车可用"回滚"退回。
          </span>
        </div>
        {
          /* 结果回报：生效了没有 / 哪把 key 为什么没生效 / 是否已验证。 */
        }
        {llmNote && (
          <div style={{
            margin: '4px 0 8px',
            padding: '8px 10px',
            borderRadius: 4,
            fontSize: 12,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-all',
            maxWidth: 520,
            background: llmNote.kind === 'error' ? 'var(--app-danger-bg, #fdecee)' : (llmNote.kind === 'warn' ? 'var(--app-warn-bg, #fff7e6)' : 'var(--app-success-bg, #e8f5e9)'),
            color: 'var(--app-text-1, #333)',
            border: '1px solid var(--app-border, #e0e0e0)',
          }}>
            <div style={{ fontWeight: 600 }}>
              {llmNote.kind === 'error' ? '✕ ' : (llmNote.kind === 'warn' ? '! ' : '✓ ')}{llmNote.head}
            </div>
            {llmNote.text && <div style={{ marginTop: 4 }}>{llmNote.text}</div>}
            {(llmNote.lines || []).map((l, i) => (
              <div key={i} style={{ marginTop: 2, color: 'var(--app-text-2)' }}>· {l}</div>
            ))}
          </div>
        )}
      </Card>

      {/* 各战法参数按分组（dragon/双凸/N 形/回头/动量）各自渲染一张卡，卡内逐字段走 renderField 编辑器 */}
      {strategyGroups.map((group) => (
        <Card key={group.key} title={group.title} style={{ marginBottom: 16 }}>
          {group.fields.map((f) => (
            <div style={rowStyle} key={f.k}>
              <span style={{ ...labelStyle, width: 200 }} title={f.hint || ''}>{f.label}</span>
              {renderField(group, f)}
            </div>
          ))}
        </Card>
      ))}

      <Card title="战法参数" style={{ marginBottom: 16 }}>
        {/* §N-4 保存闸三态提示：error=红条「读取失败，禁止保存」（版本冲突复用此条，措辞换版）；
            loading=灰字进行态；loaded=原说明。error 态禁用保存按钮并给「重载」出口。 */}
        {strategyLoadState === 'error' && (
          <div
            role="alert"
            style={{
              margin: '0 0 10px',
              padding: '8px 10px',
              borderRadius: 4,
              fontSize: 12,
              background: 'var(--app-danger-bg, #fdecee)',
              border: '1px solid var(--app-border, #e0e0e0)',
              color: 'var(--app-text-1, #333)',
            }}
          >
            <div style={{ fontWeight: 600 }}>
              {strategyConflict ? '⛔ 版本冲突，禁止保存' : '⛔ 读取失败，禁止保存'}
            </div>
            <div style={{ marginTop: 4 }}>{strategyLoadError || '战法参数读取失败（原因未知），为避免把缺失值写成 0，已禁止保存。'}</div>
          </div>
        )}
        <div style={rowStyle}>
          <span style={labelStyle}>说明</span>
          <span className="muted" style={{ fontSize: 12 }}>
            {strategyLoadState === 'loading' && '战法参数读取中…'}
            {strategyLoadState === 'loaded' && '参数保存后热更新生效；权重请保持各策略合计 ≤ 1；后端为稀疏保存（未改动/缺失字段保留库中原值）'}
            {strategyLoadState === 'error' && '当前不可保存：先「重载」'}
          </span>
        </div>
        <div style={{ ...rowStyle, gap: 8 }}>
          <Button theme="primary" onClick={saveStrategy} loading={strategySaving} disabled={strategyLoadState !== 'loaded'}>
            保存战法参数
          </Button>
          {strategyLoadState !== 'loaded' && (
            <Button variant="outline" onClick={loadStrategyCfg} disabled={strategyLoadState === 'loading'}>重载</Button>
          )}
        </div>
        {strategyDirty && <span style={{ marginLeft: 8, color: 'var(--app-warn-text)', fontSize: 12 }}>● 有未保存修改</span>}
      </Card>

      <Card title="资讯显示" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={{ ...labelStyle, width: 240 }} title="开启后弱档/中性资讯（|score|<0.25）也出现在资讯列表；关闭则仅显示有价值的强事件">显示全部资讯（含弱/中性）</span>
          <ToggleSw
            checked={newsShowAll}
            onChange={(v) => { setNewsShowAll(v); toggleNewsShowAll(v) }}
          />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>说明</span>
          <span className="muted" style={{ fontSize: 12 }}>该开关即时生效，不影响引擎打分（引擎始终按 |score|≥0.5 过滤）</span>
        </div>
      </Card>

      {/* §D-3 配置历史/回滚（admin）：防"自己偷偷改参数无痕迹"——每次配置写操作都有快照，
          这里列表+diff+一键回滚。规则快照=config.json（LLM/风控/撮合等全部 rules 层）；
          战法参数快照=参数版本化（审批应用/寻优写入时落）。 */}
      {histAdmin && (
        <Card title="配置历史与回滚" style={{ marginBottom: 16 }} loading={histLoading}>
          <div style={{ fontSize: 12, color: 'var(--app-text-2)', marginBottom: 8 }}>
            共 {histRuleSnaps.length} 个规则快照 / {histStratSnaps.length} 个战法参数快照。回滚为原子恢复并写入运维日志，操作前请确认当前时段。
          </div>
          {/* 规则配置快照表：diff 展开 + 回滚 */}
          <div style={{ fontWeight: 600, margin: '6px 0 4px' }}>规则配置（config.json）</div>
          {histRuleSnaps.length === 0 ? (
            <div className="muted" style={{ fontSize: 12 }}>暂无快照（保存过设置后自动生成）</div>
          ) : (
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <tbody>
                {histRuleSnaps.slice(0, 20).map((sn) => (
                  <tr key={sn.snapshot_ts} style={{ borderBottom: '1px solid #eee' }}>
                    <td style={{ padding: '3px 6px', fontFamily: 'monospace', fontSize: 12 }}>{sn.snapshot_ts}</td>
                    <td style={{ padding: '3px 6px', textAlign: 'right', whiteSpace: 'nowrap' }}>
                      <Button size="sm" variant="text" theme="primary" onClick={() => openHistDiff(sn.snapshot_ts)}>
                        {histDiff && histDiff.snapshot_ts === sn.snapshot_ts ? '收起 diff' : 'diff'}
                      </Button>
                      <Button size="sm" variant="text" theme="danger" onClick={() => setHistRollback({ kind: 'rules', ts: sn.snapshot_ts })}>回滚</Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {histDiff && (
            <pre style={{ background: 'var(--app-bg-2, #f6f7f9)', padding: 8, fontSize: 12, maxHeight: 240, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: '6px 0 10px' }}>
              {histDiff.diff || '（无差异）'}
            </pre>
          )}
          {/* 战法参数快照表：仅回滚（diff 面在研究页参数监测） */}
          <div style={{ fontWeight: 600, margin: '10px 0 4px' }}>战法参数（版本化快照）</div>
          {histStratSnaps.length === 0 ? (
            <div className="muted" style={{ fontSize: 12 }}>暂无快照（审批/寻优应用战法参数时生成）</div>
          ) : (
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <tbody>
                {histStratSnaps.slice(0, 20).map((sn) => (
                  <tr key={sn.snapshot_ts} style={{ borderBottom: '1px solid #eee' }}>
                    <td style={{ padding: '3px 6px', fontFamily: 'monospace', fontSize: 12 }}>{sn.snapshot_ts}</td>
                    <td style={{ padding: '3px 6px', textAlign: 'right' }}>
                      <Button size="sm" variant="text" theme="danger" onClick={() => setHistRollback({ kind: 'strategy', ts: sn.snapshot_ts })}>回滚</Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
      )}

      <Card title="系统" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>版本</span>
          <span>量仔 v1.1.0 桌面版</span>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>后端</span>
          <span>Go 1.22+ 单二进制</span>
        </div>
      </Card>

      {/* §D-3 回滚二次确认：写清目标快照与影响面，确认才执行（原子恢复+审计在服务端） */}
      <Dialog
        visible={!!histRollback}
        header="确认回滚配置"
        onClose={() => setHistRollback(null)}
        onCancel={() => setHistRollback(null)}
        onConfirm={confirmHistRollback}
        confirmBtn={{ content: '确认回滚', theme: 'danger' }}
        cancelBtn="取消"
      >
        {histRollback && (
          <div style={{ fontSize: 13 }}>
            将把「{histRollback.kind === 'rules' ? '规则配置（LLM/风控/撮合等全部 rules 层）' : '战法参数'}」
            原子恢复到快照 <b style={{ fontFamily: 'monospace' }}>{histRollback.ts}</b>。
            <div style={{ color: 'var(--td-warning-color)', marginTop: 6 }}>⚠ 当前未保存的修改会被覆盖，操作将写入运维日志。</div>
          </div>
        )}
      </Dialog>
    </div>
  )
}

// 板块小标题：设置页各区块的简短分组标题
function SectionLabel({ children }) {
  return <div style={{ fontWeight: 600, margin: '8px 0 4px', fontSize: 13 }}>{children}</div>
}
