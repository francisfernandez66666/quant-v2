try {
  $r = Invoke-WebRequest -Uri http://127.0.0.1:8787/ -UseBasicParsing -TimeoutSec 8
  Write-Host ("pydata / status=" + $r.StatusCode)
} catch {
  Write-Host ("pydata / err=" + $_.Exception.Message)
}
try {
  $r2 = Invoke-WebRequest -Uri http://127.0.0.1:8787/health -UseBasicParsing -TimeoutSec 8
  Write-Host ("pydata /health status=" + $r2.StatusCode + " body=" + $r2.Content)
} catch {
  Write-Host ("pydata /health err=" + $_.Exception.Message)
}
