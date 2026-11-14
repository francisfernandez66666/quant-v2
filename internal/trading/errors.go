// 文件概述（internal/trading/errors.go）
// §F8-20261114：给「账号未启用实盘 / 未填 gateway_url」一个可判别的哨兵身份。
// 此前三处 errors.New 用同一个字面量，handler 只拿到普通 error、无法与"网关真故障"
// 分档，只能一律 502——把配置态报成网关故障（P1-5 实锤：test01 未接入柜台拨
// /api/qmt/pending-review 得 502）。调用方必须用 errors.Is 判定，不得再靠字符串匹配。
// 字面量保持一字不改：历史日志/告警解析依赖它。
// English: §F8-20261114 — sentinel error separating "not configured" (409) from a real
// gateway failure (502); the literal text is frozen for log-parsing compatibility.
package trading

import "errors"

// ErrGatewayNotConfigured：账号未启用实盘或未填 gateway_url。调用方须用 errors.Is 判定，
// 不得再靠字符串匹配——这是"配置态"与"网关真故障"的分界，两者的 HTTP 语义不同。
// English: ErrGatewayNotConfigured — the account has no live chain wired; callers must
// branch with errors.Is instead of string matching.
var ErrGatewayNotConfigured = errors.New("qmt not enabled or gateway_url not set")
