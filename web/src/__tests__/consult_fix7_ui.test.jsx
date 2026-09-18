// ── §FIX-7(a)(20260919)：咨询页 LLM 配置卡的权限呈现 ──
//
// 锁两件事：
//  1. 非管理员：GET /api/config/llm 回 403 是"无权限"，不是"未配置"——旧实现吞 403 后
//     常驻一张保存必然 403 的可编辑配置卡（误导）；现在只出只读提示卡。
//  2. 管理员且真未配置：可编辑卡照常出现（首配入口不能丢）。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

const h = vi.hoisted(() => ({
  isAdmin: vi.fn(() => true),
  fetchLLMConfig: vi.fn(async () => ({ api_url: 'https://p.example/v1', api_keys: ['sk-…1234'], model: 'm' })),
  fetchConsultHistory: vi.fn(async () => []),
  fetchConsultProMode: vi.fn(async () => ({ enabled: true })),
  setConsultProMode: vi.fn(async () => ({ enabled: true })),
  consultChat: vi.fn(async () => ({ reply: '好的' })),
  clearConsultHistory: vi.fn(async () => ({})),
  setLLMConfig: vi.fn(async () => ({ result: { applied: true } })),
}))

vi.mock('../api/index.js', () => ({
  isAdmin: h.isAdmin,
  isForbidden: (e) => !!(e && e.status === 403),
  fetchLLMConfig: (...a) => h.fetchLLMConfig(...a),
  fetchConsultHistory: (...a) => h.fetchConsultHistory(...a),
  fetchConsultProMode: (...a) => h.fetchConsultProMode(...a),
  setConsultProMode: (...a) => h.setConsultProMode(...a),
  consultChat: (...a) => h.consultChat(...a),
  clearConsultHistory: (...a) => h.clearConsultHistory(...a),
  setLLMConfig: (...a) => h.setLLMConfig(...a),
}))
vi.mock('../ui.jsx', () => ({ showToast: vi.fn() }))

import Consult from '../pages/Consult.jsx'

function forbiddenErr() {
  const e = new Error('请求失败 403')
  e.status = 403
  return e
}

describe('Consult LLM 配置卡权限（§FIX-7a）', () => {
  beforeEach(() => {
    h.isAdmin.mockReset().mockReturnValue(true)
    h.fetchLLMConfig.mockReset().mockResolvedValue({ api_url: 'https://p.example/v1', api_keys: ['sk-…1234'], model: 'm' })
    h.fetchConsultHistory.mockResolvedValue([])
  })

  it('非管理员遇 403 → 只读提示卡，绝不出现可编辑配置卡', async () => {
    h.isAdmin.mockReturnValue(false)
    h.fetchLLMConfig.mockRejectedValue(forbiddenErr())
    render(<Consult />)
    await screen.findByText(/AI 顾问由管理员统一配置/)
    expect(screen.queryByText(/🔑 LLM 配置/)).toBeNull()
    // 只读卡也提示了额度语义（预算 429 的出路：找管理员）
    expect(screen.getByText(/联系管理员/)).toBeInTheDocument()
  })

  it('管理员且未配置（无任何 key/url）→ 可编辑配置卡出现', async () => {
    h.fetchLLMConfig.mockResolvedValue({ api_url: '', api_keys: [], model: '' })
    render(<Consult />)
    await waitFor(() => expect(screen.getByText(/🔑 LLM 配置/)).toBeInTheDocument())
    expect(screen.queryByText(/AI 顾问由管理员统一配置/)).toBeNull()
  })

  it('管理员已配置 → 两张卡都不出现', async () => {
    render(<Consult />)
    await screen.findByText('股票咨询')
    expect(screen.queryByText(/🔑 LLM 配置/)).toBeNull()
    expect(screen.queryByText(/AI 顾问由管理员统一配置/)).toBeNull()
  })
})
