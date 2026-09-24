// minute_klines_test.go — §MINUTE-K 分钟 K 落库层的定点测试（2026-09-24）。
//
// 这一张表存在的唯一理由是"让动量回放能用上实盘同尺寸的 5 分钟 MACD"，所以测试全部围绕
// 三条口径写死（见文件头）：
//  1. **主键幂等**：重复写同一 (ts_code, scale, ts) 只能有一行——回填与日增走同一条 upsert
//     路径，一旦重复，"当日 48 根"就会变成 96 根、MACD 口径静默改变；
//  2. **按日切片只吃 ts 前缀**：跨日的一根都不能混进来（右边界用 "day 24:00:00" 而不是
//     substr()，既为索引也为语义）；
//  3. **零行必须报零行**：空表/没这张表时 MinuteTableStats 返回 Rows=0 且**不报错**，
//     调用方据此区分"没数据"与"取数失败"——本仓反复出事的"降级报成功"就在这条边界上。
//
// 外加两条防线：主键三件套缺一项直接拒写；池表为空时 MinutePoolUniverse 如实报错，
// 绝不悄悄退化成全市场（2GB 量级的写入会藏在一次"例行回填"里）。
//
// English: pinned tests for the minute-bar store — primary-key idempotency, day slicing by ts
// prefix, honest zero-row reporting, PK rejection, and no silent fallback to the whole market.
package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newMinuteDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// mkBars 造某股某日的 count 根分钟线（ts 从 09:35 起每 5 分钟一根，收盘价递增可查顺序）。
// 48 根正好铺满一日（09:35..15:00 含午休前后两段，这里只要求"同日 48 个不同 ts"，
// 具体时刻不影响按日切片与主键幂等的判定）。
func mkBars(tsCode string, day string, count int) []MinuteBar {
	out := make([]MinuteBar, 0, count)
	for i := 0; i < count; i++ {
		total := 9*60 + 35 + 5*i
		ts := fmt.Sprintf("%s %02d:%02d:00", day, total/60, total%60)
		p := 10.0 + float64(i)*0.01
		out = append(out, MinuteBar{TsCode: tsCode, Scale: 5, Ts: ts,
			Open: p, High: p * 1.001, Low: p * 0.999, Close: p, Vol: 1000, Amount: p * 1000})
	}
	return out
}

func TestMinuteUpsertIdempotent(t *testing.T) {
	db := newMinuteDB(t)
	bars := mkBars("600000.SH", "2026-09-24", 48)
	if _, err := db.UpsertMinuteBars(bars); err != nil {
		t.Fatalf("首轮写入: %v", err)
	}
	if _, err := db.UpsertMinuteBars(bars); err != nil {
		t.Fatalf("重复写入（回填与日增同一路径）: %v", err)
	}
	st, err := db.MinuteTableStats(5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if st.Rows != 48 {
		t.Fatalf("重复写同一主键必须仍是 48 行，实得 %d（出现双行＝当日根数翻倍，MACD 口径静默改变）", st.Rows)
	}
	if st.Codes != 1 {
		t.Fatalf("去重股票数应为 1，实得 %d", st.Codes)
	}
	if st.AvgBars < 47.9 {
		t.Fatalf("平均每(票,日)根数应≈48，实得 %.2f", st.AvgBars)
	}
}

func TestMinuteDaySliceIsolation(t *testing.T) {
	db := newMinuteDB(t)
	all := append(mkBars("600000.SH", "2026-09-23", 3), mkBars("600000.SH", "2026-09-24", 48)...)
	all = append(all, mkBars("600000.SH", "2026-09-25", 2)...)
	// 另一周期（scale=15）也写同一天：按 scale 过滤，不许混进 5 分钟口径
	fifteen := mkBars("600000.SH", "2026-09-24", 16)
	for i := range fifteen {
		fifteen[i].Scale = 15
	}
	all = append(all, fifteen...)
	if _, err := db.UpsertMinuteBars(all); err != nil {
		t.Fatalf("写入: %v", err)
	}
	got, err := db.MinuteBarsByDay("600000.SH", 5, "2026-09-24")
	if err != nil {
		t.Fatalf("按日查询: %v", err)
	}
	if len(got) != 48 {
		t.Fatalf("2026-09-24 的 5 分钟应为 48 根，实得 %d（相邻日或别的周期漏进来了）", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ts <= got[i-1].Ts {
			t.Fatalf("第 %d 根未按 ts 升序：%s <= %s", i, got[i].Ts, got[i-1].Ts)
		}
	}
	if got[0].Ts < "2026-09-24" || got[len(got)-1].Ts >= "2026-09-25" {
		t.Fatalf("日切片越界：%s .. %s", got[0].Ts, got[len(got)-1].Ts)
	}
}

func TestMinuteEmptyTableReportsZeroNotError(t *testing.T) {
	db := newMinuteDB(t)
	st, err := db.MinuteTableStats(5)
	if err != nil {
		t.Fatalf("空表必须不报错（报错与零行是两件事）：%v", err)
	}
	if st.Rows != 0 || st.Codes != 0 {
		t.Fatalf("空表读数应为 0/0，实得 %+v", st)
	}
	if st.AvgBars != 0 {
		t.Fatalf("空表平均根数应为 0，实得 %.2f（除零会在这里冒出 NaN）", st.AvgBars)
	}
	// 空表按日查询也必须是"零根 + 无错"，回放据此判定退回日线近似
	if bars, err := db.MinuteBarsByDay("600000.SH", 5, "2026-09-24"); err != nil || len(bars) != 0 {
		t.Fatalf("空表按日查询应 0 根无错，实得 %d 根 err=%v", len(bars), err)
	}
}

func TestMinuteRejectsMissingPrimaryKey(t *testing.T) {
	db := newMinuteDB(t)
	cases := []struct {
		name string
		bar  MinuteBar
	}{
		{"缺 ts_code", MinuteBar{Scale: 5, Ts: "2026-09-24 09:35:00", Close: 10}},
		{"缺 ts", MinuteBar{TsCode: "600000.SH", Scale: 5, Close: 10}},
		{"scale<=0", MinuteBar{TsCode: "600000.SH", Scale: 0, Ts: "2026-09-24 09:35:00", Close: 10}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := db.UpsertMinuteBars([]MinuteBar{c.bar}); err == nil {
				t.Fatalf("%s：主键缺项必须拒写（否则该行的 ts 前缀切不出来，回放按日取数会瞎）", c.name)
			}
		})
	}
	// 空切片不是错误（幂等语义：这一轮没有东西要写）
	if n, err := db.UpsertMinuteBars(nil); err != nil || n != 0 {
		t.Fatalf("空切片应返回 0 无错，实得 n=%d err=%v", n, err)
	}
}

func TestMinutePoolUniverseNoSilentWholeMarket(t *testing.T) {
	db := newMinuteDB(t)
	// 池表一张数据都没有（全新库/池同步未跑）：清单必须是**空的**，绝不能退化成全市场
	// StockCodes()——那会把 2GB 量级的写入悄悄塞进一次"例行回填"。空清单由调用方判失败
	// （dataload minute-sync 的"取数清单为空"分支），这一层只负责不替调用方做主。
	if got, err := db.MinutePoolUniverse("20260624", 500); err != nil || len(got) != 0 {
		t.Fatalf("空池应返回空清单（不是全市场、也不是报错），实得 %d 只 err=%v", len(got), err)
	}
	// 有池数据：按出现次数降序、受 limit 截断
	if _, err := db.db.Exec(`INSERT INTO ths_limit_up_daily(ts_code,trade_date) VALUES
		('600111.SH','20260901'),('600111.SH','20260902'),('600111.SH','20260903'),
		('600222.SH','20260901'),('600222.SH','20260902'),
		('600333.SH','20260901')`); err != nil {
		t.Fatalf("seed 涨停池: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO ths_break_pool_daily(ts_code,trade_date) VALUES('600444.SH','20260905')`); err != nil {
		t.Fatalf("seed 炸板池: %v", err)
	}
	got, err := db.MinutePoolUniverse("20260901", 3)
	if err != nil {
		t.Fatalf("池查询: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("limit=3 应截断成 3 只，实得 %d（%v）", len(got), got)
	}
	if got[0] != "600111.SH" {
		t.Fatalf("出现次数最多的票必须排第一，实得 %v", got)
	}
	// 起点之前的票不进清单
	if later, err := db.MinutePoolUniverse("20260905", 10); err != nil || len(later) != 1 || later[0] != "600444.SH" {
		t.Fatalf("since 过滤失效：%v err=%v", later, err)
	}
}

func TestMinuteIncrementalListAndMaxTs(t *testing.T) {
	db := newMinuteDB(t)
	if _, err := db.UpsertMinuteBars(mkBars("600000.SH", "2026-09-24", 48)); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if _, err := db.UpsertMinuteBars(mkBars("000001.SZ", "2026-09-23", 5)); err != nil {
		t.Fatalf("写入: %v", err)
	}
	codes, err := db.MinuteCodes(5)
	if err != nil {
		t.Fatalf("codes: %v", err)
	}
	if len(codes) != 2 || codes[0] != "000001.SZ" || codes[1] != "600000.SH" {
		t.Fatalf("日增清单应为已存代码去重升序，实得 %v", codes)
	}
	// 别的周期必须是空的（清单自维护不能跨周期串台）
	if other, err := db.MinuteCodes(15); err != nil || len(other) != 0 {
		t.Fatalf("scale=15 不应有代码：%v err=%v", other, err)
	}
	minT, maxT, err := db.MinuteMaxTs("600000.SH", 5)
	if err != nil {
		t.Fatalf("maxts: %v", err)
	}
	if minT > maxT || maxT < "2026-09-24" {
		t.Fatalf("断点读数异常：%s ~ %s", minT, maxT)
	}
	if a, b, err := db.MinuteMaxTs("999999.SH", 5); err != nil || a != "" || b != "" {
		t.Fatalf("未存代码应返回空串：%q %q err=%v", a, b, err)
	}
}

// TestMinuteHasBarsGateProbe 调度器门控用的 O(1) 探针：空表 false、有行 true、
// **别的周期不算数**（只有 15 分钟行时 scale=5 必须仍报 false，否则夜间日增环会被"别的周期
// 的数据"放行，跑成一晚 0 行失败）。
func TestMinuteHasBarsGateProbe(t *testing.T) {
	db := newMinuteDB(t)
	if has, err := db.MinuteHasBars(5); err != nil || has {
		t.Fatalf("空表应报 false 且无错：has=%v err=%v", has, err)
	}
	if _, err := db.UpsertMinuteBars(mkBars("600000.SH", "2026-09-24", 3)); err != nil {
		t.Fatalf("写入: %v", err)
	}
	if has, err := db.MinuteHasBars(5); err != nil || !has {
		t.Fatalf("有行时应报 true：has=%v err=%v", has, err)
	}
	if has, err := db.MinuteHasBars(15); err != nil || has {
		t.Fatalf("scale=15 不该被 scale=5 的数据放行：has=%v err=%v", has, err)
	}
	// 反方向也要判住：只有"更大周期"时，小周期照样是空的——这一条专门打死 `scale>=?` 那种
	// 写成区间的门控（区间门控会让 5 分钟日增环被 15 分钟历史放行，跑成每晚 0 行失败）。
	if _, err := db.db.Exec(`INSERT INTO minute_klines(ts_code,scale,ts,open,high,low,close,vol,amount)
		VALUES('600000.SH',15,'2026-09-24 09:45:00',10,10,10,10,100,1000)`); err != nil {
		t.Fatalf("写入 scale=15: %v", err)
	}
	if has, err := db.MinuteHasBars(15); err != nil || !has {
		t.Fatalf("scale=15 有行应报 true：has=%v err=%v", has, err)
	}
	if has, err := db.MinuteHasBars(3); err != nil || has {
		t.Fatalf("scale=3 必须为 false（只有更大周期的行，门控不能按区间放行）：has=%v err=%v", has, err)
	}
}
