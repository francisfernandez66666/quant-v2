// ── §P2-I N1~N3 + §P2-J P14/P15 + §P2-H M3（2026-10-06 修复批 波 6）派生覆盖面扫描锁 ──
//
// 这一组不是"行为用例"，而是**覆盖面自检**：它们回答的是"下一批新增一个轮询页/一处吞错/一条
// 前端调用链时，会不会悄悄落在锁外面"。三条缺陷的原始形态都正是"锁面是硬编码清单"：
//  · N1：各页轮询没有请求代号守卫（慢响应后到覆盖新数据），修一批页面如果只按文件名点名，
//    下一个新增 setInterval 的页面自动落在锁外（§BOM-REPO-DERIVE 同族教训）。
//  · P14：`catch (_) {}` 吞错——全仓一百多处 catch 里只有接了 §P2-J 台账的页面被对账过，
//    写死"Hotspot 9 处、Admin 17 处"这种行号清单的话，任何人重排一次代码锁就废了。
//  · M3：后端 GET/POST /api/config/d1 早就齐了，前端**一个调用点都没有**（grep 零命中）——
//    这条零命中本身就是缺陷，所以锁的形状必须是"命中数 ≥1 且面板真被页面挂载"。
//
// ★ 判据实现**只有一份**：`scripts/fe_contract_scan.mjs`（本文件 import 它的导出函数）。
// 原先这一组判据在本文件里自己写了一遍 classify/auditCatches，门禁 §113 又在 bash 里写了第二遍，
// 两份并存的结局本批已经踩过太多次——只修一份、另一份继续读旧判据（§0929DRILL 的 record_freshness、
// §BOM-REPO-DERIVE 的派生清单同族）。现在两个消费者读同一本账，本文件只负责**断言**，
// 不再负责**判定**。
//
// 判据形状（每枚都配"摘掉就红"的反证，反证在文件内用合成源码跑，不去改产品代码）：
//  N1 派生集合 S＝pages 里含轮询特征的文件 ⇒ S 中每个文件都必须 import 统一守卫且 begin/isStale 都在用；
//  N2 空转正锁：|S| ≥ 现测下界（扫描模式一旦失效，S 会掉到 0，此时必须判红而不是"恒绿空循环"）；
//  N3 判据自证：合成一份"有 setInterval、没守卫"的源码必须被报成缺守卫，
//     而"只在注释里提到 setInterval"的源码必须不算进 S（否则静态负锁会误伤说明注释，§107 同族）；
//  P14 射程＝import utils/loadLedger.js 的文件集合（派生，不是点名）⇒ 每个文件 unaccounted catch＝0；
//  P15 空转正锁：接入台账的页面数与 catch 语料量级都不得低于下界；
//  M3 api 出口在位 + 页面侧调用点 ≥1 + 面板真被 pages 挂载；
//  T1 涨跌令牌：亮/暗两处定义的语义必须都是"涨暖跌冷"（P1/P1b 的等值分母来自同一扫描器）。
//
// English: derivation-based coverage locks for wave 6. The judgement code lives once in
// scripts/fe_contract_scan.mjs; this file (and gate §113) only assert on its readings.
import { describe, it, expect } from 'vitest'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
// 单实现扫描器：路径从本文件位置派生（不写死绝对路径，换 checkout 目录照样跑）
import {
  FLOORS,
  WEB_SRC,
  classify,
  codeOnly,
  scanPollers,
  guardAccount,
  pollCoverage,
  auditCatches,
  catchCoverage,
  d1Coverage,
  tokenTruths,
  report,
} from '../../../scripts/fe_contract_scan.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
// 本文件与扫描器指向同一个源码根——不一致就是 import 走偏（镜像/副本场景要能立刻看出来）
expect(path.join(HERE, '..')).toBe(WEB_SRC)

describe('§P2-I N1~N3 轮询页守卫派生覆盖面', () => {
  // 一次扫描、多枚断言：pollCoverage 是 S 的唯一来源（自己再 listFiles 一遍就是第二份实现）
  const coverage = pollCoverage()

  // N1 派生等值：S 里每一页都必须接守卫，且 begin/isStale 都在用（只 import 不调用＝守卫恒不生效）
  it('N1 每个轮询页都 import 统一守卫并实际使用 begin/isStale', () => {
    expect(coverage.missing, '§P2-I N1：轮询页缺守卫接线 → ' + coverage.missing.join(' | ')).toEqual([])
  })

  // N2 空转正锁：|S| 掉到下界即红（扫描模式一失效、目录一改名的话 S 会静默变 0，整段退化成恒绿空循环）
  it('N2 轮询页集合不为空（派生清单本身可信）', () => {
    expect(coverage.total, '§P2-I N2：派生集合缩水＝判据失效，不是"页面都修好了"').toBeGreaterThanOrEqual(FLOORS.pollPages)
    // 三类特征各自真的有人命中（少一类＝那一类轮询整体在锁外）。
    // EventSource 保留在特征里但今天 pages 零命中：SSE 连接由 api 层单点持有（App 建连后走 sseBus 分发），
    // 页面不直接 new——不为凑等值把它从判据里删掉（将来出现页面自建连接时仍要被抓），
    // 也不假装它已经有人命中（覆盖面账如实记这一条）。
    const seen = new Set()
    for (const p of coverage.pages) for (const m of p.marks) seen.add(m)
    for (const m of ['setInterval', 'sseBus', 'useSseRefresh']) {
      expect(seen.has(m), '特征 ' + m + ' 在 pages 里 0 命中＝扫描面或页面写法变了，需要重新定射程').toBe(true)
    }
  })

  // N3 反证（合成源码，不碰产品代码）：判据必须抓得住"有轮询没守卫"，也绝不误伤注释里的字样
  it('N3 判据自证：无守卫轮询必被点名，纯注释里的 setInterval 不算轮询', () => {
    const bad = 'import { createStaleGuard } from "./x.js"\nconst t = setInterval(load, 5000)\n'
    expect(scanPollers(bad)).toEqual(['setInterval'])
    expect(guardAccount(bad).importsGuard, '只 import 别的模块不算接守卫').toBe(false)
    // 摘掉 import 那一行 ⇒ 判据必红（这就是 N3 要的反证形态）
    const good = bad.replace('./x.js', '../utils/staleGuard.js') + 'const k = g.begin(); if (!g.isStale(k)) set(k)\n'
    const acc = guardAccount(good)
    expect(acc.importsGuard).toBe(true)
    expect(acc.begins).toBe(1)
    expect(acc.stales).toBe(1)
    // 注释误伤反证：只在注释里出现 setInterval 的文件不得进 S
    const commented = '// 本页原本有 setInterval 轮询，现已改事件驱动\nconst a = 1 // setInterval( 出现在行尾注释里\n'
    expect(scanPollers(commented), '§P2-I N3：注释里的字样不算一处实现').toEqual([])
  })
})

describe('§P2-J P14/P15 catch 对账（射程派生自台账接入面）', () => {
  const cat = catchCoverage()

  // P14 等值对账：接入面内每处 catch 要么体内有可见错误态，要么挂 §P2-J 可吞 豁免
  it('P14 台账接入页的 catch 全部对账（0 处静默吞错）', () => {
    expect(cat.unaccounted, '§P2-J P14：吞错未对账 ' + cat.unaccounted.length + ' 处 → ' + JSON.stringify(cat.unaccounted)).toEqual([])
  })

  // P15 空转正锁：接入页面数与 catch 语料量级都不得缩水
  // （两个数各拦一种退化：接入面被改窄＝P14 扫不到新页面；扫描器坏＝total 掉到 0 而"每处都对账"恒真）
  it('P15 台账接入面与 catch 语料量级可信', () => {
    expect(cat.pages, '§P2-J P15：接了台账的页面数低于下界＝接入面被改窄').toBeGreaterThanOrEqual(FLOORS.ledgerFiles)
    expect(cat.total, '§P2-J P15：catch 语料量级缩水＝扫描器或判据被改坏').toBeGreaterThanOrEqual(FLOORS.catchTotal)
    // 接入面非空（files 是页面清单，红条组件也在里面——它同样接了台账，是共用实现的定义处）
    expect(cat.files.length, '§P2-J P15：接入面为空＝判据射程失效').toBeGreaterThan(cat.pages)
  })

  // P14b 反证（合成源码）：判据必须把"体内只有 console.error"报成未对账，
  // 并把"体内写台账"与"上方挂豁免"两种正解都放行——否则豁免机制一坏，整条锁会恒红。
  it('P14b 判据自证：console.error 不算可见，台账写入与豁免注释都算', () => {
    const swallowed = 'try { x() } catch (e) { console.error(e) }\n'
    expect(auditCatches(swallowed).unaccounted.length).toBe(1)
    const surfaced = 'try { x() } catch (e) { markLoadFail("持仓", e.message) }\n'
    expect(auditCatches(surfaced).unaccounted.length).toBe(0)
    const exempted = '// §P2-J 可吞：旁证读数，失败保留上一份\ntry { x() } catch (e) { void e }\n'
    expect(auditCatches(exempted).unaccounted.length).toBe(0)
    // 体长反证：`} catch (e) {` 之前的右括号不许被计入配平（v1 形态会把体判成一行而看不见体内动作）
    const nested = 'if (a) { b() } catch (e) { markLoadFail("x", 1); deep = { k: 1 } }\n'
    expect(auditCatches(nested).unaccounted.length, '体内动作必须真被读到').toBe(0)
  })
})

describe('§P2-H M3 全局 D1 通道的前端调用点派生锁', () => {
  // M3 后端通道早就齐了、前端零调用点（grep 'config/d1' 命中 0）⇒ 这条锁钉"调用点在位"，
  // 并且钉"面板真被页面挂载"：组件写了但没人 render，等于通道还是没人用（§DEADGAUGE 的"定义没接"同族）。
  it('M3 api 出口 + 页面调用点 + 面板挂载三处都在位', () => {
    const d1 = d1Coverage()
    expect(d1.apiGet, 'GET 出口缺失').toBe(1)
    expect(d1.apiPost, 'POST 出口缺失').toBe(1)
    expect(d1.endpointHits, 'GET/POST 各一处端点串').toBeGreaterThanOrEqual(2)
    expect(d1.panelGet).toBeGreaterThanOrEqual(1)
    expect(d1.panelPost).toBeGreaterThanOrEqual(1)
    expect(d1.mountedPages.length, '§P2-H M3：面板没被任何页面挂载＝前端调用点仍是 0').toBeGreaterThanOrEqual(1)
  })

  // M3b 反证：调用点扫描只看代码行——把面板名字只写在注释里的页面不能被算成"已挂载"
  it('M3b 挂载判据不被注释里的组件名骗到', () => {
    const onlyComment = '// 以后可以在这里放 <D1ConfigPanel /> 组件（components/D1ConfigPanel.jsx）\nconst a = 1\n'
    const code = codeOnly(onlyComment)
    expect(/<D1ConfigPanel/.test(code)).toBe(false)
    expect(/components\/D1ConfigPanel\.jsx/.test(code)).toBe(false)
    // classify 的行数必须与原文一致（判据按行做 catch 体配平，行数错位会把体读短）
    expect(classify('a\nb\nc').length).toBe(3)
  })
})

describe('§P3-FE T1 涨跌令牌语义 + 汇总读数自证', () => {
  // T1 亮色与暗色**两处**定义都得是"涨暖（R>G）跌冷（G>R）"；
  // 只取第一处的话，暗色块被改成涨绿跌红照样绿——那正是本批 P1/P1b 要拦的形态。
  it('T1 styles.css 涨跌令牌的全部定义语义正确（亮/暗两处都在射程）', () => {
    const tk = tokenTruths()
    expect(tk.up.length, '§P3-FE T1：--app-up 定义处数缩水＝暗色块落在锁外').toBeGreaterThanOrEqual(FLOORS.upTokenDefs)
    expect(tk.down.length, '§P3-FE T1：--app-down 定义处数缩水').toBeGreaterThanOrEqual(FLOORS.upTokenDefs)
    expect(tk.upRgba.every((x) => x.warm), '涨必须暖色 → ' + JSON.stringify(tk.upRgba)).toBe(true)
    expect(tk.downRgba.every((x) => !x.warm), '跌必须冷色 → ' + JSON.stringify(tk.downRgba)).toBe(true)
  })

  // T1b 反向对（等值而不是"更红更亮"的单向锁，§equal-locks 纪律）：涨跌两处令牌值必须互换才叫反向
  it('T1b 涨跌令牌等值：把 --app-up 写成冷色必被点名', () => {
    const tk = tokenTruths()
    // 合成反证：暖冷判据本身要能区分（把 up 换成一个冷色，warm 必须为 false）
    const coldish = { hex: '#00a870', r: 0, g: 168, warm: 0 > 168 }
    expect(coldish.warm).toBe(false)
    // 现网读数里 up 与 down 的值不得相同（同值＝涨跌无从区分，着色恒一色）
    for (const u of tk.up) for (const d of tk.down) expect(u).not.toBe(d)
  })

  // 汇总出口自证：report() 的键就是门禁 §113 读的键——两个消费者读同一份 JSON，
  // 键名在这里被改坏，vitest 会先红而不是留到门禁那一轮才发现（§0929DRILL 的旧键名同族）。
  it('report() 读数与各分项一致（门禁 §113 依赖这些键）', () => {
    const r = report()
    const cat = catchCoverage()
    expect(r.poll_total).toBe(pollCoverage().total)
    expect(r.poll_missing).toEqual([])
    expect(r.catch_total).toBe(cat.total)
    expect(r.ledger_pages).toBe(cat.pages)
    expect(r.ledger_files).toEqual(cat.files)
    expect(r.token_up).toEqual(tokenTruths().up)
    expect(r.floors).toBe(FLOORS)
    expect(Object.keys(r).length, '汇总键数量（门禁逐键断言，少一键即红）').toBeGreaterThanOrEqual(18)
  })
})
