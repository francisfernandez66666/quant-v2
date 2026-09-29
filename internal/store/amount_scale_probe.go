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
package store

import (
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

// AmountScaleProbe 一次抽检的读数与结论。
// English: one probe reading — sample counts by band, the median ratio, and the verdict.
type AmountScaleProbe struct {
	Table       string  `json:"table"`        // 被抽表的表名（当前只 daily 有量纲问题）
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

// ProbeDailyAmountScale 对 daily 表某交易日抽样并判定成交额量纲。
//
// date 为空时取表内最近的 trade_date；maxRows<=0 时取默认 800 行（按 ts_code 升序取样，
// 结果确定可复跑——门禁与夜间腿都要能拿同一把尺子复现，所以这里绝不用 RANDOM()）。
// 均价 = amount / (vol × 100)：daily.vol 落库单位是"手"（baostock 的股数除以 100、
// tushare 原生即手），×100 得股数，与 amount 相除即为当日均价，天然是未复权量纲。
// SQL 层先滤掉 vol<=0 或 amount<=0 的行（停牌/集合竞价空行会造出 0 除与假低价样本）。
//
// English: samples one trading day of `daily` and decides which caliber its `amount` is in.
func (d *DB) ProbeDailyAmountScale(date string, maxRows int) (AmountScaleProbe, error) {
	if maxRows <= 0 {
		maxRows = 800
	}
	p := AmountScaleProbe{Table: "daily", WantRows: maxRows, Verdict: AmountScaleNoData, Reason: "无样本"}

	// 未指定日期时以表内最近交易日为准（而不是今天：休市日/装载滞后都该抽到真实存在的那天）。
	if date == "" {
		var latest string
		if err := d.db.QueryRow(`SELECT MAX(trade_date) FROM daily`).Scan(&latest); err != nil {
			return p, fmt.Errorf("量纲抽检取最近交易日: %w", err)
		}
		date = latest
	}
	p.Date = date
	if date == "" {
		return p, nil
	}

	rows, err := d.db.Query(`SELECT amount/(vol*100.0) FROM daily
		WHERE trade_date=? AND vol>0 AND amount>0
		ORDER BY ts_code LIMIT ?`, date, maxRows)
	if err != nil {
		return p, fmt.Errorf("量纲抽检取样: %w", err)
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

// AmountCaliberFactor 给出流动性质控腿应当使用的换算系数（元口径＝1，千元口径＝1000）。
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
func (d *DB) AmountCaliberFactor(date string) (float64, AmountScaleProbe, error) {
	p, err := d.ProbeDailyAmountScale(date, 0)
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
// （The thousand-CNY → CNY multiplier, kept in store so store need not import data.）
const TushareThousandToCNY = 1000.0
