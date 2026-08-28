// ── 分时视图容器 MinuteView.jsx ──
// 将分时图与买卖盘组合为一个自适应容器：分时图占满整行宽度，盘口在其下方。
import React from 'react'
import KLineChart from './KLineChart.jsx'
import DepthPanel from './DepthPanel.jsx'

export default function MinuteView({ code, name }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12, width: '100%' }}>
      <div style={{ width: '100%' }}>
        <KLineChart code={code} name={name} />
      </div>
      <div style={{ width: '100%', maxWidth: 720 }}>
        <DepthPanel code={code} name={name} />
      </div>
    </div>
  )
}
