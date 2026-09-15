# register_backup_task.ps1 — 注册广州备份计划任务（HARDENING 件1，运维可重入）
# 职责：把 backup_snapshot.ps1 注册为每日 04:00 SYSTEM 任务（与 15:30 夜间链错峰；
#       /RL HIGHEST 保证磁盘/凭据操作不受降权影响）。重复执行安全（先删后建）。
# 用法（在服务器）：powershell -NoProfile -ExecutionPolicy Bypass -File register_backup_task.ps1
$taskName = "quant-backup-snap"
$script   = "C:\opt\quant\deploy\qmt-win\backup_snapshot.ps1"
$action   = "powershell -NoProfile -ExecutionPolicy Bypass -File $script"

schtasks /Query /TN $taskName 2>$null | Out-Null
if ($LASTEXITCODE -eq 0) { schtasks /Delete /TN $taskName /F | Out-Null }
schtasks /Create /F /TN $taskName /SC DAILY /ST 04:00 /RU SYSTEM /RL HIGHEST /TR $action
if ($LASTEXITCODE -ne 0) { throw "schtasks /Create failed exit=$LASTEXITCODE" }
schtasks /Query /TN $taskName /FO LIST
Write-Host "REGISTERED $taskName daily 04:00"
