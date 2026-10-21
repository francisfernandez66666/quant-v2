// registry_dayclose_test.go — §N6 盘后导出的时钟与失败语义回归（2026-10-21）。
//
// 现网实录：paper_daily 自 2026-10-14 起整段缺失，而 paper_trades 同期仍在增长——导出不是
// "没数据"，是根本没被触发。两条成因在这里各钉一枚用例：
//  1. 触发点原本挂在近实时打分循环的 dispatchPaperMark 上，而该循环在 IsActiveSession 为假时
//     （15:00 收盘后即如此）整段休眠，盘后维护分支必须能独立驱动导出；
//  2. hook 原本无返回值，"写库失败"与"写库成功"在注册表眼里没有区别，一次研究库写锁抖动
//     就被记成"今天导过了"，当天不再重试。
//
// English: regression guards for the post-close export — a wall-clock trigger independent of the
// in-session scoring loop, and a success signal so a failed DB write is retried instead of being
// recorded as done.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/paper"
)

// TestExportPaperDailyIfDue 覆盖四态：盘中不导出 / 盘后对自动撮合账号导出 / 写库失败保留重试 /
// 成功当天不再重复、次日重新触发。
func TestExportPaperDailyIfDue(t *testing.T) {
	dir := t.TempDir()
	tmpl := paper.New(paper.Config{Enabled: true, InitialCapital: 100000}, "")
	r := NewRegistry(EngineOptions{Paper: tmpl, DataDir: dir})
	// 共享引擎 + 一自动一普通两个账号：只有自动撮合账号参与盘后导出。
	r.cores["fp_test"] = &Engine{}
	core := r.cores["fp_test"]
	r.coreUsers[core] = []string{"u_auto", "u_manual"}
	r.autoCheck = func(uid string) bool { return uid == "u_auto" }

	var calls []string
	okNext := false
	r.SetDayCloseExport(func(uid string, _ *paper.Engine) bool {
		if okNext {
			calls = append(calls, uid)
		}
		return okNext
	})

	at := func(hh, mm int) time.Time {
		return time.Date(2026, 10, 21, hh, mm, 0, 0, cntime.Loc)
	}
	// 盘中（15:00 前）不触发：导出是盘后动作，不能把盘中快照当成收盘快照。
	r.ExportPaperDailyIfDue(at(14, 59))
	if len(calls) != 0 {
		t.Fatalf("15:00 前不得导出, got %v", calls)
	}

	// 15:00 后首次调用：hook 返回失败（模拟研究库写锁）⇒ 不记账，必须能重试。
	r.ExportPaperDailyIfDue(at(15, 5))
	okNext = true
	r.ExportPaperDailyIfDue(at(15, 20))
	if len(calls) != 1 || calls[0] != "u_auto" {
		t.Fatalf("失败后必须重试且只导出自动撮合账号, got %v", calls)
	}

	// 成功后当天不再重复（幂等由唯一键兜底，但没必要反复写）。
	r.ExportPaperDailyIfDue(at(16, 0))
	if len(calls) != 1 {
		t.Fatalf("成功当天不得重复导出, got %v", calls)
	}

	// 次日盘后重新触发。
	r.ExportPaperDailyIfDue(time.Date(2026, 10, 22, 15, 5, 0, 0, cntime.Loc))
	if len(calls) != 2 {
		t.Fatalf("次日盘后应重新导出, got %v", calls)
	}
}
