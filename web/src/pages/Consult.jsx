// ── 股票咨询页面 Consult.jsx ──
// 提供与 LLM 的多轮对话能力，支持专业模式切换、LLM 配置、历史记录加载与清空。
// 使用 TDesign React 组件（Card / Switch / Input / Textarea / Button / Tag）。
import React, { useState, useEffect, useRef, useCallback } from 'react'
import { Card, Input, Textarea, Button, Tag } from 'tdesign-react'
import ToggleSw from '../components/ToggleSw'
import Disclaimer from '../components/Disclaimer.jsx'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import Markdown from '../components/Markdown.jsx'

// 将 ISO 时间格式化为 HH:mm:ss，用于消息气泡展示
function fmtTime(t) {
  if (!t) return ''
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

// 聊天框容器样式：占满剩余高度并允许纵向滚动，消息按时间纵列排列
const chatBoxStyle = { flex: 1, overflowY: 'auto', padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }
// 单条消息气泡样式：限制最大宽度、圆角、自动折行，避免长文本溢出
const bubbleStyle = { maxWidth: '80%', padding: '10px 12px', borderRadius: 8, fontSize: 14, lineHeight: 1.5, wordBreak: 'break-word' }

/**
 * 股票咨询页面组件
 * 与 LLM 进行多轮对话，支持专业模式与 LLM 配置管理。
 * @returns {JSX.Element}
 */
export default function Consult() {
  const [messages, setMessages] = useState([])
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(false)
  const chatBox = useRef(null) // 聊天消息容器引用（自动滚动到底部）

  // 带数据咨询默认开（§生产 20260916：AI 顾问必须拿到个股近期+今日实测数据再回答）；挂载后以 GET /api/consult/pro-mode 为准
  const [proMode, setProMode] = useState(true)
  const [proModeSaving, setProModeSaving] = useState(false)

  const [llmConfigured, setLlmConfigured] = useState(true)
  // §FIX-7(a)(20260919) llmGated：LLM 配置由管理员统一维护（GET 回 403）时置真——
  // 旧实现把 403 吞成"未配置"，给子账号弹一张保存必然失败的配置卡（误导性常驻）。
  const [llmGated, setLlmGated] = useState(false)
  // §FIX-7(a)：LLM 写入端点是 admin-only（全员咨询共用运营 key 计费，配置属运营级动作），
  // 非管理员不再展示可编辑配置卡。
  const admin = api.isAdmin()
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
    // 清空输入框、设置加载状态、追加用户消息到聊天列表
    setDraft('')
    setLoading(true)
    setMessages((m) => [...m, { role: 'user', content: text, time: new Date().toISOString() }])
    scrollToBottom()
    try {
      // 调用后端咨询接口，获取 LLM 回复
      const res = await api.consultChat(text)
      setMessages((m) => [...m, { role: 'assistant', content: res.reply, time: new Date().toISOString() }])
      // 回复包含"未配置"时标记 LLM 未配置，触发配置卡片显示
      if (res.reply && res.reply.includes('未配置')) {
        setLlmConfigured(false)
      }
    } catch (e) {
      // 请求失败：追加错误消息到聊天列表
      setMessages((m) => [...m, { role: 'assistant', content: '⚠️ ' + (e.message || '咨询失败'), time: new Date().toISOString() }])
      // §FIX-9d(20260919)：改按后端机读错误码判定"未配置"，不再拿 message 猜"配置"关键字
      // （旧逻辑任何含"配置"二字的错误——如"上游模型服务拒绝了请求…核查配置"——都会误弹配置卡）。
      if (e.code === 'llm_not_configured') {
        setLlmConfigured(false)
      }
    } finally {
      // 无论成功失败，结束加载状态并滚动到底部
      setLoading(false)
      scrollToBottom()
    }
  }

  // 保存 LLM API 地址、Key 与模型配置
  //
  // 2026-09-18：这里只提交用户填了的字段（`|| undefined`）——后端已把"未提交"定义成
  // **保持原值**（此前会解成空串并清掉已存的供应商地址/模型）。响应也改为如实回报：
  // 只有后端确认 applied 才算保存成功，否则展示探测给出的原因（含逐把 key 的结论）。
  async function saveLLM() {
    setLlmSaving(true)
    setLlmMsg('')
    try {
      // 调用后端保存 LLM 配置接口
      const resp = await api.setLLMConfig({
        api_keys: cfgApiKey ? [cfgApiKey] : undefined,
        api_url: cfgApiUrl || undefined,
        model: cfgModel || undefined,
      })
      const res = resp?.result || {}
      if (!res.applied) {
        setLlmMsg('LLM 配置未生效（探测未通过）：' + (res.reason || res.warning || '请检查配置'))
        setLlmMsgType('err')
      } else {
        // 已生效：标记已配置。未验证/有保留意见时如实说明，不谎报"成功"。
        setLlmConfigured(true)
        setLlmMsg(res.warning ? 'LLM 配置已生效，但有保留意见：' + res.warning : 'LLM 配置已热生效并验证通过')
        setLlmMsgType(res.warning ? 'err' : 'ok')
      }
    } catch (e) {
      // 保存失败：显示错误提示（409 拒绝时 e.message 即后端给出的逐把探测结论）
      setLlmMsg('保存失败: ' + (e.message || '未知错误'))
      setLlmMsgType('err')
    }
    setLlmSaving(false)
  }

  // 清空后端咨询历史记录
  // §FIX-9g(20260919)：先服务端确认、后清屏——后端引擎缺失已改回 503（不再假 ok），
  // 失败时保留消息并给出可见提示，杜绝"界面清空、刷新复活"的分裂态。
  async function onClear() {
    try {
      await api.clearConsultHistory()
      setMessages([])
    } catch (e) {
      setMessages((m) => [...m, { role: 'assistant', content: '⚠️ 清空失败：' + (e.message || '服务端未确认') + '（历史仍在，请稍后重试）', time: new Date().toISOString() }])
    }
  }

  // 初始化：加载 LLM 配置、专业模式与历史记录
  // 页面初始化：加载 LLM 配置、专业模式状态、历史聊天记录
  useEffect(() => {
    ;(async () => {
      // 探测 LLM 配置状态：有 API Key 或自定义 API 地址即视为已配置
      try {
        const cfg = await api.fetchLLMConfig()
        if (cfg) {
          setCfgApiUrl(cfg.api_url || '')
          setCfgModel(cfg.model || '')
          setLlmConfigured(!!(cfg.api_keys && cfg.api_keys.length) || !!cfg.api_url)
        } else {
          setLlmConfigured(false)
        }
      } catch (e) {
        // §FIX-7(a)(20260919)：403=无权限查看（管理员统一配置），不是"未配置"。
        if (api.isForbidden(e)) {
          setLlmGated(true)
          setLlmConfigured(true) // 不弹可编辑卡；咨询是否可用以实际发送结果为准
        } else {
          setLlmConfigured(false)
        }
      }
      await loadProMode()
      await loadHistory()
    })()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 渲染单条聊天消息气泡：用户右对齐蓝色，AI左对齐灰色，含时间戳
  function renderMessage(m, i) {
    return (
      <div key={i} style={{ alignSelf: m.role === 'user' ? 'flex-end' : 'flex-start' }}>
        <div className="muted" style={{ fontSize: 12, marginBottom: 2 }}>{m.role === 'user' ? '我' : 'AI 顾问'}</div>
        <div style={{
          ...bubbleStyle,
          background: m.role === 'user' ? 'var(--td-brand-color)' : 'var(--app-surface-2)',
          color: m.role === 'user' ? '#fff' : 'var(--app-text)',
        }}>{m.role === 'user' ? m.content : <Markdown text={m.content} />}</div>
        {m.time && <div className="muted" style={{ fontSize: 12, marginTop: 2, textAlign: m.role === 'user' ? 'right' : 'left' }}>{fmtTime(m.time)}</div>}
      </div>
    )
  }

  // 输入框按键事件处理：Enter 发送、Shift+Enter 换行
  function handleInputKeydown(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      onSend()
    }
  }

  // 渲染底部输入区域：多行文本框 + 发送按钮，支持 Enter 发送、Shift+Enter 换行
  function renderInputArea() {
    // 发送按钮：加载中或无输入时禁用
    const sendBtn = (
      <Button theme="primary" onClick={onSend} disabled={loading || !draft.trim()}>
        {loading ? '...' : '发送'}
      </Button>
    )
    // 文本输入框：支持多行、自动伸缩、Enter 发送
    const textInput = (
      <Textarea
        value={draft}
        onChange={(v) => setDraft(v)}
        placeholder="输入你想咨询的问题，Enter 发送，Shift+Enter 换行"
        autosize={{ minRows: 2, maxRows: 6 }}
        style={{ flex: 1 }}
        onKeydown={handleInputKeydown}
      />
    )

    // 输入区域：多行文本框 + 发送按钮，支持 Enter 发送
    return (
      <div style={{ display: 'flex', gap: 8, marginTop: 12, alignItems: 'flex-end' }}>
        {textInput}
        {sendBtn}
      </div>
    )
  }

  /* 股票咨询页面主渲染：工具栏 → LLM配置（未配置时） → 聊天消息区 → 输入框 */
  return (
    <div className="page" style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 32px)' }}>
      {/* 顶部工具栏：标题 + 专业模式开关 + 清空对话按钮 */}
      <div className="toolbar" style={{ justifyContent: 'space-between', marginBottom: 12, flexWrap: 'wrap', gap: 8 }}>
        <SectionLabel>股票咨询</SectionLabel>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {/* §FIX-9a(20260919)：提示语删除"盘中每 15 分钟限流一次"承诺——该限流早已随
              "带数据咨询改默认能力"（§生产 20260916）整体移除，UI 继续承诺会误导用户等待。 */}
          <label className="muted" title="开启后咨询将注入该股全部实时行情（现价/净流入/大单明细/均线/MACD/策略信号）。" style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
            <ToggleSw checked={proMode} disabled={proModeSaving} onChange={onToggleProMode} />
            专业模式
          </label>
          <Button theme="default" variant="outline" onClick={onClear} disabled={loading}>🗑 清空对话</Button>
        </div>
      </div>

      {/* LLM 配置卡片：首次使用或未配置 API Key 时显示，可填写地址/Key/模型。
          §FIX-7(a)(20260919)：仅管理员可见可编辑——LLM 属运营级共享配置（全员咨询/新闻归因/D1
          共用这一份 key），子账号拿到卡也只会撞后端 403；无权限者改为只读提示卡。 */}
      {!llmConfigured && admin && !llmGated && (
        <Card title="🔑 LLM 配置（首次使用请填写 API Key）" style={{ marginBottom: 12 }}>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            <Input value={cfgApiUrl} onChange={(v) => setCfgApiUrl(v)} placeholder="API 地址（如 https://api.siliconflow.cn/v1/chat/completions）" style={{ minWidth: 240, flex: 1 }} />
            <Input value={cfgApiKey} type="password" onChange={(v) => setCfgApiKey(v)} placeholder="API Key (sk-...)" style={{ minWidth: 240, flex: 1 }} />
            <Input value={cfgModel} onChange={(v) => setCfgModel(v)} placeholder="模型（如 THUDM/GLM-Z1-9B-0414）" style={{ minWidth: 200, flex: 1 }} />
            <Button theme="primary" onClick={saveLLM} loading={llmSaving}>保存</Button>
          </div>
          {llmMsg && <div style={{ marginTop: 8 }}><Tag theme={llmMsgType === 'ok' ? 'success' : 'danger'} variant="light">{llmMsg}</Tag></div>}
        </Card>
      )}
      {(llmGated || !llmConfigured) && !admin && (
        <Card title="ℹ️ AI 顾问由管理员统一配置" style={{ marginBottom: 12 }}>
          <div className="muted" style={{ fontSize: 13 }}>
            LLM 密钥与模型由管理员在设置页统一维护；若咨询不可用或提示当日额度用尽，请联系管理员调整，无需在本页配置。
          </div>
        </Card>
      )}

      {/* 聊天消息区域：用户消息右对齐蓝色气泡，AI消息左对齐灰色气泡 */}
      <div ref={chatBox} style={chatBoxStyle}>
        {messages.length === 0 && <div className="muted" style={{ textAlign: 'center', padding: 24 }}>开始咨询，向 AI 提问任意 A 股相关问题</div>}
        {messages.map(renderMessage)}
        {/* AI思考中状态指示器 */}
        {loading && (
          <div style={{ alignSelf: 'flex-start' }}>
            <div className="muted" style={{ fontSize: 12, marginBottom: 2 }}>AI 顾问</div>
            <div style={{ ...bubbleStyle, background: 'var(--app-surface-2)', color: 'var(--app-text)' }}>思考中...</div>
          </div>
        )}
      </div>

      {/* §F6 咨询合规尾注（UAT 4.1）：AI 回答为模型生成，参考性质 */}
      {messages.length > 0 && <Disclaimer variant="inline" />}
      {/* 底部输入区域：多行文本框 + 发送按钮，支持 Enter 发送、Shift+Enter 换行 */}
      {renderInputArea()}
    </div>
  )
}

// 板块小标题
function SectionLabel({ children }) {
  return <div style={{ fontWeight: 600, margin: '8px 0 4px', fontSize: 13 }}>{children}</div>
}
