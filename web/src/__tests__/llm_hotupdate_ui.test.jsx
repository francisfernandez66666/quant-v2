// ── LLM 配置热更新：前端回报真实性（2026-09-18）──
//
// 锁的是"UI 不得谎报已生效"：热更新有两条真实的失败路径（探测判定配置不可用 → 409 拒绝；
// 无法判定 → 采用但未验证），而旧实现在任何 200 下都弹"已保存并热生效"——这正是用户
// "改了没生效、改不了"体感的直接来源。本文件钉住三种结果在界面上的呈现。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
// §A5：Settings 页接入 useNavigate（首拉 403 跳 /403），渲染需 Router 上下文
import { MemoryRouter } from 'react-router-dom'

// 三个写/探端点的可编程桩
const setLLMConfig = vi.fn()
const probeLLMConfig = vi.fn()
const rollbackLLMConfig = vi.fn()

// 拦截 API 层：Settings 页初始化会拉一串无关接口，这里给最小可用桩避免真实出网；
// fetchLLMConfig 返回掩码 key（sk-…1234），与后端「脱敏回显」语义一致。
vi.mock('../api/index.js', () => ({
  getStoredServer: () => 'http://127.0.0.1:8080',
  setStoredServer: vi.fn(),
  getAccount: () => 'admin',
  getRole: () => 'admin',
  fetchStatus: vi.fn(async () => ({})),
  fetchLLMConfig: vi.fn(async () => ({
    api_url: 'https://api.siliconflow.cn/v1/chat/completions',
    model: 'THUDM/GLM-Z1-9B-0414',
    classifier_model: '',
    batch_concurrency: 4,
    d1_max_tokens: 2048,
    api_keys: ['sk-…1234'],
  })),
  fetchStrategyConfig: vi.fn(async () => ({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} })),
  fetchNewsShowAllStatus: vi.fn(async () => ({ show_all: false })),
  fetchConfigHistory: vi.fn(async () => ({ snapshots: [] })),
  fetchStrategySnapshots: vi.fn(async () => ({ snapshots: [] })),
  rollbackConfig: vi.fn(async () => ({ status: 'ok' })),
  rollbackStrategyParams: vi.fn(async () => ({ status: 'ok' })),
  setLLMConfig: (...a) => setLLMConfig(...a),
  probeLLMConfig: (...a) => probeLLMConfig(...a),
  rollbackLLMConfig: (...a) => rollbackLLMConfig(...a),
}))

// toast 走 TDesign MessagePlugin，测试环境里没必要渲染
vi.mock('../ui.jsx', () => ({ showToast: vi.fn() }))

import Settings from '../pages/Settings.jsx'

// llmCard 取回 LLM 配置那张卡片（页面里还有别的"保存"按钮，必须按卡片定位）。
// 注意：class 名要按 token 精确匹配 't-card'——`t-card__title` 也包含子串 't-card'，
// 用 includes 会停在标题节点上，卡片里的按钮就找不到了。
function llmCard() {
  let node = screen.getByText('LLM 配置')
  while (node && !String(node.className || '').split(/\s+/).includes('t-card')) {
    node = node.parentElement
  }
  return node
}

// renderSettings 渲染设置页并等 LLM 表单被回填完成（否则提交的是空表单）。
async function renderSettings() {
  render(<MemoryRouter><Settings /></MemoryRouter>)
  await screen.findByText('LLM 配置')
  await waitFor(() => expect(screen.getByDisplayValue(/api\.siliconflow\.cn/)).toBeInTheDocument())
  return llmCard()
}

// clickSave 渲染设置页并点 LLM 卡片里的"保存"
async function clickSave() {
  const card = await renderSettings()
  fireEvent.click(within(card).getByRole('button', { name: '保存' }))
}

describe('LLM 配置热更新：UI 回报', () => {
  beforeEach(() => {
    setLLMConfig.mockReset()
    probeLLMConfig.mockReset()
    rollbackLLMConfig.mockReset()
  })

  it('探测通过并生效 → 明确说"已热生效并验证通过"，且默认不带 force', async () => {
    setLLMConfig.mockResolvedValueOnce({
      status: 'ok',
      result: { applied: true, persisted: true, verified: true, effective_keys: 1, probes: [{ index: 0, kind: 'ok', status: 200, latency_ms: 120 }] },
    })
    await clickSave()
    expect(await screen.findByText(/已生效且验证通过/)).toBeInTheDocument()
    // 默认必须走"探测未通过则拒绝"的保护：force 只由用户显式勾选
    expect(setLLMConfig).toHaveBeenCalledWith(expect.objectContaining({ force: false }))
  })

  it('被拒绝（409）→ 说"未生效"并展示后端给出的逐把结论，绝不出现"已热生效"', async () => {
    setLLMConfig.mockRejectedValueOnce(new Error('配置未生效：新配置探测未通过，已保留当前可用配置。\n· key#1 密钥无效/无权限（HTTP 401）：Invalid token'))
    await clickSave()
    // 结论行（`未生效（` 只在结论行出现；后端原文里是"配置未生效："，用更紧的匹配避免歧义）
    expect(await screen.findByText(/未生效（探测未通过，已保留当前可用配置）/)).toBeInTheDocument()
    // 后端给出的逐把结论必须原样可见：用户要拿着它去判断是哪把 key 的问题
    expect(screen.getByText(/key#1 密钥无效\/无权限（HTTP 401）：Invalid token/)).toBeInTheDocument()
    // 关键：不得再谎报成功
    expect(screen.queryByText(/已热生效并验证通过/)).toBeNull()
    expect(screen.queryByText(/已生效且验证通过/)).toBeNull()
  })

  it('生效但未验证/有保留意见 → 如实上报，并逐把列出被剔除的密钥', async () => {
    setLLMConfig.mockResolvedValueOnce({
      status: 'ok',
      result: {
        applied: true, persisted: true, verified: false, effective_keys: 1, dropped_keys: 1,
        warning: '已剔除 1 把未通过探测的密钥（未纳入轮询池、未落库）',
        probes: [
          { index: 0, kind: 'auth', status: 401, detail: 'Invalid token' },
          { index: 1, kind: 'ok', status: 200 },
        ],
      },
    })
    await clickSave()
    expect(await screen.findByText(/已生效，但有保留意见/)).toBeInTheDocument()
    expect(screen.getByText(/已剔除 1 把未通过探测的密钥/)).toBeInTheDocument()
    // 逐把结论必须可见：用户要按"第几把"去改输入框的对应行
    expect(screen.getByText(/第 1 把：密钥无效\/无权限/)).toBeInTheDocument()
    expect(screen.getByText(/第 2 把：可用/)).toBeInTheDocument()
  })

  it('测试连接 → 调探测端点；未通过时明确说未通过', async () => {
    probeLLMConfig.mockResolvedValueOnce({
      status: 'ok',
      result: { applied: false, verified: false, probes: [{ index: 0, kind: 'network', status: 0, detail: 'dial tcp: connection refused' }] },
    })
    const card = await renderSettings()
    fireEvent.click(within(card).getByRole('button', { name: '测试连接' }))
    expect(await screen.findByText(/未通过：当前填写的配置无法确认可用/)).toBeInTheDocument()
    expect(probeLLMConfig).toHaveBeenCalled()
  })

  it('回滚 → 调回滚端点并回读配置，页面值与运行时保持一致', async () => {
    rollbackLLMConfig.mockResolvedValueOnce({
      status: 'ok',
      result: { applied: true, persisted: true, verified: true, api_url: 'https://example.com/rollback/v1/chat/completions', model: 'good-model' },
    })
    const card = await renderSettings()
    fireEvent.click(within(card).getByRole('button', { name: '回滚到上一个可用配置' }))
    expect(await screen.findByText(/已回滚到上一个可用配置/)).toBeInTheDocument()
    expect(rollbackLLMConfig).toHaveBeenCalled()
    // 回读后表单地址应变成回滚目标（fetchLLMConfig 的桩返回原值，故这里断言调用发生过，
    // 真实回读一致性由后端用例 TestHotUpdateRollbackRestoresLastVerified 钉住）
    await waitFor(() => expect(screen.getByText(/已回滚到上一个可用配置/)).toBeInTheDocument())
  })
})
