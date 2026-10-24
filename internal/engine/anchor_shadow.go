// anchor_shadow.go — §ANCHOR-FIRST（2026-10-24 裁决二）W2 影子对照表在引擎侧的落地面。
//
// signalctl.CompareAnchor 是纯函数（一笔持仓、两个基准、各算一遍线位）；本文件把它接进真实
// 裁决轮次，产出一份**可签认的对照表**：
//   - 每个（账本 × 北京交易日）只在**该账本当日首轮有效裁决**记一次，写 <acctDir>/anchor_shadow.json；
//   - 同一时刻打一行日志总账（行数 / 线位移动数 / 判线翻转数 / 锚点未回填数），journald 直接可读；
//   - 文件按日累积，只保留最近 anchorShadowKeepDays 天——Q10 要「≥3 个交易日」的对照证据，
//     单日快照不够，但也不许把 5 秒轮变成 IO 热点，所以是"每日一行"，不是"每轮一行"。
//
// 为什么取"当日首轮"而不是收盘：表里真正价格无关的是**线位**（止损价/止盈价只由基准与阈值决定），
// 只有 LineChanged（本轮价格落在哪条线上）依赖取样时刻。取首轮＝取开盘附近那个读数，
// written_at 如实进表，读的人知道这一行代表哪一刻。
//
// 零处置原则：本文件不读持仓、不写账本、不改任何裁决结果——它只把已经装配好的 SellInput
// （含 §ANCHOR-FIRST 随行锚点）再算一遍差。切硬属 W3，且必须等 owner 看过这张表（Q10）。
//
// 每次**落盘**都走「读盘→改→原子写」，不在内存里长期缓存整份文档：跨轮缓存会造出
// "内存副本比盘上滞后一天"这种静默分叉，而这张表的全部价值就是它对得上盘上的数。
// 只有「今天这个账本记过了」这一枚标记留在内存（anchorShadowSampled）：当日某账本的读数一旦
// 写下就不再变化，拿它挡在读盘之前，5 秒轮才不会变成 IO 热点。
//
// English: the engine-side host of the W2 shadow table. The pure math is signalctl.CompareAnchor;
// here one sample per (book × Beijing day) is appended to <acctDir>/anchor_shadow.json plus a
// one-line ledger log. It reads no book, changes no verdict, and re-reads the file on every sample
// (a long-lived cache would fork memory from disk — the whole point of this table is that its
// numbers match the file). Hard cutover is W3, gated on the owner signing this table (Q10).
package engine

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

// anchorShadowKeepDays 对照表按日保留天数（Q10 要「≥3 个交易日」证据，留 60 天足够回看）。
// English: daily records kept in the artifact — Q10 needs at least three trading days of evidence.
const anchorShadowKeepDays = 60

// anchorShadowBookEntry 单个账本在某一日的对照读数（总账与明细同批生成，绝不"总账这批、明细那批"）。
// English: one book's snapshot for one day; totals and rows come from one and the same batch.
type anchorShadowBookEntry struct {
	Summary signalctl.AnchorShadowSummary `json:"summary"`
	Rows    []signalctl.AnchorShadowRow   `json:"rows"`
}

// anchorShadowDayEntry 一日内各账本的读数（book → 读数；live / paper / report 三口）。
// English: all books sampled for one Beijing day.
type anchorShadowDayEntry struct {
	Day   string                            `json:"day"`
	Books map[string]*anchorShadowBookEntry `json:"books"`
}

// anchorShadowFile <acctDir>/anchor_shadow.json 的整体结构（按账号数据目录天然隔离）。
// English: the file shape; the per-account data dir keeps accounts separated.
type anchorShadowFile struct {
	Account  string                 `json:"account"`
	KeepDays int                    `json:"keep_days"`
	Days     []anchorShadowDayEntry `json:"days"`
}

// recordAnchorShadow 记一次账本对照读数：同一 (book, 北京交易日) 只有首轮落盘，其余静默返回。
// 空表不交（len(rows)==0 直接返回）："0 行"分不清"今天全空仓"和"持仓快照还没到"，
// 这两种含义在 Q10 签认材料里正好相反——宁可缺行，也不写一行会误导的读数。
// anchorShadowPath 空＝纯内存测试路径：不落盘，日志总账照打（测试要能看见判据真走到过这里）。
// 落盘失败时**不打当日已取样标记**：下一轮会重试，宁吵不哑（对照表是切硬的前置材料）。
// English: samples once per (book, Beijing day); an empty table is never handed over ("0 rows"
// cannot tell "flat today" from "broker snapshot not in yet" — opposite readings for Q10).
// A failed write leaves the day unmarked so the next round retries: loud beats silent.
func (e *Engine) recordAnchorShadow(account, book string, rows []signalctl.AnchorShadowRow, now time.Time) {
	if len(rows) == 0 {
		return
	}
	summary := signalctl.SummarizeAnchorRows(rows, cntime.DayOf(now), now)
	e.anchorShadowMu.Lock()
	defer e.anchorShadowMu.Unlock()
	if e.anchorShadowSampled == nil {
		e.anchorShadowSampled = map[string]bool{}
	}
	key := book + "|" + summary.Day
	if e.anchorShadowSampled[key] {
		return // 当日该账本已记过：5 秒轮不得把这里变成 IO 热点
	}
	if e.anchorShadowPath == "" {
		e.anchorShadowSampled[key] = true // 纯内存面：日志即交付物
		logAnchorShadowLedger(book, summary)
		return
	}
	doc := loadAnchorShadowLocked(e.anchorShadowPath, account)
	if !doc.setBookEntry(summary.Day, book, &anchorShadowBookEntry{Summary: summary, Rows: rows}) {
		e.anchorShadowSampled[key] = true // 盘上已有当日读数（进程重启后的正常形态）：标记补齐，不再读盘
		return
	}
	if !e.writeAnchorShadowLocked(doc, book, summary) {
		return // 写失败：不标记，下一轮重试
	}
	e.anchorShadowSampled[key] = true
}

// setBookEntry 把某日某账本的读数并入文档；该日该账本已有读数时返回 false（当日只记首轮）。
// 新的一天追加在末尾并按保留天数裁剪——追加即最新，截头即保新。
// English: merges one sample; returns false when that book already has a reading for the day.
func (d *anchorShadowFile) setBookEntry(day, book string, entry *anchorShadowBookEntry) bool {
	for i := range d.Days {
		if d.Days[i].Day != day {
			continue
		}
		if d.Days[i].Books == nil {
			d.Days[i].Books = map[string]*anchorShadowBookEntry{}
		}
		if _, done := d.Days[i].Books[book]; done {
			return false
		}
		d.Days[i].Books[book] = entry
		return true
	}
	d.Days = append(d.Days, anchorShadowDayEntry{
		Day:   day,
		Books: map[string]*anchorShadowBookEntry{book: entry},
	})
	if len(d.Days) > anchorShadowKeepDays {
		d.Days = d.Days[len(d.Days)-anchorShadowKeepDays:]
	}
	return true
}

// writeAnchorShadowLocked 原子落盘 + 一行日志总账（调用方持 anchorShadowMu），返回是否真的落地。
// 写失败必须出声：对照表是 W3 切硬的前置材料，静默丢一天＝那一天没有证据。
// 日志只在写成功后打——"报了总账但文件没落地"正是本项目反复吃亏的自证缺口。
// English: atomic write plus the ledger line (caller holds the mutex) and reports whether it landed;
// the log only fires after a successful write ("reported totals but nothing on disk" is the
// self-证 gap this project keeps hitting).
func (e *Engine) writeAnchorShadowLocked(doc *anchorShadowFile, book string, summary signalctl.AnchorShadowSummary) bool {
	day := summary.Day
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		log.Printf("[anchor-shadow] §ANCHOR-FIRST 序列化失败 账本=%s 日=%s: %v", book, day, err)
		return false
	}
	ensureParentDir(e.anchorShadowPath)
	if err := data.AtomicWrite(e.anchorShadowPath, raw, 0644); err != nil {
		log.Printf("[anchor-shadow] §ANCHOR-FIRST 落盘失败 账本=%s 日=%s 路径=%s: %v", book, day, e.anchorShadowPath, err)
		return false
	}
	logAnchorShadowLedger(book, summary)
	return true
}

// loadAnchorShadowLocked 读回既有对照文件（缺失/损坏一律按空文档起步，绝不因读失败而清盘）。
// English: loads the artifact; missing or corrupt starts from an empty document, never wipes.
func loadAnchorShadowLocked(path, account string) *anchorShadowFile {
	doc := &anchorShadowFile{Account: account, KeepDays: anchorShadowKeepDays}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[anchor-shadow] §ANCHOR-FIRST 对照文件读取失败（本轮按空表起步）: %v", err)
		}
		return doc
	}
	var persisted anchorShadowFile
	if err := json.Unmarshal(raw, &persisted); err != nil {
		log.Printf("[anchor-shadow] §ANCHOR-FIRST 对照文件解析失败（本轮按空表起步）: %v", err)
		return doc
	}
	if persisted.Account != "" {
		doc.Account = persisted.Account
	}
	doc.Days = persisted.Days
	return doc
}

// logAnchorShadowLedger 打一行账本总账（与落盘同一时刻，日志与文件对得上账）。
// 未回填数必须单独看得见：回填完成前"差异 0"读起来像"切硬无影响"，实际是"还没得比"。
// English: one ledger line per sample; the un-backfilled count stays visible on its own — while
// anchors are missing, "0 divergences" means "nothing to compare yet", not "the cutover is harmless".
func logAnchorShadowLedger(book string, s signalctl.AnchorShadowSummary) {
	per := make([]string, 0, len(s.ByBook))
	for _, k := range []string{"live", "paper", "report"} {
		if n, ok := s.ByBook[k]; ok {
			per = append(per, k+"="+strconv.Itoa(n))
		}
	}
	log.Printf("[anchor-shadow] §ANCHOR-FIRST 影子对照 账本=%s 日=%s 行=%d 线位移动=%d 判线翻转=%d 锚点未回填=%d 取样=%s 分账本=%s（W2 只对照不切硬，切硬待 Q10 签认）",
		book, s.Day, s.Rows, s.Shifted, s.Diverged, s.Unknown, s.RoundAt.Format("15:04:05"),
		strings.Join(per, ","))
}
