#!/bin/bash
# restic_pull_backup.sh — Mac 开发机异地备份拉取器（HARDENING 件1，restic-copy 拉取模式）
# 职责：广州机每晚产出一致性快照并备份进本地中转仓库（backup_snapshot.ps1），
#       本脚本用 `restic copy --from-repo sftp:gz:...` 只拉缺失的 chunk 包到 Mac 本地仓库——
#       方向是 Mac→广州出站（复用现成 ssh gz，NAT 无关，无需 Tailscale/远程登录），
#       传输量=日增量 chunk（首次 ~1.4GB 压缩包，之后每晚几十 MB），restic 自校验完整性。
# 依赖：ssh 主机别名 gz（~/.ssh/config）、restic（brew）、仓库密码在 macOS 钥匙串 quant-restic-repo-pass。
# 安装（launchd 每日 07:00；机器睡着则唤醒后补跑）：见 deploy/mac/com.quant.backup.plist。
# 失败（快照过期/标记非 ok/copy 失败/check 失败）→ ntfy 告警（备份失败必须有人知道）。
set -uo pipefail

SSH_HOST="${SSH_HOST:-gz}"
REPO_REMOTE="sftp:${SSH_HOST}:C:/var/lib/quant-restic-repo"   # 广州中转仓库（restic copy 源）
REPO_LOCAL="$HOME/backups/quant/restic"                        # Mac 异地仓库（restic copy 目标）
KEYCHAIN_ITEM="quant-restic-repo-pass"
NTFY_URL="${NTFY_URL:-https://ntfy.sh}"
NTFY_TOPIC="${NTFY_TOPIC:-5fc177ea37320815462af122b5218849}"
FRESH_MAX_HOURS="${FRESH_MAX_HOURS:-26}"                       # 快照超过 26h 未更新视为广州侧失败
LOG="$HOME/backups/quant/pull_backup.log"

mkdir -p "$(dirname "$LOG")"
log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" | tee -a "$LOG"; }

alert() {  # 关键事件推 ntfy
  local title="$1" body="$2" pri="${3:-high}"
  curl -fs -H "Title: $title" -H "Priority: $pri" -H "Tags: package" \
       -d "$body" "$NTFY_URL/$NTFY_TOPIC" >/dev/null 2>&1 \
    || log "ntfy 告警发送失败（网络？）"
}

fail() { log "ERROR: $*"; alert "quant 备份失败" "$*" high; exit 1; }

# restic 密码从钥匙串取，绝不落盘/回显。两仓库同密码。
command -v restic >/dev/null || fail "restic 未安装"
RESTIC_PASSWORD="$(security find-generic-password -a "$USER" -s "$KEYCHAIN_ITEM" -w)" \
  || fail "钥匙串取不到仓库密码 $KEYCHAIN_ITEM"
export RESTIC_PASSWORD
# restic copy 需要目标 + 源两套密码；两仓库同密码，都从钥匙串导出。
export RESTIC_FROM_PASSWORD="$RESTIC_PASSWORD"
trap 'unset RESTIC_PASSWORD RESTIC_FROM_PASSWORD' EXIT

# 1) 读广州侧 SNAPSHOT_OK 标记：确认快照新鲜且 integrity ok（copy 前先看源头）。
log "读取广州快照标记 ..."
MARK="$(ssh -o ConnectTimeout=20 -o LogLevel=ERROR "$SSH_HOST" \
  'type C:\var\lib\quant-snapshot\SNAPSHOT_OK' 2>/dev/null)" \
  || fail "读不到广州 SNAPSHOT_OK（快照任务可能没跑）"
echo "$MARK" | grep -q '"ok":true' || fail "广州快照标记非 ok：$MARK"

SNAP_TS="$(echo "$MARK" | sed -n 's/.*"ts":"\([^"]*\)".*/\1/p')"
AGE_H=$(python3 - "$SNAP_TS" <<'PY'
import sys,datetime
t=datetime.datetime.strptime(sys.argv[1][:19],"%Y-%m-%dT%H:%M:%S")
print((datetime.datetime.now()-t).total_seconds()/3600)
PY
) 2>/dev/null || AGE_H=999
awk "BEGIN{exit !($AGE_H > $FRESH_MAX_HOURS)}" && fail "快照过期 ${AGE_H%.*}h > ${FRESH_MAX_HOURS}h"
log "快照 OK：ts=$SNAP_TS age=${AGE_H%.*}h"

# 2) restic copy：只拉本地仓库缺失的 pack（真增量）。
#    慢速公网 sftp 偶发抖动会触发 restic 熔断器（circuit breaker）——copy 可断点续传，
#    已拷 pack 自动跳过，故包一层重试循环把它磨完。
log "restic copy（拉增量）..."
COPY_OK=0
for attempt in 1 2 3 4 5 6 7 8; do
  if restic copy -o 'sftp.args=-o LogLevel=ERROR -o ServerAliveInterval=15 -o ServerAliveCountMax=4' \
       --from-repo "$REPO_REMOTE" --repo "$REPO_LOCAL" 2>>"$LOG"; then
    COPY_OK=1
    break
  fi
  log "copy 第 $attempt 次失败（熔断/网络抖动），45s 后重试..."
  sleep 45
done
[ "$COPY_OK" = "1" ] || fail "restic copy 连续 8 次失败"

# 3) 保留策略（Mac 侧）：7 日 / 4 周 / 6 月；prune 就地跑（本地仓库无带宽顾虑）。
log "restic forget/prune ..."
restic -r "$REPO_LOCAL" forget --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune 2>>"$LOG" \
  || log "forget/prune 失败（不影响本份副本）"

# 4) 月度轻自检：每周一次结构校验（开销小）；read-data 抽检每季度人工跑。
DOW="$(date +%u)"
[ "$DOW" = "7" ] && { log "restic check（周日）..."; restic -r "$REPO_LOCAL" check 2>>"$LOG" || alert "quant 备份仓库自检失败" "restic check 报错，看 $LOG" high; }

log "=== 备份成功 ==="
SNAPS="$(restic -r "$REPO_LOCAL" snapshots --short 2>/dev/null | tail -1)"
alert "quant 异地备份完成" "Mac 仓库最新快照: $SNAPS" low
