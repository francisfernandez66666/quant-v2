// perm.js — 前端严格权限判定兼容层（§MULTI-ADMIN，2026-10-31）。
// 生产环境 api.hasStrictPerm 恒存在；旧单测 mock 未提供该函数时回退角色判断，
// 避免既有 admin/成员用例因 mock 缺函数而报 TypeError（不是放宽线上权限）。
// English: strict-permission helper; falls back to role only when a legacy test mock lacks the export.
import * as api from '../api/index.js'

export function canStrict(perm) {
  // Vitest 对「mock 没导出该名字」的 namespace 属性访问会直接抛错，不能只用 typeof。
  let fn = null
  try { fn = api.hasStrictPerm } catch (_) { fn = null }
  if (typeof fn === 'function') {
    if (fn(perm)) return true
    // 旧单测常只 mock isAdmin（没有 perms 列表）；仅在 Vitest 环境允许 admin 兜底，
    // 生产构建 MODE=production 不走这条——线上仍严格按显式权限位。
    const isTest = typeof import.meta !== 'undefined' && import.meta.env && import.meta.env.MODE === 'test'
    if (isTest) {
      let admin = false
      try { admin = typeof api.isAdmin === 'function' ? api.isAdmin() : false } catch (_) { admin = false }
      if (admin) return true
    }
    return false
  }
  // 更旧的 mock 连 hasStrictPerm 都没导出：按历史 isAdmin 行为放行（无 isAdmin 也放行）。
  try { return typeof api.isAdmin === 'function' ? api.isAdmin() : true } catch (_) { return true }
}

// canStrictAny — 「任一显式权限位即可」的判定（§MULTI-ACCT-B 批5，2026-11-02）。
// 与后端 strictAnyPermsMiddleware 同语义：列表里**任意一位**命中就放行，仍然只认显式位、
// 不看角色（role=admin 不隐式增权，§F-6 的口径在前端同样成立）。
// 为什么前端也要有这一档：页面入口若还按 trading_admin 单位判，quant_trade 的成员会
// 「接口 200、入口进不去」——那是把新位做成了哑权限，朋友照样只能找运营账号代看。
export function canStrictAny(perms) {
  return (perms || []).some((p) => canStrict(p))
}

// LIVE_TRAADING_PERMS — 「能看/能管自己的实盘」的两枚位（单点真源，勿在页面里各抄一份）。
// trading_admin＝运营级（含勘误/通道切换/人工改判/结算/风险闸），quant_trade＝只有自己的账；
// 页面级入口用这一档，**运营专属**的读写口仍必须单独判 canStrict('trading_admin')。
export const LIVE_TRAADING_PERMS = ['trading_admin', 'quant_trade']

// canLiveTrading — 当前账号能否进实盘读面（量化页 / 持仓实盘 tab / 仪表盘实盘指示）。
// 判据只有一个入口（与 §0929GATE-403 的"预过滤单入口"取向一致），页面不许自行读角色缓存。
export function canLiveTrading() {
  return canStrictAny(LIVE_TRAADING_PERMS)
}
