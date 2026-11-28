// §BT-NULL-INDUSTRY（2026-11-28 生产首跑）行为锁：
// stocks.industry 允许 NULL，B4 事件合成的区间查询曾直接 Scan 进 string，
// 线上候选 #1 第一次跑就报 "converting NULL to string is unsupported" 整批失败。
// 本用例种一只未归类涨停股，断言合成不报错且不产出空板块事件。
package backtest

import "testing"

func TestSynthesizeEventsSkipsNullIndustry(t *testing.T) {
	db := seedBT(t) // 已有 8 个交易日，其中 20230103 芯片有 3 只涨停
	if _, err := db.InsertRows("stocks",
		[]string{"ts_code", "name", "industry", "list_date"},
		[]map[string]any{{"ts_code": "999999.SZ", "name": "未归类", "industry": nil, "list_date": "20150101"}},
	); err != nil {
		t.Fatalf("插入未归类股票失败: %v", err)
	}
	if _, err := db.InsertRows("daily",
		[]string{"ts_code", "trade_date", "open", "high", "low", "close", "vol", "amount"},
		[]map[string]any{{"ts_code": "999999.SZ", "trade_date": "20230103", "open": 10, "high": 11,
			"low": 10, "close": 11, "vol": 1000, "amount": 11000}},
	); err != nil {
		t.Fatalf("插入行情失败: %v", err)
	}
	if _, err := db.InsertRows("stk_limit",
		[]string{"ts_code", "trade_date", "up_limit", "down_limit"},
		[]map[string]any{{"ts_code": "999999.SZ", "trade_date": "20230103", "up_limit": 11, "down_limit": 9}},
	); err != nil {
		t.Fatalf("插入涨停价失败: %v", err)
	}

	events, err := SynthesizeEvents(db, "20230103", "20230112", 3, 3)
	if err != nil {
		t.Fatalf("NULL industry 不得让 B4 事件合成整批失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("芯片涨停潮事件仍应存在（不能因跳过 NULL 而全丢）")
	}
	for _, e := range events {
		if e.Industry == "" {
			t.Fatalf("不得产出空板块事件: %+v", e)
		}
	}
}
