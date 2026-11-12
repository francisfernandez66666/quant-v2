// ── 信号/自选列表排序单元测试 sorting.test.js ──
// 验证 P2#23 问题4 修复：信号列表/自选股列表表头排序。
// sorterStr（字符串字典序：代码/名称/策略/产生时间）与 sorterNum（数值序：现价/总分）
// 为提取自 Signals.jsx 的纯函数排序器，一一断言其排序结果与缺失字段兜底。
import { describe, it, expect } from 'vitest'
import { sorterStr, sorterNum } from '../pages/Signals.jsx'

describe('sorterStr 字符串字典序排序器', () => {
  it('按代码升序排序', () => {
    const rows = [{ code: '600519' }, { code: '000001' }, { code: '300750' }]
    expect(rows.sort(sorterStr('code'))).toEqual([
      { code: '000001' }, { code: '300750' }, { code: '600519' },
    ])
  })

  it('按产生时间字符串排序（YYYY-MM-DD HH:MM:SS 字典序即时间序）', () => {
    const rows = [
      { generated_at: '2026-09-09 10:00:00' },
      { generated_at: '2026-09-07 14:30:00' },
      { generated_at: '2026-09-08 09:15:00' },
    ]
    expect(rows.sort(sorterStr('generated_at')).map((r) => r.generated_at)).toEqual([
      '2026-09-07 14:30:00',
      '2026-09-08 09:15:00',
      '2026-09-09 10:00:00',
    ])
  })

  it('缺失字段按空串参与比较不抛错', () => {
    const rows = [{ name: '甲' }, { name: '' }, { name: '乙' }]
    expect(() => rows.sort(sorterStr('name'))).not.toThrow()
  })
})

describe('sorterNum 数值排序器', () => {
  it('按总分降序场景提供稳定比较器（升序）', () => {
    const rows = [{ total_score: 80 }, { total_score: 95 }, { total_score: 60 }]
    expect(rows.sort(sorterNum('total_score'))).toEqual([
      { total_score: 60 }, { total_score: 80 }, { total_score: 95 },
    ])
  })

  it('按现价升序排序', () => {
    const rows = [{ price: 12.5 }, { price: 3.2 }, { price: 88 }]
    expect(rows.sort(sorterNum('price'))).toEqual([
      { price: 3.2 }, { price: 12.5 }, { price: 88 },
    ])
  })

  it('缺失数值沉底（升序），双缺按 code 稳定兜底', () => {
    const rows = [{ code: 'B', total_score: 70 }, { code: 'Z' }, { code: 'A', total_score: 90 }]
    const cmp = sorterNum('total_score')
    const asc = [...rows].sort(cmp)
    expect(asc.map((r) => r.code)).toEqual(['B', 'A', 'Z'])
    expect(asc[2].total_score).toBeUndefined()
    const desc = [...rows].sort((a, b) => cmp(b, a))
    // 降序靠反转比较器实现，缺值随之浮到顶部（既有产品行为，本次不改排序器）
    expect(desc.map((r) => r.code)).toEqual(['Z', 'A', 'B'])
    expect(desc[0].code).toBe('Z')
    const ties = [
      { code: '600519' }, { code: '000001' },
      { code: '300750', total_score: 50 }, { code: '000002', total_score: 50 },
    ]
    expect([...ties].sort(cmp).map((r) => r.code))
      .toEqual(['000002', '300750', '000001', '600519'])
  })
})