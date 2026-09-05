// baostock 历史数据客户端（研究链路 B0 主数据源）。
// 通过本地 Python sidecar（cmd/pydata/server.py，baostock 主 + akshare 降级）以 HTTP 获取，
// 仅服务离线研究（dataload/因子/回测），不进入盘中实时关键路径。
// sidecar 返回 CSV；本文件负责请求、CSV 解析、数值化、代码/日期归一。
// （BaostockClient talks to the local Python sidecar (baostock primary + akshare fallback) over
// HTTP for the offline research chain only. The sidecar returns CSV; this file handles requests,
// CSV parsing, numeric conversion and code/date normalization.）
package data

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BaostockClient 本地 sidecar 的 Go 客户端。
// （BaostockClient is the Go client for the local baostock sidecar.）
type BaostockClient struct {
	base   string // 形如 http://127.0.0.1:8787
	client *http.Client
}

// NewBaostockClient 创建 sidecar 客户端。
// （NewBaostockClient builds the sidecar client.）
func NewBaostockClient(base string) *BaostockClient {
	if base == "" {
		base = "http://127.0.0.1:8787"
	}
	return &BaostockClient{
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// call 请求 sidecar 的 GET 端点并解析 CSV 为行映射。
// strCols 中列保留字符串（代码/日期/名称），其余尽力转 float64（空值/解析失败为 nil）。
// 业务错误以 "error: " 前缀返回（与 Tushare 风格一致）。
// （call GETs a sidecar endpoint and parses the CSV into row maps. Columns listed in strCols stay
// strings (codes/dates/names); other columns are converted to float64 when possible (empty → nil).）
func (c *BaostockClient) call(method string, params map[string]string, strCols map[string]bool) ([]TushareRow, error) {
	u, err := url.Parse(c.base + "/" + method)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	resp, err := c.client.Get(u.String())
	if err != nil {
		return nil, fmt.Errorf("baostock %s: %v", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("baostock %s read: %v", method, err)
	}
	text := string(body)
	// baostock 以文本行返回结果：error: 前缀表示调用失败。
	if strings.HasPrefix(text, "error:") {
		return nil, fmt.Errorf("baostock %s: %s", method, strings.TrimSpace(text))
	}
	// 其余为 CSV 文本（首行表头）。
	r := csv.NewReader(strings.NewReader(text))
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("baostock %s csv: %v", method, err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	headers := records[0]
	out := make([]TushareRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(TushareRow, len(headers))
		for i, h := range headers {
			key := strings.ToLower(h)
			if i >= len(rec) {
				continue
			}
			cell := strings.TrimSpace(rec[i])
			if cell == "" {
				continue // 空值 → nil
			}
			if !strCols[key] {
				if f, err := strconv.ParseFloat(cell, 64); err == nil {
					row[key] = f
					continue
				}
			}
			row[key] = cell
		}
		out = append(out, row)
	}
	return out, nil
}

// strColsDay 日期列保留字符串。
func strColsDay() map[string]bool { return map[string]bool{"calendar_date": true} }

// strColsCodeDate 代码+日期列保留字符串（行情类）。
func strColsCodeDate() map[string]bool {
	return map[string]bool{"date": true, "code": true, "calendar_date": true}
}

// strColsStock 股票列表（代码/名称）。
func strColsStock() map[string]bool {
	return map[string]bool{"code": true, "code_name": true, "tradestatus": true}
}

// strColsFina 财务类（代码/公告日/统计日）。
func strColsFina() map[string]bool {
	return map[string]bool{"code": true, "pubdate": true, "statdate": true}
}

// TradeDays 拉交易日历（calendar_date, is_open）。
// （TradeDays pulls the trading calendar.）
func (c *BaostockClient) TradeDays(start, end string) ([]TushareRow, error) {
	return c.call("trade_days", map[string]string{"start": start, "end": end}, strColsDay())
}

// AllStock 拉全部股票（code, code_name, tradeStatus）。
// （AllStock pulls the whole stock universe.）
func (c *BaostockClient) AllStock() ([]TushareRow, error) {
	return c.call("all_stock", nil, strColsStock())
}

// StockBasic 拉单只股票基本信息（code, code_name, ipoDate, outDate）。
// （StockBasic pulls one stock's listing info.）
func (c *BaostockClient) StockBasic(code string) ([]TushareRow, error) {
	return c.call("stock_basic", map[string]string{"code": code}, strColsStock())
}

// StockKline 拉单只股票不复权日线（含换手/停牌/估值/ST），date 升序。
// （StockKline pulls one stock's unadjusted daily bars incl. turnover/suspension/valuation/ST.）
func (c *BaostockClient) StockKline(code, start, end string) ([]TushareRow, error) {
	return c.call("kline", map[string]string{
		"code": code, "start": start, "end": end, "adjust": "3",
	}, strColsCodeDate())
}

// IndexKline 拉指数日线（不含估值字段）。
// （IndexKline pulls index daily bars.）
func (c *BaostockClient) IndexKline(code, start, end string) ([]TushareRow, error) {
	return c.call("index_kline", map[string]string{
		"code": code, "start": start, "end": end, "adjust": "3",
	}, strColsCodeDate())
}

// AdjFactor 拉复权因子（dividOperateDate, backAdjustFactor, adjustFactor…）。
// 后复权价 = close × backAdjustFactor。
// （AdjFactor pulls adjustment factors; hfq price = close × backAdjustFactor.）
func (c *BaostockClient) AdjFactor(code, start, end string) ([]TushareRow, error) {
	return c.call("adjust_factor", map[string]string{
		"code": code, "start": start, "end": end,
	}, map[string]bool{"code": true, "dividoperatedate": true})
}

// FinaProfit 拉单只股票某年某季度盈利能力（含 pubDate/statDate）。
// （FinaProfit pulls one stock's quarterly profitability.）
func (c *BaostockClient) FinaProfit(code string, year, quarter int) ([]TushareRow, error) {
	return c.call("profit", map[string]string{
		"code": code, "year": itoa(year), "quarter": itoa(quarter),
	}, strColsFina())
}

// FinaGrowth 拉单只股票某年某季度成长能力（净利同比等）。
// （FinaGrowth pulls one stock's quarterly growth metrics.）
func (c *BaostockClient) FinaGrowth(code string, year, quarter int) ([]TushareRow, error) {
	return c.call("growth", map[string]string{
		"code": code, "year": itoa(year), "quarter": itoa(quarter),
	}, strColsFina())
}

// FinaBalance 拉单只股票某年某季度偿债能力（资产负债率等）。
// （FinaBalance pulls one stock's quarterly solvency metrics.）
func (c *BaostockClient) FinaBalance(code string, year, quarter int) ([]TushareRow, error) {
	return c.call("balance", map[string]string{
		"code": code, "year": itoa(year), "quarter": itoa(quarter),
	}, strColsFina())
}

// Dividend 拉单只股票某年分红方案（DP 股息率因子来源）。
// （Dividend pulls one stock's dividend plans for the DP factor.）
func (c *BaostockClient) Dividend(code, year string) ([]TushareRow, error) {
	return c.call("dividend", map[string]string{
		"code": code, "year": year, "year_type": "report",
	}, map[string]bool{"code": true, "dividprenoticedate": true, "dividplannoticedate": true,
		"dividplanannouncedate": true, "dividoperatedate": true})
}

// TsCodeToBS 把 store 的 ts_code（600000.SH）转 baostock 代码（sh.600000）。
// （TsCodeToBS converts a store ts_code into a baostock code.）
func TsCodeToBS(code string) string {
	code = strings.TrimSpace(code)
	if strings.Contains(code, ".") {
		num, exch := splitTsCode(code)
		return bsPrefix(exch) + num
	}
	// 无后缀容错：按首位判断交易所
	num := code
	return bsPrefix(guessExchange(num)) + num
}

// BsCodeToTS 把 baostock 代码（sh.600000）转 store ts_code（600000.SH）。
// （BsCodeToTS converts a baostock code into a store ts_code.）
func BsCodeToTS(code string) string {
	code = strings.TrimSpace(code)
	parts := strings.SplitN(code, ".", 2)
	if len(parts) != 2 {
		// 无前缀容错（akshare fallback 返回裸代码）：按首位猜交易所。
		return code + guessExchange(code)
	}
	var suffix string
	switch parts[0] {
	case "sh":
		suffix = ".SH"
	case "sz":
		suffix = ".SZ"
	case "bj":
		suffix = ".BJ"
	default:
		return code
	}
	return parts[1] + suffix
}

// splitTsCode 拆分 "600000.SH" → ("600000", ".SH")。
func splitTsCode(code string) (string, string) {
	if i := strings.Index(code, "."); i >= 0 {
		return code[:i], code[i:]
	}
	return code, ""
}

// bsPrefix 交易所后缀 → baostock 前缀。
func bsPrefix(suffix string) string {
	switch suffix {
	case ".SH":
		return "sh."
	case ".SZ":
		return "sz."
	case ".BJ":
		return "bj."
	default:
		return "sh."
	}
}

// guessExchange 无后缀时按代码首位猜交易所（6→沪, 0/3→深, 4/8/920→北）。
// 920 为北交所新代码段（原 8 开头 4 开头除外），补齐后北交所研究/实盘链路能正确转 bj. 前缀。
func guessExchange(num string) string {
	if num == "" {
		return ".SH"
	}
	switch num[0] {
	case '6':
		return ".SH"
	case '0', '3':
		return ".SZ"
	case '4', '8':
		return ".BJ"
	case '9':
		if len(num) >= 3 && num[:3] == "920" {
			return ".BJ"
		}
		return ".SH"
	default:
		return ".SH"
	}
}
