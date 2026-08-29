Stop-Process -Id 4088 -Force -ErrorAction SilentlyContinue
Stop-Process -Id 7880 -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
$venv = 'C:\opt\quant\venv'
& $venv\Scripts\python.exe -m pip install --quiet --upgrade pip -i https://pypi.tuna.tsinghua.edu.cn/simple 2>&1 | Select-Object -Last 2
& $venv\Scripts\python.exe -m pip install --quiet baostock pandas -i https://pypi.tuna.tsinghua.edu.cn/simple 2>&1 | Select-Object -Last 6
Write-Host "--- verify ---"
& $venv\Scripts\python.exe -c "import baostock, pandas; print('OK baostock', getattr(baostock,'__version__','?'), 'pandas', pandas.__version__)" 2>&1 | Select-Object -Last 1
