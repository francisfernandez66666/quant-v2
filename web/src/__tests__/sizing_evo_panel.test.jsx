// ── §SIZING-EVO 仓位进化面板（自动研究页新 tab）展示映射回归锁 ──
// 锁三件事：
//  1. 灰度判定三态互不混淆：pending（观察期未满/未结算）绝不能被显示成「回拒」；
//  2. 运行报告摘要与 CLI 日志同口径（评估数/达标数/进灰度 ID/跳过原因）；
//  3. 夜间摘要必须把"待人工审批"单列——auto_promote=false 时它是常态而非失败。
import { describe, it, expect } from 'vitest'
import { evoVerdictBadge, evoRunSummary } from '../pages/Research.jsx'

describe('§SIZING-EVO 灰度判定标签', () => {
  it('promote → 达标待晋升 / success', () => {
    expect(evoVerdictBadge({ verdict: 'promote' })).toEqual({ label: '达标待晋升', theme: 'success' })
  })

  it('reject → 回拒 / danger', () => {
    expect(evoVerdictBadge({ verdict: 'reject' })).toEqual({ label: '回拒', theme: 'danger' })
  })

  it('pending 是独立状态：观察期未满/未结算都显示「观测中」，不得显示成回拒', () => {
    expect(evoVerdictBadge({ verdict: 'pending' })).toEqual({ label: '观测中', theme: 'warning' })
    expect(evoVerdictBadge({}).label).toBe('观测中')
    expect(evoVerdictBadge(null).label).toBe('观测中')
    expect(evoVerdictBadge(undefined).label).toBe('观测中')
  })
})

describe('§SIZING-EVO 运行报告摘要', () => {
  it('周度：进灰度时点名候选 ID 与样本内 IR 口径', () => {
    const s = evoRunSummary('weekly', { evaluated: 7, passed: 2, entered: 'evoa5b381ac' })
    expect(s).toContain('评估 7 候选')
    expect(s).toContain('2 达标')
    expect(s).toContain('evoa5b381ac')
    expect(s).toContain('进灰度')
  })

  it('周度：无进灰度时带上跳过原因（冷启动/队列满/已在灰度）', () => {
    expect(evoRunSummary('weekly', { evaluated: 0, passed: 0, skipped: '冷启动：可用样本 50 < 200，不动配置' }))
      .toContain('冷启动')
    expect(evoRunSummary('weekly', { skipped: '灰度队列已满（3/3），等 L2 判定消化后再进' }))
      .toContain('队列已满')
  })

  it('夜间：晋升/待人工/回拒/观测中四类分别计数，降级时点名规则', () => {
    const s = evoRunSummary('nightly', {
      promoted: ['a'], awaiting_human: ['b', 'c'], rejected: ['d'], pending: ['e', 'f', 'g'],
      demoted: { rule_id: 'evox', verdict: 'disable' },
    })
    expect(s).toContain('晋升 1')
    expect(s).toContain('待人工 2')
    expect(s).toContain('回拒 1')
    expect(s).toContain('观测中 3')
    expect(s).toContain('已降级 evox')
  })

  it('空报告不抛错（首次运行前 report 为 null）', () => {
    expect(() => evoRunSummary('weekly', null)).not.toThrow()
    expect(evoRunSummary('nightly', null)).toContain('晋升 0')
  })
})
