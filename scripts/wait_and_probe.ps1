# wait_and_probe.ps1 - check Xt processes, userdata_mini activity, then trader probe
Get-Process | Where-Object { $_.ProcessName -match '^Xt' } |
  Select-Object ProcessName, Id, StartTime, MainWindowTitle | Format-Table -AutoSize | Out-String -Width 200
$root = (Get-Item 'C:\Program Files (x86)\*QMT*' | Select-Object -First 1).FullName
$mini = Join-Path $root 'userdata_mini'
"=== userdata_mini recent writes (top 8) ==="
Get-ChildItem $mini -Recurse -File -ErrorAction SilentlyContinue |
  Sort-Object LastWriteTime -Descending | Select-Object -First 8 |
  Select-Object Name, LastWriteTime | Format-Table -AutoSize | Out-String -Width 160
"=== log dir tail ==="
Get-ChildItem (Join-Path $mini 'log') -ErrorAction SilentlyContinue |
  Sort-Object LastWriteTime -Descending | Select-Object -First 5 |
  Select-Object Name, Length, LastWriteTime | Format-Table -AutoSize | Out-String -Width 160
"=== probe ==="
& 'C:\Python312\python.exe' 'C:\qmt\probe_xtquant.py'
