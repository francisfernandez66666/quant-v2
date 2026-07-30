<!--
  消息中心页面 MsgCenter.vue
  展示所有提醒/告警消息，支持按等级过滤（命中提醒/策略信号/止盈止损/持仓提示）
-->
<template>
  <div class="msg-page">
    <!-- 页头：标题 + 等级筛选按钮 -->
    <div class="page-header">
      <h2>消息中心</h2>
      <div class="filter-row">
        <button v-for="f in filters" :key="f.key"
          :class="['filter-btn', activeFilter === f.key ? 'active' : '']"
          @click="activeFilter = f.key">
          {{ f.label }}
        </button>
      </div>
    </div>

    <!-- 消息列表 -->
    <div class="msg-list">
      <div v-for="(a, i) in filteredAlerts" :key="i" :class="['msg-card', alertClass(a)]">
        <div class="msg-header">
          <span :class="['badge-level', levelClass(a.level)]">{{ a.level }}</span>
          <span class="msg-stock">{{ a.code }} {{ a.name }}</span>
          <span class="msg-time">{{ a.time }}</span>
          <span :class="['badge-action', actionClass(a)]">{{ actionText(a) }}</span>
        </div>
        <div class="msg-title">{{ a.title }}</div>
        <div class="msg-body">{{ a.body }}</div>
      </div>
    </div>

    <div class="empty" v-if="filteredAlerts.length === 0">
      <span class="empty-text">暂无消息</span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'
import { onMounted, onUnmounted } from 'vue'
import * as api from '../api/index.js'

// ── 响应式状态 ──
const alerts = ref([])               // 全部提醒消息
const activeFilter = ref('all')      // 当前筛选类别
let timer = null
let unsubSSE = null

/** SSE 触发刷新 */
function handleSSE() { load() }

// 筛选选项
const filters = [
  { key: 'all', label: '全部' },
  { key: 'hit', label: '命中提醒' },
  { key: 'strategy', label: '策略信号' },
  { key: 'stop', label: '止盈止损' },
  { key: 'hold', label: '持仓提示' },
]

/** 根据 activeFilter 过滤消息 */
const filteredAlerts = computed(() => {
  if (activeFilter.value === 'all') return alerts.value
  if (activeFilter.value === 'hit') return alerts.value.filter(a => a.level === '命中提醒')
  if (activeFilter.value === 'strategy') return alerts.value.filter(a => a.level === '策略信号')
  if (activeFilter.value === 'stop') return alerts.value.filter(a => a.level === '止盈' || a.level === '止损')
  if (activeFilter.value === 'hold') return alerts.value.filter(a => a.level === '持仓提示')
  return alerts.value
})

/** 根据消息等级返回卡片边框颜色类 */
function alertClass(a) {
  if (a.level === '止损' || a.level === '策略信号') return 'alert-danger'
  if (a.level === '止盈' || a.level === '加仓') return 'alert-success'
  if (a.level === '减仓') return 'alert-warn'
  return 'alert-info'
}

/** 根据等级返回徽标颜色类 */
function levelClass(level) {
  if (level === '止损' || level === '策略信号') return 'level-danger'
  if (level === '止盈' || level === '加仓') return 'level-success'
  if (level === '减仓') return 'level-warn'
  return 'level-info'
}

/** 根据消息内容推导操作文本：买入 / 卖出 / 持有 */
function actionText(a) {
  if (a.level === '策略信号') return '买入'
  return a.title?.includes('卖出') ? '卖出' : a.title?.includes('买入') ? '买入' : '持有'
}

/** 根据操作文本返回操作徽标颜色类 */
function actionClass(a) {
  const t = actionText(a)
  if (t === '买入') return 'action-buy'
  if (t === '卖出') return 'action-sell'
  return 'action-hold'
}

/** 从 API 加载所有提醒，过滤掉日历类消息 */
async function load() {
  try {
    const all = await api.fetchAlerts()
    alerts.value = (all || []).filter(a => a.code !== 'CAL' && !a.level?.startsWith('日历'))
  } catch (_) {}
}

onMounted(() => {
  load()
  timer = setInterval(load, 15000)
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (unsubSSE) { unsubSSE(); unsubSSE = null }
})
</script>

<style scoped>
.msg-page { max-width: 900px; }
.page-header { margin-bottom: 16px; }
.page-header h2 { font-size: 16px; color: #e0e0e0; margin-bottom: 12px; }
.filter-row { display: flex; gap: 8px; }
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
