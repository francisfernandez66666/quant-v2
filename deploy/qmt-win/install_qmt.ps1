# install_qmt.ps1 - silent-install QMT via scheduled task (bypass UAC prompt limitation of SSH sessions)
# All-ASCII on purpose: WinPS 5.1 misreads BOM-less UTF-8 Chinese as GBK and fails to parse.
$ErrorActionPreference = "Continue"

Write-Host "[1] kill stuck installer instance ..."
Get-Process qmt_installer -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 2

Write-Host "[2] create scheduled task (Administrator, highest privileges, hidden)..."
schtasks /Create /F /TN qmt-install /TR "C:\qmt\qmt_installer.exe /S" /SC ONCE /ST 23:59 /RU Administrator /RL HIGHEST | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "[fail] schtasks create failed ($LASTEXITCODE)"; exit 1 }

Write-Host "[3] start task ..."
schtasks /Run /TN qmt-install | Out-Null

Write-Host "[4] poll installer exit (max 12 min) ..."
$deadline = (Get-Date).AddMinutes(12)
while ((Get-Date) -lt $deadline) {
    Start-Sleep -Seconds 10
    if (-not (Get-Process qmt_installer -ErrorAction SilentlyContinue)) {
        Write-Host "[ok] installer exited"
        break
    }
}
if (Get-Process qmt_installer -ErrorAction SilentlyContinue) {
    Write-Host "[fail] still running after 12 min - fallback: manual install via RDP"
    exit 2
}

Write-Host "[5] cleanup task"
schtasks /Delete /TN qmt-install /F | Out-Null

Write-Host "[6] newest C-root dirs:"
Get-ChildItem C:\ -Directory | Sort-Object LastWriteTime -Descending |
    Select-Object -First 6 Name, LastWriteTime | Format-Table -AutoSize | Out-String -Width 120

Write-Host "[7] locate XtMiniQmt.exe / userdata_mini:"
$hits = Get-ChildItem C:\ -Directory -ErrorAction SilentlyContinue |
    ForEach-Object { Join-Path $_.FullName "bin.x64\XtMiniQmt.exe" } |
    Where-Object { Test-Path $_ }
if ($hits) {
    $hits | ForEach-Object {
        Write-Host "  EXE : $_"
        $ud = Join-Path (Split-Path (Split-Path $_) ) "userdata_mini"
        Write-Host ("  USERDATA_MINI exists: " + (Test-Path $ud) + " -> " + $ud)
    }
} else {
    Write-Host "  NOT_FOUND_LEVEL1"
}
