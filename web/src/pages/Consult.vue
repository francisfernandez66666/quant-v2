<!--
  股票咨询页面 Consult.vue (Stock consultation page)
  多轮 LLM 股票咨询：对话气泡展示 + 输入框发送 + 内联 API Key 配置区
  Multi-turn LLM stock consultation: chat bubbles + message input + inline API Key config
  对话历史保存在后端（consult_history.json），跨交易日自动清空
  Conversation history is kept on the backend (consult_history.json) and cleared each trading day
-->
<template>
  <div class="consult-page">
    <!-- 页头：标题 + 专业模式开关 + 刷新历史 + 清空对话 -->
    <div class="page-header">
      <h2>股票咨询</h2>
      <div class="header-right">
        <label class="pro-mode" title="开启后咨询将注入该股全部实时行情（现价/净流入/大单明细/均线/MACD/策略信号）。盘中每 15 分钟限流一次，盘前盘后不限。">
          <input type="checkbox" v-model="proMode" @change="onToggleProMode" :disabled="proModeSaving" />
          专业模式
        </label>
        <button class="btn-clear" @click="onClear" :disabled="loading">
          🗑 清空对话
        </button>
      </div>
    </div>

    <!-- 内联 API Key 配置区：LLM 未配置或未初始化时展示，配置后折叠 -->
    <div v-if="!llmConfigured" class="llm-config">
      <div class="llm-config-title">🔑 LLM 配置（首次使用请填写 API Key）</div>
      <div class="llm-config-row">
        <input v-model="cfgApiUrl" placeholder="API 地址（如 https://api.siliconflow.cn/v1/chat/completions）" />
        <input v-model="cfgApiKey" type="password" placeholder="API Key (sk-...)" />
        <input v-model="cfgModel" placeholder="模型（如 THUDM/GLM-Z1-9B-0414）" />
        <button class="btn-save" @click="saveLLM" :disabled="llmSaving">
          {{ llmSaving ? '保存中...' : '保存' }}
        </button>
      </div>
      <p v-if="llmMsg" :class="['llm-msg', llmMsgType === 'ok' ? 'msg-ok' : 'msg-err']">{{ llmMsg }}</p>
    </div>

    <!-- 对话区：消息气泡列表（user 靠右、assistant 靠左；加载历史后自动滚到底部） -->
    <div ref="chatBox" class="chat-box">
      <div v-if="messages.length === 0" class="chat-empty">开始咨询，向 AI 提问任意 A 股相关问题</div>
      <div v-for="(m, i) in messages" :key="i" :class="['bubble', m.role === 'user' ? 'bubble-user' : 'bubble-assistant']">
        <div class="bubble-name">{{ m.role === 'user' ? '我' : 'AI 顾问' }}</div>
        <div class="bubble-content">{{ m.content }}</div>
        <div v-if="m.time" class="bubble-time">{{ formatTime(m.time) }}</div>
      </div>
      <!-- 输入中状态：等待 LLM 回复时展示占位气泡 -->
      <div v-if="loading" class="bubble bubble-assistant bubble-loading">
        <div class="bubble-name">AI 顾问</div>
        <div class="bubble-content">思考中...</div>
      </div>
    </div>

    <!-- 输入区：消息输入框（Enter 发送 / Shift+Enter 换行）+ 发送按钮 -->
    <div class="chat-input">
      <textarea
        v-model="draft"
        placeholder="输入你想咨询的问题，Enter 发送，Shift+Enter 换行"
        rows="2"
        @keydown.enter.exact.prevent="onSend"
      ></textarea>
      <button class="btn-send" @click="onSend" :disabled="loading || !draft.trim()">
        {{ loading ? '...' : '发送' }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { ref, nextTick, onMounted } from 'vue' // Vue 组合式 API：响应式引用 ref 与挂载生命周期钩子 (Vue composition API: reactive ref + mount lifecycle hook)
import * as api from '../api/index.js' // 后端 API 调用封装（咨询 / 历史 / LLM 配置） (backend API wrapper: chat / history / LLM config)

// ── 响应式状态 ── (Reactive state)
const messages = ref([])          // 当日对话历史（user/assistant 消息）(today's conversation history: user/assistant messages)
const draft = ref('')             // 输入框草稿 (input draft text)
const loading = ref(false)        // 是否正在等待 LLM 回复 (whether waiting for the LLM reply)
const chatBox = ref(null)         // 对话区 DOM 引用（滚动到底部）(reference to the chat area DOM for auto-scroll)
const proMode = ref(false)        // 专业模式开关：开启后注入全部实时行情 (pro mode: injects full realtime market data when on)
const proModeSaving = ref(false)  // 开关是否正在保存 (whether the toggle is being saved)

// ── LLM 配置区状态 ── (LLM config area state)
const llmConfigured = ref(true)   // LLM 是否已配置（未配置时展示内联表单）(whether LLM is configured; show inline form when not)
const llmSaving = ref(false)      // 是否正在保存配置 (whether config saving is in progress)
const llmMsg = ref('')            // 配置保存反馈信息 (feedback message for config save)
const llmMsgType = ref('ok')      // 反馈类型：ok / err (feedback type: ok / err)
const cfgApiUrl = ref('')         // API 地址 (API URL)
const cfgApiKey = ref('')         // API Key (API Key)
const cfgModel = ref('')          // 模型名 (model name)

/** 格式化时间为 HH:mm:ss (Format time as HH:mm:ss) */
function formatTime(t) {
  if (!t) return ''
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/** 滚动对话区到底部 (Scroll the chat area to the bottom) */
// 在 DOM 更新后（nextTick）再执行滚动，确保新消息已渲染 (scroll after DOM updates in nextTick so new messages are rendered)
function scrollToBottom() {
  nextTick(() => {
    if (chatBox.value) chatBox.value.scrollTop = chatBox.value.scrollHeight
  })
}

/** 加载当日咨询历史 (Load today's consultation history) */
async function loadHistory() {
  try {
    const h = await api.fetchConsultHistory()
    messages.value = Array.isArray(h) ? h : []
  } catch (_) {}
  scrollToBottom()
}

/** 加载专业模式开关状态 (Load the pro mode toggle state) */
async function loadProMode() {
  try {
    const r = await api.fetchConsultProMode()
    proMode.value = !!(r && r.enabled)
  } catch (_) {}
}

/** 切换专业模式开关 (Toggle pro mode) */
async function onToggleProMode() {
  proModeSaving.value = true
  try {
    const r = await api.setConsultProMode(proMode.value)
    proMode.value = !!(r && r.enabled)
  } catch (e) {
    // 保存失败回滚开关状态，避免界面与后端不一致 (on failure, revert the toggle so UI stays consistent with the backend)
    proMode.value = !proMode.value
    messages.value.push({ role: 'assistant', content: '⚠️ 专业模式切换失败: ' + (e.message || '未知错误'), time: new Date().toISOString() })
  } finally {
    proModeSaving.value = false
  }
}

/** 发送咨询消息 (Send a consultation message) */
async function onSend() {
  const text = draft.value.trim()
  if (!text || loading.value) return
  draft.value = ''
  loading.value = true
  // 乐观追加用户消息，立即展示 (optimistically append the user message so it shows immediately)
  messages.value.push({ role: 'user', content: text, time: new Date().toISOString() })
  scrollToBottom()
  try {
    const res = await api.consultChat(text)
    // 回复成功后追加 AI 消息（后端已持久化完整对话）(append the AI reply on success; the backend already persists the full conversation)
    messages.value.push({ role: 'assistant', content: res.reply, time: new Date().toISOString() })
    if (res.reply && res.reply.includes('未配置')) {
      llmConfigured.value = false
    }
  } catch (e) {
    // 咨询失败（如未配置 LLM / 网络错误 / 盘中限流）：提示并刷新历史回滚
    // (on failure — e.g. LLM unconfigured / network error / intraday rate limit — show the error message)
    messages.value.push({ role: 'assistant', content: '⚠️ ' + (e.message || '咨询失败'), time: new Date().toISOString() })
    if ((e.message || '').includes('LLM_API_KEY') || (e.message || '').includes('配置')) {
      // 检测到 LLM 未配置时收起对话并展示配置表单 (when LLM not configured, reveal the config form)
      llmConfigured.value = false
    }
  } finally {
    loading.value = false
    scrollToBottom()
  }
}

/** 保存 LLM 配置（内联表单） (Save the LLM config from the inline form) */
async function saveLLM() {
  llmSaving.value = true
  llmMsg.value = ''
  try {
    await api.setLLMConfig({
      api_key: cfgApiKey.value || undefined,
      api_url: cfgApiUrl.value || undefined,
      model: cfgModel.value || undefined,
    })
    llmConfigured.value = true
    llmMsg.value = 'LLM 配置已保存'
    llmMsgType.value = 'ok'
  } catch (e) {
    llmMsg.value = '保存失败: ' + (e.message || '未知错误')
    llmMsgType.value = 'err'
  }
  llmSaving.value = false
}

/** 清空当日对话 (Clear today's conversation) */
async function onClear() {
  try {
    await api.clearConsultHistory()
    messages.value = []
  } catch (_) {}
}

// 挂载时先拉取 LLM 配置判断是否已配置，再加载专业模式开关与历史
// (On mount: fetch LLM config to detect configuration, then load pro mode state and history)
onMounted(async () => {
  try {
    // 后端返回配置信息则回填表单并判断是否已配置 (if config returned, backfill the form and decide whether configured)
    const cfg = await api.fetchLLMConfig()
    if (cfg) {
      cfgApiUrl.value = cfg.api_url || ''
      cfgModel.value = cfg.model || ''
      llmConfigured.value = !!(cfg.api_key || cfg.api_url)
    } else {
      llmConfigured.value = false
    }
  } catch (_) { llmConfigured.value = false }
  await loadProMode()
  await loadHistory()
})
</script>

<style scoped>
.consult-page { max-width: 860px; margin: 0 auto; display: flex; flex-direction: column; height: 100%; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.page-header h2 { font-size: 18px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.pro-mode {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 12px; border-radius: 6px; border: 1px solid #b388ff;
  background: rgba(179,136,255,0.08); color: #b388ff; font-size: 14px; cursor: pointer;
}
.pro-mode input { accent-color: #b388ff; cursor: pointer; }
.pro-mode:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-clear {
  padding: 6px 14px; border-radius: 6px; border: 1px solid #FF4D4F;
  background: transparent; color: #FF4D4F; font-size: 14px; cursor: pointer;
}
.btn-clear:disabled { opacity: 0.5; }
.btn-clear:hover { background: rgba(255,77,79,0.1); }

/* LLM 配置区 */
.llm-config { background: #1a1a2e; border: 1px solid #2a2a3e; border-radius: 8px; padding: 12px 14px; margin-bottom: 12px; }
.llm-config-title { font-size: 14px; color: #b388ff; margin-bottom: 8px; }
.llm-config-row { display: flex; gap: 8px; flex-wrap: wrap; }
.llm-config-row input {
  flex: 1; min-width: 140px; padding: 8px 10px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none;
}
.llm-config-row input:focus { border-color: #b388ff; }
.btn-save {
  padding: 8px 16px; border-radius: 6px; border: none; background: #b388ff;
  color: #fff; font-size: 14px; cursor: pointer; white-space: nowrap;
}
.btn-save:disabled { opacity: 0.5; }
.llm-msg { margin-top: 8px; font-size: 14px; }
.msg-ok { color: #4caf50; }
.msg-err { color: #FF4D4F; }

/* 对话区 */
.chat-box {
  flex: 1; overflow-y: auto; background: #1a1a2e; border-radius: 8px;
  border: 1px solid #2a2a3e; padding: 14px; display: flex; flex-direction: column; gap: 10px;
  min-height: 300px; max-height: calc(100vh - 300px);
}
.chat-empty { text-align: center; color: #666; font-size: 14px; padding: 40px 0; }
.bubble { max-width: 82%; padding: 10px 12px; border-radius: 8px; font-size: 14px; line-height: 1.5; }
.bubble-name { font-size: 14px; color: #888; margin-bottom: 4px; }
.bubble-user { align-self: flex-end; background: rgba(179,136,255,0.15); border: 1px solid #b388ff; }
.bubble-user .bubble-name { text-align: right; }
.bubble-assistant { align-self: flex-start; background: #0f0f23; border: 1px solid #333; }
.bubble-content { white-space: pre-wrap; word-break: break-word; color: #e0e0e0; }
.bubble-time { font-size: 14px; color: #666; margin-top: 4px; }
.bubble-loading .bubble-content { color: #888; }

/* 输入区 */
.chat-input { display: flex; gap: 8px; margin-top: 12px; }
.chat-input textarea {
  flex: 1; padding: 10px 12px; border-radius: 8px; border: 1px solid #333;
  background: #1a1a2e; color: #e0e0e0; font-size: 14px; outline: none; resize: none;
  font-family: inherit;
}
.chat-input textarea:focus { border-color: #FF4D4F; }
.btn-send {
  padding: 0 24px; border-radius: 8px; border: none; background: #FF4D4F;
  color: #fff; font-size: 14px; cursor: pointer;
}
.btn-send:disabled { opacity: 0.5; }

/* ====== Mobile ====== */
@media (max-width: 768px) {
  .page-header { flex-wrap: wrap; gap: 8px; }
  .header-right { flex-wrap: wrap; gap: 8px; }
  .bubble { max-width: 90%; }
  .bubble-content { font-size: 14px; }
  .chat-box { max-height: calc(100vh - 340px); padding: 10px; }
}
</style>
