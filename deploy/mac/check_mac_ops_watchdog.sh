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
#   0 = 四条腿都判 ok（心跳在阈内、副本零漂移、退役调度确不在位、库新度读得出）
#   1 = 至少一条腿判出事（心跳超龄/没有心跳/有副本漂移/退役任务复活）——这是**要人处理**的形态
#   2 = 至少一条腿**读不出**（留档时间戳解析不了、仓库根不存在、探测器自己退 2、日志不可读、
#       launchctl 给不出确定答案、本地行情库读不到）
#       ⇒ 探测器自己坏了不许报"一切正常"（§MAC-DRIFT 的下限退 2 同姿势，本仓反复锤的空 glob 静默空转）
#
# ★ 腿 3/4 的由来（§MAC-SYNC-RETIRE，2026-10-11，owner 当面裁"整条停掉"）：
#   `com.quant.syncdata`（本地 baostock 下载→推送云端）已退役，退役的依据不是"最近老失败"，
#   而是**它在整机生命周期里一次都没成功过**：
#     · 该任务自己那份 stdout 留档 168 行里，"云端 max:" 出现 **0 次**、"同步完成" 出现 **0 次**
#       （第 3 步之后就没往下走过，六步链从未走到第 6 步）；
#     · 同一份 stderr 里 `sqlite3.OperationalError: unable to open database file` 出现 **21 次**
#       ＝最近 7 个交易日 × 每次 3 试（脚本自带重试），launchd 侧 `last exit code = 1`；
#     · 根因在目标机：脚本默认写死的云端数据目录在那台机上**不存在**（只剩一个 8 月 13 日的
#       `.bak-<epoch>` 残骸，里面装的是另一个应用的配置），`/opt` 下现在的目录属于别的服务，
#       quant 的服务、二进制、数据目录一概不在——这台机 8 月中旬就改行了，而链路没人下线，
#       于是每天 15:20 照样起来、照样跑 37 分钟到 6 小时 25 分的真实下载（daily 单轮最长实录），
#       再照样撞在第 3 步退 1。**烧的是真资源，红的是零人知晓**。
#   为什么"没人知晓"这一半才是本条要收的东西：这台机上其余五条 quant 调度全都在看门狗或门禁的
#   覆盖面里，只有这条——`syncdata` 三字在 deploy/mac/*.sh 与 scripts/verify_changes.sh 里
#   **一次都检索不到**——它每天失败、失败七天、无人被告知。这正是本周刚收口那一族
#   （有调度、恒失败、无读数）在本机的最后一块，也是它自己而不是被读码想出来的。
#   所以退役之后**不能只是删掉调度**：删掉之后这条链就什么都没有了，下一个人（或下一次的
#   install_sync_agent.sh）把它重新挂上时，没人会知道它是被有意停掉的。腿 3 就是把"不许复活"
#   变成一条每天有人验的判据；腿 4 把"本地行情库还剩多新"变成读数——下载停了，这面镜子
#   从此只能靠人，而"靠人"必须是个看得见的数，不能是"下次发现时已经停了两个月"
#   （实录：本机 index_daily 的最大交易日停在 2026-08-17，而同一库的 daily 停在 2026-10-09，
#    两个数差着近两个月且当时无人知道——本批只把它摆成读数，断更成因另立一批，不混账）。

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
# 腿 3 的两个对象名都可覆盖：退役的那条调度在别的机器/别的标签上，判据要能跟着挪，
# 而不是把标签写死成第二份来源（写死之后改名的那一天这条腿就恒绿了）。
SYNC_RETIRE_LABEL="${SYNC_RETIRE_LABEL:-com.quant.syncdata}"
SYNC_RETIRE_PLIST="${SYNC_RETIRE_PLIST:-$HOME_DIR/Library/LaunchAgents/${SYNC_RETIRE_LABEL}.plist}"
# 腿 4：本地行情库的新度。默认**只出读数不判红**（DATA_FRESH_ALERT_DAYS=0）——下载链已退役，
# 新度不再自动前进是**既定态**，把它判成红就等于给一条 owner 有意关掉的链天天发噪声（§107 DRILL-C
# 那一课：新判据一挂就恒红在待处置面上＝探测器自杀成噪声源）。要抬成判红请显式给天数。
LOCAL_DB="${LOCAL_DB:-$HOME_DIR/.quant-trading-v2/trading.db}"
DATA_FRESH_ALERT_DAYS="${DATA_FRESH_ALERT_DAYS:-0}"
DATA_FRESH_TABLES="${DATA_FRESH_TABLES:-daily,stk_limit,index_daily}"
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
    local title="$1" body="$2" pri_raw="${3:-high}" pri rc=0
    if [ -z "$NTFY_TOPIC" ]; then
        line "ALERT-NOT-SENT reason=no-topic title=$title body=$body"
        return 0
    fi
    # §NTFY-PRI（2026-10-15）：词表与"一次拨号"都在 ntfy_topic.sh 里单实现（与拉取腿同一份）。
    #   本函数的默认值过去写 medium——那不是 ntfy 认的词（上游回 400 invalid priority parameter），
    #   于是这条腿的告警从装机起 100% 发不出去，而失败文案是「网络？」，10-12 批的"修法"又是
    #   给必然 400 的请求加三次重试。今天补齐两刀：默认值改 high；非法值兜到 high 并**点名原值**，
    #   绝不因为一个拼错的词把通知打死（发不出去才是真损失），也不静默兜底（日志里查得到是谁传的）。
    rc=0
    pri="$(ntfy_priority_normalize "$pri_raw")" || rc=$?
    if [ "$rc" -ne 0 ]; then
        line "WARN ntfy Priority 非法值已兜底 raw=${pri_raw} used=${pri}（词表见 ntfy_topic.sh；这条告警照发，不是降级）"
    fi
    # 重试用 if 形式而不是 `[ ] && sleep`：条件为假时后者整句返回 1，在 set -e 的调用方里就是意外中止。
    local attempt=1
    while [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; do
        rc=0
        ntfy_attempt line "$NTFY_URL" "$NTFY_TOPIC" "$title" "$pri" "package" "$body" watchdog || rc=$?
        if [ "$rc" -eq 0 ]; then
            return 0
        fi
        # rc=2 ⇒ 上游回 4xx：请求本身不合格，重试只是把同一条错误请求重放。
        #   ntfy_attempt 已把 http 码与响应体摘要打进日志，这里直接收手（网络抖动那一族才配重试）。
        if [ "$rc" -eq 2 ]; then
            return 0
        fi
        attempt=$((attempt + 1))
        if [ "${attempt}" -le "${NTFY_ALERT_ATTEMPTS}" ]; then
            sleep "${NTFY_ALERT_BACKOFF_S}"
        fi
    done
    line "ntfy 告警发送失败（已试 ${NTFY_ALERT_ATTEMPTS} 次仍失败，均为传输层错误/5xx；非 4xx——4xx 会在上面那条 http= 行里点名。启动行的 topic_fp 可判断配的是哪一份：$(ntfy_topic_report watchdog-alert-failure)）"
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
        alert "quant Mac 副本漂移（调度执行的不是仓库那份）" "$DRIFT_SUMMARY 修法：对应 install_mac_*_agent.sh 先跑一次缺省预览再 -Apply（探测器只读不写）" default
    else
        alert "quant Mac 漂移探测器自己读不出（${DRIFT_VERDICT}）" "$DRIFT_SUMMARY 这条不是「没有漂移」——是这条判据当前失效，先修探测器" high
    fi
fi

# ── 腿 3：已退役调度不得复活（§MAC-SYNC-RETIRE 的第①半：把"有意停掉"变成有人每天验的判据）──
# 三种"不在位"要分开，因为它们修法相反：
#   · loaded ＝这次会话里又被挂上去了（有人跑了安装器 / 有人手工 bootstrap）→ 判红并说清是退役链
#   · plist-in-place 且没加载 ＝文件回来了但还没起来 → **也判红**：下次登录 launchd 就把它挂回去，
#     而这条链一挂就是每天几十分钟到几小时的真实下载 + 一次必撞的死路，等它自己跑起来才发现太晚。
#   · launchctl 给不出确定答案（命令本身失败／输出里没有那句"找不到服务"）→ 退 2，
#     绝不因为"看起来没在跑"就报 ok（读不出≠没复活，这条是本仓所有负向判据的下限）。
rc_retired=0
RET_VERDICT=""
RET_DETAIL=""
if ! command -v launchctl >/dev/null 2>&1; then
    RET_VERDICT="probe-broken"
    RET_DETAIL="本机没有 launchctl 命令——这条腿无从判起（不是「没复活」）"
    rc_retired=2
else
    uid_num="$(id -u 2>/dev/null || true)"
    if [ -z "$uid_num" ]; then
        RET_VERDICT="probe-broken"
        RET_DETAIL="id -u 取不到 uid，print 的域拼不出来（读不出≠没复活）"
        rc_retired=2
    else
        ret_out="$(launchctl print "gui/${uid_num}/${SYNC_RETIRE_LABEL}" 2>&1)"
        ret_rc=$?
        if [ "$ret_rc" = "0" ]; then
            RET_VERDICT="loaded"
            RET_DETAIL="label=${SYNC_RETIRE_LABEL} 又在调度里了（这条链已于 2026-10-11 退役：目标机改行、六步链从未走通、每天真实下载 37min~6h25m 后恒退 1）"
            rc_retired=1
        elif printf '%s' "$ret_out" | grep -qiF "could not find service"; then
            if [ -f "$SYNC_RETIRE_PLIST" ]; then
                RET_VERDICT="plist-in-place"
                RET_DETAIL="本次会话没加载，但 ${SYNC_RETIRE_PLIST} 在位＝下次登录 launchd 会把它挂回去（退役要连 plist 一起摘：改名成 .retired-<日期> 或直接删）"
                rc_retired=1
            else
                RET_VERDICT="absent"
                RET_DETAIL="launchd 里没有该服务、LaunchAgents 里也没有那份 plist＝退役状态成立"
            fi
        else
            RET_VERDICT="probe-broken"
            RET_DETAIL="launchctl rc=${ret_rc} 输出里没有那句「找不到服务」，读不出在不在位 line=$(printf '%s' "$ret_out" | tr -d '\r\n' | head -c 120)"
            rc_retired=2
        fi
    fi
fi
line "WATCHDOG|retired verdict=$RET_VERDICT label=$SYNC_RETIRE_LABEL detail=$RET_DETAIL"
if [ "$rc_retired" != "0" ]; then
    if [ "$rc_retired" = "1" ]; then
        alert "quant 已退役的行情同步链回到调度里（${RET_VERDICT}）" "$RET_DETAIL 背景：云端目标机 8 月中旬改行，这条链六步从未走通；恢复它之前先确认要推的是哪台机、哪个库" default
    else
        alert "quant 退役链探针自己读不出（${RET_VERDICT}）" "$RET_DETAIL 这条不是「没复活」——是这条判据当前失效，先修判据" high
    fi
fi

# ── 腿 4：本地行情库新度只出读数（§MAC-SYNC-RETIRE 的第②半：下载停了，新度变成要靠人的数）──
# 退役前这面镜子由那条链每天推；退役后本地库不再自动前进，于是"库还剩多新"这件事第一次
# 需要被**读出来**——本机实录是 daily 到 2026-10-09 而 index_daily 停在 2026-08-17，
# 差近两个月且当时无人知道。默认 DATA_FRESH_ALERT_DAYS=0＝只读数不判红（见上面 env 处的理由）。
rc_fresh=0
FRESH_VERDICT=""
FRESH_DETAIL=""
if [ ! -e "$LOCAL_DB" ]; then
    FRESH_VERDICT="db-absent"
    FRESH_DETAIL="path=$LOCAL_DB 不存在（读不出新度；研究侧若还要用本地库，这是个真问题）"
    rc_fresh=2
elif ! command -v python3 >/dev/null 2>&1; then
    FRESH_VERDICT="probe-broken"
    FRESH_DETAIL="本机没有 python3——sqlite 读法无从执行（不是「库是新的」）"
    rc_fresh=2
else
    fresh_out="$(LOCAL_DB="$LOCAL_DB" DATA_FRESH_TABLES="$DATA_FRESH_TABLES" python3 - <<'PYF' 2>/dev/null
import os, sqlite3, datetime, re
db = os.environ["LOCAL_DB"]
tables = [t for t in os.environ.get("DATA_FRESH_TABLES", "").split(",") if t]
today = datetime.date.today()
parts = []
try:
    # mode=ro：看门狗是纯读数腿，一个字节都不许写；库被别的进程占用时 sqlite 会抛，
    # 抛了就走下面的 except 落"读不出"，不猜、也不打印半个旧值冒充现值。
    conn = sqlite3.connect("file:%s?mode=ro" % db, uri=True, timeout=10)
    # ★ 先做一次**根探针**（读 sqlite_master 一条表名），再逐表取 MAX。
    #   为什么必须有这一步（本批自己拨出来的形态）：把一个不是数据库的文件指进来时,
    #   sqlite3.connect **不抛**（惰性打开）,异常要等到第一次 execute 才出现——于是逐表的
    #   `except Exception` 会把"整份文件根本不是库"这张假牌洗成三行 `notable`,
    #   整条腿退 0 报"读数正常"。这正是本仓最恨的那一族（坏读法冒充好读数,§[xml]"" 不抛同族）。
    #   根探针只问一句"这是不是一份 sqlite"：它抛＝整份读不出（退 2）；它通过才允许下面
    #   按表区分"这张表没有/空表"（那是读数,不是腿坏）。
    conn.execute("SELECT name FROM sqlite_master WHERE type='table' LIMIT 1").fetchone()
    for t in tables:
        try:
            v = conn.execute("SELECT MAX(trade_date) FROM %s" % t).fetchone()[0]
        except Exception:
            parts.append("%s=notable" % t)
            continue
        if not v:
            parts.append("%s=empty" % t)
            continue
        s = str(v)
        m = re.search(r"(\d{4})\D?(\d{2})\D?(\d{2})", s)
        if not m:
            parts.append("%s=unparsable(%s)" % (t, s[:16]))
            continue
        d = datetime.date(int(m.group(1)), int(m.group(2)), int(m.group(3)))
        parts.append("%s=%s age_d=%d" % (t, s, (today - d).days))
    conn.close()
except Exception as e:
    print("ERR " + type(e).__name__)
else:
    print("OK " + " | ".join(parts))
PYF
)"
    case "$fresh_out" in
        OK\ *)
            FRESH_VERDICT="read"
            FRESH_DETAIL="${fresh_out#OK }"
            # 抬红只在 owner 显式给了天数阈值时才发生：按**最小的那个 age_d** 判（最陈旧的一腿）。
            if [ "$DATA_FRESH_ALERT_DAYS" != "0" ]; then
                max_age="$(printf '%s' "$FRESH_DETAIL" | grep -oE 'age_d=[0-9]+' | sed 's/age_d=//' | sort -n | tail -1)"
                if [ -n "$max_age" ] && awk "BEGIN{exit !($max_age > $DATA_FRESH_ALERT_DAYS)}" 2>/dev/null; then
                    FRESH_VERDICT="stale"
                    rc_fresh=1
                    FRESH_DETAIL="$FRESH_DETAIL 阈值 ${DATA_FRESH_ALERT_DAYS}d（最陈旧一腿 ${max_age}d）"
                fi
            fi
            ;;
        ERR\ *)
            FRESH_VERDICT="unreadable"
            FRESH_DETAIL="path=$LOCAL_DB python3 报 ${fresh_out#ERR }（库在、读不出＝锁/损坏/权限，不是「新度正常」）"
            rc_fresh=2
            ;;
        *)
            FRESH_VERDICT="probe-broken"
            FRESH_DETAIL="python3 没给出 OK/ERR 前缀 line=$(printf '%s' "$fresh_out" | tr -d '\r\n' | head -c 120)"
            rc_fresh=2
            ;;
    esac
fi
line "WATCHDOG|dbfresh verdict=$FRESH_VERDICT db=$LOCAL_DB alert_days=$DATA_FRESH_ALERT_DAYS detail=$FRESH_DETAIL"
if [ "$rc_fresh" != "0" ]; then
    alert "quant 本地行情库新度（${FRESH_VERDICT}）" "$FRESH_DETAIL 读法：sqlite mode=ro 取 MAX(trade_date)；本地下载链已于 2026-10-11 退役，新度要靠手更" high
fi

# ── 汇总：取所有腿里最坏的语义（2=读不出 > 1=有事 > 0=都好）─────────────────
rc=0
[ "$rc_pull" = "1" ] && rc=1
[ "$rc_drift" = "1" ] && rc=1
[ "$rc_retired" = "1" ] && rc=1
[ "$rc_fresh" = "1" ] && rc=1
{ [ "$rc_pull" = "2" ] || [ "$rc_drift" = "2" ] || [ "$rc_retired" = "2" ] || [ "$rc_fresh" = "2" ]; } && rc=2

res="ok"
[ "$rc" = "1" ] && res="alert"
[ "$rc" = "2" ] && res="broken"
# ⚠ 这一段在 `set -u` 下**一个变量名都不能写错**：本批第一次真拨时这里把 PULL_VERDICT 打成
#   PULL_VERDIFT，于是 unbound variable 直接在"两条腿都判完、准备落汇总行与留档"的位置退 1——
#   所有态都退 1、留档一个字都不写、汇总行永远不出现，而两条腿各自的 WATCHDOG| 行看着全是绿的。
#   这类"尾巴上的笔误"只能靠**每条行为腿都断言 overall 行在位且留档非空**来拦（§110 的 WD 组就是这么配的），
#   光看退出码会把它读成"有事"、光看腿级读数会把它读成"没事"。
DETAIL="pull=$PULL_VERDICT drift=${DRIFT_VERDICT} retired=${RET_VERDICT} dbfresh=${FRESH_VERDICT} age_h=${PULL_AGE:-none} topic=${TOPIC_SOURCE}"
line "WATCHDOG|overall=$res rc=$rc $DETAIL"
# 主题取不到也要在日志里吵一次（只报长度/指纹，永不回显明文）：
# 看门狗全部的对外价值都在这一条推送上，它哑了和"两条腿都是绿的"在现象上一模一样。
[ -n "$NTFY_TOPIC" ] || line "WARN $(ntfy_topic_report watchdog-preflight)⇒ 本次异常只落日志不推送"
record_line "$res" "$rc" "$DETAIL"
exit "$rc"
