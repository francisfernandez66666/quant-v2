# decommission_qmt_mock.ps1 — §QMT-MOCK-DECOM（2026-09-23）：退役实盘机上残留的 UAT qmt-mock。
# 背景（docs/PROGRESS.md 遗留项 + AUDIT_E2E_FULL_20260922PM）：C:\qmt\uat\qmt-mock.exe 自 09-10 起
#   常驻监听 0.0.0.0:8799（对外口）。本脚本退役的是**这个残留部署**，不是 cmd/qmt-mock 源码——
#   源码仍是测试基建（默认 -listen :8789 + scripts/uat_bootstrap.sh 用 :18789），仓库代码/配置
#   已 grep 核实对 8799 零引用。
# 语义：幂等 + 只认 C:\qmt\uat。四道硬规矩：
#   ① **可逆**：exe 只改名 <name>.disabled-<yyyyMMddHHmm>，绝不删除；
#   ② 凡可执行路径不在 $UatRoot 之下的进程一律 action=skip，不杀不rename（哪怕命令行里带 8799
#      ——真网关 :8789 与别的端口占用者都可能被文本误伤）；
#   ③ 每步判定打一行 `action=stop|rename|skip|plan target=<path>`，全 ASCII、不含任何密钥材料
#      （PS→SSH→bash 回传按 GBK 解码，中文明细会变乱码且不可 grep，教训见 verify 探针注释）；
#   ④ **缺省只预览**（§OPS-ALIGN 2026-09-23，owner 裁决）：不带 -Apply 一律 dry-run。
#      为什么缺省必须是预览而不是动手：这是一个会**改名生产机上的可执行文件**的脚本，幂等安全阀的
#      意义就在于——任何"顺手跑一下/复制粘贴了一条命令"的调用都不该在未被人显式确认的情况下改动
#      现网文件。同批的 rotate_qmt_token.ps1 已经是"缺省 dry-run + 显式 -Apply 才写"，两个运维脚本
#      缺省方向相反本身就会造成误操作（操作人按另一个脚本的肌肉记忆敲命令）。故此处对齐同一口径：
#      缺省＝只读预览（退出码 0），动手＝必须显式 -Apply，任何异常一律非零退出、绝不静默报成功。
#      （M-8/N-6 族姿势：失败必须响亮，"跑完了"不等于"办成了"。）
# 退出码：dry-run（缺省）→ 0（含义严格限定为"没改任何东西"，不代表退役已完成）；
#         -Apply 且停不干净（目标进程仍在监听）或 exe 改名失败 → 非 0；
#         -Apply 且已退役（无进程/无监听/exe 不在位）→ 0；预览或复核自身读不到 → 非 0。
# 部署侧入口：scripts/deploy_guangzhou.sh 的 QMT_MOCK_DECOMMISSION=1 显式开关（默认关）。
#   ⚠ 该步目前的 SSH 命令**没有带 -Apply**，改缺省后它只会打印预览就退出（退役不会发生）——
#     调用点必须显式补 -Apply，见 docs 与 verify 第 19 探针注释。
# 用法（管理员 PowerShell，或经部署脚本 SSH 执行）：
#   powershell ... -File decommission_qmt_mock.ps1            # 缺省 dry-run：只报计划与复核，不改任何东西
#   powershell ... -File decommission_qmt_mock.ps1 -Apply     # 确认 plan 清单后才真停进程+改名
param(
    [string]$UatRoot = "C:\qmt\uat",
    [int]$Port = 8799,
    [string]$ExeName = "qmt-mock.exe",
    # 缺省即预览，$DryRun 保留为显式预览的兼容别名（RUNBOOK 旧命令照旧可用），与 -Apply 互斥。
    [switch]$DryRun,
    [switch]$Apply
)
$ErrorActionPreference = "Stop"

function Log($m) { Write-Host "[decom] $m" }
# Die：走不下去的异常一律非零退出。绝不允许"异常被 catch 掉之后当成成功"（M-8/N-6 同族姿势）。
function Die($m) { Log $m; exit 1 }

# 缺省方向归一：没显式 -Apply 就是 dry-run（与 rotate_qmt_token.ps1 同一句式、同一语义）。
if ($Apply -and $DryRun) { Die "action=abort reason=dryrun-apply-mutually-exclusive (pass at most one of -DryRun/-Apply)" }
if (-not $Apply) { $DryRun = $true }

# 管理员校验：**只有动手侧需要**（Stop-Process 他进程 + 改名系统盘文件，同 register_engine_services.ps1
# 口径）。预览是纯只读的，拿普通权限也该能看清单——把管理员要求降回 dry-run 之外，等于让操作人
# 必须先提权才敢预览，反而更容易跳过预览直接动手。
if ($Apply) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Die "action=abort reason=not-admin"
    }
}

$rootPrefix = $UatRoot.TrimEnd('\') + '\'
$stamp = Get-Date -Format "yyyyMMddHHmm"

function Test-UnderUat([string]$p) {
    return ($p -and $p.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase))
}

# Get-MockState：**只读**复核当前退役状态。dry-run 与 -Apply 用**同一把尺子**量（两套判据迟早分叉，
# 分叉那天预览就是骗人的）。不改任何东西、不杀不rename。
# 返回：ExeFiles=目录内在位的现役名 exe 绝对路径；UatProcs=可执行路径落在 $UatRoot 下的进程；
#       MockListeners=监听 $Port 且属于 UatProcs 的（pid+exe）；OtherListeners=无关占用者计数（不判红）；
#       ListenerRead=$false 表示这台机器读不到监听表 —— 调用方必须把它当"未确认"而不是"没有监听"。
function Get-MockState {
    $st = @{ ExeFiles = @(); UatProcs = @(); MockListeners = @(); OtherListeners = 0; ListenerRead = $true }
    if (Test-Path -LiteralPath $UatRoot) {
        $st.ExeFiles = @(Get-ChildItem -LiteralPath $UatRoot -Filter $ExeName -File -ErrorAction SilentlyContinue | ForEach-Object { [string]$_.FullName })
    }
    try {
        foreach ($pr in @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)) {
            $ep = [string]$pr.ExecutablePath
            if (Test-UnderUat $ep) { $st.UatProcs += [pscustomobject]@{ Pid = [int]$pr.ProcessId; Exe = $ep } }
        }
    } catch {
        Die ("action=abort reason=proc-review-failed detail=" + $_.Exception.Message)
    }
    try {
        foreach ($l in @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop)) {
            $lExe = ""
            try { $lExe = [string](Get-CimInstance -ClassName Win32_Process -Filter ("ProcessId=" + $l.OwningProcess) -ErrorAction SilentlyContinue).ExecutablePath } catch { }
            if (Test-UnderUat $lExe) { $st.MockListeners += [pscustomobject]@{ Pid = [int]$l.OwningProcess; Exe = $lExe } }
            else { $st.OtherListeners += 1 }
        }
    } catch { $st.ListenerRead = $false }
    return $st
}

# 汇总一行 ASCII 复核结论（明细全 ASCII 的原因见文件头规矩③：GBK 回传让中文 grep 恒不命中）。
function Format-MockReview([string]$mode, $st, [int]$planCount) {
    $pending = $st.UatProcs.Count + $st.MockListeners.Count + $st.ExeFiles.Count
    $concl = "clean"
    if (-not $st.ListenerRead) { $concl = "indeterminate-listener-source" }
    if ($pending -gt 0) { $concl = "retirement-pending" }
    return ("review mode=" + $mode + " plan_targets=" + $planCount + " exe_in_place=" + $st.ExeFiles.Count +
        " uat_procs=" + $st.UatProcs.Count + " uat_listeners=" + $st.MockListeners.Count +
        " other_listeners=" + $st.OtherListeners + " listener_source=" + $(if ($st.ListenerRead) { "ok" } else { "unavailable" }) +
        " conclusion=" + $concl)
}

# 收集候选进程：可执行路径在 $UatRoot 之下，或命令行含独立端口号 8799（前后不接数字，防误伤
# 8789/18789 等近邻——真网关在 :8789，UAT 假柜台在 :18789，两者都**不在**退役范围）。
# Get-CimInstance 一次拿全（ExecutablePath + CommandLine），排除自身 PID。
$targets = @()
$skips = @()
try {
    $procs = @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)
} catch {
    Log ("action=abort reason=cim-enumeration-failed detail=" + $_.Exception.Message)
    exit 1
}
foreach ($pr in $procs) {
    if ($pr.ProcessId -eq $PID) { continue }
    $exe = [string]$pr.ExecutablePath
    $cl = [string]$pr.CommandLine
    $byPath = Test-UnderUat $exe
    $byCmd = ($cl -match ('(?<!\d)' + [regex]::Escape("$Port") + '(?!\d)'))
    if (-not $byPath -and -not $byCmd) { continue }
    if ($byPath) {
        $targets += [pscustomobject]@{ Pid = $pr.ProcessId; Exe = $exe; Name = [string]$pr.Name }
    } else {
        # 命令行命中但路径不在 $UatRoot：规矩②——skip 并留痕（target 取 exe，取不到记 pid）。
        $t = if ($exe) { $exe } else { ("pid=" + $pr.ProcessId) }
        $skips += $t
    }
}

Log ("phase=scan uatRoot=" + $UatRoot + " port=" + $Port + " targets=" + $targets.Count + " skips=" + $skips.Count)
foreach ($s in ($skips | Sort-Object -Unique)) { Log ("action=skip target=" + $s + " reason=not-under-uat-root") }

# ── 计划清单：本次调用**承诺**要改名的绝对路径（dry-run 只报它，apply 动完还逐条回查它）──
# 三个来源并起来去重：①在跑的 UAT 目标进程可执行路径；②目录里躺着的现役名文件（进程不在位的残留）；
# ③规范路径 <UatRoot>\<ExeName> 本身——即使前两者为空也要列，操作人才看得清"这个脚本动的是哪个绝对
# 路径、当前在不在位"，空清单与"脚本跑挂了"才是两回事（判据要能自证，别靠人脑补）。
$planned = @()
foreach ($t in ($targets | Sort-Object -Property Exe -Unique)) { $planned += [string]$t.Exe }
if (Test-Path -LiteralPath $UatRoot) {
    foreach ($f in @(Get-ChildItem -LiteralPath $UatRoot -Filter $ExeName -File -ErrorAction SilentlyContinue)) {
        if ($planned -notcontains $f.FullName) { $planned += [string]$f.FullName }
    }
}
$canonical = Join-Path $UatRoot $ExeName
if ($planned -notcontains $canonical) { $planned += $canonical }

# ── 缺省分支（dry-run）：只打印计划 + 当前是否在位 + 只读复核结论，然后原样退出 ──────────
if ($DryRun) {
    foreach ($p in $planned) {
        Log ("action=plan target=" + $p + " exists=" + (Test-Path -LiteralPath $p) + " new=" + (Split-Path -Leaf $p) + ".disabled-" + $stamp)
    }
    $stv = Get-MockState
    Log (Format-MockReview "dryrun" $stv $planned.Count)
    Log ("phase=dryrun done (nothing stopped, nothing renamed, exit_code_0_means_no_change_not_decommissioned)")
    # 这一行给人眼看的（与 rotate_qmt_token.ps1 同一套"预览→显式执行"话术，§OPS-ALIGN 要求）。
    # ⚠ 判据永远不要去 grep 这行中文：PS→SSH→bash 回传按 GBK 解码，中文明细不可 grep（规矩③）。
    #   机器判据只有两个：退出码，和上面那批 ASCII 的 action=/review 行。
    Write-Host "[decom] DRY-RUN 未改动任何东西 —— 以上是只读预览；核对 plan 清单后加 -Apply 才会真改名（退役）"
    exit 0
}

# ── 动手分支（显式 -Apply 才会走到这里）────────────────────────────────────────
foreach ($t in ($targets | Sort-Object -Property Exe -Unique)) {
    # 1) 停进程（该 exe 的全部实例）。Stop-Process 失败不致命：可能已自然退出，以监听复核为准。
    foreach ($p in ($targets | Where-Object { $_.Exe -eq $t.Exe })) {
        try {
            Stop-Process -Id $p.Pid -Force -ErrorAction Stop
            Log ("action=stop target=" + $t.Exe + " pid=" + $p.Pid)
        } catch {
            Log ("action=stop-fail target=" + $t.Exe + " pid=" + $p.Pid + " detail=" + $_.Exception.Message)
        }
    }
    # 2) 改名（可逆退役）。等文件锁释放：轮询到句柄放开或超时。
    $locked = $true
    for ($i = 0; $i -lt 10; $i++) {
        try { Rename-Item -LiteralPath $t.Exe -NewName ($t.Name + ".disabled-" + $stamp) -ErrorAction Stop; $locked = $false; break }
        catch { Start-Sleep -Milliseconds 500 }
    }
    if (-not $locked) {
        Log ("action=rename target=" + $t.Exe + " new=" + $t.Name + ".disabled-" + $stamp)
    } else {
        Log ("action=rename-fail target=" + $t.Exe + " reason=still-locked-after-5s")
    }
}

# 3) 幂等补刀：进程不在、但 exe 仍躺在 $UatRoot（上次部署残留 / 手工拷贝）→ 同样改名退役。
#    上面按进程处置过的文件此刻已从磁盘消失（改名成功）或仍在那儿（改名失败，这里再试一次），
#    收尾一律以 $planned 逐条回查为准，不靠本步的日志。
if (Test-Path -LiteralPath $UatRoot) {
    foreach ($f in @(Get-ChildItem -LiteralPath $UatRoot -Filter $ExeName -File -ErrorAction SilentlyContinue)) {
        try {
            Rename-Item -LiteralPath $f.FullName -NewName ($f.Name + ".disabled-" + $stamp) -ErrorAction Stop
            Log ("action=rename target=" + $f.FullName + " new=" + $f.Name + ".disabled-" + $stamp)
        } catch {
            Log ("action=rename-fail target=" + $f.FullName + " detail=" + $_.Exception.Message)
        }
    }
}

# ── 收尾复核（e 条判据）：一律用 dry-run 那把同一个尺子量，别写两套──
# 与本退役无关的占用者（路径不在 $UatRoot）此前已 action=skip，不判红——本脚本只对自己承诺退役的东西负责。
Start-Sleep -Seconds 1
$fail = @()
$st = Get-MockState
foreach ($l in $st.MockListeners) {
    Log ("action=residual target=" + $l.Exe + " pid=" + $l.Pid + " reason=still-listening")
    $fail += ("listener pid=" + $l.Pid + " exe=" + $l.Exe)
}
foreach ($pr in $st.UatProcs) { $fail += ("process pid=" + $pr.Pid + " exe=" + $pr.Exe) }
# 计划清单里任何一条**仍然在位**＝改名没成功＝退役未成立。这里比旧实现更严：旧版只回查"有进程的
# 目标"，进程不在位的残留文件改名失败会被静默放过、最后照样报 ok=1（M-8/N-6 族那个"异常被吞成成功"
# 的形状），现在按 $planned 逐条查。
foreach ($p in $planned) {
    if (Test-Path -LiteralPath $p) {
        Log ("action=rename-unconfirmed target=" + $p + " reason=still-present")
        $fail += ("exe-present " + $p)
    }
}
# 读不到监听表＝"未确认"不是"没问题"：动手模式下绝不带着这个盲区报成功。
if (-not $st.ListenerRead) {
    Log ("action=residual target=port-" + $Port + "-listener-table reason=source-unavailable")
    $fail += "listener-check-unavailable"
}
Log (Format-MockReview "apply" $st $planned.Count)
if ($fail.Count -gt 0) {
    Log ("phase=result ok=0 residual=" + ($fail -join " | "))
    exit 1
}
Log "phase=result ok=1 (no target process, no target listener, uat exe retired or never present)"
exit 0
