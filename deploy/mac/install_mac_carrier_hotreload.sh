#!/bin/bash
# install_mac_carrier_hotreload.sh — 战法白名单热更新腿的安装/升级器（本机操作，不碰生产）
# 同 install_mac_backup_agent.sh 的 TCC 教训：launchd 读不到 Desktop 仓库，脚本稳定
# 副本必须放 ~/backups/quant/bin/（非保护目录），plist 指它；仓库版永远是源。
# 用法：
#   ./install_mac_carrier_hotreload.sh            # 预览（零改动）
#   ./install_mac_carrier_hotreload.sh -Apply     # 安装/更新脚本与 plist 并重载任务
#   ./install_mac_carrier_hotreload.sh -Apply -Kick  # 完成后立即补跑一次验证通道活
set -euo pipefail

REPO_MAC_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/backups/quant/bin"
AGENT="$HOME/Library/LaunchAgents/com.quant.carrier-hotreload.plist"
APPLY=0; KICK=0
for a in "$@"; do
  case "$a" in
    -Apply) APPLY=1 ;;
    -Kick)  KICK=1 ;;
    *) echo "unknown arg: $a" >&2; exit 2 ;;
  esac
done

echo "plan: copy 2 scripts -> $BIN_DIR ; plist -> $AGENT (schedule 08:25 + 15:10 daily)"
[[ $APPLY -eq 1 ]] || { echo "preview only (pass -Apply to install)"; exit 0; }

mkdir -p "$BIN_DIR"
cp "$REPO_MAC_DIR/../scripts/carrier_hot_reload.sh" "$BIN_DIR/carrier_hot_reload.sh"
chmod +x "$BIN_DIR/carrier_hot_reload.sh"
# 稳定副本内解析 ROOT 用 BASH_SOURCE 的上级——但副本不在仓库里，改为仓库绝对路径写死
mkdir -p "$HOME/backups/quant"
cat > "$BIN_DIR/carrier_hot_reload.sh" <<EOF
#!/bin/bash
exec bash "$REPO_MAC_DIR/../scripts/carrier_hot_reload.sh" "\$@"
EOF
chmod +x "$BIN_DIR/carrier_hot_reload.sh"
cp "$REPO_MAC_DIR/com.quant.carrier-hotreload.plist" "$AGENT"
launchctl unload "$AGENT" 2>/dev/null || true
launchctl load "$AGENT"
echo "installed: $AGENT"
[[ $KICK -eq 1 ]] && { echo "kick start..."; launchctl start com.quant.carrier-hotreload; sleep 2; tail -5 "$HOME/backups/quant/carrier_hotreload.log" 2>/dev/null || true; }
echo "done"
