# bootstrap_ssh.ps1 — 广州机一次性引导：安装 OpenSSH Server + 授权 Mac 端部署密钥
# 用途：让 MacBook（opencode）能免密 SSH/SCP 到这台广州机，自动完成 QMT 安装包传输与网关部署。
# 运行方式：腾讯云控制台 → 轻量服务器 → 「一键登录」（网页终端，SYSTEM 权限）→ 整段粘贴执行。
# 执行前提：腾讯云防火墙已放行 TCP 22（来源建议限 58.246.155.23/32，家庭宽带 IP 会变，失效需更新规则）。

$ErrorActionPreference = "Continue"

# ① 安装并启动 OpenSSH Server（Win Server 2022 内置可选功能）
$cap = Get-WindowsCapability -Online -Name "OpenSSH.Server*"
if ($cap.State -ne "Installed") {
    Add-WindowsCapability -Online -Name $cap.Name
}
Set-Service sshd -StartupType Automatic
Start-Service sshd
Write-Host "[1/3] sshd 状态: $((Get-Service sshd).Status)"

# ② 默认 Shell 设为 PowerShell（远端命令走 powershell 而非 cmd）
New-ItemProperty -Path "HKLM:\SOFTWARE\OpenSSH" -Name DefaultShell `
    -Value "C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe" `
    -PropertyType String -Force | Out-Null
Write-Host "[2/3] DefaultShell = powershell.exe"

# ③ 授权部署公钥（Administrator 属管理员组 → 必须写 administrators_authorized_keys 并收紧 ACL）
$pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDUTrktGqyLvqVwtRAW6hNjzgmZRESS5NzEGpFCDiUS1 opencode@macbook-qmt-gz"
$keyFile = "C:\ProgramData\ssh\administrators_authorized_keys"
Add-Content -Path $keyFile -Value $pub
icacls $keyFile /inheritance:r /grant "SYSTEM:F" /grant "BUILTIN\Administrators:F" | Out-Null
Write-Host "[3/3] 公钥已授权"

Write-Host "`n完成。Mac 端验证: ssh -i ~/.ssh/qmt_gz Administrator@81.71.69.17"
