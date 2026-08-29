$f = Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway\logs -Filter gateway*.log | Sort-Object LastWriteTime -Descending | Select-Object -First 1
Write-Host ("LOG: " + $f.FullName + "  modified " + $f.LastWriteTime)
Get-Content $f.FullName -Tail 20
