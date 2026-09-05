// blacklist.go — 黑名单代码匹配的唯一权威实现（§R3-8 P1-H 口径统一）。
// 此前风控层（risk.checkBlacklist 精确字符串相等）与执行层（trading.blacklisted 后缀归一）
// 两套判定：`600519.SH` 在配置时，裸码 `600519` 请求会被执行层拦截、却被风控层放行——
// 同一黑名单两层结论相反。现收敛到本函数，两侧共用。
// 匹配规则：
//   - 条目与请求代码均剥离交易所后缀（.SH/.SZ/.BJ 及首个 . 之后的部分）
//   - 按纯数字前缀比对（600519 匹配 600519.SH）
//   - 空列表/空代码恒为 false
//
// English: R3-8 P1-H — the single canonical blacklist matcher. The risk layer used exact string
// equality while the execution layer normalized exchange suffixes; the same configured entry could
// pass one layer and be blocked by the other. Both now call this.
package config

import "strings"

// CodeInBlacklist 判断 code 是否命中黑名单 list：
// 条目与请求代码均剥离交易所后缀（`.SH/.SZ/.BJ` 及首个 `.` 之后的部分）后按纯数字前缀比对，
// 空列表/空代码恒为 false。English: both sides are suffix-stripped before comparing pure codes;
// empty list or empty code never matches.
func CodeInBlacklist(list []string, code string) bool {
	if len(list) == 0 || code == "" {
		return false
	}
	pure := func(c string) string {
		if i := strings.IndexByte(c, '.'); i > 0 {
			c = c[:i]
		}
		return strings.TrimSpace(c)
	}
	pc := pure(code)
	for _, item := range list {
		if pi := pure(item); pi != "" && pi == pc {
			return true
		}
	}
	return false
}
