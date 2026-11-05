// news_tier_readout_test.go — §NEWS-TIER 生产接线的读面腿（GET /api/usage 的 news_tier 段）。
//
// 分级器本体与流水线接线分别由 internal/engine 的两份测试盯着；这里锁的是**运营看得见**：
//   - 注册表在位且实现读数接口 ⇒ attached=true，各账号最近一轮读数原样透传
//     （含 constructed=false + skip_reason 的"跑过但被跳过"态——那是本批要根除的
//     "没分级被读成没新闻"的解药，读面上必须留得下来）；
//   - 注册表没实现该接口（或干脆没装配）⇒ attached=false，且 accounts 为空数组——
//     "看不见分级腿"与"分级腿没跑"必须是两个不同的读数，不许互相冒充；
//   - 只读：GET 一次不推进任何状态。
//
// English: locks the operator-facing news_tier block on /api/usage — attached vs skipped vs
// per-account readouts must never collapse into one another.
package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/paper"
)

// newsTierFakeRegistry EngineRegistry 假实现 + NewsTierStatusMaps：
// 只关心读面透传，其余方法为接口占位（与 fakeReviewRegistry 同形状）。
type newsTierFakeRegistry struct {
	maps []map[string]interface{} // NewsTierStatusMaps 的固定返回（模拟各引擎留存读数聚合）
}

func (f *newsTierFakeRegistry) GetController(string) EngineController     { return nil }
func (f *newsTierFakeRegistry) InitStatusJSON(string) map[string]any      { return nil }
func (f *newsTierFakeRegistry) AllControllers() []EngineController        { return nil }
func (f *newsTierFakeRegistry) PaperForUser(string) *paper.Engine         { return nil }
func (f *newsTierFakeRegistry) Len() int                                  { return 0 }
func (f *newsTierFakeRegistry) SetPaperPools([]string)                    {}
func (f *newsTierFakeRegistry) SetPaperConfig(paper.Config)               {}
func (f *newsTierFakeRegistry) SetPaperLabelResolver(func(string) string) {}
func (f *newsTierFakeRegistry) OpenPositionStrategyCounts() combat_agent.HeldStrategyKeys {
	return nil
}
func (f *newsTierFakeRegistry) TriggerPositionReview(string) (int, error) { return 0, nil }

// NewsTierStatusMaps 返回构造好的聚合读数（读面测试的唯一职责）。
func (f *newsTierFakeRegistry) NewsTierStatusMaps() []map[string]interface{} { return f.maps }

func TestNewsTierUsageReadout(t *testing.T) {
	s, admin := newAdminTestServer(t)

	get := func() map[string]any {
		t.Helper()
		rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/usage", ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("usage 应 200, got %d: %s", rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode usage: %v", err)
		}
		return out
	}

	// ① 注册表在位且实现接口：attached=true，账号读数原样透传（跳过态必须在读面上留痕）。
	s.SetEngineRegistry(&newsTierFakeRegistry{maps: []map[string]interface{}{
		{"uid": "u_a", "constructed": true, "high_pushed": float64(1)},
		{"uid": "u_b", "constructed": false, "skip_reason": "无推送通知器(notifier=nil)", "events": float64(3)},
	}})
	usage := get()
	tier, ok := usage["news_tier"].(map[string]any)
	if !ok {
		t.Fatalf("usage 缺 news_tier 段（或形状不对）: %+v", usage["news_tier"])
	}
	if tier["attached"] != true {
		t.Errorf("注册表已实现读数接口却 attached=false: %+v", tier)
	}
	accounts, _ := tier["accounts"].([]any)
	if len(accounts) != 2 {
		t.Fatalf("accounts 应透传 2 条，实得 %+v", tier["accounts"])
	}
	first := accounts[0].(map[string]any)
	if first["uid"] != "u_a" || first["constructed"] != true {
		t.Errorf("读数透传变形: %+v", first)
	}
	second := accounts[1].(map[string]any)
	// 跳过态三件套一个都不许丢：constructed=false、点名原因、本轮真实事件数。
	if second["constructed"] != false || second["skip_reason"] == "" || second["events"] != float64(3) {
		t.Errorf("「跑过但被跳过」的读数在读面上被折叠: %+v", second)
	}

	// ② 注册表实现接口但还没有任何一轮：attached=true + 空数组——
	// "引擎在、还没跑新闻轮"与①的"跑过但跳过"、③的"腿没接上"是三行不同读数。
	s.SetEngineRegistry(&newsTierFakeRegistry{})
	usage = get()
	tier = usage["news_tier"].(map[string]any)
	if tier["attached"] != true {
		t.Errorf("空账也应 attached=true: %+v", tier)
	}
	if accounts, _ := tier["accounts"].([]any); len(accounts) != 0 {
		t.Errorf("没跑过轮却塞了读数: %+v", tier["accounts"])
	}

	// ③ 注册表没实现读数接口：attached=false（"看不见"不许冒充"没跑过"）。
	s.SetEngineRegistry(&plainFakeNoAggregator{})
	usage = get()
	tier = usage["news_tier"].(map[string]any)
	if tier["attached"] != false {
		t.Errorf("读数腿没接上却报 attached=true: %+v", tier)
	}
	if accounts, _ := tier["accounts"].([]any); len(accounts) != 0 {
		t.Errorf("attached=false 时不该有 accounts 条目: %+v", tier["accounts"])
	}
}

// plainFakeNoAggregator 逐字实现 EngineRegistry 主接口、刻意不实现 NewsTierStatusMaps——
// 用于复刻"注册表在位但读数腿没接"的部署形态（旧二进制 + 新前端之类）。
// English: implements EngineRegistry WITHOUT the aggregator, reproducing "registry present but
// the readout leg not attached".
type plainFakeNoAggregator struct{}

func (p *plainFakeNoAggregator) GetController(string) EngineController     { return nil }
func (p *plainFakeNoAggregator) InitStatusJSON(string) map[string]any      { return nil }
func (p *plainFakeNoAggregator) AllControllers() []EngineController        { return nil }
func (p *plainFakeNoAggregator) PaperForUser(string) *paper.Engine         { return nil }
func (p *plainFakeNoAggregator) Len() int                                  { return 0 }
func (p *plainFakeNoAggregator) SetPaperPools([]string)                    {}
func (p *plainFakeNoAggregator) SetPaperConfig(paper.Config)               {}
func (p *plainFakeNoAggregator) SetPaperLabelResolver(func(string) string) {}
func (p *plainFakeNoAggregator) OpenPositionStrategyCounts() combat_agent.HeldStrategyKeys {
	return nil
}
func (p *plainFakeNoAggregator) TriggerPositionReview(string) (int, error) { return 0, nil }
