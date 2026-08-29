function code($u) { try { return (Invoke-WebRequest -Uri $u -UseBasicParsing -TimeoutSec 5).StatusCode } catch { return $_.Exception.Response.StatusCode.Value__ } }
$h0 = code http://127.0.0.1:8080/
Write-Host ("web 8080 / : " + $h0)
try { $html = (Invoke-WebRequest -Uri http://127.0.0.1:8080/ -UseBasicParsing -TimeoutSec 5).Content; Write-Host ("web html len: " + $html.Length + " has /assets: " + ($html -match '/assets/')) } catch { Write-Host "web err" }
Write-Host ("web-proxy /api/health: " + (code http://127.0.0.1:8080/api/health))
Write-Host ("engine /api/health: " + (code http://127.0.0.1:8081/api/health))
try { $gw = (Invoke-WebRequest -Uri http://127.0.0.1:8789/health -UseBasicParsing -TimeoutSec 5).Content; Write-Host ("gateway /health: " + $gw) } catch { Write-Host ("gateway /health ERR") }
