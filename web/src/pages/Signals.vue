<!--
  策略信号页面 Signals.vue
  展示所有策略评级信号，支持按等级筛选、查看 D1-D4 子维度评分、确认买入/忽略操作
-->
<template>
  <div class="signals-page">
    <!-- 页头：标题 + 等级筛选按钮 -->
    <div class="page-header">
      <h2>策略信号</h2>
      <div class="filter-row">
        <button v-for="f in filters" :key="f.key"
          :class="['filter-btn', activeFilter === f.key ? 'active' : '']"
          @click="activeFilter = f.key">
          {{ f.label }}
        </button>
        <!-- 策略名称筛选：与"策略"列同名，按信号所属策略名称过滤 -->
        <select v-model="activeStrategy" class="strategy-select" title="按策略名称筛选">
          <option value="all">全部策略</option>
          <option v-for="st in strategyOptions" :key="st" :value="st">
            {{ st }}
          </option>
        </select>
        <button class="btn-log" @click="showLog = true">📋 日志</button>
      </div>
    </div>
    <LogModal :visible="showLog" @close="showLog = false" />

    <!-- 信号列表表格 -->
    <div class="signals-table">
      <div class="table-header">
        <span class="col-code">代码</span>
        <span class="col-name">名称</span>
        <span class="col-strategy">策略</span>
        <span class="col-score">总分</span>
        <span class="col-level">等级</span>
        <span class="col-detail">D1/D2/D3/D4</span>
      </div>
      <!-- 信号行：代码/名称/策略/总分/等级徽标/D1-D4 维度评分（含简短描述）+ 操作（买入或忽略） -->
      <div v-for="s in filteredSignals" :key="s.code" class="table-row">
        <span class="col-code">{{ s.code }}</span>
        <span class="col-name">{{ s.name || '-' }}</span>
        <span class="col-strategy">{{ s.strategy }}</span>
        <span class="col-score">{{ s.total_score?.toFixed(0) }}</span>
        <span class="col-level">
          <span :class="['tag', s.remind_level]">
            {{ s.level === '交易' ? '交易' : s.level === '观望' ? '观望' : s.remind_level === 'strong' ? '可开仓' : s.remind_level === 'observe' ? '观察' : '静默' }}
          </span>
        </span>
        <span class="col-detail">
          <span class="d-pill d1" :title="'D1: ' + (s.d1_desc || '')">
            {{ (s.d1 || 0).toFixed(0) }}<em v-if="s.d1_desc">{{ shortDesc(s.d1_desc) }}</em>
          </span>
          <span class="d-pill d2" :title="'D2: ' + (s.d2_desc || '')">
            {{ (s.d2 || 0).toFixed(0) }}<em v-if="s.d2_desc">{{ shortDesc(s.d2_desc) }}</em>
          </span>
          <span class="d-pill d3" :title="'D3: ' + (s.d3_desc || '')">
            {{ (s.d3 || 0).toFixed(0) }}<em v-if="s.d3_desc">{{ shortDesc(s.d3_desc) }}</em>
          </span>
          <span class="d-pill d4" :title="'D4: ' + (s.d4_desc || '')">
            {{ (s.d4 || 0).toFixed(0) }}<em v-if="s.d4_desc">{{ shortDesc(s.d4_desc) }}</em>
          </span>
        </span>
        <!-- 操作列：可开仓时显示"买入"按钮；已确认买入的显示"忽略"按钮；其余显示占位符 -->
        <span class="col-action">
          <button v-if="s.can_open" class="btn-buy" @click="confirmTrade(s, 'buy')">买入</button>
          <button v-else-if="s.action === 'buy'" class="btn-ignore" @click="confirmTrade(s, 'ignore')">忽略</button>
          <span v-else class="text-muted">—</span>
        </span>
      </div>
      <div class="empty" v-if="filteredSignals.length === 0">暂无信号</div>
    </div>

    <!-- 交易确认弹窗 -->
    <div class="modal-overlay" v-if="showConfirm">
      <div class="modal">
        <h3>确认交易</h3>
        <div class="modal-body">
          <p><strong>{{ tradeTarget.code }}</strong> {{ tradeTarget.name }}</p>
          <p>策略: {{ tradeTarget.strategy }}</p>
          <p>总分: {{ tradeTarget.total_score?.toFixed(0) }}</p>
          <p>价格: {{ tradeTarget.price ? '¥' + tradeTarget.price.toFixed(2) : '—' }}</p>
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showConfirm = false">取消</button>
          <button v-if="tradeAction === 'buy'" class="btn-buy" @click="doAction('buy')">确认买入</button>
          <button v-if="tradeAction === 'ignore'" class="btn-ignore" @click="doAction('ignore')">忽略</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue' // Vue 组合式 API：响应式、计算属性与生命周期钩子
import * as api from '../api/index.js'                      // 后端 API 调用封装（信号列表、操作、SSE 等）
import LogModal from '../components/LogModal.vue'            // 日志弹窗（LLM 批次 + 信号批次）

// ── 响应式状态 ──
const signals = ref([])               // 原始信号列表
const activeFilter = ref('all')       // 当前筛选等级
const activeStrategy = ref('all')     // 当前筛选战法
const showConfirm = ref(false)        // 是否显示交易确认弹窗
const showLog = ref(false)            // 是否打开日志弹窗
const tradeTarget = ref({})           // 待操作的信号对象
const tradeAction = ref('')           // 操作类型：'buy' | 'ignore'

let timer = null        // 3 秒轮询定时器句柄
let unsubSSE = null     // SSE 订阅解绑函数（卸载时调用以取消订阅）

// 等级筛选选项
const filters = [
  { key: 'all', label: '全部' },
  { key: 'strong', label: '可开仓' },
  { key: 'observe', label: '观察' },
  { key: 'mute', label: '静默' },
]

// 策略名称筛选：动态收集当前信号中的全部策略名称（与"策略"列同名，保证精确匹配）
const strategyOptions = computed(() => {
  const set = new Set()
  signals.value.forEach(s => { if (s.strategy) set.add(s.strategy) })
  return [...set]
})

/** 根据 activeFilter 与 activeStrategy 双重过滤信号 */
const filteredSignals = computed(() => {
  let list = signals.value
  // 等级过滤
  if (activeFilter.value !== 'all') list = list.filter(s => s.remind_level === activeFilter.value)
  // 策略名称过滤
  if (activeStrategy.value !== 'all') list = list.filter(s => s.strategy === activeStrategy.value)
  return list
})

/**
 * 截断维度描述文本，取第一个逗号前或前 6 字符
 * @param {string} s - 原始描述
 * @returns {string}
 */
function shortDesc(s) {
  if (!s) return ''
  // 截取逗号前的摘要，无逗号则取前 6 字符
  const idx = s.indexOf(',')
  return idx > 0 ? s.slice(0, idx) : s.slice(0, 6)
}

/** 打开交易确认弹窗 */
function confirmTrade(s, action) {
  // 记录目标信号与操作类型并弹出确认框
  tradeTarget.value = s
  tradeAction.value = action
  showConfirm.value = true
}

/** 执行买入/忽略操作，完成后刷新列表 */
async function doAction(action) {
  try {
    // 调用后端接口执行买入/忽略操作
    const res = await api.actionSignal(tradeTarget.value.code, action)
    showConfirm.value = false
    // 操作成功后刷新信号列表
    await load()
  } catch (e) {
    showConfirm.value = false
    alert('操作失败: ' + e.message)
  }
}

/** 从 API 加载信号列表 */
async function load() {
  try { signals.value = await api.fetchSignals() } catch (_) {}
}

/** SSE 消息触发刷新（新信号或扫描完成） */
function handleSSE(msg) {
  // 仅新信号或扫描完成事件触发刷新
  if (msg.signal || msg.type === 'scan') load()
}

/** 挂载时首次加载，启动 3 秒轮询 + SSE */
onMounted(() => {
  load()
  // 每 3 秒轮询一次信号列表
  timer = setInterval(load, 3000)
  // 订阅后端 SSE 推送
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
})
/** 卸载时清理 */
onUnmounted(() => {
  // 清理定时器与 SSE 订阅
  if (timer) clearInterval(timer)
  if (unsubSSE) unsubSSE()
})
</script>

<style scoped>
.signals-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.filter-row { display: flex; gap: 8px; }
.filter-btn {
  padding: 6px 16px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #888; font-size: 13px; cursor: pointer;
}
.filter-btn.active { background: #FF4D4F; border-color: #FF4D4F; color: #fff; }
.strategy-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 13px; cursor: pointer; outline: none;
}
.strategy-select:focus { border-color: #FF4D4F; }
.btn-log {
  padding: 6px 14px; border-radius: 6px; border: 1px solid #b388ff;
  background: transparent; color: #b388ff; font-size: 13px; cursor: pointer;
}
.btn-log:hover { background: rgba(179,136,255,0.1); }
.signals-table { background: #1a1a2e; border-radius: 8px; overflow: hidden; }
.table-header, .table-row {
  display: flex; align-items: center; padding: 10px 16px; gap: 8px; font-size: 13px;
}
.table-header { background: #2a2a3e; color: #888; font-weight: 600; }
.table-row { border-bottom: 1px solid #2a2a3e; }
.table-row:last-child { border-bottom: none; }
.col-code { width: 80px; font-family: monospace; color: #4fc3f7; }
.col-name { width: 100px; color: #e0e0e0; }
.col-strategy { width: 80px; color: #e0e0e0; }
.col-score { width: 60px; font-weight: 600; color: #FAAD14; text-align: center; }
.col-level { width: 70px; }
.col-detail { flex: 1; display: flex; gap: 6px; align-items: center; }
.col-action { width: 80px; text-align: center; }
.tag { font-size: 11px; padding: 2px 10px; border-radius: 10px; }
.tag.strong { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag.observe { background: rgba(250,173,20,0.15); color: #FAAD14; }
.tag.mute { background: rgba(153,153,153,0.15); color: #999; }
.d-pill {
  display: inline-flex; align-items: center; gap: 2px;
  font-size: 11px; padding: 0 5px; border-radius: 3px; white-space: nowrap;
}
.d-pill em { font-size: 10px; font-style: normal; opacity: 0.85; }
.d-pill.d1 { color: #FF4D4F; background: rgba(255,77,79,0.10); }
.d-pill.d2 { color: #FAAD14; background: rgba(250,173,20,0.10); }
.d-pill.d3 { color: #4fc3f7; background: rgba(79,195,247,0.10); }
.d-pill.d4 { color: #4caf50; background: rgba(76,175,80,0.10); }
.d-bar { flex: 1; height: 6px; background: #2a2a3e; border-radius: 3px; overflow: hidden; }
.d-fill { height: 100%; border-radius: 3px; }
.d-fill.d1 { background: #FF4D4F; }
.d-fill.d2 { background: #FAAD14; }
.d-fill.d3 { background: #4fc3f7; }
.d-fill.d4 { background: #4caf50; }
.btn-buy {
  padding: 4px 12px; border-radius: 4px; border: none;
  background: #FF4D4F; color: #fff; font-size: 12px; cursor: pointer;
}
.btn-ignore {
  padding: 4px 12px; border-radius: 4px; border: 1px solid #555;
  background: transparent; color: #888; font-size: 12px; cursor: pointer;
}
.text-muted { color: #555; font-size: 12px; }
.empty { text-align: center; padding: 40px; color: #555; font-size: 13px; }

.modal-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,0.6);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal { background: #1a1a2e; border-radius: 8px; padding: 24px; width: 360px; }
.modal h3 { font-size: 16px; margin-bottom: 16px; color: #e0e0e0; }
.modal-body p { font-size: 13px; color: #888; margin-bottom: 6px; }
.modal-body strong { color: #e0e0e0; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 20px; }
.btn-cancel {
  padding: 8px 16px; border-radius: 4px; border: 1px solid #333;
  background: transparent; color: #888; cursor: pointer; font-size: 13px;
}
</style>
