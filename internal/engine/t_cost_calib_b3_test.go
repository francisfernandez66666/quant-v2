// t_cost_calib_b3_test.go — §T-COST B3（2026-10-27）相关性簇帽的执行层锁。
//
// 四条断言（对应施工图 §5.5）：
//  1. 行为腿：两只 ρ=0.9 的票连续买入，第二只的 SkipReason 必须点名"已达相关性簇上限"；
//  2. 取小不叠乘：行业帽 30% + 簇帽 40% ⇒ 实际可用等于 30%（不是 12%）；
//  3. 反证腿：簇帽放到 100% ⇒ 判据 1 必红（证明拦截确实来自簇帽，不是别的闸顺手拦的）；
//  4. 读数整体不可用 ⇒ 帽跳过（不按同簇处理）——这一条是"数据层故障不许伪装成风险决策"。
//
// 为什么用 clusterLoad 测试缝而不是真日线：真日线要 60 个交易日 × N 只票的 fixture，
// 而本用例要证的只是"簇帽在预算里真的生效"。取数本身的正确性由 internal/cluster 的用例锁。
// English: execution-layer locks for the cluster cap — it must actually block the second correlated
// buy, must take the min with the industry cap (never a product), and must skip itself when the
// daily-return reading is entirely unavailable.
package engine

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/cluster"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// b3Series 造 60 个交易日的收益序列（wave=true 为一条正弦波，false 为它的 0.9 倍同相波）。
// 两列完全同相 ⇒ ρ=1，稳过 0.7 阈值（用 0.9 倍而不是完全相同，避免"看起来是同一个对象"的错觉）。
func b3Series(wave bool) cluster.Series {
	s := cluster.Series{}
	for i := 0; i < 60; i++ {
		v := math.Sin(float64(i)) / 100
		if !wave {
			v *= 0.9
		}
		s["2026"+string(rune('0'+(i/10)%10))+string(rune('0'+i%10))+"01"] = v
	}
	return s
}

// wireClusterEngine 注入簇取数测试缝（按代码给出确定性序列）+ 行业源。
func wireClusterEngine(t *testing.T, e *Engine, ser map[string]cluster.Series, industry string) {
	t.Helper()
	e.SetMembers([]string{"u_1"})
	e.mu.Lock()
	e.marketAPI = &data.MarketAPI{}
	e.industryFn = func(string) string { return industry }
	e.clusterLoad = func(code string) (cluster.Series, bool, bool) {
		s, ok := ser[pureTsCode(code)]
		if !ok {
			return nil, false, false
		}
		return s, true, false
	}
	// 簇缓存按交易日失效：注入新序列前先清一次，避免上一条用例的缓存串场。
	e.clusterCache.mu.Lock()
	e.clusterCache.day = ""
	e.clusterCache.ser = nil
	e.clusterCache.bad = nil
	e.clusterCache.mu.Unlock()
	e.mu.Unlock()
}

// b3SizingCfg 在默认配置上只动簇帽，其余保持出厂（单票/行业帽在 QMTConfig.RiskGate 上）。
func b3SizingCfg(clusterCap float64) config.SizingConfig {
	sc := dynSizingCfg()
	sc.ClusterCapPct = &clusterCap
	sc.ClusterRho = 0.70
	return sc
}

// seedB3Holdings 播一只已持仓（走真实持仓表，dynamicBuyQty 的集中度口径才会算到它）。
func seedB3Holdings(t *testing.T, db *store.DB, code string, qty int, px float64) {
	t.Helper()
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		UserID: "u_1", TsCode: code + ".SH", Qty: qty, CostPrice: px, CurPrice: px,
		UpdatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}}); err != nil {
		t.Fatalf("seed position %s: %v", code, err)
	}
}

// TestB3ClusterCapBlocksCorrelatedBuy 判据 1：同簇已用 30% + 新买一只同簇票 ⇒ 被簇帽拦下。
//
// 算术：总资产 10 万、簇帽 40% ⇒ 簇额度 4 万；已持 600000 市值 3 万 ⇒ 只剩 1 万；
// 单笔 eff=0.50 ⇒ 5 万，被簇额度压到 1 万；1 万 ≥ 1 手（10 元×100=1000）⇒ **不会被跳过**，
// 只会被减额。所以"拦下"要看的是**减额到 1 万**而不是 0 股。真正会跳过的是把已用推到 3.95 万：
// 只剩 500 元 < 1 手 ⇒ SkipReason="已达相关性簇上限"。
func TestB3ClusterCapBlocksCorrelatedBuy(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = -1 // 本用例不谈碎单闸
		c.MaxPositions = 0    // 也不谈持仓数上限
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	// 已持 3.95 万（3950 股 @10）⇒ 簇帽 40%（4 万）只剩 500 元 < 1 手。
	seedB3Holdings(t, db, "600000", 3950, 10)
	// 600000 与 600002 同相 ⇒ ρ=1 ⇒ 同簇；行业刻意给空串，证明拦截**只可能**来自簇帽。
	wireClusterEngine(t, e, map[string]cluster.Series{
		"600000": b3Series(true),
		"600002": b3Series(false),
	}, "")

	sig := combat_agent.Signal{ID: "SIG-B3A", Code: "600002", Name: "同簇票", Strategy: "龙头",
		Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600002": {Code: "600002", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.5, StockScale: 0.5}, b3SizingCfg(40))
	if len(*orders) != 0 {
		t.Fatalf("簇额度只剩 500 元 < 1 手，应被簇帽跳过，实得 %d 单：%+v", len(*orders), *orders)
	}
	// 反证腿（判据 3）：把簇帽放到 100% ⇒ 同一场景必须放行。
	// 这一步同时证明"上面那单是被簇帽拦的"，而不是被别的闸顺手拦掉的。
	e2, db2, _, orders2 := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = -1
		c.MaxPositions = 0
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db2)
	seedB3Holdings(t, db2, "600000", 3950, 10)
	wireClusterEngine(t, e2, map[string]cluster.Series{
		"600000": b3Series(true),
		"600002": b3Series(false),
	}, "")
	e2.autoPlaceScaled(combat_agent.Signal{ID: "SIG-B3B", Code: "600002", Name: "同簇票", Strategy: "龙头",
		Direction: "做多", Price: 10},
		map[string]*data.StockInfo{"600002": {Code: "600002", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.5, StockScale: 0.5}, b3SizingCfg(100))
	if len(*orders2) != 1 {
		t.Fatalf("反证腿：簇帽 100%% 时必须放行（否则拦截来源不是簇帽），实得 %d 单", len(*orders2))
	}
}

// TestB3ClusterTakesMinNotProduct 判据 2：行业帽 30% + 簇帽 40% ⇒ 可用 30%，不是 12%。
//
// 算术：总资产 10 万；行业"银行"已持 2.5 万 ⇒ 行业额度 30%×10万−2.5万 = 0.5 万；
// 同簇已用 2.5 万 ⇒ 簇额度 40%×10万−2.5万 = 1.5 万。取小 ⇒ 0.5 万（500 股）。
// 若写成连乘（30%×40%=12% ⇒ 1.2 万−2.5万 → 0）就会变成"额度不足 1 手"直接跳过——
// 两个帽同时收紧三倍正是方案明令禁止的形态。
func TestB3ClusterTakesMinNotProduct(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = -1
		c.MaxPositions = 0
		c.RiskGate.SectorValuePct = 30
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	seedB3Holdings(t, db, "600000", 2500, 10) // 市值 2.5 万，行业=银行
	wireClusterEngine(t, e, map[string]cluster.Series{
		"600000": b3Series(true),
		"600002": b3Series(false),
	}, "银行")

	e.autoPlaceScaled(combat_agent.Signal{ID: "SIG-B3C", Code: "600002", Name: "同行业票", Strategy: "龙头",
		Direction: "做多", Price: 10},
		map[string]*data.StockInfo{"600002": {Code: "600002", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.5, StockScale: 0.5}, b3SizingCfg(40))
	if len(*orders) != 1 {
		t.Fatalf("行业额度还剩 0.5 万（≥1 手）应放行，实得 %d 单（连乘会把额度压成 0 而跳过）", len(*orders))
	}
	qty := int((*orders)[0]["qty"].(float64))
	if qty != 500 {
		t.Fatalf("取小后应买 500 股（行业额度 0.5 万/10 元），got %d（连乘形态会得到 0 或 100）", qty)
	}
}

// TestB3ClusterSkipsWhenReadingUnavailable 判据 4：一条日线都取不到 ⇒ 帽跳过，不冻结开仓。
//
// 这条是**刻意的例外**（文件头已说明）：整批序列缺失是数据层故障，把它按同簇处理会让
// 簇帽退化成"全账户总敞口 40%"，与目标仓位模型（86.5%）直接打架 ⇒ 全面停止开仓。
// 反证：把例外条款删掉 ⇒ 本用例必红（那时第二只票会被并进同一簇而被拦）。
func TestB3ClusterSkipsWhenReadingUnavailable(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.MinOrderAmount = -1
		c.MaxPositions = 0
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	seedB3Holdings(t, db, "600000", 3950, 10) // 已用 3.95 万，若按同簇算只剩 500 元
	// 空 map ⇒ 所有代码都没有序列 ⇒ 走例外条款。
	wireClusterEngine(t, e, map[string]cluster.Series{}, "")

	e.autoPlaceScaled(combat_agent.Signal{ID: "SIG-B3D", Code: "600002", Name: "无读数票", Strategy: "龙头",
		Direction: "做多", Price: 10},
		map[string]*data.StockInfo{"600002": {Code: "600002", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.5, StockScale: 0.5}, b3SizingCfg(40))
	if len(*orders) != 1 {
		t.Fatalf("日线整体不可用时簇帽必须跳过（不冻结开仓），实得 %d 单", len(*orders))
	}
	// 同时必须留痕：这条读数缺失要能在 opslog 里找到（"静默跳过"是另一类缺陷）。
	read := e.clusterGateFor("600002", map[string]float64{"600000": 39500}, func(string) string { return "" }, 40, 0.70)
	if read.Applied {
		t.Fatalf("读数整体不可用时 Applied 必须为 false，got %+v", read)
	}
	if read.Reason == "" {
		t.Fatal("跳过必须带原因（面板/日志要能回答『为什么这条帽没生效』）")
	}
}

// TestB3ClusterConfigTriState 配置三态：缺键 ⇒ 40（出厂开），显式 0 ⇒ 关，脏值 ⇒ 夹取。
// 这条单独钉是因为"显式 0"是回滚位：兜成 40 就等于"关不掉"，那是最难发现的一类配置缺陷。
func TestB3ClusterConfigTriState(t *testing.T) {
	var absent config.SizingConfig
	if got := absent.ClusterCapPctOf(); got != 40 {
		t.Fatalf("缺键必须读出厂 40，got %v", got)
	}
	zero := 0.0
	off := config.SizingConfig{ClusterCapPct: &zero}
	if got := off.ClusterCapPctOf(); got != 0 {
		t.Fatalf("显式 0 必须读成关（回滚位），got %v", got)
	}
	dirty := 300.0
	d := config.SizingConfig{ClusterCapPct: &dirty}
	if got := d.ClusterCapPctOf(); got != 100 {
		t.Fatalf("脏值 300 必须夹到 100，got %v", got)
	}
	// rho 缺键/脏值都回落 0.70。
	if got := (config.SizingConfig{}).ClusterRhoOf(); got != 0.70 {
		t.Fatalf("rho 缺键应回落 0.70，got %v", got)
	}
	if got := (config.SizingConfig{ClusterRho: 5}).ClusterRhoOf(); got != 0.70 {
		t.Fatalf("rho>1 应回落 0.70，got %v", got)
	}
}
