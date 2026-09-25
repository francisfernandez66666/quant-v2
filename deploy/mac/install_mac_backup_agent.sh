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
[ -f "$AGENT" ] && cp "$AGENT" "$AGENT.bak-$(date +%Y%m%d-%H%M%S)"
cp "$REPO_MAC_DIR/com.quant.backup.plist" "$AGENT"

launchctl bootout "gui/$(id -u)/com.quant.backup" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$AGENT"
echo "INSTALLED label=com.quant.backup script=$BIN_DIR/restic_pull_backup.sh"

if [ "$KICK" = "1" ]; then
  launchctl kickstart "gui/$(id -u)/com.quant.backup"
  echo "KICKED 跑动中：增量要拉 10 天的包，进度看 ~/backups/quant/pull_backup.log（成功锚点「=== 备份成功 ===」）"
fi
