// daily_source.go — §P2-F（2026-10-06 修复批 波 5）日线来源列的**读侧**统一判据。
//
// 为什么判据要单源：降级行（新浪/东财兜底腿写的行）在库里唯一的痕迹就是 daily.source
// 这一列。如果每个统计点各自写一遍 `source LIKE '%degraded%'`，就会复制出 09-25 那批
// 「同一个判据在两个读数点各写一遍公式」的老问题（§0927AUDIT-D1 的教训：日内亏损与看板
// 各算各的，最后只修一份）。所以本文件只提供两个东西：
//  1. DailySourceDegraded 常量族：写侧/读侧共用的字面量（LIKE 模式、来源名）；
//  2. 判据 SQL 片段构造器，调用点拼进自己的 WHERE（别名固定 dv/d，两处都用同一片段）。
//
// 与写侧的契约（改任何一侧必须同时改另一侧，否则判据恒假＝"降级行筛不掉"）：
//   - cmd/pydata/server.py 的 _ak_sina_daily/_ak_em_daily 行尾输出 sina_degraded /
//     eastmoney_degraded；主链路输出 baostock；
//   - cmd/dataload/baostock.go 的 dailySourceOf 原样落该值（缺列时按 baostock）；
//   - NULL＝本列落地之前的老行，**不**判为降级（宁可继续参与统计，也不把历史数据凭空删掉；
//     世代未知的真实成因是"列还没落地"，不是"数据坏了"）。
//
// English: single source of truth for the daily.source column semantics — the degraded-row
// predicate every breadth statistic must share, so a fix can't land in one reader and miss another.
package store

// DailySourceLikeDegraded 是"降级行"的 LIKE 模式（写侧的两个标记都含 degraded 后缀，
// 主链路标记不含）。刻意用后缀匹配而不是枚举两个来源名：新增第三条兜底腿时
// 只要沿用 *_degraded 命名，读侧不需要改动（枚举式判据会对新腿恒假＝静默放行降级数据）。
const DailySourceLikeDegraded = "%degraded%"

// DailySourceBaostock / DailySourceTushare 主链路标记（写侧同源常量，读侧只用作对照值）。
const (
	DailySourceBaostock = "baostock"
	DailySourceTushare  = "tushare"
)

// DailySourceNotDegraded 返回可直接拼进 WHERE 的片段（**不含**前置 AND/括号外的连接词），
// 含义："这一行不是降级腿写的，或它来自本列落地前的世代"。
// alias 为 daily 表在该查询里的别名（空串表示不带别名）。
// English: returns the SQL fragment excluding degraded daily rows (NULL source = pre-column era,
// still counted — the honest reading of "unknown generation" is not "bad data").
func DailySourceNotDegraded(alias string) string {
	col := dailySourceQualified(alias)
	return "(" + col + " IS NULL OR " + col + " NOT LIKE '" + DailySourceLikeDegraded + "')"
}

// DailySourceDegradedOnly 与上面互斥的那一支："只有降级行"。
// 供"标注置信度/数降级行数"的统计点用（判据同样是这一列，两处不得各写一遍字面量）。
func DailySourceDegradedOnly(alias string) string {
	col := dailySourceQualified(alias)
	return "(" + col + " LIKE '" + DailySourceLikeDegraded + "')"
}

// dailySourceQualified 按别名限定列名；无别名时给裸列名。
func dailySourceQualified(alias string) string {
	if alias == "" {
		return "source"
	}
	return alias + ".source"
}
