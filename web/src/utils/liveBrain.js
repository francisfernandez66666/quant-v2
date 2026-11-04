// liveBrain.js — §EXT-BRAIN-1104（2026-11-04）外脑模式的展示口径单点。
//
// 背景：券商 11-05 起关掉 QMT 内置策略的文件读写与网络，量仔↔柜台的自动通道随之断。
// 后端把这件事压成一个**档位数字**（GET /api/qmt/state 的 external_brain，
// 真源 internal/trading/external_brain.go：0 不适用 / 1 正常 / 2 读不到柜台 / 3 通道断 / 4 显式影子态），
// 另发一句 external_brain_reason。这里只按数字分形——**文案绝不解析后端那句 reason**
// （它是给人看的句子，不是契约；改成解析它就把观测字段变成了数据面依赖）。
//
// 为什么单独成文件（同 blindDims.js 的取向）：量化页的链路卡与仪表盘的实盘行要显示
// **同一句结论**，各写一份中文就会漂成"一页说通道断、另一页说腿异常"，而这两屏正是要对照着看的。
//
// 三态文案是刻意的，不许折成一句（同 §STARVE-STALE 里 J0 与 J2 必须各有文案那条教训）：
//   · 2 柜台读数过期 ⇒ 处置=核对券商导出/手填持仓资金；
//   · 3 通道断       ⇒ 处置=查网关进程与桥；
//   · 4 显式影子态   ⇒ 处置=这是 owner 自己的意图，不是故障，别去查链路。
// 三个下一步动作完全不同，折成"外脑异常"一句就是让人盲操作。
// 0 与 1（不适用/正常）不渲染：健康态不需要一块标签。
// English: single source for external-brain tier copy; tiers 0/1 render nothing.

// 档位数字常量：与 Go 侧 ExternalBrain* 常量同值，改号必须同批改这里。
export const EXTERNAL_BRAIN_TIER = {
  NA: 0,          // 不适用：该账号没配实盘网关
  NORMAL: 1,      // 正常：通道通、柜台读数新鲜，可以自动动手
  READ_STALE: 2,  // 读不到柜台：通道在，但资金/持仓读数过窗
  CHANNEL_DOWN: 3, // 通道断：网关不可达或桥心跳已断
  SHADOW: 4,      // 显式影子态：owner 主动关掉 qmt.enabled
}

// 2/3/4 共用的结论半句：这三档引擎都不再自动开新仓，但卖出与纸面照旧。
// 为什么要把后半句写在每一档上：只写"不再自动开新仓"会让人以为整个账号停了、
// 止损也不出——那会让人在真跌的时候不去看盘。
const TAIL = '不再自动开新仓，卖出与纸面照旧'

// 各档的中文句子（theme 给 Tag 用：过期与未知是黄，通道断是红，影子态是中性——它是意图不是故障）。
const TIER_COPY = {
  [EXTERNAL_BRAIN_TIER.READ_STALE]: {
    level: 'read_stale',
    theme: 'warning',
    text: `外脑·柜台读数过期（资金/持仓超过 1 小时没更新）：请核对券商导出或手动填写持仓资金，${TAIL}`,
  },
  [EXTERNAL_BRAIN_TIER.CHANNEL_DOWN]: {
    level: 'channel_down',
    theme: 'danger',
    text: `外脑·通道断（网关不可达或 QMT 桥心跳已断）：请查网关进程与桥是否还在，${TAIL}`,
  },
  [EXTERNAL_BRAIN_TIER.SHADOW]: {
    level: 'shadow',
    theme: 'default',
    text: `外脑·显式影子态（owner 已主动关停 qmt.enabled，非故障）：${TAIL}`,
  },
}

// 未知档位的句子：刻意不出现"正常"二字，也不复用上面任何一句。
// 为什么不折成"正常"：档 4 的字段缺席与"这版引擎压根没发这个字段"是两件事，
// 把缺席当成健康就是 §0925EVE-W3-E 的"读不到不装已知"。
const UNKNOWN_COPY = {
  level: 'unknown',
  theme: 'warning',
  text: `外脑档位读数未知（该引擎版本没发 external_brain 字段或值不在 0-4）：请核对版本，勿按健康理解`,
}

// 只认整数（含纯整数字符串，因为 JSON 里偶尔会带成串）；null/undefined/空串/小数/NaN/Infinity 一律判未知。
// 这条归一化是必需的：`Number(null)===0` 会把"没这个读数"渲染成"不适用"，
// 而 `Number(undefined)` 是 NaN——同一个未知值两种下场，正是本函数要避免的形状。
function toTierCode(code) {
  if (typeof code === 'number') {
    return Number.isInteger(code) ? code : null
  }
  if (typeof code === 'string' && /^-?\d+$/.test(code.trim())) {
    return parseInt(code.trim(), 10)
  }
  return null
}

// externalBrainTier 把档位数字折成 { active, level, theme, text }。
// active=false（档 0/1）表示"不渲染"；调用方只判这一个字段，不许自己再写一份 0/1 的判断。
// English: map the numeric tier to display copy; 0/1 => active=false (render nothing).
export function externalBrainTier(code) {
  const n = toTierCode(code)
  if (n === null) return { active: true, ...UNKNOWN_COPY }
  if (n === EXTERNAL_BRAIN_TIER.NA || n === EXTERNAL_BRAIN_TIER.NORMAL) {
    return { active: false, level: n === EXTERNAL_BRAIN_TIER.NA ? 'na' : 'normal', theme: 'default', text: '' }
  }
  const copy = TIER_COPY[n]
  if (!copy) return { active: true, ...UNKNOWN_COPY }
  return { active: true, ...copy }
}
