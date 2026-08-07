// 涨停池 / 龙虎榜解析单测：锁定价格单位换算，防止"分/厘/元"误判回归。
package data

import (
	"testing"
)

// TestParseLimitUpPoolPriceUnit 东财涨停池价格字段 p 为"厘"（0.001元），须 ÷1000。
// 回归用例（实测 2026-08-07）：有研新材 600206 实时价 48.17 → 涨停池原始 p=48170。
// 曾误作"分 ÷100"导致信号价格放大 10 倍（前端显示 481.7 而非 48.17）。
func TestParseLimitUpPoolPriceUnit(t *testing.T) {
	body := `{"rc":0,"data":{"pool":[
		{"c":"600206","n":"有研新材","p":48170,"zdp":9.99,"amount":4287778276,"ltsz":12563692485.58,"hs":10.83,"lbc":3,"fbt":100000,"fund":305090470,"zbc":0,"hybk":"半导体","zttj":{"days":3}},
		{"c":"603328","n":"依顿电子","p":10850,"zdp":10.04,"amount":148161205,"ltsz":10833102329.35,"hs":1.38,"lbc":1,"fbt":93050,"fund":126633756,"zbc":0,"hybk":"元件","zttj":{"days":1}},
		{"c":"","n":"空代码应跳过","p":99999}
	]}}`
	stocks, err := parseLimitUpPool([]byte(body))
	if err != nil {
		t.Fatalf("parseLimitUpPool: %v", err)
	}
	if len(stocks) != 2 {
		t.Fatalf("应解析2只(空代码跳过), got %d", len(stocks))
	}
	// 价格：厘 → 元（÷1000），48.17 不能是 481.7
	if stocks[0].Price != 48.17 {
		t.Fatalf("600206 价格=%.2f, want 48.17（曾误÷100=481.7）", stocks[0].Price)
	}
	if stocks[1].Price != 10.85 {
		t.Fatalf("603328 价格=%.2f, want 10.85", stocks[1].Price)
	}
	// 首封时间 fbt HHMMSS → "HH:MM"
	if stocks[0].FirstSeal != "10:00" || stocks[1].FirstSeal != "09:30" {
		t.Fatalf("首封时间错误: %s / %s", stocks[0].FirstSeal, stocks[1].FirstSeal)
	}
	// 封单占比 = fund/ltsz*100
	if got := stocks[0].SealRatio; got <= 0 {
		t.Fatalf("封单占比应>0, got %.4f", got)
	}
	// 连板 / 行业字段透传
	if stocks[0].LianBan != 3 || stocks[0].Industry != "半导体" {
		t.Fatalf("字段透传错误: %+v", stocks[0])
	}
}

// TestParseLimitUpPoolInvalid 非法/空响应不应返回脏数据（错误或空切片）。
func TestParseLimitUpPoolInvalid(t *testing.T) {
	if _, err := parseLimitUpPool([]byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	stocks, err := parseLimitUpPool([]byte(`{"rc":0,"data":{"pool":[]}}`))
	if err != nil {
		t.Fatalf("空池不应报错: %v", err)
	}
	if len(stocks) != 0 {
		t.Fatalf("空池应返回空切片, got %d", len(stocks))
	}
}

// TestSealTime 封板时间格式化边界。
func TestSealTime(t *testing.T) {
	cases := []struct{ in int; want string }{
		{0, ""}, {92500, "09:25"}, {100000, "10:00"}, {113001, "11:30"}, {150500, "15:05"},
	}
	for _, c := range cases {
		if got := sealTime(c.in); got != c.want {
			t.Errorf("sealTime(%d)=%q, want %q", c.in, got, c.want)
		}
	}
}
