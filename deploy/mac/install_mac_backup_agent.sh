#!/bin/bash
# install_mac_backup_agent.sh — Mac 异地备份拉取腿的安装/升级器（本机操作，不碰生产）
# 为什么需要它：09-16~09-25 的实录教训——launchd 直接跑 **Desktop 仓库路径**里的
#   restic_pull_backup.sh，被 macOS TCC（桌面是保护目录）判 `Operation not permitted`
#   （退出码 126），**每天 07:00 定时触发、每天静默失败、ntfy 一声不吭**（脚本根本没跑，
#   告警代码执行不到），异地副本停在 09-15。修法＝脚本稳定副本放 ~/backups/quant/bin/
#   （非保护目录），plist 指它；仓库版永远是源，升级=重跑本脚本同步。
# 用法：
#   ./install_mac_backup_agent.sh            # 只打印将做什么（预览，零改动）
#   ./install_mac_backup_agent.sh -Apply     # 安装/更新脚本与 plist 并重载任务
#   ./install_mac_backup_agent.sh -Apply -Kick  # 上面做完再立即补跑一次（验证通道活）
set -euo pipefail

REPO_MAC_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/backups/quant/bin"
AGENT="$HOME/Library/LaunchAgents/com.quant.backup.plist"
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
say "脚本稳定副本 : $REPO_MAC_DIR/restic_pull_backup.sh → $BIN_DIR/restic_pull_backup.sh"
say "任务 plist    : ${REPO_MAC_DIR}/com.quant.backup.plist → ${AGENT}（先备份旧份）"
say "调度          : 每日 07:00 + 10:30 两个时点（§0929DRILL-B：广州快照收工在 05:20~08:19 浮动，"
say "                单靠 07:00 会系统性只拉到前一天那份；10:30 同时兼作 sftp 熔断后的补跑窗口）"
say "重载          : launchctl bootout（存在则）→ bootstrap → （-Kick 时）kickstart 补跑一次"
[ "$APPLY" = "1" ] || { echo "预览模式：本机一个字节没动。动手请加 -Apply。"; exit 0; }

# 前置体检：restic 在 brew 路径、ssh 别名 gz、钥匙串条目存在（只查存在性，绝不取值）。
command -v restic >/dev/null 2>&1 || [ -x /opt/homebrew/bin/restic ] || { echo "X restic 未安装（brew install restic）" >&2; exit 1; }
awk '/^Host[ \t]/{for(i=2;i<=NF;i++) if($i=="gz") f=1} END{exit !f}' "$HOME/.ssh/config" 2>/dev/null \
  || { echo "X ~/.ssh/config 的 Host 行里没有 gz 别名（拉取腿靠它出站连广州）" >&2; exit 1; }
security find-generic-password -a "$USER" -s quant-restic-repo-pass >/dev/null 2>&1 \
  || { echo "X 钥匙串缺 quant-restic-repo-pass（本脚本只查存在性，不读取口令值）" >&2; exit 1; }

mkdir -p "$BIN_DIR"
cp "$REPO_MAC_DIR/restic_pull_backup.sh" "$BIN_DIR/restic_pull_backup.sh"
chmod +x "$BIN_DIR/restic_pull_backup.sh"

# plist 的 ProgramArguments 必须指稳定副本；仓库模板里就是稳定路径，直接拷。
if ! grep -q "$BIN_DIR/restic_pull_backup.sh" "$REPO_MAC_DIR/com.quant.backup.plist"; then
  echo "X 仓库 plist 模板的脚本路径不是 $BIN_DIR/…（模板没跟着改，拒绝安装旧指桌面的版本）" >&2
  exit 1
fi
# §0929DRILL-B：拉取腿现在是**两个时点**（07:00 + 10:30），装旧模板（单时点）等于把
#   "异地永远晚一天"这个缺陷又装回现网。判据取 plist 结构本身而不是文本相等：
#   ① `<key>Hour</key>` 的行数必须 >=2（单时点模板就是 1 行，必然红）；
#   ② 两个时点值必须各在位一次（7 与 10），这样只加一个 Hour 的半改模板也会红。
#   为什么不用"整段字符串等于某版模板"：那种锁在下次改时间时会先把锁改绿再改值，
#   等于没有锁（本仓 §BOM-REPO 同族：锁面写成硬编码清单就自动落在锁外）。
HOUR_LINES=$(grep -c '<key>Hour</key>' "$REPO_MAC_DIR/com.quant.backup.plist" || true)
if [ "$HOUR_LINES" -lt 2 ]; then
  echo "X com.quant.backup.plist 只有 ${HOUR_LINES} 个时点（应为 2：07:00 兜早、10:30 兜广州收工浮动）——拒绝把单时点旧模板装上去" >&2
  exit 1
fi
for h in 7 10; do
  n=$(grep -c "<integer>${h}</integer>" "$REPO_MAC_DIR/com.quant.backup.plist" || true)
  [ "$n" -eq 1 ] || { echo "X 时点 Hour=${h} 在 plist 里出现 ${n} 次（应恰好 1 次）" >&2; exit 1; }
done
plutil -lint "$REPO_MAC_DIR/com.quant.backup.plist" >/dev/null \
  || { echo "X com.quant.backup.plist 语法不过（plutil -lint），拒绝装载" >&2; exit 1; }
[ -f "$AGENT" ] && cp "$AGENT" "$AGENT.bak-$(date +%Y%m%d-%H%M%S)"
cp "$REPO_MAC_DIR/com.quant.backup.plist" "$AGENT"

launchctl bootout "gui/$(id -u)/com.quant.backup" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$AGENT"
echo "INSTALLED label=com.quant.backup script=$BIN_DIR/restic_pull_backup.sh"

if [ "$KICK" = "1" ]; then
  launchctl kickstart "gui/$(id -u)/com.quant.backup"
  echo "KICKED 跑动中：增量要拉 10 天的包，进度看 ~/backups/quant/pull_backup.log（成功锚点「=== 备份成功 ===」）"
fi
