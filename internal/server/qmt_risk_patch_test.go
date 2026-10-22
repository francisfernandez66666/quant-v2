// qmt_risk_patch_test.go — §C1/C2/C3/C5/B7 的 QMT 配置补丁往返锁。
//
// 这三组参数通过 POST /api/config/qmt 的指针式局部 merge 落账号规则；未出现的键必须保持原值。
// English: round-trip locks for the §C1/C2/C3/C5/B7 QMT patch — only present keys change.
package server

import (
	"net/http"
	"testing"
)

func TestQMTRiskPatchRoundTrip(t *testing.T) {
	s, admin, _ := newRegimeTestServer(t)
	body := `{
		"auto_sell_all_sources": true,
		"auto_risk_caution": true,
		"sell_unified_mode": "shadow",
		"entry_pullback_pct": 0.3,
		"entry_timeout_sec": 60,
		"max_signal_vwap_premium_pct": 1.2,
		"risk_gate": {
			"sector_value_pct": 30,
			"sector_value_shadow": false,
			"concept_value_pct": 25,
			"concept_value_shadow": true,
			"risk_per_trade_pct": 0.5,
			"risk_budget_shadow": false,
			"limit_up_block_buy": true,
			"limit_down_block_sell": true,
			"day_loss_limit_pct": 3,
			"single_stock_value_pct": 20,
			"stale_quote_ms": 30000
		}
	}`
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", body)); rr.Code != 200 {
		t.Fatalf("写执行层配置应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	cfg := s.cfg.GetQMTConfigFor(admin.ID)
	if !cfg.AutoSellAllSourcesOn() || !cfg.AutoCautionOn() {
		t.Fatalf("卖出/谨慎层开关未落库: %+v", cfg)
	}
	if cfg.EntryPullbackPct != 0.3 || cfg.EntryTimeoutSec != 60 || cfg.MaxSignalVWAPPremiumPct != 1.2 {
		t.Fatalf("VWAP 参数未落库: %+v", cfg)
	}
	rg := cfg.RiskGate
	if rg.SectorValuePct != 30 || rg.SectorValueShadowOn() || rg.ConceptValuePct != 25 || !rg.ConceptValueShadowOn() {
		t.Fatalf("行业/概念闸未落库: %+v", rg)
	}
	if rg.RiskPerTradePct != 0.5 || rg.RiskBudgetShadowOn() || !rg.LimitUpBlockBuy || !rg.LimitDownBlockSellEnabled() {
		t.Fatalf("ATR/涨跌停闸未落库: %+v", rg)
	}
	if rg.DayLossLimitPct != 3 || rg.SingleStockValuePct != 20 || rg.StaleQuoteMs != 30000 {
		t.Fatalf("既有闸参数未落库: %+v", rg)
	}
	// GET 回显同样带 risk_gate（前端 ExecutionRiskPanel 依赖）。
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	if rr.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rr.Code)
	}
	if !containsAll(rr.Body.String(), `"auto_sell_all_sources":true`, `"sector_value_pct":30`, `"risk_per_trade_pct":0.5`) {
		t.Fatalf("GET 未回显新配置: %s", rr.Body.String())
	}
	// 稀疏语义：只改 min_order_amount，其它新键保持。
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", `{"min_order_amount":10000}`)); rr.Code != 200 {
		t.Fatalf("改 min_order_amount 应 200, got %d", rr.Code)
	}
	if got := s.cfg.GetQMTConfigFor(admin.ID); !got.AutoSellAllSourcesOn() || got.RiskGate.SectorValuePct != 30 {
		t.Fatalf("未出现的键被覆盖: %+v", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
