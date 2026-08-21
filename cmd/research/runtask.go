// run-task 子命令（子系统统一改造一期）：research_tasks 队列的唯一执行入口。
// worker 只负责出队/杀进程/解析进度；本命令读取任务行 payload，按 type 分发到既有实现：
//
//	discover_factors / discover_patterns / sector_rebuild / paper_research /
//	backtest_candidate / backtest_nightly / list → 进程内直接调用对应子命令；
//	backtest_strategy（战法库规则回放）→ 一期暂 exec bt_strategy 二进制（二期并入后改为进程内）。
//
// 进度协议：子命令按既有格式打印"回测进度 xx%"等行，由 worker 逐行解析回写队列。
// English: run-task dispatcher (unified-subsystem phase 1) — the single execution entry for the
// research_tasks queue. The worker only dequeues/kills/parses progress; this command reads the task
// row's payload and dispatches by type to the existing implementations (in-process), except library
// rule replay which shells out to bt_strategy until phase 2 merges it in.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strconv"
	"time"

	"quant-trading-v2/internal/btreplay"
	"quant-trading-v2/internal/store"
)

// cmdRunTask 执行一条队列任务。
func cmdRunTask(db *store.DB, dbPath string, args []string) {
	fs := flag.NewFlagSet("run-task", flag.ExitOnError)
	taskID := fs.Int64("task-id", 0, "research_tasks.id")
	fs.Parse(args)
	if *taskID <= 0 {
		log.Fatalf("用法: research [--db …] run-task --task-id <id>")
	}
	tk, err := db.GetResearchTask(*taskID)
	if err != nil {
		log.Fatalf("读取任务失败: %v", err)
	}
	if tk == nil {
		log.Fatalf("任务不存在: #%d", *taskID)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(tk.Payload), &p); err != nil {
		p = map[string]any{}
	}

	switch tk.Type {
	case store.TaskDiscoverFactors:
		cmdDiscoverFactors(db, payloadArgs(p,
			"start", "end", "h", "min-stocks", "max-factors", "split",
			"min-ir", "min-days", "min-gen-t", "metric", "factors", "codes"))
	case store.TaskDiscoverPatterns:
		cmdDiscoverPatterns(db, payloadArgs(p,
			"start", "end", "h", "min-trigger", "min-excess", "split", "codes"))
	case store.TaskSectorRebuild:
		cmdSectorRebuild(db, payloadStr(p, "start", "20200101"), payloadStr(p, "end", today()))
	case store.TaskPaperResearch:
		cmdPaperResearch(db, nil)
	case store.TaskList:
		cmdList(db, nil)
	case store.TaskBacktestCandidate:
		btArgs := payloadArgs(p, "start", "end", "h", "min-stocks", "min-limit-ups", "top-k", "max-per-day")
		id := payloadInt(p, "id", 0)
		if id == 0 {
			id = tk.RefID // ref_id 为权威候选 ID（ref_id is the authoritative candidate ID）
		}
		if id > 0 {
			btArgs = append([]string{"--id", strconv.FormatInt(id, 10)}, btArgs...)
		}
		cmdBacktestCandidate(db, btArgs)
	case store.TaskBacktestNightly:
		cmdBacktestCandidate(db, payloadArgs(p, "start", "end", "h", "max-per-day"))
	case store.TaskBacktestStrategy:
		// 二期：进程内调用 btreplay（bt_strategy 已并入 research 二进制）。
		// English: phase-2 — in-process replay via internal/btreplay.
		o := &btreplay.Options{
			DBPath:      dbPath,
			DataDir:     payloadStr(p, "datadir", dataDirOf(dbPath)),
			Start:       payloadStr(p, "start", "20230101"),
			End:         payloadStr(p, "end", today()),
			Strategy:    payloadStr(p, "kind", "factor"),
			MaxStocks:   payloadIntDef(p, "maxstocks", 300),
			D1Score:     float64(payloadInt(p, "d1", 20)),
			Industry:    payloadStr(p, "industry", "") == "true",
			CandidateID: payloadInt(p, "candidate_id", 0), // 形态候选直读回放（§8.6-B）
		}
		if err := o.Run(); err != nil {
			log.Fatalf("战法库回放失败: %v", err)
		}
	default:
		log.Fatalf("未知任务类型: %s", tk.Type)
	}
}

// payloadArgs 按给定 key 顺序把 payload 展平为 CLI 参数（--key value；缺失跳过）。
// JSON 数字统一 %f(-1) 格式化避免科学计数/多余小数。
// English: flattens payload into CLI flags in key order (--key value); missing keys are skipped;
// JSON numbers format without exponent or trailing zeros.
func payloadArgs(p map[string]any, keys ...string) []string {
	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		v, ok := p[k]
		if !ok || v == nil {
			continue
		}
		switch tv := v.(type) {
		case float64:
			out = append(out, "--"+k, strconv.FormatFloat(tv, 'f', -1, 64))
		case string:
			if tv == "" {
				continue
			}
			out = append(out, "--"+k, tv)
		case bool:
			out = append(out, "--"+k, strconv.FormatBool(tv))
		default:
			out = append(out, "--"+k, fmt.Sprint(tv))
		}
	}
	return out
}

// payloadStr 取字符串参数：缺失或空串回退默认值。
func payloadStr(p map[string]any, key, def string) string {
	if v, ok := p[key].(string); ok && v != "" {
		return v
	}
	return def
}

// payloadInt 取整数参数：JSON 反序列化后数字统一为 float64，此处转 int64；缺失回退默认值。
func payloadInt(p map[string]any, key string, def int64) int64 {
	if v, ok := p[key].(float64); ok {
		return int64(v)
	}
	return def
}

// payloadIntDef payloadInt 的 int 版本（CLI flag 常为 int）。
func payloadIntDef(p map[string]any, key string, def int) int {
	return int(payloadInt(p, key, int64(def)))
}

// today 当前日期 YYYYMMDD（researchd 已固定 Asia/Shanghai 时区，海外主机不偏移）。
func today() string { return time.Now().Format("20060102") }

// dataDirOf 从库路径推导数据目录（applied_*.json 所在，与 QUANT_DATA_DIR 约定一致）。
func dataDirOf(dbPath string) string {
	dir := "."
	for i := len(dbPath) - 1; i >= 0; i-- {
		if dbPath[i] == '/' {
			dir = dbPath[:i]
			break
		}
	}
	if dir == "" {
		dir = "."
	}
	return dir
}
