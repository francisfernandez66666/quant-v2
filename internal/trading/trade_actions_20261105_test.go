// trade_actions_20261105_test.go — §TRADE-CARD（2026-11-05）撤单动作快照与"卡＝自动交易数据"的锁。
//
// 三组事情，逐条都是本轮裁决的落形：
//  1. 动作快照的形态（同委托号覆盖、日切清空、封顶只丢最旧）；
//  2. 快照投影成卡上的 撤 行（一条委托一行、缺 order_id 不出行、窗口/被拒的紧迫度高于已发出）；
//  3. 过滤不许碰撤单行——战法开关只管买入方向，把一条撤单提示滤掉等于把那笔挂单留在市场上。
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// recAction 构造并记下一条撤单动作（测试辅助，走的是生产同一个入口）。
func recAction(c *Controller, oid, day, kind, source string, at time.Time) {
	c.recordSweepAction(SweepAction{OrderID: oid, SignalID: "buy:600000:n_shape:" + day,
		TsCode: "600000.SH", Side: SideBuy, Qty: 100, Kind: kind, Source: source,
		Reason: "测试留痕", At: at, Day: day})
}

// emptyCfg 一个只开 Enabled 的配置（本组不测下单闸，控制器只当动作快照的宿主）。
func emptyCfg() config.QMTConfig {
	return config.QMTConfig{Enabled: true}
}

// TestSweepActionOverwriteKeepsLatest 同一笔委托的两次动作按覆盖语义只留最新形态：
// 先 window（窗口内没发）后 requested（窗口一过就发出去）⇒ 卡上应当只剩 requested，
// 因为那才是此刻的事实；追加会让同一委托号在映射里互相遮蔽成"随机显示一条"。
func TestSweepActionOverwriteKeepsLatest(t *testing.T) {
	c := NewController(guardServer(), nil, "u_ta", emptyCfg(), nil)
	now := bjAt(9, 25)
	recAction(c, "GW-1", "20261028", SweepActionWindow, SweepSourceSignalGone, now)
	recAction(c, "GW-1", "20261028", SweepActionRequested, SweepSourceSignalGone, now.Add(5*time.Minute))

	acts := c.SweepActions()
	if len(acts) != 1 {
		t.Fatalf("同委托号应只留一条, got %d", len(acts))
	}
	if acts[0].Kind != SweepActionRequested {
		t.Fatalf("应保留最新形态 requested, got %s", acts[0].Kind)
	}
}

// TestSweepActionDayReset 跨日不复活：昨天的动作在今天第一批写入时整本换掉。
// 与 orders/t_rounds 的 day-keyed 口径一致——留痕跨日累积会让"今天撤了几笔"数不出去。
func TestSweepActionDayReset(t *testing.T) {
	c := NewController(guardServer(), nil, "u_ta", emptyCfg(), nil)
	recAction(c, "GW-Y", "20261027", SweepActionRequested, SweepSourceSweep, bjAt(10, 0))
	recAction(c, "GW-T", "20261028", SweepActionRequested, SweepSourceSweep, bjAt(10, 1))

	acts := c.SweepActions()
	if len(acts) != 1 || acts[0].OrderID != "GW-T" {
		t.Fatalf("日切后只应剩当日一条, got %+v", acts)
	}
}

// TestSweepActionCap 封顶时按时刻丢最旧，且刚写入的这一笔必须在（整本清空会把它也抹掉，
// 读数就反过来变成"什么都没发生"）。
func TestSweepActionCap(t *testing.T) {
	c := NewController(guardServer(), nil, "u_ta", emptyCfg(), nil)
	base := bjAt(9, 30)
	for i := 0; i < sweepActionLimit+20; i++ {
		recAction(c, oidOf(i), "20261028", SweepActionRequested, SweepSourceSweep, base.Add(time.Duration(i)*time.Second))
	}
	acts := c.SweepActions()
	if len(acts) != sweepActionLimit {
		t.Fatalf("应封顶在 %d 条, got %d", sweepActionLimit, len(acts))
	}
	latest := acts[0] // 倒序，第一条是最新
	if latest.OrderID != oidOf(sweepActionLimit+19) {
		t.Fatalf("最新一笔必须留下, got %s", latest.OrderID)
	}
}

// oidOf 生成第 i 笔的委托号（纯 ASCII，避免用例里出现中文键）。
func oidOf(i int) string {
	return "GW-CAP-" + digit(i)
}

// digit 把序号转成十进制字符串（不 import strconv，本文件只用这一处）。
func digit(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// TestCancelAdvicesProjectOneRowPerTicket 快照 → 卡上的撤行：一条委托一行、
// 形态与来源原样带上、没有委托号的动作不出行（指不到东西的提示宁可不显示）。
func TestCancelAdvicesProjectOneRowPerTicket(t *testing.T) {
	now := bjAt(10, 0)
	rows := cancelAdvices([]SweepAction{
		{OrderID: "GW-A", TsCode: "600000.SH", Side: SideBuy, Qty: 300, Kind: SweepActionRequested,
			Source: SweepSourceSignalGone, Reason: "信号消失即撤", At: now},
		{OrderID: "GW-B", TsCode: "600362.SH", Side: SideBuy, Qty: 100, Kind: SweepActionWindow,
			Source: SweepSourceSweep, Reason: "超时撤但撞上集合竞价", At: now},
		{OrderID: "  ", TsCode: "600519.SH", Kind: SweepActionRefused, At: now},
	})
	if len(rows) != 2 {
		t.Fatalf("两条有效 + 一条空委托号应得 2 行, got %d", len(rows))
	}
	byCode := map[string]PositionAdvice{}
	for _, r := range rows {
		byCode[r.Code] = r
	}
	a := byCode["600000"]
	if a.Action != adviceActionCancel || a.CancelKind != SweepActionRequested || a.CancelSource != SweepSourceSignalGone {
		t.Fatalf("撤行应原样携带形态与来源: %+v", a)
	}
	if a.OrderID != "GW-A" {
		t.Fatalf("撤行必须定位到委托号: %+v", a)
	}
	if a.Source != AdviceSourceCancel {
		t.Fatalf("撤行的来源必须是执行词表外的新值（防被自动卖出腿认成卖出建议）: %+v", a.Source)
	}
	if a.Qty != 0 {
		t.Fatalf("Qty 字段语义是持仓量，撤行不许塞委托量: %+v", a)
	}
	// 窗口/被拒＝这单还挂在柜台上 ⇒ 高；已发出＝等确认 ⇒ 中。
	if byCode["600000"].Level != "中" || byCode["600362"].Level != "高" {
		t.Fatalf("紧迫度应按'还挂着没有'分档: %+v", rows)
	}
}

// TestAdviseCancelRowsSurviveTacticsFilter 战法开关只滤加仓：撤单行即便无战法归属、且开关表里
// 根本没有那条战法，也必须留在屏上（滤掉一条撤单提示的方向是让那笔挂单继续留在市场上）。
func TestAdviseCancelRowsSurviveTacticsFilter(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	// 白名单非空＝只有表内战法有资格出买入建议；这里故意不含任何与撤行相关的名字。
	cfg.Strategies = []string{"momentum"}
	in := AdviceInput{
		Cfg: cfg,
		// 无持仓：Advise 的旧形态在这里直接 return nil，撤行必须照样出来。
		CancelActions: []SweepAction{{OrderID: "GW-A", TsCode: "600000.SH", Side: SideBuy, Qty: 300,
			Kind: SweepActionRequested, Source: SweepSourceSignalGone, At: bjAt(10, 0)}},
	}
	rows, flt := Advise(in)
	if len(rows) != 1 || rows[0].Action != adviceActionCancel {
		t.Fatalf("无持仓时撤行仍应出卡, got %+v", rows)
	}
	if flt.FilteredBySwitch != 0 {
		t.Fatalf("撤行不进战法过滤, got %d", flt.FilteredBySwitch)
	}
}

// TestAdviseCancelRowsLastInTheList 撤行排在列表**最后**：前端建议列是 ts_code→一行的映射
// （后写覆盖先写），同一只票既有早先算出的持仓建议、又有一笔刚被撤的单时，屏上应显示最新事实。
func TestAdviseCancelRowsLastInTheList(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	db := testDB(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 1000, CostPrice: 9, Amount: 10000, UserID: "u_ta"},
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	// 一条来自持仓的建议（任何非撤动作）+ 一条同码撤行：末尾必须是撤行。
	in := AdviceInput{
		Cfg:       cfg,
		Positions: mustPositions(t, db, "u_ta"),
		Quotes:    map[string]*data.StockInfo{"600000": {Price: 10.5, Name: "浦发"}},
		MarketAPI: nil,
	}
	in.CancelActions = []SweepAction{{OrderID: "GW-A", TsCode: "600000.SH", Side: SideBuy, Qty: 300,
		Kind: SweepActionRequested, Source: SweepSourceClose, At: bjAt(10, 0)}}
	rows, _ := Advise(in)
	if len(rows) < 2 {
		t.Fatalf("应同时有持仓建议与撤行, got %+v", rows)
	}
	last := rows[len(rows)-1]
	if last.Action != adviceActionCancel {
		t.Fatalf("撤行应在列表末尾（前端映射后写覆盖）, got %s", last.Action)
	}
}

// mustPositions 读回该账号持仓（Advise 的入参要的是 store 形态，不从测试里手搓字段名）。
func mustPositions(t *testing.T, db *store.DB, uid string) []store.RealPosition {
	t.Helper()
	ps, err := db.RealPositionsForUser(uid)
	if err != nil {
		t.Fatalf("读持仓失败: %v", err)
	}
	return ps
}
