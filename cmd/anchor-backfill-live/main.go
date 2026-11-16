package main

// cmd/anchor-backfill-live —— 实盘持仓「首买锚点」签字回填（§ANCHOR-SIGNOFF 2026-11-16）。
//
// 用途：把「买入后从未加减仓、成本价即首买价」的存量持仓，经 owner 签字确认为
// 可开 T 的首买锚点（anchor_source=first_buy_order），让做 T 的 step0 门认可它们。
//
// 这是**动真钱前置数据**的工具，所以设计成两段式 + 硬签字：
//   1. plan  : 只读列出候选票、当前锚点状态、建议值。先看清再决定。
//   2. apply : 必须显式给出 --confirm、--signer、--basis、以及要改的票（--codes），
//              少一项都拒绝执行。每笔写入落审计表，可复核可回滚。
//
// 用法（在 gz 上，数据目录须停 quant 服务或确认无并发写）
//   /opt/quant/anchor-backfill-live --db /var/lib/quant-trading-v2/live.db --plan
//   /opt/quant/anchor-backfill-live --db /var/lib/quant-trading-v2/live.db --apply \
//       --codes 000630.SZ,002115.SZ,002466.SZ,600580.SH,600775.SH \
//       --signer owner --basis "owner 确认：以上持仓买入后无加减仓，成本价即首买价" \
//       --confirm
//
// 判读锚点（纯 ASCII）：ANCHOR_SIGNOFF_PLAN eligible=N skipped=N ... / ANCHOR_SIGNOFF_APPLY filled=N skipped=N
// 任何一步失败：ANCHOR_SIGNOFF_ABORT reason=...，不写半库。
//
// English: signed two-phase backfill of live first-buy anchors for the T gate.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"quant-trading-v2/internal/store"
)

const confirmPhrase = "I-CONFIRM-NO-ADD-REDUCE"

func main() {
	dbPath := flag.String("db", "", "live.db 路径（必填）")
	plan := flag.Bool("plan", false, "只读规划：列出候选与建议锚点")
	apply := flag.Bool("apply", false, "执行签字回填（需 --confirm/--signer/--basis/--codes）")
	codes := flag.String("codes", "", "要回填的 ts_code，逗号分隔（apply 必填，防止整批误伤）")
	signer := flag.String("signer", "", "签字人标识（apply 必填）")
	basis := flag.String("basis", "", "事实依据原文（apply 必填）")
	confirm := flag.String("confirm", "", "确认短语，必须为 "+confirmPhrase)
	log := flag.Bool("log", false, "只读打印最近 200 条签字审计记录")
	flag.Parse()

	if *dbPath == "" {
		die("db_required", "必须用 --db 指定 live.db 路径")
	}
	if !*log && *plan == *apply { // log 模式独占；否则 plan/apply 必须且只能选其一
		die("bad_mode", "必须且只能指定 --plan 或 --apply 之一（或用 --log 只看审计）")
	}
	if *log {
		d, err := store.Open(*dbPath)
		if err != nil {
			die("db_open_failed", err.Error())
		}
		defer d.Close()
		rows, err := d.AnchorSignoffLog()
		if err != nil {
			die("log_read_failed", err.Error())
		}
		emit(map[string]any{"anchor_signoff_log": rows})
		return
	}

	d, err := store.Open(*dbPath)
	if err != nil {
		die("db_open_failed", err.Error())
	}
	defer d.Close()

	if *plan {
		p, err := d.AnchorSignoffPlanBackfill()
		if err != nil {
			die("plan_failed", err.Error())
		}
		emit(map[string]any{
			"anchor": "ANCHOR_SIGNOFF_PLAN",
			"total":  p.Total, "eligible": p.Eligible,
			"skipped": p.Skipped, "already_ok": p.AlreadyOK,
			"candidates": p.Candidates,
		})
		return
	}

	// apply: 硬签字校验，缺一不可
	if *confirm != confirmPhrase {
		die("confirm_missing", "apply 必须显式给出 --confirm="+confirmPhrase+"（表示确认无加减仓）")
	}
	if strings.TrimSpace(*signer) == "" {
		die("signer_required", "apply 必须给出 --signer")
	}
	if strings.TrimSpace(*basis) == "" {
		die("basis_required", "apply 必须给出 --basis（事实依据原文）")
	}
	codeList := splitCodes(*codes)
	if len(codeList) == 0 {
		die("codes_required", "apply 必须给出 --codes（逐票白名单，防止整批误伤）")
	}

	filled, skipped, err := d.AnchorSignoffApply(*signer, *basis, codeList)
	if err != nil {
		die("apply_failed", err.Error())
	}
	emit(map[string]any{
		"anchor": "ANCHOR_SIGNOFF_APPLY",
		"filled": filled, "skipped": skipped,
		"codes": codeList, "signer": *signer,
		"note": "每笔写入已落 anchor_signoff_log；复核用 --log",
	})
}

func splitCodes(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func emit(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func die(reason, detail string) {
	fmt.Fprintf(os.Stderr, "ANCHOR_SIGNOFF_ABORT reason=%s detail=%s\n", reason, detail)
	os.Exit(2)
}
