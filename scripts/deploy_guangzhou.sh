#!/bin/bash
# 一键部署到广州腾讯云 Win Server 2022（单机全合一，docs/MIGRATION_GUANGZHOU_ALLINONE.md §2/§4）。
#
# 设计要点（防错）：
#   * 首次部署默认「影子模式」：引擎启动但 qmt.enabled=false → NoopExecutor，只做评分/记账，
#     不下真实单，与现有首尔决策链路零冲突（可并行一周验证，见 M3）。
#   * 真正切流（gateway report_url 改 localhost + qmt.enabled=true + 关首尔）在 M4 手动执行。
#   * §A7 前端指纹一致性（2026-09-20 补）：本脚本必须同步 web/dist → 云端 Caddy 站点根
#     $DEPLOY_DIR/web。漏传会让浏览器端内嵌/访问的前端冻结在旧 buildCommit，持续误报
#     「前端与服务器版本不一致」。见步 [2c]（含 npm 构建兜底 + tar 打包 + 清 ._* 垃圾）。
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
#   QMT_GATEWAY_DIR  qmt_gateway Python 网关目录（默认 C:/qmt/quant-trading-v2/qmt_gateway；
#                    §QMT-DUAL 需同步含 qmt_bridge.py 的网关文件到此目录）
#   MINIQMT_PATH     QMT 完整交易端 XtItClient.exe 路径（默认 C:/Program Files (x86)/东莞证券QMT实盘交易端/bin.x64/XtItClient.exe；
#                    注意：必须是 XtItClient.exe——可自动登录交易；不能是 XtMiniQmt.exe，后者无法自动登录，
#                    会导致 broker 永远连不上）
#   GATEWAY_TOKEN    §M7c 网关 config.xt.json 的 token（留空则首次生成时现场随机 48 位 hex 并打印，
#                    需抄存并同步到引擎 rules.qmt.token；已存在的 config.xt.json 不会被覆盖）
#   GATEWAY_ACCOUNT  §M7c 东莞证券资金账号（留空 → broker=mock 影子期存活，不接真实柜台）
#   XT_USERDATA_PATH §M7c QMT userdata_mini 目录（broker=xt 时必需）

set -euo pipefail

SYNC_ONLY=0
while getopts ":s" opt; do case $opt in s) SYNC_ONLY=1 ;; *) ;; esac; done

: "${GZ_IP:?请设置 GZ_IP（广州服务器公网 IP）}"
GZ_USER="${GZ_USER:-Administrator}"
LLM_API_KEY="${LLM_API_KEY:-}"
# 默认仅注册服务时写入 bootstrap 环境变量；§UI-AUTHORITATIVE（2026-09-14）起
# 运行期以「设置页保存」的配置为最高优先级，这里改默认值不会再顶掉 UI 配置。
# 旧默认 siliconflow/GLM 已弃用（kiraai 免费档为现网生效通道）。
LLM_API_URL="${LLM_API_URL:-https://kiraai.vn/v1/chat/completions}"
LLM_MODEL="${LLM_MODEL:-qwen3.8-flash-free}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
QMT_GATEWAY_DIR="${QMT_GATEWAY_DIR:-C:/qmt/quant-trading-v2/qmt_gateway}"
MINIQMT_PATH="${MINIQMT_PATH:-C:/Program Files (x86)/东莞证券QMT实盘交易端/bin.x64/XtItClient.exe}"

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$APP_DIR"

# IdentitiesOnly + 指定密钥：agent 里若挂多把 key，ssh 会逐把尝试直至撞服务端
# MaxAuthTries 上限跌落到密码认证（2026-09-18 部署实录：脚本卡在 password 提示，
# 手工 BatchMode ssh 正常——差异即在 agent key 枚举）。~/.ssh/config 的 Host 条目
# 已指定 IdentityFile ~/.ssh/id_rsa，此处显式声明与之对齐，保证任何会话形态下确定性行为。
SSH="ssh -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"

# §DEPLOY-PREFLIGHT（2026-09-20）：本脚本若被放进「网络可通但私钥不可读」的执行上下文
# （沙箱/受限会话），ssh 会静默回退密码认证并把提示写向 tty —— 脚本自身的 stdout 重定向
# 吞不掉它，表现为**无任何输出的挂死**（实测白等 30 分钟、服务一条都没停，远端零副作用）。
# 因此开跑前先做一次 BatchMode 探活：认证不可用就立刻失败，绝不进入可能挂起的路径。
# 手工 ssh 会被放行、脚本内 ssh 不会，是本机环境的既有差异（见 RUNBOOK §4.1b.1 ⑦）。
if ! ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
         -o IdentitiesOnly=yes -i "$HOME/.ssh/id_rsa" "${GZ_USER}@${GZ_IP}" 'echo ok' >/dev/null 2>&1; then
  echo "X 非交互 SSH 认证失败，已中止（避免静默挂死在密码提示上）。" >&2
  echo "  排查：① $HOME/.ssh/id_rsa 是否可读（权限/沙箱）② 公钥是否在远端 authorized_keys" >&2
  echo "  提示：若在受限执行上下文里跑本脚本，网络可能通但私钥不可读 → ssh 回退密码认证。" >&2
  exit 1
fi

# §A7-C（2026-09-20 实录补）：停服必须有兜底拉起，否则「停」是不可逆的。
#
# [2/5] 为释放 exe 文件锁会 `net stop quant; net stop quant-research`，此后**只有 [4/5]
# register 会拉起它们**。这条链有两个口子会把线上引擎永久留在停机态：
#   ① `-s`（仅同步）模式下 [4/5] 被跳过 —— 而 `-s` 恰恰是 RUNBOOK §4.1b 推荐的发布命令；
#   ② 任何早于 [4/5] 的失败（scp 失败、[2c] 指纹校验中止、构建失败…）在 `set -e` 下直接退出。
# 2026-09-20 实录踩中 ②：前端重建被本机 node 删除守卫拦下 → 脚本在 [2c] 退出，
# 线上引擎停在停机态约 3 分钟，靠人工 `nssm start` 才救回（当时无任何提示说明服务已停）。
# 现加 EXIT 兜底：只要本次停过服务且尚未被拉起，无论脚本以何种方式退出都补一次 start。
NSSM='C:/opt/quant/qmt-win/tools/nssm-2.24/win64/nssm.exe'
SERVICES_STOPPED=0
start_engine_services() {
  echo "[cleanup] 拉起部署期间停掉的服务（quant / quant-research）..."
  $SSH "powershell -NoProfile -Command \"& '${NSSM}' start quant ; & '${NSSM}' start quant-research\"" 2>/dev/null || true
}
restore_services_on_exit() {
  rc=$?
  # 用 if 而非 `[ … ] && fn`：后者在条件不成立时返回 1，会让 set -e 在 exit 之前打断本函数。
  if [ "$SERVICES_STOPPED" = "1" ]; then
    start_engine_services
  fi
  exit $rc
}
trap restore_services_on_exit EXIT

# ps1_bom <file>：上传前把 Windows PowerShell 脚本归一为「UTF-8 单 BOM + CRLF」。
# 为何需要：PS 5.1 读无 BOM 的 UTF-8 按 GBK 解析中文注释会撕裂字面量直接 ParserError（现网实录）；
# 但历史上 restart_gateway.ps1 等已自带 BOM，若再无条件 cat 拼一个就成双 BOM——PS 报
# 「无法将「?#」识别为 cmdlet」（本次部署实录：[3b] restart_gateway 首行噪音）。
# 本函数幂等：先剥掉所有前导 BOM，再补恰好一个；不动已规范的字节序（CRLF 保留）。
ps1_bom() {
  python3 - "$1" <<'PY'
import sys
p = sys.argv[1]
d = open(p, 'rb').read()
while d.startswith(b'\xef\xbb\xbf'):
    d = d[3:]
open(p, 'wb').write(b'\xef\xbb\xbf' + d)
PY
}

echo "=============================================="
echo " quant-trading-v2 -> 广州 Win Server"
echo " IP:      $GZ_IP"
echo " 目录:    $DEPLOY_DIR (二进制) / $DATA_DIR (数据)"
echo " 模式:    $([ $SYNC_ONLY -eq 1 ] && echo 仅同步 || echo 同步+注册服务)"
echo "=============================================="

# ── 1. 交叉编译 windows/amd64 ──
echo "[1/5] 交叉编译 windows/amd64..."
# §R6 P1-1 二进制指纹：把 git 短 SHA 注入 quant buildCommit，启动自检可比对线上版本是否漂移
# §A7-B（2026-09-20）：同一份 SHA 也用于校验前端产物是否配套（见步 [2c]），所以提到外面只算一次。
GIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
LDFLAGS="-X main.buildCommit=${GIT_SHA}"
GOOS=windows GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o /tmp/quant.exe      ./cmd/quant
GOOS=windows GOARCH=amd64 go build -o /tmp/researchd.exe  ./cmd/researchd
GOOS=windows GOARCH=amd64 go build -o /tmp/dataload.exe   ./cmd/dataload
GOOS=windows GOARCH=amd64 go build -o /tmp/research.exe   ./cmd/research
GOOS=windows GOARCH=amd64 go build -o /tmp/qmtctl.exe     ./cmd/qmtctl
echo "      产物: $(ls -lh /tmp/*.exe | awk '{print $5, $9}') (quant buildCommit=$LDFLAGS)"

# ── 2. 上传二进制 + 部署脚本 + qmtctl ──
echo "[2/5] 上传二进制/脚本到 $DEPLOY_DIR ..."
# 2026-09-18 部署实录：NSSM 运行中的 quant/quant-research 锁死旧 exe，scp 直接
# "dest open Failure"（register 脚本是 stop→install→start，救不了上传阶段的锁）。
# 先显式停服释放文件锁——服务重启本就属于本次部署语义（步 [4/5] 注册即拉起）。
$SSH "powershell -NoProfile -Command \"net stop quant; net stop quant-research; exit 0\"" >/dev/null 2>&1 || true
SERVICES_STOPPED=1   # 置位后由 [4/5]/[4/5]-s 拉起，任何提前退出都由 EXIT 兜底补 start
$SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path $DEPLOY_DIR, $DATA_DIR, ${DEPLOY_DIR}/qmt-win, ${DEPLOY_DIR}/pydata | Out-Null\""
$SCP /tmp/quant.exe /tmp/researchd.exe /tmp/dataload.exe /tmp/research.exe /tmp/qmtctl.exe "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/"
$SCP deploy/qmt-win/register_engine_services.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
# §H8(2026-09-22) 探针端口/端点同源配置文件——register 与运维探针（all_service_watchdog/
# daily_ops_check）都 dot-source 它，必须随 register 同目录上传，缺了 register 会回退字面量并告警。
ps1_bom deploy/qmt-win/service_probe_config.ps1
$SCP deploy/qmt-win/service_probe_config.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
# §RFIX-5 日志保留脚本（register 第 6 段据此注册 Quant-Log-Prune 计划任务；新增部署文件
# 必须入本清单——教训见 §ENH-5 quote_feed.py 漏列导致网关 ImportError 起不来）
ps1_bom deploy/qmt-win/prune_logs.ps1
$SCP deploy/qmt-win/prune_logs.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
# baostock sidecar
$SCP cmd/pydata/server.py cmd/pydata/requirements.txt "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/pydata/"

# ── 2b. 同步 qmt_gateway Python 网关（§QMT-DUAL：含 qmt_bridge.py 策略桥；
#      §CB-TICKWINDOW 2026-09-21 起 qmt_bridge_strategy.py 也入列——曾因不在清单，
#      现网策略文件停在 9/18 手工拷贝，桥侧修复不会随部署下发）──
echo "[2b/5] 同步 qmt_gateway 到 $QMT_GATEWAY_DIR ..."
$SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path $QMT_GATEWAY_DIR | Out-Null\""
$SCP qmt_gateway/gateway.py qmt_gateway/broker.py qmt_gateway/handler.py \
     qmt_gateway/store.py qmt_gateway/ids.py qmt_gateway/qmt_bridge.py \
     qmt_gateway/qmt_bridge_strategy.py \
     qmt_gateway/trading_calendar.py \
     qmt_gateway/quote_feed.py \
     qmt_gateway/config.bridge.example.json \
     "${GZ_USER}@${GZ_IP}:${QMT_GATEWAY_DIR}/"
# §M7c（2026-09-22 修复批）config.xt.json 收编：gateway_watchdog.ps1 依赖的
# <QMT_GATEWAY_DIR>/config.xt.json 过去只有 setup_windows.ps1 手工路径才生成，脚本化部署
# 从不下发 → 全新机器网关秒起秒死、watchdog 空转。现随部署执行幂等 ensure（已存在不覆盖；
# listen 端口 dot-source §H8 service_probe_config.ps1 同源）。可选注入：
#   GATEWAY_TOKEN 网关 token（留空现场随机生成并打印，需抄到引擎 rules.qmt.token）
#   GATEWAY_ACCOUNT 资金账号（留空 broker=mock 影子期存活）
#   XT_USERDATA_PATH QMT userdata_mini 目录（broker=xt 时必需）
ps1_bom deploy/qmt-win/ensure_gateway_config.ps1
$SCP deploy/qmt-win/ensure_gateway_config.ps1 deploy/qmt-win/config.xt.template.json \
     "${GZ_USER}@${GZ_IP}:${QMT_GATEWAY_DIR}/"
# §M7c 实参拼装：powershell -File 会把空引号参数 '' 整个吞掉（2026-09-22 部署实录：
# 留空 Token 时 PS 报 MissingArgument「缺少参数 Token」，[2b] 直接中断部署链）。
# 故三个可选参数只在非空时传递，缺省值交给脚本自身的 param 默认。
gw_extra=""
[ -n "${GATEWAY_TOKEN:-}" ]   && gw_extra="$gw_extra -Token '$GATEWAY_TOKEN'"
[ -n "${GATEWAY_ACCOUNT:-}" ] && gw_extra="$gw_extra -Account '$GATEWAY_ACCOUNT'"
[ -n "${XT_USERDATA_PATH:-}" ] && gw_extra="$gw_extra -XtPath '$XT_USERDATA_PATH'"
$SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${QMT_GATEWAY_DIR}/ensure_gateway_config.ps1 -GatewayDir '${QMT_GATEWAY_DIR}'${gw_extra} -ProbeConfigPath '${DEPLOY_DIR}/qmt-win/service_probe_config.ps1'"

# ── 2c. 同步前端 web/dist 到云端 Caddy 站点根（§A7 版本漂移根治）──
# 根因：旧版脚本只同步二进制/gateway/pydata，从不传 web/dist → 云端 Caddy 前端
#       冻结在旧 buildCommit，浏览器端触发「前端与服务器版本不一致」横幅（2026-09-20 实录）。
#       现改为：本地 web/dist 缺失则先 npm run build 生成，再 tar 打包传云端解包到
#       $DEPLOY_DIR/web（Caddy :8080 站点根）；并清除 macOS 打包产生的 ._* 垃圾。
#
# §A7-B（2026-09-20 补，第二次同类事故）：**光"传前端"不够，还得保证传的是"这一版"前端**。
#   实录：本地先 `npm run build`、之后才 commit，于是 dist 内嵌指纹停在上一版（50f3594），
#   后端用新 SHA（f78c74b）上线 → 用户立刻看到版本不一致横幅。旧脚本的
#   `if [ ! -d web/dist ]` 分支写了"产物存在就原样上传"，所以**部署成功 ≠ 产物是新的**。
#   现改为三道校验，任一不过即重建/中止，不再靠人记得"先提交再构建"：
#     ① 产物不存在        → 构建
#     ② 产物自称的 SHA ≠ 待部署 SHA（dist/BUILD_COMMIT，由 vite closeBundle 落盘）→ 强制重建
#     ③ 构建完仍不相等（git 不可用会退化成 'dev'）→ 中止，绝不把漂移的前端传上去
echo "[2c/5] 同步前端 web/dist 到 $DEPLOY_DIR/web ..."
DIST_SHA_FILE="web/dist/BUILD_COMMIT"
dist_sha() { [ -f "$DIST_SHA_FILE" ] && tr -d '\r\n' < "$DIST_SHA_FILE" || echo ""; }

NEED_BUILD=0
if [ ! -d web/dist ]; then
  echo "  web/dist 不存在，先构建前端..."
  NEED_BUILD=1
else
  ds="$(dist_sha)"
  if [ "$ds" != "$GIT_SHA" ]; then
    echo "  [!] 前端产物陈旧：dist/BUILD_COMMIT='${ds:-缺失}' ≠ 待部署 '${GIT_SHA}' → 强制重建"
    echo "      （这正是「前端与服务器版本不一致」横幅的成因：先 build 后 commit）"
    NEED_BUILD=1
  fi
fi
if [ "$NEED_BUILD" = "1" ]; then
  # 构建失败**不能**直接让脚本在 set -e 下退出：那会跳过下面的指纹校验（真正的判据），
  # 也跳过后面的收尾（2026-09-20 实录：构建失败 → 脚本静默退出 → 服务停在停机态）。
  # 这里吞掉退出码，把结论交给指纹校验统一裁决：等 → 上传；不等 → 明确中止并给排查指引。
  if ! ( cd web && { npm ci --no-audit --no-fund >/dev/null 2>&1 || npm install --no-audit --no-fund >/dev/null 2>&1; } && npm run build ); then
    echo "  [!] 前端构建失败（退出码非 0，详情见上方 vite 输出）——交由下一步指纹校验裁决。"
  fi
fi

if [ -d web/dist ]; then
  ds="$(dist_sha)"
  if [ "$GIT_SHA" != "unknown" ] && [ "$ds" != "$GIT_SHA" ]; then
    echo "  X 前端产物指纹 '${ds:-缺失}' 与待部署 '${GIT_SHA}' 不一致，中止部署。"
    echo "    这会让线上前端与后端指纹不符，用户端必然出现版本不一致横幅。"
    echo "    排查：web/vite.config.js 的 buildCommit() 是否拿不到 git（回退 'dev'）。"
    exit 1
  fi
  # ${ds} 必须带花括号：紧跟其后的全角「）」会被 bash 并入变量名（实测报
  # `ds）: unbound variable`，在 set -u 下直接中止部署）——本行曾因此漏过首次上传。
  echo "  OK 前端指纹校验通过（BUILD_COMMIT=${ds}）"
  $SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path ${DEPLOY_DIR}/web | Out-Null\""
  tar -czf /tmp/webdist.tgz -C web/dist .
  $SCP /tmp/webdist.tgz "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/webdist.tgz"
  # 尾部的 `; exit 0` 是必需的，不是保守写法：`Get-ChildItem -Filter '._*'` 在**没有匹配项**时
  # 会置 $? 为 false → powershell.exe 退出码 1 → 它是本条 ssh 的最后一条语句 → `set -e` 直接
  # 把脚本打死在 [2c]。2026-09-20 实测（新解压的 web 目录天然没有 ._* 文件，即**首次必踩**）：
  # 前端其实已同步成功，但脚本在收尾处静默退出，[3/5] 之后的全部步骤被跳过。
  # 这正是 RUNBOOK 长期把「前端 dist 发布」列为"脚本外手动流程"的原因——[2c] 从未跑完过。
  # 本步是尽力而为的垃圾清理，失败不得中断部署。
  $SSH "powershell -NoProfile -Command \"tar -xzf ${DEPLOY_DIR}/webdist.tgz -C ${DEPLOY_DIR}/web; Remove-Item -Force ${DEPLOY_DIR}/webdist.tgz; Get-ChildItem -Path ${DEPLOY_DIR}/web -Recurse -Filter '._*' | Remove-Item -Force -ErrorAction SilentlyContinue; exit 0\"" || true
  rm -f /tmp/webdist.tgz
  echo "  OK 前端已同步到 $DEPLOY_DIR/web"
else
  echo "  X web/dist 构建失败，跳过前端同步（其余部署照常进行）"
fi

# ── 2d. quant-web（Caddy）站点注册与配置刷新（§M7b，2026-09-22 修复批）──
# 根因：verify_deploy_guangzhou.sh 的探针清单一直要求 quant/quant-research/pydata/quant-web
#       四服务全 Running，但 quant-web（Caddy）的注册与 Caddyfile 只存在于 GUANGZHOU_CADDY.md
#       的手工流程里，部署脚本从不管它——「部署面」与「校验面」各说各话（§M7 审计）。
# 现状：注册脚本 deploy/qmt-win/register_web_service.ps1 幂等收编三件事——
#       ① caddy.exe 缺失自动下载预置；② Caddyfile validate 通过且内容变化才 .bak 备份替换；
#       ③ NSSM 服务 quant-web 不存在则 install、存在则纠正参数后按需 restart（配置没变不闪断）。
#       健康口取自 §H8 service_probe_config.ps1 的 $ProbeCaddyPort，与运维探针同源。
# -s（仅同步）模式同样执行：同步发布也应保证站点配置随代码走（脚本自身幂等，代价仅一次校验/比对）。
echo "[2d/5] 同步 Caddy 站点配置并确保 quant-web 服务注册 (§M7b) ..."
if [ ! -d web/dist ]; then
  echo "  [!] web/dist 缺失（上一步构建失败？）——Caddy 站点根将为空，仍注册服务但前端必然 404，先修构建"
fi
ps1_bom deploy/qmt-win/register_web_service.ps1
$SCP deploy/caddy/guangzhou.conf "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/guangzhou.conf.new"
$SCP deploy/qmt-win/register_web_service.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
$SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/qmt-win/register_web_service.ps1 -DeployDir '${DEPLOY_DIR}' -CaddyConfSrc '${DEPLOY_DIR}/guangzhou.conf.new' -ProbeConfigPath '${DEPLOY_DIR}/qmt-win/service_probe_config.ps1'" \
  || echo "  [!] quant-web 注册/健康确认未通过（退出码非 0）——引擎与网关部署不受影响，但 verify_deploy 的 svc:quant-web / web:/ 探针会红，按上方输出修复后单独重跑本步或 ./scripts/verify_deploy_guangzhou.sh 复核"

# ── 2e. 夜间快照备份链随部署下发（§P0-B 收编，2026-09-23）──
# 背景：backup_snapshot.ps1 / backup_snap.py 历史上是**手工安装**，一直不在本脚本的 scp 清单里。
#       于是 §P0-B 把备份对象从「trading.db + 9 个 JSON」扩到「+ live.db + accounts/ 整目录」之后，
#       改好的脚本仍躺在仓库、现网 04:00 跑的还是只快照 trading.db 的旧版——施工面修好了、部署面
#       没有通路，结果就是**实盘四本账依旧无灾备**（同族教训：§ENH-5 quote_feed.py 漏列、
#       §A5-DEPLOY trading_calendar.py 漏列，新增部署文件必须入清单）。
# 落盘目录刻意取 ${DEPLOY_DIR}/deploy/qmt-win 而非其它 qmt-win：这是 register_backup_task.ps1
#       的默认路径、RUNBOOK_LIVEBACKUP §2 的手工安装路径、以及现网在跑任务指向三者同源的位置；
#       换目录等于制造"部署更新一份、任务执行另一份"的双份脚本漂移。
# 语义：上传 + 幂等重注册（先删后建，任务参数与脚本在位性一并核对；脚本内容随 scp 已更新，
#       在跑任务下次触发即载入新版）。注册失败**只告警不阻断**——灾备链坏了不该把前后端发布
#       卡在半态，但 verify 第 16 探针会持续判红直到修好。
echo "[2e/5] 同步广州夜间快照备份链 (§P0-B) ..."
BACKUP_DIR="${DEPLOY_DIR}/deploy/qmt-win"
$SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path $BACKUP_DIR | Out-Null\""
ps1_bom deploy/qmt-win/backup_snapshot.ps1
ps1_bom deploy/qmt-win/register_backup_task.ps1
$SCP deploy/qmt-win/backup_snapshot.ps1 deploy/qmt-win/backup_snap.py deploy/qmt-win/register_backup_task.ps1 \
     "${GZ_USER}@${GZ_IP}:${BACKUP_DIR}/"
$SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${BACKUP_DIR}/register_backup_task.ps1 -ScriptPath ${BACKUP_DIR}/backup_snapshot.ps1" \
  || echo "  [!] 快照计划任务注册未通过（退出码非 0）——前后端发布不受影响，但 §P0-B 灾备尚未生效：按 RUNBOOK_LIVEBACKUP.md §2 处理后重跑本步即可"

# ── 3. 数据目录 + 默认 config.json（影子模式：qmt.enabled=false）──
# §UAT 20260915 部署加固：原内联 SSH 命令的 bash→PS 双层转义在每个部署日都报 ParserError
# 噪音，现抽为独立脚本上传后执行（转义链路归零；已有 config 一律保留不覆盖）。
echo "[3/5] 初始化数据目录 + 默认 config.json（影子模式）..."
ps1_bom deploy/qmt-win/init_default_config.ps1
$SCP deploy/qmt-win/init_default_config.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
$SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/qmt-win/init_default_config.ps1 -DataDir ${DATA_DIR}"

# ── 3a. 根级 qmt 死键清理（§ROOTQMT，2026-09-22 C批）──
# 背景：旧生成器曾把 qmt 写在 config.json **根级**，而 Go 解析侧 wrapper 只认 rules/d1 两段
#       （internal/config/config.go Manager.Load），根级残留被静默忽略——行为无差但持续误导排障。
#       层级修正（§M7a）后新写的文件已正确落在 rules.qmt，存量残留靠本步收口。
# 竞态澄清：引擎/researchd 的 Save 是全量落盘且只写 {rules,d1}，清理后不存在回写风险；
#           引擎每分钟热载（§P1-6），改完自动生效，无需重启服务。
# 语义：脚本幂等（无残留输出 ROOTQMT: CLEAN），有残留则先备份再原子写回；本步失败**不中断部署**
#       （与 [2d] 同姿势）——配置面残留不该让整条链半途停在停机态，交人工在 verify 前处理。
echo "[3a/5] 清理 config.json 根级 qmt 死键 (§ROOTQMT) ..."
ps1_bom deploy/qmt-win/clean_root_qmt.ps1
$SCP deploy/qmt-win/clean_root_qmt.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
$SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/qmt-win/clean_root_qmt.ps1 -ConfigPath '${DATA_DIR}/config.json'" \
  || echo "  [!] 根级 qmt 清理未通过（PARSE_FAIL/ROLLBACK，详见上方 ROOTQMT 输出）——不阻断部署，verify 前人工处理"

# ── 3b. 重启 qmt_gateway（§UAT 20260915 新增）──
# 背景：步 [2b] 同步了网关 .py，但旧流程不重启网关——新代码要等 5 分钟粒度的
# QMT-Gateway-Ensure 计划任务"碰巧"拉起才生效（2026-09-15 部署实录：/settlement 端点
# 延迟上线，手动 kill 网关 python 后由 ensure 任务自动拉起新代码）。现部署即重启：
# 杀掉 gateway python 进程 → ensure/watchdog 守护自动重拉 → 轮询 /health 直至就绪。
# RESTART_GATEWAY=0 可跳过（如只想同步文件、不动交易时段的网关连接）。
if [ "${RESTART_GATEWAY:-1}" = "1" ]; then
  echo "[3b/5] 重启 qmt_gateway（载入新网关代码）..."
  # 杀进程逻辑抽为独立 PS1（避免 bash→SSH→PS 三层引号转义链，§UAT 20260915 教训）；
  # ensure/watchdog 守护 3s 自动重拉，下方轮询 /health 确认就绪。
  ps1_bom deploy/qmt-win/restart_gateway.ps1
$SCP deploy/qmt-win/restart_gateway.ps1 "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/qmt-win/"
  $SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/qmt-win/restart_gateway.ps1"
  # 轮询 /health 至多 120s：ensure 计划任务 5 分钟粒度，兜底主动触发一次
  gw_ok=0
  for i in $(seq 1 24); do
    sleep 5
    if $SSH "powershell -NoProfile -Command \"try { (Invoke-WebRequest -Uri http://127.0.0.1:8789/health -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200 } catch { exit 1 }\"" 2>/dev/null; then
      gw_ok=1
      break
    fi
  done
  if [ $gw_ok -eq 0 ]; then
    echo "  /health 未就绪，主动触发 QMT-Gateway-Ensure 计划任务..."
    $SSH "schtasks /Run /TN QMT-Gateway-Ensure" || true
    for i in $(seq 1 24); do
      sleep 5
      if $SSH "powershell -NoProfile -Command \"try { (Invoke-WebRequest -Uri http://127.0.0.1:8789/health -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200 } catch { exit 1 }\"" 2>/dev/null; then
        gw_ok=1
        break
      fi
    done
  fi
  [ $gw_ok -eq 1 ] && echo "  OK 网关已就绪 (127.0.0.1:8789/health)" || echo "  X 网关未就绪：远程查看 C:/qmt/quant-trading-v2/qmt_gateway/gateway*.log"
else
  echo "[3b/5] 跳过网关重启（RESTART_GATEWAY=0）"
fi

# ── 3c. APK 分发同步（§APPVER 可选步，2026-09-22 C批）──
# 背景：客户端强制更新改为服务端驱动（GET /api/app/version 下发 apk_url），安装包二进制不入库，
#       由本机 release 构建产物随部署上传，Caddy 以 /dl/* 暴露（见 deploy/caddy/guangzhou.conf）。
# 落盘目录必须与 Caddy /dl 块的 root 同源：C:\opt\quant\apk（即 ${DEPLOY_DIR}/apk，
#       不要写成 ${DEPLOY_DIR}/../apk —— 那会跑到 C:/opt/apk，下载必 404）。
# 语义：**可选** —— 本机没有 release 产物（缺 MOBILE_KEYSTORE_PASS）时只提示不失败，
#       APK 缺失不该阻塞前后端发布。
echo "[3c/5] APK 分发同步（§APPVER 可选步）..."
APK_LOCAL="mobile/app/build/outputs/apk/release/app-release.apk"
if [ -f "$APK_LOCAL" ]; then
  $SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path ${DEPLOY_DIR}/apk | Out-Null; exit 0\""
  if $SCP "$APK_LOCAL" "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/apk/quant-latest.apk"; then
    echo "  OK APK 已同步 -> ${DEPLOY_DIR}/apk/quant-latest.apk（外网下载入口 /dl/quant-latest.apk）"
  else
    echo "  [!] APK 上传失败——不阻断部署，可稍后单独重跑本步或手工 scp"
  fi
else
  echo "[ ] 本机无 release APK，跳过上传（需先 MOBILE_KEYSTORE_PASS=xxx ./scripts/build_apk.sh release）"
fi

# ── 4. 注册 Windows 服务（NSSM）+ qmtctl 任务计划 ──
if [ $SYNC_ONLY -eq 1 ]; then
  # §A7-C：`-s` 跳过 [4/5] 注册，但那一步原本是**唯一**拉起服务的地方 ——
  # 若不在此显式拉起，`-s`（RUNBOOK §4.1b 推荐命令）每次都会把引擎留在停机态。
  echo "[4/5] 跳过服务注册（-s）；拉起 [2/5] 为释放文件锁而停掉的服务..."
  start_engine_services
  SERVICES_STOPPED=0   # 已拉起，EXIT 兜底无需重复；启动失败由 [5/5] 健康检查暴露
else
  echo "[4/5] 远程注册服务（管理员 PowerShell）..."
  # §UAT-20260917 转义修复：MiniQmtPath 含 "(x86) " 空格，裸传被远端 PowerShell 拆词
  # （实录：报 `x86: 无法将…识别为 cmdlet`，register 整步失败）。所有路径参数统一加 PS 单引号——
  # Windows OpenSSH 默认 shell 为 cmd，单引号原样透传给 powershell -File 解析为整体字面量。
  REMOTE_ARGS="-QuantExe '${DEPLOY_DIR}/quant.exe' -ResearchExe '${DEPLOY_DIR}/researchd.exe' -PydataVenv '${DEPLOY_DIR}/venv' -QmtctlExe '${DEPLOY_DIR}/qmtctl.exe' -MiniQmtPath '${MINIQMT_PATH}' -DataDir '${DATA_DIR}'"
  if [ -n "$LLM_API_KEY" ]; then
    REMOTE_ARGS="$REMOTE_ARGS -LLMApiKey '$LLM_API_KEY' -LLMApiURL '$LLM_API_URL' -LLMModel '$LLM_MODEL'"
  fi
  ps1_bom deploy/qmt-win/register_engine_services.ps1
  $SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/qmt-win/register_engine_services.ps1 $REMOTE_ARGS"
  SERVICES_STOPPED=0   # register 内已 nssm restart/start，EXIT 兜底无需重复
fi

# ── 5. 健康检查（§UAT 20260915 升级：引擎/Caddy 前端/网关三探针）──
echo "[5/5] 健康检查..."
sleep 3
# 引擎（NSSM quant 服务 :8081，Caddy 反代 :8080）：/setup 未初始化时 200、已初始化时 404，
# 两者都证明引擎存活——不再像旧版只探 8080/setup（现网拓扑下恒 404 误报"未就绪"）。
if $SSH "powershell -NoProfile -Command \"try { (Invoke-WebRequest -Uri http://127.0.0.1:8081/setup -UseBasicParsing -TimeoutSec 10).StatusCode } catch { \$_.Exception.Response.StatusCode.value__ }\"" 2>/dev/null | grep -qE "^(200|404)$"; then
  echo "  OK 引擎已响应 (127.0.0.1:8081)"
else
  echo "  X 引擎未就绪，远程查看: Get-Service quant ; 日志在 $DATA_DIR/logs 或 nssm 日志"
fi
# 网关（步 [3b] 已重启）：/health ok 才算本次部署的网关代码生效
if $SSH "powershell -NoProfile -Command \"try { (Invoke-WebRequest -Uri http://127.0.0.1:8789/health -UseBasicParsing -TimeoutSec 5).StatusCode -eq 200 } catch { exit 1 }\"" 2>/dev/null; then
  echo "  OK 网关已响应 (127.0.0.1:8789/health)"
else
  echo "  X 网关未就绪（RESTART_GATEWAY=0 时属预期）；可用 scripts/verify_deploy_guangzhou.sh 复核"
fi
echo "=============================================="
echo " 部署完成（当前 config 原样保留，qmt 开关状态不受部署影响）。"
echo " 验证：./scripts/verify_deploy_guangzhou.sh（buildCommit 指纹/新端点/服务状态全量复核）"
echo "=============================================="
