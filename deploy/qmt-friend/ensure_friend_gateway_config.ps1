# ensure_friend_gateway_config.ps1 —— 朋友机器网关配置的生成与自证（§FRIEND-PROFILE，2026-11-02 批 1）
#
# 职责：把 config.xt.friend.template.json 的占位符换成这台机器自己的四元
#       （资金账号 / 归属账号 / 一把钥匙 / overlay 地址），落盘前备份、落盘后**让网关自己判一次**。
#
# 为什么判定不在本脚本里：validate_friend_profile() 的真源在 qmt_gateway/gateway.py。
#   PowerShell 再抄一份就是两把尺子——漂开的那次不会红，只会变成"配置看起来合规、账记到别人名下"
#   （§INDEX-BLIND 那批"两份段序表"的同形教训：卡上三条、实际四条）。
#   所以本脚本只做"形态校验 + 落盘 + 调 --profile-check + 失败回滚"，不复制任何业务判据。
#
# 关键边界：
#   1) 缺省 **只检视不写盘**（dry-run），必须显式 -Apply 才落盘（对齐 §TOKEN-PERAACCT 里
#      rotate_qmt_token.ps1 -Align 的"缺省 written=0"纪律）；
#   2) 钥匙只从 **stdin** 读，全程不回显、不进日志、不进命令行参数（命令行参数会被留在
#      进程列表与 PowerShell 转录里）；
#   3) 写盘前落 .pre-friendcfg-<ts> 备份，--profile-check 不过就**回滚到原文件**——
#      绝不留"新配置写坏了但服务照起"的半态；
#   4) 落盘用无 BOM UTF-8（网关 json.load 不容 BOM），NTFS ACL 立刻收敛到 Administrators+SYSTEM；
#   5) 写一份 friend.env（QUANT_GATEWAY_BIND / ALLOWED_IPS），供批 2 的看门狗拉起网关时带上——
#      这两个环境变量缺失时网关会把监听收敛回 127.0.0.1，变成"只上报不收单"的半条命形态。
#
# 用法（钥匙走 stdin，不回显）：
#   'tk-xxxxxxxx' | .\ensure_friend_gateway_config.ps1 -Account 2069008957 -UserId u_friend_001 `
#       -OverlayIP 10.77.0.2 -DecisionIP 10.77.0.1 -Apply
#
# 判读锚点（纯 ASCII，中文经管道会被码页换成 ? ⇒ 上层一律按这些行判）：
#   FRIENDCFG apply=0|1 written=0|1 account_tail=XXXX owner=XXXXXXXX listen=...
#   PROFILE_CHECK=PASS ...        （由 gateway.py 自己打）
#   REREAD_MATCH=1                （写后复读逐字段比对）
#   FRIENDCFG_RESULT=OK|ROLLBACK|ABORT

param(
    [Parameter(Mandatory = $true)][string]$Account,
    [Parameter(Mandatory = $true)][string]$UserId,
    [Parameter(Mandatory = $true)][string]$OverlayIP,
    [Parameter(Mandatory = $true)][string]$DecisionIP,
    [string]$GatewayDir   = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$PythonExe    = "C:\Python312\python.exe",
    [string]$TemplatePath = "",
    [switch]$Apply
)

$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[INFO] $m" }
function Warn($m) { Write-Host "[WARN] $m" }
function Fail($m, $code) {
    Write-Host "[FAIL] $m"
    Write-Host "FRIENDCFG_RESULT=ABORT reason=$code"
    exit 2
}

# —— 0) 模板定位：默认与脚本同目录，找不到就退（不猜路径）——
if (-not $TemplatePath) {
    $TemplatePath = Join-Path $PSScriptRoot "config.xt.friend.template.json"
}
if (-not (Test-Path $TemplatePath)) { Fail "template not found: $TemplatePath" "NO_TEMPLATE" }

# —— 1) 钥匙从 stdin 读，长度与占位值自检（形态校验，不是业务判定）——
$Token = ([Console]::In.ReadToEnd() -replace "`r|`n", "").Trim()
if ($Token.Length -lt 16) { Fail "token from stdin too short (len=$($Token.Length))" "TOKEN_SHORT" }
if ($Token -eq "change-me" -or $Token -eq "__TOKEN__") { Fail "placeholder token refused" "TOKEN_PLACEHOLDER" }

# —— 2) 地址形态与"两地址不得相同"自检 ——
$ipPat = '^\d{1,3}(\.\d{1,3}){3}$'
if ($OverlayIP -notmatch $ipPat) { Fail "OverlayIP not IPv4: $OverlayIP" "BAD_OVERLAY_IP" }
if ($DecisionIP -notmatch $ipPat) { Fail "DecisionIP not IPv4: $DecisionIP" "BAD_DECISION_IP" }
if ($OverlayIP -eq $DecisionIP) { Fail "OverlayIP == DecisionIP (同机不需要远端档)" "SAME_IP" }
if ($OverlayIP -eq "127.0.0.1" -or $OverlayIP.StartsWith("0.")) { Fail "OverlayIP is loopback/unset: $OverlayIP" "OVERLAY_LOOPBACK" }

# —— 3) 读模板、填占位（用对象赋值而不是字符串替换：JSON 里的反斜杠/引号踩坑史太多）——
$cfg = Get-Content -Raw -Encoding UTF8 -Path $TemplatePath | ConvertFrom-Json
$cfg.account      = $Account
$cfg.user_id      = $UserId
$cfg.owner_user_id = $UserId
$cfg.token        = $Token
$cfg.report_token = $Token
$cfg.listen       = "${OverlayIP}:8789"
if (@($cfg.account_tokens).Count -ne 1) { Fail "template account_tokens must hold exactly one entry" "TEMPLATE_TOKENS" }
$cfg.account_tokens[0].user_id = $UserId
$cfg.account_tokens[0].token   = $Token

$cfgPath = Join-Path $GatewayDir "config.xt.json"
$envPath = Join-Path $GatewayDir "friend.env"
$stamp   = (Get-Date -Format "yyyyMMddHHmmss")
$backup  = "$cfgPath.pre-friendcfg-$stamp"

if (-not $Apply) {
    Info "缺省只检视不写盘（加 -Apply 才落盘）。计划写入：listen=$($cfg.listen) owner=$UserId account_tail=$($Account.Substring([Math]::Max(0, $Account.Length - 4)))"
    Info "profile-check 将以 QUANT_GATEWAY_BIND=$($cfg.listen) / ALLOWED_IPS=$DecisionIP 运行"
    Write-Host "FRIENDCFG apply=0 written=0 account_tail=$($Account.Substring([Math]::Max(0, $Account.Length - 4))) owner=$UserId"
    Write-Host "FRIENDCFG_RESULT=DRYRUN"
    exit 0
}

# —— 4) 写前备份（已存在才备；不存在就是首次装填，回滚即"删掉新文件"）——
$hadOriginal = Test-Path $cfgPath
if ($hadOriginal) {
    Copy-Item $cfgPath $backup -Force
    Info "已备份原配置 -> $backup"
}

$json = $cfg | ConvertTo-Json -Depth 6
# 无 BOM UTF-8：网关 json.load 不容 BOM（带了就是"配置存在但读不出"那种静默死法）
[System.IO.File]::WriteAllText($cfgPath, $json, (New-Object System.Text.UTF8Encoding($false)))
# ACL 收敛：这份文件里是**别人账户的实盘钥匙**，同机其他账号不许能读到
$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
icacls $cfgPath /inheritance:r /grant "*S-1-5-32-544:F" /grant "*S-1-5-18:F" /grant "*${sid}:(F)" | Out-Null
Info "已写入 $cfgPath（ACL: Administrators+SYSTEM）"

# —— 5) friend.env：看门狗拉起网关时必须带这两个值（缺一就等于"只上报不收单"）——
#     ALLOWED_IPS 必须含两址：
#       * 决策机 $DecisionIP —— 真正下单方，来源白名单的本来目的；
#       * 本机 overlay $OverlayIP —— 让本机装填器的 /health 自探不被 IP 白名单判 403。
#         /health 只对回环与白名单开放，而网关绑定在 overlay 地址（validate_friend_profile 禁止
#         0.0.0.0），回环并未监听，故本机自探只能走 overlay 地址，必须把它自己放进白名单。
#         只加本机自己，不放开任何第三方；/order 等仍需 Bearer token，放 IP 不等于放开鉴权。
Set-Content -Path $envPath -Value @(
    "QUANT_GATEWAY_BIND=$($cfg.listen)",
    "ALLOWED_IPS=$DecisionIP,$OverlayIP",
    "PYTHON_EXE=$PythonExe"
) -Encoding ASCII
Info "已写 $envPath（供看门狗/计划任务注入）"

# —— 6) 让网关自己判一次：判据真源在 gateway.py，本脚本不复制规则 ——
$gwPy = Join-Path $GatewayDir "gateway.py"
if (-not (Test-Path $gwPy)) { Fail "gateway.py not found: $gwPy" "NO_GATEWAY_PY" }

$oldBind = $env:QUANT_GATEWAY_BIND
$oldAllow = $env:ALLOWED_IPS
try {
    $env:QUANT_GATEWAY_BIND = $cfg.listen
    $env:ALLOWED_IPS = $DecisionIP
    # PS5.1: stderr lines become ErrorRecords under EAP=Stop -> wrap the native call.
    $eapP = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $pyOut = & $PythonExe $gwPy -c $cfgPath --profile-check 2>&1
    $pyOut | ForEach-Object { Write-Host $_ }
    $ErrorActionPreference = $eapP
    $rc = $LASTEXITCODE
} finally {
    if ($null -eq $oldBind) { Remove-Item Env:\QUANT_GATEWAY_BIND -ErrorAction SilentlyContinue } else { $env:QUANT_GATEWAY_BIND = $oldBind }
    if ($null -eq $oldAllow) { Remove-Item Env:\ALLOWED_IPS -ErrorAction SilentlyContinue } else { $env:ALLOWED_IPS = $oldAllow }
}

if ($rc -ne 0) {
    # 判据不过 ⇒ 立刻回滚，绝不留"新配置已落盘 + 服务下次重启就带病上线"的半态
    if ($hadOriginal) {
        Copy-Item $backup $cfgPath -Force
        Write-Host "FRIENDCFG_RESULT=ROLLBACK exit=$rc backup=$backup"
    } else {
        Remove-Item $cfgPath -Force -ErrorAction SilentlyContinue
        Remove-Item $envPath -Force -ErrorAction SilentlyContinue
        Write-Host "FRIENDCFG_RESULT=ROLLBACK exit=$rc removed_new_config=1"
    }
    exit 2
}

# —— 7) 写后复读自证：把落盘文件读回来逐字段比（防 ConvertTo-Json 把 false 写成字符串之类）——
$back = Get-Content -Raw -Encoding UTF8 -Path $cfgPath | ConvertFrom-Json
$checks = @(
    ($back.deploy_profile -eq "friend"),
    ($back.broker -eq "queued"),
    ($back.account -eq $Account),
    ($back.user_id -eq $UserId),
    ($back.owner_user_id -eq $UserId),
    ($back.listen -eq "$($OverlayIP):8789"),
    ($back.quotes_enabled -eq $false),
    ($back.failover_enable -eq $false),
    (@($back.account_tokens).Count -eq 1),
    ($back.account_tokens[0].token -eq $Token)
)
$match = -not ($checks -contains $false)
Write-Host "REREAD_MATCH=$(if ($match) { 1 } else { 0 }) fields=$($checks.Count)"
if (-not $match) {
    if ($hadOriginal) { Copy-Item $backup $cfgPath -Force }
    Write-Host "FRIENDCFG_RESULT=ROLLBACK reason=REREAD_MISMATCH"
    exit 2
}

Write-Host "FRIENDCFG apply=1 written=1 account_tail=$($Account.Substring([Math]::Max(0, $Account.Length - 4))) owner=$UserId listen=$($cfg.listen)"
Write-Host "FRIENDCFG_RESULT=OK"
