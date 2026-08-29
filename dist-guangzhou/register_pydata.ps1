$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
$pyExe = 'C:\opt\quant\venv\Scripts\python.exe'
$pyScript = 'C:\opt\quant\pydata\server.py'
& $nssm install pydata $pyExe $pyScript --host 127.0.0.1 --port 8787 2>&1 | Select-Object -Last 3
& $nssm set pydata AppDirectory 'C:\opt\quant\pydata' 2>&1 | Out-Null
& $nssm set pydata AppPriority BELOW_NORMAL_PRIORITY_CLASS 2>&1 | Out-Null
& $nssm set pydata AppRestartDelay 5000 2>&1 | Out-Null
& $nssm set pydata AppExit Default Restart 2>&1 | Out-Null
& $nssm set pydata Start SERVICE_AUTO_START 2>&1 | Out-Null
& $nssm set pydata AppEnvironmentExtra "TZ=Asia/Shanghai" 2>&1 | Out-Null
& $nssm restart pydata 2>&1 | Select-Object -Last 2
Start-Sleep -Seconds 3
$svc = Get-Service pydata -ErrorAction SilentlyContinue
Write-Host ("pydata service status: " + $svc.Status)
