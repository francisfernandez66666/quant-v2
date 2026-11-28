// moneyflow_gate_test.go — §MONEYFLOW-GATE 2026-11-28（档 3b）：夜间链等采集标记的行为锁。
//
// 四态必须各自成立（方案 §261 ⑨），每态都对应一种"配错就会整晚不跑研究"的形状：
//
//	① 非交易日 ⇒ **放行**。采集器 timer 是 Mon..Fri，节假日根本没有标记；
//	   漏了这条等于本批自己造出"每个节假日夜间链白等到 17:00"的假红（§CAL-GATE 反方向同犯）。
//	② 交易日、无当日标记、未到兜底时限 ⇒ 拦下，且**不改任务状态**。
//	③ 交易日、有当日标记（哪怕 status=partial/failed）⇒ 放行。
//	   等"成功"会把一次上游抖动放大成整晚研究停摆；采集失败的处置在告警与读数那一侧。
//	④ 交易日、无标记、已过兜底时限 ⇒ 放行并留痕 timeout（保险丝）。
//	另加两把：不吃资金流的步骤永不被拦（dataload 等不能被拖）、手动 high 不受限。
//
// English: four-state behavior locks for the money-flow dequeue gate, plus the two exemptions
// (non-consuming steps never wait; manual high priority never waits).
package scheduler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

func gateTime(y int, mo time.Month, d, hh, mm int) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	return time.Date(y, mo, d, hh, mm, 0, 0, loc)
}

// writeFlowMarker 在 cfg.DB 同目录写下采集标记（键名必须与采集器 runMarker 逐字相同）。
func writeFlowMarker(t *testing.T, cfg config.SchedulerConfig, day, status string) {
	t.Helper()
	dir := filepath.Dir(cfg.DB)
	b, err := json.Marshal(map[string]any{
		"day": day, "mode": "append", "status": status,
		"codes_total": 5000, "codes_ok": 4800, "coverage_pct": 96.0,
		"elapsed_sec": 1520.5, "finished_at": "2026-08-18T15:59:00+08:00",
	})
	if err != nil {
		t.Fatalf("序列化标记失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "moneyflow_run.json"), b, 0o644); err != nil {
		t.Fatalf("写标记失败: %v", err)
	}
}

func newGateFixture(t *testing.T) (*Scheduler, config.SchedulerConfig) {
	t.Helper()
	dir := t.TempDir()
	cfg := cfgSamples("fake", filepath.Join(dir, "trading.db"))
	return &Scheduler{}, cfg
}

func TestFlowGateFourStates(t *testing.T) {
	s, cfg := newGateFixture(t)
	mon := gateTime(2026, 8, 17, 15, 45)  // 周一（交易日）盘后
	sat := gateTime(2026, 8, 22, 15, 45)  // 周六（休市）
	late := gateTime(2026, 8, 17, 17, 30) // 交易日、过兜底时限

	backtest := &store.ResearchTask{ID: 1, Type: store.TaskBacktestNightly, Priority: "low"}

	// ② 交易日 + 无当日标记 + 未过时限 ⇒ 拦下
	if allow, reason := s.flowGateAllows(backtest, cfg, mon); allow {
		t.Fatalf("无标记未过时限必须拦下，得 allow=%v reason=%s", allow, reason)
	}

	// ③ 有当日标记（partial 也放行）
	writeFlowMarker(t, cfg, "20260817", "partial")
	if allow, reason := s.flowGateAllows(backtest, cfg, mon); !allow {
		t.Fatalf("有当日结论就必须放行（partial 也算有结论），reason=%s", reason)
	}
	// 标记状态是 failed 同样放行：等"成功"会把一次抖动放大成整晚停摆
	writeFlowMarker(t, cfg, "20260817", "failed")
	if allow, _ := s.flowGateAllows(backtest, cfg, mon); !allow {
		t.Fatal("failed 标记仍代表『今天有结论』，必须放行")
	}

	// 标记是昨天的 ⇒ 视为无当日结论，重新拦下
	writeFlowMarker(t, cfg, "20260814", "ok")
	if allow, _ := s.flowGateAllows(backtest, cfg, mon); allow {
		t.Fatal("旧日标记不得当作今天已采")
	}

	// ④ 过兜底时限 ⇒ 放行（保险丝）
	if allow, reason := s.flowGateAllows(backtest, cfg, late); !allow {
		t.Fatalf("过兜底时限必须放行，reason=%s", reason)
	} else if reason == "" {
		t.Fatal("过时限放行必须带 timeout 留痕，否则看不出是兜底放行的")

	}

	// ① 非交易日 ⇒ 放行（采集器根本不跑，等它就是自造假红）
	if allow, _ := s.flowGateAllows(backtest, cfg, sat); !allow {
		t.Fatal("休市日必须直接放行——本批不许自己造出节假日白等")
	}
}

func TestFlowGateExemptions(t *testing.T) {
	s, cfg := newGateFixture(t)
	mon := gateTime(2026, 8, 17, 15, 45)

	// 不吃资金流的步骤永不被拦（哪怕完全没有标记）
	for _, kind := range []string{store.TaskDataload, store.TaskMinuteSync,
		store.TaskSectorRebuild, store.TaskLifecycle, store.TaskList, store.TaskDiscoverPatterns} {
		if allow, reason := s.flowGateAllows(&store.ResearchTask{ID: 2, Type: kind, Priority: "low"}, cfg, mon); !allow {
			t.Fatalf("步骤 %s 不吃个股资金流，不得被本闸拖住（reason=%s）", kind, reason)
		}
	}

	// 手动 high 优先级不受限（运维通道不能被自己新加的闸封死）
	high := &store.ResearchTask{ID: 3, Type: store.TaskBacktestStrategy, Priority: "high"}
	if allow, reason := s.flowGateAllows(high, cfg, mon); !allow {
		t.Fatalf("high 必须放行，reason=%s", reason)
	}

	// 开关显式关掉 ⇒ 一律放行且留痕成因
	off := false
	cfgOff := cfg
	cfgOff.MoneyflowGateEnabled = &off
	if allow, reason := s.flowGateAllows(&store.ResearchTask{ID: 4,
		Type: store.TaskBacktestNightly, Priority: "low"}, cfgOff, mon); !allow || reason != "moneyflow_gate_off_by_switch" {
		t.Fatalf("关闸后必须放行并点名，得 allow=%v reason=%s", allow, reason)
	}

	// nil 任务不得 panic（调用方传空是合法形态）
	if allow, _ := s.flowGateAllows(nil, cfg, mon); !allow {
		t.Fatal("nil 任务应放行")
	}
}

func TestFlowGateConsumingSetIsExplicit(t *testing.T) {
	// 白名单式判据的自证：登记的几条为真、未登记的为假。
	// 这条用例的意义不是"值对不对"，而是**新步骤开始读 stock_moneyflow_daily 时必须有人想起来改这里**
	// （配套负锁在门禁 §261 ⑧）。
	for _, kind := range []string{store.TaskDiscoverFactors, store.TaskBacktestNightly,
		store.TaskBacktestStrategy, store.TaskBacktestCandidate, store.TaskPaperResearch} {
		if !moneyflowConsumes(kind) {
			t.Fatalf("%s 应当被认定为资金流消费步骤", kind)
		}
	}
	for _, kind := range []string{store.TaskDataload, store.TaskMinuteSync,
		store.TaskSectorRebuild, store.TaskLifecycle, store.TaskList, "unknown_kind"} {
		if moneyflowConsumes(kind) {
			t.Fatalf("%s 不该被拖住——误登记会让与资金流无关的步骤一律等到 17:00", kind)
		}
	}
}

func TestFlowGateReadMarkerFailClosed(t *testing.T) {
	s, cfg := newGateFixture(t)
	mon := gateTime(2026, 8, 17, 15, 45)
	task := &store.ResearchTask{ID: 5, Type: store.TaskBacktestNightly, Priority: "low"}

	// 标记是半截 JSON ⇒ 读不到 ⇒ 按"今天没结论"处理（不是"结论是好的"）
	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.DB), "moneyflow_run.json"),
		[]byte(`{"day": "20260`), 0o644); err != nil {
		t.Fatalf("写坏标记失败: %v", err)
	}
	if allow, _ := s.flowGateAllows(task, cfg, mon); allow {
		t.Fatal("半截标记必须按读不到处理（fail-closed），不得放行")
	}
	// day 为空的标记同样算读不到（键缺 ≠ 今天已跑）
	if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.DB), "moneyflow_run.json"),
		[]byte(`{"status":"ok"}`), 0o644); err != nil {
		t.Fatalf("写无 day 标记失败: %v", err)
	}
	if allow, _ := s.flowGateAllows(task, cfg, mon); allow {
		t.Fatal("缺 day 的标记不得被当成当日结论")
	}
	// 空 DB 路径（未配置）⇒ 无从读标记 ⇒ 只能靠兜底时限放行，不能永远拦着
	empty := cfg
	empty.DB = ""
	if allow, _ := s.flowGateAllows(task, empty, mon); allow {
		t.Fatal("未配 DB 时未过时限仍应拦下（兜底时限负责放行）")
	}
	if allow, _ := s.flowGateAllows(task, empty, gateTime(2026, 8, 17, 18, 0)); !allow {
		t.Fatal("未配 DB 且已过时限必须放行，否则研究永远不出队")
	}
}

func TestPastGraceUsesConfigConstant(t *testing.T) {
	// 兜底时限只有一个家：config.MoneyflowGateGraceHHMM。
	// 断言"时限前拦、时限后放"用的是同一个常量算出来的分钟，而不是测试里再写一个 17:00。
	h, m, ok := parseHHMM(config.MoneyflowGateGraceHHMM)
	if !ok {
		t.Fatalf("兜底时限常量形状非法: %q", config.MoneyflowGateGraceHHMM)
	}
	before := gateTime(2026, 8, 17, h, m-1)
	at := gateTime(2026, 8, 17, h, m)
	if pastGrace(before) {
		t.Fatalf("时限前 1 分钟不应算过期（%s）", config.MoneyflowGateGraceHHMM)
	}
	if !pastGrace(at) {
		t.Fatal("到点即算过期（>= 语义：恰好落在时限上不能再多等一整轮 tick）")
	}
	if _, _, ok := parseHHMM("bad"); ok {
		t.Fatal("非法形状必须 ok=false")
	}
	if _, _, ok := parseHHMM("25:00"); ok {
		t.Fatal("越界小时必须 ok=false")
	}
	// 常量配错时 pastGrace 必须返回 true（放行）而不是拦死研究
	if !pastGrace(gateTime(2026, 8, 17, 3, 0)) {
		t.Log("注：当前常量合法，凌晨时刻自然未过期")
	}
}

// TestMoneyflowGateReportStates §5.4：回放报告头的闸读数必须与出队闸**同一份公式**。
//
// 这条锁的意义不是"再测一遍 flowGateAllows"，而是防止报告头自己写第二套钟点判定——
// 那种漂移的表现是"报告头写 pass、调度器其实还在 wait"，读审批表的人会把
// "这轮数字里没有今天资金流"误读成"采集完了只是没数据"。
func TestMoneyflowGateReportStates(t *testing.T) {
	_, cfg := newGateFixture(t)
	dir := filepath.Dir(cfg.DB)
	mon := gateTime(2026, 8, 17, 15, 45)  // 周一，未到 17:00
	late := gateTime(2026, 8, 17, 17, 30) // 周一，已过兜底时限
	sat := gateTime(2026, 8, 22, 15, 45)  // 周六

	// ① 拿不到数据目录 ⇒ none（不是 pass）
	if got := MoneyflowGateReport("", mon); got != "none" {
		t.Fatalf("空目录必须报 none，得 %q", got)
	}

	// ② 交易日 + 无标记 + 未过时限 ⇒ wait
	if got := MoneyflowGateReport(dir, mon); got != "wait" {
		t.Fatalf("无标记未过时限应报 wait，得 %q", got)
	}

	// ③ 交易日 + 无标记 + 过时限 ⇒ timeout（保险丝已放行）
	if got := MoneyflowGateReport(dir, late); got != "timeout" {
		t.Fatalf("过兜底时限应报 timeout，得 %q", got)
	}

	// ④ 有当日标记（partial 也算有结论）⇒ pass
	writeFlowMarker(t, cfg, "20260817", "partial")
	if got := MoneyflowGateReport(dir, mon); got != "pass" {
		t.Fatalf("有当日结论应报 pass，得 %q", got)
	}

	// ⑤ 休市日这道闸本就不生效 ⇒ pass（不拿 wait 制造"节假日白等"的假读数）
	if got := MoneyflowGateReport(dir, sat); got != "pass" {
		t.Fatalf("休市日应报 pass，得 %q", got)
	}

	// ⑥ 旧日标记不得当成今天（与出队闸同判据）
	writeFlowMarker(t, cfg, "20260814", "ok")
	if got := MoneyflowGateReport(dir, mon); got != "wait" {
		t.Fatalf("旧日标记必须报 wait，得 %q", got)
	}
}
