<!--
  热点页面 Hotspot.vue
  展示热点板块（含异动原因弹窗）、全市场个股评分排名、宏观日历、IPO日历、热点资讯
-->
<template>
  <div class="hotspot-page">
    <!-- 热点板块卡片网格 -->
    <div class="card">
      <div class="card-header">
        <span>🔥 热点板块</span>
        <select class="hot-round-select" v-model="hotRoundIdx" :disabled="hotRecords.length < 2" @change="applyHotRound">
          <option v-for="(r, i) in hotRecords" :key="r.process_time" :value="i">
            {{ formatHotTime(r.process_time) }}（{{ r.sectors.length }} 板块）
          </option>
        </select>
      </div>
      <!-- 板块卡片：名称/异动原因摘要/评分/涨幅/涨停数/净流入，点击弹出异动详情 -->
      <div class="sector-grid" v-if="sectors.length">
        <div v-for="s in sectors" :key="s.code" class="sector-card" @click="showReason(s)">
          <div class="sec-name">{{ s.name }}</div>
          <div v-if="s.reason" class="sec-reason">{{ shortReason(s.reason) }}</div>
          <div class="sec-score">{{ Math.round((s.score || 0) * 100) }}分</div>
          <div :class="['sec-pct', (s.change_pct || 0) >= 0 ? 'up' : 'down']">
            {{ (s.change_pct || 0) > 0 ? '+' : '' }}{{ (s.change_pct || 0).toFixed(2) }}%
          </div>
          <div class="sec-meta">
            <span v-if="(s.d1 || 0) > 0" class="d1-badge">D1 {{ s.d1.toFixed(0) }}</span>
            <span>涨停 {{ s.limitup_cnt || 0 }}</span>
            <span>流入 {{ s.net_inflow ? (s.net_inflow / 1e8).toFixed(1) + '亿' : '—' }}</span>
          </div>
        </div>
      </div>
      <div class="empty" v-else>暂无热点板块数据</div>
    </div>

    <!-- 热点板块异动原因弹窗 -->
    <div class="modal-overlay" v-if="reasonTarget" @click="reasonTarget = null"></div>
    <div class="modal" v-if="reasonTarget">
      <div class="modal-header">{{ reasonTarget.name }}</div>
      <div class="modal-body">
        <!-- 异动原因详情段落：优先展示 reason_detail，缺失时回退到 reason -->
        <div class="modal-section">
          <div class="modal-subtitle">板块异动原因</div>
          <div class="modal-reason">{{ reasonTarget.reason_detail || reasonTarget.reason || '暂无' }}</div>
        </div>
        <!-- 触发新闻列表：列出与本次异动相关的新闻标题 -->
        <div v-if="reasonTarget.news_titles && reasonTarget.news_titles.length" class="modal-section">
          <div class="modal-subtitle">触发新闻（{{ reasonTarget.news_titles.length }}条）</div>
          <div v-for="(t, i) in reasonTarget.news_titles" :key="i" class="modal-news-item">
            <span class="modal-news-idx">{{ i + 1 }}.</span>
            <span class="modal-news-title">{{ t }}</span>
          </div>
        </div>
      </div>
      <button class="modal-close" @click="reasonTarget = null">知道了</button>
    </div>

    <!-- 个股全维度评分排名（含排序功能） -->
    <div class="card" style="margin-top: 14px;">
      <div class="card-header">
        <span>📊 个股评分排名</span>
        <span class="card-sub">N形≥60 / 龙头≥70 / 双凸≥70 / 回头≥60 / 动量≥50</span>
      </div>
      <div class="eval-table" v-if="evals.length">
        <div class="ev-header">
          <span class="ev-code sortable" @click="setSort('code')">代码{{ sortArrow('code') }}</span>
          <span class="ev-name sortable" @click="setSort('name')">名称{{ sortArrow('name') }}</span>
          <span class="ev-price sortable" @click="setSort('price')">现价{{ sortArrow('price') }}</span>
          <span class="ev-chg sortable" @click="setSort('change_pct')">涨跌{{ sortArrow('change_pct') }}</span>
          <span class="ev-n sortable" @click="setSort('n_score')" title="N形≥60可操作">N≥60{{ sortArrow('n_score') }}</span>
          <span class="ev-dragon sortable" @click="setSort('dragon_score')" title="龙头≥70买入,≥50观察">龙≥70{{ sortArrow('dragon_score') }}</span>
          <span class="ev-db sortable" @click="setSort('db_score')" title="双凸≥70买入,50-70观察">凸≥70{{ sortArrow('db_score') }}</span>
          <span class="ev-dr sortable" @click="setSort('dr_score')" title="龙回头≥60首次入场">回≥60{{ sortArrow('dr_score') }}</span>
          <span class="ev-m sortable" @click="setSort('m_score')" title="动量≥50值得看">量≥50{{ sortArrow('m_score') }}</span>
        </div>
        <!-- 评分行：代码/名称/现价/涨跌 + 五维评分，强势或达标时整行高亮 -->
        <div class="ev-body">
          <div v-for="e in sortedEvals" :key="e.code" :class="rowClass(e)">
            <span class="ev-code">{{ e.code }}</span>
            <span class="ev-name">{{ e.name || '-' }}</span>
            <span class="ev-price">¥{{ (e.price || 0).toFixed(2) }}</span>
            <span :class="['ev-chg', (e.change_pct || 0) >= 0 ? 'up' : 'down']">
              {{ (e.change_pct || 0) > 0 ? '+' : '' }}{{ (e.change_pct || 0).toFixed(2) }}%
            </span>
            <span :class="scoreClass(e.n_score, e.n_pass, 80)">{{ e.n_score > 0 ? e.n_score.toFixed(0) : '—' }}</span>
            <span :class="scoreClass(e.dragon_score, e.dragon_pass, 80)">{{ e.dragon_score > 0 ? e.dragon_score.toFixed(0) : '—' }}</span>
            <span :class="scoreClass(e.db_score, e.db_pass, 80)">{{ e.db_score > 0 ? e.db_score.toFixed(0) : '—' }}</span>
            <span :class="scoreClass(e.dr_score, e.dr_pass, 80)">{{ e.dr_score > 0 ? e.dr_score.toFixed(0) : '—' }}</span>
            <span :class="scoreClass(e.m_score, e.m_pass, 70)">{{ e.m_score > 0 ? e.m_score.toFixed(0) : '—' }}</span>
          </div>
        </div>
      </div>
      <div class="empty" v-else>
        <span class="loading-dot"></span> 等待评估结果...
      </div>
      <div class="legend">
        <span class="lg-strong">≥80 强势</span>
        <span class="lg-pass">≥门槛 达标</span>
        <span class="lg-low">&lt;门槛 偏低</span>
        <span class="lg-sep">|</span>
        <span class="lg-item">N形≥60操作, 龙头≥70买入/≥50观察, 双凸≥70买入/50-70观察, 回头≥60入场, 动量≥50关注</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">点击表头排序</span>
      </div>
    </div>

    <!-- 宏观日历 -->
    <div class="card" style="margin-top: 14px;">
      <div class="card-header">📅 宏观日历</div>
      <!-- 宏观日历事件行：日期（MM-DD）+ 事件标题 -->
      <div class="hs-cal-scroll">
        <div v-for="(c, i) in calendarEvents" :key="'c'+i" class="hs-cal-item">
          <span class="hs-cal-date">{{ c.datetime ? c.datetime.slice(5, 10) : '' }}</span>
          <span class="hs-cal-title">{{ c.title }}</span>
        </div>
        <div v-if="!calendarEvents.length" class="hs-cal-empty">暂无日历事件</div>
      </div>
    </div>

    <!-- IPO日历 -->
    <div class="card" style="margin-top: 14px;">
      <div class="card-header">📋 IPO日历</div>
      <!-- IPO 事件行：日期 + 名称（代码）+ 发行价 + 上市状态（L=已上市） -->
      <div class="hs-cal-scroll">
        <div v-for="(c, i) in ipoCalendar" :key="'ipo'+i" class="hs-cal-item">
          <span class="hs-cal-date">{{ c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : '') }}</span>
          <span class="hs-cal-title">{{ c.name }}（{{ c.code }}）</span>
          <span v-if="c.issue_price" class="cal-price">¥{{ c.issue_price.toFixed(2) }}</span>
              <span :class="['cal-status', c.list_status === 'L' ? 'cal-status-l' : 'cal-status-u']">{{ ipoCountdown(c) }}</span>
        </div>
        <div v-if="!ipoCalendar.length" class="hs-cal-empty">暂无IPO日历</div>
      </div>
    </div>

    <!-- 热点资讯列表 -->
    <div class="card" style="margin-top: 14px;">
      <div class="card-header">📰 热点资讯</div>
      <!-- 资讯行：时间 + 标题 + 情绪/方向/影响/板块/个股标签 -->
      <div v-if="newsItems.length" class="hs-news-scroll">
        <div v-for="(n, i) in newsItems" :key="i" class="hs-news-item">
          <div class="hs-news-head">
            <span class="hs-news-time">{{ n.datetime ? n.datetime.slice(5, 16) : '' }}</span>
            <span class="hs-news-title">{{ n.title }}</span>
          </div>
          <div class="hs-news-tags">
            <span v-if="n.sentiment" :class="['tag', 'tag-sent-' + n.sentiment]">{{ n.sentiment }}</span>
            <span v-if="n.direction" :class="['tag', n.direction === '利好' ? 'tag-up' : n.direction === '利空' ? 'tag-down' : 'tag-neutral']">{{ n.direction }}</span>
            <span v-if="n.impact_level" :class="['tag', 'tag-impact-' + n.impact_level]">{{ n.impact_level }}影响</span>
            <span v-if="n.sectors?.length" v-for="sec in n.sectors" :key="sec" class="sector-tag">{{ sec }}</span>
            <span v-if="n.stocks?.length" v-for="stk in n.stocks" :key="stk" class="stock-tag">{{ stk }}</span>
            <span v-if="!n.sentiment && !n.direction && !n.sectors?.length" class="tag-placeholder"></span>
          </div>
        </div>
      </div>
      <div class="empty" v-else>暂无资讯</div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'  // Vue 组合式 API：响应式 ref、计算属性、挂载/卸载生命周期钩子
import * as api from '../api/index.js'                       // 后端 API 封装：状态/评分/板块轮次/资讯/IPO 等数据接口

// ── 工具函数 ──

/** 截取板块异动原因的前半段（第一个逗号前或前18字） */
function shortReason(r) {
  if (!r) return ''
  // 取第一个中文逗号前的摘要，否则截取前 18 字
  const idx = r.indexOf('，')
  return idx > 0 ? r.slice(0, idx) : r.slice(0, 18)
}

/** 根据评分阈值返回 CSS 类名 */
function scoreClass(score, pass, strongMin) {
  if (!score || score <= 0) return 'ev-score'
  // 达强势阈值标红，过门槛标黄
  if (score >= strongMin) return 'ev-score strong'
  if (pass) return 'ev-score pass'
  return 'ev-score'
}

/** 根据多维度评分判断行样式 */
function rowClass(e) {
  // 任一分维度达强势阈值即整行高亮
  const strong = (e.n_score || 0) >= 80 || (e.dragon_score || 0) >= 80 || (e.db_score || 0) >= 80 || (e.dr_score || 0) >= 80 || (e.m_score || 0) >= 70
  if (strong) return 'ev-row strong'
  const watch = (e.n_score || 0) >= 60 || (e.dragon_score || 0) >= 70 || (e.db_score || 0) >= 70 || (e.dr_score || 0) >= 60 || (e.m_score || 0) >= 50
  if (watch) return 'ev-row watch'
  return 'ev-row'
}

// ── 排序状态 ──
const sortKey = ref('')
const sortDir = ref(-1)

/** 设置排序列：同列再次点击切换升降序，切换列时默认降序 */
function setSort(key) {
  if (sortKey.value === key) {
    // 同列再次点击时切换升降序
    sortDir.value *= -1
  } else {
    sortKey.value = key
    sortDir.value = -1
  }
}

/** 返回排序列的箭头指示符：▼ 降序 / ▲ 升序，非排序列返回空串 */
function sortArrow(key) {
  if (sortKey.value !== key) return ''
  return sortDir.value === -1 ? ' ▼' : ' ▲'
}

/** 安全取值：字符串为空返回 ''，其余为空返回 0，用于排序比较 */
function val(e, key) {
  const v = e[key]
  if (typeof v === 'string') return v || ''
  return v || 0
}

// ── 响应式数据 ──
const sectors = ref([])           // 热点板块（当前选中轮次）
const hotRecords = ref([])        // 当日热点板块轮次记录（持久化）
const hotRoundIdx = ref(0)        // 选中轮次索引（默认最新）
const evals = ref([])             // 全市场个股评分
const news = ref([])              // 资讯 + 日历事件
const ipoCalendar = ref([])       // IPO 日历
const reasonTarget = ref(null)    // 当前查看异动原因的板块

/** 展示板块异动原因弹窗（点击板块卡片触发） */
function showReason(s) { reasonTarget.value = s }

/** 切换轮次记录：展示该轮次板块快照 */
function applyHotRound() {
  const r = hotRecords.value[hotRoundIdx.value]
  // 将选中轮次的板块快照设为当前展示
  sectors.value = r ? r.sectors || [] : []
}

/** 格式化轮次时间为 MM-DD HH:mm:ss */
function formatHotTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  const p = n => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

let timer = null                  // 定时轮询句柄
let unsubSSE = null               // SSE 取消订阅函数
let loading = false              // 防并发请求标志

// ── 计算属性 ──
/** 过滤出非日历的普通资讯 */
const newsItems = computed(() => news.value.filter(n => n.source !== '宏观日历'))
/** 过滤出宏观日历事件 */
const calendarEvents = computed(() => news.value.filter(n => n.source === '宏观日历'))

/** IPO 倒计时文本 */
function ipoCountdown(c) {
  const ds = c.listing_date || c.ipo_date
  if (!ds) return c.list_status === 'L' ? '已上市' : '即将上市'
  // 解析 YYYYMMDD 日期并计算与今天的相差天数
  const t = new Date(+ds.slice(0,4), +ds.slice(4,6)-1, +ds.slice(6,8))
  const diff = Math.ceil((t - Date.now()) / 86400000)
  if (diff > 0) return `${diff}天后`
  if (diff === 0) return '📌今天'
  return `${-diff}天前`
}

/** 全市场最高评分（用于缩放显示） */
const maxScore = computed(() => {
  let m = 0
  // 遍历所有个股取各维度最高分
  for (const e of evals.value) {
    const t = Math.max(e.n_score || 0, e.dragon_score || 0, e.db_score || 0, e.dr_score || 0, e.m_score || 0)
    if (t > m) m = t
  }
  return m || 100
})

/** 按排序列和方向排序后的评分列表 */
const sortedEvals = computed(() => {
  if (!evals.value || !evals.value.length) return []
  const arr = [...evals.value]
  const sk = sortKey.value
  if (!sk) {
    // 未选排序列时按各维度最高分降序
    return arr.sort((a, b) => {
      const sa = Math.max(a.n_score || 0, a.dragon_score || 0, a.db_score || 0, a.dr_score || 0, a.m_score || 0)
      const sb = Math.max(b.n_score || 0, b.dragon_score || 0, b.db_score || 0, b.dr_score || 0, b.m_score || 0)
      return sb - sa
    })
  }
  const dir = sortDir.value
  // 按指定列排序，字符串用 localeCompare
  return arr.sort((a, b) => {
    const va = val(a, sk)
    const vb = val(b, sk)
    if (typeof va === 'string') return va.localeCompare(vb) * dir
    return (va - vb) * dir
  })
})

// ── 数据加载 ──

/** 加载板块热点、评分、资讯、IPO 数据（带防并发） */
async function load() {
  if (loading) return
  loading = true
  try {
    const st = await api.fetchStatus()
    api.setLastSession(st.session)
    if (api.isTradingSession(st.session) || !evals.value.length) {
      try {
        // 交易时段内刷新全市场评分
        const e = await api.fetchEvaluations()
        if (e) evals.value = e
      } catch (_) {}
    }
  } catch (_) {}
  let fromRecords = false
  try {
    // 优先加载轮次记录，用于切换历史快照
    const recs = await api.fetchSectorHotRecords()
    if (Array.isArray(recs) && recs.length) {
      hotRecords.value = recs
      hotRoundIdx.value = 0
      applyHotRound()
      fromRecords = true
    }
  } catch (_) {}
  if (!fromRecords) {
    try {
      // 无轮次记录时直接拉取当前热点板块
      const s = await api.fetchSectorHot()
      if (s) sectors.value = s
    } catch (_) {}
  }
  try {
    const n = await api.fetchNews(true)
    if (n) news.value = n
  } catch (_) {}
  try {
    const ipo = await api.fetchIPOCalendar()
    if (ipo) ipoCalendar.value = ipo
  } catch (_) {}
  loading = false
}

/** SSE 触发刷新 */
function handleSSE() { load() }

onMounted(() => {
  load()
  // 每 3 秒轮询一次热点数据
  timer = setInterval(load, 3000)
  // 订阅后端 SSE 推送
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
})
onUnmounted(() => {
  // 清理定时器与 SSE 订阅
  if (timer) clearInterval(timer)
  if (unsubSSE) { unsubSSE(); unsubSSE = null }
})
</script>

<style scoped>
.hotspot-page { max-width: 1200px; }
.card { background: #1a1a2e; border-radius: 8px; padding: 14px; }
.card-header { font-size: 14px; font-weight: 600; color: #ccc; margin-bottom: 10px; display: flex; align-items: center; justify-content: space-between; }
.card-sub { font-size: 11px; color: #666; font-weight: 400; }
.hot-round-select {
  padding: 4px 8px; border-radius: 5px; border: 1px solid #333;
  background: #1a1a2e; color: #ccc; font-size: 11px; cursor: pointer; max-width: 200px;
}

.sector-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 10px; }
.sector-card { background: #0f0f23; border-radius: 6px; padding: 12px; cursor: pointer; }
.sector-card:active { opacity: 0.8; }
.sec-name { font-size: 14px; font-weight: 600; color: #e0e0e0; }
.sec-reason {
  font-size: 11px; color: #4fc3f7; margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  background: rgba(79,195,247,0.08); border-radius: 3px; padding: 1px 6px; display: inline-block; max-width: 100%;
}

.modal-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,0.6);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal {
  position: fixed; top: 50%; left: 50%; transform: translate(-50%, -50%); z-index: 101;
  background: #1a1a2e; border-radius: 10px; padding: 20px; width: 85%; max-width: 360px;
  text-align: center;
}
.modal-header { font-size: 14px; font-weight: 600; color: #e0e0e0; margin-bottom: 10px; }
.modal-body { font-size: 12px; color: #ccc; line-height: 1.6; margin-bottom: 16px; word-break: break-word; text-align: left; max-height: 60vh; overflow-y: auto; }
.modal-section { margin-bottom: 12px; }
.modal-section:last-child { margin-bottom: 0; }
.modal-subtitle { font-size: 11px; color: #888; font-weight: 600; margin-bottom: 4px; border-bottom: 1px solid #2a2a3e; padding-bottom: 3px; }
.modal-reason { font-size: 12px; color: #e0e0e0; line-height: 1.7; white-space: pre-line; }
.modal-news-item { display: flex; gap: 4px; padding: 2px 0; font-size: 12px; line-height: 1.5; }
.modal-news-idx { color: #666; flex-shrink: 0; min-width: 18px; }
.modal-news-title { color: #ccc; }
.modal-close {
  padding: 8px 28px; border-radius: 8px; border: none;
  background: #FF4D4F; color: #fff; font-size: 13px; cursor: pointer;
}
.modal-close:active { opacity: 0.8; }
.sec-score { font-size: 11px; color: #FAAD14; margin-top: 4px; }
.sec-pct { font-size: 20px; font-weight: 700; margin-top: 4px; }
.sec-pct.up { color: #FF4D4F; }
.sec-pct.down { color: #4caf50; }
.sec-meta { display: flex; gap: 12px; font-size: 11px; color: #666; margin-top: 6px; }
.d1-badge { color: #b388ff; background: rgba(179,136,255,0.15); padding: 0 5px; border-radius: 3px; font-weight: 600; }

.eval-table { font-size: 12px; }
.ev-header, .ev-row { display: flex; align-items: center; padding: 4px 0; gap: 4px; }
.ev-header { color: #555; border-bottom: 1px solid #2a2a3e; font-weight: 600; }
.ev-row { border-bottom: 1px solid #1a1a26; }
.ev-row.watch { background: rgba(250,173,20,0.08); }
.ev-row.strong { background: rgba(255,77,79,0.10); }
.ev-row:last-child { border-bottom: none; }
.ev-code { width: 64px; font-family: monospace; color: #4fc3f7; }
.ev-name { width: 72px; color: #ccc; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ev-price { width: 70px; text-align: right; color: #e0e0e0; }
.ev-chg { width: 62px; text-align: right; font-weight: 600; }
.ev-chg.up { color: #FF4D4F; }
.ev-chg.down { color: #4caf50; }
.ev-score, .ev-n, .ev-dragon, .ev-db, .ev-dr, .ev-m { width: 40px; text-align: center; color: #555; font-weight: 600; }
.ev-score.pass { color: #FAAD14; }
.ev-score.strong { color: #FF4D4F; }
.sortable { cursor: pointer; user-select: none; }
.sortable:hover { color: #ccc; }
.ev-body { max-height: 400px; overflow-y: auto; }
.ev-body::-webkit-scrollbar { width: 4px; }
.ev-body::-webkit-scrollbar-thumb { background: #333; border-radius: 2px; }

.hs-cal-scroll { max-height: 80px; overflow-y: auto; }
.hs-cal-item { display: flex; align-items: center; gap: 8px; padding: 3px 0; font-size: 12px; }
.hs-cal-date { color: #888; width: 76px; flex-shrink: 0; font-size: 11px; }
.hs-cal-title { flex: 1; color: #e0e0e0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.hs-cal-empty { font-size: 12px; color: #555; padding: 6px 0; }
.cal-price { width: 60px; text-align: right; color: #4fc3f7; font-size: 11px; flex-shrink: 0; }
.cal-status { width: 56px; text-align: right; font-size: 10px; padding: 1px 6px; border-radius: 3px; flex-shrink: 0; }
.cal-status-l { color: #888; background: rgba(136,136,136,0.15); }
.cal-status-u { color: #FAAD14; background: rgba(250,173,20,0.15); }
.hs-news-scroll { max-height: 400px; overflow-y: auto; }
.hs-news-item { padding: 6px 0; border-bottom: 1px solid #1a1a26; }
.hs-news-item:last-child { border-bottom: none; }
.hs-news-head { display: flex; gap: 8px; align-items: flex-start; }
.hs-news-time { color: #888; width: 76px; flex-shrink: 0; font-size: 11px; line-height: 16px; }
.hs-news-title { flex: 1; color: #ccc; font-size: 12px; line-height: 16px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hs-news-tags { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 3px; margin-left: 84px; min-height: 16px; }
.tag-placeholder { display: inline-block; width: 1px; height: 16px; }
.tag { font-size: 10px; padding: 1px 6px; border-radius: 3px; white-space: nowrap; }
.tag-sent-正面 { background: #1a3a1a; color: #4caf50; }
.tag-sent-负面 { background: #3a1a1a; color: #ff4d4f; }
.tag-sent-中性 { background: #2a2a2a; color: #999; }
.tag-up { background: #1a3a1a; color: #4caf50; }
.tag-down { background: #3a1a1a; color: #ff4d4f; }
.tag-neutral { background: #2a2a2a; color: #999; }
.tag-impact-高 { background: #3a2a1a; color: #faad14; }
.tag-impact-中 { background: #2a2a3a; color: #4fc3f7; }
.tag-impact-低 { background: #2a2a2a; color: #666; }
.tag-type { background: #1a2a3a; color: #4fc3f7; }
.tag-strategy { background: #2a1a3a; color: #b388ff; }
.tag-urgent { background: #3a1a1a; color: #ff4d4f; font-weight: 600; }
.tag-watch { background: #3a2a1a; color: #faad14; }
.tag-obs { background: #2a2a2a; color: #666; }
.sector-label { font-size: 10px; color: #555; padding: 1px 0; }
.sector-tag { font-size: 10px; padding: 1px 6px; border-radius: 3px; background: rgba(179,136,255,0.15); color: #b388ff; white-space: nowrap; }
.stock-tag { font-size: 10px; padding: 1px 6px; border-radius: 3px; background: rgba(255,152,0,0.15); color: #FF9800; font-family: monospace; }
.empty { text-align: center; padding: 30px; color: #555; font-size: 12px; }
.loading-dot { display: inline-block; width: 6px; height: 6px; background: #4fc3f7; border-radius: 50%; margin-right: 6px; animation: pulse 1.5s infinite; }
@keyframes pulse { 0% { opacity: 1; } 50% { opacity: 0.3; } 100% { opacity: 1; } }
.legend {
  margin-top: 8px; padding: 6px 12px; font-size: 11px; color: #666;
  background: #1a1a2e; border-radius: 6px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}
.lg-strong { color: #FF4D4F; }
.lg-pass { color: #FAAD14; }
.lg-low { color: #555; }
.lg-sep { color: #333; }
.lg-item { color: #666; }
</style>
