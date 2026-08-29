$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
foreach ($s in @('quant','quant-research','quant-gateway','pydata','quant-web')) { & $nssm start $s 2>&1 | Select-Object -Last 1 }
Start-Sleep -Seconds 25
function code($u) { try { return (Invoke-WebRequest -Uri $u -UseBasicParsing -TimeoutSec 5).StatusCode } catch { return $_.Exception.Response.StatusCode.Value__ } }
Write-Host ("engine 8081: " + (code http://127.0.0.1:8081/api/health))
$hdr = @{ Authorization = 'Bearer REPLACE_WITH_QMT_REPORT_TOKEN' }
try { $r = Invoke-WebRequest -Uri http://127.0.0.1:8081/api/qmt/report -Method POST -ContentType 'application/json' -Headers $hdr -Body '{"type":"heartbeat","account":"2069008957","ts":0}' -UseBasicParsing; Write-Host ("report auth 8081: " + $r.StatusCode) } catch { Write-Host ("report auth 8081 err: " + $_.Exception.Response.StatusCode.Value__) }
Write-Host ("web 8080 /: " + (code http://127.0.0.1:8080/))
Write-Host ("web-proxy /api/health: " + (code http://127.0.0.1:8080/api/health))
try { $gw = (Invoke-WebRequest -Uri http://127.0.0.1:8789/health -UseBasicParsing -TimeoutSec 5).Content; Write-Host ("gateway /health: " + $gw) } catch { Write-Host "gateway ERR" }
