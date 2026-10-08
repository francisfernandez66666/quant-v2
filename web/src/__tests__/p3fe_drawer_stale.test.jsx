// ── §P3-FE P4/P5 + §P2-I N4（2026-10-06 修复批 波 6）抽屉「价与涨幅同源 + 后到丢弃」──
//
// 两条缺陷在同一行读数上，形态不同但根因同一：**抽屉头部的价是活的，涨幅不是**。
//  · P4（AUDIT_20261005 / Drawer:86）：现价每 5s 走 fetchStockLookup 刷新，而涨幅取的是
//    `changePct` 这个**打开抽屉那一刻**由宿主表格传进来的 props ⇒ 屏上同一分钟里
//    「价格已经涨上去了、涨幅还是开抽屉那一刻的数」。这类矛盾读数不会被当成 bug 报上来，
//    因为它看起来总有一个是"对的"，但它恰恰是决策页最不该出现的东西。
//  · N4（§P2-I）：本抽屉被五页共用，轮询没有请求代号守卫时，慢的第 1 轮可以在第 2 轮之后落地，
//    于是 P4 修完之后的覆盖面更宽了——被覆盖的是**整行价+涨幅**（用户看到"价格跳回去了"）。
//
// 断言分三层，缺任何一层都会留下假绿空间：
//  1) 纯函数等值（resolveDrawerChg/numOrNil/deriveChg）：把「同批优先、缺数不折叠成 0、
//     只有实时腿全缺才标冻结」三条判据钉成具体数字，不看 DOM；
//  2) 静态派生锁：旧写法 `= changePct` 直取 props 的形态、以及"判据被复制进 JSX"的形态必须为 0 命中，
//     并且冻结标注文案在位（判据在但没接出去＝等于没修，同族 §DEADGAUGE 的教训）；
//  3) 挂载行为腿（vi.useFakeTimers，照 §M-10 P1 先例）：第二次轮询的新涨幅真的上屏（P4），
//     第 1 轮慢响应后到整包丢弃（N4）。
//
// English: wave-6 locks for the global stock drawer — the change% must come from the same
// reading batch as the price (with an explicit "as-of-open" label only when no live reading
// exists), missing values must never collapse into 0/flat, and a late poll response must not
// overwrite the newest one.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
// §DET-TIME：等 UI 一律用 settle()（排空微任务），不用 waitFor/findBy。
import { settle } from './settle.js'

const HERE = path.dirname(fileURLToPath(import.meta.url))

// 受控 api：只有 fetchStockLookup 需要换返回体（其余 api 实现原样保留）。
// 桩名必须来自**静态函数**而不是 state.impl：mock factory 在本文件 import 组件时就执行，
// 那时 beforeEach 还没跑、state.impl 是空对象 ⇒ 一个桩都不会注册，测试会去打真实端点（假绿形态）。
// 同族 §M-10 用 okPayloads() 就是这个原因，这里照同一口径。
function drawerStubs() {
  return { fetchStockLookup: () => ({}) }
}
const { state } = vi.hoisted(() => ({ state: { impl: {} } }))
vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(drawerStubs())) {
    stubs[name] = vi.fn(async (...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})
// 分时/盘口子件在抽屉里会自己取数，本文件只测抽屉头部读数 ⇒ 桩掉，避免把别的端点卷进来
vi.mock('../components/MinuteView.jsx', () => ({ default: () => null }))

import StockDetailDrawer, { numOrNil, batchPrice, deriveChg, resolveDrawerChg } from '../components/StockDetailDrawer.jsx'

// 剥注释（块 /* */ 与行 //）：静态派生锁只看代码行，
// 否则文件头说明注释里引用的旧写法字符串会被当成一处实现（锁自己造红）。
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

describe('§P3-FE P4/P5 抽屉涨幅同源：纯函数等值', () => {
  // D1 numOrNil：缺数的四种形态一律 null，绝不折叠成 0（0 是合法平盘读数，不能拿来表示「没读到」）
  it('D1 缺数形态（null/undefined/空串/非数）一律解成 null', () => {
    expect(numOrNil(null)).toBeNull()
    expect(numOrNil(undefined)).toBeNull()
    expect(numOrNil('')).toBeNull()
    expect(numOrNil('abc')).toBeNull()
    expect(numOrNil(NaN)).toBeNull()
    // 反向：合法的 0 必须还是 0（把 0 也判成缺数＝平盘无法显示）
    expect(numOrNil(0)).toBe(0)
    expect(numOrNil('1.23')).toBe(1.23)
  })

  // D2 batchPrice：后端取不到行情时 price 回 0（handlers_fix.go:1838），0 不是可信现价
  it('D2 现价只在同批 price>0 时可信', () => {
    expect(batchPrice(null)).toBeNull()
    expect(batchPrice({ price: 0, change_pct: 0, prev_close: 0 })).toBeNull()
    expect(batchPrice({ price: '10.5' })).toBe(10.5)
  })

  // D3 deriveChg：与后端同为百分数口径（1.23＝+1.23%），并按 r2 口径收拢 2 位小数便于等值断言
  it('D3 同批 price/prev_close 自算涨幅（百分数口径、2 位小数）', () => {
    expect(deriveChg(10.2, 10)).toBe(2)
    expect(deriveChg(9.8, 10)).toBe(-2)
    // 1/3 这种无限小数：Math.round 收拢后必须等于 0.33（不是 0.3333…，也不是 0）
    expect(deriveChg(10.033333, 10)).toBe(0.33)
    expect(deriveChg(10.5, 0)).toBeNull()
    expect(deriveChg(null, 10)).toBeNull()
    expect(deriveChg(10, null)).toBeNull()
  })

  // D4 三态判据等值：同批 change_pct / 同批自算 / 只有 props 冻结值
  it('D4 实时腿优先，props 冻结值只在实时腿全缺时兜底且标 frozen', () => {
    // ① 同批 change_pct 直接用（哪怕与 prev_close 自算结果不同——change_pct 是后端权威口径）
    const a = resolveDrawerChg({ price: 10.5, change_pct: 5, prev_close: 10 }, 1)
    expect(a.chg).toBe(5)
    expect(a.live).toBe(true)
    expect(a.frozen).toBe(false)
    expect(a.up).toBe(true)
    // ② change_pct 缺席（老后端/降级）→ 同批 prev_close 自算，仍是实时腿、不标冻结
    const b = resolveDrawerChg({ price: 9.9, prev_close: 10 }, 3.4)
    expect(b.chg).toBe(-1)
    expect(b.live).toBe(true)
    expect(b.frozen).toBe(false)
    expect(b.up).toBe(false)
    // ③ lookup 没给行情（price=0）→ 回落 props，且 frozen=true（该标注必须出现）
    const c = resolveDrawerChg({ price: 0, change_pct: 0, prev_close: 0 }, 3.4)
    expect(c.chg).toBe(3.4)
    expect(c.live).toBe(false)
    expect(c.frozen).toBe(true)
    // ④ 还没拉到 + 宿主也没传涨幅 → 三个都不许伪造：chg null、frozen false、up false
    const d = resolveDrawerChg(null, null)
    expect(d.chg).toBeNull()
    expect(d.frozen).toBe(false)
    expect(d.up).toBe(false)
  })

  // D5 P5 反证：旧口径「涨幅恒取 props」在新读数下必须与实现**不等**
  // （只断"新的对"的话，把实现改回 `const chg = changePct` 照样绿——这条就是那枚反证）
  it('D5 改回只读 props 的旧口径必然与同源实现给出不同读数（P5 反证）', () => {
    const quote = { price: 11, change_pct: 10, prev_close: 10 }
    const legacy = numOrNil(3.4) // 旧实现：rawChg = changePct（开抽屉那一刻）
    expect(resolveDrawerChg(quote, 3.4).chg).toBe(10)
    expect(resolveDrawerChg(quote, 3.4).chg).not.toBe(legacy)
    // 缺数形态的旧口径更危险：props 缺失时 Number(v) 折成 0，实现必须给 null 而不是 0
    expect(resolveDrawerChg(null, undefined).chg).not.toBe(Number(undefined) || 0)
  })
})

describe('§P3-FE P4/P5 静态派生锁：判据唯一出口 + 冻结标注接线', () => {
  // S1 判据出口各恰一处（定义两次＝组件里还留了一份旧判据，改一处漏一处）
  it('S1 resolveDrawerChg/numOrNil/deriveChg 定义各恰一处', () => {
    const src = readSrc('components/StockDetailDrawer.jsx')
    for (const fn of ['resolveDrawerChg', 'numOrNil', 'deriveChg', 'batchPrice']) {
      expect((src.match(new RegExp('function\\s+' + fn + '\\s*\\(', 'g')) || []).length, fn + ' 定义数应为 1').toBe(1)
    }
    expect((src.match(/resolveDrawerChg\(quote, changePct\)/g) || []).length).toBe(1)
  })

  // S2 旧形态清零：涨幅直接取 props、以及在 JSX 里就地写三元判据
  it('S2 涨幅直取 props 的旧形态 0 命中', () => {
    const src = readSrc('components/StockDetailDrawer.jsx')
    // `const rawChg = changePct`（波 6 前的实现）以及任何形式的「chg 等于 changePct」都属旧口径
    expect((src.match(/const\s+(rawChg|chg)\s*=\s*changePct\b/g) || []).length).toBe(0)
    // 组件里不许再出现本地的缺数判定三元式（判据必须走 numOrNil，两处两套语义就是本批缺陷本体）
    expect((src.match(/Number\.isFinite\(Number\(quote\.price\)\)/g) || []).length).toBe(0)
  })

  // S3 接线锁：frozen 判据必须真的接上标注（定义没接＝标注恒不出现，而只看函数体的静态扫描看不见这件事）
  it('S3 冻结标注 testid 与 reading.frozen 接线在位', () => {
    const src = readSrc('components/StockDetailDrawer.jsx')
    expect((src.match(/const chgFrozen = reading\.frozen/g) || []).length).toBe(1)
    expect((src.match(/data-testid="sdd-chg-frozen"/g) || []).length).toBe(1)
    expect(src.includes('（开抽屉时刻值）')).toBe(true)
    // 缺读数不得染色：现价颜色必须先看 chg 是否为 null（旧写法无条件按 chgUp 染色）
    expect((src.match(/color: chg == null \? 'var\(--app-faint\)'/g) || []).length).toBe(1)
  })
})

describe('§P3-FE P4 / §P2-I N4 挂载行为腿（fake timers 轮询）', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.impl = {
      fetchStockLookup: () => ({ code: '600000', name: '浦发银行', price: 10.0, change_pct: 0.5, prev_close: 9.95 }),
    }
    const api = await import('../api/index.js')
    if (typeof api.fetchStockLookup?.mockClear === 'function') api.fetchStockLookup.mockClear()
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  // P4 第二次轮询的新涨幅必须上屏（这是"同源"唯一能被观察到的形式：同一行两个读数一起变）
  it('P4 5s 轮询到新价格 → 涨幅随同批读数一起刷新（不再是开抽屉那一刻的值）', async () => {
    vi.useFakeTimers()
    let calls = 0
    state.impl.fetchStockLookup = () => {
      calls += 1
      if (calls === 1) return Promise.resolve({ price: 10.0, change_pct: 0.5, prev_close: 9.95, name: '浦发银行' })
      return Promise.resolve({ price: 11.0, change_pct: 10.5, prev_close: 9.95, name: '浦发银行' })
    }
    render(<StockDetailDrawer open code="600000" name="浦发银行" price={10} changePct={0.5} onClose={() => {}} />)
    await settle()
    expect(screen.getByTestId('sdd-price').textContent).toBe('10.00')
    expect(screen.getByTestId('sdd-chg').textContent).toBe('+0.50%')
    // 宿主传进来的冻结值 0.5 已不是最新读数，标注「开抽屉时刻值」不该出现
    expect(screen.queryByTestId('sdd-chg-frozen')).toBeNull()

    await act(async () => { vi.advanceTimersByTime(5000) })
    await settle()
    expect(calls).toBe(2)
    expect(screen.getByTestId('sdd-price').textContent).toBe('11.00')
    expect(screen.getByTestId('sdd-chg').textContent).toBe('+10.50%')
  })

  // P4b change_pct 缺席的老后端：同批 prev_close 自算，且不标冻结（实时腿仍然在）
  it('P4b lookup 无 change_pct → 用同批 prev_close 自算，不标「开抽屉时刻值」', async () => {
    vi.useFakeTimers()
    state.impl.fetchStockLookup = () => Promise.resolve({ price: 9.8, prev_close: 10, name: '浦发银行' })
    render(<StockDetailDrawer open code="600000" name="浦发银行" price={10} changePct={3.4} onClose={() => {}} />)
    await settle()
    expect(screen.getByTestId('sdd-chg').textContent).toBe('-2.00%')
    expect(screen.queryByTestId('sdd-chg-frozen')).toBeNull()
    // 反证：3.4（宿主冻结值）不得出现在屏上
    expect(screen.queryByText('+3.40%')).toBeNull()
  })

  // P4c 行情取不到（price=0）：回落宿主值**并且**必须标注口径——这是「不撒谎」的半边
  it('P4c lookup 回 price=0 → 显示宿主初值并标注「开抽屉时刻值」', async () => {
    vi.useFakeTimers()
    state.impl.fetchStockLookup = () => Promise.resolve({ price: 0, change_pct: 0, prev_close: 0, name: '' })
    render(<StockDetailDrawer open code="600000" name="浦发银行" price={10} changePct={3.4} onClose={() => {}} />)
    await settle()
    expect(screen.getByTestId('sdd-price').textContent).toBe('10.00')
    expect(screen.getByTestId('sdd-chg').textContent).toBe('+3.40%')
    expect(screen.getByTestId('sdd-chg-frozen')).toBeInTheDocument()
  })

  // P4d 双侧都没读数：现价/涨幅格整体不渲染（旧写法把 null 折成 0 显示 +0.00% 假平盘）
  it('P4d 无 lookup 价且无 props 涨幅 → 不渲染涨幅格（缺数不冒充平盘）', async () => {
    vi.useFakeTimers()
    state.impl.fetchStockLookup = () => Promise.resolve({ price: 0, change_pct: 0, prev_close: 0 })
    render(<StockDetailDrawer open code="600000" name="浦发银行" onClose={() => {}} />)
    await settle()
    expect(screen.queryByTestId('sdd-chg')).toBeNull()
    expect(screen.queryByTestId('sdd-price')).toBeNull()
    expect(screen.queryByTestId('sdd-chg-frozen')).toBeNull()
  })

  // N4 第 1 轮慢响应后到：整行（价+涨幅）都不许覆盖第 2 轮的新读数
  it('N4 慢的第 1 轮后到 → 守卫整包丢弃，价格与涨幅仍是第 2 轮', async () => {
    vi.useFakeTimers()
    let resolveSlow = null
    let calls = 0
    state.impl.fetchStockLookup = () => {
      calls += 1
      if (calls === 1) return new Promise((r) => { resolveSlow = r })
      return Promise.resolve({ price: 11.0, change_pct: 10.5, prev_close: 9.95, name: '浦发银行' })
    }
    render(<StockDetailDrawer open code="600000" name="浦发银行" price={10} changePct={0.5} onClose={() => {}} />)
    await settle()
    await act(async () => { vi.advanceTimersByTime(5000) })
    await settle()
    expect(screen.getByTestId('sdd-price').textContent).toBe('11.00')
    expect(screen.getByTestId('sdd-chg').textContent).toBe('+10.50%')
    // 旧轮此刻才返回：守卫必须按代号丢掉（P4 之后被覆盖的是整行读数，不只是价格）
    await act(async () => { resolveSlow({ price: 9.0, change_pct: -9.55, prev_close: 9.95, name: '浦发银行' }) })
    await settle()
    expect(screen.getByTestId('sdd-price').textContent, '§P2-I N4：迟到的旧轮不得覆盖最新价').toBe('11.00')
    expect(screen.getByTestId('sdd-chg').textContent, '§P2-I N4：迟到的旧轮不得覆盖最新涨幅').toBe('+10.50%')
  })

  // N4b 换股（code 变更）：旧股的在途响应不得写进新股的抽屉。
  // 覆盖面说明（不吹成守卫腿）：这条走的是 effect 清理里的 `alive=false` 半边——
  // 摘掉 guard 它也绿，所以**代号守卫那一半边由 N4 主腿负责**（同一次 effect 内两轮竞态，alive 都是 true）。
  it('N4b code 变更 → 旧股在途响应被丢弃，抽屉显示新股读数', async () => {
    vi.useFakeTimers()
    let resolveOld = null
    state.impl.fetchStockLookup = (code) => {
      if (code === '600000') return new Promise((r) => { resolveOld = r })
      return Promise.resolve({ price: 20.0, change_pct: 2, prev_close: 19.6, name: '美的集团' })
    }
    const { rerender } = render(<StockDetailDrawer open code="600000" name="浦发银行" onClose={() => {}} />)
    await settle()
    rerender(<StockDetailDrawer open code="000333" name="美的集团" onClose={() => {}} />)
    await settle()
    expect(screen.getByTestId('sdd-price').textContent).toBe('20.00')
    await act(async () => { resolveOld({ price: 9.0, change_pct: -9.55, prev_close: 9.95, name: '浦发银行' }) })
    await settle()
    expect(screen.getByTestId('sdd-price').textContent, '§P2-I N4：换股后旧股响应不得回写').toBe('20.00')
    expect(screen.getByTestId('sdd-chg').textContent).toBe('+2.00%')
  })
})
