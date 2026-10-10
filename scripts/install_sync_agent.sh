#!/bin/bash
# 安装/刷新本地数据同步 agent（launchd）——子系统数据管线（阶段2.1b）的装机入口。
#
# ⚠ 本链已于 2026-10-11 **退役**（owner 当面裁「整条停掉」，含每日本地下载那一步）。
#   退役的事实根据写在 scripts/sync_delta_to_cloud.sh 的头注里（目标机 8 月中旬改行、六步链
#   在整个日志生命周期里一次都没走到第 6 步、每天烧 37 分钟~6 小时 25 分的真实下载后恒退 1、
#   且这条任务当时不在任何看门狗/门禁的覆盖面内＝无人被告知）。
#   本文件因此**改成一个默认拒绝的装入口**，而不是删掉：
#     · 默认（arm）＝直接退 1 并说明理由。理由很具体——这条链的死法不是"脚本写坏了"，
#       而是"没人把目标机没了这件事告诉调度"，所以装机入口必须显式知情，不能像以前那样
#       谁重跑一次安装器就把它悄悄挂回每天 15:20（挂回来之后照样恒失败、照样没人知道）。
#       真要重装请显式传 SYNC_CHAIN_ARMED=1，并且先把 SERVER_IP 指到一台确实跑着
#       quant 数据面、且那个库允许被写入的机器上。
#     · `-Retire` ＝正规退役通道（幂等）：bootout 卸调度 → plist 改名留证 → 打印复核命令。
#       为什么要有这一支而不是让人手敲 launchctl：退役动作本身也要有一条可复核、可重跑的
#       仓库指令（"必存/必回"要能落到一条指令上），并且要保证**摘干净**——只 bootout 不摘
#       plist 的话，下次登录 launchd 会把它原样挂回去，看上去"已经停了"其实是本会话特例。
#       这一支挂上之后由看门狗腿 3 每天反查在位性（loaded/plist-in-place 都判红）。
#
# 为什么需要本脚本：launchd 进程没有终端的"桌面文件夹"访问权，直接执行 ~/Desktop 下
# 的脚本会被 macOS TCC 拦截（Operation not permitted）。故把运行时副本装到
# ~/Library/Application Support/quant-trading/（非保护目录），仓库仍是唯一源码，
# 改完脚本重跑本安装器即可刷新副本。
#
# 做四件事（arm 分支）：
#   1) 复制 sync_delta_to_cloud.sh 与 cmd/pydata/server.py 到运行时目录
#   2) 构建本地 dataload 二进制到运行时目录（免 go 编译依赖）
#   3) 生成 launchd plist（日志落 ~/Library/Logs/，不再用会被清理的 /tmp）
#   4) 重载 launchd 并打印验证命令
#
# 用法：
#   ./scripts/install_sync_agent.sh -Retire                      # 退役（幂等，默认动作已停用）
#   SYNC_CHAIN_ARMED=1 SERVER_IP=x.x.x.x ./scripts/install_sync_agent.sh   # 显式知情后的重装
set -euo pipefail

ACTION="arm"
for arg in "$@"; do
    case "$arg" in
        -Retire|--retire) ACTION="retire" ;;
        *) echo "未知参数：${arg}（可用：-Retire）" >&2; exit 2 ;;
    esac
done

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RUNTIME="$HOME/Library/Application Support/quant-trading"
LOGS="$HOME/Library/Logs"
PLIST="$HOME/Library/LaunchAgents/com.quant.syncdata.plist"
LABEL="com.quant.syncdata"

if [ "$ACTION" = "retire" ]; then
    # 幂等退役：每一步都允许"已经做过"，但**结论必须回读**，不能只看命令退 0
    # （launchctl bootout 对不存在的服务报错、对已摘除的服务退非 0，两种都得区分开）。
    uid_num="$(id -u)"
    echo "[1/3] 卸调度（launchd 域内 ${LABEL}）"
    if launchctl bootout "gui/${uid_num}/${LABEL}" >/dev/null 2>&1; then
        echo "  ok - 已从调度中摘除"
    elif launchctl print "gui/${uid_num}/${LABEL}" >/dev/null 2>&1; then
        echo "  FAIL bootout 没成功而服务仍在调度里——请人工查该服务是否被别的域持有" >&2
        exit 1
    else
        echo "  ok - 本会话里本来就不在调度（幂等）"
    fi
    echo "[2/3] plist 改名留证（下次登录不会再被挂回去）"
    stamp="$(date +%Y%m%d)"
    if [ -f "$PLIST" ]; then
        mv "$PLIST" "${PLIST%.plist}.retired-${stamp}"
        echo "  ok - 已改名 → ${PLIST%.plist}.retired-${stamp}（留证不删，退役理由见本文件头注）"
    else
        echo "  ok - 没有 .plist 在位（幂等）"
    fi
    echo "[3/3] 复核（这两条必须都给不出该服务，才算退役成立）"
    # 这两条复核一律写成 if 形式，不写 `cmd && { ...; }`：set -e 下"复核通过"那一支（命令返回非 0）
    # 会让整条与列表返回 1，脚本在没有任何输出的位置自己中止——正是本仓 §雷 的那一形。
    if launchctl print "gui/${uid_num}/${LABEL}" >/dev/null 2>&1; then
        echo "  FAIL 服务仍可 print＝没摘干净" >&2
        exit 1
    fi
    if [ -f "$PLIST" ]; then
        echo "  FAIL plist 仍在位＝下次登录会复活" >&2
        exit 1
    fi
    echo "  ok - ${LABEL} 既不在调度、也没有 plist"
    echo "运行时副本（${RUNTIME}）与两份日志（${LOGS}/quant_sync*.log）保留为证据，不删。"
    echo "反查这条退役状态是否被破坏：deploy/mac/check_mac_ops_watchdog.sh 腿 3（loaded/plist-in-place 都判红）"
    exit 0
fi

if [ "${SYNC_CHAIN_ARMED:-0}" != "1" ]; then
    echo "本链已于 2026-10-11 退役，装机入口默认拒绝（见本文件头注的事实根据）。" >&2
    echo "  · 想把它退役/复核退役状态：./scripts/install_sync_agent.sh -Retire" >&2
    echo "  · 确实要重新挂上调度：确认目标机与那个库的归属后，显式传 SYNC_CHAIN_ARMED=1 + SERVER_IP=<那台机>" >&2
    exit 1
fi

SERVER_IP="${SERVER_IP:?请设置 SERVER_IP（云端公网 IP；SYNC_CHAIN_ARMED=1 时才需要）}"
SERVER_USER="${SERVER_USER:-root}"

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
