// 数据新鲜度自检（§D-4 GAP_VERIFY_20260917_PM）：行情库"最新交易日覆盖"断言。
// 背景：免费源改反爬/同步断供时，回测与情绪链会静默吃旧数据（历史同型缺陷：复盘断供）。
// 本文件只做查询判定，告警投递由调用方（quant 进程每日一次钩子）负责。
// English: §D-4 — DB-side freshness assertion. Source outages silently degrade backtest/sentiment
// pipelines to stale data; the quant process runs this once a day and alerts on lag.
package store

import "fmt"

// DataFreshness 行情库新鲜度判定结果。
// （Freshness verdict: whether daily/ths_daily cover the most recent trading day.）
type DataFreshness struct {
	OK         bool   `json:"ok"`           // true=覆盖到位（或无从判定：日历/数据表为空）
	Latest     string `json:"latest"`       // 行情库最新交易日（daily ∪ ths_daily 取大）
	LastOpen   string `json:"last_open"`    // 交易日历中 ≤今日 的最近交易日
	LagDays    int    `json:"lag_days"`     // 滞后交易日数（0=最新即最近交易日）
	TradeCalOK bool   `json:"trade_cal_ok"` // 日历表是否可用（false=无从判定，OK=true 不误报）
}

// String 生成人读告警文案（调用方直接进 opslog/推送）。
func (f DataFreshness) String() string {
	return fmt.Sprintf("行情库最新 %s，最近交易日 %s，滞后 %d 个交易日", f.Latest, f.LastOpen, f.LagDays)
}

// CheckDataFreshness 判定行情库是否覆盖最近交易日：latest = MAX(daily.trade_date, ths_daily.trade_date)，
// lastOpen = trade_cal 中 ≤ today 的最大开市日，lag = (latest, lastOpen] 区间开市日数。
// 口径：日历表空（全新部署/未同步）→ TradeCalOK=false，OK=true 不误报；行情两表全空视同未启动
// 采集（全新部署合法态）→ OK=true 但 Latest 为空，调用方可自行提示。滞后 >maxLag 判不新鲜。
// English: compares the DB's newest trade_date against the newest open calendar day ≤ today;
// empty calendar or empty quotes are treated as "not started" (no false alarm).
func (d *DB) CheckDataFreshness(today string, maxLag int) (DataFreshness, error) {
	f := DataFreshness{OK: true}
	var calRows int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM trade_cal`).Scan(&calRows); err != nil {
		return f, err
	}
	if calRows == 0 {
		return f, nil // 无日历：无从判定（避免全新部署误报）
	}
	f.TradeCalOK = true
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(cal_date),'') FROM trade_cal WHERE is_open=1 AND cal_date<=?`, today).Scan(&f.LastOpen); err != nil {
		return f, err
	}
	if f.LastOpen == "" {
		return f, nil // 日历不含今日以前的开市日（异常数据）——不误报
	}
	// 行情最新交易日：daily ∪ ths_daily 取大（YYYYMMDD 字符串同格式可比）
	var dailyMax, thsMax string
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(trade_date),'') FROM daily`).Scan(&dailyMax); err != nil {
		return f, err
	}
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(trade_date),'') FROM ths_daily`).Scan(&thsMax); err != nil {
		return f, err
	}
	f.Latest = dailyMax
	if thsMax > f.Latest {
		f.Latest = thsMax
	}
	if f.Latest == "" {
		return f, nil // 行情两表全空：未启动采集（全新部署合法态）
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM trade_cal WHERE is_open=1 AND cal_date>? AND cal_date<=?`, f.Latest, f.LastOpen).Scan(&f.LagDays); err != nil {
		return f, err
	}
	f.OK = f.LagDays <= maxLag
	return f, nil
}
