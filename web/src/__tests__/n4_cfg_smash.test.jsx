// ── §N-4（2026-09-22 傍晚批 §CFGSMASH + §中-6）设置页战法参数保存闸行为锁 ──
//
// 缺陷链：Settings.jsx 加载战法 `catch (_) {}` 静默 → 表单落空对象 → 数字缺失被 `?? 0`
// 渲染成 0 → 整份 POST → 旧后端全量替换 = 五套战法阈值清零落库（重启救不回）。
// 修法三闸：① 加载三态（loading/loaded/error），error 禁保存+红条「读取失败，禁止保存」；
// ② 缺失数字渲染为空 + 保存前必填校验（缺失≠0）；③ §中-6 updated_at 乐观锁，
// 版本冲突（409）→ 红条转「版本冲突，禁止保存」，只能重载后再改。后端稀疏 merge 见
// internal/server/strategy_config_test.go 同批用例。
// English: §N-4 frontend locks — load-failed disables saving with a red banner, missing
// numbers are validated as required (never folded into 0), and a 409 version conflict
// re-blocks the form until reload.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
// §DET-TIME（2026-09-24）：等 UI 一律用 settle()（排空微任务队列）而不是 waitFor/findBy——这些用例的接口都是 resolved promise 的 mock，用真实时钟轮询在邻居负载下必偶发红（见 settle.js 文件头）。
import { settle } from './settle.js'
import { MemoryRouter } from 'react-router-dom'

const { state } = vi.hoisted(() => ({ state: {} }))

// 战法配置正常载荷：两组各挑两个字段给值即可（其余字段由「缺失必填校验」用例专门锤）。
function strategyPayload() {
  return {
    updated_at: 'v1',
    dragon: { f1_seal_weight: 0.4, f2_resonance_weight: 0.2, f3_premium_weight: 0.2, f4_rs_weight: 0.2, pullback_max_pct: 0.03, breaker_sell_half_pct: 0.02, breaker_sell_all_pct: 0.05, buy_pullback_sell_half_pct: 0.03, buy_pullback_sell_all_pct: 0.05, buy_day_close_below: 0.01, next_open_if_below: 0.02, take_profit_pct: 10 },
    double_bump: { first_break_volume_multiple: 2.5, second_break_volume_multiple: 3, adjust_vol_ratio_max: 0.5, position_weight: 0.4, ma_weight: 0.3, volume_weight: 0.3, double_bump_take_profit_pct: 0.08 },
    n_shape: { n_pattern_score_threshold: 70, hard_stop_loss: 0.05 },
    dragon_return: { stop_loss_pct: 0.03, take_profit_pct: 0.12, max_hold_days: 5, target1_multiplier: 1.5, target2_multiplier: 2.5, trailing_drawback: 0.05 },
    momentum: { volume_price_weight: 40, macd_weight: 30, trend_weight: 30, momentum_gate_enabled: true, momentum_delta_tol: 2 },
  }
}

function okPayloads() {
  return {
    getStoredServer: () => 'http://localhost:8080',
    getAccount: () => 'admin',
    getToken: () => 'tok',
    getRole: () => 'member', // 非 admin：跳过配置历史卡，减少挂载面
    fetchStatus: () => ({}),
    fetchLLMConfig: () => ({ api_url: '', api_keys: [] }),
    fetchNewsShowAllStatus: () => ({ news_show_all: false }),
    // §N-1 同款手法：战法读取/保存两个端点是本文件唯一覆盖对象
    fetchStrategyConfig: () => strategyPayload(),
    setStrategyConfig: () => ({ status: 'ok', updated_at: 'v2' }),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const stubs = {}
  for (const name of Object.keys(okPayloads())) {
    // 同步包装：getToken/getStoredServer 等在 useState 初始化器里被**同步**调用，
    // 异步壳会让它们拿到 Promise（token.slice 崩溃）。实现自身返回 Promise 时照常 await 兼容。
    stubs[name] = vi.fn((...args) => state.impl[name](...args))
  }
  return { ...actual, ...stubs }
})

import Settings from '../pages/Settings.jsx'

const ERR500 = (msg) => Object.assign(new Error(msg), { status: 500 })

// getSaveButton 读「保存战法参数」按钮。
// ⚠ 不能按 role 查询：TDesign Button 在 disabled=true 时渲染成 `<div type=button disabled>`
// （无 button role），enabled 时才是 `<button>`——按 role 找会在 error 态查不到而假失败。
// English: query by DOM (not role) because tdesign swaps the primary button's tag to a
// role-less div when disabled.
function getSaveButton() {
  const el = [...document.querySelectorAll('button,div[type="button"]')]
    .find((b) => (b.textContent || '').trim() === '保存战法参数')
  if (!el) throw new Error('未找到「保存战法参数」按钮')
  return el
}

// getSaveButtonDisabled 统一判禁用态：原生 button 走 .disabled 属性，
// tdesign 禁用态 div 走 disabled 特性/t-is-disabled 类。
function getSaveButtonDisabled() {
  const el = getSaveButton()
  return el.disabled === true || el.hasAttribute('disabled') || el.classList.contains('t-is-disabled')
}

describe('§N-4 战法参数保存闸：读取失败禁保存 / 缺失必填 / 版本冲突 409', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.impl = okPayloads()
    const api = await import('../api/index.js')
    for (const name of Object.keys(state.impl)) {
      if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
    }
  })
  afterEach(() => cleanup())

  // E1 加载 500 → 红条「读取失败，禁止保存」+ 保存按钮禁用；点保存绝不打 POST。
  it('E1 战法加载失败 → 红条 + 保存禁用 + setStrategyConfig 零调用（旧形态：静默落 0 整份 POST）', async () => {
    state.impl.fetchStrategyConfig = () => { throw ERR500('config 拉取失败') }
    render(<MemoryRouter><Settings /></MemoryRouter>)
    await settle()
    const banner = screen.getByText(/读取失败，禁止保存/)
    expect(banner).toBeInTheDocument()
    await settle()
    expect(getSaveButtonDisabled()).toBe(true)
    // 「重载」再失败一次：确认错误态可重入且依旧禁存（防只挡首帧、点一下就解禁的假闸）。
    fireEvent.click(screen.getByRole('button', { name: /重载/ }))
    await settle()
    fireEvent.click(getSaveButton())
    const api = await import('../api/index.js')
    expect(api.setStrategyConfig.mock.calls.length, '§N-4：error 态绝不发起保存').toBe(0)
  })

  // E2 加载成功但字段缺失（n_shape 整组后端没回）→ 必填校验拦截，缺失不得折叠成 0 提交。
  it('E2 数字字段缺失 → 必填校验拒绝保存，payload 不出现“缺失写 0”', async () => {
    const partial = strategyPayload()
    delete partial.n_shape
    state.impl.fetchStrategyConfig = () => partial
    render(<MemoryRouter><Settings /></MemoryRouter>)
    await settle()
    expect(getSaveButtonDisabled()).toBe(false)
    fireEvent.click(getSaveButton())
    const api = await import('../api/index.js')
    // 等一拍：校验是同步的，若误放行会立刻调用。用 settle() 而不是 setTimeout(50) ——
    // 后者在邻居抢 CPU 时会把"还没轮到跑"读成"跑了且没调用"，方向是假绿不是假红，更该避免。
    await settle()
    expect(api.setStrategyConfig.mock.calls.length, '§N-4：缺失字段必须被必填校验拦下').toBe(0)
  })

  // E3 正常路径 → 保存成功，payload 携带 §中-6 updated_at=v1 基线与完整表单值；成功后基线推进。
  it('E3 正常保存 → payload 带 updated_at 基线（后端稀疏 merge 的另一半判据）', async () => {
    render(<MemoryRouter><Settings /></MemoryRouter>)
    await settle()
    expect(getSaveButtonDisabled()).toBe(false)
    fireEvent.click(getSaveButton())
    const api = await import('../api/index.js')
    await settle()
    expect(api.setStrategyConfig.mock.calls.length).toBe(1)
    const payload = api.setStrategyConfig.mock.calls[0][0]
    expect(payload.updated_at).toBe('v1')
    expect(payload.dragon.take_profit_pct).toBe(10)
    expect(payload.n_shape.n_pattern_score_threshold).toBe(70)
  })

  // E4 版本冲突 409 → 红条转「版本冲突，禁止保存」+ 按钮重新禁用（只能走重载）。
  it('E4 保存遇 409（他人已先写）→ 冲突红条 + 再禁保存，不自动重放', async () => {
    state.impl.setStrategyConfig = () => { throw Object.assign(new Error('version conflict'), { status: 409 }) }
    render(<MemoryRouter><Settings /></MemoryRouter>)
    await settle()
    expect(getSaveButtonDisabled()).toBe(false)
    fireEvent.click(getSaveButton())
    await settle()
    const banner = screen.getByText(/版本冲突，禁止保存/)
    expect(banner).toBeInTheDocument()
    await settle()
    expect(getSaveButtonDisabled()).toBe(true)
  })
})
