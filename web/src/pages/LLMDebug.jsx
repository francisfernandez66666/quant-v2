// ── LLM 分析诊断页面 LLMDebug.jsx ──
// 展示最新一轮 Stage 流水线结果（Stage1 初筛 / Stage2 事件分析），
// 并内嵌 LogModal 弹窗用于按批次查看 LLM 分析与信号批次日志。
import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
import './LLMDebug.css'

/**
 * 日志弹窗子组件（LLM 分析 / 信号批次）
 * 按轮次展示 Stage 流水线记录，支持跨批次搜索与战法筛选。
 * @param {{visible:boolean, onClose:()=>void}} props
 * @returns {JSX.Element|null}
 */
function LogModal({ visible, onClose }) {
  const [activeTab, setActiveTab] = useState('llm')
  const [loading, setLoading] = useState(false)

  const [llmRecords, setLlmRecords] = useState([])
  const [llmIdx, setLlmIdx] = useState(0)
  const [llmData, setLlmData] = useState(null)
  const [llmNoData, setLlmNoData] = useState(false)
  const [llmQuery, setLlmQuery] = useState('')
  const [selectedSet, setSelectedSet] = useState(new Set())

  const [sigRecords, setSigRecords] = useState([])
  const [sigIdx, setSigIdx] = useState(0)
  const [sigData, setSigData] = useState(null)
  const [sigNoData, setSigNoData] = useState(false)
  const [sigQuery, setSigQuery] = useState('')
  const [activeSigStrategy, setActiveSigStrategy] = useState('all')

  // 格式化时间为 HH:mm:ss
  function fmtTime(t) {
    if (!t) return '-'
    const d = new Date(t)
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  // 判断 Stage1 初筛中第 i 条是否被 LLM 选中
  const isSelected = (i) => selectedSet.has(i)

  // 将当前选中的 LLM 轮次数据应用到展示态
  function applyLLM() {
    const r = llmRecords[llmIdx]
    setLlmData(r || null)
    setLlmNoData(!r)
    setSelectedSet(new Set(r ? r.selected_idx || [] : []))
  }
  // 将当前选中的信号批次数据应用到展示态
  function applySignal() {
    const r = sigRecords[sigIdx]
    setSigData(r || null)
    setSigNoData(!r)
  }

  // 判断文本是否包含查询词（不区分大小写）
  function hasText(text, q) {
    if (!text || !q) return false
    return String(text).toUpperCase().includes(q)
  }
  // 判断事件对象是否命中搜索条件（标题/理由/板块/个股）
  function eventHit(ev, q) {
    if (!ev) return false
    if (hasText(ev.title, q)) return true
    if (hasText(ev.reason, q)) return true
    if (ev.sectors && ev.sectors.some((s) => hasText(s, q))) return true
    if (ev.related_stocks && ev.related_stocks.some((s) => hasText(s, q))) return true
    if (ev.cleaned_stocks && ev.cleaned_stocks.some((s) => hasText(s, q))) return true
    return false
  }
  // 判断信号对象是否命中搜索条件（代码/名称/板块/战法/理由）
  function sigHit(sg, q) {
    if (!sg) return false
    if (hasText(sg.code, q)) return true
    if (hasText(sg.name, q)) return true
    if (hasText(sg.sector, q)) return true
    if (hasText(sg.strategy, q)) return true
    if (hasText(sg.reason, q)) return true
    return false
  }

  const sigStrategyOptions = useMemo(() => {
    const set = new Set()
    for (const r of sigRecords) {
      for (const sg of (r.signals || [])) {
        if (sg.strategy) set.add(sg.strategy)
      }
    }
    return [...set]
  }, [sigRecords])

  const sigFiltered = useMemo(() => {
    const sigs = sigData?.signals || []
    if (activeSigStrategy === 'all') return sigs
    return sigs.filter((sg) => sg.strategy === activeSigStrategy)
  }, [sigData, activeSigStrategy])

  const llmSearching = (llmQuery || '').trim() !== ''
  const sigSearching = (sigQuery || '').trim() !== ''
  const llmSearchGroups = useMemo(() => {
    const q = (llmQuery || '').trim().toUpperCase()
    if (!q) return []
    const groups = []
    for (const r of llmRecords) {
      const items = (r.stage2_events || []).filter((ev) => eventHit(ev, q))
      if (items.length) groups.push({ time: r.process_time, items })
    }
    return groups
  }, [llmRecords, llmQuery])
  const llmTotalHits = llmSearchGroups.reduce((n, g) => n + g.items.length, 0)
  const sigSearchGroups = useMemo(() => {
    const q = (sigQuery || '').trim().toUpperCase()
    if (!q) return []
    const groups = []
    for (const r of sigRecords) {
      const items = (r.signals || []).filter((sg) => sigHit(sg, q) && (activeSigStrategy === 'all' || sg.strategy === activeSigStrategy))
      if (items.length) groups.push({ time: r.process_time, items })
    }
    return groups
  }, [sigRecords, sigQuery, activeSigStrategy])
  const sigTotalHits = sigSearchGroups.reduce((n, g) => n + g.items.length, 0)

  // 加载 Stage 记录与信号批次日志，并更新默认选中项
  const load = useCallback(async () => {
    if (loading) return
    setLoading(true)
    const [srRes, slRes] = await Promise.allSettled([api.fetchStageRecords(), api.fetchSignalLogs()])
    if (srRes.status === 'fulfilled' && Array.isArray(srRes.value) && srRes.value.length) {
      setLlmRecords(srRes.value)
      setLlmIdx(0)
      applyLLM()
    } else {
      setLlmRecords([])
      setLlmData(null)
      setLlmNoData(true)
    }
    if (slRes.status === 'fulfilled' && Array.isArray(slRes.value) && slRes.value.length) {
      setSigRecords(slRes.value)
      setSigIdx(0)
      applySignal()
    } else {
      setSigRecords([])
      setSigData(null)
      setSigNoData(true)
    }
    setLoading(false)
  }, [loading])

  // 切换日志弹窗标签：无数据时自动加载
  function switchTab(t) {
    setActiveTab(t)
    if ((t === 'llm' && llmData) || (t === 'signal' && sigData)) return
    if (!llmRecords.length && !sigRecords.length) load()
  }

  // 弹窗显示时自动加载日志
  useEffect(() => {
    if (visible) load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible])

  if (!visible) return null

  return (
    <div className="log-overlay" onClick={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="log-modal">
        <div className="log-header">
          <span className="log-title">📋 日志</span>
          <button className="log-close" onClick={onClose}>✕</button>
        </div>
        <div className="log-tabs">
          <span className={'log-tab' + (activeTab === 'llm' ? ' active' : '')} onClick={() => switchTab('llm')}>LLM 分析</span>
          <span className={'log-tab' + (activeTab === 'signal' ? ' active' : '')} onClick={() => switchTab('signal')}>信号批次</span>
        </div>

        {activeTab === 'llm' && (
          <div className="log-body">
            <div className="log-toolbar">
              <input
                value={llmQuery}
                onChange={(e) => setLlmQuery(e.target.value)}
                type="text"
                className="log-search"
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
              />
              {!llmSearching && (
                <select
                  value={llmIdx}
                  disabled={llmRecords.length < 2}
                  onChange={(e) => { setLlmIdx(Number(e.target.value)); applyLLM() }}
                  className="log-select"
                >
                  {llmRecords.map((r, i) => (
                    <option key={r.process_time} value={i}>
                      轮次 {llmRecords.length - i} · {fmtTime(r.process_time)}（{r.raw_count} 条 / 选 {r.selected_count}）
                    </option>
                  ))}
                </select>
              )}
              <button className="btn-refresh" onClick={load} disabled={loading}>刷新</button>
            </div>

            {llmSearching ? (
              <div className="search-view">
                {!llmSearchGroups.length ? (
                  <div className="log-empty">未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  <>
                    <div className="search-summary">共 {llmTotalHits} 条事件命中，跨 {llmSearchGroups.length} 个轮次</div>
                    {llmSearchGroups.map((g, gi) => (
                      <div key={gi} className="search-group">
                        <div className="search-group-head">
                          <span className="search-batch">轮次 {fmtTime(g.time)}</span>
                          <span className="search-count">命中 {g.items.length} 条</span>
                        </div>
                        {g.items.map((ev, i) => (
                          <div key={i} className="event-card">
                            <div className="event-header">
                              <span className="event-title">{ev.title}</span>
                              <span className={'tag tag-' + (ev.direction || '中性')}>{ev.direction || '中性'}</span>
                              <span className="event-score">评分 {Number(ev.score || 0).toFixed(2)}</span>
                            </div>
                            <div className="event-body">
                              {ev.sectors && ev.sectors.length > 0 && (
                                <div className="event-row">
                                  <span className="event-label">板块</span>
                                  <span className="event-tags">
                                    {ev.sectors.map((s) => <span key={s} className="mini-tag sector">{s}</span>)}
                                  </span>
                                </div>
                              )}
                              {ev.related_stocks && ev.related_stocks.length > 0 && (
                                <div className="event-row">
                                  <span className="event-label">个股</span>
                                  <span className="event-tags">
                                    {ev.related_stocks.map((s) => <span key={s} className="mini-tag stock">{s}</span>)}
                                  </span>
                                </div>
                              )}
                              {ev.reason && (
                                <div className="event-row">
                                  <span className="event-label">理由</span>
                                  <span className="event-reason">{ev.reason}</span>
                                </div>
                              )}
                            </div>
                          </div>
                        ))}
                      </div>
                    ))}
                  </>
                )}
              </div>
            ) : (
              <>
                {llmNoData && <div className="log-empty">暂无 LLM 分析记录，等待下一轮扫描</div>}
                {llmData && (
                  <>
                    <div className="summary-bar">
                      <div className="summary-item">
                        <span className="summary-label">Stage1 模式</span>
                        <span className={'summary-value ' + (llmData.stage1_mode === 'llm' ? 'tag-llm' : 'tag-keyword')}>
                          {llmData.stage1_mode === 'llm' ? 'LLM' : '关键词'}
                        </span>
                      </div>
                      <div className="summary-item"><span className="summary-label">原始条数</span><span className="summary-value">{llmData.raw_count}</span></div>
                      <div className="summary-item"><span className="summary-label">筛选后</span><span className="summary-value">{llmData.selected_count}</span></div>
                      <div className="summary-item"><span className="summary-label">分析时间</span><span className="summary-value">{fmtTime(llmData.process_time)}</span></div>
                    </div>

                    <h3 className="section-title">Stage1 · 新闻初筛</h3>
                    <div className="stage1-list">
                      {(llmData.raw_titles || []).map((title, i) => (
                        <div key={i} className={'title-item ' + (isSelected(i) ? 'selected' : 'discarded')}>
                          <span className="title-idx">{i + 1}</span>
                          <span className="title-text">{title}</span>
                          <span className={'title-badge ' + (isSelected(i) ? 'badge-pass' : 'badge-skip')}>
                            {isSelected(i) ? '通过' : '过滤'}
                          </span>
                        </div>
                      ))}
                    </div>

                    <h3 className="section-title">Stage2 · LLM 分析结果</h3>
                    {llmData.stage2_events && llmData.stage2_events.length > 0 ? (
                      <div className="stage2-events">
                        {llmData.stage2_events.map((ev, i) => (
                          <div key={i} className="event-card">
                            <div className="event-header">
                              <span className="event-title">{ev.title}</span>
                              <span className={'tag tag-' + (ev.direction || '中性')}>{ev.direction || '中性'}</span>
                              <span className="event-score">评分 {Number(ev.score || 0).toFixed(2)}</span>
                            </div>
                            <div className="event-body">
                              {ev.sectors && ev.sectors.length > 0 && (
                                <div className="event-row">
                                  <span className="event-label">板块</span>
                                  <span className="event-tags">
                                    {ev.sectors.map((s) => <span key={s} className="mini-tag sector">{s}</span>)}
                                  </span>
                                </div>
                              )}
                              {ev.related_stocks && ev.related_stocks.length > 0 && (
                                <div className="event-row">
                                  <span className="event-label">个股</span>
                                  <span className="event-tags">
                                    {ev.related_stocks.map((s) => <span key={s} className="mini-tag stock">{s}</span>)}
                                  </span>
                                </div>
                              )}
                              {ev.reason && (
                                <div className="event-row">
                                  <span className="event-label">理由</span>
                                  <span className="event-reason">{ev.reason}</span>
                                </div>
                              )}
                            </div>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <div className="log-empty">Stage2 无分析结果</div>
                    )}
                  </>
                )}
              </>
            )}
          </div>
        )}

        {activeTab === 'signal' && (
          <div className="log-body">
            <div className="log-toolbar">
              <input
                value={sigQuery}
                onChange={(e) => setSigQuery(e.target.value)}
                type="text"
                className="log-search"
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
              />
              <select
                value={activeSigStrategy}
                onChange={(e) => setActiveSigStrategy(e.target.value)}
                className="log-strategy-select"
                title="按战法策略筛选"
              >
                <option value="all">全部战法</option>
                {sigStrategyOptions.map((st) => <option key={st} value={st}>{st}</option>)}
              </select>
              {!sigSearching && (
                <select
                  value={sigIdx}
                  disabled={sigRecords.length < 2}
                  onChange={(e) => { setSigIdx(Number(e.target.value)); applySignal() }}
                  className="log-select"
                >
                  {sigRecords.map((r, i) => (
                    <option key={r.process_time} value={i}>
                      批次 {sigRecords.length - i} · {fmtTime(r.process_time)}（{r.signals.length} 信号 / {r.raw_count} 条）
                    </option>
                  ))}
                </select>
              )}
              <button className="btn-refresh" onClick={load} disabled={loading}>刷新</button>
            </div>

            {sigSearching ? (
              <div className="search-view">
                {!sigSearchGroups.length ? (
                  <div className="log-empty">未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  <>
                    <div className="search-summary">共 {sigTotalHits} 条信号命中，跨 {sigSearchGroups.length} 个批次</div>
                    {sigSearchGroups.map((g, gi) => (
                      <div key={gi} className="search-group">
                        <div className="search-group-head">
                          <span className="search-batch">批次 {fmtTime(g.time)}</span>
                          <span className="search-count">命中 {g.items.length} 条</span>
                        </div>
                        {g.items.map((sg, i) => (
                          <div key={i} className="signal-item">
                            <div className="sig-head">
                              <span className="sig-code">{sg.code}</span>
                              <span className="sig-name">{sg.name || '-'}</span>
                              <span className="sig-strategy">{sg.strategy || '-'}</span>
                              <span className={'tag dir-' + (sg.direction || '中性')}>{sg.direction || '中性'}</span>
                              <span className={'tag act-' + (sg.action || '-')}>{sg.action || '-'}</span>
                              <span className="sig-conf">置信 {Number(sg.confidence || 0).toFixed(2)}</span>
                              {sg.price && <span className="sig-price">¥{Number(sg.price).toFixed(2)}</span>}
                            </div>
                            <div className="sig-body">
                              {sg.sector && <span className="sig-sector">{sg.sector}</span>}
                              {sg.reason && <span className="sig-reason">{sg.reason}</span>}
                            </div>
                          </div>
                        ))}
                      </div>
                    ))}
                  </>
                )}
              </div>
            ) : (
              <>
                {sigNoData && <div className="log-empty">暂无信号批次记录，等待下一轮扫描</div>}
                {sigData && (
                  <>
                    <div className="summary-bar">
                      <div className="summary-item"><span className="summary-label">批次时间</span><span className="summary-value">{fmtTime(sigData.process_time)}</span></div>
                      <div className="summary-item"><span className="summary-label">原始条数</span><span className="summary-value">{sigData.raw_count}</span></div>
                      <div className="summary-item"><span className="summary-label">信号数</span><span className="summary-value">{sigData.signals.length}</span></div>
                    </div>
                    {sigFiltered.length > 0 ? (
                      <div className="signal-list">
                        {sigFiltered.map((sg, i) => (
                          <div key={sg.id || i} className="signal-item">
                            <div className="sig-head">
                              <span className="sig-code">{sg.code}</span>
                              <span className="sig-name">{sg.name || '-'}</span>
                              <span className="sig-strategy">{sg.strategy || '-'}</span>
                              <span className={'tag dir-' + (sg.direction || '中性')}>{sg.direction || '中性'}</span>
                              <span className={'tag act-' + (sg.action || '-')}>{sg.action || '-'}</span>
                              <span className="sig-conf">置信 {Number(sg.confidence || 0).toFixed(2)}</span>
                              {sg.price && <span className="sig-price">¥{Number(sg.price).toFixed(2)}</span>}
                            </div>
                            <div className="sig-body">
                              {sg.sector && <span className="sig-sector">{sg.sector}</span>}
                              {sg.reason && <span className="sig-reason">{sg.reason}</span>}
                            </div>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <div className="log-empty">{sigData.signals.length ? '当前战法无匹配信号' : '本轮无信号产出'}</div>
                    )}
                  </>
                )}
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * LLM 分析诊断页面组件
 * 拉取最新 Stage 流水线记录并展示 Stage1 初筛、Stage2 事件分析详情。
 * @returns {JSX.Element}
 */
export default function LLMDebug() {
  const [loading, setLoading] = useState(false)
  const [records, setRecords] = useState([])
  const [data, setData] = useState(null)
  const [noAgent, setNoAgent] = useState(false)
  const [noData, setNoData] = useState(false)
  const [showLog, setShowLog] = useState(false)

  const [selectedSet, setSelectedSet] = useState(new Set())

  const isSelected = (i) => selectedSet.has(i)

  // 取最新一轮 Stage 记录更新页面展示态
  function applyLatest() {
    const r = records[0] || null
    setData(r)
    setNoAgent(false)
    setNoData(!r)
    setSelectedSet(new Set(r ? r.selected_idx || [] : []))
  }

  // 格式化时间为 HH:mm:ss
  function formatTime(t) {
    if (!t) return '-'
    const d = new Date(t)
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  // 拉取 Stage 记录并处理 Agent 未就绪、无数据、正常数据三种状态
  async function loadData() {
    setLoading(true)
    try {
      const res = await api.fetchStageRecords()
      if (res && res.status === 'no_engine') {
        setNoAgent(true)
        setNoData(false)
        setRecords([])
        setData(null)
      } else if (!Array.isArray(res) || res.length === 0) {
        setNoData(true)
        setNoAgent(false)
        setRecords([])
        setData(null)
      } else {
        setRecords(res)
        applyLatest()
      }
    } catch (e) {
      console.error('LLMDebug 加载失败', e)
    } finally {
      setLoading(false)
    }
  }

  // 页面挂载时加载最新 Stage 记录
  useEffect(() => { loadData() }, [])

  return (
    <div className="llm-debug-page">
      <div className="page-header">
        <h2>LLM 分析诊断</h2>
        <div className="header-right">
          <button className="btn-log" onClick={() => setShowLog(true)}>📋 日志</button>
          <button className="btn-refresh" onClick={loadData} disabled={loading}>
            {loading ? '加载中...' : '刷新'}
          </button>
        </div>
      </div>
      <LogModal visible={showLog} onClose={() => setShowLog(false)} />

      {noAgent && <div className="empty">Agent 未就绪</div>}
      {!noAgent && noData && <div className="empty">暂无数据，等待下一轮扫描</div>}
      {data && (
        <>
          <div className="summary-bar">
            <div className="summary-item">
              <span className="summary-label">Stage1 模式</span>
              <span className={'summary-value tag-' + data.stage1_mode}>{data.stage1_mode === 'llm' ? 'LLM' : '关键词'}</span>
            </div>
            <div className="summary-item"><span className="summary-label">原始条数</span><span className="summary-value">{data.raw_count}</span></div>
            <div className="summary-item"><span className="summary-label">筛选后</span><span className="summary-value">{data.selected_count}</span></div>
            <div className="summary-item"><span className="summary-label">分析时间</span><span className="summary-value">{formatTime(data.process_time)}</span></div>
          </div>

          <h3 className="section-title">Stage1 · 新闻初筛</h3>
          <div className="stage1-list">
            {(data.raw_titles || []).map((title, i) => (
              <div key={i} className={'title-item ' + (isSelected(i) ? 'selected' : 'discarded')}>
                <span className="title-idx">{i + 1}</span>
                <span className="title-text">{title}</span>
                <span className={'title-badge ' + (isSelected(i) ? 'badge-pass' : 'badge-skip')}>
                  {isSelected(i) ? '通过' : '过滤'}
                </span>
              </div>
            ))}
          </div>

          <h3 className="section-title">Stage2 · LLM 分析结果</h3>
          {data.stage2_events && data.stage2_events.length > 0 ? (
            <div className="stage2-events">
              {data.stage2_events.map((ev, i) => (
                <div key={i} className="event-card">
                  <div className="event-header">
                    <span className="event-title">{ev.title}</span>
                    <span className={'tag tag-' + (ev.direction || '中性')}>{ev.direction || '中性'}</span>
                    <span className="event-score">评分 {Number(ev.score || 0).toFixed(2)}</span>
                  </div>
                  <div className="event-body">
                    {ev.sectors && ev.sectors.length > 0 && (
                      <div className="event-row">
                        <span className="event-label">板块</span>
                        <span className="event-tags">
                          {ev.sectors.map((s) => <span key={s} className="mini-tag sector">{s}</span>)}
                        </span>
                      </div>
                    )}
                    {ev.related_stocks && ev.related_stocks.length > 0 && (
                      <div className="event-row">
                        <span className="event-label">个股</span>
                        <span className="event-tags">
                          {ev.related_stocks.map((s) => <span key={s} className="mini-tag stock">{s}</span>)}
                        </span>
                      </div>
                    )}
                    {ev.upstream_sectors && ev.upstream_sectors.length > 0 && (
                      <div className="event-row">
                        <span className="event-label">上游</span>
                        <span className="event-tags">
                          {ev.upstream_sectors.map((s) => <span key={s} className="mini-tag sector">{s}</span>)}
                        </span>
                      </div>
                    )}
                    {ev.downstream_sectors && ev.downstream_sectors.length > 0 && (
                      <div className="event-row">
                        <span className="event-label">下游</span>
                        <span className="event-tags">
                          {ev.downstream_sectors.map((s) => <span key={s} className="mini-tag sector">{s}</span>)}
                        </span>
                      </div>
                    )}
                    {ev.impact_level && (
                      <div className="event-row">
                        <span className="event-label">影响</span>
                        <span className={'mini-tag impact-' + ev.impact_level}>{ev.impact_level}</span>
                      </div>
                    )}
                    {ev.event_type && (
                      <div className="event-row">
                        <span className="event-label">类型</span>
                        <span className="mini-tag">{ev.event_type}</span>
                      </div>
                    )}
                    {ev.reason && (
                      <div className="event-row">
                        <span className="event-label">理由</span>
                        <span className="event-reason">{ev.reason}</span>
                      </div>
                    )}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <div className="empty">Stage2 无分析结果</div>
          )}
        </>
      )}
    </div>
  )
}
