$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'
foreach ($s in @('quant','quant-research','quant-gateway','pydata','quant-web')) {
  & $nssm stop $s 2>&1 | Out-Null
  Start-Sleep -Seconds 1
}
Start-Sleep -Seconds 2
$ts = Get-Date -Format 'yyyyMMddHHmmss'
$src = 'C:\var\lib\quant-trading-v2'
$info = Get-ChildItem $src -Recurse | Measure-Object -Property Length -Sum
Write-Host ("GZ data dir size MB: " + [math]::Round($info.Sum/1MB,1))
$tdb = Join-Path $src 'trading.db'
if (Test-Path $tdb) { Write-Host ("GZ trading.db MB: " + [math]::Round((Get-Item $tdb).Length/1MB,1)) }
$d = Get-PSDrive C
Write-Host ("C free GB: " + [math]::Round($d.Free/1GB,1))
$dst = "C:\var\lib\quant-trading-v2.bak.$ts.tgz"
tar czf $dst $src 2>&1 | Out-Null
if (-not (Test-Path $dst)) { Write-Host "BACKUP FAILED"; exit 1 }
Write-Host ("backup -> " + $dst + "  size MB: " + [math]::Round((Get-Item $dst).Length/1MB,1))
