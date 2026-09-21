# register_engine_services.ps1 - Guangzhou all-in-one: register engine Windows services (NSSM) + qmtctl task scheduler.
# Usage (admin PowerShell):
#   powershell -ExecutionPolicy Bypass -File register_engine_services.ps1 `
#       -QuantExe C:\opt\quant\quant.exe -ResearchExe C:\opt\quant\researchd.exe `
#       -PydataVenv C:\opt\quant\venv -QmtctlExe C:\opt\quant\qmtctl.exe `
#       -MiniQmtPath "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe" `
#       -DataDir C:\var\lib\quant-trading-v2 -LLMApiKey "..." -LLMApiURL "..." -LLMModel "..." `
#       -HithinkApiKey "..."
# NOTE: MiniQmtPath MUST be the full client XtItClient.exe (auto-login + trading). XtMiniQmt.exe
#       cannot auto-login → broker never connects.
# NOTE (§ENH-0 2026-09-19): HithinkApiKey = HITHINK_FINANCE_API_KEY，交易日历/行情主源密钥。
#       旧脚本从不注入它 → quant 主服务永远"周末口径兜底"，法定节假日误判为交易日。请随部署传入。
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
    [string]$HithinkApiKey = "",
    [string]$NSSMUrl = "https://nssm.cc/release/nssm-2.24.zip"
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[eng] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# §H8(2026-09-22)：端口/端点唯一来源收编到 service_probe_config.ps1——本脚本与运维探针
# （all_service_watchdog.ps1 / daily_ops_check.ps1）读同一组变量，根除"部署端口与探针端口各改各的"
# （H8 实录：watchdog 硬编码 :8080/api/status，而这里早已改注 QUANT_ADDR=127.0.0.1:8081）。
# 配置缺失时回退为本文件旧字面量并告警，不阻断注册流程。
$probeCfg = Join-Path $PSScriptRoot "service_probe_config.ps1"
if (Test-Path $probeCfg) {
    . $probeCfg
    Info "probe config loaded: $probeCfg (§H8 端口同源)"
} else {
    Warn "missing $probeCfg - fallback to in-script literals (§H8：运维探针将与本脚本端口脱钩，请补齐部署清单)"
    $ProbeQuantPort  = 8081
    $ProbePydataPort = 8787
    $ProbeGatewayUrl = "http://127.0.0.1:8789/health"
}

# §ENH-0(2026-09-19)：统一构造服务级环境变量。此前 LLM 三元组之外的密钥（尤其
# HITHINK_FINANCE_API_KEY=交易日历/行情主源）从不注入，quant 生产进程一直缺它。
# 逗号 return 防 PowerShell 单元素数组被标量化。
function Get-BaseEnvExtra {
    $e = @("TZ=Asia/Shanghai", "QUANT_DATA_DIR=$DataDir")
    if ($LLMApiKey)     { $e += @("LLM_API_KEY=$LLMApiKey", "LLM_API_URL=$LLMApiURL", "LLM_MODEL=$LLMModel") }
    if ($HithinkApiKey) { $e += @("HITHINK_FINANCE_API_KEY=$HithinkApiKey") }
    return ,$e
}

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
    & $nssm set $name AppEnvironmentExtra (Get-BaseEnvExtra) | Out-Null
}

# 2. quant (NORMAL)
if (-not (Test-Path $QuantExe)) { Die "missing $QuantExe" }
Register-NssmService "quant" $QuantExe @() "NORMAL_PRIORITY_CLASS"
# §部署修复 2026-09-17：端口必须是 127.0.0.1:8081——广州拓扑下 Caddy 占用 :8080
# （Caddyfile /api/* → reverse_proxy 127.0.0.1:8081）。旧值 0.0.0.0:8080 与 Caddy
# 撞端口，配合 §W4-b fail-fast 会让 quant 服务起不来（5s 重启循环，部署实录）。
# §ENH-0(2026-09-19)：env 统一走 Get-BaseEnvExtra（含可选 HITHINK_FINANCE_API_KEY），
# 覆盖注册函数刚才写入的集合——AppEnvironmentExtra 是整体替换语义，必须带全量再叠 QUANT_ADDR。
& $nssm set quant AppEnvironmentExtra ((Get-BaseEnvExtra) + @("QUANT_ADDR=127.0.0.1:$ProbeQuantPort")) | Out-Null
if (-not $HithinkApiKey) {
    Warn "HithinkApiKey 未提供：交易日历将按周末口径兜底（法定节假日会误判为交易日，直到首次成功拉取后的磁盘缓存生效）"
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
    Register-NssmService "pydata" $pyExe @("$pyScript", "--host", "127.0.0.1", "--port", "$ProbePydataPort") "BELOW_NORMAL_PRIORITY_CLASS"
    & $nssm set pydata AppDirectory "C:\opt\quant\pydata" | Out-Null
    & $nssm restart pydata
    Start-Sleep -Seconds 2
    Ok "pydata registered/restarted (127.0.0.1:$ProbePydataPort)"
}

# 5. qmtctl scheduled task (interactive session) - generate a wrapper ps1 to avoid nested quoting
if (-not (Test-Path $QmtctlExe)) {
    Warn "missing $QmtctlExe - skip qmtctl task"
} else {
    $wrapper = Join-Path $PSScriptRoot "ensure_miniqmt.ps1"
    $wrapContent = "& '$QmtctlExe' ensure-miniqmt -path '$MiniQmtPath' -gateway-url $ProbeGatewayUrl"
    # UTF8（PS5.1 带 BOM）：MiniQmtPath 常含中文安装目录，ASCII 会写成 '?' 导致启动失败
    Set-Content -Path $wrapper -Value $wrapContent -Encoding UTF8
    $taskName = "QMT-Ensure-Running"
    # §FIX 2026-08-31：无窗口包装——wscript(GUI 子系统)经 VBS 隐藏运行，根除交互任务
    # 每 10 分钟的黑框闪烁（"监控闪退"观感）。VBS 内容 ASCII（wscript 不认 UTF-8 BOM）。
    $vbs = Join-Path $PSScriptRoot "run_qmt_ensure.vbs"
    [IO.File]::WriteAllText($vbs, "CreateObject(""WScript.Shell"").Run ""powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper"", 0, True", (New-Object Text.ASCIIEncoding))
    $action = "wscript.exe //B $vbs"
    # no /RU SYSTEM: runs in the logged-on interactive session (MiniQMT needs GUI session)
    schtasks /Create /F /SC MINUTE /MO 10 /TN $taskName /TR $action
    if ($LASTEXITCODE -eq 0) { Ok "task $taskName created (every 10 min, interactive)" }
    else { Warn "schtasks create failed (exit=$LASTEXITCODE); create manually (current user, not SYSTEM)" }
}

# 6. §RFIX-5 日志保留计划任务（每日 07:30，SYSTEM 可无窗执行——纯文件清理不涉 GUI）
$prune = Join-Path $PSScriptRoot "prune_logs.ps1"
if (Test-Path $prune) {
    schtasks /Create /F /SC DAILY /ST 07:30 /TN "Quant-Log-Prune" /TR "powershell -NoProfile -ExecutionPolicy Bypass -File $prune" | Out-Null
    if ($LASTEXITCODE -eq 0) { Ok "task Quant-Log-Prune created (daily 07:30, keep 20 rotations)" }
    else { Warn "Quant-Log-Prune 创建失败（exit=$LASTEXITCODE）：轮转日志将无限累积，请手动创建" }
} else {
    Warn "missing $prune - skip log-prune task（researchd/quant_stderr 轮转日志不会自动清理）"
}

Ok "engine services registered. Verify: Get-Service quant,quant-research,pydata ; schtasks /Query /TN QMT-Ensure-Running"
