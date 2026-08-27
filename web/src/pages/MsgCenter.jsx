// ── 消息中心页面 MsgCenter.jsx ──
// 展示所有提醒/告警消息，支持按等级过滤、交易信号二级战法分类、删除/清空、一键模拟卖出
import React, { useState, useEffect, useRef, useMemo } from 'react'
import * as api from '../api/index.js'
import './MsgCenter.css'

const filters = [
  { key: 'all', label: '全部' },
  { key: 'hit', label: '命中提醒' },
  { key: 'trade', label: '交易信号' },
  { key: 'strategy', label: '策略信号' },
  { key: 'stop', label: '止盈止损' },
  { key: 'hold', label: '持仓提示' },
]

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

  // 根据消息等级与方向返回卡片样式类
  function alertClass(a) {
    if (a.level === '止损' || a.level === '策略信号') return 'alert-danger'
    if (a.level === '交易信号') {
      return a.direction === '做空' || a.action === '卖出' ? 'alert-danger' : 'alert-success'
    }
    if (a.level === '止盈' || a.level === '加仓') return 'alert-success'
    if (a.level === '减仓') return 'alert-warn'
    return 'alert-info'
  }

  // 根据等级返回徽章样式类
  function levelClass(level) {
    if (level === '止损' || level === '策略信号') return 'level-danger'
    if (level === '交易信号') return 'level-success'
    if (level === '止盈' || level === '加仓') return 'level-success'
    if (level === '减仓') return 'level-warn'
    return 'level-info'
  }

  // 提取消息对应的建议动作（买入/卖出/持有）
  function actionText(a) {
    if (a.level === '交易信号' || a.level === '策略信号') {
      return (a.action === '卖出') ? '卖出' : '买入'
    }
    return a.title && a.title.includes('卖出') ? '卖出' : (a.title && a.title.includes('买入')) ? '买入' : '持有'
  }

  // 根据动作返回徽章样式类
  function actionClass(a) {
    const t = actionText(a)
    if (t === '买入') return 'action-buy'
    if (t === '卖出') return 'action-sell'
    return 'action-hold'
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
    if (!window.confirm(`删除该消息？\n${a.title || ''}`)) return
    try {
      await api.deleteAlert(a.id)
      load()
    } catch (_) {}
  }

  // 判断消息是否为卖出类提醒
  function isSellAlert(a) {
    return ['清仓', '减仓', '止盈', '止损', '利空抛售'].includes(a.level)
  }

  // 在模拟盘按实时价全仓卖出该消息对应持仓
  async function onPaperSell(a) {
    if (!window.confirm(`模拟卖出 ${a.code} ${a.name || ''}？（按实时价全仓卖出）`)) return
    try {
      await api.sellPaperPosition(a.code, 0)
      window.alert(`${a.code} 模拟卖出成功`)
      load()
    } catch (e) {
      window.alert('模拟卖出失败: ' + (e.message || e))
    }
  }

  // 清空全部消息
  async function onClearAll() {
    if (!window.confirm('确定清空全部消息？(当日已删除的将不再自动出现)')) return
    try {
      await api.clearAlerts()
      load()
    } catch (_) {}
  }

  // 挂载时加载消息、启动轮询并订阅 SSE；卸载时清理
  useEffect(() => {
    load()
    timerRef.current = setInterval(load, 15000)
    api.connectSSE()
    unsubSSERef.current = api.onSSE(handleSSE)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
      if (unsubSSERef.current) { unsubSSERef.current(); unsubSSERef.current = null }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div className="msg-page">
      <div className="page-header">
        <h2>消息中心</h2>
        <div className="header-actions">
          <button className="btn-clear" onClick={onClearAll}>清空全部</button>
        </div>
      </div>
      <div className="filter-row">
        {filters.map((f) => (
          <button key={f.key}
            className={['filter-btn', activeFilter === f.key ? 'active' : ''].join(' ')}
            onClick={() => setActiveFilter(f.key)}>
            {f.label}
          </button>
        ))}
      </div>

      {activeFilter === 'trade' && strategyOptions.length > 0 && (
        <div className="filter-row strategy-row">
          <span className="strategy-label">战法</span>
          <select value={activeStrategy} className="strategy-select" title="按战法策略筛选交易信号"
            onChange={(e) => setActiveStrategy(e.target.value)}>
            <option value="all">全部战法</option>
            {strategyOptions.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </div>
      )}

      <div className="msg-list">
        {filteredAlerts.map((a, i) => (
          <div key={a.id || i} className={['msg-card', alertClass(a)].join(' ')}>
            <div className="msg-header">
              <span className={['badge-level', levelClass(a.level)].join(' ')}>{a.level}</span>
              <span className="msg-stock">{a.code} {a.name}</span>
              <span className="msg-time">{a.time}</span>
              <span className={['badge-action', actionClass(a)].join(' ')}>{actionText(a)}</span>
              {isSellAlert(a) && (
                <button className="btn-paper-sell" title="按实时价在模拟盘卖出该持仓" onClick={() => onPaperSell(a)}>模拟卖出</button>
              )}
              <button className="btn-del" title="删除该消息" onClick={() => onDeleteOne(a)}>✕</button>
            </div>
            <div className="msg-title">{a.title}</div>
            <div className="msg-body">{a.body}</div>
          </div>
        ))}
      </div>

      {filteredAlerts.length === 0 && (
        <div className="empty"><span className="empty-text">暂无消息</span></div>
      )}
    </div>
  )
}
