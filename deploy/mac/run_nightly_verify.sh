#!/bin/bash
# run_nightly_verify.sh — 广州夜间研究的**每日验收**定时入口薄壳（§0929NIGHTLY，2026-09-29）。
#
# 为什么要有这个薄壳（与 run_drill_weekly.sh 同一条理由，不是再造一个包装）：
#   scripts/verify_nightly_guangzhou.sh 是 09-29 审计批 ⑪-2 新写的七腿验收（服务态/心跳/内存/
#   量纲抽检/队列积压/候选产出/财报覆盖），它**能跑**但和当年的 restore_drill.sh 一样
#   「有脚本无调度」——本仓这条族已经犯过五次（§ENH-5 网关运维脚本、§P0-B 备份脚本、
#   §0927KA keepalive、恢复演练本体），每次都是判据写得很完整、没人或没钟去触发，
#   于是"每天都有验收"纸面上成立、现场一次没发生。本壳把它挂上 launchd。
#
# 三件事只做在这个薄壳里（阈值一律留在被调脚本里，改判据不用重装任务）：
#   ① **GZ_IP 从 ssh 别名派生**：仓库里任何文件都不许出现广州公网 IP 字面量（本仓纪律），
#      而定时任务必须能连上现网 ⇒ 取 ~/.ssh/config 里 gz 的 hostname（`ssh -G gz` 展开真值，
#      与 install_mac_backup_agent.sh 检查 gz 别名是同一条来源）。派生失败就判红退出，
#      不回落到写死的地址，也不"跳过并算成功"。
#   ② **把这一次真跑了写进日志与留档**：09-16~09-25 异地备份断更 10 天的教训（§OPS-CLOSEOUT）
#      是"定时任务没被拉起来时，它体内的告警代码一行都不执行"。所以 START / DONE rc= / FAIL rc=
#      三个锚点固定打，结果另追加一行 JSON 到 ~/backups/quant/nightly_record.jsonl，
#      由每天必然跑成的备份拉取腿反查它的新鲜度（死调度探测器，见 restic_pull_backup.sh §4.6）。
#   ③ **超时封顶**：七腿走公网 SSH，卡住不能永远挂着（launchd 下卡死＝后面每天都不触发）。
#
# 依赖方向：本壳 → scripts/verify_nightly_guangzhou.sh（七腿判据的唯一实现，这里不重写第二份）。
# 安装/升级：./install_mac_nightly_agent.sh -Apply（稳定副本 ~/backups/quant/nightly/，
#   必须放非 TCC 保护目录且镜像 scripts/ 一层，理由见 install_mac_nightly_agent.sh 文件头）。
#
# English: daily scheduled entry for the Guangzhou nightly-research acceptance probe. Derives the
# host from the ssh alias (no literal IP in the repo), caps runtime, and appends one JSON record per
# run so the always-on backup pull leg can detect a dead schedule.
set -uo pipefail

NIGHTLY_HOME="${NIGHTLY_HOME:-$HOME/backups/quant/nightly}"
LOG_DIR="${LOG_DIR:-$HOME/backups/quant}"
LOG="$LOG_DIR/nightly_verify.log"
RECORD="$LOG_DIR/nightly_record.jsonl"
SSH_ALIAS="${SSH_ALIAS:-gz}"                  # 广州出站唯一入口（与部署/拉取腿同一个别名）
VERIFY_TIMEOUT_SEC="${VERIFY_TIMEOUT_SEC:-1800}"   # 七腿含两次 scp + 三次 ssh，30 分钟封顶

mkdir -p "$LOG_DIR"
ts() { date '+%Y-%m-%d %H:%M:%S'; }
line() { echo "$(ts) $*" | tee -a "$LOG"; }

# 结果留档：纯 bash 拼 JSON（这台机器上留痕腿不能反过来把判据腿拖下水——python3 缺失时
# 演练照样要能记下行）。文案先剥双引号/反斜杠，一行 JSON 被引号撑裂就什么都读不出来了。
record_line() {
    local res="$1" rc="$2" detail="$3"
    mkdir -p "$(dirname "$RECORD")" 2>/dev/null || true
    printf '{"ts":"%s","result":"%s","rc":"%s","source":"nightly_verify","detail":"%s"}\n' \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$res" "$rc" \
        "$(printf '%s' "$detail" | tr -d '"\\')" >> "$RECORD" 2>/dev/null \
        || line "WARN record-unwritable path=${RECORD}（判据结果不受影响）"
}

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="$SELF_DIR/scripts/verify_nightly_guangzhou.sh"

# 别名前提（§0929NIGHTLY-B，2026-09-29 深夜锁面预演时锤实的新缺陷）：
#   原先这条守卫写成「ssh -G "$SSH_ALIAS" 展开出的 hostname 非空即通过」。实测（未配置的名字）：
#     $ ssh -G qzzz-not-a-real-alias | grep '^hostname '
#     hostname qzzz-not-a-real-alias        ← ssh 把**别名本身**当主机名原样回显，rc 仍是 0
#   ⇒ "非空"对"别名根本不存在"这种失效形态永远为真＝本仓最恨的自证绿（install_mac_nightly_agent.sh
#   第 47 行早就用 Host 行声明做前置了，薄壳这一侧漏了同款判据；两条腿必须同一个前提，
#   否则"装的时候绿、每天跑的时候红"又会变成人盯）。
#   取向：按 ~/.ssh/config 里**是否真有一条 Host 声明点名了这个别名**判。
#   注意通配（Host *）不算点名——宁可判红也不接受"任何别名都通过"，现网 gz 是显式声明的。
alias_declared() {
    ALIAS_DECLINE=""
    if [ ! -f "$HOME/.ssh/config" ]; then
        ALIAS_DECLINE="ssh-config-missing path=$HOME/.ssh/config"
        return 1
    fi
    if ! awk -v a="$SSH_ALIAS" '/^Host[ \t]/{for(i=2;i<=NF;i++) if($i==a){print "yes"; exit}}' "$HOME/.ssh/config" \
            | grep -q '^yes$'; then
        ALIAS_DECLINE="host-line-absent alias=$SSH_ALIAS"
        return 1
    fi
    return 0
}

# -Preview：安装期自检用（证明稳定副本两半都在、七腿命令真拼得出来），
#   **不写日志、不写留档、不发一次 SSH**。留档新鲜度探测器（restic_pull_backup.sh §4.6）
#   读的就是 nightly_record.jsonl 的最新时间戳，自检要是往里面写一行，
#   就等于"每天演练之前先伪造一条今天跑过的证据"——那是本仓最恨的自证绿形态。
if [ "${1:-}" = "-Preview" ]; then
    if [ ! -f "$TARGET" ]; then
        echo "PREVIEW-FAIL missing-target=$TARGET"
        exit 1
    fi
    HOST_PREVIEW="$(ssh -G "$SSH_ALIAS" 2>/dev/null | awk '/^hostname /{print $2; exit}')"
    # 两条各判各的失效形态：①别名没在 Host 行里点名（上面的实测形态）；②点名了但展开不出 hostname。
    alias_declared || { echo "PREVIEW-FAIL alias-not-declared $ALIAS_DECLINE"; exit 1; }
    if [ -z "$HOST_PREVIEW" ]; then
        echo "PREVIEW-FAIL host-derive-empty alias=$SSH_ALIAS"
        exit 1
    fi
    echo "PREVIEW|alias=${SSH_ALIAS} target=${TARGET}"
    GZ_IP="$HOST_PREVIEW" /bin/bash "$TARGET" -Preview
    exit $?
fi

if [ ! -f "$TARGET" ]; then
    # 这条专门咬"稳定副本只拷了一半"：plist 指到薄壳、薄壳却找不到被调脚本时，
    # 装上去要等到第一次定时触发才发现（而定时触发的失败又是静默的）。
    line "FAIL reason=verify_nightly_guangzhou.sh-missing path=$TARGET"
    record_line "fail" 1 "missing-target:$TARGET"
    exit 1
fi

# GZ_IP 派生（①）：只认 ssh 配置展开的真值，不接受仓库里的任何字面量。
# 先过"别名被 Host 行点名"这道前提，再看展开结果——顺序反了也没用，因为未点名的别名
# 展开值就是别名本身（非空），第二步永远看不出问题。
if ! alias_declared; then
    line "FAIL reason=alias-not-declared ${ALIAS_DECLINE}（拒绝回落到把别名当主机名去连）"
    record_line "fail" 1 "alias-not-declared:$ALIAS_DECLINE"
    exit 1
fi
GZ_HOST="$(ssh -G "$SSH_ALIAS" 2>/dev/null | awk '/^hostname /{print $2; exit}')"
if [ -z "$GZ_HOST" ]; then
    line "FAIL reason=host-derive-empty alias=${SSH_ALIAS}（~/.ssh/config 里没有该 Host 行，拒绝回落到写死地址）"
    record_line "fail" 1 "host-derive-empty"
    exit 1
fi
line "START host=$SSH_ALIAS log=$LOG record=$RECORD timeout=${VERIFY_TIMEOUT_SEC}s target=$TARGET"

if command -v timeout >/dev/null 2>&1; then
    RUNNER=(timeout "$VERIFY_TIMEOUT_SEC")
    line "RUNNER=timeout"
elif command -v perl >/dev/null 2>&1; then
    RUNNER=(perl -e 'alarm shift; exec @ARGV' "$VERIFY_TIMEOUT_SEC")
    line "RUNNER=perl-alarm"
else
    RUNNER=()
    line "WARN runner=none（本机既无 timeout 也无 perl，超时封顶失效——请装 coreutils）"
fi

# 只把主机名交给被调脚本（它自己按 GZ_IP 拼 ssh/scp），口令/凭据一律不经过这里。
if [ ${#RUNNER[@]} -gt 0 ]; then
    GZ_IP="$GZ_HOST" "${RUNNER[@]}" /bin/bash "$TARGET" >>"$LOG" 2>&1
else
    GZ_IP="$GZ_HOST" /bin/bash "$TARGET" >>"$LOG" 2>&1
fi
rc=$?
# 从输出里摘那条"== 结果：N 通过 / M 失败"读数进留档：只带计数，不带明细（明细已在 $LOG 里，
# 而留档行会被另一条腿 tail 出来读，越短越不会被折行/码页问题咬到）。
detail="$(grep -a '结果：' "$LOG" | tail -1 | tr -d '\r' | sed -n 's/.*结果：//p' | head -c 120)"
[ -n "$detail" ] || detail="rc=$rc no-result-line"
if [ "$rc" -eq 0 ]; then
    line "DONE rc=0（七腿全绿；${detail}）"
    record_line "ok" 0 "$detail"
else
    line "FAIL rc=${rc}（详见 $LOG 末尾；留档已写，拉取腿的新鲜度判据会看到它）"
    record_line "fail" "$rc" "$detail"
fi
exit "$rc"
