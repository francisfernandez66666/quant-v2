// trade_calendar_cache_test.go — §ENH-0 交易日历磁盘缓存的单测：落盘/回读、
// 冷启动与坏文件分流、路径口径（QUANT_DATA_DIR 优先）。
package data

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestTradingCalendarCacheRoundTrip §ENH-0：落盘→读回必须无损（含"空休市日集合"也要能与"无缓存"区分）。
func TestTradingCalendarCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "trading_calendar.json") // 子目录不存在→验证自动建目录
	if err := SaveTradingCalendarCache(path, []string{"20261001", "20261002"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	days, savedAt, err := LoadTradingCalendarCache(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if savedAt == "" {
		t.Fatal("saved_at 应非空")
	}
	if len(days) != 2 || days[0] != "20261001" || days[1] != "20261002" {
		t.Fatalf("休市日回读不符: %v", days)
	}
	// 空集合（窗口内无休市日）也必须可回读，且不误判为"文件不存在"
	if err := SaveTradingCalendarCache(path, nil); err != nil {
		t.Fatalf("save empty: %v", err)
	}
	days, savedAt, err = LoadTradingCalendarCache(path)
	if err != nil || savedAt == "" {
		t.Fatalf("空集合应可回读: days=%v savedAt=%q err=%v", days, savedAt, err)
	}
	// 空路径 = no-op（不落盘也不报错）
	if err := SaveTradingCalendarCache("", []string{"x"}); err != nil {
		t.Fatalf("empty path should be no-op: %v", err)
	}
}

// TestLoadTradingCalendarCacheAbsentAndCorrupt 冷启动（文件不存在）与坏文件的分流语义。
func TestLoadTradingCalendarCacheAbsentAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	// 不存在 → (nil,"",nil) 正常冷启动，不算错误
	days, savedAt, err := LoadTradingCalendarCache(filepath.Join(dir, "absent.json"))
	if err != nil || days != nil || savedAt != "" {
		t.Fatalf("absent should be nil,nil,nil: days=%v savedAt=%q err=%v", days, savedAt, err)
	}
	// 坏 JSON → 报错（调用方记日志后按周末口径起步）
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadTradingCalendarCache(bad); err == nil {
		t.Fatal("坏文件应返回错误")
	}
	// 缺 saved_at 标记 → 视为无效缓存报错，防止半写文件被当真
	noMark := filepath.Join(dir, "nomark.json")
	raw, _ := json.Marshal(map[string]any{"closed_days": []string{"20261001"}})
	if err := os.WriteFile(noMark, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadTradingCalendarCache(noMark); err == nil {
		t.Fatal("缺 saved_at 标记应判无效")
	}
}

// TestCalendarCacheFilePathUsesDataDirEnv 路径口径：QUANT_DATA_DIR 优先于家目录（与 llmcfg/scheduler 一致）。
func TestCalendarCacheFilePathUsesDataDirEnv(t *testing.T) {
	t.Setenv("QUANT_DATA_DIR", t.TempDir())
	got := calendarCacheFilePath()
	if filepath.Base(got) != "trading_calendar.json" {
		t.Fatalf("文件名口径不符: %s", got)
	}
	if filepath.Dir(got) != os.Getenv("QUANT_DATA_DIR") {
		t.Fatalf("应落在 QUANT_DATA_DIR 下: %s", got)
	}
}
