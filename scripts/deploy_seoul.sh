#!/bin/bash
# 一键部署到首尔阿里云 Ubuntu 服务器：交叉编译 → 上传 → 安装 Caddy/systemd → 健康检查。
#
# 用法（本地 macOS 上执行）：
#   SERVER_IP=1.2.3.4 SERVER_DOMAIN=your-domain.com ./scripts/deploy_seoul.sh
#   或先 export SERVER_IP=... SERVER_DOMAIN=... 再 ./scripts/deploy_seoul.sh
#
# 参数说明（均为必填，除注明外）：
#   SERVER_IP         首尔服务器公网 IP（SSH 用）
#   SERVER_USER       SSH 用户（默认 root）
#   SERVER_DOMAIN     域名（Caddy 用，必须已解析到 SERVER_IP；Caddy 首次启动会做 ACME 验证）
#   LLM_API_KEY       LLM 服务 API Key（写入 /etc/quant.env）
#   LLM_API_URL       LLM API 地址（可选，默认 https://api.siliconflow.cn/v1/chat/completions）
#   LLM_MODEL         LLM 模型名（可选，默认 THUDM/GLM-Z1-9B-0414）
#   DEPLOY_DIR        服务器代码目录（默认 /opt/quant）
#   QUANT_DATA_DIR    服务器数据目录（默认 /var/lib/quant-trading-v2）

set -euo pipefail

# ── 必填参数校验 ──
: "${SERVER_IP:?请设置 SERVER_IP（首尔服务器公网 IP）}"
: "${SERVER_DOMAIN:?请设置 SERVER_DOMAIN（域名，需已解析到 SERVER_IP）}"
SERVER_USER="${SERVER_USER:-root}"
LLM_API_KEY="${LLM_API_KEY:-}"
LLM_API_URL="${LLM_API_URL:-https://api.siliconflow.cn/v1/chat/completions}"
LLM_MODEL="${LLM_MODEL:-THUDM/GLM-Z1-9B-0414}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/quant}"
QUANT_DATA_DIR="${QUANT_DATA_DIR:-/var/lib/quant-trading-v2}"

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$APP_DIR"
# §SSH 加固：密钥登录+端口 28022（~/.ssh/config 有 Host 43.108.86.140 seoul 别名自动匹配）
SSH="ssh -o StrictHostKeyChecking=accept-new $SERVER_USER@$SERVER_IP"
SCP="scp -o StrictHostKeyChecking=accept-new"

echo "=============================================="
echo " quant-trading-v2 部署到首尔服务器"
echo " IP:     $SERVER_IP"
echo " 域名:   $SERVER_DOMAIN"
echo " 目录:   $DEPLOY_DIR (代码) / $QUANT_DATA_DIR (数据)"
echo " LLM:    $LLM_API_URL / $LLM_MODEL"
echo "=============================================="

# ── 1. 本地交叉编译（linux/amd64，静态链接纯 Go）──
echo "[1/8] 交叉编译 linux/amd64..."
GOOS=linux GOARCH=amd64 go build -o /tmp/quant_linux ./cmd/quant
echo "      产物: /tmp/quant_linux ($(du -h /tmp/quant_linux | cut -f1))"

# ── 2. 上传二进制 + 配置文件到服务器 ──
echo "[2/8] 上传二进制与配置..."
$SSH "sudo mkdir -p $DEPLOY_DIR/config"
$SCP /tmp/quant_linux $SERVER_USER@$SERVER_IP:/tmp/quant_linux
$SSH "sudo mv /tmp/quant_linux $DEPLOY_DIR/quant && sudo chmod +x $DEPLOY_DIR/quant"

# 研究/下载/调度二进制：独立研究服务（quant-research）与 sidecar 依赖
echo "      编译 research/dataload/researchd (linux/amd64)..."
# 子系统统一改造（docs/RESEARCH_TASK_QUEUE_PLAN.md 二期）：bt_strategy 已并入 research
# （backtest-strategy 子命令），不再单独构建/分发；服务器上的旧二进制顺带清理。
# English: since the phase-2 merge, bt_strategy lives inside the research binary — no separate build.
GOOS=linux GOARCH=amd64 go build -o /tmp/research_linux ./cmd/research
GOOS=linux GOARCH=amd64 go build -o /tmp/dataload_linux ./cmd/dataload
GOOS=linux GOARCH=amd64 go build -o /tmp/researchd_linux ./cmd/researchd
$SCP /tmp/research_linux /tmp/dataload_linux /tmp/researchd_linux $SERVER_USER@$SERVER_IP:/tmp/
$SSH "sudo mv /tmp/research_linux $DEPLOY_DIR/research && sudo mv /tmp/dataload_linux $DEPLOY_DIR/dataload && sudo mv /tmp/researchd_linux $DEPLOY_DIR/researchd && sudo rm -f $DEPLOY_DIR/bt_strategy"
$SSH "sudo chmod +x $DEPLOY_DIR/research $DEPLOY_DIR/dataload $DEPLOY_DIR/researchd"
# 事件匹配规则（相对路径加载），缺失时优雅降级但也尽量带上
EVENTS_SRC=""
for cand in "$APP_DIR/config/events_leftside.yaml" "$APP_DIR/events_leftside.yaml"; do
    if [ -f "$cand" ]; then EVENTS_SRC="$cand"; break; fi
done
if [ -n "$EVENTS_SRC" ]; then
    $SCP "$EVENTS_SRC" $SERVER_USER@$SERVER_IP:/tmp/events_leftside.yaml
    $SSH "sudo mv /tmp/events_leftside.yaml $DEPLOY_DIR/config/events_leftside.yaml"
else
    echo "      [warn] 未找到 events_leftside.yaml，事件匹配将被禁用"
fi
# Caddy：先装包再写 Caddyfile，避免与 apt 自带默认 Caddyfile 的 conffile 交互提示冲突
$SSH "which caddy >/dev/null 2>&1 || (sudo apt-get update -qq && DEBIAN_FRONTEND=noninteractive sudo apt-get install -y -qq caddy)"
$SCP "$APP_DIR/deploy/Caddyfile" $SERVER_USER@$SERVER_IP:/tmp/Caddyfile
$SSH "sudo mkdir -p /etc/caddy"
$SSH "sudo mv /tmp/Caddyfile /etc/caddy/Caddyfile"
$SSH "sudo chown root:root /etc/caddy/Caddyfile && sudo chmod 644 /etc/caddy/Caddyfile"
# 同服务器另一项目（翻译助手）：translator 站点通过 `import /etc/caddy/translator.conf` 引入，
# 本文件只含本站点 + import 行（不内联 translator），保证另一项目配置独立。
# 若服务器尚无 translator.conf（另一项目未部署过），用本仓库 deploy/caddy/translator.conf 兜底，
# 避免 caddy import 缺失文件启动失败。
# English: the translator project on the same server is imported via `import /etc/caddy/translator.conf`;
# this file only contains this site + the import line (translator is never inlined), keeping the other
# project's config independent. If the server lacks translator.conf (other project not yet deployed),
# fall back to deploy/caddy/translator.conf so caddy's import doesn't fail to start.
if $SSH "sudo test -f /etc/caddy/translator.conf"; then
    echo "      translator.conf 已存在（另一项目部署过），保留服务器现有文件"
else
    if [ -f "$APP_DIR/deploy/caddy/translator.conf" ]; then
        $SCP "$APP_DIR/deploy/caddy/translator.conf" $SERVER_USER@$SERVER_IP:/tmp/translator.conf
        $SSH "sudo mv /tmp/translator.conf /etc/caddy/translator.conf"
        $SSH "sudo chown root:root /etc/caddy/translator.conf && sudo chmod 644 /etc/caddy/translator.conf"
        echo "      translator.conf 不存在，已用本仓库兜底文件部署"
    else
        echo "      [warn] 未找到 deploy/caddy/translator.conf，且服务器无该文件——import 将导致 caddy 启动失败"
    fi
fi

# ── 3. 数据目录 + 运行用户 ──
echo "[3/8] 初始化数据目录与运行用户..."
$SSH "sudo mkdir -p $QUANT_DATA_DIR /var/log/caddy /var/www/quant-web"
$SSH "id quant >/dev/null 2>&1 || sudo useradd -r -s /usr/sbin/nologin quant"
$SSH "sudo chown -R quant:quant $QUANT_DATA_DIR $DEPLOY_DIR"

# ── 3b. baostock 研究数据 sidecar（Python venv，dataload 依赖 :8787）──
echo "[3b/8] 部署 baostock sidecar (Python venv)..."
$SSH "sudo mkdir -p $DEPLOY_DIR/pydata"
$SCP "$APP_DIR/cmd/pydata/server.py" "$APP_DIR/cmd/pydata/requirements.txt" $SERVER_USER@$SERVER_IP:/tmp/
$SSH "sudo mv /tmp/server.py $DEPLOY_DIR/pydata/server.py && sudo mv /tmp/requirements.txt $DEPLOY_DIR/pydata/requirements.txt"
$SSH "which python3 >/dev/null 2>&1 || (sudo apt-get update -qq && DEBIAN_FRONTEND=noninteractive sudo apt-get install -y -qq python3 python3-venv)"
$SSH "sudo -u quant python3 -m venv $DEPLOY_DIR/venv"
$SSH "sudo -u quant $DEPLOY_DIR/venv/bin/pip install --quiet -r $DEPLOY_DIR/pydata/requirements.txt"

# ── 4. 写入环境变量文件（LLM Key 等敏感项）──
echo "[4/8] 写入 /etc/quant.env..."
# 未提供 LLM_API_KEY 时（云端后台已配）保留服务器现有文件，避免误覆盖
if [ -n "$LLM_API_KEY" ]; then
    ENV_CONTENT="LLM_API_KEY=$LLM_API_KEY"
    [ -n "$LLM_API_URL" ] && ENV_CONTENT="$ENV_CONTENT
LLM_API_URL=$LLM_API_URL"
    [ -n "$LLM_MODEL" ] && ENV_CONTENT="$ENV_CONTENT
LLM_MODEL=$LLM_MODEL"
    $SSH "sudo tee /etc/quant.env >/dev/null" <<EOF
$ENV_CONTENT
EOF
    $SSH "sudo chmod 600 /etc/quant.env"
else
    echo "      LLM_API_KEY 未提供，保留服务器现有 /etc/quant.env（云端后台配置）"
fi

# ── 5. 域名占位符替换 + 安装 Caddy ──
echo "[5/8] 前端构建上传 + 配置 Caddy ($SERVER_DOMAIN)..."
echo "      构建前端 (npm run build)..."
(cd "$APP_DIR/web" && npm run build >/dev/null)
echo "      上传前端到 /var/www/quant-web..."
$SSH "sudo rm -rf /var/www/quant-web/* && sudo mkdir -p /tmp/quant-web"
$SCP -r "$APP_DIR/web/dist/." $SERVER_USER@$SERVER_IP:/tmp/quant-web/
$SSH "sudo mv /tmp/quant-web/* /var/www/quant-web/ && sudo chown -R caddy:caddy /var/www/quant-web"
$SSH "sudo sed -i 's/YOUR_DOMAIN_HERE.com/$SERVER_DOMAIN/g' /etc/caddy/Caddyfile"
$SSH "sudo chown -R caddy:caddy /var/log/caddy 2>/dev/null || true"
$SSH "which caddy >/dev/null 2>&1 || (sudo apt-get update -qq && sudo apt-get install -y -qq caddy)"

# ── 6. 安装 systemd 单元并启动 ──
echo "[6/8] 安装并启动 quant.service..."
$SCP "$APP_DIR/deploy/quant.service" $SERVER_USER@$SERVER_IP:/tmp/quant.service
$SSH "sudo mv /tmp/quant.service /etc/systemd/system/quant.service"
# 独立研究服务 + baostock sidecar（按时段切换调度，见 deploy/quant-research.service）
$SCP "$APP_DIR/deploy/quant-research.service" $SERVER_USER@$SERVER_IP:/tmp/quant-research.service
$SSH "sudo mv /tmp/quant-research.service /etc/systemd/system/quant-research.service"
$SCP "$APP_DIR/deploy/pydata.service" $SERVER_USER@$SERVER_IP:/tmp/pydata.service
$SSH "sudo mv /tmp/pydata.service /etc/systemd/system/pydata.service"
 $SSH "sudo systemctl daemon-reload"
 # restart 而非 enable --now：对已运行服务 enable --now 是空操作，会导致
 # 新二进制上线后进程仍是旧代码（本次部署实际踩坑）。
 # English: restart unconditionally — `enable --now` on a running unit is a no-op and leaves the old
 # binary running after an upgrade (bit us in practice).
 $SSH "sudo systemctl enable quant >/dev/null 2>&1; sudo systemctl restart quant"
 $SSH "sudo systemctl enable pydata >/dev/null 2>&1; sudo systemctl restart pydata"
 $SSH "sudo systemctl enable quant-research >/dev/null 2>&1; sudo systemctl restart quant-research"
 $SSH "sudo systemctl restart caddy"

# ── 6b. QMT 网关（AUTO_TRADING_PLAN M1/M2 预留）──
#  - cmd/qmt-mock：Linux 上先跑 mock 网关做端到端联调（qmt-mock.service 默认关闭，联调时 enable）
#  - qmt_gateway/：M2 真实 Windows 网关 Python 骨架（待 Windows 云主机后上传运行，此处仅落盘）
# English: QMT gateway prep — uploads the Go mock gateway (unit disabled by default) and the M2 Python
# gateway skeleton to the server.
echo "[6b/8] 部署 QMT 网关（mock + M2 Python 骨架）..."
GOOS=linux GOARCH=amd64 go build -o /tmp/qmt-mock_linux ./cmd/qmt-mock
$SCP /tmp/qmt-mock_linux $SERVER_USER@$SERVER_IP:/tmp/qmt-mock_linux
$SSH "sudo mv /tmp/qmt-mock_linux $DEPLOY_DIR/qmt-mock && sudo chmod +x $DEPLOY_DIR/qmt-mock"
$SCP "$APP_DIR/deploy/qmt-mock.service" $SERVER_USER@$SERVER_IP:/tmp/qmt-mock.service
$SSH "sudo mv /tmp/qmt-mock.service /etc/systemd/system/qmt-mock.service"
# 默认关闭（联调时手动 enable --now qmt-mock）；token 由 /etc/qmt-mock.env 提供
$SSH "sudo mkdir -p $DEPLOY_DIR/qmt_gateway /tmp/qmt_gateway"
# 先清空目标与暂存目录再复制（__pycache__/tests 等非空目录会让 mv 覆盖失败，set -e 中断部署）
# English: clear target & staging first — non-empty dirs (pycache/tests) break plain mv overwrite.
$SSH "sudo rm -rf /tmp/qmt_gateway $DEPLOY_DIR/qmt_gateway && sudo mkdir -p /tmp/qmt_gateway $DEPLOY_DIR/qmt_gateway"
$SCP -r "$APP_DIR/qmt_gateway/." $SERVER_USER@$SERVER_IP:/tmp/qmt_gateway/
$SSH "sudo mv /tmp/qmt_gateway/* $DEPLOY_DIR/qmt_gateway/ && sudo rm -rf /tmp/qmt_gateway"
$SSH "sudo systemctl daemon-reload"
echo "      qmt-mock 已部署（service 关闭）；qmt_gateway/ Python 骨架已落盘 $DEPLOY_DIR/qmt_gateway"

# ── 7. 健康检查 ──
echo "[7/8] 健康检查..."
sleep 3
# 后端本机直连检查（不经 Caddy）
if $SSH "curl -sf -o /dev/null -m 10 http://127.0.0.1:8080/setup"; then
    echo "  ✓ 后端进程已响应 (127.0.0.1:8080)"
else
    echo "  ✗ 后端未就绪，请查看日志: journalctl -u quant -n 50"
fi
# 独立研究服务 + baostock sidecar 状态
for svc in pydata quant-research; do
    if $SSH "systemctl is-active $svc >/dev/null 2>&1"; then
        echo "  ✓ $svc 运行中"
    else
        echo "  ✗ $svc 未运行，查看: journalctl -u $svc -n 50"
    fi
done
# HTTPS 检查（首次 ACME 申请可能要等几秒~几十秒）
echo "  等待 HTTPS 证书就绪 (最多 60s)..."
for i in $(seq 1 60); do
    if $SSH "curl -sf -o /dev/null -m 10 https://$SERVER_DOMAIN/"; then
        echo "  ✓ HTTPS 已就绪: https://$SERVER_DOMAIN/"
        break
    fi
    sleep 1
    [ "$i" = "60" ] && echo "  ✗ HTTPS 未就绪，检查: journalctl -u caddy -n 50（确认域名 DNS 已生效）"
done

echo "=============================================="
echo " 部署完成。首次登录："
echo "   1. 浏览器打开 https://$SERVER_DOMAIN ，直接进前端登录页"
echo "   2. 全新部署：POST https://$SERVER_DOMAIN/setup 创建管理员（§GAP2-W1 起不再有默认口令 admin/admin123）"
echo "   3. APK 走 /api 直接用账号登录"
echo " 研究调度（quant-research）："
echo "   - 交易时段只做 dataload 增量下载；盘后 15:30/周末各跑一轮自动研究"
echo "   - 配置: $QUANT_DATA_DIR/config.json -> rules.scheduler（默认即可，无需改动）"
echo "   - 查看: journalctl -u quant-research -f / systemctl restart quant-research"
echo " 常用运维："
echo "   journalctl -u quant -f          # 后端日志"
echo "   systemctl restart quant         # 重启后端"
echo "   systemctl reload caddy          # 重载 Caddy 配置"
echo "=============================================="