# uninstall.ps1 —— 一键卸载朋友机器上的执行腿（§FRIEND-PROFILE 批 3，owner 令）
#
# 删除面写死（B-N9）：只动「服务 + 计划任务 + 配置 + 钥匙 + 本目录脚本」。
#   保留：data.db / gateway-*.log / bridge_*.jsonl / friend_ledger_*.zip / .pre-friendcfg-*
#   ——Q9 之后**账本真源就在这台机器上**，卸载顺手删库等于把唯一的凭据销毁；
#     要连账本一起清必须显式 -PurgeLedger 且输入 confirm 串，这是"人工决定"不是"手滑"。
# 前置硬门槛（B-N11）：必须先 export_ledger.ps1 成功；导出失败（库坏/撕裂/重名）一律拒绝继续。
param(
    [string]$GatewayDir  = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe   = "C:\Python312\python.exe",
    [string]$Uid         = "",
    [string]$OutDir      = "$env:USERPROFILE\Desktop",
    [switch]$PurgeLedger,
    [string]$Confirm     = "",
    [switch]$Apply
)

$ErrorActionPreference = "Stop"

function Abort($reason) {
    Write-Host "UNINSTALL_RESULT=ABORT reason=$reason"
    exit 2
}

$here = $PSScriptRoot
$today = (Get-Date -Format "yyyyMMdd")

# —— 1) 证据先落地：导出失败就不许往下拆（这一步不是"建议"，是门槛）——
& (Join-Path $here "export_ledger.ps1") -GatewayDir $GatewayDir -PythonExe $PythonExe -OutDir $OutDir -Uid $Uid -Day $today
if ($LASTEXITCODE -ne 0) { Abort ("export_failed_exit_{0}" -f $LASTEXITCODE) }

if (-not $Apply) {
    Write-Host "UNINSTALL_RESULT=DRYRUN would=stop_leg,delete_tasks,delete_config,keep_ledger"
    exit 0
}

# —— 2) 停腿（先停腿再拆任务，顺序不能反：腿还活着时删任务会被重建）——
& (Join-Path $here "stop_leg.ps1") -GatewayDir $GatewayDir -PythonExe $PythonExe
if ($LASTEXITCODE -ne 0) { Abort ("stop_leg_failed_exit_{0}" -f $LASTEXITCODE) }

# —— 3) 删四个计划任务（走 install_tasks 的卸载分支，任务名不在这儿抄第二份）——
& (Join-Path $here "install_tasks.ps1") -Uninstall -Apply -GatewayDir $GatewayDir
if ($LASTEXITCODE -ne 0) { Abort ("tasks_delete_failed_exit_{0}" -f $LASTEXITCODE) }

# —— 4) 删配置与钥匙（含 .pre-* 之外的所有凭据载体），账本与日志一律不动 ——
$targets = @("config.xt.json", "friend.env", "config.bridge.json")
foreach ($t in $targets) {
    $p = Join-Path $GatewayDir $t
    if (Test-Path $p) { Remove-Item $p -Force; Write-Host ("DELETED file={0}" -f $t) }
}

# —— 5) 可选清账本：必须显式 -PurgeLedger + confirm 串（形如 PURGE-<uid>-<日期>）——
if ($PurgeLedger) {
    if (-not $Uid) { Abort "purge_needs_uid" }
    $want = "PURGE-{0}-{1}" -f $Uid, $today
    # 锚点必须纯 ASCII（中文经 PS 管道会变 ? ⇒ 上层判读恒假），提示语放下一行给人看
    if ($Confirm -ne $want) { Write-Host ("HINT: 要清账本请输入 confirm={0}" -f $want); Abort "confirm_mismatch" }
    foreach ($p in @("data.db", "bridge_report.jsonl", "bridge_report_dead.jsonl", "bridge_seen.jsonl")) {
        $f = Join-Path $GatewayDir $p
        if (Test-Path $f) { Remove-Item $f -Force; Write-Host ("PURGED file={0}" -f $p) }
    }
    Write-Host "UNINSTALL_LEDGER_PURGED=1"
}

Write-Host "UNINSTALL_RESULT=OK ledger_kept=$(if ($PurgeLedger) { 0 } else { 1 }) export_dir=$OutDir"
