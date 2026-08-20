<!--
  模拟盘页面 Paper.vue
  Paper-trading page Paper.vue
  独立于真实持仓的纸面交易：admin 账户的 buy 信号按实时价自动撮合成虚拟持仓；普通用户为手动
  记账（信号页/持仓页手动买入/加仓/减仓，输入价格与手数，静态存储展示）。页内展示分仓余量、
  持仓与成交日志（div-grid + 分时展开 + 移动端底部操作菜单）、净值曲线与信号质量统计。
  Isolated from the real book: on the admin account, strategy buy signals auto-fill at the live price into
  virtual positions; normal users keep a manual book (buy/add/trim typed price and lots, static storage &
  view). The page shows the strategy-pool allocation, positions & fill log (div-grid + K-line expand +
  mobile bottom action sheet), an equity curve and signal-quality stats.
-->
<template>
  <div class="paper-page">
    <!-- 页头：标题 + 清盘重置（Header: title + reset button）-->
    <div class="page-header">
      <h2>模拟盘</h2>
      <div class="header-right">
        <span class="admin-badge" v-if="isAdmin" title="admin 账户的模拟盘支持回测与自动化交易联动">联动版</span>
        <span class="enabled-badge" :class="enabled ? 'on' : 'off'">
          {{ enabled ? (isAdmin ? '自动撮合中' : '手动记账（静态）') : '未启用（rules.paper.enabled）' }}
        </span>
        <span class="cap-badge" v-if="enabled" title="当前生效的持仓上限（经确认资金固化）">
          上限：{{ appliedMax > 0 ? appliedMax + ' 只' : '不设限' }}
        </span>
        <input v-model="initialCapital" type="number" min="0" step="1000"
               :disabled="!enabled" class="cap-input" placeholder="注入资金" :title="'当前累计投入 ' + fmt(initialCapital)" />
        <input v-model="maxPos" type="number" min="0" step="1"
               :disabled="!enabled" class="cap-input cap-max" placeholder="持仓上限" title="持仓上限（0=不设限，由资金决定持仓）" />
        <button class="btn-confirm" :disabled="!enabled" @click="confirmDeposit">注入资金</button>
        <button class="btn-reset" :disabled="!enabled" @click="doReset">清盘重置</button>
      </div>
    </div>

    <!-- 分仓条：当前启用战法的资金池余量（Strategy-pool allocation strip）-->
    <div class="pools-bar" v-if="enabled && pools.length">
      <div class="pools-title">分仓资金池</div>
      <div class="pool-chip" v-for="p in pools" :key="p.key"
           :class="{ other: !p.key }" :title="p.key || '其他/手动'">
        <span class="pool-label">{{ p.label }}</span>
        <span class="pool-cash">¥{{ fmt(p.cash) }}</span>
        <span class="pool-meta">{{ p.ratio_pct.toFixed(1) }}% · {{ p.positions }} 仓</span>
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
          <!-- 标注收益计算基数：基于累计投入（注入资金会同步累加），避免"收益百分比失真"的误读 -->
          <!-- Notes the return basis: computed against the cumulative investment (deposits accumulate), so the % reads clearly -->
          <em class="sub">基于累计投入 ¥{{ fmt(stats.initial_capital) }}</em>
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

    <!-- 信号质量统计卡：仅联动版（admin 自动撮合）有意义（Signal-quality stats, meaningful only on the
         auto-filled admin book）-->
    <div class="stats-grid quality" v-if="stats && isAdmin">
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

    <!-- 净值曲线（Equity curve；普通用户为静态记账，无自动净值，显示说明）-->
    <div class="panel" v-if="isAdmin">
      <div class="panel-title">净值曲线 <em class="sub">（{{ stats?.equity_curve_points || 0 }} 个交易日）</em></div>
      <svg v-if="equity.length > 1" class="equity-chart" :viewBox="'0 0 ' + W + ' ' + H" preserveAspectRatio="none">
        <polyline :points="linePoints" fill="none" stroke="#FF4D4F" stroke-width="2" />
        <line v-for="lvl in gridLines" :key="lvl.y" :x1="0" :y1="lvl.y" :x2="W" :y2="lvl.y" class="grid-line" />
      </svg>
      <div v-else class="empty-hint">净值数据不足（自动撮合开启并产生成交后显示）</div>
    </div>

    <!-- 持仓 / 成交日志 页签（tabs: positions / trade log）-->
    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'positions' }" @click="tab = 'positions'">
        当前持仓 <em class="sub">{{ positions.length }} 只</em>
      </button>
      <button class="tab" :class="{ active: tab === 'trades' }" @click="tab = 'trades'">
        成交日志 <em class="sub">{{ trades.length }} 笔 · 近3月</em>
      </button>
    </div>

    <!-- 持仓列表：div-grid（照搬真实持仓页模式：行内字段 + 分时展开 + 移动端 sheet）-->
    <div class="panel" v-if="tab === 'positions'">
      <div class="panel-title">当前持仓 <em class="sub">{{ positions.length }} 只</em></div>
      <div class="positions-table" v-if="positions.length">
        <div class="table-header">
          <span class="col-code">代码</span>
          <span class="col-name">名称</span>
          <span class="col-num">数量</span>
          <span class="col-price">成本价</span>
          <span class="col-price">现价</span>
          <span class="col-chg">浮盈</span>
          <span class="col-chg">浮盈%</span>
          <span class="col-pool">池</span>
          <span class="col-kline">分时</span>
          <span class="col-actions">操作</span>
        </div>
        <div v-for="p in positions" :key="p.code" class="pos-row-group">
          <div class="table-row" @click="onRowTap(p)">
            <span class="col-code" data-label="代码">{{ p.code }}</span>
            <span class="col-name" data-label="名称">{{ p.name }}</span>
            <span class="col-num" data-label="数量">{{ p.qty }}</span>
            <span class="col-price" data-label="成本价">{{ p.cost_price.toFixed(2) }}</span>
            <span class="col-price" data-label="现价">{{ (p.mark || 0).toFixed(2) }}</span>
            <span :class="['col-chg', pnlCls(p.pnl)]" data-label="浮盈">{{ fmt(p.pnl) }}</span>
            <span :class="['col-chg', pnlCls(p.pnl)]" data-label="浮盈%">{{ fmt(p.pnl_pct) }}%</span>
            <span class="col-pool" data-label="池">
              <span class="tag">{{ poolLabel(p.strategy_type) }}</span>
            </span>
            <span class="col-kline" data-label="分时">
              <button class="btn-kline" @click.stop="toggleKline(p.code)" :title="klineOpen.has(p.code) ? '收起分时' : '展开分时'">
                {{ klineOpen.has(p.code) ? '收起' : '分时' }}
              </button>
            </span>
            <span class="col-actions" data-label="操作">
              <button class="btn-lot" @click.stop="openTrade(p, 'add')">加仓</button>
              <button class="btn-cost" @click.stop="openTrade(p, 'trim')">减仓</button>
              <button class="btn-sell" @click.stop="openTrade(p, 'close')">清仓</button>
            </span>
          </div>
          <!-- 展开的分时区（全宽，位于该行下方）（Expanded K-line area, full width, below the row）-->
          <div v-if="klineOpen.has(p.code)" class="pos-kline-row">
            <div class="kline-flex">
              <div class="kline-main"><KLineChart :code="p.code" :name="p.name" /></div>
              <div class="depth-side"><DepthPanel :code="p.code" :name="p.name" /></div>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="empty-hint">
        {{ isAdmin ? '暂无持仓（出现可开仓信号时按实时价自动买入）' : '暂无持仓（在信号页点「模拟买入」，或上方加仓/减仓管理已有持仓）' }}
      </div>
    </div>

    <!-- 成交日志：div-grid（同模式：行内字段 + 分时展开 + 移动端 sheet）-->
    <div class="panel" v-if="tab === 'trades'">
      <div class="panel-title">成交日志 <em class="sub">{{ trades.length }} 笔 · 近3个月</em></div>
      <div class="positions-table" v-if="trades.length">
        <div class="table-header">
          <span class="col-time">时间</span>
          <span class="col-side">方向</span>
          <span class="col-code">代码</span>
          <span class="col-name">名称</span>
          <span class="col-pool">战法</span>
          <span class="col-num">数量</span>
          <span class="col-price">价格</span>
          <span class="col-price">金额</span>
          <span class="col-kline">分时</span>
        </div>
        <div v-for="(t, i) in trades" :key="i" class="pos-row-group">
          <div class="table-row" @click="onTradeTap(t, i)">
            <span class="col-time" data-label="时间">{{ fmtTime(t.time) }}</span>
            <span class="col-side" data-label="方向">
              <span class="tag" :class="t.side === 'buy' ? 'buy' : 'sell'">{{ t.side === 'buy' ? '买入' : '卖出' }}</span>
            </span>
            <span class="col-code" data-label="代码">{{ t.code }}</span>
            <span class="col-name" data-label="名称">{{ t.name }}</span>
            <span class="col-pool" data-label="战法"><span class="tag">{{ t.strategy }}</span></span>
            <span class="col-num" data-label="数量">{{ t.qty }}</span>
            <span class="col-price" data-label="价格">{{ t.price.toFixed(2) }}</span>
            <span class="col-price" data-label="金额">{{ fmt(t.amount) }}</span>
            <span class="col-kline" data-label="分时">
              <button class="btn-kline" @click.stop="toggleKline('trade_' + i)">{{ klineOpen.has('trade_' + i) ? '收起' : '分时' }}</button>
            </span>
          </div>
          <div v-if="klineOpen.has('trade_' + i)" class="pos-kline-row">
            <div class="kline-flex">
              <div class="kline-main"><KLineChart :code="t.code" :name="t.name" /></div>
              <div class="depth-side"><DepthPanel :code="t.code" :name="t.name" /></div>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="empty-hint">暂无成交记录</div>
    </div>

    <!-- 移动端：点击行弹出的底部操作菜单（持仓）（Mobile bottom action sheet for a position row）-->
    <div class="sheet-overlay" v-if="sheetPos" @click="sheetPos = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetPos.code }} {{ sheetPos.name }}</div>
        <button class="sheet-btn" @click="sheetKline"> {{ klineOpen.has(sheetPos.code) ? '收起分时' : '展开分时' }}</button>
        <button class="sheet-btn" @click="sheetTrade('add')">加仓</button>
        <button class="sheet-btn" @click="sheetTrade('trim')">减仓</button>
        <button class="sheet-btn sheet-danger" @click="sheetTrade('close')">清仓</button>
        <button class="sheet-btn sheet-cancel" @click="sheetPos = null">取消</button>
      </div>
    </div>
    <!-- 移动端：成交行底部操作菜单（Mobile bottom action sheet for a fill row）-->
    <div class="sheet-overlay" v-if="sheetTradeRow" @click="sheetTradeRow = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetTradeRow.code }} {{ sheetTradeRow.name }}</div>
        <button class="sheet-btn" @click="sheetTradeKline">
          {{ klineOpen.has('trade_' + sheetTradeRow.idx) ? '收起分时' : '展开分时' }}
        </button>
        <button class="sheet-btn sheet-cancel" @click="sheetTradeRow = null">取消</button>
      </div>
    </div>

    <!-- 交易弹窗：加仓 / 减仓 / 清仓（输入价格 + 手数；照搬真实持仓页的加减仓模式）-->
    <div class="modal-overlay" v-if="tradeModal" @click.self="tradeModal = false">
      <div class="modal">
        <div class="modal-title">
          {{ tradeDir === 'add' ? '加仓' : (tradeDir === 'trim' ? '减仓' : '清仓') }}
          {{ tradeTarget?.code }} {{ tradeTarget?.name }}
        </div>
        <div class="form-row">
          <label>当前持仓</label>
          <span class="static-val">{{ tradeTarget?.qty }} 股 / 成本 ¥{{ tradeTarget?.cost_price?.toFixed(2) }}</span>
        </div>
        <div class="form-row">
          <label>价格</label>
          <input v-model.number="tradeFormPrice" type="number" step="0.001" placeholder="成交价格（留空用实时价）" />
        </div>
        <div class="form-row">
          <label>{{ tradeDir === 'add' ? '加仓手数' : (tradeDir === 'trim' ? '减仓手数' : '清仓') }}</label>
          <input v-if="tradeDir !== 'close'" v-model.number="tradeFormQty" type="number" step="1" placeholder="手数（1手=100股）" />
          <span v-else class="static-val">{{ tradeTarget?.qty }} 股（全部）</span>
        </div>
        <div class="preview" v-if="tradeDir === 'trim' && tradePreviewQty > 0">
          减仓后：剩余 {{ tradeTarget.qty - tradePreviewQty * 100 }} 股
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="tradeModal = false">取消</button>
          <button class="btn-confirm" :class="tradeDir === 'close' ? 'btn-confirm-sell' : ''"
                  @click="confirmTrade" :disabled="tradeOverSell">确定</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ── (Imports)
import { ref, computed, onMounted, onUnmounted } from 'vue' // Vue 组合式 API（响应式与生命周期）
import * as api from '../api/index.js' // 后端 API 封装（模拟盘接口）
import KLineChart from '../components/KLineChart.vue' // 分时图组件（展开行展示）
import DepthPanel from '../components/DepthPanel.vue' // 盘口面板（展开行展示，买卖五档）

// ── 状态 ── (State)
const enabled = ref(false)       // 模拟盘总开关
const isAdmin = ref(false)       // admin 账户标记（联动版：自动撮合/回测/盘后研究导出）
const initialCapital = ref('')   // 自定义初始资金输入（确认资金/清盘重置时生效）
const maxPos = ref('')           // 自定义持仓上限输入（0=不设限）
const appliedMax = ref(0)        // 当前生效的持仓上限（经确认资金固化；0=不设限，header 展示用）
const tab = ref('positions')     // 页签：positions=持仓 / trades=成交日志
const stats = ref(null)          // 绩效与信号质量汇总
const positions = ref([])        // 当前持仓
const trades = ref([])           // 成交记录
const equity = ref([])           // 净值序列
const pools = ref([])            // 分仓资金池快照（strategy_pools）
const W = 900, H = 220           // 净值折线 SVG 画布尺寸
let timer = null                 // 轮询定时器

// ── 分时展开 / 移动端 sheet（照搬真实持仓页）── (K-line expand / mobile sheet, ported from Positions)
const klineOpen = ref(new Set())      // 已展开分时的行键集合（持仓=code，成交='trade_'+i）
const sheetPos = ref(null)            // 移动端：被点击的持仓行
const sheetTradeRow = ref(null)       // 移动端：被点击的成交行
// ── 交易弹窗（加仓/减仓/清仓）── (Trade modal: add / trim / close)
const tradeModal = ref(false)         // 交易弹窗开关
const tradeDir = ref('add')           // add / trim / close
const tradeTarget = ref(null)         // 目标持仓
const tradeFormPrice = ref(0)         // 输入价格（0=用实时价）
const tradeFormQty = ref(1)           // 输入手数（1手=100股）
const tradePreviewQty = computed(() => {
  const q = parseInt(tradeFormQty.value, 10)
  return isNaN(q) || q <= 0 ? 0 : q
})
const tradeOverSell = computed(() =>
  tradeDir.value === 'trim' && tradeTarget.value && tradePreviewQty.value * 100 >= tradeTarget.value.qty
)

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
// 战法池展示名（空=其他/手动）
function poolLabel(k) {
  if (!k) return '其他/手动'
  const labels = { dragon: '龙头', double_bump: '双板', n_shape: 'N形', dragon_return: '龙回头', factor: '波动突破', pattern: '形态' }
  return labels[k] || k
}

// ── 分时 / sheet 交互（照搬真实持仓页）── (K-line & sheet interactions, ported from Positions)
// 展开/收起某行的分时区
function toggleKline(key) {
  const next = new Set(klineOpen.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  klineOpen.value = next
}
// 移动端：点击持仓行打开操作菜单
function onRowTap(p) {
  if (window.innerWidth > 768) return
  sheetPos.value = p
}
// 移动端：点击成交行打开操作菜单
function onTradeTap(t, i) {
  if (window.innerWidth > 768) return
  sheetTradeRow.value = { ...t, idx: i }
}
// 移动端：操作菜单 - 展开/收起持仓分时
function sheetKline() {
  if (!sheetPos.value) return
  toggleKline(sheetPos.value.code)
  sheetPos.value = null
}
// 移动端：操作菜单 - 加仓/减仓/清仓
function sheetTrade(dir) {
  if (!sheetPos.value) return
  const p = sheetPos.value
  sheetPos.value = null
  openTrade(p, dir)
}
// 移动端：成交行 - 展开/收起分时
function sheetTradeKline() {
  if (!sheetTradeRow.value) return
  toggleKline('trade_' + sheetTradeRow.value.idx)
  sheetTradeRow.value = null
}

// 打开交易弹窗（加仓/减仓/清仓）：回填当前持仓
function openTrade(p, dir) {
  tradeTarget.value = p
  tradeDir.value = dir
  tradeFormPrice.value = p.mark || 0
  tradeFormQty.value = dir === 'close' ? p.qty : 1
  tradeModal.value = true
}

// 提交交易：加仓走 buy（BuyEx 已持仓自动合并）；减仓/清仓走 sell（SellEx 支持指定数量/价格）
async function confirmTrade() {
  const p = tradeTarget.value
  if (!p) return
  const price = parseFloat(tradeFormPrice.value)
  const qty = parseInt(tradeFormQty.value, 10)
  if (tradeDir.value !== 'close' && (isNaN(qty) || qty <= 0)) { alert('请输入有效的数量'); return }
  try {
    if (tradeDir.value === 'add') {
      await api.buyPaperPosition(p.code, p.name || '', p.strategy || '', 0, price > 0 ? price : 0, qty)
      alert(`已加仓 ${p.code} ${qty} 手`)
    } else {
      await api.sellPaperPosition(p.code, price > 0 ? price : 0, tradeDir.value === 'close' ? 0 : qty)
      alert(`已${tradeDir.value === 'close' ? '清仓' : '减仓'} ${p.code}`)
    }
    tradeModal.value = false
    await load()
  } catch (e) { alert(e.message || '操作失败') }
}

// ── 数据加载 ── (Data loading)
// 拉取模拟盘全量状态（开关/统计/分仓/持仓/成交/净值），失败时静默保留旧数据
// Fetch the full paper state; keep stale data on failure
async function load() {
  try {
    const st = await api.fetchPaperState()
    enabled.value = !!st.enabled
    isAdmin.value = !!st.is_admin
    if (st.initial_capital > 0 && !initialCapital.value) initialCapital.value = String(st.initial_capital)
    if (st.max_positions !== undefined && !maxPos.value) maxPos.value = st.max_positions > 0 ? String(st.max_positions) : '0'
    appliedMax.value = (st.max_positions !== undefined && st.max_positions > 0) ? st.max_positions : 0
    stats.value = st.stats || null
    pools.value = Array.isArray(st.strategy_pools) ? st.strategy_pools : []
  } catch (_) {}
  if (!enabled.value) return
  try { positions.value = await api.fetchPaperPositions() } catch (_) {}
  try { trades.value = await api.fetchPaperTrades() } catch (_) {}
  try { equity.value = await api.fetchPaperEquity() } catch (_) {}
}

// ── 操作 ── (Actions)
// 注入资金：输入注入金额（可选持仓上限）后确认，增量加现金并保留现有持仓/净值/成交日志
// （与真实持仓一致：加钱不清仓，收益基准=累计投入）。
// Deposit: enter the amount to add (and optional position cap), confirm, then cash increases
// incrementally while positions / equity / fill log are all kept (just like the real book: adding
// money never clears holdings; the return basis is the cumulative investment).
async function confirmDeposit() {
  const amt = parseFloat(initialCapital.value)
  if (!(amt > 0)) { alert('请输入有效的注入金额'); return }
  const mp = parseInt(maxPos.value, 10)
  const mpv = mp > 0 ? mp : 0
  const capHint = mpv > 0 ? '，持仓上限 ' + mpv + ' 只' : '（持仓上限不设限，由资金决定）'
  if (!confirm('确认注入资金 ¥' + fmt(amt) + capHint + '？将增量计入现金，保留现有持仓/净值/成交记录。')) return
  try {
    const res = await api.resetPaper(amt, mpv)
    // 注入成功后同步输入框与当前生效上限，避免轮询 load 覆盖显示旧值
    // After a successful deposit, sync the inputs and the applied cap so polling doesn't show stale values
    initialCapital.value = String(res.initial_capital || (parseFloat(initialCapital.value) + amt))
    maxPos.value = String(res.max_positions > 0 ? res.max_positions : 0)
    appliedMax.value = res.max_positions > 0 ? res.max_positions : 0
    await load()
  } catch (e) { alert(e.message || '注入失败') }
}

// 清盘重置：仅清仓并按配置初始资金重置，不修改自定义资金
// Reset: liquidate everything and reset to the configured capital, without changing custom settings
async function doReset() {
  if (!confirm('清盘模拟盘？将按最后估值价平仓全部持仓并重置净值。此操作不影响真实持仓。')) return
  try {
    await api.resetPaper(0, 0)
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
.header-right { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.enabled-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; }
.enabled-badge.on { background: rgba(82, 196, 26, 0.15); color: #52c41a; }
.enabled-badge.off { background: rgba(255, 255, 255, 0.06); color: #8fa3bf; }
.admin-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; background: rgba(255, 213, 79, 0.15); color: #FFD54F; }
.cap-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; background: rgba(255, 255, 255, 0.04); color: #8fa3bf; border: 1px solid rgba(255, 255, 255, 0.1); white-space: nowrap; }
.cap-input { background: #16162a; color: #e6edf3; border: 1px solid rgba(255, 255, 255, 0.12); border-radius: 6px; padding: 6px 10px; font-size: 13px; width: 120px; }
.cap-input:disabled { opacity: 0.4; cursor: not-allowed; }
.cap-max { width: 90px; }
.btn-confirm { background: rgba(82, 196, 26, 0.15); color: #52c41a; border: 1px solid rgba(82, 196, 26, 0.4); padding: 6px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.btn-confirm:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-reset { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border: 1px solid rgba(255, 77, 79, 0.35); padding: 6px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.btn-reset:disabled { opacity: 0.4; cursor: not-allowed; }
.tabs { display: flex; gap: 8px; margin-bottom: 16px; }
.tab { background: rgba(255, 255, 255, 0.04); color: #8fa3bf; border: 1px solid rgba(255, 255, 255, 0.1); padding: 8px 16px; border-radius: 8px; cursor: pointer; font-size: 13px; }
.tab.active { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border-color: rgba(255, 77, 79, 0.4); }
.panel { background: #1b1b30; border-radius: 10px; padding: 16px; margin-bottom: 16px; }
.panel-title { font-size: 15px; font-weight: 600; margin-bottom: 12px; }
.sub { font-size: 12px; font-weight: 400; color: #8fa3bf; font-style: normal; }
.empty-hint { color: #8fa3bf; font-size: 13px; padding: 12px 0; text-align: center; }

/* ── 分仓资金池条 ── (Strategy-pool allocation strip) */
.pools-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; background: #1b1b30; border-radius: 10px; padding: 12px 14px; margin-bottom: 16px; }
.pools-title { font-size: 12px; color: #8fa3bf; margin-right: 4px; white-space: nowrap; }
.pool-chip { display: inline-flex; align-items: center; gap: 6px; background: rgba(255, 255, 255, 0.04); border: 1px solid rgba(255, 255, 255, 0.1); border-radius: 8px; padding: 6px 10px; font-size: 12px; }
.pool-chip.other { border-style: dashed; opacity: 0.85; }
.pool-label { color: #b388ff; font-weight: 600; }
.pool-cash { color: #e6edf3; }
.pool-meta { color: #8fa3bf; }

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

/* ── 数据表（div-grid，照搬真实持仓页）── (div-grid, ported from the real positions page) */
.positions-table { background: #16162a; border-radius: 8px; overflow-x: auto; font-size: 13px; white-space: nowrap; }
.table-header, .table-row { display: flex; align-items: center; padding: 9px 14px; gap: 0; min-width: 980px; }
.table-header { background: #22223a; color: #8fa3bf; font-weight: 600; }
.pos-row-group { border-bottom: 1px solid #22223a; min-width: 980px; }
.pos-row-group:last-child { border-bottom: none; }
.table-row:hover { background: rgba(255, 255, 255, 0.03); }
.col-code  { flex: 1; color: #4fc3f7; text-align: center; }
.col-name  { flex: 1; overflow: hidden; text-overflow: ellipsis; }
.col-num   { flex: 1; text-align: center; }
.col-price { flex: 1; text-align: center; }
.col-chg   { flex: 1; text-align: center; }
.col-chg.up, .up { color: #FF4D4F; font-weight: 600; }
.col-chg.down, .down { color: #52c41a; font-weight: 600; }
.col-pool  { flex: 1; text-align: center; }
.col-time  { flex: 1; text-align: center; }
.col-side  { flex: 1; text-align: center; }
.col-kline { flex: 0 0 64px; text-align: center; }
.btn-kline { background: transparent; border: 1px solid #3a3a55; color: #7ab8ff; border-radius: 4px; cursor: pointer; font-size: 12px; padding: 2px 8px; }
.btn-kline:hover { border-color: #4fc3f7; color: #4fc3f7; }
.pos-kline-row { padding: 8px 14px 12px; background: #14142a; }
.kline-flex { display: flex; gap: 12px; align-items: stretch; }
.kline-main { flex: 1 1 auto; min-width: 0; }
.depth-side { flex: 0 0 300px; }
@media (max-width: 720px) {
  .kline-flex { flex-direction: column; }
  .depth-side { flex: 1 1 auto; }
}
.col-actions { display: flex; gap: 4px; flex: 0 0 200px; justify-content: center; }
.btn-edit, .btn-sell, .btn-lot, .btn-cost { padding: 3px 10px; border-radius: 4px; font-size: 12px; cursor: pointer; white-space: nowrap; }
.btn-lot { border: 1px solid #7c4dff; background: transparent; color: #b388ff; }
.btn-lot:hover { background: rgba(124, 77, 255, 0.12); }
.btn-cost { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-cost:hover { background: rgba(250, 173, 20, 0.1); }
.btn-sell { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-sell:hover { background: rgba(250, 173, 20, 0.1); }
.tag { display: inline-block; padding: 1px 8px; border-radius: 8px; background: rgba(255, 255, 255, 0.08); font-size: 12px; }
.tag.buy { background: rgba(255, 77, 79, 0.15); color: #FF4D4F; }
.tag.sell { background: rgba(82, 196, 26, 0.15); color: #52c41a; }

/* ── 弹窗（照搬真实持仓页）── (Modals, ported from the real positions page) */
.modal-overlay { position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0, 0, 0, 0.6); display: flex; align-items: center; justify-content: center; z-index: 100; }
.modal { background: #1a1a2e; border-radius: 10px; padding: 24px; width: 380px; }
.modal-title { font-size: 16px; font-weight: 600; color: #e0e0e0; margin-bottom: 16px; }
.form-row { margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.form-row label { width: 80px; color: #888; font-size: 14px; flex-shrink: 0; }
.form-row input { flex: 1; padding: 8px 12px; border-radius: 6px; border: 1px solid #333; background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none; }
.form-row input:focus { border-color: #FF4D4F; }
.static-val { color: #e0e0e0; font-size: 14px; white-space: nowrap; }
.preview { margin: 4px 0 8px 88px; font-size: 14px; color: #b388ff; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.btn-cancel { padding: 8px 20px; border-radius: 6px; border: 1px solid #333; background: transparent; color: #888; font-size: 14px; cursor: pointer; }
.btn-confirm { padding: 8px 20px; border-radius: 6px; border: none; background: #FF4D4F; color: #fff; font-size: 14px; cursor: pointer; }
.btn-confirm:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-confirm-sell { background: #52c41a; }

/* ── 移动端：横向滚动 + sheet ── (Mobile: horizontal scroll + bottom action sheet) */
@media (max-width: 768px) {
  .positions-table { overflow-x: auto; white-space: nowrap; -webkit-overflow-scrolling: touch; }
  .table-header, .table-row { min-width: 1000px; padding: 9px 12px; }
  .pos-row-group { min-width: 0; }
  .page-header { flex-direction: column; align-items: stretch; gap: 8px; }
  .header-right { flex-wrap: wrap; gap: 8px; }
  .modal { width: 92%; max-width: 380px; padding: 18px; }
  .form-row { flex-wrap: wrap; }
  .form-row label { width: 70px; }
  .preview { margin-left: 0; }
  .table-row { cursor: pointer; }
  .sheet-overlay { position: fixed; inset: 0; z-index: 300; background: rgba(0, 0, 0, 0.6); display: flex; align-items: flex-end; }
  .action-sheet { width: 100%; background: #1a1a2e; border-radius: 14px 14px 0 0; padding: 10px 12px calc(10px + env(safe-area-inset-bottom, 0px)); }
  .sheet-title { font-size: 14px; color: #999; text-align: center; padding: 8px 0 12px; border-bottom: 1px solid #2a2a3e; margin-bottom: 8px; }
  .sheet-btn { width: 100%; padding: 14px; border-radius: 8px; border: none; background: #0f0f23; color: #4fc3f7; font-size: 16px; cursor: pointer; margin-bottom: 8px; text-align: center; }
  .sheet-btn:active { opacity: 0.8; }
  .sheet-danger { color: #FF4D4F; }
  .sheet-cancel { background: #2a2a3e; color: #888; }
}
</style>