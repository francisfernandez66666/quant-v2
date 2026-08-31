# register_engine_services.ps1 - Guangzhou all-in-one: register engine Windows services (NSSM) + qmtctl task scheduler.
# Usage (admin PowerShell):
#   powershell -ExecutionPolicy Bypass -File register_engine_services.ps1 `
#       -QuantExe C:\opt\quant\quant.exe -ResearchExe C:\opt\quant\researchd.exe `
#       -PydataVenv C:\opt\quant\venv -QmtctlExe C:\opt\quant\qmtctl.exe `
#       -MiniQmtPath "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe" `
#       -DataDir C:\var\lib\quant-trading-v2 -LLMApiKey "..." -LLMApiURL "..." -LLMModel "..."
# NOTE: MiniQmtPath MUST be the full client XtItClient.exe (auto-login + trading). XtMiniQmt.exe
#       cannot auto-login → broker never connects.
# Design (docs/MIGRATION_GUANGZHOU_ALLINONE.md section 4):
#   quant          NSSM service, NORMAL priority
#   quant-research NSSM service, BELOW_NORMAL priority (session-gated off-hours)
#   pydata         NSSM service, BELOW_NORMAL priority (baostock sidecar, port 8787)
#   qmt-gateway    MUST run in the interactive session (see register_service.ps1 header):
#                  xtquant talks to the QMT client via per-session shared-memory queues,
#                  a Session-0/NSSM instance can never complete the heartbeat handshake.
#   qmtctl         scheduled task (interactive session, every 10 min) - must NOT use NSSM (needs GUI login)
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

# 0. admin check
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Die "run as Administrator"
}

# 1. prepare nssm
$tools = Join-Path $PSScriptRoot "tools"
$nssm = Join-Path $tools "nssm-2.24\win64\nssm.exe"
if (-not (Test-Path $nssm)) {
    New-Item $tools -ItemType Directory -Force | Out-Null
    Info "downloading nssm ..."
    try {
        Invoke-WebRequest -Uri $NSSMUrl -OutFile (Join-Path $tools "nssm.zip") -UseBasicParsing -TimeoutSec 60
        Expand-Archive (Join-Path $tools "nssm.zip") $tools -Force
    } catch {
        Die "nssm download failed: $($_.Exception.Message)"
    }
}
if (-not (Test-Path $nssm)) { Die "nssm not found at $nssm" }

# helper: register a service with base props + env + log rotation
function Register-NssmService($name, $exe, $appArgs, $priority) {
    Info "registering service $name ..."
    & $nssm install $name $exe @appArgs | Out-Null
    & $nssm set $name AppDirectory (Split-Path -Parent $exe) | Out-Null
    & $nssm set $name AppPriority $priority | Out-Null
    & $nssm set $name AppRestartDelay 5000 | Out-Null
    & $nssm set $name AppExit Default Restart | Out-Null
    & $nssm set $name AppRotateFiles 1 | Out-Null
    & $nssm set $name AppRotateOnline 1 | Out-Null
    & $nssm set $name AppRotateBytes 10485760 | Out-Null
    & $nssm set $name Start SERVICE_AUTO_START | Out-Null
    if ($LLMApiKey) {
        & $nssm set $name AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" "LLM_API_KEY=$LLMApiKey" "LLM_API_URL=$LLMApiURL" "LLM_MODEL=$LLMModel" | Out-Null
    } else {
        & $nssm set $name AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" | Out-Null
    }
}

# 2. quant (NORMAL)
if (-not (Test-Path $QuantExe)) { Die "missing $QuantExe" }
Register-NssmService "quant" $QuantExe @() "NORMAL_PRIORITY_CLASS"
& $nssm set quant AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" "QUANT_ADDR=0.0.0.0:8080" | Out-Null
if ($LLMApiKey) {
    & $nssm set quant AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=$DataDir" "QUANT_ADDR=0.0.0.0:8080" "LLM_API_KEY=$LLMApiKey" "LLM_API_URL=$LLMApiURL" "LLM_MODEL=$LLMModel" | Out-Null
}
& $nssm restart quant
Start-Sleep -Seconds 2
Ok "quant registered/restarted"

# 3. quant-research (BELOW_NORMAL)
if (-not (Test-Path $ResearchExe)) { Die "missing $ResearchExe" }
Register-NssmService "quant-research" $ResearchExe @() "BELOW_NORMAL_PRIORITY_CLASS"
& $nssm restart quant-research
Start-Sleep -Seconds 2
Ok "quant-research registered/restarted"

# 4. pydata (baostock sidecar, port 8787)
$pyExe = Join-Path $PydataVenv "Scripts\python.exe"
$pyScript = "C:\opt\quant\pydata\server.py"
if (-not (Test-Path $pyScript)) {
    Warn "missing $pyScript - skip pydata"
} elseif (-not (Test-Path $pyExe)) {
    Warn "missing venv python $pyExe - skip pydata (run setup_venv first)"
} else {
    Register-NssmService "pydata" $pyExe @("$pyScript", "--host", "127.0.0.1", "--port", "8787") "BELOW_NORMAL_PRIORITY_CLASS"
    & $nssm set pydata AppDirectory "C:\opt\quant\pydata" | Out-Null
    & $nssm restart pydata
    Start-Sleep -Seconds 2
    Ok "pydata registered/restarted (127.0.0.1:8787)"
}

# 5. qmtctl scheduled task (interactive session) - generate a wrapper ps1 to avoid nested quoting
if (-not (Test-Path $QmtctlExe)) {
    Warn "missing $QmtctlExe - skip qmtctl task"
} else {
    $wrapper = Join-Path $PSScriptRoot "ensure_miniqmt.ps1"
    $wrapContent = "& '$QmtctlExe' ensure-miniqmt -path '$MiniQmtPath' -gateway-url http://127.0.0.1:8789/health"
    # UTF8（PS5.1 带 BOM）：MiniQmtPath 常含中文安装目录，ASCII 会写成 '?' 导致启动失败
    Set-Content -Path $wrapper -Value $wrapContent -Encoding UTF8
    $taskName = "QMT-Ensure-Running"
    $action = "powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper"
    # no /RU SYSTEM: runs in the logged-on interactive session (MiniQMT needs GUI session)
    schtasks /Create /F /SC MINUTE /MO 10 /TN $taskName /TR $action
    if ($LASTEXITCODE -eq 0) { Ok "task $taskName created (every 10 min, interactive)" }
    else { Warn "schtasks create failed (exit=$LASTEXITCODE); create manually (current user, not SYSTEM)" }
}

Ok "engine services registered. Verify: Get-Service quant,quant-research,pydata ; schtasks /Query /TN QMT-Ensure-Running"
