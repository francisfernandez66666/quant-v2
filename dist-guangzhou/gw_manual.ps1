Remove-Item C:\opt\quant\gateway.out, C:\opt\quant\gateway.err -ErrorAction SilentlyContinue
Start-Process C:\Python312\python.exe -ArgumentList "C:\qmt\quant-trading-v2\qmt_gateway\gateway.py","-c","C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json" -RedirectStandardOutput C:\opt\quant\gateway.out -RedirectStandardError C:\opt\quant\gateway.err
Start-Sleep -Seconds 8
$gp = netstat -an | Where-Object { $_ -match ':8789 ' }
Write-Host ("gateway 8789 listening: " + $(if ($gp) { "YES" } else { "NO" }))
Write-Host "=== gateway.err ==="; Get-Content C:\opt\quant\gateway.err -Tail 20
Write-Host "=== gateway.out ==="; Get-Content C:\opt\quant\gateway.out -Tail 10
