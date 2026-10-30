# rotate_qmt_token.ps1 — §QMT-TOKENROT（2026-09-23）：QMT 网关 token 可轮换 + 指纹可比对。
# 背景：网关鉴权 token 同时活在最多五个地方，必须一起动，而今天没有任何东西检查它们一致：
#   1) 网关配置文件 config.xt.json 的 token / report_token（ensure_gateway_config.ps1 生成）；
#   2) 进程环境变量**覆盖文件**：QUANT_GATEWAY_TOKEN / QUANT_GATEWAY_REPORT_TOKEN
#      （qmt_gateway/gateway.py :186/:199；经 NSSM 服务 AppEnvironmentExtra 落盘）；
#   3) 引擎侧 $QUANT_DATA_DIR\config.json 的 rules.qmt.token（权威写入方＝web 设置页）；
#   4) 桥客户端 qmt_bridge.py --token（或同目录 config.bridge.json 的 token）；
#   5) §0926ROT-SRC5（2026-09-26 发版实录补腿）：引擎账号级快照 $QUANT_DATA_DIR\auth.json 的
#      configs[]（key=quant_config_json_v1）里 value（整棵 Rules 的 JSON 串）中 .qmt.token——
#      GetQMTConfigFor 的优先级是「账号自身覆盖→运营覆盖→全局」，快照不改则真实下单永远拿旧
#      口令（09-26 实录：轮换+对齐后 verify 第 20 探针仍红 engineAuth 分歧＝周一开盘链路无凭据）。
#      注意：auth store 是引擎启动时载入内存（auth.go m.db），改完文件必须重启 quant 服务才吃到。
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
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\rotate_qmt_token.ps1 -Align -Apply  # 对齐落地：源1 现值→源3/源4/源5
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
    # 以源1（config.xt.json，网关真源）现值为权威，把它复制到源3（引擎 config.json rules.qmt.token）、
    # 源4（config.bridge.json，如在位）与源5（auth.json 账号快照 .qmt.token，§0926ROT-SRC5）。
    # 值只在服务器本地函数栈里过，输出一律指纹。
    [switch]$Align,
    # §TOKEN-PERAACCT（2026-10-30）：一把钥匙铺给所有账号的旧对齐语义就此止步——
    #  - LegacySpread 是把"源3/源5 仍按源1 复制"的旧路径显式留住的历史开关，**缺省关闭**；
    #    关闭时 -Align 只对齐源4（桥，物理上必须与网关同钥匙），源3/源5 改为"报告 + 指引签发通道"。
    #  - RegisterAccount：把一把**已由引擎侧签发到该账号快照**的钥匙登记进网关 account_tokens 注册表；
    #    钥匙从 stdin 读入（不进命令行、不进日志、不回显），只打印指纹。
    #  - ShowRegistry：只读列出网关注册表与引擎侧各账号快照的持有关系（一把钥匙两个主人＝红）。
    # English: per-account credentials — -Align no longer spreads one key to every account unless
    # -LegacySpread is passed explicitly; -RegisterAccount adds a key from stdin; -ShowRegistry audits holders.
    [switch]$LegacySpread,
    # §TOKEN-PERAACCT：把"这个资金账号的账归谁"写进网关配置（持仓/资金这类账户级事件的归属），
    # 而不是靠 `user_id` 空串回退到"users 顺序第一个命中者"猜。
    [string]$SetOwner = "",
    [string]$RegisterAccount = "",
    # §TOKEN-PERAACCT 撤销通道：把某账号快照里的实盘凭证清掉（只动它自己那一条），
    # 以及把引擎全局 rules.qmt.token 清空（N1：兜底位不该持有实盘凭证）。
    # 两者都是"摘钥匙"而不是"换钥匙"：摘掉后该账号即使打开实盘开关，敲到网关也只是 401。
    # §TOKEN-PERAACCT 逐账号签发：生成一把新钥匙并同时落到**两侧**
    # （网关 account_tokens 注册表 + 该账号自己的配置快照 .qmt.token）。明文只在服务器本地函数栈里过，
    # 输出一律指纹——这也是为什么没有"生成后打印一次给用户抄"的形态（那会把实盘凭证落进控制台历史）。
    [string]$Issue = "",
    [switch]$WriteSnapshot,
    [switch]$TokenFromGenerate,
    [string]$RevokeAccount = "",
    # 撤销时默认只清"等于网关现钥匙"的那把；该账号连的是别的网关时需显式确认才清空
    [switch]$RevokeAnyToken,
    [switch]$ClearGlobal,
    [switch]$ShowRegistry
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
# §TOKEN-PERAACCT：引擎侧账号快照的路径要在**各写模式之前**就定好——旧版把它留在源5 读侧
# （本行之后几百行），于是 -Issue 里取到的是 $null ⇒ Test-Path 直接抛（22:40 副本实测）。
# 就地重复定义一次，读侧那行保持原样（§102 的 src5 锚点计数依赖它）。
$authPath = Join-Path $DataDir "auth.json"

# ── §TOKEN-PERAACCT（2026-10-30）网关注册表通道：只读检视 + 逐账号登记 ──────────────
# 为什么需要它：一把 `token` 铺给所有 App 账号时，"这把钥匙属于谁"在网关侧不可判定，
# 回报只能按账号表顺序猜（现网七个账号同一把 ⇒ 下单方自己那行永远没人推进）。
# 从这一刻起每账号一把钥匙登记进 `account_tokens`，网关鉴权按钥匙反查账号。
# 边界（刻意的）：本脚本**不碰 auth.json**——引擎侧账号快照是引擎的私有数据，
# 由它自己的 API/设置页写（有写前快照、写后复读与审计），这里只负责网关这一侧。
function Get-AuthTokenRegistry($obj) {
    $items = @()
    foreach ($t in @($obj.account_tokens)) {
        if ($null -eq $t) { continue }
        $tkT = [string]$t.token; $tkU = [string]$t.user_id
        if ($tkT -and $tkU) { $items += [pscustomobject]@{ user_id = $tkU; token = $tkT; src = "account_tokens" } }
    }
    if ([string]$obj.token) {
        $legacyUid = [string]$obj.owner_user_id
        if (-not $legacyUid) { $legacyUid = [string]$obj.user_id }
        $items += [pscustomobject]@{ user_id = $legacyUid; token = [string]$obj.token; src = "legacy_token" }
    }
    return ,@($items)
}
# -Issue 是"登记 + 生成 + 落两侧"的组合别名：走的是同一个写模式分支（同一套拒判与自证），
# 不另开第二条写路径——两条写路径迟早漂移，出事时没人知道哪条是真的。
if ($Issue) {
    if ($RegisterAccount -and $RegisterAccount -ne $Issue) { Die "Issue refused: -Issue 与 -RegisterAccount 同时给了且不一致" }
    $RegisterAccount = $Issue
    $TokenFromGenerate = $true
    $WriteSnapshot = $true
}

# ── 账号快照 token 的条目窗口手术（§TOKEN-PERAACCT 的签发/撤销共用）──────────────
# auth.json 的 configs[] 每条是 {key, value, user_id}，而 value 是**字符串化的整棵 Rules**
# （内层引号带反斜杠）。所以不能整文件反序列化重排（PS5.1 的 ConvertTo-Json 会把没动过的字段
# 也洗一遍形态），只能"在目标条目窗口内原文替换"。
# 唯一性按 **(key, user_id)** 判：一个账号在 configs[] 里本来就有 llm_api_key/consult_pro_mode
# 等多条，只按 uid 找必然"命中多处"——那是判据写错，不是数据有问题。
function Get-SnapshotTokenWindow {
    param([string]$Raw, [string]$Uid)
    $needle = '"user_id":"' + $Uid + '"'
    if ($Raw.IndexOf($needle) -lt 0) { $needle = '"user_id": "' + $Uid + '"' }
    if ($Raw.IndexOf($needle) -lt 0) { throw ("auth.json 原文里没有 user_id=" + $Uid + " 的配置条目") }
    $cands = @()
    $scan = 0
    while (($scan = $Raw.IndexOf($needle, $scan)) -ge 0) {
        $anyKey = $Raw.LastIndexOf('"key":"', $scan)
        if ($anyKey -lt 0) { $anyKey = $Raw.LastIndexOf('"key": "', $scan) }
        $qKey = $Raw.LastIndexOf('"key":"quant_config_json_v1"', $scan)
        if ($qKey -lt 0) { $qKey = $Raw.LastIndexOf('"key": "quant_config_json_v1"', $scan) }
        if ($qKey -ge 0 -and $qKey -eq $anyKey) { $cands += [pscustomobject]@{ k = $qKey; n = $scan } }
        $scan += $needle.Length
    }
    if (@($cands).Count -ne 1) {
        throw ("(quant_config_json_v1, " + $Uid + ") 命中 " + @($cands).Count + " 条（应为恰 1，无法唯一确定条目）")
    }
    $kIdx = [int]$cands[0].k
    # 右边界从**这一条自己的** uid 锚点往后找：第一次命中可能属于同账号的另一条配置
    $eIdx = $Raw.IndexOf('}', [int]$cands[0].n + $needle.Length)
    if ($eIdx -lt 0) { throw "条目右边界没找到（形态不认识）" }
    return @{ start = $kIdx; end = $eIdx; win = $Raw.Substring($kIdx, $eIdx - $kIdx + 1) }
}

function Set-SnapshotTokenRaw {
    param([string]$Raw, [string]$Uid, [string]$OldTok, [string]$NewTok)
    $w = Get-SnapshotTokenWindow -Raw $Raw -Uid $Uid
    $escOld = '\"token\":\"' + $OldTok + '\"'
    $n = ([regex]::Matches($w.win, [regex]::Escape($escOld))).Count
    if ($n -ne 1) {
        throw ("条目内旧钥匙命中 " + $n + " 次（应为恰 1）——不猜该改哪一处")
    }
    $winNew = $w.win.Replace($escOld, ('\"token\":\"' + $NewTok + '\"'))
    return @{ text = $Raw.Substring(0, $w.start) + $winNew + $Raw.Substring($w.end + 1); delta = $winNew.Length - $w.win.Length }
}

if ($SetOwner) {
    # 只改一个字符串字段，形与 -ClearGlobal 同款（锚点窗口外科替换 + 提案自证 + 备份 + 复读）。
    if ($SetOwner -notmatch '^[A-Za-z0-9_.:@-]{1,64}$') { Die "SetOwner refused: user_id 形态非法" }
    $oRaw = Get-Content -LiteralPath $ConfigFile -Raw -Encoding UTF8
    $oObj = $null
    try { $oObj = $oRaw | ConvertFrom-Json } catch { Die ("SetOwner refused: config.xt.json 解析失败（" + $_.Exception.Message + "）") }
    if ((FpLabel([string]$oObj.owner_user_id)) -eq (FpLabel $SetOwner)) {
        Ok ("set owner done action=same uid=" + $SetOwner); exit 0
    }
    $oNew = $null
    if ($oRaw -match '"owner_user_id"') {
        try { $oNew = Set-JsonStringFieldRaw -Text $oRaw -Anchor '' -Field 'owner_user_id' -Value $SetOwner -Check { param($o) ([string]$o.owner_user_id) -eq $SetOwner } } catch { Die ("SetOwner refused: " + $_.Exception.Message) }
    } else {
        # 键不存在：插到 "user_id" 之后（没有该键就插到 "account" 之后），逗号由替换文本自带
        $reIns = [regex]'("(?:user_id|account)"\s*:\s*"[^"]*")'
        $mIns = $reIns.Match($oRaw)
        if (-not $mIns.Success) { Die "SetOwner refused: 找不到可插入的锚点键（user_id/account 都不在，形态不认识，停手）" }
        $oNew = $oRaw.Substring(0, $mIns.Index + $mIns.Length) + (', "owner_user_id": "' + $SetOwner + '"') + $oRaw.Substring($mIns.Index + $mIns.Length)
    }
    $oChk = $null
    try { $oChk = $oNew | ConvertFrom-Json } catch { Die "SetOwner refused: 提案文本解析不过（手术切坏形态，拒写）" }
    if ([string]$oChk.owner_user_id -ne $SetOwner) { Die "SetOwner refused: 提案里 owner 未命中目标值（切错位置嫌疑）" }
    foreach ($f in @('token', 'report_token', 'broker', 'account', 'report_url', 'db')) {
        if ([string]$oChk.$f -ne [string]$oObj.$f) { Die ("SetOwner refused: 字段 " + $f + " 在提案里变了——手术刀不干净，拒写") }
    }
    Info ("set owner plan was=" + (FpLabel([string]$oObj.owner_user_id)) + " to=" + (FpLabel $SetOwner) + "（uid 不是秘密，明文可回显但这里仍只报指纹）")
    if (-not $Apply) { Ok "set owner plan-only（未写；加 -Apply 落地）"; exit 0 }
    $stampO = Get-Date -Format "yyyyMMddHHmmss"
    $bakO = $ConfigFile + ".pre-setowner-" + $stampO
    Copy-Item -LiteralPath $ConfigFile -Destination $bakO -Force
    $tmpO = $ConfigFile + ".settingowner-" + $stampO
    [System.IO.File]::WriteAllText($tmpO, $oNew, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $tmpO -Destination $ConfigFile -Force
    $backO = $null
    try { $backO = Get-Content -LiteralPath $ConfigFile -Raw -Encoding UTF8 | ConvertFrom-Json } catch { Die ("SetOwner read-back parse failed（回滚副本: " + $bakO + "）") }
    if ([string]$backO.owner_user_id -ne $SetOwner) { Die ("SetOwner read-back mismatch（回滚副本: " + $bakO + "）") }
    Ok ("set owner done action=written uid=" + $SetOwner + " backup=" + $bakO)
    Info "生效前提：重启 qmt_gateway 后，持仓/资金这类账户级事件才按这个账号归因（restart_gateway.ps1 / QMT-Gateway-Ensure）"
    exit 0
}
if ($ShowRegistry) {
    $reg0 = Get-AuthTokenRegistry $cfg
    Info ("registry entries=" + @($reg0).Count)
    foreach ($r in @($reg0)) {
        Info ("  uid=" + $r.user_id + " token_fp=" + (FpLabel $r.token) + " src=" + $r.src)
        if (-not $r.user_id) { Warn "  该条没有 user_id ⇒ 网关无法据这把钥匙归因，请补齐或删掉" }
    }
    foreach ($g in (@($reg0) | Where-Object { $_.user_id } | Group-Object token)) {
        if (@($g.Group).Count -gt 1) {
            Warn ("同一把钥匙挂 " + @($g.Group).Count + " 个账号 fp=" + (FpLabel $g.Name) + " ⇒ 归属不可判定，请撤销多余那条")
        }
    }
    exit 0
}
if ($RegisterAccount) {
    # user_id 只允许安全字符集：它要被拼进 JSON 字面量，不接受"顺手传个引号"
    if ($RegisterAccount -notmatch '^[A-Za-z0-9_.:@-]{1,64}$') {
        Die "RegisterAccount refused: user_id 形态非法（只允许字母数字与 . _ : @ -）"
    }
    # 钥匙来源二选一：① stdin 读入（不进命令行、不进日志、不回显）；
    # ② 本地生成 48 位 hex（-Issue 走这条；明文只进函数栈，绝不打印——打印就等于把实盘凭证
    #    落进 PS 控制台历史与 SSH 会话记录）。
    $rtok = ""
    if ($TokenFromGenerate) {
        # 用 RandomNumberGenerator（CSP 实现），**不要**写 [System.Security.Cryptography+RNGCryptoServiceProvider]：
        # 那个 `+` 是 C# 的嵌套类型写法，PS5.1 认不出来 ⇒ New-Object 直接 TypeNotFound，
        # 而本脚本非终止错误形态下会带着空钥匙继续往下走——正是"生成失败却像成功"的形状。
        try {
            $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
            $buf = New-Object 'byte[]' 24
            $rng.GetBytes($buf)
            $rng.Dispose()
            $rtok = -join ($buf | ForEach-Object { $_.ToString("x2") })
        } catch {
            Die ("RegisterAccount refused: 本地生成钥匙失败（" + $_.Exception.Message + "）——拒绝用空钥匙登记")
        }
        if ($rtok.Length -lt 32) { Die "RegisterAccount refused: 生成的钥匙长度异常，停手" }
    } else {
        $rtokRaw = [Console]::In.ReadLine()
        $rtok = ("$rtokRaw").Trim()
    }
    if (-not $rtok) { Die "RegisterAccount refused: 没拿到钥匙（stdin 空；-Issue 生成失败）——拒绝空登记" }
    if ($rtok -notmatch '^[A-Za-z0-9]{32,128}$') { Die "RegisterAccount refused: 钥匙形态异常（要求 32-128 位字母数字；明文不回显）" }
    $rtFp = FpLabel $rtok
    # 签发的前置确认（顺序有意义）：要落两侧，就先验**引擎侧那一半落得下去**，
    # 再去改网关注册表。反过来的失败形态是"网关多出一条谁都用不上的钥匙"——
    # 不致命，但会让 -ShowRegistry 的读数骗人（看着像已签发完成）。
    $snProp = $null
    $snObj0 = $null
    $snRaw = ""
    if ($WriteSnapshot) {
        if (-not (Test-Path $authPath)) { Die ("RegisterAccount refused: -Issue 需要引擎侧快照，但 auth.json 不在位 " + $authPath) }
        $snRaw = Get-Content -LiteralPath $authPath -Raw -Encoding UTF8
        try { $snObj0 = $snRaw | ConvertFrom-Json } catch {
            Die ("RegisterAccount refused: auth.json 解析失败（" + $_.Exception.Message + "）——结构不可信，网关侧也一并停手")
        }
        # 现值从**解析出的对象**里取（不在原文上正则捞：22:41 副本实测那种捞法在转义层不稳）
        $snOld = ""
        $snFound = $false
        foreach ($e0 in @($snObj0.configs)) {
            if ([string]$e0.user_id -ne $RegisterAccount -or [string]$e0.key -ne 'quant_config_json_v1') { continue }
            $v0 = $null
            try { $v0 = "$($e0.value)" | ConvertFrom-Json } catch { Die "RegisterAccount refused: 该账号快照 value 解析失败（形态不认识，停手）" }
            $snOld = [string]$v0.qmt.token; $snFound = $true
        }
        if (-not $snFound) {
            Die ("RegisterAccount refused: 该账号还没有 quant_config_json_v1 快照——先在设置页保存一次它的 QMT 配置" +
                 "（由引擎建快照）再签发；本次网关侧与引擎侧都一个字未改")
        }
        try { $snProp = Set-SnapshotTokenRaw -Raw $snRaw -Uid $RegisterAccount -OldTok $snOld -NewTok $rtok } catch {
            Die ("RegisterAccount refused: " + $_.Exception.Message + " —— 提案算不出来，两侧都不改")
        }
        $snChk0 = $null
        try { $snChk0 = $snProp.text | ConvertFrom-Json } catch { Die "RegisterAccount refused: 快照提案文本解析不过（手术切坏形态，拒写）" }
        if (($snProp.text.Length - $snRaw.Length) -ne $snProp.delta) { Die "RegisterAccount refused: 快照提案字节差与预测不符（存在计划外命中）" }
        $snHit = ""; $snMoved = 0
        foreach ($e1 in @($snChk0.configs)) {
            if ($null -eq $e1 -or [string]$e1.key -ne 'quant_config_json_v1') { continue }
            $v1 = $null
            try { $v1 = "$($e1.value)" | ConvertFrom-Json } catch { Die ("RegisterAccount refused: 提案里 " + [string]$e1.user_id + " 的 value 解析不过") }
            if ([string]$e1.user_id -eq $RegisterAccount) { $snHit = [string]$v1.qmt.token; continue }
            $o0 = $null
            foreach ($e0 in @($snObj0.configs)) { if ([string]$e0.user_id -eq [string]$e1.user_id -and [string]$e0.key -eq 'quant_config_json_v1') { $o0 = $e0 } }
            if ($null -ne $o0) {
                $ov = $null
                try { $ov = "$($o0.value)" | ConvertFrom-Json } catch { $ov = $null }
                if ((FpLabel([string]$ov.qmt.token)) -ne (FpLabel([string]$v1.qmt.token))) { $snMoved++ }
            }
        }
        if ((FpLabel $snHit) -ne $rtFp) { Die "RegisterAccount refused: 提案里目标账号的钥匙指纹不匹配（切错位置嫌疑）" }
        if ($snMoved -gt 0) { Die ("RegisterAccount refused: 有 " + $snMoved + " 个其它账号的凭证被连带改动（只允许动目标账号）") }
    }
    $reg1 = Get-AuthTokenRegistry $cfg
    foreach ($r in @($reg1)) {
        if ((FpLabel $r.token) -eq $rtFp -and $r.user_id -ne $RegisterAccount) {
            Die ("RegisterAccount refused: 这把钥匙已属于账号 " + $r.user_id + "（一把钥匙只能一个主人）")
        }
    }
    # 组装新的 account_tokens：保留既有条目（含别的账号），本账号存在则替换、不存在则追加
    $kept = @($reg1 | Where-Object { $_.user_id -and $_.user_id -ne $RegisterAccount })
    $arr = @()
    foreach ($k in $kept) { $arr += ('{"user_id":"' + $k.user_id + '","token":"' + $k.token + '"}') }
    $arr += ('{"user_id":"' + $RegisterAccount + '","token":"' + $rtok + '"}')
    $newArray = '"account_tokens": [' + ($arr -join ",") + ']'
    $xtRaw = Get-Content -Path $ConfigFile -Raw -Encoding UTF8
    if ($xtRaw -match '"account_tokens"\s*:\s*\[[^\]]*\]') {
        $xtNew = [regex]::Replace($xtRaw, '"account_tokens"\s*:\s*\[[^\]]*\]', { param($m) $newArray })
    } else {
        # 老库形态（没有这个键）：插到 "report_token" 之后，逗号由替换文本自带。
        # 注意必须用**实例方法**的计数重载：[regex]::Replace(input, pattern, evaluator, 1) 的第 4 个
        # 参数绑的是 RegexOptions（1=IgnoreCase）而不是"只换一次"⇒ 会在每一处匹配都插一遍，
        # 造出两个同名 account_tokens 键；JSON 解析器静默取最后一个，自证还照样过——假成功。
        $reIns = [regex]'("(?:report_token|token)"\s*:\s*"[^"]*")'
        $xtNew = $reIns.Replace($xtRaw, { param($m) $m.Groups[1].Value + ", " + $newArray }, 1)
    }
    $xtChk = $null
    try { $xtChk = $xtNew | ConvertFrom-Json } catch { Die ("RegisterAccount refused: 提案文本解析不过（手术切坏形态，拒写）：" + $_.Exception.Message) }
    # 三重自证：①新键里本账号命中这把钥匙；②既有条目一条都没丢；③除 account_tokens 外其它字段原样
    $hit = @($xtChk.account_tokens | Where-Object { [string]$_.user_id -eq $RegisterAccount })
    if (@($hit).Count -ne 1) { Die "RegisterAccount refused: 提案里本账号出现 " + @($hit).Count + " 次（应为恰 1）" }
    if ((FpLabel([string]$hit[0].token)) -ne $rtFp) { Die "RegisterAccount refused: 提案里本账号的钥匙指纹不匹配（切错位置嫌疑）" }
    foreach ($k in $kept) {
        if (-not (@($xtChk.account_tokens | Where-Object { [string]$_.user_id -eq $k.user_id -and (FpLabel([string]$_.token)) -eq (FpLabel $k.token) }).Count -eq 1)) {
            Die ("RegisterAccount refused: 既有账号 " + $k.user_id + " 的条目在提案里丢了或变了")
        }
    }
    if (@($xtChk.account_tokens).Count -ne ($kept.Count + 1)) { Die "RegisterAccount refused: 提案条目数与预测不符" }
    foreach ($f in @('token', 'report_token', 'broker', 'account', 'report_url', 'db')) {
        if ([string]$xtChk.$f -ne [string]$cfg.$f) { Die ("RegisterAccount refused: 字段 " + $f + " 在提案里变了——手术刀不干净，拒写") }
    }
    if (-not $Apply) {
        Ok ("register plan uid=" + $RegisterAccount + " token_fp=" + $rtFp + " entries=" + ($kept.Count + 1) + " (未写，加 -Apply 落地)")
        exit 0
    }
    $stamp2 = Get-Date -Format "yyyyMMddHHmmss"
    $xtBak = $ConfigFile + ".pre-register-" + $stamp2
    Copy-Item -LiteralPath $ConfigFile -Destination $xtBak -Force
    $xtTmp = $ConfigFile + ".registering-" + $stamp2
    [System.IO.File]::WriteAllText($xtTmp, $xtNew, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $xtTmp -Destination $ConfigFile -Force
    $xtNow = $null
    try { $xtNow = Get-Content -Path $ConfigFile -Raw -Encoding UTF8 | ConvertFrom-Json } catch { Die ("RegisterAccount read-back parse failed（回滚副本: " + $xtBak + "）") }
    $rb = @($xtNow.account_tokens | Where-Object { [string]$_.user_id -eq $RegisterAccount })
    if (@($rb).Count -ne 1 -or (FpLabel([string]$rb[0].token)) -ne $rtFp) { Die ("RegisterAccount read-back mismatch（回滚副本: " + $xtBak + "）") }
    Ok ("register done uid=" + $RegisterAccount + " token_fp=" + $rtFp + " entries=" + @($xtNow.account_tokens).Count + " backup=" + $xtBak)
    if (-not $WriteSnapshot) {
        Info "生效前提：网关重启后载入注册表；引擎侧那把同名钥匙必须由设置页或 POST /api/admin/users/{id}/config/qmt 落进该账号快照"
        exit 0
    }
    # —— 签发另一半：把前置算好并通过自证的提案落到 auth.json（这一段不再做任何判断）——
    $bakS = $authPath + ".pre-issue-" + $stamp2
    Copy-Item -LiteralPath $authPath -Destination $bakS -Force
    $tmpS = $authPath + ".issuing-" + $stamp2
    [System.IO.File]::WriteAllText($tmpS, $snProp.text, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $tmpS -Destination $authPath -Force
    $backS = $null
    try { $backS = Get-Content -LiteralPath $authPath -Raw -Encoding UTF8 | ConvertFrom-Json } catch {
        Die ("Issue read-back parse failed（auth 回滚副本: " + $bakS + "；网关侧回滚副本: " + $xtBak + "）")
    }
    $rbTok = ""
    foreach ($e in @($backS.configs)) {
        if ([string]$e.user_id -ne $RegisterAccount -or [string]$e.key -ne 'quant_config_json_v1') { continue }
        $bv = $null
        try { $bv = "$($e.value)" | ConvertFrom-Json } catch { Die ("Issue read-back value parse failed（auth 回滚副本: " + $bakS + "）") }
        $rbTok = [string]$bv.qmt.token
    }
    if ((FpLabel $rbTok) -ne $rtFp) { Die ("Issue read-back mismatch：快照里的钥匙与网关注册表不同源（auth 回滚副本: " + $bakS + "）") }
    Ok ("issue done uid=" + $RegisterAccount + " token_fp=" + $rtFp + " 两侧同源自证通过 backup_gw=" + $xtBak + " backup_auth=" + $bakS)
    Info "生效前提：重启 qmt_gateway（载入注册表）+ 重启 quant（auth store 只在启动时载入）；跑完两侧再拨部署探针第 20 看 snapshot_holders 是否 =1"
    exit 0
}

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
Info ("src3 engine_cfg    rules.qmt.token=" + (FpLabel $engToken) + " (权威写入方＝设置页；-Align 模式本脚本可按源1补齐，热加载 5s 吃到)")

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

# ── 源5 读侧（§0926ROT-SRC5，2026-09-26 发版实录补腿）：引擎账号级快照 ──────────────────
# auth.json 的 configs[] 里 key=quant_config_json_v1 的 value 是整棵 Rules 的 JSON 字符串，
# 其中 .qmt.token 才是引擎对该账号真实下单用的口令（GetQMTConfigFor：账号覆盖→运营覆盖→全局，
# internal/config/config.go:1962）。09-26 实录：轮换+src3 对齐后 verify 第 20 探针仍红
# engineAuth=旧fp——快照这一腿不在轮换/对齐覆盖面里 ⇒ 周一开盘链路会拿旧口令敲新网关（401）。
# 读法与 verify ③b 腿同型：逐条解析 value，只收非空 token；明文只进本地数组供写侧替换，打印一律指纹。
$authPath = Join-Path $DataDir "auth.json"
$authSnapVals = @()   # 各快照 token 原值（函数栈内；绝不回显）
$authParseErr = $false
if (Test-Path $authPath) {
    $authObj = $null
    try { $authObj = Get-Content -Path $authPath -Raw -Encoding UTF8 | ConvertFrom-Json } catch { $authObj = $null }
    if ($null -eq $authObj) {
        $authParseErr = $true   # 读侧如实标失明；写侧遇此形态直接拒判（结构不可信一个字都不写）
        Info "src5 auth_snap     parse-error (auth.json 解析失败——读侧失明已标注，-Align 将拒写)"
    } else {
        foreach ($ac in @($authObj.configs)) {
            if ($null -eq $ac) { continue }
            if ([string]$ac.key -ne 'quant_config_json_v1') { continue }
            if (-not ("$($ac.value)").Trim()) { continue }
            $ar = $null
            try { $ar = "$($ac.value)" | ConvertFrom-Json } catch { $ar = $null }
            if ($null -eq $ar) { continue }
            $at = [string]$ar.qmt.token
            if ($at) { $authSnapVals += $at }
        }
        $authFps = @($authSnapVals | ForEach-Object { FpLabel $_ } | Sort-Object -Unique)
        Info ("src5 auth_snap     n=" + @($authSnapVals).Count + " tokens=" + $(if ($authFps.Count) { $authFps -join "," } else { "none" }) + " (引擎按账号取令牌的权威源；-Align 按源1补齐，改后需重启 quant 服务方吃到——auth store 启动时载入)")
    }
} else { Info "src5 auth_snap     no-file (auth.json 不在位——读侧如实标注，-Align 不判红)" }

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
if ($RevokeAccount) {
    # §TOKEN-PERAACCT 撤销通道：把某账号快照里的实盘凭证清掉——**只动它自己那一条**。
    # 为什么必须做：一把钥匙扩散到多个账号时，网关与首尔都无法判断"这笔是谁的"（现网 admin 与
    # test01 快照里是同一把 ⇒ 回报恒归 users 顺序第一个，test01 自己那行永远停在撤单中）。
    # 写形：auth.json 的 configs[].value 是**字符串化 JSON**（内层引号带反斜杠），
    # 因此按"该条目窗口内的转义 token 字段"原文替换；命中数不是恰好 1 就拒写（不猜）。
    if ($RevokeAccount -notmatch '^[A-Za-z0-9_.:@-]{1,64}$') { Die "RevokeAccount refused: user_id 形态非法" }
    if (-not (Test-Path $authPath)) { Die ("RevokeAccount refused: auth.json 不在位 " + $authPath) }
    $aRaw = Get-Content -LiteralPath $authPath -Raw -Encoding UTF8
    $aObj0 = $null
    try { $aObj0 = $aRaw | ConvertFrom-Json } catch { Die ("RevokeAccount refused: auth.json 解析失败（" + $_.Exception.Message + "）——结构不可信，一个字节都不写") }
    # 只清这把**在册实盘凭证**：别的字段（llm 的 api_key、tushare token 等）不在本通道职责内
    $liveTok = [string]$cfg.token
    if (-not $liveTok) { Die "RevokeAccount refused: 网关现钥匙读不到（src1 失明），无法判定要清哪把" }
    $wR = $null
    try { $wR = Get-SnapshotTokenWindow -Raw $aRaw -Uid $RevokeAccount } catch { Die ("RevokeAccount refused: " + $_.Exception.Message + "（停手）") }
    $win = $wR.win
    $scopeAll = $false
    $oldTok = $liveTok
    if (([regex]::Matches($win, [regex]::Escape(('\"token\":\"' + $liveTok + '\"')))).Count -eq 0) {
        # 该账号持有的不是网关现钥匙（连的是别的网关）：只有显式 -RevokeAnyToken 才允许清空
        $reAny = [regex]::Matches($win, '\\"token\\":\\"([^\\]{1,200})\\"')
        if (@($reAny).Count -ne 1) { Die ("RevokeAccount refused: 条目内 token 命中数=" + @($reAny).Count + "（不是恰好 1，不猜该清哪把）") }
        if (-not $RevokeAnyToken) { Die "RevokeAccount refused: 该账号的钥匙不是网关现钥匙（可能是别的网关）；确要清空请加 -RevokeAnyToken" }
        # 取捕获组，不用字符串剥前缀：剥前缀要在"PS 字面量 / 正则 / JSON 转义"三层里数反斜杠，
        # 数错一层就拿着带尾巴的值去替换 ⇒ 命中 0 次（22:43 副本实测就是这个形态）。
        $oldTok = $reAny[0].Groups[1].Value
        $scopeAll = $true
    }
    $prop = $aRaw
    $delta = 0
    try {
        $sR = Set-SnapshotTokenRaw -Raw $aRaw -Uid $RevokeAccount -OldTok $oldTok -NewTok ''
        $prop = $sR.text; $delta = $sR.delta
    } catch { Die ("RevokeAccount refused: " + $_.Exception.Message) }
    $pObj = $null
    try { $pObj = $prop | ConvertFrom-Json } catch { Die ("RevokeAccount refused: 提案文本解析不过（手术切坏形态，拒写）：" + $_.Exception.Message) }
    if (($prop.Length - $aRaw.Length) -ne $delta) { Die "RevokeAccount refused: 提案字节差与预测不符（存在计划外命中）" }
    # 逐条自证：目标条目清空、**其它条目一把钥匙都没动**、users 与 configs 条数不变
    $chkN = 0; $rbN = 0
    foreach ($e in @($aObj0.configs)) { if ([string]$e.key -eq 'quant_config_json_v1') { $chkN++ } }
    foreach ($e in @($pObj.configs)) { if ([string]$e.key -eq 'quant_config_json_v1') { $rbN++ } }
    if ($rbN -ne $chkN) { Die ("RevokeAccount refused: 快照条目数变了（" + $rbN + " != " + $chkN + "）") }
    if (@($pObj.users).Count -ne @($aObj0.users).Count) { Die "RevokeAccount refused: users 条数变了（手术刀切到账号表，拒写）" }
    foreach ($e in @($pObj.configs)) {
        if ($null -eq $e -or [string]$e.key -ne 'quant_config_json_v1') { continue }
        $vv = $null
        try { $vv = "$($e.value)" | ConvertFrom-Json } catch { Die ("RevokeAccount refused: 提案里 " + [string]$e.user_id + " 的 value 解析不过") }
        $tkNow = [string]$vv.qmt.token
        if ([string]$e.user_id -eq $RevokeAccount) {
            if ($tkNow) { Die ("RevokeAccount refused: 提案里目标账号 token 仍未清空（fp=" + (FpLabel $tkNow) + "）") }
        } else {
            $orig = $null
            foreach ($o in @($aObj0.configs)) { if ([string]$o.user_id -eq [string]$e.user_id -and [string]$o.key -eq 'quant_config_json_v1') { $orig = $o } }
            if ($null -ne $orig) {
                $ov = $null
                try { $ov = "$($orig.value)" | ConvertFrom-Json } catch { $ov = $null }
                if ((FpLabel([string]$ov.qmt.token)) -ne (FpLabel $tkNow)) {
                    Die ("RevokeAccount refused: 账号 " + [string]$e.user_id + " 的凭证被连带改动（只允许动目标账号）")
                }
            }
        }
    }
    Info ("revoke plan uid=" + $RevokeAccount + " target_fp=" + (FpLabel $liveTok) +
          " scope=" + $(if ($scopeAll) { "any-token(显式)" } else { "live-credential" }) + " entries=" + $rbN)
    if (-not $Apply) { Ok "revoke plan-only（未写；加 -Apply 落地）"; exit 0 }
    $stampR = Get-Date -Format "yyyyMMddHHmmss"
    $bakR = $authPath + ".pre-revoke-" + $stampR
    Copy-Item -LiteralPath $authPath -Destination $bakR -Force
    $tmpR = $authPath + ".revoking-" + $stampR
    [System.IO.File]::WriteAllText($tmpR, $prop, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $tmpR -Destination $authPath -Force
    $backR = $null
    try { $backR = Get-Content -LiteralPath $authPath -Raw -Encoding UTF8 | ConvertFrom-Json } catch { Die ("RevokeAccount read-back parse failed（回滚副本: " + $bakR + "）") }
    foreach ($e in @($backR.configs)) {
        if ([string]$e.user_id -ne $RevokeAccount -or [string]$e.key -ne 'quant_config_json_v1') { continue }
        $bv = $null
        try { $bv = "$($e.value)" | ConvertFrom-Json } catch { Die ("RevokeAccount read-back value parse failed（回滚副本: " + $bakR + "）") }
        if ((FpLabel([string]$bv.qmt.token)) -ne (FpLabel "")) { Die ("RevokeAccount read-back mismatch（回滚副本: " + $bakR + "）") }
    }
    Ok ("revoke done uid=" + $RevokeAccount + " backup=" + $bakR + "（auth store 启动时载入：必须重启 quant 服务方吃到）")
    exit 0
}
if ($ClearGlobal) {
    # §TOKEN-PERAACCT N1：引擎全局 rules.qmt.token 不是实盘凭证——它等于网关现钥匙时，
    # 任何**没有自己快照**的账号（成员号）都会继承这把钥匙并回 401 之外真正下单。
    # 这里把它清空；写形与源3 对齐同款（锚点窗口外科替换 + 提案自证 + 备份 + 复读）。
    if (-not (Test-Path $engPath)) { Die ("ClearGlobal refused: engine config 不在位 " + $engPath) }
    $gRaw = Get-Content -LiteralPath $engPath -Raw -Encoding UTF8
    $gObj = $null
    try { $gObj = $gRaw | ConvertFrom-Json } catch { Die ("ClearGlobal refused: config.json 解析失败（" + $_.Exception.Message + "）——结构不可信，一个字都不写") }
    if (-not $gObj.rules -or -not $gObj.rules.qmt) { Die "ClearGlobal refused: rules.qmt 节点不在（不猜结构）" }
    $gWas = [string]$gObj.rules.qmt.token
    if (-not $gWas) { Ok "clear global done action=same (全局 rules.qmt.token 本就是空)"; exit 0 }
    if ((FpLabel $gWas) -ne (FpLabel([string]$cfg.token))) {
        Info ("clear global note 全局现值 fp=" + (FpLabel $gWas) + " 并不等于网关现钥匙 fp=" + (FpLabel([string]$cfg.token)) + "（本就不是在册凭证，仍按要求清空）")
    }
    $gNew = $null
    try { $gNew = Set-JsonStringFieldRaw -Text $gRaw -Anchor '"qmt"' -Field 'token' -Value '' -Check { param($o) ([string]$o.rules.qmt.token) -eq '' } } catch { Die ("ClearGlobal refused: " + $_.Exception.Message) }
    $gChk = $null
    try { $gChk = $gNew | ConvertFrom-Json } catch { Die "ClearGlobal refused: 提案文本解析不过（手术切坏形态，拒写）" }
    foreach ($f in @('gateway_url', 'price_type', 'mode')) {
        if ([string]$gChk.rules.qmt.$f -ne [string]$gObj.rules.qmt.$f) { Die ("ClearGlobal refused: 字段 " + $f + " 在提案里变了——手术刀不干净，拒写") }
    }
    Info ("clear global plan was=" + (FpLabel $gWas) + " to=empty")
    if (-not $Apply) { Ok "clear global plan-only（未写；加 -Apply 落地）"; exit 0 }
    $stampG = Get-Date -Format "yyyyMMddHHmmss"
    $bakG = $engPath + ".pre-clear-" + $stampG
    Copy-Item -LiteralPath $engPath -Destination $bakG -Force
    $tmpG = $engPath + ".clearing-" + $stampG
    [System.IO.File]::WriteAllText($tmpG, $gNew, (New-Object System.Text.UTF8Encoding($false)))
    Move-Item -LiteralPath $tmpG -Destination $engPath -Force
    $backG = $null
    try { $backG = Get-Content -LiteralPath $engPath -Raw -Encoding UTF8 | ConvertFrom-Json } catch { Die ("ClearGlobal read-back parse failed（回滚副本: " + $bakG + "）") }
    if ([string]$backG.rules.qmt.token) { Die ("ClearGlobal read-back mismatch：全局 token 仍在（回滚副本: " + $bakG + "）") }
    Ok ("clear global done action=written backup=" + $bakG + "（必须重启 quant：内存里的全局规则仍是旧值，5s 热加载不会重读磁盘）")
    exit 0
}
if ($Align) {
    # §TOKEN-PERAACCT 默认语义变更（2026-10-30）：**一把钥匙不再铺给所有账号**。
    # 旧 -Align 把源1复制到源3（引擎全局 rules.qmt.token）与源5（每个账号快照），
    # 现网七个账号共用同一把实盘凭证、回报只能按 users 顺序猜归属，正是这条通道造成的。
    # 现在缺省只对齐源4（桥与网关物理同机同钥匙，必须一致），源3/源5 改为"报告 + 指向签发通道"；
    # 确需恢复旧行为（例如回滚）才显式加 -LegacySpread。
    if (-not $LegacySpread) {
        Info ("align src1 truth fp=" + (FpLabel([string]$cfg.token)) + " （本轮只对齐源4，源3/源5 不再复制）")
        $spreadHolders = @()
        foreach ($hc in @($authSnapVals)) { if ((FpLabel $hc) -eq (FpLabel([string]$cfg.token))) { $spreadHolders += "match" } }
        Info ("align src3 plan engine_cfg will_write=False action=SKIPPED was=" + (FpLabel $engToken) +
              "（N1：全局兜底位不该持有实盘凭证；若它等于源1，部署第 20 探针会判红）")
        Info ("align src5 plan auth_snapshots action=SKIPPED holders=" + @($spreadHolders).Count +
              "（N2：在册凭证的快照持有者至多 1；逐账号签发走 -RegisterAccount，撤销走 -RevokeAccount）")
        $bridgeOnly = Join-Path $GatewayDir "config.bridge.json"
        # 锚点与 -LegacySpread 路径同名（align src4 plan bridge action=…）：驱动侧的计划完整性
        # 判据不必分叉（scripts/rotate_qmt_token_guangzhou.sh 认的就是这一串）。
        $brActionOnly = "absent"
        if (Test-Path $bridgeOnly) { $brActionOnly = if ((FpLabel $brToken) -ne (FpLabel([string]$cfg.token))) { "write" } else { "same" } }
        Info ("align src4 plan bridge action=" + $brActionOnly + " (absent=桥未落位，落位时写源1同值即可)")
        if (Test-Path $bridgeOnly) {
            if ((FpLabel $brToken) -ne (FpLabel([string]$cfg.token))) { Warn "align src4 bridge fp != src1（桥必须与网关同钥匙，见下方写侧）" }
            else { Ok "align src4 bridge same-as-src1" }
        } else { Info "align src4 bridge absent (桥未落位，落位时写源1 同值)" }
        if (-not $Apply) { Info "ALIGN_DRY plan-only（默认不写源3/源5）" }
        else { Ok "align done（默认形态：只检视+对齐源4，源3/源5 未写；确需旧扩散请显式加 -LegacySpread）" }
        exit 0
    }
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
    # 源5 计划（§0926ROT-SRC5）：快照里任何与源1不同指纹的 token 都是"待刷"值；
    # 文件在位却解析失败＝结构不可信，计划阶段直接拒判（一个字都不写），不许带病落地。
    $authStaleVals = @()
    foreach ($av in ($authSnapVals | Sort-Object -Unique)) { if ((FpLabel $av) -ne $alignFp) { $authStaleVals += $av } }
    $authAction5 = "same"
    if ($authParseErr) { $authAction5 = "parse-error" }
    elseif (@($authSnapVals).Count -eq 0) { $authAction5 = "absent" }
    elseif ($authStaleVals.Count -gt 0) { $authAction5 = "write" }
    Info ("align src5 plan auth_snapshots action=" + $authAction5 + " n=" + @($authSnapVals).Count + " stale_kinds=" + @($authStaleVals).Count + " target_fp=" + $alignFp)
    if ($authAction5 -eq "parse-error") { Die "align src5 refused: auth.json 解析失败——结构不可信，一个字节都不写（读侧失明已标注）" }
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
    # ── 源5：auth.json 账号快照（§0926ROT-SRC5 写侧，2026-09-26 发版实录补腿）───────────
    # 写形选择与源3 同 posture 但更简单：快照 value 是字符串化 JSON（内层引号全部转义），
    # PS 反序列化重排会把整文件字段格式洗掉 ⇒ 一律**原文精确替换旧值**：token 是高熵字面量，
    # 整文本 Replace 无误撞面；替换后仍三重自证——①提案可解析；②提案里带 token 的快照条数
    # 不变且每条指纹命中源1；③字节差=计划预测量（出现计划外命中即拒写）。全过才落
    # .pre-align 副本+原子写+复读自证。生效前提：auth store 启动时载入内存 ⇒ 必须重启 quant。
    switch ($authAction5) {
        "absent" { Ok ("align src5 done token_fp=" + $alignFp + " action=absent (auth.json 不在位或无带 token 快照)") }
        "same"   { Ok ("align src5 done token_fp=" + $alignFp + " action=same (全部快照已与源1同值)") }
        "write"  {
            if (-not (Test-Path $authPath)) { Die ("align src5 refused: auth.json not found at " + $authPath + "（快照计数>0 而文件消失＝竞态形态，停手）") }
            $authRaw5 = Get-Content -Path $authPath -Raw -Encoding UTF8
            $authProp = $authRaw5
            $authDeltaPlan = 0
            foreach ($ov in ($authStaleVals | Sort-Object -Unique)) {
                $oc = ([regex]::Matches($authRaw5, [regex]::Escape($ov))).Count
                if ($oc -lt 1) { Die "align src5 refused: 旧值在原文中不出现（读写链不一致，拒写）" }
                $authDeltaPlan += $oc * ($alignTok.Length - $ov.Length)
                $authProp = $authProp.Replace($ov, $alignTok)
            }
            $authChkObj = $null
            try { $authChkObj = $authProp | ConvertFrom-Json } catch { Die "align src5 refused: 提案文本解析不过（手术切坏形态，拒写）" }
            if (($authProp.Length - $authRaw5.Length) -ne $authDeltaPlan) { Die "align src5 refused: 提案字节差与预测量不符（存在计划外命中，停手）" }
            $chkN5 = 0
            foreach ($cc in @($authChkObj.configs)) {
                if ($null -eq $cc) { continue }
                if ([string]$cc.key -ne 'quant_config_json_v1') { continue }
                if (-not ("$($cc.value)").Trim()) { continue }
                $cv = $null
                try { $cv = "$($cc.value)" | ConvertFrom-Json } catch { $cv = $null }
                if ($null -eq $cv) { continue }
                $ct = [string]$cv.qmt.token
                if ($ct) {
                    $chkN5++
                    if ((FpLabel $ct) -ne $alignFp) { Die ("align src5 refused: 账号 " + [string]$cc.user_id + " 快照 token 在提案中仍未命中源1（手术刀不干净，拒写）") }
                }
            }
            if ($chkN5 -ne @($authSnapVals).Count) { Die ("align src5 refused: 提案中 token 快照条数变了（" + $chkN5 + " != " + @($authSnapVals).Count + "）") }
            $authBak5 = $authPath + ".pre-align-" + $stamp
            Copy-Item -LiteralPath $authPath -Destination $authBak5 -Force
            $authTmp5 = $authPath + ".aligning-" + $stamp
            [System.IO.File]::WriteAllText($authTmp5, $authProp, (New-Object System.Text.UTF8Encoding($false)))
            Move-Item -LiteralPath $authTmp5 -Destination $authPath -Force
            $authNow5 = $null
            try { $authNow5 = Get-Content -Path $authPath -Raw -Encoding UTF8 | ConvertFrom-Json } catch { Die ("align src5 read-back parse failed（回滚副本: " + $authBak5 + "）") }
            $rbN5 = 0
            foreach ($rc in @($authNow5.configs)) {
                if ($null -eq $rc) { continue }
                if ([string]$rc.key -ne 'quant_config_json_v1') { continue }
                if (-not ("$($rc.value)").Trim()) { continue }
                $rv = $null
                try { $rv = "$($rc.value)" | ConvertFrom-Json } catch { $rv = $null }
                if ($null -eq $rv) { continue }
                $rt = [string]$rv.qmt.token
                if ($rt) {
                    $rbN5++
                    if ((FpLabel $rt) -ne $alignFp) { Die ("align src5 read-back mismatch（回滚副本: " + $authBak5 + "）") }
                }
            }
            if ($rbN5 -ne @($authSnapVals).Count) { Die ("align src5 read-back count mismatch（回滚副本: " + $authBak5 + "）") }
            Ok ("align src5 done token_fp=" + $alignFp + " action=written read-back confirmed, backup=.pre-align-" + $stamp + " (提醒：auth store 启动时载入，重启 quant 服务后方吃到新快照)")
        }
        default { Die ("align src5 refused: unknown action=" + $authAction5) }
    }
    Info "ALIGN_APPLIED machine-readable sources unified to src1 truth. Reminder: gateway restart only AFTER this returns 5-source agreement; final check = verify deploy probe 20 token_fp_agree; src5 written => restart quant service (auth store loads at startup)."
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
