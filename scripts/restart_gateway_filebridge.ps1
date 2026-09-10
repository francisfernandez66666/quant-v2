# restart_gateway_filebridge.ps1 - kill old gateway, restart via interactive-session ensure task
Get-CimInstance Win32_Process -Filter "name='python.exe'" |
  Where-Object { $_.CommandLine -match 'gateway.py' } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force; Write-Output ("killed gateway " + $_.ProcessId) }
Start-Sleep -Seconds 2
schtasks /run /tn "QMT-Gateway-Ensure"
Start-Sleep -Seconds 12
Get-CimInstance Win32_Process -Filter "name='python.exe'" |
  Where-Object { $_.CommandLine -match 'gateway.py' } |
  ForEach-Object {
    $p = Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue
    Write-Output ("gateway pid=" + $_.ProcessId + " session=" + $(if ($p) { $p.SessionId }))
  }
try { (Invoke-RestMethod -Uri 'http://127.0.0.1:8789/health' -TimeoutSec 5) | ConvertTo-Json -Depth 2 } catch { "health pending" }
