// ── §2.12 信号弹窗卡（2026-11-04，外脑化 B1' 批）：页内弹窗 + 桌面/手机三通道派发 ──
// 桥 11-05 断后没有柜台链路，量仔的输出端只剩"给人照着下单的卡"。这张卡的判定
// （缺什么、可不可执行、去重、通道开关）全部在 utils/signalCard.js 里，本组件**只渲染结论**——
// 判定放组件里就没法在 vitest 里钉住，而这张卡说谎的代价是有人照着它下真单。
//
// 触发源：SSE 的 `message` 事件（level=交易信号）只是"有新信号"的门铃；
// 卡片本体一律从 GET /api/signals 的 `card` 字段取（那里才有后端装配好的缺字段判定）。
// 为什么不直接拿 SSE 的 item 拼卡：item 里没有股数/参考价/置信度（data.MessageItem 就没这些字段），
// 前端自己拼只会拼出"要么全缺、要么靠正则从正文里抠"两种形状——后者会抠出现价当股数。
import React, { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Checkbox, Tag } from 'tdesign-react'
import * as api from '../api/index.js'
import { on } from '../sseBus.js'
import {
  CARD_FIELDS, CHANNELS, MISSING_LABEL, createSeenStore, dispatchCard,
  loadChannelPrefs, normalizeCard, saveChannelPrefs,
} from '../utils/signalCard.js'

// MAX_CARDS 页内最多堆叠几张卡：超过即丢最旧的。
// 为什么要有上限：信号多的日子一轮就能出十几条，全堆在屏幕上等于没有弹窗
// （用户会直接关掉整块，那条真正要看的信号就死了）。
const MAX_CARDS = 8

// 卡片样式：刻意用边框 + 浅色底，不复用告警红/绿——
// 这是"建议"不是"已成交"，配色上与真实回报区分开是本批的一条硬要求。
const cardStyle = {
  border: '1px solid #d9d9d9', borderRadius: 6, padding: '10px 12px', marginBottom: 8,
  background: '#fffbe6',
}
const missingStyle = { color: '#b8860b', fontWeight: 600 }

// SignalCardModal 信号弹窗层。挂在主布局里（App.jsx），全站一份。
export default function SignalCardModal() {
  const [cards, setCards] = useState([])        // 页内堆叠的卡（normalizeCard 的产物）
  const [prefs, setPrefs] = useState(() => loadChannelPrefs(typeof window !== 'undefined' ? window.localStorage : null))
  const seenRef = useRef(createSeenStore())     // 已推账（跨三通道共用一本）
  const fetchingRef = useRef(false)
  const prefsRef = useRef(prefs)
  prefsRef.current = prefs

  // toggle 单条通道开关：立刻生效并落本地
  const toggle = useCallback((channel, on) => {
    setPrefs((prev) => {
      const next = { ...prev, [channel]: on }
      saveChannelPrefs(typeof window !== 'undefined' ? window.localStorage : null, next)
      return next
    })
  }, [])

  // pull 取一次信号卡并按三通道派发（门铃来了才响，也允许用户手动"拉一次"）
  const pull = useCallback(async () => {
    if (fetchingRef.current) return // 并发门铃（连着来两条消息）只跑一趟，避免同一批卡派发两次
    fetchingRef.current = true
    try {
      const list = await api.fetchSignals()
      if (!Array.isArray(list)) return
      const fresh = []
      list.forEach((sig) => {
        if (!sig || !sig.card) return
        const c = normalizeCard(sig.card)
        if (!c.signalId) return
        const res = dispatchCard(sig.card, {
          prefs: prefsRef.current,
          seen: seenRef.current,
          pushModal: (card) => fresh.push(card),
        })
        if (res.deduped) return
        // 弹窗关了但卡仍然要能被"手动拉一次"看见吗？——不：关了就是关了，
        // 但不可执行的卡**不进**桌面/手机通知（见 dispatchCard 注释），只进弹窗。
        if (res.modal && fresh.length < MAX_CARDS) {
          setCards((prev) => [c, ...prev].slice(0, MAX_CARDS))
        }
      })
    } catch (_) {
      // 拉取失败静默：这条腿是展示腿，失败由信号页自己的 loadLedger 点名，不在这里重复报
    } finally {
      fetchingRef.current = false
    }
  }, [])

  useEffect(() => {
    // 门铃：SSE 的 `message`（交易信号）与 `scan`（新一轮批次完成）都触发一次拉取。
    // 为什么两个都听：scan 是"这一轮算完了"，message 是"有一条新信号进消息中心"，
    // 两者到达顺序不固定（引擎先广播 message，scan 在同轮稍后），只听一个就会漏。
    const off1 = on('message', (msg) => {
      const item = msg && msg.item
      if (!item) return
      const level = String(item.level || '')
      if (level.indexOf('交易信号') < 0) return
      pull()
    })
    const off2 = on('scan', () => pull())
    return () => { off1(); off2() }
  }, [pull])

  return (
    <div data-testid="signal-card-modal" style={{ position: 'fixed', right: 16, bottom: 16, width: 320, zIndex: 2000 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6, fontSize: 12, color: '#666' }}>
        <span>信号卡</span>
        {Object.keys(CHANNELS).map((k) => (
          <Checkbox
            key={k}
            checked={prefs[k] !== false}
            onChange={(v) => toggle(k, v)}
            label={k === 'modal' ? '弹窗' : k === 'desktop' ? '桌面' : '手机'}
            style={{ marginRight: 4 }}
          />
        ))}
      </div>
      {cards.map((c) => (
        <SignalCard key={c.signalId} card={c} onDone={() => setCards((prev) => prev.filter((x) => x.signalId !== c.signalId))} />
      ))}
    </div>
  )
}

// SignalCard 单张卡：八字段逐格渲染，缺的那格写「数据缺失」，不可执行时整卡显式标出来。
export function SignalCard({ card, onDone }) {
  const c = card || {}
  const rows = [
    ['股票名', c.stockName], ['代码', c.code], ['方向', c.side], ['股数', c.qty],
    ['参考价', c.price], ['策略/理由', c.reason], ['置信/档位', c.confidence], ['有效期', c.expiresAt],
  ]
  return (
    <div style={cardStyle} data-testid="signal-card">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
        <strong>{c.side === MISSING_LABEL ? '交易信号' : '交易信号 · ' + c.side}</strong>
        {c.executable
          ? <Tag theme="success" variant="light-outline">可执行</Tag>
          : <Tag theme="warning" variant="light-outline">不可执行</Tag>}
      </div>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
        <tbody>
          {rows.map(([label, value]) => {
            const isMissing = value === MISSING_LABEL || (value === 0 && label === '股数')
            return (
              <tr key={label}>
                <td style={{ color: '#888', width: 72, verticalAlign: 'top' }}>{label}</td>
                <td style={isMissing ? missingStyle : undefined}>{isMissing ? MISSING_LABEL : String(value)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {c.expiryNote ? <div style={{ fontSize: 11, color: '#999', marginTop: 4 }}>有效期口径：{c.expiryNote}</div> : null}
      {!c.executable && c.missing && c.missing.length > 0
        ? <div style={{ fontSize: 11, color: '#b8860b', marginTop: 4 }}>缺：{c.missing.join('、')}（字段缺一不可，不猜数）</div>
        : null}
      <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
        <Button size="small" variant="outline" onClick={onDone}>已处理</Button>
        <Button size="small" variant="text" onClick={onDone}>忽略</Button>
        {/* 点卡**不**下单（§2.12 安全边界：卡是建议，执行走手动/券商托管/本地执行器） */}
        <span style={{ marginLeft: 'auto', fontSize: 11, color: '#aaa' }}>{c.sourceLabel || '量仔建议'}</span>
      </div>
    </div>
  )
}

// 导出常量给用例对撞后端字段清单（漂一个字就要红）
export { CARD_FIELDS }
