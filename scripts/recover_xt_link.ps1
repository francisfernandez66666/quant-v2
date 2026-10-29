# recover_xt_link.ps1 - 实盘链路失联时的分阶段恢复/取证入口（2026-10-29 断链实录收编）。
# 背景：网关在 queued（QMT 内嵌桥）无心跳时会持续重试 xtquant 直连，每次 connect() 都在客户端
# 共享内存占一个 writer 槽，槽打满（WaitingFreeWriter exceed）会把 QMT 客户端拖崩，客户端一崩
# 桥更没心跳 —— 死循环。本脚本把「停网关 → 走计划任务干净重启客户端 → 再起网关 → 读数」拆开，
# 每阶段一条命令，且客户端只允许经 QMT-Ensure-Running 拉起（ssh 会话内直起会随会话被杀）。
# English: staged recovery for the live-link outage (kill the xt retry storm, relaunch the client
# via its scheduled task so it survives the SSH session, then bring the gateway back and report).
# Usage: recover_xt_link.ps1 -Phase kill|client|restartclient|gateway|report|procs|boot|
#                             formula|formula2|formula3|pyerr|calc|ds300|qmtlogs|strcfg|wrapper
#        （-Phase kill 加 -KeepGatewayDown 会同时禁用 QMT-Gateway-Ensure，事后必须用
#          -Phase gateway 复启用；只读阶段不改任何现网状态。）
param(
  [Parameter(Mandatory=$true)][string]$Phase,
  [switch]$KeepGatewayDown
)

function Get-GatewayProc {
  Get-CimInstance Win32_Process -Filter "name='python.exe'" |
    Where-Object { $_.CommandLine -match 'gateway\.py' }
}

switch ($Phase) {
  'kill' {
    Get-GatewayProc | ForEach-Object {
      Stop-Process -Id $_.ProcessId -Force
      Write-Output ("KILLED_GATEWAY pid=" + $_.ProcessId)
    }
    Start-Sleep -Seconds 3
    if (Get-GatewayProc) { Write-Output 'GATEWAY_STILL_RUNNING' } else { Write-Output 'GATEWAY_DOWN' }
    if ($KeepGatewayDown) {
      Disable-ScheduledTask -TaskName QMT-Gateway-Ensure -ErrorAction SilentlyContinue | Out-Null
      Write-Output 'GATEWAY_ENSURE_DISABLED'
    }
  }
  'client' {
    Get-Process XtItClient -ErrorAction SilentlyContinue | ForEach-Object { Write-Output ("KILLED_CLIENT pid=" + $_.Id); Stop-Process -Id $_.Id -Force }
    Start-Sleep -Seconds 3
    & C:\opt\quant\qmtctl.exe
    Start-Sleep -Seconds 50
    Get-Process | Where-Object { $_.ProcessName -match '^Xt' } |
      ForEach-Object { Write-Output ("CLIENT pid=" + $_.Id + " name=" + $_.ProcessName + " start=" + $_.StartTime.ToString('HH:mm:ss')) }
  }
  'gateway' {
    Enable-ScheduledTask -TaskName QMT-Gateway-Ensure -ErrorAction SilentlyContinue | Out-Null
    Write-Output 'GATEWAY_ENSURE_ENABLED'
    schtasks /run /tn QMT-Gateway-Ensure | Out-Null
    Start-Sleep -Seconds 15
    Get-GatewayProc | ForEach-Object { Write-Output ("GATEWAY pid=" + $_.ProcessId) }
  }
  'procs' {
    Get-Process | Where-Object { $_.ProcessName -match '^Xt' } |
      ForEach-Object { Write-Output ("PROC pid=" + $_.Id + " name=" + $_.ProcessName + " start=" + $_.StartTime.ToString('HH:mm:ss') + " title=[" + $_.MainWindowTitle + "]") }
    $r = 'C:\qmt\quant-trading-v2\qmt_gateway\bridge_report.jsonl'
    $i = Get-Item $r -ErrorAction SilentlyContinue
    if ($i) { Write-Output ("REPORT size=" + $i.Length + " mtime=" + $i.LastWriteTime.ToString('MM-dd HH:mm:ss')) }
    $b = 'C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log'
    $j = Get-Item $b -ErrorAction SilentlyContinue
    if ($j) { Write-Output ("BOOT size=" + $j.Length + " mtime=" + $j.LastWriteTime.ToString('MM-dd HH:mm:ss')) }
  }
  'boot' {
    Get-Content 'C:\qmt\quant-trading-v2\qmt_gateway\bridge_boot.log' -Tail 12 |
      ForEach-Object { if ($_.Length -gt 110) { Write-Output ($_.Substring(0, 110) + ' ...') } else { Write-Output $_ } }
  }
  'qmtlogs' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    Write-Output ("ROOT=" + $root)
    Get-ChildItem -Path (Join-Path $root 'bin.x64') -Recurse -Depth 3 -Include *.log -ErrorAction SilentlyContinue |
      Where-Object { $_.LastWriteTime -gt (Get-Date).AddMinutes(-90) } |
      Sort-Object LastWriteTime -Descending | Select-Object -First 12 |
      ForEach-Object { Write-Output ("LOG mtime=" + $_.LastWriteTime.ToString('MM-dd HH:mm:ss') + " size=" + $_.Length + " name=" + $_.Name) }
  }
  'strcfg' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $hits = Get-ChildItem -Path $root -Recurse -File -Include *.xml, *.json -ErrorAction SilentlyContinue |
      Where-Object { $_.FullName -notmatch '\\export|\\download|\\resource|\\formulas|\\charts' } |
      Select-String -Pattern 'autotrade|auto_run|autorun|period|cycle' -List -ErrorAction SilentlyContinue |
      Select-Object -First 12
    foreach ($h in $hits) {
      Write-Output ("CFG mtime=" + $h.Path.LastWriteTime.ToString('MM-dd HH:mm') + " file=" + $h.Path.Substring($root.Length))
    }
    Write-Output '--- newest 12 files under xtmodel/userdata ---'
    Get-ChildItem -Path (Join-Path $root 'userdata'), (Join-Path $root 'xtmodel') -Recurse -File -ErrorAction SilentlyContinue |
      Sort-Object LastWriteTime -Descending | Select-Object -First 12 |
      ForEach-Object { Write-Output ("NEW mtime=" + $_.LastWriteTime.ToString('MM-dd HH:mm:ss') + " size=" + $_.Length + " path=" + $_.FullName.Substring($root.Length)) }
  }
  'formula' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $day = (Get-Date).ToString('yyyyMMdd')
    foreach ($n in @("XtClient_FormulaOutput_$day.log", "XtClient_Message_$day.log")) {
      $f = Join-Path $root "userdata\log\$n"
      Write-Output ("===== " + $n + " =====")
      if (Test-Path $f) {
        Get-Content $f -Tail 25 | ForEach-Object {
          if ($_.Length -gt 150) { Write-Output ($_.Substring(0, 150) + ' ...') } else { Write-Output $_ }
        }
      } else { Write-Output 'MISSING' }
    }
  }
  'formula2' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $day = (Get-Date).ToString('yyyyMMdd')
    $f = Join-Path $root "userdata\log\XtClient_Formula_$day.log"
    Write-Output ("FILE=" + $f + " size=" + (Get-Item $f).Length)
    Get-Content $f -Tail 400 | Select-String -Pattern 'handlebar|error|Error|exception|Traceback|python|000300' |
      Select-Object -Last 20 | ForEach-Object {
        $t = $_.Line
        if ($t.Length -gt 160) { $t = $t.Substring(0, 160) }
        Write-Output ("F2 " + $t)
      }
  }
  'formula3' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $day = (Get-Date).ToString('yyyyMMdd')
    $f = Join-Path $root "userdata\log\XtClient_Formula_$day.log"
    Get-Content $f -Tail 45 | ForEach-Object {
      $t = $_
      if ($t.Length -gt 170) { $t = $t.Substring(0, 170) }
      Write-Output ("F3 " + $t)
    }
  }
  'calc' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $day = (Get-Date).ToString('yyyyMMdd')
    $f = Join-Path $root "userdata\log\XtClient_Formula_$day.log"
    Write-Output '--- start calc / receive data lines, last 12 ---'
    Select-String -Path $f -Pattern 'start calc|receive data' | Select-Object -Last 12 | ForEach-Object {
      $t = $_.Line
      if ($t.Length -gt 150) { $t = $t.Substring(0, 150) }
      Write-Output ("C " + $t)
    }
    Write-Output '--- datasource log tail 8 ---'
    $d = Join-Path $root "userdata\log\XtClient_datasource_$day.log"
    Get-Content $d -Tail 8 | ForEach-Object { $t=$_; if ($t.Length -gt 150) { $t=$t.Substring(0,150) }; Write-Output ("D " + $t) }
  }
  'ds300' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $day = (Get-Date).ToString('yyyyMMdd')
    $d = Join-Path $root "userdata\log\XtClient_datasource_$day.log"
    Write-Output '--- datasource lines mentioning 000300, last 10 ---'
    Select-String -Path $d -Pattern '000300' | Select-Object -Last 10 | ForEach-Object {
      $t = $_.Line; if ($t.Length -gt 165) { $t = $t.Substring(0, 165) }; Write-Output ("DS " + $t) }
    Write-Output '--- datasource error-ish lines, last 10 ---'
    Select-String -Path $d -Pattern 'fail|error|timeout|disconnect|reconnect' | Select-Object -Last 10 | ForEach-Object {
      $t = $_.Line; if ($t.Length -gt 165) { $t = $t.Substring(0, 165) }; Write-Output ("ER " + $t) }
  }
  'pyerr' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $day = (Get-Date).ToString('yyyyMMdd')
    foreach ($n in @("XtClient_FormulaOutput_$day.log", "XtClient_Formula_$day.log", "XtClient_$day.log")) {
      $f = Join-Path $root "userdata\log\$n"
      if (-not (Test-Path $f)) { Write-Output ("SKIP missing " + $n); continue }
      Write-Output ("===== " + $n + " size=" + (Get-Item $f).Length + " =====")
      Select-String -Path $f -Pattern 'Traceback|Error|error:|Exception|handlebar|on_tick|not exist|None' |
        Select-Object -Last 12 | ForEach-Object {
          $t = $_.Line; if ($t.Length -gt 165) { $t = $t.Substring(0, 165) }; Write-Output ("P " + $t) }
    }
  }
  'wrapper' {
    $root = (Get-ChildItem 'C:\Program Files (x86)' -Directory | Where-Object { $_.Name -like '*QMT*' } | Select-Object -First 1).FullName
    $pydir = Join-Path $root 'python'
    Write-Output ("PYDIR=" + $pydir)
    $name = [string][char]0x65B0 + [string][char]0x5EFA + [string][char]0x7B56 + [string][char]0x7565 + [string][char]0x6587 + [string][char]0x4EF6 + '.py'
    $f = Join-Path $pydir $name
    Write-Output ("WRAPPER=" + $f + " exists=" + (Test-Path $f))
    if (Test-Path $f) {
      $i = Get-Item $f
      Write-Output ("META size=" + $i.Length + " mtime=" + $i.LastWriteTime.ToString('MM-dd HH:mm:ss'))
      Get-Content $f -Encoding UTF8 | ForEach-Object { $t=$_; if ($t.Length -gt 150) { $t=$t.Substring(0,150) }; Write-Output ("W " + $t) }
    }
  }
  'restartclient' {
    # 客户端必须由计划任务在交互会话里拉起：ssh 会话内直接 Start-Process 会随会话结束被杀（RUNBOOK 4 已记）。
    Get-Process XtItClient -ErrorAction SilentlyContinue | ForEach-Object { Write-Output ("KILLED_CLIENT pid=" + $_.Id); Stop-Process -Id $_.Id -Force }
    Start-Sleep -Seconds 4
    schtasks /run /tn QMT-Ensure-Running | Out-Null
    Write-Output 'ENSURE_TASK_TRIGGERED'
    Start-Sleep -Seconds 75
    Get-Process | Where-Object { $_.ProcessName -match '^Xt' } |
      ForEach-Object { Write-Output ("CLIENT pid=" + $_.Id + " name=" + $_.ProcessName + " start=" + $_.StartTime.ToString('HH:mm:ss')) }
  }
  'report' {
    try {
      $h = Invoke-RestMethod -Uri 'http://127.0.0.1:8789/health' -TimeoutSec 6
      Write-Output ("HEALTH ok=" + $h.ok + " broker=" + $h.broker + " xt=" + $h.xt_connected +
        " queued=" + $h.queued_connected + " feed=" + $h.feed_connected +
        " live_ready=" + $h.live_ready + " last_fail=" + $h.xt_last_fail_at + " ts=" + $h.ts)
    } catch { Write-Output 'HEALTH_UNREACHABLE' }
  }
  default { Write-Output 'BAD_PHASE' }
}
