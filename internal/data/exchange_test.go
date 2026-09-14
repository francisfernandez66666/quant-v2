// exchange_test.go §P1-6（2026-09-15）交易所后缀分类回归：920xxx 北交所段不再被 `9` 前缀
// 误判 .SH（历史三处实现互相矛盾：engine/server 把 920 发错交易所，LimitUpPct 却按北交所
// 30% 涨跌幅计算）。逐段断言唯一口径。
package data

import "testing"

func TestExchangeSuffixSegments(t *testing.T) {
	cases := map[string]string{
		"600519": "600519.SH", // 沪主板
		"900901": "900901.SH", // 沪 B 保留 9→SH
		"920819": "920819.BJ", // §P1-6 北交所新股 920 段（旧实现误判 .SH）
		"300750": "300750.SZ", // 创业板
		"000001": "000001.SZ", // 深主板
		"688981": "688981.SH", // 科创板
		"430047": "430047.BJ", // 北交所老段
		"830799": "830799.BJ", // 北交所老段
		"600000.SH": "600000.SH", // 已带后缀原样返回
	}
	for in, want := range cases {
		if got := ExchangeSuffix(in); got != want {
			t.Errorf("ExchangeSuffix(%s)=%s want %s", in, got, want)
		}
	}
}
