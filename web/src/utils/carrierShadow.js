// carrierShadow.js — §CARRIER-SHADOW（2026-11-05）影子对照读数的展示口径单点。
//
// 这块读数是 owner 那枚「武装」按钮唯一的证据：每枚移植规则必须先与引擎对照跑满
// ≥5 个交易日才准碰钱（docs/IMPL_QMT_INTERNAL_LOOP_20261103.md）。以前 shadow 命中只是
// 执行器本地一行日志，屏上什么都没有，于是那枚按钮背后是零数据。
//
// 为什么单独成文件（同 liveBrain.js / blindDims.js 的取向）：仪表盘的实盘行与量化页的
// 链路卡要显示**同一句结论**，各写一份中文就会漂成"一页说没跑、另一页说没命中"。
// 状态值与真源 internal/server/carrier_shadow.go 的四个常量同值——**改值必须同批改那里**，
// 两侧任何一侧偷偷漂移，读数就会把四种完全不同的情况折成同一句让人盲操作的话。
//
// 四种状态是刻意的，一条都不许折（missing≠empty 这一族在本仓咬过多次）：
//   · agreeing  有命中且引擎认了 ⇒ 真的一"致"，计入分子；
//   · diverged  有命中但引擎一条都不认 ⇒ 分歧，去查规则/阈值，**不是**缺数据；
//   · no_hits   那天报了（别的种类有行）、这一类零命中 ⇒ 真实的一"零"，计入窗口；
//   · no_report 那天整根腿没报 ⇒ 无从对照，**不计入窗口**——把它算成一天，
//     5 日窗口会被一串静默日自己凑满（§STARVE-STALE 的 J0「零回包不算健康」同形）。
// English: single source for the shadow-comparison copy; the four per-day states and their
// next actions must never be folded into one sentence.

// 与 Go 侧 carrierShadowState* 常量一一对应（值即契约，改一边即红）。
export const SHADOW_DAY_STATE = {
  AGREEING: 'agreeing',
  DIVERGED: 'diverged',
  NO_HITS: 'no_hits',
  NO_REPORT: 'no_report',
}

// 每档一句结论 + 下一步动作（动作不同就不许共用语句）。theme 给 Tag 用。
const STATE_COPY = {
  [SHADOW_DAY_STATE.AGREEING]: {
    theme: 'success',
    text: '一致：载体有命中，引擎同日同码也认',
  },
  [SHADOW_DAY_STATE.DIVERGED]: {
    theme: 'warning',
    text: '分歧：载体有命中而引擎一条都不认（查阈值/移植口径，不是查链路）',
  },
  [SHADOW_DAY_STATE.NO_HITS]: {
    theme: 'default',
    text: '零命中：那天盒子报了数，这一类没触发（真实的一天，计入窗口）',
  },
  [SHADOW_DAY_STATE.NO_REPORT]: {
    theme: 'warning',
    text: '未上报：那天整根影子腿一个字节都没报（不计入窗口，查载体/上行腿）',
  },
}

export function shadowDayText(state) {
  const hit = STATE_COPY[state]
  // 未登记的态只能判"读不出"：折成四档里的任何一档都是替载体编一个结论。
  if (!hit) return { theme: 'warning', text: '读数未知（未登记的态）' }
  return hit
}

// windowProgress 把 kinds[] 压成一句人能直接判读的话。
//
// 两个刻意的形状：
//  ① known=false（读数构建不出来）返回的是"未知"，**绝不返回 0 天**——
//     "0/5" 会被读成"跑了但一条都没命中"，而真相是我们不知道（missing≠empty）；
//  ② 窗口凑满那句只说"对照满 N 个交易日"，**不说"可以武装"**：
//     分子够不够是一回事，要不要按下武装是 owner 的决定（本仓的开关一律不许替人决定）。
export function windowProgress(view) {
  if (!view || view.known !== true) {
    return { known: false, text: '读数未知（影子账本未接入或窗口定不出来，不是"没跑过"）', kinds: [] }
  }
  const kinds = Array.isArray(view.kinds) ? view.kinds : []
  if (kinds.length === 0) {
    return {
      known: true,
      text: `窗口 ${view.window_days || 0} 个交易日：这一版载体还没产出任何影子判定` +
        `（已上报 ${view.days_reported || 0} 天）`,
      kinds,
    }
  }
  const filled = kinds.filter((k) => k.window_filled === true).length
  return {
    known: true,
    text: `${kinds.length} 类规则：${filled} 类已对照满 ${view.required_days || 5} 个交易日` +
      `（满不满只说明证据够不够，武装与否是另一个决定）`,
    kinds,
  }
}

// kindRow 把一类规则的三枚天数摊平成"分子/分母 + 三种零各几条"的一行读数。
// 三种零（有命中 / 零命中 / 未上报）分开数是本读面的本体：合成一个"跑了 3 天"
// 就再也问不出"另外两天是没触发还是没报"。
export function kindRow(kind) {
  const required = kind.required_days || 5
  return {
    kind: kind.kind,
    daysWithData: kind.days_with_data || 0,
    daysNoHits: kind.days_no_hits || 0,
    daysNoReport: kind.days_no_report || 0,
    carrierRows: kind.carrier_rows || 0,
    engineAgreed: kind.engine_agreed || 0,
    required,
    filled: kind.window_filled === true,
    text: `${kind.kind}：有命中 ${kind.days_with_data || 0}/${required} 天` +
      `，零命中 ${kind.days_no_hits || 0} 天，未上报 ${kind.days_no_report || 0} 天` +
      `；载体 ${kind.carrier_rows || 0} 条 / 引擎认 ${kind.engine_agreed || 0} 条`,
  }
}
