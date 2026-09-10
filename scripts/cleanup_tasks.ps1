# cleanup_tasks.ps1 - remove all debug one-shot tasks, keep production ones (ASCII source)
$keep = @('QMT-Ensure-Running','QMT-Gateway-Ensure','QMT-Gateway-Logon')
Get-ChildItem 'C:\Windows\System32\Tasks' | Where-Object { $_.Name -match '^(qmt-|QMT-)' } | ForEach-Object {
  if ($keep -notcontains $_.Name) {
    schtasks /delete /tn $_.Name /f 2>&1 | Out-Null
    Write-Output ("deleted " + $_.Name)
  }
}
Write-Output 'remaining:'
schtasks /query /fo csv 2>$null | Select-String -Pattern '"\\(QMT|qmt)' | ForEach-Object { $_.Line.Split(',')[0] }
