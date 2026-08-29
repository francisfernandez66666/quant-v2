$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
# 开放公网入站 8090
netsh advfirewall firewall add rule name="quant-web 8090" dir=in action=allow protocol=TCP localport=8090 2>&1 | Out-Null
Write-Host "firewall rule added"
# 注册 caddy 为服务
& $nssm install quant-web 'C:\opt\quant\caddy.exe' 2>&1 | Select-Object -Last 2
& $nssm set quant-web AppParameters "run --config C:\opt\quant\Caddyfile" 2>&1 | Out-Null
& $nssm set quant-web AppDirectory 'C:\opt\quant' 2>&1 | Out-Null
& $nssm set quant-web AppPriority NORMAL_PRIORITY_CLASS 2>&1 | Out-Null
& $nssm set quant-web Start SERVICE_AUTO_START 2>&1 | Out-Null
& $nssm set quant-web AppExit Default Restart 2>&1 | Out-Null
& $nssm set quant-web AppRotateFiles 1 2>&1 | Out-Null
& $nssm set quant-web AppRotateBytes 10485760 2>&1 | Out-Null
& $nssm restart quant-web 2>&1 | Select-Object -Last 2
Start-Sleep -Seconds 4
$svc = Get-Service quant-web -ErrorAction SilentlyContinue
Write-Host ("quant-web service: " + $svc.Status)
