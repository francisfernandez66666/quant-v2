// ── 自选股页面 Watchlist.jsx ──
// 展示自选股多维评分（N形/龙头/双凸/龙回头/动量），支持添加/删除/排序、展开分时+盘口。
// 纯 TDesign 组件（Table / Card / Tag / Button / Input / Dialog），无自定义 CSS。
import React, { useState, useEffect, useRef } from 'react'
import { Card, Table, Button, Input, Dialog, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）
import KLineChart from '../components/KLineChart.jsx'
import DepthPanel from '../components/DepthPanel.jsx'
import StockDetailDrawer from '../components/StockDetailDrawer.jsx'

// 自选股列表的 localStorage 缓存键（账号后缀由 cacheKeyForAccount() 拼接）
const CACHE_KEY = 'wl_cache_v1'

// §F29 修复：自选股缓存按账号隔离——旧版共用 CACHE_KEY，同一浏览器切账号后
// 首帧渲染会先闪出前一账号的自选列表（后端返回后被覆盖，但那一瞬已泄露）。
// 迁移：读时若发现无账号后缀的旧键，一次性清除。
// English: F29 — account-scoped watchlist cache; the legacy un-scoped key is purged on read.
function cacheKeyForAccount() {
  const acc = (typeof api.getAccount === 'function' && api.getAccount()) || ''
  return acc ? CACHE_KEY + ':' + acc : CACHE_KEY
}

// 将自选股列表持久化到 localStorage
function persistCache(stocks) {
  try { localStorage.setItem(cacheKeyForAccount(), JSON.stringify(stocks)) } catch (_) {}
}
// 从 localStorage 读取自选股缓存
function loadCache() {
  try {
    const legacy = localStorage.getItem(CACHE_KEY)
    if (legacy) localStorage.removeItem(CACHE_KEY)
    const raw = localStorage.getItem(cacheKeyForAccount())
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr : []
  } catch (_) { return [] }
}

// 根据分数与阈值返回评分单元格的颜色样式
function scoreStyle(score, pass, strongMin) {
  if (!score || score <= 0) return { color: 'var(--app-text-2)', fontWeight: 600 }
  if (score >= strongMin) return { color: 'var(--app-up)', fontWeight: 600 }
  if (pass) return { color: 'var(--td-warning-color)', fontWeight: 600 }
  return { color: 'var(--app-text-2)', fontWeight: 600 }
}

// §P3-FE P8/P9（缺数渲染，20261006 修复批）：自选行原先在**合并阶段**就把「没读到」折成 0
// （`Number(wlMap[code]?.price) || 0` / `Number(s.change_pct) || 0`），表格再无条件渲染成
// 「¥0.00」并按 `>=0` 染成涨红——一只停牌/未进快照覆盖的票，在界面上和「今天正好平盘」长得一模一样，
// 用户按颜色判断涨跌就会读反（与 §P2-F 降级链把断源日写成平盘同族）。
// 现在合并侧保留 null、渲染侧按 null 出 '--' 且不着色，缺数与平盘从此是两种读数。
// 两把尺子必须分开定义，因为 0 在两个字段里的含义不同：
//   price：0 不是合法现价（后端取不到行情就回 0，见 handlers_fix.go 的 price>0 判据）⇒ 只有 >0 算有数；
//   change_pct：0 是合法实测值（平盘）⇒ 只要可解析成有限数就算有数，null/'' /undefined 才算缺数。
function priceOrNil(v) {
  // 现价缺数判据：可解析且 >0 才认，其余（0/NaN/null/undefined）一律 null
  const n = Number(v)
  // 常量 n：局部定义
  return Number.isFinite(n) && n > 0 ? n : null
}
function pctOrNil(v) {
  // 涨跌幅缺数判据：null/空串/不可解析→null；0 与负值都是合法实测读数
  if (v === null || v === undefined || v === '') return null
  const n = Number(v)
  // 常量 n：局部定义
  return Number.isFinite(n) ? n : null
}

// §P3-FE P8/P9 渲染出口（提到模块作用域并 export：整页挂载要 mock 快照/自选/评分/时段四路端点，
// 与「缺数怎么显示」这条断言无关，做法同 Research.jsx 的 isAdjBasisStale；表格里两格的缺数口径
// 只有这一份实现，列定义与单测共用，不会出现「测的是 helper、表里写的是另一套」）。
// 占位符选「—」：与本页评分列既有惯例一致（5 个评分格缺数都写「—」），不另造第二种占位符；
// FIX_PLAN 条目里写的 '--' 是 DepthPanel 那侧的形态，两处各按各页惯例，断言按「同页占位符唯一」钉。
// 缺数一律不着色（var(--app-faint)）：旧写法无条件按 `>=0` 染涨红/跌绿，把「不知道涨跌」显示成「在跌」，
// 用户按颜色判断就会读反（与 §P2-F 降级链把断源日写成平盘同族）。
// English: §P3-FE — the two quote cells' missing-data rendering, single implementation shared by the
// table columns and the unit tests (no color at all when there is no reading).
export const WL_NO_DATA_PLACEHOLDER = '—'

/**
 * 现价单元格：有数显示 ¥xx.xx，缺数（含 0，0 不是合法现价）显示占位符且不着色。
 * @param {{value: number|null|string}} props
 * @returns {JSX.Element}
 */
export function PriceCell({ value }) {
  const p = priceOrNil(value)
  // 常量 p：局部定义
  return p == null
    ? <span style={{ color: 'var(--app-faint)' }} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
    : <span>¥{p.toFixed(2)}</span>
}

/**
 * 涨跌幅单元格：有数按红涨绿跌着色（0 是合法的平盘实测），缺数显示占位符且不着色。
 * @param {{value: number|null|string}} props
 * @returns {JSX.Element}
 */
export function PctCell({ value }) {
  const c = pctOrNil(value)
  // 常量 c：局部定义
  return c == null
    ? <span style={{ color: 'var(--app-faint)' }} data-testid="wl-no-data">{WL_NO_DATA_PLACEHOLDER}</span>
    : <span data-testid="wl-pct" style={{ color: c >= 0 ? 'var(--app-up)' : 'var(--app-down)', fontWeight: 600 }}>{c > 0 ? '+' : ''}{c.toFixed(2)}%</span>
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
  // §F3 全局个股详情抽屉目标（{code,name,price,changePct}），null=关闭
  const [detail, setDetail] = useState(null)
  // 轮询定时器（30s）
  const timer = useRef(null)
  // §P2-I（2026-10-06 修复批 波 6）60s 轮询代号守卫：上一轮还在途时下一轮已发出，
  // 旧响应迟到会把新一轮刚写好的行情/评分覆盖回去（数据倒挂，且一直显示到下次轮询）。
  // 本页自 §M-10 起就漏在守卫外——自选页恰好是"评分列"最需要新鲜读数的地方。
  const loadGuard = useStaleGuard()
  // §修复 P2#23：受控排序状态——点击表头排序后持久保留，避免 30s 数据轮询整体替换把排序重置
  // English: P2#23 — controlled sort state keeps the user's column sort across the 30s data poll.
  const [sort, setSort] = useState(null)

  // 初始化：读取缓存、加载数据、启动 30s 轮询
  useEffect(() => {
    setStocks(loadCache())
    load()
    // 每 30s 轮询刷新自选股行情与评分
    timer.current = setInterval(load, 60000) // §F5 兜底降为 60s
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

  // §WL-FIX（20260917）：库存经 stocksRef 供 load 读取——旧实现直接闭包引用 stocks，
  // 60s 轮询持有的是首帧闭包（恒 []），「非交易时段且已有行情则跳过」的判定永远失真（恒不跳过）。
  // English: WL-FIX — load reads stocksRef.current so the 60s poll no longer sees a stale closure.
  const stocksRef = useRef([])
  useEffect(() => { stocksRef.current = stocks }, [stocks])

  // 加载自选行情、评估数据并合并快照信息
  async function load() {
    // §P2-I：本轮代号在**发起请求前**盖章，每次 await 回来后先判后到再写 state
    const token = loadGuard.begin()
    try {
      const st = await api.fetchStatus()
      if (loadGuard.isStale(token)) return // 后到的旧轮次：整包丢弃，连 session 也不写
      const cur = stocksRef.current
      const hasEmptyCode = cur.some((s) => !s.code)
      // 非交易时段且已有关联行情时跳过刷新，避免无谓请求
      if (!api.isTradingSession(st.session) && cur.length && !hasEmptyCode) return
      api.setLastSession(st.session)
      const [snap, wl, ev] = await Promise.all([
        api.fetchSnapshot(), api.fetchWatchlist(), api.fetchEvaluations(),
      ])
      // §P2-I：三份数据是并行拉的，判定必须落在**合并写 state 之前**（Promise.all 回来即最新
      // 可用判点）；放在 setStocks 之后再判等于已经覆盖完了才丢弃。
      if (loadGuard.isStale(token)) return
      const wlStocks = (wl.stocks || []).map((c) => (typeof c === 'object' ? c : { code: c }))
      // 归一为 {code} 形式的股票列表再取代码集合
      const codes = wlStocks.map((c) => c.code)
      if (!codes.length) { setStocks([]); return }
      const wlMap = {}
      wlStocks.forEach((c) => { wlMap[c.code] = c })
      const evMap = {}
      if (ev) ev.forEach((e) => { evMap[e.code] = e })
      // §WL-FIX（20260917）：显式传递 wlMap/evMap——旧实现把 wlRow 定义在 load 内、
      // 却被组件级 buildDisplayList 调用，属于跨作用域引用，首屏整表合并必抛
      // ReferenceError(wlRow is not defined) 且被静默 catch 吞掉 → 永远渲染「暂无自选股」，
      // 只有当次会话内手动添加的乐观行可见（即线上「看不到自选/添加后刷新即丢」的根因）。
      // 组装最终展示列表：优先快照数据，回退评估数据，补全缺失股票
      setStocks(buildDisplayList(snap, ev, codes, wlMap, evMap))
    } catch (e) { console.warn('WL_LOAD_FAIL', e && (e.stack || e.message || String(e))) }
  }

  // 将快照/评估数据与自选股列表合并为统一展示行数组
  // §WL-FIX：wlRow 提升为本函数内部闭包（同作用域），参数化 wlMap/evMap，不再依赖 load 局部变量。
  function buildDisplayList(snap, ev, codes, wlMap, evMap) {
    // 单行构造：行情快照、自选列表与评估数据合并，缺失字段兜底默认值
    const wlRow = (c) => {
      const code = typeof c === 'string' ? c : (c && c.code)
      return {
        code: typeof code === 'string' ? code : '',
        name: wlMap[code]?.name || evMap[code]?.name || code,
        // §P3-FE P8/P9：缺数保留 null（旧写法 Number(x)||0 把「没读到」写成了「读到 0」）
        price: priceOrNil(wlMap[code]?.price),
        change_pct: pctOrNil(wlMap[code]?.change_pct),
        n_score: evMap[code]?.n_score || 0, n_pass: evMap[code]?.n_pass || false,
        dragon_score: evMap[code]?.dragon_score || 0, dragon_pass: evMap[code]?.dragon_pass || false,
        db_score: evMap[code]?.db_score || 0, db_pass: evMap[code]?.db_pass || false,
        dr_score: evMap[code]?.dr_score || 0, dr_pass: evMap[code]?.dr_pass || false,
        m_score: evMap[code]?.m_score || 0, m_pass: evMap[code]?.m_pass || false,
      }
    }
    // 快照数据优先：过滤出自选股范围内的股票，合并行情与评估数据
    if (snap && snap.length) {
      const list = snap
        .filter((s) => codes.includes(s.code))
        .map((s) => {
          const base = wlRow(s.code)
          return {
            ...base,
            name: s.name || base.name,
            // §P3-FE P8/P9：快照有数用快照、缺数回落到自选列表带来的上一份读数，两者都没有才是 null
            // （§WL-FIX 的教训仍在位：Number() 失败得 NaN，旧写法 `?? ` 对 NaN 不兜底，
            //  故判据统一走 priceOrNil/pctOrNil——它们用 Number.isFinite 把 NaN 也归成「没数」）。
            price: priceOrNil(s.price) ?? base.price,
            change_pct: pctOrNil(s.change_pct) ?? base.change_pct,
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
          // §P3-FE P8/P9：添加返回没带行情时留 null（旧写法写 0 会让新行立刻显示「¥0.00 +0.00%」，
          // 看起来像读到平盘；下一轮快照有数后才会被真值替换）
          price: priceOrNil(res.stock.price),
          change_pct: pctOrNil(res.stock.change_pct),
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
        // §P3-FE P8/P9：行情两键留 null ⇒ 表内出 '--' 且不着色，等下一轮快照补真值
        setStocks((prev) => [...prev, { code, name: code, price: null, change_pct: null }])
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
    { colKey: 'code', title: '代码', width: 90, sorter: (a, b) => (a.code || '').localeCompare(b.code || ''), cell: ({ row }) => <span role="button" title="查看个股详情" onClick={(e) => { e.stopPropagation(); setDetail({ code: row.code, name: row.name, price: row.price, changePct: row.change_pct }) }} style={{ color: 'var(--app-accent)', fontFamily: 'monospace', cursor: 'pointer' }}>{row.code}</span> },
    // 名称列：灰色字体，支持按名称排序
    { colKey: 'name', title: '名称', width: 90, sorter: (a, b) => (a.name || '').localeCompare(b.name || ''), cell: ({ row }) => <span style={{ color: 'var(--app-faint)' }}>{row.name || '-'}</span> },
    // 现价列：带人民币符号，支持按价格排序
    // 涨跌列/现价列的缺数口径（§P3-FE P8/P9）：渲染出口是模块作用域的 PriceCell/PctCell（单实现，
    // 表列与单测共用同一份，见文件头注释）；价格判据走 priceOrNil：0 不是合法现价（缓存里的历史 0 同归缺数）。
    { colKey: 'price', title: '现价', width: 90, sorter: (a, b) => (a.price || 0) - (b.price || 0), cell: ({ row }) => <PriceCell value={row.price} /> },
    // 涨跌幅列：红涨绿跌配色，支持按涨跌排序（缺数见上方口径）
    { colKey: 'change_pct', title: '涨跌', width: 100, sorter: (a, b) => (a.change_pct || 0) - (b.change_pct || 0), cell: ({ row }) => <PctCell value={row.change_pct} /> },
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

        // §F1 展开为受控模式：整行点击不展开（避免与移动端 onRowTap 打开底部面板冲突），
        // 分时行的展开/收起只由「K线」列按钮与底部面板经 toggleKline 改 expandedKeys
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
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', fontSize: 12, color: 'var(--app-muted)', marginTop: 12 }}>
        <span style={{ color: 'var(--app-up)' }}>≥80 强势</span>
        <span style={{ color: 'var(--td-warning-color)' }}>≥门槛 达标</span>
        <span style={{ color: 'var(--app-text-2)' }}>&lt;门槛 偏低</span>
        <span style={{ color: 'var(--app-text-2)' }}>|</span>
        <span>N形≥60操作, 龙头≥70买入/≥50观察, 双凸≥70买入/50-70观察, 回头≥60入场, 动量≥50关注</span>
        <span style={{ color: 'var(--app-text-2)' }}>|</span>
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

      {/* §F3 全局个股详情抽屉：代码点开，实时价 + 分时/盘口 */}
      <StockDetailDrawer open={!!detail} code={detail?.code} name={detail?.name}
        price={detail?.price} changePct={detail?.changePct} onClose={() => setDetail(null)} />
    </div>
  )
}
