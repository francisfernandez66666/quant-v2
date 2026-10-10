#!/bin/bash
# install_mac_watchdog_agent.sh — Mac 日频看门狗定时任务的安装/升级器（§MAC-WATCHDOG，2026-10-10）。
#
# 这条调度补的是哪一块（两条账，都写在被调脚本的文件头，这里只讲安装面）：
#   ① 拉取腿自己没有看门狗：§0929DRILL 那台死调度探测器挂在拉取腿上，反查演练与夜间验收，
#      而"拉取腿多久没跑成"在整个系统里没有读数——09-16~09-25 断更十天就是靠人翻日志才发现的。
#      看门狗必须挂在**另一条**每天必然跑成的腿上（挂在被看对象身上等于没看）。
#   ② 漂移探测器只有人坐在前面才跑：§MAC-DRIFT 首跑实测 `pairs=11 same=6 diff=3 missing_mirror=2`，
#      而它当时不能挂调度——一挂就天天红在待处置的健康面上（§107 DRILL-C 那一课）。
#      2026-10-10 owner 当面把三个安装器 -Apply 跑完、实测 `same=11` DRIFT_RC=0 ⇒ 前提落地。
#
# 稳定副本为什么是"同目录三件"而不是镜像树：
#   check_mac_ops_watchdog.sh 按 **同目录** source ntfy_topic.sh（主题的单一取用口径），
#   并按 **同目录** 调 check_mac_agent_drift.sh（漂移判决的唯一实现，这里不重写第二份）。
#   少拷任何一个，11:00 那一次就是当场 FATAL/退 2——而定时触发的失败是静默的，
#   所以这四个文件（三件脚本 + plist）的 say/cp/chmod/存在性自检四件一起配齐，
#   缺一件都要等到第一次触发才发现（清单式锁复发的形态，本仓已付过三次账）。
# TCC 同族约束照抄：launchd 拉起的 /bin/bash 读不到 Desktop（保护目录），稳定副本放 ~/backups/quant/watchdog/。
#
# 用法：
#   ./install_mac_watchdog_agent.sh              # 只打印将做什么（预览，零改动）
#   ./install_mac_watchdog_agent.sh -Apply       # 安装/更新脚本与 plist 并重载任务
#   ./install_mac_watchdog_agent.sh -Apply -Kick # 上面做完再立即补跑一次（验证通道活）
set -euo pipefail

REPO_MAC_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${REPO_MAC_DIR}/../.." && pwd)"
WD_HOME="$HOME/backups/quant/watchdog"
AGENT="$HOME/Library/LaunchAgents/com.quant.watchdog.plist"
# 被调脚本里那条"仓库根"的缺省值（漂移探测器要拿仓库源与稳定副本两边比字节）。
# 这里**不把它抄进 plist**，而是在安装期核"安装器自己所在的那份仓库 == 那个缺省路径"：
# 两处各写一份路径正是本仓反复锤的分叉形态；核不过就拒绝安装并给正确改道（env 覆盖是设计好的口子）。
WD_DEFAULT_REPO="$HOME/Desktop/quant-trading-v2"
APPLY=0; KICK=0
for a in "$@"; do
  case "$a" in
    -Apply) APPLY=1 ;;
    -Kick)  KICK=1 ;;
    *) echo "未知参数：${a}（只认 -Apply / -Kick）" >&2; exit 1 ;;
  esac
done

say() { echo "  $*"; }
echo "==> 计划"
say "稳定副本 : ${REPO_MAC_DIR}/check_mac_ops_watchdog.sh → ${WD_HOME}/check_mac_ops_watchdog.sh"
say "           ${REPO_MAC_DIR}/ntfy_topic.sh            → ${WD_HOME}/ntfy_topic.sh（告警主题取用单实现）"
say "           ${REPO_MAC_DIR}/check_mac_agent_drift.sh → ${WD_HOME}/check_mac_agent_drift.sh（漂移判决单实现，本壳只调不重写）"
say "任务 plist : ${REPO_MAC_DIR}/com.quant.watchdog.plist → ${AGENT}（先备份旧份）"
say "调度       : 每日 11:00（排在 07:00/10:30 拉取、09:20 夜间验收、周日 09:00 演练之后，不与它们抢窗口）"
say "仓库根     : ${WD_DEFAULT_REPO}（被调脚本的缺省值；核不过就拒绝安装，见上面注释）"
say "留档       : ~/backups/quant/watchdog_record.jsonl（每次一行 JSON，失败也写；由拉取腿反查新鲜度）"
say "重载       : launchctl bootout（存在则）→ bootstrap →（-Kick 时）kickstart 立即跑一次"
[ "$APPLY" = "1" ] || { echo "预览模式：本机一个字节没动。动手请加 -Apply。"; exit 0; }

# 前置体检：三条依赖都在装机期拦，别等 11:00 静默失败。
command -v python3 >/dev/null 2>&1 || { echo "X 缺 python3：心跳年龄那条腿按它解析时间戳，缺了只能读出 unparsable（判 2）" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "X 缺 curl：这条调度的唯一对外出口就是 ntfy 推送" >&2; exit 1; }
[ -f "${REPO_ROOT}/deploy/mac/check_mac_agent_drift.sh" ] || { echo "X 仓库里找不到 check_mac_agent_drift.sh（探测器与被调脚本是一对）" >&2; exit 1; }
if [ "$REPO_ROOT" != "$WD_DEFAULT_REPO" ]; then
  echo "X 本仓库根 $REPO_ROOT 不等于被调脚本里那条缺省仓库根 $WD_DEFAULT_REPO" >&2
  echo "  ⇒ 11:00 那次会按缺省路径找不到仓库根，漂移那条腿判 2（读不出，不是没有漂移）。" >&2
  echo "  改道二选一：① 在 ${AGENT} 的 EnvironmentVariables 里加 QUANT_REPO_ROOT=$REPO_ROOT 后重装；" >&2
  echo "              ② 把检出目录放回缺省路径。本安装器不代你写 plist，避免两处各定一份路径。" >&2
  exit 1
fi
# 告警主题取不到就**拒绝安装**（与备份/演练两个安装器同一条理由）：这条调度的全部价值是
#   "别人没跑成的时候有人知道"，装上去推不出通知＝把报警器拆了还不自知（§N-6/§M2 同族）。
# shellcheck source=ntfy_topic.sh
. "${REPO_MAC_DIR}/ntfy_topic.sh"
if ! ntfy_topic_report watchdog-installer-preflight; then
  echo "X 取不到 ntfy 主题（env NTFY_TOPIC 未给，钥匙串 ${NTFY_KEYCHAIN_ITEM:-quant-ntfy-topic} 也没有）。" >&2
  echo "  落值只走一个入口：printf '%s' '<主题>' | deploy/mac/migrate_ntfy_topic_to_keychain.sh --from-stdin -Apply" >&2
  echo "  （别手敲 security add-generic-password … -w 不带值那条提示式写法：它 prompt 的是终端而不是管道，" >&2
  echo "    值会被丢掉、条目照样建出来但口令是空的，2026-10-10 本机真踩过一次）" >&2
  exit 1
fi

mkdir -p "$WD_HOME"
cp "${REPO_MAC_DIR}/check_mac_ops_watchdog.sh" "${WD_HOME}/check_mac_ops_watchdog.sh"
cp "${REPO_MAC_DIR}/ntfy_topic.sh" "${WD_HOME}/ntfy_topic.sh"
cp "${REPO_MAC_DIR}/check_mac_agent_drift.sh" "${WD_HOME}/check_mac_agent_drift.sh"
chmod +x "${WD_HOME}/check_mac_ops_watchdog.sh" "${WD_HOME}/ntfy_topic.sh" "${WD_HOME}/check_mac_agent_drift.sh"

# 装完当场自证**稳定副本**三件都在（不是仓库源都在）：少一件就拒绝重载任务。
[ -f "${WD_HOME}/check_mac_ops_watchdog.sh" ] || { echo "X 稳定副本缺 check_mac_ops_watchdog.sh" >&2; exit 1; }
[ -f "${WD_HOME}/ntfy_topic.sh" ] \
  || { echo "X 稳定副本缺 ntfy_topic.sh（被调脚本按同目录 source 它，缺了当场 FATAL 退 2）" >&2; exit 1; }
[ -f "${WD_HOME}/check_mac_agent_drift.sh" ] \
  || { echo "X 稳定副本缺 check_mac_agent_drift.sh（漂移那条腿会读成 probe-absent＝整条判据失效）" >&2; exit 1; }

# plist 的 ProgramArguments 必须指稳定副本里的那份（模板没跟着改就拒绝装旧指桌面的版本）。
if ! grep -q "${WD_HOME}/check_mac_ops_watchdog.sh" "${REPO_MAC_DIR}/com.quant.watchdog.plist"; then
  echo "X 仓库 plist 模板的脚本路径不是 ${WD_HOME}/…（模板与被调脚本不同源，拒绝安装）" >&2
  exit 1
fi
[ -f "$AGENT" ] && cp "$AGENT" "$AGENT.bak-$(date +%Y%m%d-%H%M%S)"
cp "${REPO_MAC_DIR}/com.quant.watchdog.plist" "$AGENT"

launchctl bootout "gui/$(id -u)/com.quant.watchdog" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$AGENT"
echo "INSTALLED label=com.quant.watchdog script=${WD_HOME}/check_mac_ops_watchdog.sh log=$HOME/backups/quant/watchdog.log record=$HOME/backups/quant/watchdog_record.jsonl"

if [ "$KICK" = "1" ]; then
  launchctl kickstart "gui/$(id -u)/com.quant.watchdog"
  echo "KICKED 跑动中：两条腿都是只读（读心跳日志 + 比副本字节），秒级完成，"
  echo "        读数看 ~/backups/quant/watchdog.log 的 WATCHDOG|overall= 那一行，退出码 0/1/2 三态。"
fi
