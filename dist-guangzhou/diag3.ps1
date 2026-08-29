Write-Host "=== listening ports (8080/8081/8090/8789) ==="
netstat -an | Where-Object { $_ -match 'LISTENING' -and ($_ -match ':8080|:8081|:8090|:8789') }
Write-Host "=== process command lines ==="
Get-CimInstance Win32_Process -Filter "Name='python.exe' OR Name='quant.exe' OR Name='caddy.exe'" | ForEach-Object {
  Write-Host ($_.ProcessId.ToString() + "  " + $_.Name + "  " + $_.CommandLine)
}
