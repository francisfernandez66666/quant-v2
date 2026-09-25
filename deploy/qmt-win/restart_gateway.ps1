# restart_gateway.ps1 — 部署步 [3b/5] 网关重启（§UAT 20260915 新增）。
# 背景：deploy 同步了网关 .py 后旧流程不重启——新代码要等 5 分钟粒度的 QMT-Gateway-Ensure
# 计划任务"碰巧"拉起才生效（2026-09-15 部署实录：/settlement 端点延迟上线）。本脚本：
# 杀掉 gateway python 进程 → ensure/watchdog 守护 3s 自动重拉（由调用方轮询 /health 确认就绪）。
# §C7-OPS（2026-09-26）：这里的"唯一重拉径"＝交互会话计划任务（任务名单源
# deploy/qmt-win/service_definitions.ps1 的 $SvcTaskGatewayEnsure，现网 QMT-Gateway-Ensure）；
# gateway_watchdog.ps1 已标记为退役旧径，不再算兜底。token 轮换后同样走本入口
# （rotate_qmt_token.ps1 §C7-OPS 4b 的重启指引即指向这里 + 该任务）。
# 注意：全文件保持 UTF-8 BOM。English: deployment step [3b] — kill the gateway python process
# so the ensure task (single path, §C7) respawns it with the freshly synced code; caller polls /health.
$gws = Get-CimInstance Win32_Process -Filter "Name='python.exe'" |
  Where-Object { $_.CommandLine -like '*qmt_gateway*gateway.py*' }
if (-not $gws) {
    Write-Host "  网关进程未在运行（ensure 任务稍后会拉起）"
    exit 0
}
foreach ($p in $gws) {
    Write-Host ("  killed gw pid=" + $p.ProcessId)
    Stop-Process -Id $p.ProcessId -Force
}
