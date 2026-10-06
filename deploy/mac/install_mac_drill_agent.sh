#!/bin/bash
# install_mac_drill_agent.sh — Mac 侧「恢复演练」定时任务的安装/升级器（§0929DRILL，2026-09-29）。
#
# 为什么现在才挂上（09-29 复核锤实的事实，不是转述）：
#   恢复演练的**脚本**从 §WS-A A1 起就存在（scripts/restore_drill.sh + deploy/mac/verify_restore.sh），
#   但**从来没有调度**：仓库里没有 drill 的 plist、没有 launchd 项、
#   而 DRILL_RECORD 这个留档旋钮是 09-29 当天才加的、也从来没有一份记录文件真实存在过。
#   「有脚本无调度」是本仓第 N 次同族（§ENH-5 网关运维脚本漏清单、§P0-B 备份脚本手工安装、
#   §0927KA keepalive 一直不在部署清单）：判据写得很完整，只是没人/没钟去触发它，
#   于是"演练过"这件事在纸面上永远成立，现场一次都没发生。
#   本脚本把这条补齐：每周日 09:00 真跑一次 verify_restore.sh（restic check + 整份恢复 + 四表可读
#   + 读写往返），并把每次结果追加进 ~/backups/quant/drill_record.jsonl 供新鲜度反查。
#
# 稳定副本必须是**镜像树**，不能像备份拉取腿那样拍平进 bin/：
#   verify_restore.sh 用 `$(dirname $0)/../..` 反推仓库根去找 scripts/restore_drill.sh，
#   拍平后那条路径指向一个不存在的位置 ⇒ 演练会在"能跑起来"之后立刻失败。
#   本脚本按 deploy/mac/ 与 scripts/ 两级目录原样拷贝，并**当场断言两半都在**（缺一半就拒绝安装，
#   而不是装上去等周日夜里第一次跑才发现）。
# TCC 同族约束照抄 install_mac_backup_agent.sh：launchd 拉起 /bin/bash 读不到 Desktop（保护目录），
#   2026-09 那次每天 07:00 静默失败 126 就是踩在这上面 ⇒ 稳定副本放 ~/backups/quant/drill/。
#
# 用法：
#   ./install_mac_drill_agent.sh              # 只打印将做什么（预览，零改动）
#   ./install_mac_drill_agent.sh -Apply       # 安装/更新脚本与 plist 并重载任务
#   ./install_mac_drill_agent.sh -Apply -Kick # 上面做完再立即补跑一次（验证通道活）
set -euo pipefail

REPO_MAC_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${REPO_MAC_DIR}/../.." && pwd)"
DRILL_HOME="$HOME/backups/quant/drill"
AGENT="$HOME/Library/LaunchAgents/com.quant.drill.plist"
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
say "稳定副本 : ${REPO_MAC_DIR}/run_drill_weekly.sh → ${DRILL_HOME}/run_drill_weekly.sh"
say "           ${REPO_MAC_DIR}/verify_restore.sh   → ${DRILL_HOME}/deploy/mac/verify_restore.sh"
say "           ${REPO_ROOT}/scripts/restore_drill.sh → ${DRILL_HOME}/scripts/restore_drill.sh"
# §KUMA-SECREDTO（2026-10-07 波 3）：ntfy 主题的缺省值已从仓库拿掉（主题＝凭据，旧值已进 git 历史），
#   verify_restore.sh 现在 source **同目录**的 ntfy_topic.sh 取主题。镜像树少拷这一个文件，
#   演练一启动就 FATAL 退出——这是「加了一条依赖却没同步到拷贝清单」的同族形态（清单式锁第三次复发），
#   所以这里 say/cp/chmod/存在性自检四件一起加，缺一件都会在周日 09:00 才第一次现形。
say "           ${REPO_MAC_DIR}/ntfy_topic.sh       → ${DRILL_HOME}/deploy/mac/ntfy_topic.sh（告警主题取用单实现）"
say "任务 plist : ${REPO_MAC_DIR}/com.quant.drill.plist → ${AGENT}（先备份旧份）"
say "调度       : 每周日 09:00（避开 07:00 的备份拉取腿；错过则唤醒后补跑）"
say "留档       : \$DRILL_RECORD 默认 ~/backups/quant/drill_record.jsonl（每次一行 JSON，失败也算）"
say "重载       : launchctl bootout（存在则）→ bootstrap →（-Kick 时）kickstart 立即跑一次"
[ "$APPLY" = "1" ] || { echo "预览模式：本机一个字节没动。动手请加 -Apply。"; exit 0; }

# 前置体检：与拉取腿同一条依赖面（restic 在 brew 路径、钥匙串条目在、演练主脚本能找到断言脚本）。
command -v restic >/dev/null 2>&1 || [ -x /opt/homebrew/bin/restic ] || { echo "X restic 未安装（brew install restic）" >&2; exit 1; }
security find-generic-password -a "$USER" -s quant-restic-repo-pass >/dev/null 2>&1 \
  || { echo "X 钥匙串缺 quant-restic-repo-pass（本脚本只查存在性，不读取口令值）" >&2; exit 1; }
[ -x /usr/bin/sqlite3 ] || command -v sqlite3 >/dev/null 2>&1 \
  || { echo "X 缺 sqlite3 CLI：restore_drill.sh 的 REQUIRE_SQLITE 默认 1，缺了必判红（不降级通过）" >&2; exit 1; }
[ -f "${REPO_ROOT}/scripts/restore_drill.sh" ] || { echo "X 仓库里找不到 scripts/restore_drill.sh" >&2; exit 1; }
# §KUMA-SECREDTO：告警主题取不到（env NTFY_TOPIC 未给且钥匙串 quant-ntfy-topic 为空）就**拒绝安装**。
#   理由与备份安装器同一条：演练的价值在「坏了有人知道」。这里必须讲清运行期兜底在哪，
#   不然会误以为"缺 lib 只是少一条推送"：
#     verify_restore.sh 查不到同目录 ntfy_topic.sh 是 **FATAL 早退**（早于 alert()、也早于
#     DRILL_RECORD 落笔），所以这一腿当场既推不出、也留不下读数；
#     兜住它的是拉取腿的 record_freshness("drill")——留档不存在或超 9 天即推
#     "quant drill(com.quant.drill) 调度疑似死了"（§0929DRILL 的死调度探测器）。
#   也就是说缺 lib 的后果=周日腿整体静默、要靠次日拉取腿的一条超龄告警才现形。既然装上去
#   就是这个形态，那就拦在装机期，让 owner 当场看到"主题取不到"这条更准的因。
# shellcheck source=ntfy_topic.sh
. "${REPO_MAC_DIR}/ntfy_topic.sh"
if ! ntfy_topic_report drill-installer-preflight; then
  echo "X 取不到 ntfy 主题（env NTFY_TOPIC 未给，钥匙串 ${NTFY_KEYCHAIN_ITEM:-quant-ntfy-topic} 也没有）。" >&2
  echo "  先落一次口令：security add-generic-password -a \"\$USER\" -s ${NTFY_KEYCHAIN_ITEM:-quant-ntfy-topic} -w" >&2
  echo "  （旧值从 git 历史迁出可用 deploy/mac/migrate_ntfy_topic_to_keychain.sh -Apply，它只报长度与指纹；" >&2
  echo "    注意旧值已进过仓库，正式口径是按已泄露处理→在 ntfy 侧建新主题后 --from-stdin 写进钥匙串）" >&2
  exit 1
fi

mkdir -p "${DRILL_HOME}/deploy/mac" "${DRILL_HOME}/scripts"
# 四个文件落在**两个不同层级**，不能一次循环搞定：
#   run_drill_weekly.sh → 稳定根（plist 直接指它）；
#   verify_restore.sh / ntfy_topic.sh → deploy/mac/ 子层（前者按 `$(dirname $0)/../..` 反推仓库根、
#     并按**同目录**source 后者，所以后者必须跟着进 deploy/mac/ 而不是稳定根）；
#   restore_drill.sh    → scripts/ 子层（就是上面那个反推的落点）。
cp "${REPO_MAC_DIR}/run_drill_weekly.sh" "${DRILL_HOME}/run_drill_weekly.sh"
cp "${REPO_MAC_DIR}/verify_restore.sh" "${DRILL_HOME}/deploy/mac/verify_restore.sh"
cp "${REPO_MAC_DIR}/ntfy_topic.sh" "${DRILL_HOME}/deploy/mac/ntfy_topic.sh"
cp "${REPO_ROOT}/scripts/restore_drill.sh" "${DRILL_HOME}/scripts/restore_drill.sh"
chmod +x "${DRILL_HOME}/run_drill_weekly.sh" "${DRILL_HOME}/deploy/mac/verify_restore.sh" \
         "${DRILL_HOME}/deploy/mac/ntfy_topic.sh" \
         "${DRILL_HOME}/scripts/restore_drill.sh"

# 装完当场自证镜像树完整（缺哪一半都拒绝重载任务）：这里断的是**稳定副本**，不是仓库源。
[ -f "${DRILL_HOME}/run_drill_weekly.sh" ] || { echo "X 稳定副本缺 run_drill_weekly.sh" >&2; exit 1; }
[ -f "${DRILL_HOME}/deploy/mac/verify_restore.sh" ] || { echo "X 稳定副本缺 deploy/mac/verify_restore.sh" >&2; exit 1; }
[ -f "${DRILL_HOME}/deploy/mac/ntfy_topic.sh" ] \
  || { echo "X 稳定副本缺 deploy/mac/ntfy_topic.sh（verify_restore.sh 在镜像里 source 的就是这一份；" >&2
       echo "    少它＝演练一启动就 FATAL，周日腿整体静默）" >&2; exit 1; }
[ -f "${DRILL_HOME}/scripts/restore_drill.sh" ] || { echo "X 稳定副本缺 scripts/restore_drill.sh（镜像树被拍平＝演练找不到断言脚本）" >&2; exit 1; }

# plist 的 ProgramArguments 必须指稳定根里的那份；仓库模板里就是稳定路径，直接拷并先校验。
if ! grep -q "${DRILL_HOME}/run_drill_weekly.sh" "${REPO_MAC_DIR}/com.quant.drill.plist"; then
  echo "X 仓库 plist 模板的脚本路径不是 ${DRILL_HOME}/…（模板没跟着改，拒绝安装旧指桌面的版本）" >&2
  exit 1
fi
[ -f "$AGENT" ] && cp "$AGENT" "$AGENT.bak-$(date +%Y%m%d-%H%M%S)"
cp "${REPO_MAC_DIR}/com.quant.drill.plist" "$AGENT"

launchctl bootout "gui/$(id -u)/com.quant.drill" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$AGENT"
echo "INSTALLED label=com.quant.drill script=${DRILL_HOME}/run_drill_weekly.sh log=$HOME/backups/quant/drill_weekly.log"

if [ "$KICK" = "1" ]; then
  launchctl kickstart "gui/$(id -u)/com.quant.drill"
  echo "KICKED 跑动中：整份 restic 恢复 + check 要几分钟，进度看 ~/backups/quant/drill_weekly.log，"
  echo "        结果锚点＝「DONE rc=0」，每次读数追加在 ~/backups/quant/drill_record.jsonl"
fi
