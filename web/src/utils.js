// ── 通用格式化工具 ──
// 各页面复用的数值/时间格式化函数，保持与旧 Vue 版一致的展示口径。

/**
 * 格式化为百分比字符串
 * @param {number|string|null} v - 原始数值（如 0.1234）
 * @param {number} digits - 保留小数位数
 * @returns {string} 如 "12.34%" 或 "-"
 */
// 百分比格式化（空值返回占位符 -）
export function fmtPct(v, digits = 2) {
  if (v === null || v === undefined || isNaN(Number(v))) return '-'
  return Number(v).toFixed(digits) + '%'
}

/**
 * 格式化为固定小数位数字
 * @param {number|string|null} v - 原始数值
 * @param {number} digits - 保留小数位数
 * @returns {string} 如 "12.34" 或 "-"
 */
// 数值格式化：千分位 + 固定小数位
export function fmtNum(v, digits = 2) {
  if (v === null || v === undefined || isNaN(Number(v))) return '-'
  return Number(v).toFixed(digits)
}

/**
 * 金额（人民币）定两位小数格式化：434.99999999999994 → "¥435.00"
 * §生产 2026-09-16：成交流水 amount=qty×price 的浮点尾数直出，统一走此函数收口。
 * @param {number|string|null} v - 原始金额
 * @returns {string} "¥435.00" 或 "-"
 */
// Currency amount, always 2 decimals (kills float artifacts like 434.99999999999994).
export function fmtCNY2(v) {
  if (v === null || v === undefined || isNaN(Number(v))) return '-'
  return '¥' + Number(v).toFixed(2)
}

/**
 * 格式化为金额（千分位，最多两位小数）
 * @param {number|string|null} v - 原始金额
 * @returns {string} 如 "1,234.56" 或 "-"
 */
// 金额格式化（¥ 前缀 + 千分位）
export function fmtMoney(v) {
  if (v === null || v === undefined || isNaN(Number(v))) return '-'
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: 2 })
}

/**
 * 根据盈亏值返回 CSS 类名（绿涨红跌）
 * @param {number|string} v - 盈亏值
 * @returns {string} 'pnl-up' | 'pnl-down' | 'pnl-flat'
 */
// 盈亏着色类名：正 red / 负 green / 零 flat
export function pnlClass(v) {
  const n = Number(v)
  if (isNaN(n) || n === 0) return 'pnl-flat'
  return n > 0 ? 'pnl-up' : 'pnl-down'
}

/**
 * 简易日期时间格式化（YYYY-MM-DD HH:mm:ss）
 * @param {number|string|Date} ts - 时间戳或日期对象
 * @returns {string} 格式化后的时间字符串或 "-"
 */
// 时间戳→本地可读时间文本
export function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(ts)
  // 两位补零：月/日/时/分/秒统一两位数展示（如 09、05）
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/**
 * 把任意值规范化为字符串（null/undefined 转为空串）
 * @param {*} v - 原始值
 * @returns {string}
 */
// 空值安全的字符串化
export function toStr(v) {
  return v === null || v === undefined ? '' : String(v)
}

/**
 * §UAT-D1（2026-09-16）把资损/运维级 SSE 事件映射为统一的告警展示描述。
 * 后端广播但前端此前零订阅的四个 type（qmt_halt / positions_clear_guard / settlement_diff / trigger）
 * 在此收敛成 {key,title,body,tone}，供 App.jsx 全局 Toast + 系统通知消费；非告警 type 返回 null。
 * 字段口径对齐后端：qmt.go:1052（halted/cancelled）、qmt.go:513（held）、qmt.go:1106+
 * store/settlement.go:18-28（diff.*）、trigger.go:188（signal.code/name/msg）。
 * English: maps the resource-loss/ops-grade SSE events (previously broadcast-but-unsubscribed)
 * to a uniform alert descriptor for the global Toast + system notification.
 */
export function sseOpsAlert(msg) {
  if (!msg || typeof msg !== 'object') return null
  switch (msg.type) {
    case 'qmt_halt':
      // halted=true=置位熔断（伴随撤在途单），false=解除；两者都必须让 UI 立刻知道
      return msg.halted
        ? { key: 'qmt_halt', title: '量仔 实盘熔断', tone: 'error',
          body: '实盘已紧急停止，在途委托撤销 ' + (msg.cancelled || 0) + ' 笔' + (msg.time ? '（' + msg.time + '）' : '') }
        : { key: 'qmt_halt', title: '量仔 实盘恢复', tone: 'success',
          body: '熔断已解除，恢复正常下单' + (msg.time ? '（' + msg.time + '）' : '') }
    case 'positions_clear_guard':
      // 守卫触发：空快照试图清空持仓被拒（qmt.go:495-520），资损级安全事件，红色强提醒
      return { key: 'pcg', title: '量仔 持仓清空守卫', tone: 'error',
        body: '收到空持仓快照但本地仍有 ' + (msg.held || 0) + ' 条持仓，已拒收全清（防断连误清账）'
          + (msg.time ? '（' + msg.time + '）' : '') }
    case 'settlement_diff': {
      const d = msg.diff || {}
      const miss = (d.missing_in_local || []).length
      const extra = (d.extra_in_local || []).length
      const mism = (d.mismatch || []).length
      if (!miss && !extra && !mism && !d.fee_diff && !d.cash_diff) return null
      const parts = []
      if (miss) parts.push('本地缺' + miss)
      if (extra) parts.push('本地多' + extra)
      if (mism) parts.push('不符' + mism)
      if (d.fee_diff) parts.push('费用差' + Number(d.fee_diff).toFixed(2))
      if (d.cash_diff) parts.push('现金差' + Number(d.cash_diff).toFixed(2))
      return { key: 'settlediff', title: '量仔 交割对账差异', tone: 'warning',
        body: (d.day || msg.time || '') + '：' + parts.join('、') + '，请到量化交易页复核' }
    }
    case 'trigger': {
      const s = msg.signal || {}
      if (!s.code && !s.msg) return null
      return { key: 'trg', title: '量仔 实时放量急拉', tone: 'warning',
        body: (s.code || '') + ' ' + (s.name || '') + '：' + (s.msg || '') }
    }
    default:
      return null
  }
}
