// ── §CONSULT-DATARICH（2026-11-10）咨询补数轮开关卡的消费契约 ──
// 与行情通道卡同族的三条会要命的形状，逐条钉死：
//   ① 勾选框初值必须取后端下发的**生效值**（出厂开＝true）——从本地 false 起步，
//      "一进卡就点保存"会用稀疏 merge 把出厂开悄悄关掉；
//   ② 未改动时保存必须一条键都不发（不制造假保存与审计噪声）；
//   ③ 改动时只发 consult_data_round 这一枚键（稀疏提交，不夹带别的 runtime 键）。
// 另钉两条本卡特有：代价文案与开关同屏（翻倍/8秒/失败即跳过/预算留位），
// 读取失败时不渲染一个看起来能操作的开关（空卡会被读成"开关是关的"）。
// English: consumer contracts for the consult supplement-round card — effective-value seeding,
// no-op save sends nothing, changed save sends exactly one key, cost copy stays on-screen,
// and a failed load must not render an operable-looking switch.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'

vi.mock('tdesign-react', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, MessagePlugin: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }
})

// 出厂态：指针位缺键＝开，后端折成生效值 true 下发（与 handlers_runtime_config.go 的 view 同形）。
const factoryView = () => ({
  consult_data_round: true,
  hot_keys: ['consult_data_round'],
  restart_required: [],
  changed: [],
})

vi.mock('../api/index.js', () => ({
  fetchRuntimeConfig: vi.fn(async () => factoryView()),
  updateRuntimeConfig: vi.fn(async () => factoryView()),
}))

import ConsultDataRoundCard from '../pages/ConsultDataRoundCard.jsx'
import * as api from '../api/index.js'

describe('咨询补数轮开关卡（出厂开语义）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.fetchRuntimeConfig.mockImplementation(async () => factoryView())
    api.updateRuntimeConfig.mockImplementation(async (patch) => ({
      ...factoryView(),
      ...patch,
      changed: Object.keys(patch),
    }))
  })

  it('勾选框按后端生效值勾选（出厂开不得从 false 起步）', async () => {
    render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getByText(/咨询补数轮（A 轮）/)).toBeInTheDocument())
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes.length).toBe(1) // 本卡只有一枚键，等值而不是"至少"
    expect(boxes[0].checked).toBe(true)
  })

  it('后端下发关闭态时勾选框必须跟着关（读的是生效值不是本地猜测）', async () => {
    api.fetchRuntimeConfig.mockImplementation(async () => ({ ...factoryView(), consult_data_round: false }))
    render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getAllByRole('checkbox')[0].checked).toBe(false))
    expect(screen.getByText(/已关闭（一轮一次调用）/)).toBeInTheDocument()
  })

  it('未改动点保存：一条键都不发，明说没有改动', async () => {
    render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getAllByRole('checkbox')[0].checked).toBe(true))
    fireEvent.click(screen.getByText('保存'))
    await waitFor(() => expect(screen.getByText('没有改动可保存')).toBeInTheDocument())
    expect(api.updateRuntimeConfig).not.toHaveBeenCalled()
  })

  it('改动后保存：只发 consult_data_round 这一枚键（稀疏提交）', async () => {
    // 保存成功后卡会 load() 重读并清掉成功文案——让第二次读取挂起，文案断言才不与重读竞速。
    api.fetchRuntimeConfig
      .mockResolvedValueOnce(factoryView())
      .mockImplementation(() => new Promise(() => {}))
    render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getAllByRole('checkbox')[0].checked).toBe(true))
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    fireEvent.click(screen.getByText('保存'))
    await waitFor(() => expect(api.updateRuntimeConfig).toHaveBeenCalledTimes(1))
    expect(api.updateRuntimeConfig).toHaveBeenCalledWith({ consult_data_round: false })
    await waitFor(() => expect(screen.getByText(/已保存：consult_data_round（保存即生效）/)).toBeInTheDocument())
  })

  it('后端说要重启时才显示需重启（前端不自己猜哪枚要重启）', async () => {
    api.fetchRuntimeConfig
      .mockResolvedValueOnce(factoryView())
      .mockImplementation(() => new Promise(() => {}))
    api.updateRuntimeConfig.mockImplementation(async (patch) => ({
      ...factoryView(),
      ...patch,
      changed: Object.keys(patch),
      restart_required: ['consult_data_round'],
    }))
    render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getAllByRole('checkbox')[0].checked).toBe(true))
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    fireEvent.click(screen.getByText('保存'))
    await waitFor(() => expect(screen.getByText(/需重启 quant 才生效/)).toBeInTheDocument())
  })

  it('代价与边界文案必须与开关同屏（翻倍/8秒/失败即跳过/预算留位/只补两个字段）', async () => {
    const { container } = render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getAllByRole('checkbox').length).toBe(1))
    // 8 秒与后端 consultDataRoundTimeout 同口径；后端改常数必须同批改卡面（跨侧等值在门禁段盯）。
    // 用整卡 textContent 判：逐短语 getByText 会因祖先节点同文而撞"多元素"误报。
    for (const phrase of ['约翻倍', '最多 8 秒', '失败即跳过', '预算留位', '只补两个字段']) {
      expect(container.textContent).toContain(phrase)
    }
  })

  it('读取失败：如实报错并给重试，不渲染可操作的开关', async () => {
    api.fetchRuntimeConfig.mockImplementation(async () => { throw new Error('网络断开') })
    render(<ConsultDataRoundCard />)
    await waitFor(() => expect(screen.getByText(/读取咨询补数轮配置失败：网络断开/)).toBeInTheDocument())
    expect(screen.queryAllByRole('checkbox').length).toBe(0)
    expect(screen.getByText('重试')).toBeInTheDocument()
  })
})