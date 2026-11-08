# enroll.ps1 —— 朋友机器的装填向导（§FRIEND-PROFILE 批 3，owner 令：压缩包 + 安装时鉴权）
#
# 流程（缺省只检视，-Apply 才落盘/装任务/起服务）：
#   1) 用现有 `POST /api/auth/login` 鉴权（不新增任何服务端路由）；口令错/账号到期/停用
#      的分码早已收口（401 invalid credentials vs 403 account_expired|account_disabled），
#      这里只如实转达，不自己造第二套话术；
#   2) 断言该账号有实盘读权限（`quant_trade` 或 `trading_admin`）——**未授权＝无服务（B-N4）**，
#      直接 ABORT，绝不"先装上再说"；
#   3) 朋友粘贴钥匙（`-Issue` 线下交付；`/api/config/qmt` 是脱敏的，向导拿不到也不该拿到）；
#   4) 交给 ensure_friend_gateway_config.ps1 生成配置并跑出厂档自证（判据真源在网关）；
#   5) 装四个计划任务；
#   6) **双向连通自证**（明细 §2.1 的校验项 11）：本机网关 /health 起来 + 用会话 token 调一次
#      `GET /api/qmt/state`，比对返回的 account 尾号与本机配置一致 ⇒ 才承认"这把钥匙两处都登记对了"。
#      不一致就 ABORT——漏一侧的现场形态是"上报进死信、页面显示今天没交易"，比装不上更贵。
#
# 锚点纯 ASCII；钥匙只在内存里流过一次，不回显、不写日志、不进命令行参数。
param(
    [string]$BaseUrl    = "https://quant-trading.top",
    [Parameter(Mandatory = $true)][string]$Username,
    [Parameter(Mandatory = $true)][string]$Account,
    [Parameter(Mandatory = $true)][string]$OverlayIP,
    [Parameter(Mandatory = $true)][string]$DecisionIP,
    [string]$UserId     = "",
    [string]$GatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe  = "C:\Python312\python.exe",
    [string]$QmtExe     = "",
    [switch]$Apply
)

$ErrorActionPreference = "Stop"

function Abort($reason) {
    Write-Host "ENROLL_RESULT=ABORT reason=$reason"
    exit 2
}
function Mask($s) {
    if (-not $s) { return "" }
    $t = [string]$s
    if ($t.Length -le 6) { return "len=$($t.Length)" }
    return "tail6=" + $t.Substring($t.Length - 6)
}

# —— 1) 登录（口令交互输入，不落命令行）——
$sec = Read-Host -Prompt "login password" -AsSecureString
$plain = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
    [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec))
$body = @{ username = $Username; password = $plain } | ConvertTo-Json
$plain = $null
try {
    $login = Invoke-RestMethod -Uri "$BaseUrl/api/auth/login" -Method Post -ContentType "application/json" `
        -Body $body -TimeoutSec 20
} catch {
    # 401 与 403 的区分是产品口径（§FIX-20261028-F04），向导必须原样传达到人，不许笼统说"登录失败"
    $resp = $_.Exception.Response
    if ($resp -and $resp.StatusCode.value__) { Abort ("login_http_{0}" -f $resp.StatusCode.value__) }
    Abort "login_failed"
}
if (-not $login.token) { Abort "login_no_token" }
if ($UserId -and $login.id -and $UserId -ne $login.id) { Abort "user_id_mismatch" }
if (-not $UserId) { $UserId = $login.id }

# —— 2) 权限断言：没有实盘读权限就没有服务（B-N4）——
$permHit = ""
foreach ($p in @("quant_trade", "trading_admin")) {
    if (@($login.perms) -contains $p) { $permHit = $p; break }
}
if (-not $permHit) { Abort "no_live_perm" }
Write-Host ("ENROLL step=perm_ok perm={0} uid={1}" -f $permHit, $UserId)

# —— 3) 钥匙：交互输入，只用于写配置与连通自证——
$ksec = Read-Host -Prompt "gateway token (from owner -Issue)" -AsSecureString
$token = [Runtime.InteropServices.Marshal]::PtrToStringAuto(
    [Runtime.InteropServices.Marshal]::SecureStringToBSTR($ksec))
if ($token.Length -lt 16) { Abort "token_short" }
Write-Host ("ENROLL step=token_read {0}" -f (Mask $token))

$here = $PSScriptRoot
$cfgScript = Join-Path $here "ensure_friend_gateway_config.ps1"
$tasksScript = Join-Path $here "install_tasks.ps1"
if (-not (Test-Path $cfgScript)) { Abort "no_config_script" }

if (-not $Apply) {
    Write-Host ("ENROLL step=plan account_tail={0} overlay={1} decision={2} apply=0" -f `
        $Account.Substring([Math]::Max(0, $Account.Length - 4)), $OverlayIP, $DecisionIP)
    Write-Host "ENROLL_RESULT=DRYRUN"
    exit 0
}

# —— 4) 落配置（写前备份 + 出厂档自证，判据在 gateway.py）——
# 钥匙必须走 stdin：ensure_friend_gateway_config.ps1 从 [Console]::In 读。
# 不能用管道（$token | & ...），管道对象不会进子脚本的 stdin，会触发参数绑定失败。
$tmpTok = Join-Path $env:TEMP ("qmt-friend-tok-" + $UserId + ".txt")
[System.IO.File]::WriteAllText($tmpTok, $token)
# PS5.1 has no "< file" input redirection; drive ensure via cmd /c and merge stderr.
$cmd = ("powershell -NoProfile -ExecutionPolicy Bypass -File `"{0}`" -Account {1} -UserId {2} -OverlayIP {3} -DecisionIP {4} -GatewayDir `"{5}`" -PythonExe `"{6}`" -Apply < `"{7}`" 2>&1" -f `
    $cfgScript, $Account, $UserId, $OverlayIP, $DecisionIP, $GatewayDir, $PythonExe, $tmpTok)
$out = cmd /c $cmd 2>&1
$out | ForEach-Object { Write-Host $_ }
Remove-Item $tmpTok -Force -ErrorAction SilentlyContinue
if ($LASTEXITCODE -ne 0) { Abort ("config_failed_exit_{0}" -f $LASTEXITCODE) }

# —— 5) 装计划任务 ——
$taskArgs = @{ Apply = $true; GatewayDir = $GatewayDir }
if ($QmtExe) { $taskArgs["QmtExe"] = $QmtExe }
if ($PythonExe) { $taskArgs["PythonExe"] = $PythonExe }
& $tasksScript @taskArgs
if ($LASTEXITCODE -ne 0) { Abort ("tasks_failed_exit_{0}" -f $LASTEXITCODE) }

# —— 6) 双向连通自证 ——
# 6a 本机网关活着且身份自证
#     先经"纯拉起"任务（QMTF-Gateway-Start：无交易日闸门、不递归）把网关进程真正拉起来；
#     再探本机 overlay 地址的 /health。该地址已在 ALLOWED_IPS 里（本机自探），故返回 200。
#     网关绑定在 overlay 地址、回环并未监听，故不能用 127.0.0.1 探；/health 免鉴权，无需 token 头。
schtasks /Run /TN "QMTF-Gateway-Start" | Out-Null
# Store-python cold start can exceed 8s; retry instead of a single shot.
$h = $null
foreach ($tryi in 1..5) {
    Start-Sleep -Seconds 6
    try {
        $h = Invoke-RestMethod -Uri "http://$OverlayIP`:8789/health" -TimeoutSec 8
        break
    } catch { }
}
if (-not $h) { Abort "local_health_unreachable" }
if (-not $h.ok) { Abort "local_health_not_ok" }
if ($h.broker -and $h.broker -ne "queued") { Abort "broker_kind_unexpected" }

# 6b 广州侧那把钥匙归属对不对：拿该账号的实盘状态，比 account 尾号。
#     尾号一致才算"两处登记同指一把"；403 说明授权没到位（结构性不能下单，正是要的效果）。
try {
    $st = Invoke-RestMethod -Uri "$BaseUrl/api/qmt/state" -TimeoutSec 20 `
        -Headers @{ Authorization = ("Bearer {0}" -f $login.token) }
} catch {
    $code = $null
    if ($_.Exception.Response -and $_.Exception.Response.StatusCode.value__) {
        $code = $_.Exception.Response.StatusCode.value__
    }
    Abort ("state_probe_http_{0}" -f $code)
}
$remoteTail = ""
if ($st.account) { $remoteTail = ([string]$st.account).Substring([Math]::Max(0, ([string]$st.account).Length - 4)) }
$localTail = $Account.Substring([Math]::Max(0, $Account.Length - 4))
if ($remoteTail -and $remoteTail -ne $localTail) { Abort "account_tail_mismatch_two_registry" }

Write-Host ("ENROLL step=two_registry_selfcheck account_tail={0} state_connected={1}" -f `
    $localTail, [bool]$st.connected)
Write-Host "ENROLL_RESULT=OK uid=$UserId"
