$raw = Get-Content 'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json' -Raw -Encoding UTF8
$new = $raw -replace 'http://127.0.0.1:8080', 'http://127.0.0.1:8081'
Set-Content 'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json' -Value $new -Encoding UTF8
Write-Host ("done; report_url=" + ([regex]::Match($new, 'http://127.0.0.1:8081').Value))
schtasks /End /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 2
schtasks /Run /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 3
Write-Host "gateway restarted"
