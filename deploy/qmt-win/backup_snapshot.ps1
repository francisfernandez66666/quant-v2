# backup_snapshot.ps1 — Guangzhou nightly consistent snapshot producer (HARDENING item 1, pull model)
# Role: at off-hours, produce a transactionally-consistent snapshot of trading.db (SQLite .backup API,
#       safe under concurrent writers) + copy the small JSON state/config files into a staging folder.
#       This staging folder is what the Mac dev machine rsync-pulls and feeds into restic.
#       We do NOT push from Guangzhou (NAT); we only prepare files for the Mac to pull.
# Install (SYSTEM scheduled task, daily 04:00 Beijing, survives RDP/SSH disconnect):
#   schtasks /Create /F /TN quant-backup-snap /SC DAILY /ST 04:00 /RU SYSTEM /RL HIGHEST ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\deploy\qmt-win\backup_snapshot.ps1"
#   schtasks /Run /TN quant-backup-snap
# Keep file ASCII-only (PS5.1 reads non-BOM UTF-8 as GBK -> ParserError on CJK).
$ErrorActionPreference = "Stop"

$DataDir  = "C:\var\lib\quant-trading-v2"
$Deploy   = "C:\opt\quant"
$SnapRoot = "C:\var\lib\quant-snapshot"      # pull target; overwritten each night
$Log      = "C:\var\lib\quant-snapshot\_snapshot.log"

# python with sqlite3 3.49 (verified on gz); resolve absolute path so SYSTEM-task PATH quirks can't break it.
$PythonCmd = Get-Command python -ErrorAction SilentlyContinue
if ($PythonCmd) { $Python = $PythonCmd.Source } else { $Python = "C:\Python312\python.exe" }

function Log($m) { $line = (Get-Date -Format "yyyy-MM-dd HH:mm:ss") + " " + $m; Add-Content -Path $Log -Value $line; Write-Host $line }

New-Item -ItemType Directory -Force -Path $SnapRoot | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path $Log) | Out-Null

try {
    Log "=== snapshot start ==="

    # 0) Disk guard: snapshot (~5GB) + relay repo need headroom on C:. Refuse if <8GB free.
    $c = Get-PSDrive -Name C
    if ($c.Free -lt 8GB) { throw ("C: free space " + [math]::Round($c.Free/1GB,1) + "GB < 8GB guard") }

    # 1+2) Consistent single-file snapshot + integrity_check via SQLite backup API (python stdlib,
    #      safe while engine keeps writing). Logic lives in backup_snap.py next to this script.
    $snapPy = Join-Path $PSScriptRoot "backup_snap.py"
    if (!(Test-Path $snapPy)) { throw "backup_snap.py missing at $snapPy" }
    $out = & $Python $snapPy 2>&1
    if ($LASTEXITCODE -ne 0) { throw "backup_snap.py exit=$LASTEXITCODE : $out" }
    Log ("backup_snap: $out")
    $dbDst = Join-Path $SnapRoot "trading.db"

    # 3) Copy small mutable state/config files (skip WAL of live db; snapshot db is standalone).
    $copyItems = @(
        "config.json",
        "auth.json",
        "grayscale_rules.json",
        "applied_factors.json",
        "applied_patterns.json",
        "paper.json",
        "portfolio.json",
        "research_state.json",
        "news_tracker.json"
    )
    $stateDir = Join-Path $SnapRoot "state"
    New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
    foreach ($f in $copyItems) {
        $p = Join-Path $DataDir $f
        if (Test-Path $p) { Copy-Item $p (Join-Path $stateDir $f) -Force }
    }

    # 4) Copy deploy scripts/config (small, for rebuild reference; NOT binaries).
    $deployDir = Join-Path $SnapRoot "deploy"
    New-Item -ItemType Directory -Force -Path $deployDir | Out-Null
    if (Test-Path "$Deploy\deploy\qmt-win") { Copy-Item "$Deploy\deploy\qmt-win" $deployDir -Recurse -Force }
    if (Test-Path "$Deploy\Caddyfile") { Copy-Item "$Deploy\Caddyfile" $deployDir -Force }
    if (Test-Path "$Deploy\config.json") { Copy-Item "$Deploy\config.json" $deployDir -Force }

    # 5) Restic backup into the LOCAL relay repo (fast, disk-only; the Mac pulls chunk deltas
    #    via `restic copy` over sftp — that direction is Mac-initiated, so no inbound to Mac needed).
    $Restic  = "C:\opt\quant\tools\restic.exe"
    $Pass    = "C:\opt\quant\tools\restic-pass.txt"
    $RepoDir = "C:\var\lib\quant-restic-repo"
    if (!(Test-Path $Restic)) { throw "restic.exe missing at $Restic" }
    if (!(Test-Path $Pass))   { throw "restic-pass.txt missing at $Pass" }
    $env:RESTIC_PASSWORD_FILE = $Pass
    & $Restic backup -r $RepoDir $SnapRoot --tag nightly 2>&1 | ForEach-Object { Log ("restic: " + $_) }
    if ($LASTEXITCODE -ne 0) { throw "restic backup exit=$LASTEXITCODE" }
    # Transient relay retention (Mac keeps the long history): 3 days + 2 weeks.
    & $Restic forget --repo $RepoDir --keep-daily 3 --keep-weekly 2 --prune 2>&1 | ForEach-Object { Log ("restic-forget: " + $_) }
    Remove-Item Env:RESTIC_PASSWORD_FILE

    # 6) Marker for the Mac puller: freshness + size + integrity summary.
    $dbSize = (Get-Item $dbDst).Length
    $marker = [ordered]@{
        ok         = $true
        ts         = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss")
        db_bytes   = $dbSize
        integrity  = "ok"
        repo       = "C:/var/lib/quant-restic-repo"
    }
    ($marker | ConvertTo-Json -Compress) | Set-Content -Path (Join-Path $SnapRoot "SNAPSHOT_OK") -Encoding ascii -NoNewline
    Log ("=== snapshot+restic done db=" + $dbSize + " ===")
    exit 0
}
catch {
    Log ("ERROR: " + $_.Exception.Message)
    $bad = [ordered]@{ ok=$false; ts=(Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"); err=$_.Exception.Message }
    ($bad | ConvertTo-Json -Compress) | Set-Content -Path (Join-Path $SnapRoot "SNAPSHOT_OK") -Encoding ascii -NoNewline
    exit 1
}
