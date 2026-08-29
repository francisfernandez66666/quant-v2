$setup = Invoke-RestMethod -Uri http://127.0.0.1:8080/setup -Method POST -ContentType 'application/json' -InFile C:\opt\quant\qmt-win\setup.json
Write-Host ("SETUP: " + ($setup | ConvertTo-Json -Compress))
$login = Invoke-RestMethod -Uri http://127.0.0.1:8080/auth/login -Method POST -ContentType 'application/json' -InFile C:\opt\quant\qmt-win\login.json
$tok = $login.token
Write-Host ("LOGIN token len=" + $tok.Length)
$hdr = @{ Authorization = 'Bearer ' + $tok }
$qmt = Invoke-RestMethod -Uri http://127.0.0.1:8080/api/config/qmt -Method POST -ContentType 'application/json' -Headers $hdr -InFile C:\opt\quant\qmt-win\qmt.json
Write-Host ("QMT config set: " + ($qmt | ConvertTo-Json -Compress))
