# gateway_watchdog.ps1 — qmt_gateway 守护循环（§通信健壮性加固）
# 职责：网关进程崩溃后 3 秒自动重启；全部输出追加到 gateway.log（超 20MB 轮转为 .old）。
# 安装为 SYSTEM 计划任务（独立会话，不随 RDP/SSH 断开而终止，开机自启）：
#   schtasks /Create /F /TN qmt-gateway-wd /SC ONSTART /RU SYSTEM /RL HIGHEST ^
#     /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\qmt\gateway_watchdog.ps1"
#   schtasks /Run /TN qmt-gateway-wd
# 说明：全文件保持 UTF-8 BOM——Windows PowerShell 5.1 无 BOM 时按 GBK 读取中文会乱码。
$ErrorActionPreference = "Continue"
$gw  = "C:\qmt\quant-trading-v2\qmt_gateway"
$log = Join-Path $gw "gateway.log"
$py  = "C:\Python312\python.exe"

function Log($m) {
    Add-Content -Path $log -Value ("[watchdog {0}] {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $m)
}

Log "watchdog started (pid=$PID)"
while ($true) {
    Log "launching gateway..."
    # 前台运行网关：进程退出（崩溃/异常）即落到下一行进入重启流程
    & $py (Join-Path $gw "gateway.py") "-c" (Join-Path $gw "config.xt.json") *>> $log
    Log ("gateway exited code=" + $LASTEXITCODE + " - restarting in 3s")
    # 日志轮转：仅保留一代 .old，活动日志上限约 20MB
    if ((Test-Path $log) -and ((Get-Item $log).Length -gt 20MB)) {
        Move-Item -Force $log ($log + ".old")
        Log "log rotated"
    }
    Start-Sleep -Seconds 3
}
