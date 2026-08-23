<!--
  盘口面板 DepthPanel.vue（Order-book panel: 买卖五档 + 派生因子）
  展示个股买卖盘口：卖一~卖五 / 买一~买五 价格与委托量（手），以及委比、买卖压力、封单量等因子。
  免费数据源仅返回五档（Level-2 十档需付费），但后端数据结构按十档预留——
  组件按后端 levels 渲染实际返回档位数，接入十档数据源后无需改动。
  Displays a stock's order book: ask1~ask5 / bid1~bid5 prices & volumes, plus derived factors
  (bid-ask ratio, pressure, seal volumes). Free sources return 5 levels only (Level-2 costs money),
  but the backend pre-allocates 10; this component renders whatever `levels` the API reports, so
  switching to a Level-2 feed needs no frontend change.

  用法 / Usage:
    <DepthPanel :code="code" :height="220" />
  组件自管理加载：按 code 拉取 /api/depth/{code}，挂载即加载，code 变化自动刷新。
  The component loads itself: fetches /api/depth/{code} on mount for the given code and reloads when it changes.
-->
<template>
  <div class="depth-panel">
    <div class="depth-toolbar">
      <span class="depth-title">{{ ob.name || code }} · 盘口</span>
      <span v-if="ob.time" class="depth-time">{{ ob.time }} <i v-if="ob.source" class="src">{{ ob.source }}</i></span>
      <button class="btn-refresh" :disabled="loading" @click="load">刷新</button>
    </div>

    <div v-if="loading" class="depth-state">加载中…</div>
    <div v-else-if="error" class="depth-state">{{ error }}</div>

    <template v-else-if="ob.bids && ob.bids.length">
      <div class="depth-body">
        <!-- 卖盘（倒序：卖五在最上，卖一贴近现价） -->
        <div v-for="i in askRows" :key="'a' + i" class="depth-row ask">
          <span class="lv">{{ '卖' + askLabel(i) }}</span>
          <span class="px" :class="askColor(i)">{{ fmtPrice(ob.asks[askIdx(i)].price) }}</span>
          <span class="vol">{{ fmtVol(ob.asks[askIdx(i)].volume) }}</span>
        </div>

        <!-- 现价行 -->
        <div class="depth-row now">
          <span class="lv">{{ ob.name || code }}</span>
          <span class="px" :class="nowCls">{{ fmtPrice(ob.price) }}</span>
          <span class="vol">{{ pctText }}</span>
        </div>

        <!-- 买盘（正序：买一在最上） -->
        <div v-for="i in bidRows" :key="'b' + i" class="depth-row bid">
          <span class="lv">{{ '买' + bidLabel(i) }}</span>
          <span class="px" :class="bidColor(i)">{{ fmtPrice(ob.bids[bidIdx(i)].price) }}</span>
          <span class="vol">{{ fmtVol(ob.bids[bidIdx(i)].volume) }}</span>
        </div>
      </div>

      <!-- 因子区 -->
      <div v-if="factors" class="depth-factors">
        <div class="f-row">
          <span class="f-label">委比</span>
          <b :class="factors.bid_ask_ratio >= 0 ? 'up' : 'down'">
            {{ (factors.bid_ask_ratio * 100).toFixed(1) }}%
          </b>
          <span class="f-label">买/卖量</span>
          <b>{{ fmtVol(factors.bid_vol) }} / {{ fmtVol(factors.ask_vol) }}</b>
        </div>
        <div class="f-row">
          <span class="f-label">买一封单</span>
          <b class="up">{{ fmtVol(factors.seal_bid) }}</b>
          <span class="f-label">卖一封单</span>
          <b class="down">{{ fmtVol(factors.seal_ask) }}</b>
        </div>
        <div class="f-row">
          <span class="f-label">价差</span>
          <b>{{ factors.spread_pct.toFixed(3) }}%</b>
          <span class="f-label">覆盖</span>
          <b>{{ factors.near_pct.toFixed(2) }}%</b>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import * as api from '../api/index.js'

const props = defineProps({
  /** 股票代码（必填） */
  code: { type: String, required: true },
  /** 初始显示名称 */
  name: { type: String, default: '' },
  /** 面板高度（px），默认 260 */
  height: { type: Number, default: 260 },
})

// 盘口原始数据：{ name, code, price, prev_close, time, source, levels, bids[], asks[], factors }
const ob = ref({})
// 由后端算好的派生因子（委比、买卖压力、封单、价差等），数据源不支持时为 null
const factors = ref(null)
// 拉取中标记，禁用刷新按钮并显示"加载中…"
const loading = ref(false)
// 加载失败时的错误文案，非空时替代盘口内容展示
const error = ref('')
// 盘口显示名，优先取后端返回的股票名称，其次为父组件传入的 name
const name = ref(props.name)

// 实际档位数：后端 levels（免费源 5，十档接入后为 10）；无则按数组长度推导
const levels = computed(() => Number(ob.value.levels) || Math.min(ob.value.bids?.length || 0, 5) || 5)
// 实际展示的档位数（受可用档位与最大档数限制）
const showLevels = computed(() => Math.min(levels.value, 10))

// 卖盘渲染行：最上层为第 showLevels 档（卖五/卖十），向下递减到卖一。
function askRows() { return Array.from({ length: showLevels.value }, (_, i) => i) }
// 卖盘档位标签（从近到远）
function askLabel(i) { return showLevels.value - i }
// 卖盘档位索引换算
function askIdx(i) { return showLevels.value - 1 - i }
// 买盘渲染行：最上层为买一，向下递增到第 showLevels 档。
function bidRows() { return Array.from({ length: showLevels.value }, (_, i) => i) }
// 买盘档位标签
function bidLabel(i) { return i + 1 }
// 买盘档位索引换算
function bidIdx(i) { return i }

// 价格格式化（保留 2 位小数）
function fmtPrice(v) {
  const n = Number(v) || 0
  return n ? n.toFixed(2) : '--'
}
// 量格式化（万/亿缩写）
function fmtVol(v) {
  const n = Number(v) || 0
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return n ? String(Math.round(n)) : '--'
}

// 现价相对昨收的涨跌幅百分比文本（如 +2.35%），用于现价行的涨幅展示
const pctText = computed(() => {
  const p = ob.value.price || 0
  const pc = ob.value.prev_close || 0
  if (!p || !pc) return '--'
  const d = (p - pc) / pc * 100
  return (d >= 0 ? '+' : '') + d.toFixed(2) + '%'
})
// 现价涨跌对应的颜色样式：上涨红色、下跌绿色（A股配色）
const nowCls = computed(() => (pctText.value.startsWith('+') ? 'up' : 'down'))

// 盘口颜色约定：卖盘绿（卖出价） / 买盘红（买入价）
function askColor(i) { return 'down' }   // 卖盘绿
function bidColor(i) { return 'up' }     // 买盘红

// 按 code 拉取盘口：成功则整体替换 ob 并取回因子/名称，失败记录 error；任何情况下复位 loading
async function load() {
  if (!props.code) return
  loading.value = true
  error.value = ''
  try {
    const data = await api.fetchDepth(props.code)
    if (data && data.bids) {
      ob.value = data
      factors.value = data.factors || null
      if (data.name) name.value = data.name
    } else {
      error.value = '盘口数据格式异常'
    }
  } catch (e) {
    error.value = e && e.message ? e.message : '盘口加载失败'
  } finally {
    loading.value = false
  }
}

// code 变化时先清空旧盘口与名称，再拉取新股盘口（组件复用于不同股票场景）
watch(() => props.code, () => {
  ob.value = {}
  factors.value = null
  name.value = props.name || ''
  load()
})

// 挂载即拉取一次盘口
onMounted(load)
</script>

<style scoped>
.depth-panel {
  background: #101828;
  border-radius: 8px;
  padding: 8px 10px 10px;
  color: #cdd9e9;
  font-size: 14px;
}
.depth-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.depth-title { font-weight: 600; color: #e8f0fe; }
.depth-time { color: #7d8fab; font-size: 13px; }
.depth-time i.src { color: #5b8ff9; font-style: normal; margin-left: 4px; }
.btn-refresh {
  margin-left: auto;
  background: #1d2b45;
  color: #cdd9e9;
  border: 1px solid #2e4161;
  border-radius: 4px;
  padding: 2px 10px;
  cursor: pointer;
  font-size: 14px;
}
.btn-refresh:hover { background: #27406a; }
.btn-refresh:disabled { opacity: 0.55; cursor: default; }
.depth-state { text-align: center; padding: 18px 0; color: #8fa3bf; }
.depth-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.depth-row {
  display: grid;
  grid-template-columns: 56px 1fr 1fr;
  align-items: center;
  padding: 2px 6px;
  border-radius: 4px;
}
.depth-row .lv { color: #7d8fab; }
.depth-row .px { font-weight: 600; font-family: monospace; text-align: right; }
.depth-row .vol { text-align: right; font-family: monospace; color: #b8c7dd; }
.depth-row.ask .px { color: #3ddc84; }     /* 卖盘绿 */
.depth-row.bid .px { color: #ff6b5a; }     /* 买盘红 */
.depth-row.now { background: #16223a; }
.depth-row.now .px { color: #e8f0fe; }
.depth-row.now .lv { color: #e8f0fe; font-weight: 600; }
.depth-factors {
  margin-top: 8px;
  border-top: 1px solid #24334d;
  padding-top: 6px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
}
.f-row { display: flex; gap: 8px; align-items: center; }
.f-label { color: #7d8fab; min-width: 52px; }
.f-row b { color: #e8f0fe; font-weight: 600; font-family: monospace; min-width: 70px; }
.f-row b.up { color: #ff6b5a; }
.f-row b.down { color: #3ddc84; }
</style>
