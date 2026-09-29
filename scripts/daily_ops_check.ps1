# daily_ops_check.ps1 - restore killer task + verify all 5 services + researchd activity (UTF-8 BOM, §H8 起改带 BOM 中文注释)
"=== 1) QMT-Ensure-Running restore ==="
# §0929OPS-⑪-1：enable_ensure.ps1 过去硬写盘根 C:\qmt\，而它随部署落在 $PSScriptRoot 同目录
# （DEPLOY_DIR/scripts），于是"日检第 1 节"会在现网指向一个不存在的文件——PowerShell 对
# -File 不存在只报一行错就继续，日检照样往下跑＝**第 1 节静默空转**。现在按三态解析：
# 同目录 → 盘根旧位 → 都没有就整节 FAIL 并说清该跑哪条部署。绝不带着不存在的路径继续。
# English: resolve enable_ensure.ps1 from $PSScriptRoot first, then the legacy C:\qmt\ copy,
# and fail loudly when neither exists instead of silently skipping the restore step.
$ensureScript = @(
    (Join-Path $PSScriptRoot 'enable_ensure.ps1'),
    'C:\qmt\enable_ensure.ps1'
) | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $ensureScript) {
  Write-Output 'FAIL enable_ensure.ps1 未找到（$PSScriptRoot 与 C:\qmt\ 两个位置都无）：QMT-Ensure-Running 未被恢复，请先确认部署清单 §0929OPS 已下发 scripts/enable_ensure.ps1'
} else {
  Write-Output ("using enable_ensure: " + $ensureScript)
  & powershell -NoProfile -ExecutionPolicy Bypass -File $ensureScript
}

"=== 2) services health ==="
Get-Service quant,quant-research,pydata,quant-web,quant-gateway -ErrorAction SilentlyContinue |
  Select-Object Name, Status, StartType | Format-Table -AutoSize | Out-String -Width 120

"=== 3) probes ==="
# §H8（2026-09-22 修复批）：探针端口/端点收敛到同源变量——dot-source deploy/qmt-win/service_probe_config.ps1。
# 旧版三连错：探 :8080/api/status（引擎实听 :8081 且 /api 带鉴权必 401）、探 researchd 不存在的 :9091/health、
# quant-web 探引擎 :8081 的 `/`（引擎无根路由；前端静态口是 Caddy :8080）、pydata 探无路由的 /pydata_status（404 误报）。
# 配置找不到就整节 FAIL——绝不带着旧硬编码值继续假报警。
$probeCfg = @(
    # §0929OPS-⑪-1 补第一条候选：随部署落位是 DEPLOY_DIR/qmt-win（本脚本在 DEPLOY_DIR/scripts），
    # 旧两条分别覆盖"仓库目录树内手工跑"与"C:\qmt\quant-trading-v2 那套老检出"，都命中不到
    # 正规发版位置 ⇒ 三节探针整节 FAIL、五节网关检查也跳过（现网实录的"日检全红其实是找不到配置"）。
    (Join-Path $PSScriptRoot '..\qmt-win\service_probe_config.ps1'),
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
  try {
    $gwj = Invoke-RestMethod -Uri $ProbeGatewayUrl -TimeoutSec 5
    $gwj | ConvertTo-Json -Depth 2
    # §0926E2E-W2D 回报 outbox 冻结观察腿：溢出不再自动删最旧（成交回报是资金事实，宁冻结
    # 不销毁），深度/水位状态在 /health 可见；收敛只有一条路——人工 outbox_admin.py --keep N --yes。
    # 旧版网关无该字段记 N/A，不判 FAIL（观察面随本批发版生效，别让校验先于施工红）。
    if ($null -ne $gwj.outbox_depth) {
      if ($gwj.outbox_overflow) { Write-Output ("WARN outbox overflow depth=" + $gwj.outbox_depth + "（冻结保留未删：先查上报链路；确认旧回报已在决策端落账后再人工收敛）") }
      else { Write-Output ("OK   outbox depth=" + $gwj.outbox_depth) }
    } else { Write-Output 'N/A  网关旧版无 outbox_depth 字段（本批发版后生效）' }
  } catch { "gateway unreachable" }
} else {
  Write-Output 'FAIL gateway probe skipped: service_probe_config.ps1 not loaded (§H8)'
}

"=== 6) watchdog/watchdog tasks ==="
schtasks /query /fo csv 2>$null | Select-String -Pattern 'quant-all-wd|QMT-Ensure|QMT-Gateway|qmt-gateway' | ForEach-Object { $_.Line }
