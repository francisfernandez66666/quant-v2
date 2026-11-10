// consult_datarich_test.go — §CONSULT-DATARICH（2026-11-10）B 期数据补齐 + A 期补数轮的双向回归。
//
// 双向证明纪律（方案 B④）：放行组每个用例必须证明「新逻辑活、旧逻辑死」（修复真有效），
// 拦截组每个用例必须证明「新旧都拦」（守卫没有退化）。旧逻辑以 consultLegacy* 副本形式
// 钉在本文件里——它不是第二把生产尺子，只是测试对照物；生产审计唯一入口仍是 auditNumbers。
//
// 覆盖面：
//   - B②：万手/亿手（正则+单位表两处）、按写出精度的舍入容差、手↔股换算、同号硬边界；
//   - B①：数据块新字段（量比/距高低/均线偏离/剔今日均量/块尾声明），盘中/盘前/非交易日三态；
//   - A 轮：白名单解析、预算留位、开关、补充段落在 ⟦DATA⟧ 内、fail-open 各失败形态、8s 上限。
//
// English: two-way regression for the consult data-rich batch. Every pass-group case must die
// under the frozen legacy replica (proof the fix works) and every block-group case must die under
// both (proof the guard did not weaken). The legacy replica is a test artifact, never a second
// production ruler.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// ── 旧逻辑副本（2026-11-10 批前形态，逐字冻结，仅作双向对照）──

// legacyAuditedNumberRe 旧正则：没有 万手/亿手，"3.2万手"会被裸"万"截断。
var legacyAuditedNumberRe = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?\s*(?:万元|亿元|元|万股|亿股|万|亿|手|笔|[%％]|倍)`)

// legacyUnitTable 旧单位表：手与笔同在 catCount，没有 万手/亿手。
var legacyUnitTable = []struct {
	suffix string
	cat    numCat
	mult   float64
}{
	{"万元", catMoney, 1e4}, {"亿元", catMoney, 1e8},
	{"万股", catShares, 1e4}, {"亿股", catShares, 1e8},
	{"元", catMoney, 1}, {"万", catAmbiguous, 1e4}, {"亿", catAmbiguous, 1e8},
	{"手", catCount, 1}, {"笔", catCount, 1},
	{"%", catPct, 1}, {"％", catPct, 1}, {"倍", catMult, 1},
}

// legacyParseToken 旧解析：逐字复刻批前 parseAuditedNumberToken。
func legacyParseToken(tok string) (numCat, float64, bool) {
	trimmed := strings.TrimRight(tok[:strings.LastIndexAny(tok, "0123456789.")+1], " ")
	suffix := strings.TrimSpace(tok[len(trimmed):])
	for _, u := range legacyUnitTable {
		if suffix == u.suffix {
			f, err := strconv.ParseFloat(trimmed, 64)
			if err != nil {
				return 0, 0, false
			}
			return u.cat, f * u.mult, true
		}
	}
	return 0, 0, false
}

// legacyCollect 旧白名单采集（来源边界与今天一致，差的只是正则与单位表）。
func legacyCollect(texts ...string) *trustedNumbers {
	t := &trustedNumbers{vals: map[numCat]map[float64]bool{}}
	for _, text := range texts {
		for _, tok := range legacyAuditedNumberRe.FindAllString(text, -1) {
			cat, val, ok := legacyParseToken(tok)
			if !ok {
				continue
			}
			if t.vals[cat] == nil {
				t.vals[cat] = map[float64]bool{}
			}
			t.vals[cat][val] = true
		}
	}
	return t
}

// legacyAllows 旧判定：只有精确命中/裸万亿兼容/反向措辞锚/推算百分比四档，
// 没有舍入容差、没有手↔股换算。
func legacyAllows(t *trustedNumbers, cat numCat, val float64, prefix string) bool {
	if setHit(t.vals[cat], val) {
		return true
	}
	if cat == catAmbiguous {
		for _, set := range t.vals {
			if setHit(set, val) {
				return true
			}
		}
	}
	if val > 0 && setHit(t.vals[cat], -val) && hasReverseAnchor(prefix) {
		return true
	}
	if cat == catPct {
		for _, d := range t.derivedPct {
			if math.Abs(val-d) <= derivedPctTolerance || math.Abs(math.Abs(val)-math.Abs(d)) <= derivedPctTolerance {
				return true
			}
		}
	}
	return false
}

// legacyAudit 旧审计主体：命中保留、否则替换 [数据缺失]。
func legacyAudit(reply string, trusted *trustedNumbers) string {
	if trusted == nil || trusted.isEmpty() {
		return reply
	}
	idx := legacyAuditedNumberRe.FindAllStringIndex(reply, -1)
	if len(idx) == 0 {
		return reply
	}
	var sb strings.Builder
	last := 0
	for _, m := range idx {
		tok := reply[m[0]:m[1]]
		cat, val, ok := legacyParseToken(tok)
		if ok && legacyAllows(trusted, cat, val, reply[:m[0]]) {
			sb.WriteString(reply[last:m[1]])
		} else {
			sb.WriteString(reply[last:m[0]])
			sb.WriteString("[数据缺失]")
		}
		last = m[1]
	}
	sb.WriteString(reply[last:])
	return sb.String()
}

// ── B② 双向证明 ──

// TestDataRichAuditPassGroup 放行组：新逻辑必须保留原文，且同一输入在旧逻辑上必须被抹
// （证明每一条放行都是本批修复挣来的，不是旧逻辑本来就放）。
func TestDataRichAuditPassGroup(t *testing.T) {
	cases := []struct {
		name  string
		data  string
		reply string
		keep  string
	}{
		{"万手舍入", "成交量 4580000股（约45800手）", "今天成交量4.6万手，不算缩量。", "4.6万手"},
		{"亿手舍入", "成交量 12345678900股（约123456789手）", "全天成交1.2亿手。", "1.2亿手"},
		{"价格舍入", "现价 36.13元", "现价36.1元附近震荡。", "36.1元"},
		{"大额舍入", "主力净流入 -22200.00万元", "主力净额-2.2亿元。", "-2.2亿元"},
		{"股换手", "成交量 4580万股", "换算下来是45.8万手。", "45.8万手"},
		{"手换股", "成交量 45800手", "也就是458万股。", "458万股"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			newGot := auditNumbers(c.reply, collectTrustedNumbers(c.data))
			if !strings.Contains(newGot, c.keep) {
				t.Fatalf("新逻辑应放行 %q（合法复述不是编造）, got: %s", c.keep, newGot)
			}
			oldGot := legacyAudit(c.reply, legacyCollect(c.data))
			if strings.Contains(oldGot, c.keep) || !strings.Contains(oldGot, "[数据缺失]") {
				t.Fatalf("双向证明失效：旧逻辑竟也放行 %q ⇒ 本用例证不了修复有效, old: %s", c.keep, oldGot)
			}
		})
	}
}

// TestDataRichAuditBlockGroup 拦截组：真编造/改方向/跨量纲冒充/超出写出精度的"舍入"，
// 新旧逻辑都必须抹掉（守卫不退化）。其中"22.09→23.00"一案专门钉死"按精度取容差"的取向：
// 若有人改回固定 ±5% 相对容差，报错价会被当四舍五入放行，本案在新逻辑上先红。
func TestDataRichAuditBlockGroup(t *testing.T) {
	cases := []struct {
		name  string
		data  string
		reply string
		ban   string
	}{
		{"真编造新值", "现价 36.10元", "主力净流入8888万元。", "8888万元"},
		{"改方向复述", "涨跌幅-5.67%", "今天涨幅5.67%，走势偏强。", "5.67%"},
		{"笔冒充股", "成交 23000笔", "相当于230万股的抛压。", "230万股"},
		{"超精度报错价", "现价 22.09元", "现价23.00元可以考虑。", "23.00元"},
		{"无锚正冒负", "主力净流入 -22200.00万元", "主力净流入2.2亿元。", "2.2亿元"},
		{"编造倍数", "量比 1.85倍", "量比达到3.50倍。", "3.50倍"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			newGot := auditNumbers(c.reply, collectTrustedNumbers(c.data))
			if strings.Contains(newGot, c.ban) {
				t.Fatalf("新逻辑必须拦截 %q（编造/改方向/跨量纲）, got: %s", c.ban, newGot)
			}
			oldGot := legacyAudit(c.reply, legacyCollect(c.data))
			if strings.Contains(oldGot, c.ban) {
				t.Fatalf("旧逻辑也必须拦截 %q（否则本案证不了守卫未退化）, old: %s", c.ban, oldGot)
			}
		})
	}
}

// TestLotsUnitTableAndRegex 万手/亿手必须同时活在正则与单位表两处：
// 只改表不改正则时 token 被裸"万"截断，表里那条后缀永远走不到（空操作）。
func TestLotsUnitTableAndRegex(t *testing.T) {
	for _, tok := range []string{"3.2万手", "1.2亿手", "45.8 万手"} {
		if got := auditedNumberRe.FindString(tok + "的成交量"); got != tok {
			t.Fatalf("正则必须整段吃下组合后缀 %q（不得截成裸万/亿）, got %q", tok, got)
		}
	}
	cases := []struct {
		tok string
		cat numCat
		val float64
	}{
		{"3.2万手", catLots, 32000},
		{"1.2亿手", catLots, 1.2e8},
		{"45800手", catLots, 45800},
		{"23000笔", catCount, 23000}, // 笔仍是笔，不与手同类
		{"4580万股", catShares, 4.58e7},
	}
	for _, c := range cases {
		cat, val, ok := parseAuditedNumberToken(c.tok)
		if !ok || cat != c.cat || !approxEqual(val, c.val) {
			t.Fatalf("parse(%q) = (%v,%v,%v), want (%v,%v,true)", c.tok, cat, val, ok, c.cat, c.val)
		}
	}
}

// TestAuditedTokenGranularity 舍入容差的尺子＝写出的那一位 precision（换算到基础量纲）。
func TestAuditedTokenGranularity(t *testing.T) {
	cases := []struct {
		tok  string
		want float64
	}{
		{"36.1元", 0.1},
		{"22.09元", 0.01},
		{"2.2亿", 1e7},
		{"2.22亿元", 1e6},
		{"45800手", 1},
		{"3.2万手", 1000},
		{"5.67%", 0.01},
		{"1.85倍", 0.01},
	}
	for _, c := range cases {
		if got := auditedTokenGranularity(c.tok); !approxEqual(got, c.want) {
			t.Fatalf("granularity(%q) = %v, want %v", c.tok, got, c.want)
		}
	}
}

// TestRoundHitSignAndZeroBoundaries 舍入档两条硬边界：异号不借容差翻身；零不参与容差。
func TestRoundHitSignAndZeroBoundaries(t *testing.T) {
	set := map[float64]bool{100: true, 0: true}
	if roundHit(set, -100, 1000) {
		t.Fatal("异号不得借容差放行（方向性编造必须仍走反向措辞锚那条保守规则）")
	}
	if roundHit(set, 0.001, 1) {
		t.Fatal("零锚点不得参与容差比对（0 与任何数都'接近'是假放行）")
	}
	if !roundHit(set, 100.4, 0.5) {
		t.Fatal("同号且在容差内应放行")
	}
}

// ── B① 数据块夹具 ──

// dataRichTransport 咨询数据块测试行情源：东财镜像报价+当日资金流、新浪日K（按 datalen
// 生成工作日序列）、push2his 五日资金流历史；其余主机 404（新浪实时/腾讯/分钟K 全落空，
// 报价链自然落到东财镜像，与 consultSourceTransport 同一故障形态）。
type dataRichTransport struct {
	endDay string // 日K最后一根的日期（yyyy-mm-dd；盘中案=今日，周末案=最近交易日）
}

func (tr dataRichTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host, path := req.URL.Hostname(), req.URL.Path
	q := req.URL.Query()
	switch {
	case host == "money.finance.sina.com.cn" && q.Get("scale") == "240":
		n, _ := strconv.Atoi(q.Get("datalen"))
		return consultResp(200, dataRichKLineJSON(n, tr.endDay)), nil
	case host == "push2.eastmoney.com":
		return nil, fmt.Errorf("EOF")
	case host == "push2delay.eastmoney.com" && path == "/api/qt/stock/get":
		if strings.Contains(q.Get("fields"), "f127") {
			return consultResp(200, `{"data":{"f57":"301068","f58":"大地海洋","f127":"环保行业","f128":"浙江板块"}}`), nil
		}
		// f43/f44/f45/f46/f60 为分，f47 为手，f168 换手×100，f170 涨跌幅×100。
		return consultResp(200, `{"data":{"f43":2209,"f44":2388,"f45":2150,"f46":2250,"f60":2236,`+
			`"f47":5000,"f48":11045000,"f57":"301068","f58":"大地海洋","f168":250,"f169":-27,"f170":-121}}`), nil
	case host == "push2delay.eastmoney.com" && path == "/api/qt/stock/fflow/kline/get":
		return consultResp(200, `{"data":{"klines":["2026-11-10 15:00,-100000000,50000000,30000000,-40000000,-60000000"]}}`), nil
	case host == "push2his.eastmoney.com" && path == "/api/qt/stock/fflow/daykline/get":
		return consultResp(200, dataRichFlowHistJSON), nil
	default:
		return consultResp(404, ""), nil
	}
}

// dataRichKLineJSON 生成 n 根工作日升序日K：最后一根量 500000/收 22.09，前5根量 1000000/收 20.00，
// 更早量 2000000。数值刻意取整，让 MA/均量/量比的期望值可以手算钉死。
func dataRichKLineJSON(n int, endDay string) string {
	end, err := time.Parse("2006-01-02", endDay)
	if err != nil || n <= 0 {
		return "[]"
	}
	var dates []time.Time
	d := end
	for len(dates) < n {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			dates = append([]time.Time{d}, dates...)
		}
		d = d.AddDate(0, 0, -1)
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i, day := range dates {
		vol, close, hi, lo, open := 2000000.0, 20.00, 20.50, 19.50, 20.00
		switch {
		case i == len(dates)-1:
			vol, close, hi, lo, open = 500000, 22.09, 23.88, 21.50, 22.50
		case i >= len(dates)-6:
			vol = 1000000
		}
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf(`{"day":%q,"open":"%.2f","high":"%.2f","low":"%.2f","close":"%.2f","volume":"%.0f","amount":"11045000"}`,
			day.Format("2006-01-02"), open, hi, lo, close, vol))
	}
	sb.WriteString("]")
	return sb.String()
}

// dataRichFlowHistJSON push2his 五日历史：行序=日期,主力,小单,中单,大单,超大单（与解析器同口径）。
// 主力净额 ±1e8 交替，合计 +1e8 ⇒ 10000.00万元。
const dataRichFlowHistJSON = `{"data":{"klines":[` +
	`"2026-11-04,100000000,-20000000,-30000000,40000000,60000000",` +
	`"2026-11-05,-100000000,20000000,30000000,-40000000,-60000000",` +
	`"2026-11-06,100000000,-20000000,-30000000,40000000,60000000",` +
	`"2026-11-07,-100000000,20000000,30000000,-40000000,-60000000",` +
	`"2026-11-10,100000000,-20000000,-30000000,40000000,60000000"]}}`

// dataRichBlockEngine 组装一个带注入时钟与行情源的引擎（不接 LLM，只测数据块）。
// endDay＝日K最后一根的日期：盘中/盘前案传今日，非交易日案传最近交易日。
func dataRichBlockEngine(t *testing.T, now time.Time, endDay string) *Engine {
	t.Helper()
	api := data.NewMarketAPI()
	api.SetTransport(dataRichTransport{endDay: endDay})
	e := &Engine{marketAPI: api}
	e.SetClock(func() time.Time { return now })
	return e
}

// cstTime 北京时区时刻构造（夹具统一 +08:00，与 cntime 口径一致）。
func cstTime(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.FixedZone("CST", 8*3600))
}

// TestConsultBlockDataRichIntraday 盘中（周二 10:30，已流逝60分钟）：新字段全部进块且数值可手算复核。
// 量比=500000×(240/60)/1000000=2.00倍；MA5=(20×4+22.09)/5=20.418；MA10=(20×9+22.09)/10=20.209。
func TestConsultBlockDataRichIntraday(t *testing.T) {
	e := dataRichBlockEngine(t, cstTime(2026, time.November, 10, 10, 30), "2026-11-10")
	block := e.buildStockBlockUncached("301068", "大地海洋")
	for _, want := range []string{
		"量比 2.00倍（盘中按已流逝交易分钟折算全天",
		"已流逝60/240交易分钟",
		"从当日最高回落 8.10%",
		"高于当日最低 2.67%",
		"分母=现价",
		"MA5=20.42元 MA10=20.21元",
		"均线偏离: MA5 +8.19%",
		"MA10 +9.31%",
		"分母=均线值、参考价22.09元",
		"前5个交易日平均成交量 1000000股（约10000手，不含今日）",
		"本块未提供：北向资金、盘口五档、同板块个股、集合竞价明细、龙虎榜、融资余额",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("盘中数据块缺 %q\n--- 块内容 ---\n%s", want, block)
		}
	}
	// 日K 收/高/低必须带元（无单位数不进白名单，模型规范复述会被误杀）。
	if !strings.Contains(block, "收22.09元 高23.88元 低21.50元") {
		t.Errorf("日K收高低必须带元单位\n%s", block)
	}
	// 旧口径"近5日平均成交量"（含今日半根）不得复活。
	if strings.Contains(block, "近5日平均成交量") {
		t.Errorf("含今日半根的旧均量口径不得复活\n%s", block)
	}
}

// TestConsultBlockDataRichPreOpen 盘前（交易日 09:00）：量比必须写"未提供"——
// IntradayVolumeRatio 内部把 elapsed<=0 兜成1分钟，直接调会把集合竞价量虚抬240倍。
// 同时"数据源未返回"是净流入真0守卫的独占断言词，新字段缺数一律用"未提供"，不得复用。
func TestConsultBlockDataRichPreOpen(t *testing.T) {
	e := dataRichBlockEngine(t, cstTime(2026, time.November, 10, 9, 0), "2026-11-10")
	block := e.buildStockBlockUncached("301068", "大地海洋")
	if !strings.Contains(block, "量比: 未提供（盘前无盘中累计量口径") {
		t.Errorf("盘前量比必须写未提供\n%s", block)
	}
	if strings.Contains(block, "量比 ") && strings.Contains(block, "倍（盘中") {
		t.Errorf("盘前不得输出盘中折算量比\n%s", block)
	}
	if strings.Contains(block, "数据源未返回") {
		t.Errorf("新字段缺数措辞不得复用净流入守卫的独占断言词\n%s", block)
	}
	// 均量剔今日逻辑不受盘前影响（今日根仍是未收盘半根）。
	if !strings.Contains(block, "前5个交易日平均成交量 1000000股") {
		t.Errorf("盘前均量仍应剔今日半根\n%s", block)
	}
}

// TestConsultBlockDataRichNonTradingDay 非交易日（周六 10:30）：报价是最近收盘口径，
// 量比必须按最近交易日（周五）收盘全日口径=500000/1000000=0.50倍，分母剔掉周五自身；
// 不得按周六时钟凭空折出"已流逝60分钟"虚抬4倍（本批补上的缺口，旧形态在此必红）。
func TestConsultBlockDataRichNonTradingDay(t *testing.T) {
	e := dataRichBlockEngine(t, cstTime(2026, time.November, 14, 10, 30), "2026-11-13") // 最后一根=周五
	block := e.buildStockBlockUncached("301068", "大地海洋")
	if !strings.Contains(block, "量比 0.50倍（非交易日，按最近交易日 2026-11-13 收盘全日口径") {
		t.Errorf("非交易日量比必须按最近交易日收盘全日口径\n%s", block)
	}
	if strings.Contains(block, "盘中按已流逝交易分钟折算") {
		t.Errorf("非交易日不得打盘中折算口径\n%s", block)
	}
	if !strings.Contains(block, "约10000手，不含最近交易日") {
		t.Errorf("非交易日均量分母必须剔除最近交易日（分子即该日量）\n%s", block)
	}
}

// TestConsultAvgVolumeExToday 剔基准日判据表：同日剔、异日不剔、不足取全部、剔空回0。
func TestConsultAvgVolumeExToday(t *testing.T) {
	bar := func(day string, vol float64) data.KLine {
		// 与 parseSinaKLine 同一时区（北京 +08:00）：UTC 午夜造夹具会让 DayOf 跨日错位。
		d, _ := time.ParseInLocation("2006-01-02", day, time.FixedZone("CST", 8*3600))
		return data.KLine{Date: d, Volume: vol}
	}
	kl := []data.KLine{
		bar("2026-11-03", 1000000), bar("2026-11-04", 1000000), bar("2026-11-05", 1000000),
		bar("2026-11-06", 1000000), bar("2026-11-09", 1000000), bar("2026-11-10", 500000),
	}
	// 周末形态：最后一根是周五（生产侧此时把基准日传成 last.Date，而不是周六的 now）。
	klFri := []data.KLine{
		bar("2026-11-05", 1000000), bar("2026-11-06", 1000000), bar("2026-11-09", 1000000),
		bar("2026-11-10", 1000000), bar("2026-11-11", 1000000), bar("2026-11-13", 500000),
	}
	cases := []struct {
		name string
		kl   []data.KLine
		now  time.Time
		want float64
	}{
		{"盘中剔今日半根", kl, cstTime(2026, time.November, 10, 10, 30), 1000000},
		{"基准日=最后一根必剔（周末传last.Date）", klFri, cstTime(2026, time.November, 13, 15, 0), 1000000},
		{"盘前最后一根是昨日不剔", kl[:5], cstTime(2026, time.November, 10, 9, 0), 1000000},
		{"不足5根取全部（剔后）", kl[4:], cstTime(2026, time.November, 10, 10, 30), 1000000},
		{"剔完为空回0", kl[5:], cstTime(2026, time.November, 10, 10, 30), 0},
		{"空序列回0", nil, cstTime(2026, time.November, 10, 10, 30), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := consultAvgVolumeExToday(c.kl, c.now); got != c.want {
				t.Fatalf("consultAvgVolumeExToday = %v, want %v", got, c.want)
			}
		})
	}
}

// ── A 轮：解析与预算 ──

// TestParseConsultDataRoundKeys 严格 JSON＋白名单＋去重＋上限；解出一律 nil（不猜意图）。
func TestParseConsultDataRoundKeys(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  []string
	}{
		{"合法双键", `["moneyflow_5d","kline_60d"]`, []string{"moneyflow_5d", "kline_60d"}},
		{"围栏+解释", "好的，需要如下字段：\n```json\n[\"kline_60d\"]\n```", []string{"kline_60d"}},
		{"白名单外丢弃", `["north_flow","industry_peers","kline_60d","龙虎榜"]`, []string{"kline_60d"}},
		{"去重", `["kline_60d","kline_60d"]`, []string{"kline_60d"}},
		{"空数组", `[]`, nil},
		{"纯幻觉键", `["龙虎榜","融资余额"]`, nil},
		{"混对象拒解", `[{"key":"kline_60d"}]`, nil},
		{"混数字拒解", `[1,2]`, nil},
		{"无括号", "不需要补充数据", nil},
		{"坏JSON", `["kline_60d",]`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseConsultDataRoundKeys(c.reply)
			if len(got) != len(c.want) {
				t.Fatalf("parse(%q) = %v, want %v", c.reply, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("parse(%q) = %v, want %v", c.reply, got, c.want)
				}
			}
		})
	}
	// 上限：临时把白名单扩到 8 项再喂 8 个合法键，证明截断真的在 5 项生效
	// （当前生产白名单只有2项，不扩表这条腿永远是空转）。跑完原样恢复。
	saved := consultSupplementFields
	defer func() { consultSupplementFields = saved }()
	for i := 0; i < 8; i++ {
		consultSupplementFields = append(consultSupplementFields, struct {
			key  string
			desc string
		}{fmt.Sprintf("fake_%d", i), "测试临时键"})
	}
	var eight []string
	for i := 0; i < 8; i++ {
		eight = append(eight, fmt.Sprintf("fake_%d", i))
	}
	raw, _ := json.Marshal(eight)
	if got := parseConsultDataRoundKeys(string(raw)); len(got) != consultDataRoundMaxFields {
		t.Fatalf("单轮补取必须截断到 %d 项, got %d", consultDataRoundMaxFields, len(got))
	}
}

// TestConsultRoundBudgetRoom 预算留位：咨询/总调用任一剩余不足2次即跳过；0=不设限放行。
func TestConsultRoundBudgetRoom(t *testing.T) {
	cases := []struct {
		name    string
		consult int64
		total   int64
		want    bool
	}{
		{"都不设限", 0, 0, true},
		{"咨询只够一次", 1, 0, false},
		{"咨询够两次", 2, 0, true},
		{"总预算只够一次", 0, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := llm.New(llm.Config{APIKey: "k", ConsultDailyCalls: c.consult, DailyCallBudget: c.total})
			if got := consultRoundBudgetRoom(client); got != c.want {
				t.Fatalf("budgetRoom(consult=%d,total=%d) = %v, want %v", c.consult, c.total, got, c.want)
			}
		})
	}
}

// TestConsultDataRoundTimeoutIsBounded A 轮超时必须钉在 8s：咨询入口是同步一次性返回，
// 这段时间是用户净多等；改回方案初稿 15s 会把一次问答推到半分钟级，本腿先红。
func TestConsultDataRoundTimeoutIsBounded(t *testing.T) {
	if consultDataRoundTimeout != 8*time.Second {
		t.Fatalf("A 轮超时上限必须是 8s, got %v", consultDataRoundTimeout)
	}
}

// ── A 轮：ConsultLLM 全链路（mock LLM 区分补数轮与主回答）──

// dataRoundLLM 记录两类调用并按请求内容分派回复。A 轮 system 含"数据补齐助手"（唯一提示词出口生成）。
type dataRoundLLM struct {
	mu            sync.Mutex
	supplement    string // A 轮回复
	main          string // 主回答回复
	supplementN   int
	mainN         int
	lastSystem    string // 主回答的 system（断言补充段位置用）
	supplement500 bool   // A 轮上游故障形态
}

func (m *dataRoundLLM) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			w.WriteHeader(400)
			return
		}
		sys := req.Messages[0].Content
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply := m.main
		if strings.Contains(sys, "数据补齐助手") {
			m.supplementN++
			if m.supplement500 {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"mock upstream down"}`))
				return
			}
			reply = m.supplement
		} else {
			m.mainN++
			m.lastSystem = sys
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": reply},
			}},
		})
	}
}

// dataRoundEngine 组装全链路引擎：行情源（日K/资金流历史）+注入时钟+配置管理器+账号隔离目录。
// 主数据块走 consultBlockLoad 测试缝固定文本，隔离行情变量；A 轮补数走真 transport。
func dataRoundEngine(t *testing.T, mock *dataRoundLLM, budget int64) (*Engine, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock.handler(t))
	t.Cleanup(srv.Close)
	api := data.NewMarketAPI()
	api.SetTransport(dataRichTransport{endDay: "2026-11-10"})
	api.SetFlowHistCacheDir(t.TempDir()) // 快照落盘隔离，绝不污染仓库
	e := &Engine{marketAPI: api}
	e.SetAccountsRoot(t.TempDir())
	e.SetClock(func() time.Time { return cstTime(2026, time.November, 10, 10, 30) })
	e.SetCfgMgr(config.NewManager(t.TempDir() + "/config.json"))
	e.consultBlockLoad = func(code, name string) string {
		return "\n—— 股票 " + code + " ——\n现价 22.09元 昨收 22.36元\n"
	}
	e.llmClient = llm.New(llm.Config{APIKey: "k", APIURL: srv.URL, Model: "m", Streaming: false, ConsultDailyCalls: budget})
	return e, srv
}

// TestConsultDataRoundSupplementInsideDataBoundary A 轮要 kline_60d ⇒ 补充段必须出现，
// 且落在 ⟦DATA⟧/⟦/DATA⟧ 边界之内（白名单自动覆盖，审计零改动的接线前提）。
func TestConsultDataRoundSupplementInsideDataBoundary(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["kline_60d"]`, main: "这票今天冲高回落，建议观察。"}
	e, _ := dataRoundEngine(t, mock, 0)
	reply, err := e.ConsultLLM(context.Background(), "u_dr1", "301068 中期趋势怎么看", false)
	if err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	if reply != mock.main {
		t.Fatalf("主回答应原样交付（无数字可审）, got: %s", reply)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.supplementN != 1 || mock.mainN != 1 {
		t.Fatalf("调用计数应为 A轮1+主回答1, got %d/%d", mock.supplementN, mock.mainN)
	}
	sys := mock.lastSystem
	iData := strings.Index(sys, "⟦DATA⟧")
	iSupp := strings.Index(sys, "—— 补充数据")
	iEnd := strings.Index(sys, "⟦/DATA⟧")
	if iData < 0 || iSupp < 0 || iEnd < 0 || !(iData < iSupp && iSupp < iEnd) {
		t.Fatalf("补充段必须在 ⟦DATA⟧ 边界内: data=%d supp=%d end=%d\n%s", iData, iSupp, iEnd, sys)
	}
	// 60根日K收盘：59根20.00＋最后一根22.09 ⇒ MA20=(20×19+22.09)/20=20.1045→20.10；
	// MA60=(20×59+22.09)/60=20.0348→20.03。
	if !strings.Contains(sys, "MA20=20.10元") || !strings.Contains(sys, "MA60=20.03元") {
		t.Fatalf("补充段应含 MA20/MA60 摘要（不注入原始K线）\n%s", sys)
	}
	if !strings.Contains(sys, "近60日最高23.88元 最低19.50元") {
		t.Fatalf("补充段应含窗口高低\n%s", sys)
	}
}

// TestConsultDataRoundSwitchOff 开关显式关 ⇒ A 轮一次都不拨，主流程字节级回到今天。
func TestConsultDataRoundSwitchOff(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["kline_60d"]`, main: "主回答照旧。"}
	e, _ := dataRoundEngine(t, mock, 0)
	off := false
	rules := e.cfgMgr.Get()
	rules.Runtime.ConsultDataRound = &off
	if err := e.cfgMgr.SetRuntimeConfig(&rules.Runtime); err != nil {
		t.Fatalf("SetRuntimeConfig: %v", err)
	}
	if _, err := e.ConsultLLM(context.Background(), "u_dr2", "301068 怎么看", false); err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.supplementN != 0 || mock.mainN != 1 {
		t.Fatalf("开关关闭时 A 轮必须零调用, got %d/%d", mock.supplementN, mock.mainN)
	}
	if strings.Contains(mock.lastSystem, "补充数据") {
		t.Fatal("开关关闭时 system 不得出现补充段")
	}
}

// TestConsultDataRoundBudgetGuard 咨询预算只够一次 ⇒ A 轮整轮跳过，额度留给主回答：
// 主回答必须成功（若 A 轮抢吃额度，这里会撞 ErrBudgetExceeded 回 429，"只增不减"破产）。
func TestConsultDataRoundBudgetGuard(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["kline_60d"]`, main: "预算紧张时主回答仍须交付。"}
	e, _ := dataRoundEngine(t, mock, 1)
	reply, err := e.ConsultLLM(context.Background(), "u_dr3", "301068 怎么看", false)
	if err != nil {
		t.Fatalf("预算只够一次时主回答不得被 A 轮挤成 429: %v", err)
	}
	if reply != mock.main {
		t.Fatalf("主回答应交付, got: %s", reply)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.supplementN != 0 || mock.mainN != 1 {
		t.Fatalf("预算留位失败: A轮=%d 主=%d（应 0/1）", mock.supplementN, mock.mainN)
	}
}

// TestConsultDataRoundFailOpenBadJSON A 轮回复解不出 ⇒ 静默跳过，主流程不受影响。
func TestConsultDataRoundFailOpenBadJSON(t *testing.T) {
	mock := &dataRoundLLM{supplement: "我觉得需要龙虎榜数据，但说不清格式。", main: "主回答不受解析失败影响。"}
	e, _ := dataRoundEngine(t, mock, 0)
	reply, err := e.ConsultLLM(context.Background(), "u_dr4", "301068 怎么看", false)
	if err != nil || reply != mock.main {
		t.Fatalf("fail-open(坏JSON): reply=%q err=%v", reply, err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if strings.Contains(mock.lastSystem, "补充数据") {
		t.Fatal("解不出时不得写空补充段")
	}
}

// TestConsultDataRoundFailOpenUpstream A 轮上游 500 ⇒ 静默跳过，主回答照常。
func TestConsultDataRoundFailOpenUpstream(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["kline_60d"]`, supplement500: true, main: "上游故障不牵连主回答。"}
	e, _ := dataRoundEngine(t, mock, 0)
	reply, err := e.ConsultLLM(context.Background(), "u_dr5", "301068 怎么看", false)
	if err != nil || reply != mock.main {
		t.Fatalf("fail-open(上游500): reply=%q err=%v", reply, err)
	}
}

// TestConsultDataRoundMoneyFlowPositive 协调器在位且历史可得 ⇒ 逐日带日期输出＋合计。
func TestConsultDataRoundMoneyFlowPositive(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["moneyflow_5d"]`, main: "资金趋势已补充。"}
	e, _ := dataRoundEngine(t, mock, 0)
	api := data.NewMarketAPI()
	api.SetTransport(dataRichTransport{endDay: "2026-11-10"})
	api.SetFlowHistCacheDir(t.TempDir())
	e.SetCoordinator(data.NewDataCoordinator(api, nil), false)
	if _, err := e.ConsultLLM(context.Background(), "u_dr6", "301068 主力连续几天流入", false); err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	sys := mock.lastSystem
	for _, want := range []string{
		"近5个交易日主力净流入",
		"2026-11-04 10000.00万元",
		"2026-11-05 -10000.00万元",
		"合计 10000.00万元",
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("资金流历史补充段缺 %q\n%s", want, sys)
		}
	}
}

// TestConsultDataRoundMoneyFlowAbstain 协调器未注入 ⇒ 弃权：要了也不写空段、不输出0冒充实测。
func TestConsultDataRoundMoneyFlowAbstain(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["moneyflow_5d"]`, main: "没有协调器就弃权。"}
	e, _ := dataRoundEngine(t, mock, 0) // 刻意不 SetCoordinator
	if _, err := e.ConsultLLM(context.Background(), "u_dr7", "301068 主力连续几天流入", false); err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if strings.Contains(mock.lastSystem, "补充数据") || strings.Contains(mock.lastSystem, "主力净流入（东财") {
		t.Fatal("取不到必须整段不写（空段会让模型以为补过了但还是没数）")
	}
}

// TestConsultDataRoundNoStockSkipped 消息里没有个股 ⇒ A 轮零调用（没有可补的对象）。
func TestConsultDataRoundNoStockSkipped(t *testing.T) {
	mock := &dataRoundLLM{supplement: `["kline_60d"]`, main: "今天大盘怎么走？"}
	e, _ := dataRoundEngine(t, mock, 0)
	if _, err := e.ConsultLLM(context.Background(), "u_dr8", "今天大盘怎么走", false); err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.supplementN != 0 {
		t.Fatalf("无个股时 A 轮必须零调用, got %d", mock.supplementN)
	}
}
