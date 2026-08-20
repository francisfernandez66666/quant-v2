#!/usr/bin/env bash
# qmt_mlink.sh — AUTO_TRADING_PLAN M1 全链路联调冒烟（本地或服务器）。
# 1. 编译 cmd/qmt-mock；2. 起 mock 网关；3. 冒烟 /health /state /order → 成交 → /state；
# 4.（可选）若 QMT_SERVER 指向在跑的 quant 服务器，则验证成交回报能推到 POST /api/qmt/report。
#
# 用法：
#   QMT_TOKEN=change-me ./scripts/qmt_mlink.sh            # 仅网关自测
#   QMT_TOKEN=x QMT_SERVER=http://127.0.0.1:8080 ./scripts/qmt_mlink.sh  # 含回报推送验证
#
# English: full-chain integration smoke for M1 — builds cmd/qmt-mock, runs it, and checks
# /health /state /order → fill → /state; optionally verifies report push to a live quant server.

set -euo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LISTEN="${QMT_MOCK_LISTEN:-127.0.0.1:8789}"
TOKEN="${QMT_TOKEN:-mock-secret}"
SERVER="${QMT_SERVER:-}"            # 在跑的 quant 服务器（如 http://127.0.0.1:8080）
DELAY="${QMT_DELAY:-1s}"
SEED="${QMT_SEED:-600519.SH,贵州茅台,100,1500.00|300750.SZ,宁德时代,200,180.00}"

BIN="$(mktemp -d)/qmt-mock"
echo "==> 编译 cmd/qmt-mock ..."
( cd "$APP_DIR" && go build -o "$BIN" ./cmd/qmt-mock )

PID=""
cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> 启动 mock 网关 : $LISTEN (token=$TOKEN delay=$DELAY)"
"$BIN" -listen "$LISTEN" -token "$TOKEN" -server "$SERVER" -report-token "$TOKEN" -delay "$DELAY" -seed "$SEED" &
PID=$!
sleep 1

base="http://${LISTEN##*:}"
host="${LISTEN%:*}"
[ "$host" = "0.0.0.0" ] && base="http://127.0.0.1:${LISTEN##*:}"
base="http://127.0.0.1:${LISTEN##*:}"
auth="Authorization: Bearer $TOKEN"

echo "==> /health"
curl -sf "$base/health" | python3 -m json.tool
echo "==> /state (预置持仓)"
curl -sf "$base/state" -H "$auth" | python3 -c "import json,sys; d=json.load(sys.stdin); print('connected=',d['connected'],'positions=',[(p['ts_code'],p['qty']) for p in d['positions']])"

echo "==> /order 买入 600519.SH 100 股 @1510"
OID=$(curl -sf -X POST "$base/order" -H "$auth" -H 'Content-Type: application/json' \
  -d '{"signal_id":"mlink@600519.SH@test","code":"600519.SH","name":"贵州茅台","side":"买入","price_type":"market","price":1510,"qty":100,"amount":151000,"created_at":"2026-08-20T00:00:00+08:00"}' \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['order_id'])")
echo "    order_id=$OID"

echo "==> 幂等重发（同 signal_id 应返回原 order_id）"
OID2=$(curl -sf -X POST "$base/order" -H "$auth" -H 'Content-Type: application/json' \
  -d '{"signal_id":"mlink@600519.SH@test","code":"600519.SH","side":"买入","price_type":"market","price":1510,"qty":100,"amount":151000,"created_at":"2026-08-20T00:00:00+08:00"}' \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['order_id'])")
[ "$OID" = "$OID2" ] && echo "    ✓ 幂等 OK (order_id=$OID2)" || { echo "    ✗ 幂等失败"; exit 1; }

echo "==> 等待成交 ($DELAY + 1s) ..."
sleep 2
curl -sf "$base/state" -H "$auth" | python3 -c "
import json,sys
d=json.load(sys.stdin)
o=d['orders'][0]
p=[x for x in d['positions'] if x['ts_code']=='600519.SH'][0]
print('order status =', o['status'])
print('600519.SH qty =', p['qty'], ' cost =', p['cost_price'], ' high =', p['highest_price'])
assert o['status']=='已成', '成交状态未更新'
assert p['qty']==200 and p['cost_price']==1505.0 and p['highest_price']==1510.0, '加权成本/最高价未按预期更新'
print('✓ 全链路冒烟通过')
"

if [ -n "$SERVER" ]; then
  echo "==> 验证回报推送 (server=$SERVER) ..."
  curl -sf -X POST "$SERVER/api/qmt/report" -H "$auth" -H 'Content-Type: application/json' \
    -d '{"type":"trade","order_id":"'$OID'","code":"600519.SH","side":"买入","price":1510,"qty":100,"amount":151000,"traded_at":"2026-08-20T10:00:00+08:00","signal_id":"mlink@600519.SH@test"}' \
    | python3 -m json.tool
  echo "    ✓ 回报已推送首尔（持仓页实盘 tab 应已刷新）"
fi

echo "done"