package server

// §0926E2E-MX3（2026-09-27 四波·矩阵补位#3）LLM 真实池周度例行探测。
//
// 背景：probe_llm.sh（手工脚本）此前只是排障观察项——池漂移（密钥吊销、上游模型下线、
// 限流收紧）没有例行暴露面，往往要等盘中某条打分链路失败才第一次现形（0920 批
// BUGFIX_LLM_PROBE_TIMEOUT 的定性残留）。本文件把同一探测收编进引擎自身例行：
// 北京每周日 10:00 后对**当前生效配置**跑一次只读 force 探测，成败都落一行 opslog
// （成功＝心跳取证；失败＝带拒因的中级别告警），opslog 里 llm_probe 分类连续两周
// 无行即"例行本身失明"，daily_ops_check 侧可按分类排查。
//
// 语义边界（三条铁律）：
//  ① 与 POST /api/config/llm/probe 同一只读路径 applyLLMSnapshot(force, persist=false,
//     hotSwap=false)——绝不改运行时、绝不落库，周度例行不得有 §LLM-HOTUPDATE 族副作用；
//  ② 日志/告警只带结论与计数（密钥条数、耗时、拒因），绝不回显任何密钥值
//     （「日志里出现密钥明文按事故处理」全仓口径）；
//  ③ 单次失败只推 LevelMedium：周窗口里上游瞬时抖动常见，文案显式引导用
//     /api/config/llm/probe 复核，连续两周红才是真池失效。
//
// English: weekly read-only LLM pool probe — same force-probe path as the admin
// endpoint, opslog evidence line every Sunday, medium alert on failure, never mutates
// runtime or persisted config, never logs key values.

import (
	"context"
	"fmt"
	"log"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/opslog"
)

// weeklyLLMProbeInterval 例行检查节奏：小时级足够（触发窗口是周日≥10:00，
// 窗口内首个 tick 即执行，此后同周判重直接空转），检查本身零成本。
const weeklyLLMProbeInterval = time.Hour

// weeklyProbeDue 周日窗口的纯判定（抽出来供单测锤日历语义，不依赖网络与配置）：
// 仅北京时区周日且小时≥10 才可能触发；last==当周键则判"本周已探过"。
// English: pure scheduling predicate — Sunday (Beijing) at/after 10:00 and not yet
// probed this ISO week.
func weeklyProbeDue(now time.Time, last string) (string, bool) {
	nowB := cntime.In(now)
	if nowB.Weekday() != time.Sunday || nowB.Hour() < 10 {
		return last, false
	}
	y, w := nowB.ISOWeek()
	key := fmt.Sprintf("%04d-W%02d", y, w)
	if key == last {
		return last, false
	}
	return key, true
}

// StartWeeklyLLMProbe 启动 LLM 池周度探测协程（ctx 随进程退出；调用点在 cmd/quant/main.go）。
// 当周幂等锚用北京时区 ISO 年-周号字符串（"2026-W39"）比较：同周多 tick 只探测一次；
// 进程重启会把锚清零、同周可能重探一次——无害，探测路径只读（见文件头铁律①）。
func (s *Server) StartWeeklyLLMProbe(ctx context.Context) {
	go func() {
		tk := time.NewTicker(weeklyLLMProbeInterval)
		defer tk.Stop()
		last := "" // 最近一次探测归属的"年-W周"（空=本进程未探过）
		// 节拍主循环：每小时醒一次问日历（weeklyProbeDue 纯函数判"该不该探"），
		// 命中窗口即探测并更新同周幂等锚；ctx 取消（进程退出）即收工。
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				key, due := weeklyProbeDue(time.Now(), last)
				if !due {
					continue
				}
				last = key
				s.runWeeklyLLMProbe(key)
			}
		}
	}()
}

// runWeeklyLLMProbe 执行一次只读探测并留证。判定口径与设置页"测试连接"完全同源：
// Rejected=true 即池失效（全部密钥都被确凿拒），Verified=false 且未拒按警告处理
// （未经证实≠可用实锤，例行心跳里如实写"未证实"，不做假绿）。
func (s *Server) runWeeklyLLMProbe(weekKey string) {
	uid := s.operatorID()
	snap, ok := s.currentLLMSnapshot(uid)
	if !ok {
		opslog.Logf("llm_probe", "§0926E2E-MX3 周度探测跳过（%s）：当前无 LLM 配置可探测（从未设置）", weekKey)
		return
	}
	res, _ := s.applyLLMSnapshot(uid, snap, true /*force：只探测*/, false /*persist*/, false /*hotSwap*/)
	line := fmt.Sprintf("§0926E2E-MX3 LLM 池周度探测 %s url=%s model=%s keys=%d probes_ok=%d dropped=%d probe_ms=%d verified=%v",
		weekKey, res.APIURL, res.Model, len(snap.Keys), countProbesOK(res), res.DroppedKeys, res.ProbeMS, res.Verified)
	if res.Rejected {
		opslog.Logf("llm_probe", "%s 结论=池失效（RED）：%s", line, res.Reason)
		log.Printf("[llm-weekly] RED %s reason=%s", weekKey, res.Reason)
		if s.notifier != nil {
			s.notifier.Push(notify.Message{
				Level: notify.LevelMedium,
				Title: "LLM 池周度探测失败",
				Content: fmt.Sprintf("%s 周度只读探测被拒（引擎未做任何改动）。拒因：%s。请用设置页「测试连接」或 "+
					"POST /api/config/llm/probe 复核；连续两周红按池失效处置（换密钥/换模型）。", weekKey, res.Reason),
			})
		}
		return
	}
	if !res.Verified {
		opslog.Logf("llm_probe", "%s 结论=未证实（WARN）：%s", line, res.Warning)
		return
	}
	opslog.Logf("llm_probe", "%s 结论=可用（GREEN）", line)
}

// countProbesOK 统计逐密钥探测中判定可用（ProbeOK/ProbeRateLimited 同 Usable 口径）的把数
// （纯观察计数，绝不取密钥值本身）。
func countProbesOK(res llmApplyResult) int {
	n := 0
	for _, p := range res.Probes {
		if p.Usable() {
			n++
		}
	}
	return n
}
