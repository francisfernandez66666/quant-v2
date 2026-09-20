// 本文件：咨询数据块的行情源接缝测试。
// §修复 20260920 回归锁定：东财 push2 主域名不可达（实测 TLS EOF）时，数据块仍须给出
// 换手率与所属行业——此前正是这两个字段缺失，导致模型口中的真实换手率被反幻觉审计
// (auditNumbers) 替换成 [数据缺失]，且行业只能凭模型记忆编造（光智科技被答成"智能驾驶、光伏"）。
// English: source-seam test for the consult data block. Locks the 2026-09-20 fix: with the
// EastMoney push2 primary host unreachable (measured TLS EOF), the block must still carry the
// turnover rate and the industry. Their absence was exactly why the model's real turnover figure
// got replaced by a missing-data marker and why the industry could only be guessed.
package engine

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
)

// consultSourceTransport 复现生产故障形态：push2 主域名不可达，镜像域名正常服务。
// push2.eastmoney.com → EOF（2026-09-20 实测：TCP 可建连、TLS 握手被重置）。
// push2delay.eastmoney.com → 按 fields 区分 stock/get 的两种用途（行情 / 行业）。
// English: reproduces the production failure — the push2 primary host is unreachable while the
// mirror serves stock/get, distinguished by the requested fields (quote vs industry).
type consultSourceTransport struct{}

func (consultSourceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == "push2.eastmoney.com" {
		return nil, errors.New("EOF")
	}
	if req.URL.Hostname() == "push2delay.eastmoney.com" && req.URL.Path == "/api/qt/stock/get" {
		if strings.Contains(req.URL.Query().Get("fields"), "f127") {
			// 行业 f127 / 地域板块 f128（值取自 2026-09-20 光智科技实测）。
			return consultResp(200, `{"data":{"f57":"300489","f58":"光智科技","f127":"光学光电子","f128":"浙江板块"}}`), nil
		}
		// 行情：f43/f44/f45/f46/f60 为分，f47 为手，f168 换手率×100，f170 涨跌幅×100。
		// 注意：不提供 f62 —— §修复 EM-FFLOW(20260920) 后行情接口的 f62 已不是资金流来源
		// （镜像上它恒为占位值 2），资金流统一走下面的 fflow 分支。
		return consultResp(200, `{"data":{"f43":22462,"f44":23180,"f45":21502,"f46":22700,"f60":22360,`+
			`"f47":161198,"f48":3594643946,"f57":"300489","f58":"光智科技",`+
			`"f168":1170,"f169":102,"f170":46}}`), nil
	}
	// 个股资金流 fflow：真实行宽 6 列 = 日期,主力净额,小单净额,中单净额,大单净额,超大单净额。
	// 取值自洽：大净(-7400万) + 超大净(-14800万) = 主力净(-22200万)，与 e2e 夹具同口径。
	if req.URL.Hostname() == "push2delay.eastmoney.com" && req.URL.Path == "/api/qt/stock/fflow/kline/get" {
		return consultResp(200, `{"data":{"klines":["2026-09-18 15:00,-222000000,130000000,92000000,-74000000,-148000000"]}}`), nil
	}
	return consultResp(404, ""), nil
}

// consultResp 构造一个 mock HTTP 响应。
func consultResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Status:     http.StatusText(code),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// TestConsultBlockCarriesTurnoverAndIndustryOnPrimaryOutage 主域名不可达时，咨询数据块必须
// 同时含"换手率 11.70%"与"所属行业: 光学光电子"（值须为真实行业，不得是地域板块"浙江板块"）。
// English: with the primary host down the block must contain both the turnover rate and the
// industry — and the industry must be the real one, not the region board.
func TestConsultBlockCarriesTurnoverAndIndustryOnPrimaryOutage(t *testing.T) {
	api := data.NewMarketAPI()
	api.SetTransport(consultSourceTransport{})
	e := &Engine{marketAPI: api}

	block := e.buildStockBlockUncached("300489", "光智科技")

	// 换手率必须进块：反幻觉审计白名单由本块构建，块内没有的数会被判编造替换。
	if !strings.Contains(block, "换手率 11.70%") {
		t.Errorf("数据块缺少换手率 11.70%%（主域名不可达时应经镜像取得）\n--- 块内容 ---\n%s", block)
	}
	// 行业必须进块，且为行业(f127)而非地域板块(f128)。
	if !strings.Contains(block, "所属行业: 光学光电子") {
		t.Errorf("数据块缺少所属行业 光学光电子\n--- 块内容 ---\n%s", block)
	}
	if strings.Contains(block, "浙江板块") {
		t.Errorf("数据块混入地域板块 f128（应只给行业 f127）\n--- 块内容 ---\n%s", block)
	}
	// 成交量/成交额是同一批缺失字段，一并锁定。
	if !strings.Contains(block, "成交量 16119800股") || !strings.Contains(block, "成交额3594643946元") {
		t.Errorf("数据块缺少成交量/成交额\n--- 块内容 ---\n%s", block)
	}
	// 主力净流入有值时必须走"有数"分支（HasFlow=true），而非"数据源未返回"。
	// §修复 EM-FFLOW(20260920)：值来自 fflow（东财 fflow 是资金流口径基准），不是行情接口的 f62。
	if !strings.Contains(block, "主力净流入 -22200.00万元") {
		t.Errorf("主力净流入应走有数分支且为 fflow 值 -22200.00万元\n--- 块内容 ---\n%s", block)
	}
	// 资金明细必须带**净额**（东财 fflow 只给净额，In/Out 恒为 0，按 In−Out 算会全 0）。
	if !strings.Contains(block, "超大单净流入-14800万") || !strings.Contains(block, "大单净流入-7400万") {
		t.Errorf("资金明细应输出净额 -14800万/-7400万\n--- 块内容 ---\n%s", block)
	}
}

// consultTHSPrimaryTransport 复现用户裁决后的目标形态：**东财整条链全挂、同花顺可用**。
// 东财两个域名一律 EOF；新浪/腾讯 404；只有同花顺 realhead 服务。
// English: the post-decision target shape — the whole EastMoney chain is down while THS serves.
type consultTHSPrimaryTransport struct{}

func (consultTHSPrimaryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Hostname() {
	case "d.10jqka.com.cn":
		// 线上实测报文形状（扁平字段 id 字典，值为字符串）。换手率 11.696%。
		return consultResp(200, `quotebridge_v2_realhead_hs_300489_last({"items":{"5":"300489",`+
			`"6":"223.60","7":"227.00","8":"231.80","9":"215.02","10":"224.62",`+
			`"13":"16119800","19":"3594643900.00","199112":"0.46","1968584":"11.696",`+
			`"name":"\u5149\u667a\u79d1\u6280"}})`), nil
	case "push2.eastmoney.com", "push2delay.eastmoney.com":
		return nil, errors.New("EOF")
	default:
		return consultResp(404, ""), nil
	}
}

// TestConsultBlockPrefersTHSWithEastMoneyDown 东财全挂时，咨询数据块仍须带同花顺的换手率与价量。
// §QUOTE-CHAIN(20260920) 用户裁决「东财不行就用同花顺，东财兜底」的回归锁定：
// 改之前（东财优先链）这条路上会退到新浪/腾讯 → 换手率恒 0.00% → 模型只能答"数据缺失"。
// 本测试与上一个测试构成双向覆盖：上一个锁"同花顺挂→东财镜像兜底"，本测试锁"东财挂→同花顺首选"。
// English: with EastMoney fully down the block must still carry THS's turnover and price/volume.
// This is the mirror image of the test above and locks the "THS primary, EastMoney fallback" decision.
func TestConsultBlockPrefersTHSWithEastMoneyDown(t *testing.T) {
	api := data.NewMarketAPI()
	rt := consultTHSPrimaryTransport{}
	api.SetTransport(rt)
	ths := data.NewTHSClient()
	ths.SetTransport(rt)
	api.SetTHSClient(ths)
	e := &Engine{marketAPI: api}

	// 先直接锁数据源选择：必须取到同花顺的换手率（新浪/腾讯都无此列）。
	q, err := api.GetRealtimeQuoteWithFlow("300489")
	if err != nil || q == nil {
		t.Fatalf("GetRealtimeQuoteWithFlow 应成功: %v", err)
	}
	if q.Price != 224.62 {
		t.Errorf("现价应取同花顺 224.62, got %.2f", q.Price)
	}
	if q.Turnover != 11.696 {
		t.Errorf("换手率应取同花顺 11.696%%, got %.3f%%（东财挂时旧链会退成 0）", q.Turnover)
	}
	if q.Name != "光智科技" {
		t.Errorf("名称应=光智科技, got %q", q.Name)
	}

	// 再锁数据块：换手率/价量必须进块，否则 auditNumbers 会把模型的真实复述判成编造。
	block := e.buildStockBlockUncached("300489", "光智科技")
	if !strings.Contains(block, "换手率 11.70%") {
		t.Errorf("数据块缺少同花顺换手率 11.70%%\n--- 块内容 ---\n%s", block)
	}
	if !strings.Contains(block, "现价 224.62元") {
		t.Errorf("数据块缺少同花顺现价 224.62元\n--- 块内容 ---\n%s", block)
	}
	if !strings.Contains(block, "成交量 16119800股") || !strings.Contains(block, "成交额3594643900元") {
		t.Errorf("数据块缺少同花顺成交量/成交额\n--- 块内容 ---\n%s", block)
	}
	// 东财全挂且无 hithink key → 净流入必须老实报缺数，不得编造 0。
	if !strings.Contains(block, "数据源未返回") {
		t.Errorf("净流入拿不到时应报缺数\n--- 块内容 ---\n%s", block)
	}
}
