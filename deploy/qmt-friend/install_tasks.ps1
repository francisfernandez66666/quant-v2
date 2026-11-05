# install_tasks.ps1 —— 朋友机器上四个计划任务的注册/注销（§FRIEND-PROFILE 批 2，2026-11-02）
#
# 为什么必须是计划任务而不是脚本自己常驻：
#   1) QMT 客户端要求交互式桌面会话，服务账号起不来（现网同形：QMT-Ensure-Running 走计划任务）；
#   2) 10-29 实证——在 ssh/远程会话里直接 Start-Process 拉客户端，进程随会话被杀；
#   3) 看门狗自己死了也要有人再把它叫起来（任务计划就是那个"守门人的守门人"）。
#
# 四个任务（名字固定，卸载按这三个 + QMTF-Client-Start 逐个删）：
#   QMTF-Client-Ensure   工作日 08:35  → watchdog -Mode ClientEnsure   （判交易日、必要时拉起客户端）
#   QMTF-Client-Start    按需被调用    → 直接起 XtItClient.exe（/IT 交互式，客户端唯一合法拉起通道）
#   QMTF-Gateway-Ensure  每 5 分钟     → watchdog -Mode GatewayEnsure  （未监听才起，起前跑 profile-check）
#   QMTF-Close           每 5 分钟     → watchdog -Mode Close          （脚本内部 15:30 前直接 retry，任务不必做时段判断）
#
# 边界：本脚本只在**本机**注册/删除任务（B-N5：对广州零权限；不出现远程计算机名/PSRemoting）。
# 缺省 **只打印不注册**（dry-run），必须显式 -Apply——与 ensure_friend_gateway_config.ps1 同纪律。
#
# 用法：
#   .\install_tasks.ps1                 # 只打印将要注册的四条
#   .\install_tasks.ps1 -Apply          # 真注册
#   .\install_tasks.ps1 -Uninstall -Apply

param(
    [string]$GatewayDir  = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$Watchdog    = "",
    [string]$QmtExe      = "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe",
    [string]$PsExe       = "$env:WINDIR\System32\WindowsPowerShell\v1.0\powershell.exe",
    [string]$PythonExe   = "C:\Python312\python.exe",
    [switch]$Apply,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

if (-not $Watchdog) { $Watchdog = Join-Path $PSScriptRoot "watchdog.ps1" }
if (-not (Test-Path $Watchdog)) {
    Write-Host "TASKS_RESULT=ABORT reason=no_watchdog path=$Watchdog"
    exit 2
}

$T1 = "QMTF-Client-Ensure"
$T2 = "QMTF-Client-Start"
$T3 = "QMTF-Gateway-Ensure"
$T4 = "QMTF-Close"
# 第五条（11-05 补，owner 11-04 原话"每天收盘后这些规则自动更新一次"）：
# 内置策略自己不能更新自己（不落文件、不联网），所以更新动作必须由这台机器上的普通进程做。
# 15:40 是刻意选的：收盘结算与三份快照已落（15:00/15:30 那条关闭腿之后），且离次日 08:35 拉起还够远。
$T5 = "QMTF-Carrier-Update"

function Tr($mode) {
    return ('"{0}" -NoProfile -ExecutionPolicy Bypass -File "{1}" -Mode {2} -GatewayDir "{3}"' -f `
        $PsExe, $Watchdog, $mode, $GatewayDir)
}

if ($Uninstall) {
    foreach ($t in @($T1, $T2, $T3, $T4, $T5)) {
        if (-not $Apply) {
            Write-Host ("WOULD_DELETE task={0}" -f $t)
            continue
        }
        schtasks /Delete /TN $t /F | Out-Null
        Write-Host ("DELETED task={0}" -f $t)
    }
    Write-Host ("TASKS_RESULT={0}" -f $(if ($Apply) { "UNINSTALLED" } else { "DRYRUN" }))
    exit 0
}

# 五条注册。schtasks 的 /TR 里嵌套引号只能按这种写法转义，PS 5.1 没有更干净的等价物。
$jobs = @(
    @{ Name = $T1; Sc = "DAILY"; St = "08:35"; Tr = (Tr "ClientEnsure"); It = $true },
    @{ Name = $T2; Sc = "ONCE";  St = "23:59"; Tr = ('"{0}"' -f $QmtExe); It = $true },
    @{ Name = $T3; Sc = "MINUTE"; Mn = 5; Tr = (Tr "GatewayEnsure"); It = $false },
    @{ Name = $T4; Sc = "MINUTE"; Mn = 5; Tr = (Tr "Close"); It = $false },
    @{ Name = $T5; Sc = "DAILY"; St = "15:40"; It = $false;
       Tr = ('"{0}" "{1}" --once --config "{2}"' -f `
             $PythonExe, (Join-Path $GatewayDir "carrier_updater.py"), (Join-Path $GatewayDir "config.xt.json")) }
)

foreach ($j in $jobs) {
    if (-not $Apply) {
        Write-Host ("WOULD_CREATE task={0} sch={1} run={2}" -f $j.Name, $j.Sc, $j.Tr)
        continue
    }
    if ($j.Name -eq $T5) {
        if (-not (Test-Path $PythonExe)) {
            Write-Host "TASKS_RESULT=ABORT reason=no_python_for_carrier_update（注册了也是空跑：先装 Python 再重跑本脚本）"
            exit 2
        }
        if (-not (Test-Path (Join-Path $GatewayDir "carrier_updater.py"))) {
            Write-Host "TASKS_RESULT=ABORT reason=no_carrier_updater"
            exit 2
        }
    }
    if (-not (Test-Path $QmtExe) -and $j.Name -eq $T2) {
        Write-Host "TASKS_RESULT=ABORT reason=no_qmt_exe（QMT 客户端路径不对，注册了也是空跑；装填器应先修 -QmtExe）"
        exit 2
    }
    $args1 = @("/Create", "/TN", $j.Name, "/TR", $j.Tr, "/SC", $j.Sc)
    if ($j.Sc -eq "DAILY") { $args1 += @("/ST", $j.St) }
    if ($j.Sc -eq "ONCE") { $args1 += @("/ST", $j.St, "/SD", (Get-Date -Format "yyyy/MM/dd")) }
    if ($j.Sc -eq "MINUTE") { $args1 += @("/MO", $j.Mn) }
    if ($j.It) { $args1 += "/IT" }   # 交互式：客户端必须有桌面会话
    $args1 += "/F"
    & schtasks @args1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Host ("TASKS_RESULT=ABORT reason=create_failed task={0} exit={1}" -f $j.Name, $LASTEXITCODE)
        exit 2
    }
    Write-Host ("CREATED task={0}" -f $j.Name)
}

# 注册后复读自证：四个名字必须在 schtasks 查询里逐个出现（写了不注册＝没有，§164 那批的"写了没跑"同形）
if ($Apply) {
    $missing = @()
    foreach ($n in @($T1, $T2, $T3, $T4, $T5)) {
        $found = schtasks /Query /TN $n 2>$null
        if (-not $found) { $missing += $n }
    }
    if ($missing.Count -gt 0) {
        Write-Host ("TASKS_RESULT=ABORT reason=reread_missing tasks={0}" -f ($missing -join ","))
        exit 2
    }
    Write-Host "TASKS_REREAD_MATCH=1 tasks=5"
}

Write-Host ("TASKS_RESULT={0}" -f $(if ($Apply) { "INSTALLED" } else { "DRYRUN" }))
