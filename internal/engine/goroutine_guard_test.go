// goroutine_guard_test.go — §GOROUTINE-GUARD（2026-11-02 盘中事故的第二半）。
//
// 同一天查出的形状不止"装配漏字段"，还有"观测腿裸 goroutine 没有 recover"：
// 资金心跳（§FUNDS-HEARTBEAT）一次 nil 解引用打死整个进程，现网 131 次重启、整天零买入评估——
// 打分循环顶部本来就有 recover（scoreCycle），所以"少一轮打分"从来不会杀死进程；
// 而同一条纪律在其它三条只读腿上只收了一半：板块成分并发拉取、概念库日同步、资金心跳都裸着。
//
// 板块 worker 那处更狠：panic 让 worker 带病退出后，剩余 jobs 没人消费，
// 投喂侧 `jobs <- nf` 永久阻塞 ⇒ 打分轮挂死，而 scoreCycle 的 recover 救不了"阻塞"只救"panic"。
// 所以守卫必须按**单个任务**收，不是按 goroutine 顶部收。
//
// 本文件是静态形状锁（读源码点名 recover/栈/守卫调用点）：这三条腿在单测里都没有真柜台/真网络，
// 跑一次 panic 的代价远大于收益，而"守卫有没有被摘掉"恰好是能用锚点判死的不变量。
// English: static locks that the read-only goroutines keep per-task panic guards, so an
// observability defect can never again take the trading process down.

package engine

import (
	"os"
	"strings"
	"testing"
)

// readEngineSrc 读本包源码（与 TestFundsPublishPointIsScoringLoop 同一手法：接线位置测不出来，就钉源码形状）。
func readEngineSrc(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s: %v", name, err)
	}
	return string(raw)
}

// TestSectorWorkerJobsAreGuardedPerJob 板块并发拉取的 worker：每个任务必须各自包一层 recover，
// 且守卫必须在 `for nf := range jobs` 体内（在 goroutine 顶部＝worker 带病退出、投喂侧永久阻塞）。
func TestSectorWorkerJobsAreGuardedPerJob(t *testing.T) {
	src := readEngineSrc(t, "engine.go")
	at := strings.Index(src, "for nf := range jobs {")
	if at < 0 {
		t.Fatalf("找不到板块 worker 的任务循环（形状变了，本锁的对照点失效）")
	}
	// 取"循环起点 → 本任务回传 results"这段：守卫必须落在这段之内，才可能是按单任务收的。
	send := strings.Index(src[at:], "results <- r")
	if send < 0 {
		t.Fatalf("找不到板块结果回传点（worker 形状变了，本锁需同批更新）")
	}
	seg := src[at : at+send+len("results <- r")]
	for _, need := range []string{"recover()", "debug.Stack()", "results <- r"} {
		if !strings.Contains(seg, need) {
			t.Fatalf("板块 worker 的单任务守卫缺 %s（少一份板块名单可以降级，进程死了是没人在管持仓）", need)
		}
	}
	// 反向锁：不许把守卫"简化"回 goroutine 顶部（那会留下阻塞形状）。
	if strings.Contains(seg, "defer wg.Done()\n\t\t\tfor nf := range jobs {\n\t\t\t\tdefer func()") {
		t.Fatalf("守卫被挪到 worker 顶部＝panic 后 jobs 无人消费、投喂侧永久阻塞")
	}
}

// TestConceptSyncGoroutineGuarded 概念库日同步的 goroutine 必须有 recover（今日失败次日再试，不在本 goroutine 重试）。
func TestConceptSyncGoroutineGuarded(t *testing.T) {
	src := readEngineSrc(t, "engine.go")
	at := strings.Index(src, "go func() {\n\t\t// 概念库同步")
	if at < 0 {
		// 注释文字可能被 gofmt 之外的改动挪位，退而求其次按调用点定位。
		at = strings.Index(src, "dc.SyncConcepts(st, 0)")
		if at < 0 {
			t.Fatalf("找不到概念库同步调用点（§C1-V2 搬迁了，本锁需同批更新）")
		}
	}
	seg := src[at : at+1200]
	if !strings.Contains(seg, "recover()") || !strings.Contains(seg, "debug.Stack()") {
		t.Fatalf("概念库同步 goroutine 缺 panic 守卫")
	}
}

// TestTotalAssetsOfHasNoDeadParam totalAssetsOf 的 db 参数在 §P1 之后成了死参数
// （函数体只走 r.ctrl）。死参数是"改了一半"的可 grep 痕迹，留着会让下次误以为它仍读账本。
func TestTotalAssetsOfHasNoDeadParam(t *testing.T) {
	src := readEngineSrc(t, "intraday_t_live.go")
	if strings.Contains(src, "func (r *tLiveRound) totalAssetsOf(db *store.DB)") {
		t.Fatalf("totalAssetsOf 又带上了 db 死参数（体内并不读 db）")
	}
	if !strings.Contains(src, "func (r *tLiveRound) totalAssetsOf() float64 {") {
		t.Fatalf("totalAssetsOf 签名形态变了，请同批复核本锁与调用点")
	}
	// 两个装配点都必须显式给出 ctrl（心跳那处曾经漏填，见 §FUNDS-HEARTBEAT）。
	for i, seg := range splitLiterals(src, "&tLiveRound{") {
		if !strings.Contains(seg, "ctrl: ctrl,") {
			t.Fatalf("第 %d 个 tLiveRound 装配点缺 ctrl: ctrl", i+1)
		}
	}
}

// splitLiterals 按字面量起点取出括号配平的片段列表（内层 map 字面量的花括号必须算进配平）。
func splitLiterals(src, mark string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(src[i:], mark)
		if j < 0 {
			return out
		}
		start := i + j + len(mark) - 1 // 从 '{' 起配平
		seg := balancedBraces(src[start:])
		if seg == "" {
			return out
		}
		out = append(out, seg)
		i = start + len(seg)
	}
}
