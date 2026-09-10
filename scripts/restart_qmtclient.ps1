# restart_qmtclient.ps1 - kill stale client, relaunch via qmtctl trading-session logic
Get-Process XtItClient -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 3
Get-Process XtMiniQmt -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 2
& C:\opt\quant\qmtctl.exe -dry
Write-Output "--- now real start ---"
& C:\opt\quant\qmtctl.exe
Write-Output "--- sleep 45 then check ---"
Start-Sleep -Seconds 45
Get-Process | Where-Object { $_.ProcessName -match '^Xt' } | Select-Object ProcessName, Id, StartTime, MainWindowTitle | Format-Table -AutoSize | Out-String -Width 200
