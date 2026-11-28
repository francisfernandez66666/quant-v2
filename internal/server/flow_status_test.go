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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
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

// TestFlowStatusSidePolicySnapshot 锁定首页侧策略快照的已知/缺失两态。
func TestFlowStatusSidePolicySnapshot(t *testing.T) {
	s, _ := newFlowStatusServer(t)
	s.researchDir = t.TempDir()
	now := time.Date(2026, 11, 24, 13, 0, 0, 0, cntime.Loc)
	st := signalctl.SidePolicyState{Enabled: true, Day: "20261124", Mode: signalctl.SidePolicyBear, AsOf: now,
		Level: 2, AllowNew: false, AllowAdd: false, TargetMultiplier: 0.7,
		LeftWeight: 1, RightWeight: 0.8, Latch: signalctl.SidePolicyLatch{Kind: "no_new", Until: "14:00"},
		LatchUntilUnix: now.Add(time.Hour).Unix(), Reason: "pm1300_latch"}
	raw, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(s.researchDir, "side_policy_latest.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	got := s.flowStatusReadout(now)
	side, ok := got["side_policy"].(map[string]interface{})
	if !ok || side["known"] != true || side["mode"] != "bear" || side["allow_new"] != false ||
		side["latch_active"] != true || side["stale"] != false || side["status"] != "known" {
		t.Fatalf("side policy readout mismatch: %+v", got["side_policy"])
	}
}

func writeSidePolicySnapshot(t *testing.T, dir string, st signalctl.SidePolicyState) {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "side_policy_latest.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestFlowStatusSidePolicyDisabledIsNotNeutralMultiplier 验证停用态不冒充中性仓位乘子。
func TestFlowStatusSidePolicyDisabledIsNotNeutralMultiplier(t *testing.T) {
	s, _ := newFlowStatusServer(t)
	s.researchDir = t.TempDir()
	now := time.Date(2026, 11, 24, 10, 30, 0, 0, cntime.Loc)
	writeSidePolicySnapshot(t, s.researchDir, signalctl.SidePolicyState{
		Enabled: false, Day: "20261124", Mode: signalctl.SidePolicyLegacy,
		AllowNew: true, AllowAdd: true, TargetMultiplier: 1, LeftWeight: 1, RightWeight: 1,
		Reason: "side_policy_disabled",
	})
	side := s.flowStatusReadout(now)["side_policy"].(map[string]interface{})
	if side["known"] != true || side["enabled"] != false || side["status"] != "disabled" || side["stale"] != false {
		t.Fatalf("disabled must be its own known state: %+v", side)
	}
	if _, exists := side["target_multiplier"]; exists {
		t.Fatalf("disabled must not publish a neutral multiplier: %+v", side)
	}
}

// TestFlowStatusSidePolicyStaleAndMissingAsOfAbstain 验证过期与缺时刻都回落 L1 禁新。
func TestFlowStatusSidePolicyStaleAndMissingAsOfAbstain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		asOf   time.Time
		reason string
	}{
		{"expired", time.Time{}, "side_policy_stale"},
		{"missing", time.Time{}, "side_policy_asof_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newFlowStatusServer(t)
			s.researchDir = t.TempDir()
			now := time.Date(2026, 11, 24, 10, 30, 0, 0, cntime.Loc)
			asOf := tc.asOf
			if tc.name == "expired" {
				asOf = now.Add(-time.Duration((config.CarrierSidePolicyTTLSec() + 1) * float64(time.Second)))
			}
			writeSidePolicySnapshot(t, s.researchDir, signalctl.SidePolicyState{
				Enabled: true, Day: "20261124", Mode: signalctl.SidePolicyBull, AsOf: asOf,
				Level: 2, AllowNew: true, AllowAdd: true, TargetMultiplier: 1.1,
				LeftWeight: 1, RightWeight: 1, Reason: "bull_mirror",
			})
			side := s.flowStatusReadout(now)["side_policy"].(map[string]interface{})
			if side["known"] != true || side["enabled"] != true || side["status"] != "stale" || side["stale"] != true ||
				side["reason"] != tc.reason || side["level"] != 1 || side["allow_new"] != false || side["allow_add"] != true {
				t.Fatalf("stale/missing AsOf must abstain to L1: %+v", side)
			}
		})
	}
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

// ---------------------------------------------------------------------------
// D6-c 2026-11-25：侧策略快照改读**账号**目录（并保留根目录兜底）
//
// 现场：`side_policy_latest.json` 曾写在数据根目录，而发版后写点搬到
// `accounts/<uid>/`。如果读面只认一个目录，迁移期必然出现"发布成功、页面显示未知"
// ——那是最难判的一类，因为两边都正常工作。
// ---------------------------------------------------------------------------

func writeSidePolicyFixture(t *testing.T, dir, day string, st signalctl.SidePolicyState) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "side_policy_latest.json"), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func liveSideFixture(day string, now time.Time) signalctl.SidePolicyState {
	return signalctl.SidePolicyState{
		Enabled: true, Day: strings.ReplaceAll(day, "-", ""),
		Mode: signalctl.SidePolicyBear, Level: 2,
		AllowNew: false, AllowAdd: true,
		TargetMultiplier: 0.70, LeftWeight: 0.8, RightWeight: 0.8,
		Reason: "bear_l2", AsOf: now, AsOfUnix: now.Unix(),
	}
}

func TestReadSidePolicyStatusPrefersTheAccountDir(t *testing.T) {
	root := t.TempDir()
	acct := filepath.Join(root, "accounts", "u_1")
	now := time.Date(2026, 11, 25, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	// 根目录放一份旧日期的（迁移残留），账号目录放当日的：必须读账号那份。
	writeSidePolicyFixture(t, root, "2026-11-24", signalctl.SidePolicyState{Enabled: true, Day: "20261124"})
	writeSidePolicyFixture(t, acct, "2026-11-25", liveSideFixture("2026-11-25", now))
	out := readSidePolicyStatus(acct, root, "2026-11-25", now)
	if ok, _ := out["known"].(bool); !ok {
		t.Fatalf("account-dir snapshot must be read, got %+v", out)
	}
	if out["status"] != "known" || out["mode"] != string(signalctl.SidePolicyBear) {
		t.Fatalf("want known/bear from the account dir, got %+v", out)
	}
	if lvl, _ := out["level"].(int); lvl != 2 {
		t.Fatalf("level must travel, got %+v", out)
	}
}

func TestReadSidePolicyStatusFallsBackToTheRootDir(t *testing.T) {
	root := t.TempDir()
	acct := filepath.Join(root, "accounts", "u_1") // 不存在
	now := time.Date(2026, 11, 25, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	writeSidePolicyFixture(t, root, "2026-11-25", liveSideFixture("2026-11-25", now))
	out := readSidePolicyStatus(acct, root, "2026-11-25", now)
	if ok, _ := out["known"].(bool); !ok {
		t.Fatalf("root-dir fallback must be read, got %+v", out)
	}
	if out["status"] != "known" {
		t.Fatalf("want known via fallback, got %+v", out)
	}
}

func TestReadSidePolicyStatusUnknownWhenBothDirsAreEmpty(t *testing.T) {
	root := t.TempDir()
	acct := filepath.Join(root, "accounts", "u_1")
	now := time.Date(2026, 11, 25, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))
	out := readSidePolicyStatus(acct, root, "2026-11-25", now)
	if ok, _ := out["known"].(bool); ok || out["status"] != "unknown" {
		t.Fatalf("missing snapshot must be unknown (never a neutral known), got %+v", out)
	}
	// 跨日快照同样不算 known：day-keyed 的文件拿错日期等于"今天没读数"。
	writeSidePolicyFixture(t, acct, "2026-11-24", liveSideFixture("2026-11-24", now))
	if out := readSidePolicyStatus(acct, root, "2026-11-25", now); out["known"].(bool) {
		t.Fatalf("yesterday's snapshot must not be reported as today's, got %+v", out)
	}
	// 空 dir（无数据目录）也不许 panic。
	if out := readSidePolicyStatus("", "", "2026-11-25", now); out["known"].(bool) {
		t.Fatalf("empty dir must be unknown, got %+v", out)
	}
}

// TestFlowStatusStockFlowReadout 锁定个股资金流读数六键的三态（§REPLAY-INPUTS 档 3b）。
//
// 这条腿最容易出的错不是"报错"，而是"停更了却看着正常"：ths_* 静默停更一年多就是这个形状。
// 所以三件事必须分开报——表里没数据（known=false）、今天没采（codes=0）、采集器未跑（status 缺席），
// 而且**当日没有 flow_factor_daily 行时也必须照常报**（盘前/非交易日正是最需要看到停更的时候）。
func TestFlowStatusStockFlowReadout(t *testing.T) {
	s, db := newFlowStatusServer(t)
	s.researchDir = t.TempDir()

	// ① 表在但从没采过：known=false，且不编造 day/codes/coverage 三键。
	got := s.flowStatusReadout(time.Now())
	if got["stock_flow_known"] != false {
		t.Fatalf("空表必须 stock_flow_known=false，实际 %+v", got["stock_flow_known"])
	}
	for _, k := range []string{"stock_flow_day", "stock_flow_codes", "stock_flow_coverage_pct"} {
		if _, exists := got[k]; exists {
			t.Fatalf("空表不许给出 %s（缺数被编造成读数）", k)
		}
	}
	if _, exists := got["stock_flow_run_status"]; exists {
		t.Fatalf("采集器没跑过时不许给 run_status，实际 %v", got["stock_flow_run_status"])
	}

	// ② 有行 + 可采池：六键齐全，day 回横线形态，覆盖率 = codes/total*100。
	if _, err := db.UpsertStockListings([]store.StockListing{
		{TsCode: "600519.SH", Name: "贵州茅台"},
		{TsCode: "000001.SZ", Name: "平安银行"},
		{TsCode: "920001.BJ", Name: "北交所样例"}, // 北交所不进分母
	}); err != nil {
		t.Fatalf("写入股票清单失败: %v", err)
	}
	if _, err := db.UpsertStockMoneyFlow([]store.StockFlowRow{
		{TsCode: "600519.SH", TradeDate: "20261127", MainNet: 1e8, HasMainNet: true,
			MainPct: 5.5, HasMainPct: true, Amount: 2e9, HasAmount: true, Source: "eastmoney_fflow_relay_seed"},
	}); err != nil {
		t.Fatalf("写入个股资金流失败: %v", err)
	}
	// 采集器标记：有结论才报（finished_at 走 RFC3339）。
	now := time.Date(2026, 11, 28, 16, 0, 0, 0, cntime.Loc)
	mark := `{"day":"20261127","status":"ok","finished_at":"2026-11-28T15:40:00+08:00"}`
	if err := os.WriteFile(filepath.Join(s.researchDir, "moneyflow_run.json"), []byte(mark), 0644); err != nil {
		t.Fatalf("写采集标记失败: %v", err)
	}

	got = s.flowStatusReadout(now)
	if got["stock_flow_known"] != true {
		t.Fatalf("有行时 stock_flow_known 必须为 true，实际 %+v", got)
	}
	if got["stock_flow_day"] != "2026-11-27" {
		t.Fatalf("stock_flow_day 必须回横线形态，实际 %v", got["stock_flow_day"])
	}
	if got["stock_flow_codes"] != 1 {
		t.Fatalf("stock_flow_codes 应为 1，实际 %v", got["stock_flow_codes"])
	}
	if got["stock_flow_total"] != 2 {
		t.Fatalf("分母必须排除北交所（2 只），实际 %v", got["stock_flow_total"])
	}
	if pct, _ := got["stock_flow_coverage_pct"].(float64); pct != 50 {
		t.Fatalf("覆盖率应为 50%%，实际 %v", got["stock_flow_coverage_pct"])
	}
	if got["stock_flow_run_status"] != "ok" {
		t.Fatalf("采集状态必须照报，实际 %v", got["stock_flow_run_status"])
	}
	if age, _ := got["stock_flow_run_age_sec"].(float64); age != 1200 {
		t.Fatalf("采集龄应为 1200 秒（16:00-15:40），实际 %v", got["stock_flow_run_age_sec"])
	}
	// ③ 当日没有 flow_factor_daily 行（本用例从未写入）时，上面六键仍必须在场——
	//    早退分支吞掉采集器读数，等于"最该看停更的那天看不见"。
	if got["known"] != false {
		t.Fatalf("无 flow_factor_daily 行时 known 仍须 false，实际 %v", got["known"])
	}
}
