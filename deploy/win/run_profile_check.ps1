# run_profile_check.ps1 - run gateway.py --profile-check on the box with all
# streams captured to a file, so the verdict survives the SSH session teardown
# (foreground python over ssh gets killed with its stdout buffer - 2026-11-16).
$py = "C:\Users\57516\AppData\Local\Python\pythoncore-3.15-64\python.exe"
$gw = "C:\qmt\quant-trading-v2\qmt_gateway"
$out = "$gw\profile_check_result.txt"
if (Test-Path $out) { Remove-Item $out -Force }
& $py "$gw\gateway.py" --profile-check --config "$gw\config.xt.json" *>> $out
Write-Host "PROFILE_CHECK_RUN_DONE"
Get-Content $out
