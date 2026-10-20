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
#                   健康判定读 /health 结构化字段（§N-01）：进程/HTTP 不可达 → 走 Restart-GatewayViaTask；
#                   进程活但 live_ready=false → 只告警不重启（重启治不了终端未登录）；feed 断 → 仅告警。
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

# §N-01（2026-10-20）：网关腿必须读结构化字段，不能只看 HTTP 200。
#   gateway.py 的 ok 恒为 true（进程能回 HTTP 就真），通道全断时它照样 200——
#   旧判据把"进程活着"当成"通道健康"，于是 broker/xt/feed 全断时看门狗每轮判健康、
#   一次都不告警（F-23 能静默 4 天的复发机制）。
#   返回 $null＝进程/HTTP 不可达（真 DOWN，走交互任务拉起）；
#   返回对象＝进程活着，再由调用方按 live_ready / feed_connected 分流（D9：通道断不重启）。
function Get-GatewayHealth($url) {
    if (-not $url) { return $null }
    try {
        $resp = Invoke-WebRequest -Uri $url -TimeoutSec 5 -UseBasicParsing
        if ($resp.StatusCode -ne 200) { return $null }
        return ($resp.Content | ConvertFrom-Json)
    } catch { return $null }
}

# 网关三态告警去抖：只在状态翻转时写一行，避免每 15s 刷屏把日志淹掉
$Script:GwLiveDownNoted = $false
$Script:GwFeedDownNoted = $false
$Script:GwFeedDownRounds = 0

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
    # ★ 10-10 首拨实录（读法供给侧第五犯）：nssm 往 stdout 写 UTF-16（register_engine_services.ps1
    #   :214 有记录），PS 按单字节解码后是夹 NUL 的文本 ⇒ 对原文直接 -match 恒不命中 ⇒
    #   四条 NSSM 腿全部误判 DOWN——而同一轮日志里探针字段全是通过（通过=True），健康服务
    #   被连环重启 + 单实例治理杀子进程，quant 引擎被杀三次才靠退避停下。修法＝先剥 NUL
    #   再匹配；剥完仍读不出任何 SERVICE_ 前缀状态词时落回 SCM 视角——"读不出"不得判成
    #   "没在跑"（fail 的方向必须是往 SCM 落，而不是往 DOWN 判）。
    if ($NssmExe -and (Test-Path $NssmExe)) {
        try {
            $raw = [string](& $NssmExe status $name 2>$null)
            $txt = $raw -replace '\x00', ''
            if ($txt -match 'SERVICE_RUNNING') { return $true }
            if ($txt -match 'SERVICE_[A-Z_]+') { return $false }
        } catch { }
        # nssm 文本读不出（SYSTEM 会话解码不可靠，10-10 首拨事故形态）→ 落 SCM 视角。
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

# §DEP-PAUSE（2026-10-11 部署实录）：读"部署停机窗口互斥牌"。
#   返回 $null＝无牌/牌不可信 ⇒ 监督照常；返回 DateTime＝牌有效到该时刻 ⇒ 本轮只读数、不动手。
#   为什么需要这张牌：部署 [2/5] 为了释放 exe 文件锁必须 net stop quant / quant-research，
#   而本守护每 15 秒巡检一轮、会把刚被部署停掉的服务判 DOWN 并 nssm restart——当日实录就是它抢在
#   scp 之前把 researchd.exe/research.exe 重新锁住，五份二进制被劈成两半（前三份新版、后两份旧版），
#   而验收脚本只看 quant 的 buildCommit，混合半态当时无人可见。
#   fail 方向定死为"往监督落、不往免检落"：文件读不出、首行不是纯数字、时间戳已过期，一律按无牌处理
#   并留一行原因。牌本身坏掉绝不能变成一条静默的"谁都别管"通道——那是 10-10 看门狗 UTF-16 误判四腿
#   连环重启的同族反面（那次是"判据自己瞎了还在动手"，这次是"判据读不出就不动手"，两者都不可接受）。
$Script:PauseExpiredNoted = $false
function Get-DeployPauseDeadline {
    if (-not (Test-Path $SvcDeployPauseFlag)) {
        # 牌不在位：过期提醒复位，下一次真过期还能再喊一次
        $Script:PauseExpiredNoted = $false
        return $null
    }
    $raw = $null
    try { $raw = (Get-Content -Path $SvcDeployPauseFlag -TotalCount 1 -ErrorAction Stop) }
    catch {
        Log ("DEPLOY-PAUSE 牌在位但读不出（{0}）——按无牌处理，监督照常" -f $_.Exception.Message)
        return $null
    }
    $head = (("$raw" -split '\|')[0]).Trim()
    $epoch = [int64]0
    if (-not [int64]::TryParse($head, [ref]$epoch)) {
        Log ("DEPLOY-PAUSE 牌首行不是 Unix 秒（读到='{0}'）——按无牌处理，监督照常" -f $head)
        return $null
    }
    $deadline = [DateTimeOffset]::FromUnixTimeSeconds($epoch).LocalDateTime
    if ($deadline -le (Get-Date)) {
        # 过期牌＝写牌方没撤成（部署被 kill -9 / SSH 断裂时不会跑 trap）。喊一次就够，
        # 但每轮都按无牌处理：过期未撤的牌不得无限期免检。
        if (-not $Script:PauseExpiredNoted) {
            Log ("DEPLOY-PAUSE 牌已过期（到期 {0}，文件仍在位＝撤牌腿没走到）——按无牌处理，监督照常，请查部署链" -f $deadline.ToString("yyyy-MM-dd HH:mm:ss"))
            $Script:PauseExpiredNoted = $true
        }
        return $null
    }
    return $deadline
}

# 五腿读数（HEARTBEAT 行与 §DEP-PAUSE 行共用同一实现——判据不抄第二份，抄了就会分叉：
# 10-10 那次"看门狗判 DOWN 而探针面全对"的根因之一就是同一件事有两套读法）。
function Get-ServiceFace {
    # §C7-OPS：gwtask 腿的"在位"＝探针本身（没有 NSSM 服务可查），其余腿维持
    # nssm status ∧ 探针 双判据——各腿语义与旧版一致，只是网关腿不再是空转。
    return (@($Services | ForEach-Object {
        if ($_.Type -eq "gwtask") {
            $gh = Get-GatewayHealth $_.Probe
            if ($null -eq $gh) { "$($_.Name):DOWN" }
            else {
                # §N-01：live_ready 缺失时回落 broker_connected（旧网关）；两者皆假＝通道不可用
                $hasLive = @($gh.PSObject.Properties.Name) -contains "live_ready"
                $live = $false
                if ($hasLive) { $live = [bool]$gh.live_ready } else { $live = [bool]$gh.broker_connected }
                if ($live) { $_.Name } else { "$($_.Name):LIVE_UNAVAILABLE" }
            }
        } elseif ((Get-NssmStatus $_.Name) -and (Test-ServiceProbe $_)) { $_.Name } else { "$($_.Name):DOWN" }
    }) -join ",")
}

Log "all-service watchdog started (pid=$PID)"
$lastHeartbeat = Get-Date

while ($true) {
    # §DEP-PAUSE：有牌＝部署正在停机窗口里（文件锁由部署链自己负责），本轮**只读数不动手**：
    # 不判 DOWN、不重启、不做单实例治理（后两者都挂在 DOWN 分支上，跳过判定即一起跳过）。
    # 读数照记，所以这段窗口在日志里是可归因的一行，而不是"看门狗这几分钟没写字"。
    $pauseDeadline = Get-DeployPauseDeadline
    if ($pauseDeadline) {
        $remain = [int](($pauseDeadline - (Get-Date)).TotalSeconds)
        Log ("DEPLOY-PAUSE 剩余 {0}s services=[{1}]（部署停机窗口，本轮只读数不重启）" -f $remain, (Get-ServiceFace))
        Start-Sleep -Seconds 15
        continue
    }
    foreach ($svc in $Services) {
        # §H8：探针判定统一走 Test-ServiceProbe（HTTP 与文件心跳两类），且每轮只探一次，
        # 不再在 DOWN 日志行里重复请求。
        # §C7-OPS：网关腿（gwtask）没有 NSSM 服务可查——在位与否以 /health 探针为准
        # （旧形态拿 nssm status 查一个不存在的服务，导致每轮恒 DOWN 且重启对象错误）。
        if ($svc.Type -eq "gwtask") {
            # §N-01/D9：进程死 → 交互任务拉起；进程活但通道断 → 只告警、绝不重启
            $gh = Get-GatewayHealth $svc.Probe
            if ($null -eq $gh) {
                Log ("DOWN {0}（交互任务腿，进程/HTTP 不可达 探针={1}）" -f $svc.Name, $svc.Probe)
                $Script:GwLiveDownNoted = $false
                $Script:GwFeedDownNoted = $false
                $Script:GwFeedDownRounds = 0
                Restart-ServiceWithBackoff $svc (Get-RestartState $svc.Name)
                continue
            }
            $names = @($gh.PSObject.Properties.Name)
            $hasLive = $names -contains "live_ready"
            $live = $false
            if ($hasLive) { $live = [bool]$gh.live_ready } else { $live = [bool]$gh.broker_connected }
            $broker = [bool]$gh.broker_connected
            $xt = $false
            if ($names -contains "xt_connected") { $xt = [bool]$gh.xt_connected }
            $feed = $false
            if ($names -contains "feed_connected") { $feed = [bool]$gh.feed_connected }
            $xfail = ""
            if ($names -contains "xt_last_fail_at") { $xfail = [string]$gh.xt_last_fail_at }
            if (-not $live) {
                if (-not $Script:GwLiveDownNoted) {
                    Log ("LIVE_UNAVAILABLE {0}（进程存活但交易通道不可用：broker={1} xt={2} feed={3} xt_last_fail_at={4}）——按 D9 不重启：重启治不了券商终端未登录，请人工登录终端" -f $svc.Name, $broker, $xt, $feed, $xfail)
                    $Script:GwLiveDownNoted = $true
                }
                continue
            }
            if ($Script:GwLiveDownNoted) {
                Log ("LIVE_READY {0}（交易通道已恢复：broker={1} xt={2} feed={3}）" -f $svc.Name, $broker, $xt, $feed)
                $Script:GwLiveDownNoted = $false
            }
            if (-not $feed) {
                # feed 断连不污染交易熔断面（gateway.py 的设计铁律），按 D9 只告警；
                # 连续 4 轮（约 60s）才喊，避免行情源瞬时抖动刷屏。
                $Script:GwFeedDownRounds++
                if ($Script:GwFeedDownRounds -ge 4 -and -not $Script:GwFeedDownNoted) {
                    Log ("FEED_UNAVAILABLE {0}（行情 feed 断开已约 {1}s；交易面不受影响，仅告警）" -f $svc.Name, ($Script:GwFeedDownRounds * 15))
                    $Script:GwFeedDownNoted = $true
                }
            } else {
                $Script:GwFeedDownRounds = 0
                if ($Script:GwFeedDownNoted) {
                    Log ("FEED_RECOVERED {0}（行情 feed 已恢复）" -f $svc.Name)
                    $Script:GwFeedDownNoted = $false
                }
            }
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
        Log ("HEARTBEAT services=[" + (Get-ServiceFace) + "]")
        $lastHeartbeat = Get-Date
    }
    Start-Sleep -Seconds 15
}
