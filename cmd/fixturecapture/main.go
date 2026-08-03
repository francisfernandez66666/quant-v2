// Package main fixture 抓取工具：联网抓取实盘数据生成 e2e 测试 fixture。
// 用法: go run ./cmd/fixturecapture
// 输出: internal/e2e/testdata/fixtures.json（测试运行时只读此文件，不联网）。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/e2e"
)

// scenarioStocks 场景涉及的个股（新闻归因 + 行情/K线/资金流）。
var scenarioStocks = []string{
	"300750", // 宁德时代 个股利好
	"600519", // 贵州茅台 个股利空
	"688981", // 中芯国际 半导体
	"300308", // 中际旭创 算力
	"600276", // 恒瑞医药 个股利好→预期差
	"002594", // 比亚迪 汽车
	"000938", // 紫光股份 通信/算力
	"300059", // 东方财富 券商
}

// scenarioBoards 场景涉及的板块名（须命中同花顺真实板块名单）。
var scenarioBoards = []string{"人工智能", "半导体", "锂电池概念", "化学制药", "白酒", "汽车整车"}

// retry 对易被临时反爬拦截的东财接口重试（最多 attempts 次，递增退避）。
func retry[T any](name string, attempts int, fn func() (T, error)) (T, error) {
	var zero T
	var err error
	for i := 1; i <= attempts; i++ {
		v, e := fn()
		if e == nil {
			return v, nil
		}
		err = e
		wait := time.Duration(i) * time.Second
		log.Printf("%s 第%d次失败(%v), %v后重试", name, i, e, wait)
		time.Sleep(wait)
	}
	return zero, err
}

// main 联网抓取实盘数据并生成 e2e 测试 fixture：
// 依次抓取同花顺板块页、东财板块/股票列表、涨停池/龙虎榜/新股日历、
// 场景板块成分股与个股行情/K线/资金流，最后写入 internal/e2e/testdata/fixtures.json。
func main() {
	log.SetFlags(log.LstdFlags)
	// 东财行情 API + 同花顺板块客户端
	api := data.NewMarketAPI()
	ths := data.NewTHSClient()

	// fixture 载体：CapturedAt 记录抓取时间，News 使用固定编写的场景新闻
	fix := &e2e.Fixture{
		CapturedAt: time.Now().Format("2006-01-02 15:04:05"),
		News:       authoredNews(),
	}

	// 1. 同花顺板块名单（UTF-8 解码后原始 HTML，供 GetBoardList/GetTopBoards 双解析）
	if html, err := ths.GetBoardListRaw(); err == nil {
		fix.THSIndustries = html["https://q.10jqka.com.cn/thshy/"]
		fix.THSConcepts = html["https://q.10jqka.com.cn/gn/"]
		log.Printf("THS 板块页: 行业%d字节 概念%d字节", len(fix.THSIndustries), len(fix.THSConcepts))
	} else {
		log.Printf("THS 板块页抓取失败: %v", err)
	}

	// 2. 东财行业板块列表
	if sectors, err := retry("东财板块", 4, func() ([]data.SectorInfo, error) { return api.GetSectorList() }); err == nil {
		fix.EMBoardList = sectors
		log.Printf("东财行业板块: %d 个", len(sectors))
	} else {
		log.Printf("东财板块失败: %v", err)
	}

	// 3. 全量股票列表（cleaner 映射）
	if list, err := retry("股票列表", 4, func() (map[string]string, error) { return api.GetStockList() }); err == nil {
		fix.StockList = list
		log.Printf("股票列表: %d 只", len(list))
	} else {
		log.Printf("股票列表失败: %v", err)
	}

	// 4. 当日涨停池 + 龙虎榜 + 新股日历
	if pool, err := api.GetLimitUpPool(""); err == nil {
		fix.LimitUpPool = pool
		log.Printf("涨停池: %d 只", len(pool))
	} else {
		log.Printf("涨停池失败: %v", err)
	}
	if lhb, err := api.GetLHBData(""); err == nil {
		fix.LHB = lhb
		log.Printf("龙虎榜: %d 条", len(lhb))
	} else {
		log.Printf("龙虎榜失败: %v", err)
	}
	if ipo, err := api.GetEastMoneyIPOCalendar(); err == nil {
		fix.IPO = ipo
		log.Printf("新股日历: %d 条", len(ipo))
	} else {
		log.Printf("新股日历失败: %v", err)
	}

	// 5. 场景板块成分股（同花顺代码 → 东财成分股）
	fix.SectorStocks = make(map[string][]data.StockInfo)
	var boards []data.SectorInfo
	if b, err := ths.GetBoardList(); err == nil {
		boards = b
		// 建立 板块名→板块信息 索引，便于按场景板块名精确命中真实板块
		byName := make(map[string]data.SectorInfo)
		for _, bl := range boards {
			byName[bl.Name] = bl
		}
		for _, name := range scenarioBoards {
			b, ok := byName[name]
			if !ok {
				log.Printf("板块 %q 未在同花顺名单中", name)
				continue
			}
			stocks, err := retry(fmt.Sprintf("板块%s成分股", name), 4, func() ([]data.StockInfo, error) { return api.GetSectorStocks(b.Code, 100) })
			if err != nil {
				log.Printf("板块 %s(%s) 成分股失败: %v", name, b.Code, err)
				continue
			}
			fix.SectorStocks[b.Code] = stocks
			log.Printf("板块 %s(%s): %d 只成分股", name, b.Code, len(stocks))
		}
	} else {
		log.Printf("同花顺板块名单失败: %v", err)
	}

	// 6. 场景个股行业 + 行情 + K线 + 资金流
	fix.Industries = make(map[string]string)
	fix.Quotes = make(map[string]string)
	fix.Klines = make(map[string][]data.KLine)
	fix.MoneyFlow = make(map[string][]string)

	sinaQuotes := api.GetSinaQuotes(scenarioStocks)
	for _, code := range scenarioStocks {
		// 新浪行情：保留到 fix.Quotes（CSV 字符串），并抓取个股所属行业
		if si, ok := sinaQuotes[code]; ok && si != nil && si.Price > 0 {
			fix.Quotes[code] = sinaQuoteCSV(si)
			if ind := api.GetStockIndustry(code); ind != "" {
				fix.Industries[code] = ind
			}
		} else {
			log.Printf("新浪行情缺失 %s", code)
		}

		// K线：新浪返回升序，mock 需要最新在前（解析器会再反转）
		if klines, err := api.GetSinaKLine(code, 120); err == nil && len(klines) > 0 {
			// 新浪返回升序，mock 需要最新在前（解析器会再反转）
			rev := make([]data.KLine, len(klines))
			for i, k := range klines {
				rev[len(klines)-1-i] = k
			}
			fix.Klines[code] = rev
		} else {
			log.Printf("K线缺失 %s: %v", code, err)
		}

		// 资金流：东财易被反爬拦截，需带重试；金额单位换算为万元后存为 CSV 行
		if cf, err := retry("资金流", 4, func() (*data.CapitalFlow, error) { return api.GetStockMoneyFlow(code) }); err == nil && cf != nil {
			fix.MoneyFlow[code] = []string{
				fmt.Sprintf("2026-07-29,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,0,0,0",
					cf.SuperLargeIn/10000, cf.SuperLargeOut/10000, cf.LargeIn/10000, cf.LargeOut/10000,
					cf.MediumIn/10000, cf.MediumOut/10000, cf.SmallIn/10000, cf.SmallOut/10000, cf.NetInflow/10000),
			}
		} else {
			log.Printf("资金流缺失 %s: %v", code, err)
		}
	}

	// 7. 指数
	if idx, _, up, down, err := api.GetIndexData(); err == nil {
		fix.IndexPrice = idx
		fix.UpCount = up
		fix.DownCount = down
		log.Printf("指数: %.2f up=%d down=%d", idx, up, down)
	}

	// 8. 缺失字段确定性兜底（东财 push2 接口可能被反爬 EOF，保证 fixture 自洽可复现）
	applyFallbacks(fix, api, sinaQuotes, boards)

	// 9. 写入 fixture
	outPath := filepath.Join("internal", "e2e", "testdata", "fixtures.json")
	raw, _ := json.MarshalIndent(fix, "", "  ")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(outPath, raw, 0644); err != nil {
		log.Fatalf("write: %v", err)
	}
	log.Printf("fixture 写入 %s (%d KB)", outPath, len(raw)/1024)
}

// applyFallbacks 对东财 push2 可能被反爬拦截的字段补确定性兜底数据：
//   - stock_list（cleaner 名称↔代码映射，Stage0 个股归因必需）
//   - money_flow（资金流向，策略行情数据用）
//   - sector_stocks（板块成分股，板块→个股传播/验证用）
//   - industries（个股行业，IPO 板块填充用）
//   - index_price / up / down（指数行情）
//
// 兜底值均为真实代码/名称/板块归属，测试可精确断言。
func applyFallbacks(fix *e2e.Fixture, api *data.MarketAPI, sinaQuotes map[string]*data.StockInfo, boards []data.SectorInfo) {
	if fix.StockList == nil {
		fix.StockList = make(map[string]string)
	}
	if fix.MoneyFlow == nil {
		fix.MoneyFlow = make(map[string][]string)
	}
	if fix.SectorStocks == nil {
		fix.SectorStocks = make(map[string][]data.StockInfo)
	}
	if fix.Industries == nil {
		fix.Industries = make(map[string]string)
	}

	// 名称↔代码：场景股 + 传播成分股（来自 sina 行情首字段）
	if len(fix.StockList) < len(scenarioStocks) {
		for _, code := range scenarioStocks {
			if q, ok := fix.Quotes[code]; ok {
				name := strings.SplitN(q, ",", 2)[0]
				if name != "" {
					fix.StockList[name] = code
				}
			}
		}
		// 板块成分股兜底成员（真实板块归属），保证 cleaner 可解析传播注入的个股
		for _, m := range sectorMembers() {
			if _, ok := fix.StockList[m.name]; !ok {
				fix.StockList[m.name] = m.code
			}
		}
		log.Printf("fallback: stock_list %d 只", len(fix.StockList))
	}

	// 资金流向：真实代码 + 确定性主力净流入（正/负）
	if len(fix.MoneyFlow) == 0 {
		net := map[string]float64{
			"300750": 8.0, "600519": -5.0, "688981": 3.0, "300308": 2.5,
			"600276": 1.5, "002594": 2.0, "000938": 1.2, "300059": 4.0,
		}
		for _, code := range scenarioStocks {
			n := net[code]
			if n == 0 {
				n = 1.0
			}
			supIn, supOut := n*0.6, n*0.1
			lgIn, lgOut := n*0.5, n*0.2
			mdIn, mdOut := n*0.3, n*0.3
			smIn, smOut := n*0.2, n*0.4
			fix.MoneyFlow[code] = []string{fmt.Sprintf("2026-07-29,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,%.0f,0,0,0",
				supIn*10000, supOut*10000, lgIn*10000, lgOut*10000, mdIn*10000, mdOut*10000, smIn*10000, smOut*10000, n*10000)}
		}
		log.Printf("fallback: money_flow %d 只", len(fix.MoneyFlow))
	}

	// 板块成分股：按同花顺真实代码填充场景板块成员
	if len(fix.SectorStocks) == 0 {
		byName := make(map[string]data.SectorInfo)
		for _, b := range boards {
			byName[b.Name] = b
		}
		for _, sec := range []struct {
			board string
			codes []string
		}{
			{"人工智能", []string{"300308", "000938"}}, // 中际旭创 / 紫光股份
			{"半导体", []string{"688981"}},            // 中芯国际
			{"锂电池概念", []string{"300750"}},          // 宁德时代
			{"化学制药", []string{"600276"}},           // 恒瑞医药
			{"白酒", []string{"600519"}},             // 贵州茅台
			{"汽车整车", []string{"002594"}},           // 比亚迪
		} {
			b, ok := byName[sec.board]
			if !ok {
				log.Printf("fallback: 板块 %q 未在同花顺名单", sec.board)
				continue
			}
			var stocks []data.StockInfo
			for _, code := range sec.codes {
				si := sinaQuotes[code]
				if si == nil {
					continue
				}
				stocks = append(stocks, data.StockInfo{
					Code: code, Name: si.Name, Price: si.Price,
					Open: si.Open, High: si.High, Low: si.Low, Close: si.Close,
					Volume: si.Volume, Amount: si.Amount, ChangePct: si.ChangePct,
				})
			}
			if len(stocks) > 0 {
				fix.SectorStocks[b.Code] = stocks
			}
		}
		log.Printf("fallback: sector_stocks %d 个板块", len(fix.SectorStocks))
	}

	// 个股行业（真实板块归属）
	if len(fix.Industries) == 0 {
		ind := map[string]string{
			"300750": "电池", "600519": "酿酒行业", "688981": "半导体", "300308": "通信设备",
			"600276": "化学制药", "002594": "汽车整车", "000938": "通信设备", "300059": "证券",
		}
		for _, code := range scenarioStocks {
			if v, ok := ind[code]; ok {
				fix.Industries[code] = v
			}
		}
		log.Printf("fallback: industries %d 只", len(fix.Industries))
	}

	if fix.IndexPrice == 0 {
		fix.IndexPrice = 3300.0
		fix.IndexMA20 = 3280.0
		fix.UpCount = 1800
		fix.DownCount = 2800
	}

	// 行情覆盖：确定性涨跌幅驱动场景路由（真实新浪价每天变化，测试需可复现）。
	// 宁德+2.9% 个股利好(涨)；恒瑞-1.5% 利好不涨→预期差；茅台-3.5% 利空兑现。
	overrides := map[string]struct{ close, prev float64 }{
		"300750": {410.00, 398.44},
		"600519": {1650.00, 1710.00},
		"600276": {44.85, 45.53},
		"688981": {56.00, 55.45},
		"300308": {90.00, 86.54},
		"000938": {27.50, 26.70},
		"002594": {240.00, 240.00},
		"300059": {20.00, 20.00},
	}
	if fix.Quotes == nil {
		fix.Quotes = make(map[string]string)
	}
	for _, code := range scenarioStocks {
		o, ok := overrides[code]
		if !ok {
			continue
		}
		name := ""
		if old, ok := fix.Quotes[code]; ok {
			name = strings.SplitN(old, ",", 2)[0]
		}
		if name == "" {
			for _, m := range sectorMembers() {
				if m.code == code {
					name = m.name
					break
				}
			}
		}
		if name == "" {
			continue
		}
		hi, lo := o.prev, o.close
		if o.close > hi {
			hi = o.close
		}
		if o.close < lo {
			lo = o.close
		}
		vol := 3e7
		amt := 1.2e10
		if si, ok := sinaQuotes[code]; ok && si != nil && si.Volume > 0 {
			vol, amt = si.Volume, si.Amount
		}
		fix.Quotes[code] = fmt.Sprintf("%s,%.2f,%.2f,%.2f,%.2f,%.2f,0,0,%.0f,%.0f,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0",
			name, o.prev*1.005, o.prev, o.close, hi*1.005, lo*0.995, vol, amt)
	}
}

// sectorMembers 场景板块成分股的 名称→代码 映射（与 applyFallbacks 内保持一致）。
// stockNameCode 是"股票名称→股票代码"的二元组，用于兜底股票列表/名称映射。
type stockNameCode struct{ name, code string }

// sectorMembers 场景板块成分股的 名称→代码 映射（与 applyFallbacks 内保持一致）。
func sectorMembers() []stockNameCode {
	return []stockNameCode{
		{"中际旭创", "300308"}, {"紫光股份", "000938"}, {"中芯国际", "688981"},
		{"宁德时代", "300750"}, {"恒瑞医药", "600276"}, {"贵州茅台", "600519"}, {"比亚迪", "002594"},
	}
}

// sinaQuoteCSV 将 StockInfo 还原为新浪行情 CSV 字段串（首字段名称）。
func sinaQuoteCSV(si *data.StockInfo) string {
	prev := si.Close
	if prev <= 0 {
		prev = si.Price
	}
	return fmt.Sprintf("%s,%.2f,%.2f,%.2f,%.2f,%.2f,0,0,%.0f,%.0f,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0",
		si.Name, si.Open, prev, si.Price, si.High, si.Low, si.Volume, si.Amount)
}

// authoredNews 场景新闻（固定标题/正文，mock LLM 按标题映射返回精确分析）。
// 返回按数据源分桶：ths 14 条 + cls 6 条 = 20 条（≥20 则新浪兜底源不请求）。
func authoredNews() map[string][]data.NewsItem {
	now := time.Now()
	ts := func(h, m int) string {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
		return t.Format("2006-01-02 15:04:05")
	}
	ths := []data.NewsItem{
		{Title: "工信部等七部门印发《人工智能算力基础设施高质量发展行动计划》", Content: "行动计划提出到2028年建成算力基础设施体系，智算中心规模翻番，利好AI算力产业链。", Datetime: ts(9, 0), Source: "同花顺"},
		{Title: "宁德时代中标沙特10GWh储能系统大单", Content: "据公告，宁德时代子公司中标沙特NEOM新城10GWh储能系统项目，合同金额约120亿元。", Datetime: ts(9, 5), Source: "同花顺"},
		{Title: "贵州茅台三季度营收净利双降 单季净利同比下滑15%", Content: "贵州茅台披露三季报，第三季度营业收入同比下降8%，归母净利润同比下降15%。", Datetime: ts(9, 10), Source: "同花顺"},
		{Title: "国家医保局拟开展第七轮药品集采 创新药面临利空", Content: "国家医保局拟组织第七轮药品集中带量采购，覆盖范围扩大至创新药，业内预计价格将再度下调。", Datetime: ts(9, 15), Source: "同花顺"},
		{Title: "中金公司研报：维持贵州茅台\"跑赢行业\"评级", Content: "中金公司发布研报，维持贵州茅台跑赢行业评级，目标价1800元。", Datetime: ts(9, 20), Source: "同花顺"},
		{Title: "美股三大指数集体收涨 纳指涨0.8%续创历史新高", Content: "隔夜美股三大指数集体收涨，纳斯达克指数涨0.8%续创历史新高，标普500涨0.5%。", Datetime: ts(9, 25), Source: "同花顺"},
		{Title: "恒瑞医药创新药获批上市", Content: "恒瑞医药公告，其自主研发的抗肿瘤创新药获国家药监局批准上市，系国内首款同类产品。", Datetime: ts(9, 30), Source: "同花顺"},
		{Title: "国家统计局发布2026年上半年国民经济运行数据", Content: "初步核算，上半年国内生产总值同比增长5.2%，国民经济运行总体平稳。", Datetime: ts(9, 35), Source: "同花顺"},
		{Title: "交通运输部：上半年铁路货运量同比增长4.2%", Content: "交通运输部数据显示，上半年全国铁路货运量同比增长4.2%，运输结构持续优化。", Datetime: ts(9, 40), Source: "同花顺"},
		{Title: "央行开展3000亿元逆回购操作", Content: "为维护银行体系流动性合理充裕，央行今日开展3000亿元7天期逆回购操作。", Datetime: ts(9, 45), Source: "同花顺"},
		{Title: "国家能源局：前6月全社会用电量同比增长5.8%", Content: "国家能源局发布数据，上半年全社会用电量同比增长5.8%，经济运行延续回升态势。", Datetime: ts(9, 50), Source: "同花顺"},
		{Title: "农业农村部：夏粮再获丰收 总产量再创新高", Content: "农业农村部宣布，今年夏粮再获丰收，总产量再创历史新高，为全年粮食生产奠定基础。", Datetime: ts(9, 55), Source: "同花顺"},
		{Title: "民航局：暑运前十天日均航班量创新高", Content: "民航局数据显示，暑运前十天日均航班量达1.7万班，创历史同期新高。", Datetime: ts(10, 0), Source: "同花顺"},
		{Title: "国家邮政局：上半年快递业务量同比增长22%", Content: "国家邮政局发布数据，上半年快递业务量累计完成850亿件，同比增长22%。", Datetime: ts(10, 5), Source: "同花顺"},
	}
	cls := []data.NewsItem{
		{Title: "突发！大消息！AI算力再迎超级风口", Content: "午后人工智能板块集体走强，算力、光模块方向领涨，多只个股触及涨停。", Datetime: ts(13, 10), Source: "财联社"},
		{Title: "新股纵横股份今日登陆科创板", Content: "纵横股份今日登陆科创板，发行价25.80元，募集资金9.8亿元。", Datetime: ts(13, 20), Source: "财联社"},
		{Title: "国家发改委：支持民营企业参与国家重大科技项目", Content: "国家发改委表示，将进一步放宽市场准入，支持民营企业参与国家重大科技项目。", Datetime: ts(13, 30), Source: "财联社"},
		{Title: "财政部：上半年全国一般公共预算收入同比增长3.1%", Content: "财政部公布数据，上半年全国一般公共预算收入同比增长3.1%。", Datetime: ts(13, 40), Source: "财联社"},
		{Title: "水利部：全国进入主汛期 各地做好防汛准备", Content: "水利部表示，全国已进入主汛期，要求各地做好防汛抗洪准备工作。", Datetime: ts(13, 50), Source: "财联社"},
		{Title: "国家电影局：上半年全国电影票房突破280亿元", Content: "国家电影局数据显示，上半年全国电影票房突破280亿元，观影人次6.5亿。", Datetime: ts(14, 0), Source: "财联社"},
	}
	return map[string][]data.NewsItem{"ths": ths, "cls": cls}
}
