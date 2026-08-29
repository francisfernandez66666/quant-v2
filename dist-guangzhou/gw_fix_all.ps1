$path = 'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json'
# read (auto-strips BOM), fix report_url, write back WITHOUT BOM
$raw = Get-Content $path -Raw -Encoding UTF8
$raw = $raw -replace 'http://127.0.0.1:8080', 'http://127.0.0.1:8081'
[System.IO.File]::WriteAllText($path, $raw, [System.Text.UTF8Encoding]::new($false))
Write-Host ("report_url now=" + ([regex]::Match($raw, 'http://127.0.0.1:8081').Value))

# kill any manual gateway
Get-CimInstance Win32_Process -Filter "CommandLine like '%gateway.py%'" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Seconds 2

$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
# register gateway as NSSM service (robust, no logged-on session needed for the process itself)
& $nssm install quant-gateway 'C:\Python312\python.exe' 2>&1 | Select-Object -Last 1
& $nssm set quant-gateway AppParameters "C:\qmt\quant-trading-v2\qmt_gateway\gateway.py -c C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json" 2>&1 | Out-Null
& $nssm set quant-gateway AppDirectory 'C:\qmt\quant-trading-v2\qmt_gateway' 2>&1 | Out-Null
& $nssm set quant-gateway Start SERVICE_AUTO_START 2>&1 | Out-Null
& $nssm set quant-gateway AppExit Default Restart 2>&1 | Out-Null
& $nssm set quant-gateway AppPriority NORMAL_PRIORITY_CLASS 2>&1 | Out-Null
# disable the old scheduled task to avoid port conflict
schtasks /Change /TN "\qmt-gateway-wd" /DISABLE 2>$null
& $nssm start quant-gateway 2>&1 | Select-Object -Last 1
Start-Sleep -Seconds 8
$gp = netstat -an | Where-Object { $_ -match ':8789 ' }
Write-Host ("gateway 8789 listening: " + $(if ($gp) { "YES" } else { "NO" }))
