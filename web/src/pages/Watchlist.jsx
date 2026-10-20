// ── 自选股页面 Watchlist.jsx ──
// 展示自选股多维评分（N形/龙头/双凸/龙回头/动量），支持添加/删除/排序、展开分时+盘口。
// 纯 TDesign 组件（Table / Card / Tag / Button / Input / Dialog），无自定义 CSS。
import React, { useState, useEffect, useRef } from 'react'
import { Card, Table, Button, Input, Dialog, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）
import { usePolling } from '../utils/usePolling.js' // §N-09 统一可见性轮询生命周期
import { useLoadLedger } from '../utils/loadLedger.js' // §F-14 读取失败按腿记账
import LoadFailBanner from '../components/LoadFailBanner.jsx' // §F-14 统一失败红条
import {
  PriceCell as SharedPriceCell, PctCell as SharedPctCell,
  priceOrNil, pctOrNil, WL_NO_DATA_PLACEHOLDER as SHARED_NO_DATA,
} from '../components/priceFormat.jsx' // §F-19 价格渲染全站单实现
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

// §F-21 / §N-11d（2026-10-20）：缓存从「裸数组」升级为「带快照时刻的信封」。
// 为什么必须带 saved_at：进页允许先显示上一份缓存，但屏上必须能回答“这是几点的行情”；
// 没有时刻的旧数组无法标注，按当帧失效处理（不显示、不续命）。
// 72h TTL 只兜“跨好几个交易日还在拿旧价”；真正的兜底是每次进页必回源（见 load(true)）。
const CACHE_VERSION = 2
const CACHE_TTL_MS = 72 * 3600 * 1000

// 将自选股列表与快照时刻一起持久化；没有真值时刻的列表一律不写回（陈旧自我续命就是这么来的）
function persistCache(rows, savedAt) {
  if (!savedAt) return
  // §P2-J 可吞：localStorage 写失败只影响首帧缓存，不影响回源主链
  try {
    localStorage.setItem(cacheKeyForAccount(), JSON.stringify({ v: CACHE_VERSION, saved_at: savedAt, rows }))
  } catch (_) {
    // §P2-J 可吞：localStorage 写失败只影响首帧缓存，不影响回源主链
  }
}
// 读取缓存：旧数组格式（无 saved_at）与超过 TTL 的缓存一律当作没有缓存
function loadCache() {
  // §P2-J 可吞：缓存读坏按无缓存处理，主数据仍会强制回源
  try {
    const legacy = localStorage.getItem(CACHE_KEY)
    if (legacy) localStorage.removeItem(CACHE_KEY)
    const raw = localStorage.getItem(cacheKeyForAccount())
    if (!raw) return { rows: [], savedAt: null }
    const parsed = JSON.parse(raw)
    if (!parsed || parsed.v !== CACHE_VERSION || !parsed.saved_at || !Array.isArray(parsed.rows)) {
      return { rows: [], savedAt: null }
    }
    const at = new Date(parsed.saved_at)
    if (!Number.isFinite(at.getTime()) || Date.now() - at.getTime() > CACHE_TTL_MS) {
      return { rows: [], savedAt: null }
    }
    return { rows: parsed.rows, savedAt: parsed.saved_at }
    // §P2-J 可吞：缓存读坏按无缓存处理，主数据仍会强制回源
  } catch (_) { return { rows: [], savedAt: null } }
}
// 清空本账号缓存（自选被删空时使用，避免下一次进页仍闪出已删除的行）
function clearCache() {
  // §P2-J 可吞：清缓存失败不影响列表正确性，下一次回源会覆盖
  try {
    localStorage.removeItem(cacheKeyForAccount())
  } catch (_) {
    // §P2-J 可吞：清缓存是旁路动作，失败不阻断主链
  }
}
// 缓存徽标文案：只写“截至”，不得出现“实时”二字
function formatCacheAt(savedAt) {
  const d = new Date(savedAt)
  if (!Number.isFinite(d.getTime())) return ''
  const p = (n) => String(n).padStart(2, '0')
  return (d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes())
}

// 根据分数与阈值返回评分单元格的颜色样式
function scoreStyle(score, pass, strongMin) {
  if (!score || score <= 0) return { color: 'var(--app-text-2)', fontWeight: 600 }
  if (score >= strongMin) return { color: 'var(--app-up)', fontWeight: 600 }
  if (pass) return { color: 'var(--td-warning-color)', fontWeight: 600 }
  return { color: 'var(--app-text-2)', fontWeight: 600 }
}

// §P3-FE P8/P9 / §F-19：缺数渲染的实现已抽到 components/priceFormat.jsx（表格与个股抽屉共用一份）。
// 这里保留同名导出，既有列定义、单测与调用方不需要改 import；占位符仍是 U+2014「—」。
export const WL_NO_DATA_PLACEHOLDER = SHARED_NO_DATA

/**
 * 现价单元格（薄封装，真正实现见 components/priceFormat.jsx）。
 * @param {{value: number|null|string}} props
 * @returns {JSX.Element}
 */
export function PriceCell(props) {
  return <SharedPriceCell {...props} />
}

/**
 * 涨跌幅单元格（薄封装，真正实现见 components/priceFormat.jsx）。
 * @param {{value: number|null|string}} props
 * @returns {JSX.Element}
 */
export function PctCell(props) {
  return <SharedPctCell {...props} />
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
  // §P2-I（2026-10-06 修复批 波 6）60s 轮询代号守卫：上一轮还在途时下一轮已发出，
  // 旧响应迟到会把新一轮刚写好的行情/评分覆盖回去（数据倒挂，且一直显示到下次轮询）。
  // 本页自 §M-10 起就漏在守卫外——自选页恰好是"评分列"最需要新鲜读数的地方。
  const loadGuard = useStaleGuard()
  // §修复 P2#23：受控排序状态——点击表头排序后持久保留，避免 30s 数据轮询整体替换把排序重置
  // English: P2#23 — controlled sort state keeps the user's column sort across the 30s data poll.
  const [sort, setSort] = useState(null)

  // 首帧缓存徽标：null=屏上已是回源真值；非空=仍在展示该时刻的缓存
  const [cacheAt, setCacheAt] = useState(null)
  // 手动刷新进行中标记
  const [refreshing, setRefreshing] = useState(false)
  // 最近一次回源成功写入的行情时刻（增删自选时沿用，避免用“现在”冒充行情时刻）
  const lastQuoteAtRef = useRef(null)
  // §F-14：读取失败台账——旧实现只 console.warn，屏上仍显示“暂无自选股”
  const { fails: loadFails, mark: markLoadFail, clear: clearLoadFail } = useLoadLedger()
  const LOAD_LEG = '自选行情'

  // 初始化：读取缓存、**强制回源一次**
  useEffect(() => {
    const cached = loadCache()
    if (cached.rows.length) { setStocks(cached.rows); setCacheAt(cached.savedAt) }
    // §F-21：进页必回源——交易时段只决定“要不要继续轮询”，不再决定“要不要取数”
    load(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // §N-09：60s 轮询统一走 usePolling（后台标签页暂停、恢复可见立即补一次、卸载清理）
  usePolling(() => load(false), 60000)

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
  // force=true：进页首轮/手动刷新——无论交易时段一律回源（F-21 的核心契约）
  async function load(force = false) {
    // §P2-I：本轮代号在**发起请求前**盖章，每次 await 回来后先判后到再写 state
    const token = loadGuard.begin()
    try {
      const st = await api.fetchStatus()
      if (loadGuard.isStale(token)) return // 后到的旧轮次：整包丢弃，连 session 也不写
      // 会话态先落盘；下面的时段判据只作用于“后续轮询轮次”，不影响进页首轮
      api.setLastSession(st.session)
      const cur = stocksRef.current
      const hasEmptyCode = cur.some((s) => !s.code)
      // §F-21：交易时段只决定“要不要继续轮询”。非交易时段的轮询轮在已有行情时跳过重活；
      // 进页首轮与手动刷新（force）必须回源，否则客户会把上周的缓存当成今天的价。
      if (!force && !api.isTradingSession(st.session) && cur.length && !hasEmptyCode) return
      const [snap, wl, ev] = await Promise.all([
        api.fetchSnapshot(), api.fetchWatchlist(), api.fetchEvaluations(),
      ])
      // §P2-I：三份数据是并行拉的，判定必须落在**合并写 state 之前**（Promise.all 回来即最新
      // 可用判点）；放在 setStocks 之后再判等于已经覆盖完了才丢弃。
      if (loadGuard.isStale(token)) return
      const wlStocks = (wl.stocks || []).map((c) => (typeof c === 'object' ? c : { code: c }))
      // 归一为 {code} 形式的股票列表再取代码集合
      const codes = wlStocks.map((c) => c.code)
      if (!codes.length) {
        lastQuoteAtRef.current = null
        setStocks([])
        setCacheAt(null)
        clearCache()
        clearLoadFail(LOAD_LEG)
        return
      }
      const wlMap = {}
      wlStocks.forEach((c) => { wlMap[c.code] = c })
      const evMap = {}
      if (ev) ev.forEach((e) => { evMap[e.code] = e })
      // §WL-FIX（20260917）：显式传递 wlMap/evMap——旧实现把 wlRow 定义在 load 内、
      // 却被组件级 buildDisplayList 调用，属于跨作用域引用，首屏整表合并必抛
      // ReferenceError(wlRow is not defined) 且被静默 catch 吞掉 → 永远渲染「暂无自选股」，
      // 只有当次会话内手动添加的乐观行可见（即线上「看不到自选/添加后刷新即丢」的根因）。
      // 组装最终展示列表：优先快照数据，回退评估数据，补全缺失股票
      // 组装最终展示列表：优先快照数据，回退评估数据，补全缺失股票
      const merged = buildDisplayList(snap, ev, codes, wlMap, evMap)
      // 只有真正回源成功时才更新缓存与“截至”时刻；失败分支不得写回（陈旧自我续命）
      const nowIso = new Date().toISOString()
      lastQuoteAtRef.current = nowIso
      setStocks(merged)
      setCacheAt(null)
      persistCache(merged, nowIso)
      clearLoadFail(LOAD_LEG)
    } catch (e) {
      // §F-21：失败不写缓存、不清徽标——屏上保留上一份可解释数据，等下一轮/手动刷新
      // §F-14：同时把失败点名进台账，避免屏上落到“暂无自选股”的假空态
      markLoadFail(LOAD_LEG, e && (e.message || String(e)))
      console.warn('WL_LOAD_FAIL', e && (e.stack || e.message || String(e)))
    } finally {
      setRefreshing(false)
    }
  }

  // 手动刷新：客户不必等开盘（F-21④）
  function refresh() {
    if (refreshing) return
    setRefreshing(true)
    load(true)
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
        // 去重后追加到列表末尾；缓存沿用上一次回源时刻，没有真值时刻就不写
        const next = [...stocksRef.current.filter((s) => s.code !== row.code), row]
        setStocks(next)
        persistCache(next, lastQuoteAtRef.current)
      } else if (!res || !res.duplicate) {
        // 后端未返回股票信息且非重复：仅用代码构建基础行
        // §P3-FE P8/P9：行情两键留 null ⇒ 表内出 '--' 且不着色，等下一轮快照补真值
        const next = [...stocksRef.current, { code, name: code, price: null, change_pct: null }]
        setStocks(next)
        persistCache(next, lastQuoteAtRef.current)
      }
      MessagePlugin.success('已添加 ' + code)
    } catch (e) { MessagePlugin.error('添加失败: ' + (e.message || '')) }
    setAdding(false)
  }

  // 删除指定自选股
  async function remove(code) {
    try {
      await api.removeWatchlist(code)
      const next = stocksRef.current.filter((s) => s.code !== code)
      setStocks(next)
      // 删空即清缓存；否则沿用上一次回源时刻（不能用“现在”冒充行情时刻）
      if (next.length) persistCache(next, lastQuoteAtRef.current)
      else clearCache()
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
    { colKey: 'name', title: '名称', width: 90, sorter: (a, b) => (a.name || '').localeCompare(b.name || ''), cell: ({ row }) => <span style={{ color: 'var(--app-faint)' }}>{row.name || WL_NO_DATA_PLACEHOLDER}</span> },
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

        // §F1 展开为受控模式：整行点击不展开分时（避免与移动端行点击打开底部面板冲突），
        // 分时行的展开/收起只由「K线」列按钮与底部面板经 toggleKline 改 expandedKeys
        expandOnRowClick={false}
        expandedRowKeys={expandedKeys}
        onExpandChange={handleExpandChange}
        // §F-16：整行点击接到 onRowTap（窄屏打开底部操作面板）；代码列/按钮列已 stopPropagation，
        // 不会双触发。旧实现 onRowTap 只有定义、从未挂到 Table 上。
        onRowClick={(ctx) => onRowTap(ctx && ctx.row ? ctx.row : ctx)}
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
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            {/* §N-11d：首帧缓存必须能看出“这是几点的行情”；回源成功后徽标消失 */}
            {cacheAt && (
              <span className="muted" data-testid="wl-cache-badge" style={{ fontSize: 12 }}>
                缓存数据 · 截至 {formatCacheAt(cacheAt)}
              </span>
            )}
            <Input value={newCode} placeholder="输入代码 (如 000001)" onChange={(v) => setNewCode(v)} onEnter={() => add()} disabled={adding} style={{ width: 200 }} />
            <Button theme="primary" onClick={add} loading={adding}>{adding ? '添加中…' : '添加'}</Button>
            {/* §F-21④：显式刷新入口，客户不必等到开盘 */}
            <Button variant="outline" onClick={refresh} loading={refreshing}>{refreshing ? '刷新中…' : '刷新'}</Button>
          </div>
        </div>
      </Card>

      {/* §F-14：读取失败红条（下方保留上一轮读数或明确失败态） */}
      <LoadFailBanner fails={loadFails} page="Watchlist" />

      {/* 自选股表格：可排序列、展开行显示分时图与盘口面板 */}
      {stocks.length > 0 ? renderStockTable() : (
        <Card><div className="muted" style={{ padding: 24, textAlign: 'center' }}>
          {Object.keys(loadFails).length > 0 ? '自选行情读取失败，请点上方「刷新」重试' : '暂无自选股，输入代码添加'}
        </div></Card>
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
