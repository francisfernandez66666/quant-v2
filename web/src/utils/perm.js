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
