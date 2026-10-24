// 自选代码校验（2026-10-24）：当前只开放 A 股股票。
// 规则必须与服务端 internal/server/handlers_fix.go::normalizeWatchStockCode 同源；
// 指数/基金/可转债等资产类型待到证券解析器落地后再开放，这里先明确拒绝，避免把
// 110043（沪市转债）、510300（ETF）、399001（指数）当股票写进自选。
const SH_PREFIXES = ['600', '601', '603', '605', '688', '689']
const SZ_PREFIXES = ['000', '001', '002', '003', '300', '301']
const BJ_PREFIXES = ['43', '83', '87', '92']

function stockMarket(code) {
  if (SH_PREFIXES.some((p) => code.startsWith(p))) return 'SH'
  if (SZ_PREFIXES.some((p) => code.startsWith(p))) return 'SZ'
  if (BJ_PREFIXES.some((p) => code.startsWith(p))) return 'BJ'
  return ''
}

function allDigits(s) {
  if (!s) return false
  for (let i = 0; i < s.length; i++) {
    if (s[i] < '0' || s[i] > '9') return false
  }
  return true
}

/**
 * 归一化并校验自选股票代码。
 * @param {string} raw 用户输入（支持 600519 / 600519.SH / SH600519）
 * @returns {{code: string, market: string, error: string}}
 */
export function normalizeStockCode(raw) {
  const s = String(raw == null ? '' : raw).trim().toUpperCase()
  if (!s) return { code: '', market: '', error: '请输入股票代码' }

  let code = s
  let market = ''
  const dot = s.indexOf('.')
  if (dot >= 0) {
    const parts = s.split('.')
    if (parts.length !== 2) return { code: '', market: '', error: '股票代码格式不正确' }
    code = parts[0]
    market = parts[1]
  } else if (s.length === 8 && ['SH', 'SZ', 'BJ'].includes(s.slice(0, 2))) {
    market = s.slice(0, 2)
    code = s.slice(2)
  }

  if (code.length !== 6 || !allDigits(code)) {
    return { code: '', market: '', error: '股票代码须为 6 位数字（可带 .SH/.SZ/.BJ 后缀）' }
  }
  const expected = stockMarket(code)
  if (!expected) {
    return { code: '', market: '', error: '暂只支持 A 股股票代码（指数/基金/可转债后续开放）' }
  }
  if (market && market !== expected) {
    return { code: '', market: '', error: `市场与代码不匹配：${code} 应为 ${expected}` }
  }
  return { code, market: expected, error: '' }
}
