<!--
  自选股页面 Watchlist.vue
  Watchlist page Watchlist.vue
  展示用户自选股列表，含多维评分（N形/龙头/双凸/龙回头/动量），支持添加/删除/排序
  Shows the user's watchlist with multi-dimension scores (N-shape/dragon/double-bump/dragon-return/momentum), supporting add/remove/sort
-->
<template>
  <div class="watchlist-page">
    <!-- 页头：标题 + 添加股票输入框（Header: title + add-stock input）-->
    <div class="page-header">
      <h2>自选股</h2>
      <div class="add-row">
        <input v-model="newCode" placeholder="输入代码 (如 000001)" @keyup.enter="add" :disabled="adding" />
        <button @click="add" class="btn-add" :disabled="adding">{{ adding ? '添加中…' : '添加' }}</button>
        <span v-if="feedback" class="feedback" :class="feedbackType">{{ feedback }}</span>
      </div>
    </div>

    <!-- 自选股评分表格（Watchlist score table）-->
    <div class="eval-table" v-if="stocks.length">
      <!-- 表头（可点击排序）（Header row, clickable to sort）-->
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
        <span class="ev-k">K线</span>
        <span class="ev-act">操作</span>
      </div>
      <div class="ev-body">
          <!-- 自选行 + 可展开 分时区（Watchlist row + expandable K-line area）-->
          <div v-for="e in sortedEvals" :key="e.code" class="ev-row-group">
          <div :class="rowClass(e)" @click="onRowTap(e)">
          <span class="ev-code" data-label="代码">{{ e.code }}</span>
          <span class="ev-name" data-label="名称">{{ e.name || '-' }}</span>
          <span class="ev-price" data-label="现价">¥{{ (e.price || 0).toFixed(2) }}</span>
          <span :class="['ev-chg', (e.change_pct || 0) >= 0 ? 'up' : 'down']" data-label="涨跌">
            {{ (e.change_pct || 0) > 0 ? '+' : '' }}{{ (e.change_pct || 0).toFixed(2) }}%
          </span>
          <span :class="scoreClass(e.n_score, e.n_pass, 80)" data-label="N形">{{ e.n_score > 0 ? e.n_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.dragon_score, e.dragon_pass, 80)" data-label="龙头">{{ e.dragon_score > 0 ? e.dragon_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.db_score, e.db_pass, 80)" data-label="双凸">{{ e.db_score > 0 ? e.db_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.dr_score, e.dr_pass, 80)" data-label="回头">{{ e.dr_score > 0 ? e.dr_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.m_score, e.m_pass, 70)" data-label="动量">{{ e.m_score > 0 ? e.m_score.toFixed(0) : '—' }}</span>
          <span data-label="K线"><button class="btn-kline" @click.stop="toggleKline(e.code)" :title="klineOpenCode === e.code ? '收起分时' : '展开分时'">{{ klineOpenCode === e.code ? '收起' : '分时' }}</button></span>
          <span data-label="操作"><button class="btn-remove" @click.stop="remove(e.code)">✕</button></span>
        </div>
        <!-- 展开的 分时区（全宽，位于该行下方）（Expanded K-line area, full width, below the row）-->
        <div v-if="klineOpenCode === e.code" class="ev-kline-row">
          <div class="kline-flex">
            <div class="kline-main"><KLineChart :key="e.code" :code="e.code" :name="e.name" /></div>
            <div class="depth-side"><DepthPanel :code="e.code" :name="e.name" /></div>
          </div>
        </div>
      </div>
      </div>
    </div>
    <div class="empty" v-else>暂无自选股，输入代码添加</div>

    <!-- 图例说明（Legend）-->
    <div class="legend">
      <span class="lg-strong">≥80 强势</span>
      <span class="lg-pass">≥门槛 达标</span>
      <span class="lg-low">&lt;门槛 偏低</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">N形≥60操作, 龙头≥70买入/≥50观察, 双凸≥70买入/50-70观察, 回头≥60入场, 动量≥50关注</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">点击表头排序</span>
    </div>

    <!-- 移动端：点击行弹出的底部操作菜单 -->
    <div class="sheet-overlay" v-if="sheetStock" @click="sheetStock = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetStock.code }} {{ sheetStock.name || '' }}</div>
        <button class="sheet-btn" @click="sheetToggleKline">
          {{ klineOpenCode === sheetStock.code ? '收起分时' : '展开分时' }}
        </button>
        <button class="sheet-btn sheet-danger" @click="sheetRemove">删除</button>
        <button class="sheet-btn sheet-cancel" @click="sheetStock = null">取消</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'  // Vue 组合式 API：响应式 ref、计算属性、监听器、生命周期钩子
// Vue Composition API: reactive refs, computed properties, watchers, and lifecycle hooks
import * as api from '../api/index.js'                             // 后端 API 封装：状态/快照/自选列表/评分等数据接口
// backend API wrapper: status/snapshot/watchlist/evaluations endpoints
import KLineChart from '../components/KLineChart.vue'              // 分时图组件（展开行展示）
// K-line chart component (shown in expanded rows)
import DepthPanel from '../components/DepthPanel.vue'              // 盘口面板（展开行展示，买卖五档）
// order-book panel (shown in expanded rows, 5 bid/ask levels)

// ── 响应式状态 ──
// ── Reactive state ──
const stocks = ref([])                // 自选股数据（含实时价格 + 评分）
// watchlist data (realtime prices + scores)
const newCode = ref('')               // 添加输入框的代码
// the code typed in the add input
const sortKey = ref('')               // 当前排序列
// the currently sorted column
const sortDir = ref(-1)               // 排序方向：-1 降序 / 1 升序
// sort direction: -1 descending / 1 ascending
const adding = ref(false)             // 是否正在添加
// whether an add is in progress
const feedback = ref('')              // 操作反馈文字
// action feedback text
const feedbackType = ref('ok')        // 反馈类型：'ok' | 'err'
// feedback type: 'ok' | 'err'
const sheetStock = ref(null)          // 移动端操作菜单当前选中的股票对象
// the stock object currently selected in the mobile action sheet

let timer = null                  // 定时轮询句柄
// polling timer handle

// ── 本地持久化镜像：进 tab 秒开，增删改才变更 ──
// ── Local persisted mirror: instant open on tab entry; only mutated on add/remove/edit ──
const CACHE_KEY = 'wl_cache_v1'   // localStorage 缓存键名
// localStorage cache key
/** 将当前自选股列表快照写入 localStorage 缓存（供下次进入页面秒开） */
/** Write the current watchlist snapshot into the localStorage cache (for instant open next time) */
function persistCache() {
  try { localStorage.setItem(CACHE_KEY, JSON.stringify(stocks.value)) } catch (_) {}
}
/** 从 localStorage 缓存恢复自选股列表；无缓存或解析失败时返回空数组 */
/** Restore the watchlist from the localStorage cache; returns an empty array when absent or unparseable */
function loadCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr : []
  } catch (_) { return [] }
}
/** 深度监听自选股列表变化，自动写入本地缓存 */
/** Deeply watch the watchlist and write it into the local cache automatically */
watch(stocks, persistCache, { deep: true })

/**
 * 根据评分值返回 CSS 类名
 * Return the CSS class based on a score
 * @param {number} score - 评分
 * @param {number} score - the score
 * @param {boolean} pass - 是否达门槛
 * @param {boolean} pass - whether the threshold is met
 * @param {number} strongMin - 强势阈值
 * @param {number} strongMin - the "strong" threshold
 * @returns {string}
 */
function scoreClass(score, pass, strongMin) {
  if (!score || score <= 0) return 'ev-score'
  // 达到强势阈值标红，达标（过门槛）标黄
  // Strong threshold turns it red; meeting the pass threshold turns it yellow
  if (score >= strongMin) return 'ev-score strong'
  if (pass) return 'ev-score pass'
  return 'ev-score'
}

/** 根据多维度评分判断行样式：强势 / 关注 / 普通 */
/** Decide the row style from multi-dimension scores: strong / watch / normal */
function rowClass(e) {
  // 任一分维度达到强势阈值即整行高亮
  // Highlight the whole row when any dimension reaches the strong threshold
  const strong = (e.n_score || 0) >= 80 || (e.dragon_score || 0) >= 80 || (e.db_score || 0) >= 80 || (e.dr_score || 0) >= 80 || (e.m_score || 0) >= 70
  if (strong) return 'ev-row strong'
  const watch = (e.n_score || 0) >= 60 || (e.dragon_score || 0) >= 70 || (e.db_score || 0) >= 70 || (e.dr_score || 0) >= 60 || (e.m_score || 0) >= 50
  if (watch) return 'ev-row watch'
  return 'ev-row'
}

// ── 排序逻辑 ──
// ── Sorting logic ──

/** 设置排序列，再次点击切换升降序 */
/** Set the sort column; clicking the same column again toggles asc/desc */
function setSort(key) {
  if (sortKey.value === key) {
    // 同列再次点击时切换升降序
    // Clicking the same column toggles the sort direction
    sortDir.value *= -1
  } else {
    sortKey.value = key
    sortDir.value = -1
  }
}

/** 返回排序列的箭头指示符 */
/** Return the arrow indicator for the sorted column */
function sortArrow(key) {
  if (sortKey.value !== key) return ''
  return sortDir.value === -1 ? ' ▼' : ' ▲'
}

/** 安全取值函数，处理字符串和数字 */
/** Safe value getter, handling both strings and numbers */
function val(e, key) {
  const v = e[key]
  if (typeof v === 'string') return v || ''
  return v || 0
}

/** 根据当前排序列和方向对自选股排序 */
/** Sort the watchlist by the current column and direction */
const sortedEvals = computed(() => {
  const arr = [...stocks.value]
  const sk = sortKey.value
  if (!sk) {
    // 未选排序列时按各维度最高分降序
    // No column chosen: sort by the highest dimension score, descending
    return arr.sort((a, b) => {
      const sa = Math.max(a.n_score || 0, a.dragon_score || 0, a.db_score || 0, a.dr_score || 0, a.m_score || 0)
      const sb = Math.max(b.n_score || 0, b.dragon_score || 0, b.db_score || 0, b.dr_score || 0, b.m_score || 0)
      return sb - sa
    })
  }
  const dir = sortDir.value
  // 按指定列排序，字符串用 localeCompare
  // Sort by the chosen column; strings use localeCompare
  return arr.sort((a, b) => {
    const va = val(a, sk)
    const vb = val(b, sk)
    if (typeof va === 'string') return va.localeCompare(vb) * dir
    return (va - vb) * dir
  })
})

// ── 数据加载 ──
// ── Data loading ──

/** 加载自选股数据，合并实时快照和多维评分，非交易时段跳过加载 */
/** Load watchlist data by merging the realtime snapshot with multi-dimension scores; skip loading outside trading hours */
async function load() {
  try {
    const st = await api.fetchStatus()
    // 非交易时段直接跳过，保留旧数据；
    // 例外：检测到空 code（历史 bug 写入的脏缓存）时仍强制刷新，否则分时展开判定会全行命中
    // Outside trading hours, keep old data; EXCEPT when rows have empty codes (stale dirty cache from an
    // earlier bug) — then force a refresh, otherwise the K-line expand check matches every row.
    const hasEmptyCode = stocks.value.some(s => !s.code)
    if (!api.isTradingSession(st.session) && stocks.value.length && !hasEmptyCode) {
      return
    }
    api.setLastSession(st.session)
    // 并发拉取快照、自选列表、评分三个接口
    // Concurrently fetch the snapshot, watchlist, and evaluations endpoints
    const [snap, wl, ev] = await Promise.all([
      api.fetchSnapshot(), api.fetchWatchlist(), api.fetchEvaluations()
    ])
    const wlStocks = (wl.stocks || []).map(c => (typeof c === 'object' ? c : { code: c }))
    const codes = wlStocks.map(c => c.code)
    if (!codes.length) { stocks.value = []; return }
    // 自选接口由后端实时行情填充权威 名称/现价/涨跌幅，作为快照/评分缺失时的兜底
    // The watchlist endpoint provides authoritative name/price/change from realtime quotes as a fallback when snapshot/evaluations lack them
    const wlMap = {}
    wlStocks.forEach(c => { wlMap[c.code] = c })
    // 构建评分映射表
    // Build the score lookup map
    const evMap = {}
    if (ev) ev.forEach(e => { evMap[e.code] = e })
    // 快照中查不到的自选股用占位数据填充（优先自选接口的名称/价格，避免显示乱码代码/0 价）
    // Watchlist stocks missing from the snapshot get placeholder rows (preferring the watchlist endpoint's name/price, avoiding garbled codes / 0 prices)
    // 兼容传入字符串或 {code} 对象两种调用方式：wlRow('000021') 与 wlRow({code:'000021'}) 均得到正确 code
    const wlRow = (c) => {
      const code = typeof c === 'string' ? c : (c && c.code)
      return {
        code,
        name: wlMap[code]?.name || evMap[code]?.name || code,
        price: Number(wlMap[code]?.price) || 0,
        change_pct: Number(wlMap[code]?.change_pct) || 0,
        n_score: evMap[code]?.n_score || 0, n_pass: evMap[code]?.n_pass || false,
        dragon_score: evMap[code]?.dragon_score || 0, dragon_pass: evMap[code]?.dragon_pass || false,
        db_score: evMap[code]?.db_score || 0, db_pass: evMap[code]?.db_pass || false,
        dr_score: evMap[code]?.dr_score || 0, dr_pass: evMap[code]?.dr_pass || false,
        m_score: evMap[code]?.m_score || 0, m_pass: evMap[code]?.m_pass || false,
      }
    }
    if (snap && snap.length) {
      stocks.value = snap
        .filter(s => codes.includes(s.code))
        .map(s => {
          const base = wlRow(s.code)
          return {
            ...base,
            name: s.name || base.name,
            price: Number(s.price) || base.price,
            change_pct: Number(s.change_pct) ?? base.change_pct,
          }
        })
    } else if (ev && ev.length) {
      stocks.value = ev.filter(e => codes.includes(e.code)).map(e => wlRow(e.code))
    } else {
      stocks.value = []
    }
    // 快照/评分里都查不到的自选股：用自选接口兜底，不再用"代码当名称 + 0 价格"污染列表
    // Watchlist stocks missing from both snapshot and scores fall back to the watchlist endpoint, instead of polluting the list with "code-as-name + 0 price"
    const known = {}
    stocks.value.forEach(s => { known[s.code] = true })
    for (const c of codes) {
      if (!known[c]) stocks.value.push(wlRow(c))
    }
    persistCache()
  } catch (_) { /* 网络失败时保留旧数据，不置空 */ }
  // On network failure keep the old data instead of clearing it
}

// ── 添加 / 删除 ──
// ── Add / remove ──

/** 添加自选股：后端同步 + 本地追加，不整表重载 */
/** Add a watchlist stock: sync to the backend + append locally, without a full reload */
async function add() {
  const code = newCode.value.trim()
  if (!code) return
  if (adding.value) return
  adding.value = true
  feedback.value = ''
  try {
    // 调用后端接口加入自选
    // Call the backend endpoint to add the stock
    const res = await api.addWatchlist(code)
    newCode.value = ''
    if (res && res.stock) {
      // 后端有返回则用返回数据本地追加行，避免整表重载
      // When the backend returns data, append that row locally to avoid a full reload
      const row = {
        code: res.stock.code || code,
        name: res.stock.name || code,
        price: res.stock.price || 0,
        change_pct: res.stock.change_pct || 0,
        n_score: 0, n_pass: false,
        dragon_score: 0, dragon_pass: false,
        db_score: 0, db_pass: false,
        dr_score: 0, dr_pass: false,
        m_score: 0, m_pass: false,
      }
      stocks.value = [...stocks.value.filter(s => s.code !== row.code), row]
    } else if (!res || !res.duplicate) {
      stocks.value.push({ code, name: code, price: 0, change_pct: 0 })
    }
    showFeedback('已添加 ' + code, 'ok')
  } catch (e) { showFeedback('添加失败: ' + e.message, 'err') }
  adding.value = false
}

/** 移除自选股：本地移除 + 后端同步 */
/** Remove a watchlist stock: remove locally + sync to the backend */
async function remove(code) {
  try {
    await api.removeWatchlist(code)
    // 后端移除成功后从本地列表删除
    // Delete from the local list after a successful backend removal
    stocks.value = stocks.value.filter(s => s.code !== code)
    showFeedback('已移除 ' + code, 'ok')
  } catch (e) { showFeedback('删除失败: ' + e.message, 'err') }
}

/** 显示临时反馈文字，2.5 秒后消失 */
/** Show temporary feedback text that disappears after 2.5 seconds */
function showFeedback(msg, type) {
  feedback.value = msg
  feedbackType.value = type || 'ok'
  // 2.5 秒后自动清空反馈
  // Auto-clear the feedback after 2.5 seconds
  setTimeout(() => { feedback.value = '' }, 2500)
}

/** 当前展开分时的自选代码（单值，互斥：同一时间只展开一只） */
/** Code of the currently expanded K-line (single value, exclusive) */
const klineOpenCode = ref('')

/** 展开/收起某自选股的分时区（单值互斥） */
/** Toggle a watchlist stock's K-line area (single-value exclusive) */
function toggleKline(code) {
  klineOpenCode.value = klineOpenCode.value === code ? '' : code
}

/** 移动端：点击行打开操作菜单 */
/** Mobile: tap a row to open the action sheet */
function onRowTap(e) {
  if (window.innerWidth > 768) return
  sheetStock.value = e
}
/** 移动端：操作菜单 - 展开/收起分时 */
function sheetToggleKline() {
  if (!sheetStock.value) return
  toggleKline(sheetStock.value.code)
  sheetStock.value = null
}
/** 移动端：操作菜单 - 删除 */
function sheetRemove() {
  if (!sheetStock.value) return
  const code = sheetStock.value.code
  sheetStock.value = null
  remove(code)
}

onMounted(() => {
  // 先读缓存秒开，再拉取最新，并 30s 轮询
  // Read the cache for an instant open, fetch fresh data, then poll every 30s
  stocks.value = loadCache()
  load()
  timer = setInterval(load, 30000)
})
/** 卸载时清理定时器，避免内存泄漏 */
/** Clear the timer on unmount to avoid a memory leak */
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.watchlist-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.add-row { display: flex; gap: 8px; }
.add-row input {
  padding: 8px 12px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; width: 160px; outline: none;
}
.add-row input:focus { border-color: #FF4D4F; }
.btn-add {
  padding: 8px 16px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 14px; cursor: pointer;
}
.btn-add:disabled { opacity: 0.5; cursor: not-allowed; }
.feedback { font-size: 14px; padding: 4px 10px; border-radius: 4px; white-space: nowrap; }
.feedback.ok { color: #4caf50; }
.feedback.err { color: #FF4D4F; }
.eval-table { font-size: 14px; background: #1a1a2e; border-radius: 8px; overflow-x: auto; white-space: nowrap; }
.ev-header, .ev-row { display: flex; align-items: center; padding: 4px 12px; gap: 0; min-width: 660px; }
.ev-header { background: #2a2a3e; color: #888; font-weight: 600; border-bottom: 1px solid #2a2a3e; }
.ev-row { border-bottom: 1px solid #1a1a26; }
.ev-row.watch { background: rgba(250,173,20,0.08); }
.ev-row.strong { background: rgba(255,77,79,0.10); }
.ev-row:last-child { border-bottom: none; }
.ev-body { max-height: 500px; overflow-y: auto; }
.ev-body::-webkit-scrollbar { width: 4px; }
.ev-body::-webkit-scrollbar-thumb { background: #333; border-radius: 2px; }
.ev-code { flex: 1; font-family: monospace; color: #4fc3f7; text-align: center; }
.ev-name { flex: 1; color: #ccc; overflow: hidden; text-overflow: ellipsis; }
.ev-price { flex: 1; text-align: center; color: #e0e0e0; }
.ev-chg { flex: 1; text-align: center; font-weight: 600; }
.ev-chg.up { color: #FF4D4F; }
.ev-chg.down { color: #4caf50; }
.ev-score, .ev-n, .ev-dragon, .ev-db, .ev-dr, .ev-m { flex: 1; text-align: center; color: #555; font-weight: 600; }
.ev-score.pass { color: #FAAD14; }
.ev-score.strong { color: #FF4D4F; }
.ev-act { flex: 0 0 30px; text-align: center; }
.ev-k { flex: 0 0 52px; text-align: center; color: #888; font-weight: 600; }
.ev-row-group { min-width: 660px; }
.btn-kline {
  background: transparent; border: 1px solid #3a3a55; color: #7ab8ff;
  border-radius: 4px; cursor: pointer; font-size: 14px; padding: 2px 8px;
}
.btn-kline:hover { border-color: #4fc3f7; color: #4fc3f7; }
.ev-kline-row { padding: 8px 12px; background: #16162a; border-bottom: 1px solid #1a1a26; }
.kline-flex { display: flex; gap: 12px; align-items: stretch; }
.kline-main { flex: 1 1 auto; min-width: 0; }
.depth-side { flex: 0 0 300px; }
@media (max-width: 720px) {
  .kline-flex { flex-direction: column; }
  .depth-side { flex: 1 1 auto; }
}
.sortable { cursor: pointer; user-select: none; }
.sortable:hover { color: #ccc; }
.btn-remove {
  background: transparent; border: 1px solid #333; color: #666;
  width: 22px; height: 22px; border-radius: 4px; cursor: pointer; font-size: 14px;
  display: flex; align-items: center; justify-content: center;
}
.btn-remove:hover { border-color: #FF4D4F; color: #FF4D4F; }
.empty { text-align: center; padding: 40px; color: #555; font-size: 14px; }
.legend {
  margin-top: 8px; padding: 6px 12px; font-size: 14px; color: #666;
  background: #1a1a2e; border-radius: 6px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}
.lg-strong { color: #FF4D4F; }
.lg-pass { color: #FAAD14; }
.lg-low { color: #555; }
.lg-sep { color: #333; }
.lg-item { color: #666; }

/* ====== Mobile: horizontal scroll + larger fonts ====== */
@media (max-width: 768px) {
  .eval-table { overflow-x: auto; white-space: nowrap; -webkit-overflow-scrolling: touch; }
  .ev-header, .ev-row { min-width: 780px; font-size: 14px; padding: 8px 12px; }
  .ev-header { display: flex; }
  .ev-body { max-height: none; overflow-y: visible; }
  .ev-row-group { min-width: 0; }
  .ev-row { cursor: pointer; }
  .ev-kline-row { padding: 6px; }
  .legend { font-size: 14px; gap: 6px; }
  .page-header { flex-direction: column; align-items: stretch; gap: 8px; }
  .add-row { width: 100%; }
  .add-row input { flex: 1; min-width: 0; }
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
  .sheet-cancel { background: #2a2a3e; color: #888; }
}
</style>
