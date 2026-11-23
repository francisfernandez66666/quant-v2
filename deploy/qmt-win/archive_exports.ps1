# archive_exports.ps1 - §EXPORT-ARCHIVE（2026-11-23）：QMT 导出目录按日归档。
#
# 为什么需要它（现网实测，不是推测）：
#   QMT 客户端每分钟写一份 Order-*.csv / Account-*.csv，按日累积、**从不清理**。
#   D:\qmt 树内实测 Stock 51,603 / HGT 51,870 / SGT 51,872（合计约 15.5 万文件），
#   上行腿单轮遍历要 494 秒（比计划任务的 60 秒周期长一个数量级），
#   于是每两轮被 IgnoreNew 拒一次，有效节拍退化成 8~25 分钟。
#
# 语义（三条护栏，缺一条这段就不该跑）：
#   1. 只认硬编码根 D:\qmt —— 参数可以传，但与硬编码根不逐字相等就拒跑；
#   2. 只搬「文件名能解析出 YYYYMMDD 且日期早于近端窗口」的文件；近端窗口缺省
#      = 今天 + 昨天（-KeepActiveDays 1），因为上行腿的候选窗口就是
#      uplink_export_max_age_days=2（今天+昨天）：**昨天的导出必须留在原地**，
#      否则 07:10 归档跑完，08:50 盘前那轮就没有可读的昨收快照；
#      解析不出日期的文件也不碰（证明不了它旧，就留在原地 —— fail-closed）；
#   3. 归档根必须在 D:\qmt **之外**（否则下一轮发现层还会把归档目录遍历一遍），
#      归档里超过 -KeepDays 天的整日目录删除。
#
# 只搬导出 CSV：策略文件、状态账本、配置与库一律不在本脚本的移动面里。
# 输出恒 ASCII 关键字，便于云端 grep：ARCHIVE_RESULT=OK moved=N freed_mb=X kept_days=3
#
# 用法：
#   .\archive_exports.ps1                 # 真归档
#   .\archive_exports.ps1 -WhatIf         # 只列清单，零写入

param(
    [string]$Root = "D:\qmt",
    [string]$ArchiveRoot = "D:\qmt-archive",
    [int]$KeepDays = 3,
    [int]$KeepActiveDays = 1,
    [switch]$WhatIf
)

$ErrorActionPreference = "Stop"

# ── 护栏一：根必须是硬编码的那一个（防止被参数改成 C:\ 之类） ──
$FixedRoot = "D:\qmt"
$rootFull = [IO.Path]::GetFullPath($Root).TrimEnd('\')
if ($rootFull -ne $FixedRoot) {
    Write-Host ("ARCHIVE_RESULT=ABORT reason=root_mismatch root={0}" -f $rootFull)
    exit 2
}

# ── 护栏二：归档根必须在 D:\qmt 之外 ──
$archFull = [IO.Path]::GetFullPath($ArchiveRoot).TrimEnd('\')
if ($archFull -eq $rootFull -or $archFull.StartsWith($FixedRoot + '\')) {
    Write-Host ("ARCHIVE_RESULT=ABORT reason=archive_inside_root archive={0}" -f $archFull)
    exit 2
}

$today = (Get-Date).Date
# 近端窗口：今天 + $KeepActiveDays 天（缺省 1 ⇒ 今天与昨天都留在原地）。
# 与 broker_reads._recent_cutoff(today, uplink_export_max_age_days=2) 同尺：
# 归档搬走的必须是上行腿再也不会读的文件。
$keepActive = [math]::Max(1, $KeepActiveDays)
$keepFrom = $today.AddDays(-1 * $keepActive)
$moved = 0
$freed = 0.0
$skippedToday = 0
$skippedUndated = 0

# 文件名里的 YYYYMMDD（与 broker_reads._EXPORT_DATE_PAT 同形，两侧对"哪天"必须一致）
$datePat = [regex]'(20\d{2})(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])'

$files = @(Get-ChildItem -LiteralPath $rootFull -Recurse -File -ErrorAction SilentlyContinue)
foreach ($f in $files) {
    $m = $datePat.Match($f.Name)
    if (-not $m.Success) {
        # 解析不出日期：不搬（fail-closed）。把它当旧文件搬走会让一份改了名的
        # 今日导出整天读不到，那比多留几个文件贵得多。
        $skippedUndated++
        continue
    }
    $dayText = $m.Groups[1].Value + $m.Groups[2].Value + $m.Groups[3].Value
    $day = [datetime]::MinValue
    $ok = [datetime]::TryParseExact($dayText, 'yyyyMMdd',
        [Globalization.CultureInfo]::InvariantCulture,
        [Globalization.DateTimeStyles]::None, [ref]$day)
    if (-not $ok) { $skippedUndated++; continue }
    if ($day -ge $keepFrom) {
        # 近端窗口内的文件一个都不碰：上行腿正读着它们（今天）或盘前还要读（昨天）。
        $skippedToday++
        continue
    }
    # 保留相对路径，避免 Stock/HGT/SGT 同名导出互相覆盖。
    $rel = $f.FullName.Substring($rootFull.Length).TrimStart('\')
    $relDir = Split-Path $rel -Parent
    $destBase = Join-Path $archFull $dayText
    if ($relDir) { $destDir = Join-Path $destBase $relDir } else { $destDir = $destBase }
    if ($WhatIf) {
        Write-Host ("ARCHIVE_WHATIF src={0} dest={1}" -f $f.FullName, $destDir)
        continue
    }
    if (-not (Test-Path -LiteralPath $destDir)) {
        New-Item -ItemType Directory -Path $destDir -Force | Out-Null
    }
    $freed += ($f.Length / 1MB)
    Move-Item -LiteralPath $f.FullName -Destination $destDir -Force
    $moved++
}

# ── 归档保留：超过 -KeepDays 天的整日目录删除（只认 yyyyMMdd 目录名） ──
$purgedDirs = 0
if (Test-Path -LiteralPath $archFull) {
    foreach ($d in @(Get-ChildItem -LiteralPath $archFull -Directory -ErrorAction SilentlyContinue)) {
        $dDay = [datetime]::MinValue
        $okDir = [datetime]::TryParseExact($d.Name, 'yyyyMMdd',
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::None, [ref]$dDay)
        if (-not $okDir) { continue }
        $ageDays = [int](($today - $dDay).TotalDays)
        if ($ageDays -le $KeepDays) { continue }
        if ($WhatIf) {
            Write-Host ("ARCHIVE_WHATIF purge={0} age_days={1}" -f $d.FullName, $ageDays)
            continue
        }
        Remove-Item -LiteralPath $d.FullName -Recurse -Force
        $purgedDirs++
    }
}

Write-Host ("ARCHIVE_RESULT=OK moved={0} freed_mb={1} kept_days={2} keep_active_days={3} purged_dirs={4} skipped_recent={5} skipped_undated={6} mode={7}" -f `
    $moved, [math]::Round($freed, 1), $KeepDays, $keepActive, $purgedDirs, $skippedToday, $skippedUndated, $(if ($WhatIf) { 'whatif' } else { 'applied' }))
exit 0
