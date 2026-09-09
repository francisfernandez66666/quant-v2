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

  it('缺失数值按 0 参与比较', () => {
    const rows = [{ total_score: 70 }, {}, { total_score: 90 }]
    const sorted = rows.sort(sorterNum('total_score'))
    expect(sorted[0]).toEqual({}) // 缺失字段按 0 → 排最前
    expect(sorted.map((r) => r.total_score || 0)).toEqual([0, 70, 90])
  })
})