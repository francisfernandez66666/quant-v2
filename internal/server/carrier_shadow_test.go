// carrier_shadow_test.go — §CARRIER-SHADOW（2026-11-05 载体影子对照批）Go 侧行为锁。
//
// 这批交付的是"移植规则须与引擎对照跑满 ≥5 个交易日"这条纪律的**可计量性**，
// 所以用例逐条钉的是"分子不能被做脏"，而不是"函数返回得对不对"：
//
//	① 归属只由钥匙决定：行上自报的 user_id 一律丢弃，写进的是这把钥匙的主人（§安全 T1）；
//	② 空批次整条 400：把"从没报过"落成"今天报了零条"，5 天窗口就能被一串静默日凑满
//	   （missing≠empty 的写面半边，读面那半见 ④）；
//	③ 观测腿不许有钱的动作：一条影子决策**即使与引擎同日同码同方向完全对上**，
//	   也绝不产生委托/成交/持仓任何一行（§OBS-GUARD 同取向）；
//	④ 三种零分开：no_report（这天整根腿没报）/ no_hits（报了但这类零命中）/
//	   diverged（有命中、引擎一条不认）三种格在同一个读数里必须各自可辨，
//	   并且只有"有命中"的那几天才推进 5 日窗口；
//	⑤ 读不出就说读不出：账本没接 / 身份缺失 ⇒ known=false + 原因，
//	   **绝不**返回一张空 kinds 让人读成"今天没有影子决策"；
//	⑥ 跨日不复活：day 进主键，昨天那格与今天同短码那格互不覆盖（存储面另有一条同名锁）。
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// 测试钉死的一个交易日基准：2026-11-05 是周四（非周末），窗口往前翻的都是工作日形态的日期，
// 用例不依赖"今天"——读面按天分格，用真实时钟跑会让周末与节假日把断言吹散。
var carrierShadowNow = time.Date(2026, 11, 5, 15, 30, 0, 0, cntime.Loc)

// newCarrierShadowServer 造一个只接了实盘账本的 Server（无 registry/无控制器/无 SSE）。
// 三样都不接是刻意的：这条腿按设计**不需要**任何资本动作的入口，接上控制器反而会给
// "观测写面能碰熔断"留下口子的形状。
func newCarrierShadowServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	s := &Server{}
	s.SetCacheDir(t.TempDir())
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	return s, db
}

// carrierShadowRow 一条合法的影子行（可选按字段覆盖）。
func carrierShadowRow(day, code, kind string, mut ...func(*store.CarrierShadowDecision)) store.CarrierShadowDecision {
	r := store.CarrierShadowDecision{
		Day: day, ShortCode: "DR" + strings.ReplaceAll(code[:6], " ", ""), TsCode: code,
		Kind: kind, Side: "buy", Qty: 100, Price: 10, DecidedAt: "2026-11-05T09:31:00+08:00",
	}
	for _, fn := range mut {
		fn(&r)
	}
	return r
}

// carrierPostReport 走**真实回报入口**（handleQMTReport）：这条链是上行腿唯一的落库通道，
// 绕开它去调内部函数就测不到"新事件类型有没有接进 switch"。
func carrierPostReport(t *testing.T, s *Server, uid string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewReader(raw))
	r = r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: uid}))
	rec := httptest.NewRecorder()
	s.handleQMTReport(rec, r)
	return rec
}

// TestCarrierShadowIngestAttributesToKeyOnly 锁 ①：行上自报的 user_id 结构上留不进账本。
// 归属错了的 5 日窗口比没有窗口更糟——那会把别人那根腿的判定算在自己头上。
func TestCarrierShadowIngestAttributesToKeyOnly(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	day := cntime.DayCompactOf(carrierShadowNow)
	rec := carrierPostReport(t, s, "u_leg", map[string]any{
		"type": "carrier_shadow", "user_id": "u_leg",
		"shadow": []map[string]any{
			{"day": day, "short_code": "DR600000", "ts_code": "600000.SH", "kind": "dragon",
				"side": "buy", "qty": 100, "price": 10.0, "user_id": "u_intruder"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("carrier_shadow 应 200，实到 %d body=%s", rec.Code, rec.Body.String())
	}
	mine, err := db.CarrierShadowRowsForDay("u_leg", day)
	if err != nil || len(mine) != 1 {
		t.Fatalf("钥匙归属的账号应拿到 1 行，got %d err=%v", len(mine), err)
	}
	// 自报的那个账号必须一行都没有：归属只有 uid（钥匙反查）这一个来源。
	theirs, err := db.CarrierShadowRowsForDay("u_intruder", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Fatalf("行上自报的 user_id 落库了（§安全 T1 被推翻）：%+v", theirs)
	}
}

// TestCarrierShadowEmptyBatchRefused 锁 ②：空批次整条 400，绝不落"今天报了零条"。
// 400 让上行侧把它记成死信（可取证、不推进去重基线），而不是让那一天进窗口当真实的一天。
func TestCarrierShadowEmptyBatchRefused(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	day := cntime.DayCompactOf(carrierShadowNow)
	rec := carrierPostReport(t, s, "u_leg2", map[string]any{
		"type": "carrier_shadow", "user_id": "u_leg2", "shadow": []map[string]any{},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空批次必须 400，实到 %d body=%s", rec.Code, rec.Body.String())
	}
	counts, err := db.CarrierShadowDayCounts("u_leg2", day, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Fatalf("空批次把静默的一天写成了\"有上报\"：%+v", counts)
	}
}

// TestCarrierShadowOverLimitRefused 锁 ②的另一半：超上限整条拒收而不是截断（静默截断＝丢证据）。
func TestCarrierShadowOverLimitRefused(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	day := cntime.DayCompactOf(carrierShadowNow)
	rows := make([]map[string]any, 0, carrierShadowMaxRows+2)
	for i := 0; i <= carrierShadowMaxRows; i++ {
		rows = append(rows, map[string]any{
			"day": day, "short_code": "DR" + strconv.Itoa(i),
			"ts_code": "600000.SH", "kind": "dragon", "side": "buy", "qty": 100, "price": 10.0,
		})
	}
	rec := carrierPostReport(t, s, "u_leg3", map[string]any{"type": "carrier_shadow", "shadow": rows})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超上限必须整条 400，实到 %d", rec.Code)
	}
	counts, _ := db.CarrierShadowDayCounts("u_leg3", day, day)
	if counts[day] != 0 {
		t.Fatalf("超上限的一批被截断落库了（静默丢证据）：%+v", counts)
	}
}

// TestCarrierShadowFutureDayRowsDropped 锁 ④的前半：晚于今天的 day 逐行摘除，其余照收。
// 一行 "20991231" 会在窗口里自成一天，而那一天永远不会被交易日历承认。
func TestCarrierShadowFutureDayRowsDropped(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	today := cntime.DayCompactOf(carrierShadowNow)
	rec := carrierPostReport(t, s, "u_leg4", map[string]any{
		"type": "carrier_shadow", "shadow": []map[string]any{
			{"day": today, "short_code": "DR600000", "ts_code": "600000.SH", "kind": "dragon",
				"side": "buy", "qty": 100, "price": 10.0},
			{"day": "20991231", "short_code": "DR600001", "ts_code": "600001.SH", "kind": "dragon",
				"side": "buy", "qty": 100, "price": 10.0},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("只该摘一行，不该整条失败，实到 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "future_day") {
		t.Fatalf("响应必须点名摘了多少未来日：%s", rec.Body.String())
	}
	var res carrierShadowIngestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if res.Written != 1 {
		t.Fatalf("只该落 1 行，实到 %d", res.Written)
	}
	future, err := db.CarrierShadowRowsForDay("u_leg4", "20991231")
	if err != nil {
		t.Fatal(err)
	}
	if len(future) != 0 {
		t.Fatalf("未来日那行落库了：%+v", future)
	}
}

// TestCarrierShadowNeverPlacesAnOrder 锁 ③（本批唯一一条"钱"的红线）：
// 影子决策即使与引擎**同日同码同方向**逐字对上，也一行委托/成交/持仓都不产生。
//
// 为什么这条必须是行为测试而不是静态 grep：shadow 这一族最容易出的错形状是
// "对照表里发现了 agreement，于是顺手在这里补一次下单/补一次额度预占"——
// 编译与静态锁都抓不到，只有把整条回报入口跑一遍才看得见。
func TestCarrierShadowNeverPlacesAnOrder(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	uid := "u_money"
	day := cntime.DayCompactOf(carrierShadowNow)
	// 引擎侧先在**同一只票**上留一份定权快照：这样影子上行到达时一定会被认成"引擎同意"，
	// 于是"同意"这条路径被真的走了一遍——它必须只写观测表。
	if err := db.UpsertSignalDecision(store.SignalDecision{
		UserID: uid, Day: day, Channel: "live", SignalID: "sig-1", Code: "600000",
		Strategy: "fac_1", At: "2026-11-05T09:30:00+08:00", Stage: "evaluated", Qty: 100,
	}); err != nil {
		t.Fatal(err)
	}
	beforeOrders := len(dbSlice(t, func() ([]store.RealOrder, error) { return db.RealOrdersForUser(uid) }))
	beforePos := len(dbSlice(t, func() ([]store.RealPosition, error) { return db.RealPositionsForUser(uid) }))

	rec := carrierPostReport(t, s, uid, map[string]any{
		"type": "carrier_shadow", "shadow": []map[string]any{
			{"day": day, "short_code": "DR600000", "ts_code": "600000.SH", "kind": "dragon",
				"side": "buy", "qty": 100, "price": 10.0},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("上行应 200，实到 %d body=%s", rec.Code, rec.Body.String())
	}

	// ③-a 委托/成交/持仓三本账一个字都没动
	afterOrders := len(dbSlice(t, func() ([]store.RealOrder, error) { return db.RealOrdersForUser(uid) }))
	afterPos := len(dbSlice(t, func() ([]store.RealPosition, error) { return db.RealPositionsForUser(uid) }))
	if beforeOrders != afterOrders || afterOrders != 0 {
		t.Fatalf("影子决策下出了委托（前 %d 后 %d）——观测腿有了钱的动作", beforeOrders, afterOrders)
	}
	if beforePos != afterPos || afterPos != 0 {
		t.Fatalf("影子决策动了持仓（前 %d 后 %d）", beforePos, afterPos)
	}
	fills, err := db.RealFills()
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 0 {
		t.Fatalf("影子决策造出了成交流水：%+v", fills)
	}
	// ③-b 没有控制器就不该凭空长出一个（熔断/在途/资金一律没被这条腿碰过）
	if ctrl := s.qmtCtrlFor(uid); ctrl != nil {
		t.Fatal("影子腿接上了执行控制器——这条链按设计不该有控盘入口")
	}
	// ③-c 落库只落在观测表里，且对照读数当场能认出"引擎同意"
	shadow, err := db.CarrierShadowRowsForDay(uid, day)
	if err != nil || len(shadow) != 1 {
		t.Fatalf("影子行应落 1 条，got %d err=%v", len(shadow), err)
	}
	view := s.carrierShadowReadout(uid, 3, carrierShadowNow)
	if !view.Known {
		t.Fatalf("读面应可读：%s", view.Reason)
	}
	if len(view.Kinds) != 1 || view.Kinds[0].EngineAgreed != 1 || view.Kinds[0].DaysWithData != 1 {
		t.Fatalf("同日同码的引擎定权快照必须被认成 agreed=1：%+v", view.Kinds)
	}
}

// dbSlice 小工具：把两种"读一本账"的调用收成同一个 len 断言姿势。
func dbSlice[T any](t *testing.T, fn func() ([]T, error)) []T {
	t.Helper()
	out, err := fn()
	if err != nil {
		t.Fatalf("读账失败: %v", err)
	}
	return out
}

// TestCarrierShadowReadoutThreeStates 锁 ④：四格三种零在同一个读数里各自可辨。
//
// 造法（全部按窗口真实算出来的日期，不硬编码，否则周末一来断言就散）：
//
//	· w[0] 今天：dragon 有命中且引擎同日同码有委托 ⇒ agreeing
//	· w[1]：dragon 有命中但引擎一条都不认        ⇒ diverged
//	· w[2]：只报 dragon（factor 类整天零命中）    ⇒ dragon=agreeing/diverged，factor=no_hits
//	· w[3]：整根腿一个字都没报                   ⇒ 两枚种类都是 no_report，且**不计入**任何窗口
func TestCarrierShadowReadoutThreeStates(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	uid := "u_states"
	window := carrierShadowTradingDays(carrierShadowNow, 10)
	if len(window) < 4 {
		t.Fatalf("窗口不足 4 天，用例无法造出四种格：%v", window)
	}
	today, y1, y2, y3 := window[0], window[1], window[2], window[3]

	// today：dragon 命中 1 + 引擎同日同码同方向有委托 ⇒ agreeing
	mustIngest(t, s, db, uid, today, []store.CarrierShadowDecision{
		carrierShadowRow(today, "600000.SH", "dragon", func(r *store.CarrierShadowDecision) {
			r.ShortCode = "DR600000"
		}),
	})
	mustOrder(t, db, uid, today, "600000.SH", "买入", "GW-T-1")

	// y1：dragon 命中 1，引擎什么都不认 ⇒ diverged
	mustIngest(t, s, db, uid, y1, []store.CarrierShadowDecision{
		carrierShadowRow(y1, "600519.SH", "dragon", func(r *store.CarrierShadowDecision) {
			r.ShortCode = "DR600519"
		}),
	})

	// y2：只报 dragon ⇒ 同一天里 factor_composite 必须是 no_hits（不是 no_report）
	mustIngest(t, s, db, uid, y2, []store.CarrierShadowDecision{
		carrierShadowRow(y2, "600000.SH", "dragon", func(r *store.CarrierShadowDecision) {
			r.ShortCode = "DR600002"
		}),
	})
	// factor_composite 这一类**只在 today 有行**：于是 y1/y2 两格应当是"盒子报了、这类零命中"
	// （no_hits），y3 那格是"盒子整根腿没报"（no_report）——同一份读数里三种零同时在场。
	mustIngest(t, s, db, uid, today, []store.CarrierShadowDecision{
		carrierShadowRow(today, "600000.SH", "factor_composite", func(r *store.CarrierShadowDecision) {
			r.ShortCode = "FC600000"
		}),
	})

	view := s.carrierShadowReadout(uid, 10, carrierShadowNow)
	if !view.Known {
		t.Fatalf("读面应可读：%s", view.Reason)
	}
	byKind := map[string]carrierShadowKind{}
	for _, k := range view.Kinds {
		byKind[k.Kind] = k
	}
	dragon, ok := byKind["dragon"]
	if !ok {
		t.Fatalf("dragon 这一类没出现在读数里：%+v", view.Kinds)
	}
	states := map[string]string{}
	for _, d := range dragon.Days {
		states[d.Day] = d.State
	}
	if states[today] != carrierShadowStateAgreeing {
		t.Errorf("今天应是 agreeing，实到 %q", states[today])
	}
	if states[y1] != carrierShadowStateDiverged {
		t.Errorf("y1 应是 diverged（有命中但引擎一条不认），实到 %q", states[y1])
	}
	if states[y2] != carrierShadowStateAgreeing && states[y2] != carrierShadowStateDiverged {
		t.Errorf("y2 的 dragon 有命中，必须落在 agreeing/diverged 之一，实到 %q", states[y2])
	}
	if states[y3] != carrierShadowStateNoReport {
		t.Errorf("y3 整根腿没报，必须判 no_report（不许算成有数据的一天），实到 %q", states[y3])
	}
	// 窗口分子只数"有命中"的天：today/y1/y2 三格，绝不能把 y3 那种静默日算进去；
	// 剩下的 10-3 天全部按 no_report 计数（"没报"与"报了但零命中"分家，见 ④ 的那条注释）。
	if dragon.DaysWithData != 3 || dragon.DaysNoReport != len(window)-3 {
		t.Fatalf("dragon 的窗口分子不对：with_data=%d no_report=%d（应为 3 / %d）",
			dragon.DaysWithData, dragon.DaysNoReport, len(window)-3)
	}
	if dragon.WindowFilled {
		t.Error("三天命中不该把 5 日窗口判满——WindowFilled 只能由**有命中**的天数推进")
	}

	// 同一份读数里的第二枚种类：它只在 today 命中，y1/y2 那两天**盒子报了别的种类**而它零命中
	// ⇒ 必须判 no_hits（真实的一"零"，计入窗口），y3 整根腿静默 ⇒ 必须 no_report（不计入）。
	// 这两格分开写就是本用例的全部意义：把 no_report 算成一天，5 日窗口会被静默日自己凑满；
	// 把 no_hits 算成 no_report，跑满的五天永远凑不出来。
	factor, ok := byKind["factor_composite"]
	if !ok {
		t.Fatalf("factor_composite 报了却在读数里消失（no_hits 被折成了没数据）")
	}
	fStates := map[string]string{}
	for _, d := range factor.Days {
		fStates[d.Day] = d.State
	}
	if fStates[today] == carrierShadowStateNoReport || fStates[today] == carrierShadowStateNoHits {
		t.Errorf("today 这一类有命中，不许折成 no_report/no_hits，实到 %q", fStates[today])
	}
	if fStates[y1] != carrierShadowStateNoHits {
		t.Errorf("y1 盒子报了 dragon、这一类零命中 ⇒ 必须 no_hits，实到 %q", fStates[y1])
	}
	if fStates[y2] != carrierShadowStateNoHits {
		t.Errorf("y2 同上 ⇒ 必须 no_hits，实到 %q", fStates[y2])
	}
	if fStates[y3] != carrierShadowStateNoReport {
		t.Errorf("y3 整根腿静默 ⇒ 必须 no_report，实到 %q", fStates[y3])
	}
	if factor.DaysWithData != 1 || factor.DaysNoHits != 2 ||
		factor.DaysNoReport != len(window)-3 {
		t.Errorf("这一类的三态计数不对（应 1 命中 / 2 零命中 / %d 静默）：%+v", len(window)-3, factor)
	}
	// 跨种类的"这天报过"分母必须同源：today 的上报行数＝两个种类之和。
	for _, d := range dragon.Days {
		if d.Day == today && d.ReportedRows != 2 {
			t.Errorf("today 的上报总行数应为 2（两种类各 1），实到 %d", d.ReportedRows)
		}
	}
	if view.DaysReported != 3 {
		t.Errorf("窗口内有上报的天数应为 3（today/y1/y2），实到 %d", view.DaysReported)
	}
}

// TestCarrierShadowReadoutUnknownIsNotEmpty 锁 ⑤：账本没接 / 身份缺失 ⇒ known=false + 原因，
// 且 kinds 必须是**空数组而不是 null**以外的"看起来像有数据"的形状——
// 前端按 known 走未知态，绝不许把这份读数渲染成"今天没有影子决策"。
func TestCarrierShadowReadoutUnknownIsNotEmpty(t *testing.T) {
	var bare Server
	view := bare.carrierShadowReadout("u_none", 10, carrierShadowNow)
	if view.Known || view.Reason == "" {
		t.Fatalf("没接账本时必须判未知并给原因，got known=%v reason=%q", view.Known, view.Reason)
	}
	if len(view.Kinds) != 0 {
		t.Fatalf("未知态不许塞任何种类（那会被读成测过了）：%+v", view.Kinds)
	}
	s, _ := newCarrierShadowServer(t)
	noUID := s.carrierShadowReadout("", 10, carrierShadowNow)
	if noUID.Known {
		t.Fatal("归属账号缺失时读面必须判未知")
	}
	// JSON 面：known=false 且 window/kinds 是空数组（null 会让前端 .map 当场炸）。
	raw, err := json.Marshal(noUID)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["known"] != false {
		t.Fatalf("known 必须是 false：%s", raw)
	}
	if _, ok := m["reason"].(string); !ok || m["reason"] == "" {
		t.Fatalf("未知态必须带原因：%s", raw)
	}
	for _, k := range []string{"window", "kinds"} {
		if arr, isArr := m[k].([]any); !isArr || len(arr) != 0 {
			t.Fatalf("%s 必须是空数组（不是 null，null 会炸前端）：%s", k, raw)
		}
	}
}

// TestCarrierShadowReadoutIgnoresYesterday 锁 ⑥：窗口按天分格，昨天的行不会算进今天那一格。
// 这一条在存储层也有同名锁；读面再钉一次是因为折叠可以发生在**任何**一层，
// 而这里折错直接把"过了一天"变成"过了两天"。
func TestCarrierShadowReadoutIgnoresYesterday(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	uid := "u_days"
	window := carrierShadowTradingDays(carrierShadowNow, 10)
	today, y1 := window[0], window[1]
	mustIngest(t, s, db, uid, y1, []store.CarrierShadowDecision{
		carrierShadowRow(y1, "600000.SH", "dragon", func(r *store.CarrierShadowDecision) {
			r.ShortCode = "DR600000"
		}),
	})
	view := s.carrierShadowReadout(uid, 10, carrierShadowNow)
	dragon := view.Kinds[0]
	seen := map[string]carrierShadowDay{}
	for _, d := range dragon.Days {
		seen[d.Day] = d
	}
	if seen[today].CarrierRows != 0 || seen[today].State != carrierShadowStateNoReport {
		t.Fatalf("今天没报，却被昨天的行填了一格：%+v", seen[today])
	}
	if seen[y1].CarrierRows != 1 {
		t.Fatalf("昨天那一格丢了：%+v", seen[y1])
	}
	if dragon.DaysWithData != 1 {
		t.Fatalf("窗口分子应为 1（只有昨天报了并命中），实到 %d", dragon.DaysWithData)
	}
}

// TestQMTStateCarriesCarrierShadow 接线锁：carrier_shadow 必须真的并进 GET /api/qmt/state，
// 且在"未接实盘"与"已接实盘"两条分支都在位（缺一条就是页面在某档看不见分子）。
func TestQMTStateCarriesCarrierShadow(t *testing.T) {
	s, db := newCarrierShadowServer(t)
	uid := "u_state"
	day := cntime.DayCompactOf(carrierShadowNow)
	mustIngest(t, s, db, uid, day, []store.CarrierShadowDecision{
		carrierShadowRow(day, "600000.SH", "dragon", func(r *store.CarrierShadowDecision) {
			r.ShortCode = "DR600000"
		}),
	})
	r := httptest.NewRequest(http.MethodGet, "/api/qmt/state?shadow_days=7", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: uid}))
	rec := httptest.NewRecorder()
	s.handleQMTState(rec, r)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("state 响应不是 JSON: %v", err)
	}
	view, ok := body["carrier_shadow"].(map[string]any)
	if !ok {
		t.Fatalf("carrier_shadow 没并进 /api/qmt/state：%s", rec.Body.String())
	}
	if view["known"] != true {
		t.Fatalf("已接账本时 known 应为 true：%v", view)
	}
	if int(view["required_days"].(float64)) != carrierShadowRequiredDays {
		t.Errorf("required_days 必须点名 5（计划文档的验收口径）：%v", view["required_days"])
	}
	if int(view["window_days"].(float64)) != 7 {
		t.Errorf("?shadow_days=7 没生效：%v", view["window_days"])
	}
	kinds, _ := view["kinds"].([]any)
	if len(kinds) != 1 {
		t.Fatalf("kinds 应有 1 枚：%v", view["kinds"])
	}
	first := kinds[0].(map[string]any)
	if first["kind"] != "dragon" || int(first["days_with_data"].(float64)) != 1 {
		t.Errorf("种类汇总不对：%v", first)
	}
}

// mustIngest 走内部入口灌一批影子行（造场景用；真实回报入口由上面的用例覆盖）。
func mustIngest(t *testing.T, s *Server, db *store.DB, uid, day string,
	rows []store.CarrierShadowDecision) {
	t.Helper()
	written, rejected, err := db.UpsertCarrierShadowRows(uid, carrierShadowNow.Format(time.RFC3339), rows)
	if err != nil {
		t.Fatalf("upsert shadow: %v", err)
	}
	if written != len(rows) {
		t.Fatalf("造的样本行没全落库：written=%d rejected=%v", written, rejected)
	}
}

// mustOrder 在引擎侧留一行**当日该账号**的委托（对照的另一把尺子）。
func mustOrder(t *testing.T, db *store.DB, uid, day, code, side, orderID string) {
	t.Helper()
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: orderID, SignalID: "sig-" + orderID, Code: code, Side: side, Status: "已成",
		Price: 10, Qty: 100, CreatedAt: dayCompactToDash(day) + " 09:35:00", UserID: uid,
	}); err != nil {
		t.Fatalf("upsert order: %v", err)
	}
}

// dayCompactToDash 把 YYYYMMDD 折成 "YYYY-MM-DD"（orders.created_at 的落库形态）。
func dayCompactToDash(day string) string {
	return day[:4] + "-" + day[4:6] + "-" + day[6:]
}
