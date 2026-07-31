// Package trigger 实时触发引擎（借鉴 freevolunteer/daban 打板触发模型）。
// 消费 5s 行情快照（data.Fetcher），对监控池个股做滑动窗口检测：
// 窗口内秒均涨幅 ≥ RaRate 且秒成交额 ≥ StockAmt 即判定为放量急拉，广播触发信号。
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
type Config struct {
	Sec      int           // 观测窗口（秒），默认 6
	RaRate   float64       // 秒均涨幅阈值（%/s），默认 0.125
	StockAmt float64       // 秒成交额阈值（元/s），默认 20万
	Cooldown time.Duration // 同股触发冷却，默认 5 分钟
}

// DefaultConfig 返回 daban 同款默认参数。
func DefaultConfig() Config {
	return Config{
		Sec:      6,
		RaRate:   0.125,
		StockAmt: 200000,
		Cooldown: 5 * time.Minute,
	}
}

// Signal 实时触发信号。
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

// tickState 单只股票的窗口滑动状态。
type tickState struct {
	prevPrice    float64
	prevAmt      float64
	prevTurn     float64
	lastAt       time.Time
	lastTrigger  time.Time
}

// Engine 实时触发引擎：持有行情采集器与 SSE 广播器，逐 tick 检测放量急拉。
type Engine struct {
	fetcher *data.Fetcher
	sse     *server.SSEBroker
	cfg     Config

	mu     sync.Mutex
	states map[string]*tickState
}

// New 创建实时触发引擎。
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
func (e *Engine) Run(ctx context.Context) {
	log.Printf("[trigger] 实时触发引擎启动: 窗口%ds 秒涨≥%.3f%% 秒额≥%.0f元 冷却%v",
		e.cfg.Sec, e.cfg.RaRate, e.cfg.StockAmt, e.cfg.Cooldown)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("[trigger] 实时触发引擎停止")
			return
		case <-ticker.C:
			if snap := e.fetcher.Snapshot(); snap != nil {
				e.check(snap)
			}
		}
	}
}

// check 对快照内所有股票做一次窗口检测。
func (e *Engine) check(snap *data.MarketSnapshot) {
	now := snap.Time
	for code, si := range snap.Stocks {
		if si == nil || si.Price <= 0 || si.Amount < 0 {
			continue
		}
		secRise, secAmt, secTurn, st := e.advance(code, si, now)
		if st == nil || secRise == 0 {
			continue
		}
		if secRise < e.cfg.RaRate || secAmt < e.cfg.StockAmt {
			continue
		}

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
func (e *Engine) advance(code string, si *data.StockInfo, now time.Time) (float64, float64, float64, *tickState) {
	e.mu.Lock()
	defer e.mu.Unlock()

	st := e.states[code]
	if st == nil {
		st = &tickState{}
		e.states[code] = st
	}
	if !st.lastTrigger.IsZero() && now.Sub(st.lastTrigger) < e.cfg.Cooldown {
		return 0, 0, 0, nil
	}
	if st.lastAt.IsZero() || st.prevPrice <= 0 {
		st.prevPrice = si.Price
		st.prevAmt = si.Amount
		st.prevTurn = si.Turnover
		st.lastAt = now
		return 0, 0, 0, st
	}
	dt := now.Sub(st.lastAt).Seconds()
	if dt <= 0 || dt > 60 {
		st.prevPrice = si.Price
		st.prevAmt = si.Amount
		st.prevTurn = si.Turnover
		st.lastAt = now
		return 0, 0, 0, st
	}

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
func (e *Engine) State() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.states)
}
