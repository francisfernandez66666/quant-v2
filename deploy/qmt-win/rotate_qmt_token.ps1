# rotate_qmt_token.ps1 — §QMT-TOKENROT（2026-09-23）：QMT 网关 token 可轮换 + 指纹可比对。
# 背景：网关鉴权 token 同时活在最多四个地方，必须一起动，而今天没有任何东西检查它们一致：
#   1) 网关配置文件 config.xt.json 的 token / report_token（ensure_gateway_config.ps1 生成）；
#   2) 进程环境变量**覆盖文件**：QUANT_GATEWAY_TOKEN / QUANT_GATEWAY_REPORT_TOKEN
#      （qmt_gateway/gateway.py :186/:199；经 NSSM 服务 AppEnvironmentExtra 落盘）；
#   3) 引擎侧 $QUANT_DATA_DIR\config.json 的 rules.qmt.token（权威写入方＝web 设置页）；
#   4) 桥客户端 qmt_bridge.py --token（或同目录 config.bridge.json 的 token）。
# 本脚本自动化 1+2（机器可写侧），3+4 是人工步——结束时打印清单。一致性由
#   scripts/verify_deploy_guangzhou.sh 第 20 探针（token 指纹分歧检测）事后复核。
# 安全铁律（§N-5 同口径）：**任何路径都不得打印 token 明文**——只打印
#   `sha256:<前8位hex>` 指纹供操作人比对。日志出现明文按事故处理。
# 规矩②（NSSM AppEnvironmentExtra 整体替换语义）：env 写入必须走并集读回再写
#   （Get-ExistingEnvExtra/Set-ServiceEnvExtra 复刻 register_engine_services.ps1 的形状；
#   读法必须是注册表直读，绝不解析 nssm 的控制台文本——UTF-16+OEM 解码会劈碎每个字符）。
# 默认 dry-run：不带 -Apply 时只读不写、打印指纹计划。含中文注释 ⇒ 文件必须带单个 UTF-8 BOM
#   （部署链 ps1_bom 归一；仓库侧同规）。
# 用法（管理员 PowerShell，现网路径）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1            # 预演
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1 -Apply     # 真写
param(
    [string]$GatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway",
    [string]$ConfigFile = "",        # 留空取 <GatewayDir>\config.xt.json
    # 携带 QUANT_GATEWAY_TOKEN 的 NSSM 服务（现网=quant；若日后挪到别的服务用 -ServiceName 指过去）。
    [string]$ServiceName = "quant",
    [string]$DataDir = "C:\var\lib\quant-trading-v2",   # 只读：报引擎侧现值指纹，供人工步对照
    [switch]$DryRun,
    [switch]$Apply
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[rot] $m" }
function Ok($m)   { Write-Host "[ ok ] $m" }
function Die($m)  { Write-Host "[fail] $m"; exit 1 }
# Invoke-Native：$ErrorActionPreference='Stop' 下原生命令的每一行 stderr 都会被 PS5.1 升级为
# 终止错误（backup_snapshot.ps1 同族教训），nssm 恰好爱往 stderr 写提示 ⇒ 必须包这一层、只信退出码。
function Invoke-Native {
    param([string]$Exe, [string[]]$CmdArgs)
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $o = & $Exe @CmdArgs 2>&1
    $c = $LASTEXITCODE
    $ErrorActionPreference = $prev
    return @{ code = $c; out = @($o | ForEach-Object { [string]$_ }) }
}
# 指纹：sha256 小写 hex 前 8 位；空值返回 "-"。值只在函数栈里过，绝不落任何输出流。
function TokenFp([string]$v) {
    if (-not "$v") { return "-" }
    $sha = [Security.Cryptography.SHA256]::Create()
    (($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes([string]$v)) | ForEach-Object { $_.ToString("x2") }) -join "").Substring(0, 8)
}
function FpLabel([string]$v) { if (-not "$v") { return "-" }; return "sha256:" + (TokenFp $v) }

if ($Apply -and $DryRun) { Die "-DryRun 与 -Apply 互斥" }
if (-not $Apply) { $DryRun = $true }   # 缺省即 dry-run（写操作必须显式 -Apply）

if (-not $ConfigFile) { $ConfigFile = Join-Path $GatewayDir "config.xt.json" }
if (-not (Test-Path $ConfigFile)) { Die "网关配置不存在: $ConfigFile" }
$nssmCandidates = @(
    "C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe",
    "C:\opt\quant\deploy\qmt-win\tools\nssm-2.24\win64\nssm.exe",
    "C:\opt\quant\tools\nssm-2.24\win64\nssm.exe"
)
$nssm = $nssmCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $nssm) { Die "nssm.exe 不在位（并集写入无法完成，禁止裸写 AppEnvironmentExtra）" }
if ($Apply) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Die "写服务运行环境需管理员权限（HKLM + nssm set）"
    }
}

# ── 1. 读四源现值指纹（只读；写侧只动 1/2）────────────────────────────────
$cfg = $null
try { $cfg = Get-Content -Path $ConfigFile -Raw | ConvertFrom-Json } catch { Die ("config.xt.json 解析失败: " + $_.Exception.Message) }
if ($null -eq $cfg) { Die "config.xt.json 内容为空" }
Info ("src1 config_file   token=" + (FpLabel([string]$cfg.token)) + " report_token=" + (FpLabel([string]$cfg.report_token)))

# 注册表直读服务 AppEnvironmentExtra（REG_MULTI_SZ 原生 string[]，无编码/折行/NUL 歧义）。
# 形状复刻 register_engine_services.ps1:Get-ExistingEnvExtra —— 唯一允许的 env 读取口径。
function Get-ExistingEnvExtra([string]$svc) {
    $list = @()
    try {
        $key = Get-Item -LiteralPath ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $svc) -ErrorAction Stop
        $vals = $key.GetValue('AppEnvironmentExtra', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        foreach ($v in @($vals)) {
            $t = ("$v").Trim()
            if ($t -match '^[A-Za-z_][A-Za-z0-9_]*=') { $list += $t }
        }
    } catch { $list = @() }
    if ($list.Count -gt 0) { return ,$list }
    # 兜底：仅当注册表读不到才走 nssm 文本（先按 ≥2 连续 NUL 认条目边界，再清单 NUL——顺序反了
    # 就会把 UTF-16 解码残留的每字符劈开，09-23 实录已锤死）。
    $r = Invoke-Native -Exe $nssm -CmdArgs @("get", $svc, "AppEnvironmentExtra")
    if ($r.code -ne 0) { return @() }
    $joined = ((@($r.out) | ForEach-Object { [string]$_ }) -join "`n") -replace '[\u0000]{2,}', "`n"
    $joined = $joined -replace '[\u0000]', ''
    foreach ($line in ($joined -split "`r?`n")) {
        $t = $line.Trim()
        if ($t -match '^[A-Za-z_][A-Za-z0-9_]*=') { $list += $t }
    }
    return ,$list
}
function Get-EnvVal([string[]]$pairs, [string]$key) {
    foreach ($kv in $pairs) {
        $i = $kv.IndexOf('=')
        if ($i -gt 0 -and $kv.Substring(0, $i) -eq $key) { return $kv.Substring($i + 1) }
    }
    return ""
}
$envPairs = @(Get-ExistingEnvExtra $ServiceName)
$envToken = Get-EnvVal $envPairs "QUANT_GATEWAY_TOKEN"
$envReport = Get-EnvVal $envPairs "QUANT_GATEWAY_REPORT_TOKEN"
$envState = if ($envToken) { "set" } else { "unset(gateway 用文件值)" }
Info ("src2 svc_env(" + $ServiceName + ") QUANT_GATEWAY_TOKEN=" + (FpLabel $envToken) + " (" + $envState + ") report=" + (FpLabel $envReport) + " read=registry-or-nssm keys=" + $envPairs.Count)

$engToken = ""
$engPath = Join-Path $DataDir "config.json"
if (Test-Path $engPath) {
    try {
        $ej = Get-Content -Path $engPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($ej.rules -and $ej.rules.qmt) { $engToken = [string]$ej.rules.qmt.token }
    } catch { }
}
Info ("src3 engine_cfg    rules.qmt.token=" + (FpLabel $engToken) + " (权威写入方＝设置页，本脚本不写)")

$brToken = ""
try {
    foreach ($pr in @(Get-CimInstance -ClassName Win32_Process -Filter "Name='python.exe' OR Name='pythonw.exe'" -ErrorAction SilentlyContinue)) {
        $cl = [string]$pr.CommandLine
        if ($cl -match 'qmt_bridge\.py') {
            if ($cl -match '--token[= ](\S+)') { $brToken = $Matches[1]; break }
            $brToken = "(running, token not on cmdline - check config.bridge.json)"
            break
        }
    }
} catch { }
if (-not $brToken) {
    $bridgeCfg = Join-Path $GatewayDir "config.bridge.json"
    if (Test-Path $bridgeCfg) {
        try {
            $bjv = (Get-Content -Path $bridgeCfg -Raw | ConvertFrom-Json).token
            if ($bjv) { $brToken = [string]$bjv }
        } catch { }
    }
}
Info ("src4 bridge        token=" + $(if ($brToken -and $brToken -notmatch '^\(') { FpLabel $brToken } else { ($(if ($brToken) { $brToken } else { "not-found(桥未在跑且无 config.bridge.json)")) }) + " (人工步，本脚本不写)")

# ── 2. 新 token 与指纹计划 ────────────────────────────────────────────────
# 任务要求用 [Security.Cryptography.RandomNumberGenerator]（不点名旧 RNGCryptoServiceProvider）。
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$buf = New-Object byte[] 32
$rng.GetBytes($buf)
$newToken = ($buf | ForEach-Object { $_.ToString("x2") }) -join ""
$newFp = FpLabel $newToken
Info ("plan new_token_fp=" + $newFp + " -> config_file(token,report_token) + " + $ServiceName + ".AppEnvironmentExtra(QUANT_GATEWAY_TOKEN,QUANT_GATEWAY_REPORT_TOKEN)")

if ($DryRun) {
    Info "DRY-RUN（缺省即 dry-run）：以上只读，未写任何文件/注册表。要真轮换请重跑加 -Apply。"
    exit 0
}

# ── 3. 写侧①：config.xt.json —— 先落时间戳副本，再原子写新值（无 BOM，网关 json.load 不容 BOM）──
$stamp = Get-Date -Format "yyyyMMddHHmmss"
$bakPath = $ConfigFile + ".pre-rotate-" + $stamp
Copy-Item -LiteralPath $ConfigFile -Destination $bakPath -Force
Ok "old config timestamped -> $bakPath"
$cfg.token = $newToken
$cfg.report_token = $newToken   # 与 ensure_gateway_config.ps1 同口径：回报鉴权同 token
$tmp = $ConfigFile + ".rotating-" + $stamp
[System.IO.File]::WriteAllText($tmp, ($cfg | ConvertTo-Json -Depth 6), (New-Object System.Text.UTF8Encoding($false)))
Move-Item -LiteralPath $tmp -Destination $ConfigFile -Force
Ok ("config_file written token_fp=" + $newFp + " report_token_fp=" + $newFp)

# ── 4. 写侧②：服务 AppEnvironmentExtra —— 规矩②并集写（现值先入表，同名键才覆盖）──────
function Set-ServiceEnvExtra([string]$svc, [string[]]$desired) {
    $order = New-Object 'System.Collections.Generic.List[string]'
    $map = New-Object 'System.Collections.Generic.Dictionary[string,string]' -ArgumentList ([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($kv in (@(Get-ExistingEnvExtra $svc) + @($desired))) {
        if (-not $kv) { continue }
        $i = $kv.IndexOf('=')
        if ($i -lt 1) { continue }
        $k = $kv.Substring(0, $i)
        if (-not $map.ContainsKey($k)) { $order.Add($k) }
        $map[$k] = $kv.Substring($i + 1)
    }
    $final = @()
    foreach ($k in $order) { $final += ("{0}={1}" -f $k, $map[$k]) }
    if ($final.Count -eq 0) { Die ("refusing to write empty AppEnvironmentExtra for " + $svc) }
    $r = Invoke-Native -Exe $nssm -CmdArgs (@("set", $svc, "AppEnvironmentExtra") + $final)
    if ($r.code -ne 0) { Die ("nssm set AppEnvironmentExtra exit=" + $r.code + "（键名列表不回显值）") }
    Ok ($svc + " AppEnvironmentExtra union-written keys=" + (($order | Sort-Object) -join ",") + "（值不回显）")
}
Set-ServiceEnvExtra $ServiceName @("QUANT_GATEWAY_TOKEN=$newToken", "QUANT_GATEWAY_REPORT_TOKEN=$newToken")

# 写后读回自证（同一注册表口径；只比指纹，不比明文、不打明文）。
$readBack = Get-EnvVal (@(Get-ExistingEnvExtra $ServiceName)) "QUANT_GATEWAY_TOKEN"
if ((FpLabel $readBack) -ne $newFp) { Die "read-back mismatch: service env 未确认写入（检查 nssm/HKLM 权限）" }
Ok "read-back confirmed via registry (fingerprint match only, value never printed)"

# ── 5. 人工步清单（本脚本刻意不自动写：3 的权威源是设置页、4 在桥的启动命令里）──────────
Write-Host ""
Info "remaining manual steps (both must move to the SAME new token before the next report/order):"
Info "  [1] web 设置页 -> QMT 配置 rules.qmt.token 改为新 token（权威写入方是设置页，勿手改 config.json）"
Info "  [2] qmt_bridge.py 启动参数 --token 改新值（或同目录 config.bridge.json）并重启桥"
Info "  [3] 重启网关进程载入新 config/env（等 QMT-Gateway-Ensure 拉起，或 restart_gateway.ps1）"
Info "  [4] 复核：GZ_IP=... ./scripts/verify_deploy_guangzhou.sh 第 20 探针 token_fp_agree=4/4"
Info ("  fingerprint to compare everywhere: " + $newFp + " (sha256 first 8 hex; NEVER paste/log the token itself)")
$newToken = $null
