$login = Invoke-RestMethod -Uri http://127.0.0.1:8080/auth/login -Method POST -ContentType 'application/json' -InFile C:\opt\quant\qmt-win\login.json
$hdr = @{ Authorization = 'Bearer ' + $login.token }
$r = Invoke-RestMethod -Uri http://127.0.0.1:8080/api/config/qmt -Headers $hdr
Write-Host ("ENGINE qmt config: " + ($r | ConvertTo-Json -Depth 6))
Write-Host ("engine users: " + ($login.token.Length))
