// ── 消息中心页面 MsgCenter.jsx ──
// 展示所有提醒/告警消息，支持按等级过滤、交易信号二级战法分类、删除/清空、一键模拟卖出
// 使用 TDesign React 组件（Card / Tag / Button / Select / Dialog）。
import React, { useState, useEffect, useRef, useMemo } from 'react'
import { Card, Tag, Button, Select, DialogPlugin, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'

// 通用确认弹窗：返回 Promise<boolean>，确认 resolve(true)、关闭 resolve(false)
function confirmDialog(body, header = '确认') {
  return new Promise((resolve) => {
    const d = DialogPlugin.confirm({
      header,
      body,
      theme: 'warning',
      onConfirm: () => { d.hide(); resolve(true) },
      onClose: () => { d.hide(); resolve(false) },
    })
  })
}

// 消息等级过滤选项：key 对应过滤逻辑，label 为按钮文案
const filters = [
  { key: 'all', label: '全部' },
  { key: 'hit', label: '命中提醒' },
  { key: 'trade', label: '交易信号' },
  { key: 'strategy', label: '策略信号' },
  { key: 'stop', label: '止盈止损' },
  { key: 'hold', label: '持仓提示' },
]

// 涨跌配色（红涨绿跌）— 此处仅用于"收益/亏损"语义外的边框，按 Vue 原色映射
function levelTagTheme(level) {
  if (level === '止损' || level === '策略信号') return 'danger'
  if (level === '交易信号') return 'success'
  if (level === '止盈' || level === '加仓') return 'success'
  if (level === '减仓') return 'warning'
  return 'primary'
}

/**
 * 消息中心页面组件
 * 展示提醒/告警/交易信号，支持等级过滤、战法二级筛选、删除与模拟卖出。
 * @returns {JSX.Element}
 */
export default function MsgCenter() {
  const [alerts, setAlerts] = useState([])
  const [activeFilter, setActiveFilter] = useState('all')
  const [activeStrategy, setActiveStrategy] = useState('all')

  const timerRef = useRef(null)
  const unsubSSERef = useRef(null)

  // SSE 推送到达时刷新消息
  function handleSSE() { load() }

  // 按交易信号中的战法名称统计可选战法
  const strategyOptions = useMemo(() => {
    const cnt = {}
    for (const a of alerts) {
      if (a.level !== '交易信号' || !a.strategy) continue
      cnt[a.strategy] = (cnt[a.strategy] || 0) + 1
    }
    return Object.entries(cnt).sort((x, y) => y[1] - x[1]).map(([k]) => k)
  }, [alerts])

  // 根据等级与战法筛选消息列表
  const filteredAlerts = useMemo(() => {
    let list = alerts
    if (activeFilter === 'hit') list = list.filter(a => a.level === '命中提醒')
    if (activeFilter === 'trade') list = list.filter(a => a.level === '交易信号')
    if (activeFilter === 'strategy') list = list.filter(a => a.level === '策略信号')
    if (activeFilter === 'stop') list = list.filter(a => a.level === '止盈' || a.level === '止损')
    if (activeFilter === 'hold') list = list.filter(a => a.level === '持仓提示')
    if (activeFilter === 'trade' && activeStrategy !== 'all') {
      list = list.filter(a => a.strategy === activeStrategy)
    }
    if (activeFilter !== 'strategy' && activeStrategy !== '预期差') {
      list = list.filter(a => a.strategy !== '预期差')
    }
    return list
  }, [alerts, activeFilter, activeStrategy])

  // 根据消息等级与方向返回卡片左边框颜色
  function alertBorder(a) {
    if (a.level === '止损' || a.level === '策略信号') return '#e34d59'
    if (a.level === '交易信号') {
      return a.direction === '做空' || a.action === '卖出' ? '#e34d59' : '#00a870'
    }
    if (a.level === '止盈' || a.level === '加仓') return '#00a870'
    if (a.level === '减仓') return '#FAAD14'
    return '#4fc3f7'
  }

  // 提取消息对应的建议动作（买入/卖出/持有）
  function actionText(a) {
    if (a.level === '交易信号' || a.level === '策略信号') {
      return (a.action === '卖出') ? '卖出' : '买入'
    }
    return a.title && a.title.includes('卖出') ? '卖出' : (a.title && a.title.includes('买入')) ? '买入' : '持有'
  }

  // 根据建议动作（买入/卖出/持有）返回操作标签的主题色
  function actionTagTheme(a) {
    const t = actionText(a)
    if (t === '买入') return 'success'
    if (t === '卖出') return 'danger'
    return 'default'
  }

  // 加载消息列表并过滤掉日历类消息
  async function load() {
    try {
      const all = await api.fetchAlerts()
      setAlerts((all || []).filter(a => a.code !== 'CAL' && !(a.level && a.level.startsWith('日历'))))
    } catch (_) {}
  }

  // 删除单条消息并刷新
  async function onDeleteOne(a) {
    const ok = await confirmDialog(`删除该消息？\n${a.title || ''}`, '删除消息')
    if (!ok) return
    try {
      await api.deleteAlert(a.id)
      load()
    } catch (_) { MessagePlugin.error('删除失败') }
  }

  // 判断消息是否为卖出类提醒
  function isSellAlert(a) {
    return ['清仓', '减仓', '止盈', '止损', '利空抛售'].includes(a.level)
  }

  // 在模拟盘按实时价全仓卖出该消息对应持仓
  async function onPaperSell(a) {
    const ok = await confirmDialog(`模拟卖出 ${a.code} ${a.name || ''}？（按实时价全仓卖出）`, '模拟卖出')
    if (!ok) return
    try {
      await api.sellPaperPosition(a.code, 0)
      MessagePlugin.success(`${a.code} 模拟卖出成功`)
      load()
    } catch (e) {
      MessagePlugin.error('模拟卖出失败: ' + (e.message || e))
    }
  }

  // 清空全部消息
  async function onClearAll() {
    const ok = await confirmDialog('确定清空全部消息？(当日已删除的将不再自动出现)', '清空全部')
    if (!ok) return
    try {
      await api.clearAlerts()
      load()
    } catch (_) { MessagePlugin.error('清空失败') }
  }

  // 挂载时加载消息、启动轮询并订阅 SSE；卸载时清理
  useEffect(() => {
    load()
    timerRef.current = setInterval(load, 15000) // 每 15s 轮询刷新消息列表
    api.connectSSE()
    unsubSSERef.current = api.onSSE(handleSSE)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
      if (unsubSSERef.current) { unsubSSERef.current(); unsubSSERef.current = null }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="page">
      <Card style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>消息中心</h2>
          <Button theme="danger" variant="outline" size="small" onClick={onClearAll}>清空全部</Button>
        </div>
      </Card>

      <div style={{ display: 'flex', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
        {filters.map((f) => (
          <Button key={f.key} size="small" variant={activeFilter === f.key ? 'base' : 'outline'}
            theme={activeFilter === f.key ? 'primary' : 'default'} onClick={() => setActiveFilter(f.key)}>
            {f.label}
          </Button>
        ))}
      </div>

      {activeFilter === 'trade' && strategyOptions.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
          <span style={{ fontSize: 14, color: '#888' }}>战法</span>
          <Select value={activeStrategy} onChange={(v) => setActiveStrategy(v)} size="small" style={{ width: 200 }}
            options={[{ label: '全部战法', value: 'all' }, ...strategyOptions.map((s) => ({ label: s, value: s }))]} />
        </div>
      )}

      {filteredAlerts.map((a, i) => (
        <Card key={a.id || i} style={{ marginBottom: 8, borderLeft: `4px solid ${alertBorder(a)}` }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 6 }}>
            <Tag theme={levelTagTheme(a.level)} size="small">{a.level}</Tag>
            <span style={{ fontFamily: 'monospace', color: '#4fc3f7', fontWeight: 600 }}>{a.code} {a.name}</span>
            <span style={{ color: '#555', flex: 1, fontSize: 13 }}>{a.time}</span>
            <Tag theme={actionTagTheme(a)} size="small" variant="light">{actionText(a)}</Tag>
            {isSellAlert(a) && (
              <Button size="small" variant="outline" theme="danger" onClick={() => onPaperSell(a)}>模拟卖出</Button>
            )}
            <Button size="small" variant="text" theme="default" onClick={() => onDeleteOne(a)}>✕</Button>
          </div>
          <div style={{ fontSize: 14, color: '#1a1a1a', fontWeight: 600 }}>{a.title}</div>
          <div style={{ fontSize: 13, color: '#999', marginTop: 4 }}>{a.body}</div>
        </Card>
      ))}

      {filteredAlerts.length === 0 && (
        <div style={{ textAlign: 'center', padding: 60, color: '#555' }}>暂无消息</div>
      )}
    </div>
  )
}
