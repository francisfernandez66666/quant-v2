// ── boardFilter.js：板块判定与过滤的前端单点（2026-11-10 owner 需求） ──
// 与后端 internal/config 的 StockBoardOf/BoardFilterConfig.Allows 共用同一份判定口径：
//   · 688/689 开头 = 科创板（star）
//   · 300/301 开头 = 创业板（chinext）
//   · 43/83/87/88/92 开头（即以 4/8 开头，另含 92x 新段） = 北交所（bse）
// 代码形态兼容 "688001.SH"（带市场后缀）与 "688001"（裸六位）。
// English: frontend single source for board classification & filtering, mirroring the backend
// StockBoardOf — the two must agree or a filtered board would leak back in client-side.

// 取代码的数字前缀（去掉 .SH/.SZ/.BJ 后缀与任意非数字尾巴）
function digitPrefix(code) {
  const s = String(code || '').split('.')[0] || ''
  let i = 0
  while (i < s.length && s[i] >= '0' && s[i] <= '9') i++
  return s.slice(0, i)
}

/**
 * 判断个股代码所属板块。
 * @param {string} code 股票代码（688001.SH / 300175 / 430047.BJ 均可）
 * @returns {string} 'star' | 'chinext' | 'bse' | ''（主板/未知）
 */
export function boardOf(code) {
  const p = digitPrefix(code)
  if (!p) return ''
  if (p.startsWith('688') || p.startsWith('689')) return 'star'
  if (p.startsWith('300') || p.startsWith('301')) return 'chinext'
  // 北交所：43/83/87/88/92 明确列出，另按 owner 口径以 4/8 开头整体归入
  if (p.startsWith('4') || p.startsWith('8') || p.startsWith('92')) return 'bse'
  return ''
}

// 三个板块开关的布尔状态（true=显示该板块）。
// backend 缺键时按「开」处理（历史兼容：没配过开关的账号行为与从前一致）。
export function boardEnabled(cfg, board) {
  if (!cfg) return true
  const key = { star: 'star_enabled', chinext: 'chinext_enabled', bse: 'bse_enabled' }[board]
  if (!key) return true
  const v = cfg[key]
  return v === undefined ? true : !!v
}

/**
 * 按板块开关过滤列表（前端兜底：后端已在下发层过滤，这里只挡旧一轮载荷/缓存残留）。
 * @param {Array} rows 带 code（或 ts_code）字段的行列表
 * @param {object|null} cfg {star_enabled, chinext_enabled, bse_enabled}，null=不过滤
 * @returns {Array} 过滤后的列表
 */
export function filterByBoardFilter(rows, cfg) {
  if (!cfg || !Array.isArray(rows)) return rows
  return rows.filter((r) => {
    const board = boardOf(r.code || r.ts_code || '')
    return board === '' || boardEnabled(cfg, board)
  })
}
