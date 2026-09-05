// 文件：position_alerts_test.go
// 包名：combat_agent
// 所属模块：「对抗式/量化交易决策 agent（买卖信号、风控）」
// 模块职责：本文件属于 对抗式/量化交易决策 agent（买卖信号、风控），负责该模块下的具体实现；
//           下文各函数/类型/方法均附有中文说明（用途、参数、返回值、副作用）。
// 说明：本文件仅补充注释，未改动任何原有代码逻辑。

package combat_agent

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
)

// quoteMockTransport 模拟东财 push2 stock/get 返回固定行情（F43 单位为分）。
// 按 secid 区分股票：600206 → 4379分(43.79)，600000 → 800分(8.00)。
// English: quoteMockTransport simulates the Eastmoney push2 stock/get endpoint returning fixed quotes (F43 unit is cents). Stocks are distinguished by secid: 600206 → 4379 cents (43.79), 600000 → 800 cents (8.00).
type quoteMockTransport struct{}

// RoundTrip 模拟东财 push2 stock/get，按 secid 返回固定行情（单位为分）。
func (rt *quoteMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Hostname(), "push2.eastmoney.com") {
		return nil, http.ErrHandlerTimeout
	}
	price := "4379"
	if strings.Contains(req.URL.RawQuery, "600000") {
		price = "800"
	}
	body := `{"data":{"f43":` + price + `,"f57":"600206","f58":"有研新材","f170":1000}}`
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}, nil
}

// newAlertTestRig 构造 CheckPositionAlerts 测试环境：
//   - Agent：仅需空策略配置（CheckPositionAlerts 不依赖策略运行器）
//   - Report：临时文件路径的持仓报表
//   - MarketAPI：mock 东财行情（避免真实网络）
//
// English: newAlertTestRig builds the CheckPositionAlerts test rig: Agent only needs an empty strategy config (CheckPositionAlerts does not depend on strategy runners); Report uses a holdings report at a temp file path; MarketAPI mocks the Eastmoney quotes (avoiding real network calls).
func newAlertTestRig(t *testing.T) (*Agent, *report.Report, *data.MarketAPI) {
	t.Helper()
	a := New(&config.StrategyConfig{})
	rpt := report.New(filepath.Join(t.TempDir(), "report.json"))
	m := data.NewMarketAPI()
	m.SetTransport(&quoteMockTransport{})
	return a, rpt, m
}

// TestCheckPositionAlerts_NoScore 无打分表时：触止盈线 → 仍产出"止盈"硬提醒。
// English: TestCheckPositionAlerts_NoScore with no score table: hitting the take-profit line → still produces a hard "take profit" alert.
func TestCheckPositionAlerts_NoScore(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 40 元，止盈 8%（现价 43.79 → 盈亏 +9.47% ≥ 8% 触发）
	// English: Opened at 40, take-profit 8% (current price 43.79 → P&L +9.47% ≥ 8% triggers).
	rpt.LogSignal("pos-1", "600206", "有研新材", "做多", "dragon", 40.0, 8.0, 5.0)
	alerts := a.CheckPositionAlerts(rpt, m, nil, nil)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条止盈提醒, got %d", len(alerts))
	}
	if alerts[0].AlertType != "止盈" {
		t.Errorf("无打分时应为硬止盈, got %s", alerts[0].AlertType)
	}
}

// TestCheckPositionAlerts_SignalActiveDowngradesToHint 有活跃信号时：触止盈/止损线 → 降级为"提示"。
// English: TestCheckPositionAlerts_SignalActiveDowngradesToHint with an active signal: hitting the take-profit/stop-loss line → downgraded to a "hint".
func TestCheckPositionAlerts_SignalActiveDowngradesToHint(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	rpt.LogSignal("pos-1", "600206", "有研新材", "做多", "dragon", 40.0, 8.0, 5.0)
	scores := map[string]StockScores{
		"600206": {Code: "600206", NScore: 70, SignalActive: true},
	}
	alerts := a.CheckPositionAlerts(rpt, m, nil, scores)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条降级提示, got %d", len(alerts))
	}
	if alerts[0].AlertType != "提示" {
		t.Errorf("有活跃信号时止盈应降级为提示, got %s", alerts[0].AlertType)
	}
	if alerts[0].Action != "关注" {
		t.Errorf("降级提示 Action 应为关注, got %s", alerts[0].Action)
	}
}

// TestCheckPositionAlerts_StopLossTrimFirst FIX#12 做多止损未出现利空确认且未深度破位 → 减仓（半仓自动退出）。
// English: TestCheckPositionAlerts_StopLossTrimFirst (FIX#12) a long stop-loss without a bearish
// confirmation and without a deep breach → trim alert (auto half-exit).
func TestCheckPositionAlerts_StopLossTrimFirst(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 10 元，止损 5%（现价 9.20 → 盈亏 -8% ≤ -5% 触发，且 -8% > -10% 未深度破位）
	// English: Opened at 10, stop-loss 5% (current price 9.20 → P&L -8% ≤ -5%, and -8% > -10% not a deep breach).
	rpt.LogSignal("pos-2", "600000", "浦发银行", "做多", "n_shape", 10.0, 8.0, 5.0)
	scores := map[string]StockScores{
		"600000": {Code: "600000", DragonReturnScore: 65, SignalActive: true},
	}
	alerts := a.CheckPositionAlerts(rpt, m, map[string]*data.StockInfo{
		"600000": {Code: "600000", Name: "浦发银行", Price: 9.20, ChangePct: 0},
	}, scores)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条减仓提醒, got %d", len(alerts))
	}
	if alerts[0].AlertType != "减仓" {
		t.Errorf("首触止损线且无利空确认应产出减仓, got %s", alerts[0].AlertType)
	}
	if alerts[0].Action != "卖出" {
		t.Errorf("减仓 Action 应为卖出, got %s", alerts[0].Action)
	}
}

// TestCheckPositionAlerts_StopLossDeepBreachHard FIX#12 深度破位（-2×止损线）兜底 → 无条件硬止损。
// English: TestCheckPositionAlerts_StopLossDeepBreachHard (FIX#12) a deep breach (2× the stop line)
// backstops to an unconditional hard stop-loss.
func TestCheckPositionAlerts_StopLossDeepBreachHard(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 10 元，止损 5%（现价 8.00 → 盈亏 -20% ≤ -10% 深破，无利空确认也硬止损）
	// English: Opened at 10, stop-loss 5% (current price 8.00 → P&L -20% ≤ -10% deep breach; hard stop even without a bearish confirmation).
	rpt.LogSignal("pos-2", "600000", "浦发银行", "做多", "n_shape", 10.0, 8.0, 5.0)
	alerts := a.CheckPositionAlerts(rpt, m, map[string]*data.StockInfo{
		"600000": {Code: "600000", Name: "浦发银行", Price: 8.00, ChangePct: 0},
	}, nil)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条硬止损, got %d", len(alerts))
	}
	if alerts[0].AlertType != "止损" {
		t.Errorf("深度破位应无条件硬止损, got %s", alerts[0].AlertType)
	}
}

// TestCheckPositionAlerts_StopLossHardWhenBear 做多止损出现做空/利空信号 → 硬止损（不降级）。
// English: TestCheckPositionAlerts_StopLossHardWhenBear a long stop-loss with a short/bearish signal → hard stop-loss (no downgrade).
func TestCheckPositionAlerts_StopLossHardWhenBear(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 10 元，止损 5%（现价 8.00 → 盈亏 -20% ≤ -5% 触发），且该股命中利空板块(做空信号)
	// English: Opened at 10, stop-loss 5% (current price 8.00 → P&L -20% ≤ -5% triggers), and the stock matches a bearish sector (short signal).
	rpt.LogSignal("pos-2", "600000", "浦发银行", "做多", "n_shape", 10.0, 8.0, 5.0)
	alerts := a.CheckPositionAlerts(rpt, m, nil, map[string]StockScores{}, map[string]bool{"600000": true})
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条硬止损, got %d", len(alerts))
	}
	if alerts[0].AlertType != "止损" {
		t.Errorf("出现做空信号应硬止损, got %s", alerts[0].AlertType)
	}
}

// TestCheckPositionAlerts_TakeProfitNeedsBull 做多止盈：有做多信号时→降级提示持有；无→硬止盈。
// English: TestCheckPositionAlerts_TakeProfitNeedsBull long take-profit: with a long signal → downgraded to a hint to hold; without one → hard take-profit.
func TestCheckPositionAlerts_TakeProfitNeedsBull(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 40 元，止盈 8%（现价 43.79 → 盈亏 +9.47% ≥ 8% 触发），有做多信号 → 提示持有
	// English: Opened at 40, take-profit 8% (current price 43.79 → P&L +9.47% ≥ 8% triggers), with a long signal → hint to hold.
	rpt.LogSignal("pos-1", "600206", "有研新材", "做多", "dragon", 40.0, 8.0, 5.0)
	scores := map[string]StockScores{"600206": {Code: "600206", SignalActive: true}}
	alerts := a.CheckPositionAlerts(rpt, m, nil, scores)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条提醒, got %d", len(alerts))
	}
	if alerts[0].AlertType != "提示" {
		t.Errorf("有做多信号时止盈应降级为提示持有, got %s", alerts[0].AlertType)
	}
	if !strings.Contains(alerts[0].Reason, "做多信号") {
		t.Errorf("降级提示理由应包含做多信号, got %s", alerts[0].Reason)
	}
}

// TestCheckPositionAlerts_NoThreshold 未设置止盈/止损阈值 → 不产出提醒。
// English: TestCheckPositionAlerts_NoThreshold with no take-profit/stop-loss thresholds set → no alerts produced.
func TestCheckPositionAlerts_NoThreshold(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	rpt.LogSignal("pos-3", "600206", "有研新材", "做多", "dragon", 40.0, 0, 0)
	alerts := a.CheckPositionAlerts(rpt, m, nil, nil)
	if len(alerts) != 0 {
		t.Errorf("无阈值不应产出提醒, got %d", len(alerts))
	}
}

// dailyDropTransport 模拟当日大跌行情（F170=-700 → 涨跌幅 -7.00%）。
// English: dailyDropTransport simulates a sharp same-day drop quote (F170=-700 → change -7.00%).
type dailyDropTransport struct{}

// RoundTrip 模拟东财 push2 stock/get 返回当日大跌行情（涨跌幅 -7.00%）。
func (rt *dailyDropTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.Contains(req.URL.Hostname(), "push2.eastmoney.com") {
		return nil, http.ErrHandlerTimeout
	}
	body := `{"data":{"f43":4000,"f57":"600206","f58":"有研新材","f170":-700}}`
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}, nil
}

// TestCheckPositionAlerts_DailyDrop 当日跌幅超过阈值、成本盈亏未触线 → 仍产出"跌幅提醒"。
// English: TestCheckPositionAlerts_DailyDrop when the day's drop exceeds the threshold but cost P&L has not hit the line → still produces a "drop alert".
func TestCheckPositionAlerts_DailyDrop(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 42 元、止损 5%（现价 40.00 → 成本盈亏 -4.76%，未触及止损线）
	// 但当日涨跌幅 -7% ≤ -5% → 应产出"跌幅提醒"
	// English: Opened at 42 with stop-loss 5% (current price 40.00 → cost P&L -4.76%, stop line not hit), but the day's change of -7% ≤ -5% → should produce a "drop alert".
	rpt.LogSignal("pos-4", "600206", "有研新材", "做多", "dragon", 42.0, 0, 5.0)
	m.SetTransport(&dailyDropTransport{})
	alerts := a.CheckPositionAlerts(rpt, m, nil, nil)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条跌幅提醒, got %d", len(alerts))
	}
	if alerts[0].AlertType != "跌幅提醒" {
		t.Errorf("AlertType 应为跌幅提醒, got %s", alerts[0].AlertType)
	}
	if alerts[0].Action != "关注" {
		t.Errorf("Action 应为关注, got %s", alerts[0].Action)
	}
	if !strings.Contains(alerts[0].Reason, "-7.00%") {
		t.Errorf("理由应包含当日跌幅, got %s", alerts[0].Reason)
	}
}

// TestCheckPositionAlerts_DailyDropOff 当日跌幅低于阈值 → 不产出跌幅提醒。
// English: TestCheckPositionAlerts_DailyDropOff when the day's drop is below the threshold → no drop alert produced.
func TestCheckPositionAlerts_DailyDropOff(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 现价 40.00 当日 -2% > -5%，未触止损线（成本 42 → -4.76%），无任何提醒
	// English: Current price 40.00, day -2% > -5%, stop line not hit (cost 42 → -4.76%) → no alerts.
	rpt.LogSignal("pos-4", "600206", "有研新材", "做多", "dragon", 42.0, 0, 5.0)
	m.SetTransport(&quoteMockTransport{}) // f170=+10%
	alerts := a.CheckPositionAlerts(rpt, m, nil, nil)
	if len(alerts) != 0 {
		t.Errorf("当日跌幅未超阈值不应产出提醒, got %d", len(alerts))
	}
}
