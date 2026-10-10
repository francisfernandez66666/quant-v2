#!/usr/bin/env bash
# check_mac_ops_watchdog.sh — Mac 自动化的**日频看门狗**（§MAC-WATCHDOG，2026-10-10）。
#
# 存在理由（两条账一起收，都是"有脚本无调度"那一族的最后一块）：
#   ① **拉取腿自己没有看门狗**。§0929DRILL 装的那台"死调度探测器"（restic_pull_backup.sh 的
#      record_freshness）反查的是演练与夜间验收两个**新**调度，而它本身挂在拉取腿上——
#      于是"拉取腿多久没跑成"这件事在系统里**没有任何读数**：09-16~09-25 那次异地备份断更十天，
#      坏的不是判据而是没人触发判据，那次是靠人手翻日志才发现的（§OPS-CLOSEOUT）。
#      把心跳反查做进被它反查的那条腿没有意义（它死了反查也死了），必须挂在**另一条**每天必然跑成的腿上。
#   ② **漂移探测器只有人坐在前面才跑**。§MAC-DRIFT 那次实测 `pairs=11 same=6 diff=3 missing_mirror=2`，
#      修法面是 `install_mac_*_agent.sh -Apply`——只有人跑才有下一轮；而"副本读的是旧代码"这一族
#      连"下一次部署会带上"都没有（广州侧还有 `-s` 推平脚本面，Mac 侧没有对应通道）。
#      它当时的台账挂法是"先推平到 same=11 再谈调度"（前提：一挂就天天红在待处置的健康面上＝
#      §107 DRILL-C 那一课），2026-10-10 当日 owner 当面把三个安装器 -Apply 跑完、实测 `same=11`
#      ⇒ 前提落地，这条调度现在挂得起了。
#
# 为什么是**新的一条调度**而不是塞进夜间验收薄壳（run_nightly_verify.sh）：
#   看门狗不能和它要看的对象共享失效域。夜间七腿自己就是被拉取腿反查的对象之一，
#   把"拉取腿的心跳"塞进夜间腿＝两条互相依赖的腿共用一个触发器，夜间腿哪天没起来，
#   拉取腿的心跳也一起静默——正是本文件要消灭的那个形态。11:00 这一档排在
#   备份两时点（07:00 / 10:30）与夜间验收（09:20）之后、演练（周日 09:00，封顶 09:40）之外，
#   不与任何一条抢本地 restic 仓库的排他锁。
#
# 只读纪律：本脚本**一个字节都不写**除以下两处以外的地方——
#   自己的日志 ~/backups/quant/watchdog.log、自己的留档 watchdog_record.jsonl（留档是"这次真跑了"的证据，
#   与演练/夜间同姿势：不写留档就没人能反查这条调度本身死没死）。
#   漂移探测那条腿走的还是 check_mac_agent_drift.sh 的只读实现（本脚本不重写第二份字节比较）。
#
# 用法：
#   deploy/mac/check_mac_ops_watchdog.sh                    # 正常跑（定时任务用的就是这一条）
#   QUIET=1 deploy/mac/check_mac_ops_watchdog.sh            # 只打汇总与 WATCHDOG| 行，逐对明细留给日志
#   QUANT_REPO_ROOT=/path/to/repo deploy/mac/check_mac_ops_watchdog.sh
#
# 退出码语义（外层与门禁行为腿都按它分流，"读不出"绝不等"没问题"）：
#   0 = 两条腿都判 ok（心跳在阈内、副本零漂移）
#   1 = 至少一条腿判出事（心跳超龄/没有心跳/有副本漂移）——这是**要人处理**的形态
#   2 = 至少一条腿**读不出**（留档时间戳解析不了、仓库根不存在、探测器自己退 2、日志不可读）
#       ⇒ 探测器自己坏了不许报"一切正常"（§MAC-DRIFT 的下限退 2 同姿势，本仓反复锤的空 glob 静默空转）
#
# 取向三条（都写在读数文案里，别让下一个人重新推）：
#   - 两种相反成因不共用一个态：「日志里没有成功锚」＝任务跑了但每次都失败（去看失败原因），
#     「日志文件压根不存在」＝任务从来没被拉起来过（去查 launchd），修法完全不同，合在一起就白查。
#   - 阈值走 env 且文案与判据同源（PULL_MAX_AGE_HOURS / DRIFT 那条不看阈值只看探测器自己的 rc）。
#   - 告警发不出去必须留痕：没主题时**不发请求**、整条正文落日志（ALERT-NOT-SENT），
#     与拉取腿/演练腿同一个姿势（打到 ntfy.sh 根路径回 404 再报"网络？"会把配置缺失伪装成网络抖动）。
set -uo pipefail

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="${HOME:-$(eval echo "~")}"

LOG_DIR="${LOG_DIR:-$HOME_DIR/backups/quant}"
PULL_LOG="${PULL_LOG:-$HOME_DIR/backups/quant/pull_backup.log}"
LOG="$LOG_DIR/watchdog.log"
RECORD="$LOG_DIR/watchdog_record.jsonl"
PULL_SUCCESS_ANCHOR="${PULL_SUCCESS_ANCHOR:-=== 备份成功 ===}"
PULL_MAX_AGE_HOURS="${PULL_MAX_AGE_HOURS:-30}"
# 拉取腿每天 07:00 与 10:30 两个时点 ⇒ 30h 的界意味着"整整一天两窗皆没跑成"才吵，
# 机器睡着错过一次不该吵（§MAC-WAKE 实录：唤醒窗口里一秒即败是常态，重试才是修法）。

# 仓库根：漂移探测器要把"仓库源"与"稳定副本"两边都拿到才比得出。稳定副本目录里反推出来的
# 是本机的镜像根（~/backups/quant/watchdog/../..），拿它当仓库根会得到一片 missing-repo 假红，
# 所以这里按**安装器写在 plist 之外的同一条路径**给缺省值，并允许 env 覆盖；
# 前提是这条目录真实存在且里面有那个探测器——不存在就判"读不出"（退 2），绝不判"没有漂移"。
QUANT_REPO_ROOT="${QUANT_REPO_ROOT:-$HOME_DIR/Desktop/quant-trading-v2}"
DRIFT_SCRIPT="${DRIFT_SCRIPT:-$SELF_DIR/check_mac_agent_drift.sh}"

QUIET="${QUIET:-0}"
mkdir -p "$LOG_DIR" 2>/dev/null || true
ts() { date '+%Y-%m-%d %H:%M:%S'; }
line() {
    # 日志不可写不是可以忽略的噪声：留档与日志是这个调度唯一的"真跑过"证据面，
    # 写不进去就等于本文件第①条存在理由里那个"没人触发判据"的形态。显式留痕并抬退 2。
    local msg
    msg="$(ts) $*"
    printf '%s\n' "$msg"
    printf '%s\n' "$msg" >>"$LOG" 2>/dev/null || printf '%s\n' "WARN log-unwritable path=$LOG"
}

NTFY_URL="${NTFY_URL:-https://ntfy.sh}"
# 发送重试：ntfy.sh 从本机网络有「TLS 握手与 HTTP/2 流都开成功、请求后被对端重置」的间歇性断连
# （2026-10-10 看门狗首跑实录：几分钟内 200/56 交替，与 launchd/curl 二进制/标题编码均无关）。
# 一次判死＝断网窗口里告警整批丢；只重试**失败**，成功路径仍恰一次外呼（门禁 WD2 钉着这个数）。
NTFY_ALERT_ATTEMPTS="${NTFY_ALERT_ATTEMPTS:-3}"
NTFY_ALERT_BACKOFF_S="${NTFY_ALERT_BACKOFF_S:-5}"
# shellcheck source=ntfy_topic.sh
NTFY_LIB="$SELF_DIR/ntfy_topic.sh"
TOPIC_SOURCE="none"
if [ ! -f "$NTFY_LIB" ]; then
    # 取不到"取主题的代码"＝这条腿的报警器被拆了还不自知（§N-6/§M2 降级不得报成功同族）。
    # 不当 WARN 处理：本脚本唯一的对外出口就是 ntfy，缺 lib 时它报出来的绿是假绿。
    printf '%s\n' "$(ts) FATAL 缺 ${NTFY_LIB}——ntfy_topic.sh 必须和本脚本一起拷进稳定副本（见 install_mac_watchdog_agent.sh）"
    printf '%s\n' "$(ts) WATCHDOG|overall=broken reason=ntfy-lib-missing"
    exit 2
fi
# shellcheck source=/dev/null
. "$NTFY_LIB"
NTFY_TOPIC="$(ntfy_topic_resolve || true)"
[ -n "$NTFY_TOPIC" ] && TOPIC_SOURCE="resolved"

alert() { # 关键事件推 ntfy（正文不回显主题值，也不带任何凭据）
    local title="$1" body="$2" pri="${3:-medium}"
    if [ -z "$NTFY_TOPIC" ]; then
        line "ALERT-NOT-SENT reason=no-topic title=$title body=$body"
        return 0
    fi
    # 重试用 if 形式而不是 `[ ] && sleep`：条件为假时后者整句返回 1，在 set -e 的调用方里就是意外中止。
    local attempt=1
    while [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; do
        if curl -fs -H "Title: $title" -H "Priority: $pri" -H "Tags: package" \
            -d "$body" "$NTFY_URL/$NTFY_TOPIC" >/dev/null 2>&1; then
            return 0
        fi
        attempt=$((attempt + 1))
        if [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; then
            sleep "${NTFY_ALERT_BACKOFF_S}"
        fi
    done
    line "ntfy 告警发送失败（已试 ${NTFY_ALERT_ATTEMPTS} 次仍失败；网络？启动行的 topic_fp 可判断配的是哪一份：$(ntfy_topic_report watchdog-alert-failure)）"
}

# 留档：纯 bash 拼 JSON（这台机器上留痕腿不能反过来把判据腿拖下水——python3 缺失时照样要记下行）。
# 文案先剥双引号/反斜杠：一行 JSON 被引号撑裂就什么都读不出来了（与 run_nightly_verify.sh 同姿势）。
record_line() {
    local res="$1" rc="$2" detail="$3"
    printf '{"ts":"%s","result":"%s","rc":"%s","source":"watchdog","detail":"%s"}\n' \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$res" "$rc" \
        "$(printf '%s' "$detail" | tr -d '"\\')" >>"$RECORD" 2>/dev/null \
        || line "WARN record-unwritable path=${RECORD}（判据结论不受影响，但拉取腿的反查会看到留档断更）"
}

# ── 腿 1：拉取腿心跳（本文件存在的第①条理由）────────────────────────────────
# 取最新一条成功锚的时间戳算年龄。锚串由 env 给（PULL_SUCCESS_ANCHOR），与拉取腿那句
# `log "=== 备份成功 ==="` 同一条来源；写死在两个文件里就是"改文案的那批不会改判据"的下一场。
PULL_VERDICT=""
PULL_AGE=""
PULL_DETAIL=""
rc_pull=0
if [ ! -e "$PULL_LOG" ]; then
    PULL_VERDICT="no-log"
    PULL_DETAIL="path=$PULL_LOG 不存在（这条腿从来没跑过：查 launchctl list | grep com.quant.backup）"
    rc_pull=2
elif [ ! -r "$PULL_LOG" ]; then
    # 可读性是独立于存在性的一条（09-26 那次 TCC 保护目录就是"文件在、读不到"）：
    # 合进 no-log 会把"权限/保护目录"伪装成"没跑过"，下一个人会去重装任务而不是查权限。
    PULL_VERDICT="log-unreadable"
    PULL_DETAIL="path=$PULL_LOG 存在但读不到（TCC 保护目录/权限；launchd 拉起的 /bin/bash 读不到 Desktop）"
    rc_pull=2
else
    last_anchor="$(grep -aF -- "$PULL_SUCCESS_ANCHOR" "$PULL_LOG" 2>/dev/null | tail -1 || true)"
    if [ -z "$last_anchor" ]; then
        PULL_VERDICT="no-anchor"
        PULL_DETAIL="path=$PULL_LOG 可读、但没有一条「${PULL_SUCCESS_ANCHOR}」＝任务被拉起来过却每次都失败，看该日志末尾的 ERROR/ALERT 行"
        rc_pull=1
    else
        # 一条捕获组搞定，不套两层：BSD sed（macOS 系统那份）对"两个 \( 只有一个 \)"的写法直接
        # 报 RE error: parentheses not balanced，本批第一次真拨就是这么把整条腿打成 unparsable 的
        # （fail-closed 的形态本身是对的——红落在"读不出"而不是"没问题"，但读不出≠没问题要能自己修）。
        anchor_ts="$(printf '%s' "$last_anchor" | sed -n 's/^\([0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}[ T][0-9]\{2\}:[0-9]\{2\}:[0-9]\{2\}\).*/\1/p' | head -n 1)"
        if [ -z "$anchor_ts" ]; then
            PULL_VERDICT="unparsable"
            PULL_DETAIL="锚行开头取不到时间戳 line=$(printf '%s' "$last_anchor" | tr -d '\r' | head -n 1)"
            rc_pull=2
        else
            PULL_AGE="$(python3 - "$anchor_ts" <<'PYW' 2>/dev/null || echo unparsable
import sys, datetime
# 拉取腿的 log() 用的是**本地** date 格式（不是 date -u 的 UTC 串），所以这里与本地 now 比。
# 截到 19 位再解析、不把 Z 放进格式串：§0929DRILL 那条假告警就是踩在"格式串里有 Z 而串被截掉了 Z"，
# 解析失败一旦落进 || 兜底的大数，就等于"读不出"冒充成"超龄"，两周后通知被关掉、探测器自己变噪声源。
fmt = "%Y-%m-%d %H:%M:%S"
t = datetime.datetime.strptime(sys.argv[1].replace("T", " "), fmt)
print(round((datetime.datetime.now() - t).total_seconds() / 3600.0, 1))
PYW
)"
            if [ "$PULL_AGE" = "unparsable" ]; then
                PULL_VERDICT="unparsable"
                PULL_DETAIL="python3 解析失败 ts=${anchor_ts}（这条腿的判据已失效，先修读法再谈备份）"
                rc_pull=2
            elif awk "BEGIN{exit !($PULL_AGE > $PULL_MAX_AGE_HOURS)}" 2>/dev/null; then
                PULL_VERDICT="stale"
                PULL_DETAIL="最近一次成功距今 ${PULL_AGE}h > ${PULL_MAX_AGE_HOURS}h（两窗皆没跑成；现网侧快照在不在跑须按那台机的读数另证）"
                rc_pull=1
            else
                PULL_VERDICT="ok"
                PULL_DETAIL="最近一次成功距今 ${PULL_AGE}h <= ${PULL_MAX_AGE_HOURS}h"
            fi
          fi
    fi
fi
line "WATCHDOG|pull verdict=$PULL_VERDICT threshold_h=${PULL_MAX_AGE_HOURS} age_h=${PULL_AGE:-none} detail=$PULL_DETAIL"
if [ "$rc_pull" != "0" ]; then
    alert "quant 备份拉取腿心跳异常（${PULL_VERDICT}）" "$PULL_DETAIL 日志=$PULL_LOG" high
fi

# ── 腿 2：副本漂移（本文件存在的第②条理由；判决全在探测器自己手里）─────────
rc_drift=0
DRIFT_SUMMARY="not-run"
DRIFT_VERDICT=""
if [ ! -d "$QUANT_REPO_ROOT" ]; then
    DRIFT_VERDICT="repo-root-absent"
    DRIFT_SUMMARY="repo_root=$QUANT_REPO_ROOT"
    rc_drift=2
elif [ ! -f "$DRIFT_SCRIPT" ]; then
    DRIFT_VERDICT="probe-absent"
    DRIFT_SUMMARY="script=$DRIFT_SCRIPT"
    rc_drift=2
else
    drift_out="$(REPO_ROOT="$QUANT_REPO_ROOT" /bin/bash "$DRIFT_SCRIPT" -Json 2>&1)"
    rc_drift=$?
    DRIFT_SUMMARY="$(printf '%s\n' "$drift_out" | grep -a '^DRIFT-SUMMARY|' | tail -1 || true)"
    [ -n "$DRIFT_SUMMARY" ] || DRIFT_SUMMARY="no-summary rc=$rc_drift"
    case "$rc_drift" in
        0) DRIFT_VERDICT="ok" ;;
        1) DRIFT_VERDICT="drift" ;;
        *) DRIFT_VERDICT="probe-broken" ;;
    esac
fi
line "WATCHDOG|drift verdict=${DRIFT_VERDICT} repo_root=$QUANT_REPO_ROOT summary=$DRIFT_SUMMARY"
if [ "$rc_drift" != "0" ]; then
    if [ "$rc_drift" = "1" ]; then
        alert "quant Mac 副本漂移（调度执行的不是仓库那份）" "$DRIFT_SUMMARY 修法：对应 install_mac_*_agent.sh 先跑一次缺省预览再 -Apply（探测器只读不写）" medium
    else
        alert "quant Mac 漂移探测器自己读不出（${DRIFT_VERDICT}）" "$DRIFT_SUMMARY 这条不是「没有漂移」——是这条判据当前失效，先修探测器" high
    fi
fi

# ── 汇总：取两条腿里最坏的语义（2=读不出 > 1=有事 > 0=都好）─────────────────
rc=0
[ "$rc_pull" = "1" ] && rc=1
[ "$rc_drift" = "1" ] && rc=1
{ [ "$rc_pull" = "2" ] || [ "$rc_drift" = "2" ]; } && rc=2

res="ok"
[ "$rc" = "1" ] && res="alert"
[ "$rc" = "2" ] && res="broken"
# ⚠ 这一段在 `set -u` 下**一个变量名都不能写错**：本批第一次真拨时这里把 PULL_VERDICT 打成
#   PULL_VERDIFT，于是 unbound variable 直接在"两条腿都判完、准备落汇总行与留档"的位置退 1——
#   所有态都退 1、留档一个字都不写、汇总行永远不出现，而两条腿各自的 WATCHDOG| 行看着全是绿的。
#   这类"尾巴上的笔误"只能靠**每条行为腿都断言 overall 行在位且留档非空**来拦（§110 的 WD 组就是这么配的），
#   光看退出码会把它读成"有事"、光看腿级读数会把它读成"没事"。
DETAIL="pull=$PULL_VERDICT drift=${DRIFT_VERDICT} age_h=${PULL_AGE:-none} topic=${TOPIC_SOURCE}"
line "WATCHDOG|overall=$res rc=$rc $DETAIL"
# 主题取不到也要在日志里吵一次（只报长度/指纹，永不回显明文）：
# 看门狗全部的对外价值都在这一条推送上，它哑了和"两条腿都是绿的"在现象上一模一样。
[ -n "$NTFY_TOPIC" ] || line "WARN $(ntfy_topic_report watchdog-preflight)⇒ 本次异常只落日志不推送"
record_line "$res" "$rc" "$DETAIL"
exit "$rc"
