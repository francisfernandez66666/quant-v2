# register_backup_task.ps1 — 注册广州夜间快照计划任务（HARDENING 件1 / §P0-B，运维可重入）
# 职责：把 backup_snapshot.ps1 注册为每日 04:00 SYSTEM 任务（与 15:30 夜间研究链错峰；
#       /RL HIGHEST 保证磁盘/凭据操作不受降权影响）。重复执行安全（先删后建）。
# 用法（在服务器）：powershell -NoProfile -ExecutionPolicy Bypass -File register_backup_task.ps1
#       由部署链 [2e] 调用时传入 -ScriptPath（与 scp 落盘目录同源，避免"任务指向一份、
#       部署更新另一份"的双份脚本漂移——§M7 校验面/施工面各说各话的同族形态）。
# 口径：本文件只做"任务注册"，不做备份内容校验（产物判据见 RUNBOOK_LIVEBACKUP.md §2 与
#       verify_deploy_guangzhou.sh 的第 16 探针）。
param(
    # 快照脚本在服务器上的绝对路径；默认值与历史手工安装位置保持一致（收编进部署链后仍同源）
    [string]$ScriptPath = "C:\opt\quant\deploy\qmt-win\backup_snapshot.ps1",
    [string]$TaskName = "quant-backup-snap",
    [string]$AtTime = "04:00"
)
$ErrorActionPreference = "Stop"

# 部署链传的是 bash 风格正斜杠路径（C:/opt/quant/...），schtasks 与历史留档都是反斜杠形态；
# 统一成反斜杠，PowerShell -File 两种都能吃，但任务库里保持一致形态便于运维肉眼核对。
$script = $ScriptPath -replace '/', '\'

# 注册前先确认脚本真的在位：任务建了但文件不存在，只会在第二天 04:00 以一次静默失败暴露
# （schtasks 的 LastTaskResult 没人看 = 账本无声失去灾备，正是 §P0-B 要消灭的形态）。
if (-not (Test-Path $script)) {
    throw "backup snapshot script not found: $script (upload it first, see RUNBOOK_LIVEBACKUP.md §2)"
}

$action = "powershell -NoProfile -ExecutionPolicy Bypass -File $script"

schtasks /Query /TN $TaskName 2>$null | Out-Null
if ($LASTEXITCODE -eq 0) { schtasks /Delete /TN $TaskName /F | Out-Null }
schtasks /Create /F /TN $TaskName /SC DAILY /ST $AtTime /RU SYSTEM /RL HIGHEST /TR $action
if ($LASTEXITCODE -ne 0) { throw "schtasks /Create failed exit=$LASTEXITCODE" }
schtasks /Query /TN $TaskName /FO LIST
Write-Host "REGISTERED $TaskName daily $AtTime -> $script"
