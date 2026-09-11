// ── 日志弹窗组件 LogModal.jsx ──
// 按批次展示 LLM 分析与信号批次两类日志，支持跨批次搜索与战法筛选。
import React, { useState, useEffect, useMemo, useRef } from 'react'
import * as api from '../api/index.js'
import './LogModal.css'

/**
 * 将时间戳格式化为本地 "HH:mm:ss"，用于下拉选项与概要栏。
 * @param {number|string} t 时间戳或日期字符串
 * @returns {string} 格式化时间或 "-"
 */
// 将时间格式化为 HH:mm:ss，用于下拉选项与概要栏
function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

/**
 * 大小写不敏感地判断文本是否包含关键词（空白关键词视为不匹配）。
 * @param {string} text 待检索文本
 * @param {string} q 关键词
 * @returns {boolean}
 */
function hasText(text, q) {
  if (!text || !q) return false
  return String(text).toUpperCase().includes(q)
}

/**
 * 判断一条 LLM 事件是否命中搜索关键词（标题/理由/板块/关联股/清洗股）。
 * @param {object} ev Stage2 事件对象
 * @param {string} q 关键词
 * @returns {boolean}
 */
function eventHit(ev, q) {
  if (!ev) return false
  if (hasText(ev.title, q)) return true
  if (hasText(ev.reason, q)) return true
  if (ev.sectors && ev.sectors.some((s) => hasText(s, q))) return true
  if (ev.related_stocks && ev.related_stocks.some((s) => hasText(s, q))) return true
  if (ev.cleaned_stocks && ev.cleaned_stocks.some((s) => hasText(s, q))) return true
  return false
}

/**
 * 判断一条信号是否命中搜索关键词（代码/名称/板块/战法/理由）。
 * @param {object} sg 信号对象
 * @param {string} q 关键词
 * @returns {boolean}
 */
function sigHit(sg, q) {
  if (!sg) return false
  if (hasText(sg.code, q)) return true
  if (hasText(sg.name, q)) return true
  if (hasText(sg.sector, q)) return true
  if (hasText(sg.strategy, q)) return true
  if (hasText(sg.reason, q)) return true
  return false
}

// 日志弹窗：按批次展示 LLM 分析 / 信号批次两类日志，支持跨批次搜索
export default function LogModal({ visible, onClose }) {
  const [activeTab, setActiveTab] = useState('llm')
  const [loading, setLoading] = useState(false)

  const [llmRecords, setLlmRecords] = useState([])
  const [llmIdx, setLlmIdx] = useState(0)
  const [llmData, setLlmData] = useState(null)
  const [llmNoData, setLlmNoData] = useState(false)
  const [llmQuery, setLlmQuery] = useState('')
  const [selectedSet, setSelectedSet] = useState(() => new Set())

  const [sigRecords, setSigRecords] = useState([])
  const [sigIdx, setSigIdx] = useState(0)
  const [sigData, setSigData] = useState(null)
  const [sigNoData, setSigNoData] = useState(false)
  const [sigQuery, setSigQuery] = useState('')
  const [activeSigStrategy, setActiveSigStrategy] = useState('all')

  const firstLoad = useRef(false) // 是否已完成首次加载（避免重复请求）

  function applyLLM() {
    // 应用当前选中的 LLM 调试记录：写入数据并回填选中索引集合
    const r = llmRecords[llmIdx]
    setLlmData(r || null)
    setLlmNoData(!r)
    setSelectedSet(new Set(r ? r.selected_idx || [] : []))
  }

  function applySignal() {
    // 应用当前选中的信号调试记录
    const r = sigRecords[sigIdx]
    setSigData(r || null)
    setSigNoData(!r)
  }

  function isSelected(i) {
    // 判断某条记录是否在选中集合中（用于高亮已选调试记录）
    return selectedSet.has(i)
  }

  function sigMatchStrategy(sg) {
    // 信号是否匹配当前筛选的战法（all 表示全部匹配）
    if (!sg) return false
    if (activeSigStrategy === 'all') return true
    return sg.strategy === activeSigStrategy
  }

  const llmSearching = useMemo(() => (llmQuery || '').trim() !== '', [llmQuery]) // LLM 搜索框是否有输入
  const sigSearching = useMemo(() => (sigQuery || '').trim() !== '', [sigQuery]) // 信号搜索框是否有输入

  const sigStrategyOptions = useMemo(() => {
    // 汇总全部信号记录中出现过的战法列表（用于筛选下拉）
    const set = new Set()
    for (const r of sigRecords) {
      for (const sg of (r.signals || [])) {
        if (sg.strategy) set.add(sg.strategy)
      }
    }
    return [...set]
  }, [sigRecords])

  const sigFiltered = useMemo(() => {
    // 按当前战法筛选过滤信号数据
    const sigs = sigData?.signals || []
    if (activeSigStrategy === 'all') return sigs
    return sigs.filter((sg) => sg.strategy === activeSigStrategy)
  }, [sigData, activeSigStrategy])

  const llmSearchGroups = useMemo(() => {
    // 按关键字检索 LLM 阶段事件，按处理批次分组返回
    const q = (llmQuery || '').trim().toUpperCase()
    if (!q) return []
    const groups = []
    for (const r of llmRecords) {
      // 过滤命中关键字的本批次 LLM 阶段事件行
      const items = (r.stage2_events || []).filter((ev) => eventHit(ev, q))
      if (items.length) groups.push({ time: r.process_time, items })
    }
    return groups
  }, [llmRecords, llmQuery])

  const llmTotalHits = useMemo(
    // LLM 检索命中总数（全部批次累加）
    () => llmSearchGroups.reduce((n, g) => n + g.items.length, 0),
    [llmSearchGroups]
  )

  const sigSearchGroups = useMemo(() => {
    // 按关键字 + 战法筛选检索信号记录，按处理批次分组返回
    const q = (sigQuery || '').trim().toUpperCase()
    if (!q) return []
    const groups = []
    for (const r of sigRecords) {
      // 过滤命中关键字且战法筛选通过的本批次信号行
      const items = (r.signals || []).filter((sg) => sigHit(sg, q) && sigMatchStrategy(sg))
      if (items.length) groups.push({ time: r.process_time, items })
    }
    return groups
  }, [sigRecords, sigQuery, activeSigStrategy])

  const sigTotalHits = useMemo(
    // 信号检索命中总数（全部批次累加）
    () => sigSearchGroups.reduce((n, g) => n + g.items.length, 0),
    [sigSearchGroups]
  )

  // 切换标签页：若目标标签尚无数据且两类记录都为空，则触发一次加载
  function switchTab(t) {
    setActiveTab(t)
    if ((t === 'llm' && llmData) || (t === 'signal' && sigData)) return
    if (!llmRecords.length && !sigRecords.length) load()
  }

  /**
   * 并行拉取 LLM 分析记录与信号批次日志（任一失败不影响另一部分展示）。
   * 成功时回填对应 state；全部为空时标记 NoData。
   * @returns {Promise<void>}
   */
  async function load() {
    if (loading) return
    setLoading(true)
    // 并发请求两类日志，使用 allSettled 避免一方失败阻断另一方
    const [srRes, slRes] = await Promise.allSettled([api.fetchStageRecords(), api.fetchSignalLogs()])
    if (srRes.status === 'fulfilled' && Array.isArray(srRes.value) && srRes.value.length) {
      setLlmRecords(srRes.value)
      setLlmIdx(0)
      const r = srRes.value[0]
      setLlmData(r || null)
      setLlmNoData(!r)
      setSelectedSet(new Set(r ? r.selected_idx || [] : []))
    } else {
      // §FIX-0921b 主源失败/为空时回落 /api/llm-debug（最新单轮快照）——
      // 即使本轮没有分析出有价值结果也如实展示 L1/L2，避免「暂无 LLM 分析记录」白板
      let fb = null
      try {
        // §FIX-0921b 回退数据校验：仅接受无 status 错误标记且含有效内容的快照
        const d = await api.fetchLLMDebug()
        if (d && !d.status && (d.raw_titles || d.stage2_events)) {
          fb = d
        }
      } catch (_) {}
      // 回落命中：包装为单条记录展示，并同步回填选中索引集合
      if (fb) {
        setLlmRecords([fb])
        setLlmIdx(0)
        setLlmData(fb)
        setLlmNoData(false)
        setSelectedSet(new Set(fb.selected_idx || []))
      } else {
        // 主源与回落均无可用数据：清空并标记 NoData，界面显示等待下一轮扫描
        setLlmRecords([])
        setLlmData(null)
        setLlmNoData(true)
      }
    }
    // 信号批次主源结果回填；为空时清空数据并标记 NoData
    if (slRes.status === 'fulfilled' && Array.isArray(slRes.value) && slRes.value.length) {
      setSigRecords(slRes.value)
      setSigIdx(0)
      const r = slRes.value[0]
      setSigData(r || null)
      setSigNoData(!r)
    } else {
      setSigRecords([])
      setSigData(null)
      setSigNoData(true)
    }
    setLoading(false)
  }

  // 弹窗每次变为可见时重新拉取最新日志
  useEffect(() => {
    if (visible) load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible])

  // 首次挂载且可见时拉取一次（与上面的 visible 监听互补，保证初始渲染即加载）
  useEffect(() => {
    if (visible && !firstLoad.current) {
      firstLoad.current = true
      load()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (!visible) return null

  return (
    <div className="log-overlay" onClick={onClose}>
      <div className="log-modal" onClick={(e) => e.stopPropagation()}>
        <div className="log-header">
          <span className="log-title">📋 日志</span>
          <button className="log-close" onClick={onClose}>✕</button>
        </div>

        <div className="log-tabs">
          <span
            className={'log-tab' + (activeTab === 'llm' ? ' active' : '')}
            onClick={() => switchTab('llm')}
          >LLM 分析</span>
          <span
            className={'log-tab' + (activeTab === 'signal' ? ' active' : '')}
            onClick={() => switchTab('signal')}
          >信号批次</span>
        </div>

        {activeTab === 'llm' && (
          // LLM 分析标签页：工具栏 + 搜索视图（跨批次）/ 单轮详情（概要栏 + Stage1/Stage2）
          <div className="log-body">
            {
              // 工具栏：搜索框 + 轮次下拉（搜索时隐藏）+ 刷新按钮
            }
            <div className="log-toolbar">
              {
                // 搜索框：输入即切换为跨批次搜索视图（大小写不敏感）
              }
              <input
                value={llmQuery}
                onChange={(e) => setLlmQuery(e.target.value)}
                type="text"
                className="log-search"
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
              />
              {
                // 轮次下拉：按处理时间倒序展示各轮 LLM 分析（搜索态隐藏）
              }
              {!llmSearching && (
                <select
                  value={llmIdx}
                  disabled={llmRecords.length < 2}
                  // 切换轮次：更新选中索引并回填该轮数据与选中集合
                  onChange={(e) => {
                    const i = Number(e.target.value)
                    setLlmIdx(i)
                    const r = llmRecords[i]
                    setLlmData(r || null)
                    setLlmNoData(!r)
                    setSelectedSet(new Set(r ? r.selected_idx || [] : []))
                  }}
                  className="log-select"
                >
                  {
                    // 下拉选项：轮次序号（最新在前）+ 分析时间 + 原始/选中条数
                  }
                  {llmRecords.map((r, i) => (
                    <option key={r.process_time} value={i}>
                      轮次 {llmRecords.length - i} · {fmtTime(r.process_time)}（{r.raw_count} 条 / 选 {r.selected_count}）
                    </option>
                  ))}
                </select>
              )}
              {
                // 手动刷新：重新拉取 LLM 分析与信号批次全部日志
              }
              <button className="btn-refresh" onClick={load} disabled={loading}>刷新</button>
            </div>

            {
              // 跨批次搜索视图：命中概要 + 按轮次分组的事件卡片
            }
            {llmSearching ? (
              <div className="search-view">
                {
                  // 无命中提示 / 命中概要（总条数与轮次数）
                }
                {!llmSearchGroups.length ? (
                  <div className="log-empty">未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  <div className="search-summary">
                    共 {llmTotalHits} 条事件命中，跨 {llmSearchGroups.length} 个轮次
                  </div>
                )}
                {
                  // 按轮次分组渲染命中的 Stage2 事件卡片
                }
                {llmSearchGroups.map((g, gi) => (
                  <div key={gi} className="search-group">
                    {
                      // 分组头：轮次时间 + 该轮命中条数
                    }
                    <div className="search-group-head">
                      <span className="search-batch">轮次 {fmtTime(g.time)}</span>
                      <span className="search-count">命中 {g.items.length} 条</span>
                    </div>
                    {
                      // 事件卡片：标题 + 多空方向标签 + 评分；正文按板块/个股/理由分行
                    }
                    {g.items.map((ev, i) => (
                      <div key={i} className="event-card">
                        {
                          // 卡片头：事件标题 + 方向标签 + 评分
                        }
                        <div className="event-header">
                          <span className="event-title">{ev.title}</span>
                          <span className={'tag tag-' + ev.direction}>{ev.direction || '中性'}</span>
                          <span className="event-score">评分 {(ev.score || 0).toFixed(2)}</span>
                        </div>
                        {
                          // 详情行：命中板块标签（有值才渲染）
                        }
                        <div className="event-body">
                          {ev.sectors && ev.sectors.length && (
                            <div className="event-row">
                              <span className="event-label">板块</span>
                              <span className="event-tags">
                                {ev.sectors.map((s) => (
                                  <span key={s} className="mini-tag sector">{s}</span>
                                ))}
                              </span>
                            </div>
                          )}
                          {
                            // 关联个股标签（命中事件的相关个股）
                          }
                          {ev.related_stocks && ev.related_stocks.length && (
                            <div className="event-row">
                              <span className="event-label">个股</span>
                              <span className="event-tags">
                                {ev.related_stocks.map((s) => (
                                  <span key={s} className="mini-tag stock">{s}</span>
                                ))}
                              </span>
                            </div>
                          )}
                          {
                            // LLM 分析理由文本
                          }
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
              </div>
            /* 单轮详情视图：无数据提示 / 概要栏 + Stage1 初筛 + Stage2 分析结果 */
            ) : (
              <>
                {/* 无数据：本轮尚无 LLM 分析记录 */}
                {llmNoData ? (
                  <div className="log-empty">暂无 LLM 分析记录，等待下一轮扫描</div>
                ) : llmData ? (
                  <>
                    {
                      // 概要栏：Stage1 模式 / 原始条数 / 筛选后条数 / 分析时间
                    }
                    <div className="summary-bar">
                      <div className="summary-item">
                        <span className="summary-label">Stage1 模式</span>
                        <span className={'summary-value ' + (llmData.stage1_mode === 'llm' ? 'tag-llm' : 'tag-keyword')}>
                          {llmData.stage1_mode === 'llm' ? 'LLM' : '关键词'}
                        </span>
                      </div>
                      <div className="summary-item">
                        <span className="summary-label">原始条数</span>
                        <span className="summary-value">{llmData.raw_count}</span>
                      </div>
                      {
                        // 概要项：筛选后保留的标题条数
                      }
                      <div className="summary-item">
                        <span className="summary-label">筛选后</span>
                        <span className="summary-value">{llmData.selected_count}</span>
                      </div>
                      {
                        // 概要项：本轮分析完成时间
                      }
                      <div className="summary-item">
                        <span className="summary-label">分析时间</span>
                        <span className="summary-value">{fmtTime(llmData.process_time)}</span>
                      </div>
                    </div>

                    {
                      // Stage1 新闻初筛列表：原始标题逐条展示，选中=通过、未选=过滤
                    }
                    <h3 className="section-title">Stage1 · 新闻初筛</h3>
                    <div className="stage1-list">
                      {(llmData.raw_titles || []).map((title, i) => (
                        <div
                          key={i}
                          className={'title-item ' + (isSelected(i) ? 'selected' : 'discarded')}
                        >
                          <span className="title-idx">{i + 1}</span>
                          <span className="title-text">{title}</span>
                          <span className={'title-badge ' + (isSelected(i) ? 'badge-pass' : 'badge-skip')}>
                            {
                              // 徽标文案：命中选中集=通过，否则=被初筛过滤
                            }
                            {isSelected(i) ? '通过' : '过滤'}
                          </span>
                        </div>
                      ))}
                    </div>

                    {
                      // Stage2 LLM 分析结果：事件卡片列表（结构与搜索视图一致），无结果给空态
                    }
                    <h3 className="section-title">Stage2 · LLM 分析结果</h3>
                    {llmData.stage2_events && llmData.stage2_events.length ? (
                      <div className="stage2-events">
                        {llmData.stage2_events.map((ev, i) => (
                          <div key={i} className="event-card">
                            <div className="event-header">
                              <span className="event-title">{ev.title}</span>
                              <span className={'tag tag-' + ev.direction}>{ev.direction || '中性'}</span>
                              <span className="event-score">评分 {(ev.score || 0).toFixed(2)}</span>
                            </div>
                            {
                              // 卡片正文：板块 / 个股 / 理由（有值才渲染对应行）
                            }
                            <div className="event-body">
                              {ev.sectors && ev.sectors.length && (
                                <div className="event-row">
                                  <span className="event-label">板块</span>
                                  <span className="event-tags">
                                    {ev.sectors.map((s) => (
                                      <span key={s} className="mini-tag sector">{s}</span>
                                    ))}
                                  </span>
                                </div>
                              )}
                              {
                                // 关联个股标签
                              }
                              {ev.related_stocks && ev.related_stocks.length && (
                                <div className="event-row">
                                  <span className="event-label">个股</span>
                                  <span className="event-tags">
                                    {ev.related_stocks.map((s) => (
                                      <span key={s} className="mini-tag stock">{s}</span>
                                    ))}
                                  </span>
                                </div>
                              )}
                              {
                                // 入选理由文本
                              }
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
                      /* 空态：本轮 Stage2 尚无分析结果 */
                      <div className="log-empty">Stage2 无分析结果</div>
                    )}
                  </>
                ) : null}
              </>
            )}
          </div>
        )}

        {
          // 信号批次标签页：工具栏 + 搜索视图（跨批次）/ 单批次详情（概要栏 + 信号列表）
        }
        {activeTab === 'signal' && (
          <div className="log-body">
            {
              // 工具栏：搜索框 + 战法筛选下拉 + 批次下拉（搜索时隐藏）+ 刷新按钮
            }
            <div className="log-toolbar">
              {
                // 搜索框：输入即切换为跨批次信号搜索视图
              }
              <input
                value={sigQuery}
                onChange={(e) => setSigQuery(e.target.value)}
                type="text"
                className="log-search"
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
              />
              {
                // 战法筛选下拉：全部战法 / 各战法（选项来自全部批次信号去重汇总）
              }
              <select
                value={activeSigStrategy}
                onChange={(e) => setActiveSigStrategy(e.target.value)}
                className="log-strategy-select"
                title="按战法策略筛选"
              >
                <option value="all">全部战法</option>
                {sigStrategyOptions.map((st) => (
                  <option key={st} value={st}>{st}</option>
                ))}
              </select>
              {
                // 批次下拉：按处理时间倒序展示各信号批次（搜索态隐藏）
              }
              {!sigSearching && (
                <select
                  value={sigIdx}
                  disabled={sigRecords.length < 2}
                  // 切换批次：更新选中索引并回填该批次的信号数据
                  onChange={(e) => {
                    const i = Number(e.target.value)
                    setSigIdx(i)
                    const r = sigRecords[i]
                    setSigData(r || null)
                    setSigNoData(!r)
                  }}
                  className="log-select"
                >
                  {
                    // 下拉选项：批次序号（最新在前）+ 处理时间 + 信号/原始条数
                  }
                  {sigRecords.map((r, i) => (
                    <option key={r.process_time} value={i}>
                      批次 {sigRecords.length - i} · {fmtTime(r.process_time)}（{r.signals.length} 信号 / {r.raw_count} 条）
                    </option>
                  ))}
                </select>
              )}
              <button className="btn-refresh" onClick={load} disabled={loading}>刷新</button>
            </div>

            {
              // 跨批次搜索视图：命中概要 + 按批次分组的信号卡片
            }
            {sigSearching ? (
              <div className="search-view">
                {
                  // 无命中提示 / 命中概要（总条数与批次数）
                }
                {!sigSearchGroups.length ? (
                  <div className="log-empty">未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  <div className="search-summary">
                    共 {sigTotalHits} 条信号命中，跨 {sigSearchGroups.length} 个批次
                  </div>
                )}
                {
                  // 按批次分组渲染命中的信号条目
                }
                {sigSearchGroups.map((g, gi) => (
                  <div key={gi} className="search-group">
                    {
                      // 分组头：批次时间 + 该批命中条数
                    }
                    <div className="search-group-head">
                      <span className="search-batch">批次 {fmtTime(g.time)}</span>
                      <span className="search-count">命中 {g.items.length} 条</span>
                    </div>
                    {
                      // 信号条目：头部（代码/名称/战法/方向/动作/置信/价格）+ 正文（板块/理由）
                    }
                    {g.items.map((sg, i) => (
                      <div key={i} className="signal-item">
                        {
                          // 信号头部：代码、名称、战法、多空方向、买卖动作、置信度、信号价
                        }
                        <div className="sig-head">
                          <span className="sig-code">{sg.code}</span>
                          <span className="sig-name">{sg.name || '-'}</span>
                          <span className="sig-strategy">{sg.strategy || '-'}</span>
                          <span className={'tag dir-' + sg.direction}>{sg.direction || '中性'}</span>
                          <span className={'tag act-' + sg.action}>{sg.action || '-'}</span>
                          <span className="sig-conf">置信 {(sg.confidence || 0).toFixed(2)}</span>
                          {sg.price && <span className="sig-price">¥{sg.price.toFixed(2)}</span>}
                        </div>
                        {
                          // 详情行：所属板块与入选理由（有值才渲染）
                        }
                        <div className="sig-body">
                          {sg.sector && <span className="sig-sector">{sg.sector}</span>}
                          {sg.reason && <span className="sig-reason">{sg.reason}</span>}
                        </div>
                      </div>
                    ))}
                  </div>
                ))}
              </div>
            /* 单批次详情视图：无数据提示 / 概要栏 + 信号列表 */
            ) : (
              <>
                {/* 无数据：当日尚无信号批次记录 */}
                {sigNoData ? (
                  <div className="log-empty">暂无信号批次记录，等待下一轮扫描</div>
                ) : sigData ? (
                  <>
                    {
                      // 概要栏：批次时间 / 原始条数 / 信号数
                    }
                    <div className="summary-bar">
                      <div className="summary-item">
                        <span className="summary-label">批次时间</span>
                        <span className="summary-value">{fmtTime(sigData.process_time)}</span>
                      </div>
                      {
                        // 概要项：原始新闻条数（信号批次）
                      }
                      <div className="summary-item">
                        <span className="summary-label">原始条数</span>
                        <span className="summary-value">{sigData.raw_count}</span>
                      </div>
                      {
                        // 概要项：本轮产出的信号总数
                      }
                      <div className="summary-item">
                        <span className="summary-label">信号数</span>
                        <span className="summary-value">{sigData.signals.length}</span>
                      </div>
                    </div>

                    {
                      // 信号列表：按战法筛选后的信号逐条展示；筛选后为空给区分性空态文案
                    }
                    {sigFiltered.length ? (
                      <div className="signal-list">
                        {sigFiltered.map((sg, i) => (
                          <div key={sg.id || i} className="signal-item">
                            {
                              // 信号头部：代码/名称/战法/方向/动作/置信/信号价
                            }
                            <div className="sig-head">
                              <span className="sig-code">{sg.code}</span>
                              <span className="sig-name">{sg.name || '-'}</span>
                              <span className="sig-strategy">{sg.strategy || '-'}</span>
                              <span className={'tag dir-' + sg.direction}>{sg.direction || '中性'}</span>
                              <span className={'tag act-' + sg.action}>{sg.action || '-'}</span>
                              <span className="sig-conf">置信 {(sg.confidence || 0).toFixed(2)}</span>
                              {sg.price && <span className="sig-price">¥{sg.price.toFixed(2)}</span>}
                            </div>
                            {
                              // 信号正文：所属板块与入选理由（有值才渲染）
                            }
                            <div className="sig-body">
                              {sg.sector && <span className="sig-sector">{sg.sector}</span>}
                              {sg.reason && <span className="sig-reason">{sg.reason}</span>}
                            </div>
                          </div>
                        ))}
                      </div>
                    ) : (
                      /* 空态：区分「当前战法无匹配」与「本轮无信号产出」 */
                      <div className="log-empty">{sigData.signals.length ? '当前战法无匹配信号' : '本轮无信号产出'}</div>
                    )}
                  </>
                ) : null}
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
