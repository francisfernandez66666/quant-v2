# register_service.ps1 — 把 qmt_gateway 注册为 Windows 服务（nssm 守护），可选 QMT 日常守护任务
# 用法（必须管理员 PowerShell）：
#   powershell -ExecutionPolicy Bypass -File register_service.ps1
#   powershell -ExecutionPolicy Bypass -File register_service.ps1 -InstallQmtRestart -InstallQmtAutostart
# 前置：先跑 setup_windows.ps1 生成 config.xt.json。
# 说明：
#   - nssm 提供开机自启 + 崩溃自动拉起 + 日志轮转；官网下载失败时手动放 nssm.exe 到 tools\ 再重跑。
#   - -InstallQmtRestart：每天 07:40 强杀极简 QMT 进程（防长跑内存增长），
#     仅当 QMT 已「记住账号密码」能自动重新登录时才启用，否则重启后停在登录页导致网关断连（首尔侧会自动熔断停单，安全但没得交易）。
#   - -InstallQmtAutostart：把 XtMiniQmt.exe 快捷方式放进当前用户启动文件夹；需配合 Windows 自动登录（netplwiz）。
param(
    [string]$PythonExe = "",
    [string]$GatewayDir = "",      # 留空取本仓库相对路径 qmt_gateway
    [string]$ConfigFile = "",      # 留空取 <GatewayDir>\config.xt.json
    [string]$ServiceName = "qmt-gateway",
    [string]$NSSMUrl = "https://nssm.cc/release/nssm-2.24.zip",
    [switch]$InstallQmtRestart,
    [switch]$InstallQmtAutostart
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[svc] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# ---- 0. 管理员校验 ----
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Die "请以管理员身份运行 PowerShell（右键 → 以管理员身份运行）"
}

# ---- 1. 路径默认值 ----
if (-not $GatewayDir) {
    $GatewayDir = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) "qmt_gateway"
}
if (-not $ConfigFile) { $ConfigFile = Join-Path $GatewayDir "config.xt.json" }
if (-not (Test-Path $ConfigFile)) { Die "缺少 $ConfigFile —— 先运行 setup_windows.ps1 生成配置" }
if (-not $PythonExe) {
    $pyCmd = Get-Command python -ErrorAction SilentlyContinue
    if (-not $pyCmd) { Die "python 不在 PATH —— 用 -PythonExe 指定 python.exe 绝对路径" }
    $PythonExe = $pyCmd.Source
}
Info "服务: $ServiceName"
Info "python: $PythonExe"
Info "config: $ConfigFile"

# ---- 2. 准备 nssm ----
$tools = Join-Path $PSScriptRoot "tools"
$nssm = Join-Path $tools "nssm-2.24\win64\nssm.exe"
if (-not (Test-Path $nssm)) {
    Info "下载并解压 nssm ..."
    New-Item $tools -ItemType Directory -Force | Out-Null
    try {
        Invoke-WebRequest -Uri $NSSMUrl -OutFile (Join-Path $tools "nssm.zip") -UseBasicParsing -TimeoutSec 60
        Expand-Archive (Join-Path $tools "nssm.zip") $tools -Force
    } catch {
        Die "nssm 下载失败（$NSSMUrl）：$($_.Exception.Message)`n手动下载后解压到 $tools 再重跑本脚本"
    }
}
if (-not (Test-Path $nssm)) { Die "未找到 $nssm —— 检查压缩包结构（需 win64\nssm.exe）" }

# ---- 3. 注册服务 ----
Info "注册服务 $ServiceName ..."
& $nssm install $ServiceName $PythonExe "gateway.py -c `"$ConfigFile`"" | Out-Null
& $nssm set $ServiceName AppDirectory $GatewayDir | Out-Null
$logDir = Join-Path $GatewayDir "logs"
New-Item $logDir -ItemType Directory -Force | Out-Null
& $nssm set $ServiceName AppStdout (Join-Path $logDir "out.log") | Out-Null
& $nssm set $ServiceName AppStderr (Join-Path $logDir "err.log") | Out-Null
& $nssm set $ServiceName AppRotateFiles 1 | Out-Null
& $nssm set $ServiceName AppRotateOnline 1 | Out-Null
& $nssm set $ServiceName AppRotateBytes 10485760 | Out-Null     # 10MB 轮转
& $nssm set $ServiceName Start SERVICE_AUTO_START | Out-Null
& $nssm restart $ServiceName
Start-Sleep -Seconds 2
Get-Service $ServiceName | Format-List Name, Status, StartType

# 本机健康检查
try {
    $h = Invoke-RestMethod "http://127.0.0.1:8789/health" -TimeoutSec 5
    Ok "网关 /health: ok=$($h.ok) broker=$($h.broker) broker_connected=$($h.broker_connected)"
} catch {
    Warn "本机 8789 健康检查失败: $($_.Exception.Message)（看日志 $logDir\err.log；broker=mock 应秒起）"
}

# ---- 4. 可选：每日 07:40 重启极简 QMT 进程（内存增长守卫）----
if ($InstallQmtRestart) {
    $taskName = "QMT-Daily-Restart"
    $ps1 = Join-Path $tools "restart_qmt.ps1"
    Set-Content -Path $ps1 -Value "Get-Process XtMiniQmt -ErrorAction SilentlyContinue | Stop-Process -Force" -Encoding ASCII
    schtasks /Create /F /SC DAILY /ST 07:40 /TN $taskName /TR "powershell -NoProfile -ExecutionPolicy Bypass -File $ps1" /RU SYSTEM
    if ($LASTEXITCODE -eq 0) { Ok "计划任务 $taskName 已创建（每天 07:40 重启 QMT 进程）" }
    else { Warn "schtasks 创建失败（exit=$LASTEXITCODE），可手动在任务计划程序里建" }
}

# ---- 5. 可选：QMT 开机自启快捷方式 ----
if ($InstallQmtAutostart) {
    $exe = $null
    foreach ($d in (@("C:", "D:", "E:") | Where-Object { Test-Path $_ })) {
        $exe = Get-ChildItem "$d\" -Recurse -Filter "XtMiniQmt.exe" -ErrorAction SilentlyContinue |
            Select-Object -First 1 -ExpandProperty FullName
        if ($exe) { break }
    }
    if (-not $exe) {
        Warn "未找到 XtMiniQmt.exe —— 手动把它加入启动文件夹后跳过本步"
    } else {
        $ws = New-Object -ComObject WScript.Shell
        $lnkPath = Join-Path "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup" "XtMiniQmt.lnk"
        $lnk = $ws.CreateShortcut($lnkPath)
        $lnk.TargetPath = $exe
        $lnk.Save()
        Ok "已加入开机自启: $exe"
        Warn "记得配置系统自动登录（netplwiz → 取消勾选'要求用户输入用户名和密码'），否则重启后停在登录页、QMT 无法自启"
    }
}

Ok "全部完成。验证命令：Get-Service $ServiceName ；curl http://127.0.0.1:8789/health"
