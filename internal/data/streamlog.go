// streamlog.go — §WS-G 行情快照流录制/回放：把每轮 5s 采集的 MarketSnapshot 追加为 JSONL
// （quote_stream.jsonl），供 staging 回放 harness 以"与线上同输入"驱动打分循环。
// English: §WS-G quote-stream recording/replay — appends every 5s MarketSnapshot as one JSONL line
// (quote_stream.jsonl) so the staging harness can drive the scoring loop on production-identical input.
package data

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// StreamLog 行情快照流录制器（追加写 + 节流 flush；staging 专用，失败不阻断采集）。
// English: snapshot-stream recorder (append + throttled flush; staging-only; failures never block).
type StreamLog struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	tick int
}

// NewStreamLog 打开/创建录制文件（追加模式）。
// English: opens/creates the recording file in append mode.
func NewStreamLog(path string) (*StreamLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &StreamLog{f: f, w: bufio.NewWriter(f)}, nil
}

// Write 追加一轮快照（单行 JSON；每 6 拍 flush 一次，避免高频 syscall）。
// English: appends one snapshot as a JSON line; flushes every 6th write to avoid syscall churn.
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
