<!--
  持仓管理页面 Positions.vue
  显示所有持仓股票，支持新增/编辑/删除，展示当日涨跌、持仓盈亏、多维评分、止盈止损线
-->
<template>
  <div class="positions-page">
    <!-- 页头：标题 + 总盈亏 + 可用资金 + 新增按钮 -->
    <div class="page-header">
      <h2>持仓管理</h2>
      <div class="header-right">
        <div class="total-pnl" :class="totalPnl >= 0 ? 'up' : 'down'">
          总盈亏: {{ totalPnl >= 0 ? '+' : '' }}¥{{ totalPnl.toFixed(2) }}
          <button class="btn-reset" @click="resetPnl">清零</button>
        </div>
        <div class="balance" v-if="!editingBalance" @click="editBalanceStart">可用资金: ¥{{ availableBalance.toFixed(2) }} ✏️</div>
        <div class="balance-editing" v-else>
          <input ref="balanceInput" v-model.number="balanceInputVal" type="number" step="0.01" @blur="editBalanceSave" @keydown.enter="editBalanceSave" @keydown.escape="editBalanceCancel" />
        </div>
        <button class="btn-add" @click="showAdd = true">+ 新增持仓</button>
      </div>
    </div>

    <!-- 新增/编辑持仓弹窗 -->
    <div class="modal-overlay" v-if="showAdd" @click.self="showAdd = false">
      <div class="modal">
        <div class="modal-title">{{ editingIdx >= 0 ? '编辑持仓' : '新增持仓' }}</div>
        <div class="form-row">
          <label>代码</label>
          <input v-model="formCode" placeholder="输入代码" @input="onCodeInput" :disabled="editingIdx >= 0" />
          <span class="lookup-result" v-if="lookupName">{{ lookupName }} ¥{{ lookupPrice?.toFixed(2) }}</span>
        </div>
        <div class="form-row">
          <label>成本价</label>
          <input v-model.number="formCost" type="number" step="0.001" placeholder="成本价" />
        </div>
        <div class="form-row">
          <label>持股数</label>
          <input v-model.number="formQty" type="number" step="1" placeholder="持股数量" />
        </div>
        <div class="form-row">
          <label>止盈%</label>
          <input v-model.number="formTp" type="number" step="0.1" placeholder="默认+8%" />
        </div>
        <div class="form-row">
          <label>止损%</label>
          <input v-model.number="formSl" type="number" step="0.1" placeholder="默认-5%" />
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showAdd = false">取消</button>
          <button class="btn-confirm" @click="confirmAdd">确定</button>
        </div>
      </div>
    </div>

    <!-- 持仓列表 -->
    <div class="positions-table" v-if="holdings.length">
      <div class="table-header">
        <span class="col-code">代码</span>
        <span class="col-name">名称</span>
        <span class="col-num">数量</span>
        <span class="col-price">成本价</span>
        <span class="col-price">现价</span>
        <span class="col-chg">当日涨跌</span>
        <span class="col-chg">持仓盈亏</span>
        <span class="col-sig" title="有策略信号">⚡</span>
        <span class="col-score" title="N形≥60可操作">N</span>
        <span class="col-score" title="龙头≥70买入">龙</span>
        <span class="col-score" title="动量≥50关注">量</span>
        <span class="col-sl">止盈/止损</span>
      </div>
      <div v-for="h in holdings" :key="h.code" :class="rowClass(h)">
        <span class="col-code">{{ h.code }}</span>
        <span class="col-name">{{ h.name }}</span>
        <span class="col-num">{{ h.quantity }}</span>
        <span class="col-price">{{ h.cost_price?.toFixed(2) }}</span>
        <span class="col-price">{{ h.cur_price?.toFixed(2) }}</span>
        <span :class="['col-chg', (h.change_pct || 0) >= 0 ? 'up' : 'down']">
          {{ (h.change_pct || 0) > 0 ? '+' : '' }}{{ (h.change_pct || 0).toFixed(2) }}%
        </span>
        <span :class="['col-chg', (h.pnl_pct || 0) >= 0 ? 'up' : 'down']">
          {{ (h.pnl_pct || 0) > 0 ? '+' : '' }}{{ (h.pnl_pct || 0).toFixed(2) }}%
        </span>
        <span v-if="h.signal_active" class="col-sig" title="有策略信号">⚡</span>
        <span v-else class="col-sig dim">—</span>
        <span :class="['col-score', (h.n_score||0) >= 60 ? 'strong' : ((h.n_score||0) > 0 ? 'watch' : '')]">
          {{ (h.n_score || 0) > 0 ? h.n_score.toFixed(0) : '—' }}
        </span>
        <span :class="['col-score', (h.dragon_score||0) >= 70 ? 'strong' : ((h.dragon_score||0) >= 50 ? 'watch' : '')]">
          {{ (h.dragon_score || 0) > 0 ? h.dragon_score.toFixed(0) : '—' }}
        </span>
        <span :class="['col-score', (h.m_score||0) >= 50 ? 'watch' : '']">
          {{ (h.m_score || 0) > 0 ? h.m_score.toFixed(0) : '—' }}
        </span>
        <span class="col-sl">
          <span class="sl-tp">+{{ (h.take_profit_pct||8).toFixed(1) }}%</span>
          <span class="sl-div">/</span>
          <span class="sl-sel">-{{ (h.stop_loss_pct||5).toFixed(1) }}%</span>
        </span>
        <span class="col-actions">
          <button class="btn-edit" @click="editHolding(h)">编辑</button>
          <button class="btn-sell" @click="removeHolding(h)">删除</button>
        </span>
      </div>
    </div>
    <div class="empty" v-else>
      <p>暂无持仓</p>
      <p class="hint">点击右上角「新增持仓」手动添加，或通过信号页确认买入自动更新</p>
    </div>

    <!-- 图例说明 -->
    <div class="legend">
      <span><span class="lg-dot up"></span>当日涨跌红涨绿跌</span>
      <span class="lg-sep">|</span>
      <span><span class="lg-dot warn"></span>持仓盈亏红赚绿亏</span>
      <span class="lg-sep">|</span>
      <span>⚡ 有策略信号</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">止盈+8% / 止损-5%</span>
      <span class="lg-sep">|</span>
      <span>N≥60可买 龙≥70买 量≥50关注</span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue' // Vue 组合式 API：响应式、计算属性、侦听器、DOM 更新钩子与生命周期钩子
import * as api from '../api/index.js'                                        // 后端 API 调用封装（持仓、资金、状态等）

// ── 响应式状态 ──
const holdings = ref([])                    // 持仓列表
const availableBalance = ref(0)             // 可用资金
const showAdd = ref(false)                  // 是否显示新增/编辑弹窗
const pnlOffset = ref(parseFloat(localStorage.getItem('pnl_offset') || '0'))  // 盈亏清零偏移量

// ── 本地持久化镜像：进 tab 秒开，增删改才变更 ──
const CACHE_KEY = 'pos_cache_v1'   // localStorage 键名：持仓与资金缓存快照
const BALANCE_KEY = 'pos_balance_v1' // localStorage 键名：可用资金缓存（预留）
/** 将当前持仓与资金快照写入 localStorage 缓存 */
function persistCache() {
  try {
    // 将持仓与资金快照写入 localStorage
    localStorage.setItem(CACHE_KEY, JSON.stringify({ holdings: holdings.value, balance: availableBalance.value }))
  } catch (_) {}
}
/** 从 localStorage 缓存恢复持仓与资金，实现进页面秒开 */
function loadCache() {
  try {
    // 从本地缓存恢复持仓与资金，进页面秒开
    const raw = localStorage.getItem(CACHE_KEY)
    const d = raw ? JSON.parse(raw) : null
    if (d) {
      holdings.value = Array.isArray(d.holdings) ? d.holdings : []
      availableBalance.value = d.balance || 0
    }
  } catch (_) {}
}
// 侦听持仓与资金变化，深拷贝写入本地缓存
watch([holdings, availableBalance], persistCache, { deep: true })

/** 计算总盈亏 = Σ(现价-成本)*数量 - 偏移量 */
const totalPnl = computed(() => {
  let sum = 0
  // 累加每只持仓的 (现价-成本)*数量
  for (const h of holdings.value) {
    const qty = h.quantity || 1
    const cost = h.cost_price || 0
    const cur = h.cur_price || 0
    sum += (cur - cost) * qty
  }
  return sum - pnlOffset.value
})

/** 清零总盈亏：将当前累计盈亏记录为偏移量 */
function resetPnl() {
  pnlOffset.value = 0
  // 将当前累计盈亏累加为偏移量，实现界面清零
  for (const h of holdings.value) {
    const qty = h.quantity || 1
    const cost = h.cost_price || 0
    const cur = h.cur_price || 0
    pnlOffset.value += (cur - cost) * qty
  }
  localStorage.setItem('pnl_offset', pnlOffset.value.toString())
}

// ── 表单状态 ──
const editingIdx = ref(-1)       // -1 表示新增，>=0 表示编辑对应索引
const formCode = ref('')         // 新增/编辑表单：股票代码
const formCost = ref(0)          // 新增/编辑表单：成本价
const formQty = ref(0)           // 新增/编辑表单：持股数量
const lookupName = ref('')       // 代码查询返回的股票名称
const lookupPrice = ref(0)       // 代码查询返回的现价
const formTp = ref(8)            // 默认止盈 +8%
const formSl = ref(5)            // 默认止损 -5%
const editingBalance = ref(false)  // 是否正在编辑可用资金（显示输入框）
const balanceInputVal = ref(0)     // 可用资金编辑输入框的值
const balanceInput = ref(null)     // 可用资金编辑输入框的 DOM 引用（用于自动聚焦）

let timer = null   // 30 秒轮询定时器句柄

/** 进入编辑可用资金模式 */
function editBalanceStart() {
  balanceInputVal.value = availableBalance.value
  editingBalance.value = true
  nextTick(() => balanceInput.value?.focus())
}
/** 保存可用资金编辑结果 */
function editBalanceSave() {
  // 写入资金并同步保存到后端
  availableBalance.value = balanceInputVal.value
  editingBalance.value = false
  saveHoldings()
}
/** 取消可用资金编辑 */
function editBalanceCancel() {
  editingBalance.value = false
}

/** 根据涨跌/盈亏/信号等返回行 CSS 类名 */
function rowClass(h) {
  const chg = h.change_pct || 0
  const pnl = h.pnl_pct || 0
  // 依次判定：信号 / 大涨 / 触达止损 / 异动，返回对应高亮类
  if (h.signal_active) return 'table-row signal'
  if (chg >= 5 || pnl >= 8) return 'table-row strong'
  if (curReachedStop(h)) return 'table-row danger'
  if (chg >= 3 || pnl >= 5 || chg <= -3 || pnl <= -5) return 'table-row watch'
  return 'table-row'
}
/** 判断是否已触达止盈或止损线 */
function curReachedStop(h) {
  if (!h.cur_price || !h.stop_loss) return false
  // 现价跌破止损或涨破止盈即视为触达
  return h.cur_price <= h.stop_loss || h.cur_price >= h.take_profit
}

/** 从 API 加载持仓和可用资金 */
async function load() {
  try {
    // 先拉取会话状态，再加载持仓与资金
    const st = await api.fetchStatus()
    api.setLastSession(st.session)
    const data = await api.fetchHoldings()
    if (data) {
      holdings.value = data.holdings || []
      availableBalance.value = data.available_balance || 0
    }
  } catch (_) {}
}

/** 持久化保存持仓和可用资金 */
async function saveHoldings() {
  try {
    await api.updateHoldings({ holdings: holdings.value, available_balance: availableBalance.value })
  } catch (_) {}
}

/** 输入代码时自动查询股票名称和现价 */
async function onCodeInput() {
  const code = formCode.value.trim()
  if (code.length < 5) { lookupName.value = ''; return }
  try {
    // 按代码查询股票名称与现价
    const data = await api.fetchStockLookup(code)
    if (data && data.name) {
      lookupName.value = data.name
      lookupPrice.value = data.price || 0
    } else {
      lookupName.value = '未找到'
      lookupPrice.value = 0
    }
  } catch (_) { lookupName.value = '' }
}

/** 提交新增或编辑的持仓 */
async function confirmAdd() {
  const code = formCode.value.trim()
  if (!code || !formCost.value || !formQty.value) { alert('请填写完整信息'); return }
  // 组装持仓对象，默认止盈 +8% / 止损 -5%
  const item = {
    code,
    name: lookupName.value || code,
    quantity: formQty.value,
    cost_price: formCost.value,
    cur_price: lookupPrice.value || 0,
    pnl_pct: 0,
    change_pct: 0,
    take_profit_pct: formTp.value || 8,
    stop_loss_pct: formSl.value || 5,
  }
  if (editingIdx.value >= 0) {
    // 编辑模式：原地更新
    holdings.value[editingIdx.value] = { ...holdings.value[editingIdx.value], quantity: formQty.value, cost_price: formCost.value,
      take_profit_pct: formTp.value, stop_loss_pct: formSl.value }
  } else {
    // 新增模式：追加到列表
    holdings.value.push(item)
  }
  // 保存后关闭弹窗并复位表单
  await saveHoldings()
  showAdd.value = false
  editingIdx.value = -1
  resetForm()
}

/** 打开编辑弹窗，回填数据 */
function editHolding(h) {
  // 定位索引并回填表单字段
  editingIdx.value = holdings.value.indexOf(h)
  formCode.value = h.code
  formCost.value = h.cost_price
  formQty.value = h.quantity
  lookupName.value = h.name
  lookupPrice.value = h.cur_price
  formTp.value = h.take_profit_pct || 8
  formSl.value = h.stop_loss_pct || 5
  showAdd.value = true
}

/** 删除持仓 */
function removeHolding(h) {
  if (!confirm(`确认删除持仓 ${h.code} ${h.name}？`)) return
  // 从列表移除并持久化保存
  holdings.value = holdings.value.filter(x => x.code !== h.code)
  saveHoldings()
}

/** 重置新增表单 */
function resetForm() {
  formCode.value = ''
  formCost.value = 0
  formQty.value = 0
  lookupName.value = ''
  lookupPrice.value = 0
}

// 先读本地缓存秒开，再拉取最新数据，并启动 30s 轮询
onMounted(() => { loadCache(); load(); timer = setInterval(load, 30000) })
// 卸载时清理定时器
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.positions-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.header-right { display: flex; align-items: center; gap: 12px; }
.total-pnl { font-size: 16px; font-weight: 700; white-space: nowrap; margin-right: 16px; }
.total-pnl.up { color: #e74c3c; }
.total-pnl.down { color: #27ae60; }
.btn-reset { font-size: 11px; margin-left: 8px; padding: 2px 8px; border: 1px solid #555; border-radius: 4px; background: none; color: #aaa; cursor: pointer; }
.btn-reset:hover { background: #333; }
.balance { font-size: 14px; color: #4caf50; font-weight: 600; cursor: pointer; }
.balance-editing input { width: 150px; padding: 6px 10px; border-radius: 6px; border: 1px solid #4caf50; background: #0f0f23; color: #4caf50; font-size: 14px; font-weight: 600; text-align: right; outline: none; }
.btn-add {
  padding: 8px 16px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 13px; cursor: pointer;
}
.positions-table { background: #1a1a2e; border-radius: 8px; overflow-x: auto; font-size: 13px; white-space: nowrap; }
.table-header, .table-row {
  display: flex; align-items: center; padding: 10px 16px; gap: 0;
  min-width: 860px;
}
.table-header { background: #2a2a3e; color: #888; font-weight: 600; }
.table-row { border-bottom: 1px solid #2a2a3e; }
.table-row.signal { background: rgba(79,195,247,0.08); }
.table-row.danger { background: rgba(250,173,20,0.15); }
.table-row.watch { background: rgba(250,173,20,0.08); }
.table-row.strong { background: rgba(255,77,79,0.10); }
.table-row:last-child { border-bottom: none; }

/* 所有字段等宽分布，溢出横向滚动 */
.col-code  { flex: 1; color: #4fc3f7; text-align: center; }
.col-name  { flex: 1; overflow: hidden; text-overflow: ellipsis; }
.col-num   { flex: 1; text-align: center; }
.col-price { flex: 1; text-align: center; }
.col-chg   { flex: 1; text-align: center; }
.col-chg.up   { color: #FF4D4F; font-weight: 700; }
.col-chg.down { color: #4caf50; font-weight: 700; }
.col-sig    { flex: 1; text-align: center; }
.col-sig.dim { color: #333; }
.col-score  { flex: 1; text-align: center; }
.col-score.strong { color: #FF4D4F; font-weight: 700; }
.col-score.watch  { color: #FAAD14; }
.col-sl     { flex: 1; text-align: center; }
.sl-tp { color: #FF4D4F; }
.sl-div { color: #333; margin: 0 2px; }
.sl-sel { color: #4caf50; }
.col-actions { flex: 0 0 100px; text-align: center; white-space: nowrap; }
.col-actions { display: flex; gap: 6px; flex: 0 0 100px; }
.btn-edit, .btn-sell {
  padding: 4px 12px; border-radius: 4px; font-size: 12px; cursor: pointer;
}
.btn-edit { border: 1px solid #4fc3f7; background: transparent; color: #4fc3f7; }
.btn-edit:hover { background: rgba(79,195,247,0.1); }
.btn-sell { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-sell:hover { background: rgba(250,173,20,0.1); }
.empty { text-align: center; padding: 60px; color: #555; font-size: 13px; }
.hint { color: #444; font-size: 12px; margin-top: 8px; }

/* modal */
.modal-overlay {
  position: fixed; top: 0; left: 0; width: 100%; height: 100%;
  background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal {
  background: #1a1a2e; border-radius: 10px; padding: 24px; width: 360px;
}
.modal-title { font-size: 16px; font-weight: 600; color: #e0e0e0; margin-bottom: 16px; }
.form-row { margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.form-row label { width: 56px; color: #888; font-size: 13px; flex-shrink: 0; }
.form-row input {
  flex: 1; padding: 8px 12px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 13px; outline: none;
}
.form-row input:focus { border-color: #FF4D4F; }
.lookup-result { font-size: 11px; color: #4caf50; white-space: nowrap; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.btn-cancel {
  padding: 8px 20px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #888; font-size: 13px; cursor: pointer;
}
.btn-confirm {
  padding: 8px 20px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 13px; cursor: pointer;
}
.legend {
  margin-top: 12px; padding: 6px 12px; font-size: 11px; color: #666;
  background: #1a1a2e; border-radius: 6px; display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
}
.lg-sep { color: #333; }
.lg-item { color: #666; }
.lg-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 3px; vertical-align: middle; }
.lg-dot.up { background: #FF4D4F; }
.lg-dot.warn { background: #FAAD14; }
</style>
