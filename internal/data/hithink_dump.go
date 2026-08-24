// hithink_dump.go 全市场数据导出（Market Dumps）：下载链接获取 + Parquet 流式解析。
//
// 三种 dump：daily-k(10年全量) / daily-k-10d(最近10交易日增量) / adjustment-factors(复权事件全量)。
// 下载走 /api/dump/market-dumps/<kind>/download-url 取 S3 预签名链接（5 分钟有效，即取即用）。
// 解析用 parquet-go 流式 Read——10 年全量约数百万行，禁止一次性载入内存。
package data

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/parquet-go/parquet-go"
)

// HithinkDumpKind dump 种类（对应官方 dump_id）。
type HithinkDumpKind string

const (
	HithinkDumpDailyK10y  HithinkDumpKind = "daily-k"            // 10 年全量日K（未复权）
	HithinkDumpDailyK10d  HithinkDumpKind = "daily-k-10d"        // 最近 10 交易日增量
	HithinkDumpAdjFactors HithinkDumpKind = "adjustment-factors" // 复权因子事件全量
)

// GetDumpDownloadURL 获取某 dump 的 S3 预签名下载链接（短时有效，取后立即用）。
// 文档：GET /api/dump/market-dumps/<kind>/download-url
func (c *HithinkClient) GetDumpDownloadURL(kind HithinkDumpKind) (string, error) {
	var out struct {
		PresignedURL string `json:"presigned_url"`
	}
	p := url.Values{}
	if err := c.get(fmt.Sprintf("/api/dump/market-dumps/%s/download-url", url.PathEscape(string(kind))), p, &out); err != nil {
		return "", err
	}
	if out.PresignedURL == "" {
		return "", fmt.Errorf("hithink: %s dump 返回空链接", kind)
	}
	return out.PresignedURL, nil
}

// DownloadDumpFile 下载 dump 到本地路径（覆盖写），返回文件大小。
func (c *HithinkClient) DownloadDumpFile(kind HithinkDumpKind, destPath string) (int64, error) {
	u, err := c.GetDumpDownloadURL(kind)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Get(u)
	if err != nil {
		return 0, fmt.Errorf("hithink: dump 下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("hithink: dump 下载 HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return n, err
	}
	return n, f.Sync()
}

// HithinkDailyKRow 日 K dump 行（date_ms 由调用方换算 yyyyMMdd）。
type HithinkDailyKRow struct {
	ThsCode  string
	Date     string // yyyyMMdd
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
	Turnover float64
}

// thsDumpDailyKRow parquet 物理行结构（列名与官方 schema 一致）。
type thsDumpDailyKRow struct {
	ThsCode    string  `parquet:"thscode"`
	Currency   string  `parquet:"currency"`
	Interval   string  `parquet:"interval"`
	Adjusted   string  `parquet:"adjusted"`
	DateMs     int64   `parquet:"date_ms"`
	OpenPrice  float64 `parquet:"open_price"`
	HighPrice  float64 `parquet:"high_price"`
	LowPrice   float64 `parquet:"low_price"`
	ClosePrice float64 `parquet:"close_price"`
	Volume     float64 `parquet:"volume"`
	Turnover   float64 `parquet:"turnover"`
}

// StreamDailyKParquet 流式解析日 K parquet，逐行回调（内存占用恒定）。
// since 非空时只回调 trade_date >= since 的行（yyyyMMdd 字典序比较）。
func StreamDailyKParquet(path string, since string, cb func(HithinkDailyKRow) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rd := parquet.NewReader(f)
	for {
		var raw thsDumpDailyKRow
		if err := rd.Read(&raw); err != nil {
			if err.Error() == "EOF" || err == io.EOF {
				return nil
			}
			return err
		}
		date := ParseHithintMs(raw.DateMs)
		if since != "" && date < since {
			continue
		}
		if raw.ThsCode == "" || raw.ClosePrice <= 0 {
			continue
		}
		if err := cb(HithinkDailyKRow{
			ThsCode: raw.ThsCode, Date: date,
			Open: raw.OpenPrice, High: raw.HighPrice,
			Low: raw.LowPrice, Close: raw.ClosePrice,
			Volume: raw.Volume, Turnover: raw.Turnover,
		}); err != nil {
			return err
		}
	}
}

var _ = time.Second // 占位保留（限速器在客户端内）

// HithinkAdjEvent 复权因子事件行（全量 dump 解析输出）。
type HithinkAdjEvent struct {
	ThsCode    string
	ExDate     string  // yyyyMMdd
	Dividend   float64 // 每股现金分红（税前）
	BonusRatio float64 // 送股比例（10送N → N/10）
	AllotRatio float64 // 配股比例
	AllotPrice float64 // 配股价
}

// thsDumpAdjRow 复权因子 dump 的 parquet 物理行结构（列名与官方 schema 一致）。
type thsDumpAdjRow struct {
	ThsCode          string  `parquet:"thscode"`
	Ticker           string  `parquet:"ticker"`
	ExDateMs         int64   `parquet:"ex_date_ms"`
	DividendPerShare float64 `parquet:"dividend_per_share"`
	PerShareBonus    float64 `parquet:"per_share_bonus"`
	AllotmentRatio   float64 `parquet:"allotment_ratio"`
	AllotmentPrice   float64 `parquet:"allotment_price"`
	Currency         string  `parquet:"currency"`
}

// StreamAdjFactorsParquet 流式解析复权因子事件 parquet，逐事件回调。
func StreamAdjFactorsParquet(path string, cb func(HithinkAdjEvent) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rd := parquet.NewReader(f)
	for {
		var raw thsDumpAdjRow
		if err := rd.Read(&raw); err != nil {
			if err.Error() == "EOF" || err == io.EOF {
				return nil
			}
			return err
		}
		if raw.ThsCode == "" {
			continue
		}
		if err := cb(HithinkAdjEvent{
			ThsCode: raw.ThsCode, ExDate: ParseHithintMs(raw.ExDateMs),
			Dividend: raw.DividendPerShare, BonusRatio: raw.PerShareBonus,
			AllotRatio: raw.AllotmentRatio, AllotPrice: raw.AllotmentPrice,
		}); err != nil {
			return err
		}
	}
}

// AdjMultiplier 单个除权事件的后复权因子乘数（>1，随分红/送转递增，与 baostock 口径一致）。
//
// 除权除息参考价 ref=(前收盘 − 每股现金分红 + 配股价×配股比) ÷ (1 + 送股比 + 配股比)。
// 原始价在除权日按 ref 跳低开盘；后复权要求数列连续 ⇒ 因子须按 前收盘/ref 同比例跳升：
// multiplier = 前收盘 ÷ ref（首版实现曾写成 ref/前收盘 导致因子反向缩小的实录教训）。
// English: per-event hfq multiplier = prevClose / ex-reference-price (>1 for dividends).
func AdjMultiplier(prevClose, dividend, bonusRatio, allotRatio, allotPrice float64) float64 {
	if prevClose <= 0 {
		return 1
	}
	ref := (prevClose - dividend + allotPrice*allotRatio) / (1 + bonusRatio + allotRatio)
	if ref <= 0 {
		return 1
	}
	return prevClose / ref
}
