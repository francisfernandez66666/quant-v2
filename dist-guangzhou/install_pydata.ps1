$venv = 'C:\opt\quant\venv'
& $venv\Scripts\python.exe -m pip install --quiet --upgrade pip 2>&1 | Select-Object -Last 2
& $venv\Scripts\python.exe -m pip install --quiet baostock pandas 2>&1 | Select-Object -Last 6
Write-Host "--- verify ---"
& $venv\Scripts\python.exe -c "import baostock, pandas; print('OK baostock', getattr(baostock,'__version__','?'), 'pandas', pandas.__version__)" 2>&1 | Select-Object -Last 3
