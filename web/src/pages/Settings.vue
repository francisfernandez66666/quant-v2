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
import { ref, onMounted } from 'vue'
import * as api from '../api/index.js'

// ── 服务器连接 ──
const serverUrl = ref(api.getStoredServer() || '')
const serverOnline = ref(false)

// ── 账户信息 ──
const account = ref(api.getAccount())
const token = ref(localStorage.getItem('liangzai_token') || '')

// ── LLM 配置 ──
const llmApiUrl = ref('')
const llmApiKey = ref('')
const llmModel = ref('')
const llmConfigured = ref(false)
const llmSaving = ref(false)
const llmMsg = ref('')
const llmMsgType = ref('ok')

/** 保存服务器地址到 localStorage */
function saveServer() {
  api.setStoredServer(serverUrl.value)
  alert('服务器地址已保存')
}

/** 请求浏览器通知权限并发送测试通知 */
function requestNotify() {
  if ('Notification' in window) {
    Notification.requestPermission().then(perm => {
      if (perm === 'granted') {
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
    const ctx = new (window.AudioContext || window.webkitAudioContext)()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.connect(gain); gain.connect(ctx.destination)
    osc.frequency.value = 660; osc.type = 'sine'
    gain.gain.value = 0.1; osc.start(); osc.stop(ctx.currentTime + 0.2)
  } catch (_) {}
}

/** 保存 LLM 配置到后端 */
async function saveLLM() {
  llmSaving.value = true
  llmMsg.value = ''
  try {
    await api.setLLMConfig({ api_key: llmApiKey.value, api_url: llmApiUrl.value, model: llmModel.value })
    llmConfigured.value = !!llmApiKey.value
    llmMsg.value = 'LLM 配置已保存，重启后端生效'
    llmMsgType.value = 'ok'
  } catch (e) {
    llmMsg.value = '保存失败: ' + (e.message || '未知错误')
    llmMsgType.value = 'err'
  }
  llmSaving.value = false
}

/** 挂载时加载服务连接状态和 LLM 现有配置 */
onMounted(async () => {
  try {
    const st = await api.fetchStatus()
    serverOnline.value = true
  } catch (_) { serverOnline.value = false }
  try {
    const cfg = await api.fetchLLMConfig()
    if (cfg) {
      llmApiUrl.value = cfg.api_url || ''
      llmModel.value = cfg.model || ''
      llmConfigured.value = !!(cfg.api_key || cfg.api_url)
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
