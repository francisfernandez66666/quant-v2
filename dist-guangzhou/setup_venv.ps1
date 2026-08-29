$venv = "C:\opt\quant\venv"
$py = "C:\Python312\python.exe"
if (-not (Test-Path "$venv\Scripts\python.exe")) {
    Write-Host "creating venv..."
    & $py -m venv $venv
} else {
    Write-Host "venv exists, skip create"
}
$pip = "$venv\Scripts\pip.exe"
Write-Host "pip install (baostock akshare pandas)..."
& $pip install --quiet -r C:\opt\quant\pydata\requirements.txt 2>&1 | Select-Object -Last 20
Write-Host "VENV_DONE exit=$LASTEXITCODE"
