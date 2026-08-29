$mini = 'C:\Program Files (x86)\东莞证券QMT实盘交易端\userdata_mini\bin\XtMiniQmt.exe'
if (Test-Path $mini) { Write-Host ("MINI_FOUND: " + $mini) } else { Write-Host "MINI_MISSING" }
# show what the scheduled task actually runs
schtasks /Query /TN "QMT-Ensure-Running" /FO LIST /V 2>$null | Where-Object { $_ -match "Task To Run|Run As|Next Run Time|Author" }
Write-Host "--- qmtctl binaries present? ---"
if (Test-Path C:\opt\quant\qmtctl.exe) { Write-Host "qmtctl.exe OK" } else { Write-Host "qmtctl.exe MISSING" }
if (Test-Path C:\opt\quant\qmt-win\ensure_miniqmt.ps1) { Write-Host "ensure_miniqmt.ps1 OK" } else { Write-Host "ensure_miniqmt.ps1 MISSING" }
