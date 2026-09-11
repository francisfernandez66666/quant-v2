# prod_diagnose.ps1 — 生产只读诊断脚本（PowerShell 原生版）
# 背景：广州单机 Windows Server 2022（deploy_guangzhou.sh + docs/MIGRATION_GUANGZHOU_ALLINONE.md），
#       bash 版 scripts/prod_diagnose.sh 无法在 Windows 运行，本脚本等价实现。
# 目的：诊断「利空归因卖出」发版后三类症状——信号 tab 空 / 实盘持仓空 / 首页推荐空。
# 约束：只读（日志 grep、接口 curl、进程/服务查询、config 校验），不做任何修改。
# 用法（生产机 PowerShell）：
#   .\prod_diagnose.ps1                                  # 默认：数据目录 C:\var\lib\quant-trading-v2，端口 8080
#   .\prod_diagnose.ps1 -DataDir C:\var\lib\quant-trading-v2 -Port 8080 -Log C:\path\log

param(
    [string]$DataDir = "C:\var\lib\quant-trading-v2",
    [int]$Port = 8080,
    [string]$Log = ""
)

$ErrorActionPreference = "Continue"

function Header($t) { Write-Host ""; Write-Host "════ $t ════" -ForegroundColor Cyan }

Header "1. 进程 / 服务状态"
Write-Host "-- 服务状态（quant / researchd / pydata / caddy）:"
Get-Service quant, researchd, pydata, caddy -ErrorAction SilentlyContinue |
    Format-Table Name, Status, StartType -AutoSize
Write-Host "-- quant.exe / engine 相关进程:"
Get-Process | Where-Object { $_.Name -match 'quant|researchd|pydata|XtItClient' } |
    Format-Table Name, Id, StartTime -AutoSize

Header "2. 今日关键日志扫描"
# NSSM 服务日志通常写入 DataDir\logs（engine/researchd 各自重定向 stdout/stderr）
if (-not $Log) {
    $cand = Get-ChildItem (Join-Path $DataDir "logs") -File -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime -Descending
    if ($cand) { $Log = $cand[0].FullName }
}
Write-Host "日志文件: $Log"
if ($Log -and (Test-Path $Log)) {
    Write-Host "-- 2.1 自动卖出 / 成交 / news_bear（实盘是否被自动执行清理）:"
    Select-String -Path $Log -Pattern '成交回报|自动卖出|策略卖出|news_bear|利空清仓|利空减仓|利空抛售' |
        Select-Object -Last 10 | ForEach-Object { "  $($_.Line)" }
    Write-Host "-- 2.2 D1/LLM 降级（推荐个股缺分的常见根因）:"
    Select-String -Path $Log -Pattern 'LLM降级|D1 LLM 评分失败|评分失败|待重试|降级为中性占位' |
        Select-Object -Last 10 | ForEach-Object { "  $($_.Line)" }
    Write-Host "-- 2.3 panic / error:"
    Select-String -Path $Log -Pattern 'panic|ERROR|goroutine [0-9]+ \[' |
        Select-Object -Last 10 | ForEach-Object { "  $($_.Line)" }
    Write-Host "-- 2.4 主循环时间线（最近 5 条流水线/评分输出）:"
    Select-String -Path $Log -Pattern '流水线完成|D1增量|scoreCycle|产生信号' |
        Select-Object -Last 5 | ForEach-Object { "  $($_.Line)" }
} else {
    Write-Host "!! 未定位到日志文件，请用 -Log 参数指定（服务日志一般在 DataDir\logs）"
}

Header "3. 后端接口抽检（本机 127.0.0.1:$Port）"
Write-Host "-- 3.1 信号列表:"
(Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/signals" -UseBasicParsing -TimeoutSec 10 -ErrorAction SilentlyContinue).Content.Substring(0, [Math]::Min(400, (Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/signals" -UseBasicParsing -TimeoutSec 10).Content.Length)) 2>$null
Write-Host ""
if (-not ($?)) { Write-Host "   （接口请求失败：服务可能未监听 $Port）" }

Write-Host "-- 3.2 实盘持仓:"
try { (Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/real/positions" -UseBasicParsing -TimeoutSec 10).Content.Substring(0, 400) } catch { Write-Host "   请求失败: $($_.Exception.Message)" }

Write-Host "-- 3.3 D1 评分 / 推荐（首页推荐个股数据源）:"
try { (Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/d1/scores" -UseBasicParsing -TimeoutSec 10).Content.Substring(0, 400) } catch { Write-Host "   请求失败: $($_.Exception.Message)" }

Write-Host "-- 3.4 资讯（Dashboard 资讯列表）:"
try { $r = (Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/news" -UseBasicParsing -TimeoutSec 10).Content; $r.Substring(0, [Math]::Min(300, $r.Length)) } catch { Write-Host "   请求失败: $($_.Exception.Message)" }

Header "4. config 兼容性校验（rules.bear_news）"
$cfgPath = Join-Path $DataDir "config.json"
if (Test-Path $cfgPath) {
    try {
        $cfg = Get-Content $cfgPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $bn = $cfg.rules.bear_news
        if ($null -eq $bn) {
            Write-Host "rules.bear_news = <缺失>（旧配置，引擎 EnsureDefaults 会回退默认并 log 提示，属正常）"
        } else {
            Write-Host "rules.bear_news = enabled=$($bn.enabled)"
        }
        Write-Host "qmt.enabled      = $($cfg.qmt.enabled)  （false=影子模式不下单）"
    } catch {
        Write-Host "!! config.json 解析失败: $($_.Exception.Message)"
    }
} else {
    Write-Host "!! 未找到 $cfgPath"
}

Header "诊断结束"
Write-Host "把以上全部输出发回给开发侧即可定位三类症状根因。"
