// 文件概述（scripts/cloud_byte_uat_readonly_test.mjs）
// §183 B1/B2/C1–C4 的可执行行为锁：不依赖本地全栈、不打生产。
//   ① 起 127.0.0.1 mock 服务（登录是唯一允许的非 GET 请求，其余全 GET 200 JSON），
//      实跑 cloud_byte_uat.mjs 并读 UAT_SUMMARY_OUT 证据文件：
//      readonly=true / skippedWrites.length === 台账派生非 GET 行数 / sentWrites=[] /
//      非 GET 请求恰好 1 条（登录）/ server5xx 允许非空（不拿"应为 0"当闸）。
//   ② 生产域名优先级纯判定：IS_PROD 为真时 UAT_ALLOW_WRITE=1 也压不过只读（不真发包）。
//   ③ 四枚变异反证：把 §183 的 A1–A8 判据原样作用在 /tmp 镜像副本上，
//      删 IS_PROD / 写死 ALLOW_WRITE=true / 删 probe 硬闸 / 删 AUTH_PATHS 白名单，均必须判红。
// English: §F1 read-only behavior lock + four mutation counter-proofs, fully local.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const PROBE = path.join(ROOT, 'scripts/cloud_byte_uat.mjs')
const LEDGER = path.join(ROOT, 'scripts/contract_ledger.tsv')

// 台账派生（不写死 81/83）：第 1 列 method、第 3 列 class。
function expectedSkipped() {
  return fs.readFileSync(LEDGER, 'utf8').split('\n').filter((ln) => {
    if (!ln.trim() || ln.startsWith('#')) return false
    const c = ln.split('\t')
    return c.length >= 3 && c[2] === '前端调用' && c[0] !== 'GET'
  }).length
}

// §183 A1–A8 判据（作用在任意一份脚本文本上；镜像变异后必须判红）。
function criteria(src) {
  const count = (needle) => src.split(needle).length - 1
  return src.includes('const IS_PROD = /quant-trading\\.top|81\\.71\\.69\\.17/.test(BASE)')
    && count('IS_PROD') >= 2
    && src.includes("const ALLOW_WRITE = process.env.UAT_ALLOW_WRITE === '1' && !IS_PROD")
    && count('UAT_ALLOW_WRITE') >= 2
    && src.includes('const AUTH_PATHS = new Set(')
    && src.includes('readonly mode')
    && count('AUTH_PATHS') >= 2
    && src.includes('AUTH_PATHS.has(rawPath)')
    && !src.includes('不破坏真实数据')
    && count('UAT_SUMMARY_OUT') >= 2
    && src.indexOf('readonly mode') < src.indexOf('probeBody(method, rawPath)')
}

function pureProdPriority(src) {
  const m = src.match(/const IS_PROD = [^\n]+\nconst READONLY = [^\n]+\nconst ALLOW_WRITE = [^\n]+/)
  assert.ok(m, 'IS_PROD/READONLY/ALLOW_WRITE 三行定义缺失')
  // eslint-disable-next-line no-new-func
  const f = new Function('BASE', 'process', `${m[0]}\nreturn {isProd: IS_PROD, readonly: READONLY, allowWrite: ALLOW_WRITE}`)
  return f('https://quant-trading.top', { env: { UAT_ALLOW_WRITE: '1', UAT_READONLY: '' } })
}

async function main() {
  const src = fs.readFileSync(PROBE, 'utf8')
  assert.ok(criteria(src), '基线脚本不满足 §183 A1–A8')

  // ② 生产域名优先级（纯判定，不发任何请求）
  const pp = pureProdPriority(src)
  assert.equal(pp.isProd, true, '生产域名必须识别')
  assert.equal(pp.readonly, true, '生产域名必须强制只读')
  assert.equal(pp.allowWrite, false, 'UAT_ALLOW_WRITE=1 不得压过生产判定（后门锁）')

  // ③ 四枚变异反证（镜像副本；每枚必须使判据转红）
  const mutations = [
    ['C1 删 IS_PROD 判定', (s) => s.replace(/const IS_PROD = [^\n]+\n/, '')],
    ['C2 ALLOW_WRITE 写死 true', (s) => s.replace(/const ALLOW_WRITE = [^\n]+/, 'const ALLOW_WRITE = true')],
    ['C3 删 probe 硬闸', (s) => s.replace(/if \(!ALLOW_WRITE && !AUTH_PATHS\.has\(rawPath\)\) throw new Error\('readonly mode'\)\n/, '')],
    ['C4 删登录白名单', (s) => s.replace(/const AUTH_PATHS = new Set\(\[[^\]]*\]\)\n/, '')],
  ]
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'f1mut-'))
  for (const [name, mutate] of mutations) {
    const bad = mutate(src)
    assert.notEqual(bad, src, `${name} 变异没落地（锚串漂了）`)
    assert.equal(criteria(bad), false, `${name} 变异后 §183 判据仍绿＝锁没牙`)
    fs.writeFileSync(path.join(tmp, `${name.slice(0, 2)}.mjs`), bad)
  }

  // ① 行为锁：mock 服务 + 实跑探针
  let nonGet = 0
  const server = createServer((req, res) => {
    if (req.method !== 'GET') nonGet += 1
    res.writeHead(200, { 'Content-Type': 'application/json' })
    if (req.url.includes('/api/auth/login')) res.end(JSON.stringify({ token: 'mock-token' }))
    else res.end('{}')
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const port = server.address().port
  const summaryPath = path.join(tmp, 'f1_summary.json')
  try {
    // 必须异步 spawn：spawnSync 会阻塞父进程事件循环，mock 服务永远无法应答登录请求。
    const child = spawn(process.execPath, [PROBE], {
      cwd: ROOT,
      env: {
        ...process.env,
        // 本机 WorkBuddy 注入的 HTTP_PROXY 会把 127.0.0.1 请求也带去代理并 abort；
        // 探针全程只许打本地 mock，显式清空代理（NODE_USE_ENV_PROXY=0 双保险）。
        HTTP_PROXY: '', HTTPS_PROXY: '', ALL_PROXY: '',
        http_proxy: '', https_proxy: '', all_proxy: '',
        NODE_USE_ENV_PROXY: '0', NO_PROXY: '127.0.0.1,localhost', NODE_OPTIONS: '',
        UAT_BASE: `http://127.0.0.1:${port}`,
        UAT_USER: 'mock',
        UAT_PASS: 'mock',
        UAT_READONLY: '1',
        UAT_SUMMARY_OUT: summaryPath,
      },
    })
    let out = '', err = ''
    child.stdout.on('data', (b) => { out += b })
    child.stderr.on('data', (b) => { err += b })
    const status = await new Promise((resolve) => child.on('close', resolve))
    assert.equal(status, 0, `探针退出码 ${status}: ${err.slice(-800)}`)
    const d = JSON.parse(fs.readFileSync(summaryPath, 'utf8'))
    assert.equal(d.readonly, true, 'readonly 必须为 true')
    assert.equal(Array.isArray(d.skippedWrites), true, 'skippedWrites 必须是数组（不是计数器）')
    assert.equal(d.skippedWrites.length, expectedSkipped(), '跳过条数必须等于台账派生非 GET 行数')
    assert.equal(d.sentWrites.length, 0, `sentWrites 必须为空：${JSON.stringify(d.sentWrites)}`)
    assert.equal(nonGet, 1, `只读模式下只允许 1 条非 GET（登录），实得 ${nonGet}`)
    assert.ok(Array.isArray(d.server5xx), 'server5xx 必须逐条在册（不拿"应为 0"当闸）')
    console.log(`F1_READONLY_OK skipped=${d.skippedWrites.length} sent=0 nonGet=${nonGet} mutations=4/4 red`)
  } finally {
    server.close()
    fs.rmSync(tmp, { recursive: true, force: true })
  }
}

main().catch((e) => { console.error('F1_READONLY_FAIL', e); process.exit(1) })
