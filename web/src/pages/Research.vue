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

    <!-- 空态：库未接入 / 无候选 -->
    <div v-if="noDB" class="empty">研究库未接入（需后端开启 B5 研究闭环）</div>
    <div v-else-if="candidates.length === 0" class="empty">暂无候选，先在命令行跑 research optimize 产出</div>

    <!-- 候选卡片列表 -->
    <div v-else class="candidate-list">
      <div v-for="c in candidates" :key="c.id" class="candidate-card">
        <div class="cand-header">
          <span class="cand-id">#{{ c.id }}</span>
          <span :class="['tag', 'status-' + c.status]">{{ statusLabel(c.status) }}</span>
          <span class="cand-time">{{ c.created_at }}</span>
        </div>
        <!-- 指标行 -->
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
        <!-- 权重 -->
        <div class="weights-row" v-if="weightList(c).length">
          <span class="weight-chip" v-for="w in weightList(c)" :key="w[0]">
            <span class="weight-fid">{{ w[0] }}</span>
            <span class="weight-val">{{ w[1].toFixed(3) }}</span>
          </span>
        </div>
        <!-- 理由（护栏判定） -->
        <div class="reason-row" v-if="c.reason">
          <span class="reason-label">护栏</span>
          <span class="reason-text">{{ c.reason }}</span>
        </div>
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
        <!-- 操作按钮：有 research_approve 权限且待审批时可审批 / 驳回 -->
        <div class="cand-actions" v-if="canApprove && c.status === 'proposed'">
          <button class="btn-approve" @click="doApprove(c)">审批并应用</button>
          <button class="btn-reject" @click="doReject(c)">驳回</button>
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
    return Object.entries(w).sort((a, b) => b[1] - a[1])
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

// 挂载时加载一次；KeepAlive 缓存激活时刷新（切换 tab 回来自动同步最新候选）
// Load once on mount; refresh on KeepAlive reactivation so switching tabs syncs the latest candidates
onMounted(() => { loadAll(); startPolling() })
onActivated(() => { loadAll(); startPolling() })
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