// streamlog.go — §WS-G 行情快照流录制/回放：把每轮 5s 采集的 MarketSnapshot 追加为 JSONL
// （quote_stream.jsonl），供 staging 回放 harness 以"与线上同输入"驱动打分循环。
// English: §WS-G quote-stream recording/replay — appends every 5s MarketSnapshot as one JSONL line
// (quote_stream.jsonl) so the staging harness can drive the scoring loop on production-identical input.
package data

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
)

// streamLogKeepDays §0926E2E-17B：按日轮转归档的保留天数（超期归档在跨日切换时惰性清理）。
const streamLogKeepDays = 30

// StreamLog 行情快照流录制器（追加写 + 节流 flush；staging 专用，失败不阻断采集）。
// §0926E2E-17B 按日轮转：当前文件恒为 quote_stream.jsonl（当日数据），北京日历日变更的
// 首拍把昨日内容改名归档为 quote_stream-YYYYMMDD.jsonl 并开新文件；归档超保留期惰性删除。
// 旧实现单文件无上限追加，5s 一拍全池快照长期开录制会把盘吃穿。
// English: snapshot-stream recorder (append + throttled flush; staging-only; failures never block).
// §0926E2E-17B rotates daily: the live file stays quote_stream.jsonl; the first write of a new
// Beijing day archives the previous content as quote_stream-YYYYMMDD.jsonl and starts fresh.
type StreamLog struct {
	mu    sync.Mutex
	path  string // 当前活动文件路径（轮转后仍是它）
	day   string // 活动文件所属北京日期 YYYYMMDD
	f     *os.File
	w     *bufio.Writer
	tick  int
	nowFn func() time.Time // 测试注入时钟（nil=time.Now）
}

// beijingDayOf 取北京日历日 YYYYMMDD（§TZ1 统一走 cntime）。
func beijingDayOf(t time.Time) string {
	return cntime.In(t).Format("20060102")
}

// NewStreamLog 打开/创建录制文件（追加模式）。
// §0926E2E-17B：活动文件所属日记为当前北京日（既有旧内容按"今日"处理，跨日照常轮转归档）。
// English: opens/creates the recording file in append mode.
func NewStreamLog(path string) (*StreamLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &StreamLog{path: path, day: beijingDayOf(time.Now()), f: f, w: bufio.NewWriter(f)}, nil
}

// now 取当前时间（测试可经 nowFn 注入时钟，跨日轮转用例依赖此缝）。
func (s *StreamLog) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// archivedPath 由活动路径推出某日的归档路径：quote_stream.jsonl → quote_stream-YYYYMMDD.jsonl。
// （拆分扩展名前缀，保持 LoadStreamLog 对归档文件直接可用。）
func archivedPath(path, day string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "-" + day + ext
}

// rotateLocked 跨日轮转（调用方持锁）：flush+close → 旧文件按"文件所属日"归档改名 →
// 清理超保留期归档 → 开新的活动文件。任一步失败只记日志退回"继续写旧文件"，绝不阻断采集。
func (s *StreamLog) rotateLocked(newDay string) {
	_ = s.w.Flush()
	_ = s.f.Close()
	archive := archivedPath(s.path, s.day)
	if err := os.Rename(s.path, archive); err != nil {
		// 归档失败：重新打开旧文件继续追加（宁可不轮转，不丢在途录制）
		log.Printf("[streamlog] §0926E2E-17B 归档改名失败（跳过本轮轮转）: %v", err)
		f, oerr := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if oerr != nil {
			log.Printf("[streamlog] §0926E2E-17B 重开活动文件失败: %v", oerr)
			return
		}
		s.f, s.w = f, bufio.NewWriter(f)
		return
	}
	s.pruneArchivesLocked()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		log.Printf("[streamlog] §0926E2E-17B 新开活动文件失败: %v", err)
		return
	}
	s.f, s.w, s.day = f, bufio.NewWriter(f), newDay
}

// pruneArchivesLocked 惰性清理：同目录归档按日期升序，删除早于保留窗口的文件。
// 文件名日期解析失败的（外来文件）一律跳过不删——与 opslog 保留策略同一口径。
func (s *StreamLog) pruneArchivesLocked() {
	ext := filepath.Ext(s.path)
	base := strings.TrimSuffix(filepath.Base(s.path), ext)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(s.path), base+"-*"+ext))
	if err != nil {
		return
	}
	cutoff := cntime.In(time.Now()).AddDate(0, 0, -streamLogKeepDays).Format("20060102")
	days := make([]string, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ext)
		day := strings.TrimPrefix(name, base+"-")
		if len(day) != 8 {
			continue
		}
		if _, perr := time.Parse("20060102", day); perr != nil {
			continue
		}
		days = append(days, day)
	}
	sort.Strings(days)
	for _, d := range days {
		if d < cutoff {
			_ = os.Remove(archivedPath(s.path, d))
		}
	}
}

// Write 追加一轮快照（单行 JSON；每 6 拍 flush 一次，避免高频 syscall）。
// §0926E2E-17B：每拍先比对北京日——跨日首拍触发按日轮转（昨日内容改名归档 + 开新文件）。
// English: appends one snapshot as a JSON line; flushes every 6th write; the first write of a new
// Beijing day triggers daily rotation (yesterday's file renamed to an archive, fresh live file).
func (s *StreamLog) Write(snap *MarketSnapshot) error {
	if snap == nil {
		return nil
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := beijingDayOf(s.now()); d != s.day {
		s.rotateLocked(d)
	}
	if _, err := s.w.Write(append(raw, '\n')); err != nil {
		return err
	}
	s.tick++
	if s.tick%6 == 0 {
		return s.w.Flush()
	}
	return nil
}

// Flush 显式落盘。
// English: flushes buffered lines to disk.
func (s *StreamLog) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Flush()
}

// Close 落盘并关闭。
// English: flushes and closes the recorder.
func (s *StreamLog) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		s.f.Close()
		return err
	}
	return s.f.Close()
}

// LoadStreamLog 读取录制流为快照序列（回放输入）。空/不存在返回空切片不报错。
// English: loads a recorded stream into snapshots (replay input); empty/missing file → nil, no error.
func LoadStreamLog(path string) ([]MarketSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil // 无录制文件 → 空输入（调用方按无数据处理）
	}
	defer f.Close()
	var out []MarketSnapshot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20) // 单行可达数 MB（全池快照）
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var snap MarketSnapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			continue // 跳过损坏行，不阻断回放
		}
		if snap.Stocks == nil {
			snap.Stocks = map[string]*StockInfo{}
		}
		out = append(out, snap)
	}
	return out, sc.Err()
}
