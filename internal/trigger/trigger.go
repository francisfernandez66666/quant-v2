// Package trigger 实时触发引擎（借鉴 freevolunteer/daban 打板触发模型）。
// 消费 5s 行情快照（data.Fetcher），对监控池个股做滑动窗口检测：
// 窗口内秒均涨幅 ≥ RaRate 且秒成交额 ≥ StockAmt 即判定为放量急拉，广播触发信号。
// （Package trigger is a real-time trigger engine (modeled after freevolunteer/daban). It consumes
// 5s market snapshots (data.Fetcher) and runs sliding-window checks on monitored stocks: if the
// per-second gain ≥ RaRate and per-second turnover ≥ StockAmt, it's a volume-spike rally → broadcast.）
package trigger

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/server"
)

// Config 实时触发配置（对应 daban TriggerConf）。
// （Config is the real-time trigger configuration, mirroring daban's TriggerConf.）
type Config struct {
	Sec      int           // 观测窗口（秒），默认 6
	RaRate   float64       // 秒均涨幅阈值（%/s），默认 0.125
	StockAmt float64       // 秒成交额阈值（元/s），默认 20万
	Cooldown time.Duration // 同股触发冷却，默认 5 分钟
}

// DefaultConfig 返回 daban 同款默认参数。
// （DefaultConfig returns the same defaults as daban.）
func DefaultConfig() Config {
	return Config{
		Sec:      6,
		RaRate:   0.125,
		StockAmt: 200000,
		Cooldown: 5 * time.Minute,
	}
}

// Signal 实时触发信号。
// （Signal is a real-time trigger signal.）
type Signal struct {
	Code      string    `json:"code"`       // 股票代码
	Name      string    `json:"name"`       // 股票名称
	Price     float64   `json:"price"`      // 当前价
	ChangePct float64   `json:"change_pct"` // 当日涨跌幅（%）
	SecRise   float64   `json:"sec_rise"`   // 窗口秒均涨幅（%/s）
	SecAmt    float64   `json:"sec_amt"`    // 窗口秒成交额（元/s）
	SecTurn   float64   `json:"sec_turn"`   // 窗口秒均换手（%/s）
	Msg       string    `json:"msg"`        // 触发描述
	At        time.Time `json:"at"`         // 触发时间
}

// tickState 单只股票的窗口滑动状态，记录上一 tick 的快照用于计算差分指标。
// （tickState holds the sliding-window state of a single stock, keeping the previous tick snapshot for delta metrics.）
type tickState struct {
	prevPrice   float64   // 上一 tick 价格
	prevAmt     float64   // 上一 tick 累计成交额
	prevTurn    float64   // 上一 tick 累计换手
	lastAt      time.Time // 上一 tick 时间
	lastTrigger time.Time // 最近一次触发时间（用于冷却判断）
}

// Engine 实时触发引擎：持有行情采集器与 SSE 广播器，逐 tick 检测放量急拉。
// （Engine is the real-time trigger engine: it holds the quote fetcher and SSE broker, checking for
// volume-spike rallies tick by tick.）
type Engine struct {
	fetcher *data.Fetcher     // 5s 行情快照采集器
	sse     *server.SSEBroker // SSE 广播器（触发时推送 signal 事件）
	cfg     Config            // 触发参数配置

	mu     sync.Mutex            // 保护 states 的互斥锁
	states map[string]*tickState // 股票代码 → 滑动窗口状态
}

// New 创建实时触发引擎。
// （New creates a real-time trigger engine.）
func New(fetcher *data.Fetcher, sse *server.SSEBroker, cfg Config) *Engine {
	if cfg.Sec <= 0 {
		cfg.Sec = 6
	}
	if cfg.RaRate <= 0 {
		cfg.RaRate = 0.125
	}
	if cfg.StockAmt <= 0 {
		cfg.StockAmt = 200000
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 5 * time.Minute
	}
	return &Engine{
		fetcher: fetcher,
		sse:     sse,
		cfg:     cfg,
		states:  make(map[string]*tickState),
	}
}

// Run 以 5s 间隔消费快照并检测，直到 ctx 取消。
// （Run consumes snapshots every 5s and runs detection until ctx is cancelled.）
func (e *Engine) Run(ctx context.Context) {
	log.Printf("[trigger] 实时触发引擎启动: 窗口%ds 秒涨≥%.3f%% 秒额≥%.0f元 冷却%v",
		e.cfg.Sec, e.cfg.RaRate, e.cfg.StockAmt, e.cfg.Cooldown)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	paused := false
	for {
		select {
		case <-ctx.Done():
			log.Println("[trigger] 实时触发引擎停止")
			return
		case <-ticker.C:
			// 非活跃时段门控：盘后/休市停止检测（fetcher 已暂停，重复消费陈旧快照
			// 只是空转；advance 对 >60s 间隔会自动重置基线，恢复后不会误触发）。
			// English: inactive-session gate — skip detection after market/holidays; the fetcher
			// is already paused, and advance resets baselines on >60s gaps so resume can't misfire.
			if !data.IsActiveSession(time.Now()) {
				if !paused {
					log.Println("[trigger] 非活跃时段, 暂停实时触发检测")
					paused = true
				}
				continue
			}
			if paused {
				log.Println("[trigger] 进入活跃时段, 恢复实时触发检测")
				paused = false
			}
			if snap := e.fetcher.Snapshot(); snap != nil {
				e.check(snap)
			}
		}
	}
}

// check 对快照内所有股票做一次窗口检测。
// （check runs one window-based detection pass over all stocks in the snapshot.）
func (e *Engine) check(snap *data.MarketSnapshot) {
	now := snap.Time
	for code, si := range snap.Stocks {
		// 过滤无效行情（空指针/价格非正/成交额为负）
		if si == nil || si.Price <= 0 || si.Amount < 0 {
			continue
		}
		// 推进窗口并计算秒均涨幅/成交额/换手
		secRise, secAmt, secTurn, st := e.advance(code, si, now)
		// st 为 nil 表示冷却期内，secRise==0 表示首帧或间隔异常（仅初始化）
		if st == nil || secRise == 0 {
			continue
		}
		// 双阈值判定：秒均涨幅 ≥ RaRate 且秒成交额 ≥ StockAmt 才触发
		if secRise < e.cfg.RaRate || secAmt < e.cfg.StockAmt {
			continue
		}

		// 记录触发时间，进入冷却期
		e.mu.Lock()
		st.lastTrigger = now
		e.mu.Unlock()

		sig := Signal{
			Code:      code,
			Name:      si.Name,
			Price:     si.Price,
			ChangePct: si.ChangePct,
			SecRise:   secRise,
			SecAmt:    secAmt,
			SecTurn:   secTurn,
			Msg: fmt.Sprintf("放量急拉: 秒涨%.2f%% 秒成交%.0f万 秒换手%.3f%%",
				secRise, secAmt/10000, secTurn),
			At: now,
		}
		log.Printf("[trigger] 触发 %s(%s) %.2f元 涨%.2f%% | %s", code, si.Name, si.Price, si.ChangePct, sig.Msg)
		if e.sse != nil {
			e.sse.Broadcast(map[string]interface{}{
				"type":   "trigger",
				"signal": sig,
			})
		}
	}
}

// advance 推进单只股票滑动窗口，返回窗口秒均涨幅/成交额/换手。
// 首个 tick 或间隔异常（>60s）仅初始化状态，返回 0 表示未触发条件评估。
// 冷却期内返回 nil 跳过。
// （advance pushes the sliding window forward for one stock and returns the window's per-second
// gain/amount/turnover. First tick or an abnormal gap (>60s) just initializes state and returns 0;
// returning nil means the stock is inside its cooldown.）
func (e *Engine) advance(code string, si *data.StockInfo, now time.Time) (float64, float64, float64, *tickState) {
	e.mu.Lock()
	defer e.mu.Unlock()

	st := e.states[code]
	if st == nil {
		st = &tickState{}
		e.states[code] = st
	}
	// 冷却期内：返回 nil 表示跳过该股票的触发评估
	if !st.lastTrigger.IsZero() && now.Sub(st.lastTrigger) < e.cfg.Cooldown {
		return 0, 0, 0, nil
	}
	// 首个 tick：仅记录基准状态，不产生触发
	if st.lastAt.IsZero() || st.prevPrice <= 0 {
		st.prevPrice = si.Price
		st.prevAmt = si.Amount
		st.prevTurn = si.Turnover
		st.lastAt = now
		return 0, 0, 0, st
	}
	// 时间间隔异常（非正或超过 60s）：视为断流，重置基准状态，不产生触发
	dt := now.Sub(st.lastAt).Seconds()
	if dt <= 0 || dt > 60 {
		st.prevPrice = si.Price
		st.prevAmt = si.Amount
		st.prevTurn = si.Turnover
		st.lastAt = now
		return 0, 0, 0, st
	}

	// 差分计算：秒均涨幅 = Δ价/基准价/Δt；秒均成交额、秒均换手同理
	secRise := (si.Price - st.prevPrice) / st.prevPrice * 100 / dt
	secAmt := (si.Amount - st.prevAmt) / dt
	secTurn := (si.Turnover - st.prevTurn) / dt

	st.prevPrice = si.Price
	st.prevAmt = si.Amount
	st.prevTurn = si.Turnover
	st.lastAt = now
	return secRise, secAmt, secTurn, st
}

// State 返回当前监控股票数量（调试用）。
// （State returns the number of monitored stocks (for debugging).）
func (e *Engine) State() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.states)
}
