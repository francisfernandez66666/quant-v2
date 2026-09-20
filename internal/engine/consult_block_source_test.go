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
		// 行情：f43/f44/f45/f46/f60 为分，f47 为手，f168 换手率×100，f170 涨跌幅×100，f62 主力净流入(元)。
		return consultResp(200, `{"data":{"f43":22462,"f44":23180,"f45":21502,"f46":22700,"f60":22360,`+
			`"f47":161198,"f48":3594643946,"f57":"300489","f58":"光智科技","f62":2,`+
			`"f168":1170,"f169":102,"f170":46}}`), nil
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
	if !strings.Contains(block, "主力净流入 0.00万元") {
		t.Errorf("主力净流入应走有数分支\n--- 块内容 ---\n%s", block)
	}
}
