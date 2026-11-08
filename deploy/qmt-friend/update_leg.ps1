# update_leg.ps1 - pull-channel updater for the friend-leg payload (2026-11-09).
#
# What it does:
#   1. fetch the published manifest from <BaseUrl>/leg/manifest.json
#   2. diff it against the local payload\MANIFEST.json (per-file SHA256)
#   3. default: dry-run, only print what WOULD change;  -Apply: backup + download
#      + verify SHA256 + swap, then refresh the local manifest copy.
#
# It ONLY syncs the payload directory. After -Apply, re-run install.ps1 -Apply to
# mirror into the runtime directory (C:\qmt\quant-trading-v2\qmt_gateway) and
# re-register tasks -- install.ps1 is idempotent by design.
#
# Anchors (pure ASCII, judged by operators/scripts):
#   REMOTE manifest commit=... files=NN contract_fp=...
#   DIFF changed=N remote_total=NN
#   WOULD_UPDATE path=...                    (dry-run per file)
#   UPDATED path=...                         (apply per file)
#   UPDATE_RESULT=DRYRUN changed=N
#   UPDATE_RESULT=OK changed=N backup=<dir>
#   UPDATE_RESULT=ABORT reason=<code>
param(
    [string]$BaseUrl  = "https://quant-trading.top",
    [string]$ToolsDir = "",
    [switch]$Apply
)
$ErrorActionPreference = "Stop"
try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch { }

if (-not $ToolsDir) { $ToolsDir = $PSScriptRoot }
$payload = Split-Path -Parent $ToolsDir
if (-not (Test-Path $payload)) { Write-Host "UPDATE_RESULT=ABORT reason=payload_dir_missing path=$payload"; exit 2 }
function Abort($reason) { Write-Host ("UPDATE_RESULT=ABORT reason=" + $reason); exit 2 }

# 1) remote manifest
$mfUrl = $BaseUrl + "/leg/MANIFEST.json"
try {
    $mf = Invoke-RestMethod -Uri $mfUrl -TimeoutSec 30
} catch {
    Abort ("manifest_fetch_failed: " + $_.Exception.Message)
}
if (-not $mf.files -or $mf.files.Count -eq 0) { Abort "manifest_no_files" }
Write-Host ("REMOTE manifest commit=" + $mf.build_commit + " files=" + $mf.files.Count + " contract_fp=" + $mf.contract_fp)

# 2) local manifest (absent/corrupt -> treat everything as changed)
$localMfPath = Join-Path $payload "MANIFEST.json"
$localByPath = @{}
if (Test-Path $localMfPath) {
    try {
        $lm = Get-Content -Raw -Encoding UTF8 $localMfPath | ConvertFrom-Json
        foreach ($f in $lm.files) { $localByPath[$f.path] = [string]$f.sha256 }
    } catch { }
}

# 3) diff
$change = @()
foreach ($f in $mf.files) {
    $dst = Join-Path $payload ($f.path -replace "/", "\")
    if (-not (Test-Path $dst)) { $change += ,$f; continue }
    $h = (Get-FileHash -Algorithm SHA256 $dst).Hash.ToLower()
    if ([string]$f.sha256 -ne "" -and $h -ne [string]$f.sha256) { $change += ,$f }
}
Write-Host ("DIFF changed=" + $change.Count + " remote_total=" + $mf.files.Count)

if (-not $Apply) {
    foreach ($f in $change) { Write-Host ("WOULD_UPDATE path=" + $f.path + " bytes=" + $f.bytes) }
    Write-Host ("UPDATE_RESULT=DRYRUN changed=" + $change.Count)
    exit 0
}

# 4) apply: backup -> download -> verify SHA256 -> swap
$stamp = Get-Date -Format "yyyyMMddHHmmss"
$backupDir = Join-Path $payload ("pre-update-" + $stamp)
$dl = New-Object System.Net.WebClient
$okN = 0
foreach ($f in $change) {
    $rel = $f.path
    $dst = Join-Path $payload ($rel -replace "/", "\")
    $url = $BaseUrl + "/leg/" + $rel
    try {
        $tmpF = $dst + ".downloading"
        $dl.DownloadFile($url, $tmpF)
        $h = (Get-FileHash -Algorithm SHA256 $tmpF).Hash.ToLower()
        if ([string]$f.sha256 -ne "" -and $h -ne [string]$f.sha256) { throw ("sha_mismatch got=" + $h) }
        $dir = Split-Path -Parent $dst
        if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
        if (Test-Path $dst) {
            $bk = Join-Path $backupDir $rel
            $bkP = Split-Path -Parent $bk
            if (-not (Test-Path $bkP)) { New-Item -ItemType Directory -Path $bkP -Force | Out-Null }
            Copy-Item $dst $bk -Force
        }
        Move-Item $tmpF $dst -Force
        $okN = $okN + 1
        Write-Host ("UPDATED path=" + $rel)
    } catch {
        if (Test-Path ($dst + ".downloading")) { Remove-Item ($dst + ".downloading") -Force -ErrorAction SilentlyContinue }
        Write-Host ("UPDATE_RESULT=ABORT reason=download_verify_fail path=" + $rel)
        Write-Host $_.Exception.Message
        exit 2
    }
}
# 5) refresh the local manifest copy so the next diff is clean
try { $dl.DownloadFile($mfUrl, $localMfPath) } catch { }
Write-Host ("UPDATE_RESULT=OK changed=" + $okN + " backup=" + $backupDir)
Write-Host "NEXT re-run install.ps1 -Apply to mirror into the runtime directory"
