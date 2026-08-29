Write-Host "=== search XtMiniQmt.exe (C:) ==="
$res = Get-ChildItem C:\ -Recurse -Filter XtMiniQmt.exe -ErrorAction SilentlyContinue | Select-Object -First 5 FullName
if ($res) { $res | ForEach-Object { Write-Host $_.FullName } } else { Write-Host "none found on C:" }
Write-Host "=== xt_path dir contents ==="
$xd = 'C:\Program Files (x86)\东莞证券QMT实盘交易端\userdata_mini'
if (Test-Path $xd) { Get-ChildItem $xd | Select-Object Name | ForEach-Object { Write-Host $_.Name } } else { Write-Host "xt_path missing" }
Write-Host "=== ensure_miniqmt.ps1 content ==="
if (Test-Path C:\opt\quant\qmt-win\ensure_miniqmt.ps1) { Get-Content C:\opt\quant\qmt-win\ensure_miniqmt.ps1 } else { Write-Host "missing" }
