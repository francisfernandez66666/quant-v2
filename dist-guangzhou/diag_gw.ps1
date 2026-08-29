$gw = Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway -Recurse -Filter gateway*.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($gw) {
  Write-Host ("LOG: " + $gw.FullName + "  modified " + $gw.LastWriteTime)
  Get-Content $gw.FullName -Tail 22
} else {
  Write-Host "no gateway log found"
}
Write-Host "--- tasklist gateway ---"
tasklist | Where-Object { $_ -match "python|gateway" }
