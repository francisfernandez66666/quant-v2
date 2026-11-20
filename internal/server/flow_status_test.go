// flow_status_test.go — §FLOW-QUADRANT 首页读数面锁（2026-11-20）。
//
// 这批要证的是"看得见且不撒谎"：云端净流入与量能状态必须在 GET /api/status 的 flow 键里
// **分开**报出来，而且缺数只能是 null + *_known=false。
// 反证：把 NULL 折成 0（`out["market_pct"] = 0.0`）或把缺量比读成 "neutral"，
// TestFlowStatusNoRowIsUnknownNotZero / TestFlowStatusPartialLegsAbstain 会红。
package server

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// newFlowStatusServer 造一个带 trading.db 的 Server（flow_factor_daily 就在这个库里）。
func newFlowStatusServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{startTime: time.Now(), agg: &display.Aggregator{}, researchDB: db}, db
}

// fptr 取址小工具（写行时用）。
func fptr(v float64) *float64 { return &v }

// TestFlowStatusNoStoreExplicitlyUnavailable 没有库 ⇒ available=false（"读数未接入"这一态必须能读出来），
// 不许把"没接线"与"今天没数据"混成同一个 known=false。
func TestFlowStatusNoStoreExplicitlyUnavailable(t *testing.T) {
	s := &Server{startTime: time.Now(), agg: &display.Aggregator{}}
	got := s.flowStatusReadout(time.Now())
	if got["available"] != false {
		t.Fatalf("researchDB 为空时 available 必须为 false，实际 %v", got["available"])
	}
	if got["known"] != false {
		t.Fatalf("没有库时 known 必须为 false，实际 %v", got["known"])
	}
	if _, exists := got["market_delta"]; exists {
		t.Fatal("没有库却给出了 market_delta（缺数被编造成读数）")
	}
	if _, exists := got["vol_state"]; exists {
		t.Fatal("没有库却给出了 vol_state（缺数被编造成量态）")
	}
}

// TestFlowStatusNoRowIsUnknownNotZero 库里今天一行都没有 ⇒ known=false 且两个腿都没有值。
// 这是现网开盘前/引擎未跑时的真实形态：必须显示"今日无读数（弃权）"，绝不显示 0% 或平量。
func TestFlowStatusNoRowIsUnknownNotZero(t *testing.T) {
	s, _ := newFlowStatusServer(t)
	got := s.flowStatusReadout(time.Now())
	if got["available"] != true {
		t.Fatalf("库已接线 available 应为 true，实际 %v", got["available"])
	}
	if got["known"] != false {
		t.Fatalf("今日无行时 known 必须为 false，实际 %v", got["known"])
	}
	if _, exists := got["market_pct"]; exists {
		t.Fatal("今日无行却给出了 market_pct：NULL 被折成了 0，弃权与中性再也分不开")
	}
	if _, exists := got["vol_state"]; exists {
		t.Fatal("今日无行却给出了 vol_state：缺数被读成了平量")
	}
	if _, exists := got["blend"]; exists {
		t.Fatal("三条腿全弃权时不该有 blend（前端显示弃权，不能显示 ×1.00 假装参与过）")
	}
}

// TestFlowStatusReadsBothLegs 两条腿都有值时逐个上屏，量态与合成乘子都走 signalctl 真源。
func TestFlowStatusReadsBothLegs(t *testing.T) {
	s, db := newFlowStatusServer(t)
	now := time.Now()
	day := cntime.DayOf(now)
	// 资金流腿：净流入占成交额 -0.6% ⇒ delta -0.2（3% 满档）；量比腿：放量（r_yest 1.5 / r_ma5 1.2）。
	if err := db.UpsertFlowMoneyOnly(store.FlowFactorDailyRow{
		TradeDate: day, MarketFlowPct: fptr(-0.6), FlowMarketDelta: fptr(-0.2),
		Source: "eastmoney_fflow",
	}); err != nil {
		t.Fatalf("写资金流行失败: %v", err)
	}
	if err := db.UpsertFlowVolumeOnly(store.FlowFactorDailyRow{
		TradeDate: day, RYest: fptr(1.5), RMa5: fptr(1.2), VolSurgeDelta: fptr(1.0),
	}); err != nil {
		t.Fatalf("写量比行失败: %v", err)
	}
	// 板块读数两行（覆盖度是行数，不是"中性"）。
	for _, name := range []string{"半导体", "证券"} {
		if err := db.UpsertSectorFlowDaily(store.SectorFlowDailyRow{
			TradeDate: day, Sector: name, NetInflow: 1e8, Amount: 1e10, Delta: 0.03,
		}); err != nil {
			t.Fatalf("写板块行失败: %v", err)
		}
	}

	got := s.flowStatusReadout(now)
	if got["known"] != true || got["market_known"] != true || got["vol_known"] != true {
		t.Fatalf("两腿都有值却标了缺数: %v", got)
	}
	if pct, _ := got["market_pct"].(*float64); pct == nil || math.Abs(*pct-(-0.6)) > 1e-9 {
		t.Fatalf("market_pct 应回 -0.6，实际 %v", got["market_pct"])
	}
	// 量态必须是放量：阈值判定来自 signalctl.VolStateOf，展示层不许另写一份 1.2/1.0 比较。
	if got["vol_state"] != signalctl.VolStateSurge {
		t.Fatalf("r_yest=1.5 / r_ma5=1.2 应为放量，实际 %v", got["vol_state"])
	}
	if d, _ := got["vol_delta"].(float64); d != 1.0 {
		t.Fatalf("放量 vol_delta 应为 +1，实际 %v", got["vol_delta"])
	}
	if n, _ := got["sector_count"].(int); n != 2 {
		t.Fatalf("板块读数行数应为 2，实际 %v", got["sector_count"])
	}
	// 合成乘子：大盘 -0.2 与量能 +1 ⇒ b=0.4 ⇒ 1+0.2×0.4=1.08；同时必须与预览入口逐位一致。
	want, ok := signalctl.FlowBlendPreview(-0.2, math.NaN(), 1.0)
	if !ok {
		t.Fatal("两条腿有值时预览不该弃权")
	}
	blend, _ := got["blend"].(float64)
	if math.Abs(blend-want) > 1e-12 {
		t.Fatalf("blend=%.6f 与 signalctl 预览 %.6f 不一致（展示层另写了公式？）", blend, want)
	}
	if _, exists := got["source"]; exists {
		t.Fatal("不该下发 source：那是两条写口共享的一列，量比腿会把它覆盖成自己的值")
	}
}

// TestFlowStatusPartialLegsAbstain 只有资金流腿有值 ⇒ 量比腿必须 absence（不是 neutral），
// 合成乘子按有效项 renormalize（不许把缺项当 0 拉低）。
func TestFlowStatusPartialLegsAbstain(t *testing.T) {
	s, db := newFlowStatusServer(t)
	now := time.Now()
	if err := db.UpsertFlowMoneyOnly(store.FlowFactorDailyRow{
		TradeDate: cntime.DayOf(now), MarketFlowPct: fptr(-1.5), FlowMarketDelta: fptr(-0.5),
	}); err != nil {
		t.Fatalf("写资金流行失败: %v", err)
	}
	got := s.flowStatusReadout(now)
	if got["market_known"] != true {
		t.Fatalf("资金流腿有值却标了缺数: %v", got)
	}
	if _, exists := got["vol_state"]; exists {
		t.Fatalf("量比腿缺数却给出了 vol_state=%v（缺数被读成平量）", got["vol_state"])
	}
	if got["vol_known"] == true {
		t.Fatal("量比腿缺数却标了 vol_known=true")
	}
	// 只有大盘一条腿 ⇒ blob = 1 + 0.2×(-0.5) = 0.90（renormalize 到单腿 100%）。
	want, ok := signalctl.FlowBlendPreview(-0.5, math.NaN(), math.NaN())
	if !ok {
		t.Fatal("单腿有值时预览不该弃权")
	}
	blend, _ := got["blend"].(float64)
	if math.Abs(blend-want) > 1e-12 {
		t.Fatalf("单腿 blend=%.6f 应等于预览 %.6f（缺项不许当 0）", blend, want)
	}
}

// TestStatusPayloadCarriesFlow /api/status 必须带 flow 键（前端只读这一个面，不新开路由）。
// 顺带钉住"缺数下发 null"的线上形态：没行时 market_pct 是 JSON null，不是 0。
func TestStatusPayloadCarriesFlow(t *testing.T) {
	s, _ := newFlowStatusServer(t)
	rr := httptest.NewRecorder()
	s.handleFixStatus(rr, httptest.NewRequest("GET", "/api/status", nil))
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应非合法 JSON: %v", err)
	}
	raw, ok := out["flow"]
	if !ok {
		t.Fatal("响应缺 flow 字段：首页仪表盘拿不到净流入/量能读数")
	}
	m, _ := raw.(map[string]interface{})
	if m == nil {
		t.Fatal("flow 必须是对象（available/known 两态要能分辨），不能是 null")
	}
	if m["available"] != true {
		t.Fatalf("库已接线 available 应为 true，实际 %v", m["available"])
	}
	if v, exists := m["market_pct"]; exists && v != nil {
		t.Fatalf("今日无行时 market_pct 必须是 JSON null，实际 %v", v)
	}
}
