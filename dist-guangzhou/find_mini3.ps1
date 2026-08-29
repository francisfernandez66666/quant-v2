$cfg = Get-Content 'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json' -Raw | ConvertFrom-Json
$xp = $cfg.xt_path
Write-Host ("xt_path=" + $xp)
$cands = @(
  (Join-Path $xp 'bin\XtMiniQmt.exe'),
  (Join-Path $xp 'XtMiniQmt.exe'),
  (Join-Path $xp '..\bin\XtMiniQmt.exe'),
  (Join-Path $xp 'userdata_mini\bin\XtMiniQmt.exe')
)
foreach ($c in $cands) { if (Test-Path $c) { Write-Host ("FOUND: " + $c) } }
Write-Host "=== bounded search under Program Files (x86) ==="
Get-ChildItem 'C:\Program Files (x86)' -Recurse -Filter XtMiniQmt.exe -ErrorAction SilentlyContinue -Depth 6 | ForEach-Object { Write-Host ("SEARCH: " + $_.FullName) }
Write-Host "=== ensure_miniqmt.ps1 ==="
if (Test-Path C:\opt\quant\qmt-win\ensure_miniqmt.ps1) { Get-Content C:\opt\quant\qmt-win\ensure_miniqmt.ps1 } else { Write-Host "missing" }
