// usage_ledger.go — LLM 当日用量的**进程级**账本（§FIX-20261028-F17，2026-10-29）。
//
// 缺陷本体（两处，第二处比第一处更频繁）：
//  1. 用量计数是 Client 自己的 atomic，进程重启即归零 —— 现网实测 uptime 14m49s 而
//     llm.calls=0（当天明明跑过归因）。日预算护栏因此可被"重启一下"绕过。
//  2. 更隐蔽：LLM 配置每保存一次都会 llmRecreate **重建 Client**（server.go:156/426 →
//     llm_apply.go:364-370），新 Client 的计数器从 0 开始 —— 改一次配置就清零一次。
//
// 修法：计数搬到进程级单例账本（本文件），Client 构造时注入同一实例并从它回填；
// 每次累加同步写回账本；账本按**北京时间交易日**落地到 <dataDir>/llm_usage.json，
// 跨日整体重置（沿用 rollUsageDay 的语义，不累计到新交易日）。
//
// 日键口径：统一用 cntime.DayCompactOf（北京时）。此前的 llmToday() 取的是服务器本地
// Year/Month/Day，与仓内其它日键（data.TradingDayDate / cntime）不同源，异地部署会错位。
package llm

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
)

// UsageLedger 进程级当日用量账本（calls/tokens/consult_calls）。
//
// 并发安全：Client 可能在多个 goroutine 里累加；落盘用互斥 + 原子替换，
// 写失败只告警不阻断调用（观测面不得反过来拖住资金/研究链路）。
type UsageLedger struct {
	mu      sync.Mutex
	path    string // 空 = 纯内存（测试/未接数据目录）
	day     string // 北京时间交易日 yyyymmdd
	calls   int64
	tokens  int64
	consult int64
}

// usageFileShape 落盘形状（字段名与 /api/usage 的读数对齐，便于人工核对）。
type usageFileShape struct {
	Day          string `json:"day"`
	Calls        int64  `json:"calls"`
	Tokens       int64  `json:"tokens"`
	ConsultCalls int64  `json:"consult_calls"`
}

// NewUsageLedger 创建账本并从 path 回填**当日**计数（跨日文件直接丢弃并改写为新日）。
// path 为空时退化为纯内存账本（仍能解决"热换 Client 清零"，只管不了重启）。
func NewUsageLedger(path string) *UsageLedger {
	l := &UsageLedger{path: path, day: cntime.DayCompactOf(time.Now())}
	if path == "" {
		return l
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return l // 首次运行：没有文件是正常态
	}
	var f usageFileShape
	if json.Unmarshal(raw, &f) != nil {
		log.Printf("[llm] 用量账本解析失败（按 0 起算，原文件未改动）: %s", path)
		return l
	}
	if f.Day != l.day {
		return l // 跨日：今日从 0 开始，下次写入时覆盖为今日
	}
	l.calls, l.tokens, l.consult = f.Calls, f.Tokens, f.ConsultCalls
	return l
}

// Snapshot 返回当前账本的（日、调用数、token 数、咨询数）快照。
func (l *UsageLedger) Snapshot() (day string, calls, tokens, consult int64) {
	if l == nil {
		return "", 0, 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDayLocked()
	return l.day, l.calls, l.tokens, l.consult
}

// AddCalls / AddTokens / AddConsult 累加并落盘（跨日先重置）。
func (l *UsageLedger) AddCalls(n int64) { l.add(n, 0, 0) }

// AddTokens 累加 token 用量。
func (l *UsageLedger) AddTokens(n int64) { l.add(0, n, 0) }

// AddConsult 累加咨询调用数。
func (l *UsageLedger) AddConsult(n int64) { l.add(0, 0, n) }

func (l *UsageLedger) add(calls, tokens, consult int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollDayLocked()
	l.calls += calls
	l.tokens += tokens
	l.consult += consult
	l.saveLocked()
}

// rollDayLocked 跨日重置（调用方必须持锁）。
func (l *UsageLedger) rollDayLocked() {
	today := cntime.DayCompactOf(time.Now())
	if l.day == today {
		return
	}
	l.day = today
	l.calls, l.tokens, l.consult = 0, 0, 0
}

// saveLocked 原子落盘（temp + rename）；失败只告警。
func (l *UsageLedger) saveLocked() {
	if l.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		log.Printf("[llm] 用量账本目录不可建（%s）: %v", l.path, err)
		return
	}
	raw, err := json.Marshal(usageFileShape{Day: l.day, Calls: l.calls, Tokens: l.tokens, ConsultCalls: l.consult})
	if err != nil {
		log.Printf("[llm] 用量账本序列化失败: %v", err)
		return
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		log.Printf("[llm] 用量账本写入失败: %v", err)
		return
	}
	if err := os.Rename(tmp, l.path); err != nil {
		log.Printf("[llm] 用量账本替换失败: %v", err)
	}
}
