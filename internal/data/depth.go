// Package data — 盘口（Order Book）数据。
// 免费数据源仅提供五档买卖盘（腾讯 qt.gtimg.cn / 东财 push2），此处按十档结构预留：
// Bids/Asks 切片固定为 10 档容量，填充五档后其余档位为零值。
// 战法可读取盘口派生因子：买卖压力、封单量、委比委差等。
// Package data — order-book data. Free sources only expose 5 levels (Tencent
// qt.gtimg.cn / EastMoney push2); the structure is pre-sized for 10 levels so
// that a future Level-2 feed simply fills the remaining slots. Strategy code can
// read derived factors: bid/ask pressure, seal volume, bid-ask ratio, etc.
package data

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// OrderLevel 盘口单档：价格与委托量（手）。
// OrderLevel is one price level: price and order volume (in lots).
type OrderLevel struct {
	Price  float64 `json:"price"`  // 价格（元）
	Volume float64 `json:"volume"` // 委托量（手）
}

// OrderBook 盘口快照。Bids/Asks 下标 0 为最优档（买一/卖一），容量预留 10 档。
// 免费源只填充前五档，6~10 档为零值；Level-2 接入后即可填满十档。
// OrderBook is an order-book snapshot. Index 0 of Bids/Asks is the best level
// (bid1/ask1); capacity is 10. Free sources fill the first five; a Level-2 feed
// can later fill all ten.
type OrderBook struct {
	Code      string       `json:"code"`       // 股票代码
	Name      string       `json:"name"`       // 股票名称
	Price     float64      `json:"price"`      // 最新价（元）
	PrevClose float64      `json:"prev_close"` // 昨收价（元）
	Time      string       `json:"time"`       // 数据时间（HH:MM:SS）
	Source    string       `json:"source"`     // 数据源："tencent"/"eastmoney"
	Bids      []OrderLevel `json:"bids"`       // 买盘（0=买一）
	Asks      []OrderLevel `json:"asks"`       // 卖盘（0=卖一）
}

// DepthLevels 常量：预留十档档位数。
// DepthLevels is the pre-reserved level count (10).
const DepthLevels = 10

// BigOrderKind 大单类型：托单（买盘大单）/压单（卖盘大单）。
// BigOrderKind classifies a detected large order: support (bid) or resistance (ask).
type BigOrderKind string

// 大单托压类型取值。
const (
	BigOrderSupport    BigOrderKind = "support"    // 托单：买盘大额挂单，意图托住股价
	BigOrderResistance BigOrderKind = "resistance" // 压单：卖盘大额挂单，意图压制股价
)

// BigOrder 识别出的一档托/压大单。
// BigOrder is a detected large order at one price level.
type BigOrder struct {
	Kind     BigOrderKind `json:"kind"`      // support=托单 / resistance=压单
	Level    int          `json:"level"`     // 档位（1=买一/卖一）
	Price    float64      `json:"price"`     // 委托价（元）
	Volume   float64      `json:"volume"`    // 委托量（手）
	SharePct float64      `json:"share_pct"` // 单档量占同侧五档总委托量比例（0~1）
	Signal   string       `json:"signal"`    // 方向含义：买盘支撑 / 卖盘压制
	Strength string       `json:"strength"`  // 强度：strong / medium / weak
}

// BigOrderConfig 托/压大单识别阈值配置。
// BigOrderConfig tunes large-order detection thresholds.
type BigOrderConfig struct {
	MinSharePct float64 // 单档占同侧总委托量比例下限（0~1，默认 0.3）
}

// DetectBigOrders 识别五档中的托单/压单。
// 口径：某档委托量 ≥ 同侧五档总委托量 × MinSharePct 时判为大单；
// 买盘大单 → 托单（支撑），卖盘大单 → 压单（压制）。
// 强度分级：share≥0.5 strong，≥0.4 medium，否则 weak。
// DetectBigOrders finds support (big bid) and resistance (big ask) levels.
// A level is flagged when its volume ≥ the same-side 5-level total × MinSharePct;
// big bids are support orders, big asks resistance orders.
func (ob *OrderBook) DetectBigOrders(cfg BigOrderConfig) []BigOrder {
	if cfg.MinSharePct <= 0 {
		cfg.MinSharePct = 0.3
	}
	var out []BigOrder
	bidSum := sumVol(ob.Bids[:5])
	askSum := sumVol(ob.Asks[:5])
	// 买盘：找托单（每档量占买盘总委托量比例 ≥ 阈值）
	for i := 0; i < 5; i++ {
		lv := ob.Bids[i]
		if lv.Volume <= 0 || lv.Price <= 0 || bidSum <= 0 {
			continue
		}
		share := lv.Volume / bidSum
		if share >= cfg.MinSharePct {
			out = append(out, BigOrder{
				Kind: BigOrderSupport, Level: i + 1, Price: lv.Price, Volume: lv.Volume,
				SharePct: share, Signal: "买盘支撑", Strength: strengthLabel(share),
			})
		}
	}
	// 卖盘：找压单
	for i := 0; i < 5; i++ {
		lv := ob.Asks[i]
		if lv.Volume <= 0 || lv.Price <= 0 || askSum <= 0 {
			continue
		}
		share := lv.Volume / askSum
		if share >= cfg.MinSharePct {
			out = append(out, BigOrder{
				Kind: BigOrderResistance, Level: i + 1, Price: lv.Price, Volume: lv.Volume,
				SharePct: share, Signal: "卖盘压制", Strength: strengthLabel(share),
			})
		}
	}
	return out
}

// strengthLabel 按单档占比给大单强度分级。
// strengthLabel tiers a large order's strength by its share of the side total.
func strengthLabel(share float64) string {
	switch {
	case share >= 0.5:
		return "strong"
	case share >= 0.4:
		return "medium"
	default:
		return "weak"
	}
}

// sumVol 统计前 levels 档委托量之和。
// sumVol totals the first `levels` levels' volume.
func sumVol(levels []OrderLevel) float64 {
	var s float64
	for _, l := range levels {
		s += l.Volume
	}
	return s
}

// newOrderBook 创建容量为 DepthLevels 的盘口快照。
func newOrderBook(code, name string) *OrderBook {
	return &OrderBook{
		Code: code,
		Name: name,
		Bids: make([]OrderLevel, DepthLevels),
		Asks: make([]OrderLevel, DepthLevels),
	}
}

// GetOrderBook 获取盘口快照：优先腾讯 qt.gtimg.cn（五档，稳定），失败回退东财 push2。
// 交易时段外或数据异常时返回错误。
// GetOrderBook fetches an order-book snapshot, preferring Tencent qt.gtimg.cn
// (5 levels, stable) and falling back to EastMoney push2 on failure.
func (m *MarketAPI) GetOrderBook(code string) (*OrderBook, error) {
	code = stripSuffix(code)
	ob, err := m.getTencentDepth(code)
	if err == nil {
		return ob, nil
	}
	em, emErr := m.getEastMoneyDepth(code)
	if emErr != nil {
		return nil, fmt.Errorf("tencent: %v; eastmoney: %v", err, emErr)
	}
	return em, nil
}

// tencentDepthRe 匹配腾讯盘口行情行：v_sh600519="...";
var tencentDepthRe = regexp.MustCompile(`v_(?:sh|sz|bj)(\d+)\s*=\s*"([^"]*)"`)

// getTencentDepth 从腾讯 qt.gtimg.cn 解析五档盘口。
// 字段布局（~ 分隔，实测）：[1]名称 [3]现价 [4]昨收
//
//	[9]/[10] 买一价/量 [11]/[12] 买二 ... [17]/[18] 买五
//	[19]/[20] 卖一价/量 [21]/[22] 卖二 ... [27]/[28] 卖五
//	数量单位为手。
//
// getTencentDepth parses the 5-level order book from Tencent qt.gtimg.cn.
func (m *MarketAPI) getTencentDepth(code string) (*OrderBook, error) {
	prefix := "sz"
	if strings.HasPrefix(code, "6") || strings.HasPrefix(code, "5") {
		prefix = "sh"
	}
	if strings.HasPrefix(code, "4") || strings.HasPrefix(code, "8") || strings.HasPrefix(code, "9") {
		prefix = "bj"
	}
	url := "https://qt.gtimg.cn/q=" + prefix + code
	TencentLimiter.Wait()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://gu.qq.com/")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tencent depth http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tencent depth read: %v", err)
	}
	utfBody, _, _ := transform.String(simplifiedchinese.GBK.NewDecoder(), string(body))
	mch := tencentDepthRe.FindStringSubmatch(utfBody)
	if mch == nil || len(mch) < 3 {
		return nil, fmt.Errorf("tencent depth: no data for %s", code)
	}
	fields := strings.Split(mch[2], "~")
	if len(fields) < 35 {
		return nil, fmt.Errorf("tencent depth: short payload (%d fields)", len(fields))
	}
	ob := newOrderBook(code, fields[1])
	ob.Price, _ = strconv.ParseFloat(fields[3], 64)
	ob.PrevClose, _ = strconv.ParseFloat(fields[4], 64)
	ob.Time = extractTencentTime(fields)
	ob.Source = "tencent"
	// 买盘：fields[9]=买一价 fields[10]=买一量，步长 2；卖盘同理从 [19] 开始。
	for i := 0; i < 5; i++ {
		bp, _ := strconv.ParseFloat(fields[9+i*2], 64)
		bv, _ := strconv.ParseFloat(fields[10+i*2], 64)
		ob.Bids[i] = OrderLevel{Price: bp, Volume: bv}
		ap, _ := strconv.ParseFloat(fields[19+i*2], 64)
		av, _ := strconv.ParseFloat(fields[20+i*2], 64)
		ob.Asks[i] = OrderLevel{Price: ap, Volume: av}
	}
	return ob, nil
}

// extractTencentTime 从腾讯行情字段中提取 HH:MM:SS 时间。
// 实测腾讯行情的时间字段（索引 30）格式为 "20260818104710"。
func extractTencentTime(fields []string) string {
	if len(fields) <= 30 {
		return ""
	}
	s := fields[30]
	if len(s) < 14 {
		return s
	}
	return s[8:10] + ":" + s[10:12] + ":" + s[12:14]
}

// getEastMoneyDepth 从东财 push2 stock/get 解析五档盘口。
// 东财字段（单位：分/手）：买一=f19/f20, 买二=f17/f18, 买三=f15/f16, 买四=f13/f14, 买五=f11/f12；
// 卖一=f39/f40, 卖二=f37/f38, 卖三=f35/f36, 卖四=f33/f34, 卖五=f31/f32。
// getEastMoneyDepth parses the 5-level order book from EastMoney push2 stock/get.
func (m *MarketAPI) getEastMoneyDepth(code string) (*OrderBook, error) {
	sid := secID(code)
	// fields 东方财富个股接口请求字段清单（分笔/盘口数据列）。
	const fields = "f11,f12,f13,f14,f15,f16,f17,f18,f19,f20,f31,f32,f33,f34,f35,f36,f37,f38,f39,f40,f43,f57,f58,f60,f86"
	url := fmt.Sprintf("https://push2.eastmoney.com/api/qt/stock/get?secid=%s&fltt=2&invt=2&fields=%s", sid, fields)
	EastMoneyLimiter.Wait()
	resp, err := m.getWithHeaders(url, emReferer)
	if err != nil {
		return nil, fmt.Errorf("eastmoney depth http: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eastmoney depth read: %v", err)
	}
	var raw struct {
		Data struct {
			F11 float64 `json:"f11"` // 买五价
			F12 float64 `json:"f12"` // 买五量
			F13 float64 `json:"f13"` // 买四价
			F14 float64 `json:"f14"` // 买四量
			F15 float64 `json:"f15"` // 买三价
			F16 float64 `json:"f16"` // 买三量
			F17 float64 `json:"f17"` // 买二价
			F18 float64 `json:"f18"` // 买二量
			F19 float64 `json:"f19"` // 买一价
			F20 float64 `json:"f20"` // 买一量
			F31 float64 `json:"f31"` // 卖五价
			F32 float64 `json:"f32"` // 卖五量
			F33 float64 `json:"f33"` // 卖四价
			F34 float64 `json:"f34"` // 卖四量
			F35 float64 `json:"f35"` // 卖三价
			F36 float64 `json:"f36"` // 卖三量
			F37 float64 `json:"f37"` // 卖二价
			F38 float64 `json:"f38"` // 卖二量
			F39 float64 `json:"f39"` // 卖一价
			F40 float64 `json:"f40"` // 卖一量
			F43 float64 `json:"f43"` // 最新价
			F57 string  `json:"f57"` // 代码
			F58 string  `json:"f58"` // 名称
			F60 float64 `json:"f60"` // 昨收
			F86 string  `json:"f86"` // 时间戳 yyyyMMddHHmmss
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("eastmoney depth json: %v", err)
	}
	if raw.Data.F43 == 0 || raw.Data.F57 == "" {
		return nil, fmt.Errorf("eastmoney depth: no data for %s", code)
	}
	ob := newOrderBook(code, raw.Data.F58)
	ob.Price = raw.Data.F43
	ob.PrevClose = raw.Data.F60
	ob.Source = "eastmoney"
	// 东财字段顺序与档位相反：f19 是买一，f11 是买五。
	bidPrices := []float64{raw.Data.F19, raw.Data.F17, raw.Data.F15, raw.Data.F13, raw.Data.F11}
	bidVols := []float64{raw.Data.F20, raw.Data.F18, raw.Data.F16, raw.Data.F14, raw.Data.F12}
	askPrices := []float64{raw.Data.F39, raw.Data.F37, raw.Data.F35, raw.Data.F33, raw.Data.F31}
	askVols := []float64{raw.Data.F40, raw.Data.F38, raw.Data.F36, raw.Data.F34, raw.Data.F32}
	for i := 0; i < 5; i++ {
		ob.Bids[i] = OrderLevel{Price: bidPrices[i], Volume: bidVols[i]}
		ob.Asks[i] = OrderLevel{Price: askPrices[i], Volume: askVols[i]}
	}
	ob.Time = raw.Data.F86
	return ob, nil
}

// ── 盘口派生因子（战法可读） ──
// ── Derived order-book factors (readable by strategies) ──

// OrderBookFactors 盘口派生因子，供战法打分与信号辅助判断。
// OrderBookFactors are derived order-book factors for strategy scoring.
type OrderBookFactors struct {
	BidVol      float64 `json:"bid_vol"`       // 买盘前 N 档委托总量（手）
	AskVol      float64 `json:"ask_vol"`       // 卖盘前 N 档委托总量（手）
	BidAskRatio float64 `json:"bid_ask_ratio"` // 委比 = (买量-卖量)/(买量+卖量)，-1~1，>0 买压强
	SealBid     float64 `json:"seal_bid"`      // 买一封单量（手，涨停时代表封单）
	SealAsk     float64 `json:"seal_ask"`      // 卖一封单量（手，跌停时代表封单）
	SpreadPct   float64 `json:"spread_pct"`    // 买一卖一价差百分比（越小流动性越好）
	NearPct     float64 `json:"near_pct"`      // 买五~卖五 报价覆盖范围占现价比例（%）
}

// Factors 计算盘口派生因子。levels 为参与统计的档位数（免费源为 5）。
// Factors derives order-book factors. levels is the number of levels to count
// (5 for free sources).
func (ob *OrderBook) Factors(levels int) OrderBookFactors {
	if levels <= 0 || levels > DepthLevels {
		levels = 5
	}
	f := OrderBookFactors{}
	var bidSum, askSum float64
	for i := 0; i < levels; i++ {
		bidSum += ob.Bids[i].Volume
		askSum += ob.Asks[i].Volume
	}
	f.BidVol = bidSum
	f.AskVol = askSum
	if bidSum+askSum > 0 {
		f.BidAskRatio = (bidSum - askSum) / (bidSum + askSum)
	}
	f.SealBid = ob.Bids[0].Volume
	f.SealAsk = ob.Asks[0].Volume
	if ob.Price > 0 {
		bid1 := ob.Bids[0].Price
		ask1 := ob.Asks[0].Price
		if bid1 > 0 && ask1 > 0 {
			f.SpreadPct = (ask1 - bid1) / ob.Price * 100
		}
		// 报价覆盖范围：买五~卖五 覆盖现价上下比例
		bidN := ob.Bids[levels-1].Price
		askN := ob.Asks[levels-1].Price
		if bidN > 0 && askN > 0 {
			f.NearPct = (askN - bidN) / ob.Price * 100
		}
	}
	return f
}

// Validate 校验盘口数据完整性（供测试与数据源健康检查使用）。
// Validate checks order-book integrity (for tests and source health checks).
func (ob *OrderBook) Validate() error {
	if ob == nil || ob.Code == "" {
		return fmt.Errorf("empty order book")
	}
	if len(ob.Bids) < 5 || len(ob.Asks) < 5 {
		return fmt.Errorf("order book must pre-allocate at least 5 levels")
	}
	if ob.Bids[0].Price <= 0 || ob.Asks[0].Price <= 0 {
		return fmt.Errorf("missing best bid/ask for %s", ob.Code)
	}
	return nil
}

var _ = json.Marshal
