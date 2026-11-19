// flow_quadrant_alert.go — §FLOW-QUADRANT 减仓决策的卖点信号构造（2026-11-19）。
// 四象限中「牛+放量+pos 高＝查滞涨→减仓闸门」产出 Reduce 决策时，构造一条 SellLevel=减仓
// 的卖点提醒信号，形态对齐 newsGateVetoAlert（Direction=提醒 + AlertType/SellLevel），
// 保证消息中心/前端可渲染、卖出裁决可消费。
// 注意：当前为**提醒级**（不自动减仓）——量比数据来自今晚 QMT 探针，未验证前不自动执行减仓，
// 避免误伤；探针数据回填并验证后，再由卖出裁决按 Source 标记决定是否自动半仓退出。
// English: builds a "reduce" sell-level alert signal from the quadrant Reduce decision; alert-only
// until the probe data is validated, so no automatic reduction happens prematurely.
package engine

import (
	"fmt"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// quadrantReduceAlert 由四象限 Reduce 决策构造减仓级卖点提醒信号。
// base 为持仓票的基底信号（代码/名称沿用），reason 为四象限 reason。
func quadrantReduceAlert(base combat_agent.Signal, reason string) combat_agent.Signal {
	alert := base // 拷贝基底（代码/名称/行情沿用）
	alert.ID = fmt.Sprintf("quadrant:%s:%s:%s", data.TradingDayDate(time.Now()), base.Code, base.Strategy)
	alert.Strategy = "资金流四象限"
	alert.StrategyType = "flow_quadrant"
	alert.Direction = "提醒"
	alert.Action = "卖出"
	alert.AlertType = "减仓"
	alert.SellLevel = "减仓"
	alert.Reason = fmt.Sprintf("⚠️资金流四象限减仓闸门: %s", reason)
	alert.GeneratedAt = time.Now()
	return alert
}
