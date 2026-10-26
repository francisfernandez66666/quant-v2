// MigrationSignoffCard.test.jsx — 发版签字单的行为锁（六轮 T4-8/B8）。
//
// 这张卡是唯一"点下去就改资金参数"的前端面，所以钉的三条全是写侧：
//   · S1 未输入 token ⇒ 两个应用按钮都禁用，且零 POST（误触一次就改了合成口径不可接受）；
//   · S2 token 不对 ⇒ 依旧禁用；输入报告回显的那串词才可点；
//   · S3 点击时把**输入框里的 token** 原样发给后端，并在成功后重读报告
//      （落盘后 residual>0 必须还能看见，不能把"写了一半"报成完成）。
//
// English: locks for the sign-off card — no write without the echoed token, and a re-read after
// every apply so a half-applied migration stays visible instead of being reported as done.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, cleanup, fireEvent } from '@testing-library/react'
import { settle } from './settle.js'

const { state } = vi.hoisted(() => ({ state: {} }))

function v4Plan(written = 0) {
  return {
    report: {
      apply: false, scanned: 3, changed: 1, written, failed: 0, followers: 1,
      rows: [{ user_id: '', layer: 'global', mode_before: 'product', mode_after: 'power_mean',
        cap_before: 0.8, cap_after: 1.0, target_before: 0.8, target_after: 1.0, changed: true }],
    },
    authorize_key: 'SIZING-V4-SIGNED',
  }
}

function anchorPlan() {
  return {
    coverage: { positions: 4, with_anchor: 2, missing: 2, backfillable: 1, cost_only: 1, unfillable: 0, coverage_pct: 50, ticket_pct: 25 },
    plan: [{ code: '600000', name: '浦发', cost_price: 12.5, new_anchor: 12.5, anchor_source: 'cost_fallback',
      stop_before: 11.5, stop_after: 11.625, line_delta_bp: 12.5 }],
    authorize_key: 'SIZING-V4-SIGNED',
  }
}

function stubs() {
  return {
    fetchSizingV4Plan: () => v4Plan(),
    fetchAnchorBackfillPlan: () => anchorPlan(),
    applySizingV4Migration: (t) => ({ applied: { written: 1 }, residual: 0, authorize: t }),
    applyAnchorBackfill: (t) => ({ filled: 1, skipped: 0, remaining: [], t_eligible: 0, authorize: t }),
  }
}

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  const s = {}
  for (const name of Object.keys(stubs())) {
    s[name] = vi.fn((...args) => state.impl[name](...args))
  }
  return { ...actual, ...s }
})

vi.mock('../ui.jsx', () => ({ showToast: vi.fn(), confirmDialog: vi.fn(async () => true) }))

import MigrationSignoffCard from '../components/MigrationSignoffCard.jsx'

function btn(text) {
  const el = [...document.querySelectorAll('button,div[type="button"]')].find(
    (b) => (b.textContent || '').trim() === text,
  )
  if (!el) throw new Error(`未找到「${text}」按钮`)
  return el
}
function disabled(el) {
  return el.disabled === true || el.hasAttribute('disabled') || el.classList.contains('t-is-disabled')
}
function tokenInput() {
  const el = [...document.querySelectorAll('input')].find((i) => i.getAttribute('placeholder') === 'SIZING-V4-SIGNED')
  if (!el) throw new Error('未找到签字 token 输入框')
  return el
}

async function mount() {
  render(<MigrationSignoffCard />)
  await settle()
}

describe('发版签字单：token 门 / 前后值 / 落盘后重读', () => {
  beforeEach(async () => {
    cleanup()
    localStorage.clear()
    state.impl = stubs()
    const api = await import('../api/index.js')
    for (const name of Object.keys(state.impl)) {
      if (typeof api[name]?.mockClear === 'function') api[name].mockClear()
    }
  })
  afterEach(() => cleanup())

  it('S1 未输入 token：两个应用按钮都禁用且零写请求', async () => {
    await mount()
    expect(disabled(btn('签字并应用 v4 迁移'))).toBe(true)
    expect(disabled(btn('签字并回填锚点'))).toBe(true)
    const api = await import('../api/index.js')
    expect(api.applySizingV4Migration.mock.calls.length).toBe(0)
    expect(api.applyAnchorBackfill.mock.calls.length).toBe(0)
  })

  it('S2 token 不对仍禁用；输入报告回显的那串词才可点', async () => {
    await mount()
    fireEvent.change(tokenInput(), { target: { value: '我看过了' } })
    await settle()
    expect(disabled(btn('签字并应用 v4 迁移'))).toBe(true)
    fireEvent.change(tokenInput(), { target: { value: 'SIZING-V4-SIGNED' } })
    await settle()
    expect(disabled(btn('签字并应用 v4 迁移'))).toBe(false)
    expect(disabled(btn('签字并回填锚点'))).toBe(false)
  })

  it('S3 报告逐行给出前后值（迁移看 mode/上限，回填看止损触发价位移）', async () => {
    await mount()
    const text = document.body.textContent || ''
    expect(text).toContain('product → power_mean')
    expect(text).toContain('0.80 → 1.00')
    expect(text).toContain('cost_fallback')
    expect(text).toContain('11.500')
    expect(text).toContain('11.625')
  })

  it('S4 应用：把输入的 token 原样发出，成功后重读两份报告', async () => {
    await mount()
    fireEvent.change(tokenInput(), { target: { value: 'SIZING-V4-SIGNED' } })
    await settle()
    const api = await import('../api/index.js')
    fireEvent.click(btn('签字并应用 v4 迁移'))
    await settle()
    expect(api.applySizingV4Migration.mock.calls[0][0]).toBe('SIZING-V4-SIGNED')
    // 落盘后必须再读一次报告（residual/remaining 只有重读才看得见）。
    expect(api.fetchSizingV4Plan.mock.calls.length).toBeGreaterThanOrEqual(2)
    fireEvent.click(btn('签字并回填锚点'))
    await settle()
    expect(api.applyAnchorBackfill.mock.calls[0][0]).toBe('SIZING-V4-SIGNED')
  })

  it('S5 接口缺失时不崩（部分 mock 的取值即抛），整卡退回可读错误态', async () => {
    state.impl = {}
    cleanup()
    render(<MigrationSignoffCard />)
    await settle()
    const text = document.body.textContent || ''
    expect(text).toContain('两份报告都读不到')
  })
})
