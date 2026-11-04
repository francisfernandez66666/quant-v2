// source_status.go — §B6「不许静默」第二条的取数侧：逐源读数留痕 + LLM 归因可用性读数。
//
// 三源全失败必须出一条通道级告警，前提是**每一轮都把"这一源取没取到"记下来**。
// 此前 fetchCatchUp 只把失败打进日志（人查得到、机器读不到），分级器拿不到输入就只能"当作没有新闻"，
// 而"取不到"被呈现成"今天没有利空"正是本仓反复出事的同一族错误（QS 系列、J0 零回包、空快照 vs 没取到）。
package newsagent

import (
	"sync"
	"time"

	"quant-trading-v2/internal/data"
)

// 三个新闻源名（与 fetchCatchUp 的三条腿一一对应；通道告警文案按这三个名字点名）。
// English: the three news-source names, one per fetch leg, named in the channel alert.
const (
	SourceTHS  = "同花顺"
	SourceCLS  = "财联社"
	SourceSina = "新浪"
)

// fetchStat 单个来源一轮的读数：是否取到、取了几条、失败原因原文。
// English: fetchStat is one source's round reading: reached or not, item count, raw error.
type fetchStat struct {
	OK    bool
	Count int
	Err   string
	At    time.Time
}

// sourceLedger 逐源读数的当日账（内存态；重启后由第一轮取数重新填，不参与任何判据的持久化）。
// English: the day's per-source ledger, in memory; it is a reading, not a persisted counter.
type sourceLedger struct {
	mu      sync.Mutex
	day     string
	stats   map[string]fetchStat
	healthy map[string]bool
}

func newSourceLedger() *sourceLedger {
	return &sourceLedger{stats: make(map[string]fetchStat), healthy: make(map[string]bool)}
}

// record 记一个来源本轮的读数；跨交易日先整本清空（与消息中心同一处 day-key 判据）。
// English: record writes one source's round reading, clearing the ledger on the trading-day key.
func (l *sourceLedger) record(now time.Time, name string, count int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	td := data.TradingDayDate(now)
	if l.day != td {
		l.day = td
		l.stats = make(map[string]fetchStat)
		l.healthy = make(map[string]bool)
	}
	st := fetchStat{OK: err == nil && count >= 0, Count: count, At: now}
	if err != nil {
		st.OK = false
		st.Err = err.Error()
	}
	l.stats[name] = st
	l.healthy[name] = st.OK
}

// snapshot 返回当日逐源读数（按源名给出站分级器当输入）。
// English: snapshot returns today's per-source readings for the dispatcher.
func (l *sourceLedger) snapshot() []SourceState {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := []string{SourceTHS, SourceCLS, SourceSina}
	out := make([]SourceState, 0, len(names))
	for _, n := range names {
		st, ok := l.stats[n]
		if !ok {
			// 从未记过账＝这一源"本轮没做取数"，不许冒充失败也不算成功：OK=false、Err 明写未取数。
			// English: never recorded means "no fetch happened", which is neither a success nor a failure.
			out = append(out, SourceState{Name: n, OK: false, Count: 0, Err: "本轮未取数"})
			continue
		}
		out = append(out, SourceState{Name: n, OK: st.OK, Count: st.Count, Err: st.Err})
	}
	return out
}

// allDown 三源是否全失败（至少三个源都有过读数，且全为失败）。
// 一条读数都没有时判 false——启动初期还没有任何取数发生，此时报"通道坏了"是假告警。
// English: allDown is true only when every source has actually been read and all reads failed;
// with no readings at all it stays false so a cold start cannot fire a false alarm.
func (l *sourceLedger) allDown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	read := 0
	for _, n := range []string{SourceTHS, SourceCLS, SourceSina} {
		st, ok := l.stats[n]
		if !ok {
			continue
		}
		read++
		if st.OK {
			return false
		}
	}
	return read >= 3
}

// SourcesStatus 返回当日逐源读数（供引擎/分级器组 RoundInput.Sources）。
// 账本为 nil（旧测试用字面量手搓 Agent）时返回空读数，由 allSourcesDown 判"没做取数"而不是"全坏"。
// English: SourcesStatus returns today's readings; a nil ledger (hand-built test Agent) yields an
// empty reading, which allDown deliberately does not treat as "everything failed".
func (a *Agent) SourcesStatus() []SourceState {
	if a.sources == nil {
		return nil
	}
	return a.sources.snapshot()
}

// NewsSourcesAllDown 三源是否全失败（§5A.3 第 2 条的判据入口，只读不动钱）。
// English: NewsSourcesAllDown is the read-only judgement entry for §5A.3 rule 2.
func (a *Agent) NewsSourcesAllDown() bool {
	return a.sources != nil && a.sources.allDown()
}

// LLMStatus 归因侧读数：客户端未配置/未注入即不可用；具体连通性由调用侧的 Stage 失败体现。
// English: LLMStatus reports the attribution side — an unset/nil client means unavailable.
func (a *Agent) LLMStatus() LLMState {
	return LLMState{Available: a.llmClientSnapshot() != nil}
}

// RecordSourceResult 供取数侧登记单个来源的本轮结果（fetchCatchUp 内部调用；导出供手动补推链路复用）。
// 账本为 nil 时静默跳过：这是观测腿，不许把一条读数记不上变成一次崩溃。
// English: RecordSourceResult registers one source's round outcome; a nil ledger is skipped —
// an observation leg must never turn a missed reading into a crash.
func (a *Agent) RecordSourceResult(name string, count int, err error) {
	if a.sources == nil {
		return
	}
	a.sources.record(time.Now(), name, count, err)
}
