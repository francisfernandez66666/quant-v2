# quant_lifecycle.ps1 — §LIFECYCLE 广州机数据生命周期治理任务（2026-10-16 建）
#
# 运维口径（本段是唯一权威描述，docs 另立 RUNBOOK 时以本文件为准）：
#
# 背景：广州执行机 C 盘 60G，2026-10-16 实录仅剩 ~7G（88% 满）。占用大头不是系统而是
# 量化数据：C:\var\lib\quant-trading-v2（trading.db 6.78G + 每日 verdicts jsonl）、
# C:\var\lib\quant-snapshot（nightly 全量暂存 7.8G）、C:\var\lib\quant-restic-repo（5.4G）。
# trading.db 经实测 freelist≈1——**不是 SQLite 膨胀，6.78G 全是真实数据**（其中
# research_ckpts 一张表 ~2.7G：逐窗口产物 JSON，口径位进 resume_key（§ADJ-BASIS）之前
# 写入的旧口径行永不重命中，是纯死重）。治理=给三类数据立保留策略，而不是 VACUUM。
#
# 本任务执行四步（每步独立成败、互不阻断，单步失败只记日志不中断后续）：
#   1) research_ckpts 旧口径修剪：research.exe prune-stale-checkpoints（dry-run 打印规模 →
#      --apply 删除）。工具自带四道门（在跑门/无基线门等），被门挡住时退出码非零=本轮跳过，
#      脚本记录完整 dry-run 统计后继续——夜间研究链积压时门会长期关闭，属预期，不是故障。
#      为什么可以自动 --apply：stale 判据=键不含当前口径位 且 created_at<cutoff，这类行
#      对任何现在/未来的任务都不可达（新任务一律带口径位），删除不影响在跑任务；规模数字
#      每轮落 lifecycle.log，owner 可复核。
#   2) verdicts-*.jsonl 归档：>JsonlKeepDays 天的按日文件 gzip。安全性依据：AttachAudit
#      只回灌**当日**文件（signalctl.go auditFileFor(now)），旧文件应用层不读；且 nightly
#      snapshot 已把 accounts/ 整目录收进 restic。gzip 后原名删除，审计能力不丢。
#   3) restic forget --keep-last N --prune：本地备份库保留策略（防夜间快照无限堆积）。
#   4) trading.db / live.db WAL checkpoint(TRUNCATE)：收缩 -wal 文件（长事务读者会阻断
#      自动检查点；TRUNCATE 失败时回落 PASSIVE，只记日志）。
#
# 安装（SYSTEM 计划任务，周日 05:30——nightly snapshot 04:00 之后、周一开盘前）：
#   落盘路径与 register_engine_services.ps1 §6c 的 $PSScriptRoot 解析同源
#   （deploy/qmt-win/，与 prune_logs.ps1 同目录），注册体/回读在 §6c，本节命令等价于
#   其上机手工直跑。执行体会做真删数据的四步，属现网特权变更（§0929OPS-⑪）：
#   schtasks /Create /F /TN Quant-Lifecycle /SC WEEKLY /D SUN /ST 05:30 /RU SYSTEM /RL HIGHEST ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\qmt-win\quant_lifecycle.ps1"
#   schtasks /Run /TN Quant-Lifecycle   # 手动触发一次验证
# 手动执行：powershell -NoProfile -ExecutionPolicy Bypass -File <本文件> [-WhatIf]
#   -WhatIf 干跑：只列将归档/将删的清单与 dry-run 统计，不写任何数据。
#
# 编码铁律（§ENH-A / §P0-B 同款教训）：运行期日志行保持 ASCII（PS5.1 的 Add-Content 走
# 系统 ANSI 码页，中文会变 '?'）；本文件含中文注释 ⇒ **必须带单个 UTF-8 BOM**，否则 PS5.1
# 按 GBK 解析中文注释直接 ParserError。部署侧由 deploy_guangzhou.sh 的 ps1_bom 归一。
#
# 返回值：0=本步无需动作或全部成功（含"被门挡住"这种正常跳过）；2=脚本级异常。
# English: weekly data-lifecycle governor for the Guangzhou box — stale-checkpoint prune
# (tool self-guards), verdicts JSONL gzip archival, restic retention, WAL checkpoint.
# Each step is independent: a failure (or a guard refusal) is logged, never fatal.
param(
    [int]$JsonlKeepDays = 14,      # verdicts jsonl 保留天数（更早的 gzip 归档）
    [int]$ResticKeep = 3,          # restic 保留最近 N 个快照
    [int]$LogMaxMB = 5,            # lifecycle.log 轮转阈值
    [switch]$WhatIf
)
$ErrorActionPreference = "Stop"

# ── 路径（绝对路径优先于 PATH：SYSTEM 任务的 PATH 与交互会话不同，见 §P0-B 同款理由） ──
$DeployDir   = "C:\opt\quant"
$DataDir     = "C:\var\lib\quant-trading-v2"
$TradingDb   = Join-Path $DataDir "trading.db"
$LiveDb      = Join-Path $DataDir "live.db"
$AccountsDir = Join-Path $DataDir "accounts"
$ResearchExe = Join-Path $DeployDir "research.exe"
$ResticExe   = "C:\opt\quant\tools\restic.exe"
$ResticRepo  = "C:\var\lib\quant-restic-repo"
$ResticPass  = "C:\opt\quant\tools\restic-pass.txt"
$Python      = "C:\Python312\python.exe"
$LifeDir     = "C:\var\lib\quant-lifecycle"
$LogFile     = Join-Path $LifeDir "lifecycle.log"

# ── 日志（ASCII 行；中文只进注释与本段，不进运行日志） ──
New-Item -ItemType Directory -Force -Path $LifeDir | Out-Null
function Log($m) {
    $line = (Get-Date -Format "yyyy-MM-dd HH:mm:ss") + " " + $m
    Add-Content -Path $LogFile -Value $line
    Write-Host $line
}
# 轮转：超阈值改名 .1（单文件历史，够用；不引第三方日志库）
if ((Test-Path $LogFile) -and ((Get-Item $LogFile).Length -gt $LogMaxMB*1MB)) {
    Move-Item -LiteralPath $LogFile -Destination ($LogFile + ".1") -Force
}

# Invoke-Native：以「stderr 不再是终止性错误」的语义跑原生命令，返回 @{code; out}。
# 存在理由：$ErrorActionPreference='Stop' 下原生 stderr 会被 PS5.1 升级成终止错误，
# 而 research.exe/restic 的正常输出大概率就走 stderr。同 backup_snapshot.ps1 口径。
function Invoke-Native {
    param([string]$Exe, [string[]]$CmdArgs)
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $o = & $Exe @CmdArgs 2>&1
    $c = $LASTEXITCODE
    $ErrorActionPreference = $prev
    return @{ code = $c; out = @($o | ForEach-Object { [string]$_ }) }
}

Log ("=== lifecycle start (WhatIf=$WhatIf JsonlKeepDays=$JsonlKeepDays ResticKeep=$ResticKeep) ===")

# ═══ Step 1: research_ckpts stale prune ═══════════════════════════════════════════
# research.exe 的 prune-stale-checkpoints：缺省 dry-run 打规模；--apply 真删。
# 退出码非零 = 被在跑门/无基线门挡住（设计行为），本轮跳过 apply，dry-run 统计照常落盘。
try {
    if (-not (Test-Path $ResearchExe)) { throw "research.exe not found: $ResearchExe" }
    if (-not (Test-Path $TradingDb))    { throw "trading.db not found: $TradingDb" }
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $pruneOut = Join-Path $LifeDir ("ckpt-prune-" + $stamp + ".log")
    if ($WhatIf) {
        Log "[1/4] ckpt prune: WhatIf - dry-run only"
        $r = Invoke-Native $ResearchExe @("--db", $TradingDb, "prune-stale-checkpoints")
        $r.out | Out-File -FilePath $pruneOut -Encoding utf8
        Log ("[1/4] ckpt dry-run exit=" + $r.code + " detail=" + $pruneOut)
    } else {
        Log "[1/4] ckpt prune: apply (tool self-guards; nonzero exit = skipped by design)"
        $r = Invoke-Native $ResearchExe @("--db", $TradingDb, "prune-stale-checkpoints", "--apply")
        $r.out | Out-File -FilePath $pruneOut -Encoding utf8
        # 从工具输出里抓两行 ASCII 摘要（TotalRows/StaleRows）——工具中文输出只进详情文件
        $sum = ($r.out | Where-Object { $_ -match "TotalRows|StaleRows|DeletedRows" }) -join " | "
        if ($sum) { Log ("[1/4] ckpt summary: " + $sum) }
        if ($r.code -ne 0) {
            Log ("[1/4] ckpt prune skipped by guard (exit=" + $r.code + "); stats in " + $pruneOut)
        } else {
            Log "[1/4] ckpt prune done"
        }
    }
} catch {
    Log ("[1/4] ckpt prune ERROR: " + $_.Exception.Message)
}

# ═══ Step 2: verdicts jsonl gzip archival ═════════════════════════════════════════
# 只动 >KeepDays 天的旧文件；当日/近期文件原样。.gz 已存在则跳过；压缩并校验后才删原件。
try {
    if (-not (Test-Path $AccountsDir)) { throw "accounts dir not found: $AccountsDir" }
    $cut = (Get-Date).AddDays(-$JsonlKeepDays)
    $files = @(Get-ChildItem $AccountsDir -Recurse -File -Filter "verdicts-*.jsonl" -ErrorAction SilentlyContinue |
        Where-Object { $_.LastWriteTime -lt $cut })
    if ($files.Count -eq 0) {
        Log "[2/4] jsonl archive: nothing older than $JsonlKeepDays days"
    } else {
        $before = ($files | Measure-Object -Sum Length).Sum
        Log ("[2/4] jsonl archive: " + $files.Count + " files, " + [math]::Round($before/1MB,1) + " MB to compress")
        $done = 0; $saved = 0
        foreach ($f in $files) {
            $gz = $f.FullName + ".gz"
            if (Test-Path $gz) { Log ("[2/4]   skip (gz exists): " + $f.Name); continue }
            if ($WhatIf) { Log ("[2/4]   whatif gzip: " + $f.Name); continue }
            $in  = [System.IO.File]::OpenRead($f.FullName)
            $out = [System.IO.File]::Create($gz)
            $zs  = New-Object System.IO.Compression.GzipStream($out, [System.IO.Compression.CompressionLevel]::Optimal)
            try {
                $in.CopyTo($zs)
            } finally {
                $zs.Dispose(); $out.Dispose(); $in.Dispose()
            }
            # 校验：.gz 非空才删原件（压缩失败但文件建了=不会到这；双保险）
            if ((Test-Path $gz) -and ((Get-Item $gz).Length -gt 0)) {
                Remove-Item -LiteralPath $f.FullName -Force
                $done++; $saved += ($f.Length - (Get-Item $gz).Length)
            } else {
                Log ("[2/4]   WARN gz empty, keep original: " + $f.Name)
            }
        }
        Log ("[2/4] jsonl archive done: compressed=" + $done + " saved=" + [math]::Round($saved/1MB,1) + " MB")
    }
} catch {
    Log ("[2/4] jsonl archive ERROR: " + $_.Exception.Message)
}

# ═══ Step 3: restic retention ═════════════════════════════════════════════════════
# WhatIf 干跑必须跳过：forget --prune 会**真的删快照**，与"-WhatIf 不写任何数据"的
# 头部承诺直接矛盾（干跑的意义就是让人先看清四步会动什么再决定按不按）。
try {
    if ($WhatIf) {
        Log "[3/4] restic retention: WhatIf - skip (would run: forget --keep-last $ResticKeep --prune)"
    } else {
    if (-not (Test-Path $ResticExe)) { throw "restic not found: $ResticExe" }
    if (-not (Test-Path $ResticRepo)) { throw "restic repo not found: $ResticRepo" }
    $before = (Get-PSDrive C).Free
    $r = Invoke-Native $ResticExe @("-r", $ResticRepo, "--password-file", $ResticPass,
        "forget", "--keep-last", "$ResticKeep", "--prune")
    $r.out | Out-File -FilePath (Join-Path $LifeDir ("restic-forget-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".log")) -Encoding utf8
    $after = (Get-PSDrive C).Free
    if ($r.code -ne 0) {
        Log ("[3/4] restic forget exit=" + $r.code + " (see detail log); continue")
    } else {
        Log ("[3/4] restic forget --keep-last $ResticKeep --prune done; disk free " +
             [math]::Round($before/1GB,2) + "G -> " + [math]::Round($after/1GB,2) + "G")
    }
    }
} catch {
    Log ("[3/4] restic retention ERROR: " + $_.Exception.Message)
}

# ═══ Step 4: WAL checkpoint ═══════════════════════════════════════════════════════
# 长事务读者（researchd 的历史扫描）会阻断 SQLite 自动检查点，-wal 会累积（实测 0.25G）。
# TRUNCATE 需要无读者；失败回落 PASSIVE（尽力收缩，不报错）。
# WhatIf 同样跳过：wal_checkpoint 是写操作（PASSIVE 也会把能搬的页搬回主库）。
try {
    if ($WhatIf) {
        Log "[4/4] wal checkpoint: WhatIf - skip (would run: PRAGMA wal_checkpoint(TRUNCATE) then PASSIVE)"
    } else {
    $py = @'
import sqlite3, sys
for db in sys.argv[1:]:
    try:
        con = sqlite3.connect(db, timeout=30)
        con.execute("PRAGMA busy_timeout=30000")
        r = con.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
        print(db, "checkpoint(TRUNCATE)=", r)
        con.close()
    except Exception as e:
        print(db, "TRUNCATE failed:", e)
        try:
            con = sqlite3.connect(db, timeout=30)
            r = con.execute("PRAGMA wal_checkpoint(PASSIVE)").fetchone()
            print(db, "checkpoint(PASSIVE)=", r)
            con.close()
        except Exception as e2:
            print(db, "PASSIVE failed:", e2)
'@
    $pyFile = Join-Path $LifeDir "_wal_checkpoint.py"
    $py | Out-File -FilePath $pyFile -Encoding utf8
    $r = Invoke-Native $Python @($pyFile, $TradingDb, $LiveDb)
    foreach ($l in $r.out) { Log ("[4/4] " + $l) }
    if ($r.code -ne 0) { Log ("[4/4] wal checkpoint exit=" + $r.code) }
    }
} catch {
    Log ("[4/4] wal checkpoint ERROR: " + $_.Exception.Message)
}

# ═══ 收尾：磁盘读数 ═══════════════════════════════════════════════════════════════
try {
    $free = (Get-PSDrive C).Free
    Log ("=== lifecycle done; C free = " + [math]::Round($free/1GB,2) + " GB ===")
} catch {
    Log "=== lifecycle done (disk readout failed) ==="
}
exit 0
