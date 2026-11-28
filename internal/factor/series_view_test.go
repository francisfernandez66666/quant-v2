// series_view_test.go — §REPLAY-INPUTS 2026-11-28（档 1）：UpTo 截断视图的行为锁。
//
// 钉三件事：
//  1. **零拷贝**：视图与本体共享底层数组——这正是它便宜的原因（一次调用 O(列数)，
//     而 900 天 × 38 列的深拷贝在 390 万次调用下会把回放进程撑爆）。
//     便宜的另一面是"写视图＝写缓存"，所以本测试用一条**故意写坏**的用例把这条纪律钉住，
//     而不是假装没人会写。
//  2. **不静默丢列**：floatColumns 必须覆盖结构体里每一个 []float64 字段。
//     漏一列的表现是那列在 as-of 之后仍被读到（前视），而分数看着完全正常——
//     载体侧六条估值腿"永远 NaN 但读数无异常"就是这个形状（§FACTOR-FIX P3）。
//  3. 越界钳制（不 panic）。
//
// English: locks the zero-copy as-of view — shared backing arrays (with a deliberate
// mutation-visible case as the discipline), full column coverage, and clamping.
package factor

import (
	"math"
	"reflect"
	"testing"
)

func newFullSeries(n int) *StockSeries {
	s := &StockSeries{Dates: make([]string, n)}
	cols := s.floatColumns()
	for _, c := range cols {
		*c = make([]float64, n)
		for i := range *c {
			(*c)[i] = float64(i)
		}
	}
	for i := 0; i < n; i++ {
		s.Dates[i] = "d" + itoaTest(i)
	}
	return s
}

func itoaTest(v int) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}

func TestSeriesUpToClampsAndKeepsLength(t *testing.T) {
	s := newFullSeries(100)
	v := s.UpTo(49)
	if v.Len() != 50 || len(v.Dates) != 50 {
		t.Fatalf("视图长度应为 50，得 %d / %d", v.Len(), len(v.Dates))
	}
	if v.CloseHfq[49] != 49 {
		t.Fatalf("最后一格应为判定日本身，得 %v", v.CloseHfq[49])
	}
	// 越界钳制：超过长度按全长，不给 panic、也不给"多出来的一格"
	if got := s.UpTo(10000).Len(); got != 100 {
		t.Fatalf("越界应钳到 100，得 %d", got)
	}
	if got := s.UpTo(-1).Len(); got != 0 {
		t.Fatalf("负索引应钳到 0 长度，得 %d", got)
	}
	if (*StockSeries)(nil).UpTo(5) != nil {
		t.Fatal("nil 接收器必须返回 nil（回放一只票不许带走整轮）")
	}
}

func TestSeriesUpToSharesBackingArray(t *testing.T) {
	// 零拷贝的证据：改视图 [0] 会同时改到本体。这不是 bug 而是设计（否则一次回放要拷 28 GB），
	// 所以把它写成断言钉住：将来有人"顺手"改成深拷贝，这条会红，逼他重新看方案 §1.2 的理由。
	s := newFullSeries(20)
	v := s.UpTo(9)
	v.CloseHfq[0] = 999
	if s.CloseHfq[0] != 999 {
		t.Fatal("UpTo 必须是共享底层数组的视图（深拷贝会让回放每次调用多拷一整列）")
	}
}

func TestSeriesUpToCoversEveryFloatColumn(t *testing.T) {
	// 覆盖性：结构体里每一个 []float64 字段都必须在 floatColumns 清单上。
	// 漏一列＝那一列在 as-of 之后仍被读到（前视），而得分完全正常——静态与计数都抓不到。
	s := newFullSeries(10)
	for _, c := range s.floatColumns() {
		if c == nil || *c == nil {
			t.Fatal("floatColumns 不得含 nil 指针槽或未装配列（夹具已填满）")
		}
	}
	v := s.UpTo(4)
	sliceFields := 0
	rv := reflect.ValueOf(*v)
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if f.Type.Kind() != reflect.Slice || f.Type.Elem().Kind() != reflect.Float64 {
			continue
		}
		sliceFields++
		col := rv.Field(i)
		if col.IsNil() {
			continue // 未装配的列（nil）本来就跳过截断
		}
		if col.Len() != 5 {
			t.Fatalf("列 %s 在视图里长度=%d，应为 5（漏截＝前视泄漏）", f.Name, col.Len())
		}
	}
	if sliceFields == 0 {
		t.Fatal("反射没数到任何 []float64 列——结构变了，本用例要先跟着改")
	}
	if len(v.floatColumns()) != sliceFields {
		t.Fatalf("floatColumns 清单与结构体列数不符：清单 %d vs 结构 %d（新增列忘了登记？）",
			len(v.floatColumns()), sliceFields)
	}
}

func TestSeriesCloneColumnIsolation(t *testing.T) {
	s := newFullSeries(10)
	c := s.CloneColumn("CloseHfq")
	if len(c) != 10 {
		t.Fatalf("克隆长度应等于本体，得 %d", len(c))
	}
	c[0] = -1
	if s.CloseHfq[0] == -1 {
		t.Fatal("CloneColumn 必须深拷贝（它存在的意义就是『要改但不许碰缓存』）")
	}
	if s.CloneColumn("NoSuchColumn") != nil {
		t.Fatal("未知列名必须返回 nil，而不是造一个空切片骗调用方")
	}
	// 未装配的列（nil）返回 nil，调用方按弃权处理
	s2 := &StockSeries{Dates: []string{"a"}}
	if s2.CloneColumn("D1") != nil {
		t.Fatal("未装配列必须返回 nil")
	}
	s3 := newFullSeries(6)
	if v := math.Mod(s3.D1[0], 1); v != 0 {
		t.Fatalf("夹具本身应为整数，得 %v", v)
	}
}
