# daily_ops_check.ps1 - restore killer task + verify all 5 services + researchd activity (UTF-8 BOM, §H8 起改带 BOM 中文注释)
"=== 1) QMT-Ensure-Running restore ==="
& powershell -NoProfile -ExecutionPolicy Bypass -File C:\qmt\enable_ensure.ps1

"=== 2) services health ==="
Get-Service quant,quant-research,pydata,quant-web,quant-gateway -ErrorAction SilentlyContinue |
  Select-Object Name, Status, StartType | Format-Table -AutoSize | Out-String -Width 120

"=== 3) probes ==="
# §H8（2026-09-22 修复批）：探针端口/端点收敛到同源变量——dot-source deploy/qmt-win/service_probe_config.ps1。
# 旧版三连错：探 :8080/api/status（引擎实听 :8081 且 /api 带鉴权必 401）、探 researchd 不存在的 :9091/health、
# quant-web 探引擎 :8081 的 `/`（引擎无根路由；前端静态口是 Caddy :8080）、pydata 探无路由的 /pydata_status（404 误报）。
# 配置找不到就整节 FAIL——绝不带着旧硬编码值继续假报警。
$probeCfg = @(
    (Join-Path $PSScriptRoot '..\deploy\qmt-win\service_probe_config.ps1'),
    'C:\qmt\quant-trading-v2\deploy\qmt-win\service_probe_config.ps1'
) | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $probeCfg) {
  Write-Output 'FAIL service_probe_config.ps1 未找到（§H8 探针同源配置），探针节跳过'
} else {
  . $probeCfg
  foreach ($u in @($ProbeQuantUrl, $ProbeWebUrl, $ProbePydataUrl, $ProbeGatewayUrl)) {
    try { $r = Invoke-RestMethod -Uri $u -TimeoutSec 5; Write-Output ("OK   " + $u); } catch { Write-Output ("FAIL " + $u + " : " + $_.Exception.Message) }
  }
  # §H8：researchd 无 HTTP 口 → 文件心跳判定（scheduler 每 30s 原子落 scheduler_status.json，mtime 新鲜即活）
  try {
    $hb = Get-Item -Path $ProbeResearchHeartbeat -ErrorAction Stop
    $ageMin = [math]::Round(((Get-Date) - $hb.LastWriteTime).TotalMinutes, 1)
    if ($ageMin -le $ProbeResearchMaxAgeMin) { Write-Output ("OK   researchd heartbeat " + $hb.FullName + " age=" + $ageMin + "min") }
    else { Write-Output ("FAIL researchd heartbeat " + $hb.FullName + " age=" + $ageMin + "min > " + $ProbeResearchMaxAgeMin + "min") }
  } catch { Write-Output ("FAIL researchd heartbeat " + $ProbeResearchHeartbeat + " 不存在（§H8 文件心跳判定）") }
}

"=== 4) researchd process + mem + latest activity ==="
Get-CimInstance Win32_Process -Filter "name='researchd.exe'" | ForEach-Object { $p=Get-Process -Id $_.ProcessId; Write-Output ("researchd pid=" + $_.ProcessId + " mem=" + [math]::Round($p.WorkingSet64/1MB) + "MB cpu=" + [math]::Round($p.TotalProcessorTime.TotalSeconds)) }
$ld = Get-ChildItem 'C:\var\lib\quant-trading-v2' -Recurse -File -ErrorAction SilentlyContinue | Where-Object { $_.LastWriteTime -gt (Get-Date).AddMinutes(-10) } | Sort-Object LastWriteTime -Descending | Select-Object -First 5
if ($ld) { $ld | Select-Object FullName, LastWriteTime | Format-List | Out-String -Width 220 } else { Write-Output 'no recent writes in data dir (research may be idle)' }

"=== 5) quant.exe + model bridge state ==="
Get-CimInstance Win32_Process -Filter "name='quant.exe'" | ForEach-Object { Write-Output ("quant pid=" + $_.ProcessId) }
# §H8：网关健康口同样取同源变量（配置缺失时本节明确 FAIL，不再回退旧硬编码 URL）
if ($probeCfg) {
  try { (Invoke-RestMethod -Uri $ProbeGatewayUrl -TimeoutSec 5) | ConvertTo-Json -Depth 2 } catch { "gateway unreachable" }
} else {
  Write-Output 'FAIL gateway probe skipped: service_probe_config.ps1 not loaded (§H8)'
}

"=== 6) watchdog/watchdog tasks ==="
schtasks /query /fo csv 2>$null | Select-String -Pattern 'quant-all-wd|QMT-Ensure|QMT-Gateway|qmt-gateway' | ForEach-Object { $_.Line }
