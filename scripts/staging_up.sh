#!/usr/bin/env bash
# staging_up.sh — §WS-G staging 影子环境一键拉起：
# 构建 → 独立 staging 数据目录（~/.quant-staging）→ QUANT_ENV=staging 影子引擎
# （决策流完整但 QMT 一律 ShadowExecutor，不真下）→ 快照流录制 → 探针冒烟。
#
# 用法：scripts/staging_up.sh [--record] [--port 8081]
#   --record   同时开启行情快照流录制（quote_stream.jsonl，供回放 harness）
#   --port     监听端口（默认 8081；staging 与生产 8080 分离）
#
# 安全护栏：staging 下 qmt.enabled=true 时 quant 进程启动即 fail-fast 拒绝（见 main.go
# verifyDeployment §WS-G），防止误连实盘网关。本脚本启动前也显式强制 qmt.enabled=false。

set -eu

PORT="${PORT:-8081}"
RECORD="${RECORD:-0}"
for a in "$@"; do
  case "$a" in
    --record) RECORD=1 ;;
    --port=*) PORT="${a#*=}" ;;
  esac
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGING_DIR="${STAGING_DIR:-$HOME/.quant-staging}"
BIN="$ROOT/bin/quant-staging"

echo "[staging] 构建 quant（staging 目标）…"
mkdir -p "$ROOT/bin"
(cd "$ROOT" && go build -o "$BIN" ./cmd/quant)

echo "[staging] 准备独立数据目录: $STAGING_DIR"
mkdir -p "$STAGING_DIR/opslog" "$STAGING_DIR/backups"

# 首次启动时播种最小 config.json（qmt.enabled=false 强制；存在则沿用，仍强制 enabled=false）
CFG="$STAGING_DIR/config.json"
if [ ! -f "$CFG" ]; then
  cat > "$CFG" <<'JSON'
{
  "qmt": { "enabled": false, "mode": "manual", "gateway_url": "", "token": "" }
}
JSON
  echo "[staging] 已生成最小 config.json（qmt.enabled=false）"
else
  # 无论存量配置如何，staging 一律强制 qmt.enabled=false（防误连实盘网关）
  python3 - "$CFG" <<'PY'
import json,sys
p=sys.argv[1]
c=json.load(open(p))
c.setdefault("qmt",{})["enabled"]=False
json.dump(c,open(p,"w"),ensure_ascii=False,indent=2)
PY
  echo "[staging] 已强制 qmt.enabled=false（存量配置）"
fi

EXTRA=""
if [ "$RECORD" = "1" ]; then
  EXTRA="QUANT_RECORD_STREAM=1"
fi

echo "[staging] 启动影子引擎 (端口 $PORT, dataDir=$STAGING_DIR)"
# 后台运行，日志落 staging 数据目录
env QUANT_ENV=staging QUANT_DATA_DIR="$STAGING_DIR" $EXTRA \
  "$BIN" -listen ":$PORT" >> "$STAGING_DIR/staging.log" 2>&1 &

PID=$!
echo "$PID" > "$STAGING_DIR/staging.pid"
echo "[staging] pid=$PID"

# 探针冒烟：等待就绪后打 /api/health（无 token 401=已就绪）
for i in $(seq 1 30); do
  if curl -s -o /dev/null -m 2 "http://127.0.0.1:$PORT/api/health"; then
    code=$(curl -s -o /dev/null -w '%{http_code}' -m 2 "http://127.0.0.1:$PORT/api/health")
    echo "[staging] 就绪: /api/health → $code（401=认证层正常）"
    exit 0
  fi
  sleep 1
done
echo "[staging] 30s 内未就绪，日志尾部："
tail -30 "$STAGING_DIR/staging.log" 2>/dev/null || true
exit 1
