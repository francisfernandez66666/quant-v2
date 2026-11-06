// manual_book_test.go — §2.12 手动维护账的存储层行为锁（2026-11-04，外脑化 B1' 批）。
//
// 四条锁，按"最容易被改坏且不会自己报错"的顺序排：
//
//	① 幂等：同一份清单提交两次，行数与读数都不许多出来（重放长行＝账上凭空多一笔仓）；
//	② 全量语义：nil（本次没带持仓）与空清单（清仓）**必须不同义**——
//	   把"只改资金"读成"清空持仓"，一次提交就能把 owner 手上所有的票从账上抹掉；
//	③ 账号隔离：A 的手动账对 B 不可见，也绝不因为 user_id='' 的遗留兜底而漏出去；
//	④ 硬闸隔离（本批主题）：手动读数**不会**出现在 real_positions / real_account 里——
//	   §M12-A 资金三态、risk.Gate 的 T+1 可卖量、集中度与买入纪律全部只读那两张表，
//	   手动行进不去，就解不开任何"必须新鲜"的 fail-close。这一条不是"设计说明"，
//	   而是下面 TestManualBookNeverReachesHardGates 里逐张表数出来的行数。
package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/cntime"
)

// manualTestDB 独立临时库（不复用包内已有的 testDB/newTestDB 夹具名，避免与并行批次撞名）。
func manualTestDB(t *testing.T) *DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manual.db")
	db, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close(); _ = os.Remove(p) })
	return db
}

func mustAsof(t *testing.T, s string) string {
	t.Helper()
	v, err := NormalizeManualAsOf(s)
	if err != nil {
		t.Fatalf("NormalizeManualAsOf(%q): %v", s, err)
	}
	return v
}

// TestManualPositionsIdempotent ①：同清单重放不长行、读数不变。
func TestManualPositionsIdempotent(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_manual"
	asof := mustAsof(t, "2026-11-04 09:30:00")
	rows := []ManualPosition{
		{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 8.5},
		{TsCode: "000001.SZ", Name: "平安银行", Qty: 500, CostPrice: 11.2},
	}
	w1, d1, err := db.PutManualPositions(uid, rows, asof)
	if err != nil || w1 != 2 || d1 != 0 {
		t.Fatalf("首次写入应 2 行 0 删，得 w=%d d=%d err=%v", w1, d1, err)
	}
	w2, d2, err := db.PutManualPositions(uid, rows, asof)
	if err != nil || w2 != 2 || d2 != 0 {
		t.Fatalf("重放应仍是 2 行 0 删，得 w=%d d=%d err=%v", w2, d2, err)
	}
	got, err := db.ManualPositionsForUser(uid)
	if err != nil || len(got) != 2 {
		t.Fatalf("重放后必须还是 2 行，得 n=%d err=%v", len(got), err)
	}
	byCode := map[string]ManualPosition{}
	for _, p := range got {
		byCode[p.TsCode] = p
	}
	if v := byCode["600000.SH"]; v.Qty != 1000 || v.CostPrice != 8.5 || v.AsOf != asof {
		t.Fatalf("读数漂移：%+v", v)
	}
	// 来源标记由表写死，调用方无法伪造成别的值
	for _, p := range got {
		if p.Source != ManualSourceLabel {
			t.Fatalf("source 必须是 %q，得 %q", ManualSourceLabel, p.Source)
		}
	}
}

// TestManualPositionsNilVsEmpty ②：nil＝不动持仓，空清单＝清仓。
func TestManualPositionsNilVsEmpty(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_nil_or_empty"
	asof := mustAsof(t, "")
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600519.SH", Qty: 100}}, asof); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 只改资金：positions 传 nil ⇒ 持仓一行都不许动
	if w, d, err := db.PutManualPositions(uid, nil, asof); err != nil || w != 0 || d != 0 {
		t.Fatalf("nil 清单应零写零删，得 w=%d d=%d err=%v", w, d, err)
	}
	if got, _ := db.ManualPositionsForUser(uid); len(got) != 1 {
		t.Fatalf("nil 清单把持仓改动了（应为全量语义下的「本次不动」）：%+v", got)
	}
	// 显式空清单 ⇒ 清仓
	if _, d, err := db.PutManualPositions(uid, []ManualPosition{}, asof); err != nil || d != 1 {
		t.Fatalf("空清单应清掉 1 行，得 deleted=%d err=%v", d, err)
	}
	if got, _ := db.ManualPositionsForUser(uid); len(got) != 0 {
		t.Fatalf("空清单后仍有行：%+v", got)
	}
}

// TestManualPositionsRejectsBadBatch 整批拒收：任一行非法 ⇒ 一条都不写（§F2 同族姿势）。
func TestManualPositionsRejectsBadBatch(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_bad_batch"
	asof := mustAsof(t, "")
	good := ManualPosition{TsCode: "600000.SH", Qty: 100}
	for name, rows := range map[string][]ManualPosition{
		"非法代码（缺交易所后缀）": {{TsCode: "600000", Qty: 100}},
		"指数代码混进个股账":    {{TsCode: "000001", Qty: 100}}, // 与 realTsCodeRe 同判据：裸 6 位码一律拒
		"数量为零":         {{TsCode: "600000.SH", Qty: 0}},
		"数量为负":         {{TsCode: "600000.SH", Qty: -100}},
		"成本为负":         {{TsCode: "600000.SH", Qty: 100, CostPrice: -1}},
		"同一份清单里重复代码":   {{TsCode: "600000.SH", Qty: 100}, {TsCode: "600000.SH", Qty: 200}},
		"好坏混合（也必须整批拒）": {good, {TsCode: "bad", Qty: 100}},
	} {
		if _, _, err := db.PutManualPositions(uid, rows, asof); !errors.Is(err, ErrInvalidManualPosition) {
			t.Fatalf("%s：应返回 ErrInvalidManualPosition，得 %v", name, err)
		}
	}
	// 关键的一条：上面每一次失败都**不许留下半批**。"好坏混合"那次尤其——
	// 如果第 1 行先写了、第 2 行才报错，账上就凭空多出一笔从没有人确认过的持仓。
	if got, _ := db.ManualPositionsForUser(uid); len(got) != 0 {
		t.Fatalf("校验失败后不许有任何行：%+v", got)
	}
}

// TestManualBookAccountScoping ③：账号隔离，且不做 user_id=” 的遗留兜底。
func TestManualBookAccountScoping(t *testing.T) {
	db := manualTestDB(t)
	asof := mustAsof(t, "")
	if _, _, err := db.PutManualPositions("u_a", []ManualPosition{{TsCode: "600000.SH", Qty: 100}}, asof); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := db.PutManualAccount("u_a", 8888, true, 0, asof); err != nil {
		t.Fatalf("seed acc a: %v", err)
	}
	if got, _ := db.ManualPositionsForUser("u_b"); len(got) != 0 {
		t.Fatalf("u_b 读到了 u_a 的手动持仓：%+v", got)
	}
	st, err := db.ManualAccountForUser("u_b")
	if err != nil || st.Present {
		t.Fatalf("u_b 读到了别人的手动资金行：present=%v err=%v", st.Present, err)
	}
}

// TestManualAccountThreeStates 资金三态：没有行 / 有行但没填 / 填了真 0，三者必须可分。
// 这条锁的由来是 §M12-A 的 H-4 事故形态（"未知"与"真 0"折成一个数，要么全拦要么全放）。
func TestManualAccountThreeStates(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_state"
	asof := mustAsof(t, "")
	if st, err := db.ManualAccountForUser(uid); err != nil || st.Present || st.Filled {
		t.Fatalf("没有行时应 Present=false： %+v err=%v", st, err)
	}
	if err := db.PutManualAccount(uid, 0, false, 0, asof); err != nil {
		t.Fatalf("put unfilled: %v", err)
	}
	if st, _ := db.ManualAccountForUser(uid); !st.Present || st.Filled {
		t.Fatalf("有行但没填时应 Present=true/Filled=false：%+v", st)
	}
	if err := db.PutManualAccount(uid, 0, true, 0, asof); err != nil {
		t.Fatalf("put zero: %v", err)
	}
	st, _ := db.ManualAccountForUser(uid)
	if !st.Present || !st.Filled || st.Account.AvailableCash != 0 {
		t.Fatalf("填了真 0 应是 Present=true/Filled=true/0：%+v", st)
	}
	if as, err := db.ManualAsOf(uid); err != nil || as == "" {
		t.Fatalf("ManualAsOf 应返回最近时刻")
	}
}

// TestManualBookNeverReachesCounterTables ④：手动值**永不写进柜台输入表**（裁决 1 改判后仍成立）。
// 别把这条读回旧口径：现在手动值会经 resolver 显式选源**参与**买卖硬闸，只是绝不落到 real_*
// 两张表——柜台账只由回报与真实成交推进，是「柜台为准」能被机器判出来的前提。
// 函数末尾另有正锁（ManualBookViewFor 必须把当日手动账判为可用），防「不写回」被做成「不参与」。
//
// 逐张数行数而不是"看代码没写那张表"：这张锁要在有人真去给 real_positions/real_account 加写入路径时
// 当场变红。三张输入表分别是——
//   - real_account：§M12-A 资金三态（Controller.AvailableCash）与 risk.Gate 的可用资金；
//   - real_positions：T+1 可卖量 checkT1Sellable / 集中度 / BuyableQtyForUserSell；
//   - orders：VWAP 硬过滤 fail-close 与收盘对账读的委托簿。
func TestManualBookNeverReachesCounterTables(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_gate"
	code := "600111.SH"
	asof := mustAsof(t, "")
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: code, Name: "手工仓", Qty: 1000, CostPrice: 9.9}}, asof); err != nil {
		t.Fatalf("put manual: %v", err)
	}
	if err := db.PutManualAccount(uid, 999999, true, 999999, asof); err != nil {
		t.Fatalf("put cash: %v", err)
	}

	// 柜台账：三张输入表一行都不许多
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(1) FROM real_positions WHERE user_id=? OR user_id=''`, uid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("手动账写进了 real_positions（n=%d err=%v）——柜台为准这条判据从此失去依据", n, err)
	}
	// real_account 是惰性建表（ensureRealAccountTable），这里按"该表要么不存在、要么没有本账号行"判：
	// 用 store 自己的读口径而不是裸 SQL，否则新建库里那张表还没建出来，锁会红在无关的 DDL 上。
	{
		accRow, aerr := db.GetRealAccountScoped(uid, false)
		if aerr != nil || accRow.UpdatedAt != "" || accRow.AvailableCash != 0 {
			t.Fatalf("手动账写进了 real_account（可用资金被当成柜台回报）：%+v err=%v", accRow, aerr)
		}
	}
	if err := db.db.QueryRow(`SELECT COUNT(1) FROM orders WHERE user_id=?`, uid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("手动账写进了 orders（n=%d err=%v）", n, err)
	}

	// 柜台输入表的三个实际取数口：读到的一定是"没有"（不写回 ≠ 不参与判定）
	acc, err := db.GetRealAccountOwn(uid)
	if err != nil || acc.AvailableCash != 0 || acc.UpdatedAt != "" {
		t.Fatalf("GetRealAccountOwn 应仍是零值行（§M12-A 必须判成口径不可得）：%+v err=%v", acc, err)
	}
	if p, err := db.RealPositionByCodeForUser(uid, code); err == nil && p.Qty != 0 {
		t.Fatalf("柜台账读到了手动持仓：%+v", p)
	}
	if q := db.BuyableQtyForUserSell(uid, code, cntime.DayOf(cntime.Now())); q != 0 {
		t.Fatalf("手动持仓凭空造出可卖量：%d", q)
	}
	// 手动账自己那侧读得到（否则上面的"没写进柜台表"只是"什么都没写"的假绿）
	if mp, err := db.ManualPositionsForUser(uid); err != nil || len(mp) != 1 || mp[0].Qty != 1000 {
		t.Fatalf("手动账自身应读得到这一行：%+v err=%v", mp, err)
	}
	if st, _ := db.ManualAccountForUser(uid); !st.Filled || st.Account.AvailableCash != 999999 {
		t.Fatalf("手动资金应读得到：%+v", st)
	}

	// 正锁（owner 2026-11-06 裁决 1）：不写回柜台表，但读侧选源必须把当日手动账判为可用，
	// 否则「喂买卖硬闸」只是文档里的一句话。engine 侧 resolver 的端到端锁见 manual_book_source_test.go。
	if v, verr := db.ManualBookViewFor(uid, cntime.DayOf(cntime.Now())); verr != nil || !v.CashUsable || !v.PositionsUsable {
		t.Fatalf("当日手动账应经 resolver 选源参与硬闸（asof 今日 + 未被接管）：%+v err=%v", v, verr)
	}
}

// TestNormalizeManualAsOf 时刻口径：空串缺省为服务端当前时刻；认不出的**拒**，不悄悄换成"现在"。
func TestNormalizeManualAsOf(t *testing.T) {
	if v, err := NormalizeManualAsOf(""); err != nil || len(v) != 19 {
		t.Fatalf("空串应归一成当前时刻，得 %q err=%v", v, err)
	}
	if v, err := NormalizeManualAsOf("2026-11-04T09:30:00+08:00"); err != nil || v != "2026-11-04 09:30:00" {
		t.Fatalf("RFC3339 应归一为墙钟串，得 %q err=%v", v, err)
	}
	if _, err := NormalizeManualAsOf("11/04/2026"); !errors.Is(err, ErrInvalidManualPosition) {
		t.Fatalf("无法识别的时刻必须拒收，得 %v", err)
	}
}
