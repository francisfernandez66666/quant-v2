<!--
  设置页面 Settings.vue
  Settings page Settings.vue
  包含服务器连接、通知、账户信息、LLM 配置、系统信息等设置项
  Contains server connection, notifications, account info, LLM config, system info and other settings
-->
<template>
  <div class="settings-page">
    <h2>设置</h2>

    <!-- 服务器连接设置（Server connection settings）-->
    <div class="setting-card">
      <div class="setting-header">服务器连接</div>
      <div class="setting-row">
        <label>服务器地址</label>
        <input v-model="serverUrl" placeholder="http://localhost:8080" />
      </div>
      <div class="setting-row">
        <label>连接状态</label>
        <span :class="['status', serverOnline ? 'online' : 'offline']">
          {{ serverOnline ? '已连接' : '离线' }}
        </span>
      </div>
      <button class="btn-save" @click="saveServer">保存</button>
    </div>

    <!-- 通知设置（Notification settings）-->
    <div class="setting-card">
      <div class="setting-header">通知设置</div>
      <div class="setting-row">
        <label>浏览器通知</label>
        <button class="btn-test" @click="requestNotify">授权并测试</button>
      </div>
      <div class="setting-row">
        <label>声音提醒</label>
        <button class="btn-test" @click="playTest">测试声音</button>
      </div>
      <div class="setting-row">
        <label>macOS 通知</label>
        <span class="status online">后台自动发送</span>
      </div>
    </div>

    <!-- 账户信息（Account info）-->
    <div class="setting-card">
      <div class="setting-header">账户信息</div>
      <div class="setting-row">
        <label>账号</label>
        <span class="account">{{ account }}</span>
      </div>
      <div class="setting-row">
        <label>令牌</label>
        <span class="status offline">{{ token ? token.slice(0, 20) + '...' : '未登录' }}</span>
      </div>
    </div>

    <!-- LLM 配置（LLM configuration）-->
    <div class="setting-card">
      <div class="setting-header">LLM 配置</div>
      <div class="setting-row">
        <label>API URL</label>
        <input v-model="llmApiUrl" placeholder="https://api.openai.com/v1" />
      </div>
      <div class="setting-row">
        <label>API Key</label>
        <input v-model="llmApiKey" type="password" placeholder="sk-..." />
      </div>
      <div class="setting-row">
        <label>模型</label>
        <input v-model="llmModel" placeholder="gpt-4o-mini" />
      </div>
      <div class="setting-row">
        <label>归因批并发度</label>
        <input v-model.number="llmBatchConcurrency" type="number" min="1" max="16" placeholder="4" />
      </div>
      <div class="setting-row">
        <label>状态</label>
        <span :class="['status', llmConfigured ? 'online' : 'offline']">
          {{ llmConfigured ? '已配置' : '未配置（降级为关键词过滤）' }}
        </span>
      </div>
      <button class="btn-save" @click="saveLLM" :disabled="llmSaving">{{ llmSaving ? '保存中...' : '保存' }}</button>
      <span v-if="llmMsg" :class="['feedback', llmMsgType]">{{ llmMsg }}</span>
    </div>

    <!-- 战法参数配置：按策略分组渲染输入项，值绑定到 strategyCfg[分组key][字段key]（Strategy parameter config: renders inputs by strategy group, bound to strategyCfg[groupKey][fieldKey]）-->
    <div class="setting-card" v-for="group in strategyGroups" :key="group.key">
      <div class="setting-header">{{ group.title }}</div>
      <!-- 分组内各字段输入行：step/type 由字段定义控制（Per-field input rows inside a group; step/type come from the field definition）-->
      <div class="setting-row" v-for="f in group.fields" :key="f.k">
        <label :title="f.hint || ''">{{ f.label }}</label>
        <!-- switch 字段渲染为开关（checkbox），其余为数字输入（switch fields render as a toggle; others as a number input）-->
        <label v-if="f.type === 'switch'" class="switch">
          <input type="checkbox" v-model="strategyCfg[group.key][f.k]" />
          <span class="slider"></span>
        </label>
        <input v-else
               v-model.number="strategyCfg[group.key][f.k]"
               :type="f.type || 'number'"
               :step="f.type === 'number' ? (f.step || 'any') : undefined"
               placeholder="0" />
      </div>
    </div>
    <div class="setting-card">
      <div class="setting-header">战法参数</div>
      <div class="setting-row">
        <label>说明</label>
        <span style="font-size:12px;color:#888">参数保存后重启后端生效；权重请保持各策略合计 ≤ 1</span>
      </div>
      <button class="btn-save" @click="saveStrategy" :disabled="strategySaving">{{ strategySaving ? '保存中...' : '保存战法参数' }}</button>
      <span v-if="strategyMsg" :class="['feedback', strategyMsgType]">{{ strategyMsg }}</span>
    </div>

    <!-- 资讯显示开关（"Show all news" toggle）-->
    <div class="setting-card">
      <div class="setting-header">资讯显示</div>
      <!-- 资讯显示开关：切换时调用 toggleNewsShowAll 持久化到后端（"Show all news" toggle: persisted to the backend via toggleNewsShowAll on change）-->
      <div class="setting-row">
        <label title="开启后弱档/中性资讯（|score|<0.25）也出现在资讯列表；关闭则仅显示有价值的强事件">显示全部资讯（含弱/中性）</label>
        <label class="switch">
          <input type="checkbox" v-model="newsShowAll" @change="toggleNewsShowAll" />
          <span class="slider"></span>
        </label>
      </div>
      <div class="setting-row">
        <label>说明</label>
        <span style="font-size:12px;color:#888">该开关即时生效，不影响引擎打分（引擎始终按 |score|≥0.5 过滤）</span>
      </div>
    </div>

    <!-- 系统信息（System info）-->
    <div class="setting-card">
      <div class="setting-header">系统</div>
      <div class="setting-row">
        <label>版本</label>
        <span>量仔期货 v1.1.0 桌面版</span>
      </div>
      <div class="setting-row">
        <label>后端</label>
        <span>Go 1.22+ 单二进制</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'   // Vue 组合式 API：响应式引用 ref 与挂载生命周期钩子
// Vue Composition API: reactive ref and the onMounted lifecycle hook
import * as api from '../api/index.js' // 后端 API 调用封装（状态、LLM 配置、战法参数等）
// backend API wrapper (status, LLM config, strategy params etc.)

// ── 服务器连接 ──
// ── Server connection ──
const serverUrl = ref(api.getStoredServer() || '') // 服务器地址（默认取 localStorage 已存值）
// server URL (defaults to the value already stored in localStorage)
const serverOnline = ref(false)                    // 后端服务是否在线
// whether the backend service is online

// ── 账户信息 ──
// ── Account info ──
const account = ref(api.getAccount())                                  // 当前登录账号
// the currently logged-in account
const token = ref(localStorage.getItem('liangzai_token') || '')        // 登录令牌（仅展示前 20 位）
// login token (only the first 20 chars shown)

// ── LLM 配置 ──
// ── LLM config ──
const llmApiUrl = ref('')          // LLM API 地址
// LLM API URL
const llmApiKey = ref('')          // LLM API Key（密码框输入）
// LLM API Key (entered in a password field)
const llmModel = ref('')           // LLM 模型名
// LLM model name
const llmBatchConcurrency = ref(4) // 新闻归因 LLM 批量并发度
// news-attribution LLM batch concurrency
const llmConfigured = ref(false)   // LLM 是否已配置（未配置则降级为关键词过滤）
// whether LLM is configured (degrades to keyword filtering otherwise)
const llmSaving = ref(false)       // LLM 配置保存中（禁用按钮防重复提交）
// LLM config saving (disables the button to prevent double submits)
const llmMsg = ref('')             // LLM 配置保存结果反馈文本
// LLM config save feedback message
const llmMsgType = ref('ok')       // LLM 反馈类型：'ok' 成功 / 'err' 失败
// LLM feedback type: 'ok' success / 'err' failure

// ── 战法参数配置 ──
// ── Strategy parameter config ──
const strategyCfg = ref({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }) // 五大战法参数字典，key 对应后端 config.json 分组
// dictionary of five strategy parameter groups; keys map to backend config.json groups
const strategySaving = ref(false)  // 战法参数保存中（禁用按钮防重复提交）
// strategy params saving (disables the button to prevent double submits)
const strategyMsg = ref('')        // 战法参数保存结果反馈文本
// strategy params save feedback message
const strategyMsgType = ref('ok')  // 战法参数反馈类型：'ok' 成功 / 'err' 失败
// strategy feedback type: 'ok' success / 'err' failure

/** 四个战法的字段定义（key 对应后端 config.json 的 json tag） */
/** Field definitions for the strategies (keys are the json tags of backend config.json) */
const strategyGroups = [
  {
    key: 'dragon', title: '龙头战法（权重合计≤1）',
    fields: [
      { k: 'f1_seal_weight', label: 'F1 首封权重', step: 0.05 },
      { k: 'f2_resonance_weight', label: 'F2 共振权重', step: 0.05 },
      { k: 'f3_premium_weight', label: 'F3 溢价权重', step: 0.05 },
      { k: 'f4_rs_weight', label: 'F4 强度权重', step: 0.05 },
      { k: 'f3_one_board_discount', label: '一字板折扣', step: 0.1 },
      { k: 'pullback_max_pct', label: '最大回撤%', step: 0.01 },
      { k: 'breaker_sell_half_pct', label: '炸板减半%', step: 0.01 },
      { k: 'breaker_sell_all_pct', label: '炸板清仓%', step: 0.01 },
      { k: 'buy_pullback_sell_half_pct', label: '买入回撤减半%', step: 0.01 },
      { k: 'buy_pullback_sell_all_pct', label: '买入回撤清仓%', step: 0.01 },
      { k: 'buy_day_close_below', label: '买入日收盘低于%', step: 0.01 },
      { k: 'next_open_if_below', label: '次日开盘低于%', step: 0.01 },
    ],
  },
  {
    key: 'double_bump', title: '双响炮战法',
    fields: [
      { k: 'first_break_volume_multiple', label: '一突量比', step: 0.1 },
      { k: 'second_break_volume_multiple', label: '二突量比', step: 0.1 },
      { k: 'big_candle_threshold', label: '大阳线阈值%', step: 0.5 },
      { k: 'adjust_vol_ratio_max', label: '调整量比上限', step: 0.5 },
      { k: 'pullback_to_entity_pct', label: '回调至实体%', step: 1 },
      { k: 'adjust_days_min', label: '最短调整天数', step: 1 },
      { k: 'adjust_days_max', label: '最长调整天数', step: 1 },
      { k: 'position_weight', label: '调整深度权重', step: 0.05 },
      { k: 'ma_weight', label: '均线权重', step: 0.05 },
      { k: 'sector_weight', label: '板块权重', step: 0.05 },
      { k: 'volume_weight', label: '量能权重', step: 0.05 },
      { k: 'first_breakout_position_pct', label: '一突仓位', type: 'text' },
      { k: 'second_breakout_position_pct', label: '二突仓位', type: 'text' },
      { k: 'third_breakout_position_mode', label: '三突模式', type: 'text' },
      { k: 'double_bump_take_profit_pct', label: '止盈%', step: 0.01 },
    ],
  },
  {
    key: 'n_shape', title: 'N 形战法',
    fields: [
      { k: 'n_pattern_score_threshold', label: 'N 形态分阈值', step: 1 },
      { k: 'n_shape_D1_threshold', label: 'D1 事件阈值', step: 0.05 },
      { k: 'n_shape_D2_min_full', label: 'D2 满分最低', step: 1 },
      { k: 'n_shape_D3_over', label: 'D3 超跌阈值', step: 0.05 },
      { k: 'oversold_pb_ratio', label: '超跌 PB 比', step: 0.05 },
      { k: 'n_shape_entry_left_pct', label: '左侧入场%', step: 0.05 },
      { k: 'n_shape_entry_right_pct', label: '右侧入场%', step: 0.05 },
      { k: 'n_shape_breakout_ratio', label: '突破幅度比', step: 0.05 },
      { k: 'n_shape_vol_ratio', label: '量比', step: 0.1 },
      { k: 'n_shape_flag_retreat_pct', label: '旗形回撤%', step: 0.01 },
      { k: 'n_flag_vol_ratio_max', label: '旗形量比上限', step: 0.1 },
      { k: 'n_second_break_vol_ratio', label: '二突量比', step: 0.1 },
      { k: 'n_second_break_macd_red_bars', label: '二突红柱数', step: 1 },
      { k: 'n_flag_duration_min', label: '旗形最短天数', step: 1 },
      { k: 'n_flag_duration_max', label: '旗形最长天数', step: 1 },
      { k: 'n_second_break_time_limit', label: '二突时间限制', type: 'text' },
      { k: 'hard_stop_loss', label: '硬止损%', step: 0.01 },
      { k: 'sector_gain_pct_min', label: '板块涨幅下限%', step: 0.1 },
    ],
  },
  {
    key: 'dragon_return', title: '龙回头战法',
    fields: [
      { k: 'min_pullback_pct', label: '最小回调%', step: 0.01 },
      { k: 'max_pullback_pct', label: '最大回调%', step: 0.01 },
      { k: 'volume_shrink_ratio', label: '量缩比', step: 0.05 },
      { k: 'rebound_volume_ratio', label: '反弹量比', step: 0.05 },
      { k: 'stop_loss_pct', label: '止损%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 0.01 },
      { k: 'max_hold_days', label: '最长持仓天数', step: 1 },
      { k: 'target1_multiplier', label: '目标1倍数', step: 0.05 },
      { k: 'target2_multiplier', label: '目标2倍数', step: 0.05 },
      { k: 'trailing_drawback', label: '移动止损回撤%', step: 0.01 },
    ],
  },
  {
     key: 'momentum', title: '动量分权重（合计建议=100）',
     fields: [
       { k: 'volume_price_weight', label: '量价权重', step: 5 },
       { k: 'macd_weight', label: 'MACD权重', step: 5 },
       { k: 'trend_weight', label: '走势权重', step: 5 },
       // 动量"提升才提醒"开关：开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响
       // Momentum "improvement-only" toggle: when on, double-bump / dragon / dragon-return signals only
       // pass when the momentum score improved (or fell within tolerance); N-shape is unaffected.
       { k: 'momentum_gate_enabled', label: '动量提升才提醒', type: 'switch', hint: '开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响' },
       // 回落容忍差(分)：相对上一轮动量分回落 ≤ 该值仍视为提升；设为 0 表示需严格不回落
       // Momentum delta tolerance (points): a fall from the prior score within this value still counts as
       // improvement; set to 0 for strictly no-fallback.
       { k: 'momentum_delta_tol', label: '回落容忍差(分)', step: 1, hint: '动量分相对上一轮回落 ≤ 该值仍视为提升；设为0表示需严格不回落' },
     ],
   },
 ]

/** 保存战法参数到后端并持久化 */
/** Save the strategy parameters to the backend for persistence */
async function saveStrategy() {
  strategySaving.value = true
  strategyMsg.value = ''
  try {
    await api.setStrategyConfig(strategyCfg.value)
    strategyMsg.value = '战法参数已保存，热更新即时生效'
    strategyMsgType.value = 'ok'
  } catch (e) {
    strategyMsg.value = '保存失败: ' + (e.message || '未知错误')
    strategyMsgType.value = 'err'
  }
  strategySaving.value = false
}

// ── 资讯显示开关 ──
// ── "Show all news" toggle ──
const newsShowAll = ref(false) // "显示全部资讯"开关状态（含弱/中性）
// "show all news" toggle state (includes weak/neutral)

/** 切换"显示全部资讯"开关并同步后端 */
/** Toggle "show all news" and sync with the backend */
async function toggleNewsShowAll() {
  try {
    const res = await api.toggleNewsShowAll(newsShowAll.value)
    if (res && typeof res.news_show_all === 'boolean') newsShowAll.value = res.news_show_all
  } catch (e) {
    newsShowAll.value = !newsShowAll.value
    alert('切换失败: ' + (e.message || '未知错误'))
  }
}

/** 保存服务器地址到 localStorage */
/** Save the server URL to localStorage */
function saveServer() {
  // 持久化服务器地址
  // Persist the server URL
  api.setStoredServer(serverUrl.value)
  alert('服务器地址已保存')
}

/** 请求浏览器通知权限并发送测试通知 */
/** Request browser notification permission and send a test notification */
function requestNotify() {
  if ('Notification' in window) {
    Notification.requestPermission().then(perm => {
      if (perm === 'granted') {
        // 授权通过则弹出测试通知
        // Permission granted: show a test notification
        new Notification('量仔期货', { body: '通知授权成功' })
        alert('通知授权成功')
      } else {
        alert('通知被拒绝，请在浏览器设置中开启')
      }
    })
  } else {
    alert('浏览器不支持通知')
  }
}

/** 播放测试提示音（660Hz 正弦波，200ms） */
/** Play a test beep (660Hz sine wave, 200ms) */
function playTest() {
  try {
    // 创建音频上下文并组装振荡器/音量链路
    // Create an audio context and wire up the oscillator/gain chain
    const ctx = new (window.AudioContext || window.webkitAudioContext)()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.connect(gain); gain.connect(ctx.destination)
    // 设定频率与音量后播放 200ms
    // Set frequency and volume, then play for 200ms
    osc.frequency.value = 660; osc.type = 'sine'
    gain.gain.value = 0.1; osc.start(); osc.stop(ctx.currentTime + 0.2)
  } catch (_) {}
}

/** 保存 LLM 配置到后端 */
/** Save the LLM configuration to the backend */
async function saveLLM() {
  // 进入保存中状态并清空上次反馈
  // Enter the saving state and clear the previous feedback
  llmSaving.value = true
  llmMsg.value = ''
  try {
    // 调用后端接口保存 LLM 配置
    // Call the backend endpoint to save the LLM config
    await api.setLLMConfig({
      api_key: llmApiKey.value,
      api_url: llmApiUrl.value,
      model: llmModel.value,
      batch_concurrency: llmBatchConcurrency.value,
    })
    // 依据是否填写 Key 判断配置是否生效
    // Whether the config takes effect depends on whether a Key was provided
    llmConfigured.value = !!llmApiKey.value
    llmMsg.value = 'LLM 配置已保存并热生效'
    llmMsgType.value = 'ok'
  } catch (e) {
    // 保存失败时展示错误信息
    // Show the error message when saving fails
    llmMsg.value = '保存失败: ' + (e.message || '未知错误')
    llmMsgType.value = 'err'
  }
  llmSaving.value = false
}

/** 挂载时加载服务连接状态和 LLM 现有配置 */
/** On mount, load the server connection status and existing LLM config */
onMounted(async () => {
  try {
    // 探测后端服务是否在线
    // Probe whether the backend service is online
    const st = await api.fetchStatus()
    serverOnline.value = true
  } catch (_) { serverOnline.value = false }
  try {
    // 拉取已保存的 LLM 配置并回填表单
    // Fetch the saved LLM config and backfill the form
    const cfg = await api.fetchLLMConfig()
    if (cfg) {
      llmApiUrl.value = cfg.api_url || ''
      llmModel.value = cfg.model || ''
      if (cfg.batch_concurrency > 0) llmBatchConcurrency.value = cfg.batch_concurrency
      llmConfigured.value = !!(cfg.api_key || cfg.api_url)
    }
  } catch (_) {}
  try {
    // 拉取四战法参数并回填表单
    // Fetch the strategy params and backfill the form
    const sc = await api.fetchStrategyConfig()
    if (sc) {
      strategyCfg.value = { dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }
      for (const group of strategyGroups) {
        const src = sc[group.key]
        if (src) Object.assign(strategyCfg.value[group.key], src)
      }
    }
  } catch (_) {}
  try {
    // 拉取"显示全部资讯"开关状态
    // Fetch the "show all news" toggle state
    const ns = await api.fetchNewsShowAllStatus()
    if (ns && typeof ns.news_show_all === 'boolean') newsShowAll.value = ns.news_show_all
  } catch (_) {}
})
</script>

<style scoped>
.settings-page { max-width: 600px; }
.settings-page h2 { font-size: 18px; font-weight: 600; margin-bottom: 16px; }
.setting-card {
  background: #1a1a2e; border-radius: 8px; padding: 16px; margin-bottom: 12px;
}
.setting-header { font-size: 14px; font-weight: 600; color: #ccc; margin-bottom: 12px; }
.setting-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 0; font-size: 13px;
}
.setting-row label { color: #888; }
.setting-row input {
  padding: 6px 10px; border-radius: 4px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 13px; width: 240px; outline: none;
}
.setting-row input:focus { border-color: #FF4D4F; }
.status.online { color: #4caf50; }
.status.offline { color: #888; }
.account { color: #FF4D4F; }
.btn-save, .btn-test {
  margin-top: 8px; padding: 6px 16px; border-radius: 4px; border: 1px solid #333;
  background: transparent; color: #e0e0e0; cursor: pointer; font-size: 13px;
}
.btn-save:hover, .btn-test:hover { background: #2a2a3e; }
.btn-save:disabled { opacity: 0.5; cursor: not-allowed; }
.feedback { font-size: 12px; margin-top: 8px; padding: 4px 8px; border-radius: 4px; display: inline-block; }
.switch { position: relative; display: inline-block; width: 44px; height: 24px; }
.switch input { opacity: 0; width: 0; height: 0; }
.slider {
  position: absolute; cursor: pointer; inset: 0; border-radius: 24px;
  background: #333; transition: 0.3s;
}
.slider::before {
  content: ''; position: absolute; height: 18px; width: 18px; left: 3px; top: 3px;
  border-radius: 50%; background: #888; transition: 0.3s;
}
.switch input:checked + .slider { background: #FF4D4F; }
.switch input:checked + .slider::before { transform: translateX(20px); background: #fff; }
.feedback.ok { color: #4caf50; }
.feedback.err { color: #FF4D4F; }
</style>
