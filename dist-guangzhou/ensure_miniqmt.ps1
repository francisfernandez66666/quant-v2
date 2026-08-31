# ensure_miniqmt.ps1 — QMT-Ensure-Running 计划任务入口（每 10 分钟，交互会话）。
# 调 qmtctl 按交易时段拉起/关闭 QMT 完整交易端 XtItClient.exe（自动登录后由 XtMiniQmt.exe 常驻）；
# -path 必须显式传完整交易端路径——qmtctl 的默认探测在多套安装时可能找错。
& 'C:\opt\quant\qmtctl.exe' ensure-miniqmt -path 'C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe' -gateway-url http://127.0.0.1:8789/health
exit $LASTEXITCODE
