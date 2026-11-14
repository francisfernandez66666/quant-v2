// 云端字节级 HTTP UAT 探针
// 用法：UAT_BASE=... UAT_USER=... UAT_PASS='...' node scripts/cloud_byte_uat.mjs
// 行为：
//   1) POST /api/auth/login 取得 Bearer token（模仿用户登录——只读探针的唯一前提，永不拦截）
//   2) 逐端点拨测前端调用的全部 REST 契约（来源：scripts/contract_ledger.tsv 的「前端调用」行）
//      - GET 端点：真实拨测，记录字节级响应（status / content-type / content-length / body SHA256 / 首 240B）
//   3) 输出 JSON 证据 + 人类可读摘要（stderr），摘要另落 UAT_SUMMARY_OUT 供验收脚本直接读文件。
//
// §F1-20261114 生产强制只读（11-14 实锤：旧版对生产发出 28 条 2xx 写，把模拟盘持仓/告警/
// 咨询历史清空、可用资金归零——"写操作最小且可逆"的前提已被证伪，该前提本身由 F2 的
// 写面门禁保证，本脚本不再假设它成立）：
//   - 生产域名（quant-trading.top / 81.71.69.17）一律只读：除 POST /api/auth/login 外
//     不发出任何非 GET，写端点逐条记入 summary.skippedWrites[]（不是计数器——
//     "到底跳了哪 81 条"必须可核）。
//   - 写探针仅在显式 UAT_ALLOW_WRITE=1 且**非生产域名**（本地 UAT 栈）下执行，
//     且 UAT_ALLOW_WRITE=1 压不过生产判定（B2 后门锁）。
//   - probe() 内层另有硬闸双保险（防将来绕过 main 直接调 probe）。

import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(__dirname, '..')

const BASE = process.env.UAT_BASE || 'https://quant-trading.top'
const USER = process.env.UAT_USER || 'admin'
const PASS = process.env.UAT_PASS || ''

// §F1-20261114：生产判定优先于一切环境变量开关（UAT_ALLOW_WRITE=1 也压不过 IS_PROD）。
const IS_PROD = /quant-trading\.top|81\.71\.69\.17/.test(BASE)
const READONLY = process.env.UAT_READONLY === '1' || IS_PROD
const ALLOW_WRITE = process.env.UAT_ALLOW_WRITE === '1' && !IS_PROD
// 登录是只读探针的唯一前提，永不拦截；除此之外只读模式下不发出任何非 GET。
const AUTH_PATHS = new Set(['/api/auth/login'])
// 摘要落盘点：验收命令直接 json.load 这个文件（本脚本零 console.log，管道吃 stdout 必然空）。
const SUMMARY_OUT = process.env.UAT_SUMMARY_OUT || ''

// 只读模式下逐条登记被跳过的写端点（method + path 数组，不许静默丢弃）；
// 真正发出的非 GET 请求逐条登记 sentWrites（只读模式下必须恒空）。
const skippedWrites = []
const sentWrites = []

const sha256 = (s) => crypto.createHash('sha256').update(s).digest('hex')
const clip = (s, n = 240) => (s.length > n ? s.slice(0, n) + `…(+${s.length - n}B)` : s)

// 路径参数样本替换
const SAMPLES = {
  id: 'u_0000000000000000000',
  code: '600519',
  order_id: '9001',
  ticket: '00000000-0000-0000-0000-000000000000',
}
function subst(p) {
  return p
    .replace(/\{id\}/g, SAMPLES.id)
    .replace(/\{code\}/g, SAMPLES.code)
    .replace(/\{order_id\}/g, SAMPLES.order_id)
    .replace(/\{ticket\}/g, SAMPLES.ticket)
}

// 安全写探针载荷（明显无效，预期被干净拒绝 / 或幂等回写）
function probeBody(method, path) {
  if (path.includes('/paper/config') || path.includes('/config/') && method === 'POST') {
    return JSON.stringify({ _probe: true })
  }
  if (path === '/api/paper/buy' || path === '/api/paper/sell') {
    return JSON.stringify({ code: SAMPLES.code, qty: 0, price: 0, _probe: true })
  }
  if (path === '/api/positions/execute') {
    return JSON.stringify({ action: 'buy', code: SAMPLES.code, qty: 0, _probe: true })
  }
  if (path === '/api/holdings') {
    return JSON.stringify({ _probe: true })
  }
  return JSON.stringify({ _probe: true })
}

// 读取契约台账，抽取「前端调用」行
function loadLedger() {
  const f = path.join(ROOT, 'scripts', 'contract_ledger.tsv')
  const lines = fs.readFileSync(f, 'utf8').split('\n')
  const rows = []
  for (const ln of lines) {
    if (!ln.includes('前端调用')) continue
    const cols = ln.split('\t')
    if (cols.length < 3) continue
    rows.push({ method: cols[0].trim(), path: cols[1].trim(), call: cols[3] || '' })
  }
  return rows
}

async function login() {
  const ctrl = new AbortController()
  const t = setTimeout(() => ctrl.abort(), REQ_TIMEOUT_MS)
  const r = await fetch(`${BASE}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: USER, password: PASS }),
    signal: ctrl.signal,
  })
  clearTimeout(t)
  const body = await r.text()
  if (r.status !== 200) throw new Error(`login failed ${r.status}: ${body}`)
  const j = JSON.parse(body)
  if (!j.token) throw new Error(`login no token: ${body}`)
  return j.token
}

const REQ_TIMEOUT_MS = 8000

async function probe(method, rawPath, token) {
  const url = BASE + subst(rawPath)
  const isRead = method === 'GET'
  const isSSE = rawPath === '/api/events' || rawPath === '/api/events/ticket'
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), REQ_TIMEOUT_MS)
  const init = {
    method,
    headers: { Authorization: `Bearer ${token}` },
    signal: controller.signal,
  }
  if (!isRead) {
    // §F1-20261114 硬闸（probe 层双保险）：只读模式下除登录白名单外一律不发。
    // 闸必须在 probeBody() **之前**抛——写在 fetch 之前但 probeBody 之后也能拦，
    // 但"载荷已构造"会让人误以为已经发了；断言只查字符串查不出这种位置错误。
    // 用 rawPath（本函数签名是 probe(method, rawPath, token)，体内没有 row 变量）。
    if (!ALLOW_WRITE && !AUTH_PATHS.has(rawPath)) throw new Error('readonly mode')
    init.headers['Content-Type'] = 'application/json'
    init.body = probeBody(method, rawPath)
    sentWrites.push(`${method} ${rawPath}`)
  }
  const t0 = Date.now()
  let status = -1,
    ct = '',
    cl = '',
    body = '',
    err = ''
  try {
    const res = await fetch(url, init)
    status = res.status
    ct = res.headers.get('content-type') || ''
    cl = res.headers.get('content-length') || ''
    if (isSSE) {
      // 流式端点：取到状态码即中止，不读 body（避免挂起）
      body = '[SSE stream — 取到状态码即中止]'
    } else {
      body = await res.text()
    }
  } catch (e) {
    if (e.name === 'AbortError') {
      // 超时/中止：若已拿到状态码则保留（SSE 场景），否则记为超时
      err = err || 'aborted(timeout)'
    } else {
      err = String(e)
    }
  } finally {
    clearTimeout(timer)
  }
  const ms = Date.now() - t0
  const isJson = ct.includes('application/json') || /^\s*[[{]/.test(body)
  return {
    method,
    path: rawPath,
    url,
    status,
    contentType: ct,
    contentLength: cl || String(body.length),
    bodySha256: err ? '' : sha256(body),
    bodyHead: err ? '' : clip(body),
    isJson,
    ms,
    err,
  }
}

async function main() {
  const token = await login()
  console.error(`[login] OK as ${USER}, token len=${token.length}`)
  const rows = loadLedger()
  console.error(`[ledger] 前端调用端点 ${rows.length} 条`)

  const results = []
  let i = 0
  for (const row of rows) {
    // §F1-20261114：只读模式下写端点不拨测——记入 skippedWrites（method + path 逐条在册，
    // 不许静默丢弃），probe 层硬闸作为双保险兜底。
    if (row.method !== 'GET' && !ALLOW_WRITE) {
      skippedWrites.push(`${row.method} ${row.path}`)
      continue
    }
    i++
    const r = await probe(row.method, row.path, token)
    const klass = r.err ? 'ERR' : r.status >= 500 ? '5xx' : r.status >= 400 ? '4xx' : r.status >= 200 && r.status < 300 ? '2xx' : '?'
    console.error(`[${String(i).padStart(3)}/${rows.length}] ${row.method.padEnd(6)} ${row.path.padEnd(42)} -> ${String(r.status).padStart(3)} ${klass} ${r.contentType.split(';')[0]} ${r.contentLength}B sha=${r.bodySha256.slice(0, 12) || 'ERR'}`)
    results.push({ ...r, call: row.call, klass })
  }

  // 摘要
  const byKlass = {}
  for (const r of results) byKlass[r.klass] = (byKlass[r.klass] || 0) + 1
  const summary = {
    base: BASE,
    user: USER,
    readonly: READONLY,
    isProd: IS_PROD,
    total: results.length,
    byKlass,
    // §F1-20261114 五字段：skippedWrites/sentWrites 是数组不是计数器（"跳了哪 81 条"必须可核）；
    // server5xx 只做在册不判 0（F8 修复前那 2 条 QMT 502 是预期内读数）。
    skippedWrites,
    sentWrites,
    server5xx: results.filter((r) => r.klass === '5xx').map((r) => `${r.method} ${r.path} -> ${r.status}`),
    clientErr: results.filter((r) => r.klass === '4xx').map((r) => `${r.method} ${r.path} -> ${r.status}`),
    transportErr: results.filter((r) => r.klass === 'ERR').map((r) => `${r.method} ${r.path} -> ${r.err.slice(0, 80)}`),
  }

  const outJson = path.join(ROOT, '.uat-data', `cloud_byte_uat_${Date.now()}.json`)
  fs.mkdirSync(path.dirname(outJson), { recursive: true })
  fs.writeFileSync(outJson, JSON.stringify({ summary, results }, null, 2))
  // §F1-20261114：摘要同时落 UAT_SUMMARY_OUT（未设时回落到原证据文件路径）——
  // 验收命令读文件，不读 stdout（本脚本零 console.log，管道形态必然吃空 stdin）。
  const summaryPath = SUMMARY_OUT || outJson
  fs.writeFileSync(summaryPath, JSON.stringify(summary, null, 2))
  console.error(`\n=== 摘要 ===`)
  console.error(JSON.stringify(summary, null, 2))
  console.error(`\n完整证据: ${outJson}`)
  console.error(`摘要落盘: ${summaryPath}（readonly=${READONLY} isProd=${IS_PROD} skippedWrites=${skippedWrites.length} sentWrites=${sentWrites.length}）`)
}

main().catch((e) => {
  console.error('FATAL', e)
  process.exit(1)
})
