#!/bin/bash
# 安装/刷新本地数据同步 agent（launchd）——子系统数据管线（阶段2.1b）的装机入口。
#
# 为什么需要本脚本：launchd 进程没有终端的"桌面文件夹"访问权，直接执行 ~/Desktop 下
# 的脚本会被 macOS TCC 拦截（Operation not permitted）。故把运行时副本装到
# ~/Library/Application Support/quant-trading/（非保护目录），仓库仍是唯一源码，
# 改完脚本重跑本安装器即可刷新副本。
#
# 做四件事：
#   1) 复制 sync_delta_to_cloud.sh 与 cmd/pydata/server.py 到运行时目录
#   2) 构建本地 dataload 二进制到运行时目录（免 go 编译依赖）
#   3) 生成 launchd plist（日志落 ~/Library/Logs/，不再用会被清理的 /tmp）
#   4) 重载 launchd 并打印验证命令
#
# 用法：SERVER_IP=x.x.x.x ./scripts/install_sync_agent.sh
set -euo pipefail

SERVER_IP="${SERVER_IP:?请设置 SERVER_IP}"
SERVER_USER="${SERVER_USER:-root}"
APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME="$HOME/Library/Application Support/quant-trading"
LOGS="$HOME/Library/Logs"
PLIST="$HOME/Library/LaunchAgents/com.quant.syncdata.plist"

echo "[1/4] 复制脚本与 pydata server → $RUNTIME"
mkdir -p "$RUNTIME"
cp "$APP_DIR/scripts/sync_delta_to_cloud.sh" "$RUNTIME/"
cp "$APP_DIR/cmd/pydata/server.py" "$RUNTIME/pydata_server.py"
chmod +x "$RUNTIME/sync_delta_to_cloud.sh"

echo "[2/4] 构建本地 dataload → $RUNTIME/dataload"
(cd "$APP_DIR" && go build -o "$RUNTIME/dataload" ./cmd/dataload)

echo "[3/4] 生成 ${PLIST}（日志 → ${LOGS}）"
mkdir -p "$HOME/Library/LaunchAgents" "$LOGS"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- 本地 baostock 每日下载→上传云端 agent。由 scripts/install_sync_agent.sh 生成，勿手改；
     改 scripts/sync_delta_to_cloud.sh 后重跑安装器即可。运行时副本在：
     ${RUNTIME}（避开 Desktop 的 TCC 保护，launchd 可执行）。 -->
<plist version="1.0">
<dict>
  <key>Label</key><string>com.quant.syncdata</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>$RUNTIME/sync_delta_to_cloud.sh</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SERVER_IP</key><string>$SERVER_IP</string>
    <key>SERVER_USER</key><string>$SERVER_USER</string>
    <!-- 运行时副本自包含：二进制与 pydata 都指向本目录，不回读 Desktop 仓库 -->
    <key>DATALOAD_BIN</key><string>$RUNTIME/dataload</string>
    <key>PYDATA_SERVER</key><string>$RUNTIME/pydata_server.py</string>
    <!-- 复权因子每日补齐默认关（首次已从云端快照补齐）；需要时改 1 -->
    <key>ADJFACTOR_ENABLED</key><string>0</string>
    <key>PATH</key><string>/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin</string>
  </dict>
  <!-- 每个交易日 15:20 触发（A 股 15:00 收盘后）；周末/节假日无新交易日自动跳过 -->
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key><integer>15</integer>
    <key>Minute</key><integer>20</integer>
  </dict>
  <key>StandardOutPath</key><string>$LOGS/quant_sync.log</string>
  <key>StandardErrorPath</key><string>$LOGS/quant_sync_err.log</string>
  <key>RunAtLoad</key><false/>
</dict>
</plist>
EOF

echo "[4/4] 重载 launchd"
launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

echo "安装完成。验证："
echo "  launchctl list | grep quant.syncdata"
echo "  launchctl start com.quant.syncdata   # 手动触发一次"
echo "  tail -f $LOGS/quant_sync.log"
