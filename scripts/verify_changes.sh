#!/usr/bin/env bash
# 改动全量验证脚本（含 2026-08-05 实时链路改动专项 + 2026-09-11 回测自动增强专项）
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
# §回测自动增强（2026-09-11 A0+A+B+C+D）专项（见 3/3）：
#   A0 配置管线   ：config.ValidateBacktest 区间/档位单调性 + FillDefaults；backtest_settings 读写；
#                  server GET/PUT /api/research/backtest-config + 三处入队注入 + runtask payload 回退链
#   A 动态滑点    ：paper_trades 实测校准中位数（战法级→全局→配置回退）+ 非对称买差 clamp
#   B 流动性约束  ：涨停封死/打开、跌停封死顺延、部分成交 fillRate、amount 千元单位自校
#   C Pareto      ：四维非支配前沿（vs 朴素参照）、推荐解（门槛全过取 Sharpe 最高）、payload 落库
#   D 前端        ：ParetoChart 渲染/交互 + BacktestConfigPanel 读写 + approveOptimization 推荐解请求体
#   回归保证      ：增强关闭（nil/Enabled=false）时旧行为逐字节一致（各包 _test 已含短路用例）
#
# 用法:
#   ./scripts/verify_changes.sh                # 编译 + 专项 e2e + 回测增强单测
#   ./scripts/verify_changes.sh -full          # 再连相关全量单测 + 前端 vitest 一起跑
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 1/3 编译检查..."
go build ./...
go vet ./internal/llm ./internal/combat_agent ./internal/engine ./internal/e2e ./internal/server ./internal/data ./internal/display ./cmd/quant \
	./internal/config ./internal/btreplay ./internal/store ./internal/scheduler ./cmd/research

echo "==> 2/3 实时链路改动专项 e2e（实盘快照 mock）..."
go test -count=1 -v ./internal/e2e/ \
	-run 'TestLLMTimeoutConfig|TestD1FallbackAcrossRuns|TestNShapeGateD1AndTotal|TestEndToEndFullPipeline|TestConsult|TestHTTP|TestAttachLiveBar' 2>&1 \
	| grep -E '^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)'

echo "==> 3/3 回测自动增强专项（A0+A+B+C+D，2026-09-11）..."
go test -count=1 -v ./internal/config ./internal/btreplay ./internal/store ./internal/server ./internal/scheduler ./cmd/research \
	-run 'TestValidateBacktest|TestFillDefaults|TestFillBacktestDefaults|TestBacktestSettingsRoundTrip|TestPaperSlippageCalib|TestBacktestConfigEndpoints|TestInjectBacktestPayload|TestPayloadBacktest|TestCostRoundTripPnlExCompat|TestSlippageTiers|TestSlippageBpsAsymmetric|TestFillRate|TestLimitBoardGating|TestCalibAudit|TestEntrySlipGating|TestUniformExit|TestFixAmountScale|TestAvgAmountWan|TestBuildSlipCtx|TestPareto|TestCapFront|TestRecommended|TestPointJSON|TestSaveSweepResultsParetoCarry' 2>&1 \
	| grep -E '^(--- FAIL|FAIL|ok)'

if [ "${1:-}" = "-full" ]; then
	echo ""
	echo "==> 附加：实时链路相关全量单测..."
	go test -count=1 ./internal/combat_agent/... ./internal/engine/... ./internal/llm/... ./internal/strategies/n_shape/... \
		./internal/data/... ./internal/display/... ./internal/newsagent/... ./internal/e2e/ ./internal/server/ ./cmd/quant/...
	echo ""
	echo "==> 附加：回测增强全量单测（含回归短路）..."
	go test -count=1 ./internal/config/... ./internal/btreplay/... ./internal/store/... ./internal/scheduler/... ./cmd/research/...
	echo ""
	echo "==> 附加：前端回测增强 + 组件测试（vitest run）..."
	( cd web && npm test )
fi

echo ""
echo "==> 全部通过"
