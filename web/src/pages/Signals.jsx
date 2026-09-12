// ── 策略信号页面 Signals.jsx ──
// 展示所有策略评级信号，支持按等级/战法筛选、查看 D1-D4 子维度评分、
// 确认买入/忽略操作、模拟买入归池、一键收藏自选、展开分时+盘口。
// 纯 TDesign React（Card / Table / Tag / Button / Select / Dialog），无自定义 CSS。
import React, { useState, useEffect, useRef } from 'react'
import { Table, Card, Tag, Button, Select, Dialog, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import MinuteView from '../components/MinuteView.jsx'
import LogModal from '../components/LogModal.jsx'

// 顶部快捷筛选：按 remind_level 划分（all/strong/observe/mute）
const FILTERS = [
  { key: 'all', label: '全部' },
  { key: 'strong', label: '可开仓' },
  { key: 'observe', label: '观察' },
  { key: 'mute', label: '静默' },
]

// 取长描述的前缀或前 6 个字符，用于 D-pill 展示
function shortDesc(s) {
  if (!s) return ''
  const idx = s.indexOf(',')
  return idx > 0 ? s.slice(0, idx) : s.slice(0, 6)
}

// 构造 D1 维度标签（事件摘要 + 评分 + 拦截标记）
function d1Tag(s) {
  const label = s.d1_event || s.d1_reason || ''
  const base = shortDesc(label) || '事件'
  const score = s.d1_score ? s.d1_score.toFixed(0) : ''
  const blocked = s.d1_blocked ? '·拦' : ''
  return [base, score, blocked].filter(Boolean).join('')
}

// 涨跌配色（红涨绿跌）
function chgColor(v) {
  return (v || 0) >= 0 ? '#e34d59' : '#00a870'
}

// 通用表头排序器工厂（§修复 P2#23 提取为纯函数，供排序单测覆盖）：
// sorterStr 按字符串字段字典序比较（代码/名称/策略/产生时间，时间串 YYYY-MM-DD HH:MM:SS 字典序即时间序）；
// sorterNum 按数值字段比较（现价/总分）。返回的排序器直接用于 TDesign 列配置 sorter 字段。
// English: generic header-sorter factories (P2#23 extracted as pure functions for the sorting unit tests):
// sorterStr compares a string field lexicographically (code/name/strategy/generated_at — the
// "YYYY-MM-DD HH:MM:SS" timestamp string sorts correctly in lexicographic order); sorterNum compares a
// numeric field (price/total score). Both plug straight into the TDesign column sorter config.
export function sorterStr(key) {
  return (a, b) => (a[key] || '').localeCompare(b[key] || '')
}

// English: numeric-field sorter factory (ascending; missing values treated as 0).
export function sorterNum(key) {
  return (a, b) => (a[key] || 0) - (b[key] || 0)
}

/**
 * 策略信号页面组件
 * 展示策略评级信号，支持等级/战法筛选、D1-D4 维度展示、买入/忽略、模拟买入与日志。
 * @returns {JSX.Element}
 */
export default function Signals() {
  // 策略信号列表
  const [signals, setSignals] = useState([])
  // 已展开分时图的信号代码集合
  const [klineOpen, setKlineOpen] = useState(new Set())
  // 当前等级筛选（all/strong/observe/mute）
  const [activeFilter, setActiveFilter] = useState('all')
  // 当前战法筛选
  const [activeStrategy, setActiveStrategy] = useState('all')
  // 买入/忽略确认弹窗显隐
  const [showConfirm, setShowConfirm] = useState(false)
  // 日志弹窗显隐
  const [showLog, setShowLog] = useState(false)
  // 移动端底部操作面板对应的信号
  const [sheetSignal, setSheetSignal] = useState(null)
  // 模拟盘是否启用（决定是否显示「模拟买入」）
  const [paperOn, setPaperOn] = useState(false)
  // §SHORT-4 做空开关（决策⑤）：关闭时列表整体隐藏做空信号；开启后追加方向筛选与红徽标
  const [shortEnabled, setShortEnabled] = useState(false)
  const [activeDir, setActiveDir] = useState('all')
  // 待确认交易的信号对象
  const [tradeTarget, setTradeTarget] = useState({})
  // 待确认交易动作（buy/ignore）
  const [tradeAction, setTradeAction] = useState('')
  // 主数据轮询定时器（5s）
  const timer = useRef(null)
  // 页面可见性变化处理函数
  const visHandler = useRef(null)
  // SSE 订阅取消函数
  const unsubSSE = useRef(null)
  // §修复 P2#23：受控排序——所有列可点击表头排序（此前表格完全没有 sorter，点表头无反应），
  // 受控状态保证 5s 轮询整体替换信号列表时用户选择的排序不被重置。
  // English: P2#23 — controlled sort so every column is header-sortable (previously the table had no
  // sorters at all) and the user's chosen sort survives the 5s signal-list poll.
  const [sort, setSort] = useState(null)

  // 从信号列表中提取全部战法名称作为筛选下拉选项
  const strategyOptions = Array.from(new Set(signals.map((s) => s.strategy).filter(Boolean)))

  // §SHORT-4 做空显隐（决策⑤）：开关关闭时做空战法信号不进列表；开启后按方向筛选
  // English: short signals are hidden entirely while the switch is off; direction filter when on.
  const dirScoped = shortEnabled
    ? signals.filter((s) => activeDir === 'all' || (s.direction || '做多') === activeDir)
    : signals.filter((s) => s.direction !== '做空')
  // 按等级筛选、战法筛选，并过滤掉「预期差」非标准评级信号
  const filteredSignals = dirScoped
    .filter((s) => (activeFilter !== 'all' ? s.remind_level === activeFilter : true))
    .filter((s) => (activeStrategy !== 'all' ? s.strategy === activeStrategy : true))
    // 过滤掉「预期差」战法（非标准评级信号，不在列表展示）
    .filter((s) => s.strategy !== '预期差')

  // 根据操作结果弹出成功/失败提示
  function showFeedback(msg, type) {
    if (type === 'ok') MessagePlugin.success(msg)
    else MessagePlugin.error(msg)
  }

  // 判断信号是否来自研究战法库（有独立资金池）
  function hasStrategyPool(s) {
    if (!s) return false
    const id = s.strategy_id || ''
    if (id.startsWith('fac_') || id.startsWith('pat_')) return true
    return !!s.strategy_type
  }

  // 打开买入/忽略确认弹窗
  function confirmTrade(s, action) {
    setTradeTarget(s)
    setTradeAction(action)
    setShowConfirm(true)
  }

  // 执行买入或忽略操作并刷新信号列表
  async function doAction(action) {
    try {
      await api.actionSignal(tradeTarget.code, action)
      setShowConfirm(false)
      await load()
    } catch (e) {
      setShowConfirm(false)
      MessagePlugin.error('操作失败: ' + e.message)
    }
  }

  // 展开/收起指定代码的分时图
  function toggleKline(code) {
    setKlineOpen((prev) => {
      const next = new Set(prev)
      if (next.has(code)) next.delete(code)
      else next.add(code)
      return next
    })
  }

  // 移动端点击行时打开底部操作面板
  function onRowTap(s) {
    if (window.innerWidth > 768) return
    setSheetSignal(s)
  }

  // 将信号对应个股加入自选股
  async function collectToWatchlist(s) {
    try {
      await api.addWatchlist(s.code)
      showFeedback('已收藏 ' + s.code, 'ok')
    } catch (e) {
      showFeedback('收藏失败: ' + e.message, 'err')
    }
  }

  // 在模拟盘按指定价格/数量买入该信号个股（弹出 prompt 确认价格与手数）
  async function paperBuy(s) {
    const priceStr = window.prompt('输入买入价（元，留空用实时价）：', s.price || s.close || '')
    const qtyStr = window.prompt('输入买入手数（1 手 = 100 股）：', '1')
    if (qtyStr === null || priceStr === null) return

    // 校验手数与价格合法性
    const qty = parseInt(qtyStr, 10)
    const price = parseFloat(priceStr)
    if (isNaN(qty) || qty <= 0) {
      window.alert('买入手数无效，请填写正整数')
      return
    }

    // 价格为空按实时价成交，有价格则用指定价格；二次确认后提交
    if (isNaN(price) || price <= 0) {
      if (!window.confirm(`确认模拟买入 ${s.code} ${s.name || ''} ${qty} 手？将按实时价成交。`)) return
    } else {
      if (!window.confirm(`确认模拟买入 ${s.code} ${s.name || ''} ${qty} 手 @${price.toFixed(2)}？`)) return
    }

    // 调用后端模拟买入接口：code/name/strategy/price/qty/type/id
    try {
      await api.buyPaperPosition(s.code, s.name || '', s.strategy || '', s.price || 0, isNaN(price) || price <= 0 ? 0 : price, qty, s.strategy_type || '', s.strategy_id || '')
      window.alert(`已模拟买入 ${s.code} ${qty} 手`)
    } catch (e) {
      window.alert(e.message || '模拟买入失败')
    }
  }

  // 加载当前策略信号列表
  async function load() {
    try { setSignals(await api.fetchSignals()) } catch (_) {}
  }

  // 探测模拟盘开关状态
  async function probePaper() {
    try { setPaperOn(!!(await api.fetchPaperState()).enabled) } catch (_) {}
  }

  // §SHORT-4 探测做空开关（关闭时隐藏全部做空内容）
  async function probeShort() {
    try { setShortEnabled(!!(await api.fetchShortStatus()).short_enabled) } catch (_) {}
  }

  // SSE 新信号或扫描到达时刷新列表
  function handleSSE(msg) {
    if (msg.signal || msg.type === 'scan') load()
  }

  // 挂载时加载信号、探测模拟盘、启动轮询与 SSE；处理页面可见性变化
  useEffect(() => {
    load()
    probePaper()
    probeShort()
    // 每 5s 轮询刷新信号列表
    timer.current = setInterval(load, 5000)
    visHandler.current = () => {
      if (document.hidden) {
        if (timer.current) { clearInterval(timer.current); timer.current = null }
      } else if (!timer.current) {
        load()
        timer.current = setInterval(load, 5000)
      }
    }
    document.addEventListener('visibilitychange', visHandler.current)
    api.connectSSE()
    unsubSSE.current = api.onSSE(handleSSE)

    // 清理：清除轮询定时器、移除可见性监听、取消SSE订阅
    return () => {
      if (timer.current) clearInterval(timer.current)
      if (visHandler.current) document.removeEventListener('visibilitychange', visHandler.current)
      if (unsubSSE.current) unsubSSE.current()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 信号表格列定义：代码、名称、现价/涨跌、策略、总分、等级、
  // D1-D4 维度评分、分时展开按钮、买入/忽略/模拟买入/收藏操作
  // 排序：代码/名称/策略/generated_at 字符串比较，现价/涨跌/总分数值比较（§修复 P2#23 补齐）
  const columns = [
    // 代码列：支持按代码字符串排序
    { colKey: 'code', title: '代码', width: 90, sorter: sorterStr('code') },

    // 名称列：个股简称，无名称时显示 '-'
    { colKey: 'name', title: '名称', width: 100, sorter: sorterStr('name'), cell: ({ row }) => row.name || '-' },

    {
      colKey: 'price', title: '现价/涨跌', width: 130, sorter: sorterNum('price'),
      cell: ({ row }) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span>¥{(row.price || 0).toFixed(2)}</span>
          <span style={{ color: chgColor(row.change_pct) }}>
            {(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%
          </span>
        </div>
      ),
    },
    {
      colKey: 'strategy', title: '策略', width: 130, sorter: sorterStr('strategy'),
      // §SHORT-4 做空战法信号：红「空」徽标 + 动作中文（卖出=持有个股走弱平仓 / 规避=非持仓）
      cell: ({ row }) => (row.direction === '做空' ? (
        <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
          <Tag theme="danger" size="small">空</Tag>
          <span>{row.strategy}</span>
          <Tag theme={row.action === 'sell' ? 'danger' : 'warning'} size="small" variant="light">{row.action === 'sell' ? '卖出' : '规避'}</Tag>
        </span>
      ) : row.strategy),
    },
    {
      // §FIX-0921 信号产生时间列（2026-09-01 用户需求）：展示信号生成时间戳，
      // 用户可判断信号新旧（此前只有现价/策略，无法区分是早盘还是午后产生的信号）
      colKey: 'generated_at', title: '产生时间', width: 155,
      sorter: sorterStr('generated_at'),
      cell: ({ row }) => <span style={{ fontSize: 12, color: '#888' }}>{row.generated_at || '-'}</span>,
    },
    {
      colKey: 'total_score', title: '总分', width: 70, sorter: sorterNum('total_score'),
      cell: ({ row }) => (row.total_score != null ? row.total_score.toFixed(0) : '—'),
    },

    // 信号等级列：按 level/remind_level 映射为 交易/观望/可开仓/观察/静默 标签
    {
      colKey: 'level', title: '等级', width: 90,
      cell: ({ row }) => {
        const lv = row.level === '交易' ? '交易'
          : row.level === '观望' ? '观望'
          : row.remind_level === 'strong' ? '可开仓'
          : row.remind_level === 'observe' ? '观察' : '静默'
        const theme = row.remind_level === 'strong' ? 'danger' : row.remind_level === 'observe' ? 'warning' : 'default'
        return <Tag theme={theme} size="small">{lv}</Tag>
      },
    },

    // D1-D4 维度评分列：四色标签（红/黄/蓝/绿）展示各维度分数与描述
    {
      colKey: 'detail', title: 'D1/D2/D3/D4', minWidth: 220,
      cell: ({ row }) => (
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', alignItems: 'center' }}>
          {/* D1 事件维度（红） */}
          <span title={'D1事件: ' + (row.d1_reason || row.d1_event || '无事件') + (row.d1_blocked ? '（负面拦截）' : '')}
            style={{ color: '#e34d59', background: 'rgba(227,77,89,0.10)', padding: '0 5px', borderRadius: 3, whiteSpace: 'nowrap' }}>
            {row.d1_score && (row.d1_reason || row.d1_event)
              ? <em style={{ fontStyle: 'normal' }}>{d1Tag(row)}</em>
              : (row.d1 != null ? row.d1.toFixed(0) : '—')}
          </span>
          {/* D2 龙头/动量维度（黄） */}
          <span title={'D2: ' + (row.d2_desc || '')} style={{ color: '#FAAD14', background: 'rgba(250,173,20,0.10)', padding: '0 5px', borderRadius: 3, whiteSpace: 'nowrap' }}>
            {row.d2 != null ? row.d2.toFixed(0) : '—'}{row.d2_desc && <em style={{ fontStyle: 'normal' }}>{shortDesc(row.d2_desc)}</em>}
          </span>
          {/* D3 N形/结构维度（蓝） */}
          <span title={'D3: ' + (row.d3_desc || '')} style={{ color: '#4fc3f7', background: 'rgba(79,195,247,0.10)', padding: '0 5px', borderRadius: 3, whiteSpace: 'nowrap' }}>
            {row.d3 != null ? row.d3.toFixed(0) : '—'}{row.d3_desc && <em style={{ fontStyle: 'normal' }}>{shortDesc(row.d3_desc)}</em>}
          </span>
          {/* D4 基本面/回踩维度（绿） */}
          <span title={'D4: ' + (row.d4_desc || '')} style={{ color: '#00a870', background: 'rgba(0,168,112,0.10)', padding: '0 5px', borderRadius: 3, whiteSpace: 'nowrap' }}>
            {row.d4 != null ? row.d4.toFixed(0) : '—'}{row.d4_desc && <em style={{ fontStyle: 'normal' }}>{shortDesc(row.d4_desc)}</em>}
          </span>
        </div>
      ),
    },

    // 分时展开按钮列：点击展开/收起该信号个股的分时图
    {
      colKey: 'kline', title: '分时', width: 80,
      cell: ({ row }) => (
        <Button size="small" variant="outline" theme="primary"
          onClick={(e) => { e.stopPropagation(); toggleKline(row.code) }}>
          {klineOpen.has(row.code) ? '收起' : '分时'}
        </Button>
      ),
    },

    // 操作列：买入/忽略/模拟买入/收藏按钮，按信号状态与模拟盘开关动态显示
    {
      colKey: 'action', title: '操作', width: 180,
      cell: ({ row }) => (
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {/* 主操作：买入（可开仓）/忽略（已标记）/无操作 */}
          {row.can_open ? (
            <Button size="small" theme="danger" onClick={(e) => { e.stopPropagation(); confirmTrade(row, 'buy') }}>买入</Button>
          ) : row.action === 'buy' ? (
            <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); confirmTrade(row, 'ignore') }}>忽略</Button>
          ) : (
            <span className="muted">—</span>
          )}
          {/* 模拟买入：仅已准入+有策略池的信号 */}
          {paperOn && row.can_open && hasStrategyPool(row) && (
            <Button size="small" variant="outline" theme="success" onClick={(e) => { e.stopPropagation(); paperBuy(row) }}
              title="模拟买入归入该信号所属战法资金池（非战法信号不可买）">模拟买入</Button>
          )}
          {/* 收藏按钮：已忽略信号可收藏到自选股 */}
          {!row.can_open && row.action !== 'buy' && (
            <Button size="small" variant="outline" theme="default" onClick={(e) => { e.stopPropagation(); collectToWatchlist(row) }}>收藏</Button>
          )}
        </div>
      ),
    },
  ]

  // 移动端底部操作面板按钮统一样式
  function sheetBtnStyle(color) {
    return {
      width: '100%', padding: 14, borderRadius: 8, border: 'none',
      background: '#f4f4f5', color, fontSize: 16, cursor: 'pointer', marginBottom: 8, textAlign: 'center',
    }
  }

  {/* 页面整体布局：顶部筛选工具栏 + 信号表格 + 移动端底部操作面板 + 确认弹窗 + 日志弹窗 */}
  return (
    <div className="page">
      {/* 工具栏卡片：页面标题 + 等级筛选按钮组 + 战法下拉筛选 + 日志入口 */}
      <Card style={{ marginBottom: 16 }}>
        <div className="toolbar" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>策略信号</h2>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            {/* 等级筛选按钮组：全部/强/达标/观察 */}
            {FILTERS.map((f) => (
              <Button key={f.key} size="small" variant={activeFilter === f.key ? 'base' : 'outline'}
                theme={activeFilter === f.key ? 'primary' : 'default'} onClick={() => setActiveFilter(f.key)}>
                {f.label}
              </Button>
            ))}
            {/* §SHORT-4 方向筛选（仅做空开启时出现） */}
            {shortEnabled && (
              <Select value={activeDir} onChange={(v) => setActiveDir(v)} size="small" style={{ width: 110 }}
                options={[{ label: '全部方向', value: 'all' }, { label: '做多', value: '做多' }, { label: '做空', value: '做空' }]} />
            )}
            {/* 战法下拉筛选 */}
            <Select value={activeStrategy} onChange={(v) => setActiveStrategy(v)} size="small" style={{ width: 160 }}
              options={[{ label: '全部策略', value: 'all' }, ...strategyOptions.map((st) => ({ label: st, value: st }))]} />
            {/* 日志入口按钮 */}
            <Button size="small" variant="outline" theme="primary" onClick={() => setShowLog(true)}>📋 日志</Button>
          </div>
        </div>
      </Card>

      {/* 信号表格：展示筛选后的策略信号列表，支持表头排序、展开行时图、行点击触发移动端面板 */}
      <Card>
        <Table
          rowKey="code"
          data={filteredSignals}
          columns={columns}
          sort={sort}
          onSortChange={(val) => setSort(val)}
          size="small"
          expandedRow={({ row }) => (
            <MinuteView code={row.code} name={row.name} />
          )}
          expandedRowKeys={[...klineOpen]}
          onExpandChange={(keys) => setKlineOpen(new Set(keys))}
          onRowClick={(ctx) => onRowTap(ctx.row)}
          // §FIX-20260902 信号一页全显：此前 pagination={{ pageSize:10, totalContent:true }}
          // 把第 11+ 条信号藏到第 2 页，且 tdesign 未传 total 时页脚错显「共 0 条数据」、
          // 下一页按钮不可用——「全部 vs 分战法」数量对不上的根因。改为关闭分页直接展示全部。
          pagination={false}
          empty="暂无信号"
        />
      </Card>

      {/* 移动端底部操作面板：点击行时弹出，提供买入/模拟买入/忽略/收藏/分时等快捷操作 */}
      {sheetSignal && (
        <div style={{
          position: 'fixed', inset: 0, zIndex: 300, background: 'rgba(0,0,0,0.6)',
          display: 'flex', alignItems: 'flex-end',
        }} onClick={() => setSheetSignal(null)}>
          <div style={{
            width: '100%', background: '#1a1a2e', borderRadius: '14px 14px 0 0',
            padding: '10px 12px calc(10px + env(safe-area-inset-bottom, 0px))',
          }} onClick={(e) => e.stopPropagation()}>
            <div style={{ fontSize: 14, color: '#999', textAlign: 'center', padding: '8px 0 12px', borderBottom: '1px solid #eef0f3', marginBottom: 8 }}>
              {sheetSignal.code} {sheetSignal.name || ''} · {sheetSignal.strategy}
            </div>
            {sheetSignal.can_open && (
              <button style={sheetBtnStyle('#FF4D4F')} onClick={() => { const s = sheetSignal; setSheetSignal(null); confirmTrade(s, 'buy') }}>买入</button>
            )}
            {/* 模拟买入按钮：仅当信号可开仓且模拟盘已开启且信号属于某战法资金池时显示 */}
            {sheetSignal.can_open && paperOn && hasStrategyPool(sheetSignal) && (
              <button style={sheetBtnStyle('#52c41a')} onClick={() => { const s = sheetSignal; setSheetSignal(null); paperBuy(s) }}>模拟买入</button>
            )}
            {sheetSignal.action === 'buy' && (
              <button style={sheetBtnStyle('#4fc3f7')} onClick={() => { const s = sheetSignal; setSheetSignal(null); confirmTrade(s, 'ignore') }}>忽略</button>
            )}
            {!sheetSignal.can_open && sheetSignal.action !== 'buy' && (
              <button style={sheetBtnStyle('#4fc3f7')} onClick={() => { const s = sheetSignal; setSheetSignal(null); collectToWatchlist(s) }}>收藏</button>
            )}
            <button style={sheetBtnStyle('#4fc3f7')} onClick={() => { toggleKline(sheetSignal.code); setSheetSignal(null) }}>
              {klineOpen.has(sheetSignal.code) ? '收起分时' : '展开分时'}
            </button>
            <button style={{ ...sheetBtnStyle('#888'), background: '#eef0f3' }} onClick={() => setSheetSignal(null)}>取消</button>
          </div>
        </div>
      )}

      {/* 交易确认弹窗：展示信号代码/名称/策略/总分/价格，确认后执行买入或忽略 */}
      <Dialog visible={showConfirm} onClose={() => setShowConfirm(false)} header="确认交易"
        onConfirm={() => doAction(tradeAction)} confirmBtn="确认" cancelBtn="取消">
        <p><strong>{tradeTarget.code}</strong> {tradeTarget.name}</p>
        <p>策略: {tradeTarget.strategy}</p>
        <p>总分: {tradeTarget.total_score != null ? tradeTarget.total_score.toFixed(0) : '—'}</p>
        <p>价格: {tradeTarget.price ? '¥' + tradeTarget.price.toFixed(2) : '—'}</p>
      </Dialog>

      <LogModal visible={showLog} onClose={() => setShowLog(false)} />
    </div>
  )
}
