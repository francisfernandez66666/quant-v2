Write-Host "=== services ==="
Get-Service quant, quant-research, quant-web, pydata -ErrorAction SilentlyContinue | ForEach-Object { Write-Host ($_.Name + " = " + $_.Status) }
Write-Host "=== processes ==="
tasklist | Where-Object { $_ -match "quant|caddy|python" }
Write-Host "=== quant log tail ==="
if (Test-Path C:\var\lib\quant-trading-v2\quant.log) { Get-Content C:\var\lib\quant-trading-v2\quant.log -Tail 15 } else { Write-Host "no quant.log" }
$gwl = Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway\logs -Filter gateway*.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($gwl) { Write-Host ("=== gateway log: " + $gwl.FullName + " ==="); Get-Content $gwl.FullName -Tail 12 }
