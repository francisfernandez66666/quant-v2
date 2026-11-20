// ── §SIDE-VOCAB（2026-11-20）── 信号卡方向词表补齐后的渲染锁
//
// 缺陷现场：引擎真实在发的 Signal.Action 绝大多数是 `watch`（观察），而后端
// normalizeCardSide 只认 买入/卖出/buy/sell ⇒ 卡片「方向」天天打「数据缺失」、标题只剩
// 光秃秃的「交易信号」，读起来像"系统没算出方向"。
// 后端词表已补 watch/hold 与四个离场动词；本文件锁前端三件：
//
//   S1 标题写得出方向（观察/持有不再是"方向待定"）；
//   S2 字段齐全但方向不可交易的卡，标签是「仅观察 · 不下单」，不是「缺读数」
//      （后者会让人去找一个根本不存在的缺字段）；
//   S3 通知正文不出现空的「不可执行（）」括号。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { cardTitle, cardBody, MISSING_LABEL } from '../utils/signalCard.js'
import { SignalCard } from '../components/SignalCardModal.jsx'

// watchCard 一张**八字段齐全**的观察卡（后端 executable=false，missing_fields 为空）。
function watchCard(over = {}) {
  return {
    signalId: 'sig-watch-1',
    stockName: '华通线缆',
    code: '605196.SH',
    side: '观察',
    qty: 1000,
    price: 12.5,
    reason: '动量战法',
    confidence: '0.72',
    expiresAt: '2026-11-20 15:00',
    missing: [],
    executable: false,
    ...over,
  }
}

describe('§SIDE-VOCAB 方向词表补齐后的卡片渲染', () => {
  it('S1 标题写得出方向', () => {
    expect(cardTitle(watchCard())).toBe('量仔 交易信号 · 观察')
    expect(cardTitle(watchCard({ side: '持有' }))).toBe('量仔 交易信号 · 持有')
    // 真正的未知（后端判不了方向）仍然如实写"方向待定"，不许猜
    expect(cardTitle(watchCard({ side: MISSING_LABEL }))).toBe('量仔 交易信号 · 方向待定')
  })

  it('S2 观察卡标签是「仅观察 · 不下单」，不是「缺读数」', () => {
    render(<SignalCard card={watchCard()} onDone={() => {}} />)
    expect(screen.getByText('交易信号 · 观察')).toBeTruthy()
    expect(screen.getByText('仅观察 · 不下单')).toBeTruthy()
    expect(screen.queryByText('弱提醒 · 缺读数')).toBeNull()
  })

  it('S2b 真缺字段的卡仍然标「弱提醒 · 缺读数」并点名列缺什么', () => {
    render(<SignalCard card={watchCard({ side: '买入', qty: MISSING_LABEL, missing: ['股数'] })} onDone={() => {}} />)
    expect(screen.getByText('弱提醒 · 缺读数')).toBeTruthy()
    expect(screen.getByText(/缺：股数/)).toBeTruthy()
  })

  it('S3 通知正文不出现空的「不可执行（）」括号', () => {
    const body = cardBody(watchCard())
    expect(body).toContain('不是下单指令')
    expect(body).not.toContain('（）')
    // 真缺字段时照旧逐项点名
    const missingBody = cardBody(watchCard({ qty: MISSING_LABEL, missing: ['股数'] }))
    expect(missingBody).toContain('不可执行（股数）')
  })
})
