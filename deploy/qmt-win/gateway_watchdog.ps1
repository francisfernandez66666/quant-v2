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
    # QUANT_GATEWAY_BIND 显式放行公网绑定：广州执行机设计上必须接收首尔（43.108.86.140）
    # 的下单/查询请求，新版网关会把 config 里的 0.0.0.0 安全收敛为 127.0.0.1，
    # 不设该环境变量首尔将完全失联（生产踩坑：2026-08-28 部署后首尔下单通道中断）。
    $env:QUANT_GATEWAY_BIND = "0.0.0.0:8789"
    # 允许来源 IP 白名单（首尔服务器出口 IP；为空则仅依赖 token 防护）
    $env:ALLOWED_IPS = "43.108.86.140,127.0.0.1"
    # 网关自管文件日志（gateway-<pid>.log，UTF-8 轮转）；此处不再做外部重定向——
    # 旧 *>> 方式在 Windows 产生 UTF-16 文件且句柄被假死实例长期持有，新实例日志全部丢失。
    & $py (Join-Path $gw "gateway.py") "-c" (Join-Path $gw "config.xt.json")
    Log ("gateway exited code=" + $LASTEXITCODE + " - restarting in 3s")
    # 旧式外部重定向日志只保留一代 .old 供历史排查，网关自身日志已轮转无需在此处理
    if ((Test-Path $log) -and ((Get-Item $log).Length -gt 20MB)) {
        Move-Item -Force $log ($log + ".old")
        Log "log rotated"
    }
    Start-Sleep -Seconds 3
}
