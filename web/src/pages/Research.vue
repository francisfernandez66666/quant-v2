<!--
  自动研究页 Research.vue（B5）
  Auto-research page Research.vue (B5)
  展示优化器产出的候选列表，支持审批通过（应用权重）/ 驳回。
  Shows optimizer-produced candidates with approve (apply weights) / reject actions.
-->
<template>
  <div class="research-page">
    <div class="page-header">
      <h2>自动研究</h2>
      <div class="header-right">
        <!-- 状态过滤：全部 / 待审批 / 已应用 / 已驳回 / 已审批 -->
        <select v-model="statusFilter" class="status-filter" @change="loadData">
          <option value="">全部</option>
          <option value="proposed">待审批</option>
          <option value="applied">已应用</option>
          <option value="approved">已审批</option>
          <option value="rejected">已驳回</option>
        </select>
        <button class="btn-refresh" @click="loadAll" :disabled="loading">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
      </div>
    </div>

    <!-- 子页切换：待审批候选 / 战法库 / 设置 -->
    <div class="research-tabs">
      <button
        v-for="t in tabs"
        :key="t.key"
        :class="['tab', activeTab === t.key ? 'active' : '']"
        @click="activeTab = t.key"
      >
        {{ t.label }}
      </button>
    </div>

    <!-- ══════════ Tab 1: 待审批候选 ══════════ -->
    <template v-if="activeTab === 'candidates'">
    <!-- 研究处理进度：数据准备度 + 候选产出状态 -->
    <div class="progress-panel" v-if="progress">
      <div class="progress-title">研究处理进度</div>
      <div class="progress-grid">
        <!-- 数据准备度：近一年有行情的股票 / 全市场 -->
        <div class="progress-item">
          <div class="progress-label">数据准备度（近一年有行情 / 全市场）</div>
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: pct(progress.ready_pct) + '%' }"></div>
          </div>
          <div class="progress-meta">{{ progress.ready_stocks }} / {{ progress.stocks }} 只（{{ pct(progress.ready_pct) }}%）</div>
        </div>
        <!-- 日线覆盖 -->
        <div class="progress-item">
          <div class="progress-label">日线数据</div>
          <div class="progress-meta">{{ fmtRows(progress.daily_rows) }} 行</div>
        </div>
        <!-- 财务覆盖 -->
        <div class="progress-item">
          <div class="progress-label">财务指标</div>
          <div class="progress-meta">{{ fmtRows(progress.fin_rows) }} 行</div>
        </div>
        <!-- 候选产出 -->
        <div class="progress-item">
          <div class="progress-label">研究候选</div>
          <div class="progress-meta">
            <span class="meta-chip">{{ progress.candidates }} 条</span>
            <span class="meta-chip" v-if="progress.applied">已应用 {{ progress.applied }}</span>
            <span class="meta-chip" v-if="progress.proposed">待审批 {{ progress.proposed }}</span>
          </div>
        </div>
      </div>
    </div>
    </template>

    <!-- ══════════ Tab 2: 战法库 ══════════ -->
    <template v-else-if="activeTab === 'library'">
    <!-- 战法库：已应用的因子战法（启用/禁用/删除/重命名 + 效果监测） -->
    <div class="library-panel">
      <div class="library-header">
        <div class="library-title">战法库（已应用因子战法）</div>
        <button class="btn-refresh" @click="loadLibrary" :disabled="loadingLibrary">
          {{ loadingLibrary ? '加载中...' : '刷新' }}
        </button>
      </div>
      <div v-if="library.length === 0" class="empty">暂无已应用因子战法。审批通过的因子候选会自动加入战法库并注入 8a/8b 实盘。</div>
      <div v-else class="library-list">
        <div v-for="s in library" :key="s.id" class="library-card">
          <div class="lib-head">
            <span class="lib-name" v-if="!editingName[s.id]">{{ s.name }}</span>
            <input
              v-else
              v-model="nameDraft[s.id]"
              class="name-input"
              @keyup.enter="saveName(s)"
              @blur="saveName(s)"
            />
            <button class="btn-rename" @click="startRename(s)" v-if="canApprove && !editingName[s.id]">改名</button>
            <span :class="['tag', 'tag-kind', s.kind === 'pattern' ? 'kind-pattern' : 'kind-factor']">{{ s.kind === 'pattern' ? '形态' : '因子' }}</span>
            <span :class="['tag', s.enabled ? 'status-applied' : 'status-rejected']">{{ s.enabled ? '已启用' : '已停用' }}</span>
            <span class="lib-id">{{ s.id }}</span>
            <span class="lib-time">{{ s.applied_at }}</span>
          </div>
          <!-- 因子战法：因子方向 + 权重；形态战法：条件集 -->
          <div class="lib-factors">
            <template v-if="s.kind === 'pattern'">
              <span v-for="(c, i) in s.conds || []" :key="i" class="cond-chip">
                {{ condLabel(c) }}
              </span>
            </template>
            <template v-else>
              <span v-for="f in ruleFactors(s)" :key="f.id" class="factor-chip">
                <span :class="['dir-badge', f.dir < 0 ? 'short' : 'long']">{{ f.dir < 0 ? '看空' : '看多' }}</span>
                <span class="factor-name">{{ f.label }}</span>
                <span class="factor-id">{{ f.id }}</span>
              </span>
            </template>
          </div>
          <div class="lib-stats">
            <span class="stat">信号 <b>{{ s.signal_count }}</b></span>
            <span class="stat">胜 <b class="pos">{{ s.win }}</b></span>
            <span class="stat">负 <b class="neg">{{ s.loss }}</b></span>
            <span class="stat">累计前向收益 <b :class="s.cum_return >= 0 ? 'pos' : 'neg'">{{ fmtPct(s.cum_return) }}</b></span>
            <span class="stat">回测超额 <b :class="signClass(s.excess)">{{ fmt(s.excess) }}</b></span>
          </div>
          <div class="lib-actions" v-if="canApprove">
            <button class="btn-toggle" @click="toggleLibrary(s)">
              {{ s.enabled ? '停用' : '启用' }}
            </button>
            <button class="btn-reject" @click="removeLibrary(s)">删除</button>
          </div>
        </div>
      </div>
    </div>
    </template>

    <!-- ══════════ Tab 3: 设置 ══════════ -->
    <template v-else-if="activeTab === 'settings'">
    <div class="settings-panel">
      <div class="settings-title">研究调度设置</div>
      <div class="setting-row">
        <div class="setting-info">
          <div class="setting-label">全量回测全局开关</div>
          <div class="setting-desc">开启后，夜间自动研究在发现因子候选后会追加一次 B4 全链路回测（回填回测超额）；关闭则只做发现、不做回测，省时省 CPU。</div>
        </div>
        <label class="switch">
          <input type="checkbox" v-model="backtestEnabled" @change="saveBacktestToggle" />
          <span class="slider"></span>
        </label>
        <span class="setting-state">{{ backtestEnabled ? '已开启' : '已关闭' }}</span>
      </div>
      <div class="setting-hint">配置写入 rules.scheduler.nightly.backtest_enabled，quant-research 服务 30s 内热生效。</div>
    </div>
    </template>

    <!-- 待审批候选：空态 + 候选列表（Tab 1 内） -->
    <div v-if="activeTab === 'candidates' && noDB" class="empty">研究库未接入（需后端开启 B5 研究闭环）</div>
    <div v-else-if="activeTab === 'candidates' && candidates.length === 0" class="empty">暂无候选，先在命令行跑 research optimize 产出</div>

    <!-- 候选卡片列表 -->
    <div v-if="activeTab === 'candidates' && candidates.length > 0" class="candidate-list">
      <div v-for="c in candidates" :key="c.id" class="candidate-card">
        <div class="cand-header">
          <span class="cand-id">#{{ c.id }}</span>
          <span :class="['tag', 'tag-kind', 'kind-' + c.kind]">{{ kindLabel(c.kind) }}</span>
          <span :class="['tag', 'status-' + c.status]">{{ statusLabel(c.status) }}</span>
          <span class="cand-time">{{ c.created_at }}</span>
        </div>

        <!-- 因子战法候选：规则 + 验证 两块重点解释 -->
        <template v-if="c.kind === 'factor'">
          <div class="block-title">这条战法在做什么</div>
          <div class="factors-row">
            <div v-for="f in factorRule(c)" :key="f.id" class="factor-chip">
              <span :class="['dir-badge', f.dir < 0 ? 'short' : 'long']">{{ f.dir < 0 ? '看空' : '看多' }}</span>
              <span class="factor-name">{{ f.label }}</span>
              <span class="factor-id">{{ f.id }}</span>
              <span class="factor-weight">{{ f.weight.toFixed(2) }}权重</span>
            </div>
          </div>
          <div class="factor-desc">
            玩法：每天给所有股票按上面 {{ factorRule(c).length }} 个指标打分，分数最高的前一批会被标记为「值得买」，赌它们接下来 {{ c.horizon }} 个交易日能涨。
            <template v-if="factorRule(c).some(f => f.dir < 0)">
              注意：带「看空」的指标是反着用的——这项数值越高，反而越说明不该买。
            </template>
          </div>

          <!-- 验证：用大白话给结论 -->
          <div class="block-title">这条规律靠谱吗？（电脑验证过）</div>
          <div class="verify-plain">
            <div class="plain-summary">
              <span class="plain-badge" :class="verdict(c).ok ? 'good' : 'bad'">{{ verdict(c).ok ? '✅ 可以试试' : '⚠️ 建议别用' }}</span>
              <span class="plain-text">{{ verdict(c).text }}</span>
            </div>
            <div class="plain-line" v-for="(l, i) in plainLines(c)" :key="i">
              <span class="plain-num">{{ i + 1 }}.</span>
              <span class="plain-body">{{ l }}</span>
            </div>
          </div>

          <!-- 细节数据：折叠起来，想细看才展开 -->
          <details class="detail-block">
            <summary>想看具体数字？展开</summary>
            <div class="detail-row"><span class="d-label">样本内测试</span><span class="d-value">前一段历史回放：IR {{ fmt(parseReason(c,'样本内IR')) }}</span></div>
            <div class="detail-row"><span class="d-label">样本外测试</span><span class="d-value">另一段没用过的历史回放：IR {{ fmt(parseReason(c,'样本外IR')) }}</span></div>
            <div class="detail-row"><span class="d-label">反推超额</span><span class="d-value">高分股比全市场平均多赚 {{ fmtPct(parseReason(c,'反推超额')) }}</span></div>
            <div class="detail-row"><span class="d-label">全样本 IR</span><span class="d-value">{{ fmt(c.ir) }}（参考）</span></div>
            <div class="detail-row"><span class="d-label">全样本 IC</span><span class="d-value">{{ fmt(c.ic_mean) }}（参考）</span></div>
            <div class="detail-row"><span class="d-label">全链路回测</span><span class="d-value">{{ btTested(c) ? fmt(c.avg_excess) : '未测' }}</span></div>
          </details>
        </template>

        <!-- 其他类型候选（权重/形态/盘口）：保持原有简洁展示 -->
        <template v-else>
          <div class="metric-row">
            <div class="metric">
              <span class="metric-label">IR</span>
              <span :class="['metric-value', signClass(c.ir)]">{{ fmt(c.ir) }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">IC</span>
              <span :class="['metric-value', signClass(c.ic_mean)]">{{ fmt(c.ic_mean) }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">回测超额</span>
              <span :class="['metric-value', signClass(c.avg_excess)]">{{ fmt(c.avg_excess) }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">前瞻天数</span>
              <span class="metric-value">{{ c.horizon }}</span>
            </div>
          </div>
          <div class="weights-row" v-if="weightList(c).length">
            <span class="weight-chip" v-for="w in weightList(c)" :key="w[0]">
              <span class="weight-fid">{{ w[0] }}</span>
              <span class="weight-val">{{ w[1].toFixed(3) }}</span>
            </span>
          </div>
        </template>

        <!-- 盘口托/压单明细（kind=depth） -->
        <div class="depth-block" v-if="c.kind === 'depth'">
          <div v-for="(s, code) in depthSummary(c)" :key="code" class="depth-stock">
            <span class="depth-code">{{ code }}</span>
            <span class="depth-touch">买1 {{ s.bid1 }} / 卖1 {{ s.ask1 }}</span>
            <span
              v-for="o in s.orders"
              :key="o.level + o.kind"
              :class="['order-chip', o.kind === 'support' ? 'support' : 'resistance']"
            >
              {{ o.kind === 'support' ? '托' : '压' }}单 档{{ o.level }}
              {{ o.price }} / {{ o.volume }}手 ({{ (o.share_pct * 100).toFixed(0) }}%)
            </span>
          </div>
        </div>
        <!-- 操作按钮：有 research_approve 权限且待审批时可审批 / 驳回；因子候选可单独全量回测 -->
        <div class="cand-actions" v-if="canApprove && c.status === 'proposed'">
          <button class="btn-approve" @click="doApprove(c)">审批并应用</button>
          <button class="btn-reject" @click="doReject(c)">驳回</button>
          <button
            v-if="c.kind === 'factor'"
            class="btn-backtest"
            :disabled="backtestLoading[c.id]"
            @click="doBacktest(c)"
          >
            {{ backtestLoading[c.id] ? (backtestState[c.id] === 'running' ? '回测中...' : '回测中...') : (c.avg_excess ? '重新回测' : '全量回测') }}
          </button>
          <span v-if="backtestResult[c.id]" class="bt-result" :class="signClass(backtestResult[c.id])">
            回测超额 {{ fmt(backtestResult[c.id]) }}
          </span>
        </div>
        <!-- 无权限提示 -->
        <div class="no-perm" v-else-if="!canApprove && c.status === 'proposed'">
          无审批权限（需管理员授予 research_approve）
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onActivated, onUnmounted } from 'vue' // Vue 组合式 API：响应式引用与生命周期钩子
// Vue Composition API: reactive ref and lifecycle hooks
import * as api from '../api/index.js'           // 后端 API 调用封装（候选列表 / 审批 / 驳回）
// backend API wrapper (candidate list / approve / reject)

// ── 响应式状态 ──
// ── Reactive state ──
const loading = ref(false)      // 是否正在加载
// whether a load is in progress
const candidates = ref([])      // 候选列表
// candidate list
const noDB = ref(false)         // 研究库未接入
// research DB not wired up
const statusFilter = ref('')    // 状态过滤条件
// status filter
const progress = ref(null)      // 研究处理进度
// research processing progress
const library = ref([])         // 战法库（已应用因子战法）
// strategy library (applied factor strategies)
const loadingLibrary = ref(false)
// library loading flag
const backtestLoading = ref({}) // 候选 id → 是否回测中
// candidate id → backtest in progress
const backtestState = ref({})   // 候选 id → running/done/error
// candidate id → backtest state
const backtestResult = ref({})  // 候选 id → 回测超额结果
// candidate id → backtest excess result

// 子页 tab
const tabs = [
  { key: 'candidates', label: '待审批候选' },
  { key: 'library', label: '战法库' },
  { key: 'settings', label: '设置' },
]
const activeTab = ref('candidates')
// active sub-tab
const editingName = ref({}) // 战法 id → 是否在改名
const nameDraft = ref({})   // 战法 id → 改名草稿
const backtestEnabled = ref(false) // 全量回测全局开关（保存到 rules.scheduler.nightly.backtest_enabled）

/** 百分比显示（0~100 取整） */
/** Percentage display (0~100, rounded) */
function pct(v) {
  if (v === null || v === undefined || isNaN(v)) return 0
  return Math.min(100, Math.round(v * 100))
}

/** 行数格式化：千分位 */
/** Row count formatting with thousands separators */
function fmtRows(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  return Number(v).toLocaleString('zh-CN')
}

/** 加载研究处理进度 */
/** Load the research processing progress */
async function loadProgress() {
  try {
    const p = await api.fetchResearchProgress()
    if (p) progress.value = p
  } catch (e) {
    // 进度接口不可用（研究库未接入等）静默降级，不影响候选列表
    console.error('Research 进度加载失败', e)
  }
}

/** 刷新全部（进度 + 候选） */
/** Refresh both progress and candidate list */
async function loadAll() {
  loading.value = true
  await loadProgress()
  await loadData()
  loading.value = false
}

/** 是否拥有研究审批权限（research_approve；admin 隐式拥有） */
/** Whether the user may approve/reject research candidates (research_approve; admin implies all) */
const canApprove = api.hasPerm('research_approve')

/** 状态显示文本 */
/** Human-readable status label */
function statusLabel(s) {
  const m = { proposed: '待审批', approved: '已审批', applied: '已应用', rejected: '已驳回' }
  return m[s] || s
}

/** 格式化指标：保留 4 位，空值显示 '-' */
/** Format a metric to 4 decimals; '-' when empty */
function fmt(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  return Number(v).toFixed(4)
}

/** 指标正负样式类：正数绿、负数红 */
/** Sign-based style class: positive green, negative red */
function signClass(v) {
  if (v === null || v === undefined || isNaN(v)) return ''
  return Number(v) >= 0 ? 'pos' : 'neg'
}

/** 解析权重 JSON 为排序后的 [factorID, weight] 数组 */
/** Parse the weights JSON into a sorted [factorID, weight] array */
function weightList(c) {
  try {
    const w = JSON.parse(c.weights || '{}')
    // E6 因子候选：weights 为复合结构 {"weights":{...},"directions":{...},"buy_threshold":N}，
    // 只取其中数值权重子对象，避免把 directions/buy_threshold 等非数值项丢进渲染导致 toFixed 报错。
    // English: E6 factor candidates store a composite {"weights":{...},"directions":{...},"buy_threshold":N};
    // extract only the numeric weights sub-object so non-numeric entries never reach toFixed() in render.
    const wm = (w && typeof w === 'object' && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights))
      ? w.weights
      : w
    if (!wm || typeof wm !== 'object') return []
    const entries = Object.entries(wm).filter(([, v]) => typeof v === 'number' && isFinite(v))
    return entries.sort((a, b) => b[1] - a[1])
  } catch (_) {
    return []
  }
}

/** 解析盘口托/压单候选的 weights JSON（code → {orders, bid1, ask1}） */
/** Parse depth-candidate weights JSON (code → {orders, bid1, ask1}) */
function depthSummary(c) {
  try {
    return JSON.parse(c.weights || '{}')
  } catch (_) {
    return {}
  }
}

// ── 因子战法候选的可解释展示 ──
// Factor-strategy candidate human-readable rendering

/** 候选类型中文名 */
/** Human-readable candidate kind label */
function kindLabel(k) {
  const m = { factor: '因子战法', pattern: '形态战法', weights: '权重优化', depth: '盘口扫描' }
  return m[k] || k
}

// 因子元数据缓存（从后端 /api/research/factors 拉取，ID → { name, cat, desc }）
// Factor metadata cache, fetched from /api/research/factors (ID → { name, cat, desc })
const factorMeta = {}

/** 拉取并缓存因子元数据（失败静默降级，仍可用 ID 兜底显示） */
/** Fetch and cache factor metadata; silently degrade to ID on failure */
async function loadFactorMeta() {
  try {
    const res = await api.fetchResearchFactors()
    if (res && Array.isArray(res.factors)) {
      for (const f of res.factors) {
        if (f && f.id) factorMeta[f.id] = f
      }
    }
  } catch (_) {
    // 后端未提供时保留空映射，前端以 ID 兜底
  }
}

/** 因子 ID → 中文展示名（优先后端元数据，缺失回退 ID） */
/** Factor ID → display name (backend metadata first, fallback to ID) */
function factorName(id) {
  const m = factorMeta[id]
  return (m && m.name) ? m.name : id
}

/** 取因子方向（factor 候选的 weights 是复合结构 {"directions":{...},"weights":{...}}） */
/** Read factor directions from a factor candidate's composite weights */
function factorDirs(c) {
  try {
    const w = JSON.parse(c.weights || '{}')
    if (w && typeof w === 'object' && w.directions && typeof w.directions === 'object') {
      return w.directions
    }
  } catch (_) {}
  return {}
}

/** 因子规则：合并方向 + 权重 + 中文名，按权重降序 */
/** Factor rule rows: direction + weight + Chinese name, sorted by weight desc */
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
    .map(([id, weight]) => ({
      id,
      label: factorName(id),
      weight,
      dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1,
    }))
    .sort((a, b) => b.weight - a.weight)
}

/** 从 reason 字符串提取数值（样本内IR / 样本外IR / 反推超额） */
/** Extract a numeric value from the reason string */
function parseReason(c, key) {
  const reason = c.reason || ''
  const pats = {
    '样本内IR': /样本内IR=(-?\d+\.?\d*)/,
    '样本外IR': /样本外IR=(-?\d+\.?\d*)/,
    '反推超额': /反推超额=(-?\d+\.?\d*)/,
  }
  const m = reason.match(pats[key])
  return m ? parseFloat(m[1]) : null
}

/** 格式化百分数（反推超额是小数，如 0.1637 → +16.4%） */
/** Format a ratio as a percentage (0.1637 → +16.4%) */
function fmtPct(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  const s = (v * 100).toFixed(1)
  return (v >= 0 ? '+' : '') + s + '%'
}

/** 总体结论：这条规律能不能用（大白话） */
/** Overall verdict: should this rule be used (plain words) */
function verdict(c) {
  const insample = parseReason(c, '样本内IR')
  const outsample = parseReason(c, '样本外IR')
  const gen = parseReason(c, '反推超额')
  const thr = 0.3 // 与 research --min-ir 默认一致
  const passed = (insample !== null ? insample >= thr : true) &&
                 (outsample !== null ? outsample >= thr : true) &&
                 (gen !== null ? gen > 0 : true)
  if (passed) {
    return { ok: true, text: '电脑用两段互不相干的历史行情分别验证过，这条规律都能跑赢，不是碰运气。' }
  }
  return { ok: false, text: '这条规律在验证中没站稳，不建议直接拿来实盘。' }
}

/** 三条大白话验证结论 */
/** Three plain-language verification conclusions */
function plainLines(c) {
  const insample = parseReason(c, '样本内IR')
  const outsample = parseReason(c, '样本外IR')
  const gen = parseReason(c, '反推超额')
  const lines = []
  if (insample !== null) {
    lines.push('先拿前半段历史行情回放：这套打分的选股效果明显（稳定度 ' + fmt(insample) + '，越高越靠谱）。')
  }
  if (outsample !== null) {
    lines.push('再拿一段完全没参与挑规律的行情回放：效果仍然明显（稳定度 ' + fmt(outsample) + '）。这一步是防止规律只对老数据灵、换市场就失灵。')
  }
  if (gen !== null) {
    lines.push('最后对比「按这套规律选出的股票」和「随便买」：选的比平均多赚 ' + fmtPct(gen) + '，说明规律确实挑得出好股票。')
  }
  if (lines.length === 0) {
    lines.push(/通过护栏/.test(c.reason || '') ? '电脑验证通过，这条规律可以试试。' : '这条规律未通过验证。')
  }
  return lines
}

/** 回测是否真的做过（avg_excess=0 且 kind=factor 默认未做全链路回测） */
/** Whether a full backtest was actually run (factor candidates default to none) */
function btTested(c) {
  return c.avg_excess !== 0
}

/** 从后端加载候选列表 */
/** Load the candidate list from the backend */
async function loadData() {
  loading.value = true
  try {
    const res = await api.fetchResearchCandidates(statusFilter.value || '')
    if (res && Array.isArray(res.candidates)) {
      candidates.value = res.candidates
      noDB.value = false
    }
  } catch (e) {
    if (e && e.message && e.message.indexOf('研究库未接入') >= 0) {
      noDB.value = true
      candidates.value = []
    } else {
      console.error('Research 加载失败', e)
    }
  } finally {
    loading.value = false
  }
}

/** 审批通过并应用候选 */
/** Approve a candidate and apply its weights */
async function doApprove(c) {
  try {
    await api.approveResearchCandidate(c.id)
    c.status = 'applied'
    alert('候选 #' + c.id + ' 已审批并应用')
  } catch (e) {
    alert('审批失败: ' + (e.message || e))
  }
}

/** 驳回候选 */
/** Reject a candidate */
async function doReject(c) {
  try {
    await api.rejectResearchCandidate(c.id)
    c.status = 'rejected'
    alert('候选 #' + c.id + ' 已驳回')
  } catch (e) {
    alert('驳回失败: ' + (e.message || e))
  }
}

// ── 战法库（已应用因子战法管理 + 效果监测）──
// Strategy library: management + effectiveness monitoring

/** 解析战法库条目的因子规则（方向 + 中文名） */
/** Parse a library entry's factor rules (direction + Chinese name) */
function ruleFactors(s) {
  const dirs = s.directions || {}
  const wm = s.weights || {}
  return Object.entries(wm)
    .filter(([, v]) => typeof v === 'number' && isFinite(v))
    .map(([id, weight]) => ({
      id,
      label: factorName(id),
      weight,
      dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1,
    }))
    .sort((a, b) => b.weight - a.weight)
}

/** 形态条件显示文案：因子 ∈ [min, max) */
/** Pattern condition label: factor ∈ [min, max) */
function condLabel(c) {
  const name = factorName(c.factor || '')
  const min = (c.min !== undefined && c.min !== null) ? c.min : '-∞'
  const max = (c.max !== undefined && c.max !== null) ? c.max : '+∞'
  return name + ' ∈ [' + min + ', ' + max + ')'
}

/** 加载战法库 */
/** Load the strategy library */
async function loadLibrary() {
  loadingLibrary.value = true
  try {
    const res = await api.fetchResearchLibrary()
    if (res && Array.isArray(res.library)) {
      library.value = res.library
    }
  } catch (e) {
    console.error('战法库加载失败', e)
  } finally {
    loadingLibrary.value = false
  }
}

/** 启用/禁用战法库某条 */
/** Enable/disable a library strategy */
async function toggleLibrary(s) {
  try {
    await api.setResearchLibraryEnabled(s.id, !s.enabled)
    s.enabled = !s.enabled
    alert('战法 ' + s.name + (s.enabled ? ' 已启用（已注入 8a/8b 实盘）' : ' 已停用'))
  } catch (e) {
    alert('操作失败: ' + (e.message || e))
  }
}

/** 删除战法库某条 */
/** Delete a library strategy */
async function removeLibrary(s) {
  if (!confirm('确定删除战法 ' + s.name + ' ？删除后不再注入 8a/8b 实盘。')) return
  try {
    await api.deleteResearchLibrary(s.id)
    library.value = library.value.filter(x => x.id !== s.id)
    alert('战法 ' + s.name + ' 已删除')
  } catch (e) {
    alert('删除失败: ' + (e.message || e))
  }
}

/** 开始改名：进入编辑态 */
/** Start rename: enter edit mode */
function startRename(s) {
  editingName.value = { ...editingName.value, [s.id]: true }
  nameDraft.value = { ...nameDraft.value, [s.id]: s.name }
}

/** 保存改名 */
/** Save the rename */
async function saveName(s) {
  const name = (nameDraft.value[s.id] || '').trim()
  editingName.value = { ...editingName.value, [s.id]: false }
  if (!name || name === s.name) return
  try {
    await api.renameResearchLibrary(s.id, name)
    s.name = name
    alert('战法已重命名为 ' + name)
  } catch (e) {
    alert('改名失败: ' + (e.message || e))
  }
}

/** 加载全量回测全局开关 */
/** Load the global full-backtest toggle */
async function loadBacktestToggle() {
  try {
    const res = await api.fetchBacktestToggle()
    if (res && typeof res.enabled === 'boolean') backtestEnabled.value = res.enabled
  } catch (e) {
    console.error('加载回测开关失败', e)
  }
}

/** 保存全量回测全局开关 */
/** Save the global full-backtest toggle */
async function saveBacktestToggle() {
  try {
    await api.setBacktestToggle(backtestEnabled.value)
    alert('全量回测全局开关已' + (backtestEnabled.value ? '开启' : '关闭'))
  } catch (e) {
    alert('保存失败: ' + (e.message || e))
    backtestEnabled.value = !backtestEnabled.value
  }
}

// ── 单条候选全量回测（异步）──
// Per-candidate full backtest (async)

/** 对指定候选触发全量回测并轮询进度 */
/** Trigger a full backtest on a candidate and poll its progress */
async function doBacktest(c) {
  backtestLoading.value = { ...backtestLoading.value, [c.id]: true }
  backtestState.value = { ...backtestState.value, [c.id]: 'running' }
  try {
    await api.backtestResearchCandidate(c.id)
  } catch (e) {
    backtestLoading.value = { ...backtestLoading.value, [c.id]: false }
    backtestState.value = { ...backtestState.value, [c.id]: 'error' }
    alert('回测启动失败: ' + (e.message || e))
    return
  }
  // 轮询任务状态（全量回测可能耗时较长）
  const poll = setInterval(async () => {
    try {
      const j = await api.fetchBacktestStatus(c.id)
      if (j.status === 'done') {
        clearInterval(poll)
        backtestLoading.value = { ...backtestLoading.value, [c.id]: false }
        backtestState.value = { ...backtestState.value, [c.id]: 'done' }
        backtestResult.value = { ...backtestResult.value, [c.id]: j.avg_excess }
        c.avg_excess = j.avg_excess
        alert('候选 #' + c.id + ' 回测完成，回测超额 ' + (j.avg_excess !== undefined ? (j.avg_excess * 100).toFixed(2) + '%' : '0%'))
        loadLibrary()
      } else if (j.status === 'error') {
        clearInterval(poll)
        backtestLoading.value = { ...backtestLoading.value, [c.id]: false }
        backtestState.value = { ...backtestState.value, [c.id]: 'error' }
        alert('候选 #' + c.id + ' 回测失败: ' + (j.error || ''))
      }
    } catch (e) {
      // 轮询临时失败，继续
    }
  }, 5000)
}

// 挂载时加载一次；KeepAlive 缓存激活时刷新（切换 tab 回来自动同步最新候选）
// Load once on mount; refresh on KeepAlive reactivation so switching tabs syncs the latest candidates
onMounted(() => { loadFactorMeta(); loadAll(); loadLibrary(); loadBacktestToggle(); startPolling() })
onActivated(() => { loadAll(); loadLibrary(); loadBacktestToggle(); startPolling() })
onUnmounted(stopPolling)

// 定时轮询研究进度（30s）：dataload 期间进度条实时推进
// Poll the research progress every 30s so the data-loading progress bar stays fresh
let pollTimer = null
function startPolling() {
  if (pollTimer) return
  pollTimer = setInterval(loadProgress, 30000)
}
function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}
</script>

<style scoped>
.research-page { max-width: 960px; margin: 0 auto; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.page-header h2 { font-size: 18px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.status-filter {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none;
}
.btn-refresh { padding: 6px 14px; border-radius: 6px; border: 1px solid #FF4D4F; background: transparent; color: #FF4D4F; font-size: 14px; cursor: pointer; }
.btn-refresh:disabled { opacity: 0.5; }
.btn-refresh:hover { background: rgba(255,77,79,0.1); }

.empty { text-align: center; padding: 40px; color: #666; font-size: 14px; }

.progress-panel { background: #1a1a2e; border: 1px solid #2a2a3e; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; }
.progress-title { font-size: 13px; font-weight: 600; color: #e0e0e0; margin-bottom: 10px; }
.progress-grid { display: grid; grid-template-columns: 2fr 1fr 1fr 1fr; gap: 14px; }
.progress-item { display: flex; flex-direction: column; gap: 4px; }
.progress-label { font-size: 12px; color: #888; }
.progress-bar { height: 8px; border-radius: 4px; background: #2a2a3e; overflow: hidden; }
.progress-fill { height: 100%; border-radius: 4px; background: linear-gradient(90deg, #4caf50, #64b5f6); transition: width 0.6s ease; }
.progress-meta { font-size: 12px; color: #aaa; display: flex; flex-wrap: wrap; gap: 6px; }
.meta-chip { padding: 1px 8px; border-radius: 4px; background: #2a2a3e; color: #64b5f6; white-space: nowrap; }

@media (max-width: 768px) {
  .progress-grid { grid-template-columns: 1fr 1fr; }
}

/* 战法库（已应用因子战法） */
.library-panel { background: #16162a; border: 1px solid #2a2a3e; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; }
.library-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.library-title { font-size: 13px; font-weight: 600; color: #e0e0e0; }
.library-list { display: flex; flex-direction: column; gap: 10px; }
.library-card { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.lib-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.lib-name { font-size: 13px; font-weight: 600; color: #e0e0e0; }
.lib-id { font-size: 11px; color: #777; }
.lib-time { font-size: 11px; color: #777; margin-left: auto; }
.lib-factors { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
.lib-stats { display: flex; flex-wrap: wrap; gap: 14px; font-size: 12px; color: #aaa; margin-bottom: 8px; }
.lib-stats .stat b { font-weight: 600; }
.lib-stats .stat b.pos { color: #4caf50; }
.lib-stats .stat b.neg { color: #FF4D4F; }
.lib-actions { display: flex; gap: 10px; }
.btn-toggle { padding: 4px 12px; border-radius: 6px; border: 1px solid #64b5f6; background: rgba(100,181,246,0.12); color: #64b5f6; font-size: 12px; cursor: pointer; }
.btn-toggle:hover { background: rgba(100,181,246,0.22); }
.btn-backtest { padding: 4px 12px; border-radius: 6px; border: 1px solid #ff9800; background: rgba(255,152,0,0.12); color: #ff9800; font-size: 12px; cursor: pointer; }
.btn-backtest:hover { background: rgba(255,152,0,0.22); }
.btn-backtest:disabled { opacity: 0.5; cursor: wait; }
 .bt-result { font-size: 12px; font-weight: 600; align-self: center; }
 .bt-result.pos { color: #4caf50; }
 .bt-result.neg { color: #FF4D4F; }

/* 子页 tab */
.research-tabs { display: flex; gap: 6px; margin-bottom: 14px; border-bottom: 1px solid #2a2a3e; padding-bottom: 8px; }
.research-tabs .tab { padding: 6px 16px; border-radius: 6px 6px 0 0; border: 1px solid transparent; background: transparent; color: #999; font-size: 14px; cursor: pointer; }
.research-tabs .tab.active { background: #1a1a2e; border-color: #2a2a3e; border-bottom-color: #64b5f6; color: #64b5f6; font-weight: 600; }

/* 战法改名 */
.btn-rename { padding: 2px 8px; border-radius: 4px; border: 1px solid #2a2a3e; background: transparent; color: #64b5f6; font-size: 11px; cursor: pointer; margin-left: 6px; }
.btn-rename:hover { background: rgba(100,181,246,0.15); }
.name-input { padding: 2px 6px; border-radius: 4px; border: 1px solid #64b5f6; background: #0f0f23; color: #e0e0e0; font-size: 13px; width: 160px; }

/* 设置页 */
.settings-panel { background: #16162a; border: 1px solid #2a2a3e; border-radius: 8px; padding: 14px; }
.settings-title { font-size: 14px; font-weight: 600; color: #e0e0e0; margin-bottom: 14px; }
.setting-row { display: flex; align-items: center; gap: 14px; padding: 10px 0; border-bottom: 1px solid #22223a; }
.setting-info { flex: 1; }
.setting-label { font-size: 13px; font-weight: 600; color: #e0e0e0; }
.setting-desc { font-size: 12px; color: #888; margin-top: 4px; line-height: 1.5; }
.setting-state { font-size: 12px; color: #aaa; white-space: nowrap; }
.setting-hint { font-size: 11px; color: #777; margin-top: 10px; }
/* 开关样式 */
.switch { position: relative; display: inline-block; width: 44px; height: 22px; flex-shrink: 0; }
.switch input { opacity: 0; width: 0; height: 0; }
.switch .slider { position: absolute; cursor: pointer; inset: 0; background: #333; border-radius: 22px; transition: 0.3s; }
.switch .slider:before { content: ""; position: absolute; height: 16px; width: 16px; left: 3px; bottom: 3px; background: #888; border-radius: 50%; transition: 0.3s; }
.switch input:checked + .slider { background: #4caf50; }
.switch input:checked + .slider:before { transform: translateX(22px); background: #fff; }

.candidate-list { display: flex; flex-direction: column; gap: 10px; }
.candidate-card { background: #1a1a2e; border-radius: 8px; padding: 12px 14px; border: 1px solid #2a2a3e; }
.cand-header { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.cand-id { font-size: 14px; font-weight: 600; color: #e0e0e0; }
.cand-time { font-size: 12px; color: #888; margin-left: auto; }

.tag { font-size: 12px; padding: 1px 8px; border-radius: 4px; white-space: nowrap; }
.status-proposed { background: rgba(255,152,0,0.15); color: #ff9800; }
.status-approved { background: rgba(100,181,246,0.12); color: #64b5f6; }
.status-applied { background: rgba(76,175,80,0.15); color: #4caf50; }
.status-rejected { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag-kind { margin-left: 4px; }
.kind-factor { background: rgba(100,181,246,0.12); color: #64b5f6; }
.kind-pattern { background: rgba(186,104,200,0.12); color: #ba68c8; }
.kind-weights { background: rgba(76,175,80,0.12); color: #4caf50; }
.kind-depth { background: rgba(255,152,0,0.12); color: #ff9800; }

/* 因子战法候选的可解释展示 */
.block-title { font-size: 12px; font-weight: 600; color: #aaa; margin: 10px 0 6px; }
.factors-row { display: flex; flex-wrap: wrap; gap: 8px; }
.factor-chip {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; padding: 4px 10px; border-radius: 6px; background: #16162a; border: 1px solid #2a2a3e;
}
.cond-chip {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; padding: 4px 10px; border-radius: 6px; background: #16162a; border: 1px solid #2a2a3e; color: #ba68c8;
}
.dir-badge { font-size: 11px; padding: 1px 6px; border-radius: 4px; font-weight: 600; }
.dir-badge.long { background: rgba(76,175,80,0.18); color: #4caf50; }
.dir-badge.short { background: rgba(255,77,79,0.18); color: #FF4D4F; }
.factor-name { color: #e0e0e0; font-weight: 600; }
.factor-id { color: #777; font-size: 11px; }
.factor-weight { color: #64b5f6; }
.factor-desc { font-size: 12px; color: #999; margin-top: 8px; line-height: 1.6; }

/* 大白话验证结论 */
.verify-plain { display: flex; flex-direction: column; gap: 8px; }
.plain-summary {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 12px; border-radius: 8px; background: #14142a;
}
.plain-badge { font-size: 13px; font-weight: 700; padding: 3px 10px; border-radius: 6px; white-space: nowrap; }
.plain-badge.good { background: rgba(76,175,80,0.2); color: #4caf50; }
.plain-badge.bad { background: rgba(255,77,79,0.2); color: #FF4D4F; }
.plain-text { font-size: 12px; color: #ccc; line-height: 1.6; }
.plain-line { display: flex; gap: 8px; font-size: 12px; color: #bbb; line-height: 1.7; }
.plain-num { color: #64b5f6; font-weight: 600; flex-shrink: 0; }
.plain-body { flex: 1; }

/* 想细看的数字，折叠展示 */
.detail-block { margin-top: 10px; font-size: 12px; }
.detail-block summary { cursor: pointer; color: #64b5f6; font-size: 12px; }
.detail-block summary:hover { text-decoration: underline; }
.detail-row { display: flex; justify-content: space-between; gap: 12px; padding: 4px 2px; color: #999; }
.detail-row .d-label { color: #aaa; }
.detail-row .d-value { color: #bbb; font-variant-numeric: tabular-nums; }

.metric-row { display: flex; gap: 24px; flex-wrap: wrap; margin-bottom: 10px; }
.metric { display: flex; flex-direction: column; gap: 2px; }
.metric-label { font-size: 12px; color: #888; }
.metric-value { font-size: 16px; font-weight: 600; }
.metric-value.pos { color: #4caf50; }
.metric-value.neg { color: #FF4D4F; }

.weights-row { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
.weight-chip { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; padding: 2px 8px; border-radius: 4px; background: #2a2a3e; color: #aaa; }
.weight-fid { color: #64b5f6; }
.weight-val { color: #e0e0e0; font-weight: 600; }

.reason-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; font-size: 13px; }
.reason-label { color: #888; }
.reason-text { color: #aaa; }

.depth-block { display: flex; flex-direction: column; gap: 8px; margin-bottom: 8px; }
.depth-stock { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; font-size: 12px; padding: 6px 8px; border-radius: 6px; background: #16162a; }
.depth-code { font-weight: 600; color: #e0e0e0; }
.depth-touch { color: #888; }
.order-chip { padding: 1px 8px; border-radius: 4px; white-space: nowrap; }
.order-chip.support { background: rgba(76,175,80,0.15); color: #4caf50; }
.order-chip.resistance { background: rgba(255,77,79,0.15); color: #FF4D4F; }

.cand-actions { display: flex; gap: 10px; }
.no-perm { font-size: 12px; color: #888; padding: 4px 0; }
.btn-approve { padding: 5px 14px; border-radius: 6px; border: 1px solid #4caf50; background: rgba(76,175,80,0.15); color: #4caf50; font-size: 13px; cursor: pointer; }
.btn-approve:hover { background: rgba(76,175,80,0.25); }
.btn-reject { padding: 5px 14px; border-radius: 6px; border: 1px solid #FF4D4F; background: rgba(255,77,79,0.1); color: #FF4D4F; font-size: 13px; cursor: pointer; }
.btn-reject:hover { background: rgba(255,77,79,0.2); }

@media (max-width: 768px) {
  .page-header { flex-wrap: wrap; gap: 8px; }
  .metric-row { gap: 16px; }
}
</style>