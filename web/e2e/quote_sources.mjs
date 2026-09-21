// ── §M1/§F4（FIX_PLAN_20260922，2026-09-22 修复批 K）quote_source 单一事实源读取面 ──
//
// 缺陷原文：引擎 internal/data 侧吐出的 quote_source 恒为小写英文（source.go 的
//   setLastSource("hithink"/"sina"/"ths"/"eastmoney") + qmt_feed.go 的 "QMT-L1" +
//   fetcher.go 主导源标注 "同花顺（新）"），而 E2E 的白名单在 uat_full.spec.mjs 里
//   硬编了一份中文列表——两侧各说各话；再叠加「盘外空串直接放过」，这条断言在 nightly
//   里从未真正执行过（假绿）。
//
// 修法（契约单源化）：白名单不再是 spec 里的字面量，而是读后端导出的 golden 事实源
//   qmt_gateway/contract/quote_sources.json。本文件只做「读 + 解析 + 显式失败」，
//   不复制任何词表内容（复制一份 = 漂移复发）。
//
// golden 缺失时**显式失败**（不是 skip、不是回退旧中文白名单）：该文件由后端修复批
// （§M1 Go 侧）落盘，缺失即代表契约单源化未完成，E2E 必须红给主代理看。
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// GOLDEN_REL 相对仓库根的事实源路径（与 Go 侧契约测试同一文件）。
export const GOLDEN_REL = 'qmt_gateway/contract/quote_sources.json'

// candidatePaths 兼容两种 cwd：playwright 在 web/ 下跑（web/../qmt_gateway/...），
// 也可能从仓库根跑（qmt_gateway/...）。全部列出，取第一个存在的。
function candidatePaths() {
  return [
    path.resolve(HERE, '..', GOLDEN_REL), // web/e2e → web → repo root
    path.resolve(process.cwd(), GOLDEN_REL),
    path.resolve(process.cwd(), '..', GOLDEN_REL),
  ]
}

// collectStrings 从 golden JSON 里取枚举串列表：
//  ① 顶层就是数组 → 直接用；
//  ② 按候选键（quote_sources/sources/values/enum/names）命中第一个字符串数组；
//  ③ 兜底：所有顶层字符串数组的并集（golden 换了键名也不至于失明，但会打 warning）。
// 返回 { sources, matchedBy }；matchedBy 用于失败信息里说清「到底读到了什么」。
function collectStrings(raw) {
  const strs = (arr) => (Array.isArray(arr) ? arr.filter((x) => typeof x === 'string') : [])
  if (Array.isArray(raw)) return { sources: strs(raw), matchedBy: '(root-array)' }
  if (!raw || typeof raw !== 'object') return { sources: [], matchedBy: '(非对象)' }
  for (const k of ['quote_sources', 'sources', 'values', 'enum', 'names']) {
    const got = strs(raw[k])
    if (got.length) return { sources: got, matchedBy: k }
  }
  const union = []
  for (const v of Object.values(raw)) union.push(...strs(v))
  return { sources: union, matchedBy: union.length ? '(union-of-top-level-arrays)' : '(无字符串数组)' }
}

/**
 * 读取 quote_source golden。
 * @returns {{file:string, sources:string[], matchedBy:string, text:string}}
 * @throws {Error} 文件缺失 / JSON 非法 / 解析不出任何枚举时抛错（错误信息即失败文案）
 */
export function loadQuoteSources() {
  const tried = candidatePaths()
  const file = tried.find((p) => fs.existsSync(p))
  if (!file) {
    throw new Error(
      '§M1/§F4 quote_source golden 未落盘：期望 ' + GOLDEN_REL + '（已尝试：' + tried.join(' , ') + '）。'
      + '该文件是行情源枚举的唯一事实源（Go 侧 internal/data 的 setLastSource/QMT-L1 导出），'
      + '缺失时本用例不再退回旧硬编中文白名单——那正是词表漂移的病根，故显式失败而非跳过。',
    )
  }
  let raw
  try {
    raw = JSON.parse(fs.readFileSync(file, 'utf8'))
  } catch (e) {
    throw new Error('§M1/§F4 quote_source golden 解析失败：' + file + ' → ' + (e && e.message ? e.message : e))
  }
  const { sources, matchedBy } = collectStrings(raw)
  if (!sources.length) {
    throw new Error('§M1/§F4 quote_source golden 无可解析枚举（' + file + '，matchedBy=' + matchedBy
      + '，顶层键=' + Object.keys(raw || {}).join(',') + '）——请在 golden 里以 quote_sources/sources 数组导出枚举。')
  }
  return { file, sources, matchedBy, text: JSON.stringify(raw).slice(0, 400) }
}
