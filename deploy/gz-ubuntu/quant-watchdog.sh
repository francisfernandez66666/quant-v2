#!/usr/bin/env bash
# quant-watchdog.sh — 云端 quant 的「假活」看门狗（2026-11-19 事故后的止血件）。
#
# 为什么需要它（现网实录，不是设想）：
#   2026-11-19 13:4x，quant 进程**活着**但所有 HTTP handler 卡在 DB 连接池
#   （`database/sql.(*DB).conn`）——内存压力把 swap 打满、SQLite 调用被拖成秒级，
#   池子 4 个连接全被占住。systemd 的 `Restart=always` 只救"进程退出"，救不了
#   "进程还在、但谁都不应答"，所以站点离线了近 10 分钟，只能人工重启。
#
# 判据（刻意只看一件事）：
#   `GET /livez` 是否在 5 秒内回 200。它是**纯进程存活**探针（handler 只回
#   {"ok":true,"build":...}），不查 DB、不查网关、不查行情——所以
#   `/readyz` 的 503（gateway=false 的诚实设计态）不会触发误重启，
#   行情源抖动、盘中休市、网关断链也都不会。
#
# 触发与防抖：
#   * 连续 FAIL_THRESHOLD 次失败（默认 3 次 × 2 分钟 = ~6 分钟）才重启；
#   * 重启后写冷却戳，COOLDOWN_SEC（默认 900s）内不再重启（防崩溃循环把日志刷爆）；
#   * 计数器放在 /run（tmpfs，重启即清零），不写业务数据目录。
#
# 输出（journal + 可选 ntfy）：
#   `QUANT_WATCHDOG ok build=<sha>`        每次成功探活一行（低频，2 分钟一行可接受）
#   `QUANT_WATCHDOG_FAIL n=<次数> last=<原因>` 每次失败一行
#   `QUANT_WATCHDOG_RESTART n=<次数>`      真正重启那一次
set -uo pipefail

LIVEZ_URL="${LIVEZ_URL:-http://127.0.0.1:8081/livez}"
STATE_DIR="${STATE_DIR:-/run/quant-watchdog}"
FAIL_FILE="$STATE_DIR/fails"
COOLDOWN_FILE="$STATE_DIR/last-restart"
FAIL_THRESHOLD="${FAIL_THRESHOLD:-3}"
COOLDOWN_SEC="${COOLDOWN_SEC:-900}"
TIMEOUT_SEC="${TIMEOUT_SEC:-5}"
SERVICE="${SERVICE:-quant}"

# 状态目录：systemd 用 RuntimeDirectory= 在 /run 下建好（属主=服务用户）；
# 手工直跑（排障）时 /run 不可写，退回 /tmp —— 状态只影响"要不要重启"，
# 绝不能因为写不了计数文件就把看门狗自己搞死（那样等于没有看门狗）。
mkdir -p "$STATE_DIR" 2>/dev/null || true
if ! : > "$STATE_DIR/.probe" 2>/dev/null; then
	STATE_DIR="/tmp/quant-watchdog"
	mkdir -p "$STATE_DIR" 2>/dev/null || true
	FAIL_FILE="$STATE_DIR/fails"
	COOLDOWN_FILE="$STATE_DIR/last-restart"
else
	rm -f "$STATE_DIR/.probe" 2>/dev/null || true
fi

body="$(curl -sS -m "$TIMEOUT_SEC" -w '\n%{http_code}' "$LIVEZ_URL" 2>/dev/null)"
code="$(printf '%s' "$body" | tail -n1)"
payload="$(printf '%s' "$body" | sed '$d' | head -c 200)"

now="$(date +%s)"
last_restart=0
[ -f "$COOLDOWN_FILE" ] && last_restart="$(cat "$COOLDOWN_FILE" 2>/dev/null || echo 0)"
case "$last_restart" in (*[!0-9]*|'') last_restart=0 ;; esac

if [ "$code" = "200" ]; then
	: > "$FAIL_FILE"
	build="$(printf '%s' "$payload" | sed -n 's/.*"build":"\([^"]*\)".*/\1/p')"
	echo "QUANT_WATCHDOG ok build=${build:-unknown}"
	exit 0
fi

fails=0
[ -f "$FAIL_FILE" ] && fails="$(cat "$FAIL_FILE" 2>/dev/null || echo 0)"
case "$fails" in (*[!0-9]*|'') fails=0 ;; esac
fails=$((fails + 1))
echo "$fails" > "$FAIL_FILE"
echo "QUANT_WATCHDOG_FAIL n=${fails} code=${code:-000} body=$(printf '%s' "$payload" | tr -d '\n')"

if [ "$fails" -lt "$FAIL_THRESHOLD" ]; then
	exit 0
fi

if [ $((now - last_restart)) -lt "$COOLDOWN_SEC" ]; then
	echo "QUANT_WATCHDOG_SKIP reason=cooldown since=$((now - last_restart))s"
	exit 0
fi

echo "QUANT_WATCHDOG_RESTART n=${fails} service=${SERVICE}"
echo "$now" > "$COOLDOWN_FILE"
: > "$FAIL_FILE"
# 只在失败达到阈值时动手；重启失败也不再叠加（下一次探活会重新计数）。
sudo -n systemctl restart "$SERVICE" || echo "QUANT_WATCHDOG_RESTART_FAILED service=${SERVICE}"
exit 0
