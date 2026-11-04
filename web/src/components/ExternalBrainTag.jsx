// ExternalBrainTag.jsx — §EXT-BRAIN-1104（2026-11-04）外脑模式档位的标签（量化页与仪表盘共用一块）。
//
// 为什么不在这两处各写一段三元表达式：外脑档位说的是"引擎还有没有手"，
// 而运维会在两个屏幕上交叉核对同一件事（仪表盘看全局、量化页看本账号那条链路）。
// 两处各写一份中文，就会漂成两种说法——同 BlindTag.jsx 当初的取向。
// 判档与文案的唯一真源在 utils/liveBrain.js，这里只负责渲染。
//
// 档 0（没配网关）与 1（通道正常）返回 null：健康态不许占地方。
// English: renders the external-brain tier tag; tiers 0/1 render nothing at all.
import React from 'react'
import { Tag } from 'tdesign-react'
import { externalBrainTier } from '../utils/liveBrain.js'

// ExternalBrainTag 只吃 /api/qmt/state 的 external_brain 数字；不读 reason 文案（那是给人看的句子，不是契约）。
export default function ExternalBrainTag({ code }) {
  const t = externalBrainTier(code)
  if (!t.active) return null
  return <Tag size="small" theme={t.theme}>{t.text}</Tag>
}
