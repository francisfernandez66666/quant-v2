// §STATUS-TOGGLE / §B4-FRESH-EXCESS（2026-11-28 owner）前端契约锁：
//  ① 候选详情「多赚 X%」必须优先取 B4 最新回测回填值，且 reason 是追加式文本，
//     必须取**最后一次**；旧实现恒取发现阶段的 `反推超额=`，现场长期卡在 16.4%。
//  ② 已通过/已驳回候选必须仍可互改状态，审批后还要同步刷新战法库，
//     否则新回测结果出来后既看不到也改不动。
import { describe, it, expect } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const src = fs.readFileSync(path.join(root, 'pages/Research.jsx'), 'utf8')
const auto = fs.readFileSync(path.join(root, '../../cmd/research/auto.go'), 'utf8')

describe('B4 最新回测超额覆盖发现阶段反推值', () => {
  it('前端有 latestExcess 并取 reason 中最后一次最新回测超额', () => {
    expect(src).toContain('function latestExcess(c)')
    expect(src).toContain("parseReason(c, '最新回测超额')")
    expect(src).toMatch(/all\[all\.length - 1\]/)
    expect(src).toContain('title={parseReason(c, \'最新回测超额\') === null')
  })
  it('B4 回测回填时写入权威键（负锁：不得只写旧的 B4事件 文案）', () => {
    expect(auto).toContain('最新回测超额=%.4f')
    expect(auto).toContain('rep.TotalEvents')
  })
})

describe('已审批战法状态互改', () => {
  it('审批按钮不再被 proposed 状态门控，且提供改通过/改驳回文案', () => {
    expect(src).not.toContain("canApprove && c.status === 'proposed' && (")
    expect(src).toContain('改通过并应用')
    expect(src).toContain('改驳回')
  })
  it('审批动作后同步刷新战法库', () => {
    const start = src.indexOf('async function refetchCandidatesAfterAction()')
    const body = src.slice(start, start + 400)
    expect(body).toContain('await loadLibrary()')
  })
})
