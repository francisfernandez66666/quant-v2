# setup_wg_service.ps1 - rebuild the box-side WireGuard tunnel as an auto-start service.
# 2026-11-16: the tunnel died 16h earlier and nothing on the box could restart it
# (no tunnel service, no conf on disk). This script regenerates the keypair locally
# (the private key never leaves this box), installs the tunnel as the service
# WireGuardTunnel$quant_wg (auto-start at boot, self-reconnecting with a 25s
# persistent keepalive) and prints the NEW public key so the gz peer can be updated.
$ErrorActionPreference = "Stop"
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { Write-Host "WG_SETUP_ABORT reason=not_admin"; exit 1 }
$wg = "C:\Program Files\WireGuard"
if (-not (Test-Path "$wg\wg.exe")) { Write-Host "WG_SETUP_ABORT reason=no_wireguard"; exit 1 }
$gzPub = "e9aMqqtCNA3S0nYnClbFTcbnVEuDjpJAWSaIezXRtng="
$endpoint = "81.71.69.17:51820"

$priv = (& "$wg\wg.exe" genkey).Trim()
$pub = $priv | & "$wg\wg.exe" pubkey
$conf = "[Interface]`nPrivateKey = $priv`nAddress = 10.77.0.2/32`n`n[Peer]`nPublicKey = $gzPub`nEndpoint = $endpoint`nAllowedIPs = 10.77.0.1/32`nPersistentKeepalive = 25`n"
$confPath = "$wg\quant_wg.conf"
[System.IO.File]::WriteAllText($confPath, $conf)

$existing = Get-Service -Name "WireGuardTunnel`$quant_wg" -ErrorAction SilentlyContinue
if ($existing) {
    & "$wg\wireguard.exe" /uninstalltunnelservice quant_wg | Out-Null
    Start-Sleep -Seconds 2
}
& "$wg\wireguard.exe" /installtunnelservice $confPath | Out-Null
Start-Sleep -Seconds 6

$svc = Get-Service -Name "WireGuardTunnel`$quant_wg" -ErrorAction SilentlyContinue
Write-Host ("WG_SERVICE status=" + $(if ($svc) { $svc.Status } else { "ABSENT" }))
& "$wg\wg.exe" show
$ok = Test-Connection 10.77.0.1 -Count 2 -Quiet
Write-Host ("WG_PING_GZ=" + $ok)
Write-Host "NEW_PUBKEY=$pub"
if ($ok -and $svc -and $svc.Status -eq "Running") {
    Remove-Item $confPath -Force
    Write-Host "WG_SETUP_RESULT=OK conf_source_removed=1"
} else {
    Write-Host "WG_SETUP_RESULT=PARTIAL conf_kept=$confPath"
}
