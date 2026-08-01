<!--
  LLM 分析诊断页面 LLMDebug.vue
  展示 LLM 新闻分析管线两阶段结果：Stage1 关键词初筛 + Stage2 LLM 深度分析
-->
<template>
  <div class="llm-debug-page">
    <!-- 页头：标题 + 轮次选择 + 刷新按钮 -->
    <div class="page-header">
      <h2>LLM 分析诊断</h2>
      <div class="header-right">
        <select class="round-select" v-model="roundIdx" :disabled="records.length < 2" @change="applyRound">
          <option v-for="(r, i) in records" :key="r.process_time" :value="i">
            轮次 {{ records.length - i }} · {{ formatTime(r.process_time) }}（{{ r.raw_count }} 条 / 选 {{ r.selected_count }}）
          </option>
        </select>
        <button class="btn-refresh" @click="loadData" :disabled="loading">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
      </div>
    </div>

    <!-- 状态判断 -->
    <div v-if="noAgent" class="empty">Agent 未就绪</div>
    <div v-else-if="noData" class="empty">暂无数据，等待下一轮扫描</div>
    <template v-else-if="data">
      <!-- 概要统计栏 -->
      <div class="summary-bar">
        <div class="summary-item">
          <span class="summary-label">Stage1 模式</span>
          <span :class="['summary-value', 'tag-' + data.stage1_mode]">{{ data.stage1_mode === 'llm' ? 'LLM' : '关键词' }}</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">原始条数</span>
          <span class="summary-value">{{ data.raw_count }}</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">筛选后</span>
          <span class="summary-value">{{ data.selected_count }}</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">分析时间</span>
          <span class="summary-value">{{ formatTime(data.process_time) }}</span>
        </div>
      </div>

      <!-- Stage1：新闻初筛标题列表，标注通过/过滤 -->
      <h3 class="section-title">Stage1 · 新闻初筛</h3>
      <div class="stage1-list">
        <div v-for="(title, i) in data.raw_titles" :key="i"
          :class="['title-item', isSelected(i) ? 'selected' : 'discarded']">
          <span class="title-idx">{{ i + 1 }}</span>
          <span class="title-text">{{ title }}</span>
          <span :class="['title-badge', isSelected(i) ? 'badge-pass' : 'badge-skip']">
            {{ isSelected(i) ? '通过' : '过滤' }}
          </span>
        </div>
      </div>

      <!-- Stage2：LLM 深度分析事件卡片 -->
      <h3 class="section-title">Stage2 · LLM 分析结果</h3>
      <div class="stage2-events" v-if="data.stage2_events && data.stage2_events.length">
        <div v-for="(ev, i) in data.stage2_events" :key="i" class="event-card">
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
            <div class="event-row" v-if="ev.upstream_sectors && ev.upstream_sectors.length">
              <span class="event-label">上游</span>
              <span class="event-tags">
                <span v-for="s in ev.upstream_sectors" :key="s" class="mini-tag sector">{{ s }}</span>
              </span>
            </div>
            <div class="event-row" v-if="ev.downstream_sectors && ev.downstream_sectors.length">
              <span class="event-label">下游</span>
              <span class="event-tags">
                <span v-for="s in ev.downstream_sectors" :key="s" class="mini-tag sector">{{ s }}</span>
              </span>
            </div>
            <div class="event-row" v-if="ev.impact_level">
              <span class="event-label">影响</span>
              <span :class="['mini-tag', 'impact-' + ev.impact_level]">{{ ev.impact_level }}</span>
            </div>
            <div class="event-row" v-if="ev.event_type">
              <span class="event-label">类型</span>
              <span class="mini-tag">{{ ev.event_type }}</span>
            </div>
            <div class="event-row" v-if="ev.reason">
              <span class="event-label">理由</span>
              <span class="event-reason">{{ ev.reason }}</span>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="empty">Stage2 无分析结果</div>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'   // Vue 组合式 API：响应式引用 ref 与挂载生命周期钩子
import * as api from '../api/index.js' // 后端 API 调用封装（获取轮次记录等）

// ── 响应式状态 ──
const loading = ref(false)        // 是否正在加载
const records = ref([])           // 当日全量轮次记录（持久化）
const roundIdx = ref(0)           // 当前选中轮次索引（records 下标，默认最新）
const data = ref(null)            // 当前轮次诊断数据
const noAgent = ref(false)        // Agent 未就绪
const noData = ref(false)         // 暂无数据

/** 被 Stage1 筛选通过的新闻索引集合 */
const selectedSet = ref(new Set())

/** 判断某条新闻是否通过 Stage1 筛选 */
function isSelected(i) {
  return selectedSet.value.has(i)
}

/** 切换轮次后应用选中记录 */
function applyRound() {
  const r = records.value[roundIdx.value]
  // 将选中轮次的记录设为当前展示数据
  data.value = r || null
  noAgent.value = false
  noData.value = !r
  // 回填该轮次通过 Stage1 筛选的新闻索引
  selectedSet.value = new Set(r ? r.selected_idx || [] : [])
}

/** 格式化时间为 HH:mm:ss */
function formatTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/** 从后端加载全量 LLM/Stage 轮次记录，默认展示最新一轮 */
async function loadData() {
  loading.value = true
  try {
    const res = await api.fetchStageRecords()
    if (res.status === 'no_engine') {
      // 后端未启用分析引擎，提示 Agent 未就绪
      noAgent.value = true
      noData.value = false
      records.value = []
      data.value = null
    } else if (!Array.isArray(res) || res.length === 0) {
      // 暂无轮次记录，展示空态
      noData.value = true
      noAgent.value = false
      records.value = []
      data.value = null
    } else {
      // 有记录时默认展示最新一轮
      records.value = res
      roundIdx.value = 0
      applyRound()
    }
  } catch (e) {
    console.error('LLMDebug 加载失败', e)
  } finally {
    loading.value = false
  }
}

// 挂载时加载一次最新轮次数据
onMounted(loadData)
</script>

<style scoped>
.llm-debug-page { max-width: 960px; margin: 0 auto; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.page-header h2 { font-size: 18px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.round-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 12px; cursor: pointer; max-width: 320px;
}
.btn-refresh { padding: 6px 14px; border-radius: 6px; border: 1px solid #FF4D4F; background: transparent; color: #FF4D4F; font-size: 13px; cursor: pointer; }
.btn-refresh:disabled { opacity: 0.5; }
.btn-refresh:hover { background: rgba(255,77,79,0.1); }

.empty { text-align: center; padding: 40px; color: #666; font-size: 14px; }

.summary-bar { display: flex; gap: 16px; flex-wrap: wrap; margin-bottom: 20px; background: #1a1a2e; border-radius: 8px; padding: 12px 16px; }
.summary-item { display: flex; flex-direction: column; gap: 2px; min-width: 80px; }
.summary-label { font-size: 11px; color: #888; }
.summary-value { font-size: 16px; font-weight: 600; color: #e0e0e0; }
.tag-llm { color: #4caf50; }
.tag-keyword { color: #ff9800; }

.section-title { font-size: 15px; margin-bottom: 10px; color: #ccc; border-bottom: 1px solid #2a2a3e; padding-bottom: 6px; }

.stage1-list { margin-bottom: 24px; }
.title-item { display: flex; align-items: center; gap: 8px; padding: 6px 10px; border-radius: 4px; margin-bottom: 2px; font-size: 13px; }
.title-item.selected { background: rgba(76,175,80,0.08); }
.title-item.discarded { opacity: 0.45; }
.title-idx { color: #666; min-width: 24px; font-size: 11px; text-align: right; }
.title-text { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.title-badge { font-size: 11px; padding: 1px 6px; border-radius: 3px; white-space: nowrap; }
.badge-pass { background: rgba(76,175,80,0.2); color: #4caf50; }
.badge-skip { background: rgba(255,152,0,0.15); color: #ff9800; }

.stage2-events { display: flex; flex-direction: column; gap: 10px; margin-bottom: 20px; }
.event-card { background: #1a1a2e; border-radius: 8px; padding: 12px 14px; border: 1px solid #2a2a3e; }
.event-header { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; }
.event-title { flex: 1; font-size: 14px; font-weight: 600; color: #e0e0e0; min-width: 0; }
.event-score { font-size: 12px; color: #888; white-space: nowrap; }

.tag { font-size: 11px; padding: 2px 7px; border-radius: 4px; white-space: nowrap; }
.tag-利好 { background: rgba(76,175,80,0.2); color: #4caf50; }
.tag-利空 { background: rgba(255,77,79,0.2); color: #FF4D4F; }
.tag-中性 { background: rgba(255,152,0,0.15); color: #ff9800; }

.event-body { display: flex; flex-direction: column; gap: 5px; }
.event-row { display: flex; align-items: flex-start; gap: 8px; font-size: 12px; }
.event-label { color: #888; min-width: 32px; flex-shrink: 0; }
.event-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.mini-tag { font-size: 11px; padding: 1px 6px; border-radius: 3px; background: #2a2a3e; color: #aaa; }
.mini-tag.sector { color: #64b5f6; background: rgba(100,181,246,0.1); }
.mini-tag.stock { color: #ff9800; background: rgba(255,152,0,0.1); }
.mini-tag.impact-高 { color: #FF4D4F; background: rgba(255,77,79,0.15); }
.mini-tag.impact-中 { color: #ff9800; background: rgba(255,152,0,0.12); }
.mini-tag.impact-低 { color: #888; background: #2a2a3e; }
.event-reason { color: #aaa; line-height: 1.4; }
</style>
