// ── 消息中心页面 MsgCenter.jsx ──
// 展示所有提醒/告警消息，支持按等级过滤、交易信号二级战法分类、删除/清空、一键模拟卖出
// 使用 TDesign React 组件（Card / Tag / Button / Select / Dialog）。
import React, { useState, useEffect, useMemo } from 'react'
import { Card, Tag, Button, Select, DialogPlugin, MessagePlugin } from 'tdesign-react'
import * as api from '../api/index.js'
import StockDetailDrawer from '../components/StockDetailDrawer.jsx'
import useSseRefresh from '../useSseRefresh.js'

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
  { key: 'review', label: '盘后复盘' },
]

// 涨跌配色（红涨绿跌）— 此处仅用于"收益/亏损"语义外的边框，按 Vue 原色映射
function levelTagTheme(level) {
  if (level === '止损' || level === '策略信号') return 'danger'
  if (level === '交易信号') return 'success'
  if (level === '止盈' || level === '加仓') return 'success'
  if (level === '减仓') return 'warning'
  if (level === '复盘') return 'primary'
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
  // §SHORT-4 做空显隐（决策⑤）：开关关闭时隐藏做空方向/做空战法消息
  const [shortEnabled, setShortEnabled] = useState(false)
  // §F3 全局个股详情抽屉的目标（{code,name}），null=关闭
  const [detail, setDetail] = useState(null)
  // §F5 分页：消息卡片列表按页展示（默认 50/页），筛选/类型变化时回到首页。
  const [page, setPage] = useState(1)
  // §DAILY_REVIEW 手动触发复盘标志（按钮 loading）
  const [reviewing, setReviewing] = useState(false)
  const PAGE_SIZE = 50

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
    // §SHORT-4 做空消息显隐：关闭时过滤掉做空方向与四做空战法消息
    if (!shortEnabled) {
      const bearTactics = ['高位滞涨', '放量破位', '龙头断板', '利好兑现砸盘']
      list = list.filter(a => a.direction !== '做空' && !bearTactics.includes(a.strategy))
    }
    // 按等级过滤
    if (activeFilter === 'hit') list = list.filter(a => a.level === '命中提醒')
    if (activeFilter === 'trade') list = list.filter(a => a.level === '交易信号')
    if (activeFilter === 'strategy') list = list.filter(a => a.level === '策略信号')
    if (activeFilter === 'stop') list = list.filter(a => a.level === '止盈' || a.level === '止损')
    if (activeFilter === 'hold') list = list.filter(a => a.level === '持仓提示')
    // §DAILY_REVIEW 盘后复盘（level=复盘，按日更新覆盖）
    if (activeFilter === 'review') list = list.filter(a => a.level === '复盘')
    // 交易信号二级筛选：按战法名称
    if (activeFilter === 'trade' && activeStrategy !== 'all') {
      list = list.filter(a => a.strategy === activeStrategy)
    }
    // 非策略信号且非预期差战法时，过滤掉预期差
    if (activeFilter !== 'strategy' && activeStrategy !== '预期差') {
      list = list.filter(a => a.strategy !== '预期差')
    }
    return list
  }, [alerts, activeFilter, activeStrategy, shortEnabled])

  // §F5 分页：筛选条件变化回到首页；page 越界时钳制（删除/筛选后总数变小）。
  useEffect(() => { setPage(1) }, [activeFilter, activeStrategy, shortEnabled])
  const totalPages = Math.max(1, Math.ceil(filteredAlerts.length / PAGE_SIZE))
  const curPage = Math.min(page, totalPages)
  const pagedAlerts = filteredAlerts.slice((curPage - 1) * PAGE_SIZE, curPage * PAGE_SIZE)

  // 根据消息等级与方向返回卡片左边框颜色
  function alertBorder(a) {
    if (a.level === '止损' || a.level === '策略信号') return 'var(--app-up)'
    if (a.level === '交易信号') {
      return a.direction === '做空' || a.action === '卖出' ? 'var(--app-up)' : 'var(--app-down)'
    }
    if (a.level === '止盈' || a.level === '加仓') return 'var(--app-down)'
    if (a.level === '减仓') return 'var(--td-warning-color)'
    // §DAILY_REVIEW 复盘卡：后市倾向 偏多→红 / 偏空→绿（涨跌语义）/ 其余→强调色
    if (a.level === '复盘') {
      if (a.direction === '偏多') return 'var(--app-up)'
      if (a.direction === '偏空') return 'var(--app-down)'
      return 'var(--app-accent)'
    }
    return 'var(--app-accent)'
  }

  // 提取消息对应的建议动作（买入/卖出/持有）
  function actionText(a) {
    if (a.level === '复盘') return a.action || a.direction || '中性' // §DAILY_REVIEW 复盘展示后市倾向
    if (a.level === '交易信号' || a.level === '策略信号') {
      return (a.action === '卖出') ? '卖出' : '买入'
    }
    return a.title && a.title.includes('卖出') ? '卖出' : (a.title && a.title.includes('买入')) ? '买入' : '持有'
  }

  // 根据建议动作（买入/卖出/持有）返回操作标签的主题色
  function actionTagTheme(a) {
    const t = actionText(a)
    if (a.level === '复盘') return t === '偏多' ? 'primary' : (t === '偏空' ? 'warning' : 'default')
    if (t === '买入') return 'success'
    if (t === '卖出') return 'danger'
    return 'default'
  }

  // 将消息时间格式化为「YYYY-MM-DD HH:MM:SS」；优先用生成时间 generated_at，回退触发时间 time
  function fmtMsgTime(a) {
    const raw = a.generated_at || a.time || ''
    if (!raw) return ''
    const d = new Date(raw)
    if (isNaN(d.getTime())) return raw // 已是可读字符串则原样返回
    // 两位补零：月/日/时/分/秒统一两位展示
    const p = (n) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  }

  // 加载消息列表并过滤掉日历类消息
  async function load() {
    try {
      const all = await api.fetchAlerts()
      setAlerts((all || []).filter(a => a.code !== 'CAL' && !(a.level && a.level.startsWith('日历'))))
    } catch (_) {}
  }

  // §DAILY_REVIEW 手动触发盘后持仓复盘：同步等待 LLM 返回，成功后切到"盘后复盘"筛选并刷新。
  async function onReviewNow() {
    if (reviewing) return
    setReviewing(true)
    try {
      const r = await api.reviewPositions()
      const n = (r && typeof r.reviewed === 'number') ? r.reviewed : 0
      if (n > 0) { MessagePlugin.success(`复盘完成：${n} 只`); setActiveFilter('review') }
      else MessagePlugin.info('本次未生成复盘（无可复盘标的或数据不足）')
      load()
    } catch (e) {
      MessagePlugin.error('复盘失败：' + (e && e.message ? e.message : e))
    } finally {
      setReviewing(false)
    }
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

  // 挂载时加载消息 + 探测做空开关；SSE 刷新/60s 兜底见下方 useSseRefresh（§F5）。
  useEffect(() => {
    load()
    // §SHORT-4 探测做空开关（关闭时消息列表隐藏做空内容）
    api.fetchShortStatus().then((r) => setShortEnabled(!!r.short_enabled)).catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // §F5 SSE 驱动：message/scan/score 到达即刷新，60s 轮询仅兜底。
  useSseRefresh(['message', 'scan', 'score'], load)

  // 渲染单条消息卡片：等级标签、股票信息、时间、操作按钮、标题与正文
  function renderAlertCard(a, i) {
    // 卡片左边框颜色根据消息等级与方向动态设置
    const borderStyle = { marginBottom: 8, borderLeft: `4px solid ${alertBorder(a)}` }
    // 头部行包含：等级标签、股票代码名称、时间、操作按钮
    const headerRow = (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 6 }}>
        <Tag theme={levelTagTheme(a.level)} size="small">{a.level}</Tag>
        {/* §F3 代码可点开全局个股详情抽屉（复用共享组件，携带同码相关消息） */}
        <span
          role="button"
          title="查看个股详情"
          onClick={() => setDetail({ code: a.code, name: a.name })}
          style={{ fontFamily: 'monospace', color: 'var(--app-accent)', fontWeight: 600, cursor: 'pointer' }}
        >{a.code} {a.name}</span>
        <span style={{ color: 'var(--app-text-2)', flex: 1, fontSize: 13 }}>{fmtMsgTime(a)}</span>
        <Tag theme={actionTagTheme(a)} size="small" variant="light">{actionText(a)}</Tag>
        {isSellAlert(a) && (
          <Button size="small" variant="outline" theme="danger" onClick={() => onPaperSell(a)}>模拟卖出</Button>
        )}
        <Button size="small" variant="text" theme="default" onClick={() => onDeleteOne(a)}>✕</Button>
      </div>
    )
    // 卡片主体：标题 + 正文
    const cardBody = (
      <>
        <div style={{ fontSize: 14, color: 'var(--app-text)', fontWeight: 600 }}>{a.title}</div>
        {/* §DAILY_REVIEW 复盘正文含换行（正文+量化事实分隔）→ pre-line 保留排版 */}
        <div style={{ fontSize: 13, color: 'var(--app-muted-2)', marginTop: 4, whiteSpace: 'pre-line' }}>{a.body}</div>
      </>
    )
    return (
      <Card key={a.id || i} style={borderStyle}>
        {headerRow}
        {cardBody}
      </Card>
    )
  }

  /* 消息中心页面主渲染：标题栏 → 等级筛选 → 战法筛选 → 消息卡片列表 */
  return (
    <div className="page">
      {/* 页面标题 + 清空全部按钮 */}
      <Card style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 8 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, margin: 0 }}>消息中心</h2>
          {/* §DAILY_REVIEW 手动复盘 + 清空 */}
          <div style={{ display: 'flex', gap: 8 }}>
            <Button theme="primary" variant="outline" size="small" loading={reviewing} onClick={onReviewNow}>立即复盘</Button>
            <Button theme="danger" variant="outline" size="small" onClick={onClearAll}>清空全部</Button>
          </div>
        </div>
      </Card>

      {/* 消息等级筛选按钮组：全部/命中提醒/交易信号/策略信号/止盈止损/持仓提示 */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
        {filters.map((f) => (
          <Button key={f.key} size="small" variant={activeFilter === f.key ? 'base' : 'outline'}
            theme={activeFilter === f.key ? 'primary' : 'default'} onClick={() => setActiveFilter(f.key)}>
            {f.label}
          </Button>
        ))}
      </div>

      {/* 交易信号二级筛选：按战法名称过滤（仅选中"交易信号"时显示） */}
      {activeFilter === 'trade' && strategyOptions.length > 0 && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14 }}>
          <span style={{ fontSize: 14, color: 'var(--app-muted)' }}>战法</span>
          <Select value={activeStrategy} onChange={(v) => setActiveStrategy(v)} size="small" style={{ width: 200 }}
            options={[{ label: '全部战法', value: 'all' }, ...strategyOptions.map((s) => ({ label: s, value: s }))]} />
        </div>
      )}

      {/* 消息卡片列表：按等级着色左边框，显示标题/时间/操作按钮（§F5 分页，每页 50 条） */}
      {pagedAlerts.map(renderAlertCard)}

      {/* §F5 分页控件：仅当超过一页时显示 */}
      {filteredAlerts.length > PAGE_SIZE && (
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 12, padding: '12px 0' }}>
          <Button size="small" variant="outline" disabled={curPage <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>上一页</Button>
          <span style={{ color: 'var(--app-muted-2)', fontSize: 13 }}>第 {curPage} / {totalPages} 页 · 共 {filteredAlerts.length} 条</span>
          <Button size="small" variant="outline" disabled={curPage >= totalPages} onClick={() => setPage((p) => Math.min(totalPages, p + 1))}>下一页</Button>
        </div>
      )}

      {/* 空状态提示：无匹配消息时展示 */}
      {filteredAlerts.length === 0 && (
        <div style={{ textAlign: 'center', padding: 60, color: 'var(--app-text-2)' }}>暂无消息</div>
      )}

      {/* §F3 全局个股详情抽屉：实时价 + 分时/盘口 + 同码相关消息 */}
      <StockDetailDrawer open={!!detail} code={detail?.code} name={detail?.name}
        related={{ messages: alerts }} onClose={() => setDetail(null)} />
    </div>
  )
}
