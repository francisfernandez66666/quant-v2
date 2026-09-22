# backup_snapshot.ps1 — Guangzhou nightly consistent snapshot producer (HARDENING item 1, pull model)
# Role: at off-hours, produce a transactionally-consistent snapshot of every production database
#       (SQLite .backup API, safe under concurrent writers) + copy the small JSON state/config files
#       and the per-account state directory into a staging folder.
#       This staging folder is what the Mac dev machine rsync/restic-pulls and feeds into restic.
#       We do NOT push from Guangzhou (NAT); we only prepare files for the Mac to pull.
# Install (SYSTEM scheduled task, daily 04:00 Beijing, survives RDP/SSH disconnect):
#   schtasks /Create /F /TN quant-backup-snap /SC DAILY /ST 04:00 /RU SYSTEM /RL HIGHEST ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\opt\quant\deploy\qmt-win\backup_snapshot.ps1"
#   schtasks /Run /TN quant-backup-snap
#
# ── §P0-B（2026-09-23 晚批，中文口径，本段是权威说明；RUNBOOK_LIVEBACKUP.md 只是副本）──────
# 变更：备份对象从「trading.db + 9 个 JSON」扩为「trading.db + live.db + 9 个 JSON + accounts/ 整目录」。
# 为什么 live.db 是本批优先级最高的不可逆风险：它是实盘持仓/委托/成交/资产四本账
#   （独立库，cmd/quant/main.go:266-278 打开），而**此前没有任何一份灾备方案覆盖它**——
#   Mac/Linux 侧 scripts/backup.sh 有它（:31 循环 trading.db live.db + :54 accounts），
#   广州侧 backup_snap.py 的 SRC/DST 当时是硬编码单库只快照 trading.db，
#   archive/R7_HARDENING_PLAN_20260908.md §R7-A1 明文只界定 Mac。广州盘坏 = 实盘账本全损且无补救。
# 铁律一：**禁止把 backup_snap.py 换成裸 cp / Copy-Item**。两库都是 WAL 且引擎在写，
#   裸拷贝会得到撕裂快照（大小正常、能打开、账本却错位）——一致性只能由 sqlite3 backup API 保证。
# 铁律二：**备份对象集合必须与 scripts/backup.sh 逐相等**（跨机等值正锁，verify 第 58 项）。
#   任何一侧新增一个库/目录而另一侧未同步 → 锁红。改 $DbItems / $CopyItems / $AccountsDirName 时同改两处。
# 铁律三：**缺库即失败，不静默跳过**。降级不得报成功（本批当日主题）：live.db 不存在时抛错退出 1，
#   SNAPSHOT_OK 写 ok:false，Mac 拉取器 restic_pull_backup.sh:47 读到非 ok 立刻 ntfy 告警。
# 编码：运行期日志行保持 ASCII（Add-Content 在 PS5.1 走系统 ANSI 码页，中文会变 '?'，
#   且本文件历史上就是 ASCII-only）；但自本版起含中文注释 ⇒ **文件必须带单个 UTF-8 BOM**，
#   否则 PS5.1 按 GBK 解析中文注释直接 ParserError（教训见 §ENH-A run_ths_backfill.ps1 锁）。
#   本文件自 2026-09-23（§P0-B 收编）起随 scripts/deploy_guangzhou.sh 步 [2e] 自动下发到
#   C:\opt\quant\deploy\qmt-win\（与计划任务指向同源），不再依赖手工安装；部署链会先跑
#   ps1_bom 归一（**只保证 UTF-8 单 BOM，不改行尾**——本文件历史上是 BOM+LF，现网 PS5.1 实跑通过，
#   所以别再照旧文档"顺手转 CRLF"，那只会让仓库副本与现网字节不一致）。手工上机口径保留在
#   RUNBOOK_LIVEBACKUP.md §2 作应急路径。
$ErrorActionPreference = "Stop"

$DataDir  = "C:\var\lib\quant-trading-v2"
$Deploy   = "C:\opt\quant"
$SnapRoot = "C:\var\lib\quant-snapshot"      # pull target; overwritten each night
$Log      = "C:\var\lib\quant-snapshot\_snapshot.log"

# 备份对象集合（§P0-B / 锁 58）：库名列表。scripts/backup.sh 的 `for DB in ...` 必须与此逐相等。
#   trading.db = 夜间研究库；live.db = 实盘账本（§OPT-3 拆分，含 real_positions/orders/fills/real_account）。
$DbItems = @("trading.db", "live.db")
# 小 JSON 状态/配置文件（与 scripts/backup.sh 的 auth/config/applied_*/grayscale 集合同源，落 state/）。
$copyItems = @(
    "config.json",
    "auth.json",
    "grayscale_rules.json",
    "applied_factors.json",
    "applied_patterns.json",
    "paper.json",
    "portfolio.json",
    "research_state.json",
    "news_tracker.json"
)
# 账号目录整目录拷贝（§P0-B 新增，对应 scripts/backup.sh:54 的 cp -r accounts）：
#   <DataDir>/accounts/<userID>/ 下是 per-user 落盘态——paper.json（internal/engine/registry.go:164）、
#   paper_sell_anchors.json（同文件 §M10 移动止盈锚点）、stage_records.json / signal_records.json
#   （internal/server/server.go:2613/2643），账号目录本身在 registry.go:699 装配。
#   丢这个目录 = 每个账号的模拟盘账本 + 当日信号留痕全丢，且不会有任何报错提示。
$AccountsDirName = "accounts"

# python with sqlite3 3.49 (verified on gz); resolve absolute path so SYSTEM-task PATH quirks can't break it.
$PythonCmd = Get-Command python -ErrorAction SilentlyContinue
if ($PythonCmd) { $Python = $PythonCmd.Source } else { $Python = "C:\Python312\python.exe" }

function Log($m) { $line = (Get-Date -Format "yyyy-MM-dd HH:mm:ss") + " " + $m; Add-Content -Path $Log -Value $line; Write-Host $line }

# Invoke-Native：以「stderr 不再是终止性错误」的语义跑原生命令，返回 @{code; out}。
# 存在理由见调用点注释（$ErrorActionPreference='Stop' + 原生 stderr 会被 PS5.1 升级成终止错误）。
# 说明：参数名刻意用 CmdArgs——$Args 是 PS 自动变量，占用会静默改语义。
function Invoke-Native {
    param([string]$Exe, [string[]]$CmdArgs)
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $o = & $Exe @CmdArgs 2>&1
    $c = $LASTEXITCODE
    $ErrorActionPreference = $prev
    return @{ code = $c; out = @($o | ForEach-Object { [string]$_ }) }
}

New-Item -ItemType Directory -Force -Path $SnapRoot | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path $Log) | Out-Null

try {
    Log "=== snapshot start ==="

    # -1) 单写者锁（§SNAP-LOCK，2026-09-23 现网实录锤实）：快照产物路径是**固定名覆盖**
    #     （SnapRoot\trading.db / live.db 每晚被下一次跑重写），所以"两个快照进程同时在跑"不是
    #     浪费一次 IO 那么简单——两份 sqlite backup 交替写同一个目标文件，产出一个大小正常、
    #     PRAGMA integrity_check 却可能通过的混合页快照，比缺一次备份更危险（坏在恢复那天才暴露）。
    #     实录：SSH 中断把一个交互 powershell 留在远端继续跑，紧接着计划任务又被触发，第二次的
    #     `restic forget --prune` 直接撞上第一次的仓库锁（exit=11 repo already locked）。
    #     不变量必须写在**被调用的脚本**里：入口有三种（04:00 计划任务 / 部署步 [6/6] 触发 /
    #     运维手工 RUNBOOK §2 步骤 3），任何调用方都只看得见自己那一种，靠调用方"先看任务状态
    #     再决定要不要触发"的守卫必然漏（我今天就先写了一版那样的假守卫）。
    # 接管口径：持有者 PID 仍是活动 powershell ⇒ 拒跑（抛错→ok:false，吵而不是等）；PID 解析不出、
    #     进程名不是 powershell/pwsh、或锁龄超过 $LockMaxMin ⇒ 视为陈旧锁，Log 一行 WARN 后接管。
    #     为什么要有龄上界：PID 会被回收复用，复用到一个无关 powershell 上时会把后来者永久锁死；
    #     留上界的最坏后果只是"拒跑到龄满"，且每次接管都带 WARN 行可查。上界取 6h：单次快照在夜间
    #     窗口内跑完是设计前提（04:00 触发、06:00 前研究链要用盘），超过即认定那一轮已经死了。
    #     （本仓库现网整轮真实耗时尚未取证——09-23 那次被 SSH 中断，日志未读到，故这里只给上界、
    #     不谎称"实测分钟级"。）
    $Lock = Join-Path $SnapRoot ".backup.lock"
    $LockMaxMin = 360
    $holderTaken = $false
    if (Test-Path $Lock) {
        $lk = ""
        try { $lk = [string](Get-Content -Path $Lock -Raw -Encoding ASCII) } catch { $lk = "" }
        $ageMin = $LockMaxMin + 1
        try { $ageMin = [int]((Get-Date) - (Get-Item $Lock).LastWriteTime).TotalMinutes } catch { }
        $holderPid = 0
        if ($lk -match 'pid=(\d+)') { $holderPid = [int]$Matches[1] }
        $holder = $null
        if ($holderPid -gt 0) { $holder = Get-Process -Id $holderPid -ErrorAction SilentlyContinue }
        if ($holder -and $holder.ProcessName -match '^(powershell|pwsh)$' -and $ageMin -lt $LockMaxMin) {
            throw ("another snapshot run is active: " + $lk.Trim())
        }
        Log ("WARN: stale snapshot lock taken over (age_min=" + $ageMin + " content=" + $lk.Trim() + ")")
    }
    # 锁内容只放排障需要的三元组（谁/在哪台/何时起），不放任何密钥或路径之外的信息。
    Set-Content -Path $Lock -Encoding ascii -NoNewline -Value ("pid=" + $PID + " host=" + $env:COMPUTERNAME + " started=" + (Get-Date -Format "yyyy-MM-dd HH:mm:ss"))
    # 从此行起才是"我持有锁"——catch 里的释锁只对持有者生效，拒跑分支（上面那条 throw）永不释别人锁。
    $holderTaken = $true

    # 0) Disk guard: now TWO databases land here (trading.db ~5GB + live.db), plus the relay repo
    #    needs headroom on C:. Refuse if <8GB free (unchanged threshold; see HARDENING plan risk note).
    $c = Get-PSDrive -Name C
    if ($c.Free -lt 8GB) { throw ("C: free space " + [math]::Round($c.Free/1GB,1) + "GB < 8GB guard") }

    # 1+2) Consistent single-file snapshot + integrity_check via SQLite backup API (python stdlib,
    #      safe while engine keeps writing). Logic lives in backup_snap.py next to this script;
    #      since §P0-B it takes (src, dst) as argv, so we call it once per database in $DbItems.
    $snapPy = Join-Path $PSScriptRoot "backup_snap.py"
    if (!(Test-Path $snapPy)) { throw "backup_snap.py missing at $snapPy" }
    $dbBytes = @{}
    foreach ($db in $DbItems) {
        $src = Join-Path $DataDir $db
        $dst = Join-Path $SnapRoot $db
        # 缺库=硬失败（铁律三）。live.db 缺失意味着实盘账本拆分未落地或数据目录漂移，
        # 今晚"少备一个库"必须吵，不能像旧版那样无感。
        if (!(Test-Path $src)) { throw "source db missing: $src (every db in `$DbItems must be backed up, silent skip is the §P0-B defect)" }
        # 临时降 EAP：PS 5.1 在 $ErrorActionPreference='Stop' 下会把原生命令的**任何 stderr 行**
        # 转成 NativeCommandError 直接终止（教训实录见 §ENH-A / run_ths_backfill.ps1 静态锁：
        # 09-20 首跑退出码 1、日志 0 字节）。backup_snap.py 的失败详情恰恰走 stderr，
        # 不降档就只能看到一个被吞掉的假栈，看不到「哪个库/为什么」。退出码仍显式上抛。
        $eapPrev = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $out = & $Python $snapPy $src $dst 2>&1
        $snapCode = $LASTEXITCODE
        $ErrorActionPreference = $eapPrev
        if ($snapCode -ne 0) { throw ("backup_snap.py " + $db + " exit=" + $snapCode + " : " + ($out -join " | ")) }
        Log ("backup_snap " + $db + ": " + ($out -join " | "))
        $dbBytes[$db] = (Get-Item $dst).Length
    }
    $dbDst = Join-Path $SnapRoot "trading.db"

    # 3) Copy small mutable state/config files (skip WAL of live db; snapshot db is standalone).
    $stateDir = Join-Path $SnapRoot "state"
    New-Item -ItemType Directory -Force -Path $stateDir | Out-Null
    foreach ($f in $copyItems) {
        $p = Join-Path $DataDir $f
        if (Test-Path $p) { Copy-Item $p (Join-Path $stateDir $f) -Force }
    }

    # 3b) §P0-B: mirror the whole per-account directory into <SnapRoot>/accounts (same layout as
    #     scripts/backup.sh's ${DEST}/accounts, so one restore drill can assert both machines).
    #     robocopy /MIR instead of Copy-Item -Recurse: Copy-Item nests the source when the
    #     destination already exists (accounts\accounts\...) and never drops deleted accounts;
    #     /MIR makes the nightly staging folder an exact mirror. Exit code >=8 is a real failure
    #     (0..7 all mean "nothing is wrong": 1 copied, 2 extras, 3 copied+extras, ...).
    $acctSrc = Join-Path $DataDir $AccountsDirName
    $acctDst = Join-Path $SnapRoot $AccountsDirName
    $acctCount = 0
    if (Test-Path $acctSrc) {
        & robocopy $acctSrc $acctDst /MIR /R:1 /W:1 /NFL /NDL /NJH /NJS /NP | Out-Null
        $rc = $LASTEXITCODE
        if ($rc -ge 8) { throw "robocopy accounts exit=$rc (mirror failed, per-account state not staged)" }
        $acctCount = @(Get-ChildItem -Path $acctDst -Recurse -File -ErrorAction SilentlyContinue).Count
        if ($acctCount -eq 0) { throw "accounts mirror produced 0 files (source $acctSrc empty or robocopy lied)" }
        Log ("accounts mirrored files=" + $acctCount)
    } else {
        # 账号目录尚不存在 = 从未有账号登录过（registry.go:699 首次登录才建目录）。允许，但必须留痕，
        # 否则「accounts 集合缺失」与「accounts 为空」在产物上同形（本批 §GATEPRESENCE 同一口径）。
        Log ("WARN accounts dir absent at $acctSrc (no account logged in yet?) - staged snapshot has no accounts/")
    }

    # 4) Copy deploy scripts/config (small, for rebuild reference; NOT binaries).
    $deployDir = Join-Path $SnapRoot "deploy"
    New-Item -ItemType Directory -Force -Path $deployDir | Out-Null
    if (Test-Path "$Deploy\deploy\qmt-win") { Copy-Item "$Deploy\deploy\qmt-win" $deployDir -Recurse -Force }
    if (Test-Path "$Deploy\Caddyfile") { Copy-Item "$Deploy\Caddyfile" $deployDir -Force }
    if (Test-Path "$Deploy\config.json") { Copy-Item "$Deploy\config.json" $deployDir -Force }

    # 5) Restic backup into the LOCAL relay repo (fast, disk-only; the Mac pulls chunk deltas
    #    via `restic copy` over sftp — that direction is Mac-initiated, so no inbound to Mac needed).
    $Restic  = "C:\opt\quant\tools\restic.exe"
    $Pass    = "C:\opt\quant\tools\restic-pass.txt"
    $RepoDir = "C:\var\lib\quant-restic-repo"
    if (!(Test-Path $Restic)) { throw "restic.exe missing at $Restic" }
    if (!(Test-Path $Pass))   { throw "restic-pass.txt missing at $Pass" }
    $env:RESTIC_PASSWORD_FILE = $Pass
    # §RESTIC-LOCK（2026-09-23 §P0-B 现网首跑前置排障，verify 第 16 探针判红锤实）：
    # 中转仓库会被 **Mac 拉取器**留下的陈旧锁毒化——`restic copy` 会锁源仓库，拉取进程被中断时
    # 锁文件留在仓库里，且锁内记录的是 Mac 的 PID/主机名（实录：`already locked by PID 25716 on
    # MafiaMacBook-Air.local by zhangzifei (UID 501, GID 20)`），Windows 端无从自证其失效。后果不是
    # "当晚少一次增量"这么简单：backup 直接失败 → 走 catch 写 ok:false → Mac 拉取器读到非 ok 就不
    # 再 copy，异地半边从此**永久停更**，而文件级快照每晚照常刷新（看起来一切正常）。
    # 处置口径：只在退出码非 0 **且**错误文本命中 `already locked` 时 unlock 一次并重试一次；
    # 其它错误一律如实抛。绝不无条件 unlock——那等于在有真实并发写入时把互斥拆掉。
    # 为什么必须包 Invoke-Native 这一层：本文件顶部是 $ErrorActionPreference='Stop'，PS5.1 在该
    # 语义下把原生命令写出的**每一行 stderr 升级为终止性错误**（同族实录：run_ths_backfill.ps1
    # 首跑退出码 1 日志 0 字节、caddy validate 的 INFO 日志杀掉 [2d]）。restic 的习惯是把进度和
    # 提示也写 stderr ⇒ "备份其实成功、脚本判失败"。唯一可信判据是退出码。
    $bkArgs = @("backup", "-r", $RepoDir, $SnapRoot, "--tag", "nightly")
    $bk = Invoke-Native $Restic $bkArgs
    foreach ($l in $bk.out) { Log ("restic: " + $l) }
    if ($bk.code -ne 0) {
        $txt = $bk.out -join ' '
        if ($txt -notmatch 'already locked') { throw ("restic backup exit=" + $bk.code + ": " + $txt) }
        Log "restic: 检出陈旧锁 -> unlock 后重试一次"
        $ulArgs = @("unlock", "-r", $RepoDir)
        $ul = Invoke-Native $Restic $ulArgs
        foreach ($l in $ul.out) { Log ("restic-unlock: " + $l) }
        $bk2 = Invoke-Native $Restic $bkArgs
        foreach ($l in $bk2.out) { Log ("restic-retry: " + $l) }
        if ($bk2.code -ne 0) { throw ("restic backup retry exit=" + $bk2.code + ": " + ($bk2.out -join ' ')) }
    }
    # Transient relay retention (Mac keeps the long history): 3 days + 2 weeks.
    $fgArgs = @("forget", "--repo", $RepoDir, "--keep-daily", "3", "--keep-weekly", "2", "--prune")
    $fg = Invoke-Native $Restic $fgArgs
    foreach ($l in $fg.out) { Log ("restic-forget: " + $l) }
    if ($fg.code -ne 0) { throw ("restic forget/prune exit=" + $fg.code + ": " + ($fg.out -join ' ')) }
    Remove-Item Env:RESTIC_PASSWORD_FILE

    # 6) Marker for the Mac puller: freshness + per-db size + integrity + accounts file count.
    #    db_bytes stays trading.db for backward compatibility (restic_pull_backup.sh today only
    #    greps "ok":true); the dbs map makes "which databases actually got snapshotted, how big"
    #    machine-checkable from the artifact itself — so the §P0-B cross-machine set equality can
    #    be verified on the *product*, not only on the two scripts.
    $marker = [ordered]@{
        ok          = $true
        ts          = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss")
        db_bytes    = $dbBytes["trading.db"]
        dbs         = $dbBytes
        integrity   = "ok"
        accounts_files = $acctCount
        repo        = "C:/var/lib/quant-restic-repo"
    }
    ($marker | ConvertTo-Json -Compress) | Set-Content -Path (Join-Path $SnapRoot "SNAPSHOT_OK") -Encoding ascii -NoNewline
    Log ("=== snapshot+restic done dbs=" + (($dbBytes.Keys | Sort-Object) -join ","))
    # 释锁在两个出口各写一次，不用 finally：`try 内 exit` 是否跑 finally 在 PS 各版本语义不一，
    # 而本文件没有 pwsh 可实跑验证——留一个"跑成功却不释锁"的锁，最坏会让下一夜白拒一次。
    Remove-Item -LiteralPath $Lock -Force -ErrorAction SilentlyContinue
    exit 0
}
catch {
    Log ("ERROR: " + $_.Exception.Message)
    $bad = [ordered]@{ ok=$false; ts=(Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"); err=$_.Exception.Message }
    ($bad | ConvertTo-Json -Compress) | Set-Content -Path (Join-Path $SnapRoot "SNAPSHOT_OK") -Encoding ascii -NoNewline
    # 只在**自己拿到过锁**时释锁：拒跑分支（"another snapshot run is active"）抛错前根本没写锁文件，
    # 此处若无条件 Remove-Item 就会把真在跑的那位的锁删掉，单写者保护当场失效——正是要防的形态。
    if ($Lock -and $holderTaken) { Remove-Item -LiteralPath $Lock -Force -ErrorAction SilentlyContinue }
    exit 1
}
