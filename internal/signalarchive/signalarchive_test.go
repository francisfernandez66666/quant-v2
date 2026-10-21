// §LOGCROSS（2026-10-22）：信号批次按交易日归档的判据锁。
//
// 归档是「战法筛选跨批次」的数据面：signal_records.json 只保留当日，隔天即失效；本包每天写一份
// signal_records_<YYYYMMDD>.json，读侧按「最近 N 个有归档的交易日 + 当日内存」合并。锁的语义：
//   - 当日内存非空 → 以内存为准，跳过同日归档（否则同一批信号出现两份）；
//   - 当日内存为空（引擎刚启动/当日尚无信号）→ 当日归档是**合法来源**，不能跳过；
//   - days 含当日（days=5 = 当日 + 4 个更早归档），按时间升序返回；
//   - 归档损坏/目录缺失/外来文件不阻断其余日期；Prune 只删本包命名的旧文件。
//
// English: locks the per-trading-day signal archive — memory wins over today's archive, an empty
// memory falls back to it, `days` includes today, ascending order, corrupt/foreign files tolerated,
// and pruning only touches files this package owns.
package signalarchive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
)

func rec(t time.Time, code string) combat_agent.SignalLog {
	return combat_agent.SignalLog{
		ProcessTime: t,
		RawCount:    1,
		Signals:     []combat_agent.Signal{{Code: code, Name: code}},
	}
}

func codes(recs []combat_agent.SignalLog) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		if len(r.Signals) > 0 {
			out = append(out, r.Signals[0].Code)
		}
	}
	return out
}

func day(base time.Time, offset int) string {
	return base.AddDate(0, 0, offset).Format("20060102")
}

func mustWrite(t *testing.T, dir, d string, recs []combat_agent.SignalLog) {
	t.Helper()
	Write(dir, d, recs, 1000) // keep 足够大，避免 Write 内部 Prune 干扰用例
}

// TestWriteCreatesPerDayFile 归档文件名必须带交易日（signal_records_<YYYYMMDD>.json），
// 且内容保留 trading_day 与完整批次——跨天复盘靠文件名取日，靠字段自证是哪一天。
func TestWriteCreatesPerDayFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "20261021", []combat_agent.SignalLog{rec(time.Now(), "600001")})
	raw, err := os.ReadFile(filepath.Join(dir, "signal_records_20261021.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		TradingDay string                   `json:"trading_day"`
		Records    []combat_agent.SignalLog `json:"records"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.TradingDay != "20261021" || len(f.Records) != 1 {
		t.Fatalf("归档结构不符: %+v", f)
	}
}

// 当日内存非空 → 同日归档必须被跳过，结果只有内存那一份（防重复批次）。
func TestReadRangeCurrentWinsOverTodayArchive(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	today := day(base, 0)
	mustWrite(t, dir, today, []combat_agent.SignalLog{rec(base.Add(-time.Hour), "archive-today")})
	cur := []combat_agent.SignalLog{rec(base, "mem-today")}
	out := ReadRange(dir, cur, 5, today)
	if got := codes(out); len(got) != 1 || got[0] != "mem-today" {
		t.Fatalf("当日内存必须压过同日归档，got %v", got)
	}
}

// 当日内存为空（引擎刚启动/当日尚无信号）→ 当日归档是唯一来源，必须能读到。
// 这是本包最容易被"去重"逻辑写坏的一条：跳过条件若只看 day==today，会把唯一来源也跳过。
func TestReadRangeEmptyCurrentFallsBackToTodayArchive(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	today := day(base, 0)
	mustWrite(t, dir, today, []combat_agent.SignalLog{rec(base, "archive-today")})
	out := ReadRange(dir, nil, 5, today)
	if got := codes(out); len(got) != 1 || got[0] != "archive-today" {
		t.Fatalf("空内存必须回落到当日归档，got %v", got)
	}
}

// days 含当日：有内存时 days=2 → 1 个更早归档 + 当日内存。
func TestReadRangeDaysWindowWithCurrent(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	today := day(base, 0)
	mustWrite(t, dir, day(base, -1), []combat_agent.SignalLog{rec(base.AddDate(0, 0, -1), "d1")})
	mustWrite(t, dir, day(base, -2), []combat_agent.SignalLog{rec(base.AddDate(0, 0, -2), "d2")})
	mustWrite(t, dir, day(base, -3), []combat_agent.SignalLog{rec(base.AddDate(0, 0, -3), "d3")})
	cur := []combat_agent.SignalLog{rec(base, "today")}
	out := ReadRange(dir, cur, 2, today)
	if got := codes(out); len(got) != 2 || got[0] != "d1" || got[1] != "today" {
		t.Fatalf("days=2 应返回 d1+today 且升序，got %v", got)
	}
}

// 无内存时 days 从当日归档开始数：days=2 → 当日归档 + 前一日归档。
func TestReadRangeDaysWindowWithoutCurrent(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	today := day(base, 0)
	mustWrite(t, dir, today, []combat_agent.SignalLog{rec(base, "today")})
	mustWrite(t, dir, day(base, -1), []combat_agent.SignalLog{rec(base.AddDate(0, 0, -1), "d1")})
	mustWrite(t, dir, day(base, -2), []combat_agent.SignalLog{rec(base.AddDate(0, 0, -2), "d2")})
	out := ReadRange(dir, nil, 2, today)
	if got := codes(out); len(got) != 2 || got[0] != "d1" || got[1] != "today" {
		t.Fatalf("days=2 应返回 d1+today 且升序，got %v", got)
	}
}

// 归档内批次时间乱序也要按时间升序输出（handler 依赖升序再倒排）。
func TestReadRangeSortsAscending(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	today := day(base, 0)
	mustWrite(t, dir, day(base, -1), []combat_agent.SignalLog{
		rec(base.AddDate(0, 0, -1).Add(2*time.Hour), "late"),
		rec(base.AddDate(0, 0, -1), "early"),
	})
	cur := []combat_agent.SignalLog{rec(base, "today")}
	out := ReadRange(dir, cur, 2, today)
	if got := codes(out); len(got) != 3 || got[0] != "early" || got[1] != "late" || got[2] != "today" {
		t.Fatalf("必须按时间升序，got %v", got)
	}
}

// 目录不存在/外来文件/损坏 JSON 都不能阻断其余日期。
func TestReadRangeToleratesMissingDirForeignAndCorruptFiles(t *testing.T) {
	if out := ReadRange(filepath.Join(t.TempDir(), "nope"), nil, 5, "20261022"); len(out) != 0 {
		t.Fatalf("目录不存在应返回空，got %v", codes(out))
	}

	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	today := day(base, 0)
	mustWrite(t, dir, day(base, -2), []combat_agent.SignalLog{rec(base.AddDate(0, 0, -2), "good")})
	if err := os.WriteFile(filepath.Join(dir, "signal_records_"+day(base, -1)+".json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := ReadRange(dir, nil, 5, today)
	if got := codes(out); len(got) != 1 || got[0] != "good" {
		t.Fatalf("损坏/外来文件应被跳过，got %v", got)
	}
	// 外来文件必须原样保留
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("Prune 不得删除外来文件: %v", err)
	}
}

// TestPruneKeepsNewestAndForeignFiles 清理只留最新 keep 份，且**只碰本包命名的文件**：
// 目录若被运维放了说明文件或日志，按后缀乱删会把现场证据一起清掉。
func TestPruneKeepsNewestAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 22, 10, 0, 0, 0, time.Local)
	for i := -4; i <= 0; i++ {
		mustWrite(t, dir, day(base, i), []combat_agent.SignalLog{rec(base.AddDate(0, 0, i), "x")})
	}
	foreign := filepath.Join(dir, "signal_records_not-a-day.json")
	if err := os.WriteFile(foreign, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	Prune(dir, 3)
	names, err := listArchives(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"signal_records_" + day(base, -2) + ".json",
		"signal_records_" + day(base, -1) + ".json",
		"signal_records_" + day(base, 0) + ".json",
	}
	if len(names) != len(want) {
		t.Fatalf("应保留最新 3 份，got %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("保留文件名不符: got %v want %v", names, want)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("外来命名文件必须保留: %v", err)
	}
}

func TestWriteIgnoresEmptyDirOrDay(t *testing.T) {
	Write("", "20261022", []combat_agent.SignalLog{rec(time.Now(), "x")}, 30)
	Write(t.TempDir(), "", []combat_agent.SignalLog{rec(time.Now(), "x")}, 30)
	// 不 panic、不建目录即通过
}
