# prune_logs.ps1 - §RFIX-5 研究/引擎轮转日志保留任务（由计划任务 Quant-Log-Prune 每日 07:30 触发）。
# 背景：NSSM 只设单文件 10MB 轮转阈值、不清历史——现网实录 researchd-*.log 累计 702MB、
# quant_stderr 轮转 150+ 文件，C 盘仅剩 ~11.7GB，而同盘还住着 5.1GB trading.db + WAL。
# 语义：按基名分组（researchd / quant_stderr），各组仅保留最近 -Keep 个轮转文件（默认 20）；
# 当前活动文件（researchd.log / quant_stderr.log）不匹配 `-*.log` 模式，天然不会被删。
# -WhatIf 干跑只列清单不删除（验收用）。
param(
    [int]$Keep = 20,
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
