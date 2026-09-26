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
#
# §C7-OPS（2026-09-26，FIX_PLAN_20260925EVE ⑯「自证通过≠吃到」修复）：
#   ① 已收编进 service_definitions.ps1 的定义：$GatewayDir 缺省（现网网关目录）、config.xt.json
#      路径（网关进程 token 的真源）、nssm.exe 候选位、计划任务名（重启指引）；param 字面量
#      保留为定义文件缺失时的回退。
#   ② 写侧②（quant 服务 AppEnvironmentExtra）如实降级为**冗余腿**：网关进程由交互会话
#      计划任务拉起，不继承 NSSM 服务的 AppEnvironmentExtra；gateway.py :186/:199 的 env 覆盖
#      读的是网关**自己进程**的环境变量。全仓 grep 证实 Go 侧（quant.exe）从不消费
#      QUANT_GATEWAY_TOKEN。旧版的"写后读回自证"回读的正是自己写进去的地方 ⇒ 证明不了网关
#      吃到了什么。写侧照旧保留（防"网关改由带 env 的会话拉起"这一形态回归时缺腿），
#      但自证移到 §C7-OPS 的 4b 段：直读网关进程真正会读的文件 + 进程启动时间链。
# 用法（管理员 PowerShell，现网路径）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1            # 预演
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1 -Apply     # 真写
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1 -Align     # §0926ROT-ALIGN 对齐计划（只读，不生成新 token）
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1 -Align -Apply  # 对齐落地：源1 现值→源3/源4
param(
    [string]$GatewayDir = "",        # 留空取 §C7 单源 $SvcGatewayDir（缺失回退 C:\qmt\quant-trading-v2\qmt_gateway）
    [string]$ConfigFile = "",        # 留空取 <GatewayDir>\config.xt.json
    # 携带 QUANT_GATEWAY_TOKEN 的 NSSM 服务（现网=quant；§C7-OPS②：这条腿对网关进程只是冗余，
    # 保留是为兼容"网关 env 由该服务会话继承"的假设形态；若日后挪到别的服务用 -ServiceName 指过去）。
    [string]$ServiceName = "",       # 留空取 §C7 单源 $SvcNameQuant
    [string]$DataDir = "",           # 只读：报引擎侧现值指纹，供人工步对照（留空取单源 $SvcDataDir）
    [switch]$DryRun,
    [switch]$Apply,
    # §0926ROT-ALIGN（2026-09-26 owner 令「我授权你来统一改」）：对齐模式——不生成新 token，
    # 以源1（config.xt.json，网关真源）现值为权威，把它复制到源3（引擎 config.json rules.qmt.token）
    # 与源4（config.bridge.json，如在位）。值只在服务器本地函数栈里过，输出一律指纹。
    [switch]$Align
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[rot] $m" }
function Ok($m)   { Write-Host "[ ok ] $m" }
function Warn($m) { Write-Host "[warn] $m" }
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

# ── §C7-OPS：服务/任务定义单源（网关目录、config.xt.json 真源路径、nssm 候选位、重启任务名）──
# 缺失时回退本脚本旧字面量并告警（轮换步是运维动作，不因缺配置文件而无法自救；
# 但 4b 自证段读文件走注册表/文件直读口径，与单源在位与否无关）。
$svcDefs = $null
foreach ($cand in @((Join-Path $PSScriptRoot "service_definitions.ps1"),
                    "C:\opt\quant\qmt-win\service_definitions.ps1")) {
    if (Test-Path $cand) { . $cand; $svcDefs = $cand; break }
}
if (-not $svcDefs) {
    Warn "service_definitions.ps1 未找到 —— 网关目录/服务名/nssm 位回退本脚本字面量（§C7 单径脱钩，请补齐部署清单）"
    $SvcGatewayDir = "C:\qmt\quant-trading-v2\qmt_gateway"
    $SvcNameQuant  = "quant"
    $SvcDataDir    = "C:\var\lib\quant-trading-v2"
    $SvcTaskGatewayEnsure = "QMT-Gateway-Ensure"
    $SvcNssmCandidates = @(
        "C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe",
        "C:\opt\quant\deploy\qmt-win\tools\nssm-2.24\win64\nssm.exe",
        "C:\opt\quant\tools\nssm-2.24\win64\nssm.exe"
    )
}
if (-not $GatewayDir) { $GatewayDir = $SvcGatewayDir }
if (-not $ServiceName) { $ServiceName = $SvcNameQuant }
if (-not $DataDir) { $DataDir = $SvcDataDir }

if (-not $ConfigFile) { $ConfigFile = Join-Path $GatewayDir "config.xt.json" }
if (-not (Test-Path $ConfigFile)) { Die "网关配置不存在: $ConfigFile" }
# nssm 候选位来自 §C7 单源（缺失回退块里已复刻同一张表）；解析不到仍判死——
# env 并集写入没有 nssm 就完不成，禁止裸写 AppEnvironmentExtra。
$nssm = $SvcNssmCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
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
# -Encoding UTF8：与 verify 第 20 探针 §TOKEN-BLIND 同口径（无 BOM UTF-8 文件在 PS5.1 缺省
# GBK 解码会吞引号 ⇒ ConvertFrom-Json 炸 ⇒ 轮换前的现值指纹误读）。
try { $cfg = Get-Content -Path $ConfigFile -Raw -Encoding UTF8 | ConvertFrom-Json } catch { Die ("config.xt.json 解析失败: " + $_.Exception.Message) }
if ($null -eq $cfg) { Die "config.xt.json 内容为空" }
Info ("src1 config_file   token=" + (FpLabel([string]$cfg.token)) + " report_token=" + (FpLabel([string]$cfg.report_token)))

# 注册表直读服务 AppEnvironmentExtra（REG_MULTI_SZ 原生 string[]，无编码/折行/NUL 歧义）。
# 形状复刻 register_engine_services.ps1:Get-ExistingEnvExtra —— 唯一允许的 env 读取口径。
function Get-ExistingEnvExtra([string]$svc) {
    $list = @()
    # §0926-ROT 实锤（09-26 休市日轮换实录）：本机 NSSM 把 AppEnvironmentExtra 存在
    # Services\<svc>\Parameters 子键，Services\<svc> 本级读恒空——旧版只读本级，写后回读
    # 跌进 nssm 文本兜底、条目被空格并成一行，指纹必然对不上（明明写成功却判红的假红）。
    # 修法=按运行时真实取值链直读两级路径（本级在前保持兼容，Parameters 补上），仍只收 KEY= 形。
    foreach ($rk in @(("HKLM:\SYSTEM\CurrentControlSet\Services\" + $svc),
                      ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $svc + "\Parameters"))) {
        try {
            $key = Get-Item -LiteralPath $rk -ErrorAction Stop
            $vals = $key.GetValue('AppEnvironmentExtra', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            foreach ($v in @($vals)) {
                $t = ("$v").Trim()
                if ($t -match '^[A-Za-z_][A-Za-z0-9_]*=' -and -not ($list -contains $t)) { $list += $t }
            }
        } catch { }
    }
    if ($list.Count -gt 0) {
        Info ("envfn direct n=" + $list.Count + " keys=" + (($list | ForEach-Object { $_.Substring(0, $_.IndexOf('=')) }) -join ","))
        return ,$list
    }
    Info "envfn direct EMPTY -> nssm-text fallback"
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
# §0926-ROT 调用侧展平（09-26 休市日轮换实录）：函数内 `envfn direct n=2` 打印的是**函数栈里**
# 的真实形态，穿到调用方 `@(func)` 后在本机 PS 5.1 上落回单元素嵌套数组（.Count=1，
# ("$整串") 字符串化后取 '=' 得"全并串"）——写侧两条干净的 64 位条目就这样被自证读成假红，
# 两轮 -Apply 全被读回腿拦停。修法不赌管道解卷语义：foreach **语句**逐层剥（嵌套形态下
# $raw 的唯一元素就是 string[]，@($x) 再展开），两种落地形态一律收敛成一维 KEY= 串数组。
function Flatten-EnvPairs($raw) {
    $flat = @()
    foreach ($x in @($raw)) { foreach ($y in @($x)) { $t = ("$y").Trim(); if ($t -match '^[A-Za-z_][A-Za-z0-9_]*=') { $flat += $t } } }
    return $flat
}
$envPairs = Flatten-EnvPairs @(Get-ExistingEnvExtra $ServiceName)
$envToken = Get-EnvVal $envPairs "QUANT_GATEWAY_TOKEN"
$envReport = Get-EnvVal $envPairs "QUANT_GATEWAY_REPORT_TOKEN"
# §C7-OPS②：如实标注——这条 env 腿对网关进程只是冗余。网关由交互会话计划任务拉起，
# 不继承 $ServiceName（NSSM 服务）的 AppEnvironmentExtra；quant.exe（Go 侧）全仓 grep
# 从不消费 QUANT_GATEWAY_TOKEN。gateway.py 的 env 覆盖只认网关自己进程的环境变量。
$envState = if ($envToken) { "set(对网关=冗余腿，仅该服务自己的进程可见)" } else { "unset(gateway 用文件值)" }
Info ("src2 svc_env(" + $ServiceName + ") QUANT_GATEWAY_TOKEN=" + (FpLabel $envToken) + " (" + $envState + ") report=" + (FpLabel $envReport) + " read=registry-or-nssm keys=" + $envPairs.Count)

$engToken = ""
$engPath = Join-Path $DataDir "config.json"
if (Test-Path $engPath) {
    try {
        $ej = Get-Content -Path $engPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($ej.rules -and $ej.rules.qmt) { $engToken = [string]$ej.rules.qmt.token }
    } catch { }
}
Info ("src3 engine_cfg    rules.qmt.token=" + (FpLabel $engToken) + " (权威写入方＝设置页；-Align 模式本脚本可按源1补齐)")

# ── [诊断] §0926-ROT：env 腿写后读回 mismatch 的结构取证（纯注册表直读、只报计数/键名/读法，
# 零值外泄）。两个疑点一次拆掉：①NSSM 可能把参数写在 Services\<svc>\Parameters 子键（两份路径
# 哪份有值、各几条）；②REG_MULTI_SZ 的条目劈分实况（几条能过 KEY= 形、几条过不了）。
# 这腿对网关本是冗余（§C7-OPS②），但写侧"自证读不到"和"真没写"必须区分开——判据本身先可信。
foreach ($diagPath in @(("HKLM:\SYSTEM\CurrentControlSet\Services\" + $ServiceName),
                        ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $ServiceName + "\Parameters"))) {
    $dv = $null; $dErr = ""
    try {
        $dk = Get-Item -LiteralPath $diagPath -ErrorAction Stop
        $dv = $dk.GetValue('AppEnvironmentExtra', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
    } catch { $dErr = $_.Exception.GetType().Name }
    $dRaw = if ($null -eq $dv) { 0 } elseif ($dv -is [string[]]) { @($dv).Count } else { 1 }
    $dMatched = @()
    foreach ($e in @($dv)) { $t = ("$e").Trim(); if ($t -match '^[A-Za-z_][A-Za-z0-9_]*=') { $dMatched += $t.Substring(0, $t.IndexOf('=')) } }
    Info ("envdiag path=" + $diagPath + " exists=" + (Test-Path $diagPath) + " entries=" + $dRaw + " matchedkeys=" + (($dMatched | Sort-Object) -join ",") + " err=" + $dErr)
    # 逐条目形状（键名+值长度是元数据，可打；值本体绝不打——09-26 实录：union 读错路径把
    # 现值并丢了，必须看清每条边界才能定修法）。
    $di = 0
    foreach ($e in $(if ($null -eq $dv) { @() } else { @($dv) })) {
        $t = ("$e").Trim(); $eqi = $t.IndexOf('=')
        $nm = if ($eqi -gt 0 -and $t.Substring(0, $eqi) -match '^[A-Za-z_][A-Za-z0-9_]*$') { $t.Substring(0, $eqi) } else { "(unnamed:" + $t.Substring(0, [Math]::Min(3, $t.Length)) + ")" }
        Info ("envdiag entry[" + $di + "] key=" + $nm + " vlen=" + [Math]::Max(0, $t.Length - $eqi - 1))
        $di++
    }
}

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
            # §0926ROT-ALIGN：补 -Encoding UTF8 与 §TOKEN-BLIND 同口径——无 BOM UTF-8 在 PS5.1
            # 缺省 GBK 解码会吞引号炸解析，src4 现值被读成空 ⇒ align 把本来同值的桥误判成 write。
            $bjv = (Get-Content -Path $bridgeCfg -Raw -Encoding UTF8 | ConvertFrom-Json).token
            if ($bjv) { $brToken = [string]$bjv }
        } catch { }
    }
}
Info ("src4 bridge        token=" + $(if ($brToken -and $brToken -notmatch '^\(') { FpLabel $brToken } else { ($(if ($brToken) { $brToken } else { "not-found(桥未在跑且无 config.bridge.json)"})) }) + " (人工步原文；-Align 模式可在位时按源1补齐，缺省仍不写)")

# ── 1z. §0926ROT-ALIGN 对齐模式（2026-09-26 owner 令「我授权你来统一改」）────────────────
# 语义：**不生成新 token**。源1（config.xt.json）是网关鉴权的真源，本模式把它的现值在服务器
# 本地复制到源3（引擎 config.json 的 rules.qmt.token）与源4（config.bridge.json，如在位）。
# 明文只在函数栈里过（写入值＝读到的源1值），所有输出仍是 sha256 前 8 位指纹——与轮换同一铁律。
# 写形选择：引擎 config.json 由 Go 侧序列化维护，结构大且格式敏感 ⇒ **绝不整文件反序列化重排**
# （PS5.1 ConvertTo-Json 会重排/改数值形态，动没动过的字段都可能变），改为"锚点后的首个
# token 字段"原文外科替换；写完**重新解析自证**三件事：①rules.qmt.token 指纹==源1；
# ②锚点块邻近关键字段（gateway_url/price_type/mode）逐值不变（防手术刀切错块）；
# ③提案文本先过同样断言再落盘——不满足就 Die，一个字节都不写。写前落 .pre-align-<ts> 副本。
function Set-JsonStringFieldRaw {
    param([string]$Text, [string]$Anchor, [string]$Field, [string]$Value, [scriptblock]$Check)
    # Anchor 为 ""（顶层直接找）或如 '"qmt"' 的锚点：只在锚点之后的窗口里找首个 Field 字符串值。
    # §0926ROT-ALIGN 防撞名：锚点字样可能在文件更早处作为字符串值出现（如 "source": "qmt"）——
    # 把锚点的**每一处出现**都当候选窗口，切完先过 $Check(解析对象) 才认；全不过＝抛，绝不写。
    $starts = @()
    if ($Anchor) {
        $i = -1
        while (($i = $Text.IndexOf($Anchor, $i + 1)) -ge 0) { $starts += $i }
        if ($starts.Count -eq 0) { throw ("anchor not found in text: " + $Anchor) }
    } else { $starts = @(0) }
    foreach ($s in $starts) {
        $head = $Text.Substring(0, $s)
        $tail = $Text.Substring($s)
        $m = [regex]::Match($tail, ('"' + $Field + '"\s*:\s*"((?:[^"\\]|\\.)*)"'))
        if (-not $m.Success) { continue }
        $cand = $head + $tail.Substring(0, $m.Index) + ('"' + $Field + '":"' + $Value + '"') + $tail.Substring($m.Index + $m.Length)
        if (-not $Check) { return $cand }
        $parsed = $null
        try { $parsed = $cand | ConvertFrom-Json } catch { continue }
        $okc = $false
        try { $okc = (& $Check $parsed) -eq $true } catch { $okc = $false }
        if ($okc) { return $cand }
    }
    # ⚠ PS 的转义引号是反引号形（`"），**不是** C 形反斜杠引号——09-26 实录：反斜杠贴引号时
    # PS5.1 把 \" 解析成「反斜杠+闭引号」，整条 throw 翻转引号语境，函数大括号被吞进字符串，
    # 仓库侧括号平衡扫描恰好抵平未拦，现网首跑 ParserError 才炸。此形已入 §102 负向锁。
    throw ('no accepted replace candidate for field "' + $Field + '" after anchor "' + $Anchor + '"（锚点全撞了错误位或字段不存在——停手）')
}
if ($Align) {
    $alignTok = [string]$cfg.token
    if (-not $alignTok) { Die "align refused: src1 token empty, no truth source to align to" }
    $alignFp = FpLabel $alignTok
    Info ("align src1 truth fp=" + $alignFp + " (value never leaves server, never printed)")
    if ((FpLabel $envToken) -ne $alignFp) {
        Warn "align src2 svc_env fp != src1（冗余腿，量的是服务 env；本轮不动它——若 src2 应同源请先跑轮换 -Apply 再对齐）"
    } else {
        Info "align src2 svc_env same-as-src1 (redundant leg confirmed by fingerprint)"
    }
    $wantEng = ((FpLabel $engToken) -ne $alignFp)
    $bridgePath = Join-Path $GatewayDir "config.bridge.json"
    $brAction = "absent"
    if (Test-Path $bridgePath) { $brAction = if ((FpLabel $brToken) -ne $alignFp) { "write" } else { "same" } }
    Info ("align src3 plan engine_cfg will_write=" + $wantEng + " was=" + (FpLabel $engToken) + " target_fp=" + $alignFp)
    Info ("align src4 plan bridge action=" + $brAction + " (absent=桥未落位，落位时写源1同值即可)")
    if ($DryRun) {
        Info "ALIGN_DRY plan-only above, nothing written. To really align rerun with -Align -Apply."
        exit 0
    }
    $stamp = Get-Date -Format "yyyyMMddHHmmss"
    # ── 源3：引擎 config.json rules.qmt.token ──
    if (-not $wantEng) {
        Ok ("align src3 done token_fp=" + $alignFp + " changed=False (already same as src1)")
    } else {
        if (-not (Test-Path $engPath)) { Die ("align src3 refused: engine config not found at " + $engPath) }
        $engRaw = Get-Content -Path $engPath -Raw -Encoding UTF8
        $engObj = $null
        try { $engObj = $engRaw | ConvertFrom-Json } catch { Die ("align src3 refused: config.json 解析失败（" + $_.Exception.Message + "）——结构不可信，一个字节都不写") }
        if (-not $engObj.rules -or -not $engObj.rules.qmt) { Die "align src3 refused: rules.qmt node missing in config.json（引擎侧结构不猜，停手）" }
        $engNew = $null
        try { $engNew = Set-JsonStringFieldRaw -Text $engRaw -Anchor '"qmt"' -Field 'token' -Value $alignTok -Check { param($o) (FpLabel([string]$o.rules.qmt.token)) -eq $alignFp } } catch { Die ("align src3 refused: " + $_.Exception.Message) }
        $engChk = $null
        try { $engChk = $engNew | ConvertFrom-Json } catch { Die "align src3 refused: 提案文本解析不过（手术切坏形态，拒写）" }
        if ((FpLabel([string]$engChk.rules.qmt.token)) -ne $alignFp) { Die "align src3 refused: 提案自证不过——rules.qmt.token 未命中锚点块（切错位置嫌疑）" }
        foreach ($f in @('gateway_url', 'price_type', 'mode')) {
            if ([string]$engChk.rules.qmt.$f -ne [string]$engObj.rules.qmt.$f) { Die ("align src3 refused: 字段 " + $f + " 在提案里变了——手术刀不干净，拒写") }
        }
        $engBak = $engPath + ".pre-align-" + $stamp
        Copy-Item -LiteralPath $engPath -Destination $engBak -Force
        $engTmp = $engPath + ".aligning-" + $stamp
        [System.IO.File]::WriteAllText($engTmp, $engNew, (New-Object System.Text.UTF8Encoding($false)))
        Move-Item -LiteralPath $engTmp -Destination $engPath -Force
        $engNow = (Get-Content -Path $engPath -Raw -Encoding UTF8 | ConvertFrom-Json)
        if ((FpLabel([string]$engNow.rules.qmt.token)) -ne $alignFp) { Die ("align src3 read-back mismatch（回滚副本: " + $engBak + "）") }
        Ok ("align src3 done token_fp=" + $alignFp + " changed=True was=" + (FpLabel $engToken) + " read-back confirmed, backup=.pre-align-" + $stamp)
    }
    # ── 源4：桥配置如在位则同写（当前现网 absent——落位时由部署步写源1同值）──
    switch ($brAction) {
        "same"    { Ok ("align src4 done token_fp=" + $alignFp + " action=same (already aligned)") }
        "write"   {
            $brRaw = Get-Content -Path $bridgePath -Raw -Encoding UTF8
            $brObj = $null
            try { $brObj = $brRaw | ConvertFrom-Json } catch { Die ("align src4 refused: config.bridge.json 解析失败（" + $_.Exception.Message + "）") }
            $brNew = $null
            try { $brNew = Set-JsonStringFieldRaw -Text $brRaw -Anchor "" -Field 'token' -Value $alignTok -Check { param($o) (FpLabel([string]$o.token)) -eq $alignFp } } catch { Die ("align src4 refused: " + $_.Exception.Message) }
            $brChk = $null
            try { $brChk = $brNew | ConvertFrom-Json } catch { Die "align src4 refused: 提案文本解析不过" }
            if ((FpLabel([string]$brChk.token)) -ne $alignFp) { Die "align src4 refused: 提案自证不过——token 字段未命中" }
            $brBak = $bridgePath + ".pre-align-" + $stamp
            Copy-Item -LiteralPath $bridgePath -Destination $brBak -Force
            $brTmp = $bridgePath + ".aligning-" + $stamp
            [System.IO.File]::WriteAllText($brTmp, $brNew, (New-Object System.Text.UTF8Encoding($false)))
            Move-Item -LiteralPath $brTmp -Destination $bridgePath -Force
            if ((FpLabel([string]((Get-Content -Path $bridgePath -Raw -Encoding UTF8 | ConvertFrom-Json).token))) -ne $alignFp) { Die ("align src4 read-back mismatch（回滚副本: " + $brBak + "）") }
            Ok ("align src4 done token_fp=" + $alignFp + " action=written read-back confirmed, backup=.pre-align-" + $stamp)
        }
        default   { Ok ("align src4 done token_fp=" + $alignFp + " action=absent (bridge not provisioned; write src1 value when it is)") }
    }
    Info "ALIGN_APPLIED machine-readable sources unified to src1 truth. Reminder: gateway restart only AFTER this returns 4-source agreement; final check = verify deploy probe 20 token_fp_agree."
    exit 0
}

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
    foreach ($kv in ((Flatten-EnvPairs @(Get-ExistingEnvExtra $svc)) + @($desired))) {
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
# ⚠ §C7-OPS：这只是**冗余腿自身**的写入确认（回读＝自己刚写的地方，证明不了网关吃到）——
#   网关侧的自证在下面的 4b 段，以 4b 为准。
$readBack = Get-EnvVal (Flatten-EnvPairs @(Get-ExistingEnvExtra $ServiceName)) "QUANT_GATEWAY_TOKEN"
if ((FpLabel $readBack) -ne $newFp) { Die "read-back mismatch: service env 未确认写入（检查 nssm/HKLM 权限）" }
Ok "read-back confirmed via registry (fingerprint match only, value never printed)"

# ── 4b. §C7-OPS「网关进程实际吃到的值」回读自证（FIX_PLAN_20260925EVE ⑯ 修法主体）────────
# 旧版只回读自己刚写进 quant 服务 env 的键 ⇒ 那是"自证通过≠吃到"的源头：网关进程真正的
# 取值链是 交互任务拉起 → gateway.py -c config.xt.json →（自身进程 env 有值才覆盖，否则用文件值）。
# 所以自证读两级，全部走**运行时真实取值链直读**（文件直读，绝不解析控制台文本——
# Out-String 120 列折行吞键是 §N-5 锤过的同族坑）：
#   (a) config.xt.json 落盘值（网关 token 的真源，$SvcGatewayConfigFile 同源）；
#   (b) 在跑网关进程的启动时刻 vs 配置文件写入时刻（旧进程必然还拿着旧 token）。
# 任何路径不回显明文，只比 sha256 前 8 位指纹。
$cfgNow = $null
try {
    # -Encoding UTF8 必带：这份文件由 ensure_gateway_config.ps1 以**无 BOM UTF-8** 落盘，
    # PS 5.1 缺省按 GBK 解会把中文字段尾后的引号吞掉 ⇒ ConvertFrom-Json 炸 ⇒ 假"读不到"
    # （verify 第 20 探针 §TOKEN-BLIND 的实录教训，读法必须与其对齐）。
    $cfgNow = Get-Content -Path $ConfigFile -Raw -Encoding UTF8 | ConvertFrom-Json
} catch { Die ("§C7 自证失败：写后重读 " + $ConfigFile + " 解析失败（检查文件是否被并发写坏；回滚副本: " + $bakPath + "）: " + $_.Exception.Message) }
$fileTokFp = FpLabel([string]$cfgNow.token)
$fileRepFp = FpLabel([string]$cfgNow.report_token)
if ($fileTokFp -ne $newFp) { Die ("§C7 自证失败：config.xt.json 的 token 指纹 " + $fileTokFp + " != 新值指纹 " + $newFp + "（写侧①被回滚/并发改写？备份在 " + $bakPath + "）") }
Ok ("§C7 self-check(a) gateway file source CONFIRMED: " + $ConfigFile + " token_fp=" + $fileTokFp + " report_token_fp=" + $fileRepFp)
if ($fileRepFp -ne $newFp) {
    Warn ("§C7 自证告警：report_token 指纹 " + $fileRepFp + " != 新值 " + $newFp + " —— 回报腿不会被网关吃到，检查写侧①（本脚本 token/report 应同值）")
}
# (b) 网关进程取值链回读：进程比配置文件旧 ⇒ 它内存里必然是旧 token。
$gwProcState = "not-running"
try {
    foreach ($pr in @(Get-CimInstance -ClassName Win32_Process -Filter "Name='python.exe' OR Name='pythonw.exe'" -ErrorAction Stop)) {
        $cl = [string]$pr.CommandLine
        if ($cl -like '*gateway.py*' -and $cl -like '*qmt_gateway*') {
            $pStart = $pr.ConvertToDateTime($pr.CreationDate)
            $cfgWri = (Get-Item -LiteralPath $ConfigFile).LastWriteTime
            $gwProcState = if ($pStart -lt $cfgWri) { "stale(old token in memory)" } else { "restarted(file value in effect)" }
            break
        }
    }
} catch { $gwProcState = "proc-read-failed" }
Info ("§C7 self-check(b) gateway process: " + $gwProcState + " (判据=进程启动时刻 vs " + (Split-Path -Leaf $ConfigFile) + " 写入时刻，纯直读)")
Info ("§C7 env-leg note: 写侧②(服务 " + $ServiceName + " AppEnvironmentExtra) 对网关进程只是冗余——网关经交互任务 " + $SvcTaskGatewayEnsure + " 拉起，不继承该服务 env；quant.exe 也不消费该键。保留它仅为兼容假定的 env 覆盖形态，自证以 (a)(b) 为准。")
if ($gwProcState -ne "restarted(file value in effect)") {
    Warn "§C7 网关进程尚未吃到新值——需要重启/重连：& '$PSScriptRoot\restart_gateway.ps1' 杀掉旧进程，再由单径交互任务拉起（schtasks /Change /TN $SvcTaskGatewayEnsure /Enable ; schtasks /Run /TN $SvcTaskGatewayEnsure）"
    Warn "§C7 重启后复验：重跑本脚本（dry-run 即可）看 src1 与 self-check(b) 是否为 restarted；最终复核＝verify_deploy_guangzhou.sh 第 20 探针 token_fp_agree"
}

# ── 5. 人工步清单（轮换模式仍不自动写源3/源4；§0926ROT-ALIGN 起，源3 可由 -Align -Apply 按源1补齐，源4 在桥落位时写源1同值）──
Write-Host ""
Info "remaining manual steps (both must move to the SAME new token before the next report/order):"
Info "  [1] web 设置页 -> QMT 配置 rules.qmt.token 改为新 token（权威写入方是设置页；或跑本脚本 -Align -Apply 由机器按源1补齐，二者取其一勿并行）"
Info "  [2] qmt_bridge.py 启动参数 --token 改新值（或同目录 config.bridge.json）并重启桥（该文件在位时 -Align 会一并核对/同写）"
Info "  [3] 重启网关进程载入新 config（§C7 单径：restart_gateway.ps1 杀旧进程 → 交互任务 $SvcTaskGatewayEnsure 拉起；env 腿对网关是冗余，别指望重启服务）"
Info "  [4] 复核：GZ_IP=... ./scripts/verify_deploy_guangzhou.sh 第 20 探针 token_fp_agree=4/4"
Info ("  fingerprint to compare everywhere: " + $newFp + " (sha256 first 8 hex; NEVER paste/log the token itself)")
$newToken = $null
