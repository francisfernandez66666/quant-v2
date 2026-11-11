# enable_remote_admin.ps1 — 一次性引导：让云端 gz 经已有 WireGuard 隧道维护本机
#
# owner 2026-11-11 需求：「实现我这边直接维护物理机的内容并迭代更新代码和操作 powershell」。
# 现状：gz wg0 10.77.0.1 ↔ 本机 10.77.0.2 握手正常，但本机对隧道没开任何管理端口，
# 云端无法远程执行。本脚本只做一件事：安装/启用 Windows 自带 OpenSSH Server，把 22 端口
# 的入站范围收紧到 WireGuard 子网，并登记云端专用公钥（其它任何来源连不上）。
#
# 一次性运行（管理员 PowerShell）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File C:\qmt\quant-trading-v2\deploy\win\enable_remote_admin.ps1
# 判读锚点（纯 ASCII，PowerShell 管道不会乱码）：
#   REMOTE_ADMIN_OK sshd=Running port=22 key=present
# 之后云端即可：ssh -i /opt/quant/keys/win_admin <user>@10.77.0.2 "powershell -Command ..."
#
# 安全边界：
#   * 只允许 10.77.0.0/24（WG 网段）连 22；不改其它防火墙规则；
#   * 只允许这一把 ed25519 公钥登录（administrators_authorized_keys，ACL 收敛到 SYSTEM+Administrators）；
#   * 不开启密码登录新通道、不动 RDP、不动 QMT 客户端；
#   * 可重复执行（幂等）：已装的 capability 不重装，公钥重复不追加。
# English: one-time bootstrap for key-only OpenSSH over the existing WireGuard subnet.

$ErrorActionPreference = 'Stop'
$PubKey = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIErtnyaHCh/bq4fezo6fLrzCeEa8/pnmVh3nbZXPR1OA quant-gz-win-admin'
$WgSubnet = '10.77.0.0/24'

# 1) 管理员校验：改 sshd/防火墙/授权文件都需要提权，静默失败比报错更危险。
# 2026-11-11 owner 实锤：[WindowsIdentity]::GetCurrent() 在个别宿主下返回 $null，
# 直接 New-Object 会得到 $null principal，IsInRole 抛 "对 Null 值表达式调用方法"。
# 改为防御式判定 + whoami /groups 兜底（Administrators SID = S-1-5-32-544）。
function Test-IsAdmin {
    try {
        $id = [Security.Principal.WindowsIdentity]::GetCurrent()
        if ($null -eq $id) { return $false }
        $pr = New-Object Security.Principal.WindowsPrincipal($id)
        if ($null -eq $pr) { return $false }
        if ($pr.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { return $true }
    } catch { }
    try {
        $g = whoami /groups 2>$null | Select-String 'S-1-5-32-544'
        if ($g -match 'Enabled') { return $true }
    } catch { }
    return $false
}
if (-not (Test-IsAdmin)) {
    Write-Output 'REMOTE_ADMIN_ABORT reason=administrator_required (请右键"以管理员身份运行" PowerShell 后重试)'
    exit 2
}

# 2) 安装 OpenSSH Server（Windows 自带可选功能，离线也可装；已装则跳过）。
$cap = Get-WindowsCapability -Online -Name 'OpenSSH.Server*' -ErrorAction SilentlyContinue | Select-Object -First 1
if ($cap -and $cap.State -ne 'Installed') {
    Add-WindowsCapability -Online -Name $cap.Name | Out-Null
}
if (-not (Get-Service sshd -ErrorAction SilentlyContinue)) {
    Write-Output 'REMOTE_ADMIN_ABORT reason=sshd_service_absent_after_install'
    exit 3
}
Set-Service sshd -StartupType Automatic
Start-Service sshd

# 3) 公钥登记（管理员账号走 administrators_authorized_keys，不是用户级 authorized_keys）。
$sshDir = 'C:\ProgramData\ssh'
$authFile = Join-Path $sshDir 'administrators_authorized_keys'
if (-not (Test-Path $sshDir)) { New-Item -ItemType Directory -Path $sshDir -Force | Out-Null }
$existing = ''
if (Test-Path $authFile) { $existing = Get-Content $authFile -Raw -ErrorAction SilentlyContinue }
if ($existing -notlike "*$PubKey*") {
    Add-Content -Path $authFile -Value $PubKey -Encoding ascii
}
# ACL：SYSTEM + Administrators，其它主体一律移除（sshd 对权限过宽的授权文件会拒绝密钥）。
icacls $authFile /inheritance:r /grant 'SYSTEM:F' /grant 'Administrators:F' | Out-Null

# 4) 防火墙：仅 WG 子网可连 22；已存在同名规则先删再建，避免叠加出一条 any-any。
netsh advfirewall firewall delete rule name='Quant-WG-SSH' | Out-Null
netsh advfirewall firewall add rule name='Quant-WG-SSH' dir=in action=allow protocol=TCP localport=22 remoteip=$WgSubnet | Out-Null

# 5) 关闭密码登录（只留公钥），并复读关键状态。
$cfg = Join-Path $sshDir 'sshd_config'
if (Test-Path $cfg) {
    $txt = Get-Content $cfg -Raw
    $txt = $txt -replace '(?m)^#?PasswordAuthentication\s+.*$', 'PasswordAuthentication no'
    $txt = $txt -replace '(?m)^#?PubkeyAuthentication\s+.*$', 'PubkeyAuthentication yes'
    Set-Content -Path $cfg -Value $txt -Encoding ascii
    Restart-Service sshd
}

$svc = (Get-Service sshd).Status
$keyOk = (Test-Path $authFile) -and ((Get-Content $authFile -Raw) -like "*$PubKey*")
Write-Output ("REMOTE_ADMIN_OK sshd=$svc port=22 key_present=$keyOk wg_subnet=$WgSubnet")
