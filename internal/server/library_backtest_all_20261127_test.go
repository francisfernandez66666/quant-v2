// §P4-ALL 2026-11-27（PLAN_FACTOR_FIX_P0_20261126 §5）：战法库「全量重测」入口的行为锁。
//
// 靶子：战法库页只能一条一条发起回放，想"全部重测一遍"只能靠 CLI
// `research backtest-strategy --strategy all`（需要登录服务器）。这里把同一个既有入口
// 接到 HTTP：POST /api/research/library/all/backtest → 入队一条
// backtest_strategy(kind=all, ref_id=0)，run-task 侧 payload.kind 直通
// btreplay.Options.Strategy，因此走的仍是 nightly library_replay 那条执行路径
// （因子规则 + 形态规则 + 内置五形态），不是第二套回测代码。
//
// 三条断言（都是"改坏了会红"的形状）：
//  1. all 入队成功，落库任务的 type/ref_id/payload.kind 三件都对；
//  2. ref_id=0 的幂等键真的在挡：连点两次只有一条活跃任务（批量回放吃整晚 CPU，
//     重复入队等于把队列排成两晚）；
//  3. 单条规则的老路径不受影响（fac_172 仍按 ref_id=172 入队，kind=factor）。
//
// English: §P4-ALL — locks the batch "replay every strategy" HTTP entry (kind=all,
// ref_id=0 idempotency) plus the untouched single-rule path.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestLibraryBacktestAll 全量重测入口：入队 + 幂等 + 不干扰单条路径。
// English: batch replay entry — enqueue, idempotency, single-rule path untouched.
func TestLibraryBacktestAll(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// ① 全量重测：202 + kind=all + 一条 queued 任务
	// English: ① batch replay returns 202 with a queued kind=all task.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/research/library/all/backtest?start=20240101&end=20240630&maxstocks=120&quality=true", nil)
	req.SetPathValue("id", "all")
	s.handleLibraryBacktest(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("全量重测状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body["kind"] != "all" {
		t.Fatalf("响应 kind=%v 期望 all", body["kind"])
	}
	taskID := int64(body["task_id"].(float64))
	if taskID <= 0 {
		t.Fatalf("task_id=%d 期望 >0", taskID)
	}

	task, err := db.GetResearchTask(taskID)
	if err != nil || task == nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if task.Type != store.TaskBacktestStrategy {
		t.Fatalf("任务类型=%s 期望 %s", task.Type, store.TaskBacktestStrategy)
	}
	if task.RefID != 0 {
		t.Fatalf("ref_id=%d 期望 0（批量任务无单一规则归属）", task.RefID)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		t.Fatalf("解析 payload 失败: %v", err)
	}
	// payload.kind 是 run-task → btreplay.Options.Strategy 的唯一来源，写成别的值
	// 就等于悄悄回放单条规则（而且 ref_id=0 会让它一条都选不出来）。
	if payload["kind"] != "all" {
		t.Fatalf("payload.kind=%v 期望 all", payload["kind"])
	}
	if payload["start"] != "20240101" || payload["end"] != "20240630" {
		t.Fatalf("时间窗未透传: start=%v end=%v", payload["start"], payload["end"])
	}
	if v, ok := payload["maxstocks"].(float64); !ok || int(v) != 120 {
		t.Fatalf("maxstocks 未透传: %v", payload["maxstocks"])
	}
	if payload["quality"] != true {
		t.Fatalf("quality 未透传: %v", payload["quality"])
	}

	// ② 幂等：连点第二次仍回同一条任务，不新增行
	// English: ② idempotent — a second click returns the same task, no extra row.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/research/library/all/backtest", nil)
	req2.SetPathValue("id", "all")
	s.handleLibraryBacktest(rr2, req2)
	if rr2.Code != http.StatusAccepted {
		t.Fatalf("第二次状态码=%d", rr2.Code)
	}
	var body2 map[string]any
	_ = json.Unmarshal(rr2.Body.Bytes(), &body2)
	if int64(body2["task_id"].(float64)) != taskID {
		t.Fatalf("第二次 task_id=%v 期望复用 %d", body2["task_id"], taskID)
	}
	var n int
	rows, qerr := db.QueryRows(`SELECT COUNT(*) AS n FROM research_tasks WHERE type=? AND ref_id=0`,
		store.TaskBacktestStrategy)
	if qerr != nil || len(rows) != 1 {
		t.Fatalf("统计任务失败: %v", qerr)
	}
	if v, ok := rows[0]["n"].(int64); ok {
		n = int(v)
	} else if v, ok := rows[0]["n"].(float64); ok {
		n = int(v)
	}
	if n != 1 {
		t.Fatalf("ref_id=0 的活跃任务数=%d 期望 1（批量回放不许重复排队）", n)
	}

	// ③ 单条规则路径不变：fac_172 → ref_id=172、kind=factor
	// English: ③ the single-rule path is untouched — fac_172 → ref_id=172, kind=factor.
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/research/library/fac_172/backtest", nil)
	req3.SetPathValue("id", "fac_172")
	s.handleLibraryBacktest(rr3, req3)
	if rr3.Code != http.StatusAccepted {
		t.Fatalf("单条回测状态码=%d body=%s", rr3.Code, rr3.Body.String())
	}
	// 单条路径的响应体只有 {"status":"queued"}（老形状，前端不依赖 task_id），
	// 所以按 ref 反查刚入队的那条。
	single, err := db.LatestTaskByRef(store.TaskBacktestStrategy, 172)
	if err != nil || single == nil {
		t.Fatalf("读取单条任务失败: %v", err)
	}
	if single.RefID != 172 {
		t.Fatalf("单条 ref_id=%d 期望 172", single.RefID)
	}
	var p3 map[string]any
	_ = json.Unmarshal([]byte(single.Payload), &p3)
	if p3["kind"] != "factor" {
		t.Fatalf("单条 payload.kind=%v 期望 factor", p3["kind"])
	}
}
