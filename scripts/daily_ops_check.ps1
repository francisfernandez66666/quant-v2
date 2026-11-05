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

"=== 7) QMT terminal version and upgrade trace (QMT-CLIENT-VERSION 2026-11-05) ==="
# 2026-11-05 现网实测：券商公告的两条禁令（A 策略本地文件读写/网络、B 禁止公有云/虚拟机）对这台终端
# **当天没有生效**——桥的上行文件 5 秒前还在写、queued_connected=True，而终端 exe 的 mtime 是
# 2026-08-07、FileVersion 2.1.26.0，比 11-03 公告还早三个月。A+B 同时没生效，最可能的机制是
# **限制随新版客户端下发**，而不是服务端按日历切（这是推断，不是结论）。
#
# 值钱的地方在这里：如果机制真是"随升级生效"，那断链的触发器就不是某个日期，而是**一次没人盯着的
# 自动升级**。所以本节不猜哪一版会禁什么，只做一件事实性的事——把当前版本与安装时间钉成基线，
# 任何一次变化都当场打一行 CHANGED。判据明细一律 ASCII：这台机器的码页是 936，
# 非 ASCII 经 PS→SSH→bash 会被换成 '?'，中文的判据等于永久假绿（本仓反复踩过）。
# English: pin the installed terminal version as a baseline and shout on any change, because an
# unattended client upgrade -- not a calendar date -- is the plausible trigger of the ban.
$QmtBaseline = 'C:\var\lib\quant-trading-v2\qmt_client_baseline.json'
$exe = $null
$cl = @(Get-CimInstance Win32_Process -Filter "name='XtItClient.exe'" -ErrorAction SilentlyContinue)
if ($cl.Count -gt 0) {
  $exe = $cl[0].ExecutablePath
}
if (-not $exe -or -not (Test-Path $exe)) {
  # 进程没起也要能读数：按 ASCII 通配找安装位，绝不在脚本里写中文路径字面量（同一码页理由）
  $guess = @(Get-ChildItem 'C:\Program Files (x86)' -Directory -ErrorAction SilentlyContinue |
      Where-Object { $_.Name -like '*QMT*' } |
      ForEach-Object { Join-Path $_.FullName 'bin.x64\XtItClient.exe' } |
      Where-Object { Test-Path $_ })
  if ($guess.Count -gt 0) { $exe = $guess[0] }
}
if (-not $exe) {
  Write-Output 'QMT_CLIENT_STATE absent reason=no-process-and-no-install-glob'
} else {
  try {
    $fi = (Get-Item $exe).VersionInfo
    $item = Get-Item $exe
    $ver = [string]$fi.FileVersion
    $mt = $item.LastWriteTime.ToString('yyyy-MM-ddTHH:mm:ss')
    $safe = ($exe -replace '[^\x20-\x7E]', '?')
    Write-Output ('QMT_CLIENT version=' + $ver + ' exe_mtime=' + $mt +
        ' exe_bytes=' + $item.Length + ' age_days=' +
        [int][Math]::Round(((Get-Date) - $item.LastWriteTime).TotalDays) + ' path_ascii=' + $safe)
    $prev = $null
    if (Test-Path $QmtBaseline) {
      try { $prev = Get-Content $QmtBaseline -Raw | ConvertFrom-Json } catch { $prev = $null }
    }
    if (-not $prev) {
      @{ version = $ver; exe_mtime = $mt; exe_bytes = $item.Length;
         path_ascii = $safe; baselined_at = (Get-Date).ToString('yyyy-MM-ddTHH:mm:ss');
         previous_version = '' } | ConvertTo-Json -Compress |
        Set-Content -Path $QmtBaseline -Encoding ASCII
      # 明细一律 ASCII：这行会被 ssh/grep 链读，中文在这条链上只会被换成 '?'（永久假绿）。
      Write-Output ('QMT_CLIENT_STATE baseline_created file=' + $QmtBaseline +
          ' note=any_future_version_or_mtime_change_prints_CHANGED')
    } elseif ($prev.version -ne $ver -or $prev.exe_mtime -ne $mt) {
      # 这一行就是"禁令可能随这次升级一起落地"的信号：出现它请先确认自动下单链路还在不在
      Write-Output ('QMT_CLIENT_CHANGED from_version=' + [string]$prev.version +
          ' to_version=' + $ver + ' from_mtime=' + [string]$prev.exe_mtime + ' to_mtime=' + $mt)
      Write-Output ('WARN QMT_CLIENT_CHANGED -- on the day this appears, re-check the uplink' +
          ' file is still growing (bridge_report.jsonl mtime) and /health queued_connected;' +
          ' a client upgrade is the plausible carrier of the 11-05 file/network ban')
      @{ version = $ver; exe_mtime = $mt; exe_bytes = $item.Length; path_ascii = $safe;
         baselined_at = (Get-Date).ToString('yyyy-MM-ddTHH:mm:ss');
         previous_version = [string]$prev.version;
         previous_mtime = [string]$prev.exe_mtime } | ConvertTo-Json -Compress |
        Set-Content -Path $QmtBaseline -Encoding ASCII
    } else {
      Write-Output ('QMT_CLIENT_STATE unchanged version=' + $ver +
          ' since=' + [string]$prev.baselined_at)
    }
  } catch {
    Write-Output ('QMT_CLIENT_STATE unreadable reason=' + $_.Exception.GetType().Name)
  }
}
