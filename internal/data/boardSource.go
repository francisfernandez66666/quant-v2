// boardSource.go — §WS-D D-1 板块/指数数据源冗余：BoardSource 抽象 + FailoverBoard 顺序降级与熔断。
// 东财曾是板块/资金流/指数唯一主源（改版/限频即全链哑）；现把"同一语义的多个可替代源"统一成
// BoardSource，由 FailoverBoard 按序降级（连续失败 N 次熔断该源，冷却后恢复探测），
// 并在源切换时告警（观测性）。零值行为：未注入任何源 → 直接返回错误（不臆造兜底）。
//
// English: §WS-D D-1 board/index data-source redundancy — BoardSource abstraction plus FailoverBoard
// (ordered failover + per-source circuit breaking: N consecutive failures open the source for a
// cooldown, then a recovery probe retries, alerting on every source switch). Zero sources → error
// (we never fabricate a fallback).
package data

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// BoardSource 板块/指数数据源的统一接口（同一语义的可替代实现）。
// English: BoardSource abstracts a board/index data source with interchangeable implementations.
type BoardSource interface {
	// Sectors 返回板块行情列表。English: returns the sector quote list.
	Sectors() ([]SectorInfo, error)
	// Name 源标识（eastmoney/ths/sina…，告警与观测用）。
	// English: source identifier (eastmoney/ths/sina…) for alerts and observability.
	Name() string
}

// failoverState 单源熔断状态。
// English: per-source circuit-breaker state.
type failoverState struct {
	name   string    // 源标识
	fails  int       // 连续失败计数
	open   bool      // 是否处于熔断（冷却期跳过该源）
	openAt time.Time // 熔断起始时间
	okAt   time.Time // 最近一次成功时间（可观测）
}

// FailoverBoard 顺序降级 + 熔断的板块数据源聚合器。
// English: FailoverBoard aggregates ordered board sources with failover and circuit breaking.
type FailoverBoard struct {
	mu      sync.Mutex
	sources []BoardSource
	states  map[string]failoverState
	// MaxFails 连续失败熔断阈值（默认 3）。Cooldown 冷却时长（默认 60s）。
	// ProbeEvery 冷却期探测间隔（默认 10s：到期后放行一次探测请求，成功即恢复）。
	MaxFails   int
	Cooldown   time.Duration
	ProbeEvery time.Duration
	// OnSwitch 源切换告警回调（可空）：old→new 切换与全源耗尽时触发。
	OnSwitch func(from, to, reason string)
}

// NewFailoverBoard 创建降级链（sources 按优先级顺序）。零值字段用默认阈值。
// English: NewFailoverBoard builds a failover chain from ordered sources (defaults applied for zeros).
func NewFailoverBoard(sources ...BoardSource) *FailoverBoard {
	return &FailoverBoard{
		sources:    sources,
		states:     map[string]failoverState{},
		MaxFails:   3,
		Cooldown:   60 * time.Second,
		ProbeEvery: 10 * time.Second,
	}
}

// Sectors 按序尝试各源：跳过熔断中的源；连续失败达阈值即熔断并告警；全源失败返回错误。
// English: Sectors tries sources in order, skipping circuit-open ones; N consecutive failures open the
// breaker (with an alert); all sources exhausted → error.
func (fb *FailoverBoard) Sectors() ([]SectorInfo, error) {
	var lastErrOut error
	for _, src := range fb.sources {
		if fb.isOpen(src.Name()) {
			continue
		}
		sectors, err := src.Sectors()
		if err != nil {
			lastErrOut = err
			fb.markFailure(src.Name())
			log.Printf("[board] 源 %s 板块失败: %v", src.Name(), err)
			continue
		}
		if len(sectors) == 0 {
			lastErrOut = fmt.Errorf("%s 空板块列表", src.Name())
			fb.markFailure(src.Name())
			continue
		}
		fb.markSuccess(src.Name())
		return sectors, nil
	}
	if lastErrOut == nil {
		lastErrOut = fmt.Errorf("所有板块源均失败")
	}
	fb.alertAllDown(lastErrOut)
	return nil, lastErrOut
}

// SourceName 最近一次成功的数据源名（无则 ""）。
// English: name of the most recent successful source ("" when none).
func (fb *FailoverBoard) SourceName() string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for i := len(fb.sources) - 1; i >= 0; i-- {
		if st, ok := fb.states[fb.sources[i].Name()]; ok && !st.okAt.IsZero() {
			return fb.sources[i].Name()
		}
	}
	return ""
}

// isOpen 该源是否处于熔断冷却期（冷却到期且到探测窗口 → 放行一次探测）。
// English: reports whether the source is in its circuit-open cooldown (cooldown expired and the probe
// window elapsed → allow one probe attempt).
func (fb *FailoverBoard) isOpen(name string) bool {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	st, ok := fb.states[name]
	if !ok || !st.open {
		return false
	}
	if time.Since(st.openAt) < fb.cooldown() {
		return true
	}
	if time.Since(st.openAt) < fb.cooldown()+fb.probeEvery() {
		// 冷却刚到期：允许一次探测（失败会重新计时熔断）。
		return false
	}
	return false
}

// markSuccess 记录成功：关闭熔断、重置失败计数。
// English: records a success — closes the breaker and resets the failure counter.
func (fb *FailoverBoard) markSuccess(name string) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	st, ok := fb.states[name]
	wasOpen := ok && st.open
	fb.states[name] = failoverState{name: name, fails: 0, open: false, okAt: time.Now()}
	if wasOpen {
		fb.alertSwitchLocked("", name, "源恢复")
	}
}

// markFailure 记录失败：连续失败达阈值 → 熔断并告警源切换。
// English: records a failure — opening the breaker at the threshold and alerting the switch.
func (fb *FailoverBoard) markFailure(name string) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	st := fb.states[name]
	st.name = name
	st.fails++
	if st.fails >= fb.maxFails() {
		if !st.open {
			st.open = true
			st.openAt = time.Now()
			fb.alertSwitchLocked("", name, fmt.Sprintf("连续失败 %d 次熔断", st.fails))
			log.Printf("[board] 源 %s 熔断 %s", name, fb.cooldown())
		} else if time.Since(st.openAt) >= fb.cooldown()+fb.probeEvery() {
			// 冷却期后的探测请求失败 → 重新计时熔断
			st.openAt = time.Now()
		}
	}
	fb.states[name] = st
}

// alertAllDown 全源耗尽告警。
// English: alerts when every source is exhausted.
func (fb *FailoverBoard) alertAllDown(lastErr error) {
	if fb.OnSwitch != nil {
		fb.OnSwitch("", "", "全部板块源不可用: "+lastErr.Error())
	}
	log.Printf("[board] 全部板块源不可用: %v", lastErr)
}

// alertSwitchLocked 源切换告警（持有锁时调用）。
func (fb *FailoverBoard) alertSwitchLocked(from, to, reason string) {
	if fb.OnSwitch != nil {
		fb.OnSwitch(from, to, reason)
	}
}

// maxFails 返回熔断判定所需的连续失败次数（未配置时默认 3 次）。
// English: consecutive failures required to trip the breaker (default 3).
func (fb *FailoverBoard) maxFails() int {
	if fb.MaxFails <= 0 {
		return 3
	}
	return fb.MaxFails
}

// cooldown 返回熔断后的冷却时长（未配置时默认 30s），冷却期内不再切源。
// English: breaker cooldown before re-probing (default 30s).
func (fb *FailoverBoard) cooldown() time.Duration {
	if fb.Cooldown <= 0 {
		return 60 * time.Second
	}
	return fb.Cooldown
}
func (fb *FailoverBoard) probeEvery() time.Duration {
	if fb.ProbeEvery <= 0 {
		return 10 * time.Second
	}
	return fb.ProbeEvery
}

// boardAdapter 把任意函数式数据源适配成 BoardSource（mock 测试与既有实现接入用）。
// English: boardAdapter adapts a closure into a BoardSource (for mocks and existing impls).
type boardAdapter struct {
	name string
	fn   func() ([]SectorInfo, error)
}

func (b boardAdapter) Name() string                   { return b.name }
func (b boardAdapter) Sectors() ([]SectorInfo, error) { return b.fn() }
