<!--
  仪表盘 Dashboard.vue
  首页概览：信号统计卡片、热门个股快照、宏观/IPO日历、热门板块、资讯、策略信号列表、系统运行状态
-->
<template>
  <div class="dashboard">
    <!-- 统计卡片：强信号 / 观察中 / 静默 / 监控个股数 -->
    <div class="stats-row">
      <div class="stat-card strong">
        <div class="stat-num">{{ strongCount }}</div>
        <div class="stat-label">强信号</div>
      </div>
      <div class="stat-card observe">
        <div class="stat-num">{{ observeCount }}</div>
        <div class="stat-label">观察中</div>
      </div>
      <div class="stat-card mute">
        <div class="stat-num">{{ muteCount }}</div>
        <div class="stat-label">静默</div>
      </div>
      <div class="stat-card holding">
        <div class="stat-num">{{ scanStats.total_stocks || snapshotStocks.length || 0 }}</div>
        <div class="stat-label">监控个股</div>
      </div>
    </div>

    <div class="grid-2col">
      <!-- 左栏：热门个股快照 -->
      <div class="card">
        <div class="card-header">
          <span>🔥 热门个股 <span class="badge-live">LIVE</span></span>
          <span class="card-sub">{{ snapshotTime }}</span>
        </div>
        <div class="stock-table" v-if="snapshotStocks.length">
          <div class="st-header">
            <span class="st-code">代码</span>
            <span class="st-name">名称</span>
            <span class="st-sector">板块</span>
            <span class="st-price">现价</span>
            <span class="st-chg">涨跌</span>
          </div>
          <div class="st-body">
            <div v-for="s in snapshotStocks" :key="s.code" class="st-row">
              <span class="st-code">{{ s.code }}</span>
              <span class="st-name">{{ s.name }}</span>
              <span class="st-sector" :title="s.sector_reason || ''">{{ s.sector || '—' }}</span>
              <span class="st-price">¥{{ (s.price || 0).toFixed(2) }}</span>
              <span :class="['st-chg', (s.change_pct || 0) >= 0 ? 'up' : 'down']">
                {{ (s.change_pct || 0) > 0 ? '+' : '' }}{{ (s.change_pct || 0).toFixed(2) }}%
              </span>
            </div>
          </div>
        </div>
        <div class="empty" v-else>
          <span class="loading-dot"></span> 等待行情数据...
        </div>
      </div>

      <!-- 右栏：最新动态（日历 + IPO + 板块 + 资讯） -->
      <div class="card">
        <div class="card-header">
          <span>最新动态</span>
          <span class="card-sub">{{ newsItems.length + hotSectors.length }}条</span>
        </div>
        <!-- 宏观日历 -->
        <div class="cal-section">
          <div class="section-label">📅 宏观日历</div>
          <div class="cal-scroll">
            <div v-for="(c, i) in calendarEvents" :key="'c'+i" class="cal-item">
              <span class="cal-date">{{ c.datetime ? c.datetime.slice(5, 10) : '' }}</span>
              <span class="cal-title">{{ c.title }}</span>
            </div>
            <div v-if="!calendarEvents.length" class="cal-empty">暂无日历事件</div>
          </div>
        </div>

        <div class="section-divider"></div>

        <!-- IPO日历 -->
        <div class="cal-section">
          <div class="section-label">📋 IPO日历</div>
          <div class="cal-scroll">
            <div v-for="(c, i) in ipoCalendar" :key="'ipo'+i" class="cal-item">
              <span class="cal-date">{{ c.listing_date ? c.listing_date.slice(5, 10) : (c.ipo_date ? c.ipo_date.slice(5, 10) : '') }}</span>
              <span class="cal-title">{{ c.name }}（{{ c.code }}）</span>
              <span v-if="c.issue_price" class="cal-price">¥{{ c.issue_price.toFixed(2) }}</span>
              <span :class="['cal-status', c.list_status === 'L' ? 'cal-status-l' : 'cal-status-u']">{{ ipoCountdown(c) }}</span>
            </div>
            <div v-if="!ipoCalendar.length" class="cal-empty">暂无IPO日历</div>
          </div>
        </div>

        <div class="section-divider"></div>

        <!-- 热门板块 -->
        <div v-if="hotSectors.length" class="section-label">🔥 热门板块</div>
        <div class="sec-scroll" v-if="hotSectors.length">
          <div v-for="(s, i) in hotSectors.slice(0, 5)" :key="'s'+i" class="sec-row">
            <span class="sec-pct">{{ (s.change_pct || 0) > 0 ? '+' : '' }}{{ (s.change_pct || 0).toFixed(1) }}%</span>
            <span class="sec-name">{{ s.name }}</span>
            <span class="sec-inflow">净流入 {{ s.net_inflow ? (s.net_inflow/1e8).toFixed(1)+'亿' : '—' }}</span>
          </div>
        </div>

        <div class="section-divider"></div>

        <!-- 资讯列表 -->
        <div v-if="newsItems.length" class="section-label">📰 资讯</div>
        <div class="news-scroll">
          <div v-for="(n, i) in newsItems.slice(0, 15)" :key="'n'+i" class="news-row">
            <div class="news-head">
              <span class="news-time">{{ n.datetime ? n.datetime.slice(5, 16) : '' }}</span>
              <span class="news-title-text">{{ n.title }}</span>
            </div>
            <div class="news-tags-line">
              <span v-if="n.direction" :class="['tag', n.direction === '利好' ? 'tag-up' : n.direction === '利空' ? 'tag-down' : 'tag-neutral']">{{ n.direction }}</span>
              <span v-if="n.impact_level" :class="['tag', 'tag-impact-' + n.impact_level]">{{ n.impact_level }}影响</span>
              <span v-if="n.sectors?.length" v-for="sec in n.sectors" :key="sec" class="sector-tag">{{ sec }}</span>
              <span v-if="n.stocks?.length" v-for="stk in n.stocks" :key="stk" class="stock-tag">{{ stk }}</span>
            </div>
          </div>
        </div>
        <div v-if="!newsItems.length && !hotSectors.length && !calendarEvents.length" class="empty">
          <span class="loading-dot"></span> 等待数据...
        </div>
      </div>
    </div>

    <!-- 策略信号迷你列表 -->
    <div class="card" style="margin-top: 16px;">
      <div class="card-header">
        <span>策略信号</span>
        <span class="card-sub">{{ signals.length }}条</span>
      </div>
      <div v-if="filteredSignals.length" class="signal-mini">
        <div v-for="s in filteredSignals" :key="s.code" class="sig-row" @click="$router.push('/signals')">
          <span class="sig-code">{{ s.code }}</span>
          <span class="sig-name">{{ s.name }}</span>
          <span class="sig-strat">{{ s.strategy }}</span>
          <div class="sig-bars">
            <div class="d-bar"><div class="d-fill d1" :style="{ width: (s.d1 || 0) + '%' }"></div></div>
            <div class="d-bar"><div class="d-fill d2" :style="{ width: (s.d2 || 0) + '%' }"></div></div>
            <div class="d-bar"><div class="d-fill d3" :style="{ width: (s.d3 || 0) + '%' }"></div></div>
            <div class="d-bar"><div class="d-fill d4" :style="{ width: (s.d4 || 0) + '%' }"></div></div>
          </div>
          <span :class="['sig-level', s.remind_level]">{{ s.total_score?.toFixed(0) }}</span>
        </div>
      </div>
      <div class="empty" v-else>
        <span class="loading-dot"></span> 扫描中，暂无触发信号...
      </div>
    </div>

    <!-- 系统运行信息 -->
    <div class="card" style="margin-top: 16px;">
      <div class="card-header">系统</div>
      <div class="status-row-inline">
        <span>运行 {{ status.uptime || '-' }}</span>
        <span>快照 {{ scanStats.total_stocks || 0 }}股 / {{ scanStats.hot_sector_count || 0 }}板块</span>
        <span>原始 {{ scanStats.raw_signals || 0 }} → 最终 {{ scanStats.final_signals || 0 }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'  // Vue 组合式 API：响应式 ref、计算属性、挂载/卸载生命周期钩子
import { useRouter } from 'vue-router'                       // Vue Router：获取路由实例，用于页面跳转
import * as api from '../api/index.js'                       // 后端 API 封装：信号/状态/资讯/板块/快照/IPO 等数据接口

const router = useRouter()                                   // 路由实例：点击策略信号行时跳转到信号详情页 /signals

// ── 响应式数据 ──
const signals = ref([])               // 策略信号列表
const status = ref({})                // 服务端状态
const newsItems = ref([])             // 资讯+日历事件
const hotSectors = ref([])            // 热门板块
const snapshotStocks = ref([])        // 热门个股快照
const snapshotTime = ref('')          // 快照更新时间
const ipoCalendar = ref([])           // IPO 日历

let timer = null                      // 定时轮询句柄
let sseUnsub = null                   // SSE 取消订阅函数

// ── 计算属性 ──
/** 扫描统计字段快捷引用（服务端状态里的 scan_stats 子对象，未返回时兜底为空对象） */
const scanStats = computed(() => status.value.scan_stats || {})

/** 强信号数量 */
const strongCount = computed(() => signals.value.filter(s => s.remind_level === 'strong').length)
/** 观察中信号数量 */
const observeCount = computed(() => signals.value.filter(s => s.remind_level === 'observe').length)
/** 静默信号数量 */
const muteCount = computed(() => signals.value.filter(s => s.remind_level === 'mute').length)

/** 截取前 10 条信号用于概览展示 */
const filteredSignals = computed(() => signals.value.slice(0, 10))

/** 过滤出宏观日历事件（与普通资讯区分） */
const calendarEvents = computed(() => newsItems.value.filter(n => n.source === '宏观日历'))

/**
 * 计算 IPO 距今日的倒计时文本
 * @param {object} c - IPO 日历项
 * @returns {string} 如 "3天后"、"📌今天"、"5天前"
 */
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

/**
 * 根据告警等级返回对应的 CSS 类名（当前未在模板中直接使用，保留以备扩展）
 */
function alertLevelClass(level) {
  if (level.includes('信号') || level.includes('买入')) return 'tag-strong'
  if (level.includes('观察')) return 'tag-observe'
  if (level.includes('止盈') || level.includes('止损')) return 'tag-warn'
  return 'tag-info'
}

/** 并发加载所有仪表盘数据（6个接口） */
async function load() {
  // 并发拉取 6 个数据源，单个失败不阻塞整体
  const [sigRes, stRes, newsRes, secRes, snapRes, ipoRes] = await Promise.allSettled([
    api.fetchSignals(), api.fetchStatus(), api.fetchNews(true), api.fetchSectorHot(), api.fetchHotSnapshot(), api.fetchIPOCalendar()
  ])
  if (sigRes.status === 'fulfilled' && Array.isArray(sigRes.value)) {
    // 写入策略信号列表
    signals.value = sigRes.value
  }
  if (stRes.status === 'fulfilled' && stRes.value) {
    // 写入服务端状态
    status.value = stRes.value
  }
  if (newsRes.status === 'fulfilled' && Array.isArray(newsRes.value)) {
    // 写入资讯与日历事件
    newsItems.value = newsRes.value
  }
  if (secRes.status === 'fulfilled' && Array.isArray(secRes.value)) {
    // 写入热门板块
    hotSectors.value = secRes.value
  }
  if (snapRes.status === 'fulfilled' && Array.isArray(snapRes.value) && snapRes.value.length) {
    // 写入热门个股快照并记录更新时间
    snapshotStocks.value = snapRes.value
    snapshotTime.value = new Date().toLocaleTimeString()
  }
  if (ipoRes.status === 'fulfilled' && Array.isArray(ipoRes.value)) {
    // 写入 IPO 日历
    ipoCalendar.value = ipoRes.value
  }
}

/** SSE 消息触发重新加载 */
function handleSSE(msg) {
  if (msg && typeof msg === 'object') {
    // 收到 SSE 推送即刷新全部数据
    load()
  }
}

/** 挂载时首次加载并启动 2 秒定时轮询 + SSE */
onMounted(() => {
  load()
  // 每 2 秒轮询刷新一次行情
  timer = setInterval(load, 2000)
  // 订阅后端 SSE 事件
  api.connectSSE()
  sseUnsub = api.onSSE(handleSSE)
})
/** 卸载时清理定时器和 SSE 订阅 */
onUnmounted(() => {
  // 清理定时器与订阅，避免泄漏
  if (timer) clearInterval(timer)
  if (sseUnsub) sseUnsub()
})
</script>

<style scoped>
.dashboard { max-width: 1200px; }
.stats-row { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 16px; }
.stat-card { text-align: center; padding: 16px; border-radius: 8px; background: #1a1a2e; }
.stat-card.strong .stat-num { color: #FF4D4F; }
.stat-card.observe .stat-num { color: #FAAD14; }
.stat-card.mute .stat-num { color: #888; }
.stat-card.holding .stat-num { color: #4fc3f7; }
.stat-num { font-size: 28px; font-weight: 700; }
.stat-label { font-size: 12px; color: #888; margin-top: 2px; }
.grid-2col { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
@media (max-width: 768px) {
  .grid-2col { grid-template-columns: 1fr; }
  .stats-row { gap: 8px; }
  .stats-row .stat-num { font-size: 28px; }
  .stock-table { font-size: 13px; }
  .st-code { width: 80px; }
  .st-sector { width: 60px; }
  .st-price { width: 90px; }
  .st-chg { width: 80px; }
}
.card { background: #1a1a2e; border-radius: 8px; padding: 14px; }
.card-header { display: flex; align-items: center; justify-content: space-between; font-size: 13px; font-weight: 600; color: #ccc; margin-bottom: 10px; }
.card-sub { font-size: 11px; color: #666; font-weight: 400; }
.badge-live { font-size: 10px; background: #FF4D4F; color: #fff; padding: 1px 6px; border-radius: 3px; margin-left: 6px; animation: pulse 2s infinite; }
@keyframes pulse { 0% { opacity: 1; } 50% { opacity: 0.5; } 100% { opacity: 1; } }

.stock-table { font-size: 12px; }
.st-header, .st-row { display: flex; align-items: center; padding: 4px 0; gap: 6px; }
.st-header { color: #555; border-bottom: 1px solid #2a2a3e; font-weight: 600; }
.st-row { border-bottom: 1px solid #1a1a26; }
.st-row:last-child { border-bottom: none; }
.st-code { width: 68px; font-family: monospace; color: #4fc3f7; }
.st-name { flex: 1; color: #ccc; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.st-sector { width: 80px; color: #b388ff; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11px; cursor: help; }
.st-price { width: 80px; text-align: right; color: #e0e0e0; font-weight: 600; }
.st-chg { width: 70px; text-align: right; font-weight: 600; }
.st-chg.up { color: #FF4D4F; }
.st-chg.down { color: #4caf50; }
.st-body { max-height: 300px; overflow-y: auto; }
.st-body::-webkit-scrollbar { width: 4px; }
.st-body::-webkit-scrollbar-thumb { background: #333; border-radius: 2px; }

.section-divider { height: 1px; background: #2a2a3e; margin: 8px 0; }
.cal-section { }
.cal-scroll { max-height: 80px; overflow-y: auto; }
.cal-item { display: flex; align-items: center; gap: 8px; padding: 3px 0; font-size: 12px; }
.cal-date { color: #888; width: 76px; flex-shrink: 0; font-size: 11px; }
.cal-title { flex: 1; color: #e0e0e0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; line-height: 16px; }
.cal-empty { font-size: 12px; color: #555; padding: 6px 0; }
.cal-price { width: 60px; text-align: right; color: #4fc3f7; font-size: 11px; flex-shrink: 0; }
.cal-status { width: 56px; text-align: right; font-size: 10px; padding: 1px 6px; border-radius: 3px; flex-shrink: 0; }
.cal-status-l { color: #888; background: rgba(136,136,136,0.15); }
.cal-status-u { color: #FAAD14; background: rgba(250,173,20,0.15); }
.sec-scroll { max-height: 110px; overflow-y: auto; }
.sec-row { display: flex; align-items: center; gap: 10px; padding: 4px 0; font-size: 12px; border-bottom: 1px solid #1f1f30; }
.sec-row:last-child { border-bottom: none; }
.sec-pct { width: 52px; text-align: right; color: #FF4D4F; font-weight: 600; flex-shrink: 0; }
.sec-pct.down { color: #4caf50; }
.sec-name { width: 64px; color: #e0e0e0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex-shrink: 0; }
.sec-inflow { flex: 1; color: #888; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.news-scroll { max-height: 260px; overflow-y: auto; }
.news-row { padding: 5px 0; border-bottom: 1px solid #1f1f30; }
.news-row:last-child { border-bottom: none; }
.news-head { display: flex; gap: 8px; align-items: flex-start; }
.news-time { color: #888; width: 76px; flex-shrink: 0; font-size: 11px; line-height: 16px; }
.news-title-text { flex: 1; color: #ccc; font-size: 12px; line-height: 16px; }
.news-tags-line { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 3px; margin-left: 84px; min-height: 16px; }
.tag { font-size: 10px; padding: 1px 6px; border-radius: 3px; }
.tag-up { background: rgba(76,175,80,0.15); color: #4caf50; }
.tag-down { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag-neutral { background: rgba(153,153,153,0.15); color: #999; }
.tag-impact-高 { background: rgba(250,173,20,0.15); color: #faad14; }
.tag-impact-中 { background: rgba(79,195,247,0.15); color: #4fc3f7; }
.tag-impact-低 { background: rgba(153,153,153,0.15); color: #666; }
.sector-tag { font-size: 10px; padding: 1px 6px; border-radius: 3px; background: rgba(179,136,255,0.15); color: #b388ff; }
.stock-tag { font-size: 10px; padding: 1px 6px; border-radius: 3px; background: rgba(255,152,0,0.15); color: #FF9800; font-family: monospace; }
.tag-strong { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag-observe { background: rgba(250,173,20,0.15); color: #FAAD14; }
.tag-warn { background: rgba(255,152,0,0.15); color: #FF9800; }
.tag-info { background: rgba(79,195,247,0.15); color: #4fc3f7; }

.signal-mini { font-size: 12px; }
.sig-row { display: flex; align-items: center; gap: 8px; padding: 6px 0; border-bottom: 1px solid #1a1a26; cursor: pointer; }
.sig-row:last-child { border-bottom: none; }
.sig-row:hover { background: rgba(255,255,255,0.02); }
.sig-code { width: 68px; font-family: monospace; color: #4fc3f7; }
.sig-name { width: 80px; color: #ccc; overflow: hidden; text-overflow: ellipsis; }
.sig-strat { width: 60px; color: #888; }
.sig-bars { flex: 1; display: flex; gap: 3px; }
.d-bar { flex: 1; height: 4px; background: #2a2a3e; border-radius: 2px; overflow: hidden; }
.d-fill { height: 100%; border-radius: 2px; }
.d-fill.d1 { background: #FF4D4F; }
.d-fill.d2 { background: #FAAD14; }
.d-fill.d3 { background: #4fc3f7; }
.d-fill.d4 { background: #4caf50; }
.sig-level { width: 30px; text-align: right; font-weight: 700; color: #FAAD14; }

.empty { text-align: center; padding: 30px; color: #555; font-size: 12px; }
.loading-dot { display: inline-block; width: 6px; height: 6px; background: #4fc3f7; border-radius: 50%; margin-right: 6px; animation: pulse 1.5s infinite; }
.status-row-inline { display: flex; gap: 20px; font-size: 12px; color: #666; flex-wrap: wrap; }
</style>
