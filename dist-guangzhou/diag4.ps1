$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
Write-Host "=== netstat for PID 9008 (quant) ==="
netstat -ano | Where-Object { $_ -match '9008' }
Write-Host "=== quant AppEnvironmentExtra ==="
& $nssm get quant AppEnvironmentExtra 2>&1
Write-Host "=== quant AppStdout/Stderr ==="
& $nssm get quant AppStdout 2>&1
& $nssm get quant AppStderr 2>&1
Write-Host "=== all caddy PIDs ==="
Get-CimInstance Win32_Process -Filter "Name='caddy.exe'" | ForEach-Object { Write-Host ($_.ProcessId.ToString() + "  " + $_.CommandLine) }
