// ths_pool_replace_test.go — §POOL-SNAPSHOT／§POOL-STAMP（2026-10-12）盘口整日替换与影子日判据。
//
// 这一批用例的存在理由：2026-10-12 现网涨停池里被写成 20261012 的那 72 行，与 20261009 逐字节相同
// （上游对没有数据的交易日不报错，而是把最近一个有数据的交易日原样兜回来）。旧写法有两个洞：
//  1. INSERT OR REPLACE 只覆盖本次回包里出现的股票，已出池的旧行留在同一天 ⇒ 池子只增不减；
//  2. 只看"请求了哪天"就盖章 trade_date ⇒ 幻影日静默落库，rc=0、行数正常、没有任何报错。
//
// 所以每条判据都配**反向腿**：拦停后库里必须还看得见旧数据（不是被删空），放行后必须真变。
package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// newPoolDB 建临时库（只建盘口三池，由 Open 的建表链完成）。
func newPoolDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// luRow 造一行涨停池（默认值都填可辨识的稳定值，用例只改关心的字段）。
func luRow(date, code, name string, price float64, cnt int, maxSeal float64) ThsLimitUpRow {
	return ThsLimitUpRow{TradeDate: date, TsCode: code, Name: name, Price: price,
		PctChg: 10.0, FirstSealTime: "09:35", ContinueCnt: cnt, ContinueText: "2天2板",
		SealMoney: 1e7, MaxSealMoney: maxSeal}
}

// TestReplaceThsLimitUpsRemovesStaleRows 整日替换的核心语义：第二批没有的股票必须从这一天消失。
// 反向腿＝旧的 UpsertThsLimitUps：同一批数据走 upsert，第一天那只会留在池子里（这正是缺陷本体）。
func TestReplaceThsLimitUpsRemovesStaleRows(t *testing.T) {
	db := newPoolDB(t)
	first := []ThsLimitUpRow{luRow("20261012", "600001.SH", "甲", 10, 1, 1e8),
		luRow("20261012", "600002.SH", "乙", 20, 2, 2e8)}
	if _, err := db.UpsertThsLimitUps(first); err != nil {
		t.Fatal(err)
	}
	// upsert 形态：第二批少一只，库里仍是两只 ⇒ 家数虚高（缺陷复现，同时证明下面那条不是自证）。
	second := []ThsLimitUpRow{luRow("20261012", "600001.SH", "甲", 10.5, 1, 1.2e8)}
	if n, err := db.UpsertThsLimitUps(second); err != nil || n != 1 {
		t.Fatalf("upsert 应写 1 行, got n=%d err=%v", n, err)
	}
	if cnt, _ := db.LimitUpCountOnDate("20261012"); cnt != 2 {
		t.Fatalf("upsert 形态下旧行应仍留在池中（缺陷本体），got %d", cnt)
	}
	// replace 形态：同一天整日替换 ⇒ 只剩本次回包那一只，且 deleted 读数为 2。
	res, err := db.ReplaceThsLimitUps(second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 2 || res.Written != 1 || res.TradeDate != "20261012" {
		t.Fatalf("替换读数应为 deleted=2 written=1, got %+v", res)
	}
	if cnt, _ := db.LimitUpCountOnDate("20261012"); cnt != 1 {
		t.Fatalf("整日替换后应只剩 1 行, got %d", cnt)
	}
	got, err := db.LimitUpsOnDate("20261012")
	if err != nil || len(got) != 1 || got[0].Price != 10.5 {
		t.Fatalf("应剩 600001.SH 且价格已更新, got %+v err=%v", got, err)
	}
	// 首次落库那一天的 deleted 必须是 0：分不清"池子缩了"与"根本没动"就是缺这条读数。
	fresh, err := db.ReplaceThsLimitUps([]ThsLimitUpRow{luRow("20261013", "600009.SH", "丙", 5, 1, 1e7)})
	if err != nil || fresh.Deleted != 0 || fresh.Written != 1 {
		t.Fatalf("新交易日首落应 deleted=0 written=1, got %+v err=%v", fresh, err)
	}
}

// TestReplaceThsLimitUpsAbortsBeforeDelete 三型拦停必须发生在 DELETE **之前**：库一个字节都不动。
// 这是整批用例里最贵的一条——空回包若先删后写，等于"上游这次没给数据"把一整天擦掉。
func TestReplaceThsLimitUpsAbortsBeforeDelete(t *testing.T) {
	db := newPoolDB(t)
	base := []ThsLimitUpRow{luRow("20261012", "600001.SH", "甲", 10, 1, 1e8),
		luRow("20261012", "600002.SH", "乙", 20, 2, 2e8)}
	if _, err := db.ReplaceThsLimitUps(base); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		rows []ThsLimitUpRow
		want error
	}{
		{"空回包", nil, ErrPoolSnapshotEmpty},
		{"缺 trade_date", []ThsLimitUpRow{luRow("", "600003.SH", "丁", 1, 1, 1)}, ErrPoolSnapshotNoDate},
		{"跨多交易日", []ThsLimitUpRow{luRow("20261012", "600003.SH", "丁", 1, 1, 1),
			luRow("20261013", "600004.SH", "戊", 1, 1, 1)}, ErrPoolSnapshotMultiDate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.ReplaceThsLimitUps(c.rows)
			if err == nil {
				t.Fatalf("%s 应被拦停", c.name)
			}
			if !strings.Contains(err.Error(), c.want.Error()) {
				t.Fatalf("错误应指向 %v, got %v", c.want, err)
			}
			// 拦停后的库面：原两行必须还在，且价格未被改写。
			got, err := db.LimitUpsOnDate("20261012")
			if err != nil || len(got) != 2 {
				t.Fatalf("拦停不得删数据, got %d 行 err=%v", len(got), err)
			}
			if cnt, _ := db.LimitUpCountOnDate("20261013"); cnt != 0 {
				t.Fatalf("跨日批次不得写进 20261013, got %d", cnt)
			}
		})
	}
}

// TestLimitUpShadowOfDetectsPhantomDay 影子日判据的四个态：相同／不同／此前无日／行数不同。
// 2026-10-12 现网那 72 行就是"相同"这一态；把"此前无日"误判成"相同"会让首日回填集体拦停，
// 把"不同"误判成"相同"则会把真数据当幻影扔掉——两个方向都得钉。
func TestLimitUpShadowOfDetectsPhantomDay(t *testing.T) {
	db := newPoolDB(t)
	yesterday := []ThsLimitUpRow{luRow("20261009", "600001.SH", "粤 传 媒", 10, 1, 1e8),
		luRow("20261009", "600002.SH", "乙", 20, 2, 2e8)}
	if _, err := db.ReplaceThsLimitUps(yesterday); err != nil {
		t.Fatal(err)
	}
	t.Run("逐行签名相同＝幻影日", func(t *testing.T) {
		// 名称写成 "粤传媒"（无空格）：上游两处空白形态必须同形，否则名字的空格差异会漏判幻影。
		same := []ThsLimitUpRow{luRow("20261012", "600001.SH", "粤传媒", 10, 1, 1e8),
			luRow("20261012", "600002.SH", "乙", 20, 2, 2e8)}
		shadow, isSame, err := db.LimitUpShadowOf("20261012", same)
		if err != nil || !isSame || shadow != "20261009" {
			t.Fatalf("应判 shadow_of=20261009 same=true, got shadow=%q same=%v err=%v", shadow, isSame, err)
		}
	})
	t.Run("价格变了＝不同一天（不误拦）", func(t *testing.T) {
		diff := []ThsLimitUpRow{luRow("20261012", "600001.SH", "粤传媒", 10.2, 1, 1e8),
			luRow("20261012", "600002.SH", "乙", 20, 2, 2e8)}
		shadow, isSame, err := db.LimitUpShadowOf("20261012", diff)
		if err != nil || isSame || shadow != "20261009" {
			t.Fatalf("真新数据不得判成幻影, got shadow=%q same=%v err=%v", shadow, isSame, err)
		}
	})
	t.Run("行数不同＝不同一天", func(t *testing.T) {
		extra := append(append([]ThsLimitUpRow{}, yesterday...), luRow("20261012", "600003.SH", "丙", 3, 1, 3e7))
		if _, isSame, err := db.LimitUpShadowOf("20261012", extra); err != nil || isSame {
			t.Fatalf("多一只就不能算相同, same=%v err=%v", isSame, err)
		}
	})
	t.Run("此前无日＝无从判定（不判相同也不报错）", func(t *testing.T) {
		shadow, isSame, err := db.LimitUpShadowOf("20260105",
			[]ThsLimitUpRow{luRow("20260105", "600007.SH", "庚", 1, 1, 1)})
		if err != nil || isSame || shadow != "" {
			t.Fatalf("无对照日应 shadow=\"\" same=false, got shadow=%q same=%v err=%v", shadow, isSame, err)
		}
	})
	t.Run("空行集不参与判定", func(t *testing.T) {
		if _, isSame, err := db.LimitUpShadowOf("20261012", nil); err != nil || isSame {
			t.Fatalf("空回包不得判成幻影, same=%v err=%v", isSame, err)
		}
	})
}

// TestPoolTextNormAndRowSig 两个比较用工具的边界：归一只去空白、签名不含盘中实时值 seal_money。
func TestPoolTextNormAndRowSig(t *testing.T) {
	if got := PoolTextNorm("粤 传\u00a0媒　X"); got != "粤传媒X" {
		t.Fatalf("PoolTextNorm 应去掉全部空白（含全角/不换行空格）, got %q", got)
	}
	if PoolTextNorm("A B") == "A B" {
		t.Fatal("比较形里不该留空格")
	}
	a := luRow("20261012", "600001.SH", "甲", 10, 1, 1e8)
	b := luRow("20261012", "600001.SH", "甲", 10, 1, 1e8)
	b.SealMoney = 9.9e7 // 盘中实时封单额：同一日两次抓取就会不同，进签名＝天天漏判
	b.TradeDate = "20260101"
	if LimitUpRowSig(a) != LimitUpRowSig(b) {
		t.Fatalf("签名不应含 seal_money/交易日期: %q vs %q", LimitUpRowSig(a), LimitUpRowSig(b))
	}
	c := b
	c.Name = " 甲 "
	if LimitUpRowSig(a) != LimitUpRowSig(c) {
		t.Fatal("签名里的名称必须走归一形")
	}
	d := c
	d.MaxSealMoney = 1e7
	if LimitUpRowSig(a) == LimitUpRowSig(d) {
		t.Fatal("峰值封单额变了就该算另一包（签名必须能区分）")
	}
}

// TestReplaceThsSimplePool 跌停/炸板池的整日替换与空回包拦停（与涨停池同一套三型）。
func TestReplaceThsSimplePool(t *testing.T) {
	db := newPoolDB(t)
	full := map[string]ThsPoolSimple{
		"600001.SH": {Name: "甲", Price: 3, PctChg: -10, OpenTimes: 0, TurnoverRatioPct: 1.5},
		"600002.SH": {Name: "乙", Price: 4, PctChg: -10.02, OpenTimes: 0},
	}
	res, err := db.ReplaceThsSimplePool("ths_limit_down_daily", "20261012", full)
	if err != nil || res.Deleted != 0 || res.Written != 2 {
		t.Fatalf("首落应 deleted=0 written=2, got %+v err=%v", res, err)
	}
	if _, err := db.ReplaceThsSimplePool("ths_limit_down_daily", "20261012",
		map[string]ThsPoolSimple{"600001.SH": {Name: "甲", Price: 3.1}}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SimplePoolCountOnDate("ths_limit_down_daily", "20261012")
	if err != nil || rows != 1 {
		t.Fatalf("整日替换后应只剩 1 只, got %d err=%v", rows, err)
	}
	// 空回包拦停后原数据必须在位（删空一整天＝把"没数据"写成"没有跌停"）。
	if _, err := db.ReplaceThsSimplePool("ths_limit_down_daily", "20261012", nil); err == nil {
		t.Fatal("空回包必须拦停")
	}
	if rows, _ := db.SimplePoolCountOnDate("ths_limit_down_daily", "20261012"); rows != 1 {
		t.Fatalf("拦停不得删数据, got %d", rows)
	}
	// 白名单外的表名必须拒绝——否则拼出来的 SQL 面就跟着命令行参数走。
	if _, err := db.ReplaceThsSimplePool("ths_limit_up_daily", "20261012", full); err == nil ||
		!strings.Contains(err.Error(), "白名单") {
		t.Fatalf("非白名单表应报错, got %v", err)
	}
	if _, err := db.SimplePoolCountOnDate("sqlite_master", "20261012"); err == nil {
		t.Fatal("行数读取腿同样要挡白名单外表名")
	}
	// 炸板池同一入口：多只股票、含开板次数与成交额。
	bres, err := db.ReplaceThsSimplePool("ths_break_pool_daily", "20261012", map[string]ThsPoolSimple{
		"600009.SH": {Name: "丙", Price: 9, PctChg: 5, OpenTimes: 3, Turnover: 5e8}})
	if err != nil || bres.Written != 1 {
		t.Fatalf("炸板池替换异常, got %+v err=%v", bres, err)
	}
	if rows, _ := db.BreakPoolOnDate("20261012"); len(rows) != 1 || rows[0].OpenTimes != 3 {
		t.Fatalf("炸板池读数异常, got %+v", rows)
	}
}
