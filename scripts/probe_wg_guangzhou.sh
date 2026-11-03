#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# probe_wg_guangzhou.sh — §MULTI-ACCT-B（2026-11-03）远端腿 overlay 的**只读**落位自检。
#
# 为什么要有这条（因果链，照本仓"判据从产物侧数、不按服务态数"的取向）：
#   B-α 的下行（下单/撤单/清场/急停）设计成"广州 → 朋友机器 :8789"，而朋友家宽在 CGNAT 后面、
#   广州只有公网入向 80/443 是开的 ⇒ 这条链结构上必须有一层 overlay（WireGuard 星型，UDP 51820）。
#   11-03 试图装它时装不动：官方包唯一源 download.wireguard.com 在境内被 DNS 污染
#   （广州与本机两处解析到 Facebook 段，alidns/DNSPod 两家 DoH 给同一个假答案），
#   而 GitHub 上官方仓 releases 为空（只有源码）。**结论不是"网络抖一下"，是拿不到包。**
#   于是这件事从"我装好了没"变成"值班的人能不能一句话看清 overlay 到底在不在位"——
#   本脚本就是那一句话。它同时把 批 0 欠的两条盘中读数（真实内存 / 引擎常驻）一起带回来，
#   因为决定"要不要为朋友账号在加机器"用的正是这两个数，而不是文档里拍出来的 MB。
#
# 三条现网纪律（照抄本仓已锤过的姿势）：
#   ① 判据行一律 ASCII：PS→SSH 回传按 GBK 码页打乱中文，拿中文当判据＝永久性假绿；
#      中文只进注释与人读的 INFO 尾巴，不进任何被 grep 的字符串。
#   ② **零写入**：本脚本不装包、不生成密钥、不写 conf、不动服务、不动防火墙。
#      装 WG 是 owner 点了名、单独授权才做的动作（见 docs/IMPL_MULTI_ACCOUNT_B_DETAIL_20261102.md §15.1），
#      自检顺手把"下一步该怎么走"打出来，但绝不代替那一步。门禁 §170 有负锁盯着这条。
#   ③ 私钥/钥匙一律不读不印：这里只看文件在不在、服务在不在、端口在不在听。
#
# 用法（本地 macOS，BatchMode 公钥登录，与部署脚本同一入口变量）：
#   GZ_IP=<广州公网 IP> ./scripts/probe_wg_guangzhou.sh
#   GZ_IP=... ./scripts/probe_wg_guangzhou.sh -Preview    # 只打印将执行的远端命令，一次 SSH 都不发
# 退出码：0＝overlay 已在位且 51820 在听；10＝未装/未注册（第一根远端腿起不来的那条真实原因）；
#        1＝SSH 通道本身不通（归属先分清楚，别把连不上当成没装）。
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail

: "${GZ_IP:?请设置 GZ_IP（广州服务器公网 IP，与 deploy_guangzhou.sh 同一入口变量，本脚本不猜主机地址）}"
GZ_USER="${GZ_USER:-Administrator}"
WG_EXE="${WG_EXE:-C:/Program Files/WireGuard/wireguard.exe}"
TUNNEL_DIR="${TUNNEL_DIR:-C:/ProgramData/WireGuard/Tunnels}"
TUNNEL_NAME="${TUNNEL_NAME:-gz-legs}"
LISTEN_PORT="${LISTEN_PORT:-51820}"
SSH="ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"

MODE="run"
[ "${1:-}" = "-Preview" ] && MODE="preview"

# 远端探针：全部 Get-*/Test-Path/netstat，没有任何写原语（New-Item / Set-Content / Out-File /
# msiexec / wireguard.exe /install-tunnel 一律不出现——门禁 §170 的反证会摘这条纪律，先看它红不红）。
PROBE_PS() {
cat <<'PSEOF'
$ErrorActionPreference = 'Continue'
# 1) 二进制与驱动在不在
Write-Output ('WG_EXE=' + (Test-Path 'C:\Program Files\WireGuard\wireguard.exe'))
$drv = @(Get-CimInstance Win32_SystemDriver -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'wg*' })
Write-Output ('WG_DRIVER_N=' + $drv.Count)
foreach ($d in $drv) { Write-Output ('WG_DRIVER name=' + $d.Name + ' state=' + $d.State) }
# 2) 隧道 conf 在不在（只看文件名与大小，**不读内容**：里面是私钥）
$tdir = 'C:\ProgramData\WireGuard\Tunnels'
Write-Output ('TUNNEL_DIR=' + (Test-Path $tdir))
if (Test-Path $tdir) {
  Get-ChildItem $tdir -File -ErrorAction SilentlyContinue | ForEach-Object {
    Write-Output ('TUNNEL_FILE name=' + $_.Name + ' bytes=' + $_.Length)
  }
}
# 3) 隧道服务在不在、什么态
$svc = @(Get-Service -ErrorAction SilentlyContinue | Where-Object { $_.Name -like 'WireGuardTunnel$*' })
Write-Output ('TUNNEL_SVC_N=' + $svc.Count)
foreach ($s in $svc) { Write-Output ('TUNNEL_SVC name=' + $s.Name + ' state=' + $s.Status) }
# 4) 端口在不在听（UDP 没有 LISTEN 态，判"有该端口的 socket 且归属 wireguard"）
$up = @(netstat -ano -p udp | Select-String -Pattern ':51820\b')
Write-Output ('UDP_51820_LINES=' + $up.Count)
foreach ($l in $up) { Write-Output ('UDP_LINE ' + ($l.Line -replace '\s+', ' ').Trim()) }
# 5) 官方包可达性取证（HEAD 一次，不落盘、不安装）
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
foreach ($u in @('https://download.wireguard.com/windows-client/wireguard-amd64.msi', 'https://github.com/')) {
  try {
    $r = Invoke-WebRequest -Uri $u -Method Head -UseBasicParsing -TimeoutSec 20
    Write-Output ('REACH url=' + $u + ' status=' + $r.StatusCode)
  } catch {
    Write-Output ('REACH url=' + $u + ' FAIL=' + $_.Exception.Message.Replace("`r", ' ').Replace("`n", ' ').Substring(0, [Math]::Min(90, $_.Exception.Message.Length)))
  }
}
$ips = @(Resolve-DnsName -Name download.wireguard.com -Type A -ErrorAction SilentlyContinue | ForEach-Object { $_.IPAddress })
Write-Output ('DNS_WG=' + ($ips -join '|'))
# 6) 批 0 欠的两条盘中读数：进程私有内存（WorkingSet 会低报，取 PrivateMemorySize64）
$os = Get-CimInstance Win32_OperatingSystem
$cs = Get-CimInstance Win32_ComputerSystem
# 总量走 Win32_ComputerSystem（字节）而不是 OperatingSystem.TotalPhysicalMemory：
# 后者在 PS5.1 + CIM 这条路上实测回过 0（11-03 现网第一跑撞见），拿 0 做预算分母＝判据自己撒谎。
Write-Output ('MEM_TOTAL_MB=' + [math]::Round($cs.TotalPhysicalMemory / 1MB))
Write-Output ('MEM_FREE_MB=' + [math]::Round($os.FreePhysicalMemory / 1KB))
foreach ($n in @('quant', 'quant-research', 'researchd', 'dataload', 'qmtctl', 'XtItClient')) {
  foreach ($p in @(Get-Process -Name $n -ErrorAction SilentlyContinue)) {
    Write-Output ('PROC name=' + $n + ' pid=' + $p.Id + ' priv_mb=' + [math]::Round($p.PrivateMemorySize64 / 1MB) + ' ws_mb=' + [math]::Round($p.WorkingSet64 / 1MB))
  }
}
foreach ($s in @(Get-Service -Name quant, quant-research -ErrorAction SilentlyContinue)) {
  Write-Output ('SVC name=' + $s.Name + ' state=' + $s.Status)
}
PSEOF
}

if [ "$MODE" = "preview" ]; then
  echo "==> [-Preview] 只打印将要执行的远端命令，本次不发 SSH"
  echo "    主机：${GZ_USER}@${GZ_IP}"
  echo "    远端：powershell -NoProfile -ExecutionPolicy Bypass（只读探针：Test-Path / Get-Service / Get-CimInstance / netstat / Invoke-WebRequest -Method Head）"
  echo "    判据：WG_EXE / TUNNEL_SVC_N / UDP_51820_LINES / REACH / MEM_*_MB / PROC priv_mb"
  echo "    零写入：不下载、不安装、不生成密钥、不写 conf、不动服务与防火墙"
  exit 0
fi

PS_FILE="$(mktemp /tmp/wg_probe_XXXXXX).ps1" || { echo "X mktemp 失败" >&2; exit 1; }
PROBE_PS > "$PS_FILE"
B64=$(iconv -f UTF-8 -t UTF-16LE "$PS_FILE" | base64 | tr -d '\n')
rm -f "$PS_FILE"
if [ -z "$B64" ]; then
  echo "X 探针脚本编码为空（本机 iconv/base64 不可用？）——不发一条盲命令去猜现网状态。" >&2
  exit 1
fi

# 不套引号调 $SSH：它本身就是「ssh -o ... 用户@主机」一串词，套了引号 bash 会把整串当成一个命令名
# （11-03 实跑撞到 rc=127，而 stderr 被 2>/dev/null 吃掉，看起来像"连不上"其实是本脚本写坏）。
# stderr 单独留档：判读要能分清「SSH 通道坏」与「现网真没装」，两者处置方向完全不同。
ERR_FILE="$(mktemp /tmp/wg_probe_err_XXXXXX)" || { echo "X mktemp 失败" >&2; exit 1; }
OUT=$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand $B64" </dev/null 2>"$ERR_FILE")
RC=$?
if [ $RC -ne 0 ]; then
  echo "X SSH/powershell 退出码 ${RC}：先分清是连不上还是现网没装（本脚本不把「连不上」判成「没装」）" >&2
  printf '%s\n' "$(cat "$ERR_FILE")" | head -5 | sed 's/^/   ssh: /'
  rm -f "$ERR_FILE"
  exit 1
fi
printf '%s\n' "$OUT" | grep -E '^(WG_|TUNNEL_|UDP_|REACH|DNS_WG|MEM_|PROC|SVC)' || {
  echo "X 远端回了零条判据行（EMPTY 判红，与门禁「零输出不等于通过」同一取向）" >&2
  exit 1
}

rm -f "$ERR_FILE"
EXE=$(printf '%s\n' "$OUT" | grep -m1 '^WG_EXE=' | cut -d= -f2)
SVCS=$(printf '%s\n' "$OUT" | grep -m1 '^TUNNEL_SVC_N=' | cut -d= -f2)
UDP=$(printf '%s\n' "$OUT" | grep -m1 '^UDP_51820_LINES=' | cut -d= -f2)
echo "---"
echo "WG_EXE=${EXE} TUNNEL_SVC=${SVCS} UDP_51820=${UDP}"
if [ "$EXE" = "True" ] && [ "${SVCS:-0}" -ge 1 ] && [ "${UDP:-0}" -ge 1 ]; then
  echo "VERDICT=ready（overlay 在位且 :${LISTEN_PORT} 有 socket ⇒ 可以按 RUNBOOK §3.9 装第一根远端腿）"
  exit 0
fi
echo "VERDICT=not_installed（第一根远端腿结构上起不来，因为下行打不到朋友机器）"
echo "下一步二选一（见 docs/IMPL_MULTI_ACCOUNT_B_DETAIL_20261102.md §15.1，本脚本不代做）："
echo "  A) owner 从可达网络取 wireguard-amd64.msi 落 C:\\opt\\wg\\ ⇒ 单独授权后先验 Authenticode 签名人再静默安装；"
echo "  B) 改「远端主动拉取指令」的下行形态（零新软件、CGNAT 可用），代价是延迟一拍与一套去重语义。"
exit 10
