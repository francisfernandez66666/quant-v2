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
type quoteMockTransport struct{}

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
func newAlertTestRig(t *testing.T) (*Agent, *report.Report, *data.MarketAPI) {
	t.Helper()
	a := New(&config.StrategyConfig{})
	rpt := report.New(filepath.Join(t.TempDir(), "report.json"))
	m := data.NewMarketAPI()
	m.SetTransport(&quoteMockTransport{})
	return a, rpt, m
}

// TestCheckPositionAlerts_NoScore 无打分表时：触止盈线 → 仍产出"止盈"硬提醒。
func TestCheckPositionAlerts_NoScore(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 40 元，止盈 8%（现价 43.79 → 盈亏 +9.47% ≥ 8% 触发）
	rpt.LogSignal("pos-1", "600206", "有研新材", "做多", "dragon", 40.0, 8.0, 5.0)
	alerts := a.CheckPositionAlerts(rpt, m, nil)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条止盈提醒, got %d", len(alerts))
	}
	if alerts[0].AlertType != "止盈" {
		t.Errorf("无打分时应为硬止盈, got %s", alerts[0].AlertType)
	}
}

// TestCheckPositionAlerts_SignalActiveDowngradesToHint 有活跃信号时：触止盈/止损线 → 降级为"提示"。
func TestCheckPositionAlerts_SignalActiveDowngradesToHint(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	rpt.LogSignal("pos-1", "600206", "有研新材", "做多", "dragon", 40.0, 8.0, 5.0)
	scores := map[string]StockScores{
		"600206": {Code: "600206", NScore: 70, SignalActive: true},
	}
	alerts := a.CheckPositionAlerts(rpt, m, scores)
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

// TestCheckPositionAlerts_StopLossDowngrade 止损同样受活跃信号压制：降级为"提示"。
func TestCheckPositionAlerts_StopLossDowngrade(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	// 开仓 10 元，止损 5%（现价 8.00 → 盈亏 -20% ≤ -5% 触发）
	rpt.LogSignal("pos-2", "600000", "浦发银行", "做多", "n_shape", 10.0, 8.0, 5.0)
	scores := map[string]StockScores{
		"600000": {Code: "600000", DragonReturnScore: 65, SignalActive: true},
	}
	alerts := a.CheckPositionAlerts(rpt, m, scores)
	if len(alerts) != 1 {
		t.Fatalf("应产出 1 条降级提示, got %d", len(alerts))
	}
	if alerts[0].AlertType != "提示" {
		t.Errorf("有活跃信号时止损应降级为提示, got %s", alerts[0].AlertType)
	}
}

// TestCheckPositionAlerts_NoThreshold 未设置止盈/止损阈值 → 不产出提醒。
func TestCheckPositionAlerts_NoThreshold(t *testing.T) {
	a, rpt, m := newAlertTestRig(t)
	rpt.LogSignal("pos-3", "600206", "有研新材", "做多", "dragon", 40.0, 0, 0)
	alerts := a.CheckPositionAlerts(rpt, m, nil)
	if len(alerts) != 0 {
		t.Errorf("无阈值不应产出提醒, got %d", len(alerts))
	}
}
