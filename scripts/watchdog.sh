#!/usr/bin/env bash
# watchdog.sh — 最小监控看门狗（§GAP7.1）：
# 进程存活 / 磁盘水位 / 内存水位 三项检查，异常时：
#   1) 向 $HEALTHCHECK_URL 发 fail 事件（healthchecks.io 类心跳服务会触发邮件/短信/TG 告警）
#   2) 向 $ALERT_WEBHOOK 发 JSON 文本（企业微信/钉钉/Slack 兼容格式可选）
# 全部正常时向 $HEALTHCHECK_URL 发心跳成功。
#
# 安装（服务器 root crontab，每分钟）：
#   * * * * * /var/lib/quant-trading-v2/scripts/watchdog.sh >> /var/log/quant-watchdog.log 2>&1
#
# 环境变量（建议写入 /etc/quant.env 或 crontab 内联）：
#   HEALTHCHECK_URL  心跳服务 ping 地址（如 https://hc-ping.com/<uuid>），空=跳过心跳
#   ALERT_WEBHOOK    告警 webhook 地址，空=仅本地日志
#   DISK_PCT         磁盘告警阈值（默认 85）
#   MIN_MEM_MB       可用内存告警阈值 MB（默认 300）
#   QUANT_SERVICE    systemd 服务名（默认 quant）

set -u

HEALTHCHECK_URL="${HEALTHCHECK_URL:-}"
ALERT_WEBHOOK="${ALERT_WEBHOOK:-}"
DISK_PCT="${DISK_PCT:-85}"
MIN_MEM_MB="${MIN_MEM_MB:-300}"
QUANT_SERVICE="${QUANT_SERVICE:-quant}"

alert() {
    local msg="$1"
    echo "[$(date '+%F %T')] ALERT: $msg"
    if [ -n "$ALERT_WEBHOOK" ]; then
        curl -sS -m 10 -X POST -H 'Content-Type: application/json' \
            -d "{\"text\":\"[quant] $msg\"}" "$ALERT_WEBHOOK" >/dev/null 2>&1 || true
    fi
}

heartbeat_fail() {
    [ -n "$HEALTHCHECK_URL" ] && curl -sS -m 10 --fail "$HEALTHCHECK_URL/fail" >/dev/null 2>&1 || true
}

# 1) 主进程存活
if command -v systemctl >/dev/null 2>&1; then
    if ! systemctl is-active --quiet "$QUANT_SERVICE"; then
        alert "服务 $QUANT_SERVICE 已停止！尝试拉起…"
        systemctl start "$QUANT_SERVICE" || true
        heartbeat_fail
        exit 1
    fi
fi

# 2) 磁盘水位（数据所在分区）
disk_pct=$(df -P /var/lib/quant-trading-v2 2>/dev/null | awk 'NR==2 {gsub(/%/,""); print $5}')
if [ -n "$disk_pct" ] && [ "$disk_pct" -ge "$DISK_PCT" ] 2>/dev/null; then
    alert "磁盘使用率 ${disk_pct}% ≥ ${DISK_PCT}%"
    heartbeat_fail
    exit 1
fi

# 3) 可用内存（与调度器内存总闸同口径 /proc/meminfo MemAvailable）
mem_mb=$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo 2>/dev/null)
if [ -n "$mem_mb" ] && [ "$mem_mb" -le "$MIN_MEM_MB" ] 2>/dev/null; then
    alert "可用内存 ${mem_mb}MB ≤ ${MIN_MEM_MB}MB（OOM 风险）"
    heartbeat_fail
    exit 1
fi

# 全部正常：发心跳
if [ -n "$HEALTHCHECK_URL" ]; then
    curl -sS -m 10 --fail "$HEALTHCHECK_URL" >/dev/null 2>&1 || true
fi
echo "[$(date '+%F %T')] ok disk=${disk_pct:-?}% mem=${mem_mb:-?}MB"
