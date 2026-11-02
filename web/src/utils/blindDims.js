// blindDims.js — §INDEX-BLIND（2026-11-02）关键读数盲区的展示口径单点。
//
// 为什么要有这一个文件：三张卡（当日资金、做 T、信号决策）都要渲染同一句"缺的是哪一维"。
// 各写一份中文就会漂成"资金卡说大盘指数、T 卡说指数涨跌"，而这两本账正是要对照着看的。
// 后端下发的是机读码（market_state / emotion_phase / index_change），中文名只在这里映射一次。
//
// 三态文案是刻意的，不许折成一句：
//   · 盲区（missing_key_input）＝目标已封顶半仓、当日 T 整体关闭 ⇒ 钱正被挡住，处置=查那一条腿；
//   · 兜底中（index_stale 或来源不是主源）＝点位仍真实，只是主链在退 ⇒ 处置=查上游可达性/熔断；
//   · 两者都不是 ⇒ 不渲染（健康态不需要一块标签）。
// 折成一句"读数异常"就是让人盲操作（同 §STARVE-STALE 里 J0 与 J2 必须各有文案那条教训）。
export const BLIND_DIM_LABEL = {
  market_state: '市场状态',
  emotion_phase: '情绪相位',
  index_change: '大盘指数',
}

// blindDimsText 把维度码折成中文串；未知码原样回显（宁可看不懂，也不编一个维度名指错处置方向）。
export function blindDimsText(dims) {
  if (!Array.isArray(dims) || dims.length === 0) return ''
  return dims.map((d) => BLIND_DIM_LABEL[d] || d).join('、')
}

// blindTag 返回 { theme, text } 或 null。readout 可以是资金帧 live.funds，也可以是纸面 target 读数。
export function blindTag(readout) {
  if (!readout) return null
  if (readout.missing_key_input) {
    const dims = blindDimsText(readout.missing_key_dims)
    // 旧帧没有 dims 字段（本次部署前发布的帧）⇒ 退回今天那句原文案，而不是渲染空白或 undefined。
    return {
      theme: 'danger',
      text: dims ? `关键读数缺失（${dims}）：目标封顶半仓且今日不做 T` : '关键读数缺失：目标封顶半仓且今日不做 T',
    }
  }
  if (readout.index_stale || (readout.index_source && readout.index_source !== 'eastmoney')) {
    const src = readout.index_source || '未知源'
    return { theme: 'warning', text: `指数为兜底读数（来源 ${src}${readout.index_stale ? '，陈旧回退' : ''}）` }
  }
  return null
}
