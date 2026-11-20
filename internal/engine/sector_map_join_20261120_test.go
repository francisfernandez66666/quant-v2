// sector_map_join_20261120_test.go — §FLOW-SECTOR-JOIN 云端逐票板块腿的行为锁。
//
// 现场（2026-11-20）：云端的「个股 → 板块 delta」解析写的是 `GetStockIndustry(code)`，
// 它直连东财 `push2/api/qt/stock/get?fields=f127`；而广州对东财全族在 TLS 层被 RST
// （11-19 / 11-20 两次实测 Empty reply）⇒ 名字恒空 ⇒ 这条腿恒弃权，且**没有任何报错**
// （blend 静默 renormalize 到「大盘+量能」两项）。
//
// 修法：连接键优先取生产者产出的 `sector_map`（东财板块名，与 secMap 的键同源），
// 只有表里没有这只票才退回东财直连。三件事必须同时成立，缺一条这条腿就还是死的：
//
//	① 本地表命中时**必须用本地表**（退回 f127 就等于没修）；
//	② 未装配/跨日时**不许崩也不许猜**，退回原路径（零回归）；
//	③ 板块 delta map 缺失/跨日 ⇒ 弃权（绝不拿昨天的 delta 当今天）。
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// joinFix 造一个只有板块 delta 相关状态的最小引擎（其余字段留零值：本用例只走这一条腿）。
type joinFix struct {
	t   *testing.T
	e   *Engine
	day string // 北京今日（YYYYMMDD，生产者的日期口径）
}

func newJoinFix(t *testing.T, mapping map[string]string, fileDay string) *joinFix {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sector_map.json")
	doc := map[string]interface{}{"day": fileDay, "sector_map": mapping}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	e := &Engine{}
	e.SetSectorMap(data.NewSectorMapCache(path))
	e.flowMoneyMu.Lock()
	e.sectorDeltaDay = cntime.DayOf(time.Now())
	e.sectorDeltaMap = map[string]float64{
		"航空机场": -0.5,
		"酿酒行业": 0.25,
		// 证监会门类：故意给一个"看起来也对"的值，用来证明连接键没有走错口径
		"C39计算机、通信和其他电子设备制造业": 0.9,
	}
	e.flowMoneyMu.Unlock()
	return &joinFix{t: t, e: e, day: cntime.DayCompactOf(time.Now())}
}

// TestSectorDeltaUsesLocalMapNotIndustryFn 本地表命中时必须以本地表为准。
func TestSectorDeltaUsesLocalMapNotIndustryFn(t *testing.T) {
	f := newJoinFix(t, map[string]string{"600029": "航空机场"}, cntime.DayCompactOf(time.Now()))
	// 东财直连返回的是证监会门类（现网 `stocks.industry` 同源）；如果这条腿走了它，
	// 就会拿到 0.9 而不是 -0.5 —— 那也正是"看起来接上了、命中率恒 0"的形状。
	f.e.industryFn = func(code string) string { return "C39计算机、通信和其他电子设备制造业" }

	got, ok := f.e.sectorDeltaFor("600029")
	if !ok {
		t.Fatal("本地表命中时不该弃权")
	}
	if got != -0.5 {
		t.Fatalf("板块 delta 走了错误口径: got %v want -0.5（说明用了 industryFn 而不是 sector_map）", got)
	}
	// 带后缀的 ts_code 也必须命中同一条（连接键归一）。
	if got, ok := f.e.sectorDeltaFor("600029.SH"); !ok || got != -0.5 {
		t.Fatalf("带后缀代码未归一: got %v ok=%v", got, ok)
	}
}

// TestSectorDeltaFallsBackToIndustryFn 表里没有这只票 ⇒ 退回原路径（有直连的部署零回归）。
func TestSectorDeltaFallsBackToIndustryFn(t *testing.T) {
	f := newJoinFix(t, map[string]string{"600029": "航空机场"}, cntime.DayCompactOf(time.Now()))
	f.e.industryFn = func(code string) string { return "C39计算机、通信和其他电子设备制造业" }
	got, ok := f.e.sectorDeltaFor("600519")
	if !ok || got != 0.9 {
		t.Fatalf("表里没有的票应退回 industryFn: got %v ok=%v", got, ok)
	}
}

// TestSectorDeltaWithoutMapIsTheOldBehaviour 未装配 ⇒ 行为与修之前完全一致（含弃权）。
func TestSectorDeltaWithoutMapIsTheOldBehaviour(t *testing.T) {
	f := newJoinFix(t, map[string]string{"600029": "航空机场"}, cntime.DayCompactOf(time.Now()))
	f.e.SetSectorMap(nil)
	f.e.industryFn = func(code string) string { return "C39计算机、通信和其他电子设备制造业" }
	if got, ok := f.e.sectorDeltaFor("600029"); !ok || got != 0.9 {
		t.Fatalf("未装配时应走原路径: got %v ok=%v", got, ok)
	}
	// 两边都答不出 ⇒ 弃权（绝不返回 0 当"中性"）。
	f.e.industryFn = func(code string) string { return "" }
	if _, ok := f.e.sectorDeltaFor("600029"); ok {
		t.Fatal("两边都没有名字时必须弃权，不能返回 0 冒充中性")
	}
}

// TestSectorDeltaStaleMapDoesNotLeak 昨日表 + 今日 delta map ⇒ 必须弃权（不许跨日混用）。
func TestSectorDeltaStaleMapDoesNotLeak(t *testing.T) {
	yesterday := cntime.DayCompactOf(time.Now().AddDate(0, 0, -1))
	f := newJoinFix(t, map[string]string{"600029": "航空机场"}, yesterday)
	f.e.industryFn = func(code string) string { return "" } // 关掉兜底，单独验"跨日不借"
	if _, ok := f.e.sectorDeltaFor("600029"); ok {
		t.Fatal("昨日连接键不许留宿（板块成分每天变，错配的 delta 会贴到错的票上）")
	}
}

// TestSectorDeltaAbstainsWhenDeltaMapIsStale 连接键是今天的、delta map 是昨天的 ⇒ 同样弃权。
func TestSectorDeltaAbstainsWhenDeltaMapIsStale(t *testing.T) {
	f := newJoinFix(t, map[string]string{"600029": "航空机场"}, cntime.DayCompactOf(time.Now()))
	f.e.flowMoneyMu.Lock()
	f.e.sectorDeltaDay = cntime.DayOf(time.Now().AddDate(0, 0, -1))
	f.e.flowMoneyMu.Unlock()
	if _, ok := f.e.sectorDeltaFor("600029"); ok {
		t.Fatal("昨日板块 delta 不许当今天用")
	}
}
