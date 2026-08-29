$roots = @(
  'C:\Program Files (x86)\东莞证券QMT实盘交易端',
  'C:\Program Files\东莞证券QMT实盘交易端',
  'C:\Program Files\QMT',
  'C:\QMT',
  'D:\QMT',
  'C:\Program Files (x86)\QMT'
)
foreach ($r in $roots) {
  if (-not (Test-Path $r)) { continue }
  Write-Host ("=== $r ===")
  Get-ChildItem $r -Recurse -Filter XtMiniQmt.exe -ErrorAction SilentlyContinue | ForEach-Object { Write-Host $_.FullName }
}
Write-Host "=== xt_path dir ==="
$xd = 'C:\Program Files (x86)\东莞证券QMT实盘交易端\userdata_mini'
if (Test-Path $xd) { Get-ChildItem $xd | Select-Object -First 20 Name | ForEach-Object { Write-Host $_.Name } } else { Write-Host "xt_path missing" }
Write-Host "=== ensure_miniqmt.ps1 ==="
if (Test-Path C:\opt\quant\qmt-win\ensure_miniqmt.ps1) { Get-Content C:\opt\quant\qmt-win\ensure_miniqmt.ps1 } else { Write-Host "missing" }
