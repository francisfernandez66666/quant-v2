# register_engine_services.ps1 — 广州单机全合一：注册引擎相关 Windows 服务（NSSM）+ qmtctl 时段启停任务
# 用法（必须管理员 PowerShell，且已跑过 setup_windows.ps1 生成 qmt_gateway/config.xt.json）：
#   powershell -ExecutionPolicy Bypass -File register_engine_services.ps1 `
#       -QuantExe C:\opt\quant\quant.exe `
#       -ResearchExe C:\opt\quant\researchd.exe `
#       -PydataVenv C:\opt\quant\venv `
#       -DataDir C:\var\lib\quant-trading-v2 `
#       -MiniQmtPath "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe" `
#       -LLMApiKey "..." -LLMApiURL "..." -LLMModel "..."
# 注意：MiniQmtPath 必须是完整交易端 XtItClient.exe（能自动登录并交易）；若传 XtMiniQmt.exe
#       （极简 miniQMT，无法自动登录）会导致 broker 永远连不上、实盘全流程被打断。
# 设计（见 docs/MIGRATION_GUANGZHOU_ALLINONE.md §4）：
#   - quant          NSSM 服务, NORMAL 优先级
#   - quant-research NSSM 服务, BELOW_NORMAL 优先级（交易时段被会话门控不跑重型任务）
#   - pydata         NSSM 服务, BELOW_NORMAL 优先级（baostock 国内直连）
#   - qmt-gateway    已由 register_service.ps1 注册（本脚本不改）
#   - qmtctl         任务计划（交互会话，每 10 分钟）管控 QMT 完整交易端启停——勿用 NSSM（需 GUI 登录会话）
param(
    [string]$QuantExe = "C:\opt\quant\quant.exe",
    [string]$ResearchExe = "C:\opt\quant\researchd.exe",
    [string]$PydataVenv = "C:\opt\quant\venv",
    [string]$QmtctlExe = "C:\opt\quant\qmtctl.exe",
    [string]$MiniQmtPath = "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe",
    [string]$DataDir = "C:\var\lib\quant-trading-v2",
    [string]$LLMApiKey = "",
    [string]$LLMApiURL = "https://api.siliconflow.cn/v1/chat/completions",
    [string]$LLMModel = "THUDM/GLM-Z1-9B-0414",
    [string]$NSSMUrl = "https://nssm.cc/release/nssm-2.24.zip"
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[eng] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# ---- 0. 管理员校验 ----
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Die "请以管理员身份运行 PowerShell"
}

# ---- 1. 准备 nssm ----
$tools = Join-Path $PSScriptRoot "tools"
$nssm = Join-Path $tools "nssm-2.24\win64\nssm.exe"
if (-not (Test-Path $nssm)) {
    New-Item $tools -ItemType Directory -Force | Out-Null
    Info "下载 nssm ..."
    try {
        Invoke-WebRequest -Uri $NSSMUrl -OutFile (Join-Path $tools "nssm.zip") -UseBasicParsing -TimeoutSec 60
        Expand-Archive (Join-Path $tools "nssm.zip") $tools -Force
    } catch {
        Die "nssm 下载失败：$($_.Exception.Message)（手动下载解压到 $tools 后重跑）"
    }
}
if (-not (Test-Path $nssm)) { Die "未找到 $nssm" }

# 通用：设置服务基础属性 + 环境变量 + 日志轮转
function Register-NssmService($name, $exe, $argsStr, $priority) {
    Info "注册服务 $name ..."
    & $nssm install $name $exe $argsStr | Out-Null
    & $nssm set $name AppDirectory (Split-Path -Parent $exe) | Out-Null
    & $nssm set $name AppPriority $priority | Out-Null
    & $nssm set $name AppRestartDelay 5000 | Out-Null
    & $nssm set $name AppExit Default Restart | Out-Null
    & $nssm set $name AppRotateFiles 1 | Out-Null
    & $nssm set $name AppRotateOnline 1 | Out-Null
    & $nssm set $name AppRotateBytes 10485760 | Out-Null
    & $nssm set $name Start SERVICE_AUTO_START | Out-Null
    # 环境变量（替代 /etc/quant.env）
    & $nssm set $name AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" | Out-Null
    if ($LLMApiKey) {
        & $nssm set $name AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" "LLM_API_KEY=$LLMApiKey" "LLM_API_URL=$LLMApiURL" "LLM_MODEL=$LLMModel" | Out-Null
    }
}

# ---- 2. quant ----
if (-not (Test-Path $QuantExe)) { Die "缺少 $QuantExe" }
Register-NssmService "quant" $QuantExe "" "NORMAL_PRIORITY_CLASS"
& $nssm set quant AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" "QUANT_ADDR=127.0.0.1:8080" | Out-Null
if ($LLMApiKey) {
    & $nssm set quant AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" "QUANT_ADDR=127.0.0.1:8080" "LLM_API_KEY=$LLMApiKey" "LLM_API_URL=$LLMApiURL" "LLM_MODEL=$LLMModel" | Out-Null
}
& $nssm restart quant
Start-Sleep -Seconds 2
Ok "quant 服务已注册/重启"

# ---- 3. quant-research（BELOW_NORMAL）----
if (-not (Test-Path $ResearchExe)) { Die "缺少 $ResearchExe" }
Register-NssmService "quant-research" $ResearchExe "" "BELOW_NORMAL_PRIORITY_CLASS"
& $nssm restart quant-research
Start-Sleep -Seconds 2
Ok "quant-research 服务已注册/重启"

# ---- 4. pydata（baostock sidecar，python venv）----
$pyExe = Join-Path $PydataVenv "python.exe"
$pyScript = Join-Path $DataDir ".." "qmt_gateway"  # 占位，实际路径由部署脚本确保
# pydata server 实际位于仓库 cmd/pydata/server.py，部署时放到 C:\opt\quant\pydata\server.py
$pyScript = "C:\opt\quant\pydata\server.py"
if (-not (Test-Path $pyScript)) { Warn "未找到 $pyScript —— 跳过 pydata 注册（可后续补全）" }
else {
    Register-NssmService "pydata" $pyExe "-m pydata.server --host 127.0.0.1 --port 8788" "BELOW_NORMAL_PRIORITY_CLASS"
    # pydata 用模块方式运行，AppDirectory 指向仓库内 cmd/pydata
    & $nssm set pydata AppDirectory "C:\opt\quant" | Out-Null
    & $nssm restart pydata
    Start-Sleep -Seconds 2
    Ok "pydata 服务已注册/重启"
}

# ---- 5. qmtctl 时段启停（任务计划，交互会话）----
if (-not (Test-Path $QmtctlExe)) { Warn "未找到 $QmtctlExe —— 跳过 qmtctl 任务（可后续补全）" }
else {
    $taskName = "QMT-Ensure-Running"
    # §FIX 2026-08-31：无窗口包装——先落 wrapper ps1（UTF8-BOM：MiniQmtPath 常含中文目录，
    # ASCII 会写成 '?' 导致启动失败），再由 wscript(GUI 子系统、无控制台)经 VBS 隐藏运行，
    # 根除交互任务每 10 分钟的黑框闪烁（"监控闪退"观感）。VBS 内容保持 ASCII。
    $wrapper = Join-Path $PSScriptRoot "ensure_miniqmt.ps1"
    $wrapContent = "& '$QmtctlExe' ensure-miniqmt -path '$MiniQmtPath' -gateway-url http://127.0.0.1:8789/health"
    Set-Content -Path $wrapper -Value $wrapContent -Encoding UTF8
    $vbs = Join-Path $PSScriptRoot "run_qmt_ensure.vbs"
    [IO.File]::WriteAllText($vbs, "CreateObject(""WScript.Shell"").Run ""powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper"", 0, True", (New-Object Text.ASCIIEncoding))
    $action = "wscript.exe //B $vbs"
    # 注意：不指定 /RU SYSTEM —— 默认在当前登录用户的交互会话运行（MiniQMT 需 GUI 登录会话）。
    # 配合 netplwiz 自动登录，开机后用户会话存在即周期执行。
    schtasks /Create /F /SC MINUTE /MO 10 /TN $taskName /TR $action
    if ($LASTEXITCODE -eq 0) { Ok "任务计划 $taskName 已创建（每 10 分钟，交互会话）" }
    else { Warn "schtasks 创建失败（exit=$LASTEXITCODE）；可手动在任务计划程序建（务必用当前用户、不选 SYSTEM）" }
}

Ok "引擎服务注册完成。验证：Get-Service quant,quant-research,pydata ；schtasks /Query /TN QMT-Ensure-Running"
