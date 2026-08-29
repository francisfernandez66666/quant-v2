Write-Host "=== quant process ==="
Get-CimInstance Win32_Process -Filter "Name='quant.exe'" | Select-Object ProcessId,CommandLine | Format-List
Write-Host "=== curl 8080/setup ==="
try { (Invoke-WebRequest http://127.0.0.1:8080/setup -UseBasicParsing -TimeoutSec 5).StatusCode } catch { "ERR: " + $_.Exception.Message }
Write-Host "=== curl 8080/health ==="
try { (Invoke-WebRequest http://127.0.0.1:8080/health -UseBasicParsing -TimeoutSec 5).StatusCode } catch { "ERR: " + $_.Exception.Message }
