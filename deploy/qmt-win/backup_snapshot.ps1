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
#   ps1_bom 归一（UTF-8 单 BOM + CRLF）。手工上机口径保留在 RUNBOOK_LIVEBACKUP.md §2 作应急路径。
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

New-Item -ItemType Directory -Force -Path $SnapRoot | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path $Log) | Out-Null

try {
    Log "=== snapshot start ==="

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
    & $Restic backup -r $RepoDir $SnapRoot --tag nightly 2>&1 | ForEach-Object { Log ("restic: " + $_) }
    if ($LASTEXITCODE -ne 0) { throw "restic backup exit=$LASTEXITCODE" }
    # Transient relay retention (Mac keeps the long history): 3 days + 2 weeks.
    & $Restic forget --repo $RepoDir --keep-daily 3 --keep-weekly 2 --prune 2>&1 | ForEach-Object { Log ("restic-forget: " + $_) }
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
    exit 0
}
catch {
    Log ("ERROR: " + $_.Exception.Message)
    $bad = [ordered]@{ ok=$false; ts=(Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"); err=$_.Exception.Message }
    ($bad | ConvertTo-Json -Compress) | Set-Content -Path (Join-Path $SnapRoot "SNAPSHOT_OK") -Encoding ascii -NoNewline
    exit 1
}
