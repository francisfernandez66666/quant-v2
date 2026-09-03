#!/bin/bash
# 一键部署到广州腾讯云 Win Server 2022（单机全合一，docs/MIGRATION_GUANGZHOU_ALLINONE.md §2/§4）。
#
# 设计要点（防错）：
#   * 首次部署默认「影子模式」：引擎启动但 qmt.enabled=false → NoopExecutor，只做评分/记账，
#     不下真实单，与现有首尔决策链路零冲突（可并行一周验证，见 M3）。
#   * 真正切流（gateway report_url 改 localhost + qmt.enabled=true + 关首尔）在 M4 手动执行。
#
# 用法（本地 macOS，需 Windows OpenSSH 已开、且本机公钥已加入管理员 authorized_keys）：
#   GZ_IP=81.71.69.17 LLM_API_KEY=sk-xxx ./scripts/deploy_guangzhou.sh
#   GZ_IP=81.71.69.17 ./scripts/deploy_guangzhou.sh -s     # 仅同步二进制不重注册服务
#
# 参数（环境变量）：
#   GZ_IP            广州公网 IP（必填）
#   GZ_USER          管理员用户（默认 Administrator）
#   LLM_API_KEY      LLM Key（写入引擎服务环境变量；留空则保留服务器现有）
#   LLM_API_URL      默认 https://api.siliconflow.cn/v1/chat/completions
#   LLM_MODEL        默认 THUDM/GLM-Z1-9B-0414
#   DEPLOY_DIR       Windows 目录（默认 C:/opt/quant）
#   DATA_DIR         数据目录（默认 C:/var/lib/quant-trading-v2）
#   MINIQMT_PATH     QMT 完整交易端 XtItClient.exe 路径（默认 C:/Program Files (x86)/东莞证券QMT实盘交易端/bin.x64/XtItClient.exe；
#                    注意：必须是 XtItClient.exe——可自动登录交易；不能是 XtMiniQmt.exe，后者无法自动登录，
#                    会导致 broker 永远连不上）

set -euo pipefail

SYNC_ONLY=0
while getopts ":s" opt; do case $opt in s) SYNC_ONLY=1 ;; *) ;; esac; done

: "${GZ_IP:?请设置 GZ_IP（广州服务器公网 IP）}"
GZ_USER="${GZ_USER:-Administrator}"
LLM_API_KEY="${LLM_API_KEY:-}"
LLM_API_URL="${LLM_API_URL:-https://api.siliconflow.cn/v1/chat/completions}"
LLM_MODEL="${LLM_MODEL:-THUDM/GLM-Z1-9B-0414}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
MINIQMT_PATH="${MINIQMT_PATH:-C:/Program Files (x86)/东莞证券QMT实盘交易端/bin.x64/XtItClient.exe}"

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$APP_DIR"

SSH="ssh -o StrictHostKeyChecking=accept-new ${GZ_USER}@${GZ_IP}"
SCP="scp -o StrictHostKeyChecking=accept-new"

echo "=============================================="
echo " quant-trading-v2 -> 广州 Win Server"
echo " IP:      $GZ_IP"
echo " 目录:    $DEPLOY_DIR (二进制) / $DATA_DIR (数据)"
echo " 模式:    $([ $SYNC_ONLY -eq 1 ] && echo 仅同步 || echo 同步+注册服务)"
echo "=============================================="

# ── 1. 交叉编译 windows/amd64 ──
echo "[1/5] 交叉编译 windows/amd64..."
# §R6 P1-1 二进制指纹：把 git 短 SHA 注入 quant buildCommit，启动自检可比对线上版本是否漂移
LDFLAGS="-X main.buildCommit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
GOOS=windows GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o /tmp/quant.exe      ./cmd/quant
GOOS=windows GOARCH=amd64 go build -o /tmp/researchd.exe  ./cmd/researchd
GOOS=windows GOARCH=amd64 go build -o /tmp/dataload.exe   ./cmd/dataload
GOOS=windows GOARCH=amd64 go build -o /tmp/research.exe   ./cmd/research
GOOS=windows GOARCH=amd64 go build -o /tmp/qmtctl.exe     ./cmd/qmtctl
echo "      产物: $(ls -lh /tmp/*.exe | awk '{print $5, $9}') (quant buildCommit=$LDFLAGS)"

# ── 2. 上传二进制 + 部署脚本 + qmtctl ──
echo "[2/5] 上传二进制/脚本到 $DEPLOY_DIR ..."
$SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path $DEPLOY_DIR, $DATA_DIR, ${DEPLOY_DIR}/qmt-win, ${DEPLOY_DIR}/pydata | Out-Null\""
$SCP /tmp/quant.exe /tmp/researchd.exe /tmp/dataload.exe /tmp/research.exe /tmp/qmtctl.exe "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/"
$SCP deploy/qmt-win/register_engine_services.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
# baostock sidecar
$SCP cmd/pydata/server.py cmd/pydata/requirements.txt "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/pydata/"

# ── 3. 数据目录 + 默认 config.json（影子模式：qmt.enabled=false）──
echo "[3/5] 初始化数据目录 + 默认 config.json（影子模式）..."
$SSH "powershell -NoProfile -Command \"
if (-not (Test-Path '$DATA_DIR/config.json')) {
  \$cfg = [ordered]@{
    qmt = [ordered]@{ enabled = \$false; mode = 'manual'; gateway_url = 'http://127.0.0.1:8789' }
    rules = [ordered]@{ scheduler = [ordered]@{} ; paper = [ordered]@{ enabled = \$false } }
  }
  [System.IO.File]::WriteAllText('$DATA_DIR/config.json', (\$cfg | ConvertTo-Json -Depth 4))
  Write-Host '  已生成默认 config.json (qmt.enabled=false, 影子模式)'
} else { Write-Host '  保留现有 config.json（未覆盖）' }
\""

# ── 4. 注册 Windows 服务（NSSM）+ qmtctl 任务计划 ──
if [ $SYNC_ONLY -eq 1 ]; then
  echo "[4/5] 跳过服务注册（-s）"
else
  echo "[4/5] 远程注册服务（管理员 PowerShell）..."
  REMOTE_ARGS="-QuantExe ${DEPLOY_DIR}/quant.exe -ResearchExe ${DEPLOY_DIR}/researchd.exe -PydataVenv ${DEPLOY_DIR}/venv -QmtctlExe ${DEPLOY_DIR}/qmtctl.exe -MiniQmtPath ${MINIQMT_PATH} -DataDir ${DATA_DIR}"
  if [ -n "$LLM_API_KEY" ]; then
    REMOTE_ARGS="$REMOTE_ARGS -LLMApiKey '$LLM_API_KEY' -LLMApiURL '$LLM_API_URL' -LLMModel '$LLM_MODEL'"
  fi
  $SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/qmt-win/register_engine_services.ps1 $REMOTE_ARGS"
fi

# ── 5. 健康检查 ──
echo "[5/5] 健康检查（本机 127.0.0.1:8080）..."
sleep 3
if $SSH "powershell -NoProfile -Command \"(Invoke-WebRequest -Uri http://127.0.0.1:8080/setup -UseBasicParsing -TimeoutSec 10).StatusCode -eq 200\"" 2>/dev/null; then
  echo "  OK 引擎已响应 (127.0.0.1:8080)"
else
  echo "  X 引擎未就绪，远程查看: Get-Service quant ; 日志在 $DATA_DIR/logs 或 nssm 日志"
fi
echo "=============================================="
echo " 影子部署完成。当前 qmt.enabled=false -> 不下真实单，与首尔并行验证安全。"
echo " 验证：http://${GZ_IP}:8080/setup（创建管理员）/ 看研究调度日志。"
echo " 真正切流（M4）：见 docs/MIGRATION_GUANGZHOU_ALLINONE.md §2 切流清单。"
echo "=============================================="
