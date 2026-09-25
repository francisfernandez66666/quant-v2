# gateway_watchdog.ps1 — 【已退役旧径，仅留灾备逃生门】qmt_gateway 直起守护循环。
# ⚠ §C7-OPS（2026-09-26，FIX_PLAN_20260925EVE ⑯ 单径化）：本脚本原头部自带
#   `schtasks /Create /TN qmt-gateway-wd /SC ONSTART /RU SYSTEM` 安装说明——那是首尔→广州
#   网络分离期的旧形态，与 08-31 实障裁决直接冲突（register_service.ps1 / CHECKLIST.md
#   常驻化段：xtquant 与 QMT 客户端经按会话隔离的共享内存通信，SYSTEM/Session 0 实例
#   永远连不上柜台，且会抢占 8789 让真网关起不来）。现网网关的唯一守护径 =
#   交互会话计划任务 QMT-Gateway-Ensure / QMT-Gateway-Logon（任务名取
#   service_definitions.ps1 $SvcTaskGatewayEnsure/$SvcTaskGatewayLogon）。
#   本脚本因此加了两道硬闸，未触发闸时行为与旧版一致（直起 + 3s 重启循环）：
#     ① Session 0 硬闸：检测到以服务/SYSTEM 会话运行 → 拒绝拉起网关并以非零码退出；
#     ② 绑定面：默认仅回环（127.0.0.1:$ProbeGatewayPort）。QUANT_GATEWAY_BIND=0.0.0.0
#        收敛逻辑（gateway.py:1877-1888）+ install_guangzhou.ps1:131-132 防火墙 8789
#        remoteip=127.0.0.1 + MIGRATION_GUANGZHOU_ALLINONE §3.5/R2 裁决（移除首尔 IP 白名单）
#        ⇒ 旧版写死 0.0.0.0:8789 + ALLOWED_IPS 含 43.108.86.140 的公网形态仅在显式
#        -AllowPublicBind 时保留（历史逃生门，用完请回单径）。
# 用法（仅灾备：交互会话、管理员已登录桌面时手工运行）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File gateway_watchdog.ps1
# 全文件保持 UTF-8 BOM——Windows PowerShell 5.1 无 BOM 时按 GBK 读取中文会乱码。
param(
    [switch]$AllowPublicBind   # 旧公网形态逃生门（0.0.0.0 绑定 + 首尔出口白名单）；缺省=仅回环
)
$ErrorActionPreference = "Continue"

# §C7-OPS：网关目录/python 路径/端口/任务名收编进 service_definitions.ps1（缺失回退旧字面量并告警）。
$svcDefs = $null
foreach ($cand in @((Join-Path $PSScriptRoot "service_definitions.ps1"),
                    "C:\opt\quant\qmt-win\service_definitions.ps1")) {
    if (Test-Path $cand) { . $cand; $svcDefs = $cand; break }
}
if (-not $svcDefs) {
    Write-Host "[gw-wd] WARN: service_definitions.ps1 未找到，路径/端口回退旧字面量（§C7 单径脱钩，请补齐部署清单）"
    $SvcGatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway"
    $SvcPythonExe  = "C:\Python312\python.exe"
    $ProbeGatewayPort = 8789
    $SvcTaskGatewayEnsure = "QMT-Gateway-Ensure"
}

# ── 硬闸①：Session 0 / 服务会话禁起网关（08-31 实障裁决，见文件头）────────────────
$thisSessionId = (Get-Process -Id $PID).SessionId
if ($thisSessionId -eq 0) {
    Write-Host "[gw-wd] REFUSED: 检测到 Session 0（SYSTEM/服务会话）——此处起的网关永远连不上券商柜台，" -ForegroundColor Red
    Write-Host "[gw-wd]   且会抢占 :$ProbeGatewayPort 挡死真网关。网关唯一守护径＝交互会话计划任务 $SvcTaskGatewayEnsure。" -ForegroundColor Red
    Write-Host "[gw-wd]   失联修复（RUNBOOK_QMT_DAILY.md §网关失效）：schtasks /change /tn $SvcTaskGatewayEnsure /enable ; schtasks /run /tn $SvcTaskGatewayEnsure" -ForegroundColor Red
    exit 1
}

$gw  = Join-Path $SvcGatewayDir ""
$log = Join-Path $gw "gateway.log"
# ── python 解析：单源在位则用；否则回退 PATH（旧版硬编码 C:\Python312\python.exe）──
$py = $SvcPythonExe
if (-not (Test-Path $py)) {
    $pyCmd = Get-Command python -ErrorAction SilentlyContinue
    if ($pyCmd) { $py = $pyCmd.Source }
}

function Log($m) {
    Add-Content -Path $log -Value ("[gw-wd(legacy) {0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $m)
}

Log "watchdog started (pid=$PID session=$thisSessionId defs=$svcDefs)"
# ── 硬闸②：绑定面与来源白名单（缺省仅回环；-AllowPublicBind 复刻旧公网形态）────────
if ($AllowPublicBind) {
    # 历史形态（首尔→广州跨公网下单时代）：显式放行公网绑定，否则 gateway.py 会把
    # config 里的 0.0.0.0 自动收敛为 127.0.0.1。R2 裁决后不再默认启用，仅保留入口。
    $env:QUANT_GATEWAY_BIND = "0.0.0.0:$ProbeGatewayPort"
    $env:ALLOWED_IPS = "$($SvcLegacySeoulEgressIp),127.0.0.1"
    Log "public bind LEGACY enabled (QUANT_GATEWAY_BIND=0.0.0.0, ALLOWED_IPS includes seoul egress)"
} else {
    $env:QUANT_GATEWAY_BIND = "127.0.0.1:$ProbeGatewayPort"
    $env:ALLOWED_IPS = "127.0.0.1"
}
while ($true) {
    Log "launching gateway..."
    # 网关自管文件日志（gateway-<pid>.log，UTF-8 轮转）；此处不再做外部重定向——
    # 旧式外部重定向 *>> 在 Windows 产生 UTF-16 文件且句柄被假死实例长期持有，新实例日志全部丢失。
    & $py (Join-Path $gw "gateway.py") "-c" (Join-Path $gw "config.xt.json")
    Log ("gateway exited code=" + $LASTEXITCODE + " - restarting in 3s")
    # 旧式外部重定向日志只保留一代 .old 供历史排查，网关自身日志已轮转无需在此处理
    if ((Test-Path $log) -and ((Get-Item $log).Length -gt 20MB)) {
        Move-Item -Force $log ($log + ".old")
        Log "log rotated"
    }
    Start-Sleep -Seconds 3
}
