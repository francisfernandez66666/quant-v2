// amount_scale_probe.go — §0929SCALE-⑩ 第二条腿：把"靠注释保证量纲"换成"靠机器保证量纲"。
//
// 背景（AUDIT_20260929 P2-4）：daily.amount 有两条装载腿，baostock 落"元"、tushare 原样落"千元"，
// 差 1000 倍。写侧归一（internal/data/amountscale.go）堵住了新数据，但**存量混源**与
// "以后有人再加一条装载腿"这两件事只能靠读数发现，于是把 btreplay/cost.go fixAmountScale 的
// 判据（当日均价 = amount/(vol×100)，A 股合理带 [1,500] 元）从"回测逐票自校"抽成
// **落库后抽检**：给定交易日按 ts_code 取样若干行，统计均价分布并给结论。
//
// 判据与消费方（三条，都在本文件钉住）：
//   - cmd/dataload 装载收尾自动跑一次（只 log P1，不把已成功的装载改判失败）；
//   - `dataload amount-check` 子命令：判红即非零退出，供夜间/verify 腿拨（见 §0929OPS）；
//   - internal/store/quality.go 的流动性质控腿按此处结论取换算系数（详见 AmountCaliberFactor）。
//
// English: §0929SCALE-⑩ — post-load sampling probe that decides whether a trading day's
// `daily.amount` is in CNY or thousand-CNY, replacing the old comment-only guarantee.
//
// §W7-D（2026-10-09 波 7）：这把尺子从"只量 daily"抬成"按 AmountProbedTables 量多表"，
// 首个新消费者是 ths_daily（同花顺 dump 主源）——它的 amount 口径此前只由互相矛盾的注释担保，
// 现改由同一组带宽与同一个取样规则读数。抽检**不等于**换算：换算白名单仍是
// internal/data.AmountScaledTables（见 AmountProbedTables 注释里那两条集合为什么要分开）。
package store

import (
	"database/sql"
	"fmt"
	"sort"
)

// 抽检结论取值（字符串入表，日志与门禁都能逐字比对；新增取值必须同步门禁 §106 的枚举锁）。
const (
	// AmountScaleOK 均价中位数落在合理带 [1,500] 元 ⇒ 元口径，下游阈值可直接比。
	AmountScaleOK = "ok"
	// AmountScaleThousand 中位数 <1 元 ⇒ 该日整体是千元口径（tushare 未归一的形态），判红。
	AmountScaleThousand = "thousand-yuan"
	// AmountScaleOver 中位数 >500 元 ⇒ 疑似被乘了两次 1000（重复归一），判红。
	AmountScaleOver = "over-scaled"
	// AmountScaleMixed 中位数正常但 ≥5% 的样本呈千元形态 ⇒ 同表混源，标量系数救不回来，判红。
	AmountScaleMixed = "mixed"
	// AmountScaleNoData 取样为空（该日无行情/日期写错/表还没装载），不参与判定。
	AmountScaleNoData = "no-data"
)

// 均价合理带：下界 1 元（连仙股都不至于低于它），上界 500 元（贵州茅台级别的最高日均价也在带内）。
// 与 internal/btreplay/cost.go fixAmountScale 保持同一把尺子，两处若要调整必须一起改。
const (
	avgPriceLowerBoundCNY = 1.0
	avgPriceUpperBoundCNY = 500.0
	// mixedLowSharePercent 千元形态样本占比达到该百分比即判混源（5% 远大于仙股真实占比）。
	mixedLowSharePercent = 5
)

// AmountProbedTables 声明"可以被同一把尺子抽检量纲"的表，值＝该表 vol 列换到**股**的倍率。
//
// §W7-D（2026-10-09 波 7）加入 ths_daily 的理由与边界：
//   - ths_daily 是 THS 日 K dump 落库表（cmd/dataload/hithink_sync.go 把 parquet 的 turnover 列
//     直写 amount、volume/100 写 vol），它的 amount 口径此前**只有注释在担保**，而且两处注释互相
//     矛盾（internal/data/hithink_dump.go 一处写"换手率（%）"、一处写"成交额/换手率"）——
//     正是 §0929SCALE-⑩ 要消灭的"靠注释保证量纲"形态；
//   - 但它**不能**进 data.AmountScaledTables 那份归一白名单：那张表的意思是"上游口径已核实为千元、
//     写侧固定 ×1000"。THS dump 的 turnover 没有这份核实记录（本仓既无该列的抽样实录、也无双源对账），
//     顺手"统一乘一次"就是把一个猜测写进 34 亿行的库里。所以这里的处置是**先测后改**：
//     用与 daily 完全同一个判据（均价 = amount/(vol×倍率) 落在 [1,500] 元带）抽读数，
//     读数说千元再谈换算；
//   - 一张表可以"可抽检"而"不可自动换算"，把两个集合分成两份变量正是为了挡住"加了抽检就顺手换算"。
//
// 倍率来源（都是本仓自己的落库口径，不是上游文档）：daily.vol 落"手"（baostock 股数÷100、
// tushare 原生即手）；ths_daily.vol 落"手"（hithink_sync 里 row.Volume/100，
// 2026-08-24 双源对账实录：平安银行 106,085,094 股 vs 1,060,851 手）。
//
// 表名会被拼进 SQL，因此**只允许本 map 的键**（调用侧传任意串一律报错，不做字符串清洗）。
// （Tables the shared caliber probe may sample, with each table's vol→shares multiplier;
// keys double as the SQL identifier whitelist.)
var AmountProbedTables = map[string]float64{
	"daily":     100.0,
	"ths_daily": 100.0,
}

// AmountScaleProbe 一次抽检的读数与结论。
// English: one probe reading — sample counts by band, the median ratio, and the verdict.
type AmountScaleProbe struct {
	Table       string  `json:"table"`        // 被抽表的表名（daily / ths_daily；见 AmountProbedTables）
	Date        string  `json:"date"`         // 实际抽检的交易日（YYYYMMDD）
	WantRows    int     `json:"want_rows"`    // 请求取样上限
	Rows        int     `json:"rows"`         // 真正参与判定的行数（vol/amount 均 >0）
	Low         int     `json:"low"`          // 均价 <1 元的行数（千元形态）
	Normal      int     `json:"normal"`       // 均价在 [1,500] 元的行数
	High        int     `json:"high"`         // 均价 >500 元的行数（疑似双重换算）
	MedianRatio float64 `json:"median_ratio"` // 均价中位数（元）
	Verdict     string  `json:"verdict"`      // 结论，取值见上方五个常量
	Reason      string  `json:"reason"`       // 判红时的人类可读理由（日志直出）
}

// Red 报告本读数是否应当判红（no-data 不算红：它由新鲜度探针负责，这里不重复报警）。
// English: whether this reading should be treated as a caliber failure.
func (p AmountScaleProbe) Red() bool {
	return p.Verdict == AmountScaleThousand || p.Verdict == AmountScaleOver || p.Verdict == AmountScaleMixed
}

// ProbeDailyAmountScale 对 daily 表某交易日抽样并判定成交额量纲（＝ProbeAmountScale("daily", …)）。
// 保留这个名字与签名是因为它已有三个消费者（装载收尾自检、amount-check 独立腿、流动性质控系数）；
// 新增表请走 ProbeAmountScale，别再复制一份判定体（两份判定的结局是修一处漏一处）。
// English: the daily-table convenience wrapper around the shared probe.
func (d *DB) ProbeDailyAmountScale(date string, maxRows int) (AmountScaleProbe, error) {
	return d.ProbeAmountScale("daily", date, maxRows)
}

// ProbeAmountScale 按 AmountProbedTables 的倍率对指定表抽样并判定成交额量纲——
// daily 与 ths_daily 用的是**同一把尺子**（同一组带宽、同一套五态结论、同一取样规则），
// 这是 §W7-D 的目的：ths_daily 的量纲不再由注释担保，而是由与 daily 完全相同的读数判定。
//
// date 为空时取表内最近的 trade_date；maxRows<=0 时取默认 800 行（按 ts_code 升序取样，
// 结果确定可复跑——门禁与夜间腿都要能拿同一把尺子复现，所以这里绝不用 RANDOM()）。
// 均价 = amount / (vol × 倍率)：两张表的 vol 都落"手"，倍率 100 得股数，与 amount 相除
// 即为当日均价，天然是未复权量纲。
// SQL 层先滤掉 vol<=0 或 amount<=0 的行（停牌/集合竞价空行会造出 0 除与假低价样本）。
// 表名不在 AmountProbedTables 内一律回错误（它要拼进 SQL，是标识符白名单而不是清洗问题）。
//
// English: samples one trading day of a whitelisted bar table and decides which caliber its
// `amount` is in, with the very same ruler daily uses; a non-whitelisted table is an error
// because the name is interpolated into the SQL identifier position.
func (d *DB) ProbeAmountScale(table, date string, maxRows int) (AmountScaleProbe, error) {
	sharesPerUnit, ok := AmountProbedTables[table]
	if !ok {
		return AmountScaleProbe{Table: table, Verdict: AmountScaleNoData},
			fmt.Errorf("量纲抽检表名不在白名单（internal/store.AmountProbedTables）: %q", table)
	}
	if maxRows <= 0 {
		maxRows = 800
	}
	p := AmountScaleProbe{Table: table, WantRows: maxRows, Verdict: AmountScaleNoData, Reason: "无样本"}

	// 未指定日期时以表内最近交易日为准（而不是今天：休市日/装载滞后都该抽到真实存在的那天）。
	// §W7-D 顺手改掉的一处自伤：这里原先直接 Scan 进 string，空表时 MAX() 回 NULL ⇒
	// "converting NULL to string is unsupported" 报错，把**表还没装**这件合法的事
	// 冒充成"抽检跑挂了"（amount-check 腿退 2 ⇒ 第 30 探针判红）。
	// 现按 no-data 走：五种结论里本来就有"取样为空不参与判定"这一态，别让它以错误形态出现。
	if date == "" {
		var latest sql.NullString
		if err := d.db.QueryRow(`SELECT MAX(trade_date) FROM ` + table).Scan(&latest); err != nil {
			return p, fmt.Errorf("量纲抽检取最近交易日(%s): %w", table, err)
		}
		if !latest.Valid {
			return p, nil // 表空或该列全 NULL ⇒ no-data（新鲜度由别的腿负责报警）
		}
		date = latest.String
	}
	p.Date = date
	if date == "" {
		return p, nil
	}

	// 倍率走绑定参数而不是拼接：它来自 AmountProbedTables 的值（float64 100.0），
	// 拼进 SQL 需要格式化浮点字面量，多一处可写错的表面；参数位天然按 REAL 处理。
	rows, err := d.db.Query(`SELECT amount/(vol*?) FROM `+table+`
		WHERE trade_date=? AND vol>0 AND amount>0
		ORDER BY ts_code LIMIT ?`, sharesPerUnit, date, maxRows)
	if err != nil {
		return p, fmt.Errorf("量纲抽检取样(%s): %w", table, err)
	}
	defer rows.Close()

	var ratios []float64
	for rows.Next() {
		var r float64
		if err := rows.Scan(&r); err != nil {
			return p, fmt.Errorf("量纲抽检读数: %w", err)
		}
		ratios = append(ratios, r)
	}
	if err := rows.Err(); err != nil {
		return p, fmt.Errorf("量纲抽检游标: %w", err)
	}
	p.Rows = len(ratios)
	if p.Rows == 0 {
		return p, nil
	}

	// 分带计数 + 中位数：中位而不是均值，是因为个别仙股/退市整理股会把均值拉偏。
	for _, r := range ratios {
		switch {
		case r < avgPriceLowerBoundCNY:
			p.Low++
		case r > avgPriceUpperBoundCNY:
			p.High++
		default:
			p.Normal++
		}
	}
	sort.Float64s(ratios)
	p.MedianRatio = ratios[len(ratios)/2]

	switch {
	case p.MedianRatio < avgPriceLowerBoundCNY:
		p.Verdict = AmountScaleThousand
		p.Reason = fmt.Sprintf("%s 中位日均价 %.4f 元 <1 元 ⇒ amount 为千元口径（未归一）", p.Date, p.MedianRatio)
	case p.MedianRatio > avgPriceUpperBoundCNY:
		p.Verdict = AmountScaleOver
		p.Reason = fmt.Sprintf("%s 中位日均价 %.1f 元 >500 元 ⇒ amount 疑似被重复换算（×1000 两次）", p.Date, p.MedianRatio)
	case p.Low*100 >= p.Rows*mixedLowSharePercent:
		// 占比判据写成乘法，避开整数除法在样本 <100 时把 5% 抹成 0 的取整陷阱。
		p.Verdict = AmountScaleMixed
		p.Reason = fmt.Sprintf("%s 有 %d/%d 行呈千元形态（≥%d%%）⇒ 同表混源，标量换算系数救不回来",
			p.Date, p.Low, p.Rows, mixedLowSharePercent)
	default:
		p.Verdict = AmountScaleOK
		p.Reason = ""
	}
	return p, nil
}

// AmountCaliberFactor 给出流动性质控腿（筛 daily 表）应当使用的换算系数——
// ＝AmountCaliberFactorFor("daily", date)。名字保留是给既有调用侧与测试的；新表请显式传表名。
// English: the daily-table convenience wrapper for the caliber factor.
func (d *DB) AmountCaliberFactor(date string) (float64, AmountScaleProbe, error) {
	return d.AmountCaliberFactorFor("daily", date)
}

// AmountCaliberFactorFor 给出指定表的换算系数（元口径＝1，千元口径＝1000）。
//
// 存在理由：quality.go 的 MinAvgAmount 阈值按"元"写死（默认 3e7＝3000 万），
// 而它比较的是 `AVG(amount)`。库里若混进千元口径，这一条阈值会**把整池股票全部剔掉**
// （日均额差 1000 倍），且不会有任何报错。这里把判定收敛到与抽检同一把尺子：
//   - 结论 thousand-yuan ⇒ 返回 1000（比较前把读取值抬到元口径）；
//   - 结论 ok ⇒ 返回 1；
//   - 结论 mixed / over-scaled ⇒ 返回 1 并交调用侧告警：这两种形态没有安全的标量系数，
//     宁可让阈值按现状判（可能整批剔票、日志点名），也不要悄悄乘一个错的数。
//
// 返回 (系数, 抽检读数, 错误)。probe 本身带 Verdict/Reason，调用侧据此决定日志级别。
// English: the multiplier the liquidity screen should apply, decided by the same probe.
func (d *DB) AmountCaliberFactorFor(table, date string) (float64, AmountScaleProbe, error) {
	p, err := d.ProbeAmountScale(table, date, 0)
	if err != nil {
		return 1, p, err
	}
	if p.Verdict == AmountScaleThousand {
		return TushareThousandToCNY, p, nil
	}
	return 1, p, nil
}

// TushareThousandToCNY 千元→元的系数常量（store 侧另立一份，避免 store 反向依赖 data 包）。
// 数值与 internal/data.TushareAmountScale 必须一致，门禁 §106 有等值锁。
// §W7-D 提醒：**不要**因为某个表进了 AmountProbedTables 就把它乘上这个系数——
// 该常量只对"上游口径已核实为千元"的 tushare 日线家族成立（见 AmountProbedTables 注释）。
// （The thousand-CNY → CNY multiplier, kept in store so store need not import data.）
const TushareThousandToCNY = 1000.0
