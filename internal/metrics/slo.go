// slo.go SLO/日报（§WS-L 维5）：盘后生成 slo_daily.json —— 可用性/信号→下单延迟 p95/
// 熔断时长/对账差异/LLM 成功率，入 opslog。SLO 承诺：交易时段下单链路可用性 ≥ 99.5%。
//
// English: SLO & daily report (WS-L 维5). Post-close job writes slo_daily.json — availability,
// signal→order latency p95, breaker duration, settlement diffs, LLM success rate, into opslog.
// SLO commitment: ≥ 99.5% availability of the intraday order chain.
package metrics

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"time"

	"quant-trading-v2/internal/fileutil"
	"quant-trading-v2/internal/opslog"
)

// SLOInput 日报输入（由调用方从当日常驻服务统计聚合）。
// English: daily-report inputs (aggregated by the caller from resident-service stats).
type SLOInput struct {
	UptimeSec       float64   // 交易时段服务在线秒数
	SessionSec      float64   // 交易时段总秒数（用于可用性分母）
	OrderLatencyMs  []float64 // 信号→下单受理时延（毫秒）样本
	BreakerSec      float64   // 当日熔断累计秒数
	SettlementDiffs int       // 当日对账差异条数
	LLMOk, LLMFail  int       // LLM 调用成功/失败次数
	ReportDay       string    // 日报日期（YYYY-MM-DD，空=今天）
}

// SLOResult 日报输出（落盘 slo_daily.json 的结构）。
// English: daily-report output (the slo_daily.json schema).
type SLOResult struct {
	Day               string  `json:"day"`
	AvailabilityPct   float64 `json:"availability_pct"` // 下单链路可用性（%）
	SLOMet            bool    `json:"slo_met"`          // 是否 ≥ 99.5% 承诺
	OrderLatencyP50Ms float64 `json:"order_latency_p50_ms"`
	OrderLatencyP95Ms float64 `json:"order_latency_p95_ms"`
	BreakerSec        float64 `json:"breaker_sec"`
	SettlementDiffs   int     `json:"settlement_diffs"`
	LLMSuccessRatePct float64 `json:"llm_success_rate_pct"`
	GeneratedAt       string  `json:"generated_at"`
}

// sloAvailabilityTarget 下单链路可用性承诺。
const sloAvailabilityTarget = 99.5

// SLOCompute 由输入聚合日报：可用性 = 在线秒/时段总秒×100；延迟 p50/p95 用样本分位；
// LLM 成功率 = ok/(ok+fail)×100（无样本=100）。
// English: aggregates the daily SLO report from inputs. Availability = online/session ×100; p50/p95
// from latency samples; LLM success = ok/(ok+fail) ×100 (no samples → 100).
func SLOCompute(in SLOInput) SLOResult {
	if in.ReportDay == "" {
		in.ReportDay = time.Now().Format("2006-01-02")
	}
	avail := 100.0
	if in.SessionSec > 0 {
		avail = in.UptimeSec / in.SessionSec * 100
		if avail > 100 {
			avail = 100
		}
	}
	llmRate := 100.0
	if in.LLMOk+in.LLMFail > 0 {
		llmRate = float64(in.LLMOk) / float64(in.LLMOk+in.LLMFail) * 100
	}
	return SLOResult{
		Day:               in.ReportDay,
		AvailabilityPct:   avail,
		SLOMet:            avail >= sloAvailabilityTarget,
		OrderLatencyP50Ms: percentile(in.OrderLatencyMs, 50),
		OrderLatencyP95Ms: percentile(in.OrderLatencyMs, 95),
		BreakerSec:        in.BreakerSec,
		SettlementDiffs:   in.SettlementDiffs,
		LLMSuccessRatePct: llmRate,
		GeneratedAt:       time.Now().Format("2006-01-02 15:04:05"),
	}
}

// percentile 计算升序样本的分位值（0~100；空返回 0）。
// English: percentile of ascending samples (0..100; empty → 0).
func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	idx := int(float64(len(sorted)-1) * p / 100)
	return sorted[idx]
}

// WriteSLODaily 把日报原子写入 dataDir/slo_daily.json 并记 opslog。
// English: atomically writes the daily SLO report to dataDir/slo_daily.json and logs to opslog.
func WriteSLODaily(dataDir string, r SLOResult) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWrite(filepath.Join(dataDir, "slo_daily.json"), b, 0o644); err != nil {
		return err
	}
	opslog.Logf("quant", "SLO 日报 %s: 可用性=%.2f%% (承诺≥%.1f%% %s) 延迟p95=%.0fms 熔断=%.0fs 对账差异=%d LLM成功率=%.1f%%",
		r.Day, r.AvailabilityPct, sloAvailabilityTarget, sloMetStr(r.SLOMet), r.OrderLatencyP95Ms, r.BreakerSec, r.SettlementDiffs, r.LLMSuccessRatePct)
	return nil
}

func sloMetStr(met bool) string {
	if met {
		return "达成"
	}
	return "未达成"
}
