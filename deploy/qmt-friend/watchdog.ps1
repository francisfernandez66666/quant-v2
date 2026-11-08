# watchdog.ps1 —— 朋友机器的每日节拍与"收盘关闭门"（§FRIEND-PROFILE 批 2，2026-11-02）
#
# 职责：一台朋友机器上的三个定时动作，全部由这一个脚本承载（-Mode 分流），
#       这样"每日节拍"的真相只有一个文件，改判据只改一处：
#         ClientEnsure   交易日 08:40 拉起 QMT 客户端（**只准走计划任务**）
#         GatewayEnsure  盘中每 5 分钟幂等守护网关端口（未监听才拉起）
#         Close          15:30 起尝试关 QMT，门关不关由 close_gate.py 判（本脚本不抄判据）
#
# 三条边界（都来自现网事故，不是理论）：
#   B-N5  看门狗只动**本机**的 QMT 与本机网关，对广州任何服务零权限——
#         这里不出现远程计算机名、不出现 Invoke-Command/PSRemoting、不出现停广州服务的动词。
#   B-N7  关闭门不开就**不关**：A 股委托收盘即失效，硬关不会带来交易风险，
#         但会把没收敛的非终态行留在别人家里过夜 —— 2026-10-30 那两笔「撤单中」挂住 3.67 万
#         战略额度的形状，只是这次没人核对。宁可整晚开着，也不带未收敛的账关门。
#   10-29 每次在会话里直接 Start-Process 拉起 QMT 会随会话被杀 ⇒ 客户端只走计划任务；
#         自愈次序固定为"先停网关（否则 xt 重连风暴占满 writer 槽拖崩客户端）→ 计划任务起客户端 → 再起网关"。
#   每一跳都要 try/catch（§OBS-GUARD：守卫只挂循环顶部只兜第一次，之后循环静默停摆）。
#
# 判读锚点（纯 ASCII）：
#   WD mode=<Mode> action=<started|already|held|closed|retry|abort> reason=<ascii_code>
#   CLOSE_GATE allowed=0|1 reason=... blocked_n=... stuck_n=... over_limit=0|1   （由 close_gate.py 自己打）
#
# 用法（计划任务调用示例）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File watchdog.ps1 -Mode ClientEnsure
#   powershell -NoProfile -ExecutionPolicy Bypass -File watchdog.ps1 -Mode Close

param(
    [Parameter(Mandatory = $true)][ValidateSet("ClientEnsure", "GatewayEnsure", "Close")][string]$Mode,
    [string]$GatewayDir   = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe    = "C:\Python312\python.exe",
    [string]$QmtExe       = "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe",
    [string]$ClientTask   = "QMTF-Client-Start",
    [string]$GatewayTask  = "QMTF-Gateway-Start",
    [int]$GatewayPort     = 8789,
    [int]$HoldLimitSec    = 3600
)

$ErrorActionPreference = "Stop"

function Anchor($action, $reason) {
    # 只打 ASCII：中文经 PowerShell 管道会被码页换成 `?`（11-02 现网两次假象同源），
    # 上层按锚点判读时，带中文的锚点会**永远不匹配**而动作照样执行。
    Write-Host ("WD mode={0} action={1} reason={2}" -f $Mode, $action, $reason)
}

function Test-TradingDay {
    # 交易日判据的唯一真源是 qmt_gateway/trading_calendar.py（与 Go 的 calendarCacheFilePath 同口径）。
    # 这里绝不再判一遍周末/节假日：那是第三份日历，漂开时没人红，代价是休市日白开柜台会话、
    # 或交易日整天不动（远端"没下单"最容易被误读成网络问题，11-02 的 J0 同族教训）。
    # 日历文件不可得时该模块自己退化成"只看周末"并打一条日志——那是可见的降级，不是静默。
    $tc = Join-Path $GatewayDir "trading_calendar.py"
    if (-not (Test-Path $tc)) { return $false }
    $eapT = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    & $PythonExe $tc 2>$null | Out-Null
    $ErrorActionPreference = $eapT
    return ($LASTEXITCODE -eq 0)
}

function Invoke-Guarded([scriptblock]$body, [string]$tag) {
    # 每一跳独立守卫：panic/异常只让这一跳失败，绝不让整个循环停摆（§OBS-GUARD 的成因形状）
    try {
        & $body
    } catch {
        Write-Host ("WD mode={0} action=abort reason={1}_exception {2}" -f $Mode, $tag, $_.Exception.Message)
    }
}

# —— 端口是否已在监听（只读本机，不做任何远程探测）——
function Test-PortListening([int]$port) {
    $conns = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
    return [bool]($conns)
}

# PYTHON_EXE from friend.env (schtasks /TR is capped at 261 chars)
$envFileWd = Join-Path $GatewayDir "friend.env"
if (Test-Path $envFileWd) {
    foreach ($lnWd in (Get-Content $envFileWd)) {
        if ($lnWd -match "^PYTHON_EXE=(.+)$") { $PythonExe = $Matches[1].Trim() }
    }
}

switch ($Mode) {
    "ClientEnsure" {
        Invoke-Guarded {
            if (-not (Test-TradingDay)) { Anchor "abort" "not_trading_day"; exit 0 }
            $running = Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -eq "XtItClient" }
            if ($running) { Anchor "already" "client_running"; exit 0 }
            # 客户端只走计划任务：会话内直起会随会话被杀（10-29 实证），任务本身由 install_tasks.ps1 注册
            schtasks /Run /TN $ClientTask | Out-Null
            Anchor "started" "via_scheduled_task"
        } "client"
    }

    "GatewayEnsure" {
        Invoke-Guarded {
            if (-not (Test-TradingDay)) { Anchor "abort" "not_trading_day"; exit 0 }
            if (Test-PortListening $GatewayPort) { Anchor "already" "port_listening"; exit 0 }
            $cfg = Join-Path $GatewayDir "config.xt.json"
            $gw = Join-Path $GatewayDir "gateway.py"
            if (-not (Test-Path $cfg) -or -not (Test-Path $gw)) { Anchor "abort" "missing_config_or_gateway"; exit 0 }

            # 起服务之前先跑出厂档自证（判据真源在 gateway.py，本脚本不复制任何规则）
            $envFile = Join-Path $GatewayDir "friend.env"
            if (Test-Path $envFile) {
                foreach ($ln in (Get-Content $envFile)) {
                    if ($ln -match "^QUANT_GATEWAY_BIND=(.+)$") { $env:QUANT_GATEWAY_BIND = $Matches[1].Trim() }
                    if ($ln -match "^ALLOWED_IPS=(.+)$") { $env:ALLOWED_IPS = $Matches[1].Trim() }
                }
            }
            $eapG = $ErrorActionPreference
            $ErrorActionPreference = "Continue"
            $pyOut = & $PythonExe $gw -c $cfg --profile-check 2>&1
            $pyOut | ForEach-Object { Write-Host $_ }
            $ErrorActionPreference = $eapG
            if ($LASTEXITCODE -ne 0) { Anchor "abort" "profile_check_failed_exit_$LASTEXITCODE"; exit 2 }

            # 拉起走计划任务（与客户端同一条纪律：不随某个会话的存活而亡）
            schtasks /Run /TN $GatewayTask | Out-Null
            Anchor "started" "gateway_via_scheduled_task"
        } "gateway"
    }

    "Close" {
        Invoke-Guarded {
            if (-not (Test-TradingDay)) { Anchor "abort" "not_trading_day"; exit 0 }
            $day = (Get-Date -Format "yyyyMMdd")
            # 15:30 只是"最早可尝试时刻"：真实门槛是 close_gate 的三条收敛判据
            $first = (Get-Date).Date.AddHours(15).AddMinutes(30)
            $elapsed = [int]((Get-Date) - $first).TotalSeconds
            if ($elapsed -lt 0) { Anchor "retry" "before_1530"; exit 0 }

            $cfg = Join-Path $GatewayDir "config.xt.json"
            if (-not (Test-Path $cfg)) { Anchor "abort" "missing_config"; exit 0 }
            # 本机只读取数（回环/overlay 自己），不触碰任何远程服务
            $st = (Invoke-RestMethod -Uri "http://127.0.0.1:$GatewayPort/admin/status" -TimeoutSec 10)
            $od = (Invoke-RestMethod -Uri "http://127.0.0.1:$GatewayPort/orders?day=$day" -TimeoutSec 15)
            $rows = $null
            if ($od.ok) { $rows = @($od.orders) }
            $payload = @{ admin_status = $st; orders = $rows; day = $day; elapsed_sec = $elapsed; hold_limit_sec = $HoldLimitSec } | ConvertTo-Json -Depth 8

            $cg = Join-Path $GatewayDir "close_gate.py"
            $eapC = $ErrorActionPreference
            $ErrorActionPreference = "Continue"
            $out = ($payload | & $PythonExe $cg 2>&1)
            $ErrorActionPreference = $eapC
            $rc = $LASTEXITCODE
            Write-Host ($out | Out-String).Trim()
            if ($rc -eq 0) {
                Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -eq "XtItClient" } | Stop-Process -Force
                Anchor "closed" "gate_open"
                exit 0
            }
            if ($rc -eq 20 -or $rc -eq 30) {
                # 等过上限 / 读数拿不到 ⇒ 仍然不关，但要转人工：
                # 通知不在这里发（远端腿的告警一律归批 5 的 friend_leg_state + 双收件人，
                # 免得更早的"观测腿顺手长了资本动作"那种越界）
                Anchor "held" "give_up_exit_$rc"
                exit 0
            }
            Anchor "retry" "gate_held_exit_$rc"
        } "close"
    }
}
