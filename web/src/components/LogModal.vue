<!--
  日志弹窗 LogModal.vue
  按"批次"（时间分组）展示两类日志，类型分 tab 隔离：
  - LLM 分析：当日各轮 Stage 流水线（Stage1 初筛 + Stage2 深度分析）
  - 信号批次：当日各轮产出的全部策略信号（做多/做空/提醒）
  每个 tab 内用轮次下拉切换批次，最新批次默认展示。
-->
<template>
  <div v-if="visible" class="log-overlay" @click.self="close">
    <div class="log-modal">
      <div class="log-header">
        <span class="log-title">📋 日志</span>
        <button class="log-close" @click="close">✕</button>
      </div>

      <!-- 类型 tab：分开日志类型 -->
      <div class="log-tabs">
        <span
          :class="['log-tab', activeTab === 'llm' ? 'active' : '']"
          @click="switchTab('llm')"
        >LLM 分析</span>
        <span
          :class="['log-tab', activeTab === 'signal' ? 'active' : '']"
          @click="switchTab('signal')"
        >信号批次</span>
      </div>

      <!-- LLM 分析 tab -->
      <div v-show="activeTab === 'llm'" class="log-body">
        <div class="log-toolbar">
          <select v-model="llmIdx" :disabled="llmRecords.length < 2" @change="applyLLM" class="log-select">
            <option v-for="(r, i) in llmRecords" :key="r.process_time" :value="i">
              轮次 {{ llmRecords.length - i }} · {{ fmtTime(r.process_time) }}（{{ r.raw_count }} 条 / 选 {{ r.selected_count }}）
            </option>
          </select>
          <button class="btn-refresh" @click="load" :disabled="loading">刷新</button>
        </div>

        <div v-if="llmNoData" class="log-empty">暂无 LLM 分析记录，等待下一轮扫描</div>
        <template v-else-if="llmData">
          <div class="summary-bar">
            <div class="summary-item">
              <span class="summary-label">Stage1 模式</span>
              <span :class="['summary-value', llmData.stage1_mode === 'llm' ? 'tag-llm' : 'tag-keyword']">
                {{ llmData.stage1_mode === 'llm' ? 'LLM' : '关键词' }}
              </span>
            </div>
            <div class="summary-item">
              <span class="summary-label">原始条数</span>
              <span class="summary-value">{{ llmData.raw_count }}</span>
            </div>
            <div class="summary-item">
              <span class="summary-label">筛选后</span>
              <span class="summary-value">{{ llmData.selected_count }}</span>
            </div>
            <div class="summary-item">
              <span class="summary-label">分析时间</span>
              <span class="summary-value">{{ fmtTime(llmData.process_time) }}</span>
            </div>
          </div>

          <h3 class="section-title">Stage1 · 新闻初筛</h3>
          <div class="stage1-list">
            <div v-for="(title, i) in llmData.raw_titles" :key="i"
              :class="['title-item', isSelected(i) ? 'selected' : 'discarded']">
              <span class="title-idx">{{ i + 1 }}</span>
              <span class="title-text">{{ title }}</span>
              <span :class="['title-badge', isSelected(i) ? 'badge-pass' : 'badge-skip']">
                {{ isSelected(i) ? '通过' : '过滤' }}
              </span>
            </div>
          </div>

          <h3 class="section-title">Stage2 · LLM 分析结果</h3>
          <div v-if="llmData.stage2_events && llmData.stage2_events.length" class="stage2-events">
            <div v-for="(ev, i) in llmData.stage2_events" :key="i" class="event-card">
              <div class="event-header">
                <span class="event-title">{{ ev.title }}</span>
                <span :class="['tag', 'tag-' + ev.direction]">{{ ev.direction || '中性' }}</span>
                <span class="event-score">评分 {{ (ev.score || 0).toFixed(2) }}</span>
              </div>
              <div class="event-body">
                <div class="event-row" v-if="ev.sectors && ev.sectors.length">
                  <span class="event-label">板块</span>
                  <span class="event-tags">
                    <span v-for="s in ev.sectors" :key="s" class="mini-tag sector">{{ s }}</span>
                  </span>
                </div>
                <div class="event-row" v-if="ev.related_stocks && ev.related_stocks.length">
                  <span class="event-label">个股</span>
                  <span class="event-tags">
                    <span v-for="s in ev.related_stocks" :key="s" class="mini-tag stock">{{ s }}</span>
                  </span>
                </div>
                <div class="event-row" v-if="ev.reason">
                  <span class="event-label">理由</span>
                  <span class="event-reason">{{ ev.reason }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="log-empty">Stage2 无分析结果</div>
        </template>
      </div>

      <!-- 信号批次 tab -->
      <div v-show="activeTab === 'signal'" class="log-body">
        <div class="log-toolbar">
          <select v-model="sigIdx" :disabled="sigRecords.length < 2" @change="applySignal" class="log-select">
            <option v-for="(r, i) in sigRecords" :key="r.process_time" :value="i">
              批次 {{ sigRecords.length - i }} · {{ fmtTime(r.process_time) }}（{{ r.signals.length }} 信号 / {{ r.raw_count }} 条）
            </option>
          </select>
          <button class="btn-refresh" @click="load" :disabled="loading">刷新</button>
        </div>

        <div v-if="sigNoData" class="log-empty">暂无信号批次记录，等待下一轮扫描</div>
        <template v-else-if="sigData">
          <div class="summary-bar">
            <div class="summary-item">
              <span class="summary-label">批次时间</span>
              <span class="summary-value">{{ fmtTime(sigData.process_time) }}</span>
            </div>
            <div class="summary-item">
              <span class="summary-label">原始条数</span>
              <span class="summary-value">{{ sigData.raw_count }}</span>
            </div>
            <div class="summary-item">
              <span class="summary-label">信号数</span>
              <span class="summary-value">{{ sigData.signals.length }}</span>
            </div>
          </div>

          <div v-if="sigData.signals.length" class="signal-list">
            <div v-for="(sg, i) in sigData.signals" :key="sg.id || i" class="signal-item">
              <div class="sig-head">
                <span class="sig-code">{{ sg.code }}</span>
                <span class="sig-name">{{ sg.name || '-' }}</span>
                <span class="sig-strategy">{{ sg.strategy || '-' }}</span>
                <span :class="['tag', 'dir-' + sg.direction]">{{ sg.direction || '中性' }}</span>
                <span :class="['tag', 'act-' + sg.action]">{{ sg.action || '-' }}</span>
                <span class="sig-conf">置信 {{ (sg.confidence || 0).toFixed(2) }}</span>
                <span v-if="sg.price" class="sig-price">¥{{ sg.price.toFixed(2) }}</span>
              </div>
              <div class="sig-body">
                <span v-if="sg.sector" class="sig-sector">{{ sg.sector }}</span>
                <span v-if="sg.reason" class="sig-reason">{{ sg.reason }}</span>
              </div>
            </div>
          </div>
          <div v-else class="log-empty">本轮无信号产出</div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch, onMounted } from 'vue'
import * as api from '../api/index.js'

const props = defineProps({
  visible: { type: Boolean, default: false },
})
const emit = defineEmits(['close'])

const activeTab = ref('llm')
const loading = ref(false)

const llmRecords = ref([])
const llmIdx = ref(0)
const llmData = ref(null)
const llmNoData = ref(false)
const selectedSet = ref(new Set())

const sigRecords = ref([])
const sigIdx = ref(0)
const sigData = ref(null)
const sigNoData = ref(false)

function isSelected(i) {
  return selectedSet.value.has(i)
}

function applyLLM() {
  const r = llmRecords.value[llmIdx.value]
  llmData.value = r || null
  llmNoData.value = !r
  selectedSet.value = new Set(r ? r.selected_idx || [] : [])
}

function applySignal() {
  const r = sigRecords.value[sigIdx.value]
  sigData.value = r || null
  sigNoData.value = !r
}

function switchTab(t) {
  activeTab.value = t
  if ((t === 'llm' && llmData.value) || (t === 'signal' && sigData.value)) return
  if (!llmRecords.value.length && !sigRecords.value.length) load()
}

function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

async function load() {
  if (loading.value) return
  loading.value = true
  // 两个接口独立抓取：单个失败/无数据不影响另一个 tab，
  // 避免一处出错导致两个下拉框全部清空。
  const [srRes, slRes] = await Promise.allSettled([api.fetchStageRecords(), api.fetchSignalLogs()])
  if (srRes.status === 'fulfilled' && Array.isArray(srRes.value) && srRes.value.length) {
    llmRecords.value = srRes.value
    llmIdx.value = 0
    applyLLM()
  } else {
    llmRecords.value = []
    llmData.value = null
    llmNoData.value = true
  }
  if (slRes.status === 'fulfilled' && Array.isArray(slRes.value) && slRes.value.length) {
    sigRecords.value = slRes.value
    sigIdx.value = 0
    applySignal()
  } else {
    sigRecords.value = []
    sigData.value = null
    sigNoData.value = true
  }
  loading.value = false
}

watch(() => props.visible, (v) => {
  // 每次打开都重新拉取，避免首次打开时后台尚未产出记录而卡在空态
  if (v) load()
})

function close() {
  emit('close')
}

onMounted(() => {
  if (props.visible) load()
})
</script>

<style scoped>
.log-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,0.65);
  display: flex; align-items: center; justify-content: center; z-index: 200;
}
.log-modal {
  width: 92%; max-width: 860px; max-height: 86vh;
  background: #16162a; border-radius: 12px;
  display: flex; flex-direction: column; overflow: hidden;
}
.log-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 12px 16px; border-bottom: 1px solid #2a2a3e;
}
.log-title { font-size: 15px; font-weight: 600; color: #e0e0e0; }
.log-close {
  border: none; background: transparent; color: #888; font-size: 16px; cursor: pointer;
}
.log-close:hover { color: #FF4D4F; }

.log-tabs {
  display: flex; gap: 6px; padding: 10px 16px 0;
}
.log-tab {
  padding: 6px 16px; border-radius: 6px 6px 0 0; font-size: 13px; color: #888;
  cursor: pointer; background: #1a1a2e; border: 1px solid #2a2a3e; border-bottom: none;
}
.log-tab.active { color: #e0e0e0; background: #0f0f23; border-color: #FF4D4F; }

.log-body {
  padding: 12px 16px 16px; overflow-y: auto;
}
.log-toolbar {
  display: flex; align-items: center; gap: 10px; margin-bottom: 12px;
}
.log-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 12px; cursor: pointer; max-width: 340px; flex: 1;
}
.btn-refresh {
  padding: 6px 14px; border-radius: 6px; border: 1px solid #FF4D4F;
  background: transparent; color: #FF4D4F; font-size: 13px; cursor: pointer;
}
.btn-refresh:disabled { opacity: 0.5; }
.btn-refresh:hover { background: rgba(255,77,79,0.1); }

.log-empty { text-align: center; padding: 40px; color: #666; font-size: 13px; }

.summary-bar {
  display: flex; gap: 16px; flex-wrap: wrap; margin-bottom: 16px;
  background: #1a1a2e; border-radius: 8px; padding: 10px 14px;
}
.summary-item { display: flex; flex-direction: column; gap: 2px; min-width: 70px; }
.summary-label { font-size: 11px; color: #888; }
.summary-value { font-size: 15px; font-weight: 600; color: #e0e0e0; }
.tag-llm { color: #4caf50; }
.tag-keyword { color: #ff9800; }

.section-title { font-size: 14px; margin: 14px 0 8px; color: #ccc; border-bottom: 1px solid #2a2a3e; padding-bottom: 5px; }
.stage1-list { margin-bottom: 8px; }
.title-item { display: flex; align-items: center; gap: 8px; padding: 5px 10px; border-radius: 4px; margin-bottom: 2px; font-size: 12px; }
.title-item.selected { background: rgba(76,175,80,0.08); }
.title-item.discarded { opacity: 0.45; }
.title-idx { color: #666; min-width: 22px; font-size: 11px; text-align: right; }
.title-text { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.title-badge { font-size: 11px; padding: 1px 6px; border-radius: 3px; white-space: nowrap; }
.badge-pass { background: rgba(76,175,80,0.2); color: #4caf50; }
.badge-skip { background: rgba(255,152,0,0.15); color: #ff9800; }

.stage2-events { display: flex; flex-direction: column; gap: 8px; }
.event-card { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.event-header { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; flex-wrap: wrap; }
.event-title { flex: 1; font-size: 13px; font-weight: 600; color: #e0e0e0; min-width: 0; }
.event-score { font-size: 11px; color: #888; white-space: nowrap; }
.event-body { display: flex; flex-direction: column; gap: 4px; }
.event-row { display: flex; align-items: flex-start; gap: 8px; font-size: 12px; }
.event-label { color: #888; min-width: 32px; flex-shrink: 0; }
.event-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.mini-tag { font-size: 11px; padding: 1px 6px; border-radius: 3px; background: #2a2a3e; color: #aaa; }
.mini-tag.sector { color: #64b5f6; background: rgba(100,181,246,0.1); }
.mini-tag.stock { color: #ff9800; background: rgba(255,152,0,0.1); }
.event-reason { color: #aaa; line-height: 1.4; }

.signal-list { display: flex; flex-direction: column; gap: 8px; }
.signal-item { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.sig-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.sig-code { font-family: monospace; color: #4fc3f7; font-size: 13px; font-weight: 600; }
.sig-name { color: #e0e0e0; font-size: 13px; }
.sig-strategy { font-size: 11px; color: #b388ff; background: rgba(179,136,255,0.12); padding: 1px 6px; border-radius: 3px; }
.sig-conf { font-size: 11px; color: #888; }
.sig-price { font-size: 11px; color: #FAAD14; }
.sig-body { display: flex; align-items: center; gap: 8px; margin-top: 5px; font-size: 12px; flex-wrap: wrap; }
.sig-sector { font-size: 11px; color: #64b5f6; background: rgba(100,181,246,0.1); padding: 1px 6px; border-radius: 3px; }
.sig-reason { color: #aaa; line-height: 1.4; }

.tag { font-size: 11px; padding: 2px 7px; border-radius: 4px; white-space: nowrap; }
.tag-利好 { background: rgba(76,175,80,0.2); color: #4caf50; }
.tag-利空 { background: rgba(255,77,79,0.2); color: #FF4D4F; }
.tag-中性 { background: rgba(255,152,0,0.15); color: #ff9800; }
.tag-做多 { background: rgba(76,175,80,0.2); color: #4caf50; }
.tag-做空 { background: rgba(255,77,79,0.2); color: #FF4D4F; }
.tag-提醒 { background: rgba(255,152,0,0.15); color: #ff9800; }
.dir-做多 { background: rgba(76,175,80,0.2); color: #4caf50; }
.dir-做空 { background: rgba(255,77,79,0.2); color: #FF4D4F; }
.dir-提醒 { background: rgba(255,152,0,0.15); color: #ff9800; }
.act-买入 { background: rgba(76,175,80,0.25); color: #4caf50; }
.act-watch { background: rgba(100,181,246,0.15); color: #64b5f6; }
.act-卖出 { background: rgba(255,77,79,0.25); color: #FF4D4F; }
</style>
