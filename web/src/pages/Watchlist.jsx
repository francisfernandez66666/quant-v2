// ── 自选股页面 Watchlist.jsx ──
// 展示自选股多维评分（N形/龙头/双凸/龙回头/动量），支持添加/删除/排序、展开分时+盘口。
// 纯 TDesign 组件（Table / Card / Tag / Button / Input / Dialog），无自定义 CSS。
import React, { useState, useEffect, useRef } from 'react'
import { Card, Table, Button, Input, Dialog, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import KLineChart from '../components/KLineChart.jsx'
import DepthPanel from '../components/DepthPanel.jsx'

// 自选股列表的 localStorage 缓存键
const CACHE_KEY = 'wl_cache_v1'

// 将自选股列表持久化到 localStorage
function persistCache(stocks) {
  try { localStorage.setItem(CACHE_KEY, JSON.stringify(stocks)) } catch (_) {}
}
// 从 localStorage 读取自选股缓存
function loadCache() {
  try {
    const raw = localStorage.getItem(CACHE_KEY)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr : []
  } catch (_) { return [] }
}

// 根据分数与阈值返回评分单元格的颜色样式
function scoreStyle(score, pass, strongMin) {
  if (!score || score <= 0) return { color: '#555', fontWeight: 600 }
  if (score >= strongMin) return { color: '#e34d59', fontWeight: 600 }
  if (pass) return { color: '#FAAD14', fontWeight: 600 }
  return { color: '#555', fontWeight: 600 }
}

// 安全读取字段值
function val(e, key) {
  const v = e[key]
  if (typeof v === 'string') return v || ''
  return v || 0
}

/**
 * 自选股页面组件
 * 展示多维评分、支持添加/删除/排序与展开分时/盘口。
 * @returns {JSX.Element}
 */
export default function Watchlist() {
  // 自选股列表（含行情与各维度评分）
  const [stocks, setStocks] = useState([])
  // 新增输入框代码
  const [newCode, setNewCode] = useState('')
  // 添加请求进行中标记（防重复提交）
  const [adding, setAdding] = useState(false)
  // 已展开分时图的代码列表
  const [expandedKeys, setExpandedKeys] = useState([])
  // 移动端操作面板对应的自选股
  const [sheetStock, setSheetStock] = useState(null)
  // 轮询定时器（30s）
  const timer = useRef(null)
  // §修复 P2#23：受控排序状态——点击表头排序后持久保留，避免 30s 数据轮询整体替换把排序重置
  // English: P2#23 — controlled sort state keeps the user's column sort across the 30s data poll.
  const [sort, setSort] = useState(null)

  // 初始化：读取缓存、加载数据、启动 30s 轮询
  useEffect(() => {
    setStocks(loadCache())
    load()
    // 每 30s 轮询刷新自选股行情与评分
    timer.current = setInterval(load, 30000)
    return () => { if (timer.current) clearInterval(timer.current) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // 自选股变动时持久化缓存
  useEffect(() => { persistCache(stocks) }, [stocks])

  // 按当前排序键计算展示列表，无排序键时按最高维度分倒序
  const sortedEvals = (() => {
    const arr = [...stocks]
    return arr.sort((a, b) => {
      const sa = Math.max(a.n_score || 0, a.dragon_score || 0, a.db_score || 0, a.dr_score || 0, a.m_score || 0)
      const sb = Math.max(b.n_score || 0, b.dragon_score || 0, b.db_score || 0, b.dr_score || 0, b.m_score || 0)
      return sb - sa
    })
  })()

  // 加载自选行情、评估数据并合并快照信息
  async function load() {
    try {
      const st = await api.fetchStatus()
      const hasEmptyCode = stocks.some((s) => !s.code)
      // 非交易时段且已有关联行情时跳过刷新，避免无谓请求
      if (!api.isTradingSession(st.session) && stocks.length && !hasEmptyCode) return
      api.setLastSession(st.session)
      const [snap, wl, ev] = await Promise.all([
        api.fetchSnapshot(), api.fetchWatchlist(), api.fetchEvaluations(),
      ])
      const wlStocks = (wl.stocks || []).map((c) => (typeof c === 'object' ? c : { code: c }))
      // 归一为 {code} 形式的股票列表再取代码集合
      const codes = wlStocks.map((c) => c.code)
      if (!codes.length) { setStocks([]); return }
      const wlMap = {}
      wlStocks.forEach((c) => { wlMap[c.code] = c })
      const evMap = {}
      if (ev) ev.forEach((e) => { evMap[e.code] = e })
      // 将行情快照、自选股列表、评估数据合并为统一展示行
      // 优先用快照实时数据，回退到评估数据，缺失字段兜底默认值
      const wlRow = (c) => {
        const code = typeof c === 'string' ? c : (c && c.code)
        return {
          code,
          name: wlMap[code]?.name || evMap[code]?.name || code,
          price: Number(wlMap[code]?.price) || 0,
          change_pct: Number(wlMap[code]?.change_pct) || 0,
          n_score: evMap[code]?.n_score || 0, n_pass: evMap[code]?.n_pass || false,
          dragon_score: evMap[code]?.dragon_score || 0, dragon_pass: evMap[code]?.dragon_pass || false,
          db_score: evMap[code]?.db_score || 0, db_pass: evMap[code]?.db_pass || false,
          dr_score: evMap[code]?.dr_score || 0, dr_pass: evMap[code]?.dr_pass || false,
          m_score: evMap[code]?.m_score || 0, m_pass: evMap[code]?.m_pass || false,
        }
      }
      // 组装最终展示列表：优先快照数据，回退评估数据，补全缺失股票
      setStocks(buildDisplayList(snap, ev, codes))
    } catch (_) { /* 保留旧数据 */ }
  }

  // 将快照/评估数据与自选股列表合并为统一展示行数组
  function buildDisplayList(snap, ev, codes) {
    // 快照数据优先：过滤出自选股范围内的股票，合并行情与评估数据
    if (snap && snap.length) {
      const list = snap
        .filter((s) => codes.includes(s.code))
        .map((s) => {
          const base = wlRow(s.code)
          return {
            ...base,
            name: s.name || base.name,
            price: Number(s.price) || base.price,
            change_pct: Number(s.change_pct) ?? base.change_pct,
          }
        })
      // 补全快照中未覆盖的自选股
      const known = {}
      list.forEach((s) => { known[s.code] = true })
      for (const c of codes) {
        if (!known[c]) list.push(wlRow(c))
      }
      return list
    }
    // 回退到评估数据
    if (ev && ev.length) {
      return ev.filter((e) => codes.includes(e.code)).map((e) => wlRow(e.code))
    }
    return []
  }

  // 添加新自选股代码并立即同步后端
  async function add() {
    const code = (newCode || '').trim()
    if (!code || adding) return
    setAdding(true)
    try {
      // 调用后端添加接口，返回新股票信息
      const res = await api.addWatchlist(code)
      setNewCode('')
      if (res && res.stock) {
        // 后端返回完整股票信息：用返回数据构建展示行
        const row = {
          code: res.stock.code || code,
          name: res.stock.name || code,
          price: res.stock.price || 0,
          change_pct: res.stock.change_pct || 0,
          // 各维度评分初始化为 0，等待下一轮评估刷新
          n_score: 0, n_pass: false,
          dragon_score: 0, dragon_pass: false,
          db_score: 0, db_pass: false,
          dr_score: 0, dr_pass: false,
          m_score: 0, m_pass: false,
        }
        // 去重后追加到列表末尾
        setStocks((prev) => [...prev.filter((s) => s.code !== row.code), row])
      } else if (!res || !res.duplicate) {
        // 后端未返回股票信息且非重复：仅用代码构建基础行
        setStocks((prev) => [...prev, { code, name: code, price: 0, change_pct: 0 }])
      }
      MessagePlugin.success('已添加 ' + code)
    } catch (e) { MessagePlugin.error('添加失败: ' + (e.message || '')) }
    setAdding(false)
  }

  // 删除指定自选股
  async function remove(code) {
    try {
      await api.removeWatchlist(code)
      setStocks((prev) => prev.filter((s) => s.code !== code))
      MessagePlugin.success('已移除 ' + code)
    } catch (e) { MessagePlugin.error('删除失败: ' + (e.message || '')) }
  }

  // 展开/收起指定代码的分时图
  function toggleKline(code) {
    setExpandedKeys((prev) => prev.includes(code) ? prev.filter((c) => c !== code) : [...prev, code])
  }

  // 移动端点击行时打开底部操作面板（桌面端不响应）
  function onRowTap(e) {
    if (window.innerWidth > 768) return
    setSheetStock(e)
  }

  // 自选股表格列定义：代码、名称、现价、涨跌，以及 N形/龙头/双凸/龙回头/动量
  // 五个维度评分（可排序），K线展开与删除操作
  const columns = [
    // 代码列：蓝色等宽字体展示，支持按代码排序
    { colKey: 'code', title: '代码', width: 90, sorter: (a, b) => (a.code || '').localeCompare(b.code || ''), cell: ({ row }) => <span style={{ color: '#4fc3f7', fontFamily: 'monospace' }}>{row.code}</span> },
    // 名称列：灰色字体，支持按名称排序
    { colKey: 'name', title: '名称', width: 90, sorter: (a, b) => (a.name || '').localeCompare(b.name || ''), cell: ({ row }) => <span style={{ color: '#ccc' }}>{row.name || '-'}</span> },
    // 现价列：带人民币符号，支持按价格排序
    { colKey: 'price', title: '现价', width: 90, sorter: (a, b) => (a.price || 0) - (b.price || 0), cell: ({ row }) => '¥' + (row.price || 0).toFixed(2) },
    // 涨跌幅列：红涨绿跌配色，支持按涨跌排序
    { colKey: 'change_pct', title: '涨跌', width: 100, sorter: (a, b) => (a.change_pct || 0) - (b.change_pct || 0), cell: ({ row }) => <span style={{ color: (row.change_pct || 0) >= 0 ? '#e34d59' : '#00a870', fontWeight: 600 }}>{(row.change_pct || 0) > 0 ? '+' : ''}{(row.change_pct || 0).toFixed(2)}%</span> },
    // N形评分列：≥80红色强势，≥60黄色达标，<60灰色偏低
    { colKey: 'n_score', title: 'N≥60', width: 70, sorter: (a, b) => (a.n_score || 0) - (b.n_score || 0), cell: ({ row }) => { const c = scoreStyle(row.n_score, row.n_pass, 80); return <span style={c}>{row.n_score > 0 ? row.n_score.toFixed(0) : '—'}</span> } },
    // 龙头评分列：≥70买入，50-70观察
    { colKey: 'dragon_score', title: '龙≥70', width: 70, sorter: (a, b) => (a.dragon_score || 0) - (b.dragon_score || 0), cell: ({ row }) => { const c = scoreStyle(row.dragon_score, row.dragon_pass, 80); return <span style={c}>{row.dragon_score > 0 ? row.dragon_score.toFixed(0) : '—'}</span> } },
    // 双凸评分列：≥70买入，50-70观察
    { colKey: 'db_score', title: '凸≥70', width: 70, sorter: (a, b) => (a.db_score || 0) - (b.db_score || 0), cell: ({ row }) => { const c = scoreStyle(row.db_score, row.db_pass, 80); return <span style={c}>{row.db_score > 0 ? row.db_score.toFixed(0) : '—'}</span> } },
    // 龙回头评分列：≥60入场信号
    { colKey: 'dr_score', title: '回≥60', width: 70, sorter: (a, b) => (a.dr_score || 0) - (b.dr_score || 0), cell: ({ row }) => { const c = scoreStyle(row.dr_score, row.dr_pass, 80); return <span style={c}>{row.dr_score > 0 ? row.dr_score.toFixed(0) : '—'}</span> } },
    // 动量评分列：≥50关注
    { colKey: 'm_score', title: '量≥50', width: 70, sorter: (a, b) => (a.m_score || 0) - (b.m_score || 0), cell: ({ row }) => { const c = scoreStyle(row.m_score, row.m_pass, 70); return <span style={c}>{row.m_score > 0 ? row.m_score.toFixed(0) : '—'}</span> } },
    // K线展开按钮：点击展开/收起分时图与盘口
    { colKey: 'kline', title: 'K线', width: 80, cell: ({ row }) => <Button size="small" variant="outline" theme="primary" onClick={(e) => { e.stopPropagation(); toggleKline(row.code) }}>{expandedKeys.includes(row.code) ? '收起' : '分时'}</Button> },
    // 删除按钮：从自选股列表中移除该股票
    { colKey: 'op', title: '操作', width: 70, cell: ({ row }) => <Button size="small" variant="outline" theme="danger" onClick={(e) => { e.stopPropagation(); remove(row.code) }}>✕</Button> },
  ]

  // 渲染自选股表格：可排序列、展开行显示分时图与盘口面板
  function renderStockTable() {
    // 展开行内容：左侧K线分时图 + 右侧盘口深度面板
    const expandedContent = ({ row }) => (
      <div style={{ display: 'flex', gap: 12, alignItems: 'stretch', flexWrap: 'wrap' }}>
        <div style={{ flex: '1 1 auto', minWidth: 0 }}><KLineChart key={row.code} code={row.code} name={row.name} /></div>
        <div style={{ flex: '0 0 300px' }}><DepthPanel code={row.code} name={row.name} /></div>
      </div>
    )
    // 表格排序变化回调
    const handleSortChange = (val) => setSort(val)
    // 展开行变化回调
    const handleExpandChange = (keys) => setExpandedKeys(keys)
    // 表格主体：支持列排序、展开行、30s 轮询数据刷新
    const tableBody = (
      <Table
        data={sortedEvals}
        columns={columns}

        sort={sort}
        onSortChange={handleSortChange}

        rowKey="code"
        size="small"
        pagination={false}
        // §F1 自选列表固定表头（自选多时表头随滚消失）
        fixedHeader
        maxHeight="calc(100vh - 300px)"

        expandOnRowClick={false}
        expandedRowKeys={expandedKeys}
        onExpandChange={handleExpandChange}
        expandedRow={expandedContent}
      />
    )
    return <Card>{tableBody}</Card>
  }

  /* 自选股页面主渲染：工具栏 → 表格(可展开分时+盘口) → 评分图例 → 移动端操作面板 */
  return (
    <div className="page">
      {/* 顶部工具栏：标题 + 新增自选股输入框 */}
      <Card style={{ marginBottom: 16 }}>
        <div className="toolbar" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>自选股</h2>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <Input value={newCode} placeholder="输入代码 (如 000001)" onChange={(v) => setNewCode(v)} onEnter={() => add()} disabled={adding} style={{ width: 200 }} />
            <Button theme="primary" onClick={add} loading={adding}>{adding ? '添加中…' : '添加'}</Button>
          </div>
        </div>
      </Card>

      {/* 自选股表格：可排序列、展开行显示分时图与盘口面板 */}
      {stocks.length > 0 ? renderStockTable() : (
        <Card><div className="muted" style={{ padding: 24, textAlign: 'center' }}>暂无自选股，输入代码添加</div></Card>
      )}

      {/* 评分图例：颜色含义 + 各维度操作阈值说明 */}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', fontSize: 12, color: '#888', marginTop: 12 }}>
        <span style={{ color: '#e34d59' }}>≥80 强势</span>
        <span style={{ color: '#FAAD14' }}>≥门槛 达标</span>
        <span style={{ color: '#555' }}>&lt;门槛 偏低</span>
        <span style={{ color: '#555' }}>|</span>
        <span>N形≥60操作, 龙头≥70买入/≥50观察, 双凸≥70买入/50-70观察, 回头≥60入场, 动量≥50关注</span>
        <span style={{ color: '#555' }}>|</span>
        <span>点击表头排序</span>
      </div>

      {/* 移动端底部操作面板：展开分时、删除、取消 */}
      <Dialog
        visible={!!sheetStock}
        header={(sheetStock ? sheetStock.code : '') + ' ' + (sheetStock ? sheetStock.name || '' : '')}
        onClose={() => setSheetStock(null)}
        footer={false}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {/* 分时展开/收起按钮 */}
          <Button theme="primary" variant="outline" onClick={() => { if (sheetStock) toggleKline(sheetStock.code); setSheetStock(null) }}>
            {sheetStock && expandedKeys.includes(sheetStock.code) ? '收起分时' : '展开分时'}
          </Button>
          <Button theme="danger" variant="outline" onClick={() => { const c = sheetStock && sheetStock.code; setSheetStock(null); if (c) remove(c) }}>删除</Button>
          <Button theme="default" onClick={() => setSheetStock(null)}>取消</Button>
        </div>
      </Dialog>
    </div>
  )
}
