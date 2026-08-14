<!--
  持仓管理页面 Positions.vue
  Holdings management page Positions.vue
  显示所有持仓股票，支持新增/编辑/删除，展示当日涨跌、持仓盈亏、多维评分、止盈止损线
  Lists all held stocks with add/edit/delete, showing daily change, position P&L, multi-dimension scores, and take-profit/stop-loss lines
-->
<template>
  <div class="positions-page">
    <!-- 页头：标题 + 总盈亏 + 可用资金 + 新增按钮（Header: title + total P&L + available cash + add button）-->
    <div class="page-header">
      <h2>持仓管理</h2>
      <div class="header-right">
        <!-- 总盈亏：红赚绿亏，点击"清零"把当前累计盈亏记为偏移量（Total P&L: red = gain, green = loss; "清零" recollects the cumulative P&L as an offset）-->
        <div class="total-pnl" :class="totalPnl >= 0 ? 'up' : 'down'">
          总盈亏: {{ totalPnl >= 0 ? '+' : '' }}¥{{ totalPnl.toFixed(2) }}
          <button class="btn-reset" @click="resetPnl">清零</button>
        </div>
        <!-- 可用资金：点击切换到编辑输入框，回车/失焦保存（Available cash: click to switch into an edit input; enter or blur saves）-->
        <div class="balance" v-if="!editingBalance" @click="editBalanceStart">可用资金: ¥{{ availableBalance.toFixed(2) }} ✏️</div>
        <div class="balance-editing" v-else>
          <input ref="balanceInput" v-model.number="balanceInputVal" type="number" step="0.01" @blur="editBalanceSave" @keydown.enter="editBalanceSave" @keydown.escape="editBalanceCancel" />
        </div>
        <!-- 新增持仓按钮：弹出新增/编辑弹窗（Add-holding button: opens the add/edit modal）-->
        <button class="btn-add" @click="openAddNew">+ 新增持仓</button>
      </div>
    </div>

    <!-- 新增/编辑持仓弹窗（Add/edit holding modal）-->
    <div class="modal-overlay" v-if="showAdd" @click.self="closeAdd">
      <div class="modal">
        <div class="modal-title">{{ editingIdx >= 0 ? '编辑持仓' : '新增持仓' }}</div>
        <!-- 代码输入行：编辑模式禁用，输入时自动查询股票名称与现价（Code input row: disabled in edit mode; auto-looks-up name and current price while typing）-->
        <div class="form-row">
          <label>代码</label>
          <input v-model="formCode" placeholder="输入代码" @input="onCodeInput" :disabled="editingIdx >= 0" />
          <span class="lookup-result" v-if="lookupName">{{ lookupName }} ¥{{ lookupPrice?.toFixed(2) }}</span>
        </div>
        <!-- 成本价 / 持股数输入行（Cost price / share quantity input rows）-->
        <div class="form-row">
          <label>成本价</label>
          <input v-model.number="formCost" type="number" step="0.001" placeholder="成本价" />
        </div>
        <div class="form-row">
          <label>持股数</label>
          <input v-model.number="formQty" type="number" step="1" placeholder="持股数量" />
        </div>
        <!-- 止盈 / 止损百分比输入行（留空则使用默认 +8% / -5%）（Take-profit / stop-loss % input rows; blank falls back to +8% / -5% defaults）-->
        <div class="form-row">
          <label>止盈%</label>
          <input v-model.number="formTp" type="number" step="0.1" placeholder="默认+8%" />
        </div>
        <div class="form-row">
          <label>止损%</label>
          <input v-model.number="formSl" type="number" step="0.1" placeholder="默认-5%" />
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="closeAdd">取消</button>
          <button class="btn-confirm" @click="confirmAdd">确定</button>
        </div>
      </div>
    </div>

    <!-- 持仓列表（Holdings list）-->
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
        <span class="col-kline">分时</span>
        <span class="col-actions">操作</span>
      </div>
      <!-- 持仓行 + 可展开 分时区（Holding row + expandable K-line area）-->
      <div v-for="h in holdings" :key="h.code" class="pos-row-group">
      <div :class="rowClass(h)" @click="onRowTap(h)">
        <span class="col-code" data-label="代码">{{ h.code }}</span>
        <span class="col-name" data-label="名称">{{ h.name }}</span>
        <span class="col-num" data-label="数量">{{ h.quantity }}</span>
        <span class="col-price" data-label="成本价">{{ h.cost_price?.toFixed(2) }}</span>
        <span class="col-price" data-label="现价">{{ h.cur_price?.toFixed(2) }}</span>
        <span :class="['col-chg', (h.change_pct || 0) >= 0 ? 'up' : 'down']" data-label="当日涨跌">
          {{ (h.change_pct || 0) > 0 ? '+' : '' }}{{ (h.change_pct || 0).toFixed(2) }}%
        </span>
        <span :class="['col-chg', (h.pnl_pct || 0) >= 0 ? 'up' : 'down']" data-label="持仓盈亏">
          {{ (h.pnl_pct || 0) > 0 ? '+' : '' }}{{ (h.pnl_pct || 0).toFixed(2) }}%
        </span>
        <span v-if="h.signal_active" class="col-sig" data-label="信号" title="有策略信号">⚡</span>
        <span v-else class="col-sig dim" data-label="信号">—</span>
        <span :class="['col-score', (h.n_score||0) >= 60 ? 'strong' : ((h.n_score||0) > 0 ? 'watch' : '')]" data-label="N形">
          {{ (h.n_score || 0) > 0 ? h.n_score.toFixed(0) : '—' }}
        </span>
        <span :class="['col-score', (h.dragon_score||0) >= 70 ? 'strong' : ((h.dragon_score||0) >= 50 ? 'watch' : '')]" data-label="龙头">
          {{ (h.dragon_score || 0) > 0 ? h.dragon_score.toFixed(0) : '—' }}
        </span>
        <span :class="['col-score', (h.m_score||0) >= 50 ? 'watch' : '']" data-label="动量">
          {{ (h.m_score || 0) > 0 ? h.m_score.toFixed(0) : '—' }}
        </span>
        <span class="col-sl" data-label="止盈/止损">
          <span class="sl-tp">+{{ (h.take_profit_pct||8).toFixed(1) }}%</span>
          <span class="sl-div">/</span>
          <span class="sl-sel">-{{ (h.stop_loss_pct||5).toFixed(1) }}%</span>
        </span>
        <span class="col-kline" data-label="分时">
          <button class="btn-kline" @click.stop="toggleKline(h.code)" :title="klineOpen.has(h.code) ? '收起分时' : '展开分时'">{{ klineOpen.has(h.code) ? '收起' : '分时' }}</button>
        </span>
        <span class="col-actions" data-label="操作">
          <button class="btn-lot" @click.stop="openAddLot(h)">加减仓</button>
          <button class="btn-cost" @click.stop="openSetCost(h)">改成本</button>
          <button class="btn-edit" @click.stop="showLotsFor(h)">明细</button>
          <button class="btn-edit" @click.stop="editHolding(h)">编辑</button>
          <button class="btn-sell" @click.stop="openCloseHolding(h)">清仓</button>
        </span>
      </div>
      <!-- 展开的 分时区（全宽，位于该行下方）（Expanded K-line area, full width, below the row）-->
      <div v-if="klineOpen.has(h.code)" class="pos-kline-row">
        <KLineChart :code="h.code" :name="h.name" />
      </div>
      </div>
    </div>

    <!-- 移动端：点击行弹出的底部操作菜单 -->
    <div class="sheet-overlay" v-if="sheetHolding" @click="sheetHolding = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetHolding.code }} {{ sheetHolding.name }}</div>
        <button class="sheet-btn" @click="sheetKline">{{ klineOpen.has(sheetHolding.code) ? '收起分时' : '展开分时' }}</button>
        <button class="sheet-btn" @click="sheetLot">加减仓</button>
        <button class="sheet-btn" @click="sheetCost">改成本</button>
        <button class="sheet-btn" @click="sheetLots">加仓明细</button>
        <button class="sheet-btn" @click="sheetEdit">编辑持仓</button>
        <button class="sheet-btn sheet-danger" @click="sheetClose">清仓</button>
        <button class="sheet-btn sheet-cancel" @click="sheetHolding = null">取消</button>
      </div>
    </div>
    <div class="modal-overlay" v-if="showLot" @click.self="showLot = false">
      <div class="modal">
        <div class="modal-title">
          加减仓 {{ lotTarget?.code }} {{ lotTarget?.name }}
          <span class="lot-dir">
            <button :class="['dir-btn', lotDir === 'add' ? 'active-add' : '']" @click="lotDir = 'add'">加仓</button>
            <button :class="['dir-btn', lotDir === 'sell' ? 'active-sell' : '']" @click="lotDir = 'sell'">减仓</button>
          </span>
        </div>
        <div class="form-row">
          <label>当前数量</label>
          <span class="static-val">{{ lotTarget?.quantity }}</span>
          <label style="width:auto">当前成本</label>
          <span class="static-val">¥{{ lotTarget?.cost_price?.toFixed(2) }}</span>
        </div>
        <div class="form-row">
          <label>现价</label>
          <span class="static-val">{{ lotCurrentPrice > 0 ? '¥' + lotCurrentPrice.toFixed(2) : '—' }}</span>
          <button v-if="lotCurrentPrice > 0" class="btn-lot" style="margin-left:8px" @click="lotFormPrice = lotCurrentPrice">按现价</button>
        </div>
        <div class="form-row">
          <label>{{ lotDir === 'add' ? '加仓价' : '减仓价' }}</label>
          <input v-model.number="lotFormPrice" type="number" step="0.001" placeholder="成交价格（默认现价）" />
        </div>
        <div class="form-row">
          <label>{{ lotDir === 'add' ? '加仓数量' : '减仓数量' }}</label>
          <input v-model.number="lotFormQty" type="number" step="1" placeholder="成交数量" />
        </div>
        <div class="preview" v-if="lotPreviewQty > 0">
          <template v-if="lotDir === 'add'">
            加仓后：共 {{ lotPreviewQty }} 股 / 平均成本 ¥{{ lotPreviewCost.toFixed(3) }}
          </template>
          <template v-else>
            <span :class="lotOverSell ? 'over-sell' : ''">
              {{ lotOverSell ? '减仓数量超过持仓！' : '减仓后：剩余 ' + lotPreviewQty + ' 股 / 平均成本 ¥' + lotPreviewCost.toFixed(3) }}
            </span>
          </template>
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showLot = false">取消</button>
          <button class="btn-confirm" :class="lotDir === 'sell' ? 'btn-confirm-sell' : ''" @click="confirmLot" :disabled="lotOverSell || fareCalcDisabled">
            {{ lotDir === 'add' ? '确定加仓' : '确定减仓' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 改成本弹窗：直接设置目标成本价（Set-cost modal: directly set the target cost price）-->
    <div class="modal-overlay" v-if="showCost" @click.self="showCost = false">
      <div class="modal">
        <div class="modal-title">更新成本 {{ costTarget?.code }} {{ costTarget?.name }}</div>
        <div class="form-row">
          <label>目标成本</label>
          <input v-model.number="costFormPrice" type="number" step="0.001" placeholder="新的成本价" />
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showCost = false">取消</button>
          <button class="btn-confirm" @click="confirmSetCost">确定</button>
        </div>
      </div>
    </div>

    <!-- 清仓弹窗：展示当前数量/成本，输入清仓价，实时预览盈亏（Close-out modal: shows qty/cost, inputs the close price, and previews the P&L live）-->
    <div class="modal-overlay" v-if="showClose" @click.self="showClose = false">
      <div class="modal">
        <div class="modal-title">清仓 {{ closeTarget?.code }} {{ closeTarget?.name }}</div>
        <div class="form-row">
          <label>当前持仓</label>
          <span class="static-val">{{ closeTarget?.quantity }} 股 / 成本 ¥{{ closeTarget?.cost_price?.toFixed(2) }}</span>
        </div>
        <div class="form-row">
          <label>清仓价</label>
          <input v-model.number="closeFormPrice" type="number" step="0.001" placeholder="清仓价格" @input="closePriceInput" />
        </div>
        <div class="preview" v-if="closePreviewValid">
          清仓盈亏：<span :class="closePnlAmount >= 0 ? 'pnl-up' : 'pnl-down'">{{ closePnlAmount >= 0 ? '+' : '' }}¥{{ closePnlAmount.toFixed(2) }}</span>
          （{{ closePnlPct >= 0 ? '+' : '' }}{{ closePnlPct.toFixed(2) }}%）
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showClose = false">取消</button>
          <button class="btn-confirm" @click="confirmCloseHolding">确认清仓</button>
        </div>
      </div>
    </div>

    <!-- 加仓批次明细弹窗（Lot-detail modal）-->
    <div class="modal-overlay" v-if="showLots && lotsTarget" @click.self="showLots = false">
      <div class="modal wide">
        <div class="modal-title">加仓明细 {{ lotsTarget.code }} {{ lotsTarget.name }}</div>
        <div class="lots-table">
          <div class="lots-header">
            <span>时间</span><span>价格</span><span>数量</span><span>金额</span>
          </div>
          <div class="lots-row" v-for="(lot, i) in lotsTarget.lots || []" :key="i">
            <span>{{ (lot.at || '').replace('T', ' ').slice(0, 19) }}</span>
            <span>¥{{ lot.price?.toFixed(3) }}</span>
            <span>{{ lot.quantity }}</span>
            <span>¥{{ (lot.price * lot.quantity).toFixed(2) }}</span>
          </div>
          <div class="lots-footer">
            合计：{{ lotsTarget.quantity }} 股 / 平均成本 ¥{{ lotsTarget.cost_price?.toFixed(3) }}
          </div>
        </div>
        <div class="modal-actions">
          <button class="btn-confirm" @click="showLots = false">关闭</button>
        </div>
      </div>
    </div>
    <div class="empty" v-else>
      <p>暂无持仓</p>
      <p class="hint">点击右上角「新增持仓」手动添加，或通过信号页确认买入自动更新</p>
    </div>

    <!-- 图例说明（Legend）-->
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
// Vue Composition API: reactive refs, computed, watchers, nextTick, and lifecycle hooks
import * as api from '../api/index.js'                                        // 后端 API 调用封装（持仓、资金、状态等）
// backend API wrapper (holdings, funds, status etc.)
import KLineChart from '../components/KLineChart.vue'                         // 分时图组件（展开行展示）
// K-line chart component (shown in expanded rows)

// ── 响应式状态 ──
// ── Reactive state ──
const holdings = ref([])                    // 持仓列表
// the holdings list
const klineOpen = ref(new Set())            // 已展开分时的持仓代码集合
// the set of holding codes whose K-line is expanded
const availableBalance = ref(0)             // 可用资金
// available cash
const showAdd = ref(false)                  // 是否显示新增/编辑弹窗
// whether the add/edit modal is visible
const pnlOffset = ref(parseFloat(localStorage.getItem('pnl_offset') || '0'))  // 盈亏清零偏移量
// P&L reset offset

// ── 本地持久化镜像：进 tab 秒开，增删改才变更 ──
// ── Local persisted mirror: instant open on tab entry; only mutated on add/remove/edit ──
const CACHE_KEY = 'pos_cache_v1'   // localStorage 键名：持仓与资金缓存快照
// localStorage key: holdings + cash cache snapshot
const BALANCE_KEY = 'pos_balance_v1' // localStorage 键名：可用资金缓存（预留）
// localStorage key: available-cash cache (reserved)
/** 将当前持仓与资金快照写入 localStorage 缓存 */
/** Write the current holdings + cash snapshot into the localStorage cache */
function persistCache() {
  try {
    // 将持仓与资金快照写入 localStorage
    // Persist the holdings and cash snapshot to localStorage
    localStorage.setItem(CACHE_KEY, JSON.stringify({ holdings: holdings.value, balance: availableBalance.value }))
  } catch (_) {}
}
/** 从 localStorage 缓存恢复持仓与资金，实现进页面秒开 */
/** Restore holdings and cash from the localStorage cache for instant page open */
function loadCache() {
  try {
    // 从本地缓存恢复持仓与资金，进页面秒开
    // Restore holdings and cash from local cache, opening the page instantly
    const raw = localStorage.getItem(CACHE_KEY)
    const d = raw ? JSON.parse(raw) : null
    if (d) {
      holdings.value = Array.isArray(d.holdings) ? d.holdings : []
      availableBalance.value = d.balance || 0
    }
  } catch (_) {}
}
// 侦听持仓与资金变化，深拷贝写入本地缓存
// Watch holdings and cash; write a deep copy into the local cache on change
watch([holdings, availableBalance], persistCache, { deep: true })

/** 计算总盈亏 = Σ(现价-成本)*数量 - 偏移量 */
/** Total P&L = Σ(current - cost) * quantity - offset */
const totalPnl = computed(() => {
  let sum = 0
  // 累加每只持仓的 (现价-成本)*数量
  // Sum (current - cost) * quantity over every holding
  for (const h of holdings.value) {
    const qty = h.quantity || 1
    const cost = h.cost_price || 0
    const cur = h.cur_price || 0
    sum += (cur - cost) * qty
  }
  return sum - pnlOffset.value
})

/** 清零总盈亏：将当前累计盈亏记录为偏移量 */
/** Reset total P&L: store the current cumulative P&L as an offset */
function resetPnl() {
  pnlOffset.value = 0
  // 将当前累计盈亏累加为偏移量，实现界面清零
  // Accumulate the current P&L into the offset to zero the display
  for (const h of holdings.value) {
    const qty = h.quantity || 1
    const cost = h.cost_price || 0
    const cur = h.cur_price || 0
    pnlOffset.value += (cur - cost) * qty
  }
  localStorage.setItem('pnl_offset', pnlOffset.value.toString())
}

// ── 表单状态 ──
// ── Form state ──
const editingIdx = ref(-1)       // -1 表示新增，>=0 表示编辑对应索引
// -1 = creating new, >=0 = editing the entry at this index
const formCode = ref('')         // 新增/编辑表单：股票代码
// add/edit form: stock code
const formCost = ref(0)          // 新增/编辑表单：成本价
// add/edit form: cost price
const formQty = ref(0)           // 新增/编辑表单：持股数量
// add/edit form: share quantity
const lookupName = ref('')       // 代码查询返回的股票名称
// stock name returned by the code lookup
const lookupPrice = ref(0)       // 代码查询返回的现价
// current price returned by the code lookup

// ── 加仓 / 改成本 / 明细 弹窗状态 ──
// ── Add-lot / set-cost / lot-detail modal state ──
const showLot = ref(false)           // 是否显示加减仓弹窗
// whether the add/trim lot modal is visible
const showCost = ref(false)          // 是否显示改成本弹窗
// whether the set-cost modal is visible
const showLots = ref(false)          // 是否显示批次明细弹窗
// whether the lot-detail modal is visible
const lotTarget = ref(null)          // 加减仓目标持仓
// the holding targeted by add/trim
const costTarget = ref(null)         // 改成本目标持仓
// the holding targeted by set-cost
const lotsTarget = ref(null)         // 明细目标持仓
// the holding whose lots are shown
const lotDir = ref('add')            // 加减仓方向：'add' 加仓 / 'sell' 减仓
// lot direction: 'add' or 'sell'
const lotFormPrice = ref(0)          // 加减仓弹窗：成交价
// add/trim modal: fill price
const lotFormQty = ref(0)            // 加减仓弹窗：成交数量
// add/trim modal: fill quantity
const lotCurrentPrice = ref(0)       // 加减仓弹窗：当前实时现价（打开时刷新）
// add/trim modal: current live price, refreshed on open
// add/trim modal: live current price, refreshed on open
const costFormPrice = ref(0)         // 改成本弹窗：目标成本
// set-cost modal: target cost

// ── 清仓弹窗状态 ──
// ── Close-out modal state ──
const showClose = ref(false)         // 是否显示清仓弹窗
// whether the close-out modal is visible
const closeTarget = ref(null)        // 清仓目标持仓
// the holding targeted by close-out
const closeFormPrice = ref(0)        // 清仓弹窗：清仓价
// close-out modal: close price
const closePnlAmount = ref(0)        // 清仓弹窗：盈亏金额
// close-out modal: P&L amount
const closePnlPct = ref(0)           // 清仓弹窗：盈亏比例
// close-out modal: P&L percentage
const sheetHolding = ref(null)       // 移动端操作菜单当前选中的持仓对象
// the holding currently selected in the mobile action sheet
const closePreviewValid = ref(false) // 清仓弹窗：是否可展示盈亏预览
// close-out modal: whether the P&L preview is valid

/** 输入清仓价时实时计算盈亏 = (清仓价-成本)×数量 */
/** Recompute P&L live while typing the close price: (close - cost) * quantity */
function closePriceInput() {
  const t = closeTarget.value
  const price = Number(closeFormPrice.value) || 0
  if (!t || price <= 0 || !t.quantity) { closePreviewValid.value = false; return }
  const qty = t.quantity || 1
  const cost = t.cost_price || 0
  closePnlAmount.value = (price - cost) * qty
  closePnlPct.value = cost > 0 ? (price - cost) / cost * 100 : 0
  closePreviewValid.value = true
}

/** 打开清仓弹窗：回填当前持仓并默认清仓价为现价 */
/** Open the close-out modal: fill in the holding and default the close price to its current price */
function openCloseHolding(h) {
  closeTarget.value = h
  closeFormPrice.value = Number(h.cur_price) || 0
  closePreviewValid.value = false
  showClose.value = true
}

/** 提交清仓：调用后端记录盈亏，成功后从列表移除该持仓 */
/** Submit the close-out: call the backend to record P&L, then remove the holding on success */
async function confirmCloseHolding() {
  const t = closeTarget.value
  const price = Number(closeFormPrice.value)
  if (!t || price <= 0) { alert('请输入有效的清仓价'); return }
  try {
    const res = await api.closeHolding(t.code, price)
    if (res && res.status === 'ok') {
      holdings.value = holdings.value.filter(x => x.code !== t.code)
      const amt = res.profit_amount || 0
      const pct = res.profit_pct || 0
      alert(`已清仓 ${t.code} ${t.name}：清仓价 ¥${price.toFixed(2)}，盈亏 ${amt >= 0 ? '+' : ''}¥${amt.toFixed(2)}（${pct >= 0 ? '+' : ''}${pct.toFixed(2)}%）`)
    }
    showClose.value = false
  } catch (e) { alert('清仓失败: ' + (e.message || '')) }
}

/** 加减仓后预览信息：加仓显示新总数，减仓显示剩余数（超卖标记为 false） */
/** Preview for add/trim: total after add, remaining after trim (over-sell is marked false) */
const lotPreviewQty = computed(() => {
  const cur = Number(lotTarget.value?.quantity) || 0
  const add = Number(lotFormQty.value) || 0
  if (lotDir.value === 'add') return add > 0 ? cur + add : 0
  return add > 0 ? cur - add : 0
})
/** 减仓数量是否超过当前持仓（超过则禁止提交） */
/** Whether the trim quantity exceeds the current holding (disables submit) */
const lotOverSell = computed(() => {
  if (lotDir.value !== 'sell') return false
  const cur = Number(lotTarget.value?.quantity) || 0
  const sell = Number(lotFormQty.value) || 0
  return sell > cur
})
/** 提交条件缺失时禁用（无价格/数量，或减仓超卖） */
/** Disable submit when inputs are missing (no price/quantity, or over-selling) */
const fareCalcDisabled = computed(() => {
  const pr = Number(lotFormPrice.value) || 0
  const qt = Number(lotFormQty.value) || 0
  if (lotDir.value === 'add') return pr <= 0 || qt <= 0
  return pr <= 0 || qt <= 0 || lotOverSell.value
})
/** 减仓后的加权平均成本 = (旧数×旧成本 - 减仓数×成本) / 剩余数 */
/** Weighted average cost after trim = (oldQty*oldCost - soldQty*cost) / remainingQty */
const lotPreviewCost = computed(() => {
  const cur = Number(lotTarget.value?.quantity) || 0
  const curCost = Number(lotTarget.value?.cost_price) || 0
  const add = Number(lotFormQty.value) || 0
  const addPrice = Number(lotFormPrice.value) || 0
  if (lotDir.value === 'add') {
    const total = cur + add
    if (total <= 0) return 0
    return (cur * curCost + add * addPrice) / total
  }
  const remain = cur - add
  if (remain <= 0) return 0
  // 减仓不改变剩余持仓的平均成本（卖出部分按成本价记账，不摊低成本）
  // Trimming does not change the average cost of remaining shares (sold shares are booked at cost, no cost averaging down)
  return curCost
})
const formTp = ref(8)            // 默认止盈 +8%
// default take-profit +8%
const formSl = ref(5)            // 默认止损 -5%
// default stop-loss -5%
const editingBalance = ref(false)  // 是否正在编辑可用资金（显示输入框）
// whether the cash edit input is active
const balanceInputVal = ref(0)     // 可用资金编辑输入框的值
// value of the cash edit input
const balanceInput = ref(null)     // 可用资金编辑输入框的 DOM 引用（用于自动聚焦）
// DOM ref of the cash edit input (for auto-focus)

let timer = null   // 30 秒轮询定时器句柄
// 30s polling timer handle

/** 进入编辑可用资金模式 */
/** Enter available-cash editing mode */
function editBalanceStart() {
  balanceInputVal.value = availableBalance.value
  editingBalance.value = true
  nextTick(() => balanceInput.value?.focus())
}
/** 保存可用资金编辑结果 */
/** Save the available-cash edit */
function editBalanceSave() {
  // 写入资金并同步保存到后端
  // Update the cash and persist to the backend
  availableBalance.value = balanceInputVal.value
  editingBalance.value = false
  saveHoldings()
}
/** 取消可用资金编辑 */
/** Cancel the available-cash edit */
function editBalanceCancel() {
  editingBalance.value = false
}

/** 根据涨跌/盈亏/信号等返回行 CSS 类名 */
/** Return the row CSS class based on change/P&L/signal etc. */
function rowClass(h) {
  const chg = h.change_pct || 0
  const pnl = h.pnl_pct || 0
  // 依次判定：信号 / 大涨 / 触达止损 / 异动，返回对应高亮类
  // Check in order: signal / big gain / stop-loss hit / notable move, then return the matching highlight class
  if (h.signal_active) return 'table-row signal'
  if (chg >= 5 || pnl >= 8) return 'table-row strong'
  if (curReachedStop(h)) return 'table-row danger'
  if (chg >= 3 || pnl >= 5 || chg <= -3 || pnl <= -5) return 'table-row watch'
  return 'table-row'
}
/** 判断是否已触达止盈或止损线 */
/** Whether the price has reached the take-profit or stop-loss line */
function curReachedStop(h) {
  if (!h.cur_price || !h.stop_loss) return false
  // 现价跌破止损或涨破止盈即视为触达
  // Reached when the price drops below stop-loss or rises above take-profit
  return h.cur_price <= h.stop_loss || h.cur_price >= h.take_profit
}

/** 从 API 加载持仓和可用资金 */
/** Load holdings and available cash from the API */
async function load() {
  try {
    // 先拉取会话状态，再加载持仓与资金
    // Fetch the session status first, then load holdings and cash
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
/** Persist holdings and available cash */
async function saveHoldings() {
  try {
    // 全量同步时不携带 lots（批次明细以后端为准，避免整表覆盖误清掉加仓明细）
    // Full sync drops `lots` (lot details live on the backend; avoids wiping add-lot history on full overwrite)
    const list = holdings.value.map(({ lots, ...rest }) => rest)
    await api.updateHoldings({ holdings: list, available_balance: availableBalance.value })
  } catch (_) {}
}

/** 输入代码时自动查询股票名称和现价 */
/** Auto-look-up the stock name and current price while typing the code */
async function onCodeInput() {
  const code = formCode.value.trim()
  if (code.length < 5) { lookupName.value = ''; return }
  try {
    // 按代码查询股票名称与现价
    // Look up the stock name and current price by code
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
/** Submit a new or edited holding */
async function confirmAdd() {
  const code = formCode.value.trim()
  if (!code || !formCost.value || !formQty.value) { alert('请填写完整信息'); return }
  // 组装持仓对象，默认止盈 +8% / 止损 -5%
  // Assemble the holding object with default take-profit +8% / stop-loss -5%
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
    // Edit mode: update in place
    holdings.value[editingIdx.value] = { ...holdings.value[editingIdx.value], quantity: formQty.value, cost_price: formCost.value,
      take_profit_pct: formTp.value, stop_loss_pct: formSl.value }
  } else {
    // 新增模式：追加到列表
    // Add mode: append to the list
    holdings.value.push(item)
  }
  // 保存后关闭弹窗并复位表单
  // Save, close the modal, and reset the form
  await saveHoldings()
  showAdd.value = false
  editingIdx.value = -1
  resetForm()
}

/** 打开编辑弹窗，回填数据 */
/** Open the edit modal, pre-filling the form */
function editHolding(h) {
  // 定位索引并回填表单字段
  // Locate the index and backfill every form field
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

/** 打开加减仓弹窗：记录目标、重置方向为加仓，成交价默认填现价并异步刷新实时价格 */
/** Open the add/trim modal: set the target, default direction to add, pre-fill the live price and refresh it async */
function openAddLot(h) {
  lotTarget.value = h
  lotDir.value = 'add'
  lotFormPrice.value = Number(h.cur_price) || 0
  lotFormQty.value = 0
  lotCurrentPrice.value = Number(h.cur_price) || 0
  showLot.value = true
  refreshLotPrice(h.code)
}

/** 异步刷新加减仓弹窗的现价：查询成功则同时回填成交价，避免展示的成本价/过期价误导 */
/** Asynchronously refresh the add/trim modal price; on success also backfills the fill price so stale/cost prices do not mislead */
async function refreshLotPrice(code) {
  if (!code) return
  try {
    const data = await api.fetchStockLookup(code)
    if (data && data.price > 0) {
      lotCurrentPrice.value = data.price
      lotFormPrice.value = data.price
    }
  } catch (_) {}
}

/** 提交加仓/减仓：按当前方向调用增量买入或 FIFO 减仓接口，用返回的持仓原地替换本地行 */
/** Submit add/trim: call the incremental-buy or FIFO-sell endpoint per direction, replacing the local row with the returned holding */
async function confirmLot() {
  const t = lotTarget.value
  const price = Number(lotFormPrice.value)
  const qty = Number(lotFormQty.value)
  if (!t || price <= 0 || qty <= 0) { alert('请填写成交价与成交数量'); return }
  try {
    if (lotDir.value === 'sell') {
      const res = await api.sellHoldingLot(t.code, price, qty)
      if (res && res.closed) {
        holdings.value = holdings.value.filter(x => x.code !== t.code)
        alert(`已全部减仓 ${t.code} ${t.name}`)
      } else if (res && res.holding) {
        upsertHolding(res.holding)
      }
    } else {
      const res = await api.addHoldingLot(t.code, price, qty)
      if (res && res.holding) upsertHolding(res.holding)
    }
    showLot.value = false
  } catch (e) { alert((lotDir.value === 'sell' ? '减仓失败: ' : '加仓失败: ') + (e.message || '')) }
}

/** 打开改成本弹窗：回填当前成本 */
/** Open the set-cost modal: backfill the current cost */
function openSetCost(h) {
  costTarget.value = h
  costFormPrice.value = Number(h.cost_price) || 0
  showCost.value = true
}

/** 提交改成本：调用后端成本更新接口，原地替换本地行 */
/** Submit the cost change: call the backend update endpoint and replace the local row */
async function confirmSetCost() {
  const t = costTarget.value
  const price = Number(costFormPrice.value)
  if (!t || price <= 0) { alert('请输入有效的成本价'); return }
  try {
    const res = await api.setHoldingCost(t.code, price)
    if (res && res.holding) upsertHolding(res.holding)
    showCost.value = false
  } catch (e) { alert('更新成本失败: ' + (e.message || '')) }
}

/** 打开加仓批次明细弹窗 */
/** Open the lot-detail modal */
function showLotsFor(h) {
  lotsTarget.value = h
  showLots.value = true
}

/** 以后端返回的持仓更新本地列表（按代码定位，无则追加） */
/** Upsert a holding into the local list by code (append if absent) */
function upsertHolding(h) {
  const idx = holdings.value.findIndex(x => x.code === h.code)
  if (idx >= 0) holdings.value[idx] = h
  else holdings.value.push(h)
}

/** 重置新增表单 */
/** Reset the add form */
function resetForm() {
  formCode.value = ''
  formCost.value = 0
  formQty.value = 0
  lookupName.value = ''
  lookupPrice.value = 0
}

/** 关闭新增/编辑弹窗（取消或点遮罩），复位编辑态避免残留 */
/** Close the add/edit modal (cancel or overlay click), resetting the edit state to avoid leftovers */
function closeAdd() {
  showAdd.value = false
  editingIdx.value = -1
}

/** 打开空白的新增持仓弹窗：重置全部表单（不留上次输入草稿） */
/** Open a blank add-holding modal: reset all fields (no draft from the last input) */
function openAddNew() {
  editingIdx.value = -1
  resetForm()
  formTp.value = 8
  formSl.value = 5
  showAdd.value = true
}

// 先读本地缓存秒开，再拉取最新数据，并启动 30s 轮询
// Read the cache for an instant open, fetch fresh data, then start 30s polling
onMounted(() => { loadCache(); load(); timer = setInterval(load, 30000) })
// 卸载时清理定时器
// Clear the timer on unmount
onUnmounted(() => { if (timer) clearInterval(timer) })

/** 展开/收起某持仓的 分时区 */
/** Toggle a holding's K-line area */
function toggleKline(code) {
  const next = new Set(klineOpen.value)
  if (next.has(code)) next.delete(code)
  else next.add(code)
  klineOpen.value = next
}

/** 移动端：点击行打开操作菜单 */
/** Mobile: tap a row to open the action sheet */
function onRowTap(h) {
  if (window.innerWidth > 768) return
  sheetHolding.value = h
}
/** 移动端：操作菜单 - 展开/收起分时 */
function sheetKline() {
  if (!sheetHolding.value) return
  toggleKline(sheetHolding.value.code)
  sheetHolding.value = null
}
/** 移动端：操作菜单 - 加减仓 */
function sheetLot() {
  if (!sheetHolding.value) return
  const h = sheetHolding.value
  sheetHolding.value = null
  openAddLot(h)
}
/** 移动端：操作菜单 - 改成本 */
function sheetCost() {
  if (!sheetHolding.value) return
  const h = sheetHolding.value
  sheetHolding.value = null
  openSetCost(h)
}
/** 移动端：操作菜单 - 加仓明细 */
function sheetLots() {
  if (!sheetHolding.value) return
  const h = sheetHolding.value
  sheetHolding.value = null
  showLotsFor(h)
}
/** 移动端：操作菜单 - 编辑持仓 */
function sheetEdit() {
  if (!sheetHolding.value) return
  const h = sheetHolding.value
  sheetHolding.value = null
  editHolding(h)
}
/** 移动端：操作菜单 - 清仓 */
function sheetClose() {
  if (!sheetHolding.value) return
  const h = sheetHolding.value
  sheetHolding.value = null
  openCloseHolding(h)
}
</script>

<style scoped>
.positions-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.header-right { display: flex; align-items: center; gap: 12px; }
.total-pnl { font-size: 16px; font-weight: 700; white-space: nowrap; margin-right: 16px; }
.total-pnl.up { color: #e74c3c; }
.total-pnl.down { color: #27ae60; }
.btn-reset { font-size: 14px; margin-left: 8px; padding: 2px 8px; border: 1px solid #555; border-radius: 4px; background: none; color: #aaa; cursor: pointer; }
.btn-reset:hover { background: #333; }
.balance { font-size: 14px; color: #4caf50; font-weight: 600; cursor: pointer; }
.balance-editing input { width: 150px; padding: 6px 10px; border-radius: 6px; border: 1px solid #4caf50; background: #0f0f23; color: #4caf50; font-size: 14px; font-weight: 600; text-align: right; outline: none; }
.btn-add {
  padding: 8px 16px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 14px; cursor: pointer;
}
.positions-table { background: #1a1a2e; border-radius: 8px; overflow-x: auto; font-size: 14px; white-space: nowrap; }
.table-header, .table-row {
  display: flex; align-items: center; padding: 10px 16px; gap: 0;
  min-width: 1120px;
}
.table-header { background: #2a2a3e; color: #888; font-weight: 600; }
.table-row { border-bottom: none; }
.pos-row-group { border-bottom: 1px solid #2a2a3e; min-width: 1120px; }
.pos-row-group:last-child { border-bottom: none; }
.table-row.signal { background: rgba(79,195,247,0.08); }
.table-row.danger { background: rgba(250,173,20,0.15); }
.table-row.watch { background: rgba(250,173,20,0.08); }
.table-row.strong { background: rgba(255,77,79,0.10); }

.col-kline { flex: 0 0 60px; text-align: center; }
.btn-kline {
  background: transparent; border: 1px solid #3a3a55; color: #7ab8ff;
  border-radius: 4px; cursor: pointer; font-size: 14px; padding: 2px 8px;
}
.btn-kline:hover { border-color: #4fc3f7; color: #4fc3f7; }
.pos-kline-row { padding: 8px 16px 12px; background: #16162a; }

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
.col-actions { display: flex; gap: 4px; flex: 0 0 240px; justify-content: center; }
.btn-edit, .btn-sell, .btn-lot, .btn-cost {
  padding: 4px 8px; border-radius: 4px; font-size: 14px; cursor: pointer; white-space: nowrap;
}
.btn-edit { border: 1px solid #4fc3f7; background: transparent; color: #4fc3f7; }
.btn-edit:hover { background: rgba(79,195,247,0.1); }
.btn-lot { border: 1px solid #7c4dff; background: transparent; color: #b388ff; }
.btn-lot:hover { background: rgba(124,77,255,0.12); }
.btn-cost { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-cost:hover { background: rgba(250,173,20,0.1); }
.btn-sell { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-sell:hover { background: rgba(250,173,20,0.1); }
.empty { text-align: center; padding: 60px; color: #555; font-size: 14px; }
.hint { color: #444; font-size: 14px; margin-top: 8px; }

/* modal */
.modal-overlay {
  position: fixed; top: 0; left: 0; width: 100%; height: 100%;
  background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal {
  background: #1a1a2e; border-radius: 10px; padding: 24px; width: 360px;
}
.modal-title { font-size: 16px; font-weight: 600; color: #e0e0e0; margin-bottom: 16px; }
.modal-title .lot-dir { display: inline-flex; gap: 4px; margin-left: 12px; vertical-align: middle; }
.lot-dir .dir-btn {
  padding: 3px 12px; border-radius: 4px; font-size: 14px; cursor: pointer;
  border: 1px solid #333; background: transparent; color: #888;
}
.lot-dir .dir-btn.active-add { border-color: #FF4D4F; background: rgba(255,77,79,0.15); color: #FF4D4F; }
.lot-dir .dir-btn.active-sell { border-color: #4caf50; background: rgba(76,175,80,0.15); color: #4caf50; }
.over-sell { color: #FF4D4F; font-weight: 700; }
.btn-confirm-sell { background: #4caf50; }
.btn-confirm-sell:disabled { opacity: 0.5; cursor: not-allowed; }
.form-row { margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.form-row label { width: 56px; color: #888; font-size: 14px; flex-shrink: 0; }
.form-row input {
  flex: 1; padding: 8px 12px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none;
}
.form-row input:focus { border-color: #FF4D4F; }
.lookup-result { font-size: 14px; color: #4caf50; white-space: nowrap; }
.static-val { color: #e0e0e0; font-size: 14px; white-space: nowrap; }
.preview { margin: 4px 0 8px 64px; font-size: 14px; color: #b388ff; }
.pnl-up { color: #FF4D4F; font-weight: 700; }
.pnl-down { color: #4caf50; font-weight: 700; }
.modal.wide { width: 480px; }
.lots-table { margin-bottom: 12px; font-size: 14px; }
.lots-header, .lots-row {
  display: flex; align-items: center; padding: 6px 8px; gap: 8px;
}
.lots-header { color: #888; font-weight: 600; border-bottom: 1px solid #2a2a3e; }
.lots-header span, .lots-row span { flex: 1; text-align: left; }
.lots-row { border-bottom: 1px solid #1a1a26; color: #ccc; }
.lots-row:last-child { border-bottom: none; }
.lots-footer { padding: 8px; color: #b388ff; font-weight: 600; text-align: right; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.btn-cancel {
  padding: 8px 20px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #888; font-size: 14px; cursor: pointer;
}
.btn-confirm {
  padding: 8px 20px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 14px; cursor: pointer;
}
.legend {
  margin-top: 12px; padding: 6px 12px; font-size: 14px; color: #666;
  background: #1a1a2e; border-radius: 6px; display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
}
.lg-sep { color: #333; }
.lg-item { color: #666; }
.lg-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 3px; vertical-align: middle; }
.lg-dot.up { background: #FF4D4F; }
.lg-dot.warn { background: #FAAD14; }

/* ====== Mobile: horizontal scroll + larger fonts ====== */
@media (max-width: 768px) {
  .positions-table { overflow-x: auto; white-space: nowrap; -webkit-overflow-scrolling: touch; }
  .table-header, .table-row { min-width: 1200px; font-size: 14px; padding: 10px 14px; }
  .table-header { display: flex; }
  .pos-row-group { min-width: 0; }
  .page-header { flex-direction: column; align-items: stretch; gap: 8px; }
  .header-right { flex-wrap: wrap; gap: 8px; }
  .total-pnl { font-size: 15px; margin-right: 0; }
  .pos-kline-row { padding: 6px; }
  .modal { width: 92%; max-width: 360px; padding: 18px; }
  .modal.wide { width: 92%; }
  .form-row { flex-wrap: wrap; }
  .form-row label { width: 64px; }
  .preview { margin-left: 0; }
  .table-row { cursor: pointer; }
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
