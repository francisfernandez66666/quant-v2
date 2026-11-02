# stop_leg.ps1 —— 停掉本机执行腿并清空"已写给桥还没执行"的清单（§FRIEND-PROFILE 批 3）
#
# 为什么"清 bridge_cmd.json"不是多余一步：撤销动作的真实顺序是
#   先停腿 → 再清待发 → 才注销钥匙（明细 §9.3 的规则 1）。
# 只停服务不清这个文件，那台机器下次起来会把旧指令再执行一遍——
# 钥匙都撤了单照下，正是 B-N4「未登录＝结构性不能下单」的反面。
# 计数与原子写都在 leg_ops.py（真源），本脚本只调用。
param(
    [string]$GatewayDir  = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe   = "C:\Python312\python.exe",
    [string]$GatewayTask = "QMTF-Gateway-Ensure",
    [string]$CloseTask   = "QMTF-Close",
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

# 1) 先把守护任务禁掉：否则"停进程 → 5 分钟后被自动拉起"，腿根本停不住
foreach ($t in @($GatewayTask, $CloseTask)) {
    if ($DryRun) { Write-Host "STOPLEG step=disable_task task=$t"; continue }
    schtasks /Change /TN $t /DISABLE | Out-Null
    Write-Host "STOPLEG step=disabled task=$t"
}

# 2) 停网关进程（只按本机命令行匹配，绝不碰远程机器：B-N5）
$procs = Get-CimInstance Win32_Process -Filter "Name='python.exe'" -ErrorAction SilentlyContinue |
         Where-Object { $_.CommandLine -and $_.CommandLine -match "gateway\.py" }
if (-not $procs) { Write-Host "STOPLEG step=no_process reason=not_running" }
foreach ($p in $procs) {
    if ($DryRun) { Write-Host ("STOPLEG step=kill pid={0}" -f $p.ProcessId); continue }
    Stop-Process -Id $p.ProcessId -Force
    Write-Host ("STOPLEG step=killed pid={0}" -f $p.ProcessId)
}

# 3) 清空待发命令文件 + 报剩余未交付行数（真源在 leg_ops）
$ops = Join-Path $GatewayDir "leg_ops.py"
$cmd = Join-Path $GatewayDir "bridge_cmd.json"
$db  = Join-Path $GatewayDir "data.db"
if (-not (Test-Path $ops)) {
    Write-Host "STOPLEG_RESULT=ABORT reason=no_leg_ops"
    Write-Host "HINT: 判据真源缺失时不许只停服务就往下走（清命令文件这一步会整条漏掉）"
    exit 30
}
if ($DryRun) {
    Write-Host ("STOPLEG step=clear_cmd_file file={0}" -f $cmd)
    Write-Host "STOPLEG_RESULT=DRYRUN"
    exit 0
}
& $PythonExe $ops stop-cmd --cmd $cmd --db $db
if ($LASTEXITCODE -ne 0) {
    Write-Host "STOPLEG_RESULT=ABORT reason=clear_failed exit=$LASTEXITCODE"
    exit $LASTEXITCODE
}
Write-Host "STOPLEG_RESULT=OK next=rotate_token_revoke"
Write-Host "HINT: 下一步才是注销钥匙（顺序反了＝钥匙没了、旧指令还在这台机器上等执行）"
