#!/bin/bash
# run_drill_weekly.sh — 恢复演练的**定时入口薄壳**（§0929DRILL，2026-09-29）。
#
# 为什么要有这个薄壳，而不是让 launchd 直接跑 verify_restore.sh：
#   ① 定时任务需要**固定的日志路径 + 固定的留档路径**，而这两个都可被环境变量覆盖；
#      把覆盖写在这里，plist 就永远只是"拉起哪个脚本"，改阈值不用重装任务。
#   ② 09-16~09-25 那次异地备份断更 10 天的教训（§OPS-CLOSEOUT）：定时任务的失败是**静默**的
#      ——脚本没被拉起来时，它体内的告警代码一行都不会执行。所以本壳除了跑演练，还负责
#      把"这一次真跑了"写进日志（START/DONE/FAIL 三态 + 退出码），让外层的拉取腿能反查新鲜度。
#   ③ 手工跑与定时跑必须走**同一条命令**（否则"手工绿、定时红"永远查不清）：
#      人工演练也直接跑本壳。
# 依赖方向：本壳 → verify_restore.sh → scripts/restore_drill.sh（结构/内容断言的唯一实现，
#   不在这里重写第二遍——§P0-B 起两套产物布局的判定只在 restore_drill.sh 里有一份）。
# 安装/升级：./install_mac_drill_agent.sh -Apply（稳定副本目录 = ~/backups/quant/drill/，
#   必须是 deploy/… + scripts/… 的镜像树，因为 verify_restore.sh 按 ../.. 反推仓库根）。
set -uo pipefail

DRILL_HOME="${DRILL_HOME:-$HOME/backups/quant/drill}"
LOG_DIR="${LOG_DIR:-$HOME/backups/quant}"
LOG="$LOG_DIR/drill_weekly.log"
DRILL_RECORD="${DRILL_RECORD:-$LOG_DIR/drill_record.jsonl}"
# 快照超龄阈值：Mac 拉取腿每晚 copy，8 天＝"连续一周以上没成功拉到"，此时演练判红是对的
# （演练验的是"备份链活着"，不是"这个脚本能不能跑完"）。
MAX_AGE_DAYS="${MAX_AGE_DAYS:-8}"
# 演练会真恢复整份快照（GB 级）+ restic check：给它 40 分钟，超了算失败而不是永远挂着。
# 为什么必须封顶：launchd 不封顶时一个卡住的演练会一直占着仓库锁，下次 copy 撞锁连败 8 次
# （§RESTIC-LOCK 2026-09-26 实录就是这个形态，只不过那次是 copy 自己卡住）。
DRILL_TIMEOUT_SEC="${DRILL_TIMEOUT_SEC:-2400}"

mkdir -p "$LOG_DIR"
ts() { date '+%Y-%m-%d %H:%M:%S'; }
line() { echo "$(ts) $*" | tee -a "$LOG"; }

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="$SELF_DIR/deploy/mac/verify_restore.sh"
if [ ! -f "$TARGET" ]; then
  # 这条判红专门用来咬"稳定副本只拷了一半"：镜像树里少了 restore_drill.sh 或目录拍平成一维，
  # verify_restore.sh 会按 ../.. 找到一个不存在的 scripts/restore_drill.sh 然后 fail
  # （它内部有这条断言），但那已经是"演练跑起来了才发现"——安装期就该拦下。
  line "FAIL reason=verify_restore.sh-missing path=$TARGET"
  exit 1
fi
if [ ! -f "$SELF_DIR/scripts/restore_drill.sh" ]; then
  line "FAIL reason=restore_drill.sh-missing-halved-mirror-tree path=$SELF_DIR/scripts/restore_drill.sh"
  exit 1
fi

line "START log=$LOG record=$DRILL_RECORD max_age_days=$MAX_AGE_DAYS timeout=${DRILL_TIMEOUT_SEC}s target=$TARGET"
# 超时优先用 GNU timeout（brew coreutils），没有就用 perl 的 alarm（系统自带），两条都没有
# 就**明写没有**并继续跑——不静默降级成"永不超时"却打印一条带秒数的日志（那行日志会变成假话）。
if command -v timeout >/dev/null 2>&1; then
  RUNNER=(timeout "$DRILL_TIMEOUT_SEC")
  line "RUNNER=timeout"
elif command -v perl >/dev/null 2>&1; then
  RUNNER=(perl -e 'alarm shift; exec @ARGV' "$DRILL_TIMEOUT_SEC")
  line "RUNNER=perl-alarm"
else
  RUNNER=()
  line "WARN runner=none（本机既无 timeout 也无 perl，超时封顶失效——演练可能无限挂着，请装 coreutils）"
fi

if [ ${#RUNNER[@]} -gt 0 ]; then
  DRILL_RECORD="$DRILL_RECORD" MAX_AGE_DAYS="$MAX_AGE_DAYS" ALERT="${ALERT:-1}" \
    "${RUNNER[@]}" /bin/bash "$TARGET" >>"$LOG" 2>&1
else
  DRILL_RECORD="$DRILL_RECORD" MAX_AGE_DAYS="$MAX_AGE_DAYS" ALERT="${ALERT:-1}" \
    /bin/bash "$TARGET" >>"$LOG" 2>&1
fi
rc=$?
# 只打退出码与日志路径：演练输出里带表名/行数，不带任何凭据；整份输出已在 $LOG 里可查。
if [ "$rc" -eq 0 ]; then
  line "DONE rc=0（浅腿+深腿全过；留档见 ${DRILL_RECORD}）"
else
  line "FAIL rc=${rc}（详见 $LOG 末尾；失败也已写进留档，拉取腿的新鲜度判据会看到它）"
fi
exit "$rc"
