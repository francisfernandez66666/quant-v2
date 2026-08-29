$p = Get-CimInstance Win32_Process -Filter "Name='python.exe'" | Where-Object { $_.CommandLine -match 'pip install' }
if ($p) { Write-Host "pip still running (pid $($p.ProcessId))" } else { Write-Host "pip done" }
& C:\opt\quant\venv\Scripts\python.exe -c "import baostock, pandas; print('OK baostock', getattr(baostock,'__version__','?'), 'pandas', pandas.__version__)" 2>&1 | Select-Object -Last 1
