$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'

# 1) 引擎改监听内网 127.0.0.1:8081（公网 8080 让给 Caddy 托管前端+反代）
& $nssm set quant AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=C:\var\lib\quant-trading-v2" "QUANT_ADDR=127.0.0.1:8081" "LLM_API_KEY=REPLACE_WITH_LLM_API_KEY" "LLM_API_URL=https://api.siliconflow.cn/v1/chat/completions" "LLM_MODEL=THUDM/GLM-Z1-9B-0414" 2>&1
& $nssm restart quant 2>&1 | Select-Object -Last 1
Start-Sleep -Seconds 4
Write-Host ("quant status: " + (Get-Service quant).Status)

# 2) 网关 report_url 指向内网引擎 8081（仅替换该串，避免破坏中文/格式）
$gw = 'C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json'
$raw = Get-Content $gw -Raw -Encoding UTF8
$new = $raw -replace 'http://127.0.0.1:8080', 'http://127.0.0.1:8081'
Set-Content $gw -Value $new -Encoding UTF8
Write-Host ("gateway report_url -> " + ([regex]::Match($new, 'http://127.0.0.1:8081').Value))

# 3) 重启网关任务
schtasks /End /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 2
schtasks /Run /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 3
Write-Host "gateway restarted"

# 4) 重启 Caddy（quant-web）加载新 Caddyfile（:8080 -> 8081）
& $nssm restart quant-web 2>&1 | Select-Object -Last 1
Start-Sleep -Seconds 4
Write-Host ("quant-web status: " + (Get-Service quant-web).Status)
