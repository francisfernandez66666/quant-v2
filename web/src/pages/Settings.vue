<!--
  设置页面 Settings.vue
  包含服务器连接、通知、账户信息、LLM 配置、系统信息等设置项
-->
<template>
  <div class="settings-page">
    <h2>设置</h2>

    <!-- 服务器连接设置 -->
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

    <!-- 通知设置 -->
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

    <!-- 账户信息 -->
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

    <!-- LLM 配置 -->
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
        <label>状态</label>
        <span :class="['status', llmConfigured ? 'online' : 'offline']">
          {{ llmConfigured ? '已配置' : '未配置（降级为关键词过滤）' }}
        </span>
      </div>
      <button class="btn-save" @click="saveLLM" :disabled="llmSaving">{{ llmSaving ? '保存中...' : '保存' }}</button>
      <span v-if="llmMsg" :class="['feedback', llmMsgType]">{{ llmMsg }}</span>
    </div>

    <!-- 战法参数配置 -->
    <div class="setting-card" v-for="group in strategyGroups" :key="group.key">
      <div class="setting-header">{{ group.title }}</div>
      <div class="setting-row" v-for="f in group.fields" :key="f.k">
        <label :title="f.hint || ''">{{ f.label }}</label>
        <input v-model.number="strategyCfg[group.key][f.k]"
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

    <!-- 系统信息 -->
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
import * as api from '../api/index.js' // 后端 API 调用封装（状态、LLM 配置、战法参数等）

// ── 服务器连接 ──
const serverUrl = ref(api.getStoredServer() || '') // 服务器地址（默认取 localStorage 已存值）
const serverOnline = ref(false)                    // 后端服务是否在线

// ── 账户信息 ──
const account = ref(api.getAccount())                                  // 当前登录账号
const token = ref(localStorage.getItem('liangzai_token') || '')        // 登录令牌（仅展示前 20 位）

// ── LLM 配置 ──
const llmApiUrl = ref('')          // LLM API 地址
const llmApiKey = ref('')          // LLM API Key（密码框输入）
const llmModel = ref('')           // LLM 模型名
const llmConfigured = ref(false)   // LLM 是否已配置（未配置则降级为关键词过滤）
const llmSaving = ref(false)       // LLM 配置保存中（禁用按钮防重复提交）
const llmMsg = ref('')             // LLM 配置保存结果反馈文本
const llmMsgType = ref('ok')       // LLM 反馈类型：'ok' 成功 / 'err' 失败

// ── 战法参数配置 ──
const strategyCfg = ref({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }) // 五大战法参数字典，key 对应后端 config.json 分组
const strategySaving = ref(false)  // 战法参数保存中（禁用按钮防重复提交）
const strategyMsg = ref('')        // 战法参数保存结果反馈文本
const strategyMsgType = ref('ok')  // 战法参数反馈类型：'ok' 成功 / 'err' 失败

/** 四个战法的字段定义（key 对应后端 config.json 的 json tag） */
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
    ],
  },
]

/** 保存战法参数到后端并持久化 */
async function saveStrategy() {
  strategySaving.value = true
  strategyMsg.value = ''
  try {
    await api.setStrategyConfig(strategyCfg.value)
    strategyMsg.value = '战法参数已保存，重启后端生效'
    strategyMsgType.value = 'ok'
  } catch (e) {
    strategyMsg.value = '保存失败: ' + (e.message || '未知错误')
    strategyMsgType.value = 'err'
  }
  strategySaving.value = false
}

/** 保存服务器地址到 localStorage */
function saveServer() {
  // 持久化服务器地址
  api.setStoredServer(serverUrl.value)
  alert('服务器地址已保存')
}

/** 请求浏览器通知权限并发送测试通知 */
function requestNotify() {
  if ('Notification' in window) {
    Notification.requestPermission().then(perm => {
      if (perm === 'granted') {
        // 授权通过则弹出测试通知
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
function playTest() {
  try {
    // 创建音频上下文并组装振荡器/音量链路
    const ctx = new (window.AudioContext || window.webkitAudioContext)()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.connect(gain); gain.connect(ctx.destination)
    // 设定频率与音量后播放 200ms
    osc.frequency.value = 660; osc.type = 'sine'
    gain.gain.value = 0.1; osc.start(); osc.stop(ctx.currentTime + 0.2)
  } catch (_) {}
}

/** 保存 LLM 配置到后端 */
async function saveLLM() {
  // 进入保存中状态并清空上次反馈
  llmSaving.value = true
  llmMsg.value = ''
  try {
    // 调用后端接口保存 LLM 配置
    await api.setLLMConfig({ api_key: llmApiKey.value, api_url: llmApiUrl.value, model: llmModel.value })
    // 依据是否填写 Key 判断配置是否生效
    llmConfigured.value = !!llmApiKey.value
    llmMsg.value = 'LLM 配置已保存，重启后端生效'
    llmMsgType.value = 'ok'
  } catch (e) {
    // 保存失败时展示错误信息
    llmMsg.value = '保存失败: ' + (e.message || '未知错误')
    llmMsgType.value = 'err'
  }
  llmSaving.value = false
}

/** 挂载时加载服务连接状态和 LLM 现有配置 */
onMounted(async () => {
  try {
    // 探测后端服务是否在线
    const st = await api.fetchStatus()
    serverOnline.value = true
  } catch (_) { serverOnline.value = false }
  try {
    // 拉取已保存的 LLM 配置并回填表单
    const cfg = await api.fetchLLMConfig()
    if (cfg) {
      llmApiUrl.value = cfg.api_url || ''
      llmModel.value = cfg.model || ''
      llmConfigured.value = !!(cfg.api_key || cfg.api_url)
    }
  } catch (_) {}
  try {
    // 拉取四战法参数并回填表单
    const sc = await api.fetchStrategyConfig()
    if (sc) {
      strategyCfg.value = { dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }
      for (const group of strategyGroups) {
        const src = sc[group.key]
        if (src) Object.assign(strategyCfg.value[group.key], src)
      }
    }
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
.feedback.ok { color: #4caf50; }
.feedback.err { color: #FF4D4F; }
</style>
