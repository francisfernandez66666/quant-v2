# ensure_gateway_config.ps1 — §M7c（2026-09-22 修复批）：config.xt.json 收编进部署清单。
# 背景：gateway_watchdog.ps1:31 以 `-c <gw>\config.xt.json` 拉起网关，但该文件过去只有
# setup_windows.ps1 这一条**手工**路径生成（且要求交互输入资金账号）——全新机器按
# deploy_guangzhou.sh 部署后文件不存在，网关秒起秒死、watchdog 3 秒一轮刷错误日志
# （§M7 审计：「config.xt.json 从不下发」）。现由部署脚本随 [2b] 步执行本脚本：
#   * 已存在 → 一律保留不覆盖（token/账号是运维资产，绝不重发/重置）；
#   * 不存在 → 按 config.xt.template.json 幂等生成，listen 端口与 §H8
#     service_probe_config.ps1 的 $ProbeGatewayPort 同源（根除「网关听 8789、探针探别的口」）；
#   * token 由 -Token 注入或现场生成 48 位 hex 并打印——需同步到引擎侧 rules.qmt.token
#     （三处一致：token / report_token / 引擎账号配置，同 setup_windows.ps1 口径）；
#   * 影子期（未给 -Account）broker 收敛为 mock：网关可存活应答 /health，但不接真实柜台，
#     真开实盘时运维手工改 broker=xt + 填资金账号（或重跑 setup_windows.ps1）。
# 编码要点：config.xt.json 必须以 **无 BOM UTF-8** 落盘——网关 json.load 读到 BOM 直接抛错
# （setup_windows.ps1 同款教训）；本脚本自身由部署侧 ps1_bom 补 BOM，两件事互不影响。
# §C7-OPS（2026-09-26，FIX_PLAN_20260925EVE ⑯）已收编进 service_definitions.ps1 的定义：
#   - $GatewayDir 缺省（现网网关目录 $SvcGatewayDir，缺失回退旧字面量）；
#   - listen 绑定面：旧版写 "0.0.0.0:<port>" 与现网形态矛盾——gateway.py:1877-1888 在无
#     QUANT_GATEWAY_BIND 时会把 0.0.0.0 自动收敛为 127.0.0.1、install_guangzhou.ps1:131-132
#     已把防火墙 8789 收严 remoteip=127.0.0.1、MIGRATION_GUANGZHOU_ALLINONE §3.5/R2 裁决
#     移除首尔白名单 ⇒ 现网形态＝仅回环。本脚本改为**如实写回环值**（$SvcGatewayBindLoopback），
#     不再依赖网关启动时的静默收敛；确需公网监听（历史灾备）显式传 -PublicBind。
# English: deployment-side idempotent generator for qmt_gateway/config.xt.json (previously only
# created by the manual setup_windows.ps1 step, so a scripted deploy left the watchdog pointing at
# a nonexistent config). Existing files are never overwritten.
param(
    [string]$GatewayDir = "",      # 留空取 §C7 单源 $SvcGatewayDir（缺失回退 C:\qmt\quant-trading-v2\qmt_gateway）
    [string]$TemplatePath = "",    # 留空取 <GatewayDir>\config.xt.template.json
    [string]$Token = "",           # 网关 Bearer token（留空自动生成 48 位 hex 并打印）
    [string]$Account = "",         # 东莞证券资金账号（留空=broker 收敛 mock，影子期不死等柜台）
    [string]$XtPath = "",          # QMT userdata_mini 目录（broker=xt 必需；mock 期可不填）
    [string]$ProbeConfigPath = "", # §H8 探针单源路径（留空按常见位置探测）
    [switch]$PublicBind            # §C7-OPS：显式要求公网监听（旧分离形态灾备；缺省=仅回环）
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[gwcfg] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# §C7-OPS：路径/绑定面单源（缺失回退旧字面量并告警，不阻断——[2b] 部署步要能自救）。
$svcDefs = $null
foreach ($cand in @((Join-Path $PSScriptRoot "service_definitions.ps1"),
                    "C:\opt\quant\qmt-win\service_definitions.ps1")) {
    if (Test-Path $cand) { . $cand; $svcDefs = $cand; break }
}
if (-not $svcDefs) {
    Warn "service_definitions.ps1 未找到 —— 网关目录/绑定面回退本脚本字面量（§C7 单径脱钩，请补齐部署清单）"
    $SvcGatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway"
}
if (-not $GatewayDir) { $GatewayDir = $SvcGatewayDir }

$cfgPath = Join-Path $GatewayDir "config.xt.json"
if (Test-Path $cfgPath) {
    Ok "保留现有 config.xt.json（未覆盖，token/账号不变）: $cfgPath"
    exit 0
}

# ---- 1. 模板 ----
if (-not $TemplatePath) { $TemplatePath = Join-Path $GatewayDir "config.xt.template.json" }
if (-not (Test-Path $TemplatePath)) {
    Die "模板缺失: $TemplatePath —— deploy_guangzhou.sh [2b] 应随网关 .py 一起 scp config.xt.template.json（§M7c 部署清单）"
}

# ---- 2. §H8 端口同源：listen 口的 8789 不再各写各的 ----
$gwPort = 8789   # 回退值 = service_probe_config.ps1 现值（静态锁脚本比对漂移）
$cands = @()
if ($ProbeConfigPath) { $cands += $ProbeConfigPath }
$cands += (Join-Path $PSScriptRoot "service_probe_config.ps1")
$cands += "C:\opt\quant\qmt-win\service_probe_config.ps1"
$probeCfg = $cands | Where-Object { Test-Path $_ } | Select-Object -First 1
if ($probeCfg) {
    . $probeCfg
    $gwPort = $ProbeGatewayPort
    Info "probe config loaded: $probeCfg (§H8 端口同源, gateway=:$gwPort)"
} else {
    Warn "missing service_probe_config.ps1 —— listen 回退字面量 :$gwPort（§H8 口径脱钩，请补齐部署清单）"
}

# ---- 3. token：显式注入优先；否则现场生成强随机（与 setup_windows.ps1 同法 24B hex）----
$tokenGenerated = $false
if (-not $Token) {
    $rng = New-Object System.Security.Cryptography.RNGCryptoServiceProvider
    $buf = New-Object byte[] 24
    $rng.GetBytes($buf)
    $Token = (($buf | ForEach-Object { $_.ToString("x2") }) -join "")
    $tokenGenerated = $true
}

# ---- 4. 渲染模板（ConvertFrom-Json 保字段序语义，替换占位符 + 对齐 §H8 端口）----
try {
    $cfg = Get-Content $TemplatePath -Raw | ConvertFrom-Json
} catch {
    Die "模板 JSON 解析失败: $($_.Exception.Message)"
}
if ($cfg -is [array]) { Die "模板顶层必须是 JSON 对象" }
$cfg.token          = $Token
$cfg.report_token   = $Token          # 网关↔引擎回报鉴权同 token（三处一致口径）
$cfg.account        = $Account
$cfg.xt_path        = $XtPath
# §C7-OPS：绑定面写现网真实形态（仅回环；依据见文件头注）。-PublicBind 是显式的历史逃生门。
if ($PublicBind) {
    Warn "按 -PublicBind 写入公网监听 0.0.0.0:$gwPort —— 与 §C7 单径裁决相反，仅灾备使用，记得同步防火墙白名单"
    $cfg.listen = "0.0.0.0:$gwPort"
} else {
    $cfg.listen = "127.0.0.1:$gwPort"
}
if (-not $Account) {
    if ($cfg.broker -ne "mock") {
        Warn "未提供 -Account：broker 由 `"$($cfg.broker)`" 收敛为 mock（影子期网关可存活应答 /health，不接真实柜台）"
        $cfg.broker = "mock"
    }
} else {
    $cfg.broker = "xt"
}

$json = $cfg | ConvertTo-Json -Depth 4
# 无 BOM UTF-8 落盘（网关 json.load 不容 BOM）
[System.IO.File]::WriteAllText($cfgPath, $json, (New-Object System.Text.UTF8Encoding($false)))
Ok "已生成 $cfgPath (broker=$($cfg.broker), listen=$($cfg.listen))"

Write-Host ""
Info "token 需与引擎侧 rules.qmt.token 一致（三处一致：token / report_token / 引擎账号配置）："
if ($tokenGenerated) {
    Write-Host "    $Token" -ForegroundColor Magenta
    Warn "该 token 为本次现场随机生成——请立即抄存，并同步到引擎设置页的 QMT 配置"
} else {
    Ok "已使用 -Token 注入值（未回显明文）"
}
