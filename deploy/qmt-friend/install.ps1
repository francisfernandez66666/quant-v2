# install.ps1 —— 朋友机器执行腿的一键安装编排（§FRIEND-PKG，2026-11-03 批 6）
#
# 职责：把"懂行才能串起来跑"的七件事固化成一次双击：清单校验 → 环境核 → 落码 → 桥落位 →
#       overlay → 装填（钥匙两处登记）→ 计划任务 → 起一次并自证。判据一律**不在本文件里另造**：
#       出厂档判定真源是 gateway.py 的 validate_friend_profile（--profile-check），
#       装填与双向自证真源是 enroll.ps1（它会自己调 ensure_friend_gateway_config.ps1），
#       关闭门真源是 close_gate.py。这里只做"编排 + 逐步判 rc + 失败即停"。
#
# 为什么缺省是预览而不是直接装（对齐本仓一贯的"缺省不动手"纪律，见 rotate_qmt_token.ps1 -Align、
# install_tasks.ps1 -Apply）：命令行直调本脚本 = 看计划；真正动手只由 install_entry.bat 传 -Apply。
# 这样门禁/排障时能在任意一台机器上跑它而不会改动别人家（记忆 [[switch-gated-rollout]] 同取向）。
#
# 三条不许松的边界：
#   1) **任何一步不过就 ABORT，且已写的东西留现场不假装没动**：日志里带 step 与 reason，
#      第 4 步落码前会把旧 qmt_gateway 整目录改名成 .pre-pkginstall-<ts>，回滚靠它，不靠"再装一次"。
#   2) **绝不让任何凭据进命令行参数、进日志、进包**：口令与钥匙一律 Read-Host -AsSecureString，
#      只在内存里解一次、经 stdin 交给下游脚本（enroll.ps1 自己那套），日志只打 Mask 后的形态。
#      券商账号密码更不出本机（B-N2）——本脚本从不问它。
#   3) **不覆盖已在的 config.xt.json 与 data.db**（§F18：账本是证据链；N2：一把钥匙只属于一个账号，
#      重装修改配置必须走 enroll，不能被"再解压一次"顺手替换）。
#
# 判读锚点（纯 ASCII；中文经管道会被码页换成 ? ⇒ 上层与门禁一律按这些行判）：
#   STEP1_MANIFEST files=NN mismatches=0 contract_fp=XXXX
#   STEP2_ENV      ps=5.x python=C:\...\python.exe pyver=3.12.x qmt_root=...
#   STEP4_LAYOUT   copied=NN mirrored=NN identity_files_in_payload=0 backup=...
#   STEP5_BRIDGE   placed=... manual=qmt_strategy_attach
#   STEP6_OVERLAY  wg=absent|present|installed tunnel=missing|present|registered
#   STEP7_ENROLL   ENROLL_RESULT=OK
#   STEP8_TASKS    tasks=4
#   STEP9_VERIFY   health=ok profile_check=PASS
#   LEG_INSTALL=PASS commit=... contract_fp=... tasks=4 report_ok=1
#   LEG_INSTALL=ABORT step=N reason=...
#
# 用法：
#   .\install.ps1                                     # 预览：只打印将做什么
#   .\install.ps1 -Apply -OverlayIP 10.77.0.2         # 动手（install_entry.bat 就是这么调的）

param(
    [string]$Root      = "C:\qmt\quant-trading-v2",
    [string]$PythonExe = "",
    [string]$Account    = "",
    [string]$Username   = "",
    [string]$OverlayIP  = "",
    [string]$DecisionIP = "10.77.0.1",
    [string]$BaseUrl    = "https://quant-trading.top",
    [string]$QmtRoot    = "",
    [switch]$Apply
)

$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
# 包结构是 <pkg>/MANIFEST.json + <pkg>/qmt_gateway/** + <pkg>/tools/**（本脚本在 tools 里），
# 所以"包根"是 here 的**父**目录。写反的形态是清单永远找不到、第 1 步直接 ABORT。
$payload = Split-Path -Parent $here
$logLines = New-Object System.Collections.Generic.List[string]

function Log($line) {
    $logLines.Add($line) | Out-Null
    Write-Host $line
}
function Abort($step, $reason) {
    Log ("LEG_INSTALL=ABORT step=" + $step + " reason=" + $reason)
    # 收尾日志写失败**绝不许盖过 ABORT 本身**：$env:TEMP 在个别主机上是空（本机 pwsh 真撞到），
    # 缺 fallback 时形态是"打了 ABORT 行、随即抛一条绑定错误、退出码变成 1"，值班会去追那条错而不是修原因。
    $tmpDir = $env:TEMP
    if (-not $tmpDir) { $tmpDir = [System.IO.Path]::GetTempPath() }
    try {
        $tail = Join-Path $tmpDir ("qmt-leg-install-" + (Get-Date -Format "yyyyMMddHHmmss") + ".log")
        Set-Content -Path $tail -Value $logLines -Encoding UTF8
        Log ("LOG=" + $tail)
    } catch {
        Log ("LOG_WRITE_FAILED " + $_.Exception.Message.Replace("`r", " ").Replace("`n", " "))
    }
    exit 10
}
function SecToPlain($sec) {
    # 只在这一处把 SecureString 解成明文，且不落任何变量名含 token 的日志；
    # 走 Marshal 这条路是因为 $sec | ConvertTo-SecureString -AsPlainText 那条在 5.1 上不存在。
    $ptr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
    try { [System.Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
    finally { [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
}

Log ("APPLY=" + $(if ($Apply) { 1 } else { 0 }) + " root=" + $Root)

# —— STEP 1：清单校验（逐文件 SHA256 + 契约指纹用网关那把尺子复算）——
$mj = Join-Path $payload "MANIFEST.json"
if (-not (Test-Path $mj)) { Abort 1 "manifest_absent" }
$manifest = Get-Content $mj -Raw -Encoding UTF8 | ConvertFrom-Json
$mismatch = 0
$checked = 0
foreach ($e in $manifest.files) {
    if ($e.path -eq "./MANIFEST.json" -or $e.path -eq "MANIFEST.json") { continue }
    $p = Join-Path $payload ($e.path -replace '^\./', '' -replace '/', '\')
    if (-not (Test-Path $p)) { $mismatch = $mismatch + 1; Log ("MANIFEST_MISSING " + $e.path); continue }
    $h = (Get-FileHash -Algorithm SHA256 -Path $p).Hash.ToLower()
    $checked = $checked + 1
    if ($h -ne $e.sha256) { $mismatch = $mismatch + 1; Log ("MANIFEST_MISMATCH " + $e.path) }
}
if ($mismatch -gt 0) {
    # 半份包 = 最贵的形态：代码新、契约旧（或反过来），网关自己算出的指纹与广州期待值不符，
    # 表现是"只报不接单"——看着像在跑。宁可装不上。
    Abort 1 ("manifest_mismatch=" + $mismatch)
}
Log ("STEP1_MANIFEST files=" + $checked + " mismatches=0 contract_fp=" + $manifest.contract_fp + " commit=" + $manifest.build_commit)

# —— STEP 2：环境核（Python 只做探测，不代装：CPython 不进包是体积与杀误报的取舍）——
if ($PythonExe -eq "") {
    foreach ($cand in @("C:\Python312\python.exe", "C:\Python311\python.exe")) {
        if (Test-Path $cand) { $PythonExe = $cand; break }
    }
}
if ($PythonExe -eq "") {
    $cmd = Get-Command python -ErrorAction SilentlyContinue
    if ($cmd) { $PythonExe = $cmd.Source }
}
if ($PythonExe -eq "" -or -not (Test-Path $PythonExe)) { Abort 2 "python_absent" }
$pyver = (& $PythonExe -V 2>&1) | Out-String
$pyver = $pyver.Trim()
if ($pyver -notmatch '3\.(1[0-9]|9)') { Abort 2 ("python_too_old:" + $pyver) }
if ($QmtRoot -eq "") {
    $g = @(Get-ChildItem "C:\Program Files (x86)" -Directory -ErrorAction SilentlyContinue | Where-Object { $_.Name -like "*QMT*" })
    if ($g.Count -ge 1) { $QmtRoot = $g[0].FullName }
}
Log ("STEP2_ENV ps=" + $PSVersionTable.PSVersion.ToString() + " python=" + $PythonExe + " pyver=" + $pyver + " qmt_root=" + $(if ($QmtRoot) { $QmtRoot } else { "absent" }))

# —— STEP 3：QMT 客户端在不在（券商的安装器与账号密码**绝不进包**，也绝不代装：B-N2）——
if ($QmtRoot -eq "" -or -not (Test-Path $QmtRoot)) {
    Abort 3 "qmt_client_absent_install_dongguan_qmt_first"
}
Log ("STEP3_QMT root_ok=1")

# —— STEP 4：落码 ——
$gwDst = Join-Path $Root "qmt_gateway"
$srcGw = Join-Path $payload "qmt_gateway"
if (-not (Test-Path $srcGw)) { Abort 4 "payload_gateway_absent" }
$backup = ""
if (-not $Apply) {
    Log ("STEP4_LAYOUT apply=0 would_copy_to=" + $gwDst)
} else {
    New-Item -ItemType Directory -Path $gwDst -Force | Out-Null
    $oldCfg = Join-Path $gwDst "config.xt.json"
    $oldDb = Join-Path $gwDst "data.db"
    if (Test-Path $gwDst) {
        $stamp = Get-Date -Format "yyyyMMddHHmmss"
        $backup = $gwDst + ".pre-pkginstall-" + $stamp
    }
    # 身份与证据文件：**构建侧 deny 清单之外还要在装的一侧断言一次**。
    # 原来这里是"看见就跳过并计数"——而包内永远不该有这些文件（构建侧拒收），所以那个计数恒 0，
    # 是一枚**永远不会咬的假锁**（11-03 用 pwsh 真跑 -Apply 才看穿：preserved_config=0 却自以为在保护）。
    # 现在的形状：包内一旦出现它们＝这份包是绕过 deny 手工拼的 ⇒ 第 4 步直接拒装，不写任何字节。
    $forbidden = @(Get-ChildItem $srcGw -File -Recurse -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -eq 'config.xt.json' -or $_.Name -eq 'data.db' -or $_.Name -eq 'auth.json' `
                       -or $_.Name -like '*.pre-*' -or $_.Name -like '*.key' -or $_.Name -like '*.pem' })
    if ($forbidden.Count -gt 0) {
        Abort 4 ("payload_carries_identity_files=" + (($forbidden | ForEach-Object { $_.Name }) -join ','))
    }
    $copied = 0
    $mirrored = 0
    foreach ($f in @(Get-ChildItem $srcGw -File -Recurse)) {
        $rel = $f.FullName.Substring($srcGw.Length).TrimStart('\')
        $dst = Join-Path $gwDst $rel
        $dir = Split-Path -Parent $dst
        if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
        if (Test-Path $dst) {
            # 只镜像"这次真的会被覆盖的那一个文件"，不整目录改名：整目录改名会把 data.db 与
            # 在跑的网关一起搬走，那才是把自己搞成半态。回滚点按相对路径落在 backup 目录里。
            $bk = Join-Path $backup $rel
            $bd = Split-Path -Parent $bk
            if (-not (Test-Path $bd)) { New-Item -ItemType Directory -Path $bd -Force | Out-Null }
            Copy-Item $dst $bk -Force
            $mirrored = $mirrored + 1
        }
        Copy-Item $f.FullName $dst -Force
        $copied = $copied + 1
    }
    Log ("STEP4_LAYOUT copied=" + $copied + " mirrored=" + $mirrored + " identity_files_in_payload=0 backup=" + $backup)
}

# —— STEP 5：桥落位（QMT 模型沙箱里那份）——
$bridgeSrc = Join-Path $gwDst "qmt_bridge.py"
$stratSrc = Join-Path $gwDst "qmt_bridge_strategy.py"
# 判存在性要分两种脸：动手时找不到＝第 4 步没落成功，必须停；预览时本来就没落码，拿包内源文件判。
if (-not $Apply) {
    # 预览时判的是**包内**那两份，路径要带 qmt_gateway 这一层（写漏就是预览必假红：
    # 11-03 用 pwsh 真跑预览才撞出来，静态读代码看不出来）。
    $bridgeSrc = Join-Path $payload "qmt_gateway\qmt_bridge.py"
    $stratSrc = Join-Path $payload "qmt_gateway\qmt_bridge_strategy.py"
}
if (-not (Test-Path $bridgeSrc)) { Abort 5 "bridge_absent_after_layout" }
$qmtPy = Join-Path $QmtRoot "python"
$sitePy = Join-Path $QmtRoot "bin.x64\lib\site-packages"
$placed = @()
if ($Apply) {
    Copy-Item $bridgeSrc (Join-Path $qmtPy "qmt_bridge.py") -Force
    $placed += "python"
    if (Test-Path $sitePy) { Copy-Item $bridgeSrc (Join-Path $sitePy "qmt_bridge.py") -Force; $placed += "site-packages" }
    # 策略文件名是 QMT 侧的约定（运营机同款），不是我们起的名字。
    Copy-Item $stratSrc (Join-Path $qmtPy "新建策略文件.py") -Force
    $placed += "strategy"
}
Log ("STEP5_BRIDGE placed=" + ($(if ($placed.Count) { $placed -join "," } else { "apply0" })) + " manual=qmt_strategy_attach")

# —— STEP 6：overlay（WG 客户端 + 隧道 conf）——
$wgExe = "C:\Program Files\WireGuard\wireguard.exe"
$wgState = if (Test-Path $wgExe) { "present" } else { "absent" }
$msi = Join-Path $payload "overlay\wireguard-amd64.msi"
if ($wgState -eq "absent" -and (Test-Path $msi)) {
    # 验签在装之前：签名人不是 WireGuard LLC 就拒装并继续报错——包里带的是别人放的包这件事，
    # 只有在这一刻还来得及判。（构建侧只嵌不验，因为 Authenticode 只在 Windows 上判得动。）
    $sig = Get-AuthenticodeSignature -FilePath $msi
    $subj = ""
    if ($sig.SignerCertificate) { $subj = $sig.SignerCertificate.Subject }
    if ($sig.Status -ne "Valid" -or $subj -notlike "*WireGuard LLC*") {
        Abort 6 ("overlay_signature_reject status=" + $sig.Status + " subject=" + $(if ($subj) { $subj } else { "none" }))
    }
    if ($Apply) {
        $p = Start-Process msiexec.exe -ArgumentList @("/i", "`"" + $msi + "`"", "/quiet", "/norestart") -Wait -PassThru
        if ($p.ExitCode -ne 0) { Abort 6 ("msiexec_rc=" + $p.ExitCode) }
        $wgState = if (Test-Path $wgExe) { "installed" } else { "absent" }
    } else { $wgState = "would_install" }
}
$tunDir = "C:\ProgramData\WireGuard\Tunnels"
$tunnel = "missing"
if (Test-Path $tunDir) {
    $confs = @(Get-ChildItem $tunDir -Filter "*.conf" -File -ErrorAction SilentlyContinue)
    if ($confs.Count -ge 1) {
        $tunnel = "present"
        if ($wgState -ne "absent" -and $Apply) {
            $svc = @(Get-Service -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'WireGuardTunnel$*' })
            if ($svc.Count -eq 0) {
                & $wgExe "/install-tunnel" (Join-Path $confs[0].FullName) | Out-Null
                $tunnel = "registered"
            } else { $tunnel = "registered" }
        }
    }
}
Log ("STEP6_OVERLAY wg=" + $wgState + " tunnel=" + $tunnel)

# —— STEP 7：装填（钥匙两处登记 + 出厂档自证 + 双向自证，判据全在 enroll/网关侧）——
if ($OverlayIP -eq "") {
    if ($tunnel -eq "missing") {
        Log "OVERLAY_TUNNEL=missing —— 隧道 conf 由运营发给你，放进 C:\ProgramData\WireGuard\Tunnels\ 后重跑本安装"
        Abort 7 "overlay_ip_unknown_and_tunnel_missing"
    }
    $ipsec = Read-Host -Prompt "this machine overlay IP (e.g. 10.77.0.2)"
    $OverlayIP = ($ipsec -replace "\s", "")
}
if ($OverlayIP -notmatch '^\d+\.\d+\.\d+\.\d+$') { Abort 7 "overlay_ip_not_v4" }
if ($OverlayIP -eq "127.0.0.1" -or $OverlayIP -eq $DecisionIP) {
    # 出厂档里这两条各有一枚拒启动的判据；在这里先拦住，是为了让"半装态"根本发生不了。
    Abort 7 "overlay_ip_is_loopback_or_same_as_decision"
}
$enroll = Join-Path $here "enroll.ps1"
if (-not (Test-Path $enroll)) { Abort 7 "enroll_script_absent" }
$QmtExePath = Join-Path $QmtRoot "bin.x64\XtItClient.exe"
if ($Apply) {
    # 身份两问只在动手时问：预览的整个意义就是"不答问题、不写东西也能看完整计划"
    # （11-03 用 pwsh 跑预览撞出来——把问答放在分支外，预览会停在 identity_fields_empty）。
    if ($Account  -eq "") { $Account  = (Read-Host -Prompt "broker account no (the one this machine trades)") -replace "\s", "" }
    if ($Username -eq "") { $Username = (Read-Host -Prompt "liangzai login username") -replace "\s", "" }
    if ($Account -eq "" -or $Username -eq "") { Abort 7 "identity_fields_empty" }
    # enroll 自己会提示输两次秘密（平台口令 + 网关钥匙，都走 SecureString），这里只把身份参数递过去；
    # 输出先收后打是为了能判 ENROLL_RESULT=OK —— Read-Host 读控制台，不受这段缓冲影响。
    $out = (& $enroll -BaseUrl $BaseUrl -Username $Username -Account $Account -OverlayIP $OverlayIP `
            -DecisionIP $DecisionIP -GatewayDir $gwDst -PythonExe $PythonExe -QmtExe $QmtExePath -Apply 2>&1) | Out-String
    Log ("STEP7_ENROLL begin`n" + $out.TrimEnd() + "`nSTEP7_ENROLL end")
    if ($out -notmatch "ENROLL_RESULT=OK") { Abort 7 "enroll_not_ok" }
} else {
    Log "STEP7_ENROLL apply=0 would_run=enroll.ps1"
}

# —— STEP 8：计划任务（起客户端 / 保网关 / 收盘关闭门）——
$tasks = Join-Path $here "install_tasks.ps1"
if (-not (Test-Path $tasks)) { Abort 8 "install_tasks_absent" }
if ($Apply) {
    $tout = (& $tasks -GatewayDir $gwDst -QmtExe $QmtExePath -Apply 2>&1) | Out-String
    # 锚点是 install_tasks.ps1 真发的那两行：TASKS_RESULT=INSTALLED + TASKS_REREAD_MATCH=1 tasks=4
    # （按记忆写成 TASKS_APPLIED=4 的话，这一步永远假红——脚本本身是对的。）
    if ($tout -notmatch "TASKS_REREAD_MATCH=1") {
        Abort 8 ("tasks_reread_missing:" + ($tout -split "`n" | Select-Object -Last 1))
    }
    Log "STEP8_TASKS tasks=4 reread=1"
} else {
    Log "STEP8_TASKS apply=0 would_run=install_tasks.ps1"
}

# —— STEP 9：起一次 + 自证（/health 与 --profile-check 都归网关自己判）——
if ($Apply) {
    $pc = (& $PythonExe (Join-Path $gwDst "gateway.py") --profile-check 2>&1) | Out-String
    if ($pc -notmatch "PROFILE_CHECK=PASS") { Abort 9 "profile_check_not_pass" }
    Log ("STEP9_VERIFY profile_check=PASS")
    try {
        $h = Invoke-WebRequest -Uri ("http://127.0.0.1:8789/health") -UseBasicParsing -TimeoutSec 10
        Log ("STEP9_VERIFY health=" + $h.StatusCode)
    } catch {
        # 网关还没被任务拉起来不是"装坏了"：任务 5 分钟一轮，这里只报不判死。
        Log "STEP9_VERIFY health=not_yet_up task_will_retry=1"
    }
} else {
    Log "STEP9_VERIFY apply=0"
}

# —— STEP 10：收尾（人话清单，别让人以为双击完就全好了）——
Log ("STEP10_DONE commit=" + $manifest.build_commit + " contract_fp=" + $manifest.contract_fp)
if ($Apply) {
    Log "MANUAL 1) QMT 客户端里新建/挂上模型策略（文件已放 python\新建策略文件.py），交易日 08:40 由任务拉起"
    Log "MANUAL 2) 收盘 15:30 的关闭门会自己判能不能关 QMT；判不过会留 CLOSE_GATE_HELD 原因，别手工硬关"
    Log "MANUAL 3) 换机/重装/卸载前先跑 export_ledger.bat 导出当日账本（§B-N9/N11）"
    Log ("LEG_INSTALL=PASS commit=" + $manifest.build_commit + " contract_fp=" + $manifest.contract_fp + " overlay=" + $wgState + " tunnel=" + $tunnel)
    exit 0
}
Log "LEG_INSTALL=DRYRUN"
exit 0
