$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
Write-Host "=== quant AppEnvironmentExtra ==="
& $nssm get quant AppEnvironmentExtra 2>&1
Write-Host "=== gateway report_url (UTF-8 read) ==="
$raw = Get-Content 'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json' -Raw -Encoding UTF8
$cfg = $raw | ConvertFrom-Json
Write-Host ("report_url=" + $cfg.report_url)
Write-Host ("token=" + $cfg.token)
Write-Host "=== Windows firewall profiles ==="
netsh advfirewall show allprofiles state 2>&1 | Select-Object -Last 12
