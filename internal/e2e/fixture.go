// Package e2e 全流程端到端测试：用实盘抓取的 fixture 数据 mock 全部外部数据源
// （东财/新浪/同花顺/财联社 + LLM），驱动 engine.Run 全链路并逐场景断言。
package e2e

import (
	"encoding/json"
	"os"

	"quant-trading-v2/internal/data"
)

// Fixture 实盘数据快照（由 cmd/fixturecapture 抓取生成，固化在 testdata/fixtures.json）。
// 测试运行时只读该文件，不联网。
type Fixture struct {
	// CapturedAt 快照抓取时间（格式 YYYY-MM-DD HH:MM:SS）。
	CapturedAt string `json:"captured_at"`

	// THS 板块页 HTML（UTF-8 解码后），同时供 GetBoardList / GetTopBoards 解析。
	THSIndustries string `json:"ths_industries_html"`
	THSConcepts   string `json:"ths_concepts_html"`

	// 东财行业板块列表（clist fs=m:90+t:2）。
	EMBoardList []data.SectorInfo `json:"em_board_list"`

	// 全量股票列表 name -> code（StockCleaner 初始化映射）。
	StockList map[string]string `json:"stock_list"`

	// 当日涨停池 / 龙虎榜 / 新股日历。
	LimitUpPool []data.LimitUpStock `json:"limit_up_pool"`
	LHB         []data.LHBItem      `json:"lhb"`
	IPO         []data.IPOEvent     `json:"ipo"`

	// 板块成分股 sectorCode -> 成分股（fs=b:<code>）。
	SectorStocks map[string][]data.StockInfo `json:"sector_stocks"`

	// 个股行业代码 -> 行业名（GetStockIndustry f128）。
	Industries map[string]string `json:"industries"`

	// 个股行情：code -> 新浪 CSV 字段串（逗号分隔，首字段为名称）。
	// 字段序：name,open,prev_close,price,high,low,...,volume,amount,...
	Quotes map[string]string `json:"quotes"`

	// 个股日K线（升序，同花顺/新浪口径），同时供新浪/东财两种 K 线接口重放。
	Klines map[string][]data.KLine `json:"klines"`

	// 个股5分钟K线（升序）：供 GetSinaMinuteKLine(scale=5) 重放（专业模式 MACD 真实数据）。
	MinuteKlines map[string][]data.KLine `json:"minute_klines"`

	// 个股资金流：code -> 东财 fflow klines 行（date,elgBuy,elgSell,lgBuy,lgSell,mdBuy,mdSell,smBuy,smSell,net,...）。
	MoneyFlow map[string][]string `json:"money_flow"`

	// 个股主力净流入（元）：code -> 东财 push2 f162，供 emStockGet 实时行情返回与专业模式断言。
	// 未填写的代码回退为 0。
	NetInflows map[string]float64 `json:"net_inflows"`

	// 指数行情（indexPrice/ma20/upCount/downCount）。
	IndexPrice float64 `json:"index_price"`
	IndexMA20  float64 `json:"index_ma20"`
	UpCount    int     `json:"up_count"`
	DownCount  int     `json:"down_count"`

	// 场景新闻：按数据源分桶，标题/正文/时间均为可复现的固定值。
	News map[string][]data.NewsItem `json:"news"`
}

// LoadFixture 从 testdata/fixtures.json 加载实盘数据快照。
func LoadFixture(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
