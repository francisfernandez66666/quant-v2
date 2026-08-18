// Tushare Pro 历史数据客户端：日线/复权因子/每日指标/涨跌停/财务指标/利润表/现金流。
// 仅供离线研究链路（B 阶段回测/因子）使用，与实时行情源相互独立。
// （Tushare Pro historical-data client: daily bars, adjustment factors, per-day indicators,
// limit prices, financial indicators, income and cash-flow statements. Used only by the
// offline research chain (Phase-B backtest/factors), independent of the realtime sources.）
package data

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TushareClient Tushare Pro HTTP 历史数据客户端。
// 仅用于离线研究链路（B 阶段回测/因子/自动研究），不进入交易时段的实时关键路径；
// 因此允许在 2 次/秒的官方限流下按需批量拉取，而不牺牲盘中延迟。
// 数据说明：daily/adj_factor 为前复权基座，hfq 由 adj_factor 在 Go 侧换算（基座因子在
// 收益率/动量等比例型因子里自然抵消，不影响因子值）。财务类接口（fina_indicator/income/
// cashflow）在 2000 积分档必须按 ts_code 单票拉取，故数据加载以单票为单位断点续传。
// （TushareClient is an HTTP client for Tushare Pro historical data, used only by the offline
// research chain (Phase-B backtest/factors/auto-research) and never on the intraday critical path.
// It batches by trade_date for bar-like tables and pulls financials per ts_code, resumable per stock.）
type TushareClient struct {
	token  string
	client *http.Client
}

// TushareRow 一行 Tushare 数据：字段名 → 值（字段缺失/值为 null 时为 nil）。
// （TushareRow is one row of Tushare data: field name → value (nil when absent/null).）
type TushareRow map[string]any

// NewTushareClient 创建 Tushare 客户端。
// （NewTushareClient builds a Tushare client with a 30s HTTP timeout.）
func NewTushareClient(token string) *TushareClient {
	return &TushareClient{
		token: token,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				ForceAttemptHTTP2: false, // 强制 HTTP1.1，规避连接复用问题（与其它源客户端一致）
			},
		},
	}
}

// tushareAPI Tushare Pro 的统一 POST 端点（官方地址）。
// 独立变量以便测试中可注入 httptest 服务器地址。
// （tushareAPI is Tushare Pro's unified POST endpoint; a variable so tests can inject a mock server.）
var tushareAPI = "https://api.tushare.pro"

// tushareResponse Tushare 统一响应外壳。
// （tushareResponse is the common response envelope of the Tushare API.）
type tushareResponse struct {
	Code int `json:"code"` // 0=成功，非 0=失败（含积分不足/参数错误）
	Msg  string `json:"msg"`
	Data struct {
		Fields []string `json:"fields"` // 列名
		Items  [][]any  `json:"items"`  // 行数据（与 fields 对齐）
	} `json:"data"`
}

// Call 调用任意 Tushare API：apiName 接口名、params 参数、fields 逗号分隔的列。
// 返回逐行字段映射；调用失败（HTTP/业务 code!=0）返回错误。
// （Call invokes any Tushare API and returns rows as field maps; errors on HTTP/business failure.）
func (c *TushareClient) Call(apiName string, params map[string]string, fields string) ([]TushareRow, error) {
	payload := map[string]any{
		"api_name": apiName,
		"token":    c.token,
		"params":   params,
		"fields":   fields,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("tushare %s 序列化: %v", apiName, err)
	}
	req, err := http.NewRequest("POST", tushareAPI, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")

	TushareLimiter.Wait()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tushare %s http: %v", apiName, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tushare %s read: %v", apiName, err)
	}
	var tr tushareResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("tushare %s 响应解析: %v", apiName, err)
	}
	if tr.Code != 0 {
		return nil, fmt.Errorf("tushare %s 业务失败 code=%d msg=%s", apiName, tr.Code, tr.Msg)
	}
	// 字段名做一次 lowercase 归一（财务接口偶有大小写差异），并保留原始顺序供后续映射。
	fieldsArr := tr.Data.Fields
	out := make([]TushareRow, 0, len(tr.Data.Items))
	for _, item := range tr.Data.Items {
		row := make(TushareRow, len(fieldsArr))
		for i, f := range fieldsArr {
			key := strings.ToLower(f)
			if i < len(item) && item[i] != nil {
				row[key] = item[i]
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// S 取字符串值（数值/字符串统一转字符串，nil 为空串）。（S returns the string value of a cell.）
func (r TushareRow) S(key string) string {
	if r == nil {
		return ""
	}
	v, ok := r[strings.ToLower(key)]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// F 取浮点值（无法解析返回 0）。（F returns the float value of a cell, 0 on parse failure.）
func (r TushareRow) F(key string) float64 {
	if r == nil {
		return 0
	}
	v, ok := r[strings.ToLower(key)]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return f
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return 0
	}
}

// I 取整数值（无法解析返回 0）。（I returns the int value of a cell, 0 on parse failure.）
func (r TushareRow) I(key string) int {
	return int(r.F(key))
}
// StockBasic 获取 A 股上市公司基础信息（全量，2020 前上市含已退市）。
// （StockBasic returns the full A-share listing universe, including delisted names.）
func (c *TushareClient) StockBasic() ([]TushareRow, error) {
	return c.Call("stock_basic", map[string]string{
		"exchange": "", "list_status": "L", "fields": "",
	}, "ts_code,symbol,name,area,industry,market,list_date,delist_date")
}

// TradeCal 获取交易日历（is_open=1 为交易日）。
// （TradeCal returns the trading calendar, is_open=1 marking trading days.）
func (c *TushareClient) TradeCal(start, end string) ([]TushareRow, error) {
	return c.Call("trade_cal", map[string]string{
		"exchange": "SSE", "start_date": start, "end_date": end,
	}, "cal_date,is_open")
}

// DailyByDate 按交易日拉全市场日线（不复权）。
// 单日返回该日全部上市股票（约 5000+ 行），行数在单次调用上限内。
// （DailyByDate pulls the whole-market daily bars (unadjusted) for one trade date.）
func (c *TushareClient) DailyByDate(tradeDate string) ([]TushareRow, error) {
	return c.Call("daily", map[string]string{"trade_date": tradeDate}, "ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount")
}

// AdjFactorByDate 按交易日拉全市场复权因子（hfq 换算基座）。
// （AdjFactorByDate pulls the whole-market adjustment factors for one trade date.）
func (c *TushareClient) AdjFactorByDate(tradeDate string) ([]TushareRow, error) {
	return c.Call("adj_factor", map[string]string{"trade_date": tradeDate}, "ts_code,trade_date,adj_factor")
}

// DailyBasicByDate 按交易日拉全市场每日指标（换手/估值/市值等）。
// （DailyBasicByDate pulls whole-market per-day fundamentals (turnover/valuation/cap) for one date.）
func (c *TushareClient) DailyBasicByDate(tradeDate string) ([]TushareRow, error) {
	return c.Call("daily_basic", map[string]string{"trade_date": tradeDate},
		"ts_code,trade_date,turnover_rate,turnover_rate_f,volume_ratio,pe,pe_ttm,pb,ps,ps_ttm,dv_ratio,dv_ttm,total_share,float_share,free_share,total_mv,circ_mv")
}

// StkLimitByDate 按交易日拉全市场涨跌停价格。
// （StkLimitByDate pulls whole-market limit-up/down prices for one date.）
func (c *TushareClient) StkLimitByDate(tradeDate string) ([]TushareRow, error) {
	return c.Call("stk_limit", map[string]string{"trade_date": tradeDate}, "ts_code,trade_date,up_limit,down_limit")
}

// IndexDaily 拉指数日线（如沪深300 000300.SH），按日期范围一次取回。
// （IndexDaily pulls index daily bars over a date range in one call.）
func (c *TushareClient) IndexDaily(tsCode, start, end string) ([]TushareRow, error) {
	return c.Call("index_daily", map[string]string{"ts_code": tsCode, "start_date": start, "end_date": end},
		"ts_code,trade_date,open,high,low,close,pre_close,change,pct_chg,vol,amount")
}

// FinaIndicator 拉单只股票财务指标（按报告期）。
// （FinaIndicator pulls one stock's financial-indicator report by period.）
func (c *TushareClient) FinaIndicator(tsCode, start, end string) ([]TushareRow, error) {
	return c.Call("fina_indicator", map[string]string{
		"ts_code": tsCode, "start_date": start, "end_date": end,
	}, "ts_code,end_date,ann_date,eps,roe,roe_waa,roa,roe_dt,grossprofit_margin,netprofit_margin,debt_to_assets,yoy_or,yoy_net_profit,or_yoy,netprofit_yoy")
}

// Income 拉单只股票利润表（归母净利等，供单季同比/SUE 降级版计算）。
// （Income pulls one stock's income statement for single-quarter YoY / degraded SUE.）
func (c *TushareClient) Income(tsCode, start, end string) ([]TushareRow, error) {
	return c.Call("income", map[string]string{
		"ts_code": tsCode, "start_date": start, "end_date": end,
	}, "ts_code,end_date,n_income_attr_p,revenue,total_revenue")
}

// Cashflow 拉单只股票现金流量表（经营/投资/筹资净现金流）。
// （Cashflow pulls one stock's cash-flow statement.）
func (c *TushareClient) Cashflow(tsCode, start, end string) ([]TushareRow, error) {
	return c.Call("cashflow", map[string]string{
		"ts_code": tsCode, "start_date": start, "end_date": end,
	}, "ts_code,end_date,n_cashflow_act,n_cashflow_inv_act,n_cashflow_fnc_act")
}