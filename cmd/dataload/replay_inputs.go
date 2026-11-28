// replay_inputs.go — §REPLAY-INPUTS 2026-11-28：dataload 的两个数据面修复子命令实现。
//
// 这两个子命令都是"补洞"，不是"重建"：
//
//	index        只补 index_daily 的尾段缺口（现网实测 000300/000001 止于 20261118、
//	             000852/000905 止于 20261106；不补的后果是引用 IndexChange 的 fac_177
//	             在**最近 9 个交易日恒 decline**，而最近这段恰恰是判断新因子有没有用的样本窗）。
//	sector-map   把当日 sector_map.json 存档进 sector_map_daily（板块归属从"只有一份快照"
//	             变成可回溯序列；拿单日快照回填历史是前视偏差，方案 §2.2 第 6 条）。
//
// 共同纪律：**跑完必须自己证明洞补上了**——两个函数都在写入后复读一次，
// 末行没到目标日就返回非零错误。"命令跑成功"与"数据到位"在本仓从来不是一回事
// （AGENTS 里"跑完了但任务状态冻结在 12:13""备份脚本 KEEP_DAYS 排在成功之后"都是同族）。
//
// English: two targeted repair subcommands — index tail backfill and sector-map archiving;
// both re-read the table after writing and fail loudly when the tail did not reach the target day.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// indexGapCodes 需要补尾段的指数集合。含 000001.SH：
// 它不在 indexCodes（超额基准用）里，但 `IndexChange` 的读侧与大盘腿都会碰到它，
// 库里同样停在 20261118，一起补掉省得下次又发现半截。
var indexGapCodes = []string{"000001.SH", "000300.SH", "000905.SH", "000852.SH"}

// runIndexGap 补 index_daily 尾段，并在写前/写后各读一次覆盖率自证。
//
// from 的取法（优先级从高到低）：
//  1. `--index-start` 显式指定（重刷全历史时用）；
//  2. 库里各指数**最小的**末行日（= 最落后那只的位置，一次把四只都补齐）；
//  3. 都没读到就用 `--start`（表刚建的形态）。
func runIndexGap(db *store.DB, bs *data.BaostockClient, provider, token, indexStart, start, end string) error {
	before, err := db.IndexDailyCoverage()
	if err != nil {
		return fmt.Errorf("读 index_daily 覆盖率失败: %w", err)
	}
	from := indexStart
	if from == "" {
		from = minCoverageDate(before)
	}
	if from == "" {
		from = start
	}
	log.Printf("[dataload] §IDX-GAP provider=%s 区间 %s..%s 写前=%s", provider, from, end, formatCoverage(before))

	if provider == "tushare" {
		if err := loadIndex(db, token, from, end); err != nil {
			return fmt.Errorf("tushare 指数日线: %w", err)
		}
	} else {
		if err := bsLoadIndex(db, bs, from, end); err != nil {
			return fmt.Errorf("baostock 指数日线: %w", err)
		}
	}

	after, err := db.IndexDailyCoverage()
	if err != nil {
		return fmt.Errorf("写后复读 index_daily 失败: %w", err)
	}
	// 目标日是"区间末的最后一个交易日"而不是日历日：周六跑不该因为周末没行情而报 PARTIAL
	// （拿自然日判新鲜，等于把休息日永远记成缺数）。
	target := data.TradingDayDate(mustParseCompactDate(end))
	gaps := make([]string, 0, len(indexGapCodes))
	for _, code := range indexGapCodes {
		last, ok := after[code]
		if !ok || asString(last[1]) < target {
			gaps = append(gaps, fmt.Sprintf("%s=%s", code, asString(last[1])))
		}
	}
	log.Printf("[dataload] §IDX-GAP 写后=%s target=%s", formatCoverage(after), target)
	if len(gaps) > 0 {
		return fmt.Errorf("尾段未补到目标交易日 target=%s 仍缺: %s", target, joinGaps(gaps))
	}
	fmt.Printf("IDX_GAP_RESULT=OK from=%s target=%s before=%d after=%d\n", from, target,
		totalRows(before), totalRows(after))
	return nil
}

// minCoverageDate 各指数末行日里最早的一只（就是最该补的位置）；一个都没有则返回空串。
func minCoverageDate(cov map[string][2]any) string {
	var min string
	for _, code := range indexGapCodes {
		v, ok := cov[code]
		if !ok {
			continue
		}
		d := asString(v[1])
		if d == "" {
			continue
		}
		if min == "" || d < min {
			min = d
		}
	}
	return min
}

// sectorMapDoc 生产者写出的文件形状（与 internal/data/sector_map.go 的读侧同三键）。
// 只解析需要的三个键：上游多写的 boards_ok/industry_boards 等运维读数不该把装载层绑住。
type sectorMapDoc struct {
	Day       string            `json:"day"`
	Generated int64             `json:"generated_at"`
	SectorMap map[string]string `json:"sector_map"`
}

// sectorMapMinRows 与生产者同一条覆盖率地板（<3000 只就不算一份可用的表）。
// 复用同一个数字而不是在这里再拍一个：两份地板必然漂，漂的表现是"生产者放行、装载层拒绝"
// 这种谁都以为对方会拦的半态。
const sectorMapMinRows = 3000

// runSectorMapArchive 把当日 sector_map.json 存档进 sector_map_daily。
//
// 代码形状：json 里是 6 位纯码（载体口径），库里四张新表统一带后缀 ts_code（与 daily 同形）
// ⇒ 后缀在**写入边界**经 data.ExchangeSuffix 归一一次（全系统唯一分类表，920/4/8 → .BJ 的
// 顺序也在那里）。绝不自己再写一份"首位数字判市场"——那是 §P1-6 那批误判 .SH 的成因。
func runSectorMapArchive(db *store.DB, mapPath string) error {
	path, err := resolveSectorMapPath(mapPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取板块归属文件失败 %s: %w", path, err)
	}
	var doc sectorMapDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("板块归属文件不是合法 JSON %s: %w", path, err)
	}
	day := cntime.CompactDay(doc.Day)
	if day == "" {
		return fmt.Errorf("板块归属文件的 day 形状不可认 %q（生产者是紧凑 YYYYMMDD）", doc.Day)
	}
	if len(doc.SectorMap) < sectorMapMinRows {
		// 半张表比没表更坏：命中率看着正常，delta 却贴在一半人身上。
		return fmt.Errorf("板块归属只 %d 只 < 地板 %d（拒绝落库，宁可不覆盖旧日）",
			len(doc.SectorMap), sectorMapMinRows)
	}
	entries := make(map[string]string, len(doc.SectorMap))
	for code, sector := range doc.SectorMap {
		entries[data.ExchangeSuffix(code)] = sector
	}
	n, err := db.UpsertSectorMapDay(day, entries, nil)
	if err != nil {
		return fmt.Errorf("存档板块归属失败: %w", err)
	}
	// 写后复读：行数与映射数不等说明有行被静默丢掉（空板块名之外的情况都算缺陷）。
	if int(n) < len(entries) {
		return fmt.Errorf("写入行数 %d < 映射数 %d（有归属被丢弃，查空板块名/主键）", n, len(entries))
	}
	fmt.Printf("SECTOR_MAP_ARCHIVE=OK path=%s day=%s rows=%d\n", path, day, n)
	return nil
}

// resolveSectorMapPath 缺省按"数据目录那份 → 构建目录那份"顺序找；
// 两者都没有就明确报错并列出试过的路径（只说"没找到"会让人去猜是不是路径写错）。
func resolveSectorMapPath(mapPath string) (string, error) {
	if mapPath != "" {
		return mapPath, nil
	}
	dataDir := os.Getenv("QUANT_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/quant-trading-v2"
	}
	candidates := []string{
		filepath.Join(dataDir, "sector_map.json"),
		filepath.Join(buildDir(), "qmt_gateway", "local_executor_sector_map.json"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到板块归属文件，试过: %s", joinGaps(candidates))
}

// buildDir 构建根（与热更链同口径：CARRIER_BUILD_DIR，缺省退回可执行文件所在目录的上级）。
func buildDir() string {
	if v := os.Getenv("CARRIER_BUILD_DIR"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return "/opt/quant"
	}
	return filepath.Dir(filepath.Dir(exe))
}

// mustParseCompactDate 把紧凑日期解析成北京时区时刻；解析不动就退回"现在"。
// 这里刻意不返回错误：end 只用来算目标交易日，形状错了由 TradingDayDate 兜住，
// 为这个把整条补数链路打断是本末倒置（真正的形状校验在 from/target 的比较上）。
func mustParseCompactDate(s string) time.Time {
	if v, err := time.ParseInLocation("20060102", cntime.CompactDay(s), cntime.Loc); err == nil {
		return v
	}
	return cntime.Now()
}

// ── 龙头代理分位预计算（§REPLAY-INPUTS 档 2）──

// leaderRankMinSectorPeers 板块内至少几只票才算得出"分位"。
// 1 只时 rank/(n-1) 会除零；2 只时只有 0/100 两个极端值——按"板块太瘦，当日不写"处理，
// 因为给一只票写 0（"同板块最弱"）是编出来的方向，宁可让它 NaN 弃权。
const leaderRankMinSectorPeers = 3

// runLeaderRank 逐日算"同板块涨幅分位"并落 leader_rank_daily。
//
// 为什么必须预计算成表而不是在逐股 Assemble 里算：Assemble 是一只票一次，
// 拿不到"当日同板块全体"这个截面（方案 §2.2 第 4 条）。
//
// 分位定义：rank/(n-1)×1.0，rank 为"板块内涨幅严格小于自己的只数"（同价并列取同名次，
// 不做随机 tie-break——回放必须可复现）。
func runLeaderRank(db *store.DB, start, end string) error {
	s, e := cntime.CompactDay(start), cntime.CompactDay(end)
	if s == "" || e == "" {
		return fmt.Errorf("日尺不可认 start=%q end=%q", start, end)
	}
	days, err := db.SectorMapDaysBetween(s, e)
	if err != nil {
		return fmt.Errorf("读板块归属日期清单失败: %w", err)
	}
	if len(days) == 0 {
		return fmt.Errorf("库里没有 sector_map_daily（先跑 sector-map 存档；无归属表就没有板块截面可算）")
	}
	written, skippedDays := 0, 0
	for _, day := range days {
		member, err := db.SectorMapOnDay(day)
		if err != nil || len(member) == 0 {
			skippedDays++
			log.Printf("[dataload] leader-rank %s 归属为空/读取失败，整日跳过: %v", day, err)
			continue
		}
		cross, err := db.DailyPctChgOn(day)
		if err != nil || len(cross) == 0 {
			skippedDays++
			log.Printf("[dataload] leader-rank %s 当日无行情，整日跳过: %v", day, err)
			continue
		}
		// 按板块分桶（同一天、同一板块的全体可比票）
		boards := make(map[string][]string)
		for code, pct := range cross {
			sector := member[code]
			if sector == "" || pct[1] == 1 {
				continue // 没归属 / 降级行（涨跌幅不可信）都不进桶，也不能给自己算分位
			}
			boards[sector] = append(boards[sector], code)
		}
		batch := make([]store.LeaderRankRow, 0, len(cross))
		for sector, codes := range boards {
			if len(codes) < leaderRankMinSectorPeers {
				continue // 板块太瘦算不出有意义的位次 ⇒ 整板块不写（票在该日按 NaN 弃权）
			}
			vals := make([]float64, 0, len(codes))
			for _, c := range codes {
				vals = append(vals, cross[c][0])
			}
			sorted := append([]float64(nil), vals...)
			sort.Float64s(sorted)
			for i, c := range codes {
				v := vals[i]
				// rank = 严格小于 v 的只数（并列同分，可复现；不做 index-of-first 的随机性）
				rank := sort.SearchFloat64s(sorted, v)
				last := rank
				for last < len(sorted) && sorted[last] == v {
					last++
				}
				// 并列时取区间中点，避免"同涨幅两票一个 0 一个 1"这种顺序依赖
				pos := float64(rank) + float64(last-rank-1)/2.0
				batch = append(batch, store.LeaderRankRow{
					TsCode: c, TradeDate: day, Sector: sector,
					PctChg: v, HasPctChg: true,
					GainRank: pos / float64(len(codes)-1), HasRank: true,
				})
			}
		}
		if len(batch) == 0 {
			skippedDays++
			log.Printf("[dataload] leader-rank %s 无满足最少票数的板块，整日跳过", day)
			continue
		}
		n, err := db.UpsertLeaderRankDay(batch)
		if err != nil {
			return fmt.Errorf("写 leader_rank_daily %s: %w", day, err)
		}
		written += int(n)
		log.Printf("[dataload] leader-rank %s 板块=%d 写入=%d", day, len(boards), n)
	}
	if written == 0 {
		return fmt.Errorf("整个区间一条龙头分位都没写出（跳过 %d 日）——归属表与行情对不上，先查 sector_map_daily", skippedDays)
	}
	fmt.Printf("LEADER_RANK_RESULT=OK days=%d written=%d skipped_days=%d min_peers=%d\n",
		len(days), written, skippedDays, leaderRankMinSectorPeers)
	return nil
}

// ── 小工具：覆盖率读数格式化（只为把"写前/写后"打进日志，别把 map 直接 %v 出来）──

func formatCoverage(cov map[string][2]any) string {
	keys := make([]string, 0, len(cov))
	for k := range cov {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		v := cov[k]
		out += fmt.Sprintf("%s{%s,%v} ", k, asString(v[1]), v[0])
	}
	if out == "" {
		return "<空>"
	}
	return out
}

func totalRows(cov map[string][2]any) int {
	n := 0
	for _, v := range cov {
		if c, ok := v[0].(int); ok {
			n += c
		}
	}
	return n
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func joinGaps(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
