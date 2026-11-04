# install_local_executor.ps1 — 装物理机上的「本地执行器」（§FRIEND-LOCAL，2026-11-03 批 7）
#
# 为什么需要这条腿（券商 2026-11-05 起的两条硬规矩）：
#   (A) QMT 内置 Python 策略禁止任何本地文件 I/O 与任何外部网络；
#   (B) QMT 交易终端禁止跑公有云/虚拟机。
#   ⇒ QMT 只能跑在物理 Windows 机器上；而"运算进不了 QMT"，所以**运算与执行**必须
#     跑在 QMT 之外的**独立普通 Python 进程**（独立 venv，不是 QMT 内置策略沙箱，
#     故可读写文件、可联网、不受 (A) 约束）。
#   本脚本就是那条腿的安装器：装 venv → 装执行器产物 → 装规则包（烘好的参数）→
#     注册计划任务 → 起服务 → 自检（执行器健康 + 版本指纹 + 网关 /health）。
#
# 与既有 install.ps1 的关系（纪律：复用，不另起炉灶）：
#   * 账号口令**不问本脚本**：复用既有 enroll.ps1 → /api/auth/login（不新增服务端路由）；
#   * config.xt.json 四元自洽（account/user_id/owner_user_id/account_tokens 恰好一条）
#     由既有 ensure_friend_gateway_config.ps1 落好了，本脚本只**读**它取账号；
#   * 计划任务用既有 install_tasks.ps1 的形状与命名法（QMTF-* 前缀）；
#   * 判据一律**不在本文件里另造**：版本指纹与哈希由产物自带，比对由
#     scripts/update_local_executor.ps1 的三段自证（备份→写入→版本回读）负责。
#
# 三条不许松的边界：
#   1) **缺省只预览**：不带 -Apply 只打印将做什么，一个字节都不写（与既有
#      install.ps1 / ensure_friend_gateway_config.ps1 / install_tasks.ps1 同纪律）。
#   2) **幂等**：重复装不炸——venv 已存在就复用、参数已存在就合并、
#      任务已存在就重注册（schtasks /F）、配置已存在就**保留**（不覆盖真配置）。
#   3) **不碰 QMT 交易客户端**：本脚本只注册/启动执行器自己的任务，
#      绝不 schtasks /End 客户端任务、绝不做 GUI 自动化（那是别人的腿）。
#
# 判读锚点（纯 ASCII；中文经 PowerShell 管道会被码页换成 ? ⇒ 上层一律按这些行判）：
#   LOCALEXEC_RESULT=DRYRUN ...
#   LOCALEXEC_RESULT=OK venv=... version=... fingerprint=... task=... health=ok
#   LOCALEXEC_RESULT=ABORT step=<n> reason=<code>
#
# 用法：
#   .\install_local_executor.ps1                                   # 预览
#   .\install_local_executor.ps1 -Apply                            # 真装
#   .\install_local_executor.ps1 -Apply -Account 2069008957         # 显式给账号（缺省读 config.xt.json）

param(
    [string]$GatewayDir   = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$ExecutorDir  = "C:\qmt\quant-trading-v2\local_executor",
    [string]$PythonExe    = "C:\Python312\python.exe",
    [string]$QmtRoot      = "",
    [string]$Account      = "",
    [string]$TaskName     = "QMTF-Exec-Ensure",
    [int]$GatewayPort     = 8789,
    [switch]$Apply
)

$ErrorActionPreference = "Stop"

function Abort($step, $reason) {
    Write-Host ("LOCALEXEC_RESULT=ABORT step={0} reason={1}" -f $step, $reason)
    exit 2
}
function Info($m) { Write-Host "[INFO] $m" }

# ---------------------------------------------------------------------------
# 0) 定位与前置（预览也要跑：预览必须能在缺件时如实说缺什么）
# ---------------------------------------------------------------------------
if (-not (Test-Path $GatewayDir)) { Abort 1 "gateway_dir_absent" }

$configPath = Join-Path $GatewayDir "config.xt.json"
$executorSrc = Join-Path $GatewayDir "local_executor.py"
$paramsSrc  = Join-Path $GatewayDir "local_executor_params.json"

if (-not (Test-Path $executorSrc)) { Abort 2 "local_executor_source_absent" }
if (-not (Test-Path $paramsSrc)) { Abort 2 "local_executor_params_absent" }

# 账号：优先显式参数；否则从既有 config.xt.json 读（不新增任何服务端路由，
# 也不重新问口令——口令那道题由 enroll.ps1 负责）。
if (-not $Account) {
    if (-not (Test-Path $configPath)) { Abort 2 "config_absent_cannot_read_account" }
    try {
        $cfg = Get-Content $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
    } catch {
        Abort 2 "config_unparsable"
    }
    # 四元自洽由 ensure_friend_gateway_config.ps1 保证；这里只取一个账号字段，
    # 并且**判一次**：account_tokens 恰好一条时账号与 user_id 才有意义。
    if (@($cfg.account_tokens).Count -ne 1) { Abort 2 "config_account_tokens_not_one" }
    $Account = [string]$cfg.account
}
if (-not $Account) { Abort 2 "account_empty" }

# 产物版本与指纹：直接读**要装的那份文件**里的常量（版本回读的真源在文件本身，
# 不是在某个旁路的清单里——清单会漂，文件不会）。
$execText = Get-Content $executorSrc -Raw -Encoding UTF8
$verMatch = [regex]::Match($execText, '(?m)^STRATEGY_VERSION\s*=\s*["'']([^"'']+)["'']')
if (-not $verMatch.Success) { Abort 2 "executor_version_unreadable" }
$version = $verMatch.Groups[1].Value
$sha = (Get-FileHash -Algorithm SHA256 -Path $executorSrc).Hash.ToLower()

$venvDir = Join-Path $ExecutorDir ".venv"
$venvPy  = Join-Path $venvDir "Scripts\python.exe"

Write-Host ("LOCALEXEC_APPLY={0} gateway_dir={1} executor_dir={2} version={3} account_tail={4}" -f `
    ($(if ($Apply) { 1 } else { 0 })), $GatewayDir, $ExecutorDir, $version, `
    $Account.Substring([Math]::Max(0, $Account.Length - 4)))

if (-not $Apply) {
    Write-Host ("LOCALEXEC_RESULT=DRYRUN would_create_venv=1 would_install_executor=1 would_install_rule_package=1 would_register_task={0} would_start=1 would_selftest=1" -f $TaskName)
    Write-Host ("LOCALEXEC_PLAN venv={0} src={1} params={2} version={3} fingerprint={4}" -f $venvDir, $executorSrc, $paramsSrc, $version, $sha)
    exit 0
}

# ---------------------------------------------------------------------------
# 1) 目录 + venv（幂等：已存在就复用，不重建）
# ---------------------------------------------------------------------------
if (-not (Test-Path $ExecutorDir)) {
    New-Item -ItemType Directory -Path $ExecutorDir -Force | Out-Null
}

if (-not (Test-Path $venvPy)) {
    if (-not (Test-Path $PythonExe)) { Abort 3 "python_exe_absent" }
    Info "创建独立 venv（执行器**不是** QMT 内置策略，故不受 (A) 的文件/网络限制）"
    & $PythonExe -m venv $venvDir
    if ($LASTEXITCODE -ne 0) { Abort 3 ("venv_create_failed_exit=" + $LASTEXITCODE) }
} else {
    Info "venv 已存在，复用（幂等）"
}
if (-not (Test-Path $venvPy)) { Abort 3 "venv_python_absent_after_create" }

# ---------------------------------------------------------------------------
# 2) 装执行器 + 装规则包（规则包 = 烘好参数的产物，落在本机本地）
# ---------------------------------------------------------------------------
$execDst = Join-Path $ExecutorDir "local_executor.py"
$paramsDst = Join-Path $ExecutorDir "local_executor_params.json"

# 覆盖前先备份（与既有 install.ps1 的 pre- 备份纪律同形）
if (Test-Path $execDst) {
    $stamp = Get-Date -Format "yyyyMMddHHmmss"
    Copy-Item $execDst "$execDst.pre-install-$stamp" -Force
    Info "已备份旧执行器 -> $execDst.pre-install-$stamp"
}
Copy-Item $executorSrc $execDst -Force
Copy-Item $paramsSrc $paramsDst -Force
Info "已装执行器与规则包到 $ExecutorDir"

# xtquant 只在 Windows 上、且随 QMT 客户端安装目录提供（不是 pip 包）。
# 做法：把 QMT 的 site-packages 写进本 venv 的 .pth —— 这样 venv 里
# `import xtquant` 就能找到它，而 venv 仍然是独立的（不受 QMT 内置策略沙箱约束）。
if ($QmtRoot) {
    $sitePy = Join-Path $QmtRoot "bin.x64\lib\site-packages"
    if (Test-Path $sitePy) {
        $pth = Join-Path $venvDir "Lib\site-packages\xtquant_local.pth"
        New-Item -ItemType Directory -Path (Split-Path -Parent $pth) -Force | Out-Null
        [System.IO.File]::WriteAllText($pth, ($sitePy + "`n"), (New-Object System.Text.UTF8Encoding($false)))
        Info "已把 QMT site-packages 接入本 venv：$sitePy"
    } else {
        Info "QMT site-packages 不在预期位置，跳过 .pth（执行器会以 xtquant 不可用 降级，不会崩）"
    }
}

# ---------------------------------------------------------------------------
# 3) 配置：账号 + 状态库路径（合并，不覆盖既有真配置）
# ---------------------------------------------------------------------------
$execCfg = Join-Path $ExecutorDir "executor.env"
$cfgLines = New-Object System.Collections.Generic.List[string]
if (Test-Path $execCfg) {
    foreach ($ln in (Get-Content $execCfg)) { $cfgLines.Add($ln) | Out-Null }
}
function Set-CfgLine($key, $val) {
    $found = $false
    for ($i = 0; $i -lt $cfgLines.Count; $i++) {
        if ($cfgLines[$i] -like "$key=*") { $cfgLines[$i] = "$key=$val"; $found = $true; break }
    }
    if (-not $found) { $cfgLines.Add("$key=$val") | Out-Null }
}
Set-CfgLine "QMT_ACCOUNT" $Account
Set-CfgLine "LOCAL_EXECUTOR_DB" (Join-Path $ExecutorDir "executor_state.db")
Set-CfgLine "LOCAL_EXECUTOR_LOG" (Join-Path $ExecutorDir "executor.log")
Set-CfgLine "LOCAL_EXECUTOR_PARAMS" $paramsDst
Set-Content -Path $execCfg -Value $cfgLines -Encoding ASCII
Info "已写 $execCfg（合并写，不覆盖别的键）"

# ---------------------------------------------------------------------------
# 4) 计划任务：只注册**执行器自己**的任务（QMTF-Exec-Ensure），不碰客户端任务
# ---------------------------------------------------------------------------
$psExe = Join-Path $env:WINDIR "System32\WindowsPowerShell\v1.0\powershell.exe"
$runner = Join-Path $ExecutorDir "run_local_executor.ps1"
$runnerBody = @(
    '# run_local_executor.ps1 - start the local executor (ordinary Python process, outside QMT).',
    'param([string]$ExecutorDir = "' + $ExecutorDir + '")',
    '$ErrorActionPreference = "Stop"',
    '$envFile = Join-Path $ExecutorDir "executor.env"',
    'if (Test-Path $envFile) { foreach ($ln in (Get-Content $envFile)) { if ($ln -match "^([^=]+)=(.*)$") { Set-Item -Path ("Env:" + $Matches[1]) -Value $Matches[2] } } }',
    '$py = Join-Path $ExecutorDir ".venv\Scripts\python.exe"',
    '$app = Join-Path $ExecutorDir "local_executor.py"',
    'if (-not (Test-Path $py)) { Write-Host "LOCALEXEC_RUNNER=ABORT reason=venv_python_absent"; exit 2 }',
    'if (-not (Test-Path $app)) { Write-Host "LOCALEXEC_RUNNER=ABORT reason=executor_absent"; exit 2 }',
    'Write-Host "LOCALEXEC_RUNNER=start"',
    '& $py $app',
    'Write-Host ("LOCALEXEC_RUNNER=exit code=" + $LASTEXITCODE)'
)
[System.IO.File]::WriteAllText($runner, (($runnerBody -join "`r`n") + "`r`n"), (New-Object System.Text.UTF8Encoding($false)))

$taskArgs = "/Create /TN `"$TaskName`" /TR `"`"$psExe`" -NoProfile -ExecutionPolicy Bypass -File `"$runner`" -ExecutorDir `"$ExecutorDir`" /SC MINUTE /MO 5 /F"
schtasks @taskArgs | Out-Null
if ($LASTEXITCODE -ne 0) { Abort 4 ("task_create_failed_exit=" + $LASTEXITCODE) }
# 注册后复读自证：写了不注册＝没有（§164 那批"写了没跑"的同形教训）
$found = schtasks /Query /TN $TaskName 2>$null
if (-not $found) { Abort 4 "task_reread_missing" }
Info "已注册任务 $TaskName（每 5 分钟；只管执行器，不碰 QMT 客户端任务）"

# ---------------------------------------------------------------------------
# 5) 起一次 + 自检（执行器健康 + 版本指纹 + 网关 /health）
# ---------------------------------------------------------------------------
schtasks /Run /TN $TaskName | Out-Null
Start-Sleep -Seconds 4

# 5a 执行器自检：--selftest 打印版本 + 指纹 + 参数摘要（纯 ASCII 锚点）
$selftest = & $venvPy $execDst --selftest 2>&1 | Out-String
$stMatch = [regex]::Match($selftest, "EXEC_SELFTEST version=(\S+) fingerprint=(\S+)")
if (-not $stMatch.Success) {
    Write-Host $selftest
    Abort 5 "executor_selftest_failed"
}
$liveVersion = $stMatch.Groups[1].Value
$liveFp = $stMatch.Groups[2].Value
if ($liveVersion -ne $version) {
    Abort 5 ("version_mismatch_installed=" + $version + "_live=" + $liveVersion)
}
Info "执行器自检通过 version=$liveVersion fingerprint=$liveFp"

# 5b 网关 /health（网关那条腿是否也活着——本腿不替它判，只如实报读数）
$gwHealth = "not_checked"
try {
    $h = Invoke-WebRequest -Uri ("http://127.0.0.1:" + $GatewayPort + "/health") -UseBasicParsing -TimeoutSec 10
    if ($h.StatusCode -eq 200) { $gwHealth = "ok" }
} catch {
    # 网关没起来**不是**本腿的失败（它有自己的 5 分钟守护任务），
    # 如实记 not_up，别把别的腿的账算到这条腿上。
    $gwHealth = "not_up"
}

Write-Host ("LOCALEXEC_RESULT=OK venv={0} version={1} fingerprint={2} task={3} health={4}" -f $venvDir, $version, $liveFp, $TaskName, $gwHealth)
exit 0
