// cost_enhance_test.go — §回测自动增强模块 A/B 成本原语回归：动态滑点（分档/非对称/
// 校准回退链）、部分成交、涨停打开/封死、跌停封死单日判定、出场引擎兼容、
// Risk-1 千元单位自校、nil 配置=旧行为。
// English: unit tests for the enhanced cost model — slippage tiers/asymmetry/calibration
// fallback, partial fill, limit-board gating, exit-engine legacy equivalence and unit self-check.
package btreplay

import (
	"math"
	"testing"

	"quant-trading-v2/internal/config"
	data "quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// nearly 浮点近似比较。
func nearly(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestCostRoundTripPnlExCompat 兼容包装：Ex(5,5) 与旧 costRoundTripPnl 逐字节一致。
func TestCostRoundTripPnlExCompat(t *testing.T) {
	for _, pair := range [][2]float64{{100, 110}, {100, 95}, {7.77, 8.31}, {0, 5}, {5, 0}} {
		if a, b := costRoundTripPnl(pair[0], pair[1]), costRoundTripPnlEx(pair[0], pair[1], 5, 5); !nearly(a, b) {
			t.Fatalf("兼容口径漂移: %v → %f vs %f", pair, a, b)
		}
	}
}

// TestSlippageTiers 流动性/冲击分档边界值与最差档口径（avgAmtWan=0 → +15bp）。
func TestSlippageTiers(t *testing.T) {
	vt := config.DefaultVolumeTiers()
	st := config.DefaultSizeTiers()
	cases := []struct {
		amt  float64
		want float64
	}{
		{0, 15}, {80, 15}, {100, 15}, {300, 12}, {500, 12}, {1500, 8}, {2000, 8},
		{3000, 3}, {5000, 3}, {6000, 0},
	}
	for _, tc := range cases {
		if got := volumePenalty(tc.amt, vt); !nearly(got, tc.want) {
			t.Fatalf("volumePenalty(%f)=%f, want %f", tc.amt, got, tc.want)
		}
	}
	// 冲击档：名义 1 万元（元）/ 日均成交额（万元）
	if got := sizePenalty(10000, 1000, st); !nearly(got, 3) { // ratio=1e4/1e7=0.001 → ≥0.1% 档 +3bp
		t.Fatalf("sizePenalty(1e4,1000万)=%f, want 3", got)
	}
	if got := sizePenalty(10000, 100, st); !nearly(got, 8) { // ratio=1e4/1e6=0.01 → 命中 min_ratio 0.01 档 +8bp
		t.Fatalf("sizePenalty 高档=%f, want 8", got)
	}
	if got := sizePenalty(10000, 4, st); !nearly(got, 10) { // ratio=1e4/4e4=0.25 >2% → +10bp
		t.Fatalf("sizePenalty 顶档=%f, want 10", got)
	}
	if got := sizePenalty(10000, 0, st); got != 0 {
		t.Fatalf("无成交额样本不叠加冲击罚, got %f", got)
	}
	if got := sizePenalty(0, 100, st); got != 0 {
		t.Fatalf("名义额 0 不罚, got %f", got)
	}
}

// TestSlippageBpsAsymmetric 非对称：买档 = 卖档 + BuyExtraBps；关闭非对称时双向一致。
func TestSlippageBpsAsymmetric(t *testing.T) {
	cfg := config.SlippageConfig{BaseBps: 3, BuyExtraBps: 1, Asymmetric: true,
		VolumeTiers: config.DefaultVolumeTiers(), SizeTiers: config.DefaultSizeTiers()}
	buy, sell := slippageBps(6000, 10000, 3, cfg) // 大盘小单：无罚档
	if !nearly(buy, 4) || !nearly(sell, 3) {
		t.Fatalf("非对称=%f/%f, want 4/3", buy, sell)
	}
	cfg.Asymmetric = false
	buy2, sell2 := slippageBps(6000, 10000, 3, cfg)
	if !nearly(buy2, sell2) {
		t.Fatalf("对称模式买卖应相等: %f/%f", buy2, sell2)
	}
}

// TestFillRate 部分成交比例：小单全成、超大单 0.3、无成交额样本回落下限。
func TestFillRate(t *testing.T) {
	if r := fillRate(10000, 6000, 0.3); !nearly(r, 1.0) { // ratio≈0.00017 <0.001
		t.Fatalf("小单应全成, got %f", r)
	}
	if r := fillRate(10000, 30, 0.3); !nearly(r, 0.3) { // ratio=1e4/3e5≈3.3% → 超大单 0.30
		t.Fatalf("超大单=%f, want 0.30", r)
	}
	if r := fillRate(10000, 0, 0.3); !nearly(r, 0.3) { // 无成交额样本按最差档
		t.Fatalf("零成交额应回落下限, got %f", r)
	}
	if r := fillRate(10000, 100, 0.6); !nearly(r, 0.6) { // ratio=1% → 表值 0.5，floor 0.6 提升
		t.Fatalf("floor 未生效, got %f", r)
	}
}

// TestLimitBoardGating 涨停打开/封死与跌停封死单日判定（主板 10%，前收 10 → 板价 11/9）。
func TestLimitBoardGating(t *testing.T) {
	if !costLimitUpOpenable("600000.SH", 10, 11.0, 10.5) {
		t.Fatal("开盘涨停盘中打开应判 openable")
	}
	if costLimitUpOpenable("600000.SH", 10, 11.0, 10.9995) { // 最低仍在涨停价（容差内）= 封死
		t.Fatal("全天未开板不应判 openable")
	}
	if costLimitUpOpenable("600000.SH", 10, 10.5, 10.0) {
		t.Fatal("未涨停开盘不应判 openable")
	}
	if !costLimitDownSealedDay("600000.SH", 10, 9.0, 9.0) {
		t.Fatal("触板收在板上应判封死")
	}
	if costLimitDownSealedDay("600000.SH", 10, 9.0, 9.5) {
		t.Fatal("触板但收在板上（打开）不判封死")
	}
	if costLimitDownSealedDay("600000.SH", 0, 9, 9) {
		t.Fatal("非法前收不判封死")
	}
}

// TestCalibAudit 校准合并三链路与护栏：实测→扣 paper 模型滑点→clamp；
// 样本不足回退配置值；非对称买差 clamp [0,5]。
func TestCalibAudit(t *testing.T) {
	bt := &config.BacktestConfig{Enabled: true, PaperModelSlippageBps: 5}
	bt.Slippage = config.SlippageConfig{BaseBps: 3, BuyExtraBps: 1, Asymmetric: true,
		AutoCalibrate: true, CalibMinSample: 30, CalibWindowDays: 90}
	// 实测买 9 / 卖 6（含 paper 模型 5bp）→ base=clamp(min(9,6)-5,3,15)=3→4? min=6-5=1→clamp=3；
	// 换组更明显的数：买 12 / 卖 8 → base=8-5=3→clamp(3)=3；买差=12-8=4 → clamp(4,0,5)=4
	bt2 := *bt
	_ = bt2
	base, extra, audit := calibAudit(bt, &store.SlippageCalib{BuyMedBps: 12, SellMedBps: 8, BuyN: 40, SellN: 35})
	if !nearly(base, 3) || !nearly(extra, 4) {
		t.Fatalf("校准合并=%f/%f, want 3/4", base, extra)
	}
	if audit["source"] != "paper_median" {
		t.Fatalf("审计 source=%v", audit["source"])
	}
	// 样本不足 → 回退配置值
	base, extra, audit = calibAudit(bt, &store.SlippageCalib{BuyMedBps: 12, SellMedBps: 8, BuyN: 29, SellN: 40})
	if !nearly(base, 3) || !nearly(extra, 1) {
		t.Fatalf("样本不足应回退配置 3/1, got %f/%f", base, extra)
	}
	if audit["source"] == "paper_median" {
		t.Fatal("样本不足不得标 paper_median")
	}
	// 无样本（nil）→ 配置值 + 标注
	base, _, audit = calibAudit(bt, nil)
	if !nearly(base, 3) || audit["source"] != "config(no_sample)" {
		t.Fatalf("无样本回退异常: %f %v", base, audit["source"])
	}
	// clamp 上限：实测 40/38 → base=clamp(33,3,15)=15；买差=clamp(2,0,5)=2
	base, extra, _ = calibAudit(bt, &store.SlippageCalib{BuyMedBps: 40, SellMedBps: 38, BuyN: 100, SellN: 100})
	if !nearly(base, 15) || !nearly(extra, 2) {
		t.Fatalf("clamp 失效: %f/%f want 15/2", base, extra)
	}
	// 关闭自动校准 → 恒配置值
	btOff := *bt
	btOff.Slippage.AutoCalibrate = false
	base, extra, audit = calibAudit(&btOff, &store.SlippageCalib{BuyMedBps: 12, SellMedBps: 8, BuyN: 40, SellN: 35})
	if !nearly(base, 3) || !nearly(extra, 1) || audit["source"] != "config" {
		t.Fatalf("关闭校准应走配置: %f/%f/%v", base, extra, audit["source"])
	}
}

// mkKLine 构造一根 K 线。
func mkBarLine(open, high, low, close, amount float64) data.KLine {
	return data.KLine{Open: open, High: high, Low: low, Close: close, Volume: amount / 100, Amount: amount}
}

// TestEntrySlipGating slipCtx 入场定档：一字封死不可成交、涨停打开加罚、
// 小盘大单罚档叠加、非对称买差。
func TestEntrySlipGating(t *testing.T) {
	cfg := &config.BacktestConfig{Enabled: true, OrderValueYuan: 10000, PaperModelSlippageBps: 5}
	cfg.Slippage = config.SlippageConfig{BaseBps: 3, BuyExtraBps: 1, Asymmetric: true,
		VolumeTiers: config.DefaultVolumeTiers(), SizeTiers: config.DefaultSizeTiers()}
	cfg.Liquidity = config.LiquidityConfig{Enabled: true, LimitUpOpenableExtraBps: 10,
		LimitDownSealedExtraBps: 15, LimitDownSealedEnabled: true, PartialFillEnabled: true, FillRateMin: 0.3}
	sc := &slipCtx{baseBps: 3, slip: cfg.Slippage, orderValue: 10000, liq: cfg.Liquidity, liqOn: true}

	// 前收 10、信号日 i=30；入场日 i+1
	kls := make([]data.KLine, 32)
	for i := range kls {
		kls[i] = mkBarLine(10, 10, 10, 10, 8e7) // 日均成交额 8000 万元 → 无流动性罚
	}
	// 情形 1：一字板（开=低=涨停价 11）→ 不可成交
	kls[30].Close = 10
	kls[31] = mkBarLine(11, 11, 11, 11, 8e7)
	if _, _, _, ok := sc.entrySlip("600000.SH", kls, 30); ok {
		t.Fatal("一字封死应不可成交")
	}
	// 情形 2：涨停开盘盘中打开 → 可成交 + 10bp
	kls[31] = mkBarLine(11, 11.05, 10.5, 10.8, 8e7)
	buy, sell, fill, ok := sc.entrySlip("600000.SH", kls, 30)
	if !ok {
		t.Fatal("涨停打开应可成交")
	}
	// 无流动性罚（8000万≥5000万）；冲击罚 ratio=1e4/8e7=0.000125 <0.001 → 0；base3+买差1+打开10=14
	if !nearly(buy, 14) || !nearly(sell, 3) {
		t.Fatalf("打开板定档=%f/%f, want 14/3", buy, sell)
	}
	if !nearly(fill, 1) {
		t.Fatalf("小单应全成, got %f", fill)
	}
	// 情形 3：低流动性（日均 300 万元）→ +12 罚、冲击 ratio=1e4/3e6≈0.33% → +3、成交比例 0.95
	for i := range kls {
		kls[i] = mkBarLine(10, 10, 10, 10, 3e6)
	}
	kls[30].Close = 10
	kls[31] = mkBarLine(10.2, 10.3, 10, 10.1, 3e6)
	buy, sell, fill, _ = sc.entrySlip("600000.SH", kls, 30)
	if !nearly(buy, 3+12+3+1) || !nearly(sell, 3+12+3) {
		t.Fatalf("低流动性定档=%f/%f, want 19/18", buy, sell)
	}
	if !nearly(fill, 0.95) {
		t.Fatalf("成交比例=%f, want 0.95", fill)
	}
}

// TestUniformExitLegacyEquivalence 旧签名与全参数版（5/5/1/0）逐日一致（回归基线）。
func TestUniformExitLegacyEquivalence(t *testing.T) {
	kls := make([]data.KLine, 20)
	for i := range kls {
		c := 10.0 + math.Sin(float64(i))
		kls[i] = mkBarLine(c, c+0.2, c-0.2, c, 5e7)
	}
	entry := 10.0
	j1, p1 := uniformExitV2ATR(kls, 5, entry, 10.2, 8, 5, 0, 10, nil, 0)
	j2, p2 := uniformExitV2Full(kls, "", 5, entry, 10.2, 8, 5, 0, 10, nil, 0,
		costSlippageBps, costSlippageBps, 1, 0)
	if j1 != j2 || !nearly(p1, p2) {
		t.Fatalf("出场引擎兼容漂移: (%d,%f) vs (%d,%f)", j1, p1, j2, p2)
	}
}

// TestUniformExitSealedDefers 跌停封死顺延：止损线在封死日触发但不可卖，
// 打开日成交并追加封死罚分（封死判定按逐日滚动前收计算跌停价）。
func TestUniformExitSealedDefers(t *testing.T) {
	// 入场 10（sigIdx=5、入场日 6）；日 7 跌停封死（前收 10 → 板价 9）；日 8 打开收 9.5
	kls := make([]data.KLine, 12)
	for i := range kls {
		kls[i] = mkBarLine(10, 10, 10, 10, 5e7)
	}
	kls[6] = mkBarLine(10, 10, 10, 10, 5e7)     // 入场日
	kls[7] = mkBarLine(9.5, 9.6, 9.0, 9.0, 5e7) // 触板收在板上 = 封死，不可卖
	kls[8] = mkBarLine(9.3, 9.6, 9.2, 9.5, 5e7) // 打开日（前收 9 → 板价 8.1，未触板）
	j, pnl := uniformExitV2Full(kls, "600000.SH", 5, 10, 10, 20, 5, 0, 6, nil, 0, 5, 5, 1, 15)
	if j < 8 {
		t.Fatalf("封死期间不得成交: exitJ=%d", j)
	}
	if pnl >= 0 {
		t.Fatalf("9.5 卖 10 买应为亏损, got %f", pnl)
	}
	// 打开日卖出滑点 5+15=20bp（此前在封死日本会按 -5% 止损线成交）
	want := costRoundTripPnlEx(10, 9.5, 5, 20)
	if !nearly(pnl, want) {
		t.Fatalf("打开日滑点未加罚: %f vs %f", pnl, want)
	}
	// 对照组：门控关闭（sealedExtra=0）时封死日照常成交（旧行为）
	j2, _ := uniformExitV2Full(kls, "600000.SH", 5, 10, 10, 20, 5, 0, 6, nil, 0, 5, 5, 1, 0)
	if j2 != 7 {
		t.Fatalf("门控关闭应在封死日按止损成交: exitJ=%d", j2)
	}
}

// TestFixAmountScale Risk-1：千元口径（均价<1 元）→ ×1000 归一；正常口径不动。
func TestFixAmountScale(t *testing.T) {
	// 千元口径样本：Vol=1万手、Amount=1.2万元 → 均价 0.012 元
	kls := make([]data.KLine, 30)
	for i := range kls {
		kls[i] = data.KLine{Open: 10, High: 10, Low: 10, Close: 10, Volume: 10000, Amount: 12000}
	}
	if !fixAmountScale(kls) {
		t.Fatal("千元口径应被识别")
	}
	if !nearly(kls[0].Amount, 1.2e7) {
		t.Fatalf("归一失败: %f", kls[0].Amount)
	}
	// 正常口径：均价 12 元，不动
	kls2 := make([]data.KLine, 30)
	for i := range kls2 {
		kls2[i] = data.KLine{Volume: 10000, Amount: 1.2e7}
	}
	if fixAmountScale(kls2) {
		t.Fatal("正常口径不应改动")
	}
	// 样本不足不判
	if fixAmountScale(kls2[:2]) {
		t.Fatal("样本 <5 不应改动")
	}
}

// TestAvgAmountWan 滑窗口径：20 根含信号日均值；有效样本 <5 → 0（最差档）。
func TestAvgAmountWan(t *testing.T) {
	kls := make([]data.KLine, 40)
	for i := range kls {
		kls[i] = data.KLine{Amount: 2e7} // 2000 万元
	}
	if got := avgAmountWan(kls, 39); !nearly(got, 2000) {
		t.Fatalf("滑窗均值=%f, want 2000", got)
	}
	// 停牌缺行（Amount=0）：窗口只剩 4 根有效 → 0
	for i := 24; i < 40; i++ {
		kls[i].Amount = 0
	}
	if got := avgAmountWan(kls, 39); got != 0 {
		t.Fatalf("有效样本不足应回退 0, got %f", got)
	}
}
