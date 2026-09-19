// trade_calendar.go — 运行时交易日历缓存（§GAP3.1）：
// 启动后经同花顺交易日历接口拉取近一年交易日序列，推导"非周末休市日"集合
// （法定节假日/临时休市），供 IsTradingDay/TradingDayDate/AddTradingDays/
// DurationToNextActiveSession 消费。加载失败时系统按周末口径兜底运行（不阻断启动）。
// English: runtime trading-calendar cache — derives non-weekend closed days (statutory
// holidays / ad-hoc closures) from the THS trading-day series; all trade-time predicates
// consume it, falling back to weekend-only semantics until loaded.
package data

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"quant-trading-v2/internal/cntime"
)

var (
	calMu      sync.RWMutex
	closedDays = map[string]bool{} // "20060102" → 非周末休市日
	calLoaded  bool                // 是否至少成功加载过一次
)

// SetClosedDays 注入非周末休市日集合（幂等覆盖）。
// English: installs the closed-day set (idempotent overwrite).
func SetClosedDays(dates []string) {
	calMu.Lock()
	defer calMu.Unlock()
	closedDays = make(map[string]bool, len(dates))
	for _, d := range dates {
		if len(d) == 8 {
			closedDays[d] = true
		}
	}
	calLoaded = true
}

// isClosedDay 查询某 YYYYMMDD 是否为非周末休市日（未加载日历时恒 false=周末口径兜底）。
func isClosedDay(yyyymmdd string) bool {
	calMu.RLock()
	defer calMu.RUnlock()
	return closedDays[yyyymmdd]
}

// CalendarLoaded 日历是否已成功加载（诊断/健康检查用）。
func CalendarLoaded() bool {
	calMu.RLock()
	defer calMu.RUnlock()
	return calLoaded
}

// ClosedDayCount 已加载的休市日数量（诊断用）。
func ClosedDayCount() int {
	calMu.RLock()
	defer calMu.RUnlock()
	return len(closedDays)
}

// RefreshTradingCalendar 拉取同花顺交易日历并推导休市日集合。
// 推导口径：API 覆盖窗口 [min,max] 内所有非周末日期，凡不在交易日序列中即休市日
// （不外推覆盖窗口之外的日期，避免误判未来未公布区间）。
// English: fetches the trading-day series and derives closed days within the covered window.
func RefreshTradingCalendar() error {
	hc, err := NewHithinkClient()
	if err != nil {
		return fmt.Errorf("hithink client: %w", err)
	}
	days, err := hc.TradingDays()
	if err != nil {
		return fmt.Errorf("trading days: %w", err)
	}
	if len(days) == 0 {
		return fmt.Errorf("empty calendar response")
	}
	trade := make(map[string]bool, len(days))
	minD, maxD := days[0].Date, days[0].Date
	for _, d := range days {
		if len(d.Date) != 8 {
			continue
		}
		trade[d.Date] = true
		if d.Date < minD {
			minD = d.Date
		}
		if d.Date > maxD {
			maxD = d.Date
		}
	}
	t0, err := time.ParseInLocation("20060102", minD, cntime.Loc)
	if err != nil {
		return fmt.Errorf("parse min date: %w", err)
	}
	t1, err := time.ParseInLocation("20060102", maxD, cntime.Loc)
	if err != nil {
		return fmt.Errorf("parse max date: %w", err)
	}
	var closed []string
	for t := t0; !t.After(t1); t = t.AddDate(0, 0, 1) {
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			continue
		}
		key := t.Format("20060102")
		if !trade[key] {
			closed = append(closed, key)
		}
	}
	SetClosedDays(closed)
	// §ENH-0(20260919)：拉取成功后原子落盘——下次冷启动即使 hithink 缺 key/停机，
	// 也能用上次结果，不再退化为"周末口径把法定节假日当交易日"。落盘失败不阻断本次加载。
	if err := SaveTradingCalendarCache(calendarCacheFilePath(), closed); err != nil {
		log.Printf("[calendar] 交易日历缓存落盘失败（本次已生效，但冷启动将退回周末口径）: %v", err)
	}
	log.Printf("[calendar] 交易日历已加载: 窗口 %s~%s，非周末休市日 %d 天", minD, maxD, len(closed))
	return nil
}

// tradingCalendarCache 交易日历磁盘缓存格式（§ENH-0）。
// English: on-disk trading-calendar cache format.
type tradingCalendarCache struct {
	SavedAt    string   `json:"saved_at"`    // 落盘日期 YYYY-MM-DD（北京时间），存在即视为有效缓存
	ClosedDays []string `json:"closed_days"` // 非周末休市日集合 YYYYMMDD（可为空=窗口内无休市日）
}

// calendarCacheFilePath 日历缓存文件路径：QUANT_DATA_DIR 优先，否则 ~/.quant-trading-v2
// （与 llmcfg/scheduler 的数据目录口径一致）；两者都取不到时返回空串=不落盘。
// English: cache path from QUANT_DATA_DIR else ~/.quant-trading-v2; empty means disabled.
func calendarCacheFilePath() string {
	dir := os.Getenv("QUANT_DATA_DIR")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".quant-trading-v2")
		}
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "trading_calendar.json")
}

// SaveTradingCalendarCache 将休市日集合原子落盘到 filePath（空路径跳过不算错误）。
// English: persists closed days atomically; empty path is a no-op.
func SaveTradingCalendarCache(filePath string, closed []string) error {
	if filePath == "" {
		return nil
	}
	if closed == nil {
		closed = []string{} // 区分"无休市日"与"文件不存在"，落空数组而非 null
	}
	raw, err := json.Marshal(tradingCalendarCache{SavedAt: cntime.DayOf(time.Now()), ClosedDays: closed})
	if err != nil {
		return err
	}
	// 目录可能不存在（首次部署/dataDir 拼错），静默失败会让"日历从不落盘"无迹可查，
	// 所以先建目录再原子写；原子写内部也是 MkdirAll+tmp+rename。
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}
	return atomicWrite(filePath, raw, 0o644)
}

// LoadTradingCalendarCache 读取日历缓存。返回 (nil, "", nil)=文件不存在（冷启动正常态）；
// 解析失败返回 err 供调用方记日志；SavedAt 为空视为无效缓存同样报错。
// English: reads the cache; nil,nil means absent; malformed returns err.
func LoadTradingCalendarCache(filePath string) ([]string, string, error) {
	if filePath == "" {
		return nil, "", nil
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	var c tradingCalendarCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, "", fmt.Errorf("parse calendar cache: %w", err)
	}
	if c.SavedAt == "" {
		return nil, "", fmt.Errorf("calendar cache missing saved_at marker")
	}
	return c.ClosedDays, c.SavedAt, nil
}

// LoadTradingCalendarAsync 启动后台日历刷新：先读磁盘缓存（§ENH-0），随后立即拉取一次，
// 之后每 24h 重试；失败仅记日志（缓存/周末口径兜底），绝不阻断主流程启动。
// English: async loader — seed from disk cache first, then immediate fetch and daily refresh.
func LoadTradingCalendarAsync() {
	go func() {
		// §ENH-0：冷启动先用上次落盘结果覆盖"周末口径"窗口（首个 API 成功前也在保护期内）。
		if days, savedAt, err := LoadTradingCalendarCache(calendarCacheFilePath()); err != nil {
			log.Printf("[calendar] 交易日历缓存读取失败（按周末口径起步，等待 API 刷新）: %v", err)
		} else if savedAt != "" {
			SetClosedDays(days)
			log.Printf("[calendar] 交易日历已从磁盘缓存加载（保存于 %s，休市日 %d 天）", savedAt, len(days))
		}
		for {
			if err := RefreshTradingCalendar(); err != nil {
				if CalendarLoaded() {
					log.Printf("[calendar] 交易日历刷新失败（沿用上次的内存/缓存口径）: %v", err)
				} else {
					log.Printf("[calendar] 交易日历加载失败（暂按周末口径兜底）: %v", err)
				}
			}
			time.Sleep(24 * time.Hour)
		}
	}()
}
