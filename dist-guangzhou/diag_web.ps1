$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
Write-Host "--- nssm status quant-web ---"
& $nssm status quant-web 2>&1 | Select-Object -Last 5
Write-Host "--- sc query ---"
sc query quant-web 2>&1 | Select-Object -Last 6
Write-Host "--- caddy validate ---"
& C:\opt\quant\caddy.exe validate --config C:\opt\quant\Caddyfile 2>&1 | Select-Object -Last 12
