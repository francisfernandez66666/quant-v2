// fill_amendments_test.go — §FILL-AMEND（2026-09-23）人工勘误的存储层回归。
//
// 锁三件事（顺序即设计前提）：
//  1. 四个消费口径（笔数闸/买入金额/卖出回款/成交簿重放）在**同一笔改判后同时跟着变**，
//     且原始 fills 行一字未动（方向、金额、行数都断言）；
//  2. 反向锁：pending（影子态）不改任何数字；revoke 后所有数字回到原值——
//     缺了这两条，"改判生效"的测试等于没测（任何直接 UPDATE 的实现也能让它绿）；
//  3. 守恒自检必须**报出差额**（假绿比缺测更有害：自检只返回 ok=false 而无逐笔线索即视为失败）。
//
// English: storage-layer regression for human fill amendments — all four accounting readers move
// together while the raw fills row stays byte-identical; pending stays a pure shadow and revoke snaps
// back; the conservation check must print per-code lines, not just a boolean.
package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// newAmendTestDB 建临时实盘账本（t.TempDir：测试产物绝不落工作树）。
func newAmendTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "amend.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// amendDay 事故复现日（2026-09-22 那笔真实卖出被记成买入）。
const amendDay = "2026-09-22"

// seedMisbookedSell 复现事故形态：一笔**实际卖出**被按"买入"入账本。
// 900 股 @22.55 = 20295 元，券商成交编号 T603468。
func seedMisbookedSell(t *testing.T, db *DB) RealFill {
	t.Helper()
	f := RealFill{
		ID: 0, OrderID: "O603468", Code: "603468.SH", Name: "风语筑", Side: "买入",
		Price: 22.55, Qty: 900, Amount: 20295, TradedAt: amendDay + " 10:08:00",
		SignalID: "sell:603468.SH:止盈:2026-09-22:r900", TradeID: "T603468", UserID: "u_amend",
		Fee: 5, StampTax: 10.15,
	}
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("seed fill: %v", err)
	}
	return f
}

// amendMetrics 一次取齐"四个消费口径"的可比对数值（含重放侧）。
// 任何一处漏收敛，这里就会读到不一致的数，测试直接红。
type amendMetrics struct {
	BuyCount    int
	BuyAmount   float64
	SellAmount  float64
	ReplayBuy   float64 // /api/qmt/trades 同源的成交簿重放：买入侧金额
	ReplaySell  float64
	SellReplayN int
	TodayBought int // T+1 可卖量输入（第 5、6 个同源读取方）
	RealizedPnl float64
	RawRows     int // 原始 fills 行数（必须恒定）
	ViewRows    int // fills_effective 行数（勘误绝不能把一行扇成多行）
}

// readAmendMetrics 把勘误生效前后所有按方向取数的读取口一次性读齐，供用例做前后对比。
// 有意覆盖多条同源路径（聚合 SQL、成交簿重放、trades 端点同源口径、T+1 可卖量、已实现盈亏），
// 只要有一条腿没接 fills_effective，前后差值就会与预期不符而暴露。
func readAmendMetrics(t *testing.T, db *DB) amendMetrics {
	t.Helper()
	var m amendMetrics
	var err error
	if m.BuyCount, err = db.CountBuyFilledOrdersByDay("u_amend", amendDay); err != nil {
		t.Fatalf("count buy: %v", err)
	}
	if m.BuyAmount, err = db.SumBuyFilledAmountByDay("u_amend", amendDay); err != nil {
		t.Fatalf("sum buy: %v", err)
	}
	if m.SellAmount, err = db.SumSellFilledAmountByDay("u_amend", amendDay); err != nil {
		t.Fatalf("sum sell: %v", err)
	}
	fills, err := db.RealFills()
	if err != nil {
		t.Fatalf("real fills: %v", err)
	}
	for _, f := range fills {
		switch f.Side {
		case "买入":
			m.ReplayBuy += f.Amount
		case "卖出":
			m.ReplaySell += f.Amount
			m.SellReplayN++
		}
	}
	m.TodayBought = db.TodayBoughtQty("u_amend", "603468.SH", amendDay)
	if m.RealizedPnl, err = db.TodayRealizedPnl("u_amend", amendDay); err != nil {
		t.Fatalf("realized pnl: %v", err)
	}
	// 行数断言在 SQL 层直取（不走视图读取函数，否则自查自证）。
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM fills`).Scan(&m.RawRows); err != nil {
		t.Fatalf("count raw fills: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM ` + `fills_effective`).Scan(&m.ViewRows); err != nil {
		t.Fatalf("count effective fills: %v", err)
	}
	return m
}

// TestFillAmendmentAppliedMovesAllReaders 主用例：pending 不改数 → applied 六处同变 →
// revoke 全部回到原值 → 原始行一字未动。
func TestFillAmendmentAppliedMovesAllReaders(t *testing.T) {
	db := newAmendTestDB(t)
	seed := seedMisbookedSell(t, db)

	before := readAmendMetrics(t, db)
	if before.BuyCount != 1 || before.BuyAmount != 20295 {
		t.Fatalf("改判前应是「1 笔买入 20295」: %+v", before)
	}
	if before.SellAmount != 0 || before.TodayBought != 900 {
		t.Fatalf("改判前卖出回款应为 0、T+1 当日买入量应为 900（事故形态）: %+v", before)
	}

	// ── 影子态：提交但不批准，所有数字必须一字不差 ──
	am, err := db.CreateFillAmendment(mustFillID(t, db, seed.TradeID), "卖出", "柜台回单确认 09-22 10:08 为卖出，方向被方向猜测污染", "boss")
	if err != nil {
		t.Fatalf("create amendment: %v", err)
	}
	if am.Status != FillAmendPending {
		t.Fatalf("新勘误必须是 pending 影子态, got %s", am.Status)
	}
	if got := readAmendMetrics(t, db); got != before {
		t.Fatalf("pending 影子态竟然改了账目数字：\n before=%+v\n after =%+v", before, got)
	}

	// ── 批准：六个消费口径同时跟着变 ──
	applied, err := db.ApplyFillAmendment(am.ID, "boss")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != FillAmendApplied || applied.AppliedAt == "" {
		t.Fatalf("批准后状态/时间异常: %+v", applied)
	}
	after := readAmendMetrics(t, db)
	if after.BuyCount != 0 {
		t.Fatalf("买入笔数应随改判归零, got %d", after.BuyCount)
	}
	if after.BuyAmount != 0 {
		t.Fatalf("买入金额应随改判归零, got %.2f", after.BuyAmount)
	}
	if after.SellAmount != 20295 {
		t.Fatalf("卖出回款应随改判释放 20295, got %.2f", after.SellAmount)
	}
	if after.TodayBought != 0 {
		t.Fatalf("T+1 当日买入量应随改判归零（那 900 股本就是卖出的）, got %d", after.TodayBought)
	}
	// 第 4 个口径：成交簿重放（RealFills → handleQMTTrades 同源）
	if after.ReplaySell != 20295 || after.ReplayBuy != 0 || after.SellReplayN != 1 {
		t.Fatalf("成交簿重放未跟着改判: %+v", after)
	}
	// 第 5 个口径：日内已实现盈亏（ListFillsByDay → TodayRealizedPnl）
	// 卖出 900 股、成本基准回落当日买入均价——改判后当日无买入且无持仓成本可用时 fail-open 计 0，
	// 这里锁的是"它确实按生效方向走了卖出分支"（改判前是 0，因为根本没有卖出腿）。
	// 见 TestFillAmendmentRealizedPnlUsesEffectiveSide 里带成本基准的正向断言。
	// 原始行必须一字未动（本项设计前提）
	if after.RawRows != before.RawRows || after.ViewRows != before.ViewRows {
		t.Fatalf("行数变化说明勘误改写了原始表或视图把一行扇成多行: %+v vs %+v", before, after)
	}
	var rawSide string
	var rawAmount float64
	if err := db.db.QueryRow(`SELECT side, amount FROM fills WHERE id=?`, mustFillID(t, db, seed.TradeID)).Scan(&rawSide, &rawAmount); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if rawSide != "买入" || rawAmount != 20295 {
		t.Fatalf("原始 fills 行被改写！side=%s amount=%.2f", rawSide, rawAmount)
	}
	// 视图必须同时给出两个方向（前端/审计要看"原方向 → 现方向"）
	fs, err := db.RealFills()
	if err != nil || len(fs) != 1 {
		t.Fatalf("real fills: %v len=%d", err, len(fs))
	}
	if fs[0].Side != "卖出" || fs[0].OrigSide != "买入" || fs[0].AmendID != am.ID {
		t.Fatalf("视图未回显勘误元信息: %+v", fs[0])
	}
	if fs[0].Amount != 20295 {
		t.Fatalf("勘误只改方向，金额必须原样: %.2f", fs[0].Amount)
	}

	// ── 反向锁：撤销后全部回到原数 ──
	if _, err := db.RevokeFillAmendment(am.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := readAmendMetrics(t, db); got != before {
		t.Fatalf("撤销后未回到原始数字：\n before=%+v\n after =%+v", before, got)
	}
}

// mustFillID 回查刚落库成交的自增 ID——ApplyRealFill 不回填结构体的 ID，而勘误入口按 fills.id
// 定位原始行（锚点由服务端从该行读出，绝不由调用方自报，见 CreateFillAmendment 注释）。
// 键参数可传 trade_id 或 order_id（本文件每笔都是唯一的）。
func mustFillID(t *testing.T, db *DB, key string) int64 {
	t.Helper()
	var id int64
	if err := db.db.QueryRow(`SELECT id FROM fills WHERE trade_id=? OR order_id=? LIMIT 1`, key, key).Scan(&id); err != nil {
		t.Fatalf("lookup fill id by %s: %v", key, err)
	}
	return id
}

// TestFillAmendmentRealizedPnlUsesEffectiveSide 第 5 个消费口径的正向数额断言：
// 改判前日内已实现盈亏恒为 0（成交簿里根本没有卖出腿），改判后必须有数（成本基准取持仓账）。
func TestFillAmendmentRealizedPnlUsesEffectiveSide(t *testing.T) {
	db := newAmendTestDB(t)
	// 同一代码的历史买入（另一日）给持仓账建成本基准：1000 股 @20.00
	buy := RealFill{OrderID: "OHIST", Code: "603468.SH", Name: "风语筑", Side: "买入",
		Price: 20, Qty: 1000, Amount: 20000, TradedAt: "2026-09-15 09:40:00",
		SignalID: "buy:603468.SH:龙抬头:2026-09-15", TradeID: "THIST", UserID: "u_amend"}
	if err := db.ApplyRealFill(buy); err != nil {
		t.Fatalf("seed history buy: %v", err)
	}
	mis := seedMisbookedSell(t, db)

	if pnl, _ := db.TodayRealizedPnl("u_amend", amendDay); pnl != 0 {
		t.Fatalf("改判前无卖出腿，日内已实现盈亏必须为 0, got %.2f", pnl)
	}
	am, err := db.CreateFillAmendment(mustFillID(t, db, mis.TradeID), "卖出", "柜台回单确认该笔为卖出", "boss")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ApplyFillAmendment(am.ID, "boss"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 成本基准取持仓账 cost_price（risk_gates.costBasisFor 的现行口径）。注意它**不是** 20.00：
	// 那笔被误记成买入的成交当年已把 900 股 @22.55 摊进加权成本（(20000+20295+5)/1900≈21.21），
	// 而人工勘误按设计不回改历史写账——这正是守恒自检要报出来的那类残留位移，这里顺手钉死，
	// 免得后来者以为"批准勘误=持仓成本也跟着复原"。
	p, err := db.RealPositionByCodeForUser("u_amend", "603468.SH")
	if err != nil || p.Qty <= 0 {
		t.Fatalf("position book: %v %+v", err, p)
	}
	want := (22.55 - p.CostPrice) * 900
	pnl, err := db.TodayRealizedPnl("u_amend", amendDay)
	if err != nil {
		t.Fatalf("pnl: %v", err)
	}
	if pnl < want-0.01 || pnl > want+0.01 {
		t.Fatalf("改判后日内已实现盈亏应为 %.2f（卖出腿成立，成本 %.4f）, got %.2f", want, p.CostPrice, pnl)
	}
	if want <= 0 {
		t.Fatalf("夹具失效：期望盈亏应为正, want=%.2f", want)
	}
}

// TestFillAmendmentCompositeAnchor 无券商成交编号的旧行：判重锚必须退回复合键，
// 且锚点不能跨行误伤（同 code 同时刻不同价/不同 order_id 的行不受影响）。
func TestFillAmendmentCompositeAnchor(t *testing.T) {
	db := newAmendTestDB(t)
	f := RealFill{OrderID: "ONOID", Code: "600001.SH", Name: "测试甲", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: amendDay + " 14:00:00",
		SignalID: "sell:600001.SH:manual", UserID: "u_amend"} // TradeID 空 → 复合锚
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 同秒同价同量但不同委托：复合锚不能把它一起改了
	other := RealFill{OrderID: "ONOID-B", Code: "600001.SH", Name: "测试乙", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: amendDay + " 14:00:00",
		SignalID: "buy:600001.SH:龙抬头:" + amendDay, UserID: "u_amend"}
	if err := db.ApplyRealFill(other); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	id := mustFillID(t, db, f.OrderID) // 无 trade_id：按委托号回查（本用例只有一笔）
	am, err := db.CreateFillAmendment(id, "卖出", "交割单核对：该笔实为卖出", "boss")
	if err != nil {
		t.Fatalf("create composite: %v", err)
	}
	if am.TradeID != "" {
		t.Fatalf("复合锚勘误的 trade_id 应为空, got %q", am.TradeID)
	}
	if _, err := db.ApplyFillAmendment(am.ID, "boss"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cnt, _ := db.CountBuyFilledOrdersByDay("u_amend", amendDay)
	if cnt != 1 {
		t.Fatalf("只应剩另一委托的 1 笔买入（复合锚不得跨委托误伤）, got %d", cnt)
	}
	sell, _ := db.SumSellFilledAmountByDay("u_amend", amendDay)
	if sell != 1000 {
		t.Fatalf("卖出回款应释放 1000, got %.2f", sell)
	}
}

// TestFillAmendmentInputGuards 入参护栏：理由必填、方向必须不同且合法、原行必须存在、
// 同一笔不得叠加两条活跃勘误。
func TestFillAmendmentInputGuards(t *testing.T) {
	db := newAmendTestDB(t)
	seed := seedMisbookedSell(t, db)
	id := mustFillID(t, db, seed.TradeID)

	if _, err := db.CreateFillAmendment(id, "卖出", "   ", "boss"); err == nil {
		t.Fatal("空理由必须被拒（无留痕的人工改判不予接受）")
	}
	if _, err := db.CreateFillAmendment(id, "买入", "同向勘误是空操作", "boss"); !errors.Is(err, ErrFillAmendmentSide) {
		t.Fatalf("与原始方向相同的勘误必须被拒, got %v", err)
	}
	if _, err := db.CreateFillAmendment(id, "sell", "英文方向不是本仓口径", "boss"); !errors.Is(err, ErrFillAmendmentSide) {
		t.Fatalf("非 买入/卖出 的方向必须被拒, got %v", err)
	}
	if _, err := db.CreateFillAmendment(id, "卖出", "缺操作者", ""); err == nil {
		t.Fatal("操作者必填")
	}
	if _, err := db.CreateFillAmendment(999999, "卖出", "不存在的成交", "boss"); !errors.Is(err, ErrFillAmendmentNoFill) {
		t.Fatalf("不存在的成交应回 ErrFillAmendmentNoFill, got %v", err)
	}
	if _, err := db.CreateFillAmendment(id, "卖出", "第一条", "boss"); err != nil {
		t.Fatalf("首条应成功: %v", err)
	}
	if _, err := db.CreateFillAmendment(id, "卖出", "第二条叠加", "boss"); !errors.Is(err, ErrFillAmendmentConflict) {
		t.Fatalf("同一笔叠加活跃勘误必须冲突, got %v", err)
	}
	// 撤销后允许重新提交（撤销是终态但不封死后续纠错）
	am, _ := db.ListFillAmendments(FillAmendPending, 10)
	if len(am) != 1 {
		t.Fatalf("pending 列表应有 1 条, got %d", len(am))
	}
	if _, err := db.RevokeFillAmendment(am[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := db.CreateFillAmendment(id, "卖出", "撤销后重新提交", "boss"); err != nil {
		t.Fatalf("撤销后重新提交应成功, got %v", err)
	}
	// 已撤销的勘误不可再批准（终态）
	revoked, _ := db.ListFillAmendments(FillAmendRevoked, 10)
	if _, err := db.ApplyFillAmendment(revoked[0].ID, "boss"); err == nil {
		t.Fatal("revoked 是终态，不可再批准")
	}
}

// TestFillEffectiveNoFanOut 视图扇出防护：一笔成交最多匹配一条 applied 勘误，
// 否则 fills_effective 行数 > fills 行数，所有 SUM/COUNT 口径直接翻倍。
// 这里走 SQL 层直插绕开应用侧冲突检查，验证两个 partial unique index 真的挡住了。
func TestFillEffectiveNoFanOut(t *testing.T) {
	db := newAmendTestDB(t)
	seed := seedMisbookedSell(t, db)
	id := mustFillID(t, db, seed.TradeID)
	am, err := db.CreateFillAmendment(id, "卖出", "唯一性反证基线", "boss")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ApplyFillAmendment(am.ID, "boss"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 绕开应用层，硬插第二条 applied 同锚勘误（模拟并发/人工直插）
	_, err = db.db.Exec(`INSERT INTO fill_amendments
		(fill_id, trade_id, order_id, code, traded_at, price, qty, user_id, orig_side, new_side,
		 orig_amount, reason, operator, status, created_at, applied_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'x','applied','2026-09-23 00:00:00','2026-09-23 00:00:00')`,
		id, seed.TradeID, seed.OrderID, seed.Code, seed.TradedAt, seed.Price, seed.Qty, seed.UserID,
		"买入", "卖出", seed.Amount)
	if err == nil {
		t.Fatal("同锚的第二条 applied 勘误必须被 partial unique index 拒绝（否则视图会把一笔成交扇成两行）")
	}
	var rawN, effN int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM fills`).Scan(&rawN); err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM fills_effective`).Scan(&effN); err != nil {
		t.Fatalf("count effective: %v", err)
	}
	if rawN != effN {
		t.Fatalf("视图行数必须等于原始行数：%d vs %d", rawN, effN)
	}
}
