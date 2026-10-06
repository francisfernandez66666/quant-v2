#!/bin/bash
# install_mac_nightly_agent.sh — Mac 侧「广州夜间研究每日验收」定时任务的安装/升级器
#   （§0929NIGHTLY，2026-09-29）。与 install_mac_drill_agent.sh 同姿势，理由也同族：
#
#   ① 稳定副本必须放**非 TCC 保护目录**（~/backups/quant/nightly/），plist 不指 Desktop 仓库路径。
#      实录教训（§OPS-CLOSEOUT）：09-16~09-25 拉取腿的 plist 直指桌面仓库，launchd 拉起的
#      /bin/bash 读不到保护目录 ⇒ 每天 07:00 定时触发、每天退出码 126 静默失败、ntfy 一声不吭，
#      异地副本停在 09-15 十天才被发现。
#   ② 副本保持 **scripts/ 一层子目录**而不是拍平：run_nightly_verify.sh 按
#      `$(dirname $0)/scripts/verify_nightly_guangzhou.sh` 找被调脚本（与演练侧"按 ../.. 反推仓库根"
#      同一条依赖形状）。拍平＝任务能拉起薄壳、薄壳当场判红，而且只在第一次定时触发时才暴露。
#      本脚本装完**当场断言两半都在**，缺一半就拒绝重载任务。
#   ③ 装完做**离线自检**（-Preview 那条腿：不连现网、只打印将要执行的远端命令）——
#      证明稳定副本本身是完整可执行的，而不是"装上了等明天 09:20 才知道跑不起来"。
#
# 用法：
#   ./install_mac_nightly_agent.sh              # 只打印将做什么（预览，零改动）
#   ./install_mac_nightly_agent.sh -Apply       # 安装/更新脚本与 plist 并重载任务 + 离线自检
#   ./install_mac_nightly_agent.sh -Apply -Kick # 上面做完再立即真跑一次（连现网，七腿只读）
set -euo pipefail

REPO_MAC_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${REPO_MAC_DIR}/../.." && pwd)"
NIGHTLY_HOME="$HOME/backups/quant/nightly"
AGENT="$HOME/Library/LaunchAgents/com.quant.nightly.plist"
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
say "稳定副本 : ${REPO_MAC_DIR}/run_nightly_verify.sh        → ${NIGHTLY_HOME}/run_nightly_verify.sh"
say "           ${REPO_ROOT}/scripts/verify_nightly_guangzhou.sh → ${NIGHTLY_HOME}/scripts/verify_nightly_guangzhou.sh"
say "任务 plist : ${REPO_MAC_DIR}/com.quant.nightly.plist → ${AGENT}（先备份旧份）"
say "调度       : 每日 09:20（广州当日产出在 04:00~08:19 之间收工；错开备份腿 07:00/10:30 与演练周日 09:00）"
say "留档       : ~/backups/quant/nightly_record.jsonl（每次一行 JSON，失败也算）"
say "主机来源   : 由 ~/.ssh/config 的 gz 别名在运行期派生（仓库里不出现广州公网 IP 字面量）"
say "重载       : launchctl bootout（存在则）→ bootstrap → 离线自检 -Preview →（-Kick 时）真跑一次"
[ "$APPLY" = "1" ] || { echo "预览模式：本机一个字节没动。动手请加 -Apply。"; exit 0; }

# 前置体检：出站别名在位（只查 Host 行有没有 gz，不读任何凭据）、被调脚本在仓库里存在。
awk '/^Host[ \t]/{for(i=2;i<=NF;i++) if($i=="gz") f=1} END{exit !f}' "$HOME/.ssh/config" 2>/dev/null \
  || { echo "X ~/.ssh/config 的 Host 行里没有 gz 别名（验收腿靠它出站连广州）" >&2; exit 1; }
[ -f "${REPO_ROOT}/scripts/verify_nightly_guangzhou.sh" ] \
  || { echo "X 仓库里找不到 scripts/verify_nightly_guangzhou.sh（七腿判据的唯一实现）" >&2; exit 1; }
# ssh -G 展开必须真给出一个主机名。⚠ 这条**不是**"派生可用"的充分证据（09-29 深夜锁面预演实测：
# 未配置的别名 ssh -G 会把别名本身当 hostname 原样回显、rc 仍为 0），所以真正的门前置是上面那条
# awk 数 Host 行点名；这里只是补抓"点名的那行写坏了/HostName 为空"这一类次级形态。
# 薄壳侧同一前提（run_nightly_verify.sh 的 alias_declared），两条腿不许一个前提一个口径。
DERIVED_HOST="$(ssh -G gz 2>/dev/null | awk '/^hostname /{print $2; exit}')"
[ -n "$DERIVED_HOST" ] || { echo "X ssh -G gz 展开不出 hostname（别名不可用，拒绝装一个永远派生不到主机的任务）" >&2; exit 1; }

mkdir -p "${NIGHTLY_HOME}/scripts"
# 为什么这里**不**拷 deploy/mac/ntfy_topic.sh（drill/backup 两个安装器拷）：实测本链两个文件
# （run_nightly_verify.sh + scripts/verify_nightly_guangzhou.sh）里 ntfy 相关代码为零——七腿的失败
# 只走 stdout/退出码，薄壳自己也不推。凭据口径出仓后（§KUMA-SECREDTO）「少拷一个 lib」会变成
# 启动即 FATAL，所以拷贝清单必须按**谁真的 source 它**来定，不能按"兄弟安装器都有"照抄。
# 将来若给夜间验收加推送，把 lib 一起带上并在门禁 §110 的文件面派生锁里同步。
cp "${REPO_MAC_DIR}/run_nightly_verify.sh" "${NIGHTLY_HOME}/run_nightly_verify.sh"
cp "${REPO_ROOT}/scripts/verify_nightly_guangzhou.sh" "${NIGHTLY_HOME}/scripts/verify_nightly_guangzhou.sh"
chmod +x "${NIGHTLY_HOME}/run_nightly_verify.sh" "${NIGHTLY_HOME}/scripts/verify_nightly_guangzhou.sh"

# 装完当场自证副本两半都在（断的是**稳定副本**，不是仓库源）。
[ -f "${NIGHTLY_HOME}/run_nightly_verify.sh" ] || { echo "X 稳定副本缺 run_nightly_verify.sh" >&2; exit 1; }
[ -f "${NIGHTLY_HOME}/scripts/verify_nightly_guangzhou.sh" ] \
  || { echo "X 稳定副本缺 scripts/verify_nightly_guangzhou.sh（拍平＝薄壳找不到被调脚本）" >&2; exit 1; }

# plist 的 ProgramArguments 必须指稳定副本；模板若还指桌面就拒绝安装（同 backup/drill 两个安装器）。
if ! grep -q "${NIGHTLY_HOME}/run_nightly_verify.sh" "${REPO_MAC_DIR}/com.quant.nightly.plist"; then
  echo "X 仓库 plist 模板的脚本路径不是 ${NIGHTLY_HOME}/…（模板没跟着改，拒绝安装指桌面的旧版）" >&2
  exit 1
fi
# 时点在位锁：模板若被回退成"每小时都没有"（例如误删 StartCalendarInterval），装上去就是个哑任务。
grep -q '<key>StartCalendarInterval</key>' "${REPO_MAC_DIR}/com.quant.nightly.plist" \
  || { echo "X com.quant.nightly.plist 没有 StartCalendarInterval（装了也不会自己跑）" >&2; exit 1; }
plutil -lint "${REPO_MAC_DIR}/com.quant.nightly.plist" >/dev/null \
  || { echo "X com.quant.nightly.plist 语法不过（plutil -lint），拒绝装载" >&2; exit 1; }
[ -f "$AGENT" ] && cp "$AGENT" "$AGENT.bak-$(date +%Y%m%d-%H%M%S)"
cp "${REPO_MAC_DIR}/com.quant.nightly.plist" "$AGENT"

launchctl bootout "gui/$(id -u)/com.quant.nightly" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$AGENT"
echo "INSTALLED label=com.quant.nightly script=${NIGHTLY_HOME}/run_nightly_verify.sh log=$HOME/backups/quant/nightly_verify.log"

# 离线自检（③）：从**稳定副本**里跑 -Preview，确认副本完整、七腿命令能拼出来且不连现网。
# 这一步刻意走稳定副本而不是仓库源——要验的就是"launchd 将来能吃到的那一份"。
# 薄壳自己派生 GZ_IP 后才把被调脚本拉起来，所以这里不需要注入任何地址。
"${NIGHTLY_HOME}/run_nightly_verify.sh" -Preview >/tmp/quant-nightly-preview.$$ 2>&1 \
  || { echo "X 稳定副本自检失败（-Preview 非零），详见 /tmp/quant-nightly-preview.$$" >&2; exit 1; }
grep -q 'verify_nightly_guangzhou -Preview' /tmp/quant-nightly-preview.$$ \
  || { echo "X 稳定副本自检输出里没有七腿预览（跑的不是预期那条链），拒绝就此收工" >&2; exit 1; }
rm -f /tmp/quant-nightly-preview.$$
echo "SELFTEST preview-ok（稳定副本可执行，且一次 SSH 都没发）"

if [ "$KICK" = "1" ]; then
  launchctl kickstart "gui/$(id -u)/com.quant.nightly"
  echo "KICKED 跑动中：七腿要两次 scp + 三次 ssh，进度看 ~/backups/quant/nightly_verify.log，"
  echo "        结果锚点＝「DONE rc=0」，每次读数追加在 ~/backups/quant/nightly_record.jsonl"
fi
