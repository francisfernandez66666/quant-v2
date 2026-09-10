# enable_ensure.ps1 - re-enable the killer task + trigger it (ASCII source)
schtasks /change /tn "QMT-Ensure-Running" /enable | Out-Null
schtasks /run /tn "QMT-Ensure-Running" | Out-Null
Write-Output 'QMT-Ensure-Running enabled+triggered'
