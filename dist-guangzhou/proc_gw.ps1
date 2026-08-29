Write-Host "=== processes (qmt/xt/mini/xiadan/userdata) ==="
tasklist | Where-Object { $_ -match "mini|xt|qmt|xiadan|userdata|Quant|xtquant" }
Write-Host "=== qmtctl task ==="
schtasks /Query /TN "QMT-Ensure-Running" /FO LIST 2>$null
Write-Host "=== active session check (qmtctl gate) ==="
$q = Get-ChildItem C:\opt\quant\qmtctl.exe -ErrorAction SilentlyContinue
if ($q) { & C:\opt\quant\qmtctl.exe gate 2>&1 } else { Write-Host "qmtctl not found" }
