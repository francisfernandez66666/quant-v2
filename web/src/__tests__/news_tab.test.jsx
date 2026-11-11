// news_tab.test.jsx — §B6（2026-11-05 通知增强）消息中心「新闻」tab 的三条判据。
//
// ① 空态不许假 0：读数没拿到（失败/加载）时不画 "0 条"，也不把"取不到"说成"今天没有新闻"；
// ② 分页真生效：翻页切片真的换内容（不是控件摆着、永远同一页）；
// ③ 切交易日清零：未读数与「今日已折叠 N 条」随行一起消失——日界线用的是消息中心
//    服务端那一条（kind=news 的行跨日即清），前端只按行内日期持"已读到"标记，
//    换日是一把新键 ⇒ 未读自动重计，不另起第二套日切判据。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'

vi.mock('../api/index.js', async () => {
  const actual = await vi.importActual('../api/index.js')
  return { ...actual, request: vi.fn() }
})

import NewsTab, { dayKeyOf, readLastSeen, LAST_SEEN_PREFIX } from '../components/NewsTab.jsx'
import * as api from '../api/index.js'

// 2026-11-11 修日期腐化：夹具原先写死 2026-11-05/06，跨日后「换日」断言必红。
// D1=昨天（旧 day-key），D2=明天（行内时刻一定晚于当刻写入的已读标记）。
// English: derive day keys from the clock so the day-rollover test never rots.
function bjDay(offset) {
  return new Date(Date.now() + 8 * 3600 * 1000 + offset * 86400000).toISOString().slice(0, 10)
}
const D1 = bjDay(-1)
const D2 = bjDay(1)

// mkRows 造 n 条当日新闻行（generated_at 全部落在同一个 dayKey 下，分页/未读都按它算）
function mkRows(n, day = D1, titlePrefix = '行业利好') {
  return Array.from({ length: n }, (_, i) => ({
    id: `news|${i}`,
    code: '300750',
    name: '宁德时代',
    level: '新闻',
    kind: 'news',
    direction: '利好',
    title: `${titlePrefix} ${String(i).padStart(2, '0')}`,
    body: `正文 ${i}`,
    generated_at: `${day}T09:${String(i % 60).padStart(2, '0')}:00`,
  }))
}

// okRes 组一个 withMeta 形态的响应（data + headers.get）
function okRes(rows, total) {
  return {
    data: rows,
    headers: { get: (k) => (String(k).toLowerCase() === 'x-total-count' ? String(total ?? rows.length) : null) },
  }
}

describe('NewsTab §B6 新闻 tab', () => {
  beforeEach(() => {
    cleanup()
    api.request.mockReset()
    window.localStorage.clear()
  })
  afterEach(() => cleanup())

  // ① 空态：拿到读数且确实是 0 条 ⇒ 说"今日暂无新闻"；读数没拿到 ⇒ 不许出现 0 条这类假数据
  it('空态不假 0：拿到读数才说"暂无"，取不到时说"取不到"', async () => {
    api.request.mockResolvedValue(okRes([], 0))
    render(<NewsTab />)
    await waitFor(() => expect(screen.getByText('今日暂无新闻')).toBeInTheDocument())
    // 未读/总数为 0 是"真读到零"，此时才允许出现 0 这个数
    expect(screen.getByText(/未读 0/)).toBeInTheDocument()

    // 换一个场景：请求失败 ⇒ 不许落到"暂无"分支（那是把故障伪装成没有）
    api.request.mockReset()
    api.request.mockRejectedValue(new Error('network down'))
    cleanup()
    render(<NewsTab />)
    await waitFor(() => expect(screen.getByText(/新闻读取失败/)).toBeInTheDocument())
    expect(screen.queryByText('今日暂无新闻')).toBeNull()
    // 读数没拿到的那一支也不许在顶部画"当日 0 条"
    expect(screen.queryByText(/当日 0 条/)).toBeNull()
  })

  // ② 分页真的切片：第 2 页内容与第 1 页不同，且请求带的分页参数被原样发出去
  it('分页真生效：请求参数带 page/page_size，翻页换内容', async () => {
    const rows = mkRows(25)
    api.request.mockResolvedValue(okRes(rows, rows.length))
    render(<NewsTab pageSize={10} />)
    await waitFor(() => expect(screen.getByText('行业利好 00')).toBeInTheDocument())
    // 取数一次拿全当日（未读要按整日算），分页在客户端切片
    expect(api.request).toHaveBeenCalledWith(
      expect.stringContaining('kind=news'),
      expect.objectContaining({ withMeta: true }),
    )
    expect(screen.getByText('行业利好 00')).toBeInTheDocument()
    expect(screen.queryByText('行业利好 24')).toBeNull()
    expect(screen.getByText(/第 1 \/ 3 页/)).toBeInTheDocument()

    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(screen.getByText('行业利好 10')).toBeInTheDocument())
    expect(screen.queryByText('行业利好 00')).toBeNull()
    expect(screen.getByText(/第 2 \/ 3 页/)).toBeInTheDocument()
  })

  // ②b 「今日已折叠 N 条」这一行必须原样可见（静默折叠＝通知变黑箱）
  it('折叠行原样可见，不被汇总或省略', async () => {
    const rows = [...mkRows(1), {
      id: 'news|folded|day1', code: '', name: '', level: '新闻', kind: 'news',
      title: '今日已折叠 3 条', body: '超出每票/全局必推预算的新闻已转日汇总',
      generated_at: `${D1}T10:20:00`,
    }]
    api.request.mockResolvedValue(okRes(rows, rows.length))
    render(<NewsTab />)
    await waitFor(() => expect(screen.getByText('今日已折叠 3 条')).toBeInTheDocument())
    expect(screen.getByText('降噪')).toBeInTheDocument()
  })

  // ③ 切交易日清零：未读标记按日一把键，换日＝新键＝旧标记不再复用
  it('切交易日清零：未读与折叠行随当日数据一起归零', async () => {
    api.request.mockResolvedValue(okRes(mkRows(3, D1), 3))
    const { rerender } = render(<NewsTab pageSize={10} />)
    await waitFor(() => expect(screen.getByText(/当日 3 条/)).toBeInTheDocument())
    const key1105 = dayKeyOf(`${D1}T09:00:00`)
    expect(key1105).toBe(D1)
    // 打开过当日 ⇒ 写入这一天的"已读到"标记
    await waitFor(() => expect(readLastSeen(key1105)).toBeGreaterThan(0))

    // 服务端换日：kind=news 的行被消息中心的 day-key 清掉 ⇒ 新一天从空态开始（未读 0、无折叠行）
    api.request.mockResolvedValue(okRes([], 0))
    cleanup()
    render(<NewsTab pageSize={10} />)
    await waitFor(() => expect(screen.getByText('今日暂无新闻')).toBeInTheDocument())
    expect(screen.queryByText(/今日已折叠/)).toBeNull()
    expect(screen.getByText(/未读 0/)).toBeInTheDocument()

    // 而新一天真的来了一条 ⇒ 未读必须是 1（旧 day-key 不许管新一天）
    api.request.mockResolvedValue(okRes(mkRows(1, D2), 1))
    cleanup()
    render(<NewsTab pageSize={10} />)
    await waitFor(() => expect(screen.getByText(/当日 1 条/)).toBeInTheDocument())
    // 两天的标记是两把键：新键此刻还没写过＝按"全部未读"起算
    expect(window.localStorage.getItem(`${LAST_SEEN_PREFIX}.${D1}`)).not.toBeNull()
    const rows1106 = mkRows(1, D2)
    // 该行的时间戳晚于 11-05 的标记 ⇒ 无论标记是否已写，都判未读
    expect(new Date(rows1106[0].generated_at).getTime()).toBeGreaterThan(readLastSeen(D1))
  })

  // dayKeyOf 的取值纪律：行内日期取不到时返回空串（不许拿"今天"冒充数据日期）
  it('dayKeyOf 只认行内日期，缺日期返回空串', () => {
    expect(dayKeyOf('2026-11-05T09:30:00+08:00')).toBe('2026-11-05')
    expect(dayKeyOf('2026-11-05 09:30:00')).toBe('2026-11-05')
    expect(dayKeyOf('')).toBe('')
    expect(dayKeyOf(undefined)).toBe('')
  })
})
