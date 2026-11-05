# switch_execution_mode.ps1 —— §CUTOVER-1105 桥腿 / 物理机本地执行器的**唯一**切换入口。
#
# 背景（owner 2026-11-05）：券商 11-05 的禁令没有在这台机器上生效，QMT 内置文件桥
# 目前仍然能读写、能下单，所以桥**保留**、一个字都不改。但"以后还行不行"没人保证，
# 桥真断的那天必须有一条能立刻跑的命令，把下单腿切到物理机本地执行器，而不是现场翻文档。
#
# 为什么必须有互锁：两条腿都会对同一批信号下单。若桥还在跑、本地执行器也起来了，
# 同一笔买入会被报两次 —— 这是本仓最不能犯的资金错误。因此：
#   * 执行器启动时读 execution_mode.json，非 "local" 一律拒绝碰柜台（local_executor.py main）；
#   * 切到 local 必须显式 -AckBridgeStopped（人工确认已在 QMT 里停掉内置策略）；
#   * 切回 bridge 时禁用计划任务并停掉残留的本地执行器进程；
#   * 任一步失败都恢复 mode 文件与任务状态，ABORT step/reason，不留半态。
#
# 纪律（同 §171）：缺省只预览；-Apply 才动手；-File 执行；不新增服务端路由；不碰桥文件。
# 用法：
#   .\switch_execution_mode.ps1                                  # 看当前模式与任务状态
#   .\switch_execution_mode.ps1 -Mode local                     # 预览切到物理机
#   .\switch_execution_mode.ps1 -Mode local -Apply -AckBridgeStopped -Reason "bridge died"
#   .\switch_execution_mode.ps1 -Mode bridge -Apply             # 切回桥

param(
    [ValidateSet("bridge", "local")][string]$Mode = "",
    [string]$ExecutorDir = "C:\qmt\quant-trading-v2\local_executor",
    [string]$TaskName = "QMTF-Exec-Ensure",
    [switch]$Apply,
    [switch]$AckBridgeStopped,
    [string]$Reason = ""
)

$ErrorActionPreference = "Stop"
$modeFile = Join-Path $ExecutorDir "execution_mode.json"

function Abort($step, $reason) {
    Write-Host ("SWITCH_RESULT=ABORT step={0} reason={1}" -f $step, $reason)
    exit 2
}

function Read-Mode {
    if (-not (Test-Path $modeFile)) { return "bridge" }
    try {
        $doc = Get-Content $modeFile -Raw -Encoding UTF8 | ConvertFrom-Json
    } catch {
        return "bridge"
    }
    if ($doc.mode -eq "local") { return "local" }
    return "bridge"
}

function Task-State {
    $q = schtasks /Query /TN $TaskName /FO CSV 2>$null | ConvertFrom-Csv
    if (-not $q) { return "absent" }
    # schtasks CSV status is localized; both spellings are accepted on purpose.
    if ($q.Status -match "Disabled|已禁用") { return "disabled" }
    return "enabled"
}

$prev = Read-Mode
$taskPrev = Task-State
Write-Host ("SWITCH_STATE mode={0} task={1} task_state={2} interlock={3}" -f `
    $prev, $TaskName, $taskPrev, $modeFile)

if (-not $Mode) {
    Write-Host "SWITCH_RESULT=SHOW"
    exit 0
}
if ($Mode -eq "local" -and $taskPrev -eq "absent") {
    Abort 1 "executor_task_absent (install the leg first: install_local_executor.ps1 -Apply)"
}
if ($Mode -eq "local" -and -not $AckBridgeStopped) {
    Abort 2 "bridge_must_be_stopped_first (stop the QMT built-in strategy, then pass -AckBridgeStopped)"
}
if (-not $Apply) {
    Write-Host ("SWITCH_RESULT=DRYRUN from={0} to={1} (pass -Apply to commit)" -f $prev, $Mode)
    exit 0
}
if ($Mode -eq $prev) {
    Write-Host ("SWITCH_RESULT=NOOP mode={0}" -f $Mode)
    exit 0
}

$backup = $null
if (Test-Path $modeFile) { $backup = Get-Content $modeFile -Raw -Encoding UTF8 }

try {
    if (-not (Test-Path $ExecutorDir)) {
        [System.IO.Directory]::CreateDirectory($ExecutorDir) | Out-Null
    }
    $doc = [ordered]@{
        mode = $Mode
        switched_at = (Get-Date).ToString("o")
        from = $prev
        reason = $Reason
    }
    $json = $doc | ConvertTo-Json -Compress
    [System.IO.File]::WriteAllText($modeFile, $json, (New-Object System.Text.UTF8Encoding($false)))
    if ((Read-Mode) -ne $Mode) { throw "mode_readback_mismatch" }

    # The scheduled runner is the only leg we own. Bridge leg is the QMT client's
    # built-in strategy and stays untouched by design (owner: keep it for now).
    if ($Mode -eq "local") {
        schtasks /Change /TN $TaskName /ENABLE | Out-Null
        if ((Task-State) -ne "enabled") { throw "task_enable_failed" }
    } else {
        schtasks /Change /TN $TaskName /DISABLE | Out-Null
        if ((Task-State) -ne "disabled") { throw "task_disable_failed" }
        # A disabled task does not stop a process that is already in its while-loop.
        # Killing only python processes whose command line names local_executor is
        # much narrower than "stop all python" (the box may run other strategies).
        $strays = Get-CimInstance Win32_Process -Filter "Name = 'python.exe'" |
                  Where-Object { $_.CommandLine -like "*local_executor*" }
        foreach ($p in $strays) {
            Write-Host ("STOPPING stray_pid={0}" -f $p.ProcessId)
            Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
        }
    }
} catch {
    # Roll BOTH halves back: a mode file saying local with the task still
    # disabled (or vice versa) is exactly the half-state that loses money.
    if ($backup -ne $null) {
        [System.IO.File]::WriteAllText($modeFile, $backup, (New-Object System.Text.UTF8Encoding($false)))
    } elseif (Test-Path $modeFile) {
        Remove-Item $modeFile -Force
    }
    if ($taskPrev -eq "enabled") {
        schtasks /Change /TN $TaskName /ENABLE | Out-Null
    } elseif ($taskPrev -eq "disabled") {
        schtasks /Change /TN $TaskName /DISABLE | Out-Null
    }
    Abort 3 ("rollback_ok error={0}" -f $_.Exception.Message)
}

Write-Host ("SWITCH_RESULT=OK from={0} to={1} task={2} reason={3}" -f `
    $prev, $Mode, (Task-State), $Reason)
