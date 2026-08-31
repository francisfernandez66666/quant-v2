# install_guangzhou.ps1 — 广州单机全合一「直接切流」一键安装（RDP 登录后跑一次）
# 用法（管理员 PowerShell，当前目录为部署包根，含各 *.exe 与 qmt-win\ 脚本）：
#   powershell -ExecutionPolicy Bypass -File install_guangzhou.ps1 `
#       -DataDir C:\var\lib\quant-trading-v2 `
#       -MiniQmtPath "C:\QMT\userdata_mini\bin\XtMiniQmt.exe" `
#       -LLMApiKey "sk-xxx" -AdminSourceIP "你的出口IP/32"
# 前置：本机已装 64 位 Python（pydata/baostock 用）；qmt_gateway 已按 setup_windows.ps1 生成 config.xt.json。
#
# 切流动作（见 docs/MIGRATION_GUANGZHOU_ALLINONE.md §2）：
#   1) 引擎 qmt.enabled=true，gateway_url=http://127.0.0.1:8789，token 复用网关 token
#   2) 网关 report_url 改 http://127.0.0.1:8080（不再回首尔），重启 qmt-gateway
#   3) qmtctl 交互会话任务（每 10 分钟确保 MiniQMT 在交易时段在线）
#   4) 防火墙收严：8789 仅 127.0.0.1；8080 仅你的出口 IP；3389 仅你的出口 IP
# 注：首尔侧需同步把该账号 qmt.enabled 置 false（停用首尔决策），避免双控。本脚本不碰首尔。
param(
    [string]$DeployRoot = ".",                       # 部署包根（含 *.exe 与 qmt-win\）
    [string]$QuantExe   = "",                        # 留空取 $DeployRoot\quant.exe
    [string]$ResearchExe= "",
    [string]$PydataVenv = "",
    [string]$QmtctlExe  = "",
    [string]$MiniQmtPath = "C:\QMT\userdata_mini\bin\XtMiniQmt.exe",
    [string]$DataDir    = "C:\var\lib\quant-trading-v2",
    [string]$GatewayDir = "",                        # 留空取 ..\qmt_gateway 或 $DeployRoot\qmt_gateway
    [string]$LLMApiKey  = "",
    [string]$LLMApiURL  = "https://api.siliconflow.cn/v1/chat/completions",
    [string]$LLMModel   = "THUDM/GLM-Z1-9B-0414",
    [string]$AdminSourceIP = "",                     # 防火墙放行的管理出口 IP（如 1.2.3.4/32）；空=不收严
    [switch]$Rollback                                # 回滚：网关 report_url 恢复首尔 + 引擎 qmt.enabled=false
)
$ErrorActionPreference = "Stop"
function Info($m){Write-Host "[install] $m" -ForegroundColor Cyan}
function Ok($m){Write-Host "[ ok ] $m" -ForegroundColor Green}
function Warn($m){Write-Host "[warn] $m" -ForegroundColor Yellow}
function Die($m){Write-Host "[fail] $m" -ForegroundColor Red; exit 1}

# 网关重启（交互会话任务模式；勿用 Restart-Service——NSSM/Session 0 实例无法与
# QMT 客户端建立 xtquant 共享内存会话，见 deploy/qmt-win/register_service.ps1 文件头）
function Restart-GatewayTask{
    Get-CimInstance Win32_Process -Filter "Name='python.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -match 'gateway\.py' } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Start-Sleep 2
    schtasks /Run /TN QMT-Gateway-Ensure | Out-Null
    Start-Sleep 8
}

# ---- 0. 管理员 ----
$id=[Security.Principal.WindowsIdentity]::GetCurrent()
if(-not (New-Object Security.Principal.WindowsPrincipal($id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))){Die "请管理员运行"}

# ---- 1. 路径 ----
$DeployRoot = Resolve-Path $DeployRoot
if(-not $QuantExe){$QuantExe=Join-Path $DeployRoot "quant.exe"}
if(-not $ResearchExe){$ResearchExe=Join-Path $DeployRoot "researchd.exe"}
if(-not $QmtctlExe){$QmtctlExe=Join-Path $DeployRoot "qmtctl.exe"}
if(-not $PydataVenv){$PydataVenv=Join-Path $DeployRoot "venv"}
foreach($e in @($QuantExe,$ResearchExe,$QmtctlExe)){if(-not (Test-Path $e)){Die "缺少 $e"}}
# 网关目录：setup_windows.ps1 约定仓库内 qmt_gateway；部署包可能带 qmt_gateway\
if(-not $GatewayDir){
    $cand=@(Join-Path $DeployRoot "qmt_gateway"; Join-Path (Split-Path $DeployRoot) "qmt_gateway"; "C:\qmt\quant-trading-v2\qmt_gateway") | Where-Object {Test-Path (Join-Path $_ "config.xt.json")}
    if($cand){$GatewayDir=$cand[0]}else{Die "未找到 qmt_gateway/config.xt.json（先跑 setup_windows.ps1）"}
}
$cfgXt=Join-Path $GatewayDir "config.xt.json"
if(-not (Test-Path $cfgXt)){Die "缺少 $cfgXt"}
$DEPLOY_DIR=Join-Path $DeployRoot ""  # 二进制就放部署包根（已就位）

# ---- 1.5 pydata (baostock) venv ----
$pyCmd=Get-Command python -ErrorAction SilentlyContinue
if(-not $pyCmd){Warn "未找到 python —— 跳过 pydata venv（baostock 数据将不可用，其余正常）"}
else{
    if(-not (Test-Path (Join-Path $PydataVenv "Scripts\python.exe"))){
        Info "创建 pydata venv: $PydataVenv"
        & $pyCmd.Source -m venv $PydataVenv
        $pip=Join-Path $PydataVenv "Scripts\pip.exe"
        & $pip install --quiet -r (Join-Path $DeployRoot "requirements.txt")
    }else{Ok "pydata venv 已存在，跳过"}
}

# ---- 2. 注册引擎服务（NSSM）----
$qmtWin=Join-Path $DeployRoot "qmt-win"
$reg=Join-Path $qmtWin "register_engine_services.ps1"
if(-not (Test-Path $reg)){Die "缺少 $reg"}
$regArgs="-QuantExe `"$QuantExe`" -ResearchExe `"$ResearchExe`" -PydataVenv `"$PydataVenv`" -QmtctlExe `"$QmtctlExe`" -MiniQmtPath `"$MiniQmtPath`" -DataDir `"$DataDir`""
if($LLMApiKey){$regArgs+=" -LLMApiKey `"$LLMApiKey`" -LLMApiURL `"$LLMApiURL`" -LLMModel `"$LLMModel`""}
Info "注册引擎服务..."
& powershell -NoProfile -ExecutionPolicy Bypass -File $reg $regArgs.Split(" ")

# ---- 3. 切流 / 回滚 ----
$xcfg = Get-Content $cfgXt -Raw | ConvertFrom-Json
$gwToken = $xcfg.token
$engineCfgPath = Join-Path $DataDir "config.json"

if($Rollback){
    Info "回滚：网关 report_url 恢复首尔 + 引擎 qmt.enabled=false"
    $xcfg.report_url = "https://quant-trading.top"
    $xcfg | ConvertTo-Json -Depth 6 | Set-Content $cfgXt
    if(Test-Path $engineCfgPath){
        $ec=Get-Content $engineCfgPath -Raw | ConvertFrom-Json
        if($ec.qmt){$ec.qmt.enabled=$false}
        $ec | ConvertTo-Json -Depth 8 | Set-Content $engineCfgPath
    }
    Restart-GatewayTask
    Ok "回滚完成（首尔侧需把该账号 qmt.enabled 重新置 true 恢复）"
    exit 0
}

# 3a. 网关 report_url → 本地引擎
Info "网关 report_url → http://127.0.0.1:8080（不再回首尔）"
$xcfg.report_url = "http://127.0.0.1:8080"
$xcfg | ConvertTo-Json -Depth 6 | Set-Content $cfgXt

# 3b. 引擎 config.json qmt 块：enabled=true, gateway_url=localhost, token=网关token
Info "引擎 config.json qmt.enabled=true (gateway_url=localhost, token 复用网关)"
$ec = if(Test-Path $engineCfgPath){Get-Content $engineCfgPath -Raw | ConvertFrom-Json}else{[ordered]@{}}
if(-not $ec.qmt){$ec|Add-Member -NotePropertyName qmt -NotePropertyValue ([ordered]@{})}
$ec.qmt.enabled     = $true
$ec.qmt.mode        = if($ec.qmt.mode){$ec.qmt.mode}else{"manual"}   # 首日默认 manual，权限批后前端翻 auto
$ec.qmt.gateway_url = "http://127.0.0.1:8789"
$ec.qmt.token       = $gwToken
if(-not $ec.rules){$ec|Add-Member -NotePropertyName rules -NotePropertyValue ([ordered]@{})}
$ec | ConvertTo-Json -Depth 8 | Set-Content $engineCfgPath

# 3c. 重启网关 + 引擎服务使配置生效
Restart-GatewayTask
Restart-Service quant -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 3

# ---- 4. 防火墙收严 ----
if($AdminSourceIP){
    Info "防火墙收严：8789→127.0.0.1；8080/3389→$AdminSourceIP"
    netsh advfirewall firewall set rule name="QMT-Gateway-8789" new remoteip=127.0.0.1 2>$null
    if($LASTEXITCODE -ne 0){netsh advfirewall firewall add rule name="QMT-Gateway-8789" dir=in action=allow protocol=TCP localport=8789 remoteip=127.0.0.1}
    netsh advfirewall firewall set rule name="Quant-API-8080" new remoteip=$AdminSourceIP 2>$null
    if($LASTEXITCODE -ne 0){netsh advfirewall firewall add rule name="Quant-API-8080" dir=in action=allow protocol=TCP localport=8080 remoteip=$AdminSourceIP}
    netsh advfirewall firewall set rule name="RDP-3389" new remoteip=$AdminSourceIP 2>$null
}

# ---- 5. 健康检查 ----
try{
    $h=Invoke-RestMethod "http://127.0.0.1:8789/health" -TimeoutSec 5
    Ok "网关 /health broker_connected=$($h.broker_connected)"
}catch{ Warn "网关 /health 失败：$($_.Exception.Message)" }
try{
    $s=Invoke-RestMethod "http://127.0.0.1:8080/setup" -TimeoutSec 5
    Ok "引擎 /setup 可达"
}catch{ Warn "引擎 /setup 失败：$($_.Exception.Message)" }

Ok "切流完成。下一步：浏览器开 http://<本机公网IP>:8080/setup 建管理员；首尔侧把该账号 qmt.enabled 置 false。"
Ok "回滚命令：powershell -ep bypass -File install_guangzhou.ps1 -Rollback"
