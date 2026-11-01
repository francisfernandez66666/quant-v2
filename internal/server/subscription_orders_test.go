// subscription_orders_test.go — §SUBSCRIPTION（2026-11-01 裁决 7）委托列表读面的行为锁。
//
// 核心是一条方向：**标注，绝不隐藏**。owner 要求申购行在委托列表里能看见——
// 那是他在券商 App 真实做过的动作，藏起来等于账上少一笔，而"看不见"从来不是消噪声的正当手段。
// 所以本文件的每条用例都在断"行数不许变少"，只在变少之外追加"标注字段"这一件事。
//
// 另外三条各自锁住一个真实风险：
//
//	① 开关关着 ⇒ 响应体与今天逐字节一致（不追加任何字段）——"这批要开关、不能直接上"的兑现点；
//	② 与正股同段（00/30）没有上市证据 ⇒ 一律不标注（裁决 8：数据不足走保留）；
//	③ 段表没命中的行 ⇒ 一次外源都不查（委托列表每翻一页都打 5500 只的全市场列表是不可接受的）。
package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// seedSubOrders 塞一批横跨四种形状的当日委托，返回 db 供读面对账。
func seedSubOrders(t *testing.T, s *Server, uid string) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	now := cntime.In(time.Now()).Format("2006-01-02T10:00:00+08:00")
	for _, o := range []store.RealOrder{
		{OrderID: "SB-1", SignalID: "buy:600000:dragon", Code: "600000.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100, CreatedAt: now, UserID: uid},
		{OrderID: "SB-2", SignalID: "ext:SB-2", Code: "370181.SZ", Side: "买入", Status: "未知(86)", Price: 0, Qty: 0, CreatedAt: now, UserID: uid},
		{OrderID: "SB-3", SignalID: "ext:SB-3", Code: "730899.SH", Side: "买入", Status: "已报", Price: 50, Qty: 1000, CreatedAt: now, UserID: uid},
		// 真实代码 + 0 值：0 值单独绝不构成标注
		{OrderID: "SB-4", SignalID: "buy:600519:dragon", Code: "600519.SH", Side: "买入", Status: "已报", Price: 0, Qty: 0, CreatedAt: now, UserID: uid},
		// 与正股同段：没有 IPO 日历证据 ⇒ 不标注（fail-safe 方向）
		{OrderID: "SB-5", SignalID: "ext:SB-5", Code: "002961.SZ", Side: "买入", Status: "已报", Price: 0, Qty: 0, CreatedAt: now, UserID: uid},
	} {
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed %s: %v", o.OrderID, err)
		}
	}
	return db
}

// setSubscriptionRecognize 通过既有设置页写面翻开关（不为新键开专用路由）。
func setSubscriptionRecognize(t *testing.T, s *Server, admin *auth.User, on bool) {
	t.Helper()
	body := `{"subscription_recognize":false}`
	if on {
		body = `{"subscription_recognize":true}`
	}
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt", body)); rr.Code != http.StatusOK {
		t.Fatalf("POST /api/config/qmt %s → %d body=%s", body, rr.Code, rr.Body.String())
	}
}

// getOrderRows 读委托列表并按 map 解析（要检查"字段在不在"，不能只解析成结构体）。
func getOrderRows(t *testing.T, s *Server, admin *auth.User) []map[string]any {
	t.Helper()
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/qmt/orders", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/qmt/orders → %d body=%s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("解析失败: %v body=%s", err, rr.Body.String())
	}
	return rows
}

// TestQMTOrdersMarksNotHides 开关开：五行全在（**一条都不许少**），申购段那两行被标注。
// 旧提案"隐藏申购行"会让本用例直接红——这就是那条裁决的可执行形态。
func TestQMTOrdersMarksNotHides(t *testing.T) {
	s, admin := newAdminTestServer(t)
	uid := admin.ID
	seedSubOrders(t, s, uid)
	setSubscriptionRecognize(t, s, admin, true)

	rows := getOrderRows(t, s, admin)
	if len(rows) != 5 {
		t.Fatalf("申购行被隐藏了：期望 5 行全可见, got %d（裁决 7：能看，不隐藏）", len(rows))
	}
	marked := map[string]string{}
	for _, r := range rows {
		if sub, ok := r["subscription"]; ok && sub == true {
			reason, _ := r["subscription_reason"].(string)
			marked[r["order_id"].(string)] = reason
		}
	}
	if marked["SB-2"] != trading.SubscriptionReasonOnlySegment {
		t.Fatalf("370181（深市新债申购，现网实录）应被标注 only-segment, got %+v", marked)
	}
	if marked["SB-3"] != trading.SubscriptionReasonOnlySegment {
		t.Fatalf("730899（沪市新股申购段）应被标注 only-segment, got %+v", marked)
	}
	for _, id := range []string{"SB-1", "SB-4", "SB-5"} {
		if _, bad := marked[id]; bad {
			t.Fatalf("%s 被误标成申购类：真实委托绝不许标注（SB-4 是 0 值真单、SB-5 是同段无证据）", id)
		}
	}
}

// TestQMTOrdersEqualsLegacyWhenSwitchOff 开关关 ⇒ 响应里**根本不出现** subscription 键，
// 且行数与今天一致。这条是"关＝与今天逐字节等价"的读面证据。
func TestQMTOrdersEqualsLegacyWhenSwitchOff(t *testing.T) {
	s, admin := newAdminTestServer(t)
	seedSubOrders(t, s, admin.ID)
	// 缺省即关（DefaultQMTConfig 的零值），不写任何配置

	rows := getOrderRows(t, s, admin)
	if len(rows) != 5 {
		t.Fatalf("关状态行数应仍为 5（隐藏形状不许复活）, got %d", len(rows))
	}
	for _, r := range rows {
		if _, exists := r["subscription"]; exists {
			t.Fatalf("开关关着却下发了 subscription 字段（order=%s）：关状态必须与今天逐字节一致", r["order_id"])
		}
		if _, exists := r["subscription_reason"]; exists {
			t.Fatal("开关关着却下发了 subscription_reason 字段：关状态不许改响应形状")
		}
	}
}

// TestQMTOrdersNoListingCallWhenSegmentMisses 全池都是非申购段 ⇒ 一次外源都不查。
// 委托列表是分页读面，若每行都去拉 5500 只全市场列表，就是把行情压力搬进委托读路径。
func TestQMTOrdersNoListingCallWhenSegmentMisses(t *testing.T) {
	s, admin := newAdminTestServer(t)
	db, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s.SetLiveDB(db)
	now := cntime.In(time.Now()).Format("2006-01-02T10:00:00+08:00")
	for i := 0; i < 20; i++ {
		o := store.RealOrder{
			OrderID: "NR-" + string(rune('a'+i)), SignalID: "buy:x" + string(rune('a'+i)),
			Code: "600519.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100,
			CreatedAt: now, UserID: admin.ID,
		}
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	setSubscriptionRecognize(t, s, admin, true)
	getOrderRows(t, s, admin)

	probe, ok := s.subscriptionSource().(*subListingProbe)
	if !ok {
		t.Fatal("问询腿类型不符（装配被改了？）")
	}
	probe.mu.Lock()
	refreshes := probe.refreshCount
	probe.mu.Unlock()
	if refreshes != 0 {
		t.Fatalf("段表未命中却查了 %d 次外源：判定顺序必须先是纯前缀比较", refreshes)
	}
}

// TestSubscriptionSwitchesRoundTripAndEcho 两个开关必须"写了能读回"（F-05 的教训：
// 只接受写入而不回显生效值，前端就会显示一个没人验证过的默认态，用户以为开了其实没开）。
func TestSubscriptionSwitchesRoundTripAndEcho(t *testing.T) {
	s, admin := newAdminTestServer(t)
	if rr := adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"subscription_recognize":true,"subscription_cancel_guard":true}`)); rr.Code != http.StatusOK {
		t.Fatalf("POST 开关 → %d body=%s", rr.Code, rr.Body.String())
	}
	rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET 解析失败: %v", err)
	}
	if resp["subscription_recognize"] != true {
		t.Fatalf("subscription_recognize 未回显生效值: %v", resp["subscription_recognize"])
	}
	if resp["subscription_cancel_guard"] != true {
		t.Fatalf("subscription_cancel_guard 未回显生效值: %v", resp["subscription_cancel_guard"])
	}
	// 显式关闭同样必须可读回（回滚只翻键、不动代码的前提）
	if rr = adminDo(s, adminReq(s, admin, http.MethodPost, "/api/config/qmt",
		`{"subscription_recognize":false}`)); rr.Code != http.StatusOK {
		t.Fatalf("关闭写入 → %d", rr.Code)
	}
	rr = adminDo(s, adminReq(s, admin, http.MethodGet, "/api/config/qmt", ""))
	resp = map[string]any{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET 解析失败: %v", err)
	}
	if resp["subscription_recognize"] != false {
		t.Fatalf("显式关闭未生效: %v", resp["subscription_recognize"])
	}
	if resp["subscription_cancel_guard"] != true {
		t.Fatalf("只改一个键时另一个键被抹平了（局部合并失守）: %v", resp["subscription_cancel_guard"])
	}
}

// TestSubListingProbeRequiresPositiveEvidence 上市状态问询腿的三态语义：
// 只有 IPO 日历的正面证据能说"未上市"；可交易列表里没有这个码**绝不构成**未上市证据。
func TestSubListingProbeRequiresPositiveEvidence(t *testing.T) {
	s, _ := newAdminTestServer(t)
	probe := &subListingProbe{s: s}
	// 手工装入两份数据（跳过外源刷新）：tradable 有 600000，ipo 明示 301099 未上市
	probe.tradable = map[string]bool{"600000": true}
	probe.ipo = map[string]data.IPOEvent{
		"301099": {Code: "301099", ListStatus: "U"},         // 日历明示未上市
		"001399": {Code: "001399", ListingDate: "20991231"}, // 上市日期晚于今天
	}
	probe.loadedAt = time.Now()
	if listed, known := probe.IsListed("600000.SH"); !listed || !known {
		t.Fatalf("出现在可交易列表里却不说已上市: listed=%v known=%v", listed, known)
	}
	// IPO 日历的正面证据才允许说"未上市"
	if listed, known := probe.IsListed("301099.SZ"); listed || !known {
		t.Fatalf("日历明示 U 应回 (false,true)，got (%v,%v)", listed, known)
	}
	if listed, known := probe.IsListed("001399.SZ"); listed || !known {
		t.Fatalf("上市日期晚于今天应回 (false,true)，got (%v,%v)", listed, known)
	}
	// 列表里没有、IPO 日历也没有 ⇒ 必须是"不知道"，而不是"未上市"
	if listed, known := probe.IsListed("002961.SZ"); known && !listed {
		t.Fatal("把「列表里没有」当成未上市：退市票也不在列表里，那是误标真实委托")
	}
	if _, known := probe.IsListed("002961.SZ"); known {
		t.Fatal("无任何正面证据时 known 必须为 false（裁决 8：数据不足 ⇒ 不判定）")
	}
}
