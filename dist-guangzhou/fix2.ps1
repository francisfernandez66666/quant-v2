$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
& $nssm stop quant 2>&1 | Out-Null
Start-Sleep -Seconds 2
& $nssm start quant 2>&1
Start-Sleep -Seconds 15
$ep = netstat -an | Where-Object { $_ -match ':8081 ' }
Write-Host ("engine 8081 listening: " + $(if ($ep) { "YES" } else { "NO" }))
$hdr = @{ Authorization = 'Bearer REPLACE_WITH_QMT_REPORT_TOKEN' }
try { $r = Invoke-WebRequest -Uri http://127.0.0.1:8081/api/qmt/report -Method POST -ContentType 'application/json' -Headers $hdr -Body '{"type":"heartbeat","account":"2069008957","ts":0}' -UseBasicParsing; Write-Host ("report auth 8081: " + $r.StatusCode) } catch { Write-Host ("report auth 8081 err: " + $_.Exception.Response.StatusCode.Value__) }

$gwl = Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway\logs -Filter gateway*.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($gwl) { Write-Host ("=== gateway log " + $gwl.FullName + " ==="); Get-Content $gwl.FullName -Tail 18 }
