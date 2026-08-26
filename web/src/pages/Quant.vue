<template>
  <div class="quant-page">
    <div class="page-title">📈 量化交易</div>
    <div class="page-sub">实盘链路参数、仓位纪律与战法白名单（保存后约 5 秒热加载生效）</div>

    <!-- ── 实盘互通健康条：首尔→广州探测 + 广州回报新鲜度（10s 轮询）── -->
    <div class="card link-card">
      <template v-if="state && state.enabled">
        <span class="dot" :class="state.last_probe_ok ? 'ok' : 'bad'">●</span>
        <span>首尔 ↔ 广州</span>
        <span class="mono">{{ state.gateway_url }}</span>
        <span v-if="state.last_latency_ms > 0" class="pill">{{ state.last_latency_ms }}ms</span>
        <span :class="['pill', state.tripped ? 'warn' : 'good']">
          {{ state.tripped ? '熔断:' + (state.trip_reason || '未知') : '正常' }}
        </span>
        <span>{{ state.mode === 'auto' ? '自动' : '手动' }}</span>
        <span v-if="fmtAgo(state.last_report_at)">回报{{ fmtAgo(state.last_report_at) }}</span>
      </template>
      <template v-else>
        <span class="dot bad">●</span><span>实盘链路未启用</span>
        <span class="hint">打开下方「总开关」并配置网关地址后开始互通</span>
      </template>
    </div>

    <!-- ── 总开关与执行方式 ── -->
    <div class="card">
      <div class="card-header">总开关与执行方式</div>
      <div class="form-row switch-row">
        <label class="lbl">实盘总开关</label>
        <label class="switch">
          <input type="checkbox" v-model="form.enabled" @change="saveSwitches" />
          <span class="slider"></span>
        </label>
        <span class="hint">关闭后引擎不再向网关传递任何信号/建议（纸面盘不受影响）</span>
      </div>
      <div class="form-row">
        <label class="lbl">执行模式</label>
        <button :class="['seg', form.mode === 'manual' ? 'on' : '']" @click="form.mode = 'manual'">手动确认</button>
        <button :class="['seg', form.mode === 'auto' ? 'on' : '']" @click="form.mode = 'auto'">全自动</button>
        <span class="hint">手动=每单前端确认；自动=信号直接下单</span>
      </div>
      <div class="form-row">
        <label class="lbl">委托价格</label>
        <button :class="['seg', form.price_type === 'market' ? 'on' : '']" @click="form.price_type = 'market'">对手价</button>
        <button :class="['seg', form.price_type === 'limit' ? 'on' : '']" @click="form.price_type = 'limit'">限价</button>
      </div>
      <div class="form-row switch-row">
        <label class="lbl">自动卖出</label>
        <label class="switch">
          <input type="checkbox" v-model="form.auto_sell" />
          <span class="slider"></span>
        </label>
        <span class="hint">自动模式下止损/清仓级建议自动全仓卖出；止盈/减仓保持提醒</span>
      </div>
      <div class="form-row">
        <label class="lbl">心跳超时(秒)</label>
        <input class="inp short" type="number" v-model.number="form.miss_heartbeat_sec" min="30" max="3600" />
        <span class="hint">连续失联超过该值触发熔断暂停下单（30-3600）</span>
      </div>
      <div class="form-row">
        <label class="lbl">网关地址</label>
        <input class="inp wide mono" v-model.trim="form.gateway_url" placeholder="http://81.71.69.17:8789" />
      </div>
      <div class="form-row">
        <label class="lbl">鉴权Token</label>
        <input class="inp wide mono" type="password" v-model.trim="tokenInput" :placeholder="form.token_masked || '未设置'" />
        <span class="hint">显示为脱敏形态；留空表示保持原值不变</span>
      </div>
      <div class="save-bar">
        <button class="btn-save" @click="saveExec" :disabled="saving">{{ saving ? '保存中…' : '保存执行参数' }}</button>
      </div>
    </div>

    <!-- ── 仓位纪律 ── -->
    <div class="card">
      <div class="card-header">仓位纪律</div>
      <div class="form-grid">
        <div class="field"><label>最大持仓数</label><input class="inp" type="number" v-model.number="form.max_positions" min="1" max="50" /><span class="hint">1-50，双端校验</span></div>
        <div class="field"><label>单票金额(元)</label><input class="inp" type="number" v-model.number="form.fixed_amount" min="0" step="500" /><span class="hint">每次买入投入金额</span></div>
        <div class="field"><label>初始资金(元)</label><input class="inp" type="number" v-model.number="form.initial_capital" min="0" step="10000" /><span class="hint">用于仓位约束预检</span></div>
        <div class="field"><label>单日买入笔数上限</label><input class="inp" type="number" v-model.number="form.daily_max_buys" min="0" /><span class="hint">0=不设限，防信号风暴</span></div>
        <div class="field"><label>单日买入预算(元)</label><input class="inp" type="number" v-model.number="form.daily_budget_amount" min="0" step="10000" /><span class="hint">0=不设限，超出拒绝新买入</span></div>
      </div>
      <div class="save-bar">
        <button class="btn-save" @click="saveCaps" :disabled="saving">{{ saving ? '保存中…' : '保存仓位纪律' }}</button>
      </div>
    </div>

    <!-- ── 战法白名单 ── -->
    <div class="card">
      <div class="card-header">战法开关
        <span class="card-sub">关闭的战法信号不会进入实盘链路（模拟盘不受影响）；全部开启 = 不设白名单</span>
      </div>
      <div v-for="s in strategyRows" :key="s.value" class="strategy-row">
        <div>
          <div class="st-name">{{ s.label }}</div>
          <div class="st-code">{{ s.value }}</div>
        </div>
        <div class="st-amount">
          <input class="inp short" type="number" min="0" step="500" v-model="amountsInput[s.value]" placeholder="全局" />
          <span class="hint">元/次</span>
        </div>
        <label class="switch">
          <input type="checkbox" v-model="strategyOn[s.value]" @change="markStrategyDirty" />
          <span class="slider"></span>
        </label>
      </div>
      <div class="save-bar">
        <span class="hint">{{ strategyHint }} · 仓位留空/0 = 使用全局单票金额</span>
        <button class="btn-save" :disabled="!strategyDirty || saving" @click="saveStrategies">
          {{ saving ? '保存中…' : (strategyDirty ? '保存战法开关 *' : '已同步') }}
        </button>
      </div>
    </div>

    <!-- ── 交易流水与整体盈亏 ── -->
    <div class="card">
      <div class="card-header">交易流水与整体盈亏
        <span class="card-sub">已实现=加权成本重放；浮动=市值-成本×数量；30s 刷新</span>
      </div>
      <template v-if="trades">
        <div class="chips">
          <div class="chip" :class="(trades.summary.total_pnl || 0) >= 0 ? 'up' : 'down'">
            <div class="chip-num">{{ fmtMoney(trades.summary.total_pnl) }}</div>
            <div class="chip-lbl">总盈亏</div>
          </div>
          <div class="chip">
            <div class="chip-num" :class="(trades.summary.realized_pnl || 0) >= 0 ? 'up' : 'down'">{{ fmtMoney(trades.summary.realized_pnl) }}</div>
            <div class="chip-lbl">已实现</div>
          </div>
          <div class="chip">
            <div class="chip-num" :class="(trades.summary.unrealized_pnl || 0) >= 0 ? 'up' : 'down'">{{ fmtMoney(trades.summary.unrealized_pnl) }}</div>
            <div class="chip-lbl">浮动盈亏</div>
          </div>
          <div class="chip"><div class="chip-num">{{ trades.summary.trade_count }}</div><div class="chip-lbl">成交笔数</div></div>
          <div class="chip"><div class="chip-num">{{ trades.summary.wins }}胜 / {{ trades.summary.losses }}负</div><div class="chip-lbl">卖出胜负</div></div>
        </div>

        <table v-if="(trades.by_strategy || []).length" class="tbl">
          <thead><tr><th>战法</th><th>买入额</th><th>卖出额</th><th>已实现盈亏</th><th>笔数</th></tr></thead>
          <tbody>
            <tr v-for="st in trades.by_strategy" :key="st.strategy">
              <td>{{ st.strategy }}</td><td>{{ st.buys }}</td><td>{{ st.sells }}</td>
              <td :class="st.realized_pnl >= 0 ? 'up' : 'down'">{{ fmtMoney(st.realized_pnl) }}</td>
              <td>{{ st.trade_count }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="hint empty-tip">暂无成交——实盘成交后此处出现按战法归因的盈亏统计（飞轮回流数据源）</p>

        <table v-if="(trades.fills || []).length" class="tbl fills-tbl">
          <thead><tr><th>时间</th><th>代码</th><th>方向</th><th>价格</th><th>数量</th><th>金额</th><th>战法</th></tr></thead>
          <tbody>
            <tr v-for="(f, i) in trades.fills" :key="f.order_id + f.traded_at + i">
              <td class="mono">{{ (f.traded_at || '').replace('T', ' ').slice(5, 19) }}</td>
              <td class="mono">{{ f.code }}</td>
              <td :class="f.side === '买入' ? 'up' : 'down'">{{ f.side }}</td>
              <td>{{ f.price }}</td><td>{{ f.qty }}</td><td>{{ f.amount }}</td>
              <td class="mono dim">{{ f.strategy }}</td>
            </tr>
          </tbody>
        </table>
      </template>
      <p v-else class="hint">加载交易流水…</p>
    </div>
  </div>
</template>

<script setup>
// Quant.vue — 量化交易页：实盘链路状态展示 + 总开关/执行方式 + 仓位纪律 + 战法白名单。
// 数据面：GET /api/qmt/state（互通快照）、GET/POST /api/config/qmt（账号级配置，局部合并保存，
// token 脱敏回显——提交空串或哨兵即保持原值）。后端校验枚举/范围，5s 热加载生效。
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import * as api from '../api/index.js'

const KNOWN_STRATEGIES = [
  { value: 'dragon', label: '龙头战法 Dragon' },
  { value: 'double_bump', label: '双响炮 DoubleBump' },
  { value: 'n_shape', label: 'N形超短 NShape' },
  { value: 'dragon_return', label: '龙回头(中线) DragonReturn' },
]

const state = ref(null)                       // /api/qmt/state 互通快照
const form = reactive({                       // 表单（GET 回填）
  enabled: false, mode: 'manual', price_type: 'market', auto_sell: false,
  gateway_url: '', token_masked: '',
  fixed_amount: 10000, max_positions: 10, initial_capital: 100000,
  daily_max_buys: 20, daily_budget_amount: 100000, miss_heartbeat_sec: 120,
})
const tokenInput = ref('')                    // token 输入框（留空=不改）
const knownStrategies = ref([...KNOWN_STRATEGIES.map(s => s.value)])
const strategyOn = reactive({})               // 战法勾选态
const strategyDirty = ref(false)              // 白名单有未保存改动
const saving = ref(false)
const savedFlash = ref('')
const trades = ref(null)                      // /api/qmt/trades 流水+盈亏（30s 轮询）
const amountsInput = reactive({})             // 每战法仓位大小输入（空串/0=使用全局 fixed_amount）
let stateTimer = null
let tradesTimer = null

const strategyRows = computed(() =>
  KNOWN_STRATEGIES.filter(s => knownStrategies.value.includes(s.value)))

// 白名单语义：数组为空 = 全部允许。全开时提交 []；部分开启时提交勾选集合。
/** 白名单勾选语义：数组为空=全部允许；部分开启=白名单精确集合 */
const allStrategyOn = computed(() => strategyRows.value.every(s => strategyOn[s.value]))
const strategyHint = computed(() => {
  const onCount = strategyRows.value.filter(s => strategyOn[s.value]).length
  if (allStrategyOn.value) return '当前：全部允许'
  return `当前：${onCount}/${strategyRows.value.length} 允许进入实盘`
})

/** GET 配置回填表单与战法勾选态 */
async function loadConfig() {
  const c = await api.fetchQMTConfig()
  Object.assign(form, {
    enabled: !!c.enabled, mode: c.mode || 'manual', price_type: c.price_type || 'market',
    auto_sell: !!c.auto_sell, gateway_url: c.gateway_url || '', token_masked: c.token_masked || '',
    fixed_amount: c.fixed_amount ?? 10000, max_positions: c.max_positions ?? 10,
    initial_capital: c.initial_capital ?? 100000,
    daily_max_buys: c.daily_max_buys ?? 20, daily_budget_amount: c.daily_budget_amount ?? 100000,
    miss_heartbeat_sec: c.miss_heartbeat_sec ?? 120,
  })
  if (Array.isArray(c.known_strategies) && c.known_strategies.length) knownStrategies.value = c.known_strategies
  // 勾选态推导：白名单为空=全开；否则按包含关系
  const wl = Array.isArray(c.strategies) ? c.strategies : []
  for (const v of knownStrategies.value) strategyOn[v] = wl.length === 0 || wl.includes(v)
  // 每战法仓位大小回填（未配置显示空串=全局）
  const sa = c.strategy_amounts || {}
  for (const v of knownStrategies.value) amountsInput[v] = sa[v] ?? ''
  strategyDirty.value = false
}

/** 拉取交易流水与盈亏 */
async function loadTrades() {
  try { trades.value = await api.fetchQMTTrades() } catch (e) { /* 忽略，保留上次数据 */ }
}

/** 盈亏金额格式化：带符号两位小数 */
function fmtMoney(v) {
  const n = Number(v) || 0
  return (n > 0 ? '+' : '') + n.toFixed(2)
}

/** 拉取互通健康快照（10s 轮询） */
async function loadState() {
  try { state.value = await api.fetchQMTState() } catch (e) { /* 忽略 */ }
}

function markStrategyDirty() { strategyDirty.value = true }

/** 通用保存：patch 提交 → 成功后重拉配置回填（含脱敏 token 刷新） */
async function patch(fields, okTip) {
  saving.value = true
  try {
    await api.updateQMTConfig(fields)
    savedFlash.value = okTip || '已保存'
    setTimeout(() => { savedFlash.value = '' }, 2000)
    await loadConfig()
  } catch (e) {
    alert('保存失败：' + (e && e.message ? e.message : e))
  } finally {
    saving.value = false
  }
}

// 总开关切换即时生效：开启需二次确认（高危操作面）
/** 总开关保存：开启实盘链路属高危操作，需二次确认；关闭立即停传信号（纸面盘不受影响） */
async function saveSwitches() {
  if (form.enabled && !window.confirm('确认启用实盘链路？启用后将按下方参数向广州网关传递真实交易指令。')) {
    form.enabled = false
    return
  }
  await patch({ enabled: form.enabled }, form.enabled ? '实盘链路已启用' : '实盘链路已停用')
}

/** 执行参数保存：mode/price_type/auto_sell/心跳/网关地址/token（留空=保持原值）；切全自动需确认 */
async function saveExec() {
  if (form.mode === 'auto' &&
      !window.confirm('确认切换为「全自动」？信号将不经人工确认直接下单（受熔断/纪律约束）。')) {
    form.mode = 'manual'
    return
  }
  const fields = {
    mode: form.mode, price_type: form.price_type, auto_sell: form.auto_sell,
    gateway_url: form.gateway_url, miss_heartbeat_sec: form.miss_heartbeat_sec,
  }
  if (tokenInput.value) fields.token = tokenInput.value   // 留空=保持原值
  await patch(fields, '执行参数已保存')
}

/** 仓位纪律保存：最大持仓/单票金额/初始资金/单日笔数与预算（后端做范围校验） */
async function saveCaps() {
  await patch({
    max_positions: form.max_positions, fixed_amount: form.fixed_amount,
    initial_capital: form.initial_capital, daily_max_buys: form.daily_max_buys,
    daily_budget_amount: form.daily_budget_amount,
  }, '仓位纪律已保存')
}

/** 战法开关+每战法仓位大小保存：全部勾选→提交空白名单（=全部允许）；仓位仅提交 >0 覆盖项 */
function saveStrategies() {
  const values = strategyRows.value.filter(s => strategyOn[s.value]).map(s => s.value)
  // 每战法仓位大小：仅提交 >0 的覆盖项；全部勾选 → strategies 提交空数组（= 不设白名单）
  const amounts = {}
  for (const v of knownStrategies.value) {
    const n = parseFloat(amountsInput[v])
    if (!Number.isNaN(n) && n > 0) amounts[v] = n
  }
  // 失败时 loadConfig 会重置勾选态
  return patch(
    { strategies: allStrategyOn.value ? [] : values, strategy_amounts: amounts },
    '战法开关与仓位已保存',
  )
}

onMounted(async () => {
  loadState()
  stateTimer = setInterval(loadState, 10000)
  loadTrades()
  tradesTimer = setInterval(loadTrades, 30000)
  try { await loadConfig() } catch (e) { alert('加载实盘配置失败：' + (e && e.message ? e.message : e)) }
})
onUnmounted(() => {
  if (stateTimer) clearInterval(stateTimer)
  if (tradesTimer) clearInterval(tradesTimer)
})
</script>

<style scoped>
.quant-page { max-width: 900px; }
.page-title { font-size: 20px; font-weight: 700; margin-bottom: 4px; }
.page-sub { font-size: 12px; color: #888; margin-bottom: 14px; }

.link-card { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 13px; padding: 12px 14px; margin-bottom: 14px; }
.dot { font-size: 16px; line-height: 1; }
.dot.ok { color: #4caf50; }
.dot.bad { color: #666; }
.mono { font-family: monospace; color: #4fc3f7; }
.pill { font-size: 11px; padding: 2px 8px; border-radius: 10px; background: rgba(255,255,255,0.08); }
.pill.good { color: #4caf50; background: rgba(76,175,80,0.15); }
.pill.warn { color: #FF4D4F; background: rgba(255,77,79,0.15); }

.card { background: #1a1a2e; border-radius: 8px; padding: 14px; margin-bottom: 14px; }
.card-header { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 13px; font-weight: 600; color: #ccc; margin-bottom: 12px; }
.card-sub { font-size: 11px; color: #666; font-weight: 400; }

.form-row { display: flex; align-items: center; gap: 10px; padding: 7px 0; flex-wrap: wrap; }
.lbl { width: 110px; flex-shrink: 0; font-size: 13px; color: #aaa; text-align: right; }
.hint { font-size: 11px; color: #666; }
.inp { background: #12121e; border: 1px solid #2a2a3e; border-radius: 6px; color: #e0e0e0; padding: 6px 9px; font-size: 13px; }
.inp:focus { outline: none; border-color: #b388ff; }
.inp.short { width: 90px; }
.inp.wide { flex: 1; min-width: 240px; }
.form-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }
.field { display: flex; flex-direction: column; gap: 5px; }
.field label { font-size: 12px; color: #aaa; }
.field .hint { font-size: 10px; }

.seg { background: transparent; border: 1px solid #2a2a3e; color: #999; border-radius: 6px; padding: 5px 14px; font-size: 12px; cursor: pointer; }
.seg.on { border-color: #b388ff; color: #b388ff; background: rgba(179,136,255,0.1); }

.switch { position: relative; width: 40px; height: 22px; flex-shrink: 0; }
.switch input { opacity: 0; width: 0; height: 0; }
.slider { position: absolute; cursor: pointer; inset: 0; background: #33334a; border-radius: 22px; transition: 0.2s; }
.slider::before { content: ''; position: absolute; height: 16px; width: 16px; left: 3px; top: 3px; background: #888; border-radius: 50%; transition: 0.2s; }
.switch input:checked + .slider { background: rgba(179,136,255,0.35); }
.switch input:checked + .slider::before { transform: translateX(18px); background: #b388ff; }

.strategy-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 9px 4px; border-bottom: 1px solid #22223a; }
.strategy-row:last-of-type { border-bottom: none; }
.st-name { font-size: 13px; color: #ddd; }
.st-code { font-family: monospace; font-size: 11px; color: #555; margin-top: 2px; }
.st-amount { display: flex; align-items: center; gap: 6px; flex: 1; justify-content: flex-end; }

.chips { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 10px; margin-bottom: 12px; }
.chip { background: #12121e; border-radius: 8px; padding: 10px; text-align: center; }
.chip-num { font-size: 18px; font-weight: 700; color: #e0e0e0; font-family: monospace; }
.chip-lbl { font-size: 11px; color: #777; margin-top: 3px; }
.up { color: #FF4D4F; }
.down { color: #4caf50; }

.tbl { width: 100%; border-collapse: collapse; font-size: 12px; margin-top: 8px; }
.tbl th { text-align: left; color: #666; font-weight: 600; padding: 5px 6px; border-bottom: 1px solid #2a2a3e; }
.tbl td { padding: 5px 6px; border-bottom: 1px solid #1a1a26; color: #ccc; }
.tbl tr:last-child td { border-bottom: none; }
.mono { font-family: monospace; }
.dim { color: #666; }
.empty-tip { padding: 8px 2px; }

.save-bar { display: flex; align-items: center; gap: 12px; justify-content: flex-end; margin-top: 10px; }
.btn-save { background: #b388ff; color: #14101e; border: none; border-radius: 6px; padding: 7px 16px; font-size: 13px; font-weight: 600; cursor: pointer; }
.btn-save:disabled { opacity: 0.45; cursor: default; }
.btn-save:not(:disabled):hover { filter: brightness(1.1); }
@media (max-width: 768px) { .lbl { text-align: left; width: 100%; } .form-grid { grid-template-columns: 1fr; } }
</style>
