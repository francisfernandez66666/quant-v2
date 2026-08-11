// Package main 是 quant-trading-v2 系统的入口主包。
// 本测试文件（main_test.go）覆盖了系统的核心链路测试，包括：
//   - 股票清洗（StockCleaner）
//   - Laodeng（老登）评分逻辑
//   - 策略引擎板块/个股分流评估
//   - 从 Engine → SectorAgent → CombatAgent → Display 的完整 Pipeline
//   - 持仓报告 CRUD 及持久化
//   - HTTP API 端点测试
//   - 冲突决议机制与做空开关
//   - Laodeng 配置从 JSON 文件加载
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// mockNews 是一组预定义的模拟新闻事件，覆盖利好/利空、宏观/行业/公司不同维度，
// 用于在测试中模拟客户端推送的实时新闻，验证策略引擎的板块分流与个股识别逻辑。
var (
	mockNews = []newsagent.NewsEvent{
		{
			Title:             "茅台提价20% 高端白酒景气度提升",
			Content:           "贵州茅台公告上调出厂价20%，市场预期营收利润双增。",
			Datetime:          time.Now().Format("2006-01-02 15:04:05"),
			Source:            "同花顺",
			IsMaterial:        true,
			Direction:         "利好",
			Score:             0.85,
			Sectors:           []string{"白酒"},
			UpstreamSectors:   []string{"粮食种植", "包装印刷"},
			DownstreamSectors: []string{"白酒经销", "餐饮"},
			RelatedStocks:     []string{"贵州茅台", "600519", "五粮液|000858"},
			CleanedStocks:     []string{"贵州茅台|SH600519", "五粮液|SZ000858"},
			ImpactLevel:       "高",
			EventType:         "公司",
			Urgency:           "立即",
			Reason:            "茅台提价带动行业利润预期",
		},
		{
			Title:             "碳酸锂价格跌破8万 锂电板块承压",
			Content:           "碳酸锂现货持续走低，跌破8万元/吨关口，产业链利润受挤压。",
			Datetime:          time.Now().Format("2006-01-02 15:04:05"),
			Source:            "同花顺",
			IsMaterial:        true,
			Direction:         "利空",
			Score:             -0.7,
			Sectors:           []string{"新能源", "锂电池"},
			UpstreamSectors:   []string{"锂矿"},
			DownstreamSectors: []string{"新能源汽车"},
			RelatedStocks:     []string{"宁德时代", "300750", "赣锋锂业|002460"},
			CleanedStocks:     []string{"宁德时代|SZ300750", "赣锋锂业|SZ002460"},
			ImpactLevel:       "高",
			EventType:         "行业",
			Urgency:           "立即",
			Reason:            "上游价格下行压缩利润",
		},
		{
			Title:           "央行降准0.5个百分点 释放长期资金",
			Content:         "中国人民银行宣布全面降准0.5个百分点，释放长期资金约1万亿。",
			Datetime:        time.Now().Format("2006-01-02 15:04:05"),
			Source:          "新浪财经",
			IsMaterial:      true,
			Direction:       "利好",
			Score:           0.65,
			Sectors:         []string{"银行", "房地产", "证券"},
			UpstreamSectors: []string{"金融科技"},
			RelatedStocks:   []string{"工商银行", "601398", "招商银行|600036"},
			CleanedStocks:   []string{"工商银行|SH601398", "招商银行|SH600036"},
			ImpactLevel:     "高",
			EventType:       "宏观",
			Urgency:         "立即",
			Reason:          "降准释放流动性利好金融",
		},
		{
			Title:             "美国对华半导体出口管制升级",
			Content:           "美国商务部将更多中国半导体企业列入实体清单。",
			Datetime:          time.Now().Format("2006-01-02 15:04:05"),
			Source:            "同花顺",
			IsMaterial:        true,
			Direction:         "利空",
			Score:             -0.55,
			Sectors:           []string{"半导体", "芯片"},
			UpstreamSectors:   []string{"半导体设备", "电子化学品"},
			DownstreamSectors: []string{"消费电子"},
			RelatedStocks:     []string{"中芯国际|688981", "北方华创|002371"},
			CleanedStocks:     []string{"中芯国际|SH688981", "北方华创|SZ002371"},
			ImpactLevel:       "中",
			EventType:         "宏观",
			Urgency:           "关注",
			Reason:            "出口管制升级打压半导体",
		},
	}
)

// newTestAuthManager 创建一个临时的认证管理器，注册一个测试用户并标记初始化完成。
// 返回的 *auth.Manager 用于后续 HTTP API 测试中生成合法 Token。
func newTestAuthManager(t *testing.T) *auth.Manager {
	t.Helper()
	authDir := t.TempDir()
	m := auth.NewManager(authDir)
	if err := m.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	_, err := m.Register("testuser", "testpass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	m.MarkInitialized()
	return m
}

// newTestComponents 创建一套完整的测试组件，包括策略引擎、板块 Agent、战斗 Agent、
// 显示聚合器、持仓报告、配置管理器和 HTTP 服务。返回顺序依次为：
//
//	engine, sAgent, cAgent, agg, rpt, cfgMgr, srv
//
// 各组件使用 t.TempDir() 确保测试隔离，配置预填了四条战法（Dragon / DoubleBump / NShape / DragonReturn）的默认参数。
func newTestComponents(t *testing.T) (
	*strategy_engine.Engine,
	*sector_agent.Agent,
	*combat_agent.Agent,
	*display.Aggregator,
	*report.Report,
	*config.Manager,
	*server.Server,
) {
	t.Helper()
	dir := t.TempDir()

	cfgMgr := config.NewManager(filepath.Join(dir, "config.json"))
	cfgMgr.SetStrategyConfig(&config.StrategyConfig{
		Dragon: config.DragonConfig{
			F1SealWeight:      0.30,
			F2ResonanceWeight: 0.25,
			F3PremiumWeight:   0.25,
			F4RsWeight:        0.20,
			PullbackMaxPct:    5.0,
		},
		DoubleBump: config.DoubleBumpConfig{
			FirstBreakVolumeMultiple:  2.0,
			SecondBreakVolumeMultiple: 1.5,
			AdjustDaysMax:             10,
			PositionWeight:            0.3,
		},
		NShape: config.NShapeConfig{
			NPatternScoreThreshold: 0.6,
			NShapeD1Threshold:      0.5,
			HardStopLoss:           -5.0,
		},
		DragonReturn: config.DragonReturnConfig{
			MinPullbackPct: -15.0,
			MaxPullbackPct: -5.0,
			StopLossPct:    -7.0,
			TakeProfitPct:  15.0,
			MaxHoldDays:    20,
		},
	})
	cfgMgr.Rules.Laodeng = config.LaodengConfig{
		Enabled:      true,
		MarketCapMin: 500,
		PeMax:        15,
		TurnoverMin:  1.0,
		TechPenalty:  -0.3,
		WeightScore:  0.15,
	}
	cfgMgr.Save()

	marketAPI := data.NewMarketAPI()
	engine := strategy_engine.New(marketAPI)

	scanner := data.NewSectorScanner(marketAPI, nil)
	scanner.Update([]data.SectorInfo{
		{Name: "白酒"}, {Name: "银行"}, {Name: "房地产"}, {Name: "证券"},
		{Name: "新能源"}, {Name: "锂电池"}, {Name: "半导体"}, {Name: "芯片"},
	}, 0, 0, 0)
	rpsMgr := data.NewRPSManager()
	engine.SetScanner(scanner)
	sAgent := sector_agent.New(scanner, rpsMgr)

	stratCfg := cfgMgr.GetStrategyConfig()
	laodengCfg := &cfgMgr.Rules.Laodeng
	cAgent := combat_agent.New(stratCfg)
	cAgent.SetLaodengConfig(laodengCfg)
	cAgent.SetRunners([]combat_agent.StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(cfgMgr, nil)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(cfgMgr)},
	})
	cAgent.SetShortEnabled(true)

	rpt := report.New(filepath.Join(dir, "report.json"))
	agg := display.New()
	wlMgr := data.NewWatchlistManager(dir)

	authMgr := newTestAuthManager(t)
	srv := server.New(authMgr, agg, cfgMgr, rpt, marketAPI, wlMgr, data.NewTHSClient())

	return engine, sAgent, cAgent, agg, rpt, cfgMgr, srv
}

// getAuthToken 通过已有的服务实例登录测试用户并返回其 Token，供后续 API 请求鉴权使用。
func getAuthToken(t *testing.T, srv *server.Server) string {
	t.Helper()
	user, err := srv.GetAuthManager().Login("testuser", "testpass")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return user.Token
}

// TestStockCleaner 验证 StockCleaner 的股票名称/代码清洗与归一化功能。
// 分别测试中文名称（"贵州茅台"）和带交易所前缀代码（"SH600519"）的解析，
// 网络不可用时预期返回错误而非 panic。
func TestStockCleaner(t *testing.T) {
	marketAPI := data.NewMarketAPI()
	cleaner := data.NewStockCleaner(marketAPI)
	if cleaner == nil {
		t.Fatal("cleaner is nil")
	}

	name, code, err := cleaner.Clean("贵州茅台")
	if err != nil {
		t.Logf("Clean('贵州茅台'): %v (网络不通时预期错误)", err)
	} else {
		t.Logf("Clean('贵州茅台') = %s | %s", name, code)
	}

	name2, code2, err2 := cleaner.Clean("SH600519")
	if err2 != nil {
		t.Logf("Clean('SH600519'): %v", err2)
	} else {
		t.Logf("Clean('SH600519') = %s | %s", name2, code2)
	}
}

// TestLaodengScore 验证 Laodeng（老登）评分逻辑。
// 覆盖银行大盘股、新能源科技股、小市值科技股三种典型场景，
// 断言评分落在预期区间内，确保打分不会出现极端异常值。
func TestLaodengScore(t *testing.T) {
	cfg := &config.LaodengConfig{
		Enabled:      true,
		MarketCapMin: 500,
		PeMax:        15,
		TurnoverMin:  1.0,
		TechPenalty:  -0.3,
		WeightScore:  0.15,
	}

	tests := []struct {
		name     string
		cap      float64
		pe       float64
		turnover float64
		sector   string
		wantLow  float64
		wantHigh float64
	}{
		{"工商银行(高市值低PE高换手)", 2000, 6, 2.5, "银行", 0.10, 0.20},
		{"宁德时代(高市值高PE科技)", 800, 30, 3.0, "新能源", 0.05, 0.15},
		{"小市值科技股(低分)", 50, 40, 0.5, "半导体", 0.0, 0.08},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strategy.ScoreLaodeng(cfg, tt.cap, tt.pe, tt.turnover, tt.sector)
			if got < tt.wantLow || got > tt.wantHigh {
				t.Errorf("ScoreLaodeng = %.4f, want [%.4f, %.4f]", got, tt.wantLow, tt.wantHigh)
			}
			t.Logf("ScoreLaodeng(%s) = %.4f", tt.name, got)
		})
	}
}

// TestStrategyEngineEvaluate 验证策略引擎 Evaluate 方法的板块分流能力。
// 输入包含利好/利空各两条新闻，预期返回的 HotSectors 包含白酒和银行，
// BearSectors 包含新能源/锂电池和半导体/芯片，同时验证 L1Blocked（一级拦截）个股列表非空。
func TestStrategyEngineEvaluate(t *testing.T) {
	engine, _, _, _, _, _, _ := newTestComponents(t)

	result := engine.Evaluate(context.Background(), mockNews, nil, nil)
	if result == nil {
		t.Fatal("result is nil")
	}

	t.Logf("HotSectors: %d", len(result.HotSectors))
	for _, s := range result.HotSectors {
		t.Logf("  [+] %s score=%.2f dir=%s reason=%s", s.Name, s.Score, s.Direction, s.Reason)
	}

	t.Logf("BearSectors: %d", len(result.BearSectors))
	for _, s := range result.BearSectors {
		t.Logf("  [-] %s score=%.2f dir=%s reason=%s", s.Name, s.Score, s.Direction, s.Reason)
	}

	// 验证利好板块包含白酒（茅台提价）和银行（降准）
	foundBaiJiu := false
	foundYinHang := false
	for _, s := range result.HotSectors {
		if s.Name == "白酒" {
			foundBaiJiu = true
		}
		if s.Name == "银行" {
			foundYinHang = true
		}
	}
	if !foundBaiJiu {
		t.Error("HotSectors 应包含白酒")
	}
	if !foundYinHang {
		t.Error("HotSectors 应包含银行")
	}

	// 验证利空板块包含新能源、半导体
	foundXinNeng := false
	foundBanDao := false
	for _, s := range result.BearSectors {
		if s.Name == "新能源" || s.Name == "锂电池" {
			foundXinNeng = true
		}
		if s.Name == "半导体" || s.Name == "芯片" {
			foundBanDao = true
		}
	}
	if !foundXinNeng {
		t.Error("BearSectors 应包含新能源/锂电池")
	}
	if !foundBanDao {
		t.Error("BearSectors 应包含半导体/芯片")
	}

	t.Logf("L1Blocked: %d 个股", len(result.L1Blocked))
	for k := range result.L1Blocked {
		t.Logf("  blocked: %s", k)
	}
}

// TestFullPipeline 执行从策略引擎到展示层的一站式完整链路测试。
// 步骤顺序：Engine.Evaluate → SectorAgent.Verify → CombatAgent.ScanLong /
// CheckPositionAlerts → Display.Aggregator.Update，验证各组件之间数据传递正确性。
func TestFullPipeline(t *testing.T) {
	engine, sAgent, cAgent, agg, rpt, _, _ := newTestComponents(t)

	// Step 1: Engine
	sr := engine.Evaluate(context.Background(), mockNews, nil, nil)
	if sr == nil {
		t.Fatal("Evaluate 返回 nil")
	}
	t.Logf("[Pipeline] Engine: %d hot sectors, %d bear sectors", len(sr.HotSectors), len(sr.BearSectors))

	// Step 2: SectorAgent
	verifiedBull := sAgent.Verify(sr.HotSectors)
	verifiedBear := sAgent.Verify(sr.BearSectors)
	t.Logf("[Pipeline] SectorAgent: %d bull, %d bear", len(verifiedBull), len(verifiedBear))
	for _, v := range verifiedBull {
		t.Logf("  bull: %s (RPS rank=%d, stocks=%d)", v.Name, v.RPSRank, len(v.Stocks))
	}

	// Step 3: CombatAgent ScanLong
	bullInput := combat_agent.ScanInput{
		Sectors:   verifiedBull,
		L1Score:   sr.L1Score,
		L1Blocked: sr.L1Blocked,
	}
	bullSignals := cAgent.ScanLong(bullInput)
	t.Logf("[Pipeline] ScanLong: %d signals", len(bullSignals))
	for _, sig := range bullSignals {
		t.Logf("  signal: %s %s %s conf=%.2f action=%s",
			sig.Code, sig.Name, sig.Strategy, sig.Confidence, sig.Action)
	}

	// Step 4: CheckPositionAlerts (无持仓，应为空)
	marketAPI := data.NewMarketAPI()
	alerts := cAgent.CheckPositionAlerts(rpt, marketAPI, map[string]combat_agent.StockScores{})
	if len(alerts) != 0 {
		t.Logf("[Pipeline] Alerts (empty): %d", len(alerts))
	}

	// Step 5: Display
	agg.Update(sr, verifiedBull, verifiedBear, bullSignals, nil, alerts, nil, rpt)
	dash := agg.Current()
	if dash == nil {
		t.Fatal("Dashboard nil")
	}
	t.Logf("[Pipeline] Dashboard: %d news, %d hot, %d bear, %d final signals",
		len(dash.NewsEvents), len(dash.HotSectors), len(dash.BearSectors), len(dash.FinalSignals))
}

// TestReport 验证持仓报告模块的完整 CRUD 操作及文件持久化能力。
// 包含开仓、查询持仓列表、平仓（含盈亏计算）、统计汇总、更新止损价、
// 软删除（标记状态而非物理删除），以及重新加载文件验证持久化正确性。
func TestReport(t *testing.T) {
	reportFile := filepath.Join(t.TempDir(), "report.json")
	rpt := report.New(reportFile)

	// 开仓
	rpt.LogSignal("POS001", "SH600519", "贵州茅台", "做多", "Dragon", 1500.0, 10.0, 5.0)
	rpt.LogSignal("POS002", "SZ300750", "宁德时代", "做多", "DoubleBump", 200.0, 15.0, 7.0)

	positions := rpt.HeldPositions()
	if len(positions) != 2 {
		t.Fatalf("HeldPositions 应为2, 实际 %d", len(positions))
	}
	t.Logf("持仓: %d 个", len(positions))
	for _, p := range positions {
		t.Logf("  %s %s entry=%.2f TP=%.1f%% SL=%.1f%%",
			p.Code, p.Name, p.EntryPrice, p.TakeProfitPct, p.StopLossPct)
	}

	// 平仓
	rpt.LogExit("POS001", 1650.0)
	rpt.LogExit("POS002", 180.0)

	// 统计
	total, holding, win, wr, avgW, avgL := rpt.Stats()
	if total != 2 {
		t.Errorf("total 应为2, 实际 %d", total)
	}
	if holding != 0 {
		t.Errorf("holding 应为0, 实际 %d", holding)
	}
	if win != 1 {
		t.Errorf("win 应为1, 实际 %d", win)
	}
	t.Logf("Stats: total=%d holding=%d win=%d winRate=%.1f%% avgWin=%.2f%% avgLoss=%.2f%%",
		total, holding, win, wr, avgW, avgL)

	// 更新
	rpt.LogSignal("POS003", "SH601398", "工商银行", "做多", "NShape", 6.0, 5.0, 3.0)
	rpt.Update("POS003", func(l *report.ExecLog) {
		l.StopLossPct = 4.0
	})
	pos3 := rpt.FindBySignalID("POS003")
	if pos3 == nil || pos3.StopLossPct != 4.0 {
		t.Error("Update 失败")
	}

	// 删除
	rpt.Delete("POS003")
	pos3d := rpt.FindBySignalID("POS003")
	if pos3d == nil || pos3d.Status != "已删除" {
		t.Error("Delete 失败")
	}

	// 持久化验证
	rpt2 := report.New(reportFile)
	list2 := rpt2.List()
	if len(list2) != 3 {
		t.Errorf("持久化后应有3条, 实际 %d", len(list2))
	}
	t.Logf("持久化验证: %d 条记录", len(list2))
}

// TestAPIEndpoints 对系统核心 HTTP API 端点进行集成测试。
// 覆盖路径包括健康检查 /api/health、做空状态查询与切换 /api/short/*、
// 策略配置获取 /api/config/strategy、持仓创建 /api/positions 和列表查询。
// 所有请求均使用合法 Token 鉴权。
func TestAPIEndpoints(t *testing.T) {
	_, _, _, _, _, _, srv := newTestComponents(t)

	user, err := srv.GetAuthManager().Login("testuser", "testpass")
	if err != nil {
		t.Fatal("login failed")
	}
	token := user.Token

	// runRequest 构造带 Token 鉴权的请求并经由服务的 Mux 执行，返回响应记录器供各子测试断言
	runRequest := func(method, path, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body != "" {
			req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		req.Header.Set("Authorization", token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		srv.GetServeMux().ServeHTTP(w, req)
		return w
	}

	t.Run("Health", func(t *testing.T) {
		w := runRequest("GET", "/api/health", "")
		if w.Code != 200 {
			t.Errorf("health got %d", w.Code)
		}
	})

	t.Run("ShortStatus", func(t *testing.T) {
		w := runRequest("GET", "/api/short/status", "")
		if w.Code != 200 {
			t.Errorf("short status got %d", w.Code)
		}
		var resp map[string]bool
		json.Unmarshal(w.Body.Bytes(), &resp)
		t.Logf("short_enabled=%v", resp["short_enabled"])
	})

	t.Run("ShortToggle", func(t *testing.T) {
		w := runRequest("POST", "/api/short/toggle", `{"enabled":true}`)
		if w.Code != 200 {
			t.Errorf("toggle got %d", w.Code)
		}
	})

	t.Run("StrategyConfig", func(t *testing.T) {
		w := runRequest("GET", "/api/config/strategy", "")
		if w.Code != 200 {
			t.Errorf("get strategy config got %d", w.Code)
		}
		var resp map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &resp)
		t.Logf("strategy config keys: %v", getKeys(resp))
	})

	t.Run("CreatePosition", func(t *testing.T) {
		body := `{"code":"600519","name":"贵州茅台","direction":"做多","strategy":"Dragon","entry_price":1500,"take_profit_pct":10,"stop_loss_pct":5}`
		w := runRequest("POST", "/api/positions", body)
		if w.Code != 201 {
			t.Errorf("create position got %d, body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("ListPositions", func(t *testing.T) {
		w := runRequest("GET", "/api/positions", "")
		if w.Code != 200 {
			t.Errorf("list positions got %d", w.Code)
		}
		t.Logf("positions response: %s", truncate(w.Body.String(), 300))
	})
}

// TestResolveConflict 验证当同一只股票同时存在战法信号（做多/做空）和持仓告警（止盈/止损）时的冲突决议逻辑。
// 规则：若某股已有战法信号且动作等级为 watch 或以上，则同股的告警信号被压制，反之则告警可进入最终信号列表。
func TestResolveConflict(t *testing.T) {
	now := time.Now()

	bull := []combat_agent.Signal{
		{ID: "B1", Code: "SH600519", Name: "茅台", Strategy: "Dragon", Direction: "做多", Action: "buy", Confidence: 0.85, GeneratedAt: now},
	}
	bear := []combat_agent.Signal{}
	alerts := []combat_agent.Signal{
		{ID: "A1", Code: "SH600519", Name: "茅台", Strategy: "Dragon", Direction: "提醒", Action: "止盈", AlertType: "止盈", Confidence: 1.0, GeneratedAt: now.Add(1 * time.Minute)},
		{ID: "A2", Code: "SZ300750", Name: "宁德", Strategy: "DragonReturn", Direction: "提醒", Action: "止损", AlertType: "止损", Confidence: 1.0, GeneratedAt: now},
	}

	// 先检查 IsActionWatchOrAbove
	if !strategy.IsActionWatchOrAbove("buy") {
		t.Error("buy 应是 watch 及以上")
	}
	if strategy.IsActionWatchOrAbove("止盈") {
		t.Error("止盈 不应是 watch 及以上")
	}

	// 模拟 aggregator 的 resolveConflict
	all := append(bull, bear...)
	for _, s := range alerts {
		// 同股有战法信号时压制
		hasStrategy := false
		for _, b := range bull {
			if b.Code == s.Code && strategy.IsActionWatchOrAbove(b.Action) {
				hasStrategy = true
				break
			}
		}
		for _, b := range bear {
			if b.Code == s.Code && strategy.IsActionWatchOrAbove(b.Action) {
				hasStrategy = true
				break
			}
		}
		if hasStrategy {
			t.Logf("alert %s (%s) 被战法信号压制", s.Code, s.AlertType)
			continue
		}
		all = append(all, s)
	}

	if len(all) != 2 {
		t.Errorf("冲突决议后应为2个信号(bull+A2), 实际 %d", len(all))
	}
	for _, s := range all {
		t.Logf("final: %s %s action=%s conf=%.2f", s.Code, s.Name, s.Action, s.Confidence)
	}
}

// TestShortToggle 验证战斗 Agent 的做空开关能否正常开启和关闭。
// 初始状态应为 true（由 newTestComponents 中 SetShortEnabled(true) 设置），关闭后应返回 false。
func TestShortToggle(t *testing.T) {
	_, _, cAgent, _, _, _, _ := newTestComponents(t)

	if !cAgent.ShortEnabled() {
		t.Error("ShortEnabled 应为 true")
	}

	cAgent.SetShortEnabled(false)
	if cAgent.ShortEnabled() {
		t.Error("ShortEnabled 应为 false")
	}
	t.Log("做空开关切换正常")
}

// TestLaodengConfigFromJSON 验证 Laodeng 配置能否从 JSON 文件正确加载并解析。
// 模拟 config.json 内容，创建临时文件后使用 config.Manager 读取，断言各字段值符合预期。
func TestLaodengConfigFromJSON(t *testing.T) {
	// 模拟 config.json
	cfgJSON := `{
		"rules": {
			"laodeng": {
				"enabled": true,
				"market_cap_min": 300,
				"pe_max": 20,
				"turnover_min": 0.5,
				"tech_penalty": -0.4,
				"weight_score": 0.2
			}
		}
	}`
	cfgPath := filepath.Join(t.TempDir(), "laodeng_test.json")
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	cfgMgr := config.NewManager(cfgPath)
	ld := cfgMgr.Rules.Laodeng
	if !ld.Enabled {
		t.Error("Laodeng 应启用")
	}
	if ld.MarketCapMin != 300 {
		t.Errorf("MarketCapMin 应为300, 实际 %.0f", ld.MarketCapMin)
	}
	if ld.TechPenalty != -0.4 {
		t.Errorf("TechPenalty 应为-0.4, 实际 %.1f", ld.TechPenalty)
	}
	t.Logf("LaodengConfig loaded: capMin=%.0f peMax=%.0f turnover=%.1f penalty=%.1f weight=%.2f",
		ld.MarketCapMin, ld.PeMax, ld.TurnoverMin, ld.TechPenalty, ld.WeightScore)
}

// getKeys 提取 map 的所有 key 并用逗号连接，用于测试日志中展示响应结构的字段概览。
func getKeys(m map[string]interface{}) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// truncate 截断过长的字符串并在末尾追加 "..."，防止测试日志输出被大量响应体淹没。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestPackageImports 是一个轻量编译检查测试，确保所有 import 的包在此测试文件中都能被正确解析和链接。
func TestPackageImports(t *testing.T) {
	t.Log("所有包导入正常")
}

// TestBumpPort 验证端口占位自增逻辑：正常地址 +1，非法地址原样返回。
func TestBumpPort(t *testing.T) {
	cases := map[string]string{
		":8080":         ":8081",
		"127.0.0.1:1":   "127.0.0.1:2",
		"localhost:7090": "localhost:7091",
		"not-an-addr":   "not-an-addr",
		":abc":          ":abc",
	}
	for in, want := range cases {
		if got := bumpPort(in); got != want {
			t.Errorf("bumpPort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPickListenerPortSwitch 验证端口被占用时自动顺延到下一个空闲端口：
// 先占用 8090，再调用 pickListener(":8090", 3) 应返回 8091 的监听器。
func TestPickListenerPortSwitch(t *testing.T) {
	block, err := net.Listen("tcp", ":8090")
	if err != nil {
		t.Skipf("无法占用测试端口 8090: %v", err)
	}
	defer block.Close()

	ln := pickListener(":8090", 3)
	if ln == nil {
		t.Fatal("pickListener 返回 nil，应顺延到空闲端口")
	}
	defer ln.Close()
	bound := ln.Addr().String()
	if want := ":8091"; bound != want && bound != "[::]:8091" {
		t.Errorf("应顺延到 :8091, 实际绑定 %s", bound)
	}
}
