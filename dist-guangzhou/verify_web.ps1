foreach ($p in @(8080, 8081, 8090, 8789)) {
  $l = netstat -an | Where-Object { $_ -match (":" + $p + " ") }
  Write-Host ("port $p listening: " + $(if ($l) { "YES" } else { "NO" }))
}
try { $r = Invoke-WebRequest -Uri http://127.0.0.1:8080/ -UseBasicParsing -TimeoutSec 8; Write-Host ("web root status=" + $r.StatusCode + " htmlBytes=" + $r.Content.Length) } catch { Write-Host ("web root err=" + $_.Exception.Message) }
try { $r2 = Invoke-WebRequest -Uri http://127.0.0.1:8080/api/health -UseBasicParsing -TimeoutSec 8; Write-Host ("web->/api/health status=" + $r2.StatusCode) } catch { Write-Host ("web->/api err=" + $_.Exception.Message) }
try { $r3 = Invoke-WebRequest -Uri http://127.0.0.1:8081/api/health -UseBasicParsing -TimeoutSec 8; Write-Host ("engine 8081 /api/health status=" + $r3.StatusCode) } catch { Write-Host ("engine 8081 err=" + $_.Exception.Message) }
$hdr = @{ Authorization = 'Bearer REPLACE_WITH_QMT_REPORT_TOKEN' }
try { $r4 = Invoke-WebRequest -Uri http://127.0.0.1:8081/api/qmt/report -Method POST -ContentType 'application/json' -Headers $hdr -Body '{"type":"heartbeat","account":"2069008957","ts":0}' -UseBasicParsing; Write-Host ("engine 8081 report auth status=" + $r4.StatusCode) } catch { Write-Host ("engine 8081 report err=" + $_.Exception.Response.StatusCode.Value__) }
