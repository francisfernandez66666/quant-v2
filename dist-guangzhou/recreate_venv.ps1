Stop-Process -Id 3956 -Force -ErrorAction SilentlyContinue
Stop-Process -Id 8068 -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
$venv = 'C:\opt\quant\venv'
if (Test-Path $venv) { Remove-Item $venv -Recurse -Force }
& C:\Python312\python.exe -m venv $venv 2>&1 | Select-Object -Last 3
& $venv\Scripts\python.exe -m pip install --quiet --upgrade pip 2>&1 | Select-Object -Last 2
& $venv\Scripts\python.exe -m pip install --quiet baostock pandas 2>&1 | Select-Object -Last 6
Write-Host "--- verify ---"
& $venv\Scripts\python.exe -c "import baostock, pandas; print('OK baostock', getattr(baostock,'__version__','?'), 'pandas', pandas.__version__)" 2>&1 | Select-Object -Last 3
