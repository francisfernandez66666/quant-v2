// advice_bear_test.go — 实盘持仓建议的利空分级映射（§NEWS_BEAR）单元测试。
// 验证 fromSignal 对利空分级信号（利空清仓/利空减仓/利空观望）的映射契约——
// 这是实盘自动卖出链路（autoExecuteRealSells）的输入依据：
//   - 利空清仓 → 止损/高 + Source=news_bear（无条件自动全平）；
//   - 利空减仓 → 减仓/高 + Source=news_bear（半平自动执行）；
//   - 利空观望 → 持有/低 + Source 空（仅提醒，不触发自动卖出）；
//   - 默认档（非显式分级）里含"利空/抛售"理由仍兜底止损（FIX#13 兼容）。
//
// English: live-advice mapping of the §NEWS_BEAR graded levels via fromSignal — the contract the
// live auto-sell path (autoExecuteRealSells) consumes: 利空清仓→止损/高+news_bear (full close),
// 利空减仓→减仓/高+news_bear (half trim), 利空观望→持有/低+empty source (reminder only); legacy
// default-branch reason fallback (利空/抛售→止损) stays FIX#13 compatible.
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// bearAdviceInput 构造含单只持仓的 AdviceInput（fromSignal 仅用 Positions 定位持仓）。
func bearAdviceInput(pos store.RealPosition) AdviceInput {
	return AdviceInput{
		Positions: []store.RealPosition{pos},
		Cfg:       config.DefaultQMTConfig(),
		BearNews:  config.DefaultBearNewsConfig(),
	}
}

// TestFromSignalBearGrading 利空分级 AlertType → Action/Level/Source 映射契约。
func TestFromSignalBearGrading(t *testing.T) {
	pos := store.RealPosition{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"}
	in := bearAdviceInput(pos)

	cases := []struct {
		alertType string
		wantAct   string
		wantLevel string
		wantSrc   string
	}{
		{"利空清仓", "止损", "高", "news_bear"}, // 清仓档 → 止损级（自动卖出保护性无条件执行）
		{"利空减仓", "减仓", "高", "news_bear"}, // 减仓档 → 减仓级（autoExecuteRealSells 放行半平）
		{"利空观望", "持有", "低", ""},          // 观望档 → 仅提醒不动作
	}
	for _, tc := range cases {
		t.Run(tc.alertType, func(t *testing.T) {
			sig := combat_agent.Signal{
				Code: "600000", Name: "浦发", Direction: "提醒",
				Action: "卖出", AlertType: tc.alertType, Price: 10, Reason: "利空归因测试",
				GeneratedAt: time.Now(),
			}
			pa := fromSignal(sig, in, time.Now(), "")
			if pa == nil {
				t.Fatal("fromSignal 不应返回 nil")
			}
			if pa.Action != tc.wantAct || pa.Level != tc.wantLevel {
				t.Fatalf("%s 应映射 %s/%s, got %s/%s", tc.alertType, tc.wantAct, tc.wantLevel, pa.Action, pa.Level)
			}
			if pa.Source != tc.wantSrc {
				t.Fatalf("%s 的 Source 应为 %q, got %q", tc.alertType, tc.wantSrc, pa.Source)
			}
		})
	}
}

// TestFromSignalBearLegacyReasonFallback 默认档（非显式分级）reason 含"利空/抛售"→ 止损高（FIX#13 兼容）。
func TestFromSignalBearLegacyReasonFallback(t *testing.T) {
	pos := store.RealPosition{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "手动"}
	in := bearAdviceInput(pos)
	sig := combat_agent.Signal{
		Code: "000001", Name: "平安", AlertType: "提示", Action: "关注",
		Price: 9.5, Reason: "利空板块抛售传导", GeneratedAt: time.Now(),
	}
	pa := fromSignal(sig, in, time.Now(), "")
	if pa.Action != "止损" || pa.Level != "高" {
		t.Fatalf("默认档 reason 含利空/抛售应兜底 止损/高, got %s/%s", pa.Action, pa.Level)
	}
}

// TestFromSignalBearRefPrice 利空分级建议 RefPrice 取自信号现价（自动卖出挂单价）。
func TestFromSignalBearRefPrice(t *testing.T) {
	pos := store.RealPosition{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"}
	in := bearAdviceInput(pos)
	sig := combat_agent.Signal{Code: "600000", AlertType: "利空减仓", Action: "卖出", Price: 9.4, Reason: "利空测试", GeneratedAt: time.Now()}
	pa := fromSignal(sig, in, time.Now(), "")
	if pa.RefPrice != 9.4 {
		t.Fatalf("利空减仓建议 RefPrice 应为现价 9.4（挂单价）, got %.2f", pa.RefPrice)
	}
}

// TestBearNewsEnsureDefaultsConfig 缺省配置下的分级决策可用（引擎/建议层注入 EnsureDefaults 后）。
func TestBearNewsEnsureDefaultsConfig(t *testing.T) {
	d := config.BearNewsConfig{}.EnsureDefaults()
	if !d.EnabledOn() || !d.LimitUpHoldOn() {
		t.Fatal("零值配置 EnsureDefaults 后应启用分级且封板保护开")
	}
	if d.SellScore <= 0 || d.TrimScore <= 0 || d.BreakPct <= 0 {
		t.Fatalf("EnsureDefaults 应回填阈值, got %+v", d)
	}
	// 显式关闭保留（Enabled=false 不被默认覆盖）
	off := false
	explicit := config.BearNewsConfig{Enabled: &off}.EnsureDefaults()
	if explicit.EnabledOn() {
		t.Fatal("显式 enabled=false 应保持关闭")
	}
}
