// ── 设置页面 Settings.jsx ──
// 服务器连接、通知、账户信息、LLM 配置、五大战法参数、资讯显示开关、系统信息
// 使用 TDesign React 组件（Card / Input / InputNumber / Switch / Button / Tag / Textarea）。
import React, { useState, useEffect } from 'react'
import { Card, Input, InputNumber, Switch, Button, Tag, Textarea } from 'tdesign-react'
import * as api from '../api/index.js'
import { requestPermission, notify as sendNotify } from '../notify.js'
import { showToast } from '../ui.jsx'

const strategyGroups = [
  {
    key: 'dragon', title: '龙头战法（权重合计≤1）',
    fields: [
      { k: 'f1_seal_weight', label: 'F1 首封权重', step: 0.05 },
      { k: 'f2_resonance_weight', label: 'F2 共振权重', step: 0.05 },
      { k: 'f3_premium_weight', label: 'F3 溢价权重', step: 0.05 },
      { k: 'f4_rs_weight', label: 'F4 强度权重', step: 0.05 },
      { k: 'pullback_max_pct', label: '最大回撤%', step: 0.01 },
      { k: 'breaker_sell_half_pct', label: '炸板减半%', step: 0.01 },
      { k: 'breaker_sell_all_pct', label: '炸板清仓%', step: 0.01 },
      { k: 'buy_pullback_sell_half_pct', label: '买入回撤减半%', step: 0.01 },
      { k: 'buy_pullback_sell_all_pct', label: '买入回撤清仓%', step: 0.01 },
      { k: 'buy_day_close_below', label: '买入日收盘低于%', step: 0.01 },
      { k: 'next_open_if_below', label: '次日开盘低于%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 1 },
    ],
  },
  {
    key: 'double_bump', title: '双响炮战法',
    fields: [
      { k: 'first_break_volume_multiple', label: '一突量比', step: 0.1 },
      { k: 'second_break_volume_multiple', label: '二突量比', step: 0.1 },
      { k: 'adjust_vol_ratio_max', label: '调整量比上限', step: 0.5 },
      { k: 'position_weight', label: '调整深度权重', step: 0.05 },
      { k: 'ma_weight', label: '均线权重', step: 0.05 },
      { k: 'volume_weight', label: '量能权重', step: 0.05 },
      { k: 'double_bump_take_profit_pct', label: '止盈%', step: 0.01 },
    ],
  },
  {
    key: 'n_shape', title: 'N 形战法',
    fields: [
      { k: 'n_pattern_score_threshold', label: 'N 形态分阈值', step: 1 },
      { k: 'hard_stop_loss', label: '硬止损%', step: 0.01 },
    ],
  },
  {
    key: 'dragon_return', title: '龙回头战法',
    fields: [
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
      { k: 'momentum_gate_enabled', label: '动量提升才提醒', type: 'switch', hint: '开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响' },
      { k: 'momentum_delta_tol', label: '回落容忍差(分)', step: 1, hint: '动量分相对上一轮回落 ≤ 该值仍视为提升；设为0表示需严格不回落' },
    ],
  },
]

const emptyStrategy = () => ({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} })

const rowStyle = { display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' }
const labelStyle = { width: 160, flexShrink: 0, color: '#999', fontSize: 13 }

/**
 * 设置页面组件
 * 管理服务器地址、通知、LLM 配置、五大战法参数与资讯显示开关。
 * @returns {JSX.Element}
 */
export default function Settings() {
  const [serverUrl, setServerUrl] = useState(api.getStoredServer() || '')
  const [serverOnline, setServerOnline] = useState(false)

  const [account] = useState(api.getAccount())
  const [token] = useState(localStorage.getItem('liangzai_token') || '')

  const [llmApiUrl, setLlmApiUrl] = useState('')
  const [llmApiKeys, setLlmApiKeys] = useState('')
  const [llmModel, setLlmModel] = useState('')
  const [llmClassifierModel, setLlmClassifierModel] = useState('')
  const [llmBatchConcurrency, setLlmBatchConcurrency] = useState(4)
  const [llmConfigured, setLlmConfigured] = useState(false)
  const [llmSaving, setLlmSaving] = useState(false)

  const [strategyCfg, setStrategyCfg] = useState(emptyStrategy())
  const [strategySaving, setStrategySaving] = useState(false)

  const [newsShowAll, setNewsShowAll] = useState(false)

  // 保存战法参数配置
  async function saveStrategy() {
    setStrategySaving(true)
    try {
      await api.setStrategyConfig(strategyCfg)
      showToast('战法参数已保存，热更新即时生效', 'success')
    } catch (e) {
      showToast('保存失败: ' + (e.message || '未知错误'), 'error')
    }
    setStrategySaving(false)
  }

  // 切换「显示全部资讯」开关
  async function toggleNewsShowAll() {
    try {
      const res = await api.toggleNewsShowAll(newsShowAll)
      if (res && typeof res.news_show_all === 'boolean') setNewsShowAll(res.news_show_all)
    } catch (e) {
      setNewsShowAll(v => !v)
      showToast('切换失败: ' + (e.message || '未知错误'), 'error')
    }
  }

  // 保存服务器地址到本地存储
  function saveServer() {
    api.setStoredServer(serverUrl)
    showToast('服务器地址已保存', 'success')
  }

  // 请求浏览器通知权限并发送测试通知
  function requestNotify() {
    requestPermission().then(perm => {
      if (perm === 'granted') {
        sendNotify('量仔期货', '通知授权成功')
        showToast('通知授权成功', 'success')
      } else {
        showToast('通知被拒绝，请在系统设置中开启通知', 'warning')
      }
    })
  }

  // 播放测试音效，验证浏览器音频提醒可用
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

  // 保存 LLM API 地址、Key、模型与并发配置
  async function saveLLM() {
    setLlmSaving(true)
    try {
      await api.setLLMConfig({
        api_keys: llmApiKeys.split(/[\n,]/).map(s => s.trim()).filter(Boolean),
        api_url: llmApiUrl,
        model: llmModel,
        classifier_model: llmClassifierModel,
        batch_concurrency: llmBatchConcurrency,
      })
      setLlmConfigured(!!llmApiKeys)
      showToast('LLM 配置已保存并热生效', 'success')
    } catch (e) {
      showToast('保存失败: ' + (e.message || '未知错误'), 'error')
    }
    setLlmSaving(false)
  }

  // 更新指定战法分组中的某个参数字段
  function setStrategyField(group, field, value) {
    setStrategyCfg(prev => ({
      ...prev,
      [group]: { ...prev[group], [field]: value },
    }))
  }

  // 初始化：检测服务器在线状态并加载 LLM/战法/资讯开关配置
  useEffect(() => {
    ;(async () => {
      try {
        await api.fetchStatus()
        setServerOnline(true)
      } catch (_) { setServerOnline(false) }
      try {
        const cfg = await api.fetchLLMConfig()
        if (cfg) {
          setLlmApiUrl(cfg.api_url || '')
          setLlmModel(cfg.model || '')
          setLlmClassifierModel(cfg.classifier_model || '')
          if (cfg.batch_concurrency > 0) setLlmBatchConcurrency(cfg.batch_concurrency)
          let keys = ''
          if (Array.isArray(cfg.api_keys) && cfg.api_keys.length) {
            keys = cfg.api_keys.join('\n')
          } else if (cfg.api_key) {
            keys = cfg.api_key
          }
          setLlmApiKeys(keys)
          setLlmConfigured(!!(keys || cfg.api_url))
        }
      } catch (_) {}
      try {
        const sc = await api.fetchStrategyConfig()
        if (sc) {
          const next = emptyStrategy()
          for (const group of strategyGroups) {
            const src = sc[group.key]
            if (src) Object.assign(next[group.key], src)
          }
          setStrategyCfg(next)
        }
      } catch (_) {}
      try {
        const ns = await api.fetchNewsShowAllStatus()
        if (ns && typeof ns.news_show_all === 'boolean') setNewsShowAll(ns.news_show_all)
      } catch (_) {}
    })()
  }, [])

  const renderField = (group, f) => {
    if (f.type === 'switch') {
      return (
        <Switch
          value={!!strategyCfg[group.key][f.k]}
          onChange={(v) => setStrategyField(group.key, f.k, v)}
        />
      )
    }
    return (
      <InputNumber
        value={strategyCfg[group.key][f.k] ?? 0}
        onChange={(v) => setStrategyField(group.key, f.k, v)}
        step={f.step || 1}
        theme="column"
        style={{ width: 200 }}
      />
    )
  }

  return (
    <div className="page">
      <SectionLabel>设置</SectionLabel>

      <Card title="服务器连接" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>服务器地址</span>
          <Input value={serverUrl} onChange={(v) => setServerUrl(v)} placeholder="http://localhost:8080" style={{ width: 280 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>连接状态</span>
          <Tag theme={serverOnline ? 'success' : 'default'} variant="light">
            {serverOnline ? '已连接' : '离线'}
          </Tag>
        </div>
        <Button theme="primary" onClick={saveServer}>保存</Button>
      </Card>

      <Card title="通知设置" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>浏览器通知</span>
          <Button theme="default" variant="outline" onClick={requestNotify}>授权并测试</Button>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>声音提醒</span>
          <Button theme="default" variant="outline" onClick={playTest}>测试声音</Button>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>macOS 通知</span>
          <Tag theme="success" variant="light">后台自动发送</Tag>
        </div>
      </Card>

      <Card title="账户信息" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>账号</span>
          <span style={{ color: '#1a1a1a' }}>{account}</span>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>令牌</span>
          <Tag theme="default" variant="light">
            {token ? token.slice(0, 20) + '...' : '未登录'}
          </Tag>
        </div>
      </Card>

      <Card title="LLM 配置" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>API URL</span>
          <Input value={llmApiUrl} onChange={(v) => setLlmApiUrl(v)} placeholder="https://api.openai.com/v1" style={{ width: 280 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>API Key(s)</span>
          <Textarea value={llmApiKeys} onChange={(v) => setLlmApiKeys(v)} placeholder="sk-...&#10;sk-...（每行一个，多个则轮询分发）" autosize={{ minRows: 4, maxRows: 8 }} style={{ width: 280 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>模型</span>
          <Input value={llmModel} onChange={(v) => setLlmModel(v)} placeholder="gpt-4o-mini" style={{ width: 280 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>分类专用模型</span>
          <Input value={llmClassifierModel} onChange={(v) => setLlmClassifierModel(v)} placeholder="留空则用主模型" style={{ width: 280 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>归因批并发度</span>
          <InputNumber value={llmBatchConcurrency} onChange={(v) => setLlmBatchConcurrency(v)} min={1} max={16} style={{ width: 200 }} />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>状态</span>
          <Tag theme={llmConfigured ? 'success' : 'default'} variant="light">
            {llmConfigured ? '已配置' : '未配置（降级为关键词过滤）'}
          </Tag>
        </div>
        <Button theme="primary" onClick={saveLLM} loading={llmSaving}>保存</Button>
      </Card>

      {strategyGroups.map((group) => (
        <Card key={group.key} title={group.title} style={{ marginBottom: 16 }}>
          {group.fields.map((f) => (
            <div style={rowStyle} key={f.k}>
              <span style={{ ...labelStyle, width: 200 }} title={f.hint || ''}>{f.label}</span>
              {renderField(group, f)}
            </div>
          ))}
        </Card>
      ))}

      <Card title="战法参数" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>说明</span>
          <span className="muted" style={{ fontSize: 12 }}>参数保存后重启后端生效；权重请保持各策略合计 ≤ 1</span>
        </div>
        <Button theme="primary" onClick={saveStrategy} loading={strategySaving}>保存战法参数</Button>
      </Card>

      <Card title="资讯显示" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={{ ...labelStyle, width: 240 }} title="开启后弱档/中性资讯（|score|<0.25）也出现在资讯列表；关闭则仅显示有价值的强事件">显示全部资讯（含弱/中性）</span>
          <Switch
            value={newsShowAll}
            onChange={(v) => { setNewsShowAll(v); toggleNewsShowAll() }}
          />
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>说明</span>
          <span className="muted" style={{ fontSize: 12 }}>该开关即时生效，不影响引擎打分（引擎始终按 |score|≥0.5 过滤）</span>
        </div>
      </Card>

      <Card title="系统" style={{ marginBottom: 16 }}>
        <div style={rowStyle}>
          <span style={labelStyle}>版本</span>
          <span>量仔期货 v1.1.0 桌面版</span>
        </div>
        <div style={rowStyle}>
          <span style={labelStyle}>后端</span>
          <span>Go 1.22+ 单二进制</span>
        </div>
      </Card>
    </div>
  )
}

// 板块小标题
function SectionLabel({ children }) {
  return <div style={{ fontWeight: 600, margin: '8px 0 4px', fontSize: 13 }}>{children}</div>
}
