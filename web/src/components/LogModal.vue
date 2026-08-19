<!--
  日志弹窗 LogModal.vue (Log modal component)
  按"批次"（时间分组）展示两类日志，类型分 tab 隔离：
  Shows two kinds of batch logs (grouped by time) in separate tabs:
  - LLM 分析：当日各轮 Stage 流水线（Stage1 初筛 + Stage2 深度分析）
    LLM analysis: each run's Stage pipeline today (Stage1 pre-filter + Stage2 deep analysis)
  - 信号批次：当日各轮产出的全部策略信号（做多/做空/提醒）
    Signal batches: all strategy signals produced each run today (long/short/alert)
  每个 tab 内支持两种浏览方式：
  Each tab supports two browsing modes:
  - 无搜索：轮次下拉切换批次，最新批次默认展示
    No search: switch batches via the dropdown, newest shown by default
  - 有搜索：跨批次聚合匹配（按 个股名称/代码/板块 等），按批次分组展示命中项
    With search: cross-batch aggregation match (by stock name/code/sector), grouped by batch
-->
<template>
  <div v-if="visible" class="log-overlay" @click.self="close">
    <div class="log-modal">
      <!-- 弹窗头部：标题 + 关闭按钮 -->
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
          <input
            v-model="llmQuery"
            type="text"
            class="log-search"
            placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
          />
          <select
            v-show="!llmSearching"
            v-model="llmIdx"
            :disabled="llmRecords.length < 2"
            @change="applyLLM"
            class="log-select"
          >
            <option v-for="(r, i) in llmRecords" :key="r.process_time" :value="i">
              轮次 {{ llmRecords.length - i }} · {{ fmtTime(r.process_time) }}（{{ r.raw_count }} 条 / 选 {{ r.selected_count }}）
            </option>
          </select>
          <button class="btn-refresh" @click="load" :disabled="loading">刷新</button>
        </div>

        <!-- 跨批次搜索结果视图 -->
        <div v-if="llmSearching" class="search-view">
          <div v-if="!llmSearchGroups.length" class="log-empty">未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
          <div v-else class="search-summary">
            共 {{ llmTotalHits }} 条事件命中，跨 {{ llmSearchGroups.length }} 个轮次
          </div>
          <div v-for="(g, gi) in llmSearchGroups" :key="gi" class="search-group">
            <div class="search-group-head">
              <span class="search-batch">轮次 {{ fmtTime(g.time) }}</span>
              <span class="search-count">命中 {{ g.items.length }} 条</span>
            </div>
            <div v-for="(ev, i) in g.items" :key="i" class="event-card">
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
        </div>

        <!-- 单批次浏览视图（无搜索时） -->
        <template v-else>
          <div v-if="llmNoData" class="log-empty">暂无 LLM 分析记录，等待下一轮扫描</div>
          <template v-else-if="llmData">
            <!-- 批次概要：Stage1 模式 / 原始条数 / 筛选后 / 分析时间 -->
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

            <!-- Stage1 新闻初筛：逐条新闻标题 + 通过/过滤徽标 -->
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

            <!-- Stage2 LLM 深度分析：每张事件卡片展示方向/评分/板块/个股/理由 -->
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
        </template>
      </div>

      <!-- 信号批次 tab -->
      <div v-show="activeTab === 'signal'" class="log-body">
        <div class="log-toolbar">
          <input
            v-model="sigQuery"
            type="text"
            class="log-search"
            placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
          />
          <!-- 战法策略筛选：与信号条上的"策略"同名精确匹配（Strategy filter: exact match against the strategy shown on each signal row）-->
          <select
            v-model="activeSigStrategy"
            class="log-strategy-select"
            title="按战法策略筛选"
          >
            <option value="all">全部战法</option>
            <option v-for="st in sigStrategyOptions" :key="st" :value="st">{{ st }}</option>
          </select>
          <select
            v-show="!sigSearching"
            v-model="sigIdx"
            :disabled="sigRecords.length < 2"
            @change="applySignal"
            class="log-select"
          >
            <option v-for="(r, i) in sigRecords" :key="r.process_time" :value="i">
              批次 {{ sigRecords.length - i }} · {{ fmtTime(r.process_time) }}（{{ r.signals.length }} 信号 / {{ r.raw_count }} 条）
            </option>
          </select>
          <button class="btn-refresh" @click="load" :disabled="loading">刷新</button>
        </div>

        <!-- 跨批次搜索结果视图 -->
        <div v-if="sigSearching" class="search-view">
          <div v-if="!sigSearchGroups.length" class="log-empty">未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
          <div v-else class="search-summary">
            共 {{ sigTotalHits }} 条信号命中，跨 {{ sigSearchGroups.length }} 个批次
          </div>
          <div v-for="(g, gi) in sigSearchGroups" :key="gi" class="search-group">
            <div class="search-group-head">
              <span class="search-batch">批次 {{ fmtTime(g.time) }}</span>
              <span class="search-count">命中 {{ g.items.length }} 条</span>
            </div>
            <div v-for="(sg, i) in g.items" :key="i" class="signal-item">
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
        </div>

        <!-- 单批次浏览视图（无搜索时） -->
        <template v-else>
          <div v-if="sigNoData" class="log-empty">暂无信号批次记录，等待下一轮扫描</div>
          <template v-else-if="sigData">
            <!-- 批次概要：批次时间 / 原始条数 / 信号数 -->
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

            <div v-if="sigFiltered.length" class="signal-list">
              <div v-for="(sg, i) in sigFiltered" :key="sg.id || i" class="signal-item">
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
            <div v-else class="log-empty">{{ sigData.signals.length ? '当前战法无匹配信号' : '本轮无信号产出' }}</div>
          </template>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ──
// ref 定义响应式；computed 派生跨批次搜索聚合结果；watch 侦听 visible 变化触发加载；onMounted 挂载时加载
// (ref for reactivity; computed for cross-batch search results; watch on visible to trigger load; onMounted initial load)
import { ref, computed, watch, onMounted } from 'vue'
// 后端 API 封装：Stage 流水线记录与信号批次记录获取接口 (backend API wrapper: stage pipeline & signal batch record fetchers)
import * as api from '../api/index.js'

// 父组件传入的可见性控制：visible 为 true 时才渲染弹窗层 (props from parent: render the modal overlay only when visible)
const props = defineProps({
  visible: { type: Boolean, default: false },
})
// 通知父组件关闭弹窗（点击遮罩 / 右上角 ✕ 时触发）(emit close event to parent on overlay click / ✕ button)
const emit = defineEmits(['close'])

// ── 响应式状态 ──
const activeTab = ref('llm')      // 当前激活的 tab：'llm'（LLM 分析）/ 'signal'（信号批次）(active tab: 'llm' | 'signal')
const loading = ref(false)        // 是否正在拉取数据（刷新按钮禁用状态）(whether data is being fetched, disables refresh button)

// LLM 分析 tab 状态 (LLM analysis tab state)
const llmRecords = ref([])        // 当日全量 Stage 轮次记录（按批次）(all Stage run records today, newest first)
const llmIdx = ref(0)             // 下拉框选中的轮次索引（默认最新=0）(selected run index in dropdown, default newest=0)
const llmData = ref(null)         // 当前展示的那一轮 LLM 分析数据 (currently displayed LLM analysis record)
const llmNoData = ref(false)      // 是否无 LLM 记录（展示空态文案）(whether no LLM records exist, shows empty state)
const llmQuery = ref('')          // LLM 搜索关键词（名称/代码/板块）(LLM search keyword: name/code/sector)
const selectedSet = ref(new Set()) // 该轮次通过 Stage1 筛选的新闻索引集合 (set of news indices that passed Stage1 filtering)

// 信号批次 tab 状态 (Signal batch tab state)
const sigRecords = ref([])       // 当日全量信号批次记录 (all signal batch records today)
const sigIdx = ref(0)            // 下拉框选中的批次索引 (selected batch index in dropdown)
const sigData = ref(null)         // 当前展示的那一批信号数据 (currently displayed signal batch record)
const sigNoData = ref(false)      // 是否无信号批次记录（展示空态文案）(whether no signal batch records exist, shows empty state)
const sigQuery = ref('')          // 信号搜索关键词（名称/代码/板块）(signal search keyword: name/code/sector)
const activeSigStrategy = ref('all') // 信号批次按战法策略筛选（all=不筛选）(signal strategy filter; 'all' disables filtering)

/** 判断某条新闻（按原始序号 i）是否通过 Stage1 筛选 (Check whether news index i passed Stage1 filtering) */
// 由 selectedSet 决定模板里显示"通过"还是"过滤"徽标 (selectedSet decides the "通过/pass" vs "过滤/skip" badge shown)
function isSelected(i) {
  return selectedSet.value.has(i)
}

/** 将当前选中的 LLM 轮次应用到展示区 (Apply the selected LLM run to the display area) */
// 原理：从 llmRecords 按 llmIdx 取出记录，回填 llmData 与筛选索引集合
// (Picks the record at llmIdx from llmRecords and fills llmData plus the filter index set)
function applyLLM() {
  const r = llmRecords.value[llmIdx.value]
  llmData.value = r || null
  llmNoData.value = !r
  selectedSet.value = new Set(r ? r.selected_idx || [] : [])
}

/** 将当前选中的信号批次应用到展示区 (Apply the selected signal batch to the display area) */
function applySignal() {
  const r = sigRecords.value[sigIdx.value]
  sigData.value = r || null
  sigNoData.value = !r
}

// ── 跨批次搜索 ──

/** 判断一条 LLM Stage2 事件是否命中关键词（名称/代码/板块/标题/理由，大小写不敏感） (Check if an LLM Stage2 event matches the keyword across title/reason/sectors/stocks, case-insensitive) */
function eventHit(ev, q) {
  if (!ev) return false
  if (hasText(ev.title, q)) return true
  if (hasText(ev.reason, q)) return true
  if (ev.sectors && ev.sectors.some((s) => hasText(s, q))) return true
  if (ev.related_stocks && ev.related_stocks.some((s) => hasText(s, q))) return true
  if (ev.cleaned_stocks && ev.cleaned_stocks.some((s) => hasText(s, q))) return true
  return false
}

/** 判断一条信号是否命中关键词（代码/名称/板块/策略/理由，大小写不敏感） (Check if a signal matches the keyword across code/name/sector/strategy/reason, case-insensitive) */
function sigHit(sg, q) {
  if (!sg) return false
  if (hasText(sg.code, q)) return true
  if (hasText(sg.name, q)) return true
  if (hasText(sg.sector, q)) return true
  if (hasText(sg.strategy, q)) return true
  if (hasText(sg.reason, q)) return true
  return false
}

/** 判断文本是否包含关键词（空串恒不命中；统一转大写忽略大小写） (Case-insensitive substring check; empty strings never match) */
function hasText(text, q) {
  if (!text || !q) return false
  return String(text).toUpperCase().includes(q)
}

/** 判断信号是否命中当前战法筛选（'all'=不筛选） (Whether a signal passes the active strategy filter; 'all' disables it) */
function sigMatchStrategy(sg) {
  if (!sg) return false
  if (activeSigStrategy.value === 'all') return true
  return sg.strategy === activeSigStrategy.value
}

/** 战法策略选项：跨批次收集全部策略名称（与信号条上的"策略"同名） (Strategy options collected across all batches, matching the strategy shown on each signal row) */
const sigStrategyOptions = computed(() => {
  const set = new Set()
  for (const r of sigRecords.value) {
    for (const sg of (r.signals || [])) {
      if (sg.strategy) set.add(sg.strategy)
    }
  }
  return [...set]
})

/** 当前批次按战法筛选后的信号列表（单批次浏览视图使用） (Signals of the selected batch filtered by strategy, for the single-batch view) */
const sigFiltered = computed(() => {
  const sigs = sigData.value?.signals || []
  if (activeSigStrategy.value === 'all') return sigs
  return sigs.filter((sg) => sg.strategy === activeSigStrategy.value)
})

/** LLM 是否处于搜索态（输入非空） (Whether LLM search mode is active, i.e. input not empty) */
const llmSearching = computed(() => (llmQuery.value || '').trim() !== '')
/** 信号是否处于搜索态（输入非空） (Whether signal search mode is active, i.e. input not empty) */
const sigSearching = computed(() => (sigQuery.value || '').trim() !== '')

/** LLM 跨批次搜索结果：按轮次分组，组内含命中事件；llmRecords 本身最新在前 (LLM cross-batch search results grouped by run; llmRecords are already newest-first) */
const llmSearchGroups = computed(() => {
  const q = (llmQuery.value || '').trim().toUpperCase()
  if (!q) return []
  const groups = []
  for (const r of llmRecords.value) {
    // 过滤出该轮命中关键词的事件 (filter events of this run that match the keyword)
    const items = (r.stage2_events || []).filter((ev) => eventHit(ev, q))
    if (items.length) {
      groups.push({ time: r.process_time, items })
    }
  }
  return groups
})

/** LLM 全部命中事件数（搜索概要展示） (Total LLM hit count for the search summary) */
const llmTotalHits = computed(() => llmSearchGroups.value.reduce((n, g) => n + g.items.length, 0))

/** 信号跨批次搜索结果：按批次分组，组内含命中信号；sigRecords 本身最新在前 (Signal cross-batch search results grouped by batch; sigRecords are already newest-first) */
const sigSearchGroups = computed(() => {
  const q = (sigQuery.value || '').trim().toUpperCase()
  if (!q) return []
  const groups = []
  for (const r of sigRecords.value) {
    // 过滤出该批命中关键词 + 命中当前战法筛选的信号 (filter signals of this batch that match both the keyword and the active strategy filter)
    const items = (r.signals || []).filter((sg) => sigHit(sg, q) && sigMatchStrategy(sg))
    if (items.length) {
      groups.push({ time: r.process_time, items })
    }
  }
  return groups
})

/** 信号全部命中数（搜索概要展示） (Total signal hit count for the search summary) */
const sigTotalHits = computed(() => sigSearchGroups.value.reduce((n, g) => n + g.items.length, 0))

/** 切换 tab（llm <-> signal） (Switch tab between 'llm' and 'signal') */
// 仅在切换后当前 tab 无数据时才触发一次加载，避免无谓重复请求
// (Only trigger a load if the newly activated tab has no data yet, avoiding redundant requests)
function switchTab(t) {
  activeTab.value = t
  if ((t === 'llm' && llmData.value) || (t === 'signal' && sigData.value)) return
  if (!llmRecords.value.length && !sigRecords.value.length) load()
}

/** 格式化时间为 HH:mm:ss（用于下拉选项与概要栏显示） (Format time as HH:mm:ss for dropdowns and summary bar) */
function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/** 并行加载 LLM 轮次记录与信号批次记录 (Load LLM run records and signal batch records in parallel) */
async function load() {
  if (loading.value) return
  loading.value = true
  // 两个接口独立抓取：单个失败/无数据不影响另一个 tab，
  // 避免一处出错导致两个下拉框全部清空。
  // (Fetch both APIs independently: a failure/empty result on one tab does not affect the other)
  const [srRes, slRes] = await Promise.allSettled([api.fetchStageRecords(), api.fetchSignalLogs()])
  // LLM 记录有值时默认展示最新一轮，否则进入空态 (if LLM records exist show newest run by default, else empty state)
  if (srRes.status === 'fulfilled' && Array.isArray(srRes.value) && srRes.value.length) {
    llmRecords.value = srRes.value
    llmIdx.value = 0
    applyLLM()
  } else {
    llmRecords.value = []
    llmData.value = null
    llmNoData.value = true
  }
  // 信号批次记录同理，成功则默认展示最新一批 (same for signal batches: show newest on success)
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

// 侦听 visible：每次打开弹窗都重新拉取，
// 避免上次关闭后到再次打开期间新产出的记录未被加载
// (Watch visible: reload every time the modal opens so records produced while closed get loaded)
watch(() => props.visible, (v) => {
  // 每次打开都重新拉取，避免首次打开时后台尚未产出记录而卡在空态 (reload on each open to avoid an empty state when nothing was ready at first open)
  if (v) load()
})

/** 关闭弹窗：向父组件派发 close 事件 (Close the modal by emitting the close event to the parent) */
function close() {
  emit('close')
}

// 挂载时若初始即为打开状态（如路由直接带着弹窗进入），补一次加载
// (On mount, if initially open — e.g. entering via a route with the modal active — trigger an extra load)
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
  padding: 6px 16px; border-radius: 6px 6px 0 0; font-size: 14px; color: #888;
  cursor: pointer; background: #1a1a2e; border: 1px solid #2a2a3e; border-bottom: none;
}
.log-tab.active { color: #e0e0e0; background: #0f0f23; border-color: #FF4D4F; }

.log-body {
  padding: 12px 16px 16px; overflow-y: auto;
}
.log-toolbar {
  display: flex; align-items: center; gap: 10px; margin-bottom: 12px;
}
.log-search {
  flex: 1; min-width: 0;
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #e0e0e0; font-size: 14px; outline: none;
}
.log-search::placeholder { color: #666; }
.log-search:focus { border-color: #FF4D4F; }
.log-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 14px; cursor: pointer; max-width: 340px; flex: 1;
}
.log-strategy-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 14px; cursor: pointer; outline: none;
}
.log-strategy-select:focus { border-color: #b388ff; }
.btn-refresh {
  padding: 6px 14px; border-radius: 6px; border: 1px solid #FF4D4F;
  background: transparent; color: #FF4D4F; font-size: 14px; cursor: pointer; white-space: nowrap;
}
.btn-refresh:disabled { opacity: 0.5; }
.btn-refresh:hover { background: rgba(255,77,79,0.1); }

.log-empty { text-align: center; padding: 40px; color: #666; font-size: 14px; }

/* ── 跨批次搜索结果视图 ── */
.search-view { display: flex; flex-direction: column; gap: 12px; }
.search-summary { font-size: 14px; color: #888; }
.search-group { display: flex; flex-direction: column; gap: 8px; }
.search-group-head {
  display: flex; align-items: center; gap: 10px;
  padding: 4px 2px; border-bottom: 1px dashed #2a2a3e;
}
.search-batch { font-size: 14px; font-weight: 600; color: #4fc3f7; }
.search-count { font-size: 14px; color: #888; }

.summary-bar {
  display: flex; gap: 16px; flex-wrap: wrap; margin-bottom: 16px;
  background: #1a1a2e; border-radius: 8px; padding: 10px 14px;
}
.summary-item { display: flex; flex-direction: column; gap: 2px; min-width: 70px; }
.summary-label { font-size: 14px; color: #888; }
.summary-value { font-size: 15px; font-weight: 600; color: #e0e0e0; }
.tag-llm { color: #4caf50; }
.tag-keyword { color: #ff9800; }

.section-title { font-size: 14px; margin: 14px 0 8px; color: #ccc; border-bottom: 1px solid #2a2a3e; padding-bottom: 5px; }
.stage1-list { margin-bottom: 8px; }
.title-item { display: flex; align-items: center; gap: 8px; padding: 5px 10px; border-radius: 4px; margin-bottom: 2px; font-size: 14px; }
.title-item.selected { background: rgba(76,175,80,0.08); }
.title-item.discarded { opacity: 0.45; }
.title-idx { color: #666; min-width: 22px; font-size: 14px; text-align: right; }
.title-text { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.title-badge { font-size: 14px; padding: 1px 6px; border-radius: 3px; white-space: nowrap; }
.badge-pass { background: rgba(76,175,80,0.2); color: #4caf50; }
.badge-skip { background: rgba(255,152,0,0.15); color: #ff9800; }

.stage2-events { display: flex; flex-direction: column; gap: 8px; }
.event-card { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.event-header { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; flex-wrap: wrap; }
.event-title { flex: 1; font-size: 14px; font-weight: 600; color: #e0e0e0; min-width: 0; }
.event-score { font-size: 14px; color: #888; white-space: nowrap; }
.event-body { display: flex; flex-direction: column; gap: 4px; }
.event-row { display: flex; align-items: flex-start; gap: 8px; font-size: 14px; }
.event-label { color: #888; min-width: 32px; flex-shrink: 0; }
.event-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.mini-tag { font-size: 14px; padding: 1px 6px; border-radius: 3px; background: #2a2a3e; color: #aaa; }
.mini-tag.sector { color: #64b5f6; background: rgba(100,181,246,0.1); }
.mini-tag.stock { color: #ff9800; background: rgba(255,152,0,0.1); }
.event-reason { color: #aaa; line-height: 1.4; }

.signal-list { display: flex; flex-direction: column; gap: 8px; }
.signal-item { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.sig-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.sig-code { font-family: monospace; color: #4fc3f7; font-size: 14px; font-weight: 600; }
.sig-name { color: #e0e0e0; font-size: 14px; }
.sig-strategy { font-size: 14px; color: #b388ff; background: rgba(179,136,255,0.12); padding: 1px 6px; border-radius: 3px; }
.sig-conf { font-size: 14px; color: #888; }
.sig-price { font-size: 14px; color: #FAAD14; }
.sig-body { display: flex; align-items: center; gap: 8px; margin-top: 5px; font-size: 14px; flex-wrap: wrap; }
.sig-sector { font-size: 14px; color: #64b5f6; background: rgba(100,181,246,0.1); padding: 1px 6px; border-radius: 3px; }
.sig-reason { color: #aaa; line-height: 1.4; }

.tag { font-size: 14px; padding: 2px 7px; border-radius: 4px; white-space: nowrap; }
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

/* ====== Mobile ====== */
@media (max-width: 768px) {
  .log-modal { width: 96%; max-height: 92vh; }
  .log-toolbar { flex-wrap: wrap; gap: 6px; }
  .log-select { max-width: 100%; }
  .log-tabs { flex-wrap: wrap; gap: 4px; }
  .log-tab { font-size: 14px; padding: 5px 10px; }
}
</style>
