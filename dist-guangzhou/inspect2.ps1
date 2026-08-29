Write-Host "=== scheduled tasks (qmt/gateway) ==="
schtasks /Query /FO LIST 2>$null | Select-String -Pattern "TaskName|qmt|gateway|QMT|Mini" | ForEach-Object { $_.Line }
Write-Host "=== python procs (pid + cmdline) ==="
Get-CimInstance Win32_Process -Filter "Name='python.exe'" | ForEach-Object { "PID=$($_.ProcessId) CMD=$($_.CommandLine)" }
Write-Host "=== qmt-gateway NSSM? ==="
& "C:\qmt\quant-trading-v2\qmt_gateway\tools\nssm-2.24\win64\nssm.exe" 2>$null
Get-Service qmt-gateway -ErrorAction SilentlyContinue | Format-Table Name,Status
