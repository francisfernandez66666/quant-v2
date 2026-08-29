$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
Write-Host "=== nssm install (raw) ==="
& $nssm install quant-web 'C:\opt\quant\caddy.exe' 2>&1
Write-Host "=== exit: $LASTEXITCODE ==="
Write-Host "=== set AppParameters ==="
& $nssm set quant-web AppParameters "run --config C:\opt\quant\Caddyfile" 2>&1
& $nssm set quant-web AppDirectory 'C:\opt\quant' 2>&1
& $nssm set quant-web Start SERVICE_AUTO_START 2>&1
& $nssm set quant-web AppExit Default Restart 2>&1
Write-Host "=== nssm start ==="
& $nssm start quant-web 2>&1
Start-Sleep -Seconds 4
Write-Host "=== status ==="
& $nssm status quant-web 2>&1
