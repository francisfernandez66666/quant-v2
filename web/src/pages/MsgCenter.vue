<!--
  消息中心页面 MsgCenter.vue
  Message center page MsgCenter.vue
  展示所有提醒/告警消息，支持按等级过滤（命中提醒/策略信号/止盈止损/持仓提示）
  Shows all reminder/alert messages with level filtering (hit reminders / strategy signals / take-profit & stop-loss / holding tips)
-->
<template>
  <div class="msg-page">
    <!-- 页头：标题 + 等级筛选按钮 + 清空（Header: title + level filter + clear-all）-->
    <div class="page-header">
      <h2>消息中心</h2>
      <div class="header-actions">
        <button class="btn-clear" @click="onClearAll">清空全部</button>
      </div>
    </div>
    <!-- 消息等级筛选栏：全部 / 命中提醒 / 策略信号 / 止盈止损 / 持仓提示（Level filter bar: all / hit/strategy signal / take-profit & stop-loss / holding tip）-->
    <div class="filter-row">
      <button v-for="f in filters" :key="f.key"
        :class="['filter-btn', activeFilter === f.key ? 'active' : '']"
        @click="activeFilter = f.key">
        {{ f.label }}
      </button>
    </div>

    <!-- 消息列表（Message list）-->
    <div class="msg-list">
      <!-- 消息卡片：等级徽标/股票代码/时间/操作徽标/删除按钮 + 标题 + 正文，按等级显示左边框颜色（Message card: level badge/code/time/action badge/delete + title + body, with a level-based left border color）-->
      <div v-for="(a, i) in filteredAlerts" :key="a.id || i" :class="['msg-card', alertClass(a)]">
        <div class="msg-header">
          <!-- 消息等级徽标：止损/策略信号=红，止盈/加仓=绿，减仓=黄，其余=蓝（Level badge: stop-loss/strategy signal=red, take-profit/add=green, trim=yellow, others=blue）-->
          <span :class="['badge-level', levelClass(a.level)]">{{ a.level }}</span>
          <!-- 关联股票代码与名称（Related stock code and name）-->
          <span class="msg-stock">{{ a.code }} {{ a.name }}</span>
          <!-- 消息产生时间（Message timestamp）-->
          <span class="msg-time">{{ a.time }}</span>
          <!-- 操作徽标：依据标题内容推导买入/卖出/持有（Action badge: buy/sell/hold derived from the title）-->
          <span :class="['badge-action', actionClass(a)]">{{ actionText(a) }}</span>
          <!-- 单条删除按钮（Single-message delete button）-->
          <button class="btn-del" title="删除该消息" @click="onDeleteOne(a)">✕</button>
        </div>
        <!-- 消息标题（Message title）-->
        <div class="msg-title">{{ a.title }}</div>
        <!-- 消息正文（Message body）-->
        <div class="msg-body">{{ a.body }}</div>
      </div>
    </div>

    <div class="empty" v-if="filteredAlerts.length === 0">
      <span class="empty-text">暂无消息</span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'        // Vue 组合式 API：响应式 ref、计算属性
// Vue Composition API: reactive refs, computed properties
import { onMounted, onUnmounted } from 'vue'  // 挂载/卸载生命周期钩子
// mount/unmount lifecycle hooks
import * as api from '../api/index.js'       // 后端 API 封装：提醒消息增删查接口
// backend API wrapper: CRUD endpoints for alert messages

// ── 响应式状态 ──
// ── Reactive state ──
const alerts = ref([])               // 全部提醒消息
// all alert messages
const activeFilter = ref('all')      // 当前筛选类别
// the currently active filter
let timer = null                  // 定时轮询句柄
// polling timer handle
let unsubSSE = null               // SSE 取消订阅函数
// SSE unsubscribe function

/** SSE 触发刷新 */
/** Refresh triggered by an SSE push */
function handleSSE() { load() }

// 筛选选项
// Filter options
const filters = [
  { key: 'all', label: '全部' },
  { key: 'hit', label: '命中提醒' },
  { key: 'trade', label: '交易信号' },
  { key: 'strategy', label: '策略信号' },
  { key: 'stop', label: '止盈止损' },
  { key: 'hold', label: '持仓提示' },
]

/** 根据 activeFilter 过滤消息 */
/** Filter messages by activeFilter */
const filteredAlerts = computed(() => {
  if (activeFilter.value === 'all') return alerts.value
  if (activeFilter.value === 'hit') return alerts.value.filter(a => a.level === '命中提醒')
  if (activeFilter.value === 'trade') return alerts.value.filter(a => a.level === '交易信号')
  if (activeFilter.value === 'strategy') return alerts.value.filter(a => a.level === '策略信号')
  // 止盈止损合并过滤
  // Stop-loss and take-profit are filtered together
  if (activeFilter.value === 'stop') return alerts.value.filter(a => a.level === '止盈' || a.level === '止损')
  if (activeFilter.value === 'hold') return alerts.value.filter(a => a.level === '持仓提示')
  return alerts.value
})

/** 根据消息等级返回卡片边框颜色类 */
/** Return the card border color class based on the message level */
function alertClass(a) {
  if (a.level === '止损' || a.level === '策略信号') return 'alert-danger'
  if (a.level === '交易信号') {
    // 方向为做空/动作卖出时用红色警示，做多用绿色（买入）
    // Short direction or a sell action is flagged red; long is green (buy)
    return a.direction === '做空' || a.action === '卖出' ? 'alert-danger' : 'alert-success'
  }
  if (a.level === '止盈' || a.level === '加仓') return 'alert-success'
  if (a.level === '减仓') return 'alert-warn'
  return 'alert-info'
}

/** 根据等级返回徽标颜色类 */
/** Return the badge color class for a level */
function levelClass(level) {
  if (level === '止损' || level === '策略信号') return 'level-danger'
  if (level === '交易信号') return 'level-success'
  if (level === '止盈' || level === '加仓') return 'level-success'
  if (level === '减仓') return 'level-warn'
  return 'level-info'
}

/** 根据消息内容推导操作文本：买入 / 卖出 / 持有 */
/** Derive the action text (buy / sell / hold) from the message content */
function actionText(a) {
  if (a.level === '交易信号' || a.level === '策略信号') {
    return (a.action === '卖出') ? '卖出' : '买入'
  }
  return a.title?.includes('卖出') ? '卖出' : a.title?.includes('买入') ? '买入' : '持有'
}

/** 根据操作文本返回操作徽标颜色类 */
/** Return the action badge color class from the action text */
function actionClass(a) {
  const t = actionText(a)
  if (t === '买入') return 'action-buy'
  if (t === '卖出') return 'action-sell'
  return 'action-hold'
}

/** 从 API 加载所有提醒，过滤掉日历类消息 */
/** Load all alerts from the API, filtering out calendar-type messages */
async function load() {
  try {
    // 拉取全部提醒并过滤日历类消息
    // Fetch all alerts and drop calendar-type messages
    const all = await api.fetchAlerts()
    alerts.value = (all || []).filter(a => a.code !== 'CAL' && !a.level?.startsWith('日历'))
  } catch (_) {}
}

/** 手工删除单条消息 */
/** Manually delete a single message */
async function onDeleteOne(a) {
  // 先弹出确认框再执行删除
  // Confirm with a dialog before deleting
  if (!confirm(`删除该消息？\n${a.title || ''}`)) return
  try {
    await api.deleteAlert(a.id)
    load()
  } catch (_) {}
}

/** 清空全部消息 */
/** Clear all messages */
async function onClearAll() {
  if (!confirm('确定清空全部消息？(当日已删除的将不再自动出现)')) return
  try {
    // 调用后端清空接口并重新加载
    // Call the backend clear endpoint and reload
    await api.clearAlerts()
    load()
  } catch (_) {}
}

onMounted(() => {
  // 首次加载并启动 15s 轮询
  // Initial load, then poll every 15s
  load()
  timer = setInterval(load, 15000)
  // 订阅后端 SSE 推送，收到事件即刷新
  // Subscribe to backend SSE pushes; refresh whenever an event arrives
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
})
onUnmounted(() => {
  // 卸载时清理定时器与 SSE 订阅
  // Clean up the timer and SSE subscription on unmount
  if (timer) clearInterval(timer)
  if (unsubSSE) { unsubSSE(); unsubSSE = null }
})
</script>

<style scoped>
.msg-page { max-width: 900px; }
.page-header { margin-bottom: 16px; }
.page-header h2 { font-size: 16px; color: #e0e0e0; margin-bottom: 12px; }
.header-actions { display: flex; align-items: center; }
.filter-row { display: flex; gap: 8px; margin-bottom: 14px; }
.btn-clear {
  padding: 6px 16px; border-radius: 6px; border: 1px solid #FF4D4F;
  background: transparent; color: #FF4D4F; font-size: 12px; cursor: pointer;
}
.btn-clear:hover { background: rgba(255,77,79,0.1); }
.btn-del {
  margin-left: auto; width: 20px; height: 20px; border-radius: 4px;
  border: none; background: transparent; color: #666; font-size: 12px;
  line-height: 1; cursor: pointer; flex-shrink: 0;
}
.btn-del:hover { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.filter-btn {
  padding: 6px 16px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #999; font-size: 12px; cursor: pointer;
}
.filter-btn.active { background: #FF4D4F; border-color: #FF4D4F; color: #fff; }
.msg-list { display: flex; flex-direction: column; gap: 8px; }
.msg-card {
  background: #1a1a2e; border-radius: 8px; padding: 14px;
  border-left: 4px solid #333;
}
.msg-card.alert-danger { border-left-color: #FF4D4F; }
.msg-card.alert-success { border-left-color: #4caf50; }
.msg-card.alert-warn { border-left-color: #FAAD14; }
.msg-card.alert-info { border-left-color: #4fc3f7; }
.msg-header { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; flex-wrap: wrap; }
.badge-level { font-size: 11px; padding: 1px 8px; border-radius: 4px; font-weight: 600; }
.msg-stock { font-size: 12px; color: #4fc3f7; font-family: monospace; font-weight: 600; }
.level-danger { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.level-success { background: rgba(76,175,80,0.15); color: #4caf50; }
.level-warn { background: rgba(250,173,20,0.15); color: #FAAD14; }
.level-info { background: rgba(79,195,247,0.15); color: #4fc3f7; }
.msg-time { font-size: 11px; color: #555; flex: 1; }
.badge-action { font-size: 11px; padding: 1px 8px; border-radius: 4px; }
.action-buy { background: rgba(76,175,80,0.15); color: #4caf50; }
.action-sell { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.action-hold { background: rgba(153,153,153,0.15); color: #999; }
.msg-title { font-size: 13px; color: #e0e0e0; font-weight: 600; }
.msg-body { font-size: 12px; color: #999; margin-top: 4px; }
.msg-footer { display: flex; gap: 12px; margin-top: 8px; }
.msg-code { font-size: 11px; color: #4fc3f7; font-family: monospace; }
.msg-name { font-size: 11px; color: #888; }
.empty { padding: 60px; text-align: center; }
.empty-text { font-size: 14px; color: #555; }
</style>
