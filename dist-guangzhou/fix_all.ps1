$nssm = 'C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe'

# 1) kill all stragglers
Stop-Process -Name caddy -Force -ErrorAction SilentlyContinue
Stop-Process -Name quant -Force -ErrorAction SilentlyContinue
Get-CimInstance Win32_Process -Filter "CommandLine like '%gateway.py%'" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Seconds 3
Write-Host "stragglers killed"

# 2) set engine env (internal 8081)
& $nssm set quant AppEnvironmentExtra "TZ=Asia/Shanghai" "QUANT_DATA_DIR=C:\var\lib\quant-trading-v2" "QUANT_ADDR=127.0.0.1:8081" "LLM_API_KEY=REPLACE_WITH_LLM_API_KEY" "LLM_API_URL=https://api.siliconflow.cn/v1/chat/completions" "LLM_MODEL=THUDM/GLM-Z1-9B-0414" 2>&1
Write-Host ("ENV READBACK: " + (& $nssm get quant AppEnvironmentExtra 2>&1))

# 3) start engine
& $nssm start quant 2>&1
Start-Sleep -Seconds 6
$ep = netstat -an | Where-Object { $_ -match ':8081 ' }
Write-Host ("engine 8081 listening: " + $(if ($ep) { "YES" } else { "NO" }))

# 4) start caddy (web :8080 -> 8081)
& $nssm start quant-web 2>&1
Start-Sleep -Seconds 5
$cp = netstat -an | Where-Object { $_ -match ':8080 ' }
Write-Host ("caddy 8080 listening: " + $(if ($cp) { "YES" } else { "NO" }))
$cp9 = netstat -an | Where-Object { $_ -match ':8090 ' }
Write-Host ("stale 8090 listening: " + $(if ($cp9) { "YES" } else { "NO" }))

# 5) gateway
schtasks /End /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 2
schtasks /Run /TN "\qmt-gateway-wd" 2>$null
Start-Sleep -Seconds 5
$gp = netstat -an | Where-Object { $_ -match ':8789 ' }
Write-Host ("gateway 8789 listening: " + $(if ($gp) { "YES" } else { "NO" }))
