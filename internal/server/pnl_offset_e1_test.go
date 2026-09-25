// pnl_offset_e1_test.go — §E1 盈亏单轨（owner 裁决 2026-09-26）服务端回归：
// ① GET /api/holdings 的 total_pnl 等值锁 = 已实现 + 浮动 − 入库偏移（算式只在后端一处）；
// ② POST /api/holdings/pnl-offset 的 offset/reset 两条分支（reset 由**后端**按自己算式取值，
//
//	不信任前端传数——那正是旧 localStorage 版两套账的病根）；
//
// ③ 校准后 GET 回读 total_pnl 归零，且留痕行数只增不减（append-only）。
// 说明：最小化 Server 无行情注入（s.quote 直接报错回退开仓价），浮动腿恒为 0 是测试环境形态，
// 不是断言"浮动=0"普适成立；浮动腿的算式在 paperPnlTotals 单点内由 buildHolding 下发字段保证同值。
// English: §E1 server regression — backend-computed total_pnl equality, offset/reset branches of the
// calibration endpoint, and append-only audit rows.
package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
)

// e1Server 搭一个带 live 库 + 报表库的最小管理员服务，返回 (s, admin, db, rpt)。
func e1Server(t *testing.T) (*Server, *auth.User, *store.DB, *report.Report) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	s.SetLiveDB(db)
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	_ = mgr.Init()
	cfgMgr := config.NewManager(dir + "/config.json")
	full := New(mgr, display.New(), cfgMgr, report.New(filepath.Join(dir, "report.json")), data.NewMarketAPI(), data.NewWatchlistManager(dir), data.NewTHSClient())
	s.rpt = full.rpt
	// buildHolding 会经 dashFor 读实时聚合器；最小 Server 的 s.agg 为 nil，这里补接
	// （否则种一笔持仓后 GET /api/holdings 直接 SIGSEGV——dashFor 的 nil 回退路径依赖 cacheDir）。
	s.agg = full.agg
	return s, admin, db, full.rpt
}

// e1GetHoldings 调 GET /api/holdings 并解析出 §E1 三个汇总字段。
func e1GetHoldings(t *testing.T, s *Server, admin *auth.User) (realized, unrealized, offset, total float64, totalNull bool) {
	t.Helper()
	req := adminReq(s, admin, "GET", "/api/holdings", "")
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("GET /api/holdings 期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	for k, dst := range map[string]*float64{
		"total_realized_pnl": &realized, "total_unrealized_pnl": &unrealized, "pnl_offset": &offset,
	} {
		v, ok := body[k].(float64)
		if !ok {
			t.Fatalf("响应缺少数值字段 %s: %v", k, body[k])
		}
		*dst = v
	}
	if tp, ok := body["total_pnl"]; !ok || tp == nil {
		totalNull = true
	} else {
		total, _ = tp.(float64)
	}
	return
}

// TestPnlOffsetE1SingleTrack 汇总等值 + 两条校准分支 + 留痕只增。
func TestPnlOffsetE1SingleTrack(t *testing.T) {
	s, admin, _, _ := e1Server(t)
	// 种一笔持仓（成本 10、100 股）+ 该标的已实现 50：total 的已实现腿有数、浮动腿环境内为 0。
	rpt := s.rpt
	rpt.LogSignalWithMetaQtyUser("sig_e1", "600999", "E1试验票", "做多", "N形", 10, 8, 5, 100, nil, admin.ID)
	rpt.Update("sig_e1", func(l *report.ExecLog) { l.RealizedPnl = 50 })

	realized, _, offset0, total0, null0 := e1GetHoldings(t, s, admin)
	if null0 || realized != 50 || offset0 != 0 || total0 != 50 {
		t.Fatalf("初始应 (realized=50, offset=0, total=50), got %v/%v/%v null=%v", realized, offset0, total0, null0)
	}

	// 分支一：显式 offset=20 → total = 50 − 20 = 30（等值锁，前端不再有第二条算式）。
	req := adminReq(s, admin, "POST", "/api/holdings/pnl-offset", `{"offset":20}`)
	if rr := adminDo(s, req); rr.Code != 200 {
		t.Fatalf("offset 校准期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, _, offset1, total1, null1 := e1GetHoldings(t, s, admin)
	if null1 || offset1 != 20 || total1 != 30 {
		t.Fatalf("offset=20 后应 (20,30), got (%v,%v) null=%v", offset1, total1, null1)
	}

	// 分支二：reset=true → 后端按自己的算式取 50（**不是**前端传什么记什么），total 归 0。
	req = adminReq(s, admin, "POST", "/api/holdings/pnl-offset", `{"reset":true,"note":"回归测试清零"}`)
	if rr := adminDo(s, req); rr.Code != 200 {
		t.Fatalf("reset 校准期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	_, _, offset2, total2, null2 := e1GetHoldings(t, s, admin)
	if null2 || offset2 != 50 || total2 != 0 {
		t.Fatalf("reset 后应 (50,0), got (%v,%v) null=%v", offset2, total2, null2)
	}

	// 留痕只增不减的等值锁在存储层（store/pnl_offset_test.go：两次追加 → COUNT=2、无 UPDATE 入口）；
	// 本用例锁的是服务端算式与端点行为，不重复数行。
}

// TestPnlOffsetE1AdminOnly 成员（非 admin）访问校准端点必 403——写口径与 balance 端点同款守卫。
func TestPnlOffsetE1AdminOnly(t *testing.T) {
	s, _, _, _ := e1Server(t)
	member, err := s.auth.CreateUser("member_e1", "pw", "", nil, 0)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	req := adminReq(s, member, "POST", "/api/holdings/pnl-offset", `{"reset":true}`)
	if rr := adminDo(s, req); rr.Code != 403 {
		t.Fatalf("成员访问校准端点应 403, got %d", rr.Code)
	}
}
