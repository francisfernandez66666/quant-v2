# decommission_qmt_mock.ps1 — §QMT-MOCK-DECOM（2026-09-23）：退役实盘机上残留的 UAT qmt-mock。
# 背景（docs/PROGRESS.md 遗留项 + AUDIT_E2E_FULL_20260922PM）：C:\qmt\uat\qmt-mock.exe 自 09-10 起
#   常驻监听 0.0.0.0:8799（对外口）。本脚本退役的是**这个残留部署**，不是 cmd/qmt-mock 源码——
#   源码仍是测试基建（默认 -listen :8789 + scripts/uat_bootstrap.sh 用 :18789），仓库代码/配置
#   已 grep 核实对 8799 零引用。
# 语义：幂等 + 只认 C:\qmt\uat。三道硬规矩：
#   ① **可逆**：exe 只改名 <name>.disabled-<yyyyMMddHHmm>，绝不删除；
#   ② 凡可执行路径不在 $UatRoot 之下的进程一律 action=skip，不杀不rename（哪怕命令行里带 8799
#      ——真网关 :8789 与别的端口占用者都可能被文本误伤）；
#   ③ 每步判定打一行 `action=stop|rename|skip|plan target=<path>`，全 ASCII、不含任何密钥材料
#      （PS→SSH→bash 回传按 GBK 解码，中文明细会变乱码且不可 grep，教训见 verify 探针注释）。
# 退出码：停不干净（目标进程仍在监听）或 exe 改名失败 → 非 0；已退役（无进程/无监听/exe 不在位）→ 0。
# 部署侧入口：scripts/deploy_guangzhou.sh 的 QMT_MOCK_DECOMMISSION=1 显式开关（默认关）。
# 用法（管理员 PowerShell，或经部署脚本 SSH 执行）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File decommission_qmt_mock.ps1            # 动手
#   powershell -NoProfile -ExecutionPolicy Bypass -File decommission_qmt_mock.ps1 -DryRun    # 只判不动
param(
    [string]$UatRoot = "C:\qmt\uat",
    [int]$Port = 8799,
    [string]$ExeName = "qmt-mock.exe",
    [switch]$DryRun
)
$ErrorActionPreference = "Stop"

function Log($m) { Write-Host "[decom] $m" }

# 管理员校验：Stop-Process 他进程 + 改名系统盘文件都需要（同 register_engine_services.ps1 口径）。
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Log "action=abort reason=not-admin"; exit 1
}

$rootPrefix = $UatRoot.TrimEnd('\') + '\'
$stamp = Get-Date -Format "yyyyMMddHHmm"

function Test-UnderUat([string]$p) {
    return ($p -and $p.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase))
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

$renamed = @()
foreach ($t in ($targets | Sort-Object -Property Exe -Unique)) {
    if ($DryRun) { Log ("action=plan target=" + $t.Exe + " note=stop+rename"); continue }
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
        $renamed += $t.Exe
    } else {
        Log ("action=rename-fail target=" + $t.Exe + " reason=still-locked-after-5s")
    }
}

# 3) 幂等补刀：进程不在、但 exe 仍躺在 $UatRoot（上次部署残留 / 手工拷贝）→ 同样改名退役。
if (Test-Path $UatRoot) {
    foreach ($f in @(Get-ChildItem -LiteralPath $UatRoot -Filter $ExeName -File -ErrorAction SilentlyContinue)) {
        if ($DryRun) { Log ("action=plan target=" + $f.FullName + " note=rename"); continue }
        try {
            Rename-Item -LiteralPath $f.FullName -NewName ($f.Name + ".disabled-" + $stamp) -ErrorAction Stop
            Log ("action=rename target=" + $f.FullName + " new=" + $f.Name + ".disabled-" + $stamp)
        } catch {
            Log ("action=rename-fail target=" + $f.FullName + " detail=" + $_.Exception.Message)
        }
    }
}

if ($DryRun) { Log "phase=dryrun done (nothing stopped, nothing renamed)"; exit 0 }

# 4) 收尾复核（e 条判据）：目标进程/目标路径的监听一律算失败；与本退役无关的 8799 监听者
#    （路径不在 $UatRoot）此前已 action=skip，不判红——本脚本只对自己承诺退役的东西负责。
Start-Sleep -Seconds 1
$fail = @()
try {
    $listeners = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
} catch { $listeners = @() }
foreach ($l in $listeners) {
    $lExe = ""
    try { $lExe = [string](Get-CimInstance -ClassName Win32_Process -Filter ("ProcessId=" + $l.OwningProcess) -ErrorAction SilentlyContinue).ExecutablePath } catch { }
    if (Test-UnderUat $lExe) {
        $fail += ("listener pid=" + $l.OwningProcess + " exe=" + $lExe)
        Log ("action=residual target=" + $lExe + " pid=" + $l.OwningProcess + " reason=still-listening")
    } else {
        Log ("action=skip target=port-" + $Port + "-listener-pid-" + $l.OwningProcess + " reason=not-under-uat-root")
    }
}
# 残留目标进程（杀不掉/复活）也算失败。
foreach ($pr in (Get-CimInstance -ClassName Win32_Process -ErrorAction SilentlyContinue)) {
    if (Test-UnderUat ([string]$pr.ExecutablePath)) { $fail += ("process pid=" + $pr.ProcessId + " exe=" + $pr.ExecutablePath) }
}
# exe 名仍指回未改成功的目标（改名失败=退役未成立）。
foreach ($t in $targets) {
    if (($renamed -notcontains $t.Exe) -and (Test-Path -LiteralPath $t.Exe)) { $fail += ("exe-present " + $t.Exe) }
}
if ($fail.Count -gt 0) {
    Log ("phase=result ok=0 residual=" + ($fail -join " | "))
    exit 1
}
Log "phase=result ok=1 (no target process, no target listener, uat exe retired or never present)"
exit 0
