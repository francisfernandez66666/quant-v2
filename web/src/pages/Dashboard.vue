<!--
  仪表盘 Dashboard.vue (Dashboard page)
  首页概览：信号统计卡片、热门个股快照、宏观/IPO日历、热门板块、资讯、策略信号列表、系统运行状态
  Home overview: signal stat cards, hot stock snapshot, macro/IPO calendars, hot sectors, news, signals, system status
-->
<template>
  <div class="dashboard">
    <button class="btn-log" @click="showLog = true">📋 日志</button>
    <LogModal :visible="showLog" @close="showLog = false" />
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
          <!-- 快照行：代码/名称/板块/现价/涨跌幅，红涨绿跌；板块悬停可看异动原因 -->
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
          <!-- 宏观日历事件行：日期（MM-DD）+ 事件标题 -->
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
          <!-- IPO 事件行：日期 + 名称（代码）+ 发行价 + 上市倒计时状态（L=已上市） -->
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
        <!-- 热门板块行：涨幅 + 板块名 + 净流入金额（只展示前 5 名） -->
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
        <!-- 资讯行：时间 + 标题 + 方向/影响/板块/个股标签（最多展示前 15 条） -->
        <div class="news-scroll">
          <div v-for="(n, i) in newsItems.slice(0, 15)" :key="'n'+i" class="news-row">
            <div class="news-head">
              <span class="news-time">{{ fmtNewsTime(n.datetime) }}</span>
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

    <!-- 系统运行信息 -->
    <div class="card" style="margin-top: 16px;">
      <div class="card-header">系统</div>
      <!-- 运行信息行：运行时长 / 快照规模（股+板块） / 原始信号到最终信号的转换数 -->
      <div class="status-row-inline">
        <span>运行 {{ status.uptime || '-' }}</span>
        <span>数据源：东财{{ dataSourceHealth.eastmoney ? '●' : '○' }} 新浪{{ dataSourceHealth.sina ? '●' : '○' }} 腾讯{{ dataSourceHealth.tencent ? '●' : '○' }} 同花顺{{ dataSourceHealth.ths ? '●' : '○' }}</span>
        <span>新闻：财联社{{ newsSourceHealth.cainanshe ? '●' : '○' }} 同花顺{{ newsSourceHealth.kuaixun ? '●' : '○' }} 新浪{{ newsSourceHealth.sina ? '●' : '○' }}</span>
        <span>快照 {{ scanStats.total_stocks || 0 }}股 / {{ scanStats.hot_sector_count || 0 }}板块</span>
        <span>原始 {{ scanStats.raw_signals || 0 }} → 最终 {{ scanStats.final_signals || 0 }}</span>
        <span>流程引擎：新闻抓取{{ engineHealth.news_agent ? "●" : "○" }} 策略引擎{{ engineHealth.strategy_engine ? "●" : "○" }} 板块验证{{ engineHealth.sector_agent ? "●" : "○" }} 战法扫描{{ engineHealth.combat_agent ? "●" : "○" }} LLM{{ engineHealth.llm ? "●" : "○" }} 同花顺{{ engineHealth.ths ? "●" : "○" }} 聚合器{{ engineHealth.aggregator ? "●" : "○" }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'  // Vue 组合式 API：响应式 ref、计算属性、挂载/卸载生命周期钩子 (Vue composition API: ref, computed, mount/unmount hooks)
import * as api from '../api/index.js'                       // 后端 API 封装：信号/状态/资讯/板块/快照/IPO 等数据接口 (backend API wrapper: signals/status/news/sectors/snapshot/IPO)
import LogModal from '../components/LogModal.vue'            // 日志弹窗（LLM 批次 + 信号批次）(log modal: LLM batches + signal batches)

// ── 响应式数据 ── (Reactive data)
const signals = ref([])               // 策略信号列表（用于顶部统计卡片）(strategy signals list for the stat cards)
const status = ref({})                // 服务端状态 (server status)
const newsItems = ref([])             // 资讯+日历事件 (news + calendar events)
const hotSectors = ref([])            // 热门板块 (hot sectors)
const snapshotStocks = ref([])        // 热门个股快照 (hot stock snapshot)
const snapshotTime = ref('')          // 快照更新时间 (snapshot update time)
const ipoCalendar = ref([])           // IPO 日历 (IPO calendar)
const showLog = ref(false)            // 是否打开日志弹窗 (whether the log modal is open)
const dataSourceHealth = ref({})      // 数据源健康状况（东财/新浪/腾讯/同花顺）
const newsSourceHealth = ref({})      // 新闻源健康状况（财联社/同花顺/新浪）
const engineHealth = ref({})          // 流程引擎子系统健康状况

let timer = null                      // 定时轮询句柄 (polling timer handle)
let sseUnsub = null                   // SSE 取消订阅函数 (SSE unsubscribe function)
let visibilityHandler = null         // 页面可见性切换回调（暂停/恢复轮询）

// ── 计算属性 ── (Computed properties)
/** 扫描统计字段快捷引用（服务端状态里的 scan_stats 子对象，未返回时兜底为空对象） (Shortcut to scan_stats sub-object in server status; falls back to {} when absent) */
const scanStats = computed(() => status.value.scan_stats || {})

/** 强信号数量 (Count of strong signals) */
const strongCount = computed(() => signals.value.filter(s => s.remind_level === 'strong').length)
/** 观察中信号数量 (Count of signals under observation) */
const observeCount = computed(() => signals.value.filter(s => s.remind_level === 'observe').length)
/** 静默信号数量 (Count of muted signals) */
const muteCount = computed(() => signals.value.filter(s => s.remind_level === 'mute').length)

/** 过滤出宏观日历/政策反制事件（与普通资讯区分） (Filter calendar/policy-event items, separate from ordinary news) */
const calendarEvents = computed(() => newsItems.value.filter(n => n.source === '宏观日历' || n.source === '政策反制'))

/**
 * 计算 IPO 距今日的倒计时文本 (Compute a countdown text from today for an IPO date)
 * @param {object} c - IPO 日历项 (IPO calendar item)
 * @returns {string} 如 "3天后"、"📌今天"、"5天前" (e.g. "3 days later" / "today" / "5 days ago")
 */
function ipoCountdown(c) {
  const ds = c.listing_date || c.ipo_date
  if (!ds) return c.list_status === 'L' ? '已上市' : '即将上市'
  // 解析 YYYYMMDD 日期并计算与今天的相差天数 (parse YYYYMMDD string and compute day difference from today)
  const t = new Date(+ds.slice(0,4), +ds.slice(4,6)-1, +ds.slice(6,8))
  const diff = Math.ceil((t - Date.now()) / 86400000)
  if (diff > 0) return `${diff}天后`
  if (diff === 0) return '📌今天'
  return `${-diff}天前`
}

/**
 * 新闻时间格式化：兼容后端归一化后的 "YYYY-MM-DD HH:MM" 字符串，
 * 以及历史遗留的 epoch 秒（数字或纯数字字符串），统一显示 "MM-DD HH:MM"。
 * (Format news time: handles normalized "YYYY-MM-DD HH:MM" strings and legacy epoch
 * seconds (numeric or numeric-string), showing "MM-DD HH:MM".)
 */
function fmtNewsTime(dt) {
  if (dt === null || dt === undefined || dt === '') return ''
  const s = String(dt)
  if (/^\d+$/.test(s)) {
    const t = new Date(Number(s) * 1000)
    if (!isNaN(t.getTime())) {
      const mm = String(t.getMonth()+1).padStart(2,'0')
      const dd = String(t.getDate()).padStart(2,'0')
      const hh = String(t.getHours()).padStart(2,'0')
      const mi = String(t.getMinutes()).padStart(2,'0')
      return `${mm}-${dd} ${hh}:${mi}`
    }
    return ''
  }
  return s.length >= 16 ? s.slice(5, 16) : s
}

/**
 * 根据告警等级返回对应的 CSS 类名（当前未在模板中直接使用，保留以备扩展）
 * (Return a CSS class based on alert level; currently unused in the template, kept for future use)
 */
function alertLevelClass(level) {
  if (level.includes('信号') || level.includes('买入')) return 'tag-strong'
  if (level.includes('观察')) return 'tag-observe'
  if (level.includes('止盈') || level.includes('止损')) return 'tag-warn'
  return 'tag-info'
}

/** 并发加载所有仪表盘数据（6个接口） (Load all dashboard data concurrently — 6 API endpoints) */
async function load() {
  // 并发拉取 6 个数据源，单个失败不阻塞整体 (fetch 6 sources in parallel; a single failure does not block the rest)
  const [sigRes, stRes, newsRes, secRes, snapRes, ipoRes] = await Promise.allSettled([
    api.fetchSignals(), api.fetchStatus(), api.fetchNews(true), api.fetchSectorHot(), api.fetchHotSnapshot(), api.fetchIPOCalendar()
  ])
  if (sigRes.status === 'fulfilled' && Array.isArray(sigRes.value)) {
    // 写入策略信号列表 (store the strategy signal list)
    signals.value = sigRes.value
  }
  if (stRes.status === 'fulfilled' && stRes.value) {
    // 写入服务端状态 (store the server status)
    status.value = stRes.value
  }
  if (newsRes.status === 'fulfilled' && Array.isArray(newsRes.value)) {
    // 写入资讯与日历事件 (store news and calendar events)
    newsItems.value = newsRes.value
  }
  if (secRes.status === 'fulfilled' && Array.isArray(secRes.value)) {
    // 写入热门板块 (store hot sectors)
    hotSectors.value = secRes.value
  }
  if (snapRes.status === 'fulfilled' && Array.isArray(snapRes.value) && snapRes.value.length) {
    // 写入热门个股快照并记录更新时间 (store the hot stock snapshot and record its update time)
    snapshotStocks.value = snapRes.value
    snapshotTime.value = new Date().toLocaleTimeString()
  }
  if (ipoRes.status === 'fulfilled' && Array.isArray(ipoRes.value)) {
    // 写入 IPO 日历 (store the IPO calendar)
    ipoCalendar.value = ipoRes.value
  }
}

/** SSE 消息触发重新加载 (Reload all data when an SSE message arrives) */
function handleSSE(msg) {
  if (msg && typeof msg === 'object') {
    // 收到 SSE 推送即刷新全部数据 (refresh everything on any SSE push)
    load()
  }
}

/** 挂载时首次加载并启动定时轮询 + SSE (On mount, load once and start polling + SSE) */
// 说明：轮询由 2s 降到 5s，并在页面不可见时暂停，大幅降低对后端行情/资讯数据源的请求洪峰
// （同一份后端结果跨设备一致，轮询仅用于补足 SSE 偶发丢失）。
// (Poll interval reduced from 2s to 5s and paused while the tab is hidden, cutting the load
// on quote/news data sources. The backend result is identical across devices; polling only
// backfills occasional SSE drops.)
onMounted(() => {
  load()
  timer = setInterval(load, 5000)
  // 订阅后端 SSE 事件 (subscribe to backend SSE events)
  api.connectSSE()
  sseUnsub = api.onSSE(handleSSE)
  // 页面不可见时暂停轮询，切回时立即补拉一次 (pause polling when hidden; refresh on return)
  visibilityHandler = () => {
    if (document.hidden) {
      if (timer) { clearInterval(timer); timer = null }
    } else {
      if (!timer) {
        load()
        timer = setInterval(load, 5000)
      }
    }
  }
  document.addEventListener('visibilitychange', visibilityHandler)
  // 初次加载：探测数据源健康状况 (probe data source health on first load)
  api.fetchDataSourceHealth().then(r => { dataSourceHealth.value = r })
  // 初次加载：探测新闻资讯源健康状况 (probe news source health on first load)
  api.fetchNewsSourceHealth().then(r => { newsSourceHealth.value = r })
  // 初次加载：探测流程引擎子系统健康状况 (probe engine health on first load)
  api.fetchEngineHealth().then(r => { engineHealth.value = (r && r.engine) || r || {} })
})
/** 卸载时清理定时器和 SSE 订阅 (Clean up the timer and SSE subscription on unmount) */
onUnmounted(() => {
  // 清理定时器与订阅，避免泄漏 (clear the timer and subscription to avoid leaks)
  if (timer) clearInterval(timer)
  if (visibilityHandler) document.removeEventListener('visibilitychange', visibilityHandler)
  if (sseUnsub) sseUnsub()
})
</script>

<style scoped>
.dashboard { max-width: 1200px; position: relative; }
.btn-log {
  position: absolute; top: 0; right: 0; z-index: 10;
  padding: 6px 14px; border-radius: 6px; border: 1px solid #b388ff;
  background: transparent; color: #b388ff; font-size: 13px; cursor: pointer;
}
.btn-log:hover { background: rgba(179,136,255,0.1); }
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
