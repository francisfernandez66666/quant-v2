// ── 股票咨询页面 Consult.jsx ──
// 提供与 LLM 的多轮对话能力，支持专业模式切换、LLM 配置、历史记录加载与清空。
// 使用 TDesign React 组件（Card / Switch / Input / Textarea / Button / Tag）。
import React, { useState, useEffect, useRef, useCallback } from 'react'
import { Card, Switch, Input, Textarea, Button, Tag } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import './Consult.css'

// 将 ISO 时间格式化为 HH:mm:ss，用于消息气泡
function fmtTime(t) {
  if (!t) return ''
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/**
 * 股票咨询页面组件
 * 与 LLM 进行多轮对话，支持专业模式与 LLM 配置管理。
 * @returns {JSX.Element}
 */
export default function Consult() {
  const [messages, setMessages] = useState([])
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(false)
  const chatBox = useRef(null)

  const [proMode, setProMode] = useState(false)
  const [proModeSaving, setProModeSaving] = useState(false)

  const [llmConfigured, setLlmConfigured] = useState(true)
  const [llmSaving, setLlmSaving] = useState(false)
  const [llmMsg, setLlmMsg] = useState('')
  const [llmMsgType, setLlmMsgType] = useState('ok')
  const [cfgApiUrl, setCfgApiUrl] = useState('')
  const [cfgApiKey, setCfgApiKey] = useState('')
  const [cfgModel, setCfgModel] = useState('')

  // 滚动聊天框至最底部
  const scrollToBottom = useCallback(() => {
    requestAnimationFrame(() => {
      if (chatBox.current) chatBox.current.scrollTop = chatBox.current.scrollHeight
    })
  }, [])

  // 加载历史咨询记录
  async function loadHistory() {
    try {
      const h = await api.fetchConsultHistory()
      setMessages(Array.isArray(h) ? h : [])
    } catch (_) {}
    scrollToBottom()
  }

  // 加载专业模式开关状态
  async function loadProMode() {
    try {
      const r = await api.fetchConsultProMode()
      setProMode(!!(r && r.enabled))
    } catch (_) {}
  }

  // 切换专业模式并同步后端
  async function onToggleProMode(val) {
    setProModeSaving(true)
    try {
      const r = await api.setConsultProMode(val)
      setProMode(!!(r && r.enabled))
    } catch (e) {
      setProMode(!val)
      setMessages((m) => [...m, { role: 'assistant', content: '⚠️ 专业模式切换失败: ' + (e.message || '未知错误'), time: new Date().toISOString() }])
    } finally {
      setProModeSaving(false)
    }
  }

  // 发送用户问题并等待 LLM 回复，失败时展示错误消息
  async function onSend() {
    const text = draft.trim()
    if (!text || loading) return
    setDraft('')
    setLoading(true)
    setMessages((m) => [...m, { role: 'user', content: text, time: new Date().toISOString() }])
    scrollToBottom()
    try {
      const res = await api.consultChat(text)
      setMessages((m) => [...m, { role: 'assistant', content: res.reply, time: new Date().toISOString() }])
      if (res.reply && res.reply.includes('未配置')) {
        setLlmConfigured(false)
      }
    } catch (e) {
      setMessages((m) => [...m, { role: 'assistant', content: '⚠️ ' + (e.message || '咨询失败'), time: new Date().toISOString() }])
      if ((e.message || '').includes('LLM_API_KEY') || (e.message || '').includes('配置')) {
        setLlmConfigured(false)
      }
    } finally {
      setLoading(false)
      scrollToBottom()
    }
  }

  // 保存 LLM API 地址、Key 与模型配置
  async function saveLLM() {
    setLlmSaving(true)
    setLlmMsg('')
    try {
      await api.setLLMConfig({
        api_key: cfgApiKey || undefined,
        api_url: cfgApiUrl || undefined,
        model: cfgModel || undefined,
      })
      setLlmConfigured(true)
      setLlmMsg('LLM 配置已保存')
      setLlmMsgType('ok')
    } catch (e) {
      setLlmMsg('保存失败: ' + (e.message || '未知错误'))
      setLlmMsgType('err')
    }
    setLlmSaving(false)
  }

  // 清空后端咨询历史记录
  async function onClear() {
    try {
      await api.clearConsultHistory()
      setMessages([])
    } catch (_) {}
  }

  // 初始化：加载 LLM 配置、专业模式与历史记录
  useEffect(() => {
    (async () => {
      try {
        const cfg = await api.fetchLLMConfig()
        if (cfg) {
          setCfgApiUrl(cfg.api_url || '')
          setCfgModel(cfg.model || '')
          setLlmConfigured(!!(cfg.api_key || cfg.api_url))
        } else {
          setLlmConfigured(false)
        }
      } catch (_) { setLlmConfigured(false) }
      await loadProMode()
      await loadHistory()
    })()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="consult-page">
      <div className="page-header">
        <h2>股票咨询</h2>
        <div className="header-right">
          <label className="pro-mode" title="开启后咨询将注入该股全部实时行情（现价/净流入/大单明细/均线/MACD/策略信号）。盘中每 15 分钟限流一次，盘前盘后不限。">
            <Switch value={proMode} disabled={proModeSaving} onChange={onToggleProMode} />
            专业模式
          </label>
          <Button theme="default" variant="outline" onClick={onClear} disabled={loading}>🗑 清空对话</Button>
        </div>
      </div>

      {!llmConfigured && (
        <Card className="llm-config" title="🔑 LLM 配置（首次使用请填写 API Key）">
          <div className="llm-config-row">
            <Input value={cfgApiUrl} onChange={(v) => setCfgApiUrl(v)} placeholder="API 地址（如 https://api.siliconflow.cn/v1/chat/completions）" />
            <Input value={cfgApiKey} type="password" onChange={(v) => setCfgApiKey(v)} placeholder="API Key (sk-...)" />
            <Input value={cfgModel} onChange={(v) => setCfgModel(v)} placeholder="模型（如 THUDM/GLM-Z1-9B-0414）" />
            <Button theme="primary" onClick={saveLLM} loading={llmSaving}>保存</Button>
          </div>
          {llmMsg && <Tag theme={llmMsgType === 'ok' ? 'success' : 'danger'} variant="light" style={{ marginTop: 8 }}>{llmMsg}</Tag>}
        </Card>
      )}

      <div ref={chatBox} className="chat-box">
        {messages.length === 0 && <div className="chat-empty">开始咨询，向 AI 提问任意 A 股相关问题</div>}
        {messages.map((m, i) => (
          <div key={i} className={'bubble ' + (m.role === 'user' ? 'bubble-user' : 'bubble-assistant')}>
            <div className="bubble-name">{m.role === 'user' ? '我' : 'AI 顾问'}</div>
            <div className="bubble-content">{m.content}</div>
            {m.time && <div className="bubble-time">{fmtTime(m.time)}</div>}
          </div>
        ))}
        {loading && (
          <div className="bubble bubble-assistant bubble-loading">
            <div className="bubble-name">AI 顾问</div>
            <div className="bubble-content">思考中...</div>
          </div>
        )}
      </div>

      <div className="chat-input">
        <Textarea
          value={draft}
          onChange={(v) => setDraft(v)}
          placeholder="输入你想咨询的问题，Enter 发送，Shift+Enter 换行"
          autosize={{ minRows: 2, maxRows: 6 }}
          onKeydown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              onSend()
            }
          }}
        />
        <Button theme="primary" onClick={onSend} disabled={loading || !draft.trim()}>
          {loading ? '...' : '发送'}
        </Button>
      </div>
    </div>
  )
}
