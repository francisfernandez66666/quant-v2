# start_gateway.ps1 —— 朋友机器网关纯拉起器（§FRIEND-PROFILE 批 2 补，2026-11-08）
#
# 为什么单独一个文件（而不是看门狗 /Run 自己）：watchdog GatewayEnsure 只负责"判端口 + 跑自证 +
# 触发拉起"，真正的拉起必须是一个**独立**的计划任务（QMTF-Gateway-Start），与 QMTF-Client-Start
# 同形——会话内直起会随会话被杀（10-29 实证），任务拉起的进程才活得久。
#
# 职责（极简，不抄任何业务判据）：
#   1) 读 friend.env 注入 QUANT_GATEWAY_BIND / ALLOWED_IPS 给子进程（gateway.py 据此绑定 overlay
#      地址、判来源白名单；缺了就收敛回 127.0.0.1，变成"只上报不收单"的半条命形态）；
#   2) 起服务之前先跑出厂档自证（判据真源在 gateway.py）；
#   3) 已在监听则幂等跳过；否则 Start-Process gateway.py（脱离当前 shell，进程随任务结束不亡）；
#   4) 不随交易日闸门——装填器自证与看门狗兜底都要能随时拉起。
#
# 判读锚点（纯 ASCII）：GATEWAY_START_RESULT=ALREADY|STARTED|ABORT reason=ascii
#
# 用法（计划任务 QMTF-Gateway-Start 调用）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File start_gateway.ps1 -GatewayDir "C:\qmt\..."

param(
    [string]$GatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe  = "C:\Python312\python.exe"
)

$ErrorActionPreference = "Stop"

function Anchor($action, $reason) {
    # 只打 ASCII：中文经 PowerShell 管道会被码页换成 `?`（11-02 现网两次假象同源）
    Write-Host ("GATEWAY_START_RESULT=$action reason=$reason")
}

$cfg = Join-Path $GatewayDir "config.xt.json"
$gw  = Join-Path $GatewayDir "gateway.py"
if (-not (Test-Path $cfg)) { Anchor "ABORT" "no_config"; exit 2 }
if (-not (Test-Path $gw))  { Anchor "ABORT" "no_gateway"; exit 2 }

# 注入 friend.env（QUANT_GATEWAY_BIND / ALLOWED_IPS）给即将拉起的子进程；
# 子进程继承本脚本的环境，故先设好再 Start-Process。
$envFile = Join-Path $GatewayDir "friend.env"
if (Test-Path $envFile) {
    foreach ($ln in (Get-Content $envFile)) {
        if ($ln -match "^QUANT_GATEWAY_BIND=(.+)$") { $env:QUANT_GATEWAY_BIND = $Matches[1].Trim() }
        if ($ln -match "^ALLOWED_IPS=(.+)$")        { $env:ALLOWED_IPS = $Matches[1].Trim() }
        if ($ln -match "^PYTHON_EXE=(.+)$")         { $PythonExe = $Matches[1].Trim() }
    }
}

# 已在监听则幂等跳过（看门狗每 5 分钟兜底，不重复拉）
$port = 8789
$listening = Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
if ($listening) { Anchor "ALREADY" "port_listening"; exit 0 }

# 起服务之前先跑出厂档自证（判据真源在 gateway.py，本脚本不复制任何规则）
# PS5.1: wrap the native call so stderr cannot kill the script under EAP=Stop.
$eapG = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$pyOut = & $PythonExe $gw -c $cfg --profile-check 2>&1
$pyOut | ForEach-Object { Write-Host $_ }
$ErrorActionPreference = $eapG
if ($LASTEXITCODE -ne 0) { Anchor "ABORT" "profile_check_exit_$LASTEXITCODE"; exit 2 }

# 拉起（不随当前 shell 亡：Start-Process 脱离，进程独立存活；看门狗每 5 分钟兜底）
Start-Process -FilePath $PythonExe -ArgumentList @($gw, "-c", $cfg) -WindowStyle Hidden -WorkingDirectory $GatewayDir
Anchor "STARTED" "port=$port"
