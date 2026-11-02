# export_ledger.ps1 —— 把本机账本出一份**可信**证据包（§FRIEND-PROFILE 批 3）
#
# 职责：只做"调真源 + 按退出码分流"，判据（integrity、行数、撕裂页）全在 qmt_gateway/leg_ops.py。
#       为什么必须这样：这是 B-N11 的门槛——换机/重装/卸载三类动作前，导出必须成功；
#       判据一旦在这里重写一遍，"看起来导出成功"和"导出的东西能用"就会分叉，
#       而分叉只有在那台机器真的坏了的那天才暴露。
#
# 退出码（与 leg_ops 同义，不另立）：
#   0  = 证据包已产出
#   30 = 库不可读 / 校验不过 / 重名拒绝覆盖  ⇒ **调用方必须停手**
param(
    [string]$GatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe  = "C:\Python312\python.exe",
    [string]$OutDir     = "$env:USERPROFILE\Desktop",
    [string]$Uid        = "",
    [string]$Day        = "",
    [switch]$Force
)

$ErrorActionPreference = "Stop"

$db  = Join-Path $GatewayDir "data.db"
$ops = Join-Path $GatewayDir "leg_ops.py"
if (-not (Test-Path $ops)) {
    Write-Host "LEDGER_EXPORT ok=0 reason=no_leg_ops path=$ops"
    exit 30
}
if (-not $Day) { $Day = (Get-Date -Format "yyyyMMdd") }
if (-not $Uid) { $Uid = "uid" }
if (-not (Test-Path $OutDir)) { New-Item -ItemType Directory -Path $OutDir -Force | Out-Null }
$out = Join-Path $OutDir ("friend_ledger_{0}_{1}.zip" -f $Uid, $Day)

$args1 = @($ops, "export", "--db", $db, "--report-dir", $GatewayDir, "--out", $out, "--day", $Day)
if ($Force) { $args1 += "--force" }
& $PythonExe @args1
$rc = $LASTEXITCODE
if ($rc -ne 0) {
    # 硬门槛：体检/导出没过就回非 0，调用方（uninstall/换机流程）必须停手。
    # 宁可不装也不出一份用不了的证据——Q9 之后广州不留副本，这份包是唯一的凭据。
    Write-Host "EXPORT_RESULT=REFUSED exit=$rc out=$out"
    exit $rc
}
Write-Host "EXPORT_RESULT=OK out=$out"
