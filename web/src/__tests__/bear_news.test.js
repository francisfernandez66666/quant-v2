// ── 利空资讯置顶排序单元测试 bear_news.test.js ──
// 验证 §NEWS_BEAR 展示保底：prioritizeBearishNews 把 direction==='利空' 的资讯稳定置顶，
// 其余保持相对顺序（Dashboard 与 Hotspot 共用该排序器，截断展示时利空不会被排挤隐藏）。
import { describe, it, expect } from 'vitest'
import { prioritizeBearishNews } from '../utils.js'

describe('prioritizeBearishNews 利空资讯置顶', () => {
  it('利空排到最前，其余保持相对顺序', () => {
    const items = [
      { title: '利好一', direction: '利好' },
      { title: '利空一', direction: '利空' },
      { title: '中性一', direction: '中性' },
      { title: '利空二', direction: '利空' },
    ]
    const out = prioritizeBearishNews(items)
    expect(out.map((n) => n.title)).toEqual(['利空一', '利空二', '利好一', '中性一'])
  })

  it('不修改入参数组（纯函数）', () => {
    const items = [{ title: '甲', direction: '利空' }, { title: '乙', direction: '利好' }]
    const snapshot = items.map((n) => ({ ...n }))
    prioritizeBearishNews(items)
    expect(items).toEqual(snapshot)
  })

  it('无利空时不改变顺序', () => {
    const items = [{ title: 'A', direction: '利好' }, { title: 'B', direction: '利好' }]
    expect(prioritizeBearishNews(items).map((n) => n.title)).toEqual(['A', 'B'])
  })

  it('direction 缺失/空列表不抛错', () => {
    expect(prioritizeBearishNews([])).toEqual([])
    expect(prioritizeBearishNews(undefined)).toEqual([])
    expect(prioritizeBearishNews([{ title: '无方向' }]).length).toBe(1)
  })

  it('截断语义验证：前 15 条内含全部利空（Dashboard slice(0,15) 场景）', () => {
    const items = []
    for (let i = 0; i < 40; i++) items.push({ title: `利好${i}`, direction: '利好' })
    items.push({ title: '尾部的利空', direction: '利空' }) // 旧顺序下会在第 41 位被截断隐藏
    const top = prioritizeBearishNews(items).slice(0, 15)
    expect(top.some((n) => n.direction === '利空')).toBe(true)
  })
})
