// ── 前端一致性契约派生扫描器 scripts/fe_contract_scan.mjs（§P2-I/§P2-J/§P3-FE，2026-10-06 修复批 波 6）──
//
// 为什么要有这个文件（缺陷本体不是产品代码，而是**锁面本身**）：
// 波 6 的四类覆盖面判据——轮询页必须接守卫（N1）、接了台账的页面 catch 必须对账（P14）、
// 全局 D1 通道必须有前端调用点（M3）、涨跌色令牌必须等于交付稿真值（P1/P1b）——
// 原本有两份实现：一份在门禁 §113 的 bash 里，一份在前端 vitest 里。
// 两份并存的结局本批已经踩过太多次：**只修一份、另一份继续读旧键名/旧判据**
// （§0929DRILL 的 record_freshness()、§BOM-REPO-DERIVE 的派生清单、§107 的三件套同形，全是同一族）。
// 所以这里收敛成**单实现、两消费者**：
//   · 消费者一 = vitest（web/src/__tests__/p6_derived_coverage.test.js 直接 import 本文件的导出函数）；
//   · 消费者二 = 门禁 §113（`node scripts/fe_contract_scan.mjs` 读 JSON 读数，逐键断言）。
// 判据只在这一处定义，两个消费者读到的必然是同一本账。
//
// 三条判据形状上的取舍（写下来免得下次被"顺手简化"掉）：
//  ① **只看代码行**：classify() 把每行拆成 code/comment 两路，块注释（含 JSX 的 {/* */}）整块进 comment。
//     静态负锁若不剥注释，会把说明注释里引用的旧写法当成一处实现（§107 预演实录：恒红的是尺子）。
//  ② **派生而不是点名**：轮询页集合、台账接入文件集合、涨跌令牌定义处数，全部由源码扫出来。
//     写死文件名清单的锁对下一个新增页面天生失明（§BOM-REPO-DERIVE 的头号教训）。
//  ③ **空转正锁由消费者钉**：本扫描器只报读数（哪怕扫出 0 也照样返回空集合），
//     "0 就是红"的判定留给两个消费者各自断言——扫描器自己不判红，
//     否则正则失效时它会"安静地什么都不报"，而那正是本枚要消灭的形态。
//
// English: single-implementation derived scanner for wave-6 frontend consistency contracts.
// Two consumers (vitest + gate §113) read the exact same judgement code; the scanner itself
// only reports numbers and never decides red/green, so a broken regex shows up as an empty
// reading that the consumers must fail on.
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
// ROOT＝仓库根，WEB_SRC＝前端源码根（两个消费者都从这里派生路径，不许各自硬编码）
export const ROOT = path.join(HERE, '..')
export const WEB_SRC = path.join(ROOT, 'web', 'src')

// 现测下界（2026-10-09 波 6 实跑读数；消费者用它做「派生面缩水即红」的等值分母）。
// 写成导出的常量而不是散在两条断言里：写两遍的结局是改一遍、另一遍继续用旧数（§0929DRILL 同族）。
export const FLOORS = {
  pollPages: 11, // 轮询/SSE 页面数
  ledgerFiles: 4, // 接了 §P2-J 台账的页面数（不含共用红条）
  catchTotal: 40, // 上述文件里 catch 的语料总量（掉下来说明扫描器坏了而不是"吞错修完了"）
  upTokenDefs: 2, // --app-up 在 styles.css 里的定义处数（亮色 + 暗色各一份；只数一处会漏掉暗色块反向）
}

/**
 * 逐行拆成「代码 / 注释」两路：块注释（/* … *\/、JSX 的 {/* … *\/}）整块只进 comment，
 * 行内 // 之后的部分进 comment。供所有静态判据复用——判据看代码，注释里的旧写法不算一处实现。
 * @param {string} src 源文件全文
 * @returns {Array<{code: string, comment: string}>} 每行一条
 */
export function classify(src) {
  const out = []
  let inBlock = false
  for (const line of src.split('\n')) {
    const t = line.trim()
    if (inBlock) {
      out.push({ code: '', comment: line })
      if (t.includes('*/')) inBlock = false
      continue
    }
    if (t.startsWith('/*') || t.startsWith('{/*')) {
      out.push({ code: '', comment: line })
      if (!t.includes('*/')) inBlock = true
      continue
    }
    const i = t.indexOf('//')
    if (i >= 0) out.push({ code: t.slice(0, i), comment: t.slice(i) })
    else out.push({ code: t, comment: '' })
  }
  return out
}

/**
 * 取一份源码的「纯代码文本」（注释位置留空行，行号不参与但行数不变）。
 * @param {string} src 源文件全文
 * @returns {string} 只剩代码行的文本
 */
export function codeOnly(src) {
  return classify(src)
    .map((r) => r.code)
    .join('\n')
}

// 读源文件（相对 web/src，或绝对路径）
function readSrc(rel) {
  const p = path.isAbsolute(rel) ? rel : path.join(WEB_SRC, rel)
  return fs.readFileSync(p, 'utf8')
}

// 列出某目录下的 .js/.jsx 文件名（派生清单而不是写死名单）。
// 参数是**绝对目录**而不是相对 web/src 的名字：镜像反证会把 root 换成副本根，
// 若这里仍去 join WEB_SRC，扫到的就是真仓、破坏的是副本 ⇒ 反证"红了/没红"都不可归属。
function listDir(absDir) {
  return fs
    .readdirSync(absDir)
    .filter((f) => /\.jsx?$/.test(f))
    .sort()
}

// POLL_MARKS：轮询/SSE 特征（N1 的射程来源）。页面自建定时器或直接吃 SSE 的都算，
// 因为两者都有"后到的旧响应覆盖新读数"的窗口。
const POLL_MARKS = [
  ['setInterval', /setInterval\(/],
  ['EventSource', /EventSource/],
  ['sseBus', /sseBus/],
  ['useSseRefresh', /useSseRefresh/],
]

/**
 * 扫一份源码命中的轮询特征名（纯函数，反证可直接喂合成源码）。
 * @param {string} src 源文件全文
 * @returns {string[]} 命中的特征名，空数组＝这页没有轮询面
 */
export function scanPollers(src) {
  const code = codeOnly(src)
  return POLL_MARKS.filter(([, re]) => re.test(code)).map(([n]) => n)
}

/**
 * 一页的守卫接线账：import 了统一守卫没有、begin/isStale 各被调用几处。
 * 只 import 不调用＝守卫恒不生效（同族 §DEADGAUGE「定义没接」），所以两个计数都要 ≥1。
 * @param {string} src 源文件全文
 * @returns {{importsGuard: boolean, begins: number, stales: number}}
 */
export function guardAccount(src) {
  const code = codeOnly(src)
  return {
    importsGuard: /utils\/staleGuard\.js/.test(code),
    begins: (code.match(/\.begin\(/g) || []).length,
    stales: (code.match(/isStale\(/g) || []).length,
  }
}

/**
 * N1 主入口：pages 目录下所有轮询页的守卫覆盖面。
 * @param {string} [root] web/src 根（镜像反证时传副本根）
 * @returns {{pages: Array<object>, missing: string[], total: number}}
 */
export function pollCoverage(root) {
  const base = root || WEB_SRC
  const dir = path.join(base, 'pages')
  const pages = []
  const missing = []
  for (const f of fs.readdirSync(dir).filter((x) => /\.jsx?$/.test(x)).sort()) {
    const src = readSrc(path.join(dir, f))
    const marks = scanPollers(src)
    if (!marks.length) continue
    const g = guardAccount(src)
    const row = { file: f, marks, ...g }
    pages.push(row)
    // 三种缺法分开点名：没 import / 只 import 不用 / 用了 begin 却不查 isStale（只盖章不判污＝半边守卫）
    if (!g.importsGuard) missing.push(`${f}:未 import 守卫`)
    else if (g.begins < 1) missing.push(`${f}:begin=0`)
    else if (g.stales < 1) missing.push(`${f}:isStale=0`)
  }
  return { pages, missing, total: pages.length }
}

// ERR_MARKS：可见错误态判据（P14）。按运行时真实取值链取「失败被写进用户看得见的地方」：
// 台账写入 / 名字含失败语义的 state setter / 明确提示 / 权限分流。
// 各页 setter 命名不同（setLoadErr / setConsErr / noteForbidden / setNoDB …），点名列举每遇到一个
// 新命名就得改判据，漏改的那一页会被报成"吞错"——那是锁自己造红。
// console.error/warn **不算**：控制台的失败用户看不见。
const ERR_MARKS = [
  /markLoadFail/,
  /markFail/,
  /showToast/,
  /showFeedback/,
  /MessagePlugin/,
  /set[A-Za-z0-9_]*(Err|Error|Fail|Forbidden|Degraded|NoDB|NoQuote)[A-Za-z0-9_]*\(/,
  /navigate\(['"]\/403/,
]

/**
 * catch 体：从 catch 所在行起做花括号配平，**从遇到的第一个左括号起算**。
 * `} catch (e) {` 行首那个右括号属于上一段——先计入它会让体长恒为一行，
 * 判据于是永远看不见体内动作（v1 实录的假红形态）。
 * @param {Array<{code: string, comment: string}>} rows classify 的产物
 * @param {number} i catch 所在行号（0 基）
 * @returns {{body: string, end: number}} 体文本与结束行号
 */
export function catchBodyOf(rows, i) {
  let depth = 0
  let started = false
  let body = ''
  let j = i
  for (; j < rows.length; j++) {
    const c = rows[j].code
    for (const ch of c) {
      if (ch === '{') {
        started = true
        depth++
      } else if (ch === '}') {
        if (started) depth--
      }
    }
    body += c + ' '
    if (started && depth <= 0) break
  }
  return { body, end: j }
}

/**
 * 对账一份源码里的所有 catch：体内有可见错误态、或体内/紧邻上方 5 行挂 `§P2-J 可吞` 豁免的算已对账。
 * @param {string} src 源文件全文
 * @returns {{total: number, unaccounted: Array<{line: number, body: string}>}}
 */
export function auditCatches(src) {
  const rows = classify(src)
  const total = []
  const unaccounted = []
  for (let i = 0; i < rows.length; i++) {
    if (!/catch\s*\(/.test(rows[i].code)) continue
    total.push(i + 1)
    const { body, end } = catchBodyOf(rows, i)
    let exempt = false
    for (let k = i; k <= end && k < rows.length; k++) {
      if (rows[k].comment.includes('§P2-J 可吞') || rows[k].code.includes('§P2-J 可吞')) exempt = true
    }
    for (let k = Math.max(0, i - 5); k < i; k++) {
      if (rows[k].comment.includes('§P2-J 可吞')) exempt = true
    }
    if (!ERR_MARKS.some((re) => re.test(body)) && !exempt) {
      unaccounted.push({ line: i + 1, body: body.trim().slice(0, 140) })
    }
  }
  return { total, unaccounted }
}

/**
 * P14 主入口：**射程派生自「谁接了 §P2-J 台账」**，而不是点名页面清单。
 * 为什么这样定射程：全仓 179 处 catch 一天内不可能全部对上账，
 * 硬扫全仓会在健康代码上恒红（判据比产品跑得快的锁没人敢留）；
 * 而点名清单会让下一个新页面自动落在锁外。取"接入面 = 对账面"这条等价关系，
 * 覆盖面随接入面自然增长，红永远是"这一页接了台账却没把 catch 对完"。
 * @param {string} [root] web/src 根（镜像反证时传副本根）
 * @returns {{files: string[], pages: number, total: number, unaccounted: Array<object>}}
 */
export function catchCoverage(root) {
  const base = root || WEB_SRC
  const files = []
  const unaccounted = []
  let total = 0
  for (const dir of ['components', 'pages']) {
    for (const f of listDir(path.join(base, dir))) {
      const rel = path.join(dir, f)
      const src = readSrc(path.join(base, rel))
      if (!/utils\/loadLedger\.js/.test(codeOnly(src))) continue
      files.push(rel)
      const a = auditCatches(src)
      total += a.total.length
      for (const u of a.unaccounted) unaccounted.push({ file: rel, ...u })
    }
  }
  // pages＝页面数（红条组件本身不是页面，消费者用它对齐"接入页面数 ≥ 下界"）
  const pages = files.filter((f) => f.startsWith('pages')).length
  return { files, pages, total, unaccounted }
}

/**
 * M3：全局 D1 通道的前端调用点覆盖面（api 出口 / 面板调用 / 页面挂载三段各数一处）。
 * 缺陷本体是"后端通道早就齐了、前端零调用点"，所以这条锁的形状必须是三段都得有，
 * 少任何一段都等于回到"没人用"：面板写了但没页面 render＝通道依旧没人用（§DEADGAUGE 定义没接同族）。
 * @param {string} [root] web/src 根
 * @returns {{apiGet: number, apiPost: number, panelGet: number, panelPost: number, mountedPages: string[]}}
 */
export function d1Coverage(root) {
  const base = root || WEB_SRC
  const api = codeOnly(readSrc(path.join(base, 'api/index.js')))
  const panelCode = codeOnly(readSrc(path.join(base, 'components/D1ConfigPanel.jsx')))
  const mountedPages = []
  for (const f of listDir(path.join(base, 'pages'))) {
    const code = codeOnly(readSrc(path.join(base, 'pages', f)))
    if (/components\/D1ConfigPanel\.jsx/.test(code) && /<D1ConfigPanel/.test(code)) mountedPages.push(f)
  }
  return {
    apiGet: (api.match(/export\s+(?:async\s+)?function\s+fetchD1Config\b/g) || []).length,
    apiPost: (api.match(/export\s+(?:async\s+)?function\s+setD1Config\b/g) || []).length,
    // 端点串按代码行数计（GET/POST 各一行 request 调用）
    endpointHits: (api.match(/\/config\/d1/g) || []).length,
    panelGet: (panelCode.match(/fetchD1Config\(/g) || []).length,
    panelPost: (panelCode.match(/setD1Config\(/g) || []).length,
    mountedPages,
  }
}

/**
 * P1/P1b：styles.css 里涨跌令牌的全部定义值。
 * 必须返回**全部**定义而不是第一处：亮色 :root 与暗色主题各有一份，
 * 只取第一处的话，暗色块被改成涨绿跌红照样绿（首跑实录，本批要拦的正是语义反向）。
 * @param {string} [root] web/src 根
 * @returns {{up: string[], down: string[], upRgba: Array<{hex: string, warm: boolean}>, downRgba: Array<{hex: string, warm: boolean}>}}
 */
export function tokenTruths(root) {
  const base = root || WEB_SRC
  const css = fs.readFileSync(path.join(base, 'styles.css'), 'utf8')
  const defs = (name) => {
    const re = new RegExp('--' + name + '\\s*:\\s*([^;]+);', 'g')
    const all = []
    let m
    while ((m = re.exec(css)) !== null) all.push(m[1].trim())
    return all
  }
  // 十六进制 → 通道；涨跌的"暖/冷"判据就是 R 与 G 谁大（红＝R 占优，绿＝G 占优）
  const warm = (hex) => {
    const h = String(hex).replace('#', '')
    const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h
    const r = parseInt(full.slice(0, 2), 16)
    const g = parseInt(full.slice(2, 4), 16)
    return { hex: String(hex), r, g, warm: r > g }
  }
  const up = defs('app-up')
  const down = defs('app-down')
  return { up, down, upRgba: up.map(warm), downRgba: down.map(warm) }
}

/**
 * 汇总读数（CLI 与门禁共用这一个出口，避免"两个消费者各自拼字段"拼出两套键名）。
 * @param {string} [root] web/src 根（镜像反证时传副本根）
 * @returns {object} 机器可读读数
 */
export function report(root) {
  const poll = pollCoverage(root)
  const cat = catchCoverage(root)
  const d1 = d1Coverage(root)
  const tk = tokenTruths(root)
  return {
    web_src: root || WEB_SRC,
    floors: FLOORS,
    poll_total: poll.total,
    poll_missing: poll.missing,
    ledger_files: cat.files,
    ledger_pages: cat.pages,
    catch_total: cat.total,
    catch_unaccounted: cat.unaccounted,
    d1_api_get: d1.apiGet,
    d1_api_post: d1.apiPost,
    d1_endpoint_hits: d1.endpointHits,
    d1_panel_get: d1.panelGet,
    d1_panel_post: d1.panelPost,
    d1_mounted_pages: d1.mountedPages,
    token_up: tk.up,
    token_down: tk.down,
    token_up_warm_all: tk.upRgba.every((x) => x.warm),
    token_down_cool_all: tk.downRgba.every((x) => !x.warm),
  }
}

// CLI：`node scripts/fe_contract_scan.mjs [web/src 根]` → 打印 JSON 读数，退出码恒 0。
// 判红留给消费者（见文件头取舍③）：扫描器把"扫到什么"如实报出来，
// 而"扫到 0 条该不该红"由门禁 §113 与 vitest 各自断言——两处断言都读同一份 JSON。
//
// isMain 必须比 **realpath**，不能比 path.resolve(argv[1]) 的字符串：macOS 的 /tmp 是指向
// /private/tmp 的符号链接，而镜像反证正是把扫描器放进 /tmp/… 副本树里执行的（§113 ⑩ 组）。
// 按字符串比时 import.meta.url 报 file:///private/tmp/…、argv[1] 报 /tmp/…，两者永不相等 ⇒
// 判成「有人 import 我」，CLI 于是**安静地不打印任何东西、退出码还是 0**——
// 消费端读到空 JSON，报出来的错是「扫描器跑不起来」，而真实形态是「扫描器跑了却没出数」。
// 这一族（空输出被当成成功）是本文件头取舍③要拦的东西，不能让它从入口这一行长回来。
function isDirectRun() {
  const argv1 = process.argv[1]
  if (!argv1) return false
  try {
    return fs.realpathSync(path.resolve(argv1)) === fs.realpathSync(fileURLToPath(import.meta.url))
  } catch {
    return false // 路径都读不到＝不是直接执行本文件（保持不打印，由消费端的空读数锁现形）
  }
}

if (isDirectRun()) {
  const argRoot = process.argv[2] ? path.resolve(process.argv[2]) : undefined
  process.stdout.write(JSON.stringify(report(argRoot), null, 2) + '\n')
}
