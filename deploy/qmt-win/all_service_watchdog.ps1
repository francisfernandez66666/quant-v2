# all_service_watchdog.ps1 — 广州执行机全服务守护（§WS-J 维3）
# 职责：巡检 §C7 单一守护清单（NSSM 四服务 quant/quant-research/pydata/quant-web
#       ＋ qmt_gateway 交互任务腿）健康端点，异常自动恢复（退避 3 次 / 10 分钟）；
#       单实例保障（按 ParentProcessId 定位"亲儿子"，清除孤儿/双实例）；心跳探针落 opslog + 日志。
# 安装（SYSTEM 计划任务，开机自启、独立会话不随 RDP/SSH 断开终止）：
#   schtasks /Create /F /TN quant-all-wd /SC ONSTART /RU SYSTEM /RL HIGHEST ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\qmt\all_service_watchdog.ps1"
#   schtasks /Run /TN quant-all-wd
# 全文件保持 UTF-8 BOM（Windows PowerShell 5.1 无 BOM 按 GBK 读中文会乱码）。
#
# §C7-OPS（2026-09-26，FIX_PLAN_20260925EVE ⑯）已收编进 service_definitions.ps1 的定义：
#   NSSM 服务名清单、网关守护腿形态（Type=gwtask，恢复动作＝schtasks 拉起交互任务，
#   **不再 nssm restart 一个现网不存在的 qmt_gateway 服务**——旧形态那条腿每 15 秒 DOWN、
#   重启对象是虚构服务，网关真挂时反而不恢复）、nssm.exe 落位（旧硬编码 C:\qmt\nssm\nssm.exe
#   属分离期残影，现网位在 C:\opt\quant\qmt-win\tools\…，见 service_definitions.ps1 头注③）、
#   $Base/$Log 路径、任务名。端口/探针 URL 仍走 §H8 service_probe_config.ps1 单源（不变）。
$ErrorActionPreference = "Continue"

function Log($m) {
    Add-Content -Path $Log -Value ("[all-wd {0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $m)
}

# 兜底字面量（仅供下面两段 dot-source 的候选路径使用；§C7 单源就位后立即被 $SvcWatchdogBase 覆盖）
$Base = "C:\qmt\quant-trading-v2"
$Log  = "C:\qmt\watchdog_all.log"

# §H8（2026-09-22 修复批）：探针端口/端点收敛到同源变量——dot-source service_probe_config.ps1。
# 旧版三连错实录：quant 探 :8080（那是 Caddy 口，引擎实听 :8081）且 /api/status 带鉴权必 401；
# researchd 探不存在的 :9091/health（无 HTTP 口）；quant-web 探引擎 :8081 的 `/`（引擎无根路由）。
# 现改为：quant→:8081 无鉴权 /setup，quant-web→Caddy :8080 静态页，researchd→心跳文件 mtime 判定。
# 配置缺失即致命退出：宁停守护也不带旧硬编码值继续误判误重启。
$ProbeConfig = $null
foreach ($cand in @((Join-Path $PSScriptRoot "service_probe_config.ps1"),
                    (Join-Path $Base "deploy\qmt-win\service_probe_config.ps1"))) {
    if (Test-Path $cand) { . $cand; $ProbeConfig = $cand; break }
}
if (-not $ProbeConfig) {
    Log "FATAL: service_probe_config.ps1 未找到（§H8 探针同源配置），守护退出，请检查部署清单"
    exit 1
}

# §C7-OPS（2026-09-26）：服务/任务定义单源——dot-source service_definitions.ps1（端口腿已由
# 上面的 §H8 块就位，此处取服务名、守护清单形态、nssm 落位、任务名、路径）。
# 与 §H8 同规：配置缺失即致命退出，宁停守护也不带着"NSSM 五服务"的旧矛盾清单空转误重启。
$SvcDefs = $null
foreach ($cand in @((Join-Path $PSScriptRoot "service_definitions.ps1"),
                    (Join-Path $Base "deploy\qmt-win\service_definitions.ps1"),
                    "C:\opt\quant\qmt-win\service_definitions.ps1")) {
    if (Test-Path $cand) { . $cand; $SvcDefs = $cand; break }
}
if (-not $SvcDefs -or $SvcWatchServices.Count -eq 0) {
    Log "FATAL: service_definitions.ps1 未找到或守护清单为空（§C7 服务定义单源），守护退出，请检查部署清单"
    exit 1
}
$NssmExe = Resolve-SvcNssm     # $null 时服务在位判定回退 Get-Service（见 Get-NssmStatus）
$Base    = $SvcWatchdogBase    # 覆盖上面的兜底字面量：自此路径只认 §C7 单源
$Log     = $SvcWatchdogLog

# 服务清单：来自 service_definitions.ps1 的 $SvcWatchServices（NSSM 四条 + 网关 gwtask 一条）。
#   Type=http       GET 探针 URL 返回 200 = 健康（均为无鉴权端点，401 不再被当成服务挂）
#   Type=heartbeat  无 HTTP 口（researchd）：探针文件 mtime 距今 ≤ MaxAgeMin 分钟 = 健康
#   Type=gwtask     qmt_gateway：非 NSSM 服务（交互会话计划任务拉起，Session 0 恒连不上柜台）——
#                   健康判定只看 /health 探针；恢复走 Restart-GatewayViaTask（schtasks /Enable + /Run）。
$Services = $SvcWatchServices

# 退避状态：服务名 → @{ Restarts=0; WindowStart=epoch }
$Backoff = @{}
$BackoffWindowSec = 600   # 10 分钟窗口
$MaxRestarts = 3          # 窗口内最多重启次数，超出则告警等待窗口滑过

function Get-RestartState($name) {
    if (-not $Backoff.ContainsKey($name)) {
        $Backoff[$name] = @{ Restarts = 0; WindowStart = [DateTime]::UtcNow }
    }
    return $Backoff[$name]
}

function Invoke-Probe($url) {
    if (-not $url) { return $true }  # 无端点 → 仅进程存活检查
    try {
        $resp = Invoke-WebRequest -Uri $url -TimeoutSec 5 -UseBasicParsing
        return ($resp.StatusCode -eq 200)
    } catch { return $false }
}

# §H8：researchd 等无 HTTP 口服务的文件心跳判定——scheduler 每 30s 落一次
# scheduler_status.json（internal/scheduler/scheduler.go tick→writeStatus），mtime 新鲜即存活；
# 文件缺失/不可读按不健康处理（进程活着但调度循环卡死也应被发现）。
function Test-HeartbeatProbe($path, $maxAgeMin) {
    try {
        $f = Get-Item -Path $path -ErrorAction Stop
        return (((Get-Date) - $f.LastWriteTime).TotalMinutes -le $maxAgeMin)
    } catch { return $false }
}

# 按服务 Type 分派探针（http → GET 200；heartbeat → 文件 mtime）
function Test-ServiceProbe($svc) {
    if ($svc.Type -eq "heartbeat") {
        return (Test-HeartbeatProbe $svc.Probe $svc.MaxAgeMin)
    }
    return (Invoke-Probe $svc.Probe)
}

function Get-NssmStatus($name) {
    # §C7-OPS：nssm 落位来自 service_definitions.ps1（Resolve-SvcNssm）；解析不到时回退
    # Get-Service 判在位（SCM 视角与 nssm status 的 SERVICE_RUNNING 等价，不改红绿语义）。
    if ($NssmExe -and (Test-Path $NssmExe)) {
        try {
            $out = & $NssmExe status $name 2>$null
            return ($out -match "SERVICE_RUNNING")
        } catch { return $false }
    }
    $st = (Get-Service -Name $name -ErrorAction SilentlyContinue).Status
    return ($st -eq "Running")
}

# §C7-OPS：网关守护腿的正确恢复通道——qmt_gateway 不是 NSSM 服务（Session 0 起不来券商会话），
# 唯一径＝交互会话计划任务（服务定义单源 $SvcTaskGatewayEnsure，由 register_service.ps1 注册）。
# 09-11 实录（RUNBOOK_QMT_DAILY.md:89-90）：该任务可能被 disable，必须先 /change /enable 再 /run；
# ssh/服务会话直接 Start-Process 拉网关会随会话被杀——所以这里只触发任务，绝不自己起进程。
function Restart-GatewayViaTask($gwTask) {
    & schtasks /Change /TN $gwTask /Enable 2>&1 | Out-Null
    & schtasks /Run /TN $gwTask 2>&1 | Out-Null
    Log ("RESTART-GATEWAY via schtasks /Run {0}（enable+run，交互任务幂等拉起）" -f $gwTask)
}

function Restart-ServiceWithBackoff($svc, $st) {
    $now = [DateTime]::UtcNow
    # 窗口滑动：距上次重启起点已过 10 分钟 → 计数重置
    if (($now - $st.WindowStart).TotalSeconds -ge $BackoffWindowSec) {
        $st.Restarts = 0
        $st.WindowStart = $now
    }
    if ($st.Restarts -ge $MaxRestarts) {
        Log ("SKIP {0}: 窗口内已重启 {1} 次（{2} 次/10min 上限），等待窗口滑过" -f $svc.Name, $st.Restarts, $MaxRestarts)
        return
    }
    if ($svc.Type -eq "gwtask") {
        Log ("RESTART {0}（第 {1} 次/10min，走交互任务）" -f $svc.Name, ($st.Restarts + 1))
        Restart-GatewayViaTask $svc.Task
        $st.Restarts++
        return
    }
    Log ("RESTART {0}（第 {1} 次/10min）" -f $svc.Name, ($st.Restarts + 1))
    if ($NssmExe -and (Test-Path $NssmExe)) {
        & $NssmExe restart $svc.Name 2>&1 | Out-Null
    } else {
        Restart-Service -Name $svc.Name -Force -ErrorAction SilentlyContinue
    }
    $st.Restarts++
}

# 单实例治理：按 NSSM 主进程 PID 找到"亲儿子"进程，孤儿/双实例一律结束。
# §WS-J：此前双实例/孤儿进程靠人工收敛（§零实录多次踩坑），此处脚本化。
function Enforce-SingleInstance($svc) {
    try {
        $main = Get-CimInstance Win32_Service -Filter "Name='$($svc.Name)'" -ErrorAction Stop
        if (-not $main) { return }
        $parentPid = $main.ProcessId
        if ($parentPid -le 0) { return }
        # 该服务派生进程（父=服务主进程，或祖链可达主进程）仅允许保留一个
        $children = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
            Where-Object { $_.ParentProcessId -eq $parentPid -or $_.ParentProcessId -eq $main.ProcessId }
        $keep = $false
        foreach ($c in $children) {
            if (-not $keep) { $keep = $true; continue }   # 第一个保留
            Log ("KILL 孤儿/双实例: {0} pid={1}（{2}）" -f $c.Name, $c.ProcessId, $svc.Name)
            Stop-Process -Id $c.ProcessId -Force -ErrorAction SilentlyContinue
        }
    } catch { /* 枚举失败不阻断巡检 */ }
}

Log "all-service watchdog started (pid=$PID)"
$lastHeartbeat = Get-Date

while ($true) {
    foreach ($svc in $Services) {
        # §H8：探针判定统一走 Test-ServiceProbe（HTTP 与文件心跳两类），且每轮只探一次，
        # 不再在 DOWN 日志行里重复请求。
        # §C7-OPS：网关腿（gwtask）没有 NSSM 服务可查——在位与否以 /health 探针为准
        # （旧形态拿 nssm status 查一个不存在的服务，导致每轮恒 DOWN 且重启对象错误）。
        if ($svc.Type -eq "gwtask") {
            if (Test-ServiceProbe $svc) { continue }
            Log ("DOWN {0}（交互任务腿，NSSM运行=n/a 探针={1} 通过=False）" -f $svc.Name, $svc.Probe)
            Restart-ServiceWithBackoff $svc (Get-RestartState $svc.Name)
            continue
        }
        $running = Get-NssmStatus $svc.Name
        $probed = Test-ServiceProbe $svc
        if ($running -and $probed) {
            continue
        }
        Log ("DOWN {0}（NSSM运行={1} 探针={2} 通过={3}）" -f $svc.Name, $running, $svc.Probe, $probed)
        Enforce-SingleInstance $svc
        Restart-ServiceWithBackoff $svc (Get-RestartState $svc.Name)
    }
    # 心跳：每 5 分钟记一次 opslog 等价留档（stdout → 计划任务日志 + 本文件）
    if (((Get-Date) - $lastHeartbeat).TotalMinutes -ge 5) {
        # §C7-OPS：gwtask 腿的"在位"＝探针本身（没有 NSSM 服务可查），其余腿维持
        # nssm status ∧ 探针 双判据——各腿语义与旧版一致，只是网关腿不再是空转。
        $up = @($Services | ForEach-Object {
            if ($_.Type -eq "gwtask") {
                if (Test-ServiceProbe $_) { $_.Name } else { "$($_.Name):DOWN" }
            } elseif ((Get-NssmStatus $_.Name) -and (Test-ServiceProbe $_)) { $_.Name } else { "$($_.Name):DOWN" }
        }) -join ","
        Log ("HEARTBEAT services=[" + $up + "]")
        $lastHeartbeat = Get-Date
    }
    Start-Sleep -Seconds 15
}
