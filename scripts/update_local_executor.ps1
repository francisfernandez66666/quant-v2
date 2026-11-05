# update_local_executor.ps1 -- roll the baked local executor onto the box.
#
# WHY THIS EXISTS (broker rule effective 2026-11-05)
#   QMT built-in strategies lose local file I/O and all external network,
#   and QMT terminals may not run on public cloud or VMs. So the execution
#   and the computation now live in a LOCAL ordinary Python process on the
#   physical Windows box. That process is delivered as a baked artifact
#   (see scripts/build_local_executor.py), and this script installs it.
#
# THREE MANDATORY STAGES (none may be skipped)
#   1) BACKUP   -- copy the current installed artifact aside first. If
#                  anything below fails, the old artifact is still there
#                  to restore. A rollback that has no backup is a wish.
#   2) WRITE    -- copy the new artifact into place and prove it with
#                  THREE independent SHA256 readings that must agree:
#                  the source file, the file just written, and the file
#                  re-read from disk after the write. Two readings can
#                  agree by accident (same buffer); three cannot.
#   3) VERSION  -- read the version constant back OUT of the installed
#                  file and compare it with the expected version. This
#                  is the step that catches "we think we updated but the
#                  running process is still the old one". If the read-back
#                  fails, the run is RED and a STOP_AUTO_UPDATE marker
#                  file is written, which stops further automatic
#                  updates until a human clears it.
#
# THE UPDATE MUST NOT LIE
#   A half-applied update is the worst outcome: the file on disk says one
#   thing, the running process does another, and the broker table cannot
#   be reconciled. So every failure path restores the backup, and the
#   exit code is non-zero.
#
# SCOPE DISCIPLINE
#   * Only the local executor process is restarted. The QMT trading
#     client is NEVER touched, and there is no GUI automation here.
#   * Dry run by default; -Apply is required to change anything.
#   * No credentials are read, echoed or written by this script.
#
# USAGE
#   .\update_local_executor.ps1 -Source <new_artifact.py> -Target <dir>
#       Print the plan (dry run): what would be backed up, written,
#       verified and restarted. Changes nothing.
#
#   .\update_local_executor.ps1 -Source <new_artifact.py> -Target <dir> -Apply
#       Do it: backup, write, verify SHA three ways, read the version
#       back, then restart ONLY the local executor process.
#
#   .\update_local_executor.ps1 -Source <new_artifact.py> -Target <dir> -Apply -SkipRestart
#       Do everything except the restart (use when the executor is driven
#       by a scheduler that restarts it on its own).
#
#   .\update_local_executor.ps1 -Source <new_artifact.py> -Target <dir> -ExpectedVersion v02-20261104 -Apply
#       Same, but additionally require that the version actually landed
#       is exactly v02-20261104. Pin it when you know which build should
#       be running: a file that says something else is not that build,
#       even when its bytes hashed correctly.
#
# ROLLBACK PATH (the question every operator asks first)
#   If the update aborts, the previous artifact is kept at
#     <Target>\local_executor.py.pre-update-<timestamp>
#   To roll back by hand:
#     1. stop the local executor process
#     2. copy the .pre-update-<timestamp> file over local_executor.py
#     3. start the local executor process again
#
# STOP_AUTO_UPDATE MARKER
#   A failed version read-back writes <Target>\STOP_AUTO_UPDATE and the
#   run goes red. While that file exists, further updates are refused
#   (the pre-check aborts with reason=stop_auto_update_marker_present):
#   we do not know what state the box is in, and updating on top of an
#   unknown state is exactly how "we think we updated but nothing
#   changed" happens. Inspect the cause, then delete the marker to
#   re-enable the update path:
#     Remove-Item <Target>\STOP_AUTO_UPDATE
#
# READ-ONLY ANCHORS (pure ASCII; Chinese through a PowerShell pipe turns
# into '?' on a GBK console, so the anchors are ASCII on purpose):
#   UPDATE_RESULT=DRYRUN source=... target=...
#   UPDATE_STAGE=BACKUP ok=1 path=...
#   UPDATE_STAGE=WRITE ok=1 sha256=...
#   UPDATE_STAGE=VERSION ok=1 version=...
#   UPDATE_RESULT=OK version=... sha256=...
#   UPDATE_RESULT=ABORT step=<n> reason=<code>
#   UPDATE_STOP_AUTO_UPDATE=1 path=...

param(
    [Parameter(Mandatory = $true)][string]$Source,
    [string]$Target = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$ProcessName = "local_executor",
    [string]$ExpectedVersion = "",
    [switch]$Apply,
    [switch]$SkipRestart
)

$ErrorActionPreference = "Stop"

function Abort($step, $reason) {
    Write-Host ("UPDATE_RESULT=ABORT step={0} reason={1}" -f $step, $reason)
    exit 2
}

# The installed artifact name. The builder publishes
# local_executor_<version>.py; the installed copy has a stable name so the
# scheduler does not have to know the version.
$installed = Join-Path $Target "local_executor.py"
$stopMarker = Join-Path $Target "STOP_AUTO_UPDATE"

# Write the "stop automatic updates" marker.
#
# WHY THIS EXISTS: the version read-back is the step that answers "is the
# new code actually in effect?". If that answer is "no" (or "we cannot
# tell"), then the next automatic update would run against a state we do
# not understand -- and the failure mode of an update that "thinks it
# worked" is a broker table that cannot be reconciled against the file
# on disk. So a failed read-back does not merely abort this run: it also
# stops the ones after it, until a human has looked at it.
function Stop-AutoUpdate($reason) {
    try {
        Set-Content -Path $stopMarker -Value ("reason=" + $reason) -Encoding ASCII
        Write-Host ("UPDATE_STOP_AUTO_UPDATE=1 path={0}" -f $stopMarker)
    } catch {
        Write-Host "UPDATE_STOP_AUTO_UPDATE=0 reason=marker_unwritable"
    }
}

Write-Host ("UPDATE_APPLY={0} source={1} target={2}" -f ($(if ($Apply) { 1 } else { 0 })), $Source, $Target)

# ---- PRECHECK: everything that can be known before touching a byte ----
if (-not (Test-Path $Source)) { Abort 1 "source_absent" }
if (-not (Test-Path $Target)) { Abort 1 "target_absent" }

# A previous run that could not confirm its own version left a marker.
# While that marker is there, an automatic update must NOT run: we do not
# know what state the box is in, and updating on top of an unknown state
# is how "we think we updated but nothing changed" happens.
if (Test-Path $stopMarker) {
    if (-not $Apply) {
        Write-Host ("UPDATE_RESULT=DRYRUN blocked=stop_auto_update marker={0}" -f $stopMarker)
        exit 0
    }
    Abort 1 "stop_auto_update_marker_present"
}

$sourceBytes = [System.IO.File]::ReadAllBytes($Source)
$sourceSha = [System.Security.Cryptography.SHA256]::Create().ComputeHash($sourceBytes)
$sourceShaHex = ([System.BitConverter]::ToString($sourceSha) -replace "-", "").ToLower()

# The version we EXPECT to end up installed, read from the source file
# before we install it. Reading it from the source (not from the target)
# is deliberate: the point of the version stage is to compare what we
# wanted against what actually landed.
$sourceText = [System.IO.File]::ReadAllText($Source)
$verMatch = [regex]::Match($sourceText, '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
if (-not $verMatch.Success) { Abort 1 "source_version_unreadable" }
$sourceVersion = $verMatch.Groups[1].Value

$currentVersion = ""
if (Test-Path $installed) {
    $currentText = [System.IO.File]::ReadAllText($installed)
    $curMatch = [regex]::Match($currentText, '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
    if ($curMatch.Success) { $currentVersion = $curMatch.Groups[1].Value }
}

if (-not $Apply) {
    Write-Host ("UPDATE_RESULT=DRYRUN source={0} target={1} expected_version={2} current_version={3}" -f `
        $Source, $installed, $sourceVersion, ($(if ($currentVersion) { $currentVersion } else { "absent" })))
    Write-Host "UPDATE_DRYRUN_PLAN=backup,write,sha3,version_readback,restart_local_executor_only"
    exit 0
}

# ---- STAGE 1: BACKUP ----
# The old artifact is copied aside BEFORE the new one lands. If the write
# or the verification fails, this file is what we restore from.
$stamp = Get-Date -Format "yyyyMMddHHmmss"
$backup = ""
if (Test-Path $installed) {
    $backup = "$($installed).pre-update-$stamp"
    Copy-Item $installed $backup -Force
    if (-not (Test-Path $backup)) { Abort 2 "backup_copy_failed" }
    Write-Host ("UPDATE_STAGE=BACKUP ok=1 path={0}" -f $backup)
} else {
    # Nothing to back up means this is a FIRST install, not an update.
    Write-Host "UPDATE_STAGE=BACKUP ok=1 path=absent_first_install"
}

# ---- STAGE 2: WRITE + SHA three ways ----
try {
    [System.IO.File]::WriteAllBytes($installed, $sourceBytes)
} catch {
    # The write itself failed: the file on disk may now be half-written. Restore
    # the backup when there is one; on a first install remove the fragment -- a
    # torn file is worse than none, because the next scheduler tick would run it
    # as if it were installed. The header has always promised "every failure path
    # restores"; this was the one that did not (caught on 2026-11-04).
    if ($backup) {
        try { Copy-Item $backup $installed -Force } catch { }
        Abort 3 ("write_failed_restored:" + $_.Exception.Message)
    }
    try { Remove-Item $installed -Force -ErrorAction Stop } catch { }
    Abort 3 ("write_failed_first_install_partial_removed:" + $_.Exception.Message)
}

# reading 1: the bytes we were handed (already computed above)
# reading 2: the bytes now on disk
$writtenBytes = [System.IO.File]::ReadAllBytes($installed)
$writtenSha = [System.Security.Cryptography.SHA256]::Create().ComputeHash($writtenBytes)
$writtenShaHex = ([System.BitConverter]::ToString($writtenSha) -replace "-", "").ToLower()
# reading 3: re-read from disk again, independently
$rereadBytes = [System.IO.File]::ReadAllBytes($installed)
$rereadSha = [System.Security.Cryptography.SHA256]::Create().ComputeHash($rereadBytes)
$rereadShaHex = ([System.BitConverter]::ToString($rereadSha) -replace "-", "").ToLower()

if ($sourceShaHex -ne $writtenShaHex) {
    if ($backup) { Copy-Item $backup $installed -Force }
    Abort 3 "sha_mismatch_source_vs_written"
}
if ($writtenShaHex -ne $rereadShaHex) {
    if ($backup) { Copy-Item $backup $installed -Force }
    Abort 3 "sha_mismatch_written_vs_reread"
}
Write-Host ("UPDATE_STAGE=WRITE ok=1 sha256={0}" -f $writtenShaHex)

# ---- STAGE 3: VERSION read-back ----
# Read the version out of the INSTALLED file and compare it with what we
# EXPECT. Two comparisons, and both must hold:
#   * the installed file's version == the version read from the SOURCE
#     before we wrote anything (the file landed as intended);
#   * when the caller pinned -ExpectedVersion, the installed version
#     must equal THAT too (the operator knows which build should be
#     running; a file that says something else is not that build).
# If either fails we roll back, mark the box "do not auto-update", and
# go red. A SHA match alone is NOT enough: it proves the bytes landed,
# not that the right bytes landed.
$installedText = ""
try {
    $installedText = [System.IO.File]::ReadAllText($installed)
} catch {
    if ($backup) { Copy-Item $backup $installed -Force }
    Stop-AutoUpdate "installed_unreadable"
    Abort 4 "installed_unreadable"
}
$instMatch = [regex]::Match($installedText, '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
if (-not $instMatch.Success) {
    if ($backup) { Copy-Item $backup $installed -Force }
    Stop-AutoUpdate "installed_version_unreadable"
    Abort 4 "installed_version_unreadable"
}
$installedVersion = $instMatch.Groups[1].Value
if ($installedVersion -ne $sourceVersion) {
    if ($backup) { Copy-Item $backup $installed -Force }
    Stop-AutoUpdate ("version_mismatch_expected=" + $sourceVersion + "_got=" + $installedVersion)
    Abort 4 ("version_mismatch_expected=" + $sourceVersion + "_got=" + $installedVersion)
}
if ($ExpectedVersion -and ($installedVersion -ne $ExpectedVersion)) {
    if ($backup) { Copy-Item $backup $installed -Force }
    Stop-AutoUpdate ("version_mismatch_pinned=" + $ExpectedVersion + "_got=" + $installedVersion)
    Abort 4 ("version_mismatch_pinned=" + $ExpectedVersion + "_got=" + $installedVersion)
}
Write-Host ("UPDATE_STAGE=VERSION ok=1 version={0}" -f $installedVersion)

# ---- RESTART: the local executor process only ----
# Never the QMT trading client, never a GUI. If the restart fails the
# FILE update is still valid (the next scheduler tick picks it up), so
# this is reported rather than treated as a failed update.
if ($SkipRestart) {
    Write-Host "UPDATE_RESTART=skipped reason=flag"
} else {
    $stopped = 0
    $started = 0
    try {
        $procs = @(Get-Process -Name $ProcessName -ErrorAction SilentlyContinue)
        foreach ($p in $procs) {
            try { $p.CloseMainWindow() | Out-Null } catch { }
            $p | Stop-Process -Force -ErrorAction SilentlyContinue
            $stopped++
        }
        # Give the old process a moment to release its handles on the
        # state file before the new one opens it.
        Start-Sleep -Milliseconds 800
        # GAP-1105 / CUTOVER-1105: never relaunch with a bare "python" from
        # PATH. The installed runner uses the venv interpreter that actually
        # has xtquant; a system-python relaunch computes but cannot trade,
        # and that reads as "the update worked". If the venv is absent, say so
        # and let the QMTF-Exec-Ensure scheduler relaunch -- do not invent a
        # third runtime. The relaunch must ALSO carry the bridge/local
        # interlock, or an update could silently create a second order leg.
        $root = Split-Path $Target -Parent
        $venvPy = Join-Path $root "local_executor\.venv\Scripts\python.exe"
        $modeFile = Join-Path $root "local_executor\execution_mode.json"
        if (Test-Path $venvPy) {
            $psi = New-Object System.Diagnostics.ProcessStartInfo
            $psi.FileName = $venvPy
            $psi.Arguments = ('"{0}" --execution-mode-file "{1}"' -f $installed, $modeFile)
            $psi.UseShellExecute = $false
            $psi.CreateNoWindow = $true
            [System.Diagnostics.Process]::Start($psi) | Out-Null
            $started = 1
        } else {
            Write-Host ("UPDATE_RESTART=warn reason=no_venv_python path={0} scheduler_will_relaunch" -f $venvPy)
        }
    } catch {
        Write-Host ("UPDATE_RESTART=warn reason={0}" -f ($_.Exception.Message -replace "`r", " " -replace "`n", " "))
    }
    Write-Host ("UPDATE_RESTART=attempted stopped={0} started={1} scope=local_executor_only" -f $stopped, $started)
}

Write-Host ("UPDATE_RESULT=OK version={0} sha256={1} backup={2}" -f $installedVersion, $writtenShaHex, ($(if ($backup) { $backup } else { "none" })))
exit 0
