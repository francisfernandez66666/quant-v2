#!/bin/bash
# restic_pull_backup.sh — Mac 开发机异地备份拉取器（HARDENING 件1，restic-copy 拉取模式）
# 职责：广州机每晚产出一致性快照并备份进本地中转仓库（backup_snapshot.ps1），
#       本脚本用 `restic copy --from-repo sftp:gz:...` 只拉缺失的 chunk 包到 Mac 本地仓库——
#       方向是 Mac→广州出站（复用现成 ssh gz，NAT 无关，无需 Tailscale/远程登录），
#       传输量=日增量 chunk（首次 ~1.4GB 压缩包，之后每晚几十 MB），restic 自校验完整性。
# 依赖：ssh 主机别名 gz（~/.ssh/config）、restic（brew）、仓库密码在 macOS 钥匙串 quant-restic-repo-pass。
# 安装（launchd 每日 07:00；机器睡着则唤醒后补跑）：./install_mac_backup_agent.sh -Apply
#       （稳定副本在 ~/backups/quant/bin/，launchd 不能直接跑 Desktop 仓库路径——TCC 保护目录，见该脚本头注释）。
# 升级口径：仓库版是源，改完仓库版重跑 -Apply 同步；直接改稳定副本会下次被覆盖。
# 失败（快照过期/标记非 ok/copy 失败/check 失败）→ ntfy 告警（备份失败必须有人知道）。
set -uo pipefail

SSH_HOST="${SSH_HOST:-gz}"
REPO_REMOTE="sftp:${SSH_HOST}:C:/var/lib/quant-restic-repo"   # 广州中转仓库（restic copy 源）
REPO_LOCAL="$HOME/backups/quant/restic"                        # Mac 异地仓库（restic copy 目标）
KEYCHAIN_ITEM="quant-restic-repo-pass"
NTFY_URL="${NTFY_URL:-https://ntfy.sh}"
# §KUMA-SECREDTO（2026-10-07 修复批 波 3）：主题不再在仓库里写缺省值。
# 为什么：ntfy 的口径是「知道主题就能往那个主题发帖」⇒ 主题串就是凭据；这个 32-hex 以前同时
# 写在本文件、verify_restore.sh、kuma_seed.js 三处（三份并存的必然结局＝改一处漏两处），
# 而且已经进过 git 历史——**改文件洗不掉历史**，所以它按「已泄露」处理（轮换属 owner 当面看
# 预演的现网动作，不在本批自动执行面内）。仓库侧现在只留取用口径：值走 ntfy_topic.sh 单实现
# （env NTFY_TOPIC 优先，其次 macOS 钥匙串 quant-ntfy-topic，与 restic 仓库密码同一条通道）。
# shellcheck source=ntfy_topic.sh
NTFY_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ntfy_topic.sh"
if [ ! -f "$NTFY_LIB" ]; then
	# 取不到"取主题的代码"不是可以降级的状态：这个脚本的失败全靠 ntfy 通知到人，
	# 静默继续跑等于把备份链的报警器拆了还不自知（§N-6/§M2「降级不得报成功」同族）。
	echo "FATAL: 缺 ${NTFY_LIB}——ntfy_topic.sh 必须和本脚本一起拷进稳定副本目录（见 install_mac_backup_agent.sh）" >&2
	exit 1
fi
. "$NTFY_LIB"
NTFY_TOPIC="$(ntfy_topic_resolve || true)"
FRESH_MAX_HOURS="${FRESH_MAX_HOURS:-26}"                       # 快照超过 26h 未更新视为广州侧失败
LOG="$HOME/backups/quant/pull_backup.log"

mkdir -p "$(dirname "$LOG")"
log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*" | tee -a "$LOG"; }

# 取不到主题＝告警通道断了。这条必须在日志里吵一次（不回显值，只报长度/指纹），
# 否则「今晚备份失败」和「今晚没人能收到失败通知」在现象上完全一样。
[ -n "$NTFY_TOPIC" ] || log "WARN: ntfy 主题未配置（env NTFY_TOPIC 与钥匙串 quant-ntfy-topic 皆空）⇒ 本任务告警只落日志不推送：$(ntfy_topic_report restic-pull)"

alert() {  # 关键事件推 ntfy
  local title="$1" body="$2" pri="${3:-high}"
  if [ -z "$NTFY_TOPIC" ]; then
    # 无主题时**不发请求**：打到 https://ntfy.sh/（根路径）只会回 404，而日志里写"网络？"
    # 会把「配置缺失」伪装成「网络抖动」——下一次真断网时没人再信这条（§0929DRILL-A 假红同族）。
    # 正文整条落日志：告警发不出去时，日志就是唯一证据面，不能只留一句"发送失败"。
    log "ALERT-NOT-SENT reason=no-topic title=$title body=$body"
    return 0
  fi
  curl -fs -H "Title: $title" -H "Priority: $pri" -H "Tags: package" \
       -d "$body" "$NTFY_URL/$NTFY_TOPIC" >/dev/null 2>&1 \
    || log "ntfy 告警发送失败（网络？启动行的 topic_fp 可判断配的是哪一份）"
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

# 2.0) 陈旧锁自愈（§RESTIC-LOCK 客户端腿，与广州侧同姿势）。
#      copy 的 restic 进程若被外层杀掉（超时/断连），会在源或目标仓库留下排他锁文件；
#      下一次窗口会对着这把死锁连撞 8 次熔断判失败——2026-09-26 07:00 窗实录：03:09
#      那次被杀的 no-op copy 在广州中转仓留锁 PID 32000，早晨整窗 8 败全为此锁。
#      `restic unlock` 只清「主机为本机且 PID 已不存在 / 锁已超时」的锁（restic 自判），
#      真并发跑着的锁清不掉、copy 照样失败，所以红绿语义不变、失败计数照常。
log "清理上次遗留的陈旧锁（源+目标两仓）..."
restic --repo "$REPO_REMOTE" unlock 2>>"$LOG" || log "源仓 unlock 未成（网络/并发占用，交给 copy 重试）"
restic --repo "$REPO_LOCAL" unlock 2>>"$LOG" || log "目标仓 unlock 未成（同上，交给 copy 重试）"

# 2.1) restic copy：只拉本地仓库缺失的 pack（真增量）。
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
  # 第 3 次仍败大概率又是锁形态（窗口中途别处断线留锁）：中途补一次自愈再进下一轮重试。
  if [ "$attempt" -eq 3 ]; then
    log "copy 三败，中途补一次 unlock 自愈 ..."
    restic --repo "$REPO_REMOTE" unlock 2>>"$LOG" || true
    restic --repo "$REPO_LOCAL" unlock 2>>"$LOG" || true
  fi
  log "copy 第 $attempt 次失败（熔断/网络抖动/锁），45s 后重试..."
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

# 4.5) 定时任务新鲜度反查（§0929DRILL + §0929NIGHTLY 的"死调度探测器"）。
# 存在理由：09-16~09-25 那次异地备份断更 10 天，坏的不是判据而是**没人触发判据**——
#   脚本体内的告警代码在脚本没跑的时候一行都不会执行。恢复演练（com.quant.drill）和
#   夜间验收（com.quant.nightly）都是 09-29 才挂上调度的新任务，同一种失效形态它们一定会
#   再犯一次，而它们的产物（两个 *_record.jsonl）此前没有任何人在看。
#   本条把"某个定时任务多久没留下读数了"挂在**每天必然跑成的这条腿上**：拉取腿天天执行
#   （07:00 + 10:30 两个时点），顺手读一眼留档的最新时间戳，超龄就推 ntfy。
# 取向：**只告警不改退出码**。拉取腿自身成败与那两个调度是两件事，把后者做成前者的红，
#   等于让"备份没坏"跟着一起吵，两周后就会被人为忽略（刷屏⇒关通知，本仓同族教训）。
#   读数无论超龄与否都打一行 log（绿也要看得到数，§SIGNAL-DIST 口径）。
# ⚠ 两个任务共用**一个**判读函数（record_freshness），不写两份近乎一样的解析：
#   本批反复锤的主题就是"同一套判据两处分叉"，两份 20 行的时间戳解析一定会漂移，
#   而漂移之后坏的那一份会安静地不再判红（§0929DRILL-A 那条 --last 假红就是这种形态）。
record_freshness() {
  local label="$1" rec="$2" max_days="$3" hint="$4"
  local last ts res age
  if [ ! -s "$rec" ]; then
    # 留档文件不存在＝这个探测器自己的前提没落地（launchd 没装 / 第一次还没跑），必须吵而不是沉默。
    log "${label} freshness=NO-RECORD（${rec} 不存在或为空）"
    alert "quant ${label} 从未留下记录" "留档文件不存在：${rec}。安装：${hint}" medium
    return 0
  fi
  last="$(tail -1 "$rec" 2>/dev/null || true)"
  ts="$(printf '%s' "$last" | sed -n 's/.*"ts":"\([^"]*\)".*/\1/p')"
  res="$(printf '%s' "$last" | sed -n 's/.*"result":"\([^"]*\)".*/\1/p')"
  age="$(python3 - "$ts" <<'PYAGE' 2>/dev/null || echo unparsable
import sys,datetime
# 只取前 19 位并按 %Y-%m-%dT%H:%M:%S 解析、**不把 Z 放进格式串**：
#   留档时间戳是 date -u 产的 UTC 串（尾部有 Z），而 strftime 格式里写死 Z 时
#   截到 19 位就已经没有 Z 可对上——首版就是这么错的（ValueError⇒走 || echo 9999 兜底
#   ⇒每天推一条"演练调度疑似死了"的假告警，两周后通知被关掉，探测器自己变成噪声源）。
#   解析失败必须显式判成"这一腿没有读数"（哨兵取 unparsable 而不是一个大数——
#   真等了那么久的留档本来就该判超龄，让两种情况共用一个数就是把"读不出"冒充成"读数"）。
t=datetime.datetime.strptime(sys.argv[1][:19],"%Y-%m-%dT%H:%M:%S")
print((datetime.datetime.utcnow()-t).total_seconds()/86400)
PYAGE
)"
  if [ "$age" = "unparsable" ]; then
    # 留档存在但最新一行解析不出时间＝探测器读法坏了：如实报"读不出"，不报"超龄"。
    log "${label} freshness=UNPARSABLE（record=${rec} line=${last:-?}）——判读法坏了而不是调度死了"
    alert "quant ${label} 新鲜度探针读不出时间戳" "留档最新一行没有可解析的 ts 字段：${rec}。这条探测器的判据已失效，请先修读法再谈调度。" medium
    return 0
  fi
  log "${label} freshness=${age%.*}d last_result=${res:-?} max=${max_days}d record=${rec}"
  if awk "BEGIN{exit !($age > $max_days)}" 2>/dev/null; then
    alert "quant ${label} 调度疑似死了" "最近一次留档已 ${age%.*} 天（>${max_days} 天）。查：launchctl list | grep ${label}、安装：${hint}" medium
  fi
  return 0
}

# 演练：每周日 09:00 一次 ⇒ 7 天 + 2 天余量。
record_freshness "drill(com.quant.drill)" \
  "${DRILL_RECORD:-$HOME/backups/quant/drill_record.jsonl}" \
  "${DRILL_MAX_AGE_DAYS:-9}" \
  "./deploy/mac/install_mac_drill_agent.sh -Apply -Kick（每周日 09:00 跑 verify_restore.sh，每次一行 JSON 读数）"

# 夜间验收：每天 09:20 一次 ⇒ 2 天余量（周日机器睡着错过一次，第二天唤醒补跑就不该吵）。
record_freshness "nightly(com.quant.nightly)" \
  "${NIGHTLY_RECORD:-$HOME/backups/quant/nightly_record.jsonl}" \
  "${NIGHTLY_MAX_AGE_DAYS:-2}" \
  "./deploy/mac/install_mac_nightly_agent.sh -Apply -Kick（每日 09:20 跑广州夜间研究七腿验收，每次一行 JSON 读数）"

log "=== 备份成功 ==="
SNAPS="$(restic -r "$REPO_LOCAL" snapshots --short 2>/dev/null | tail -1)"
alert "quant 异地备份完成" "Mac 仓库最新快照: $SNAPS" low
