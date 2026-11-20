# update_qmt_strategy.ps1 -- deliver the baked BUILT-IN strategy onto the physical box.
#
# WHY THIS EXISTS (carrier decision, owner 2026-11-04)
#   From 2026-11-05 the broker's QMT built-in strategy is the only automated
#   execution path still inside its own exemption ("signals produced entirely
#   inside QMT, no interaction with the outside world"). A built-in strategy
#   cannot read files and cannot reach the network -- so it also cannot be
#   updated by itself, and it cannot phone home. Updating it is therefore a job
#   for the ordinary local process on the same box, which is NOT inside the
#   sandbox and may use files and the network. This script is that job:
#
#       write the new strategy file  ->  restart the QMT client through the
#       existing scheduled task  ->  read the version back  ->  report to the
#       decision brain over HTTPS (the reporting leg lives in the gateway).
#
# THREE MANDATORY STAGES (same ruler as update_local_executor.ps1)
#   1) BACKUP -- the currently installed strategy file is copied aside first.
#   2) WRITE  -- the new bytes land, proven by THREE independent SHA256 readings
#                (source, just-written, re-read). Two can agree by accident.
#   3) VERSION -- the version constant is read OUT of the installed file and
#                 compared with the version the source claims. A SHA match does
#                 not prove the right bytes landed; this does. Failure writes a
#                 STOP_AUTO_UPDATE marker and further automatic updates are
#                 refused until a human clears it.
#
# THE CLIENT RESTART IS ORDERED, AND THE ORDER IS THE POINT (10-29 outage)
#   The gateway retries xtquant connections every 60s while the bridge has no
#   heartbeat; each connect() grabs a writer slot in the client's shared memory,
#   and a slot storm CRASHES the QMT client (measured: 713 consecutive failures,
#   client dying every 3-6 minutes). So:
#       stop the gateway  ->  stop the ensure task  ->  kill the client
#       ->  start the client THROUGH THE SCHEDULED TASK  ->  restart the gateway
#   Starting the client directly from inside an SSH/agent session is forbidden:
#   the child dies with the session (10-29). That is why the only start path here
#   is schtasks /Run, and why no line in this file launches XtItClient itself.
#
# WHAT THIS SCRIPT NEVER DOES
#   * no GUI automation, no screen reading, no emulator;
#   * no credentials read, echoed or written (the reporting leg owns those);
#   * nothing outside -QmtPythonDir and the backup folder beside it.
#
# USAGE
#   .\update_qmt_strategy.ps1 -Source <artifact.py> -QmtPythonDir <dir> -StrategyFile <name>
#       Dry run: print the plan, change nothing. -Apply is required to write.
#   ... -Apply
#       Backup, write, verify three ways, read the version back, restart in the
#       documented order.
#   ... -Apply -ExpectedVersion v02-20261105
#       Additionally require that the version which landed is exactly that one.
#   ... -Apply -SkipRestart
#       Deliver the file only (use when the client is being restarted by hand).
#
# ROLLBACK
#   The previous file stays at <QmtPythonDir>\<name>.pre-strategy-update-<ts>.
#   To roll back: stop the gateway, restore that file over the strategy file,
#   run the ensure task again, start the gateway.
#
# READ-ONLY ANCHORS (ASCII on purpose: a GBK console turns CJK into '?')
#   STRATEGY_UPDATE_RESULT=DRYRUN|OK|ABORT  step= reason=
#   STRATEGY_UPDATE_STAGE=BACKUP|WRITE|VERSION ok=1 ...
#   STRATEGY_UPDATE_RESTART=order=gateway_stop,ensure_stop,client_kill,task_run,gateway_start
#   STRATEGY_STOP_AUTO_UPDATE=1 path=...

param(
    [Parameter(Mandatory = $true)][string]$Source,
    [Parameter(Mandatory = $true)][string]$QmtPythonDir,
    [string]$StrategyFile = "local_strategy.py",
    # §CLIENT-REVIVE（2026-11-20 现场）：这里以前默认 `QMT-Ensure-Running` —— 那是 Windows
    # 时代运营机的任务名；朋友包/现网叫 `QMTF-Client-Ensure`。名字对不上时 `schtasks /Change`
    # 与 `/Run` 都会被 try/catch 吞成一行 warn，于是**客户端被本脚本杀掉后没人拉起**，
    # 而日志里只有 RESTART 那行看起来"attempted"（今天 17:03 投递 v146 就是这样丢了客户端）。
    # 新默认取现网那个名字；仍可由 carrier_ensure_task 覆盖。
    [string]$EnsureTaskName = "QMTF-Client-Ensure",
    [string]$GatewayProcessName = "python",
    [string]$GatewayScriptMatch = "gateway.py",
    [string]$ExpectedVersion = "",
    [switch]$Apply,
    [switch]$SkipRestart
)
# ENTER ANCHOR: first statement after param proves the script was actually
# parsed and run (2026-11-16: updater_rc_0 with ZERO output twice --
# parse-level silent death suspected; separates "never ran" from "died mid-way").
Write-Output "STRATEGY_UPDATE_ENTERED param_ok=1"

$ErrorActionPreference = "Stop"

# P1-1 (2026-11-17): a host-console write is invisible to
# subprocess(capture_output=True), so carrier_updater saw rc=0 with no RESULT anchor
# and reported a false "updater_rc_0" failure even though the file had landed.
# Write-Output puts every STRATEGY_UPDATE_* anchor on stdout where the updater reads it.
function Say($line) { Write-Output $line }
function Abort($step, $reason) {
    Say ("STRATEGY_UPDATE_RESULT=ABORT step={0} reason={1}" -f $step, $reason)
    exit 2
}

Say ("STRATEGY_UPDATE_APPLY={0} source={1} dir={2} file={3}" -f `
    ($(if ($Apply) { 1 } else { 0 })), $Source, $QmtPythonDir, $StrategyFile)

if (-not (Test-Path $QmtPythonDir)) { Abort 1 "qmt_python_dir_absent" }
if (-not (Test-Path $Source)) { Abort 1 "source_absent" }

$installed = Join-Path $QmtPythonDir $StrategyFile
$stopMarker = Join-Path $QmtPythonDir "STOP_AUTO_UPDATE"

function Stop-AutoUpdate($reason) {
    try {
        Set-Content -Path $stopMarker -Value ("reason=" + $reason) -Encoding ASCII
        Say ("STRATEGY_STOP_AUTO_UPDATE=1 path={0}" -f $stopMarker)
    } catch {
        Say "STRATEGY_STOP_AUTO_UPDATE=0 reason=marker_unwritable"
    }
}

# A previous run that could not confirm its own version left a marker. Updating
# on top of a state we do not understand is how "we think we shipped v12 and the
# client is running v07" happens, so refuse until a human clears it.
if (Test-Path $stopMarker) {
    if (-not $Apply) {
        Say ("STRATEGY_UPDATE_RESULT=DRYRUN blocked=stop_auto_update marker={0}" -f $stopMarker)
        exit 0
    }
    Abort 1 "stop_auto_update_marker_present"
}

$sourceBytes = [System.IO.File]::ReadAllBytes($Source)
$sourceSha = [System.BitConverter]::ToString(
    [System.Security.Cryptography.SHA256]::Create().ComputeHash($sourceBytes)) -replace "-", ""
$sourceText = [System.IO.File]::ReadAllText($Source)
$verMatch = [regex]::Match($sourceText, '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
if (-not $verMatch.Success) { Abort 1 "source_version_unreadable" }
$sourceVersion = $verMatch.Groups[1].Value

# A strategy file that never imports anything is the carrier's constraint, not a
# style choice: the sandbox tears down on a GBK-torn source, so a non-ASCII byte
# in the artifact means it will not load at all. Refuse it here rather than let
# the client discover it at 09:30.
$nonAscii = 0
foreach ($byte in $sourceBytes) { if ($byte -gt 127) { $nonAscii++ } }
if ($nonAscii -gt 0) { Abort 1 ("artifact_not_pure_ascii_n=" + $nonAscii) }

$currentVersion = ""
if (Test-Path $installed) {
    $curMatch = [regex]::Match([System.IO.File]::ReadAllText($installed),
        '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
    if ($curMatch.Success) { $currentVersion = $curMatch.Groups[1].Value }
}

if (-not $Apply) {
    Say ("STRATEGY_UPDATE_RESULT=DRYRUN expected_version={0} current_version={1} target={2}" -f `
        $sourceVersion, ($(if ($currentVersion) { $currentVersion } else { "absent" })), $installed)
    Say "STRATEGY_UPDATE_DRYRUN_PLAN=backup,write,sha3,version_readback,restart_in_documented_order"
    Say ("STRATEGY_UPDATE_RESTART=order=gateway_stop,ensure_stop,client_kill,task_run({0}),gateway_start" -f `
        $EnsureTaskName)
    exit 0
}

# ---- STAGE 1: BACKUP ----
$stamp = Get-Date -Format "yyyyMMddHHmmss"
$backup = ""
if (Test-Path $installed) {
    $backup = ("{0}.pre-strategy-update-{1}" -f $installed, $stamp)
    Copy-Item $installed $backup -Force
    if (-not (Test-Path $backup)) { Abort 2 "backup_copy_failed" }
    Say ("STRATEGY_UPDATE_STAGE=BACKUP ok=1 path={0}" -f $backup)
} else {
    Say "STRATEGY_UPDATE_STAGE=BACKUP ok=1 path=absent_first_install"
}

function Restore-Backup($step, $reason) {
    if ($backup) {
        try { Copy-Item $backup $installed -Force } catch { }
    }
    Abort $step $reason
}

# ---- STAGE 2: WRITE + SHA three ways ----
try {
    [System.IO.File]::WriteAllBytes($installed, $sourceBytes)
} catch {
    # The write itself failed: the previous file may be half-written. Restore if
    # we have a backup; if this was a first install, remove the partial file so
    # the client cannot load a torn strategy.
    if ($backup) {
        Restore-Backup 3 ("write_failed:" + $_.Exception.Message)
    }
    try { Remove-Item $installed -Force -ErrorAction Stop } catch { }
    Abort 3 ("write_failed_first_install_partial_removed:" + $_.Exception.Message)
}

$writtenSha = [System.BitConverter]::ToString(
    [System.Security.Cryptography.SHA256]::Create().ComputeHash(
        [System.IO.File]::ReadAllBytes($installed))) -replace "-", ""
$rereadSha = [System.BitConverter]::ToString(
    [System.Security.Cryptography.SHA256]::Create().ComputeHash(
        [System.IO.File]::ReadAllBytes($installed))) -replace "-", ""
if ($sourceSha -ne $writtenSha) { Restore-Backup 3 "sha_mismatch_source_vs_written" }
if ($writtenSha -ne $rereadSha) { Restore-Backup 3 "sha_mismatch_written_vs_reread" }
Say ("STRATEGY_UPDATE_STAGE=WRITE ok=1 sha256={0}" -f $writtenSha.ToLower())

# ---- STAGE 3: VERSION read-back ----
try {
    $installedText = [System.IO.File]::ReadAllText($installed)
} catch {
    Stop-AutoUpdate "installed_unreadable"
    Restore-Backup 4 "installed_unreadable"
}
$instMatch = [regex]::Match($installedText, '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
if (-not $instMatch.Success) {
    Stop-AutoUpdate "installed_version_unreadable"
    Restore-Backup 4 "installed_version_unreadable"
}
$installedVersion = $instMatch.Groups[1].Value
if ($installedVersion -ne $sourceVersion) {
    Stop-AutoUpdate ("version_mismatch_expected=" + $sourceVersion + "_got=" + $installedVersion)
    Restore-Backup 4 ("version_mismatch_source=" + $sourceVersion + "_installed=" + $installedVersion)
}
if ($ExpectedVersion -and ($installedVersion -ne $ExpectedVersion)) {
    Stop-AutoUpdate ("version_mismatch_pinned=" + $ExpectedVersion + "_got=" + $installedVersion)
    Restore-Backup 4 ("version_mismatch_pinned=" + $ExpectedVersion + "_got=" + $installedVersion)
}
Say ("STRATEGY_UPDATE_STAGE=VERSION ok=1 version={0}" -f $installedVersion)

# ---- RESTART: the documented order, and the order is the safety ----
# 1 stop the gateway: its retry loop would otherwise grab writer slots in the
#   client's shared memory while the client is coming back (10-29 slot storm).
#   §RESTART-FIX 20261116: match by COMMAND LINE (gateway.py), never by process
#   name "python" -- Stop-Process -Name python killed EVERY python.exe including
#   the carrier_updater process that was running THIS script, which is why the
#   updater output always died mid-flight (v39/v44/v46 all showed it) and the
#   client restart leg never ran. Exact-PID kill only.
# 2 kill the client.
# 3 start it THROUGH THE SCHEDULED TASK (never a direct launch: a child started
#   inside an SSH/agent session dies with the session).
#   §RESTART-FIX 20261116: the old DISABLE -> /Run -> ENABLE order guaranteed
#   failure -- schtasks /Run REFUSES a disabled task, so the client was never
#   relaunched while STRATEGY_UPDATE_RESULT=OK still reported success (three
#   live incidents 2026-11-16). ENABLE is now ensured BEFORE the /Run.
# 4 start the gateway again (same command-line match as step 1; the ensure
#   watchdog's GatewayEnsure leg brings it back within 5 minutes regardless).
if ($SkipRestart) {
    Say "STRATEGY_UPDATE_RESTART=skipped reason=flag"
} else {
    $log = @()
    try {
        $gwPids = @(Get-CimInstance Win32_Process -Filter "Name='python.exe'" |
            Where-Object { $_.CommandLine -match "gateway\.py" } |
            Select-Object -ExpandProperty ProcessId)
        foreach ($gwPid in $gwPids) {
            Stop-Process -Id $gwPid -Force -ErrorAction SilentlyContinue
        }
        $log += ("gateway_stop=attempted pids=" + ($gwPids -join "+"))
    } catch { $log += ("gateway_stop=warn:" + $_.Exception.Message) }
    try {
        schtasks /Change /TN $EnsureTaskName /ENABLE | Out-Null
        $log += "ensure_enabled=pre_run"
    } catch { $log += ("ensure_enabled=warn:" + $_.Exception.Message) }
    try {
        Stop-Process -Name "XtItClient" -ErrorAction SilentlyContinue -Force | Out-Null
        $log += "client_kill=attempted"
    } catch { $log += ("client_kill=warn:" + $_.Exception.Message) }
    Start-Sleep -Milliseconds 800
    try {
        schtasks /Run /TN $EnsureTaskName 2>&1 | Out-Null
        # ASCII-only anchor: NEVER echo localized schtasks output into the log --
        # the updater decodes this stream as UTF-8 and a GBK byte (0xCB) crashes
        # its reader thread (UnicodeDecodeError -> updater rc=1, 2026-11-16 live).
        $log += ("task_run=" + $(if ($LASTEXITCODE -eq 0) { "ok" } else { "rc$LASTEXITCODE" }))
    } catch { $log += ("task_run=warn:" + $_.Exception.Message) }
    Say ("STRATEGY_UPDATE_RESTART=" + ($log -join ","))
    Say "STRATEGY_UPDATE_RESTART_NOTE=gateway_start_is_the_ensure_tasks_job_or_the_operator"
}

Say ("STRATEGY_UPDATE_RESULT=OK version={0} sha256={1} backup={2}" -f `
    $installedVersion, $writtenSha.ToLower(), ($(if ($backup) { $backup } else { "none" })))
Say "STRATEGY_UPDATE_EFFECTIVE_READBACK=next_trade_day_from_order_remark_version"
exit 0
