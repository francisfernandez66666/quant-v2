// sector_constituents_merge_test.go — §SCORES-FAST-1000（2026-11-13 owner 17:14 裁决
// 「THS 扒 + 东财兜底」）合并语义回归。
//
// 背景实测（当日）：THS 成分页单页 10 只、深分页被 hexin-v 反爬拦死、且无行情字段；
// 东财 clist 单页 pz 上限 100、带完整行情。旧实现「THS 成功即 return」导致每板块只有
// 10 只（TopN=100 形同虚设）。本测试钉合并语义：
//  1. THS 名单在前（板块语义权威），东财在后补足到 topN；
//  2. 同代码去重；THS 行缺行情（Price<=0）时被东财富行覆盖；
//  3. 封顶 topN；空输入不 panic。
package data

import "testing"

func TestMergeConstituentsTHSFirstEmTopUp(t *testing.T) {
	ths := []StockInfo{
		{Code: "600001"}, {Code: "600002"}, {Code: "600003"},
	}
	em := []StockInfo{
		{Code: "600002", Price: 10.5, ChangePct: 3.2}, // 与 THS 重叠：应覆盖为富行
		{Code: "600010", Price: 8.8, ChangePct: 1.1},
		{Code: "600011", Price: 9.9, ChangePct: -0.5},
	}
	merged := mergeConstituents(ths, em, 10)
	if len(merged) != 5 {
		t.Fatalf("合并应 5 只（THS 3 + 东财新增 2）, got %d", len(merged))
	}
	// 顺序：THS 在前（板块语义权威）
	if merged[0].Code != "600001" || merged[1].Code != "600002" || merged[2].Code != "600003" {
		t.Fatalf("THS 名单应保持在前: %+v", merged[:3])
	}
	// 重叠代码用东财富行覆盖（行情字段补上）
	if merged[1].Price != 10.5 || merged[1].ChangePct != 3.2 {
		t.Fatalf("重叠代码应被东财行情覆盖, got %+v", merged[1])
	}
	if merged[3].Code != "600010" || merged[4].Code != "600011" {
		t.Fatalf("东财补足行顺序不符: %+v", merged[3:])
	}
}

func TestMergeConstituentsCapAndEdge(t *testing.T) {
	// 封顶 topN：THS 已满时不再吃东财
	ths := []StockInfo{{Code: "600001"}, {Code: "600002"}}
	em := []StockInfo{{Code: "600010", Price: 1}}
	if got := mergeConstituents(ths, em, 2); len(got) != 2 {
		t.Fatalf("topN=2 应封顶 2 只, got %d", len(got))
	}
	// THS 为空 → 纯东财（兜底路径）
	if got := mergeConstituents(nil, em, 10); len(got) != 1 || got[0].Code != "600010" {
		t.Fatalf("THS 空时应纯东财兜底, got %+v", got)
	}
	// 双空 → 空（调用方转 error，不 panic）
	if got := mergeConstituents(nil, nil, 10); len(got) != 0 {
		t.Fatalf("双空应返回空, got %+v", got)
	}
	// 空代码行被丢弃
	if got := mergeConstituents([]StockInfo{{Code: ""}}, []StockInfo{{Code: "600010", Price: 1}}, 10); len(got) != 1 {
		t.Fatalf("空代码行应被丢弃, got %+v", got)
	}
}
