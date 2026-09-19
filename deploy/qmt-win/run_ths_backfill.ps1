# run_ths_backfill.ps1 - §ENH-A 生产一次性三池历史回填包装脚本（2026-09-20）。
# 语义：从机器级注册表读 HITHINK_FINANCE_API_KEY（不经本机、不回显）注入进程环境，
# 调 dataload.exe ths-backfill 逐交易日回填 涨停/跌停/炸板 三池（已收录日自动跳过，
# 幂等可重跑）。输出全量落 ths_backfill_20260920.log 供轮询与审计。
# 承载方式：一次性计划任务 Quant-THS-Backfill（RUNBOOK 实证 ssh 内直接拉起会随会话被杀）。
# 输出捕获用 cmd /c 重定向而非 PS 管道：PS 5.1 在 $ErrorActionPreference='Stop' 下，
# 原生命令任何 stderr 行都会被转成 NativeCommandError 直接终止脚本（09-20 首跑实录：
# 任务退出码 1、日志 0 字节——dataload 的进度日志走 stderr 全被吞）。子进程继承
# $env:HITHINK_FINANCE_API_KEY，密钥仍只留在服务器侧。
$key = [Environment]::GetEnvironmentVariable('HITHINK_FINANCE_API_KEY', 'Machine')
if (-not $key) { '[ths-backfill] 机器注册表无 HITHINK_FINANCE_API_KEY，中止' | Out-File C:\opt\quant\ths_backfill_20260920.log -Append -Encoding utf8; exit 1 }
$env:HITHINK_FINANCE_API_KEY = $key
cmd /c "C:\opt\quant\dataload.exe -db C:\var\lib\quant-trading-v2\trading.db ths-backfill --start 20250801 --end 20260919 >> C:\opt\quant\ths_backfill_20260920.log 2>&1"
"[ths-backfill] 进程退出码 $LASTEXITCODE" | Out-File C:\opt\quant\ths_backfill_20260920.log -Append -Encoding utf8
exit $LASTEXITCODE
