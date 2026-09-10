# state_now.ps1 - post-curfew state (ASCII source)
Get-Process | Where-Object { $_.ProcessName -match '^Xt|miniquote' } | Select-Object ProcessName, Id, StartTime | Format-Table -AutoSize | Out-String -Width 160
"=== boot log tail ==="
if (Test-Path 'C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log') { Get-Content 'C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log' -Tail 12 | Out-String -Width 200 } else { 'boot log absent' }
