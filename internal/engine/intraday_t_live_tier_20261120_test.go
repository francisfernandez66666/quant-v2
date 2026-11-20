// turnover_tier_20261120_test.go — §T-TURNOVER-TIER 行为锁（实盘腿）。
//
// 载体与云端必须按同一个键分档：流通市值＝昨收 × 流通股本（亿元）。这组用例锁的是
// **引擎这条腿真的吃了分档**，而不是只把表塞进结构体：
//   - 600 亿 + 0.69% 换手 ⇒ 放行（旧平铺 1.50% 会拦掉，这正是 11-20 现网那条 skip）；
//   - 50 亿 + 0.69% 换手 ⇒ 仍拦（小票行为一个字都没改）；
//   - 市值读不到 ⇒ 按最严档拦（"看不见"不许变成"放宽"）。
package engine

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
)

// TestTLiquidityTurnoverTierLiveRound 实盘腿按票分档。
func TestTLiquidityTurnoverTierLiveRound(t *testing.T) {
	// 分档表取配置包的真源（不在用例里抄一遍数字）。
	tiers := (&config.TConfig{}).TMinTurnoverTiersOf()
	p := paper.TParams{
		MinTurnoverPct:   1.5,
		MinTurnoverTiers: tiers,
		// 这条用例只管换手率一维：成交额/上市天数两顶帽子关掉，避免被别的门先拦。
		MinAmountWan:  0,
		MinListedDays: 0,
	}
	r := &tLiveRound{p: p}

	big := &data.StockInfo{Code: "600519", Name: "贵州茅台", Turnover: 0.69, FloatMarketCap: 600e8}
	if why := r.liquidityBlock("600519", big); why != "" {
		t.Fatalf("600 亿 / 0.69%% 应按 0.40%% 档放行，却被拦：%s", why)
	}
	mid := &data.StockInfo{Code: "000672", Name: "上峰水泥", Turnover: 0.69, FloatMarketCap: 121.9e8}
	if why := r.liquidityBlock("000672", mid); why != "" {
		t.Fatalf("121 亿 / 0.69%% 应按 0.60%% 档放行，却被拦：%s", why)
	}
	small := &data.StockInfo{Code: "000009", Name: "中国宝安", Turnover: 0.69, FloatMarketCap: 50e8}
	if why := r.liquidityBlock("000009", small); why == "" || !strings.Contains(why, "1.50") {
		t.Fatalf("50 亿 / 0.69%% 应被 1.50%% 档拦下，得到：%q", why)
	}
	unknown := &data.StockInfo{Code: "000001", Name: "平安银行", Turnover: 0.69}
	if why := r.liquidityBlock("000001", unknown); why == "" {
		t.Fatal("市值读不到时应按最严档拦下，不能放行")
	}

	// 显式平铺值（分档表为空）⇒ 行为与今天完全一致：600 亿 + 0.69% 照样被 1.50% 拦掉。
	flat := &tLiveRound{p: paper.TParams{MinTurnoverPct: 1.5}}
	if why := flat.liquidityBlock("600519", big); why == "" {
		t.Fatal("没有分档表时应走平铺值，600 亿 0.69% 必须被拦")
	}
}
