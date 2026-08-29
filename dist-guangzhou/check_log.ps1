$dir = "C:\qmt\quant-trading-v2\qmt_gateway"
$log = Get-ChildItem $dir -Filter "gateway-*.log" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime | Select-Object -Last 1
if (-not $log) { $log = Get-ChildItem $dir -Filter "*.log" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime | Select-Object -Last 1 }
Write-Host ("log file: " + $log.FullName)
if ($log) {
    $lines = Get-Content $log.FullName -Tail 40
    $lines | ForEach-Object { Write-Host $_ }
    Write-Host "=== report POST status summary ==="
    ($lines | Select-String "report" | Select-String "401|403|200|fail|error" | ForEach-Object { $_.Line }) -join "`n"
}
