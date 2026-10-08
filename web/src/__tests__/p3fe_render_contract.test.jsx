// ── §P3-FE（2026-10-06 修复批 波 6）前端「读数一致性」渲染契约锁 ──
//
// 这一批缺陷的共同形态不是「功能没做」，而是**同一个事实在两个位置用两套语义渲染**：
//  · Research 页同时存在「胜→绿」与「正→红」两套着色（AUDIT_20261005 P3 / 着色反向），
//    同一屏里「跑赢基准」是跌绿、「跑输」是涨红——用户按颜色判断就会读反；
//  · 「回测超额」在 toast 里乘 100 显示 5.23%、在表格里直接把 0.0523 当数字显示（两口径）；
//  · 抽屉里现价每 5s 刷新而涨幅仍是**开抽屉那一刻**的冻结值（自相矛盾的读数）；
//  · 自选股缺行情时 `Number(x)||0` 把「没读到」渲染成 ¥0.00 / -0.00%，
//    盘口缺数时 `'--'` 落进 `startsWith('+') ? up : down` 的二态判据被染成跌绿；
//  · 胜率 toFixed(0) 把 99.6% 显示成 100%（§0929 ⑧ 已裁决「亚单位不取整」）。
// 断言一律用**等值**而不是「看起来对」：着色断到 CSS 变量名，再从 styles.css 读回该变量的真值
// 十六进制比对（等值锁必须等于交付真值，不能只断「更大/更亮」那种单向形状）；
// 格式化断到字符串完全相等；缺数断到占位符字面量 + style.color 精确值。
// 每枚正向断言旁边配一枚「改回旧口径必红」的反证腿（P2/P5/P7/P9/P11/P13），
// 反证在这里的写法是：把旧口径的表达式在同一用例里再算一遍，断言它与新实现**不等**——
// 这样即使有人把实现改回去，红的也是这条契约本身，而不是某条挂载细节。
//
// English: §P3-FE wave-6 frontend consistency contract locks — one token-accurate colour outlet,
// one excess formatter, drawer change% from the same reading as the price, missing data rendered as
// a neutral placeholder (never ¥0.00 nor "down"), and sub-unit percentages no longer rounded.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
// §DET-TIME：等 UI 一律用 settle()（排空微任务）而不是 findBy/waitFor——邻居负载下真实时钟轮询会偶发红。
import { settle } from './settle.js'

// DepthPanel 的取数端点走受控 mock（其余 api 实现原样保留，Watchlist/Research 的导入不受影响）
const { state } = vi.hoisted(() => ({ state: { depth: {} } }))
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return { ...actual, fetchDepth: vi.fn(async () => state.depth) }
})
// 被测出口全部是模块作用域的导出（提出来就是为了做等值断言，不必挂载整页去 mock 十余端点）
import { signColor, fmtExcess } from '../pages/Research.jsx'
import { PriceCell, PctCell, WL_NO_DATA_PLACEHOLDER } from '../pages/Watchlist.jsx'
// pctState/DEPTH_NO_DATA 是模块作用域导出（等值断言用），DepthPanel 是默认导出（挂载行为腿用）——
// 两者都要：只 import 具名会让 mountDepth 里的 <DepthPanel/> 变成 ReferenceError（本文件首跑实录）。
import DepthPanel, { pctState, DEPTH_NO_DATA } from '../components/DepthPanel.jsx'

const HERE = path.dirname(fileURLToPath(import.meta.url))
// DepthPanel 挂载helper：把 fetchDepth 的返回换成给定盘口载荷后渲染一次，返回 unmount。
// 只有这一处写挂载细节（两个三态用例共用），mock 换的是取数载荷而不是判据。
// 为什么 render 要再包一层 async act：组件的取数 effect 是 async 函数，
// RTL 的 render 只做**同步** act，于是 await fetchDepth 之后的那一串 setState 会落在 act 边界外
// （React 报「update not wrapped in act」，首跑实录三条）。落界外不影响本用例的结论
// （settle() 之后读数已到终态），但会让警告淹没别的用例的真警告——包进 async act 让续体留在界内。
// @param {object} payload fetchDepth 的返回体
// @returns {Promise<{unmount: () => void}>}
async function mountDepth(payload) {
  state.depth = payload
  let result = null
  await act(async () => {
    result = render(<DepthPanel code="600000.SH" />)
  })
  return { unmount: result.unmount }
}
// 剥注释（行 // 与块 /* */，含 JSX 的 {/* */}）：着色/格式化出口的派生扫描只看代码行，
// 否则说明注释里引用的旧写法字符串会被当成一处实现（锁自己造红）。
function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((l) => {
      const i = l.indexOf('//')
      return i >= 0 ? l.slice(0, i) : l
    })
    .join('\n')
}
function readSrc(rel) {
  return stripComments(fs.readFileSync(path.join(HERE, '..', rel), 'utf8'))
}

// styles.css 的涨跌令牌**全部**定义值（亮色 :root 与暗色主题各一份；只取第一份的话，
// 暗色块被改成涨绿跌红照样绿——那正是本批要拦的语义反向）。
function tokenDefs(name) {
  const css = fs.readFileSync(path.join(HERE, '..', 'styles.css'), 'utf8')
  const re = new RegExp('--' + name + '\\s*:\\s*([^;]+);', 'g')
  const out = []
  let m
  while ((m = re.exec(css)) !== null) out.push(m[1].trim())
  if (!out.length) throw new Error('styles.css 里找不到令牌 --' + name)
  return out
}
// hexIsRed/hexIsGreen 按 RGB 通道判色相方向（等值判据不写死具体十六进制，暗色主题换值也拦得住方向）
function hexChannels(hex) {
  const h = String(hex).replace('#', '')
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h.slice(0, 6)
  const n = parseInt(full, 16)
  // 常量 n：局部定义
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

describe('P1/P2/P3 着色唯一出口（Research 正负数值）', () => {
  // P1 正→涨红、负→跌绿、缺数→中性（等值断言到 CSS 变量名）
  it('P1 signColor 逐值等值到令牌名', () => {
    expect(signColor(0.0523)).toBe('var(--app-up)')
    expect(signColor(0)).toBe('var(--app-up)')   // 0 归「不亏」侧，与本页 fmtPct 的 >=0 口径一致
    expect(signColor(-0.01)).toBe('var(--app-down)')
    expect(signColor(null)).toBe('var(--app-faint)')
    expect(signColor(undefined)).toBe('var(--app-faint)')
    expect(signColor(Number.NaN)).toBe('var(--app-faint)')
  })

  // P1b 令牌真值等值：涨必须是红、跌必须是绿（A股口径）。
  // 只断变量名的话，有人把 --app-up 改成绿色值也能过——那才是这套缺陷最坏的重开方式；
  // 而只断第一处定义的话，暗色主题块被反向也看不见 ⇒ 两处都读，第一处等值交付稿、每一处判色相方向。
  it('P1b styles.css 的涨跌令牌真值＝交付稿（红涨绿跌，含暗色块）', () => {
    const ups = tokenDefs('app-up')
    const downs = tokenDefs('app-down')
    expect(ups.length).toBeGreaterThanOrEqual(2)   // 亮色 :root + 暗色主题各一枚
    expect(ups[0].toLowerCase()).toBe('#e34d59')   // 交付稿真值（styles.css:114）
    expect(downs[0].toLowerCase()).toBe('#00a870') // 交付稿真值（styles.css:115）
    for (const hex of ups) {
      const [r, g] = hexChannels(hex)
      expect(r, `涨色令牌 ${hex} 必须红（R>G）`).toBeGreaterThan(g)
    }
    for (const hex of downs) {
      const [r, g] = hexChannels(hex)
      expect(g, `跌色令牌 ${hex} 必须绿（G>R）`).toBeGreaterThan(r)
    }
  })

  // P2 反证：旧反向口径（胜→绿、负→红）与现实现逐值相反 ⇒ 改回去这枚必红
  it('P2 反向口径与现实现不等（旧缺陷形状的重现对照）', () => {
    const oldWinSide = (v) => (v >= 0 ? 'var(--app-down)' : 'var(--app-up)')
    expect(oldWinSide(1)).not.toBe(signColor(1))
    expect(oldWinSide(-1)).not.toBe(signColor(-1))
    // 0 也要对上：旧写法在 0 上同样反向（>=0 落绿）
    expect(oldWinSide(0)).not.toBe(signColor(0))
  })

  // P3 负锁（派生）：同页只准一枚正负着色出口；旧的第二出口 signClass 不得复活。
  // 判据从源码派生而不是写死行号：函数定义恰一枚、`>=0 ? --app-down` 反向三元式零命中。
  it('P3 Research 页正负着色出口唯一且反向三元式零命中', () => {
    const src = readSrc('pages/Research.jsx')
    const defs = (src.match(/function\s+signColor\s*\(/g) || []).length
    expect(defs, 'signColor 定义必须恰好一枚（两枚＝又回到两套语义）').toBe(1)
    expect((src.match(/function\s+signClass\s*\(/g) || []).length).toBe(0)
    expect((src.match(/signClass/g) || []).length).toBe(0)
    // 反向三元式：`>=0` 配跌绿——本批缺陷的原始形状（旧 :1405 的「累计前向收益 >=0 ? --app-down」）。
    // 刻意**不**拦 `>0 ? --app-down`：那是合法的「负」计数着色（s.loss > 0 显示绿，语义正确），
    // 一把只按「出现 >0 配绿」判红的尺子会在健康代码上恒红（静态负锁必须限定到缺陷形状）。
    expect((src.match(/>=\s*0\s*\?\s*'var\(--app-down\)'/g) || []).length).toBe(0)
    expect((src.match(/<=\s*0\s*\?\s*'var\(--app-up\)'/g) || []).length).toBe(0)
  })
})

describe('P6/P7 回测超额单一口径（toast 与表格同源）', () => {
  // P6 唯一格式化出口：小数比率 → 带符号百分比；缺数出「-」而不是谎报 0%
  it('P6 fmtExcess 等值（含缺数占位）', () => {
    expect(fmtExcess(0.0523)).toBe('+5.23%')
    expect(fmtExcess(-0.0175)).toBe('-1.75%')
    expect(fmtExcess(0)).toBe('+0.00%')
    expect(fmtExcess(null)).toBe('-')
    expect(fmtExcess(undefined)).toBe('-')
    expect(fmtExcess(Number.NaN)).toBe('-')
  })

  // P6b toast 与表格同源：两处渲染字符串必须是同一个函数产出的**同一串**
  // （旧形态是 toast 自己乘 100 取两位、表格直接显示裸比率 0.0523，同一候选两个数）
  it('P6b 同一读数在 toast 与表格里是同一个字符串', () => {
    const v = 0.0523
    const toastSide = '候选 #7 回测完成，回测超额 ' + fmtExcess(v)
    const tableSide = fmtExcess(v)
    expect(toastSide.endsWith(tableSide)).toBe(true)
    expect(tableSide).toBe('+5.23%')
  })

  // P7 反证：旧两口径各自都会与现实现不等（裸比率 / 无符号百分比）
  it('P7 旧口径（裸比率与无符号百分比）必红', () => {
    const v = 0.0523
    expect(String(v)).not.toBe(fmtExcess(v))              // 表格旧口径：直接显示 0.0523
    expect((v * 100).toFixed(2) + '%').not.toBe(fmtExcess(v)) // toast 旧口径：少了正号
    expect(fmtExcess(null)).not.toBe('0%')                // 缺数不许谎报 0%
  })

  // P7b 派生负锁：Research 里不许再有第二处自己乘 100 渲染 avg_excess
  it('P7b avg_excess 的渲染只走 fmtExcess（无本地 ×100 口径）', () => {
    const src = readSrc('pages/Research.jsx')
    expect((src.match(/avg_excess\s*\*\s*100/g) || []).length).toBe(0)
    expect((src.match(/avg_excess\)?\s*\*\s*100/g) || []).length).toBe(0)
    // 出口本身定义恰一枚
    expect((src.match(/function\s+fmtExcess\s*\(/g) || []).length).toBe(1)
  })
})

describe('P8/P9 自选股两格缺数渲染（¥0.00 与 -0.00% 都不许出现）', () => {
  // P8 现价格：null/undefined/空串/非数 **以及 0**（后端取不到行情就回 0）一律占位符
  it('P8 现价缺数出「—」且不着色', () => {
    for (const bad of [null, undefined, '', Number.NaN, 0, 'abc']) {
      render(<PriceCell value={bad} />)
      const el = screen.getByTestId('wl-no-data')
      expect(el.textContent).toBe(WL_NO_DATA_PLACEHOLDER)
      expect(el.style.color).toBe('var(--app-faint)')
      expect(el.textContent).not.toContain('¥0.00')
      cleanup()
    }
  })

  // P9 涨跌格：缺数同上；而 **0 是合法实测平盘**，必须照常显示 0.00% 并着色
  it('P9 涨跌幅缺数出「—」，0 是合法平盘读数', () => {
    for (const bad of [null, undefined, '', Number.NaN, 'abc']) {
      render(<PctCell value={bad} />)
      const el = screen.getByTestId('wl-no-data')
      expect(el.textContent).toBe(WL_NO_DATA_PLACEHOLDER)
      expect(el.style.color).toBe('var(--app-faint)')
      cleanup()
    }
    render(<PctCell value={0} />)
    const flat = screen.getByTestId('wl-pct')
    expect(flat.textContent).toBe('0.00%')
    expect(flat.style.color).toBe('var(--app-up)') // >=0 归涨侧，与全页 >=0 口径一致
    cleanup()
    render(<PctCell value={-1.23} />)
    expect(screen.getByTestId('wl-pct').style.color).toBe('var(--app-down)')
    cleanup()
    render(<PctCell value={2.5} />)
    expect(screen.getByTestId('wl-pct').textContent).toBe('+2.50%')
    cleanup()
  })

  // P9b 反证：旧写法 `¥${Number(x)||0 .toFixed(2)}` 会把缺数渲染成 ¥0.00（与占位符不等）
  it('P9b 旧「缺数折成 0」口径必红', () => {
    const oldPrice = (v) => '¥' + (Number(v) || 0).toFixed(2)
    expect(oldPrice(null)).not.toBe(WL_NO_DATA_PLACEHOLDER)
    expect(oldPrice(null)).toBe('¥0.00') // 这就是缺陷本体的读数
    const oldPct = (v) => ((Number(v) || 0) > 0 ? '+' : '') + (Number(v) || 0).toFixed(2) + '%'
    expect(oldPct(null)).toBe('0.00%')   // 缺数冒充平盘，与 P3-FE「缺数≠平盘」同族
  })

  // P9c 派生锁：同页占位符只有一枚常量、两格渲染走同一个出口（不再各写一套）
  it('P9c Watchlist 缺数占位符与渲染出口唯一', () => {
    const src = readSrc('pages/Watchlist.jsx')
    expect((src.match(/export const WL_NO_DATA_PLACEHOLDER/g) || []).length).toBe(1)
    expect((src.match(/function PriceCell/g) || []).length).toBe(1)
    expect((src.match(/function PctCell/g) || []).length).toBe(1)
    // 表列引用的是这两个组件，不是本地重新写的 Number(x)||0
    expect((src.match(/Number\((row\.price|row\.change_pct)\)\s*\|\|\s*0/g) || []).length).toBe(0)
  })
})

describe('P10/P11 盘口涨跌幅三态（缺数不再染成跌绿）', () => {
  // P10 三态各自有明确字面量
  it('P10 pctState 三态等值', () => {
    expect(pctState('+1.23%')).toBe('up')
    expect(pctState('-0.45%')).toBe('down')
    expect(pctState(DEPTH_NO_DATA)).toBe('neutral')
    expect(pctState('')).toBe('neutral')
    expect(pctState(null)).toBe('neutral')
    expect(pctState('—')).toBe('neutral')
    expect(pctState('--x')).toBe('neutral')
    expect(pctState('+.5%')).toBe('neutral') // 符号后没数字＝不是读数
  })

  // P11 反证：旧二态判据把 '--' 判成 down（本条缺陷本体），三态判据必须与它不等
  it('P11 旧二态判据对缺数必红', () => {
    const oldTwoState = (t) => (String(t).startsWith('+') ? 'up' : 'down')
    expect(oldTwoState(DEPTH_NO_DATA)).toBe('down')
    expect(pctState(DEPTH_NO_DATA)).not.toBe(oldTwoState(DEPTH_NO_DATA))
  })

  // P11b 三态真的接到消费点（不是只测纯函数）：缺价/缺昨收时根节点 data-pct-state=neutral
  it('P11b DepthPanel 根节点按三态取色（挂载行为腿）', async () => {
    const { unmount } = await mountDepth({ name: '浦发银行', bids: [], asks: [], time: '09:30:00', source: 'sina_degraded' })
    await settle()
    expect(screen.getByTestId('depth-panel').getAttribute('data-pct-state')).toBe('neutral')
    unmount()
    cleanup()
  })

  // P11c 有昨收与现价时按真读数取色（up），证明 neutral 不是恒绿兜底
  it('P11c 有价有昨收 → data-pct-state=up', async () => {
    const { unmount } = await mountDepth({ name: '浦发银行', bids: [], asks: [], price: 10.5, prev_close: 10.0 })
    await settle()
    expect(screen.getByTestId('depth-panel').getAttribute('data-pct-state')).toBe('up')
    unmount()
    cleanup()
  })

  // P11d 派生锁：死变量 nowCls 与二态三元式都不得在本组件复活
  it('P11d DepthPanel 二态判据只存在于 pctState 内', () => {
    const src = readSrc('components/DepthPanel.jsx')
    expect((src.match(/nowCls/g) || []).length).toBe(0)
    expect((src.match(/function\s+pctState\s*\(/g) || []).length).toBe(1)
    // 三态字面量都在（少了 neutral 就等于回到二态）
    for (const k of ["'up'", "'down'", "'neutral'"]) {
      expect(src.includes(k), '三态字面量缺失：' + k).toBe(true)
    }
  })
})

afterEach(() => cleanup())
