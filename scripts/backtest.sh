#!/usr/bin/env bash
# backtest 回测数据脚本快捷启动器
#
# 用法:
#   ./scripts/backtest.sh                    # 单轮，默认输出 backtest_out/
#   ./scripts/backtest.sh -cycles 3          # 连续跑 3 轮（稳定性校验）
#   ./scripts/backtest.sh -config backtest_params.json -watchlist 300750,600519 -out ./bt
#   LLM_API_KEY=xxx ./scripts/backtest.sh    # 配置 LLM 后运行
set -euo pipefail
cd "$(dirname "$0")/.."

echo "==> 编译 backtest 工具..."
go build -o /tmp/quant-backtest ./cmd/backtest

echo "==> 运行回测..."
# 传递所有额外参数
exec /tmp/quant-backtest "$@"
