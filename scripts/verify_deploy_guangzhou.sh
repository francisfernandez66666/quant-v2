#!/bin/bash
# verify_deploy_guangzhou.sh — 广州部署后自动化验证（§UAT 20260915 部署加固配套）。
#
# 定位：deploy_guangzhou.sh 只做"传上去+活着"，本脚本做"传对了吗+新代码生效了吗"的全量复核，
# 任一探针失败即退出码非 0（可挂 CI / 部署后 cron / 手工复核）。
#
# 设计（2026-09-15 实录教训）：探针逻辑全部写进 verify_probes.ps1 由 scp 上传执行——
# bash→SSH→PowerShell 三层引号转义链在 `$_`/CR 尾/编码 三处都不可靠（首版全部探针误报失败）；
# bash 层只负责编排与结果计数，PS 层输出 PASS/FAIL 行。
#
# 覆盖探针（对应 2026-09-15 部署实录的手工验证清单）：
#   1) 服务状态：quant / quant-research / pydata / quant-web 四个 NSSM 服务全部 Running
#   2) 二进制指纹：quant.exe 内含期望的 git 短 SHA（buildCommit 漂移自检）
#   3) 引擎：/setup → 200/404（存在即存活）；/api/status 未鉴权 → 401（鉴权面完好）
#   4) 前端：GET :8080/ → 200（Caddy 静态资源）
#   4b) 前端指纹（§A7-B，2026-09-20 补）：被服务的 index-*.js 内必须含期望 SHA
#       —— 旧版只查首页 200，产物陈旧（先 build 后 commit）时 12/12 全绿却仍报版本不一致横幅
#   5) 网关：/health ok:true；/settlement 未鉴权 → 401（§P0-1a 新端点路由已上线）
#   6) 引擎新端点：POST /api/holdings/balance、/api/positions/execute 未鉴权 → 401
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/verify_deploy_guangzhou.sh
#   GZ_IP=81.71.69.17 COMMIT=beb5b80 ./scripts/verify_deploy_guangzhou.sh   # 显式指定指纹
#
# 参数（环境变量）：GZ_IP（必填）/ GZ_USER / COMMIT（默认本地 HEAD）/ DEPLOY_DIR / 端口三项
set -uo pipefail

: "${GZ_IP:?请设置 GZ_IP（广州服务器公网 IP）}"
GZ_USER="${GZ_USER:-Administrator}"
APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMMIT="${COMMIT:-$(git -C "$APP_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
ENGINE_PORT="${ENGINE_PORT:-8081}"
WEB_PORT="${WEB_PORT:-8080}"
GW_PORT="${GW_PORT:-8789}"

SSH="ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 ${GZ_USER}@${GZ_IP}"
SCP="scp -o StrictHostKeyChecking=accept-new"

# ── 生成探针脚本（quoted heredoc：PS 的 $ 原样落盘，不经 bash 展开）──
# BSD mktemp（macOS）只认 X 在结尾的模板（2026-09-18 实录：`vd_probes_XXXX.ps1`
# 报 "File exists" 后靠 shell 容错侥幸跑通）——X 移尾、.ps1 前缀名自拼。
PROBES="$(mktemp /tmp/vd_probes_XXXXXX).ps1" || { echo "mktemp 失败"; exit 1; }
cat > "$PROBES" <<'PSEOF'
# verify_probes.ps1 — 部署后探针（由 verify_deploy_guangzhou.sh 现场生成上传执行）。
# 输出协议：每行 "PASS|探针名" 或 "FAIL|探针名|附加信息"；bash 层汇总计数定退出码。
# English: deployment probes — emit PASS/FAIL lines; the bash driver counts them.
param(
    [string]$Commit = "unknown",
    [int]$EnginePort = 8081,
    [int]$WebPort = 8080,
    [int]$GwPort = 8789
)
$ErrorActionPreference = "Continue"

function Probe($name, $ok, $detail) {
    if ($ok) { Write-Output ("PASS|" + $name) } else { Write-Output ("FAIL|" + $name + "|" + $detail) }
}
# status 探针：返回 HTTP 状态码字符串；任何异常归一为 "ERR"（不打印异常栈）
function HCode($method, $url, $body) {
    try {
        if ($method -eq "POST") {
            return [string](Invoke-WebRequest -Uri $url -Method POST -ContentType "application/json" -Body $body -UseBasicParsing -TimeoutSec 10).StatusCode
        }
        return [string](Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 10).StatusCode
    } catch {
        $r = $_.Exception.Response
        if ($null -ne $r) { return [string][int]$r.StatusCode }
        return "ERR"
    }
}

# 1) NSSM 服务状态
foreach ($s in @("quant", "quant-research", "pydata", "quant-web")) {
    $st = (Get-Service $s -ErrorAction SilentlyContinue).Status
    Probe ("svc:" + $s) ($st -eq "Running") ("got=" + $st)
}

# 2) 二进制指纹（buildCommit 防漂移）
$hit = Select-String -Path "C:\opt\quant\quant.exe" -Pattern $Commit -Quiet -ErrorAction SilentlyContinue
Probe ("fingerprint:" + $Commit) ($hit -eq $true) "quant.exe 内不含期望短 SHA"

# 3) 引擎：/setup（未初始化 200 / 已初始化 404，两者皆存活）+ 鉴权面
$code = HCode "GET" ("http://127.0.0.1:" + $EnginePort + "/setup")
Probe "engine:/setup" ($code -eq "200" -or $code -eq "404") ("got=" + $code)
$code = HCode "GET" ("http://127.0.0.1:" + $EnginePort + "/api/status")
Probe "engine:/api/status unauth=401" ($code -eq "401") ("got=" + $code)

# 4) 前端（Caddy 静态）
$code = HCode "GET" ("http://127.0.0.1:" + $WebPort + "/")
Probe "web:/" ($code -eq "200") ("got=" + $code)

# 4b) §A7-B 前端指纹（2026-09-20 补）：**线上实际服务的那个 bundle 必须内嵌待部署 SHA**。
#
# 为什么单列一条探针（旧版只查首页 200，12/12 全绿也照样漏掉版本漂移横幅）：
# 用户端的「前端与服务器版本不一致」判定是 App.jsx 用 bundle 内嵌的 __BUILD_COMMIT__
# 与 /api/status 的 build_commit **严格不等即告警**。所以"前端传上去了"不等于"传的是这一版"——
# 实录：本地先 npm run build、之后才 commit，dist 内嵌指纹停在上一版，后端换新 SHA 上线，
# 横幅立刻出现，而旧探针全绿（2026-09-20 两次）。
# 这里直接在被服务的 assets 里找期望 SHA：找不到即真实漂移，与用户看到的横幅一一对应。
$webRoot = "C:\opt\quant\web"
$idxHtml = Join-Path $webRoot "index.html"
$hit = $false
$detail = ""
if (Test-Path $idxHtml) {
    $m = [regex]::Match((Get-Content $idxHtml -Raw), 'assets/index-[A-Za-z0-9_\-]+\.js')
    if ($m.Success) {
        $chunk = Join-Path $webRoot $m.Value
        if (Test-Path $chunk) {
            $hit = (Select-String -Path $chunk -Pattern $Commit -Quiet -ErrorAction SilentlyContinue) -eq $true
            $detail = "index.html->" + $m.Value + " 内" + $(if ($hit) { "含" } else { "不含" }) + " " + $Commit
        } else {
            $detail = "index.html 引用的 " + $m.Value + " 在站点根不存在"
        }
    } else {
        $detail = "index.html 里找不到 assets/index-*.js 引用"
    }
} else {
    $detail = ($webRoot + "\index.html 不存在")
}
Probe ("web:fingerprint:" + $Commit) $hit $detail

# 5) 网关：/health + §P0-1a /settlement 新端点路由
try {
    $h = (Invoke-WebRequest -Uri ("http://127.0.0.1:" + $GwPort + "/health") -UseBasicParsing -TimeoutSec 10).Content
    Probe "gw:/health" ($h -match '"ok":\s*true') ("got=" + $h.Substring(0, [Math]::Min(80, $h.Length)))
} catch { Probe "gw:/health" $false $_.Exception.Message }
$code = HCode "GET" ("http://127.0.0.1:" + $GwPort + "/settlement?date=2026-01-01")
Probe "gw:/settlement unauth=401" ($code -eq "401") ("got=" + $code + "；404=网关仍是旧代码")

# 6) 引擎新端点路由（路由存在且受保护，未鉴权一律 401；404=二进制未更新）
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/holdings/balance") '{"available_balance":1}'
Probe "engine:/api/holdings/balance unauth=401" ($code -eq "401") ("got=" + $code)
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/positions/execute") '{}'
Probe "engine:/api/positions/execute unauth=401" ($code -eq "401") ("got=" + $code)
PSEOF

# PS 5.1 无 BOM 的 UTF-8 文件按 GBK 解析——中文注释会撕裂字符串字面量直接 ParserError，
# 所以上传前补 UTF-8 BOM（deploy/qmt-win/ 各 PS1 同款约定）。
printf '\357\273\277' | cat - "$PROBES" > "$PROBES.bom" && mv "$PROBES.bom" "$PROBES"
$SCP "$PROBES" "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/verify_probes.ps1" 2>/dev/null

echo "== verify_deploy_guangzhou @ ${GZ_IP}（期望 buildCommit=${COMMIT}）=="
out=$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/verify_probes.ps1 -Commit ${COMMIT} -EnginePort ${ENGINE_PORT} -WebPort ${WEB_PORT} -GwPort ${GW_PORT}" 2>&1 | LC_ALL=C tr -d '\r')

PASS=0
FAIL=0
while IFS= read -r line; do
  case "$line" in
    PASS\|*) PASS=$((PASS+1)); echo "  ✓ ${line#PASS|}" ;;
    FAIL\|*) FAIL=$((FAIL+1)); echo "  ✗ ${line#FAIL|}" ;;
  esac
done <<< "$out"

rm -f "$PROBES"
echo "== 结果：${PASS} 通过 / ${FAIL} 失败 =="
[ "$FAIL" -eq 0 ] || exit 1
echo "全部探针通过。"
