#!/usr/bin/env bash
# 本次"LLM 慢响应处理"改动验证脚本
#
# 用实盘数据快照（internal/e2e/testdata/fixtures.json）离线 mock 全部外部数据源，
# 专项验证三项改动：
#   1) 超时配置  ：llm.Config.Timeout 默认 60s、自定义生效
#   2) D1 回退   ：mock LLM 对 D1 返回 500 → 3 次轮询重试失败 → 回退上一轮评分（NScore 仍>0）
#   3) 近实时注入：5s ScorePool 注入 D1 缓存（配套回归，见 combat_agent/engine 测试）
#
# 用法:
#   ./scripts/verify_changes.sh                # 只跑本次改动专项
#   ./scripts/verify_changes.sh -full          # 连本次改动相关的全量单测一起跑
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 1/2 编译检查..."
go build ./...
go vet ./internal/llm ./internal/combat_agent ./internal/engine ./internal/e2e ./internal/server ./cmd/quant

echo "==> 2/2 跑本次改动专项 e2e（实盘快照 mock）..."
go test -count=1 -v ./internal/e2e/ \
	-run 'TestLLMTimeoutConfig|TestD1FallbackAcrossRuns|TestNShapeGateD1AndTotal|TestEndToEndFullPipeline' 2>&1 \
	| grep -E '^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)|回退上一轮|轮询重试|NScore|事件归因|N形'

if [ "${1:-}" = "-full" ]; then
	echo ""
	echo "==> 附加：本次改动相关全量单测..."
	go test -count=1 ./internal/combat_agent/... ./internal/engine/... ./internal/llm/... ./internal/strategies/n_shape/... ./internal/e2e/ ./cmd/quant/...
fi

echo ""
echo "==> 全部通过"
