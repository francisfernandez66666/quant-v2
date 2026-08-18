// 战法退出引擎实时接线测试：CheckPositionsExits 的分发、移动止盈、派发、尾盘强平、
// 手动回退、阶段高点持久化，以及情绪退潮减仓告警。
package combat_agent

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
)

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	cfg := config.NewManager("").GetStrategyConfig()
	return New(cfg)
}

func qs(m map[string]float64) map[string]*data.StockInfo {
	out := make(map[string]*data.StockInfo, len(m))
	for code, p := range m {
		out[code] = &data.StockInfo{Code: code, Price: p}
	}
	return out
}

// TestExitDragonReturnTrailing 龙回头移动止盈：阶段高点 12、现价 10.8（回撤>8%）→ P2 减仓。
func TestExitDragonReturnTrailing(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p1", "600276", "恒瑞", "做多", "dragon_return", 10, 20, 5)
	r.RaiseHighest("p1", 12)

	alerts := a.CheckPositionsExits(r, qs(map[string]float64{"600276": 10.8}), nil, time.Now())
	if len(alerts) != 1 {
		t.Fatalf("应触发 1 条移动止盈提醒, got %d", len(alerts))
	}
	s := alerts[0]
	if s.AlertType != "减仓" || s.Action != "卖出" {
		t.Errorf("P2 应映射为 减仓/卖出, got %s/%s", s.AlertType, s.Action)
	}
	if s.Reason != "龙回头移动止盈" {
		t.Errorf("原因应为 龙回头移动止盈, got %q", s.Reason)
	}
}

// TestExitDoubleBumpDistribution 双凸派发：放量1.5倍+收阴 → P1 清仓。
func TestExitDoubleBumpDistribution(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p2", "300750", "宁德", "做多", "double_bump", 10, 15, 5)

	// 5 根日K：前 4 根量 100，最后一根放量 200 且收阴（11→10.6）
	ks := make([]data.KLine, 5)
	for i := 0; i < 4; i++ {
		ks[i] = data.KLine{Open: 10, High: 11, Low: 10, Close: 10.5, Volume: 100}
	}
	ks[3] = data.KLine{Open: 10.8, High: 11, Low: 10, Close: 11, Volume: 100}
	ks[4] = data.KLine{Open: 11, High: 11, Low: 10.6, Close: 10.6, Volume: 200}

	alerts := a.CheckPositionsExits(r, qs(map[string]float64{"300750": 10.5}), map[string][]data.KLine{"300750": ks}, time.Now())
	if len(alerts) != 1 {
		t.Fatalf("应触发 1 条派发清仓, got %d", len(alerts))
	}
	s := alerts[0]
	if s.AlertType != "清仓" || s.Action != "卖出" {
		t.Errorf("P1 应映射为 清仓/卖出, got %s/%s", s.AlertType, s.Action)
	}
	if s.Reason != "双凸派发信号" {
		t.Errorf("原因应为 双凸派发信号, got %q", s.Reason)
	}
}

// TestExitNShapeHardStop N 形硬止损：现价跌破成本×(1−8%) → P1 清仓。
func TestExitNShapeHardStop(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p3", "600580", "卧龙", "做多", "n_shape", 10, 5, 3)

	alerts := a.CheckPositionsExits(r, qs(map[string]float64{"600580": 9.0}), nil, time.Now())
	if len(alerts) != 1 {
		t.Fatalf("应触发硬止损, got %d", len(alerts))
	}
	if alerts[0].AlertType != "清仓" || alerts[0].Reason != "N形硬止损" {
		t.Errorf("应清仓 N形硬止损, got %s/%q", alerts[0].AlertType, alerts[0].Reason)
	}
}

// TestExitNShapeLateClose N 形尾盘强平：14:58 后未完成形态 → P2 减仓。
func TestExitNShapeLateClose(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p4", "000001", "平安", "做多", "n_shape", 10, 5, 3)

	now := time.Date(2026, 8, 12, 14, 58, 0, 0, time.Local)
	alerts := a.CheckPositionsExits(r, qs(map[string]float64{"000001": 10.5}), nil, now)
	if len(alerts) != 1 {
		t.Fatalf("14:58 后应尾盘强平, got %d", len(alerts))
	}
	if alerts[0].AlertType != "减仓" || alerts[0].Reason != "N形收盘强平" {
		t.Errorf("应减仓 N形收盘强平, got %s/%q", alerts[0].AlertType, alerts[0].Reason)
	}
}

// TestExitManualTrailing 手动持仓走通用回退：阶段高点 12、现价 10 → 回撤移动止盈 P2。
func TestExitManualTrailing(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p5", "600006", "东风", "做多", "手动", 10, 8, 5)
	r.RaiseHighest("p5", 12)

	alerts := a.CheckPositionsExits(r, qs(map[string]float64{"600006": 10}), nil, time.Now())
	if len(alerts) != 1 {
		t.Fatalf("手动持仓回撤应触发出退提醒, got %d", len(alerts))
	}
	if alerts[0].AlertType != "减仓" || alerts[0].Reason != "回撤止损(移动止盈)" {
		t.Errorf("应减仓 移动止盈, got %s/%q", alerts[0].AlertType, alerts[0].Reason)
	}
}

// TestExitNoSignal 正常持有不产生退出提醒；无效报价跳过。
func TestExitNoSignal(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p6", "600519", "茅台", "做多", "dragon_return", 100, 20, 5)
	// 价格 99（-1%）：未到止损(-5%)、未创新高、T1 需>=成本价且未到 T2 → 无退出
	if alerts := a.CheckPositionsExits(r, qs(map[string]float64{"600519": 99}), nil, time.Now()); len(alerts) != 0 {
		t.Errorf("正常持有不应有退出提醒, got %d", len(alerts))
	}
	// 停牌（现价 0）应跳过
	if alerts := a.CheckPositionsExits(r, qs(map[string]float64{"600519": 0}), nil, time.Now()); len(alerts) != 0 {
		t.Errorf("无效报价应跳过, got %d", len(alerts))
	}
}

// TestExitPersistsStageHigh 价格创新高时阶段高点被持久化（移动止盈基准随之抬高）。
func TestExitPersistsStageHigh(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("p7", "601318", "平安", "做多", "dragon_return", 10, 20, 5)

	a.CheckPositionsExits(r, qs(map[string]float64{"601318": 11.5}), nil, time.Now())
	l := r.FindBySignalID("p7")
	if l == nil || l.HighestPrice != 11.5 {
		t.Fatalf("创新高后 HighestPrice 应=11.5, got %+v", l)
	}
}

// TestEmotionRetreatAlerts 退潮/背离阶段应向做多持仓发减仓告警；做空与无关阶段不发。
func TestEmotionRetreatAlerts(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("long1", "600276", "恒瑞", "做多", "dragon_return", 10, 20, 5)
	r.LogSignal("short1", "600519", "茅台", "做空", "手动", 100, 20, 5)

	if alerts := a.EmotionRetreatAlerts(r, qs(map[string]float64{"600276": 10, "600519": 100}), "发酵", time.Now()); len(alerts) != 0 {
		t.Errorf("非退潮/背离阶段不应发减仓, got %d", len(alerts))
	}
	alerts := a.EmotionRetreatAlerts(r, qs(map[string]float64{"600276": 10, "600519": 100}), "退潮", time.Now())
	if len(alerts) != 1 {
		t.Fatalf("退潮阶段应只对做多持仓发 1 条减仓, got %d", len(alerts))
	}
	if alerts[0].AlertType != "减仓" || alerts[0].Code != "600276" {
		t.Errorf("应减仓 600276, got %+v", alerts[0])
	}
}

// TestBearishAttributionAlerts 利空归因持仓抛售提醒（E4）：命中利空板块的做多持仓产抛售提醒，
// 未命中或做空持仓不产；归因说明应带板块名/原因。
func TestBearishAttributionAlerts(t *testing.T) {
	a := newTestAgent(t)
	r := report.New("")
	r.LogSignal("long1", "600276", "恒瑞", "做多", "dragon_return", 10, 20, 5)
	r.LogSignal("long2", "600519", "茅台", "做多", "手动", 100, 20, 5)
	r.LogSignal("short1", "000001", "平安", "做空", "手动", 8, 20, 5)

	// 仅 600276 命中利空板块（医药板块利空）
	bearReasons := map[string]string{
		"600276": "医药(集采利空) 事件:医药集采落地",
	}
	alerts := a.BearishAttributionAlerts(r, qs(map[string]float64{
		"600276": 9, "600519": 100, "000001": 8,
	}), bearReasons, time.Now())
	if len(alerts) != 1 {
		t.Fatalf("应只对命中利空的做多持仓发 1 条抛售提醒, got %d", len(alerts))
	}
	sig := alerts[0]
	if sig.Code != "600276" || sig.AlertType != "利空抛售" || sig.Action != "卖出" {
		t.Errorf("抛售提醒字段异常: %+v", sig)
	}
	if sig.Reason == "" || !containsStr(sig.Reason, "集采") || !containsStr(sig.Reason, "尽快抛售") {
		t.Errorf("抛售提醒应含归因说明, reason=%s", sig.Reason)
	}

	// 空 bearReasons → 无提醒
	if a := a.BearishAttributionAlerts(r, qs(map[string]float64{"600276": 9}), nil, time.Now()); len(a) != 0 {
		t.Errorf("空归因不应发提醒, got %d", len(a))
	}
}