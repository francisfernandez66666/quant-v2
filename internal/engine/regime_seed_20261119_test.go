// regime_seed_20261119_test.go — §REGIME-SEED（2026-11-19 现网事故）回归锁。
//
// 事故：`lastEmotionPhase` / `lastMarketState` 的唯一写入点是异步 `Run` 流水线，
// 而 5s 打分循环不等它。进程刚重启、或上游超时把某一轮拖长时，定权读到空串 +
// "这一维配置说启用" ⇒ `applyMissingKeyCap` 判盲区 ⇒ 目标封顶 50% 且当天不做 T。
// 现网实测：13:41 重启后 250+ 条决策 MS=0.5，而 `market_risk_daily` 当天那行
// 明明写着 情绪=高潮 / 状态=range。
//
// 这三条用例钉住边界的三个方向：
//  1. 当天有行 ⇒ 当种子（不再空着）；
//  2. 只有昨天的行 ⇒ 不许当种子（昨天的情绪不该给今天定权）；
//  3. 一天只读一次库（缓存），且读失败/无行时保持空（假数据比盲区更贵）。
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// newRegimeSeedEngine 造一个只带研究库的最小引擎（本用例只碰 todayRegimeSeed）。
func newRegimeSeedEngine(t *testing.T) (*Engine, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Engine{d1Store: db}, db
}

// TestRegimeSeedUsesTodayRow 当天有风险档快照 ⇒ 两维都补齐。
func TestRegimeSeedUsesTodayRow(t *testing.T) {
	e, db := newRegimeSeedEngine(t)
	now := time.Date(2026, 11, 19, 13, 45, 0, 0, cntime.Loc)
	today := data.TradingDayDate(now)
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{
		TradeDate: today, Emotion: "高潮", MarketState: "range", RiskTier: "Red",
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	emo, st := e.todayRegimeSeed(now)
	if emo != "高潮" || st != "range" {
		t.Fatalf("当天快照必须当种子，got emotion=%q state=%q", emo, st)
	}
}

// TestRegimeSeedIgnoresOtherDays 只有昨天的行 ⇒ 一律不许当种子。
func TestRegimeSeedIgnoresOtherDays(t *testing.T) {
	e, db := newRegimeSeedEngine(t)
	now := time.Date(2026, 11, 19, 13, 45, 0, 0, cntime.Loc)
	yesterday := data.TradingDayDate(now.AddDate(0, 0, -1))
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{
		TradeDate: yesterday, Emotion: "高潮", MarketState: "bull",
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	emo, st := e.todayRegimeSeed(now)
	if emo != "" || st != "" {
		t.Fatalf("昨天的行不许当今天的种子，got emotion=%q state=%q", emo, st)
	}
}

// TestApplyRegimeSeedsFillsOnlyEmpty 只补空、不覆盖主循环刚算出的新值。
//
// 这条用例对应现网那条真实的覆盖：`overlayDynamicRegime` 用 last*（空）把种子抹掉，
// 于是"种子打了日志但决策仍然半仓"。补完之后必须既能补空、又不能碰非空值。
func TestApplyRegimeSeedsFillsOnlyEmpty(t *testing.T) {
	e, db := newRegimeSeedEngine(t)
	now := time.Date(2026, 11, 19, 14, 1, 0, 0, cntime.Loc)
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{
		TradeDate: data.TradingDayDate(now), Emotion: "高潮", MarketState: "range",
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	empty := signalctl.Regime{}
	e.applyRegimeSeeds(&empty, now)
	if empty.EmotionPhase != "高潮" || empty.MarketState != "range" {
		t.Fatalf("空字段必须补种子，got emotion=%q state=%q", empty.EmotionPhase, empty.MarketState)
	}
	// 非空值绝不许被种子覆盖（主循环刚算出来的新读数优先）。
	live := signalctl.Regime{EmotionPhase: "冰点", MarketState: "bear"}
	e.applyRegimeSeeds(&live, now)
	if live.EmotionPhase != "冰点" || live.MarketState != "bear" {
		t.Fatalf("种子覆盖了主循环新读数：emotion=%q state=%q", live.EmotionPhase, live.MarketState)
	}
}

// TestRegimeSeedReadsStoreOncePerDay 缓存：同一天第二次调用不再读库（库关掉也照样返回）。
func TestRegimeSeedReadsStoreOncePerDay(t *testing.T) {
	e, db := newRegimeSeedEngine(t)
	now := time.Date(2026, 11, 19, 9, 40, 0, 0, cntime.Loc)
	today := data.TradingDayDate(now)
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{TradeDate: today, Emotion: "分歧", MarketState: "bear"}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if emo, _ := e.todayRegimeSeed(now); emo != "分歧" {
		t.Fatalf("第一次读库应拿到 分歧，got %q", emo)
	}
	db.Close() // 库关掉后仍应命中缓存（否则说明每次都在查库）
	emo, st := e.todayRegimeSeed(now)
	if emo != "分歧" || st != "bear" {
		t.Fatalf("同日第二次必须走缓存，got emotion=%q state=%q", emo, st)
	}
}
