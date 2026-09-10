# place_bridges.ps1 - copy new qmt_bridge.py to all candidate sandbox dirs (ASCII source, wildcard-safe)
$root = (Get-Item 'C:\Program Files (x86)\*QMT*' | Select-Object -First 1).FullName
Copy-Item 'C:\qmt\quant-trading-v2\qmt_gateway\qmt_bridge.py' (Join-Path $root 'python\qmt_bridge.py') -Force
$sp = Join-Path $root 'bin.x64\lib\site-packages'
if (Test-Path $sp) { Copy-Item 'C:\qmt\quant-trading-v2\qmt_gateway\qmt_bridge.py' $sp -Force; Write-Output 'copied to bin site-packages' } else { Write-Output ('site-packages absent: ' + $sp) }
Copy-Item 'C:\qmt\quant-trading-v2\qmt_gateway\qmt_bridge_strategy.py' (Join-Path $root 'python\新建策略文件.py') -Force
"placed:"
Get-ChildItem (Join-Path $root 'python') -Filter 'qmt_bridge*' | Select-Object Name, Length, LastWriteTime | Format-Table -AutoSize | Out-String -Width 140
