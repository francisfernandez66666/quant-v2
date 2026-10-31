# prune_logs.ps1 - §RFIX-5 研究/引擎轮转日志保留任务（由计划任务 Quant-Log-Prune 每日 07:30 触发）。
# 背景：NSSM 只设单文件 10MB 轮转阈值、不清历史——现网实录 researchd-*.log 累计 702MB、
# quant_stderr 轮转 150+ 文件，C 盘仅剩 ~11.7GB，而同盘还住着 5.1GB trading.db + WAL。
# 语义：按基名分组（researchd / quant_stderr），各组仅保留最近 -Keep 个轮转文件（默认 20）；
# 当前活动文件（researchd.log / quant_stderr.log）不匹配 `-*.log` 模式，天然不会被删。
# -WhatIf 干跑只列清单不删除（验收用）。
#
# §PRUNE-EXEBAK（2026-10-31 owner 令「看看每日清理任务能不能安全扩大范围」）：新增第三组
# 「手工换过但从未回收的二进制备份」`C:\opt\quant\*.exe.bak*`。为什么只有它能加：
#   ·现网实测这族 27 个文件 / 590MB，最老的 2026-08-30、最新的 2026-09-16（≥45 天），没有任何策略管过它；
#   ·task_logs **不加**——调度器自己有 30 天轮转（internal/scheduler/worker.go §P1-12 `cleanupTaskLogs`），
#    再叠一层就是两本账（同族判例：模拟盘配置两个真源）；而且它是前端"查看日志"的读源；
#   ·accounts/*/verdicts-*.jsonl **不加**——周日 lifecycle 按 JsonlKeepDays=14 gzip 归档，职责已定；
#   ·`.pre-*` / `*.bak` 的**配置与库副本**（auth.json / config.json / live.db.bak-*）一律不加：
#    那是撤销与人工纠正的取证与回滚件（§F18 同一取向），不是垃圾。
# 三条硬护栏（缺一条这段就不该跑）：只认 C:\opt\quant 根下的 `*.exe.bak*`、每族留最近 -ExeBakKeep 个、
# 且只删落盘超过 -ExeBakMinAgeDays 天的——防止把刚做的那次回滚副本顺手删掉。
param(
    [int]$Keep = 20,
    # 第三组的两道闸：每族（quant/research/researchd/dataload/qmtctl/…）留最近几个、至少多少天才许删
    [int]$ExeBakKeep = 2,
    [int]$ExeBakMinAgeDays = 7,
    [switch]$WhatIf
)
$ErrorActionPreference = "Stop"
$groups = [ordered]@{
    "researchd"    = "C:\opt\quant\researchd-*.log"
    "quant_stderr" = "C:\opt\quant\quant_stderr-*.log"
}
foreach ($name in $groups.Keys) {
    $files = @(Get-ChildItem $groups[$name] -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
    if ($files.Count -le $Keep) {
        Write-Host "[prune] $name : $($files.Count) 个轮转文件 <= $Keep，无需清理"
        continue
    }
    foreach ($f in ($files | Select-Object -Skip $Keep)) {
        if ($WhatIf) {
            Write-Host "[prune][whatif] 将删除 $($f.FullName)"
        } else {
            Remove-Item -LiteralPath $f.FullName -Force
            Write-Host "[prune] 已删除 $($f.Name)"
        }
    }
    Write-Host "[prune] $name : $($files.Count) -> $Keep（$(if ($WhatIf) {'dry-run'} else {'已清理'})）"
}

# ── §PRUNE-EXEBAK 第三组：手工换二进制时留下的 `*.exe.bak*`（每族留最近 N 个 + 年龄闸）──
# 为什么按"族"而不是整目录一个 Keep：一次 -Apply 部署会同时留下 quant/research/researchd/
# dataload/qmtctl 五族各一份，按整目录留 N 个会把某几族的新回滚件全挤掉——留最近必须逐族算。
# 输出恒 ASCII 关键字（exe_backups / DEL / KEEP / skipped_young），与日检读数同姿势，便于事后 grep。
$exeRoot = "C:\opt\quant"
$exeBakTotal = 0
$exeBakDeleted = 0
$exeBakSkippedYoung = 0
$exeBakFreedMB = 0.0
$exeBakRoot = $null
if ([IO.Path]::GetFullPath($exeRoot).TrimEnd('\') -ne "C:\opt\quant") {
    Write-Host "[prune] exe_backups : refused (root 必须是 C:\opt\quant，护栏不许被改写)"
} else {
    $exeBakRoot = $exeRoot
}
if ($exeBakRoot) {
    $cands = @(Get-ChildItem -LiteralPath $exeBakRoot -File -Filter "*.exe.bak*" -ErrorAction SilentlyContinue)
    # 第二道形态闸：文件名必须真是 <某>.exe.bak...，且不得含 .db / .jsonl / .json（防被将来误改的 -Filter 拖进来）
    $cands = @($cands | Where-Object {
        $_.Name -match '\.exe\.bak' -and $_.Name -notmatch '\.db|\.jsonl|\.json'
    })
    $byFamily = $cands | Group-Object { ($_.Name -split '\.exe\.bak')[0] }
    foreach ($grp in $byFamily) {
        $ordered = @($grp.Group | Sort-Object LastWriteTime -Descending)
        $exeBakTotal += $ordered.Count
        if ($ordered.Count -le $ExeBakKeep) { continue }
        foreach ($f in ($ordered | Select-Object -Skip $ExeBakKeep)) {
            $ageDays = ((Get-Date) - $f.LastWriteTime).TotalDays
            if ($ageDays -lt $ExeBakMinAgeDays) {
                # 太新＝它可能就是这次部署的回滚件，宁可留着也不赌
                $exeBakSkippedYoung++
                Write-Host "[prune] exe_backups skipped_young $($f.Name) age=$([math]::Round($ageDays,1))d"
                continue
            }
            $exeBakFreedMB += ($f.Length / 1MB)
            if ($WhatIf) {
                Write-Host "[prune][whatif] exe_backups DEL $($f.FullName) age=$([math]::Round($ageDays,1))d MB=$([math]::Round($f.Length/1MB))"
            } else {
                Remove-Item -LiteralPath $f.FullName -Force
                Write-Host "[prune] exe_backups DEL $($f.Name) age=$([math]::Round($ageDays,1))d"
            }
            $exeBakDeleted++
        }
    }
    Write-Host ("[prune] exe_backups : total={0} families={1} keep_per_family={2} min_age_days={3} deleted={4} skipped_young={5} freedMB={6} mode={7}" -f `
        $exeBakTotal, @($byFamily).Count, $ExeBakKeep, $ExeBakMinAgeDays, $exeBakDeleted, $exeBakSkippedYoung, [math]::Round($exeBakFreedMB), $(if ($WhatIf) { 'dry-run' } else { 'applied' }))
}
