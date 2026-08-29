$pip = "C:\opt\quant\venv\Scripts\pip.exe"
Write-Host "pip install baostock pandas (skip heavy akshare for now)..."
& $pip install --quiet baostock pandas 2>&1 | Select-Object -Last 10
Write-Host "BAOSTOCK_DONE exit=$LASTEXITCODE"
