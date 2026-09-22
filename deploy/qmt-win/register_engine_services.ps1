# register_engine_services.ps1 - Guangzhou all-in-one: register engine Windows services (NSSM) + qmtctl task scheduler.
# Usage (admin PowerShell):
#   powershell -ExecutionPolicy Bypass -File register_engine_services.ps1 `
#       -QuantExe C:\opt\quant\quant.exe -ResearchExe C:\opt\quant\researchd.exe `
#       -PydataVenv C:\opt\quant\venv -QmtctlExe C:\opt\quant\qmtctl.exe `
#       -MiniQmtPath "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe" `
#       -DataDir C:\var\lib\quant-trading-v2 `
#       -SecretFile C:\etc\quant.env
#   # 密钥走磁盘密钥文件（默认即是），不必在命令行传；老写法仍兼容，需要覆盖时才传：
#   #   ... -LLMApiKey "..." -LLMApiURL "..." -LLMModel "..." -HithinkApiKey "..."
# NOTE: MiniQmtPath MUST be the full client XtItClient.exe (auto-login + trading). XtMiniQmt.exe
#       cannot auto-login → broker never connects.
# NOTE (§ENH-0 2026-09-19): HithinkApiKey = HITHINK_FINANCE_API_KEY，交易日历/行情主源密钥。
#       旧脚本从不注入它 → quant 主服务永远"周末口径兜底"，法定节假日误判为交易日。请随部署传入。
# NOTE (§N-5 2026-09-23 晚批，owner 裁决 6): 密钥参数缺省时**不再跳过**，改为按
#       「显式参数 > 磁盘密钥文件 $SecretFile > 机器级环境变量 > 参数内置默认」解析，
#       写入前先读回服务现值做**并集**，脚本尾部再按**键名**断言（只断言键名、绝不回显值），
#       缺键 Warn + 非零退出。旧形态（条件追加 + 整体替换）会让"改端口/修故障/二次部署"
#       这类不带密钥的重跑静默删掉 LLM_API_KEY/LLM_API_URL/LLM_MODEL——降级却报部署成功。
#       只改本文件：dist-guangzhou/qmt-win/ 下那份同名脚本是 2026-08-31 的陈旧分叉
#       （scripts/deploy_guangzhou.sh:145 只 scp deploy/qmt-win/ 这份，现网用现网的就是本文件）。
# Design (docs/MIGRATION_GUANGZHOU_ALLINONE.md section 4):
#   quant          NSSM service, NORMAL priority
#   quant-research NSSM service, BELOW_NORMAL priority (session-gated off-hours)
#   pydata         NSSM service, BELOW_NORMAL priority (baostock sidecar, port 8787)
#   qmt-gateway    MUST run in the interactive session (see register_service.ps1 header):
#                  xtquant talks to the QMT client via per-session shared-memory queues,
#                  a Session-0/NSSM instance can never complete the heartbeat handshake.
#   qmtctl         scheduled task (interactive session, every 10 min) - must NOT use NSSM (needs GUI login)
param(
    [string]$QuantExe = "C:\opt\quant\quant.exe",
    [string]$ResearchExe = "C:\opt\quant\researchd.exe",
    [string]$PydataVenv = "C:\opt\quant\venv",
    [string]$QmtctlExe = "C:\opt\quant\qmtctl.exe",
    [string]$MiniQmtPath = "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe",
    [string]$DataDir = "C:\var\lib\quant-trading-v2",
    [string]$LLMApiKey = "",
    [string]$LLMApiURL = "https://api.siliconflow.cn/v1/chat/completions",
    [string]$LLMModel = "THUDM/GLM-Z1-9B-0414",
    [string]$HithinkApiKey = "",
    # §N-5 磁盘密钥文件（KEY=VALUE，一行一键，# 起注释）——Windows 侧的 /etc/quant.env 对位物，
    # 口径见 PROJECT_DOCUMENT.md「LLM_API_KEY 不传则保留服务器现有 C:\etc\quant.env」。
    # 文件里放 LLM_API_KEY / LLM_API_URL / LLM_MODEL / HITHINK_FINANCE_API_KEY 四键即可；
    # 它含明文密钥，**NTFS ACL 必须收敛到 Administrators**（同 C:\opt\quant\tools\restic-pass.txt 的口径），
    # 且本脚本永不打印它的内容——只打印键名与"取到了几个键"。
    [string]$SecretFile = "C:\etc\quant.env",
    [string]$NSSMUrl = "https://nssm.cc/release/nssm-2.24.zip"
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[eng] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# §H8(2026-09-22)：端口/端点唯一来源收编到 service_probe_config.ps1——本脚本与运维探针
# （all_service_watchdog.ps1 / daily_ops_check.ps1）读同一组变量，根除"部署端口与探针端口各改各的"
# （H8 实录：watchdog 硬编码 :8080/api/status，而这里早已改注 QUANT_ADDR=127.0.0.1:8081）。
# 配置缺失时回退为本文件旧字面量并告警，不阻断注册流程。
$probeCfg = Join-Path $PSScriptRoot "service_probe_config.ps1"
if (Test-Path $probeCfg) {
    . $probeCfg
    Info "probe config loaded: $probeCfg (§H8 端口同源)"
} else {
    Warn "missing $probeCfg - fallback to in-script literals (§H8：运维探针将与本脚本端口脱钩，请补齐部署清单)"
    $ProbeQuantPort  = 8081
    $ProbePydataPort = 8787
    $ProbeGatewayUrl = "http://127.0.0.1:8789/health"
}

# ── §N-5(2026-09-23 晚批) 密钥解析：参数缺省时从磁盘密钥文件 / 机器级环境变量取，绝不"缺省即跳过" ──
# 缺陷本体（三重叠加，缺一不会出事）：
#   ① $LLMApiKey/$HithinkApiKey 默认 ""；② Get-BaseEnvExtra 只在值非空时追加对应键（条件追加）；
#   ③ nssm set AppEnvironmentExtra 是**整体替换**语义（下面原 :109-110 注释自己承认"必须带全量"）。
#   ⇒ 任何一次不带密钥参数重跑（改端口/修故障/二次部署）都会把上一次注入的密钥键**静默删掉**，
#     而且 LLM 三元组当年连 Warn 都没有（只有 Hithink 有）。RUNBOOK §4.1b.1 ③ 甚至据此把
#     "注册脚本不要顺手重跑"写成了运维纪律——那是用运维规矩绕脚本缺陷，本批把它改对。
# 优先级：显式传参 > $SecretFile 磁盘密钥文件 > 机器级环境变量 > 参数内置默认。
#   机器级这一层是**复用现成实现**：deploy/qmt-win/run_ths_backfill.ps1:10 就用
#   [Environment]::GetEnvironmentVariable('HITHINK_FINANCE_API_KEY','Machine') 取同一把 key，
#   现网该键长期只存在于机器级（RUNBOOK §4.1b.2 顺带核查记录，长度 41），不读它=等于没有。
# 安全铁律：本脚本任何路径（Info/Ok/Warn/Die/异常消息/退出码前的摘要）都**不得输出密钥值**，
#   只输出键名与"命中来源"。日志里出现密钥明文按事故处理。

# 读磁盘密钥文件：KEY=VALUE 一行一键，# 注释、空行忽略，值两端引号剥掉；文件不存在返回空表。
function Read-SecretFile([string]$path) {
    # 键名大小写不敏感：Windows 环境变量本就不区分大小写，密钥文件里写 llm_api_key 也得认。
    # 逗号 return：Dictionary 是 IEnumerable，裸 return 会被 PowerShell 拆成一堆 DictionaryEntry。
    $map = New-Object 'System.Collections.Generic.Dictionary[string,string]' -ArgumentList ([System.StringComparer]::OrdinalIgnoreCase)
    if (-not (Test-Path $path)) { return ,$map }
    foreach ($raw in (Get-Content -Path $path -Encoding UTF8)) {
        $line = "$raw".Trim()
        if ($line -eq '' -or $line.StartsWith('#')) { continue }
        $i = $line.IndexOf('=')
        if ($i -lt 1) { continue }
        $k = $line.Substring(0, $i).Trim()
        $v = $line.Substring($i + 1).Trim().Trim('"').Trim("'")
        if ($v) { $map[$k] = $v }
    }
    return ,$map
}

# 解析单个键的最终值（不回显）。$explicitParam 用 $PSBoundParameters 判定"操作人真的传了"，
# 传了就以它为准（保留旧参数式入口的全部兼容性）；没传才依次向下找。
function Resolve-Secret([string]$name, [string]$fallback, [bool]$explicitParam) {
    if ($explicitParam -and $fallback) { return $fallback }
    if ($script:secretFileMap.ContainsKey($name)) { return $script:secretFileMap[$name] }
    $m = [Environment]::GetEnvironmentVariable($name, 'Machine')
    if ($m) { return $m }
    return $fallback
}

$script:secretFileMap = Read-SecretFile $SecretFile
if (Test-Path $SecretFile) {
    Info "密钥文件已加载: ${SecretFile}（可用键数 $($script:secretFileMap.Count)，值不回显）"
} else {
    Warn "密钥文件不存在: $SecretFile —— 本轮密钥只从显式参数/机器级环境变量取；两者都没有的键会在尾部断言处判红（§N-5）"
}
$LLMApiKeyResolved  = Resolve-Secret 'LLM_API_KEY'               $LLMApiKey     ($PSBoundParameters.ContainsKey('LLMApiKey'))
$LLMApiURLResolved  = Resolve-Secret 'LLM_API_URL'               $LLMApiURL     ($PSBoundParameters.ContainsKey('LLMApiURL'))
$LLMModelResolved   = Resolve-Secret 'LLM_MODEL'                 $LLMModel      ($PSBoundParameters.ContainsKey('LLMModel'))
$HithinkResolved    = Resolve-Secret 'HITHINK_FINANCE_API_KEY'   $HithinkApiKey ($PSBoundParameters.ContainsKey('HithinkApiKey'))

# §ENH-0(2026-09-19)：统一构造服务级环境变量。此前 LLM 三元组之外的密钥（尤其
# HITHINK_FINANCE_API_KEY=交易日历/行情主源）从不注入，quant 生产进程一直缺它。
# §N-5：键值改为上面的"解析结果"，且四个键各自独立判定（旧版把 URL/MODEL 绑在
# 有 KEY 的分支里，缺 KEY 会把本来有默认值的两个非密钥键一起丢掉）。
# 逗号 return 防 PowerShell 单元素数组被标量化。
function Get-BaseEnvExtra {
    $e = @("TZ=Asia/Shanghai", "QUANT_DATA_DIR=$DataDir")
    if ($LLMApiKeyResolved) { $e += @("LLM_API_KEY=$LLMApiKeyResolved") }
    if ($LLMApiURLResolved) { $e += @("LLM_API_URL=$LLMApiURLResolved") }
    if ($LLMModelResolved)  { $e += @("LLM_MODEL=$LLMModelResolved") }
    if ($HithinkResolved)   { $e += @("HITHINK_FINANCE_API_KEY=$HithinkResolved") }
    return ,$e
}

# 读回服务级 AppEnvironmentExtra 现值（REG_MULTI_SZ，逐行）。**这是唯一允许看到密钥值的读取点**：
# 返回值只进内存变量供并集用，全脚本任何日志/异常路径都不得把它写出去（同上安全铁律）。
function Get-ExistingEnvExtra([string]$svc) {
    # 临时降 EAP：PS 5.1 在 Stop 下会把原生命令的 stderr 行转成 NativeCommandError 直接终止
    # （§ENH-A run_ths_backfill.ps1 09-20 实录），nssm get 对"未设置该项"恰好会往 stderr 写提示。
    $eapPrev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $raw = & $nssm get $svc AppEnvironmentExtra 2>$null
    $code = $LASTEXITCODE
    $ErrorActionPreference = $eapPrev
    if ($code -ne 0 -or -not $raw) { return @() }
    $list = @()
    foreach ($line in (("$raw" | Out-String) -split "`r?`n")) {
        $t = $line.Replace([string][char]0, '').Trim()   # REG_MULTI_SZ 尾随 NUL 清洗
        if ($t -match '^[A-Za-z_][A-Za-z0-9_]*=') { $list += $t }
    }
    return ,$list
}

# 服务级 env 的唯一写入点（verify 第 62 号锁的口径：AppEnvironmentExtra 的 set 必须经这里，
# 且实参必须来自 Get-BaseEnvExtra）。并集语义：现值先入表，本脚本给出的键覆盖同名键，
# 未被提及的键**原样保留** ⇒ 从根上取消"少传一个参数=删一个变量"这条路径。
# 已知边界：若某键值本身含换行，nssm get 读回会被拆成两条非法行（正则已过滤，不会被写回），
# 现网这四个键都是单行；真出现时由尾部键名断言暴露，而不是静默丢键。
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
    if ($final.Count -eq 0) { Die "refusing to write empty AppEnvironmentExtra for $svc" }
    & $nssm set $svc AppEnvironmentExtra $final | Out-Null
    Info ("$svc AppEnvironmentExtra 键名: " + (($order | Sort-Object) -join ",") + "（值不回显）")
}


# 0. admin check
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Die "run as Administrator"
}

# 1. prepare nssm
$tools = Join-Path $PSScriptRoot "tools"
$nssm = Join-Path $tools "nssm-2.24\win64\nssm.exe"
if (-not (Test-Path $nssm)) {
    New-Item $tools -ItemType Directory -Force | Out-Null
    Info "downloading nssm ..."
    try {
        Invoke-WebRequest -Uri $NSSMUrl -OutFile (Join-Path $tools "nssm.zip") -UseBasicParsing -TimeoutSec 60
        Expand-Archive (Join-Path $tools "nssm.zip") $tools -Force
    } catch {
        Die "nssm download failed: $($_.Exception.Message)"
    }
}
if (-not (Test-Path $nssm)) { Die "nssm not found at $nssm" }

# helper: register a service with base props + env + log rotation
function Register-NssmService($name, $exe, $appArgs, $priority) {
    Info "registering service $name ..."
    & $nssm install $name $exe @appArgs | Out-Null
    & $nssm set $name AppDirectory (Split-Path -Parent $exe) | Out-Null
    & $nssm set $name AppPriority $priority | Out-Null
    & $nssm set $name AppRestartDelay 5000 | Out-Null
    & $nssm set $name AppExit Default Restart | Out-Null
    & $nssm set $name AppRotateFiles 1 | Out-Null
    & $nssm set $name AppRotateOnline 1 | Out-Null
    & $nssm set $name AppRotateBytes 10485760 | Out-Null
    & $nssm set $name Start SERVICE_AUTO_START | Out-Null
    # §N-5：走 Set-ServiceEnvExtra（读现值 → 并集 → 一次写入），不再裸写 AppEnvironmentExtra。
    Set-ServiceEnvExtra $name (Get-BaseEnvExtra)
}

# 2. quant (NORMAL)
if (-not (Test-Path $QuantExe)) { Die "missing $QuantExe" }
Register-NssmService "quant" $QuantExe @() "NORMAL_PRIORITY_CLASS"
# §部署修复 2026-09-17：端口必须是 127.0.0.1:8081——广州拓扑下 Caddy 占用 :8080
# （Caddyfile /api/* → reverse_proxy 127.0.0.1:8081）。旧值 0.0.0.0:8080 与 Caddy
# 撞端口，配合 §W4-b fail-fast 会让 quant 服务起不来（5s 重启循环，部署实录）。
# §ENH-0(2026-09-19)：env 统一走 Get-BaseEnvExtra（含可选 HITHINK_FINANCE_API_KEY）。
# §N-5(2026-09-23)：这里原本是裸 `nssm set quant AppEnvironmentExtra (全量 + QUANT_ADDR)`——
# AppEnvironmentExtra 是整体替换语义，所以旧注释要求"必须带全量再叠 QUANT_ADDR"，
# 而"全量"取决于本次命令行传了哪些密钥 → 少传即少删。现改走 Set-ServiceEnvExtra：
# 先 nssm get 读回现值做并集，再一次性写回，QUANT_ADDR 只是叠加的一个键，**不再有删东西的能力**。
Set-ServiceEnvExtra "quant" ((Get-BaseEnvExtra) + @("QUANT_ADDR=127.0.0.1:$ProbeQuantPort"))
if (-not $HithinkResolved) {
    Warn "HITHINK_FINANCE_API_KEY 三处来源（参数/密钥文件/机器级环境变量）都没取到：交易日历将按周末口径兜底（法定节假日会误判为交易日），尾部键名断言会判红"
}
& $nssm restart quant
Start-Sleep -Seconds 2
Ok "quant registered/restarted"

# 3. quant-research (BELOW_NORMAL)
if (-not (Test-Path $ResearchExe)) { Die "missing $ResearchExe" }
Register-NssmService "quant-research" $ResearchExe @() "BELOW_NORMAL_PRIORITY_CLASS"
& $nssm restart quant-research
Start-Sleep -Seconds 2
Ok "quant-research registered/restarted"

# 4. pydata (baostock sidecar, port 8787)
$pyExe = Join-Path $PydataVenv "Scripts\python.exe"
$pyScript = "C:\opt\quant\pydata\server.py"
if (-not (Test-Path $pyScript)) {
    Warn "missing $pyScript - skip pydata"
} elseif (-not (Test-Path $pyExe)) {
    Warn "missing venv python $pyExe - skip pydata (run setup_venv first)"
} else {
    Register-NssmService "pydata" $pyExe @("$pyScript", "--host", "127.0.0.1", "--port", "$ProbePydataPort") "BELOW_NORMAL_PRIORITY_CLASS"
    & $nssm set pydata AppDirectory "C:\opt\quant\pydata" | Out-Null
    & $nssm restart pydata
    Start-Sleep -Seconds 2
    Ok "pydata registered/restarted (127.0.0.1:$ProbePydataPort)"
}

# 5. qmtctl scheduled task (interactive session) - generate a wrapper ps1 to avoid nested quoting
if (-not (Test-Path $QmtctlExe)) {
    Warn "missing $QmtctlExe - skip qmtctl task"
} else {
    $wrapper = Join-Path $PSScriptRoot "ensure_miniqmt.ps1"
    $wrapContent = "& '$QmtctlExe' ensure-miniqmt -path '$MiniQmtPath' -gateway-url $ProbeGatewayUrl"
    # UTF8（PS5.1 带 BOM）：MiniQmtPath 常含中文安装目录，ASCII 会写成 '?' 导致启动失败
    Set-Content -Path $wrapper -Value $wrapContent -Encoding UTF8
    $taskName = "QMT-Ensure-Running"
    # §FIX 2026-08-31：无窗口包装——wscript(GUI 子系统)经 VBS 隐藏运行，根除交互任务
    # 每 10 分钟的黑框闪烁（"监控闪退"观感）。VBS 内容 ASCII（wscript 不认 UTF-8 BOM）。
    $vbs = Join-Path $PSScriptRoot "run_qmt_ensure.vbs"
    [IO.File]::WriteAllText($vbs, "CreateObject(""WScript.Shell"").Run ""powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper"", 0, True", (New-Object Text.ASCIIEncoding))
    $action = "wscript.exe //B $vbs"
    # no /RU SYSTEM: runs in the logged-on interactive session (MiniQMT needs GUI session)
    schtasks /Create /F /SC MINUTE /MO 10 /TN $taskName /TR $action
    if ($LASTEXITCODE -eq 0) { Ok "task $taskName created (every 10 min, interactive)" }
    else { Warn "schtasks create failed (exit=$LASTEXITCODE); create manually (current user, not SYSTEM)" }
}

# 6. §RFIX-5 日志保留计划任务（每日 07:30，SYSTEM 可无窗执行——纯文件清理不涉 GUI）
$prune = Join-Path $PSScriptRoot "prune_logs.ps1"
if (Test-Path $prune) {
    schtasks /Create /F /SC DAILY /ST 07:30 /TN "Quant-Log-Prune" /TR "powershell -NoProfile -ExecutionPolicy Bypass -File $prune" | Out-Null
    if ($LASTEXITCODE -eq 0) { Ok "task Quant-Log-Prune created (daily 07:30, keep 20 rotations)" }
    else { Warn "Quant-Log-Prune 创建失败（exit=$LASTEXITCODE）：轮转日志将无限累积，请手动创建" }
} else {
    Warn "missing $prune - skip log-prune task（researchd/quant_stderr 轮转日志不会自动清理）"
}

# ── 7. §N-5 收尾：服务运行环境键名存在性断言（缺键即 Warn + 非零退出，降级不得报成功）────
# 硬规矩：**只断言键名、绝不回显值**——本段以及它调用的 Get-ExistingEnvExtra 的输出里
#   任何时候出现密钥明文都算事故。所以这里比对的集合只有键名，Warn 也只拼键名。
# 为什么是"红"不是" Warn 后继续"：旧版最坏的地方不是没告警，而是**洗掉密钥后仍然打印
#   "engine services registered"**——部署脚本 $?/退出码全绿，引擎却从此没有 LLM/日历凭据。
#   与同批 N-6「降级不得报成功」同口径：宁可这次部署失败并吵起来，也不交出缺键的运行态。
# 注意：quant-research 不监听端口，故不要求 QUANT_ADDR；pydata 是 baostock sidecar，
#   不吃 LLM/HITHINK 键，故不进断言（把不该有的键写成要求会造出永久性假红）。
$envRequired = @{
    "quant"          = @("TZ", "QUANT_DATA_DIR", "QUANT_ADDR", "LLM_API_KEY", "LLM_API_URL", "LLM_MODEL", "HITHINK_FINANCE_API_KEY")
    "quant-research" = @("TZ", "QUANT_DATA_DIR", "LLM_API_KEY", "LLM_API_URL", "LLM_MODEL", "HITHINK_FINANCE_API_KEY")
}
$envMissing = @()
foreach ($svc in ($envRequired.Keys | Sort-Object)) {
    $have = New-Object 'System.Collections.Generic.Dictionary[string,byte]' -ArgumentList ([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($kv in (Get-ExistingEnvExtra $svc)) {
        $i = $kv.IndexOf("=")
        if ($i -gt 0) { $have[$kv.Substring(0, $i)] = 1 }   # 只留键名，值就地丢弃
    }
    foreach ($k in $envRequired[$svc]) {
        if (-not $have.ContainsKey($k)) {
            Warn "服务 $svc 的运行环境缺键 $k（只报键名，值不回显）"
            $envMissing += ("{0}.{1}" -f $svc, $k)
        }
    }
}
if ($envMissing.Count -gt 0) {
    Warn ("§N-5 部署判失败：服务运行环境缺 " + $envMissing.Count + " 个键 -> " + ($envMissing -join ", "))
    Warn ("排查：把缺失键写进密钥文件 ${SecretFile}（KEY=VALUE）或机器级环境变量，或用 -LLMApiKey/-HithinkApiKey 传入后重跑；" +
          "验证命令（只看键名）：& `"$nssm`" get quant AppEnvironmentExtra")
    exit 1
}
Ok "服务 env 键名断言通过：quant 7 键 / quant-research 6 键在位（全程未回显任何值）"

Ok "engine services registered. Verify: Get-Service quant,quant-research,pydata ; schtasks /Query /TN QMT-Ensure-Running"
