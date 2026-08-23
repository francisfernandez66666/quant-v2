package store

import (
	"path/filepath"
	"testing"
)

// TestRawBarsRouting 数据源路由行为：开关 on 读 ths_daily、off 读旧表——
// 同一代码两套价格可区分，断言路由真实生效（§HITHINK_DATA_SOURCE_PLAN C 阶段验收）。
func TestRawBarsRouting(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := setupRoutingFixture(db); err != nil {
		t.Fatal(err)
	}

	PrimarySourceThsDaily = true
	defer func() { PrimarySourceThsDaily = false }()

	bars, err := db.RawBars("000001.SZ", "20260801", "20260831")
	if err != nil || len(bars) != 2 {
		t.Fatalf("ths 路由应返回2根: %v n=%d", err, len(bars))
	}
	if bars[0].Close != 10.5 { // ths 价格，而非旧表 99
		t.Fatalf("应读到 ths_daily 价格: %+v", bars[0])
	}

	PrimarySourceThsDaily = false
	bars2, err := db.RawBars("000001.SZ", "20260801", "20260831")
	if err != nil || len(bars2) != 2 || bars2[0].Close != 99 {
		t.Fatalf("关闭路由应回退旧表: %v %+v", err, bars2[0])
	}

	// 无 ths 数据的代码：开路由也自动回退旧表（缺口回退语义）
	PrimarySourceThsDaily = true
	bars3, err := db.RawBars("600519.SH", "20260801", "20260831")
	if err != nil {
		t.Fatalf("无数据回退不应报错: %v", err)
	}
	_ = bars3 // 空切片即可
}

// TestHfqBarsGate 复权门禁：factors_ready=false 时 HfqBars 不走 ths 表（防口径混用）。
func TestHfqBarsGate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := setupRoutingFixture(db); err != nil {
		t.Fatal(err)
	}
	PrimarySourceThsDaily = true
	ThsFactorsReady = false
	defer func() { ThsFactorsReady = false }()

	hfq, err := db.HfqBars("000001.SZ", "20260801", "20260831")
	if err != nil || len(hfq) == 0 || hfq[0].Close != 99 {
		t.Fatalf("门禁关闭应走旧表: %v %+v", err, hfq[:1])
	}
	// 门禁开 → ths join（close×factor）
	ThsFactorsReady = true
	defer func() { ThsFactorsReady = false }()
	hfq2, err := db.HfqBars("000001.SZ", "20260801", "20260831")
	if err != nil || len(hfq2) == 0 {
		t.Fatalf("门禁开启读取失败: %v", err)
	}
	if hfq2[1].Close < 17 { // 11.8×1.5=17.7（两日均有因子）
		t.Fatalf("ths 因子 join 未生效: %+v", hfq2[1])
	}
}
