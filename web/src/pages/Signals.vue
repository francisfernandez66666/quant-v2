<!--
  策略信号页面 Signals.vue
  Strategy signals page Signals.vue
  展示所有策略评级信号，支持按等级筛选、查看 D1-D4 子维度评分、确认买入/忽略操作
  Lists all strategy-rated signals with level filtering, D1-D4 sub-dimension scores, and confirm buy/ignore actions
-->
<template>
  <div class="signals-page">
    <!-- 页头：标题 + 等级筛选按钮（Header: title + level filter buttons）-->
    <div class="page-header">
      <h2>策略信号</h2>
      <div class="filter-row">
        <button v-for="f in filters" :key="f.key"
          :class="['filter-btn', activeFilter === f.key ? 'active' : '']"
          @click="activeFilter = f.key">
          {{ f.label }}
        </button>
        <!-- 策略名称筛选：与"策略"列同名，按信号所属策略名称过滤（Strategy-name filter: same names as the "strategy" column, filters by the signal's strategy）-->
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

    <!-- 信号列表表格（Signal table）-->
    <div class="signals-table">
      <div class="table-header">
        <span class="col-code">代码</span>
        <span class="col-name">名称</span>
        <span class="col-price">现价/涨跌</span>
        <span class="col-strategy">策略</span>
        <span class="col-score">总分</span>
        <span class="col-level">等级</span>
        <span class="col-detail">D1/D2/D3/D4</span>
        <span class="col-kline">分时</span>
        <span class="col-action">操作</span>
      </div>
      <!-- 信号行 + 可展开 分时区（Signal row + expandable K-line area）-->
      <div v-for="s in filteredSignals" :key="s.code" class="table-row-group">
      <div class="table-row" @click="onRowTap(s)">
        <span class="col-code" data-label="代码">{{ s.code }}</span>
        <span class="col-name" data-label="名称">{{ s.name || '-' }}</span>
        <span class="col-price" data-label="现价/涨跌">
          <span class="px-price">¥{{ (s.price || 0).toFixed(2) }}</span>
          <span :class="['px-chg', (s.change_pct || 0) >= 0 ? 'up' : 'down']">
            {{ (s.change_pct || 0) > 0 ? '+' : '' }}{{ (s.change_pct || 0).toFixed(2) }}%
          </span>
        </span>
        <span class="col-strategy" data-label="策略">{{ s.strategy }}</span>
        <span class="col-score" data-label="总分">{{ s.total_score?.toFixed(0) }}</span>
        <span class="col-level" data-label="等级">
          <span :class="['tag', s.remind_level]">
            {{ s.level === '交易' ? '交易' : s.level === '观望' ? '观望' : s.remind_level === 'strong' ? '可开仓' : s.remind_level === 'observe' ? '观察' : '静默' }}
          </span>
        </span>
        <span class="col-detail" data-label="D1/D2/D3/D4">
          <span class="d-pill d1"
                :title="'D1事件: ' + (s.d1_reason || s.d1_event || '无事件') + (s.d1_blocked ? '（负面拦截）' : '')">
            <em v-if="s.d1_score && (s.d1_reason || s.d1_event)">{{ d1Tag(s) }}</em>
            <span v-else class="d1-none">{{ s.d1 ? s.d1.toFixed(0) : '—' }}</span>
          </span>
          <span class="d-pill d2" :title="'D2: ' + (s.d2_desc || '')">
            {{ s.d2 ? s.d2.toFixed(0) : '—' }}<em v-if="s.d2_desc">{{ shortDesc(s.d2_desc) }}</em>
          </span>
          <span class="d-pill d3" :title="'D3: ' + (s.d3_desc || '')">
            {{ s.d3 ? s.d3.toFixed(0) : '—' }}<em v-if="s.d3_desc">{{ shortDesc(s.d3_desc) }}</em>
          </span>
          <span class="d-pill d4" :title="'D4: ' + (s.d4_desc || '')">
            {{ s.d4 ? s.d4.toFixed(0) : '—' }}<em v-if="s.d4_desc">{{ shortDesc(s.d4_desc) }}</em>
          </span>
        </span>
        <span class="col-kline" data-label="分时">
          <button class="btn-kline" @click.stop="toggleKline(s.code)" :title="klineOpen.has(s.code) ? '收起分时' : '展开分时'">{{ klineOpen.has(s.code) ? '收起' : '分时' }}</button>
        </span>
        <!-- 操作列：可开仓时显示"买入"按钮；已确认买入的显示"忽略"按钮；其余显示占位符；增加"收藏"按钮可一键添加到自选股（Action column: "buy" when tradeable; "ignore" after a confirmed buy; a placeholder otherwise; "collect" button to add to watchlist）-->
        <span class="col-action" data-label="操作">
          <button v-if="s.can_open" class="btn-buy" @click.stop="confirmTrade(s, 'buy')">买入</button>
          <button v-else-if="s.action === 'buy'" class="btn-ignore" @click.stop="confirmTrade(s, 'ignore')">忽略</button>
          <span v-else class="text-muted">—</span>
          <!-- 模拟盘买入：后端启用模拟盘时显示，按实时价自动撮合（Paper buy: shown when paper trading is enabled, fills at the live price）-->
          <button v-if="paperOn && s.can_open" class="btn-paper" @click.stop="paperBuy(s)">模拟买入</button>
          <!-- 收藏/加入自选股按钮：一键将信号股票代码加入自选股列表（Add to watchlist button: one-click add signal's code to watchlist）-->
          <button v-if="!s.can_open && s.action !== 'buy'" class="btn-collect" @click.stop="collectToWatchlist(s)">收藏</button>
        </span>
      </div>
      <!-- 展开的 分时区（全宽，位于该行下方）（Expanded K-line area, full width, below the row）-->
      <div v-if="klineOpen.has(s.code)" class="col-kline-row">
        <div class="kline-flex">
          <div class="kline-main"><KLineChart :code="s.code" :name="s.name" /></div>
          <div class="depth-side"><DepthPanel :code="s.code" :name="s.name" /></div>
        </div>
      </div>
      </div>
      <div class="empty" v-if="filteredSignals.length === 0">暂无信号</div>
    </div>

    <!-- 移动端：点击行弹出的底部操作菜单 -->
    <div class="sheet-overlay" v-if="sheetSignal" @click="sheetSignal = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetSignal.code }} {{ sheetSignal.name || '' }} · {{ sheetSignal.strategy }}</div>
        <button v-if="sheetSignal.can_open" class="sheet-btn sheet-danger" @click="sheetBuy">买入</button>
        <!-- 模拟盘买入（移动端菜单）：仅在模拟盘启用时显示，按实时价成交（mobile menu paper buy: only when paper trading is enabled, fills at live price）-->
        <button v-if="sheetSignal.can_open && paperOn" class="sheet-btn sheet-paper" @click="sheetPaperBuy">模拟买入</button>
        <button v-if="sheetSignal.action === 'buy'" class="sheet-btn" @click="sheetIgnore">忽略</button>
        <button v-if="!sheetSignal.can_open && sheetSignal.action !== 'buy'" class="sheet-btn" @click="sheetCollect">收藏</button>
        <button class="sheet-btn" @click="sheetKline">{{ klineOpen.has(sheetSignal.code) ? '收起分时' : '展开分时' }}</button>
        <button class="sheet-btn sheet-cancel" @click="sheetSignal = null">取消</button>
      </div>
    </div>

    <!-- 交易确认弹窗（Trade-confirm modal）-->
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
// Vue Composition API: reactive refs, computed properties, and lifecycle hooks
import * as api from '../api/index.js'                      // 后端 API 调用封装（信号列表、操作、SSE 等）
// backend API wrapper (signal list, actions, SSE etc.)
import LogModal from '../components/LogModal.vue'            // 日志弹窗（LLM 批次 + 信号批次）
// log modal (LLM batches + signal batches)
import KLineChart from '../components/KLineChart.vue'        // 分时图组件（展开行展示）
// K-line chart component (shown in expanded rows)
import DepthPanel from '../components/DepthPanel.vue'        // 盘口面板（展开行展示，买卖五档）
// order-book panel (shown in expanded rows, 5 bid/ask levels)

// ── 响应式状态 ──
// ── Reactive state ──
const signals = ref([])               // 原始信号列表
// raw signal list
const klineOpen = ref(new Set())      // 已展开分时的信号代码集合
// the set of signal codes whose K-line is expanded
const activeFilter = ref('all')       // 当前筛选等级
// the currently selected level filter
const activeStrategy = ref('all')     // 当前筛选战法
// the currently selected strategy filter
const showConfirm = ref(false)        // 是否显示交易确认弹窗
// whether the trade-confirm modal is visible
const showLog = ref(false)            // 是否打开日志弹窗
// whether the log modal is visible
const sheetSignal = ref(null)         // 移动端操作菜单当前选中的信号对象
// the signal object currently selected in the mobile action sheet
const paperOn = ref(false)            // 模拟盘开关（后端启用时显示"模拟买入"按钮）
// paper-trading switch (shows the "paper buy" button when enabled)
const tradeTarget = ref({})           // 待操作的信号对象
// the signal object pending an action
const tradeAction = ref('')           // 操作类型：'buy' | 'ignore'
// the action type: 'buy' | 'ignore'

let timer = null        // 5 秒轮询定时器句柄
let visHandler = null   // 页面可见性切换回调（暂停/恢复轮询）
// 3s polling timer handle
let unsubSSE = null     // SSE 订阅解绑函数（卸载时调用以取消订阅）
// SSE unsubscribe function (called on unmount to cancel the subscription)

// 等级筛选选项
// Level filter options
const filters = [
  { key: 'all', label: '全部' },
  { key: 'strong', label: '可开仓' },
  { key: 'observe', label: '观察' },
  { key: 'mute', label: '静默' },
]

// 策略名称筛选：动态收集当前信号中的全部策略名称（与"策略"列同名，保证精确匹配）
// Strategy filter: dynamically collect all strategy names present in the signals (same as the "strategy" column for exact matches)
const strategyOptions = computed(() => {
  const set = new Set()
  signals.value.forEach(s => { if (s.strategy) set.add(s.strategy) })
  return [...set]
})

/** 根据 activeFilter 与 activeStrategy 双重过滤信号 */
/** Filter signals by both activeFilter and activeStrategy */
const filteredSignals = computed(() => {
  let list = signals.value
  // 等级过滤
  // Level filter
  if (activeFilter.value !== 'all') list = list.filter(s => s.remind_level === activeFilter.value)
  // 策略名称过滤
  // Strategy-name filter
  if (activeStrategy.value !== 'all') list = list.filter(s => s.strategy === activeStrategy.value)
  // 默认排除“预期差”策略信号（非开仓提醒类），保持四大战法纯净
  // English: always filter out "Expectation Gap" strategy signals (non-trading reminder type)
  // to keep the four core strategies pristine
  list = list.filter(s => s.strategy !== '预期差')
  return list
})

/**
 * 截断维度描述文本，取第一个逗号前或前 6 字符
 * Truncate a dimension description to its first comma or the first 6 characters
 * @param {string} s - 原始描述
 * @param {string} s - the original description
 * @returns {string}
 */
function shortDesc(s) {
  if (!s) return ''
  // 截取逗号前的摘要，无逗号则取前 6 字符
  // Take the part before the comma, or the first 6 chars when there is no comma
  const idx = s.indexOf(',')
  return idx > 0 ? s.slice(0, idx) : s.slice(0, 6)
}

/** D1 事件列的徽标文字：优先事件标题，其次 LLM 理由，最多展示 8 字符，并附分数与拦截标记 */
/** D1-event pill label: prefers the event title, then the LLM reason, capped at 8 chars, with score/blocked flag */
function d1Tag(s) {
  const label = s.d1_event || s.d1_reason || ''
  const base = shortDesc(label) || '事件'
  // D1 分数为 0~40 满分制，直接显示（不再 ×100 当作 0~1 百分比）
  // English: the D1 score is already on the 0~40 scale, shown directly (no longer *100 as a 0~1 percent).
  const score = s.d1_score ? s.d1_score.toFixed(0) : ''
  const blocked = s.d1_blocked ? '·拦' : ''
  return [base, score, blocked].filter(Boolean).join('')
}

/** 打开交易确认弹窗 */
/** Open the trade-confirm modal */
function confirmTrade(s, action) {
  // 记录目标信号与操作类型并弹出确认框
  // Store the target signal and action type, then show the confirm modal
  tradeTarget.value = s
  tradeAction.value = action
  showConfirm.value = true
}

/** 执行买入/忽略操作，完成后刷新列表 */
/** Execute a buy/ignore action, then refresh the list */
async function doAction(action) {
  try {
    // 调用后端接口执行买入/忽略操作
    // Call the backend endpoint to execute the buy/ignore action
    const res = await api.actionSignal(tradeTarget.value.code, action)
    showConfirm.value = false
    // 操作成功后刷新信号列表
    // Refresh the signal list after a successful action
    await load()
  } catch (e) {
    showConfirm.value = false
    alert('操作失败: ' + e.message)
  }
}

/** 展开/收起某信号的 分时区 */
/** Toggle a signal's K-line area */
function toggleKline(code) {
  const next = new Set(klineOpen.value)
  if (next.has(code)) next.delete(code)
  else next.add(code)
  klineOpen.value = next
}

/** 移动端：点击行打开操作菜单 */
/** Mobile: tap a row to open the action sheet */
function onRowTap(s) {
  if (window.innerWidth > 768) return
  sheetSignal.value = s
}
/** 移动端：操作菜单 - 买入 */
function sheetBuy() {
  if (!sheetSignal.value) return
  const s = sheetSignal.value
  sheetSignal.value = null
  confirmTrade(s, 'buy')
}

/** 模拟盘买入：确认后按实时价自动撮合一笔虚拟持仓（paper buy: confirm then auto-fill a virtual position at the live price）*/
async function paperBuy(s) {
  if (!confirm(`确认模拟买入 ${s.code} ${s.name || ''}？将按实时价成交固定资金。`)) return
  try {
    await api.buyPaperPosition(s.code, s.name || '', s.strategy || '', s.price || 0)
    alert(`已模拟买入 ${s.code}`)
  } catch (e) {
    alert(e.message || '模拟买入失败')
  }
}

/** 移动端：操作菜单 - 模拟买入 */
function sheetPaperBuy() {
  if (!sheetSignal.value) return
  const s = sheetSignal.value
  sheetSignal.value = null
  paperBuy(s)
}
/** 移动端：操作菜单 - 忽略 */
function sheetIgnore() {
  if (!sheetSignal.value) return
  const s = sheetSignal.value
  sheetSignal.value = null
  confirmTrade(s, 'ignore')
}
/** 移动端：操作菜单 - 收藏 */
function sheetCollect() {
  if (!sheetSignal.value) return
  const s = sheetSignal.value
  sheetSignal.value = null
  collectToWatchlist(s)
}
/** 移动端：操作菜单 - 展开/收起分时 */
function sheetKline() {
  if (!sheetSignal.value) return
  toggleKline(sheetSignal.value.code)
  sheetSignal.value = null
}

/** 从 API 加载信号列表 */
/** Load the signal list from the API */

/** 一键收藏到自选股 */
/** Add to watchlist one-click */
async function collectToWatchlist(s) {
  try {
    // 调用后端接口将信号股票加入自选
    // Call the backend endpoint to add the signal's code to watchlist
    const res = await api.addWatchlist(s.code)
    // 显示反馈
    // Show feedback
    showFeedback('已收藏 ' + s.code, 'ok')
    // 刷新自观列表（如果有打开）
    // Refresh watchlist if open
  } catch (e) {
    showFeedback('收藏失败: ' + e.message, 'err')
  }
}
async function load() {
  try { signals.value = await api.fetchSignals() } catch (_) {}
}

/** 探测模拟盘开关：启用后显示"模拟买入"按钮（probe the paper switch once; enables the paper-buy button）*/
async function probePaper() {
  try { paperOn.value = !!(await api.fetchPaperState()).enabled } catch (_) {}
}

/** SSE 消息触发刷新（新信号或扫描完成） */
/** Refresh triggered by an SSE message (new signal or completed scan) */
function handleSSE(msg) {
  // 仅新信号或扫描完成事件触发刷新
  // Only new-signal or scan-complete events trigger a refresh
  if (msg.signal || msg.type === 'scan') load()
}

/** 挂载时首次加载，启动 5 秒轮询 + SSE；页面不可见时暂停轮询 */
/** On mount: initial load, start 5s polling + SSE; pause while the tab is hidden */
// 说明：轮询由 3s 降到 5s 并配合页面可见性暂停，降低对行情数据源的请求洪峰；
// 信号为后端统一计算结果，轮询仅补足 SSE 偶发丢失，跨设备结果一致。
// (Poll interval reduced from 3s to 5s plus visibility pause to cut quote-source load; the
// signals are the backend's single computed result, polling only backfills SSE drops.)
onMounted(() => {
  load()
  probePaper()
  // 每 5 秒轮询一次信号列表 (poll the signal list every 5 seconds)
  timer = setInterval(load, 5000)
  // 页面不可见时暂停轮询，切回时立即补拉一次 (pause polling when hidden; refresh on return)
  visHandler = () => {
    if (document.hidden) {
      if (timer) { clearInterval(timer); timer = null }
    } else {
      if (!timer) {
        load()
        timer = setInterval(load, 5000)
      }
    }
  }
  document.addEventListener('visibilitychange', visHandler)
  // 订阅后端 SSE 推送
  // Subscribe to backend SSE pushes
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
})
/** 卸载时清理 */
/** Clean up on unmount */
onUnmounted(() => {
  // 清理定时器与 SSE 订阅
  // Clear the timer and SSE subscription
  if (timer) clearInterval(timer)
  if (visHandler) document.removeEventListener('visibilitychange', visHandler)
  if (unsubSSE) unsubSSE()
})
</script>


/** 显示临时反馈文字，2.5 秒后消失 */
/** Show temporary feedback text that disappears after 2.5 seconds */
function showFeedback(msg, type) {
  const typeClass = type || 'ok'
  const div = document.createElement('div')
  div.className = 'toast ' + typeClass
  div.textContent = msg
  const container = document.querySelector('.signals-page')
  if (container) {
    container.insertBefore(div, container.firstChild)
  }
  setTimeout(() => { div.remove() }, 2500)
}
<style scoped>
.signals-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.filter-row { display: flex; gap: 8px; }
.filter-btn {
  padding: 6px 16px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #888; font-size: 14px; cursor: pointer;
}
.filter-btn.active { background: #FF4D4F; border-color: #FF4D4F; color: #fff; }
.strategy-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 14px; cursor: pointer; outline: none;
}
.strategy-select:focus { border-color: #FF4D4F; }
.btn-log {
  padding: 6px 14px; border-radius: 6px; border: 1px solid #b388ff;
  background: transparent; color: #b388ff; font-size: 14px; cursor: pointer;
}
.btn-log:hover { background: rgba(179,136,255,0.1); }
.signals-table { background: #1a1a2e; border-radius: 8px; overflow: hidden; }
.table-header, .table-row {
  display: flex; align-items: center; padding: 10px 16px; gap: 8px; font-size: 14px;
}
.table-row-group { border-bottom: 1px solid #2a2a3e; }
.table-header { background: #2a2a3e; color: #888; font-weight: 600; }
.table-row { border-bottom: none; }
.col-code { width: 80px; font-family: monospace; color: #4fc3f7; }
.col-name { width: 100px; color: #e0e0e0; }
.col-price { width: 130px; display: flex; flex-direction: column; gap: 2px; }
.px-price { color: #e0e0e0; font-weight: 600; }
.px-chg { font-size: 14px; }
.px-chg.up { color: #FF4D4F; }
.px-chg.down { color: #4caf50; }
.col-strategy { width: 80px; color: #e0e0e0; }
.col-score { width: 60px; font-weight: 600; color: #FAAD14; text-align: center; }
.col-level { width: 70px; }
.col-detail { flex: 1; display: flex; gap: 6px; align-items: center; }
.col-action { width: 80px; text-align: center; }
.col-kline { width: 60px; text-align: center; }
.btn-kline {
  background: transparent; border: 1px solid #3a3a55; color: #7ab8ff;
  border-radius: 4px; cursor: pointer; font-size: 14px; padding: 2px 8px;
}
.btn-kline:hover { border-color: #4fc3f7; color: #4fc3f7; }
.col-kline-row { padding: 8px 16px 12px; background: #16162a; }
.kline-flex { display: flex; gap: 12px; align-items: stretch; }
.kline-main { flex: 1 1 auto; min-width: 0; }
.depth-side { flex: 0 0 300px; }
@media (max-width: 720px) {
  .kline-flex { flex-direction: column; }
  .depth-side { flex: 1 1 auto; }
}
.tag { font-size: 14px; padding: 2px 10px; border-radius: 10px; }
.tag.strong { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag.observe { background: rgba(250,173,20,0.15); color: #FAAD14; }
.tag.mute { background: rgba(153,153,153,0.15); color: #999; }
.d-pill {
  display: inline-flex; align-items: center; gap: 2px;
  font-size: 14px; padding: 0 5px; border-radius: 3px; white-space: nowrap;
}
.d-pill em { font-size: 14px; font-style: normal; opacity: 0.85; }
.d-pill.d1 { color: #FF4D4F; background: rgba(255,77,79,0.10); }
.d-pill .d1-none { color: #8fa3bf; }
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
  background: #FF4D4F; color: #fff; font-size: 14px; cursor: pointer;
}
.btn-paper {
  padding: 4px 10px; border-radius: 4px; border: 1px solid rgba(82, 196, 26, 0.5);
  background: rgba(82, 196, 26, 0.12); color: #52c41a; font-size: 14px; cursor: pointer;
}
.btn-ignore {
  padding: 4px 12px; border-radius: 4px; border: 1px solid #555;
  background: transparent; color: #888; font-size: 14px; cursor: pointer;
}
.text-muted { color: #555; font-size: 14px; }
.empty { text-align: center; padding: 40px; color: #555; font-size: 14px; }

.modal-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,0.6);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal { background: #1a1a2e; border-radius: 8px; padding: 24px; width: 360px; }
.modal h3 { font-size: 16px; margin-bottom: 16px; color: #e0e0e0; }
.modal-body p { font-size: 14px; color: #888; margin-bottom: 6px; }
.modal-body strong { color: #e0e0e0; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 20px; }
.btn-cancel {
  padding: 8px 16px; border-radius: 4px; border: 1px solid #333;
  background: transparent; color: #888; cursor: pointer; font-size: 14px;
}

/* ====== Mobile: horizontal scroll + larger fonts ====== */
@media (max-width: 768px) {
  .signals-table { overflow-x: auto; -webkit-overflow-scrolling: touch; }
  .table-header, .table-row { min-width: 900px; font-size: 14px; padding: 10px 14px; gap: 8px; }
  .table-header { display: flex; }
  .page-header { flex-direction: column; align-items: stretch; gap: 8px; }
  .filter-row { flex-wrap: wrap; gap: 6px; }
  .filter-btn { padding: 6px 14px; font-size: 14px; }
  .strategy-select { flex: 1; min-width: 0; font-size: 14px; }
  .col-kline-row { padding: 6px; }
  .modal { width: 90%; max-width: 360px; }
  .table-row { cursor: pointer; }
  .sheet-overlay {
    position: fixed; inset: 0; z-index: 300; background: rgba(0,0,0,0.6);
    display: flex; align-items: flex-end;
  }
  .action-sheet {
    width: 100%; background: #1a1a2e; border-radius: 14px 14px 0 0;
    padding: 10px 12px calc(10px + env(safe-area-inset-bottom, 0px));
  }
  .sheet-title {
    font-size: 14px; color: #999; text-align: center;
    padding: 8px 0 12px; border-bottom: 1px solid #2a2a3e; margin-bottom: 8px;
  }
  .sheet-btn {
    width: 100%; padding: 14px; border-radius: 8px; border: none;
    background: #0f0f23; color: #4fc3f7; font-size: 16px; cursor: pointer;
    margin-bottom: 8px; text-align: center;
  }
  .sheet-btn:active { opacity: 0.8; }
  .sheet-danger { color: #FF4D4F; }
  .sheet-paper { color: #52c41a; }
  .sheet-cancel { background: #2a2a3e; color: #888; }
}
</style>
