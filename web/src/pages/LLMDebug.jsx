// ── LLM 分析诊断页面 LLMDebug.jsx ──
// 展示最新一轮 Stage 流水线结果（Stage1 初筛 / Stage2 事件分析），
// 并内嵌 Dialog + Tabs 弹窗用于按批次查看 LLM 分析与信号批次日志。
// 使用 TDesign React 组件（Card / Table / Tag / Button / Dialog / Tabs / Input / Select）。
import React, { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { Card, Table, Tag, Button, Dialog, Tabs, Input, Select } from 'tdesign-react'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'
// §P2-I（2026-10-06 修复批 波 6）轮询「后到丢弃」统一入口。本页两条读取路都写 state 且会交错：
// 页面主体 loadData 被 15s 轮询 + SSE 推送 + 手动刷新三路驱动，弹窗 load 被「可见性 + switchTab +
// 刷新」驱动；旧请求迟到响应会把新一轮刚写好的 records/data 盖回去（数据倒挂）。用这一个 hook
// 各取一个跨渲染稳定的守卫实例（内部 useRef 惰性初始化，禁止写成 useRef(createStaleGuard())）。
import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）
// §P2-J 同族（吞错可见化）：读取腿失败按腿名进共用台账，页顶/弹窗各出红条点名「哪条腿没读到」，
// 不再用空态冒充「没数据」（读取失败冒充无数据正是本页 Promise.allSettled 的老形态）。
import { useLoadLedger } from '../utils/loadLedger.js' // §P2-J 读取失败按腿记账（共用台账）
// §P2-J 台账红条单实现：页面（含日志弹窗）只管记账与摆放位置，文案/testid 都在组件里，不在页内抄 div。
import LoadFailBanner from '../components/LoadFailBanner.jsx' // §P2-J 台账红条（单实现，testid 只有一处）

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
const emptyStyle = { textAlign: 'center', color: 'var(--app-muted)', padding: 24 }

/**
 * 日志弹窗子组件（LLM 分析 / 信号批次）
 * 按轮次展示 Stage 流水线记录，支持跨批次搜索与战法筛选。
 * @param {{visible:boolean, onClose:()=>void}} props
 * @returns {JSX.Element|null}
 */
// 引擎流水线日志弹窗（LLM/信号双 tab）
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

  // §P2-I：日志弹窗 load 的守卫——它由「弹窗可见性 effect + switchTab + 手动刷新」驱动，开弹窗
  // 触发的加载还没回、用户又点刷新或切 tab 时，旧响应迟到会把 records/data 盖回上一轮，故接一个
  // 独立于页面主体的守卫实例（两路 Promise.allSettled 同轮共用一个代号，回来先判后到再写 state）。
  const modalGuard = useStaleGuard()
  // §P2-J：弹窗两条读取腿（LLM 分析记录 / 信号批次日志）的失败台账，红条在弹窗顶部点名。
  const { fails: modalFails, mark: markLoadFail, clear: clearLoadFail } = useLoadLedger()

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
    // §P2-I：本轮代号在两路 allSettled 发起之前盖章；allSettled 回来后统一判后到再写 records/data。
    const token = modalGuard.begin()
    // 并行拉取两侧数据：Stage 记录（LLM 分析）与信号批次日志
    const [srRes, slRes] = await Promise.allSettled([api.fetchStageRecords(), api.fetchSignalLogs()])
    // §P2-I：allSettled 是唯一 await 点，两路在此汇合——旧轮次整包丢弃，一个数据 state 都不写；
    // loading 是弹窗 Spinner（非读数），复位一次即可，交给最新一轮收尾时也会复位，不会卡住。
    if (modalGuard.isStale(token)) { setLoading(false); return }
    // LLM 分析记录：三态分开处置——fulfilled 有数据=成功销案；fulfilled 空数组=真读数无数据（非失败，
    // 仍销案并走空态）；rejected=读取失败，进台账点名且**绝不清空既有读数**（§P2-J 铁律3：读取失败
    // 冒充「暂无 LLM 分析记录」正是本缺陷的老形态）。
    if (srRes.status === 'fulfilled') {
      if (Array.isArray(srRes.value) && srRes.value.length) {
        setLlmRecords(srRes.value)
        setLlmIdx(0)
        applyLLM()
        clearLoadFail('LLM 分析记录')
      } else {
        // fulfilled 但空数组：这是真实读数（本轮确实没有记录），照常置无数据空态，并撤销可能的旧失败记录
        setLlmRecords([])
        setLlmData(null)
        setLlmNoData(true)
        clearLoadFail('LLM 分析记录')
      }
    } else {
      // rejected：读取失败——只点名、保留上一轮读数，不清空、不置无数据（区别于「真的没有」）
      markLoadFail('LLM 分析记录', (srRes.reason && (srRes.reason.message || String(srRes.reason))) || '未知错误')
    }
    // 信号批次日志：与 LLM 分析记录同口径三态处置
    if (slRes.status === 'fulfilled') {
      if (Array.isArray(slRes.value) && slRes.value.length) {
        setSigRecords(slRes.value)
        setSigIdx(0)
        applySignal()
        clearLoadFail('信号批次日志')
      } else {
        setSigRecords([])
        setSigData(null)
        setSigNoData(true)
        clearLoadFail('信号批次日志')
      }
    } else {
      markLoadFail('信号批次日志', (slRes.reason && (slRes.reason.message || String(slRes.reason))) || '未知错误')
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
    // 基础列：标题 / 方向 / 评分
    { colKey: 'title', title: '标题', ellipsis: true, minWidth: 160 },
    {
      colKey: 'direction', title: '方向', width: 80,
      cell: ({ row }) => <Tag theme={dirTheme(row.direction)} size="small">{row.direction || '中性'}</Tag>,
    },
    { colKey: 'score', title: '评分', width: 90, cell: ({ row }) => Number(row.score || 0).toFixed(2) },
    // 关联对象列：板块 / 个股 / 上游板块 / 下游板块（打标展示）
    { colKey: 'sectors', title: '板块', minWidth: 120, cell: ({ row }) => <TagList items={row.sectors} /> },
    { colKey: 'stocks', title: '个股', minWidth: 120, cell: ({ row }) => <TagList items={row.related_stocks} kind="stock" /> },
    { colKey: 'upstream', title: '上游', minWidth: 100, cell: ({ row }) => <TagList items={row.upstream_sectors} /> },
    { colKey: 'downstream', title: '下游', minWidth: 100, cell: ({ row }) => <TagList items={row.downstream_sectors} /> },
    // 定性列：影响等级 / 事件类型 / LLM 给出的分析理由
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
    // 标的与战法归属列：代码 / 名称 / 所属战法
    { colKey: 'code', title: '代码', width: 90 },
    { colKey: 'name', title: '名称', width: 100 },
    { colKey: 'strategy', title: '战法', width: 130 },
    // 信号结论列：方向 / 动作 / 置信度
    { colKey: 'direction', title: '方向', width: 80, cell: ({ row }) => <Tag theme={dirTheme(row.direction)} size="small">{row.direction || '中性'}</Tag> },
    { colKey: 'action', title: '动作', width: 80, cell: ({ row }) => <Tag size="small" variant="light">{row.action || '-'}</Tag> },
    { colKey: 'confidence', title: '置信', width: 90, cell: ({ row }) => Number(row.confidence || 0).toFixed(2) },
    // 行情与归因列：现价 / 板块 / 生成理由
    { colKey: 'price', title: '现价', width: 90, cell: ({ row }) => row.price ? '¥' + Number(row.price).toFixed(2) : '-' },
    { colKey: 'sector', title: '板块', width: 120, ellipsis: true },
    { colKey: 'reason', title: '理由', ellipsis: true, minWidth: 160 },
  ]

  return (
    <Dialog visible={visible} onClose={onClose} header="📋 日志" width="900px" footer={false}>
      <div>
        {/* §P2-J（2026-10-06 修复批 波 6）吞错可见化红条：LLM 分析记录 / 信号批次日志 任一读取腿
            rejected 就在此点名。旧形态是 Promise.allSettled 的 rejected 被当成「没数据」置 noData 空态——
            读取失败冒充无数据，运维无从分辨。红条只报「哪条腿失败 + 原因」，**不清空既有读数**，
            下一轮成功即自动销案。红条本体走共享组件（单 testid、单文案），页内不抄 div。
            English: §P2-J — per-leg load-failure banner inside the log dialog. */}
        <LoadFailBanner fails={modalFails} page="LLMDebug-LogModal" />
        <Tabs value={activeTab} onChange={(v) => switchTab(v)}>
          <Tabs.TabPanel value="llm" label="LLM 分析">
            <div className="toolbar" style={toolbarStyle}>
              {
                /* 工具栏：跨轮次搜索框 + 轮次下拉 + 刷新按钮 */
              }
              <Input
                value={llmQuery}
                onChange={(v) => setLlmQuery(v)}
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
                style={{ flex: 1 }}
              />
              {!llmSearching && (
                // 搜索时隐藏轮次下拉，切换后立即应用该轮数据
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
              // 搜索态：按轮次分组展示跨批次命中结果
              <div>
                {!llmSearchGroups.length ? (
                  // 无命中空态提示
                  <div style={emptyStyle}>未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  // 命中分组列表：每个轮次一张命中事件小表
                  <>
                    <div className="muted" style={{ marginBottom: 8 }}>共 {llmTotalHits} 条事件命中，跨 {llmSearchGroups.length} 个轮次</div>
                    {llmSearchGroups.map((g, gi) => (
                      // 单个轮次的命中：批次时间 + 命中数 + 事件表
                      <div key={gi} style={{ marginBottom: 12 }}>
                        {
                          /* 轮次时间与命中条数头 */
                        }
                        <div style={{ display: 'flex', gap: 12, marginBottom: 4, fontSize: 13 }}>
                          <span className="muted">轮次 {fmtTime(g.time)}</span>
                          <span className="muted">命中 {g.items.length} 条</span>
                        </div>
                        {
                          /* 该轮命中事件用 Stage2 列渲染 */
                        }
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
              // 常规态：无数据空态 + 选中轮次的概要条与两级表格
              <>
                {llmNoData && <div style={emptyStyle}>暂无 LLM 分析记录，等待下一轮扫描</div>}
                {llmData && (
                  <>
                    {
                      /* 概要条：Stage1 模式 / 原始条数 / 筛选后 / 分析时间 */
                    }
                    <div style={summaryBarStyle}>
                      <div style={summaryItemStyle}>
                        <span className="muted">Stage1 模式</span>
                        <span style={{ color: llmData.stage1_mode === 'llm' ? 'var(--td-brand-color)' : 'var(--td-warning-color)', fontWeight: 600 }}>
                          {llmData.stage1_mode === 'llm' ? 'LLM' : '关键词'}
                        </span>
                      </div>
                      <div style={summaryItemStyle}><span className="muted">原始条数</span><span style={{ color: 'var(--app-text)' }}>{llmData.raw_count}</span></div>
                      <div style={summaryItemStyle}><span className="muted">筛选后</span><span style={{ color: 'var(--app-text)' }}>{llmData.selected_count}</span></div>
                      <div style={summaryItemStyle}><span className="muted">分析时间</span><span style={{ color: 'var(--app-text)' }}>{fmtTime(llmData.process_time)}</span></div>
                    </div>

                    {
                      /* Stage1 表：本轮全部原始标题及是否通过关键词筛选 */
                    }
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
                      // 有事件结果：分页展示 Stage2 分析明细
                      <Table
                        data={llmData.stage2_events.map((ev, i) => ({ ...ev, _k: i }))}
                        columns={stage2Columns}
                        rowKey="_k"
                        size="small"
                        // §FIX-20260902 补 total=长度：不传 total 时 tdesign 分页错显「共 0 条」且无法翻页
                        pagination={{ pageSize: 10, showJumper: true, total: llmData.stage2_events.length }}
                      />
                    ) : (
                      // 本轮无事件结果的空态
                      <div style={emptyStyle}>Stage2 无分析结果</div>
                    )}
                  </>
                )}
              </>
            )}
          </Tabs.TabPanel>

          <Tabs.TabPanel value="signal" label="信号批次">
            <div className="toolbar" style={toolbarStyle}>
              {
                /* 工具栏：跨批次搜索框 + 战法筛选 + 批次下拉 + 刷新按钮 */
              }
              <Input
                value={sigQuery}
                onChange={(v) => setSigQuery(v)}
                placeholder="搜索：个股名称 / 代码 / 板块（跨批次）"
                style={{ flex: 1 }}
              />
              {
                /* 战法筛选下拉：全部战法或指定战法 */
              }
              <Select
                value={activeSigStrategy}
                options={[{ label: '全部战法', value: 'all' }, ...sigStrategyOptions]}
                onChange={(v) => setActiveSigStrategy(v)}
                style={{ width: 160 }}
              />
              {!sigSearching && (
                // 搜索时隐藏批次下拉
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
              // 搜索态：按批次分组展示跨批次命中结果
              <div>
                {!sigSearchGroups.length ? (
                  // 无命中空态提示
                  <div style={emptyStyle}>未找到匹配项（可试：代码 / 名称 / 板块关键词）</div>
                ) : (
                  // 命中分组列表：每个批次一张命中信号小表
                  <>
                    <div className="muted" style={{ marginBottom: 8 }}>共 {sigTotalHits} 条信号命中，跨 {sigSearchGroups.length} 个批次</div>
                    {sigSearchGroups.map((g, gi) => (
                      // 单个批次的命中：批次时间 + 命中数 + 信号表
                      <div key={gi} style={{ marginBottom: 12 }}>
                        {
                          /* 批次时间与命中条数头 */
                        }
                        <div style={{ display: 'flex', gap: 12, marginBottom: 4, fontSize: 13 }}>
                          <span className="muted">批次 {fmtTime(g.time)}</span>
                          <span className="muted">命中 {g.items.length} 条</span>
                        </div>
                        {
                          /* 该批命中信号用信号列渲染 */
                        }
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
              // 常规态：无数据空态 + 选中批次的概要条与信号表
              <>
                {sigNoData && <div style={emptyStyle}>暂无信号批次记录，等待下一轮扫描</div>}
                {sigData && (
                  <>
                    {
                      /* 概要条：批次时间 / 原始条数 / 信号数 */
                    }
                    <div style={summaryBarStyle}>
                      <div style={summaryItemStyle}><span className="muted">批次时间</span><span style={{ color: 'var(--app-text)' }}>{fmtTime(sigData.process_time)}</span></div>
                      <div style={summaryItemStyle}><span className="muted">原始条数</span><span style={{ color: 'var(--app-text)' }}>{sigData.raw_count}</span></div>
                      <div style={summaryItemStyle}><span className="muted">信号数</span><span style={{ color: 'var(--app-text)' }}>{sigData.signals.length}</span></div>
                    </div>
                    {sigFiltered.length > 0 ? (
                      // 有信号：按战法过滤后分页展示
                      <Table
                        data={sigFiltered.map((sg, i) => ({ ...sg, _k: i }))}
                        columns={signalColumns}
                        rowKey="_k"
                        size="small"
                        // §FIX-20260902 补 total=长度：不传 total 时 tdesign 分页错显「共 0 条」且无法翻页
                        pagination={{ pageSize: 10, showJumper: true, total: sigFiltered.length }}
                      />
                    ) : (
                      // 无信号空态：区分"战法过滤后无命中"与"本轮无产出"
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
  // §A5（20260918 审计批）：admin 数据源首拉 403（角色缓存伪冒/中途被降权）统一跳 /403
  const navigate = useNavigate()
  const [loading, setLoading] = useState(false)
  const [records, setRecords] = useState([])
  const [data, setData] = useState(null)
  const [noAgent, setNoAgent] = useState(false)
  const [noData, setNoData] = useState(false)
  const [showLog, setShowLog] = useState(false)
  // §FIX-0921d 取数自诊断（页顶小字）：记录主源/回落成败、轮数与耗时——远程定位「白板」根因用，
  // 用户刷新后一眼可见取数链路真实状态（2026-09-01 实录：服务端 20 条正常但用户端白板，无法定位）。
  const [diag, setDiag] = useState(null)
  const timerRef = useRef(null)      // 轮询定时器句柄
  const sseUnsubRef = useRef(null)   // SSE 取消订阅函数引用

  const [selectedSet, setSelectedSet] = useState(new Set())

  // §P2-I：页面主体守卫。loadData 被 15s 轮询 + SSE 推送 + 手动刷新三路交错驱动；interval 与
  // SSE 回调持的是挂载期闭包（其中的 loading 恒为初始 false），既有的 `if (loading) return` 互斥
  // 挡不住它们，上一轮 fetchStageRecords 还在途时下一轮已发出，旧响应迟到会整包覆盖 records/data
  // （数据倒挂，直到下次轮询）。故发起前盖代号、每个 await 回来后写 state 之前判后到。
  const mainGuard = useStaleGuard()
  // §P2-J：主体读取腿台账（Stage 流水线记录 = 主源 stage-records + 回落 llm-debug 的双源合一腿）。
  const { fails: loadFails, mark: markLoadFail, clear: clearLoadFail } = useLoadLedger()

  const isSelected = (i) => selectedSet.has(i)
  // 判断 Stage1 初筛中第 i 条是否被 LLM 选中（复用 selectedSet）

  // 取最新一轮 Stage 记录更新页面展示态
  // §FIX-0921e 根修（2026-09-01 白板实录）：改为接收本次取到的记录作为参数——此前读闭包里的
  // `records` state，`setRecords` 之后立即调用读到的是**上一次渲染的旧值**（首次加载为 []），
  // 导致 r=null → noData=true → 「暂无数据」白板，而自检行照常显示 20 轮 ✅（setState 异步
  // 提交的经典陷阱；旧版靠 15s 轮询的下一次调用自愈，但闭包仍滞后一轮）。
  function applyLatest(recs) {
    const list = recs || records
    const r = (list && list[0]) || null
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
  // §FIX-0921b 双源兜底（2026-09-01 用户反馈实录）：主源 /api/stage-records（当日全量轮次，
  // LLM 修好后单轮含百余条原始标题 + 数十条事件，弱网下可能超时或被判空）拉不到时，
  // 回落 /api/llm-debug（最新单轮快照，含待归因原始新闻 + 已归因事件）——保证「即使新闻
  // 没有分析出有价值结果，也如实显示 L1/L2 内容」，不再出现「暂无数据」白板。
  async function loadData() {
    if (loading) return // §FIX-0921d 防并发风暴：SSE/15s 轮询/手动刷新互斥，避免同时多个 700KB 请求挤占连接
    setLoading(true)
    // §P2-I：本轮代号在发起任何请求之前盖章；下面每个 await 回来后、写 state 之前都判一次后到，
    // 漏一个分支就是漏一条腿（no_engine / 双源皆空 / 成功 三条写 state 的路径都在这道代号之下）。
    const token = mainGuard.begin()
    // 计时与诊断标记：主源/回落是否成功、错误信息
    const t0 = Date.now()
    let recs = null
    let mainOk = false
    let fbOk = false
    let errMsg = ''
    let forbidden = false // §A5：双源任一回 403 即视为服务端权威角色不足
    // 主源：当日全量轮次记录
    try {
      recs = await api.fetchStageRecords()
      mainOk = Array.isArray(recs) && recs.length > 0
    }
    // §P2-J 可吞：主源异常先汇入 recs=null / errMsg / forbidden，交由下面的双源回落与统一终判——
    // 此处不直接判红，因为回落成功仍是有效读数；真正的「没读到」只在双源皆失的 else 分支进台账。
    catch (e) {
      recs = null
      errMsg = (e && e.message) || String(e)
      if (api.isForbidden(e)) forbidden = true
    }
    // §P2-I：主源 await 回来后、写任何 state 之前判后到——后到的旧轮次整包丢弃（连 no_engine 分支也不写）。
    if (mainGuard.isStale(token)) return
    // 引擎未启动：直接置「Agent 未就绪」空态并返回
    if (recs && recs.status === 'no_engine') {
      setNoAgent(true)
      setNoData(false)
      setRecords([])
      setData(null)
      setDiag({ n: 0, mainOk: false, fbOk: false, ms: Date.now() - t0, err: 'no_engine' })
      clearLoadFail('Stage 流水线记录') // §P2-J：no_engine 是服务端真实响应（引擎未就绪），非读取失败→销案
      setLoading(false)
      return
    }
    // 主源失败 → 尝试回落源
    if (!mainOk) {
      // 回落源：最新单轮快照（字段与轮次记录同构，直接包一层数组复用渲染）
      try {
        const d = await api.fetchLLMDebug()
        if (d && !d.status && (d.raw_titles || d.stage2_events)) {
          recs = [d]
          fbOk = true
        }
      }
      // §P2-J 可吞：回落源异常同样汇入 errMsg / forbidden，最终由下面 both-fail 分支统一记台账；
      // 中间态不判红（主源可能已成功、只是没走到这条，或本条失败但整体稍后据 forbidden/空态定夺）。
      catch (e2) {
        if (!errMsg) errMsg = (e2 && e2.message) || String(e2)
        if (api.isForbidden(e2)) forbidden = true
      }
      // §P2-I：回落 await 回来后判后到，旧轮次不得写 state
      if (mainGuard.isStale(token)) return
    }
    // §P2-I：进入终判/写 state 段落前再判一次后到——无论走主源直达还是回落，此处都是最后一道闸
    if (mainGuard.isStale(token)) return
    // 双源任一生效：展示记录并应用最新一轮
    if (Array.isArray(recs) && recs.length) {
      setRecords(recs)
      applyLatest(recs) // §FIX-0921e 传入本次取到的记录，避免读到 setState 前的旧闭包
      setDiag({ n: recs.length, mainOk, fbOk, ms: Date.now() - t0, err: '' })
      clearLoadFail('Stage 流水线记录') // §P2-J：双源任一生效=读取成功，销案
    } else if (forbidden) {
      // §A5：数据首拉即 403——不再渲染「暂无数据」白板掩盖权限问题，重路由统一 403 页
      // 跳走即卸载本页，台账随组件消失，无需在此记账
      navigate('/403')
    } else {
      // 双源皆失败：置无数据空态并记录诊断错误
      setNoData(true)
      setNoAgent(false)
      setRecords([])
      setData(null)
      setDiag({ n: 0, mainOk, fbOk, ms: Date.now() - t0, err: errMsg })
      // §P2-J：这才是真正的「读取腿没读到」——两条源都失败，进台账点名，红条标出下方为上一轮/空、非最新
      markLoadFail('Stage 流水线记录', errMsg || '未知错误')
    }
    setLoading(false)
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
    // 基础列：标题 / 方向 / 评分
    { colKey: 'title', title: '标题', ellipsis: true, minWidth: 160 },
    {
      colKey: 'direction', title: '方向', width: 80,
      cell: ({ row }) => <Tag theme={dirTheme(row.direction)} size="small">{row.direction || '中性'}</Tag>,
    },
    { colKey: 'score', title: '评分', width: 90, cell: ({ row }) => Number(row.score || 0).toFixed(2) },
    // 关联对象列：板块 / 个股 / 上游板块 / 下游板块（打标展示）
    { colKey: 'sectors', title: '板块', minWidth: 120, cell: ({ row }) => <TagList items={row.sectors} /> },
    { colKey: 'stocks', title: '个股', minWidth: 120, cell: ({ row }) => <TagList items={row.related_stocks} kind="stock" /> },
    { colKey: 'upstream', title: '上游', minWidth: 100, cell: ({ row }) => <TagList items={row.upstream_sectors} /> },
    { colKey: 'downstream', title: '下游', minWidth: 100, cell: ({ row }) => <TagList items={row.downstream_sectors} /> },
    // 定性列：影响等级 / 事件类型 / LLM 给出的分析理由
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
      {/* §P2-J（2026-10-06 修复批 波 6）吞错可见化红条：主体读取腿「Stage 流水线记录」本轮没读到
          （主源 + 回落双源皆失）就在此点名。旧形态是失败被吞成 noData 空态，界面显示「暂无数据」冒充
          真的没有，运维只能靠反推。红条只报「哪条腿失败 + 原因」，**不清空既有读数**，下一轮成功即销案。
          注意与下面的 §FIX-0921d 取数自诊断小字是两件事：diag 记的是链路耗时/主源回落是否命中，
          本条记的是「这条读取腿整体没读到」，两者各留各的格子，不合并。
          English: §P2-J — per-leg load-failure banner; kept separate from the self-diag line above the cards. */}
      {Object.keys(loadFails).length > 0 && <LoadFailBanner fails={loadFails} page="LLMDebug" />}
      <LogModal visible={showLog} onClose={() => setShowLog(false)} />

      {/* §FIX-0921d 取数自诊断行：主源/回落/轮数/耗时/错误，一眼定位白板根因 */}
      {diag && (
        // 诊断行：轮数 · 主源/回落成败 · 耗时 · 错误信息
        <div style={{ fontSize: 12, color: 'var(--app-muted-2)', marginBottom: 8 }}>
          数据自检: {diag.n} 轮 · 主源{diag.mainOk ? '✅' : '❌'} · 回落{diag.fbOk ? '✅' : '❌'} · {diag.ms}ms
          {diag.err ? ' · ' + diag.err : ''}
        </div>
      )}
      {noAgent && <div style={emptyStyle}>Agent 未就绪</div>}
      {!noAgent && noData && <div style={emptyStyle}>暂无数据，等待下一轮扫描</div>}
      {data && (
        // 有数据：概要卡片 + Stage1 初筛卡片 + Stage2 分析结果卡片
        <>
          <Card style={{ marginBottom: 16 }}>
            <div style={summaryBarStyle}>
              <div style={summaryItemStyle}>
                <span className="muted">Stage1 模式</span>
                <span style={{ color: data.stage1_mode === 'llm' ? 'var(--td-brand-color)' : 'var(--td-warning-color)', fontWeight: 600 }}>
                  {data.stage1_mode === 'llm' ? 'LLM' : '关键词'}
                </span>
              </div>
              <div style={summaryItemStyle}><span className="muted">原始条数</span><span style={{ color: 'var(--app-text)' }}>{data.raw_count}</span></div>
              <div style={summaryItemStyle}><span className="muted">筛选后</span><span style={{ color: 'var(--app-text)' }}>{data.selected_count}</span></div>
              <div style={summaryItemStyle}><span className="muted">分析时间</span><span style={{ color: 'var(--app-text)' }}>{formatTime(data.process_time)}</span></div>
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
                // §FIX-20260902 补 total=长度：不传 total 时 tdesign 分页错显「共 0 条」且无法翻页
                pagination={{ pageSize: 10, showJumper: true, total: data.stage2_events.length }}
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
