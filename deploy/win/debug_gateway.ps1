# debug_gateway.ps1 - inject friend.env (as start_gateway.ps1 does), then start
# the real gateway detached with stderr captured; report port + error tail.
$gw = "C:\qmt\quant-trading-v2\qmt_gateway"
$py = "C:\Users\57516\AppData\Local\Python\pythoncore-3.15-64\python.exe"
foreach ($ln in (Get-Content "$gw\friend.env")) {
    if ($ln -match "^QUANT_GATEWAY_BIND=(.+)$") { $env:QUANT_GATEWAY_BIND = $Matches[1].Trim() }
    if ($ln -match "^ALLOWED_IPS=(.+)$")        { $env:ALLOWED_IPS = $Matches[1].Trim() }
}
Start-Process -FilePath $py `
  -ArgumentList @("$gw\gateway.py", "--config", "$gw\config.xt.json") `
  -RedirectStandardOutput "$gw\gw_run_out.txt" `
  -RedirectStandardError "$gw\gw_run_err.txt" `
  -WindowStyle Hidden
Start-Sleep -Seconds 12
Write-Host "--- netstat 8789 ---"
netstat -ano | findstr :8789
Write-Host "--- gw_run_err tail ---"
Get-Content "$gw\gw_run_err.txt" -Tail 25
