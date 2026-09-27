// 文件：w4b_breadth_abstain_test.go
// 职责：§0926E2E-14（FIX_PLAN_20260926E2E 四波 14 项）GetIndexData 涨跌家数去伪造。
//
// 旧行为：东财概况子请求（fflow/kline f62/f63）失败或返回非正时，GetIndexData 把涨跌
// 家数伪造成 1500/1500 中性值随 err==nil 一起放行——"取数失败"被包装成"涨跌各半"的
// 假实测，历史上正是为此另立了真实弃权语义的 GetBreadth（BREADTH-FAKE 20260920）。
// 新行为：失败/非正一律 0/0 弃权；成功返回真实 f62/f63；指数主体链路语义不变。
//
// 三把锁：
//
//	T1 概况子请求失败 → err==nil（指数腿成功）且 up/down 恒 0/0（绝不冒出 1500）；
//	T2 概况子请求成功 → 真实 f62/f63 原样透传；
//	T3 源码静态负锁 → getEastMoneyIndexData 函数体内不得再出现 "1500"（防伪造默认回潮）。
package data

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// w4Dispatch 按 URL 路径分派响应体的传输层：指数主请求恒回有效 f43，
// fflow/kline（涨跌家数概况腿）回用例给定的载荷，其余请求回空 JSON。
func w4Dispatch(fflow string) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.Path, "/api/qt/stock/get"):
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"f43":312500}}`))}, nil
		case strings.Contains(req.URL.Path, "fflow/kline"):
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fflow))}, nil
		default:
			// K线等其余子请求：给空 JSON，MA20 腿自然跳过，不影响涨跌断言。
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
	})
}

// T1：概况腿失败 → 0/0 真弃权（旧伪造实现此处必回 1500/1500，断言当场红）。
func TestW4BGetIndexDataBreadthAbstainsOnFailure(t *testing.T) {
	DisableAll = true
	defer func() { DisableAll = false }()

	api := NewMarketAPI()
	// fflow 请求返回 200 但载荷无 f62/f63（东财降级页/字段缺失形态）：解析成功、值非正 → 弃权
	api.SetTransport(w4Dispatch(`{"data":{}}`))
	idx, _, up, down, err := api.GetIndexData()
	if err != nil || idx != 3125.0 {
		t.Fatalf("指数腿应成功：idx=%v err=%v", idx, err)
	}
	if up != 0 || down != 0 {
		t.Fatalf("§0926E2E-14 概况无有效 f62/f63 时必须弃权 0/0，得到 %d/%d", up, down)
	}

	// HTTP 层直接失败形态同样弃权（且不得被 LKG 兜底成上一份——本实例从未成功，indexLKG 为空）
	api2 := NewMarketAPI()
	api2.SetTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "fflow/kline") {
			// 概况腿 HTTP 层直接失败（模拟东财概况接口断链）
			return nil, errors.New("dial tcp: 概况腿断链")
		}
		if strings.Contains(req.URL.Path, "/api/qt/stock/get") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"f43":312500}}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}))
	idx2, _, up2, down2, err2 := api2.GetIndexData()
	if err2 != nil || idx2 != 3125.0 || up2 != 0 || down2 != 0 {
		t.Fatalf("§0926E2E-14 概况 HTTP 失败应指数成功+涨跌弃权，得到 idx=%v up=%d down=%d err=%v", idx2, up2, down2, err2)
	}
}

// T2：概况腿有效 → 真实 f62/f63 原样透传（去伪造不是去数据）。
func TestW4BGetIndexDataBreadthRealValues(t *testing.T) {
	DisableAll = true
	defer func() { DisableAll = false }()

	api := NewMarketAPI()
	api.SetTransport(w4Dispatch(`{"data":{"f62":2113,"f63":907}}`))
	idx, _, up, down, err := api.GetIndexData()
	if err != nil || idx != 3125.0 {
		t.Fatalf("指数腿应成功：idx=%v err=%v", idx, err)
	}
	if up != 2113 || down != 907 {
		t.Fatalf("真实 f62/f63 应原样透传，得到 %d/%d", up, down)
	}
}

// T3：静态负锁——getEastMoneyIndexData 函数体内不得再出现字面量 1500（伪造默认防回潮）。
// 只看真实代码：先剥行注释，避免说明性注释（本批修复注释会引用旧值）造成假红。
func TestW4BNoFakeBreadthLiteralInSource(t *testing.T) {
	srcBytes, err := os.ReadFile("market.go") // 测试工作目录＝包目录
	if err != nil {
		t.Fatalf("读 market.go 失败: %v", err)
	}
	src := string(srcBytes)
	start := strings.Index(src, "func (m *MarketAPI) getEastMoneyIndexData(")
	end := strings.Index(src[start:], "\nfunc ")
	if start < 0 || end < 0 {
		t.Fatal("定位 getEastMoneyIndexData 函数体失败（函数改名须同步本锁）")
	}
	body := src[start : start+end]
	// 剥 // 行注释后找 "1500"
	var code strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	if strings.Contains(code.String(), "1500") {
		t.Fatal("§0926E2E-14：getEastMoneyIndexData 真实代码里再现 1500 字面量（伪造涨跌家数默认回潮）")
	}
}
