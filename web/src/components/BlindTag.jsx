// BlindTag.jsx — §INDEX-BLIND（2026-11-02）关键读数盲区的三态标签（一个组件，三张卡共用）。
//
// 为什么单独成组件而不是在每张卡里各写一段三元表达式：三张卡（当日资金 / 做 T / 信号决策）
// 要显示的是同一句结论，而"缺哪一维""谁在给点位"这两处一旦各写各的，两本账就没法对照
// ——这正是本批要消灭的形态（现网 11-02 那天，卡片只说"缺失"，处置方向全靠人猜）。
// 文案与判档的唯一真源在 utils/blindDims.js，这里只负责渲染。
import React from 'react'
import { Tag } from 'tdesign-react'
import { blindTag } from '../utils/blindDims.js'

// BlindTag 读一份定权读数（资金帧 live.funds 或纸面 target），需要时才渲染标签。
export default function BlindTag({ readout }) {
  const t = blindTag(readout)
  if (!t) return null
  return <Tag size="small" theme={t.theme}>{t.text}</Tag>
}
