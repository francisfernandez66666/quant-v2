<!--
  分时图组件 KLineChart.vue (Pure-SVG intraday chart: 分时 + 成交量 + MACD)
  无需第三方图表库：基于原生 SVG 绘制分时价格线（含昨收/均价参考线）+ 成交量柱 + MACD（DIF/DEA + 柱状），
  带价格 / 时间刻度与 hover 十字线 / 提示。
  No third-party chart lib: draws the intraday price line (with prev-close & average-line references),
  volume bars and MACD (DIF/DEA + histogram) in plain SVG, with price/time axes and a hover crosshair/tooltip.

  用法 / Usage:
    <KLineChart :code="code" :height="220" />
  组件自管理加载：按 code 拉取 /api/minute（默认 1 分钟、全天 241 点），挂载即加载。
  The component loads by itself: fetches /api/minute (1-minute, full-day 241 points) on mount for the given code.

  约定 / Conventions:
  - A股配色：当日较昨收上涨为红、下跌为绿（与页面涨跌色一致）。
    A-share colors: above prev close = red, below = green (consistent with the page's up/down colors).
  - MACD：柱为正红负绿，DIF 黄线、DEA 蓝线，与主流行情软件一致。
    MACD: histogram red positive / green negative, DIF yellow, DEA blue, matching mainstream charting apps.
  - 坐标按容器实际像素绘制（viewBox 与渲染尺寸 1:1），图形不失真。
    Geometry is computed in real container pixels (viewBox matches rendered size 1:1) so nothing gets stretched.
-->
<template>
  <div class="kline-chart">
    <div class="kline-toolbar">
      <span class="kline-title">
        {{ name || code }} · 分时
      </span>
      <span class="kline-summary" v-if="lastClose">
        现价 <b :class="last >= 0 ? 'up' : 'down'">{{ lastClose.toFixed(2) }}</b>
        涨跌 <b :class="last >= 0 ? 'up' : 'down'">{{ last >= 0 ? '+' : '' }}{{ last.toFixed(2) }}%</b>
      </span>
      <button class="btn-refresh" :disabled="loading" @click="load">刷新</button>
    </div>

    <!-- 加载 / 空 / 失败态 -->
    <div v-if="loading" class="kline-state">加载中…</div>
    <div v-else-if="error" class="kline-state">{{ error }}</div>
    <div v-else-if="points.length === 0" class="kline-state">暂无分时数据</div>

    <!-- 图表主体 -->
    <div v-show="!loading && !error && points.length > 0" ref="wrapRef" class="kline-wrap">
      <svg
        class="kline-svg"
        :viewBox="'0 0 ' + viewW + ' ' + viewH"
        :style="{ height: viewH + 'px' }"
        @mousemove="onMove"
        @mouseleave="onLeave"
      >
        <!-- 价格区网格 + 左侧价格刻度 -->
        <g v-for="g in priceGrid" :key="'pg' + g.y">
          <line :x1="plotL" :x2="viewW - plotR" :y1="g.y" :y2="g.y" class="grid-line" />
          <text :x="plotL - 4" :y="g.y + 3" class="axis-text" text-anchor="end">{{ g.label }}</text>
        </g>

        <!-- 昨收参考线（价格区） -->
        <line :x1="plotL" :x2="viewW - plotR" :y1="prevCloseY" :y2="prevCloseY" class="prev-line" />
        <text :x="viewW - plotR" :y="prevCloseY - 3" class="prev-label" text-anchor="end">{{ prevLabel }}</text>

        <!-- 分时价格线 -->
        <polyline :points="priceLine" :stroke="priceColor" stroke-width="1.4" fill="none" />
        <!-- 均价线 -->
        <polyline :points="avgLine" :stroke="avgColor" stroke-width="1" fill="none" />

        <!-- 成交量区网格分隔线 -->
        <line :x1="plotL" :x2="viewW - plotR" :y1="volTop" :y2="volTop" class="panel-line" />
        <!-- MACD 区上下分隔线 -->
        <line :x1="plotL" :x2="viewW - plotR" :y1="macdTop" :y2="macdTop" class="panel-line" />
        <!-- MACD 零轴 -->
        <line :x1="plotL" :x2="viewW - plotR" :y1="macdZero" :y2="macdZero" class="grid-line" />

        <!-- 成交量柱 -->
        <rect
          v-for="v in volBars"
          :key="'v' + v.i"
          :x="v.x" :y="v.y" :width="v.w" :height="v.h"
          :fill="v.color"
          fill-opacity="0.5"
        />

        <!-- MACD 柱状图 -->
        <rect
          v-for="m in macdBars"
          :key="'m' + m.i"
          :x="m.x" :y="m.y" :width="m.w" :height="m.h"
          :fill="m.color"
          fill-opacity="0.8"
        />
        <!-- MACD DIF / DEA 线 -->
        <polyline :points="difLine" :stroke="difColor" stroke-width="1" fill="none" />
        <polyline :points="deaLine" :stroke="deaColor" stroke-width="1" fill="none" />

        <!-- X 轴时间刻度 -->
        <text
          v-for="t in timeAxis"
          :key="'t' + t.x"
          :x="t.x" :y="viewH - 5"
          class="axis-text"
          text-anchor="middle"
        >{{ t.label }}</text>

        <!-- hover 十字线 + 提示 -->
        <g v-if="hover">
          <line :x1="hover.x" :x2="hover.x" :y1="plotT" :y2="viewH - axisB" class="crosshair" />
          <line :x1="plotL" :x2="viewW - plotR" :y1="hover.y" :y2="hover.y" class="crosshair" />
          <circle :cx="hover.x" :cy="hover.y" :r="3" class="hover-dot" />
        </g>
      </svg>

      <!-- hover 顶部价格标签 -->
      <div v-if="hover" class="kline-tip" :style="{ left: hover.tipX + 'px', top: '4px' }">
        <div class="tip-date">{{ hover.point.time }}</div>
        <div class="tip-row">价 <b :class="hover.delta >= 0 ? 'up' : 'down'">{{ hover.point.close.toFixed(2) }}</b>
          涨 <b :class="hover.delta >= 0 ? 'up' : 'down'">{{ hover.delta >= 0 ? '+' : '' }}{{ hover.pct.toFixed(2) }}%</b>
          均 <b>{{ (hover.avg || 0).toFixed(2) }}</b></div>
        <div class="tip-row">开 <b>{{ hover.point.open.toFixed(2) }}</b> 高 <b>{{ hover.point.high.toFixed(2) }}</b> 低 <b>{{ hover.point.low.toFixed(2) }}</b></div>
        <div class="tip-row">量 {{ fmtVol(hover.point.volume) }} · 额 {{ fmtAmt(hover.point.amount) }}</div>
        <div class="tip-row macd">DIF <b>{{ hover.point.dif.toFixed(3) }}</b> DEA <b>{{ hover.point.dea.toFixed(3) }}</b> BAR <b>{{ hover.point.bar.toFixed(3) }}</b></div>
      </div>
    </div>

    <!-- 图例 -->
    <div v-if="points.length > 0" class="kline-legend">
      <span><i style="background: #5b8ff9"></i>价格</span>
      <span><i style="background: #f6b73c"></i>均价</span>
      <span><i style="background: #f0b90b"></i>昨收</span>
      <span class="macd"><i style="background: #f6b73c"></i>DIF</span>
      <span class="macd"><i style="background: #5b8ff9"></i>DEA</span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import * as api from '../api/index.js'

// ── 输入属性 ──
// ── Props ──
const props = defineProps({
  /** 股票代码（必填） */
  code: { type: String, required: true },
  /** 初始显示名称（可后续由数据回填） */
  name: { type: String, default: '' },
  /** 图表高度（px），默认 220 */
  height: { type: Number, default: 220 },
  /** 分时周期分钟数，默认 1 */
  scale: { type: Number, default: 1 },
  /** 分时点数，默认 241（全天） */
  count: { type: Number, default: 241 },
})

// ── 布局常量（逻辑单位 = 实际像素）──
// ── Layout constants (logical units == real pixels) ──
const axisL = 50               // 左侧价格刻度宽度
const plotL = axisL            // 绘图区左边界
const plotR = 8                // 右留白
const axisB = 16               // 底部时间刻度高度
const plotT = 6                // 顶部留白
// 价格区 / 成交量区 / MACD 区高度分配（MACD 约 32%，成交量约 18%）——随自适应高度联动
const innerH = computed(() => viewH.value - plotT - axisB)
const macdH = computed(() => Math.round(innerH.value * 0.32))
const volH = computed(() => Math.round(innerH.value * 0.18))
const mainH = computed(() => innerH.value - macdH.value - volH.value)
// 分区边界 Y
const priceBottom = computed(() => plotT + mainH.value)   // 价格区底（= 成交量区顶）
const volTop = computed(() => priceBottom.value)          // 成交量区顶
const volBottom = computed(() => volTop.value + volH.value)
const macdTop = computed(() => volBottom.value)           // MACD 区顶
const macdZero = computed(() => macdTop.value + macdH.value / 2)

// ── 响应式状态 ──
// ── Reactive state ──
const points = ref([])         // 渲染用分时点（含坐标）
const raw = ref([])            // 原始分时数据（重算坐标用，避免重复请求）
const prevClose = ref(0)       // 昨收价
const loading = ref(false)     // 加载中
const error = ref('')          // 错误信息
const lastClose = ref(0)       // 最新价
const last = ref(0)            // 最新涨跌幅（%）
const name = ref(props.name)
const hover = ref(null)        // hover 状态
const wrapRef = ref(null)      // 容器 DOM，用于宽度自适应
const viewW = ref(axisL + 300) // 当前渲染宽度（px，随容器自适应）

// MACD 配色常量
const difColor = '#f6b73c'
const deaColor = '#5b8ff9'
const avgColor = '#f0b90b'
const priceColor = '#7cd6ff'

// ── 计算布局 ──
// ── Computed layout ──
// 高度自适应（拉长上下坐标）：默认取 props.height，但在容器很窄时按最小宽高比拉高，
// 保证分时线纵向伸展空间充足、不因横向挤压而压缩变形（viewBox 与渲染 1:1 仍不失真）。
// English: adaptive height — defaults to props.height, but when the container is narrow we raise the
// height to honor a minimum aspect ratio so the vertical axis stays tall/stretched and the line isn't
// squeezed down by a narrow horizontal span (viewBox still matches rendered size 1:1, so no distortion).
const viewH = computed(() => {
  const base = props.height
  const minRatio = 0.55 // 宽高比下限：宽度/高度 >= minRatio 时维持 base
  const minH = Math.round(viewW.value * minRatio)
  return Math.max(base, minH)
})
const plotW = computed(() => viewW.value - plotL - plotR)
// 昨日涨跌标签上的门槛展示
const prevLabel = computed(() => prevClose.value ? prevClose.value.toFixed(2) : '')

// 价格区范围：围绕昨收对称（分时图习惯），并始终包住全部价格（含均价），杜绝价格线冲出绘图区。
// - 至少 ±1.5% 的固定带（保证坐标足够高/够伸展，价格波动小时不至于贴在一条线上）；
// - 实际高低点超出固定带时自动加 15% 余量扩容，确保任何价格线都落在绘图区内、不越界。
// - 直播行情软件的日均分时即固定对称带，故此处优先对称、再兜底防溢出。
// English: price-area range symmetric around prev close (intraday convention), always covering every price
// (incl. the average line) so the line can never escape the plot area. A fixed band of at least ±1.5%
// keeps the axis tall/stretched even for flat prices; when the actual high/low exceeds that band we auto
// widen with 15% headroom so nothing clips.
const priceRange = computed(() => {
  const pc = prevClose.value || 0
  if (raw.value.length === 0) return { lo: 0, hi: 1 }
  let min = Infinity
  let max = -Infinity
  for (const p of raw.value) {
    if (p.high > max) max = p.high
    if (p.low < min) min = p.low
  }
  if (pc > 0 && max >= 0 && min >= 0) {
    // 固定对称带：至少 ±1.5%（可延伸拉长上下坐标）；若实际振幅更大则按振幅外扩 15% 余量
    const span = Math.max(0.015, (max - min) / (2 * pc))
    const half = span * 1.15
    return { lo: Math.min(pc * (1 - half), min - (max - min) * 0.05), hi: Math.max(pc * (1 + half), max + (max - min) * 0.05) }
  }
  const pad = (max - min) * 0.06 || 0.01
  return { lo: min - pad, hi: max + pad }
})

// 价格刻度线（5 条）+ 左侧标注
const priceGrid = computed(() => {
  if (points.value.length === 0) return []
  const { lo, hi } = priceRange.value
  const lines = []
  for (let i = 0; i <= 4; i++) {
    const v = lo + (hi - lo) * i / 4
    lines.push({ y: plotT + mainH.value - (v - lo) / (hi - lo) * mainH.value, label: v.toFixed(2) })
  }
  return lines
})

// X 轴时间刻度：均匀取 ≤6 个，显示 HH:MM
const timeAxis = computed(() => {
  const n = points.value.length
  if (n === 0) return []
  const count = Math.min(6, n)
  const out = []
  for (let i = 0; i < count; i++) {
    const idx = Math.round((n - 1) * i / (count - 1 || 1))
    const p = points.value[idx]
    out.push({ x: p.cx, label: (p.time || '').slice(11, 16) })
  }
  return out
})

// ── 数据构建 ──
// ── Geometry building ──
// 按当前宽度重算分时几何坐标（不重新请求），并刷新价格线/均价线/成交量/MACD 图元与最新价
function build() {
  const data = raw.value
  if (!Array.isArray(data) || data.length === 0) return
  const n = data.length
  const step = plotW.value / n

  points.value = data.map((p, i) => {
    const cx = plotL + step * i + step / 2
    return {
      i,
      raw: p,
      time: p.time || '',
      cx,
      yClose: priceY(p.close),
    }
  })

  buildLines()
  buildVolBars()
  buildMacdBars()

  const lastPoint = data[data.length - 1]
  lastClose.value = lastPoint.close
  last.value = prevClose.value > 0 ? ((lastPoint.close - prevClose.value) / prevClose.value) * 100 : 0
}

// 分时价格线 & 均价线 point 串
function buildLines() {
  const data = raw.value
  const pts = points.value.map(p => p.cx.toFixed(1) + ',' + p.yClose.toFixed(1)).join(' ')
  priceLine.value = pts

  // 累计均价 = 累计成交额 / 累计成交量（量与额同维度），按下标对齐原始数据
  avgPointsRaw.value = new Array(data.length).fill(0)
  const avgPts = []
  let cumAmt = 0
  let cumVol = 0
  for (let i = 0; i < data.length; i++) {
    cumAmt += data[i].amount || 0
    cumVol += data[i].volume || 0
    if (cumVol <= 0) continue
    const avg = cumAmt / cumVol
    avgPointsRaw.value[i] = avg
    const y = priceY(avg)
    avgPts.push(cxOf(i).toFixed(1) + ',' + y.toFixed(1))
  }
  avgLine.value = avgPts.join(' ')
}

// 成交量柱
function buildVolBars() {
  const data = raw.value
  let maxV = 0
  for (const p of data) if (p.volume > maxV) maxV = p.volume
  if (maxV <= 0) maxV = 1
  const n = data.length
  const step = plotW.value / n
  const w = Math.max(1, step * 0.6)
  volBars.value = data.map((p, i) => {
    const cx = plotL + step * i + step / 2
    const h = (p.volume / maxV) * volH.value
    const up = prevClose.value > 0 ? p.close >= prevClose.value : p.close >= p.open
    return {
      i,
      x: cx - w / 2,
      w,
      y: volBottom.value - h,
      h: Math.max(0.5, h),
      color: up ? '#e2432a' : '#1fa747',
    }
  })
}

// MACD 柱 + DIF/DEA 线
function buildMacdBars() {
  const data = raw.value
  let maxAbs = 0.0001
  for (const p of data) maxAbs = Math.max(maxAbs, Math.abs(p.bar), Math.abs(p.dif), Math.abs(p.dea))
  const n = data.length
  const step = plotW.value / n
  const w = Math.max(1, step * 0.55)
  const half = macdH.value / 2

  const dPts = []
  const ePts = []
  macdBars.value = data.map((p, i) => {
    const cx = plotL + step * i + step / 2
    const b = p.bar
    const hgt = (Math.abs(b) / maxAbs) * half
    const y = b >= 0 ? macdZero.value - hgt : macdZero.value
    dPts.push(cx.toFixed(1) + ',' + macdLineY(p.dif, maxAbs, half).toFixed(1))
    ePts.push(cx.toFixed(1) + ',' + macdLineY(p.dea, maxAbs, half).toFixed(1))
    return {
      i,
      x: cx - w / 2,
      w,
      y,
      h: Math.max(0.5, hgt),
      color: b >= 0 ? '#e2432a' : '#1fa747',
    }
  })
  difLine.value = dPts.join(' ')
  deaLine.value = ePts.join(' ')
}

// 昨收线 Y 坐标
const prevCloseY = computed(() => priceY(prevClose.value))

// 价格 → Y（价格区）
function priceY(v) {
  const { lo, hi } = priceRange.value
  return plotT + (hi - v) / (hi - lo) * mainH.value
}

// MACD 值 → Y（MACD 区，绕零轴）
function macdLineY(v, maxAbs, half) {
  return macdZero.value - (v / maxAbs) * half
}

// 中间态：points 构建时使用的辅助变量（保持在原始作用域）
// 以下 SVG 图元数据由 build() 根据当前宽度重算，改变容器大小后即重新生成
const priceLine = ref('')        // 分时价格线的 SVG polyline points 串
const avgLine = ref('')          // 均价线的 SVG polyline points 串
const volBars = ref([])          // 成交量柱数组 {x,y,w,h,color}
const macdBars = ref([])         // MACD 柱数组 {x,y,w,h,color}
const difLine = ref('')          // MACD DIF 线的 polyline points 串
const deaLine = ref('')          // MACD DEA 线的 polyline points 串
const avgPointsRaw = ref([])     // 每个数据下标对应的累计均价

// x 坐标由索引换算（平均线用）
function cxOf(idx) {
  const n = raw.value.length
  if (n === 0) return plotL
  const step = plotW.value / n
  return plotL + step * idx + step / 2
}

// hover 时该点累计均价
function avgAt(idx) {
  return avgPointsRaw.value[idx] || 0
}

// refit：读取容器实际宽度并重算坐标（保持 1:1，不分时失真）
function refit() {
  const el = wrapRef.value
  if (!el) return
  const w = el.clientWidth
  if (w && w !== viewW.value) {
    viewW.value = w
    if (points.value.length) nextTick(build)
  }
}

// ── 加载 ──
// ── Load ──
// 拉取分时数据并回填昨收/名称，随后重建全部图元坐标；失败写入 error 展示错误态
async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await api.fetchMinute(props.code, props.scale, props.count)
    const pts = data && Array.isArray(data.points) ? data.points : null
    if (!pts) {
      error.value = '分时数据格式异常'
      return
    }
    if (pts.length === 0) return
    raw.value = pts
    prevClose.value = Number(data.prev_close) || 0
    if (data.name) name.value = data.name
    build()
  } catch (e) {
    error.value = e && e.message ? e.message : '分时加载失败'
  } finally {
    loading.value = false
  }
}

// ── hover ──
// ── Hover ──
// 鼠标移动：按 X 距离找最近分时点，计算相对昨收的涨跌额/幅度与累计均价，驱动十字线与顶部提示框
function onMove(ev) {
  if (points.value.length === 0) return
  const rect = ev.currentTarget.getBoundingClientRect()
  const lx = ev.clientX - rect.left
  const ly = ev.clientY - rect.top

  // 线性扫描最近点（点数最多 241，性能可接受）
  let best = null
  let bestDist = Infinity
  for (const p of points.value) {
    const d = Math.abs(p.cx - lx)
    if (d < bestDist) { bestDist = d; best = p }
  }
  if (!best) return
  const pc = prevClose.value
  const delta = pc > 0 ? best.raw.close - pc : 0
  const pct = pc > 0 ? (delta / pc) * 100 : 0

  hover.value = {
    x: best.cx,
    y: best.yClose,
    point: best.raw,
    delta,
    pct,
    avg: avgAt(best.i),
    // 提示框横向偏移夹在绘图区内，避免溢出容器左右边缘
    tipX: Math.min(Math.max(best.cx - 84, axisL), viewW.value - 168),
  }
}

// 鼠标移出：清除十字线
function onLeave() {
  hover.value = null
}

// 格式化：量/额大于 1 亿显示"亿"、大于 1 万显示"万"，否则原样输出（hover 提示用）
function fmtVol(v) {
  const n = Number(v) || 0
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿'
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return String(n)
}
function fmtAmt(v) {
  const n = Number(v) || 0
  if (n >= 1e8) return (n / 1e8).toFixed(2) + '亿'
  if (n >= 1e4) return (n / 1e4).toFixed(1) + '万'
  return String(n)
}

// ── 生命周期 ──
// ── Lifecycle ──
let ro = null
let timer = null

onMounted(() => {
  // 先按容器宽度自适应，再拉取数据
  refit()
  load()
  // 监听容器尺寸变化（如页面展开/折叠、响应式布局），变化时按新宽度重算坐标；
  // 老环境无 ResizeObserver 时退化为 500ms 轮询检测宽度
  if (wrapRef.value && typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(refit)
    ro.observe(wrapRef.value)
  } else {
    timer = setInterval(refit, 500)
  }
})

// code 变化时重新加载（组件实例被复用/展开不同股票时强制刷新，避免显示上个股票的数据）
watch(() => props.code, () => {
  name.value = props.name || ''
  points.value = []
  raw.value = []
  load()
})

// 卸载时断开尺寸监听与轮询定时器，避免内存泄漏
onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.kline-chart {
  background: #101828;
  border-radius: 8px;
  padding: 8px 10px 6px;
  color: #cdd9e9;
  font-size: 14px;
}
.kline-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 6px;
}
.kline-title {
  font-weight: 600;
  color: #e8f0fe;
}
.kline-summary b.up { color: #ff6b5a; }
.kline-summary b.down { color: #3ddc84; }
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
.kline-state {
  text-align: center;
  padding: 24px 0;
  color: #8fa3bf;
}
.kline-wrap {
  position: relative;
  width: 100%;
}
.kline-svg {
  display: block;
  width: 100%;
}
.grid-line { stroke: #2a3a55; stroke-width: 1; stroke-dasharray: 3 3; }
.panel-line { stroke: #24334d; stroke-width: 1; }
.prev-line { stroke: #f0b90b; stroke-width: 1; stroke-dasharray: 4 3; }
.prev-label { fill: #f0b90b; font-size: 14px; font-family: monospace; }
.axis-text {
  fill: #7d8fab;
  font-size: 14px;
  font-family: monospace;
}
.crosshair { stroke: #f0b90b; stroke-width: 1; }
.hover-dot { fill: #7cb3ff; }
.kline-tip {
  position: absolute;
  background: #0a0f1c;
  border: 1px solid #2e4161;
  border-radius: 4px;
  padding: 4px 8px;
  pointer-events: none;
  z-index: 5;
  font-size: 14px;
  line-height: 1.5;
  white-space: nowrap;
}
.tip-date { font-weight: 600; color: #e8f0fe; }
.tip-row b { color: #e8f0fe; font-weight: 600; }
.tip-row b.up { color: #ff6b5a; }
.tip-row b.down { color: #3ddc84; }
.tip-row.macd b { color: #e8f0fe; }
.kline-legend {
  display: flex;
  gap: 12px;
  margin-top: 4px;
  color: #8fa3bf;
}
.kline-legend i {
  display: inline-block;
  width: 10px;
  height: 3px;
  margin-right: 4px;
  vertical-align: middle;
  border-radius: 2px;
}
</style>