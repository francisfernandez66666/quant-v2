// amountscale_test.go — §0929SCALE-⑩ 写侧量纲归一的单元测试。
// 覆盖：tushare 千元形态行归一为元；白名单外表原样不动；nil/非数值跳过且不伪造 0；
// vol/close 不参与换算（均价判据依赖它们）；nil map 单元格不被创建。
package data

import "testing"

// TestNormalizeTushareAmount 喂 tushare 形态的 daily 行，断言 amount 归一到元口径。
// 数值取真实形态：600000.SH 某日 vol=200000 手、amount=200000.0（千元）⇒ 换算后应为 2e8 元，
// 均价 = 2e8/(200000×100) = 10 元落在 [1,500] 合理带内（与抽检探针同一把尺子）。
func TestNormalizeTushareAmount(t *testing.T) {
	rows := []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20260918", "close": 10.0, "vol": 200000.0, "amount": 200000.0},
		{"ts_code": "000001.SZ", "trade_date": "20260918", "close": 12.5, "vol": 80000.0, "amount": 100000.0},
	}
	changed := NormalizeTushareAmount("daily", rows)
	if changed != 2 {
		t.Fatalf("换算行数=%d, 期望 2（白名单表两行都应换算）", changed)
	}
	if got := rows[0]["amount"]; got != 2e8 {
		t.Errorf("600000.SH amount=%v, 期望 2e8（千元×1000 归一到元）", got)
	}
	if got := rows[1]["amount"]; got != 1e8 {
		t.Errorf("000001.SZ amount=%v, 期望 1e8", got)
	}
	// 方向锁：均价必须落进抽检合理带 [1,500] 元。把换算摘掉（回未归一口径）这里必红。
	for _, r := range rows {
		avg := r["amount"].(float64) / (r["vol"].(float64) * 100)
		if avg < 1 || avg > 500 {
			t.Errorf("%s 归一后日均价 %.4f 元不在 [1,500] 带内 ⇒ 归一方向或倍率错了", r["ts_code"], avg)
		}
	}
	// vol 是"手"、close 是"元"，两者都不许被牵连（均价判据与复权链路都读它们）。
	if rows[0]["vol"] != 200000.0 || rows[0]["close"] != 10.0 {
		t.Errorf("非 amount 列被改动：vol=%v close=%v", rows[0]["vol"], rows[0]["close"])
	}
}

// TestNormalizeTushareAmountWhitelist 断言白名单外的表一行都不动。
// daily_basic 的 total_mv/circ_mv 是"万元"、minute_klines 走另一条腿，两处都不许被顺手乘一次；
// 本用例就是这条白名单的负锁——有人把表名加进 AmountScaledTables 而没同步这里就会红。
func TestNormalizeTushareAmountWhitelist(t *testing.T) {
	rows := []map[string]any{
		{"ts_code": "600000.SH", "trade_date": "20260918", "amount": 200000.0, "total_mv": 1e7},
	}
	if changed := NormalizeTushareAmount("daily_basic", rows); changed != 0 {
		t.Fatalf("非白名单表 daily_basic 被换算 %d 行，期望 0", changed)
	}
	if rows[0]["amount"] != 200000.0 {
		t.Errorf("daily_basic amount 被改动：%v", rows[0]["amount"])
	}
	// index_daily 必须在白名单内（与 daily 同一口径问题）。
	if !AmountScaledTables["index_daily"] {
		t.Error("index_daily 不在换算白名单 ⇒ 指数日线会把千元当元落库")
	}
}

// TestNormalizeTushareAmountSkipsBadCells 断言缺失/NULL/非数值单元格被跳过且**不伪造 0**。
// 写 0 会让下游把"缺数"当"零成交"，进而被流动性质控整批剔票——这是本仓最忌的静默降级形态。
func TestNormalizeTushareAmountSkipsBadCells(t *testing.T) {
	rows := []map[string]any{
		{"ts_code": "A", "amount": nil},            // NULL 单元格
		{"ts_code": "B"},                           // 根本没有 amount 键
		{"ts_code": "C", "amount": "not-a-number"}, // 上游给了脏串
		{"ts_code": "D", "amount": "123.5"},        // 数字字符串（装载层可能出现的形态）要能换算
		{"ts_code": "E", "amount": 7},              // int 形态同样要换算
	}
	changed := NormalizeTushareAmount("daily", rows)
	if changed != 2 {
		t.Fatalf("换算行数=%d, 期望 2（只有 D/E 两个可解析数值）", changed)
	}
	if rows[0]["amount"] != nil {
		t.Errorf("nil 单元格被改动：%v（不许伪造数值）", rows[0]["amount"])
	}
	if _, ok := rows[1]["amount"]; ok {
		t.Error("缺键行被凭空创建 amount 键")
	}
	if rows[2]["amount"] != "not-a-number" {
		t.Errorf("脏串被改动：%v（应原样交下游自校判断）", rows[2]["amount"])
	}
	if got, ok := rows[3]["amount"].(float64); !ok || got != 123500.0 {
		t.Errorf("数字字符串未换算：%v", rows[3]["amount"])
	}
	if got, ok := rows[4]["amount"].(float64); !ok || got != 7000.0 {
		t.Errorf("int 形态未换算：%v", rows[4]["amount"])
	}
}

// TestTushareAmountScaleMatchesStoreConst 钉死写侧系数就是 1000。
// 说明：store 侧另立了同值常量 TushareThousandToCNY（避开 store→data 反向依赖），
// 两者的**等值**锁放在 cmd/dataload 的测试里（那里可以同时 import 两个包，不会成环），
// 见 cmd/dataload/amount_scale_crosscheck_test.go。
func TestTushareAmountScaleMatchesStoreConst(t *testing.T) {
	// 不 import store（会成环），这里按字面钉死：data 侧常量必须是 1000。
	if TushareAmountScale != 1000.0 {
		t.Fatalf("TushareAmountScale=%v，期望 1000.0（与 internal/store.TushareThousandToCNY 同源）", TushareAmountScale)
	}
}
