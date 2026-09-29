// heartbeat.go — 「有卖出但已实现盈亏恒为 0」业务心跳的实盘控制器侧喂数腿（§0929HB-2，
// 09-29 全量审计批 ⑪-4 的第三条腿，读数口径在 store/realized_pnl_heartbeat.go）。
//
// 为什么喂数点在这里而不是打分循环里：
//   - 判据要的两个数（当日卖出笔数、当日已实现盈亏）都归 *store.DB + userID 管，
//     这两样都在控制器上；引擎侧只有控制器句柄，把账本口径外搬到 engine 包会造出
//     第二套"当日已实现盈亏"算法——§0927AUDIT-D1 那次扣费缺陷的根因正是
//     「同一个量在两个读数点各写一遍公式」，此处不再重复。
//   - 与 §0929HB-1 同一纪律：只有实盘开关打开（snap.Enabled）的控制器落笔；非实盘引擎
//     不写 0，避免影子账号的正常状态把实盘账号的异常读数掩掉。
//
// 读数失败（查库报错）一律写 0（不触发）并 opslog 留痕：把"观测面故障"报成"资金账断了"
// 是不可接受的告警（§DEADGAUGE/§CAL-GATE 一致的"未知不伪造"口径），但也不能静默——
// 查库天天失败的话这条心跳本身就瞎了，日志是唯一线索。
//
// English: feeds the "sells today yet realized P&L is zero" gauge from the live controller,
// so the alert reads the exact same number the intraday-loss breaker acts on. Unknown/not-live
// writes 0 (never fabricate a money-side incident from an observability failure).
package trading

import (
	"log"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
)

// 量规键名在写入点写死字面量 "realized_pnl_zero_with_sells"（告警规则读端同名；不立 const 别名
// 的理由同 internal/engine/heartbeat.go 头部那条：§DEADGAUGE 通用守卫按字面量形态扫赋值点）。

// RefreshRealizedPnlHeartbeat 喂当日"有卖出但已实现盈亏为 0"心跳量规（0/1）。
// 由引擎的周期量规刷新（refreshStalenessGauges）在确认本引擎实盘已开后调用。
// day 口径：cntime 的 YYYY-MM-DD，与 fills_effective.traded_at 前 10 位、熔断闸 g.today() 同源。
// English: feeds the 0/1 heartbeat gauge for "sell fills exist today but realized P&L is zero".
func (c *Controller) RefreshRealizedPnlHeartbeat(now time.Time) {
	if c == nil {
		return
	}
	c.mu.RLock()
	st := c.store
	uid := c.userID
	enabled := c.cfg.Enabled
	c.mu.RUnlock()
	if st == nil || !enabled {
		return // 非实盘/未接账本：不写（掩蔽纪律见文件头）
	}
	day := cntime.In(now).Format("2006-01-02")
	h, err := st.RealizedPnlHeartbeatForUser(uid, day)
	if err != nil {
		metrics.SetGauge("realized_pnl_zero_with_sells", 0)
		// 节流留痕：每 24h 一条，别把日志刷成新的噪音源（§CAL-GATE 同款 OncePer 姿势）。
		opslog.OncePer("hb-realized-pnl-read-err", 24*time.Hour, func() {
			log.Printf("[P2][hb] §0929HB-2 已实现盈亏心跳读数失败（本周期不判红，查 fills 表可读性）: %v", err)
		})
		return
	}
	v := int64(0)
	if h.Suspicious {
		v = 1
		// 量规只有 0/1，成因写进日志：卖出笔数与盈亏读数同时可见，才知道是"成本不可知"
		// 还是"方向错记"（两者的处置完全不同，见 §0925EVE D-25 那族方向错记事故）。
		opslog.OncePer("hb-realized-pnl-zero", 6*time.Hour, func() {
			log.Printf("[P2][hb] §0929HB-2 当日卖出 %d 笔但已实现盈亏为 0（day=%s user=%s）："+
				"日内亏损熔断闸与成交页此刻都看不到任何亏损，查成本口径与卖出腿费用入账", h.SellFills, day, uid)
		})
	}
	metrics.SetGauge("realized_pnl_zero_with_sells", v)
}
