Write-Host "=== venv2 install log tail ==="
if (Test-Path C:\opt\quant\venv2.log) { Get-Content C:\opt\quant\venv2.log -Tail 8 } else { Write-Host "no venv2.log" }
Write-Host "=== venv Scripts/python.exe exists? ==="
if (Test-Path C:\opt\quant\venv\Scripts\python.exe) { Write-Host "YES venv/Scripts/python.exe" } else { Write-Host "NO venv/Scripts/python.exe" }
if (Test-Path C:\opt\quant\venv\python.exe) { Write-Host "YES venv/python.exe (old path)" } else { Write-Host "NO venv/python.exe" }
Write-Host "=== baostock importable? ==="
& C:\opt\quant\venv\Scripts\python.exe -c "import baostock, pandas; print('baostock+ pandas OK', baostock.__version__ if hasattr(baostock,'__version__') else '')" 2>&1
