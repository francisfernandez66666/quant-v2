# register_web_service.ps1 — §M7b（2026-09-22 修复批）：quant-web（Caddy）站点注册收编进部署清单。
# 背景：verify_deploy_guangzhou.sh 一直要求 quant / quant-research / pydata / quant-web 四服务全部
# Running，但 deploy_guangzhou.sh 从不注册/刷新 web 站点——Caddy 服务与 Caddyfile 是当年的手工
# 一次性操作（步骤散落在 deploy/GUANGZHOU_CADDY.md），部署面与校验面各说各话（§M7 审计实录）。
# 本脚本做幂等收编，可反复执行：
#   1) caddy.exe 缺失 → 自动下载官方 Windows amd64 构建（现场已预置则跳过；-NoDownload 可禁）；
#   2) Caddyfile 更新：先 caddy validate 校验新配置，与现网内容比对，不同才备份旧文件为 .bak 并替换
#      （validate 不过则拒绝替换、保留现网配置——绝不把坏配置推给在线站点）；
#   3) Windows 服务（NSSM）quant-web：不存在则 install，存在则纠正 AppParameters/自启后 restart；
#   4) 健康确认：轮询 $ProbeCaddyPort（§H8 service_probe_config.ps1 单源，缺失回退 8080）GET / 200。
# 端口口径：本站点注册不新造任何端口——:8080 应急静态口/:8081 引擎反代目标均取自仓内
# deploy/caddy/guangzhou.conf，与 §H8 探针单源一致性由 scripts/check_deploy_static_locks.sh 静态锁把守。
# 编码：上传前由 deploy_guangzhou.sh 的 ps1_bom 统一补单个 BOM（PS 5.1 无 BOM 按 GBK 读中文会撕裂字面量）。
# English: idempotent quant-web (Caddy) registration — previously a manual-only step missing from
# the deploy manifest while verify_deploy required it Running.
param(
    [string]$DeployDir = "C:\opt\quant",
    [string]$CaddyConfSrc = "",      # 已上传的 deploy/caddy/guangzhou.conf（留空则跳过 Caddyfile 更新）
    [string]$ProbeConfigPath = "",   # §H8 探针单源路径（留空按常见位置探测）
    [string]$CaddyDownloadUrl = "https://caddyserver.com/api/download?os=windows&arch=amd64",
    [string]$NSSMUrl = "https://nssm.cc/release/nssm-2.24.zip",
    [switch]$NoDownload              # caddy.exe 缺失时不自动下载（离线/镜像环境：直接失败提示人工放置）
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[web] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# ---- 0. 管理员校验（服务 install/restart 必需）----
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Die "需要管理员 PowerShell（NSSM 服务注册）"
}

# ---- 1. §H8 探针单源：quant-web 的验证端口必须与运维探针同源，不许本脚本另立字面量 ----
$webPort = 8080   # 回退值 = service_probe_config.ps1 现值（静态锁脚本会比对两者漂移）
$cands = @()
if ($ProbeConfigPath) { $cands += $ProbeConfigPath }
$cands += (Join-Path $PSScriptRoot "service_probe_config.ps1")
$cands += (Join-Path $DeployDir "qmt-win\service_probe_config.ps1")
$probeCfg = $cands | Where-Object { Test-Path $_ } | Select-Object -First 1
if ($probeCfg) {
    . $probeCfg
    $webPort = $ProbeCaddyPort
    Info "probe config loaded: $probeCfg (§H8 端口同源, web=:$webPort)"
} else {
    Warn "missing service_probe_config.ps1 —— 回退字面量 :$webPort（§H8：运维探针将与本脚本脱钩，请补齐部署清单）"
}

# ---- 2. 路径准备 ----
$exe      = Join-Path $DeployDir "caddy.exe"
$conf     = Join-Path $DeployDir "Caddyfile"
$logDir   = "C:\var\log\caddy"          # guangzhou.conf 内 log 指令落盘目录，不存在则 Caddy 起不来
$nssm     = Join-Path $DeployDir "qmt-win\tools\nssm-2.24\win64\nssm.exe"
New-Item $logDir -ItemType Directory -Force | Out-Null

# ---- 3. caddy.exe：缺失则下载官方 zip 解出 ----
if (-not (Test-Path $exe)) {
    if ($NoDownload) { Die "$exe 不存在且 -NoDownload——请人工放置 Caddy 二进制" }
    Info "caddy.exe 缺失，开始下载官方 Windows 构建 ..."
    $tmpZip  = Join-Path $env:TEMP "caddy_dl.zip"
    $tmpDir  = Join-Path $env:TEMP "caddy_dl"
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $CaddyDownloadUrl -OutFile $tmpZip -UseBasicParsing -TimeoutSec 180
        if (Test-Path $tmpDir) { Remove-Item $tmpDir -Recurse -Force }
        Expand-Archive $tmpZip $tmpDir -Force
        $got = Get-ChildItem $tmpDir -Recurse -Filter "caddy.exe" | Select-Object -First 1
        if (-not $got) { Die "zip 内未找到 caddy.exe" }
        Copy-Item $got.FullName $exe -Force
        Ok "caddy.exe 就位: $exe"
    } catch {
        Die "caddy 下载失败: $($_.Exception.Message)（可用 -NoDownload + 人工预置 $exe）"
    } finally {
        Remove-Item $tmpZip -Force -ErrorAction SilentlyContinue
        Remove-Item $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# ---- 4. Caddyfile：validate 通过且内容有变化才替换（幂等 + 变更留底 .bak）----
$confChanged = $false
if ($CaddyConfSrc -and (Test-Path $CaddyConfSrc)) {
    & $exe validate --config $CaddyConfSrc 2>&1 | ForEach-Object { Write-Host "  $_" }
    if ($LASTEXITCODE -ne 0) { Die "guangzhou.conf validate 失败——保留现网 Caddyfile，不替换（先修配置再重跑）" }
    $newHash = (Get-FileHash $CaddyConfSrc -Algorithm SHA256).Hash
    $oldHash = if (Test-Path $conf) { (Get-FileHash $conf -Algorithm SHA256).Hash } else { "" }
    if ($newHash -ne $oldHash) {
        if (Test-Path $conf) {
            Copy-Item $conf "$conf.bak" -Force
            Info "旧 Caddyfile 已备份为 $conf.bak（回滚：copy $conf.bak $conf 后重启 quant-web）"
        }
        Copy-Item $CaddyConfSrc $conf -Force
        $confChanged = $true
        Ok "Caddyfile 已更新（SHA 不同）"
    } else {
        Ok "Caddyfile 与现网一致，跳过替换"
    }
    Remove-Item $CaddyConfSrc -Force -ErrorAction SilentlyContinue   # 部署暂存文件不留在盘上
} else {
    if (-not (Test-Path $conf)) { Die "既无 $conf 也无 -CaddyConfSrc——无法继续" }
    Warn "未提供 -CaddyConfSrc，跳过 Caddyfile 更新（沿用现网 $conf）"
}

# ---- 5. NSSM 服务 quant-web：install 或纠正参数后 restart（幂等）----
# nssm 自举：register_engine_services.ps1 的 tools 下载发生在 [4/5]，本步（[2d]）在其之前，
# 首次部署必须自带兜底，否则全新机器 quant-web 永远注册不上（§M7b 收编的正是"没人管 web"）。
if (-not (Test-Path $nssm)) {
    if ($NoDownload) { Die "nssm 未就位: $nssm 且 -NoDownload——请人工放置后重跑" }
    $tools = Join-Path $DeployDir "qmt-win\tools"
    New-Item $tools -ItemType Directory -Force | Out-Null
    Info "nssm 缺失，开始下载（与 register_engine_services.ps1 同 URL/同落盘路径）..."
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $NSSMUrl -OutFile (Join-Path $tools "nssm.zip") -UseBasicParsing -TimeoutSec 120
        Expand-Archive (Join-Path $tools "nssm.zip") $tools -Force
    } catch {
        Die "nssm 下载失败: $($_.Exception.Message)"
    }
    if (-not (Test-Path $nssm)) { Die "nssm 下载后仍未就位: $nssm" }
    Ok "nssm 就位: $nssm"
}
$svc = Get-Service "quant-web" -ErrorAction SilentlyContinue
$appArgs = "run --config `"$conf`""
if ($null -eq $svc) {
    Info "install service quant-web (NSSM) ..."
    & $nssm install quant-web $exe $appArgs | Out-Null
    if ($LASTEXITCODE -ne 0) { Die "nssm install quant-web 失败（exit=$LASTEXITCODE）" }
    & $nssm set quant-web AppDirectory $DeployDir | Out-Null
    & $nssm set quant-web AppRestartDelay 5000 | Out-Null
    & $nssm set quant-web AppExit Default Restart | Out-Null
    & $nssm set quant-web AppRotateFiles 1 | Out-Null
    & $nssm set quant-web AppRotateBytes 10485760 | Out-Null
    & $nssm set quant-web Start SERVICE_AUTO_START | Out-Null
    & $nssm start quant-web
    Ok "quant-web 已注册并启动"
} else {
    # 已存在：AppParameters 整体覆盖为规范值（run --config <DeployDir>\Caddyfile），保证幂等收敛
    & $nssm set quant-web AppParameters $appArgs | Out-Null
    & $nssm set quant-web Start SERVICE_AUTO_START | Out-Null
    if ($confChanged -or $svc.Status -ne "Running") {
        & $nssm restart quant-web | Out-Null
        Ok "quant-web 已重启（配置变化或服务未运行）"
    } else {
        Ok "quant-web 已在运行且配置未变——不重启（避免无谓闪断）"
    }
}

# ---- 6. 健康确认（§H8 同源端口）----
$url = "http://127.0.0.1:$webPort/"
$alive = $false
for ($i = 1; $i -le 15; $i++) {
    Start-Sleep -Seconds 2
    try {
        $r = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 5
        if ($r.StatusCode -eq 200) { $alive = $true; break }
    } catch { }
}
if ($alive) {
    Ok "quant-web 健康确认通过: $url -> 200"
} else {
    Warn "quant-web 启动后 30s 内 $url 未回 200——服务状态请查 Get-Service quant-web / C:\var\log\caddy"
    exit 2
}
