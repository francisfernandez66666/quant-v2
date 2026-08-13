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
#   LLM_API_URL       LLM API 地址（可选，默认 https://api.siliconflow.cn）
#   LLM_MODEL         LLM 模型名（可选，默认 THUDM/GLM-Z1-9B-0414）
#   DEPLOY_DIR        服务器代码目录（默认 /opt/quant）
#   QUANT_DATA_DIR    服务器数据目录（默认 /var/lib/quant-trading-v2）

set -euo pipefail

# ── 必填参数校验 ──
: "${SERVER_IP:?请设置 SERVER_IP（首尔服务器公网 IP）}"
: "${SERVER_DOMAIN:?请设置 SERVER_DOMAIN（域名，需已解析到 SERVER_IP）}"
SERVER_USER="${SERVER_USER:-root}"
LLM_API_KEY="${LLM_API_KEY:-}"
LLM_API_URL="${LLM_API_URL:-https://api.siliconflow.cn}"
LLM_MODEL="${LLM_MODEL:-THUDM/GLM-Z1-9B-0414}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/quant}"
QUANT_DATA_DIR="${QUANT_DATA_DIR:-/var/lib/quant-trading-v2}"

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$APP_DIR"
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
echo "[1/7] 交叉编译 linux/amd64..."
GOOS=linux GOARCH=amd64 go build -o /tmp/quant_linux ./cmd/quant
echo "      产物: /tmp/quant_linux ($(du -h /tmp/quant_linux | cut -f1))"

# ── 2. 上传二进制 + 配置文件到服务器 ──
echo "[2/7] 上传二进制与配置..."
$SSH "sudo mkdir -p $DEPLOY_DIR/config"
$SCP /tmp/quant_linux $SERVER_USER@$SERVER_IP:/tmp/quant_linux
$SSH "sudo mv /tmp/quant_linux $DEPLOY_DIR/quant && sudo chmod +x $DEPLOY_DIR/quant"
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

# ── 3. 数据目录 + 运行用户 ──
echo "[3/7] 初始化数据目录与运行用户..."
$SSH "sudo mkdir -p $QUANT_DATA_DIR /var/log/caddy"
$SSH "id quant >/dev/null 2>&1 || sudo useradd -r -s /usr/sbin/nologin quant"
$SSH "sudo chown -R quant:quant $QUANT_DATA_DIR $DEPLOY_DIR"

# ── 4. 写入环境变量文件（LLM Key 等敏感项）──
echo "[4/7] 写入 /etc/quant.env..."
ENV_CONTENT="LLM_API_KEY=$LLM_API_KEY"
[ -n "$LLM_API_URL" ] && ENV_CONTENT="$ENV_CONTENT
LLM_API_URL=$LLM_API_URL"
[ -n "$LLM_MODEL" ] && ENV_CONTENT="$ENV_CONTENT
LLM_MODEL=$LLM_MODEL"
$SSH "sudo tee /etc/quant.env >/dev/null" <<EOF
$ENV_CONTENT
EOF
$SSH "sudo chmod 600 /etc/quant.env"

# ── 5. 域名占位符替换 + 安装 Caddy ──
echo "[5/7] 配置 Caddy 域名 ($SERVER_DOMAIN)..."
# Caddyfile 中的占位符改为真实域名
$SSH "sudo sed -i 's/YOUR_DOMAIN_HERE.com/$SERVER_DOMAIN/g' /etc/caddy/Caddyfile"
$SSH "sudo chown -R caddy:caddy /var/log/caddy 2>/dev/null || true"
$SSH "which caddy >/dev/null 2>&1 || (sudo apt-get update -qq && sudo apt-get install -y -qq caddy)"

# ── 6. 安装 systemd 单元并启动 ──
echo "[6/7] 安装并启动 quant.service..."
$SCP "$APP_DIR/deploy/quant.service" $SERVER_USER@$SERVER_IP:/tmp/quant.service
$SSH "sudo mv /tmp/quant.service /etc/systemd/system/quant.service"
$SSH "sudo systemctl daemon-reload"
$SSH "sudo systemctl enable --now quant"
$SSH "sudo systemctl restart caddy"

# ── 7. 健康检查 ──
echo "[7/7] 健康检查..."
sleep 3
if $SSH "curl -sf -o /dev/null -m 10 http://127.0.0.1:8080/setup"; then
    echo "  ✓ 后端进程已响应 (127.0.0.1:8080)"
else
    echo "  ✗ 后端未就绪，请查看日志: journalctl -u quant -n 50"
fi
# HTTPS 检查（首次 ACME 申请可能要等几秒~几十秒）
echo "  等待 HTTPS 证书就绪 (最多 60s)..."
for i in $(seq 1 60); do
    if $SSH "curl -sf -o /dev/null -m 10 https://$SERVER_DOMAIN/setup"; then
        echo "  ✓ HTTPS 已就绪: https://$SERVER_DOMAIN/setup"
        break
    fi
    sleep 1
    [ "$i" = "60" ] && echo "  ✗ HTTPS 未就绪，检查: journalctl -u caddy -n 50（确认域名 DNS 已生效）"
done

echo "=============================================="
echo " 部署完成。首次登录："
echo "   1. 浏览器/APK 打开 https://$SERVER_DOMAIN"
echo "   2. 首次启动系统已自动创建默认账号 admin / admin123"
echo "   3. 登录后请立即改密码"
echo " 常用运维："
echo "   journalctl -u quant -f          # 后端日志"
echo "   systemctl restart quant         # 重启后端"
echo "=============================================="