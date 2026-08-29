# engine manual run to capture startup error
$env:QUANT_ADDR='127.0.0.1:8081'
$env:QUANT_DATA_DIR='C:\var\lib\quant-trading-v2'
$env:TZ='Asia/Shanghai'
$env:LLM_API_KEY='REPLACE_WITH_LLM_API_KEY'
$env:LLM_API_URL='https://api.siliconflow.cn/v1/chat/completions'
$env:LLM_MODEL='THUDM/GLM-Z1-9B-0414'
Remove-Item C:\opt\quant\quant.out, C:\opt\quant\quant.err -ErrorAction SilentlyContinue
Start-Process C:\opt\quant\quant.exe -RedirectStandardOutput C:\opt\quant\quant.out -RedirectStandardError C:\opt\quant\quant.err
Start-Sleep -Seconds 7
Write-Host "=== quant.out ==="; Get-Content C:\opt\quant\quant.out -Tail 25
Write-Host "=== quant.err ==="; Get-Content C:\opt\quant\quant.err -Tail 25
Get-Process quant | Stop-Process -Force -ErrorAction SilentlyContinue

# gateway task + log
Write-Host "=== gateway task ==="
schtasks /Query /TN "\qmt-gateway-wd" /FO LIST /V 2>$null | Select-Object -First 10
$gwl = Get-ChildItem C:\qmt\quant-trading-v2\qmt_gateway\logs -Filter gateway*.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if ($gwl) { Write-Host ("=== gateway log " + $gwl.FullName + " ==="); Get-Content $gwl.FullName -Tail 15 }
