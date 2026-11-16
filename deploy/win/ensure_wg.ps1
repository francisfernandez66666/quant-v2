# ensure_wg.ps1 - second-layer watchdog for the WireGuard tunnel service.
# First layer is the service itself (WireGuardTunnel$quant_wg: auto-start at boot,
# 25s persistent keepalive self-reconnect). This task is the belt-and-braces leg:
# every 5 minutes, ping the gz side of the tunnel; if unreachable, bounce the
# service once and report. ASCII anchors only (GBK console safe).
$ok = Test-Connection 10.77.0.1 -Count 2 -Quiet
if ($ok) { Write-Host "WG_ENSURE_RESULT=OK"; exit 0 }
$svc = Get-Service -Name "WireGuardTunnel`$quant_wg" -ErrorAction SilentlyContinue
if (-not $svc) { Write-Host "WG_ENSURE_RESULT=ABORT service_absent"; exit 1 }
Restart-Service -Name "WireGuardTunnel`$quant_wg" -Force
Start-Sleep -Seconds 6
$ok2 = Test-Connection 10.77.0.1 -Count 2 -Quiet
Write-Host ("WG_ENSURE_RESULT=" + $(if ($ok2) { "RECOVERED" } else { "STILL_DOWN" }))
