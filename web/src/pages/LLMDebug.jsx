// ── LLM 分析诊断页面 LLMDebug.jsx ──
// 展示最新一轮 Stage 流水线结果（Stage1 初筛 / Stage2 事件分析），
// 并内嵌 Dialog + Tabs 弹窗用于按批次查看 LLM 分析与信号批次日志。
// 使用 TDesign React 组件（Card / Table / Tag / Button / Dialog / Tabs / Input / Select）。
import React, { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import { Card, Table, Tag, Button, Dialog, Tabs, Input, Select } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

// 根据新闻/信号方向（利好/利空/中性）返回对应的 TDesign Tag 主题色
function dirTheme(d) {
  if (d === '利好') return 'success'
  if (d === '利空') return 'danger'
  return 'warning'
}

// 将标签数组渲染为一组 TDesign Tag；kind 为 'stock' 时使用 warning 主题区分个股标签
function TagList({ items, kind }) {
  if (!items || !items.length) return <span className="muted">—</span>
  return (
    <span style={{ display: 'inline-flex', gap: 4, flexWrap: 'wrap' }}>
      {items.map((s, i) => (
        <Tag key={i} size="small" theme={kind === 'stock' ? 'warning' : 'primary'} variant="light">{s}</Tag>
      ))}
    </span>
  )
}

// 工具栏容器样式（搜索框 + 批次下拉 + 刷新按钮）
const toolbarStyle = { display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12, flexWrap: 'wrap' }
// 顶部统计条样式：横向排列原始条数 / 筛选后 / 分析时间等指标
const summaryBarStyle = { display: 'flex', gap: 24, flexWrap: 'wrap', fontSize: 13, padding: '4px 0' }
// 单个统计项：标签在上、数值在下
const summaryItemStyle = { display: 'flex', flexDirection: 'column', gap: 2 }
// 空数据占位样式
const emptyStyle = { textAlign: 'center', color: '#888', padding: 24 }

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
    // 将时间戳转为 Date 对象
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  // 判断 Stage1 初筛中第 i 条是否被 LLM 选中
  const isSelected = (i) => selectedSet.has(i)

  // 将当前选中的 LLM 轮次数据应用到展示态
  function applyLLM() {
    const r = llmRecords[llmIdx]
    // 取当前选中的记录（LLM 轮次 / 信号批次 / Stage 记录）
    setLlmData(r || null)
    setLlmNoData(!r)
    setSelectedSet(new Set(r ? r.selected_idx || [] : []))
  }
  // 将当前选中的信号批次数据应用到展示态
  function applySignal() {
    const r = sigRecords[sigIdx]
    // 取当前选中的记录（LLM 轮次 / 信号批次 / Stage 记录）
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
  // 从信号批次日志中提取全部战法名，生成下拉筛选项
    const set = new Set()
    // 用于去重收集战法名的集合
    for (const r of sigRecords) {
      for (const sg of (r.signals || [])) {
        if (sg.strategy) set.add(sg.strategy)
      }
    }
    return [...set].map((s) => ({ label: s, value: s }))
  }, [sigRecords])

  const sigFiltered = useMemo(() => {
  // 按当前选中的战法过滤出要展示的信号列表
    const sigs = sigData?.signals || []
    // 当前信号数据中的信号数组（空值兜底）
    if (activeSigStrategy === 'all') return sigs
    return sigs.filter((sg) => sg.strategy === activeSigStrategy)
  }, [sigData, activeSigStrategy])

  const llmSearching = (llmQuery || '').trim() !== ''
  // 是否存在 LLM 日志搜索关键词
  const sigSearching = (sigQuery || '').trim() !== ''
  // 是否存在信号搜索关键词
  const llmSearchGroups = useMemo(() => {
  // 按关键词对 LLM 各轮 stage2 事件分组聚合
    const q = (llmQuery || '').trim().toUpperCase()
    // 归一化后的搜索关键词（去除首尾空格并转大写）
    if (!q) return []
    const groups = []
    // 聚合命中结果的按轮次/批次分组容器
    for (const r of llmRecords) {
      const items = (r.stage2_events || []).filter((ev) => eventHit(ev, q))
      // 当前轮次/批次中命中搜索条件的条目
      if (items.length) groups.push({ time: r.process_time, items })
    }
    return groups
  }, [llmRecords, llmQuery])
  const llmTotalHits = llmSearchGroups.reduce((n, g) => n + g.items.length, 0)
  // LLM 搜索命中的事件总条数
  const sigSearchGroups = useMemo(() => {
  // 按关键词与战法对信号批次分组聚合
    const q = (sigQuery || '').trim().toUpperCase()
    // 归一化后的搜索关键词（去除首尾空格并转大写）
    if (!q) return []
    const groups = []
    // 聚合命中结果的按轮次/批次分组容器
    for (const r of sigRecords) {
      const items = (r.signals || []).filter((sg) => sigHit(sg, q) && (activeSigStrategy === 'all' || sg.strategy === activeSigStrategy))
      // 当前轮次/批次中命中搜索条件的条目
      if (items.length) groups.push({ time: r.process_time, items })
    }
    return groups
  }, [sigRecords, sigQuery, activeSigStrategy])
  const sigTotalHits = sigSearchGroups.reduce((n, g) => n + g.items.length, 0)
  // 信号搜索命中的信号总条数

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

  const llmBatchOptions = llmRecords.map((r, i) => ({
  // LLM 轮次下拉选项（含时间/原始条数/选中数）
    label: `轮次 ${llmRecords.length - i} · ${fmtTime(r.process_time)}（${r.raw_count} 条 / 选 ${r.selected_count}）`,
    value: i,
  }))
  const sigBatchOptions = sigRecords.map((r, i) => ({
  // 信号批次下拉选项（含时间/信号数/原始条数）
    label: `批次 ${sigRecords.length - i} · ${fmtTime(r.process_time)}（${r.signals.length} 信号 / ${r.raw_count} 条）`,
    value: i,
  }))

  // Stage1 初筛表格列定义：序号 / 标题 / 是否通过筛选
  const stage1Columns = [
    { colKey: 'idx', title: '#', width: 60 },
    { colKey: 'title', title: '标题', ellipsis: true },
    {
      colKey: 'sel', title: '筛选', width: 90,
      cell: ({ row }) => (
        <Tag theme={row.sel ? 'success' : 'default'} variant="light" size="small">
          {row.sel ? '通过' : '过滤'}
        </Tag>
      ),
    },
  ]
  // Stage2 事件分析表格列定义：标题 / 方向 / 评分 / 板块 / 个股 / 上下游 / 影响 / 类型 / 理由
  const stage2Columns = [
    { colKey: 'title', title: '标题', ellipsis: true, minWidth: 160 },
    {
      colKey: 'direction', title: '方向', width: 80,
      cell: ({ row }) => <Tag theme={dirTheme(row.direction)} size="small">{row.direction || '中性'}</Tag>,
    },
    { colKey: 'score', title: '评分', width: 90, cell: ({ row }) => Number(row.score || 0).toFixed(2) },
    { colKey: 'sectors', title: '板块', minWidth: 120, cell: ({ row }) => <TagList items={row.sectors} /> },
    { colKey: 'stocks', title: '个股', minWidth: 120, cell: ({ row }) => <TagList items={row.related_stocks} kind="stock" /> },
    { colKey: 'upstream', title: '上游', minWidth: 100, cell: ({ row }) => <TagList items={row.upstream_sectors} /> },
    { colKey: 'downstream', title: '下游', minWidth: 100, cell: ({ row }) => <TagList items={row.downstream_sectors} /> },
    {
      colKey: 'impact', title: '影响', width: 80,
      cell: ({ row }) => row.impact_level
        ? <Tag size="small" theme={row.impact_level === '高' ? 'danger' : row.impact_level === '中' ? 'warning' : 'default'} variant="light">{row.impact_level}</Tag>
        : <span className="muted">—</span>,
    },
    {
      colKey: 'type', title: '类型', width: 110,
      cell: ({ row }) => row.event_type ? <Tag size="small" variant="light">{row.event_type}</Tag> : <span className="muted">—</span>,
    },
    { colKey: 'reason', title: '理由', ellipsis: true, minWidth: 160 },
  ]

  // 信号批次表格列定义：代码 / 名称 / 战法 / 方向 / 动作 / 置信 / 现价 / 板块 / 理由
  const signalColumns = [
    { colKey: 'code', title: '代码', width: 90 },
    { colKey: 'name', title: '名称', width: 100 },
    { colKey: 'strategy', title: '战法', width: 130 },
    { colKey: 'direction', title: '方向', width: 80, cell: ({ row }) => <Tag theme={dirTheme(row.direction)} size="small">{row.direction || '中性'}</Tag> },
    { colKey: 'action', title: '动作', width: 80, cell: ({ row }) => <Tag size="small" variant="light">{row.action || '-'}</Tag> },
    { colKey: 'confidence', title: '置信', width: 90, cell: ({ row }) => Number(row.confidence || 0).toFixed(2) },
    { colKey: 'price', title: '现价', width: 90, cell: ({ row }) => row.price ? '¥' + Number(row.price).toFixed(2) : '-' },
    { colKey: 'sector', title: '板块', width: 120, ellipsis: true },
    { colKey: 'reason', title: '理由', ellipsis: true, minWidth: 160 },
  ]

  return (
    <Dialog visible={visible} onClose={onClose} header="📋 日志" width="900px" footer={false}>
      <div>
        <Tabs value={activeTab} onChange={(v) => switchTab(v)}>
          <Tabs.TabPanel value="llm" label="LLM 分析">
            <div className="toolbar" style={toolbarStyle}>
              <Input
                value={llmQuery}
                onChange={(v) => setLlmQuery(v)}
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
                style={{ flex: 1 }}
              />
              {!llmSearching && (
                <Select
                  value={llmIdx}
                  options={llmBatchOptions}
                  onChange={(v) => { setLlmIdx(Number(v)); applyLLM() }}
                  disabled={llmRecords.length < 2}
                  style={{ width: 320 }}
                />
              )}
              <Button theme="default" variant="outline" onClick={load} loading={loading}>刷新</Button>
            </div>

            {llmSearching ? (
              <div>
                {!llmSearchGroups.length ? (
                  <div style={emptyStyle}>未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  <>
                    <div className="muted" style={{ marginBottom: 8 }}>共 {llmTotalHits} 条事件命中，跨 {llmSearchGroups.length} 个轮次</div>
                    {llmSearchGroups.map((g, gi) => (
                      <div key={gi} style={{ marginBottom: 12 }}>
                        <div style={{ display: 'flex', gap: 12, marginBottom: 4, fontSize: 13 }}>
                          <span className="muted">轮次 {fmtTime(g.time)}</span>
                          <span className="muted">命中 {g.items.length} 条</span>
                        </div>
                        <Table
                          data={g.items.map((ev, i) => ({ ...ev, _k: gi + '_' + i }))}
                          columns={stage2Columns}
                          rowKey="_k"
                          size="small"
                          pagination={false}
                        />
                      </div>
                    ))}
                  </>
                )}
              </div>
            ) : (
              <>
                {llmNoData && <div style={emptyStyle}>暂无 LLM 分析记录，等待下一轮扫描</div>}
                {llmData && (
                  <>
                    <div style={summaryBarStyle}>
                      <div style={summaryItemStyle}>
                        <span className="muted">Stage1 模式</span>
                        <span style={{ color: llmData.stage1_mode === 'llm' ? '#0052d9' : '#faad14', fontWeight: 600 }}>
                          {llmData.stage1_mode === 'llm' ? 'LLM' : '关键词'}
                        </span>
                      </div>
                      <div style={summaryItemStyle}><span className="muted">原始条数</span><span style={{ color: '#1a1a1a' }}>{llmData.raw_count}</span></div>
                      <div style={summaryItemStyle}><span className="muted">筛选后</span><span style={{ color: '#1a1a1a' }}>{llmData.selected_count}</span></div>
                      <div style={summaryItemStyle}><span className="muted">分析时间</span><span style={{ color: '#1a1a1a' }}>{fmtTime(llmData.process_time)}</span></div>
                    </div>

                    <SectionLabel>Stage1 · 新闻初筛</SectionLabel>
                    <Table
                      data={(llmData.raw_titles || []).map((t, i) => ({ idx: i + 1, title: t, sel: isSelected(i) }))}
                      columns={stage1Columns}
                      rowKey="idx"
                      size="small"
                      pagination={false}
                    />

                    <SectionLabel>Stage2 · LLM 分析结果</SectionLabel>
                    {llmData.stage2_events && llmData.stage2_events.length > 0 ? (
                      <Table
                        data={llmData.stage2_events.map((ev, i) => ({ ...ev, _k: i }))}
                        columns={stage2Columns}
                        rowKey="_k"
                        size="small"
                        pagination={{ pageSize: 10, showJumper: true }}
                      />
                    ) : (
                      <div style={emptyStyle}>Stage2 无分析结果</div>
                    )}
                  </>
                )}
              </>
            )}
          </Tabs.TabPanel>

          <Tabs.TabPanel value="signal" label="信号批次">
            <div className="toolbar" style={toolbarStyle}>
              <Input
                value={sigQuery}
                onChange={(v) => setSigQuery(v)}
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
                style={{ flex: 1 }}
              />
              <Select
                value={activeSigStrategy}
                options={[{ label: '全部战法', value: 'all' }, ...sigStrategyOptions]}
                onChange={(v) => setActiveSigStrategy(v)}
                style={{ width: 160 }}
              />
              {!sigSearching && (
                <Select
                  value={sigIdx}
                  options={sigBatchOptions}
                  onChange={(v) => { setSigIdx(Number(v)); applySignal() }}
                  disabled={sigRecords.length < 2}
                  style={{ width: 320 }}
                />
              )}
              <Button theme="default" variant="outline" onClick={load} loading={loading}>刷新</Button>
            </div>

            {sigSearching ? (
              <div>
                {!sigSearchGroups.length ? (
                  <div style={emptyStyle}>未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  <>
                    <div className="muted" style={{ marginBottom: 8 }}>共 {sigTotalHits} 条信号命中，跨 {sigSearchGroups.length} 个批次</div>
                    {sigSearchGroups.map((g, gi) => (
                      <div key={gi} style={{ marginBottom: 12 }}>
                        <div style={{ display: 'flex', gap: 12, marginBottom: 4, fontSize: 13 }}>
                          <span className="muted">批次 {fmtTime(g.time)}</span>
                          <span className="muted">命中 {g.items.length} 条</span>
                        </div>
                        <Table
                          data={g.items.map((sg, i) => ({ ...sg, _k: gi + '_' + i }))}
                          columns={signalColumns}
                          rowKey="_k"
                          size="small"
                          pagination={false}
                        />
                      </div>
                    ))}
                  </>
                )}
              </div>
            ) : (
              <>
                {sigNoData && <div style={emptyStyle}>暂无信号批次记录，等待下一轮扫描</div>}
                {sigData && (
                  <>
                    <div style={summaryBarStyle}>
                      <div style={summaryItemStyle}><span className="muted">批次时间</span><span style={{ color: '#1a1a1a' }}>{fmtTime(sigData.process_time)}</span></div>
                      <div style={summaryItemStyle}><span className="muted">原始条数</span><span style={{ color: '#1a1a1a' }}>{sigData.raw_count}</span></div>
                      <div style={summaryItemStyle}><span className="muted">信号数</span><span style={{ color: '#1a1a1a' }}>{sigData.signals.length}</span></div>
                    </div>
                    {sigFiltered.length > 0 ? (
                      <Table
                        data={sigFiltered.map((sg, i) => ({ ...sg, _k: i }))}
                        columns={signalColumns}
                        rowKey="_k"
                        size="small"
                        pagination={{ pageSize: 10, showJumper: true }}
                      />
                    ) : (
                      <div style={emptyStyle}>{sigData.signals.length ? '当前战法无匹配信号' : '本轮无信号产出'}</div>
                    )}
                  </>
                )}
              </>
            )}
          </Tabs.TabPanel>
        </Tabs>
      </div>
    </Dialog>
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
  const timerRef = useRef(null)
  const sseUnsubRef = useRef(null)

  const [selectedSet, setSelectedSet] = useState(new Set())

  const isSelected = (i) => selectedSet.has(i)
  // 判断 Stage1 初筛中第 i 条是否被 LLM 选中（复用 selectedSet）

  // 取最新一轮 Stage 记录更新页面展示态
  function applyLatest() {
    const r = records[0] || null
    // 取当前选中的记录（LLM 轮次 / 信号批次 / Stage 记录）
    setData(r)
    setNoAgent(false)
    setNoData(!r)
    setSelectedSet(new Set(r ? r.selected_idx || [] : []))
  }

  // 格式化时间为 HH:mm:ss
  function formatTime(t) {
    if (!t) return '-'
    const d = new Date(t)
    // 将时间戳转为 Date 对象
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  }

  // 拉取 Stage 记录并处理 Agent 未就绪、无数据、正常数据三种状态
  async function loadData() {
    setLoading(true)
    try {
      const res = await api.fetchStageRecords()
      // 拉取到的 Stage 记录接口响应
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

  // 页面挂载：首次加载 + SSE 实时刷新 + 15s 轮询兜底
  // 近实时翻转信号由 scoring_loop 固化进 signal_records，主循环轮次之外也能及时上屏。
  // English: initial load + SSE-driven refresh + 15s poll fallback, so near-realtime signals
  // (persisted by scoring_loop) show up even between main-loop rounds.
  useEffect(() => {
    loadData()
    api.connectSSE()
    sseUnsubRef.current = api.onSSE(loadData)
    timerRef.current = setInterval(loadData, 15000)
    return () => {
      if (timerRef.current) clearInterval(timerRef.current)
      if (sseUnsubRef.current) { sseUnsubRef.current(); sseUnsubRef.current = null }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Stage1 初筛表格列定义：序号 / 标题 / 是否通过筛选
  const stage1Columns = [
    { colKey: 'idx', title: '#', width: 60 },
    { colKey: 'title', title: '标题', ellipsis: true },
    {
      colKey: 'sel', title: '筛选', width: 90,
      cell: ({ row }) => (
        <Tag theme={row.sel ? 'success' : 'default'} variant="light" size="small">
          {row.sel ? '通过' : '过滤'}
        </Tag>
      ),
    },
  ]
  // Stage2 事件分析表格列定义：标题 / 方向 / 评分 / 板块 / 个股 / 上下游 / 影响 / 类型 / 理由
  const stage2Columns = [
    { colKey: 'title', title: '标题', ellipsis: true, minWidth: 160 },
    {
      colKey: 'direction', title: '方向', width: 80,
      cell: ({ row }) => <Tag theme={dirTheme(row.direction)} size="small">{row.direction || '中性'}</Tag>,
    },
    { colKey: 'score', title: '评分', width: 90, cell: ({ row }) => Number(row.score || 0).toFixed(2) },
    { colKey: 'sectors', title: '板块', minWidth: 120, cell: ({ row }) => <TagList items={row.sectors} /> },
    { colKey: 'stocks', title: '个股', minWidth: 120, cell: ({ row }) => <TagList items={row.related_stocks} kind="stock" /> },
    { colKey: 'upstream', title: '上游', minWidth: 100, cell: ({ row }) => <TagList items={row.upstream_sectors} /> },
    { colKey: 'downstream', title: '下游', minWidth: 100, cell: ({ row }) => <TagList items={row.downstream_sectors} /> },
    {
      colKey: 'impact', title: '影响', width: 80,
      cell: ({ row }) => row.impact_level
        ? <Tag size="small" theme={row.impact_level === '高' ? 'danger' : row.impact_level === '中' ? 'warning' : 'default'} variant="light">{row.impact_level}</Tag>
        : <span className="muted">—</span>,
    },
    {
      colKey: 'type', title: '类型', width: 110,
      cell: ({ row }) => row.event_type ? <Tag size="small" variant="light">{row.event_type}</Tag> : <span className="muted">—</span>,
    },
    { colKey: 'reason', title: '理由', ellipsis: true, minWidth: 160 },
  ]

  return (
    <div className="page">
      <div className="toolbar" style={{ justifyContent: 'space-between', marginBottom: 16 }}>
        <SectionLabel>LLM 分析诊断</SectionLabel>
        <div style={{ display: 'flex', gap: 8 }}>
          <Button theme="default" variant="outline" onClick={() => setShowLog(true)}>📋 日志</Button>
          <Button theme="primary" onClick={loadData} loading={loading}>刷新</Button>
        </div>
      </div>
      <LogModal visible={showLog} onClose={() => setShowLog(false)} />

      {noAgent && <div style={emptyStyle}>Agent 未就绪</div>}
      {!noAgent && noData && <div style={emptyStyle}>暂无数据，等待下一轮扫描</div>}
      {data && (
        <>
          <Card style={{ marginBottom: 16 }}>
            <div style={summaryBarStyle}>
              <div style={summaryItemStyle}>
                <span className="muted">Stage1 模式</span>
                <span style={{ color: data.stage1_mode === 'llm' ? '#0052d9' : '#faad14', fontWeight: 600 }}>
                  {data.stage1_mode === 'llm' ? 'LLM' : '关键词'}
                </span>
              </div>
              <div style={summaryItemStyle}><span className="muted">原始条数</span><span style={{ color: '#1a1a1a' }}>{data.raw_count}</span></div>
              <div style={summaryItemStyle}><span className="muted">筛选后</span><span style={{ color: '#1a1a1a' }}>{data.selected_count}</span></div>
              <div style={summaryItemStyle}><span className="muted">分析时间</span><span style={{ color: '#1a1a1a' }}>{formatTime(data.process_time)}</span></div>
            </div>
          </Card>

          <Card title="Stage1 · 新闻初筛" style={{ marginBottom: 16 }}>
            <Table
              data={(data.raw_titles || []).map((t, i) => ({ idx: i + 1, title: t, sel: isSelected(i) }))}
              columns={stage1Columns}
              rowKey="idx"
              size="small"
              pagination={false}
            />
          </Card>

          <Card title="Stage2 · LLM 分析结果">
            {data.stage2_events && data.stage2_events.length > 0 ? (
              <Table
                data={data.stage2_events.map((ev, i) => ({ ...ev, _k: i }))}
                columns={stage2Columns}
                rowKey="_k"
                size="small"
                pagination={{ pageSize: 10, showJumper: true }}
              />
            ) : (
              <div style={emptyStyle}>Stage2 无分析结果</div>
            )}
          </Card>
        </>
      )}
    </div>
  )
}

// 板块小标题
function SectionLabel({ children }) {
  return <div style={{ fontWeight: 600, margin: '8px 0 4px', fontSize: 13 }}>{children}</div>
}
