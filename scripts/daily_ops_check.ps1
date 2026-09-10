# daily_ops_check.ps1 - restore killer task + verify all 5 services + researchd activity (ASCII source)
"=== 1) QMT-Ensure-Running restore ==="
& powershell -NoProfile -ExecutionPolicy Bypass -File C:\qmt\enable_ensure.ps1

"=== 2) services health ==="
Get-Service quant,quant-research,pydata,quant-web,quant-gateway -ErrorAction SilentlyContinue |
  Select-Object Name, Status, StartType | Format-Table -AutoSize | Out-String -Width 120

"=== 3) probes ==="
foreach ($u in @('http://127.0.0.1:8080/api/status','http://127.0.0.1:9091/health','http://127.0.0.1:8787/pydata_status','http://127.0.0.1:8081/','http://127.0.0.1:8789/health')) {
  try { $r = Invoke-RestMethod -Uri $u -TimeoutSec 5; Write-Output ("OK   " + $u); } catch { Write-Output ("FAIL " + $u + " : " + $_.Exception.Message) }
}

"=== 4) researchd process + mem + latest activity ==="
Get-CimInstance Win32_Process -Filter "name='researchd.exe'" | ForEach-Object { $p=Get-Process -Id $_.ProcessId; Write-Output ("researchd pid=" + $_.ProcessId + " mem=" + [math]::Round($p.WorkingSet64/1MB) + "MB cpu=" + [math]::Round($p.TotalProcessorTime.TotalSeconds)) }
$ld = Get-ChildItem 'C:\var\lib\quant-trading-v2' -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.LastWriteTime -gt (Get-Date).AddMinutes(-10) } | Sort-Object LastWriteTime -Descending | Select-Object -First 5
if ($ld) { $ld | Select-Object FullName, LastWriteTime | Format-List | Out-String -Width 220 } else { Write-Output 'no recent writes in data dir (research may be idle)' }

"=== 5) quant.exe + model bridge state ==="
Get-CimInstance Win32_Process -Filter "name='quant.exe'" | ForEach-Object { Write-Output ("quant pid=" + $_.ProcessId) }
try { (Invoke-RestMethod -Uri 'http://127.0.0.1:8789/health' -TimeoutSec 5) | ConvertTo-Json -Depth 2 } catch { "gateway unreachable" }

"=== 6) watchdog/watchdog tasks ==="
schtasks /query /fo csv 2>$null | Select-String -Pattern 'quant-all-wd|QMT-Ensure|QMT-Gateway|qmt-gateway' | ForEach-Object { $_.Line }
