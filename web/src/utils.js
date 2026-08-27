// ── 通用格式化工具 ──
// 各页面复用的数值/时间格式化函数，保持与旧 Vue 版一致的展示口径。

/**
 * 格式化为百分比字符串
 * @param {number|string|null} v - 原始数值（如 0.1234）
 * @param {number} digits - 保留小数位数
 * @returns {string} 如 "12.34%" 或 "-"
 */
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
export function fmtNum(v, digits = 2) {
  if (v === null || v === undefined || isNaN(Number(v))) return '-'
  return Number(v).toFixed(digits)
}

/**
 * 格式化为金额（千分位，最多两位小数）
 * @param {number|string|null} v - 原始金额
 * @returns {string} 如 "1,234.56" 或 "-"
 */
export function fmtMoney(v) {
  if (v === null || v === undefined || isNaN(Number(v))) return '-'
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: 2 })
}

/**
 * 根据盈亏值返回 CSS 类名（绿涨红跌）
 * @param {number|string} v - 盈亏值
 * @returns {string} 'pnl-up' | 'pnl-down' | 'pnl-flat'
 */
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
export function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts)
  if (isNaN(d.getTime())) return String(ts)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/**
 * 把任意值规范化为字符串（null/undefined 转为空串）
 * @param {*} v - 原始值
 * @returns {string}
 */
export function toStr(v) {
  return v === null || v === undefined ? '' : String(v)
}
