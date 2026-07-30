// Package notify 信号历史记录，将信号持久化到 CSV 文件并支持按日期查询。
package notify

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SignalRecord 单条信号历史记录，包含时间、代码、策略、操作、评分等字段。
type SignalRecord struct {
	Time           time.Time
	Code           string
	Name           string
	Strategy       string
	Action         string
	Priority       int
	Score          float64
	D1, D2, D3, D4 float64
	Price          float64
	Level          string
}

// History 信号历史管理器，维护当日 CSV 文件写入和内存记录。
type History struct {
	mu      sync.Mutex
	file    *os.File
	writer  *csv.Writer
	records []SignalRecord
	dir     string
	today   string
}

// NewHistory 创建历史管理器，自动创建或追加当日 CSV 文件。
func NewHistory(dir string) *History {
	h := &History{dir: dir}
	h.ensureFile()
	return h
}

// Record 记录一条信号，追加到内存列表并写入 CSV。
func (h *History) Record(sig *SignalRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.records = append(h.records, *sig)

	if h.writer != nil {
		record := []string{
			sig.Time.Format("2006-01-02 15:04:05"),
			sig.Code, sig.Name, sig.Strategy, sig.Action,
			fmt.Sprintf("%d", sig.Priority),
			fmt.Sprintf("%.1f", sig.Score),
			fmt.Sprintf("%.1f", sig.D1),
			fmt.Sprintf("%.1f", sig.D2),
			fmt.Sprintf("%.1f", sig.D3),
			fmt.Sprintf("%.1f", sig.D4),
			fmt.Sprintf("%.2f", sig.Price),
			sig.Level,
		}
		h.writer.Write(record)
		h.writer.Flush()
	}
}

// TodayRecords 返回今天的所有信号记录。
func (h *History) TodayRecords() []SignalRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filterByDate(time.Now())
}

// YesterdayRecords 返回昨天的所有信号记录。
func (h *History) YesterdayRecords() []SignalRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filterByDate(time.Now().AddDate(0, 0, -1))
}

func (h *History) filterByDate(t time.Time) []SignalRecord {
	date := t.Format("2006-01-02")
	var out []SignalRecord
	for _, r := range h.records {
		if r.Time.Format("2006-01-02") == date {
			out = append(out, r)
		}
	}
	return out
}

// Summary 生成今日信号汇总日报文字，含强信号/观察/买入/卖出计数。
func (h *History) Summary() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	var strong, observe, buy, sell int
	for _, r := range h.records {
		if r.Time.Format("2006-01-02") != today {
			continue
		}
		switch r.Level {
		case "strong":
			strong++
		case "observe":
			observe++
		}
		switch r.Action {
		case "buy":
			buy++
		case "sell":
			sell++
		}
	}

	return fmt.Sprintf(`=== 量仔 交易信号日报 ===
日期: %s
总信号数: %d
强信号: %d
观察中: %d
买入建议: %d
卖出建议: %d
`, today, strong+observe, strong, observe, buy, sell)
}

// Close 刷新并关闭 CSV 文件。
func (h *History) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.writer != nil {
		h.writer.Flush()
	}
	if h.file != nil {
		h.file.Close()
	}
}

// ensureFile 检查日期是否变更，按日切换 CSV 文件并写入表头。
func (h *History) ensureFile() {
	today := time.Now().Format("2006-01-02")
	if h.today == today && h.file != nil {
		return
	}
	h.today = today

	if h.file != nil {
		h.file.Close()
	}

	path := filepath.Join(h.dir, fmt.Sprintf("signals_%s.csv", today))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.Printf("history file %s: %v", path, err)
		return
	}

	h.file = f
	h.writer = csv.NewWriter(f)

	if info, _ := f.Stat(); info.Size() == 0 {
		h.writer.Write([]string{"time", "code", "name", "strategy", "action", "priority", "score", "d1", "d2", "d3", "d4", "price", "level"})
		h.writer.Flush()
	}

	log.Printf("信号历史写入 %s", path)
}
