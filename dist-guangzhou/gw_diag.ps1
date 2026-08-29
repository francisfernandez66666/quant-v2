schtasks /End /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 2
schtasks /Run /TN "\qmt-gateway-wd" 2>&1
Start-Sleep -Seconds 6
$gp = netstat -an | Where-Object { $_ -match ':8789 ' }
Write-Host ("gateway 8789 listening: " + $(if ($gp) { "YES" } else { "NO" }))
Get-CimInstance Win32_Process -Filter "CommandLine like '%gateway.py%'" | ForEach-Object { Write-Host ($_.ProcessId.ToString() + " " + $_.CommandLine) }
$gwl = Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway\logs -Filter gateway*.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($gwl) { Write-Host ("=== log " + $gwl.FullName + " ==="); Get-Content $gwl.FullName -Tail 20 }
