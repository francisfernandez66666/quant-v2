$p = "C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json"
$c = Get-Content $p -Raw | ConvertFrom-Json
Write-Host ("BEFORE report_url=" + $c.report_url)
$c.report_url = "http://127.0.0.1:8080"
$c | ConvertTo-Json -Depth 6 | Set-Content $p
Write-Host ("AFTER  report_url=" + $c.report_url)
# restart gateway via its watchdog scheduled task
schtasks /End /TN "\qmt-gateway-wd" 2>$null
Start-Sleep 2
schtasks /Run /TN "\qmt-gateway-wd" 2>$null
Start-Sleep 4
Write-Host "gateway task restarted"
