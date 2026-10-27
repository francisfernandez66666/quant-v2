// t_shadow_no_t.go — §T-COST B5（2026-10-27）"做 T vs 不做 T"影子对照账。
//
// 目的：用**对照**而不是体感回答"做 T 到底值不值"。影子账与真实账吃同一批信号、同一份资金、
// 同一套仓位模型，唯一差别是**不执行 T 腿**；两条权益曲线之差就是当日 T 的归因。
//
// 为什么影子账必须独立一本（方案 §10 风险 1）：只要它与真实账共用过一次"T 腿带来的现金/持仓变化"，
// 归因就会恒 0 或虚高——那不是对照，是自欺。所以本文件只做三件事：
//  1. 镜像**非 T** 成交（策略买入、卖出裁决执行），按调用点显式调用（绝不在共享原语里靠
//     `strategy == "§T-MODE"` 猜：T 腿的战法名是留痕常量，靠它猜等于把两件事焊死）；
//  2. T 腿只记"跳过了几条"（skipped_t_legs，判据：应等于当日 T 腿数）；
//  3. 每日一条 equity_series（≤240 点），落盘 <dataDir>/accounts/<uid>/t_shadow_no_t.json（原子写）。
//
// 铁律：影子读数**不得被任何判定读取**。Room/预算/风控零引用，只有本文件的读数组装与落盘能碰它；
// 这条由 t_shadow_no_t_test.go 的静态扫描兜底（不是靠自觉）。
//
// English: the "no-T" shadow book for B5. Same signals/funds/sizing, only T legs are skipped; the
// difference between the two equity curves is the T attribution. Mirroring is explicit at call sites
// (never inferred from the §T-MODE strategy name). The shadow readout is never consumed by Room,
// budget or risk code — enforced by a static scan in the test file.
package paper

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/cntime"
)

// tShadowNoTFile 影子账文件名（与 t_rounds_live.json 同根：accounts/<uid>/ 下）。
const tShadowNoTFile = "t_shadow_no_t.json"

// tShadowMaxPointsPerDay equity_series 每日点数上限（约 5s 一轮 × 4 小时 ≈ 2880，240 点足够画形状）。
const tShadowMaxPointsPerDay = 240

// tShadowPos 影子持仓：cost 是**每股均价**（与 Position.CostPrice 同口径），不是总成本。
type tShadowPos struct {
	Qty  int     `json:"qty"`
	Cost float64 `json:"cost"`
}

// tShadowEquityPoint 影子账的一个净值点。
// Real 是**同一时刻**真实账的长仓权益（现金+持仓市值）：归因曲线必须同刻两账，
// 只在读接口时用"当前"真实权益去配历史影子点，会把曲线画成两条时间错位的线。
type tShadowEquityPoint struct {
	Ts     string  `json:"ts"`
	Equity float64 `json:"equity"` // 影子账权益
	Real   float64 `json:"real"`   // 同刻真实账权益（长仓口径）
}

// tShadowFill 影子账的非 T 成交（对照判据 1 的逐笔证据：同码/方向/数量/价格）。
type tShadowFill struct {
	At    string  `json:"at"`
	Code  string  `json:"code"`
	Side  string  `json:"side"`
	Qty   int     `json:"qty"`
	Price float64 `json:"price"`
}

// tShadowNoTBook 影子账本体（与真实账并列，不共享任何可变状态）。
type tShadowNoTBook struct {
	Day        string                `json:"day"`
	BaseEquity float64               `json:"base_equity"` // 当日首帧真实权益（归因百分比的分母）
	Cash       float64               `json:"cash"`
	Positions  map[string]tShadowPos `json:"positions"`
	EquitySer  []tShadowEquityPoint  `json:"equity_series"`
	Mirrored   int                   `json:"mirrored"`
	SkippedLeg int                   `json:"skipped_t_legs"`
	// Fills 非 T 成交流水（对照判据 1 的证据面；真实账那边用 trades 里非 §T-MODE 的行比对）。
	Fills []tShadowFill `json:"fills,omitempty"`
}

// shadowNoTPath 影子账文件路径（账本无路径＝纯内存测试账本，返回空串表示不落盘）。
func (e *Engine) shadowNoTPath() string {
	if e.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(e.path), tShadowNoTFile)
}

// ensureShadowNoTLocked 确保影子账是**当日**的（须持锁调用）。
// 初始化口径：以真实账当前现金+持仓为起点（同资金同仓位），当日首帧权益记 base_equity；
// 同一天重启则从文件恢复（行情/持仓都来自真实账，恢复只补回当日已发生的镜像历史）。
func (e *Engine) ensureShadowNoTLocked(now time.Time) *tShadowNoTBook {
	day := cntime.DayCompactOf(now)
	if e.shadowNoT != nil && e.shadowNoT.Day == day {
		return e.shadowNoT
	}
	book := &tShadowNoTBook{
		Day:       day,
		Cash:      e.cash,
		Positions: map[string]tShadowPos{},
	}
	mv := 0.0
	for code, p := range e.positions {
		if p == nil || p.Qty <= 0 {
			continue
		}
		book.Positions[code] = tShadowPos{Qty: p.Qty, Cost: p.CostPrice}
		mark := p.Mark
		if mark <= 0 {
			mark = p.CostPrice
		}
		mv += mark * float64(p.Qty)
	}
	book.BaseEquity = round2(e.cash + mv)
	// 同日重启恢复：只认同一交易日的文件；跨日文件保留（只读审计），但不参与今天的镜像。
	if path := e.shadowNoTPath(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var old tShadowNoTBook
			if uerr := json.Unmarshal(b, &old); uerr == nil && old.Day == day && old.Positions != nil {
				book = &old
			}
		}
	}
	e.shadowNoT = book
	e.tShadowAppendPointLocked(now)
	e.tShadowPersistLocked()
	return book
}

// tShadowEquityLocked 影子账当前权益＝现金 + Σ持仓×真实账最新标记价（须持锁调用）。
// 标记价刻意取真实账的 Mark：影子持仓与真实持仓在非 T 部分完全一致，单独维护一份报价只会引入漂移。
func (e *Engine) tShadowEquityLocked() float64 {
	if e.shadowNoT == nil {
		return 0
	}
	eq := e.shadowNoT.Cash
	for code, sp := range e.shadowNoT.Positions {
		if sp.Qty == 0 {
			continue
		}
		px := sp.Cost
		if p := e.positions[code]; p != nil && p.Mark > 0 {
			px = p.Mark
		}
		if p := e.positions[code]; p != nil && px <= 0 {
			px = p.CostPrice
		}
		eq += px * float64(sp.Qty)
	}
	return eq
}

// tShadowAppendPointLocked 落一个净值点（上限 240；满了替换最后一点，保证曲线末端总是最新）。
func (e *Engine) tShadowAppendPointLocked(now time.Time) {
	if e.shadowNoT == nil {
		return
	}
	realEquity := e.cash + e.marketValueLocked()
	pt := tShadowEquityPoint{
		Ts:     now.Format(time.RFC3339),
		Equity: round2(e.tShadowEquityLocked()),
		Real:   round2(realEquity),
	}
	if len(e.shadowNoT.EquitySer) >= tShadowMaxPointsPerDay {
		e.shadowNoT.EquitySer[len(e.shadowNoT.EquitySer)-1] = pt
		return
	}
	e.shadowNoT.EquitySer = append(e.shadowNoT.EquitySer, pt)
}

// tShadowPersistLocked 原子写影子账（tmp+rename）。失败只告警：影子是观察面，丢一点不该拖停交易。
func (e *Engine) tShadowPersistLocked() {
	path := e.shadowNoTPath()
	if path == "" || e.shadowNoT == nil {
		return
	}
	b, err := json.MarshalIndent(e.shadowNoT, "", "  ")
	if err != nil {
		log.Printf("[paper] §T-SHADOW 影子账序列化失败（不参与判定）：%v", err)
		return
	}
	if dir := filepath.Dir(path); dir != "" {
		if merr := os.MkdirAll(dir, 0o755); merr != nil {
			log.Printf("[paper] §T-SHADOW 影子账建目录失败：%v", merr)
			return
		}
	}
	tmp := path + ".tmp"
	if werr := os.WriteFile(tmp, b, 0o644); werr != nil {
		log.Printf("[paper] §T-SHADOW 影子账写临时文件失败：%v", werr)
		return
	}
	if rerr := os.Rename(tmp, path); rerr != nil {
		log.Printf("[paper] §T-SHADOW 影子账原子替换失败：%v", rerr)
	}
}

// SetShadowNoT 注入"不做 T 影子账"开关（账号级 sizing.t.shadow_no_t，热生效）。
// 只影响镜像与采样，不影响任何判定；关掉即停镜像，已落盘文件保留只读（方案 §3.5 回滚）。
func (e *Engine) SetShadowNoT(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shadowNoTOn = on
	if on {
		e.ensureShadowNoTLocked(e.clockNow())
	}
}

// tShadowMirrorBuyLocked 镜像一笔**非 T** 买入（策略信号/手动建仓/加仓；调用方显式调用）。
//
// 现金允许为负：影子账比真实账少赚的正是"被跳过的 T 腿"带来的现金，若因此缩小镜像数量，
// 就破坏了对照成立的唯一前提——非 T 成交逐笔相等。宁可让现金为负，也不改数量。
func (e *Engine) tShadowMirrorBuyLocked(code string, price float64, qty int, now time.Time) {
	if !e.shadowNoTOn || qty <= 0 || price <= 0 {
		return
	}
	book := e.ensureShadowNoTLocked(now)
	fee := e.buyFeeLocked(price * float64(qty))
	book.Cash -= price*float64(qty) + fee
	pos := book.Positions[code]
	if pos.Qty < 0 {
		pos.Qty = 0 // 影子账不做空：负持仓只会来自镜像卖出越界，按 0 收口
	}
	totalCost := pos.Cost*float64(pos.Qty) + price*float64(qty)
	pos.Qty += qty
	if pos.Qty > 0 {
		pos.Cost = totalCost / float64(pos.Qty)
	}
	book.Positions[code] = pos
	book.Mirrored++
	book.Fills = append(book.Fills, tShadowFill{At: now.Format(time.RFC3339), Code: code, Side: "buy", Qty: qty, Price: round4(price)})
	e.tShadowAppendPointLocked(now)
	e.tShadowPersistLocked()
}

// tShadowMirrorSellLocked 镜像一笔**非 T** 卖出（止盈/止损/减仓/清仓；调用方显式调用）。
// price 必须是**已含卖出滑点**的成交价（与真实 trades 里的 Price 同口径），否则判据 1 会假红。
func (e *Engine) tShadowMirrorSellLocked(code string, price float64, qty int, now time.Time) {
	if !e.shadowNoTOn || qty <= 0 || price <= 0 {
		return
	}
	book := e.ensureShadowNoTLocked(now)
	pos := book.Positions[code]
	if pos.Qty < qty {
		qty = pos.Qty // 镜像数量不得超过影子持仓（真实账多卖的部分必然来自 T 腿，不该进对照）
	}
	if qty <= 0 {
		return
	}
	fee := e.sellFeeLocked(price * float64(qty))
	book.Cash += price*float64(qty) - fee
	pos.Qty -= qty
	if pos.Qty == 0 {
		delete(book.Positions, code)
	} else {
		book.Positions[code] = pos
	}
	book.Mirrored++
	book.Fills = append(book.Fills, tShadowFill{At: now.Format(time.RFC3339), Code: code, Side: "sell", Qty: qty, Price: round4(price)})
	e.tShadowAppendPointLocked(now)
	e.tShadowPersistLocked()
}

// tShadowSkipTLegLocked 记一条被跳过的 T 腿（调用点在 T 腿真实成交之后，**只数成交的腿**）。
// 判据：skipped_t_legs == 当日 T 腿数（首腿 + 配对腿）；多跳＝误把策略单当 T，少跳＝漏镜像的证据。
func (e *Engine) tShadowSkipTLegLocked(n int, now time.Time) {
	if !e.shadowNoTOn || n <= 0 {
		return
	}
	book := e.ensureShadowNoTLocked(now)
	book.SkippedLeg += n
	e.tShadowPersistLocked()
}

// tShadowSampleLocked 每轮估值落一个点（由 Snapshot 调用）。
func (e *Engine) tShadowSampleLocked(now time.Time) {
	if !e.shadowNoTOn {
		return
	}
	e.ensureShadowNoTLocked(now)
	e.tShadowAppendPointLocked(now)
	e.tShadowPersistLocked()
}

// TShadowNoTSeriesPoint 接口用的双线点。
type TShadowNoTSeriesPoint struct {
	Ts     string  `json:"ts"`
	Real   float64 `json:"real"`
	Shadow float64 `json:"shadow"`
}

// TShadowNoTReadout 影子对照的只读一帧（供 /api/paper/t-rounds 的 shadow_no_t 段）。
type TShadowNoTReadout struct {
	Day       string `json:"day"`
	Available bool   `json:"available"`
	// Reason available=false 的原因（"没有对照"≠"做 T 没有影响"，这句必须能上屏）。
	Reason          string                  `json:"reason,omitempty"`
	Enabled         bool                    `json:"enabled"` // 当前开关（false 时曲线是关停前的最后一段）
	BaseEquity      float64                 `json:"base_equity"`
	EquityReal      float64                 `json:"equity_real"`
	EquityShadow    float64                 `json:"equity_shadow"`
	TAttribution    float64                 `json:"t_attribution"`
	TAttributionPct float64                 `json:"t_attribution_pct"`
	Mirrored        int                     `json:"mirrored"`
	SkippedTLegs    int                     `json:"skipped_t_legs"`
	Series          []TShadowNoTSeriesPoint `json:"series"`
}

// TShadowNoTReadout 返回影子对照的一帧读数（一次加锁一帧，与 TargetReadout 同风格）。
// 不可用（开关从未打开/没有当日影子账）时回 available=false + 原因——绝不回一条"恒等于 0"的
// 假曲线：那会被读成"做 T 没有影响"，而真实含义是"没有对照"。
func (e *Engine) TShadowNoTReadout() (TShadowNoTReadout, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	day := cntime.DayCompactOf(e.clockNow())
	out := TShadowNoTReadout{Day: day, Enabled: e.shadowNoTOn}
	if e.shadowNoT == nil || e.shadowNoT.Day != day {
		out.Reason = "当日没有影子账（sizing.t.shadow_no_t 未开启或开关开启后尚未采样）"
		return out, out.Reason
	}
	book := e.shadowNoT
	real := round2(e.cash + e.marketValueLocked())
	shadow := round2(e.tShadowEquityLocked())
	out.Available = true
	out.BaseEquity = book.BaseEquity
	out.EquityReal = real
	out.EquityShadow = shadow
	out.TAttribution = round2(real - shadow)
	if book.BaseEquity > 0 {
		out.TAttributionPct = round4((real - shadow) / book.BaseEquity * 100)
	}
	out.Mirrored = book.Mirrored
	out.SkippedTLegs = book.SkippedLeg
	out.Series = make([]TShadowNoTSeriesPoint, 0, len(book.EquitySer))
	for _, p := range book.EquitySer {
		out.Series = append(out.Series, TShadowNoTSeriesPoint{Ts: p.Ts, Real: p.Real, Shadow: p.Equity})
	}
	return out, ""
}

// String 排障用（日志里点名影子账是哪个交易日、跳了几条腿）。
func (b *tShadowNoTBook) String() string {
	if b == nil {
		return "<nil>"
	}
	return fmt.Sprintf("shadow_no_t day=%s base=%.2f cash=%.2f mirrored=%d skipped=%d",
		b.Day, b.BaseEquity, b.Cash, b.Mirrored, b.SkippedLeg)
}
