<!--
  模拟盘页面 Paper.vue
  Paper-trading page Paper.vue
  独立于真实持仓的纸面交易：策略 buy 信号按实时价自动撮合成虚拟持仓，记录净值曲线，
  并用「信号价 vs 成交价」滑点与「信号发出→成交」延迟量化信号质量与时效性对收益的影响。
  Isolated from the real book: strategy buy signals auto-fill at the live price into virtual positions,
  tracking an equity curve and quantifying signal quality/timeliness via signal-vs-fill slippage and
  signal-to-fill latency.
-->
<template>
  <div class="paper-page">
    <!-- 页头：标题 + 清盘重置（Header: title + reset button）-->
    <div class="page-header">
      <h2>模拟盘</h2>
      <div class="header-right">
        <span class="admin-badge" v-if="isAdmin" title="admin 账户的模拟盘支持回测与自动化交易联动">联动版</span>
        <span class="enabled-badge" :class="enabled ? 'on' : 'off'">
          {{ enabled ? '自动撮合中' : '未启用（rules.paper.enabled）' }}
        </span>
        <input v-model="initCapital" type="number" min="10000" step="10000"
               :disabled="!enabled" class="cap-input" placeholder="初始资金" :title="'当前初始资金 ' + fmt(initialCapital)" />
        <button class="btn-reset" :disabled="!enabled" @click="doReset">清盘重置</button>
      </div>
    </div>

    <!-- 绩效统计卡（Performance stat cards）-->
    <div class="stats-grid" v-if="stats">
      <div class="stat-card">
        <div class="stat-label">总资产</div>
        <div class="stat-value">¥{{ fmt(stats.total_value) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">总收益</div>
        <div class="stat-value" :class="stats.total_return_pct >= 0 ? 'up' : 'down'">
          {{ stats.total_return_pct >= 0 ? '+' : '' }}{{ stats.total_return_pct.toFixed(2) }}%
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">当日收益</div>
        <div class="stat-value" :class="stats.today_return_pct >= 0 ? 'up' : 'down'">
          {{ stats.today_return_pct >= 0 ? '+' : '' }}{{ stats.today_return_pct.toFixed(2) }}%
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">现金</div>
        <div class="stat-value">¥{{ fmt(stats.cash) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">持仓市值 / 已实现盈亏</div>
        <div class="stat-value">
          ¥{{ fmt(stats.market_value) }}
          <em class="sub" :class="stats.realized_pnl >= 0 ? 'up' : 'down'">
            {{ stats.realized_pnl >= 0 ? '+' : '' }}¥{{ fmt(stats.realized_pnl) }}
          </em>
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已平仓胜率</div>
        <div class="stat-value">{{ stats.win_rate_pct.toFixed(0) }}% <em class="sub">/ {{ stats.open_positions }}仓</em></div>
      </div>
    </div>

    <!-- 信号质量统计卡：实时价 vs 信号价 的延迟与滑点（Signal-quality stats: live vs signal price latency & slippage）-->
    <div class="stats-grid quality" v-if="stats">
      <div class="stat-card">
        <div class="stat-label">已撮合买入信号</div>
        <div class="stat-value">{{ stats.filled_buys }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">平均成交延迟</div>
        <div class="stat-value">{{ stats.avg_latency_sec }}s <em class="sub">最大 {{ stats.max_latency_sec }}s</em></div>
      </div>
      <div class="stat-card">
        <div class="stat-label">平均滑点（成交 vs 信号价）</div>
        <div class="stat-value" :class="stats.avg_slippage_pct >= 0 ? 'down' : 'up'">
          {{ stats.avg_slippage_pct >= 0 ? '+' : '' }}{{ stats.avg_slippage_pct.toFixed(2) }}%
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">滑点累计成本</div>
        <div class="stat-value" :class="stats.slippage_cost >= 0 ? 'down' : 'up'">
          {{ stats.slippage_cost >= 0 ? '+' : '' }}¥{{ fmt(stats.slippage_cost) }}
          <em class="sub">占初始 {{ stats.signal_amount_pct.toFixed(2) }}%</em>
        </div>
      </div>
    </div>

    <!-- 净值曲线（Equity curve）-->
    <div class="panel">
      <div class="panel-title">净值曲线 <em class="sub">（{{ stats?.equity_curve_points || 0 }} 个交易日）</em></div>
      <svg v-if="equity.length > 1" class="equity-chart" :viewBox="'0 0 ' + W + ' ' + H" preserveAspectRatio="none">
        <polyline :points="linePoints" fill="none" stroke="#FF4D4F" stroke-width="2" />
        <line v-for="lvl in gridLines" :key="lvl.y" :x1="0" :y1="lvl.y" :x2="W" :y2="lvl.y" class="grid-line" />
      </svg>
      <div v-else class="empty-hint">净值数据不足（模拟盘开启并产生成交后显示）</div>
    </div>

    <!-- 持仓表（Positions table）-->
    <div class="panel">
      <div class="panel-title">当前持仓 <em class="sub">{{ positions.length }} 只</em></div>
      <table class="data-table" v-if="positions.length">
        <thead>
          <tr>
            <th>代码</th><th>名称</th><th>战法</th><th>数量</th>
            <th>成交价</th><th>现价</th><th>市值</th><th>浮盈</th><th>浮盈%</th>
            <th>信号价</th><th>滑点%</th><th>延迟</th><th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in positions" :key="p.code">
            <td>{{ p.code }}</td>
            <td>{{ p.name }}</td>
            <td><span class="tag">{{ p.strategy }}</span></td>
            <td>{{ p.qty }}</td>
            <td>{{ p.cost_price.toFixed(2) }}</td>
            <td>{{ p.mark.toFixed(2) }}</td>
            <td>{{ fmt(p.mark * p.qty) }}</td>
            <td :class="pnlCls(p.pnl)">{{ fmt(p.pnl) }}</td>
            <td :class="pnlCls(p.pnl)">{{ fmt(p.pnl_pct) }}%</td>
            <td>{{ p.signal_price ? p.signal_price.toFixed(2) : '—' }}</td>
            <td :class="pnlCls(-p.slippage_pct)">{{ p.slippage_pct ? fmt(p.slippage_pct) + '%' : '—' }}</td>
            <td>{{ p.latency_sec }}s</td>
            <td><button class="btn-sell" @click="sell(p.code)">卖出</button></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty-hint">暂无持仓（出现可开仓信号时按实时价自动买入）</div>
    </div>

    <!-- 成交记录（Fill records）-->
    <div class="panel">
      <div class="panel-title">成交记录 <em class="sub">{{ trades.length }} 笔</em></div>
      <table class="data-table" v-if="trades.length">
        <thead>
          <tr>
            <th>时间</th><th>方向</th><th>代码</th><th>名称</th><th>战法</th>
            <th>数量</th><th>价格</th><th>金额</th><th>信号价</th><th>延迟</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(t, i) in trades" :key="i">
            <td>{{ fmtTime(t.time) }}</td>
            <td><span class="tag" :class="t.side === 'buy' ? 'buy' : 'sell'">{{ t.side === 'buy' ? '买入' : '卖出' }}</span></td>
            <td>{{ t.code }}</td>
            <td>{{ t.name }}</td>
            <td>{{ t.strategy }}</td>
            <td>{{ t.qty }}</td>
            <td>{{ t.price.toFixed(2) }}</td>
            <td>{{ fmt(t.amount) }}</td>
            <td>{{ t.signal_price ? t.signal_price.toFixed(2) : '—' }}</td>
            <td>{{ t.latency_sec ? t.latency_sec + 's' : '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty-hint">暂无成交记录</div>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ── (Imports)
import { ref, computed, onMounted, onUnmounted } from 'vue' // Vue 组合式 API（响应式与生命周期）
import * as api from '../api/index.js' // 后端 API 封装（模拟盘接口）

// ── 状态 ── (State)
const enabled = ref(false)       // 模拟盘总开关
const isAdmin = ref(false)       // admin 账户标记（模拟盘可联动回测/自动化交易）
const initialCapital = ref('')   // 自定义初始资金输入（清盘重置时生效）
const stats = ref(null)          // 绩效与信号质量汇总
const positions = ref([])        // 当前持仓
const trades = ref([])           // 成交记录
const equity = ref([])           // 净值序列
const W = 900, H = 220           // 净值折线 SVG 画布尺寸
let timer = null                 // 轮询定时器

// ── 净值折线 ── (Equity line)
// 把净值点映射为 SVG polyline 坐标（首末留白，Y 轴按最小值缩放）
// Map equity points to SVG polyline coordinates (with padding; Y scaled from the min)
const linePoints = computed(() => {
  if (equity.value.length < 2) return ''
  const pad = 10
  const vals = equity.value.map(p => p.value)
  const min = Math.min(...vals), max = Math.max(...vals)
  const range = max - min || 1
  return equity.value.map((p, i) => {
    const x = pad + (i / (equity.value.length - 1)) * (W - 2 * pad)
    const y = H - pad - ((p.value - min) / range) * (H - 2 * pad)
    return x.toFixed(1) + ',' + y.toFixed(1)
  }).join(' ')
})

// 三条横向网格线（1/2/3 位置）
const gridLines = computed(() => [1, 2, 3].map(k => ({ y: (H / 4) * k })))

// ── 工具函数 ── (Helpers)
// 数字格式化：千分位 + 两位小数（thousands separator, two decimals）
function fmt(v) { return (v ?? 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 }) }
// 时间格式化：MM-DD HH:MM（time formatting）
function fmtTime(t) { return t ? t.slice(5, 16) : '—' }
// 涨跌颜色类：非负红（A股习惯红涨），负绿（positive = red per A-share convention）
function pnlCls(v) { return v >= 0 ? 'up' : 'down' }

// ── 数据加载 ── (Data loading)
// 拉取模拟盘全量状态（开关/统计/持仓/成交/净值），失败时静默保留旧数据
// Fetch the full paper state; keep stale data on failure
async function load() {
  try {
    const st = await api.fetchPaperState()
    enabled.value = !!st.enabled
    isAdmin.value = !!st.is_admin
    if (st.initial_capital > 0 && !initialCapital.value) initialCapital.value = String(st.initial_capital)
    stats.value = st.stats || null
  } catch (_) {}
  if (!enabled.value) return
  try { positions.value = await api.fetchPaperPositions() } catch (_) {}
  try { trades.value = await api.fetchPaperTrades() } catch (_) {}
  try { equity.value = await api.fetchPaperEquity() } catch (_) {}
}

// ── 操作 ── (Actions)
// 手动卖出：确认后按实时价清仓并刷新（Manual sell: confirm, close at live price, refresh）
async function sell(code) {
  if (!confirm('确认按实时价卖出 ' + code + '？')) return
  try {
    await api.sellPaperPosition(code)
    await load()
  } catch (e) { alert(e.message || '卖出失败') }
}

// 清盘重置：确认后重置现金/成交/净值；输入框数值>0 时一并自定义初始资金
// Reset: confirm then reset cash/trades/equity; a positive input also customizes the starting capital
async function doReset() {
  const cap = parseFloat(initialCapital.value)
  const hint = cap > 0 ? '（并设置初始资金为 ¥' + fmt(cap) + '）' : ''
  if (!confirm('清盘模拟盘？将按最后估值价平仓全部持仓并重置净值。此操作不影响真实持仓。' + hint)) return
  try {
    await api.resetPaper(cap > 0 ? cap : 0)
    await load()
  } catch (e) { alert(e.message || '重置失败') }
}

// ── 生命周期 ── (Lifecycle)
onMounted(() => {
  load()
  timer = setInterval(load, 15000) // 15s 轮询刷新（持仓现价/净值随实时行情变化）
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
/* ── 页面与面板 ── (Page & panels) */
.paper-page { padding: 20px; max-width: 1200px; margin: 0 auto; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; flex-wrap: wrap; gap: 8px; }
.page-header h2 { margin: 0; font-size: 20px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.enabled-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; }
.enabled-badge.on { background: rgba(82, 196, 26, 0.15); color: #52c41a; }
.enabled-badge.off { background: rgba(255, 255, 255, 0.06); color: #8fa3bf; }
.admin-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; background: rgba(255, 213, 79, 0.15); color: #FFD54F; }
.cap-input { background: #16162a; color: #e6edf3; border: 1px solid rgba(255, 255, 255, 0.12); border-radius: 6px; padding: 6px 10px; font-size: 13px; width: 120px; }
.cap-input:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-reset { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border: 1px solid rgba(255, 77, 79, 0.35); padding: 6px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.btn-reset:disabled { opacity: 0.4; cursor: not-allowed; }
.panel { background: #1b1b30; border-radius: 10px; padding: 16px; margin-bottom: 16px; }
.panel-title { font-size: 15px; font-weight: 600; margin-bottom: 12px; }
.sub { font-size: 12px; font-weight: 400; color: #8fa3bf; font-style: normal; }
.empty-hint { color: #8fa3bf; font-size: 13px; padding: 12px 0; text-align: center; }

/* ── 统计卡 ── (Stat cards) */
.stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 10px; margin-bottom: 16px; }
.stats-grid.quality { margin-bottom: 8px; }
.stat-card { background: #1b1b30; border-radius: 10px; padding: 12px 14px; }
.stat-label { font-size: 12px; color: #8fa3bf; margin-bottom: 6px; }
.stat-value { font-size: 18px; font-weight: 600; }
.stat-value em.sub { display: block; font-size: 11px; margin-top: 2px; }

/* ── 涨跌颜色（A股红涨绿跌）── (A-share color: red up / green down) */
.up { color: #FF4D4F; }
.down { color: #52c41a; }

/* ── 净值折线 ── (Equity chart) */
.equity-chart { width: 100%; height: 220px; background: #16162a; border-radius: 8px; }
.grid-line { stroke: rgba(255, 255, 255, 0.06); stroke-width: 1; }

/* ── 数据表 ── (Data table) */
.data-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.data-table th { text-align: left; color: #8fa3bf; font-weight: 500; padding: 8px 6px; border-bottom: 1px solid rgba(255, 255, 255, 0.08); white-space: nowrap; }
.data-table td { padding: 8px 6px; border-bottom: 1px solid rgba(255, 255, 255, 0.04); white-space: nowrap; }
.tag { display: inline-block; padding: 1px 8px; border-radius: 8px; background: rgba(255, 255, 255, 0.08); font-size: 12px; }
.tag.buy { background: rgba(255, 77, 79, 0.15); color: #FF4D4F; }
.tag.sell { background: rgba(82, 196, 26, 0.15); color: #52c41a; }
.btn-sell { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border: 1px solid rgba(255, 77, 79, 0.35); padding: 3px 10px; border-radius: 6px; cursor: pointer; font-size: 12px; }
.btn-sell:hover { background: rgba(255, 77, 79, 0.25); }
</style>
