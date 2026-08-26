# setup_windows.ps1 — qmt_gateway 部署辅助脚本（AUTO_TRADING_PLAN M2，广州腾讯云 Win Server 2022）
# 职责：环境自检（Python 64 位 / xtquant 可导入）→ 自动探测 QMT 安装目录与 userdata_mini →
#       生成正式 config.xt.json（强随机 token，report_url 指向首尔生产）→ 可选 mock 本机自测。
# 用法（普通或管理员 PowerShell 均可）：
#   powershell -ExecutionPolicy Bypass -File setup_windows.ps1                        # 交互式
#   powershell -ExecutionPolicy Bypass -File setup_windows.ps1 -Account 8800661234 -SelfTest
# 说明：token 生成后要同步到首尔侧 rules.qmt.token（三处一致：token / report_token / 首尔账号配置）。
param(
    [string]$Account = "",         # 东莞证券资金账号（留空则交互输入）
    [string]$Token = "",           # 网关 Bearer token（留空则自动生成 48 位 hex）
    [string]$XtPath = "",          # QMT 的 userdata_mini 目录（留空自动探测；mock 模式可不填）
    [string]$GatewayDir = "",      # qmt_gateway 目录（留空取本仓库相对路径）
    [switch]$SelfTest,             # 生成配置后在本机 127.0.0.1:18789 起 mock 网关做一次 /health 自检
    [switch]$Force                 # 目标 config.xt.json 已存在时允许覆盖
)
$ErrorActionPreference = "Stop"

function Info($m) { Write-Host "[setup] $m" -ForegroundColor Cyan }
function Ok($m)   { Write-Host "[ ok ] $m" -ForegroundColor Green }
function Warn($m) { Write-Host "[warn] $m" -ForegroundColor Yellow }
function Die($m)  { Write-Host "[fail] $m" -ForegroundColor Red; exit 1 }

# ---- 0. 定位 qmt_gateway 目录 ----
if (-not $GatewayDir) {
    $GatewayDir = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) "qmt_gateway"
}
if (-not (Test-Path (Join-Path $GatewayDir "gateway.py"))) {
    Die "在 $GatewayDir 未找到 gateway.py —— 请用 -GatewayDir 指定仓库内 qmt_gateway 目录"
}
Info "网关目录: $GatewayDir"

# ---- 1. Python 自检：存在且 64 位 ----
$pyCmd = Get-Command python -ErrorAction SilentlyContinue
if (-not $pyCmd) { Die "python 不在 PATH —— 先装 64 位 Python 3.10+（勾选 Add to PATH）后重开终端再跑" }
$py = $pyCmd.Source
$bits = (& $py -c "import struct;print(struct.calcsize('P')*8)")
if ("$bits" -ne "64") { Die "当前 Python 是 $($bits) 位 —— xtquant 仅支持 64 位，请改装 64 位 Python" }
$ver = (& $py -c "import sys;print('%d.%d.%d' % sys.version_info[:3])")
Ok "Python $ver (64-bit): $py"

# ---- 2. xtquant 检查 / 从 QMT 安装目录自动复制 ----
function Test-Xtquant {
    & $py -c "from xtquant import xttrader" 2>$null | Out-Null
    return ($LASTEXITCODE -eq 0)
}
if (-not (Test-Xtquant)) {
    Info "xtquant 不可导入，开始在 C:/D:/E: 盘根目录探测 QMT 安装目录 ..."
    $drives = @("C:", "D:", "E:") | Where-Object { Test-Path $_ }
    $qmtDirs = @()
    foreach ($d in $drives) {
        $qmtDirs += Get-ChildItem "$d\" -Directory -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -match "QMT|迅投|东莞证券|国金" }
    }
    $src = $null
    foreach ($dir in $qmtDirs) {
        foreach ($rel in @("bin.x64\Lib\site-packages\xtquant", "bin\Lib\site-packages\xtquant")) {
            $p = Join-Path $dir.FullName $rel
            if (Test-Path $p) { $src = $p; break }
        }
        if ($src) { break }
    }
    if (-not $src) {
        Die "未找到 xtquant。请确认 QMT 客户端已安装；或手动把 <QMT安装目录>\bin.x64\Lib\site-packages\xtquant 复制到 Python 的 site-packages 后重跑本脚本"
    }
    $sp = (& $py -c "import sysconfig;print(sysconfig.get_paths()['purelib'])").Trim()
    Info "发现 xtquant: $src"
    Info "复制到 Python site-packages: $sp"
    Copy-Item $src (Join-Path $sp "xtquant") -Recurse -Force
    if (-not (Test-Xtquant)) { Die "复制后仍无法导入 xtquant —— 排查 Python 位数、杀毒软件拦截" }
}
Ok "xtquant 导入正常"

# ---- 3. 探测 userdata_mini（xt 通道连接路径；mock 模式不依赖）----
if (-not $XtPath) {
    foreach ($d in (@("C:", "D:", "E:") | Where-Object { Test-Path $_ })) {
        $hit = Get-ChildItem "$d\" -Directory -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -match "QMT|迅投|东莞证券|国金" } |
            ForEach-Object { Join-Path $_.FullName "userdata_mini" } |
            Where-Object { Test-Path $_ } |
            Select-Object -First 1
        if ($hit) { $XtPath = $hit; break }
    }
    if ($XtPath) { Ok "userdata_mini: $XtPath" }
    else { Warn "未自动找到 userdata_mini（mock 联调不需要；后续切 broker=xt 前必须手工回填）"; $XtPath = "" }
}

# ---- 4. token / 资金账号 ----
if (-not $Token) {
    $rng = New-Object System.Security.Cryptography.RNGCryptoServiceProvider
    $buf = New-Object byte[] 24
    $rng.GetBytes($buf)
    $Token = (($buf | ForEach-Object { $_.ToString("x2") }) -join "")
    Ok "已生成随机 token: $Token"
}
if (-not $Account) { $Account = Read-Host "请输入东莞证券资金账号" }
if (-not $Account) { Die "资金账号不能为空" }

# ---- 5. 生成 config.xt.json（无 BOM UTF-8，兼容网关 json.load）----
$cfgPath = Join-Path $GatewayDir "config.xt.json"
if ((Test-Path $cfgPath) -and -not $Force) {
    Die "$cfgPath 已存在 —— 如确认覆盖请加 -Force 参数"
}
$cfg = [ordered]@{
    listen         = "0.0.0.0:8789"
    token          = $Token
    broker         = "mock"                       # 联调通过后手工改成 "xt"
    account        = $Account
    user_id        = ""                           # 回报归属由 token 解析（§GAP2-W1），无需填写
    db             = "data.db"
    report_url     = "https://quant-trading.top"  # 首尔生产（Caddy 反代 /api）
    report_token   = $Token                       # 必须与首尔侧 rules.qmt.token 一致
    reconcile_sec  = 60
    max_positions  = 10
    seed           = @()
    xt_path        = $XtPath
    session_id     = 1
    reconnect_sec  = 5
}
$json = $cfg | ConvertTo-Json
[System.IO.File]::WriteAllText($cfgPath, $json)
Ok "已生成 $cfgPath"

Write-Host ""
Write-Host "==============================================================" -ForegroundColor Magenta
Info "请记录 token（三处一致）：config.xt.json 的 token/report_token = 首尔侧 rules.qmt.token"
Write-Host "    $Token" -ForegroundColor Magenta
Write-Host "==============================================================" -ForegroundColor Magenta
Write-Host ""

# ---- 6. 可选：本机 mock 自测（临时监听 127.0.0.1:18789，不影响正式 8789）----
if ($SelfTest) {
    Info "mock 自测：临时启动网关于 127.0.0.1:18789 ..."
    $gwPy = Join-Path $GatewayDir "gateway.py"
    $proc = Start-Process -FilePath $py `
        -ArgumentList "`"$gwPy`" -c `"$cfgPath`" --listen 127.0.0.1:18789" `
        -PassThru -WindowStyle Hidden
    Start-Sleep -Seconds 3
    try {
        $h = Invoke-RestMethod "http://127.0.0.1:18789/health" -TimeoutSec 5
        if ($h.ok -and $h.broker_connected) {
            Ok "自测通过：/health ok=True broker=$($h.broker) broker_connected=$($h.broker_connected)"
        } else {
            Warn "自测响应异常: $($h | ConvertTo-Json -Compress)"
        }
    } catch {
        Warn "自测请求失败: $($_.Exception.Message)（检查端口占用或防火墙）"
    } finally {
        if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force }
    }
}

Info "完成。下一步按 CHECKLIST.md 阶段 B(6)→C→D 继续。"
