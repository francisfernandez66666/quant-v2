// Package data — exchange.go 股票代码 → 交易所后缀的唯一权威实现。
// §P1-6（2026-09-15）：此前 engine.withSuffix / server.normalizeTsCode / data.LimitUpPct
// 三处各自实现交易所归属且口径矛盾——920xxx 北交所新股在两处后缀函数里被 `9` 前缀误判为 .SH
// （下单发错交易所、账本代码写错），而 LimitUpPct 又按 `92` 前缀当北交所 30% 涨跌幅，
// 同一代码三处归属互相打架。现统一收口到 ExchangeSuffix，三处全部改为调用本函数。
// English: §P1-6 — the single authoritative pure-code → exchange-suffix classifier. The three
// historical call sites (engine.withSuffix / server.normalizeTsCode / data.LimitUpPct) disagreed on
// the 920xxx Beijing exchange segment (two suffixed it .SH, one treated it as 30%-limit BJ) — now
// they all delegate here.
package data

import "strings"

// ExchangeSuffix 为纯数字股票代码补交易所后缀：
//   - 920 前缀 → .BJ（北交所 2024 起新股 920 段，先于 `9`→.SH 的老规则判定）
//   - 4/8 前缀 → .BJ（北交所/新三板精选层平移）
//   - 6 前缀、900 沪 B → .SH
//   - 其余（0/3 等）→ .SZ
//
// 已带后缀的代码原样返回。注意 `9` 前缀需先排除 920（北交所）再归 .SH（沪 B 900）。
// English: appends the exchange suffix to a bare digit code (920-prefix → .BJ before the legacy
// 9→.SH rule for Shanghai B-shares; 4/8 → .BJ; 6/900 → .SH; else .SZ). Codes already carrying a
// suffix are returned unchanged.
func ExchangeSuffix(code string) string {
	if strings.Contains(code, ".") {
		return code
	}
	switch {
	case strings.HasPrefix(code, "920"):
		return code + ".BJ"
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "8"):
		return code + ".BJ"
	case strings.HasPrefix(code, "6"), strings.HasPrefix(code, "9"):
		return code + ".SH"
	default:
		return code + ".SZ"
	}
}
