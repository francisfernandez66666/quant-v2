#!/usr/bin/env bash
# 今日改动全量验证脚本（2026-08-05）
#
# 用实盘数据快照（internal/e2e/testdata/fixtures.json / fixtures_600580.json）离线 mock 全部外部数据源，
# 专项验证改动：
#   1) LLM 超时配置   ：llm.Config.Timeout 默认 60s、自定义生效
#   2) D1 回退        ：mock LLM 对 D1 返回 500 → 3 次轮询重试失败 → 回退上一轮评分
#   3) N形门槛放开     ：D1>0 且总分≥60 → Valid（D2/D3/D4 仅贡献总分）
#   4) 咨询专业模式   ：注入真实实时行情（东财 quote/资金流/分钟 MACD），缺失严禁编造
#   5) HTTP 级全端点  ：/api/news、/api/signals change_pct、/api/signal-logs、/api/sector/hot 兜底、consult API
#   6) 政策反制/confrontation 落盘、ths 昨收推算、GetStockList 新浪→东财兜底、updateHotPool、resolveConflict、PE 预取
#
# 用法:
#   ./scripts/verify_changes.sh                # 只跑今日改动专项
#   ./scripts/verify_changes.sh -full          # 连相关全量单测一起跑
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 1/2 编译检查..."
go build ./...
go vet ./internal/llm ./internal/combat_agent ./internal/engine ./internal/e2e ./internal/server ./internal/data ./internal/display ./cmd/quant

echo "==> 2/2 跑今日改动专项 e2e（实盘快照 mock）..."
go test -count=1 -v ./internal/e2e/ \
	-run 'TestLLMTimeoutConfig|TestD1FallbackAcrossRuns|TestNShapeGateD1AndTotal|TestEndToEndFullPipeline|TestConsult|TestHTTP|TestAttachLiveBar' 2>&1 \
	| grep -E '^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)'

if [ "${1:-}" = "-full" ]; then
	echo ""
	echo "==> 附加：今日改动相关全量单测..."
	go test -count=1 ./internal/combat_agent/... ./internal/engine/... ./internal/llm/... ./internal/strategies/n_shape/... \
		./internal/data/... ./internal/display/... ./internal/newsagent/... ./internal/e2e/ ./internal/server/ ./cmd/quant/...
fi

echo ""
echo "==> 全部通过"
