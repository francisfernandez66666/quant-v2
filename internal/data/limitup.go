// 东方财富涨停池与龙虎榜数据源（push2ex / datacenter-web）。
// 涨停池字段：lbc连板 / fund封单 / zbc炸板次数 / fbt首次封板时间 / hybk行业 / hs换手 / ltsz流通市值。
// 龙虎榜字段：BILLBOARD_NET_AMT净买入 / BUY_SEAT卖出席位数 / EXPLAIN席位说明。
// English: EastMoney limit-up pool and dragon-tiger-list data sources (push2ex / datacenter-web).
// Pool fields: lbc consecutive limit-ups / fund seal amount / zbc break count / fbt first-seal time /
// hybk industry / hs turnover / ltsz float market cap.
// LHB fields: BILLBOARD_NET_AMT net buy / BUY_SEAT buy-seat count / EXPLAIN seat explanation.
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// LimitUpStock 涨停池中的单只股票。
// English: LimitUpStock is a single stock in the limit-up pool.
type LimitUpStock struct {
	Code       string  `json:"code"`        // 股票代码
	Name       string  `json:"name"`        // 股票名称
	Price      float64 `json:"price"`       // 最新价（元）
	ChangePct  float64 `json:"change_pct"`  // 涨跌幅（%）
	Amount     float64 `json:"amount"`      // 成交额（元）
	FlowMCap   float64 `json:"flow_mcap"`   // 流通市值（元）
	Turnover   float64 `json:"turnover"`    // 换手率（%）
	LianBan    int     `json:"lian_ban"`    // 连板数
	FirstSeal  string  `json:"first_seal"`  // 首次封板时间（HH:MM）
	SealAmt    float64 `json:"seal_amt"`    // 封单资金（元）
	SealRatio  float64 `json:"seal_ratio"`  // 封单/流通市值（%）
	BreakCount int     `json:"break_count"` // 炸板次数
	Industry   string  `json:"industry"`    // 所属行业
	UpDays     int     `json:"up_days"`     // 近期涨停天数
	LimitType  string  `json:"limit_type"`  // 涨停原因分类（由分析层填充）
}

// LHBItem 龙虎榜单条记录。
// English: LHBItem is a single dragon-tiger-list record.
type LHBItem struct {
	Code        string  `json:"code"`        // 股票代码
	Name        string  `json:"name"`        // 股票名称
	Price       float64 `json:"price"`       // 收盘价
	ChangePct   float64 `json:"change_pct"`  // 涨跌幅（%）
	Reason      string  `json:"reason"`      // 上榜原因
	SeatInfo    string  `json:"seat_info"`   // 席位说明（如"2家机构买入"）
	NetAmt      float64 `json:"net_amt"`     // 净买入额（元）
	BuyAmt      float64 `json:"buy_amt"`     // 买入额（元）
	SellAmt     float64 `json:"sell_amt"`    // 卖出额（元）
	BuySeat     int     `json:"buy_seat"`    // 买入席位数
	SellSeat    int     `json:"sell_seat"`   // 卖出席位数
	Turnover    float64 `json:"turnover"`    // 换手率（%）
	Institution bool    `json:"institution"` // 是否含机构席位
}

// GetLimitUpPool 获取东财涨停池。date 格式 "2006-01-02"，为空则取当日。
// 返回当日涨停股票列表（含连板/封单/炸板/首封时间等盘面数据）。
func (m *MarketAPI) GetLimitUpPool(date string) ([]LimitUpStock, error) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	poolDate := strings.ReplaceAll(date, "-", "")
	url := fmt.Sprintf("https://push2ex.eastmoney.com/getTopicZTPool?ut=7eea3edcaed734bea9cbfc24409ed989&dpt=wz.ztzt&Pageindex=0&pagesize=600&sort=fbt:asc&date=%s", poolDate)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney ztpool http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney ztpool read: %v", err)
	}
	return parseLimitUpPool(body)
}

// limitUpRaw 东方财富涨停池接口原始响应结构（仅取需要的字段）。
// 价格字段 p 单位为"厘"（0.001 元），解析时需 ÷1000；其余字段含义见各字段注释。
type limitUpRaw struct {
	Data struct {
		Pool []struct {
			Code       string  `json:"c"`      // 代码
			Name       string  `json:"n"`      // 名称
			Price      float64 `json:"p"`      // 最新价（厘，0.001元，需 ÷1000）
			ChangePct  float64 `json:"zdp"`    // 涨跌幅（%）
			Amount     float64 `json:"amount"` // 成交额（元）
			FlowMCap   float64 `json:"ltsz"`   // 流通市值（元）
			Turnover   float64 `json:"hs"`     // 换手率（%）
			LianBan    int     `json:"lbc"`    // 连板数
			FirstSeal  int     `json:"fbt"`    // 首次封板时间 HHMMSS
			SealAmt    float64 `json:"fund"`   // 封单资金（元）
			BreakCount int     `json:"zbc"`    // 炸板次数
			Industry   string  `json:"hybk"`   // 行业
			ZTJ        struct {
				Days int `json:"days"` // 近期涨停天数
			} `json:"zttj"`
		} `json:"pool"`
	} `json:"data"` // 数据主体
}

// parseLimitUpPool 解析东财涨停池 JSON。
// 价格字段 p 单位为厘（0.001元/千分位）需 ÷1000（实测：有研新材 p=48170 = 48.17元）。
// 首次封板时间 fbt 为 HHMMSS 整数转 "HH:MM"。封单占比 SealRatio 由封单资金与流通市值计算得出。
func parseLimitUpPool(body []byte) ([]LimitUpStock, error) {
	var raw limitUpRaw
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney ztpool json: %v", err)
	}
	stocks := make([]LimitUpStock, 0, len(raw.Data.Pool))
	for _, p := range raw.Data.Pool {
		if p.Code == "" {
			continue
		}
		stocks = append(stocks, LimitUpStock{
			Code:       p.Code,
			Name:       p.Name,
			Price:      p.Price / 1000,
			ChangePct:  p.ChangePct,
			Amount:     p.Amount,
			FlowMCap:   p.FlowMCap,
			Turnover:   p.Turnover,
			LianBan:    p.LianBan,
			FirstSeal:  sealTime(p.FirstSeal),
			SealAmt:    p.SealAmt,
			SealRatio:  sealRatio(p.SealAmt, p.FlowMCap),
			BreakCount: p.BreakCount,
			Industry:   p.Industry,
			UpDays:     p.ZTJ.Days,
		})
	}
	return stocks, nil
}

// sealTime 将东财 HHMMSS 整数转为 "HH:MM" 字符串（090000 → "09:00"）。
func sealTime(t int) string {
	if t <= 0 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", t/10000, t/100%100)
}

// sealRatio 封单占流通市值比例（%）。
func sealRatio(sealAmt, flowMCap float64) float64 {
	if flowMCap <= 0 {
		return 0
	}
	return sealAmt / flowMCap * 100
}

// GetLHBData 获取东财龙虎榜。date 格式 "2006-01-02"，为空则取上一个交易日。
// 返回当日龙虎榜全部记录，含净买入、席位、机构标记等。
func (m *MarketAPI) GetLHBData(date string) ([]LHBItem, error) {
	if date == "" {
		date = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	}
	filter := fmt.Sprintf("(TRADE_DATE='%s')", date)
	url := fmt.Sprintf("https://datacenter-web.eastmoney.com/api/data/v1/get?reportName=RPT_DAILYBILLBOARD_DETAILSNEW&columns=ALL&filter=%s&sortColumns=BILLBOARD_NET_AMT&sortTypes=-1&pageSize=500&pageNumber=1", filter)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emDataReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney lhb http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney lhb read: %v", err)
	}
	return parseLHB(body)
}

// parseLHB 解析东财龙虎榜 JSON。
// 席位字段 BUY_SEAT/SELL_SEAT 为 float64，转换为 int；
// Institution 通过检查席位说明是否含"机构"关键字标记。
func parseLHB(body []byte) ([]LHBItem, error) {
	var raw struct {
		Success bool `json:"success"` // 接口调用是否成功
		Result  *struct {
			Data []struct {
				Code      string  `json:"SECURITY_CODE"`      // 代码
				Name      string  `json:"SECURITY_NAME_ABBR"` // 名称
				Price     float64 `json:"CLOSE_PRICE"`        // 收盘价
				ChangePct float64 `json:"CHANGE_RATE"`        // 涨跌幅（%）
				Reason    string  `json:"EXPLANATION"`        // 上榜原因
				SeatInfo  string  `json:"EXPLAIN"`            // 席位说明
				NetAmt    float64 `json:"BILLBOARD_NET_AMT"`  // 净买入额（元）
				BuyAmt    float64 `json:"BILLBOARD_BUY_AMT"`  // 买入额（元）
				SellAmt   float64 `json:"BILLBOARD_SELL_AMT"` // 卖出额（元）
				BuySeat   float64 `json:"BUY_SEAT"`           // 买入席位数
				SellSeat  float64 `json:"SELL_SEAT"`          // 卖出席位数
				Turnover  float64 `json:"TURNOVERRATE"`       // 换手率（%）
			} `json:"data"` // 龙虎榜记录数组
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney lhb json: %v", err)
	}
	if !raw.Success || raw.Result == nil {
		return nil, fmt.Errorf("eastmoney lhb: API returned success=false")
	}
	// 逐条龙虎榜记录组装 LHBItem（跳过空代码行）。
	items := make([]LHBItem, 0, len(raw.Result.Data))
	for _, r := range raw.Result.Data {
		if r.Code == "" {
			continue
		}
		items = append(items, LHBItem{
			Code:        r.Code,
			Name:        r.Name,
			Price:       r.Price,
			ChangePct:   r.ChangePct,
			Reason:      r.Reason,
			SeatInfo:    r.SeatInfo,
			NetAmt:      r.NetAmt,
			BuyAmt:      r.BuyAmt,
			SellAmt:     r.SellAmt,
			BuySeat:     int(r.BuySeat),
			SellSeat:    int(r.SellSeat),
			Turnover:    r.Turnover,
			Institution: strings.Contains(r.SeatInfo, "机构"),
		})
	}
	return items, nil
}
