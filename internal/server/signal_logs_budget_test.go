// signal_logs_budget_test.go — §PAYLOAD-BOUND（2026-11-01）：/api/signal-logs 的三道闸。
//
// 背景（现网实测，2026-11-01）：`days` 只夹天数、不限批次数，归档窗口 30 天 × 每交易日约 100 批
// ⇒ `days=30` 一次返回 3183 批（未压缩 16.2 MB / gzip 1.9 MB / 4.2 s），缺省 `days=5` 也有 591 批。
// 本文件锁三件事：**批次上限**、**游标翻页**、**响应字节预算**，以及「三者不许互相顶掉」。
//
// English: locks the three payload guards on /api/signal-logs — batch cap, cursor paging, byte budget.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
)

// fakeSignalCtrl 只实现本端点用到的两个读方法。
// 嵌入 nil 的 EngineController 是本仓既有姿势（见 signalLogRangeProvider 的注释）：
// 未实现的方法一旦被调用会 panic，正好把「端点偷偷摸了别的能力」暴露成红，而不是静默通过。
// English: embeds a nil interface on purpose — any unexpected call panics instead of silently passing.
type fakeSignalCtrl struct {
	EngineController
	recs []combat_agent.SignalLog
}

// GetSignalLogs 当日内存批次（升序）。
func (f *fakeSignalCtrl) GetSignalLogs() []combat_agent.SignalLog { return f.recs }

// GetSignalLogsRange 跨天合并批次（升序）；本 fake 直接回同一份，days 语义由 signalLogDays 单测覆盖。
func (f *fakeSignalCtrl) GetSignalLogsRange(int) []combat_agent.SignalLog { return f.recs }

// mkBatch 造一批信号：每个信号的 Name 填 pad 个字符，用来精确控制单批体积。
func mkBatch(at time.Time, nSignals, pad int) combat_agent.SignalLog {
	sigs := make([]combat_agent.Signal, nSignals)
	for i := range sigs {
		sigs[i] = combat_agent.Signal{
			ID:       "s",
			Code:     "000001",
			Name:     strings.Repeat("x", pad),
			Strategy: "dragon",
			Action:   "买入",
			Price:    11.5,
		}
	}
	return combat_agent.SignalLog{ProcessTime: at, RawCount: 10, Signals: sigs}
}

// mkBatches 造 n 批，时间从 base 起每批 +1 分钟（升序，与引擎产出一致）。
func mkBatches(n, nSignals, pad int) []combat_agent.SignalLog {
	base := time.Date(2026, 11, 1, 9, 30, 0, 0, time.Local)
	out := make([]combat_agent.SignalLog, n)
	for i := 0; i < n; i++ {
		out[i] = mkBatch(base.Add(time.Duration(i)*time.Minute), nSignals, pad)
	}
	return out
}

// callSignalLogs 直接打 handler（不经 mux，避开鉴权装配），返回 recorder 与解析后的批次。
func callSignalLogs(t *testing.T, recs []combat_agent.SignalLog, query string) (*httptest.ResponseRecorder, []combat_agent.SignalLog) {
	t.Helper()
	s := &Server{ctrl: &fakeSignalCtrl{recs: recs}}
	r := httptest.NewRequest("GET", "/api/signal-logs"+query, nil)
	w := httptest.NewRecorder()
	s.handleSignalLogs(w, r)
	if w.Code != 200 {
		t.Fatalf("期望 200，实得 %d body=%s", w.Code, w.Body.String())
	}
	var out []combat_agent.SignalLog
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON 数组: %v body=%s", err, w.Body.String())
	}
	return w, out
}

// A1：缺省只回 20 批。反证靶心是「不限批次」——50 批全回就是 2.5 倍缺省预算。
func TestSignalLogBatchesDefaultCapsTo20(t *testing.T) {
	_, got := callSignalLogs(t, mkBatches(50, 3, 20), "")
	if len(got) != signalLogDefaultBatches {
		t.Fatalf("缺省批次数 = %d，期望 %d", len(got), signalLogDefaultBatches)
	}
}

// A2：?batches=N 生效。
func TestSignalLogBatchesParamRespected(t *testing.T) {
	_, got := callSignalLogs(t, mkBatches(50, 3, 20), "?batches=5")
	if len(got) != 5 {
		t.Fatalf("?batches=5 实得 %d 批", len(got))
	}
}

// A3：?batches 超上限夹到 200（防 99999 绕过预算闸把序列化成本打满）。
func TestSignalLogBatchesUpperClampedTo200(t *testing.T) {
	_, got := callSignalLogs(t, mkBatches(300, 2, 10), "?batches=99999")
	if len(got) > signalLogMaxBatches {
		t.Fatalf("?batches=99999 实得 %d 批，超过上限 %d", len(got), signalLogMaxBatches)
	}
	if len(got) != signalLogMaxBatches {
		t.Fatalf("应夹到 %d 批，实得 %d", signalLogMaxBatches, len(got))
	}
}

// A4：?batches 非法/非正数一律回落缺省（不静默变成「不限」）。
func TestSignalLogBatchesInvalidFallsBackTo20(t *testing.T) {
	for _, q := range []string{"?batches=abc", "?batches=0", "?batches=-1", "?batches="} {
		_, got := callSignalLogs(t, mkBatches(50, 3, 20), q)
		if len(got) != signalLogDefaultBatches {
			t.Fatalf("%s 实得 %d 批，期望回落 %d", q, len(got), signalLogDefaultBatches)
		}
	}
}

// A5：cursor 只放行**比它更新**的批次，且不含 cursor 那一批本身（严格大于）。
func TestSignalLogCursorFiltersOlder(t *testing.T) {
	all := mkBatches(3, 2, 10)
	cur := all[0].ProcessTime.UnixMilli()
	_, got := callSignalLogs(t, all, "?cursor="+itoa64(cur))
	if len(got) != 2 {
		t.Fatalf("cursor 过滤后应为 2 批，实得 %d", len(got))
	}
	for _, b := range got {
		if !b.ProcessTime.After(all[0].ProcessTime) {
			t.Fatalf("cursor 应排除不晚于它的批次，实得 %v", b.ProcessTime)
		}
	}
}

// A5b：before 是**反方向**——只放行更老的批次，供「加载更早」翻页。
// 这一条专门锁"一个方向不够"：方案原稿只有 `> cursor`，往更早翻会永远返回空。
// 场景按真实翻页走：60 批、每页 10 批，连翻两页，断言两页不相交且逐页变老。
func TestSignalLogBeforePagesOlder(t *testing.T) {
	all := mkBatches(60, 2, 10)
	// 第 1 页：比第 50 批更老的，取最新 10 批 ⇒ 应为下标 40..49
	_, page1 := callSignalLogs(t, all, "?batches=10&before="+itoa64(all[50].ProcessTime.UnixMilli()))
	if len(page1) != 10 {
		t.Fatalf("第 1 页应 10 批，实得 %d", len(page1))
	}
	// 返回是倒序（最新在前），故 page1[0] 是下标 49、page1[len-1] 是下标 40
	if !page1[0].ProcessTime.Equal(all[49].ProcessTime) {
		t.Fatalf("第 1 页首条应为第 49 批（最新在前），实得 %v", page1[0].ProcessTime)
	}
	oldest := page1[len(page1)-1].ProcessTime
	if !oldest.Equal(all[40].ProcessTime) {
		t.Fatalf("第 1 页末条应为第 40 批（本页最老），实得 %v", oldest)
	}
	// 第 2 页：拿本页最老那批当 before ⇒ 应为下标 30..39，且与第 1 页无交集
	_, page2 := callSignalLogs(t, all, "?batches=10&before="+itoa64(oldest.UnixMilli()))
	if len(page2) != 10 {
		t.Fatalf("第 2 页应 10 批，实得 %d", len(page2))
	}
	if !page2[0].ProcessTime.Equal(all[39].ProcessTime) {
		t.Fatalf("第 2 页首条应为第 39 批，实得 %v", page2[0].ProcessTime)
	}
	if !page2[0].ProcessTime.Before(oldest) {
		t.Fatal("第 2 页必须严格早于第 1 页，否则翻页会原地打转")
	}
}

// A6：字节预算。造 20 批 × 50 信号 × 1200 字符（远超 512 KB），
// 断言整包不超预算，且 X-Signal-Logs-Truncated=1（丢过就要说）。
func TestSignalLogResponseUnderBudget(t *testing.T) {
	w, got := callSignalLogs(t, mkBatches(20, 50, 1200), "")
	if n := w.Body.Len(); n > maxSignalLogBytes {
		t.Fatalf("响应 %d 字节，超预算 %d", n, maxSignalLogBytes)
	}
	if flag := w.Header().Get("X-Signal-Logs-Truncated"); flag != "1" {
		t.Fatalf("被裁过却未置 X-Signal-Logs-Truncated=1（实得 %q）", flag)
	}
	if len(got) == 0 {
		t.Fatal("预算裁剪不得裁成空数组——前端会误读成「今天没有信号」")
	}
}

// A7：倒序契约——最新在前。前端日志弹窗靠它把最新一轮放在顶部。
func TestSignalLogOrderNewestFirst(t *testing.T) {
	_, got := callSignalLogs(t, mkBatches(10, 2, 10), "")
	if len(got) < 2 {
		t.Fatalf("样本太少：%d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].ProcessTime.After(got[i-1].ProcessTime) {
			t.Fatalf("第 %d 批比前一批更新，倒序被破坏", i)
		}
	}
}

// A8：days 口径不许回归（signal_logs_days_test.go 锁解析，这里锁"handler 真的用了它"）。
// 说明：本 fake 不区分 days，故只断言 signalLogParams 透传 —— 解析层已被既有用例钉死。
func TestSignalLogDaysSemanticsUnchanged(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"/api/signal-logs", 5},
		{"/api/signal-logs?days=1", 1},
		{"/api/signal-logs?days=31", 30},
		{"/api/signal-logs?days=abc", 5},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.url, nil)
		if d := signalLogParams(r).days; d != c.want {
			t.Fatalf("%s → days=%d，期望 %d", c.url, d, c.want)
		}
	}
}

// A9：批次数不再由 days 唯一决定——days 变、批次数不变（证明新闸独立生效）。
func TestSignalLogBatchCountIndependentOfDays(t *testing.T) {
	recs := mkBatches(50, 2, 10)
	_, d1 := callSignalLogs(t, recs, "?days=1")
	_, d30 := callSignalLogs(t, recs, "?days=30")
	if len(d1) != len(d30) {
		t.Fatalf("days=1 与 days=30 批次数应相同（新闸独立），实得 %d vs %d", len(d1), len(d30))
	}
	if len(d1) != signalLogDefaultBatches {
		t.Fatalf("批次数应受 batches 缺省控制，实得 %d", len(d1))
	}
}

// itoa64 避免为一行整数格式化引入 strconv（与生产代码同值口径：毫秒时间戳十进制）。
func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
