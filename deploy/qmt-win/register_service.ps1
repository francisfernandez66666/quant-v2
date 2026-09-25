# register_service.ps1 — 把 qmt_gateway 注册为「交互会话计划任务」（勿用 NSSM！），可选 QMT 守护任务
# 用法（必须管理员 PowerShell）：
#   powershell -ExecutionPolicy Bypass -File register_service.ps1
#   powershell -ExecutionPolicy Bypass -File register_service.ps1 -InstallQmtAutostart
# 前置：先跑 setup_windows.ps1 生成 config.xt.json。
#
# ⚠️ 为什么不能用 NSSM/Windows 服务（2026-08-31 广州机实障结论）：
#   xtquant 与 QMT 客户端之间经「按会话隔离的命名共享内存队列」通信（down_queue_win_N/
#   up_queue_*/lock_*，位于 userdata_mini）。服务跑在 Session 0，客户端跑在交互桌面会话，
#   双方各连各的共享内存 → 客户端日志每 8 秒循环 "quant session connected → heartbeat
#   timeout → file lock not held, offline"，XtQuantTrader.connect() 恒返回 -1。
#   因此网关 python 必须与 XtItClient/XtMiniQmt 同在登录用户的交互会话中运行——
#   由"仅在用户登录时运行"的计划任务拉起（与 qmtctl 同模式）。
# 说明：
#   - 计划任务提供登录自启 + 每 5 分钟幂等守护（端口活着就不动，死了才拉起）。
#   - -InstallQmtAutostart：把 XtItClient.exe 快捷方式放进当前用户启动文件夹；
#     需配合 Windows 自动登录（netplwiz），否则重启后无人登录、客户端与网关都起不来。
#
# §C7-OPS（2026-09-26，FIX_PLAN_20260925EVE ⑯）已收编进 service_definitions.ps1 的定义：
#   交互任务名（$SvcTaskGatewayEnsure/$SvcTaskGatewayLogon）、守护间隔（$SvcGatewayEnsureIntervalMin）、
#   遗留 NSSM 网关服务名清单（$SvcLegacyGwServiceNames）、网关目录推导缺省（$SvcGatewayDir）、
#   幂等判据端口（§H8 $ProbeGatewayPort，旧版在此脚本硬编码 8789 两处）。param() 字面量保留为
#   定义文件缺失时的回退；操作人显式传参永远赢过单源缺省。
param(
    [string]$PythonExe = "",
    [string]$GatewayDir = "",      # 留空取 §C7 单源 $SvcGatewayDir（现网 C:\qmt\quant-trading-v2\qmt_gateway），
                                   # 定义文件缺失时回退本仓库相对路径 qmt_gateway
    [string]$ConfigFile = "",      # 留空取 <GatewayDir>\config.xt.json
    [string]$TaskEnsureName = "",  # 留空取 §C7 单源（默认 QMT-Gateway-Ensure）
    [string]$TaskLogonName = "",   # 留空取 §C7 单源（默认 QMT-Gateway-Logon）
    [int]$EnsureIntervalMin = 0,   # 0 = 取 §C7 单源（默认 5 分钟）
    [switch]$InstallQmtRestart,
    [switch]$InstallQmtAutostart
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[svc] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# ---- §C7-OPS：服务/任务定义单源（缺失回退本脚本字面量并告警，不阻断——注册步是施工入口）----
$svcDefs = Join-Path $PSScriptRoot "service_definitions.ps1"
if (Test-Path $svcDefs) {
    . $svcDefs
    Info "service definitions loaded: $svcDefs (§C7 服务/任务定义同源)"
} else {
    Warn "missing $svcDefs - 任务名/端口回退本脚本字面量（§C7：watchdog 与本脚本的定义将脱钩，请补齐部署清单）"
    $SvcTaskGatewayEnsure = "QMT-Gateway-Ensure"; $SvcTaskGatewayLogon = "QMT-Gateway-Logon"
    $SvcGatewayEnsureIntervalMin = 5
    $SvcLegacyGwServiceNames = @("qmt-gateway", "quant-gateway")
    $ProbeGatewayPort = 8789
}
if (-not $TaskEnsureName)   { $TaskEnsureName = $SvcTaskGatewayEnsure }
if (-not $TaskLogonName)    { $TaskLogonName = $SvcTaskGatewayLogon }
if ($EnsureIntervalMin -le 0) { $EnsureIntervalMin = $SvcGatewayEnsureIntervalMin }

# ---- 0. 管理员校验 ----
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Die "请以管理员身份运行 PowerShell（右键 → 以管理员身份运行）"
}

# ---- 1. 路径默认值 ----
if (-not $GatewayDir) {
    # §C7-OPS：现网网关目录取单源 $SvcGatewayDir（= deploy_guangzhou.sh QMT_GATEWAY_DIR）；
    # 定义文件缺失（上面已 Warn）时回退旧的"本仓库相对路径"推导。
    if ($SvcGatewayDir -and (Test-Path $SvcGatewayDir)) {
        $GatewayDir = $SvcGatewayDir
    } else {
        $GatewayDir = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) "qmt_gateway"
    }
}
if (-not $ConfigFile) { $ConfigFile = Join-Path $GatewayDir "config.xt.json" }
if (-not (Test-Path $ConfigFile)) { Die "缺少 $ConfigFile —— 先运行 setup_windows.ps1 生成配置" }
if (-not $PythonExe) {
    $pyCmd = Get-Command python -ErrorAction SilentlyContinue
    if (-not $pyCmd) { Die "python 不在 PATH —— 用 -PythonExe 指定 python.exe 绝对路径" }
    $PythonExe = $pyCmd.Source
}
Info "python: $PythonExe"
Info "config: $ConfigFile"

# ---- 2. 停用遗留 NSSM 网关服务（Session 0 实例永远连不上，且会抢占 8789 端口）----
# §C7-OPS：遗留名清单来自 service_definitions.ps1（含 all_service_watchdog 旧守护清单用过的
# 下划线名 qmt_gateway——若那台机器真按旧径装过它，一并停用；绝不可把它当网关拉起路径复活）。
$nssmLegacy = if (Get-Command Resolve-SvcNssm -ErrorAction SilentlyContinue) { Resolve-SvcNssm } else { $null }
if (-not $nssmLegacy) { $nssmLegacy = (Join-Path $PSScriptRoot "tools\nssm-2.24\win64\nssm.exe") }
foreach ($legacy in $SvcLegacyGwServiceNames) {
    $svc = Get-Service -Name $legacy -ErrorAction SilentlyContinue
    if ($svc) {
        Warn "检测到遗留服务 $legacy（NSSM/Session 0）—— 停止并禁用"
        Stop-Service $legacy -Force -ErrorAction SilentlyContinue
        if (Test-Path $nssmLegacy) {
            & $nssmLegacy set $legacy Start SERVICE_DISABLED 2>$null
            if ($LASTEXITCODE -ne 0) { Set-Service $legacy -StartupType Disabled -ErrorAction SilentlyContinue }
        } else {
            Set-Service $legacy -StartupType Disabled -ErrorAction SilentlyContinue
        }
    }
}

# ---- 3. 生成守护 wrapper：网关口未监听才拉起（幂等），与客户端同交互会话 ----
# §C7-OPS：判据端口来自 §H8 单源 $ProbeGatewayPort（旧版在这里和 :health 检查两处硬编码 8789）。
$wrapper = Join-Path $PSScriptRoot "ensure_gateway.ps1"
$wrap = @"
`$ErrorActionPreference = 'SilentlyContinue'
`$py = '$PythonExe'
`$gw = '$GatewayDir\gateway.py'
`$cfg = '$ConfigFile'
`$dir = '$GatewayDir'
# xtquant <-> QMT 客户端经按会话隔离的共享内存通信：网关必须与客户端同交互会话，
# 绝不能以服务/SYSTEM 会话运行（详见 register_service.ps1 文件头说明）。
`$listening = Get-NetTCPConnection -LocalPort $ProbeGatewayPort -State Listen
if (-not `$listening) {
  Start-Process -FilePath `$py -ArgumentList "`"`$gw`" -c `"`$cfg`"" -WorkingDirectory `$dir -WindowStyle Hidden
  exit 1
}
exit 0
"@
# UTF8（PS5.1 带 BOM）：防路径含中文时被按 GBK 误读成乱码
Set-Content -Path $wrapper -Value $wrap -Encoding UTF8
Ok "已生成守护 wrapper: $wrapper"

# ---- 3.5 无窗口包装器（§FIX 2026-08-31）：交互会话计划任务每次触发都会弹黑色控制台
#      （用户观感="监控程序闪退"）。wscript.exe 是 GUI 子系统程序自身无控制台，
#      经 VBS 以隐藏窗口+同步等待运行 wrapper，任务触发间隔/会话亲和/单实例策略不变。
#      VBS 内容保持 ASCII（wscript 对 UTF-8 BOM 支持差）。
$vbs = Join-Path $PSScriptRoot "run_gateway_ensure.vbs"
[IO.File]::WriteAllText($vbs, "CreateObject(""WScript.Shell"").Run ""powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper"", 0, True", (New-Object Text.ASCIIEncoding))
Ok "已生成无窗口包装器: $vbs"

# ---- 4. 注册计划任务（交互会话，勿加 /RU SYSTEM）----
$taskAction = "wscript.exe //B $vbs"
schtasks /Create /F /SC MINUTE /MO $EnsureIntervalMin /TN $TaskEnsureName /TR $taskAction
if ($LASTEXITCODE -eq 0) { Ok "任务 $TaskEnsureName 已创建（每 $EnsureIntervalMin 分钟幂等守护，仅登录会话运行）" }
else { Warn "schtasks 创建失败（exit=$LASTEXITCODE），请在任务计划程序手动创建（当前用户、交互令牌）" }
schtasks /Create /F /SC ONLOGON /TN $TaskLogonName /TR $taskAction
if ($LASTEXITCODE -eq 0) { Ok "任务 $TaskLogonName 已创建（用户登录即拉起）" }

# ---- 5. 立即跑一次并做本机健康检查 ----
schtasks /Run /TN $TaskEnsureName
Start-Sleep -Seconds 8
try {
    $h = Invoke-RestMethod "http://127.0.0.1:$ProbeGatewayPort/health" -TimeoutSec 5
    Ok "网关 /health: ok=$($h.ok) broker=$($h.broker) broker_connected=$($h.broker_connected)（xt 通道需客户端已登录才为 true）"
} catch {
    Warn "本机 $ProbeGatewayPort 健康检查失败: $($_.Exception.Message)（gateway-<pid>.log 可查；先确认 QMT 客户端在线）"
}

# ---- 6. 可选：每日 07:40 重启 QMT 客户端（内存增长守卫）----
if ($InstallQmtRestart) {
    $taskName = "QMT-Daily-Restart"
    $ps1 = Join-Path $PSScriptRoot "tools\restart_qmt.ps1"
    Set-Content -Path $ps1 -Value "Get-Process XtItClient,XtMiniQmt -ErrorAction SilentlyContinue | Stop-Process -Force" -Encoding ASCII
    # 无窗口包装（同 §3.5）：每日一次的黑框闪烁同样消除
    $vbsR = Join-Path $PSScriptRoot "run_daily_restart.vbs"
    [IO.File]::WriteAllText($vbsR, "CreateObject(""WScript.Shell"").Run ""powershell -NoProfile -ExecutionPolicy Bypass -File $ps1"", 0, True", (New-Object Text.ASCIIEncoding))
    # 必须交互会话运行（/RU SYSTEM 会话里杀不到桌面进程的窗口句柄，且重登需 GUI）
    schtasks /Create /F /SC DAILY /ST 07:40 /TN $taskName /TR "wscript.exe //B $vbsR"
    if ($LASTEXITCODE -eq 0) { Ok "计划任务 $taskName 已创建（每天 07:40 重启客户端；重启后由 QMT-Ensure-Running 拉起并自动登录）" }
    else { Warn "schtasks 创建失败（exit=$LASTEXITCODE），可手动在任务计划程序里建" }
}

# ---- 7. 可选：QMT 完整交易端开机自启快捷方式 ----
if ($InstallQmtAutostart) {
    $exe = $null
    foreach ($d in (@("C:", "D:", "E:") | Where-Object { Test-Path $_ })) {
        # 拉起完整交易端 XtItClient.exe（自动登录），而非 XtMiniQmt.exe（无法自动登录）
        $exe = Get-ChildItem "$d\Program Files*" -Recurse -Depth 4 -Filter "XtItClient.exe" -ErrorAction SilentlyContinue |
            Select-Object -First 1 -ExpandProperty FullName
        if ($exe) { break }
    }
    if (-not $exe) {
        Warn "未找到 XtItClient.exe —— 手动把它加入启动文件夹后跳过本步"
    } else {
        $ws = New-Object -ComObject WScript.Shell
        $lnkPath = Join-Path "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup" "XtItClient.lnk"
        $lnk = $ws.CreateShortcut($lnkPath)
        $lnk.TargetPath = $exe
        $lnk.Save()
        Ok "已加入开机自启: $exe"
        Warn "记得配置系统自动登录（netplwiz → 取消勾选'要求用户输入用户名和密码'），否则重启后停在登录页、客户端与网关都无法自启"
    }
}

Ok "全部完成。验证命令：schtasks /Query /TN $TaskEnsureName ；curl http://127.0.0.1:$ProbeGatewayPort/health"
