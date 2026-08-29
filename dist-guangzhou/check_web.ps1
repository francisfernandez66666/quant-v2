$svc = Get-Service quant-web -ErrorAction SilentlyContinue
Write-Host ("status=" + $svc.Status)
$p = Get-Process caddy -ErrorAction SilentlyContinue
Write-Host ("caddy proc: " + $(if ($p) { $p.Id } else { "none" }))
$l = netstat -an | Where-Object { $_ -match ':8090 ' }
Write-Host ("8090 listening: " + $(if ($l) { "YES" } else { "NO" }))
