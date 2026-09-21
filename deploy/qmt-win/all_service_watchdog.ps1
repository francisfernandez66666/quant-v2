# all_service_watchdog.ps1 — 广州执行机全服务守护（§WS-J 维3）
# 职责：巡检 NSSM 五服务（quant/quant-research/pydata/quant-web/qmt_gateway）健康端点，
#       异常自动重启（退避 3 次 / 10 分钟）；单实例保障（按 ParentProcessId 定位"亲儿子"，
#       清除孤儿/双实例）；心跳探针落 opslog + 日志。
# 安装（SYSTEM 计划任务，开机自启、独立会话不随 RDP/SSH 断开终止）：
#   schtasks /Create /F /TN quant-all-wd /SC ONSTART /RU SYSTEM /RL HIGHEST ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\qmt\all_service_watchdog.ps1"
#   schtasks /Run /TN quant-all-wd
# 全文件保持 UTF-8 BOM（Windows PowerShell 5.1 无 BOM 按 GBK 读中文会乱码）。
$ErrorActionPreference = "Continue"
$Log = "C:\qmt\watchdog_all.log"
$Base = "C:\qmt\quant-trading-v2"

function Log($m) {
    Add-Content -Path $Log -Value ("[all-wd {0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $m)
}

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

# 服务清单：NSSM 名 → 探针定义（§H8：URL/路径/阈值全部来自 service_probe_config.ps1）。
#   Type=http       GET 探针 URL 返回 200 = 健康（均为无鉴权端点，401 不再被当成服务挂）
#   Type=heartbeat  无 HTTP 口（researchd）：探针文件 mtime 距今 ≤ MaxAgeMin 分钟 = 健康
$Services = @(
    @{ Name = "quant";          Type = "http";      Probe = $ProbeQuantUrl },
    @{ Name = "quant-research"; Type = "heartbeat"; Probe = $ProbeResearchHeartbeat; MaxAgeMin = $ProbeResearchMaxAgeMin },
    @{ Name = "pydata";         Type = "http";      Probe = $ProbePydataUrl },
    @{ Name = "quant-web";      Type = "http";      Probe = $ProbeWebUrl },
    @{ Name = "qmt_gateway";    Type = "http";      Probe = $ProbeGatewayUrl }
)

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
    try {
        $out = & "C:\qmt\nssm\nssm.exe" status $name 2>$null
        return ($out -match "SERVICE_RUNNING")
    } catch { return $false }
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
    Log ("RESTART {0}（第 {1} 次/10min）" -f $svc.Name, ($st.Restarts + 1))
    & "C:\qmt\nssm\nssm.exe" restart $svc.Name 2>&1 | Out-Null
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
        $up = @($Services | ForEach-Object {
            if ((Get-NssmStatus $_.Name) -and (Test-ServiceProbe $_)) { $_.Name } else { "$($_.Name):DOWN" }
        }) -join ","
        Log ("HEARTBEAT services=[" + $up + "]")
        $lastHeartbeat = Get-Date
    }
    Start-Sleep -Seconds 15
}
