// Package signalarchive 信号批次历史库：按交易日归档 signal_records 快照，供信号日志跨天复盘。
//
// 为什么需要独立归档：`signal_records.json` 是**当日**文件（引擎启动与跨日滚动都只认当天），
// 隔天就会被覆盖/忽略，所以「按战法回看几天前的信号」在数据面根本走不通（台账 2026-10-15 第三条）。
// 本包把每轮落盘的当日全量快照同时写一份 `signal_records_<YYYYMMDD>.json`，按天独立、原子写入、
// 只保留最近 N 个交易日；读侧按「最近 N 个有归档的交易日 + 引擎内存中的当日」合并，时间升序返回。
//
// 边界：
//   - 只存信号批次快照本身，不做任何补价/回填——历史批次的涨跌幅必须来自生成时刻的冻结字段；
//   - 归档写失败只记日志，不影响当日主文件的落盘（复盘能力退化，不能拖垮信号生产）；
//   - 目录为空/无权限时读侧退化为「只看内存当日」，绝不 panic。
package signalarchive

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"quant-trading-v2/internal/combat_agent"
)

const (
	// filePrefix 归档文件名前缀，完整形态 signal_records_20261021.json。
	filePrefix = "signal_records_"
	// fileSuffix 归档文件名后缀。
	fileSuffix = ".json"
	// DefaultKeepDays 默认保留的交易日数：30 天足够覆盖一个月的复盘窗口，
	// 同时把归档目录控制在几十份小文件内（每份只有信号批次，不含行情全量）。
	DefaultKeepDays = 30
)

// file 归档文件的磁盘结构（与引擎当日 signal_records.json 同形，便于人工对照）。
type file struct {
	TradingDay string                   `json:"trading_day"` // 交易日 YYYYMMDD
	Records    []combat_agent.SignalLog `json:"records"`     // 该交易日全部信号批次（时间升序）
}

// Write 把某交易日的全量批次快照写入归档（同一天再次写入 = 覆盖为最新全量），
// 随后清理超出保留窗口的旧文件。写失败只记日志：归档是复盘增强面，不能反向拖垮信号主链。
// English: writes one day's full snapshot (overwrites the same day) and prunes old files; failures
// are logged, never propagated — archiving must not take down signal production.
func Write(dir, day string, records []combat_agent.SignalLog, keep int) {
	if dir == "" || day == "" {
		return
	}
	if keep <= 0 {
		keep = DefaultKeepDays
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[signalarchive] 创建归档目录失败 %s: %v", dir, err)
		return
	}
	raw, err := json.MarshalIndent(file{TradingDay: day, Records: records}, "", "  ")
	if err != nil {
		log.Printf("[signalarchive] 归档序列化失败 day=%s: %v", day, err)
		return
	}
	path := filepath.Join(dir, filePrefix+day+fileSuffix)
	if err := atomicWrite(path, raw); err != nil {
		log.Printf("[signalarchive] 归档写入失败 %s: %v", path, err)
		return
	}
	Prune(dir, keep)
}

// Prune 只保留文件名（即交易日）最新的 keep 份归档，其余删除。
// 仅处理本包命名规则的文件，目录里的人工文件一概不碰。
// English: keeps the newest `keep` archives by trading-day filename; unrelated files are untouched.
func Prune(dir string, keep int) {
	if dir == "" || keep <= 0 {
		return
	}
	names, err := listArchives(dir)
	if err != nil {
		return
	}
	if len(names) <= keep {
		return
	}
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			log.Printf("[signalarchive] 清理旧归档失败 %s: %v", name, err)
		}
	}
}

// ReadRange 合并「最近 days 个交易日」的信号批次，时间升序返回。
//
//   - current 是引擎内存中的当日批次；非空时视为当日最新版本，读取时跳过同日归档（归档可能落后半轮）；
//   - current 为空（引擎刚启动/当日尚无信号）时，当日归档也是合法来源；
//   - days 含当日：days=5 表示当日 + 最多 4 个更早的有归档交易日；
//   - 不存在的日期不占位，最多返回 days 天，少则如实返回。
//
// English: merges up to `days` trading days (today plus prior archived days), ascending by time.
// In-memory today's batches win over today's archive when present; an empty memory falls back to it.
func ReadRange(dir string, current []combat_agent.SignalLog, days int, today string) []combat_agent.SignalLog {
	if days < 1 {
		days = 1
	}
	limit := days
	if len(current) > 0 {
		limit-- // 当日已由内存代表
	}
	out := make([]combat_agent.SignalLog, 0, len(current))
	if dir != "" && limit > 0 {
		names, err := listArchives(dir)
		if err == nil {
			chosen := make([]string, 0, limit)
			// names 已按日期升序，从尾部（最新）向前挑，最后再按时间升序拼装。
			for i := len(names) - 1; i >= 0 && len(chosen) < limit; i-- {
				name := names[i]
				// 内存有当日批次时以内存为准，跳过同日归档避免同一批信号出现两份；
				// 内存为空（引擎刚启动/当日尚无信号）时当日归档才是唯一来源，不能跳过。
				if len(current) > 0 && dayOf(name) == today {
					continue
				}
				chosen = append(chosen, name)
			}
			for i := len(chosen) - 1; i >= 0; i-- {
				out = append(out, readFile(filepath.Join(dir, chosen[i]))...)
			}
		}
	}
	out = append(out, current...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ProcessTime.Before(out[j].ProcessTime) })
	return out
}

// listArchives 列出目录内符合命名规则的归档文件名，按日期升序（文件名天然可比）。
// 目录不存在视为空；其他错误同样返回空（读侧退化为仅当日，不影响接口可用性）。
// English: lists matching archive filenames in ascending day order; missing/unreadable dir = empty.
func listArchives(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		if len(dayOf(name)) != 8 {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// dayOf 从归档文件名提取交易日：signal_records_20261021.json → 20261021。
// 非本包命名的文件返回空串（调用方按不匹配处理）。
// English: extracts YYYYMMDD from signal_records_YYYYMMDD.json (empty for foreign names).
func dayOf(name string) string {
	if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
}

// readFile 读取单份归档；文件缺失/解析失败返回 nil（单日损坏不阻断其余日期）。
// English: reads one archive; missing/corrupt files contribute nothing to the merged result.
func readFile(path string) []combat_agent.SignalLog {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[signalarchive] 归档解析失败 %s: %v", path, err)
		return nil
	}
	return f.Records
}

// atomicWrite 先写同目录临时文件再 rename，保证归档在崩溃/断电下不出现半截 JSON。
// 与引擎的 atomic_write.go 同策略；本包独立一份避免 engine ↔ signalarchive 的循环依赖。
// English: write-temp-then-rename so a crash cannot leave a truncated archive.
func atomicWrite(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
