// Package opslog 每日系统运行日志（§DAILY_OPSLOG，2026-08-31）。
//
// 目的：把 quant 引擎与 research 研究调度每天"值得留档的核心运行记录"按本地日写成一份
// 人类可读文件（opslog-YYYYMMDD.log，UTF-8），供盘后复盘/巡检/对账。与全量诊断日志
// （stderr / gateway-<pid>.log / task_logs/task_<id>.log）互为补充——这里只记策划性低频事件：
//   - quant：进程启停、开盘资产快照/首次持仓对账、下单受理与拒因、成交回报、委托状态推进、
//     收盘清单撤单、熔断/恢复、kill-switch、网关断线
//   - research：任务入队/启动/终态/失败重试/抢占、夜间链首尾、内存总闸拦截（节流）
//
// 设计要点：
//   - 每次写入独立 打开-追加-关闭（O_APPEND）：quant 与 researchd 两个进程写同一目录时
//     行级追加安全（NTFS/POSIX 对小粒度 O_APPEND 写按文件尾原子定位），且无常驻句柄的
//     失效/滚动问题；事件频率低（每日几十行），开销可忽略
//   - 文件按本地日期滚动：文件名即日期，无需滚动逻辑
//   - 保留期默认 90 天：本地日期变更后的首次写入触发惰性清理（解析失败的名字一律跳过不删）
//   - best-effort：未 Init / 打开或写入失败一律静默降级（首次失败记一条 stderr），
//     绝不影响交易与研究主链路
//
// 用法：
//
//	opslog.Init(dataDir, 0)                 // 进程入口调用一次；keepDays<=0 取默认 90
//	opslog.Logf("quant", "成交 %s %s qty=%d", side, code, qty)
//	opslog.DayOnce("asset:"+uid, func(){ ... Logf ... })   // 每本地日至多一次（开盘快照等）
//	opslog.OncePer("memgate", time.Hour, func(){ ... })    // 节流（内存闸拦截等高频潜在事件）
//
// English: daily ops journal for the quant engine and research scheduler — one UTF-8 file per
// local day, curated low-frequency core events only, append-per-write for cross-process safety,
// lazy retention cleanup, strictly best-effort (never disturbs the trading/research paths).
package opslog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 日志文件名与行内时间戳的格式、保留策略常量。
const (
	// filePrefix 文件名前缀；dateLayout 文件名与行内时间戳共用的本地日期格式。
	filePrefix = "opslog-"
	dateLayout = "20060102"
	// lineLayout 行首时间戳格式（本地时区，量化主程序/researchd 均固定 Asia/Shanghai）。
	lineLayout = "2006-01-02 15:04:05"
	// defaultKeepDays 默认保留天数（保留期内的每日文件全量留存）。
	defaultKeepDays = 90
)

var (
	mu       sync.Mutex
	dir      string // 日志目录；空串=未 Init，Logf 直接静默返回
	keepDays int    // 保留天数
	lastDay  string // 上次写入的本地日期（YYYYMMDD）——跨日时触发保留期清理
	once     map[string]time.Time
	now      = time.Now // 可注入的时钟（测试用）
	warned   bool       // 写失败只告警一次，避免日志系统自身刷屏
)

// Init 初始化日志目录与保留期。dir 为空则保持禁用；重复调用幂等（换目录即生效）。
// English: Init sets the journal directory (empty disables) and retention days (<=0 → default).
func Init(d string, keep int) {
	mu.Lock()
	defer mu.Unlock()
	if d == "" {
		return
	}
	if keep <= 0 {
		keep = defaultKeepDays
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		warnLocked(fmt.Errorf("mkdir %s: %w", d, err))
		return
	}
	dir = d
	keepDays = keep
	lastDay = "" // 强制下次写入时做一次清理巡检
}

// Logf 追加一行核心记录：`2006-01-02 15:04:05 [tag] message`。
// 未 Init 或任何 IO 失败都静默降级（首次失败记 stderr）。并发/跨进程安全。
// English: append one curated line; silently no-op when uninitialized or on IO errors.
func Logf(tag, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if dir == "" {
		return
	}
	t := now()
	line := fmt.Sprintf("%s [%s] %s\n", t.Format(lineLayout), tag, fmt.Sprintf(format, args...))
	path := filepath.Join(dir, filePrefix+t.Format(dateLayout)+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		warnLocked(err)
		return
	}
	_, werr := io.WriteString(f, line)
	f.Close() // O_APPEND 追加写， Close 错误不影响已写入内容，忽略以保 best-effort 语义
	if werr != nil {
		warnLocked(werr)
		return
	}
	if d := t.Format(dateLayout); d != lastDay {
		lastDay = d
		sweepLocked(t) // 本地跨日后的首次写入：顺手清理过期文件（同步执行，目录内文件量小）
	}
}

// OncePer 同键节流：距上次执行不足 window 则跳过，否则执行 fn（fn 在锁外运行，
// 可安全调用 Logf）。适合"条件持续成立时会反复触达"的事件（如内存总闸拦截）。
// English: throttled execution — run fn at most once per window per key; fn runs unlocked.
func OncePer(key string, window time.Duration, fn func()) {
	if fn == nil {
		return
	}
	mu.Lock()
	t := now()
	if last, ok := once[key]; ok && t.Sub(last) < window {
		mu.Unlock()
		return
	}
	if once == nil {
		once = make(map[string]time.Time)
	}
	once[key] = t
	pruneLocked(t) // 顺带修剪早已过期的键，防长期运行下 map 缓涨
	mu.Unlock()
	fn()
}

// DayOnce 每本地日至多一次的便捷封装（键自动嵌入当日日期）。
// 适合开盘快照、每日首个对账等"按天去重"的记录点。
// English: run fn at most once per local calendar day per key.
func DayOnce(key string, fn func()) {
	OncePer(now().Format(dateLayout)+"|"+key, 48*time.Hour, fn)
}

// pruneLocked 清理 once 表中已远超窗口的键（阈值：超过 256 条时才值得扫一遍）。
func pruneLocked(t time.Time) {
	if len(once) <= 256 {
		return
	}
	for k, v := range once {
		if t.Sub(v) > 48*time.Hour {
			delete(once, k)
		}
	}
}

// sweepLocked 清理超过保留期的每日文件（解析失败/命名不符的一律跳过）。
// 按日历日期比较：严格早于「今天-保留天数」才删（恰为保留天数的文件保留）。
// 调用方持有 mu；文件数量级=保留天数，同步执行开销可忽略。
func sweepLocked(t time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := t.AddDate(0, 0, -keepDays).Format(dateLayout)
	var names []string
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, filePrefix) && strings.HasSuffix(n, ".log") {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(n, filePrefix), ".log"))
		}
	}
	sort.Strings(names) // 旧日期在前，可提前退出
	for _, n := range names {
		if _, err := time.ParseInLocation(dateLayout, n, time.Local); err != nil {
			continue // 非本包命名的文件不动
		}
		if n >= cutoff {
			break // 已按名排序，后续必然更新
		}
		if err := os.Remove(filepath.Join(dir, filePrefix+n+".log")); err != nil && !os.IsNotExist(err) {
			warnLocked(err)
		}
	}
}

// warnLocked 首次失败告警（stderr），之后静默——opslog 自身绝不能反噬主链路。
func warnLocked(err error) {
	if warned {
		return
	}
	warned = true
	log.Printf("[opslog] 写入失败，后续静默降级（不影响交易/研究主链路）: %v", err)
}
