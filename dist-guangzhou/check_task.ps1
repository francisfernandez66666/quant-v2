$t = Get-ScheduledTask -TaskName 'QMT-Ensure-Running' -ErrorAction SilentlyContinue
if ($t) {
  $t.Actions | ForEach-Object { Write-Host ("ACTION: " + $_.Execute + " " + $_.Arguments) }
  Write-Host ("LOGON: " + $t.Principal.LogonType)
} else {
  Write-Host "task missing"
}
