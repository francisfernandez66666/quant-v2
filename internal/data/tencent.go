// Package data — 腾讯(ifzq.gtimg.cn) K 线 API 客户端。
// 作为 新浪→同花顺→腾讯→东财 降级链中的 K 线备用源。
// 当新浪 K 线接口被封/东财不可用时，腾讯日K与分钟K仍可正常返回。
// 所有请求经 TencentLimiter 限流（10/s，20突发）。
// Package data — the Tencent (ifzq.gtimg.cn) K-line API client.
// It serves as a backup K-line source in the Sina→THS→Tencent→EastMoney chain,
// still returning day/minute K-lines when other sources fail.
// Requests are rate-limited by TencentLimiter (10/s, burst 20).
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tencentPrefix 返回腾讯 K 线接口所需的板块前缀（sh/sz）。
// tencentPrefix returns the exchange prefix (sh/sz) required by the Tencent K-line API.
func tencentPrefix(code string) string {
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "5") {
		return "sh"
	}
	return "sz"
}

// gbKLine 请求体解构用的内部结构。
// tencentKResp is the internal struct for decoding the Tencent K-line response.
type tencentKResp struct {
	Code int                     `json:"code"`
	Data map[string]tencentKData `json:"data"`
}

// tencentKData 不同周期 K 线数组容器（前复权日线/日线/分钟线）。
// tencentKData holds K-line arrays per granularity (qfq daily, daily, and minute lines).
// 分钟线行内偶有裸 `{}` 占位对象（如成交量字段），无法直接解到 [][]string，
// 故分钟字段使用 tencentRow（容错非字符串元素 → 空串）。
// Minute rows may carry a bare `{}` placeholder (e.g. the volume column), which cannot
// unmarshal into [][]string; the minute fields therefore use tencentRow (tolerant).
type tencentKData struct {
	QfqDay [][]string   `json:"qfqday"`
	Day    [][]string   `json:"day"`
	M1     []tencentRow `json:"m1"`
	M5     []tencentRow `json:"m5"`
	M15    []tencentRow `json:"m15"`
	M30    []tencentRow `json:"m30"`
	M60    []tencentRow `json:"m60"`
}

// tencentRow 是腾讯分钟 K 线的一行；非字符串元素（如 `{}` 占位）容错为空串。
// tencentRow is a single Tencent minute K-line row; non-string elements (e.g. the `{}`
// placeholder) are tolerated and replaced with the empty string.
type tencentRow []string

// UnmarshalJSON 逐元素解包，非字符串元素记为 ""。
// UnmarshalJSON decodes element by element, mapping non-string elements to "".
func (r *tencentRow) UnmarshalJSON(b []byte) error {
	var elems []json.RawMessage
	if err := json.Unmarshal(b, &elems); err != nil {
		return err
	}
	out := make([]string, 0, len(elems))
	for _, el := range elems {
		var s string
		if err := json.Unmarshal(el, &s); err != nil {
			s = ""
		}
		out = append(out, s)
	}
	*r = out
	return nil
}

// getTencentAndParse 发起腾讯 K 线请求并通过给定解析器解析。
// getTencentAndParse issues a Tencent K-line request and parses it with the given parser.
func (m *MarketAPI) getTencentAndParse(url string, parse func(body []byte, status int) ([]KLine, error)) ([]KLine, error) {
	TencentLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("tencent kline request: %v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Referer", "https://gu.qq.com/")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tencent kline http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tencent kline read: %v", err)
	}
	return parse(body, resp.StatusCode)
}

// GetTencentKLine 获取腾讯前复权日 K 线。
// code 为股票代码，count 为根数。返回按日期升序排列的 KLine。
// GetTencentKLine fetches Tencent qfq (forward-adjusted) daily K-lines,
// returned in ascending date order.
func (m *MarketAPI) GetTencentKLine(code string, count int) ([]KLine, error) {
	url := fmt.Sprintf("https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=%s%s,day,,,%d,qfq",
		tencentPrefix(code), code, count)
	return m.getTencentAndParse(url, func(body []byte, status int) ([]KLine, error) {
		if status != http.StatusOK {
			return nil, fmt.Errorf("tencent kline status: %d", status)
		}
		var raw tencentKResp
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("tencent kline json: %v", err)
		}
		stk, ok := raw.Data[tencentPrefix(code)+code]
		if !ok {
			return nil, fmt.Errorf("tencent kline missing symbol")
		}
		rows := stk.QfqDay
		if len(rows) == 0 {
			rows = stk.Day
		}
		return parseTencentKLine(rows, false)
	})
}

// GetTencentMinuteKLine 获取腾讯分钟 K 线。
// scale 为分钟数（1/5/15/30/60），count 为根数。返回按时间升序排列的 KLine。
// GetTencentMinuteKLine fetches Tencent minute K-lines for scale 1/5/15/30/60,
// returned in ascending time order.
func (m *MarketAPI) GetTencentMinuteKLine(code string, scale, count int) ([]KLine, error) {
	url := fmt.Sprintf("https://ifzq.gtimg.cn/appstock/app/kline/mkline?param=%s%s,m%d,,%d",
		tencentPrefix(code), code, scale, count)
	return m.getTencentAndParse(url, func(body []byte, status int) ([]KLine, error) {
		if status != http.StatusOK {
			return nil, fmt.Errorf("tencent minute kline status: %d", status)
		}
		var raw tencentKResp
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("tencent minute kline json: %v", err)
		}
		stk, ok := raw.Data[tencentPrefix(code)+code]
		if !ok {
			return nil, fmt.Errorf("tencent minute kline missing symbol")
		}
		var rows [][]string
		switch scale {
		case 1:
			rows = toRows(stk.M1)
		case 15:
			rows = toRows(stk.M15)
		case 30:
			rows = toRows(stk.M30)
		case 60:
			rows = toRows(stk.M60)
		default:
			rows = toRows(stk.M5)
		}
		return parseTencentKLine(rows, true)
	})
}

// toRows 将容错分钟行转换为 [][]string，供 parseTencentKLine 使用。
// toRows converts tolerant minute rows into [][]string for parseTencentKLine.
func toRows(rows []tencentRow) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = []string(r)
	}
	return out
}

// parseTencentKLine 解析腾讯 K 线行数组。
// 腾讯字段顺序为 [时间, 开, 收, 高, 低, 成交量(手), ...]。
// 日线时间格式 "2006-01-02"；分钟线时间格式 "200601021504"（yyyymmddHHMM）。
// 严格校验每根K线有效且 high>=low，避免脏数据流入评分。
// parseTencentKLine parses Tencent K-line rows. Column order is
// [time, open, close, high, low, volume(...), ...]; every row is validated
// (positive values and high>=low) so bad data never reaches the scoring logic.
func parseTencentKLine(rows [][]string, isMinute bool) ([]KLine, error) {
	klines := make([]KLine, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		var t time.Time
		var err error
		if isMinute {
			if len(r[0]) >= 12 {
				t, err = time.ParseInLocation("200601021504", r[0][:12], time.Local)
			} else {
				t, err = time.ParseInLocation("20060102", r[0][:8], time.Local)
			}
		} else {
			t, err = time.Parse("2006-01-02", r[0])
		}
		if err != nil {
			continue
		}
		open := toFloat64(r[1])
		close := toFloat64(r[2])
		high := toFloat64(r[3])
		low := toFloat64(r[4])
		volume := toFloat64(r[5])
		// 校验数值有效，跳过脏数据行
		if open <= 0 || high <= 0 || low <= 0 || close <= 0 {
			continue
		}
		if high < open || high < close || high < low || low > open || low > close {
			continue
		}
		amount := 0.0
		if len(r) > 7 {
			if v, aerr := strconv.ParseFloat(r[7], 64); aerr == nil {
				amount = v
			}
		}
		klines = append(klines, KLine{
			Date:   t,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: volume,
			Amount: amount,
		})
	}
	if len(klines) == 0 {
		return nil, fmt.Errorf("tencent kline no valid rows")
	}
	// 统一按时间升序排序（腾讯日线可能部分倒序）
	sort.Slice(klines, func(i, j int) bool {
		return klines[i].Date.Before(klines[j].Date)
	})
	return klines, nil
}
