#!/usr/bin/env node
/**
 * 注释覆盖度审计器（Go / TypeScript-JSX / Python 同一口径）
 *
 * 与 internal/commentcheck 的 Go 侧规则保持一致，但把扫描范围扩到：
 *   - 后端 Go（含 _test.go，commentcheck 只查非测试文件）
 *   - qmt_gateway Python（重连柜台的资金安全代码）
 *   - web/src 前端（JSX 逻辑块）
 *
 * 判定口径：连续的、既非注释也非空行、且含控制流的代码行达到阈值即算「一个逻辑块」；
 * 若块首之前 lookback 个非空行内没有任何注释行，则记为**缺口**。
 *
 * 中文要求：除 qmt_gateway/qmt_bridge_strategy.py（必须在 GBK 沙箱执行，**纯 ASCII 强制**，
 * 中文注释会乱码）外，其余文件要求注释行内含中日韩字符。该文件只要求「有注释」。
 *
 * 用法：
 *   node scripts/check_comments.js            # 输出缺口清单，有缺口退出码 1
 *   node scripts/check_comments.js --full     # 附带每个文件的覆盖率统计
 */
'use strict'

const fs = require('fs')
const path = require('path')

// ── 扫描范围与参数 ────────────────────────────────────────────────
// ROOTS：要扫描的目录（相对仓库根）；EXT：纳入的文件扩展名。
const ROOTS = ['internal', 'cmd', 'qmt_gateway', 'web/src']
const EXT = new Set(['.go', '.js', '.jsx', '.py'])
// SKIP_DIRS：构建产物与第三方目录，不参与审计。
const SKIP_DIRS = new Set(['node_modules', 'dist', 'build', '__pycache__', '.pytest_cache', 'vendor', 'testdata'])
// THRESHOLD / LOOKBACK：与 internal/commentcheck.DefaultOptions 对齐。
const THRESHOLD = 15
const LOOKBACK = 3

// 强制纯 ASCII 的文件（中文注释会导致 GBK 沙箱乱码）：只要求「有注释」，不要求「有中文」。
const ASCII_ONLY = new Set(['qmt_gateway/qmt_bridge_strategy.py'])

// 控制流/数据操作特征：出现即认为该行是「逻辑」而非纯声明。
const CTRL_RE = /\b(if|else|for|while|switch|case|catch|return|func |def |class |lambda|:=|await|yield)\b|[=!<>]=|\b(len|append|map|filter|forEach|reduce|push|concat|sort|split|join|slice|includes|parseInt|parseFloat|JSON\.|Object\.|Array\.|Math\.|Promise\.|String\.|Number\.|Boolean\.)\s*\(|\b(map|set|dict|list)\[|=>/

const HAN_RE = /[\u4e00-\u9fff]/

/** 是否是纯注释行或语法边界行（与 commentcheck.isCommentLine 同口径）。 */
function isCommentLine(s) {
  return s.startsWith('//') || s.startsWith('/*') || s.startsWith('*') ||
    s.startsWith('#') || s.startsWith('<!--')
}

/** 行内是否含注释片段（行尾注释也算该行带注释）。 */
function hasInlineComment(line, ext) {
  if (ext === '.py') return line.includes('#')
  // JS/Go：跳过字符串字面量里的 //（粗判即可，不追求完美词法分析）
  const noStr = line.replace(/(['"`])(?:\\.|(?!\1)[^\\])*\1/g, '')
  return noStr.includes('//') || noStr.includes('/*')
}

/** 判断块首上方是否有文档覆盖。返回 {found, han}：
 *  found — 上方存在注释 / docstring；han — 覆盖物（或紧邻的代码行）里含中文。
 *
 * 三个关键口径：
 *  1. 向上穿过**整段连续注释块**：本项目惯例是「中文行在前 + English: 行在后」，
 *     只看紧邻一行会把双语注释块误判成「注释无中文」。
 *  2. docstring 行与注释同权（传入 docLines 标记）。
 *  3. 强制 ASCII 文件（qmt_bridge_strategy.py，GBK 沙箱禁中文）里，**存在注释即覆盖**，
 *     不要求中文；其余文件要求覆盖物含中文。
 */
function prevComment(lines, start, docLines, asciiOnly) {
  let sawComment = false
  for (let i = start - 1; i >= 0; i--) {
    const s = lines[i].trim()
    if (s === '') continue
    if (isCommentLine(s) || docLines.has(i)) {
      sawComment = true
      if (HAN_RE.test(s)) return { found: true, han: true }
      continue // 是注释但无中文：继续向上找（可能是 English: 翻译行）
    }
    if (HAN_RE.test(s)) {
      // 紧邻块首的中文**代码行**（如调用参数里的中文文案）也算就近自述。
      return { found: true, han: true }
    }
    break // 碰到无中文的代码行，注释块结束
  }
  // 强制 ASCII 文件：有英文注释即达标（中文被硬约束禁止）。
  if (sawComment && asciiOnly) return { found: true, han: true }
  return { found: sawComment, han: false }
}

/** 扫描单个文件，返回缺口数组。 */
function scanFile(abs, rel) {
  const ext = path.extname(abs)
  const raw = fs.readFileSync(abs, 'utf8')
  const lines = raw.split('\n')
  const asciiOnly = ASCII_ONLY.has(rel)
  const gaps = []
  let run = 0, start = 0, ctrl = false

  // 预扫描：标记 Python docstring 覆盖的行号集合（与注释同权）。
  // 不识别的话，纯英文 docstring 的正文行会被当成代码，把明明写了文档的函数误报成缺口。
  const docLines = new Set()
  if (ext === '.py') {
    let inDoc = false
    let docQuote = ''
    for (let i = 0; i < lines.length; i++) {
      const s = lines[i].trim()
      if (inDoc) {
        docLines.add(i)
        if (s.includes(docQuote)) inDoc = false
        continue
      }
      const m = s.match(/^("""|''')/)
      if (m) {
        docQuote = m[1]
        docLines.add(i)
        // 同行即收尾（"""一行文档"""）不算进入多行文档串。
        const restAfterOpen = s.slice(3)
        if (!restAfterOpen.includes(docQuote)) inDoc = true
      }
    }
  }

  const flush = () => {
    if (run >= THRESHOLD && ctrl) {
      const cov = prevComment(lines, start, docLines, asciiOnly)
      // 缺口两种形态：上方无任何注释/docstring；或有覆盖但不含中文（强制 ASCII 文件除外）。
      if (!cov.found) {
        gaps.push({ line: start + 1, len: run, head: lines[start].trim().slice(0, 70), why: '缺注释' })
      } else if (!cov.han && !asciiOnly) {
        gaps.push({ line: start + 1, len: run, head: lines[start].trim().slice(0, 70), why: '注释无中文' })
      }
    }
    run = 0
    ctrl = false
  }

  // 主循环：注释/空行/docstring/含中文行打断连续块，其余按逻辑块累计。
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i]
    const s = l.trim()
    if (s === '' || isCommentLine(s) || hasInlineComment(l, ext) || docLines.has(i)) {
      // 注释行、空行、行尾带注释的行、docstring 行都打断连续块。
      flush()
      continue
    }
    if (HAN_RE.test(l)) {
      // 含中文的行视为「已自述」（中文文案 / 中文注释），打断连续块——
      // 与 internal/commentcheck 对 Go 的口径一致。
      flush()
      continue
    }
    if (run === 0) {
      start = i
      ctrl = false
    }
    run++
    if (CTRL_RE.test(l)) ctrl = true
  }
  flush()
  return gaps
}

/** 递归收集 ROOTS 下所有待审计文件。 */
function collect(root, out) {
  let entries
  try {
    entries = fs.readdirSync(root, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    const p = path.join(root, e.name)
    if (e.isDirectory()) {
      if (SKIP_DIRS.has(e.name)) continue
      collect(p, out)
      continue
    }
    if (!EXT.has(path.extname(e.name))) continue
    // 跳过前端构建期生成的文件与压缩产物
    if (/\.min\.|\.bundle\./.test(e.name)) continue
    out.push(p)
  }
  return out
}

const full = process.argv.includes('--full')
const files = []
for (const r of ROOTS) collect(r, files)
files.sort()

let totalGaps = 0
let totalFiles = 0
let cleanFiles = 0
const perFile = []

for (const f of files) {
  const rel = path.relative(process.cwd(), f)
  const n = fs.readFileSync(f, 'utf8').split('\n').length
  const gaps = scanFile(f, rel)
  totalFiles++
  const han = (fs.readFileSync(f, 'utf8').match(/[\u4e00-\u9fff]/g) || []).length
  if (gaps.length === 0) cleanFiles++
  else {
    totalGaps += gaps.length
    perFile.push({ rel, n, han, gaps })
  }
}

// ── 输出 ────────────────────────────────────────────────────────
if (full) {
  console.log(`扫描文件 ${totalFiles} 个，其中无缺口 ${cleanFiles} 个，有缺口 ${perFile.length} 个，缺口总数 ${totalGaps}`)
}
if (perFile.length) {
  for (const pf of perFile) {
    console.log(`\n${pf.rel}  (${pf.n} 行, 含中文行 ${pf.han}, 缺口 ${pf.gaps.length})`)
    for (const g of pf.gaps.slice(0, 12)) {
      console.log(`  L${g.line} [${g.why}] ${g.len} 行: ${g.head}`)
    }
    if (pf.gaps.length > 12) console.log(`  … 其余 ${pf.gaps.length - 12} 处省略`)
  }
  console.log(`\n合计：${totalFiles} 个文件 / ${totalGaps} 处缺口（${perFile.length} 个文件待补）`)
  process.exit(1)
}
console.log(`注释审计通过：${totalFiles} 个文件全部覆盖（阈值 ${THRESHOLD} 行 / 回看 ${LOOKBACK} 行）`)
