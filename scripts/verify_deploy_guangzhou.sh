#!/bin/bash
# verify_deploy_guangzhou.sh — 广州部署后自动化验证（§UAT 20260915 部署加固配套）。
#
# 定位：deploy_guangzhou.sh 只做"传上去+活着"，本脚本做"传对了吗+新代码生效了吗"的全量复核，
# 任一探针失败即退出码非 0（可挂 CI / 部署后 cron / 手工复核）。
#
# 设计（2026-09-15 实录教训）：探针逻辑全部写进 verify_probes.ps1 由 scp 上传执行——
# bash→SSH→PowerShell 三层引号转义链在 `$_`/CR 尾/编码 三处都不可靠（首版全部探针误报失败）；
# bash 层只负责编排与结果计数，PS 层输出 PASS/FAIL 行。
#
# 覆盖探针（对应 2026-09-15 部署实录的手工验证清单）：
#   1) 服务状态：quant / quant-research / pydata / quant-web 四个 NSSM 服务全部 Running
#   2) 二进制指纹：quant.exe 内含期望的 git 短 SHA（buildCommit 漂移自检）
#   3) 引擎：/setup → 200/404（存在即存活）；/api/status 未鉴权 → 401（鉴权面完好）
#   4) 前端：GET :8080/ → 200（Caddy 静态资源）
#   4b) 前端指纹（§A7-B，2026-09-20 补）：被服务的 index-*.js 内必须含期望 SHA
#       —— 旧版只查首页 200，产物陈旧（先 build 后 commit）时 12/12 全绿却仍报版本不一致横幅
#   5) 网关：/health ok:true；/settlement 未鉴权 → 401（§P0-1a 新端点路由已上线）
#   6) 引擎新端点：POST /api/holdings/balance、/api/positions/execute 未鉴权 → 401
#   7) §20260922PM：POST /api/news/test-attribution 未鉴权 → 401（M-14 收权 admin 生效复核）
#   8) §N-5（2026-09-23 晚批，第 15 探针）：HITHINK_FINANCE_API_KEY 键名必须在位（它只有 env
#      这一条路）+ LLM 必须有**任一可用来源**（服务级/机器级 env 键名，或 auth.json 里设置页
#      已保存的非空 llm_api_key(s)——§UI-AUTHORITATIVE 下 env 只是 bootstrap，硬要求 env 会造
#      永久性假红）。注册脚本洗掉密钥后仍会打印"registered"，故必须在部署面独立复核
#      （只报键名/布尔，绝不报值）
#   9) §P0-B 收编（2026-09-23，第 16 探针）：夜间快照灾备链生效复核——任务 quant-backup-snap 在位
#      + 落盘脚本在位且**内容认识 live.db**（旧版只快照 trading.db，光看"任务在跑"会完全假绿）
#      + 产物 SNAPSHOT_OK.ok=true 且 dbs 同时含 trading.db/live.db（字节>0）、accounts_files>0、
#      标记新鲜度 ≤30h（任务存在但每晚失败只有时间戳能暴露）
#  10) §P0-B-HEADROOM（2026-09-23，第 17 探针）：备份护栏的**前置条件**本身要日检——C: 余量必须
#      ≥ backup_snapshot.ps1 step 0 的 8GB 护栏（同数由 verify_changes.sh §72 等值锁钉住）。
#      实录：04:00 那次护栏还过、07:1x 首跑只剩 6.9GB，余量是单调往下走的；只读 SNAPSHOT_OK 的
#      ok=false 要等人去读 err 才发现根因，本探针判红明细直接带 free/snapshot/relay/datadir 四数。
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/verify_deploy_guangzhou.sh
#   GZ_IP=81.71.69.17 COMMIT=beb5b80 ./scripts/verify_deploy_guangzhou.sh   # 显式指定指纹
#
# 参数（环境变量）：GZ_IP（必填）/ GZ_USER / COMMIT（默认本地 HEAD）/ DEPLOY_DIR / 端口三项
set -uo pipefail

: "${GZ_IP:?请设置 GZ_IP（广州服务器公网 IP）}"
GZ_USER="${GZ_USER:-Administrator}"
APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMMIT="${COMMIT:-$(git -C "$APP_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"   # §N-5 第 15 探针：auth.json（LLM 权威源）所在
BACKUP_DIR="${BACKUP_DIR:-${DEPLOY_DIR}/deploy/qmt-win}"   # §P0-B 第 16 探针：快照脚本落盘位（= 部署步 [2e]）
SNAP_DIR="${SNAP_DIR:-C:/var/lib/quant-snapshot}"          # §P0-B 第 16 探针：每晚产物 + SNAPSHOT_OK 所在
ENGINE_PORT="${ENGINE_PORT:-8081}"
WEB_PORT="${WEB_PORT:-8080}"
GW_PORT="${GW_PORT:-8789}"

SSH="ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 ${GZ_USER}@${GZ_IP}"
SCP="scp -o StrictHostKeyChecking=accept-new"

# ── 生成探针脚本（quoted heredoc：PS 的 $ 原样落盘，不经 bash 展开）──
# BSD mktemp（macOS）只认 X 在结尾的模板（2026-09-18 实录：`vd_probes_XXXX.ps1`
# 报 "File exists" 后靠 shell 容错侥幸跑通）——X 移尾、.ps1 前缀名自拼。
PROBES="$(mktemp /tmp/vd_probes_XXXXXX).ps1" || { echo "mktemp 失败"; exit 1; }
cat > "$PROBES" <<'PSEOF'
# verify_probes.ps1 — 部署后探针（由 verify_deploy_guangzhou.sh 现场生成上传执行）。
# 输出协议：每行 "PASS|探针名" 或 "FAIL|探针名|附加信息"；bash 层汇总计数定退出码。
# English: deployment probes — emit PASS/FAIL lines; the bash driver counts them.
param(
    [string]$Commit = "unknown",
    [int]$EnginePort = 8081,
    [int]$WebPort = 8080,
    [int]$GwPort = 8789,
    # §N-5 第 15 探针用：运营数据目录（auth.json 落这里，LLM 权威源＝设置页保存）。
    [string]$DataDir = "C:\var\lib\quant-trading-v2",
    # §P0-B 第 16 探针用：快照脚本落盘目录 + 每晚产物目录（与部署步 [2e] 同源）。
    [string]$BackupDir = "C:\opt\quant\deploy\qmt-win",
    [string]$SnapDir = "C:\var\lib\quant-snapshot"
)
$ErrorActionPreference = "Continue"

function Probe($name, $ok, $detail) {
    if ($ok) { Write-Output ("PASS|" + $name) } else { Write-Output ("FAIL|" + $name + "|" + $detail) }
}
# status 探针：返回 HTTP 状态码字符串；任何异常归一为 "ERR"（不打印异常栈）
function HCode($method, $url, $body) {
    try {
        if ($method -eq "POST") {
            return [string](Invoke-WebRequest -Uri $url -Method POST -ContentType "application/json" -Body $body -UseBasicParsing -TimeoutSec 10).StatusCode
        }
        return [string](Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 10).StatusCode
    } catch {
        $r = $_.Exception.Response
        if ($null -ne $r) { return [string][int]$r.StatusCode }
        return "ERR"
    }
}

# 1) NSSM 服务状态
foreach ($s in @("quant", "quant-research", "pydata", "quant-web")) {
    $st = (Get-Service $s -ErrorAction SilentlyContinue).Status
    Probe ("svc:" + $s) ($st -eq "Running") ("got=" + $st)
}

# 2) 二进制指纹（buildCommit 防漂移）
$hit = Select-String -Path "C:\opt\quant\quant.exe" -Pattern $Commit -Quiet -ErrorAction SilentlyContinue
Probe ("fingerprint:" + $Commit) ($hit -eq $true) "quant.exe 内不含期望短 SHA"

# 3) 引擎：/setup（未初始化 200 / 已初始化 404，两者皆存活）+ 鉴权面
$code = HCode "GET" ("http://127.0.0.1:" + $EnginePort + "/setup")
Probe "engine:/setup" ($code -eq "200" -or $code -eq "404") ("got=" + $code)
$code = HCode "GET" ("http://127.0.0.1:" + $EnginePort + "/api/status")
Probe "engine:/api/status unauth=401" ($code -eq "401") ("got=" + $code)

# 4) 前端（Caddy 静态）
$code = HCode "GET" ("http://127.0.0.1:" + $WebPort + "/")
Probe "web:/" ($code -eq "200") ("got=" + $code)

# 4b) §A7-B 前端指纹（2026-09-20 补）：**线上实际服务的那个 bundle 必须内嵌待部署 SHA**。
#
# 为什么单列一条探针（旧版只查首页 200，12/12 全绿也照样漏掉版本漂移横幅）：
# 用户端的「前端与服务器版本不一致」判定是 App.jsx 用 bundle 内嵌的 __BUILD_COMMIT__
# 与 /api/status 的 build_commit **严格不等即告警**。所以"前端传上去了"不等于"传的是这一版"——
# 实录：本地先 npm run build、之后才 commit，dist 内嵌指纹停在上一版，后端换新 SHA 上线，
# 横幅立刻出现，而旧探针全绿（2026-09-20 两次）。
# 这里直接在被服务的 assets 里找期望 SHA：找不到即真实漂移，与用户看到的横幅一一对应。
$webRoot = "C:\opt\quant\web"
$idxHtml = Join-Path $webRoot "index.html"
$hit = $false
$detail = ""
if (Test-Path $idxHtml) {
    $m = [regex]::Match((Get-Content $idxHtml -Raw), 'assets/index-[A-Za-z0-9_\-]+\.js')
    if ($m.Success) {
        $chunk = Join-Path $webRoot $m.Value
        if (Test-Path $chunk) {
            $hit = (Select-String -Path $chunk -Pattern $Commit -Quiet -ErrorAction SilentlyContinue) -eq $true
            $detail = "index.html->" + $m.Value + " 内" + $(if ($hit) { "含" } else { "不含" }) + " " + $Commit
        } else {
            $detail = "index.html 引用的 " + $m.Value + " 在站点根不存在"
        }
    } else {
        $detail = "index.html 里找不到 assets/index-*.js 引用"
    }
} else {
    $detail = ($webRoot + "\index.html 不存在")
}
Probe ("web:fingerprint:" + $Commit) $hit $detail

# 5) 网关：/health + §P0-1a /settlement 新端点路由
try {
    $h = (Invoke-WebRequest -Uri ("http://127.0.0.1:" + $GwPort + "/health") -UseBasicParsing -TimeoutSec 10).Content
    Probe "gw:/health" ($h -match '"ok":\s*true') ("got=" + $h.Substring(0, [Math]::Min(80, $h.Length)))
} catch { Probe "gw:/health" $false $_.Exception.Message }
$code = HCode "GET" ("http://127.0.0.1:" + $GwPort + "/settlement?date=2026-01-01")
Probe "gw:/settlement unauth=401" ($code -eq "401") ("got=" + $code + "；404=网关仍是旧代码")

# 6) 引擎新端点路由（路由存在且受保护，未鉴权一律 401；404=二进制未更新）
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/holdings/balance") '{"available_balance":1}'
Probe "engine:/api/holdings/balance unauth=401" ($code -eq "401") ("got=" + $code)
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/positions/execute") '{}'
Probe "engine:/api/positions/execute unauth=401" ($code -eq "401") ("got=" + $code)

# 7) §20260922PM 收口面探针：test-attribution 已收权 admin（未鉴权必须 401，404=旧二进制）
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/news/test-attribution") '{}'
Probe "engine:/api/news/test-attribution unauth=401" ($code -eq "401") ("got=" + $code + "；404=二进制未更新，200=收权未生效（成员越权面仍在）")

# 8) §N-5（2026-09-23 晚批）第 15 探针：HITHINK 键名必须在位 + LLM 必须有可用来源。
#
# 为什么要单列一条（注册脚本尾部已经断言过了）：register_engine_services.ps1 的
#   AppEnvironmentExtra 是整体替换语义，旧版任何一次不带密钥参数的重跑（改端口/修故障/二次部署）
#   都会静默删掉 LLM_API_KEY/LLM_API_URL/LLM_MODEL，而且**照打 "engine services registered"**。
#   修完之后注册步会自己判红，但现网态仍可能被一次手工 nssm set / 旧脚本副本洗掉——
#   所以校验面必须独立于施工面（与 §M7「verify 面要求 quant-web Running、deploy 面没人管」同族教训）。
# ⚠ 口径修正（2026-09-23 部署首跑就是这条把它自己判红的）：**LLM 三元组不得当硬 env 键要求**。
#   §UI-AUTHORITATIVE 起 LLM 权威源是设置页保存（auth.json per-account 配置项），
#   env 只是 bootstrap（internal/llmcfg/llmcfg.go 解析链 ①设置页>②env>③全局auth>④config>⑤默认）。
#   现网密钥在 ①，硬要求 ② = 一条永久性假红，还会诱使人为凑绿把密钥再抄一份进 env/密钥文件
#   （扩大泄露面）。故 LLM 判定改为「有任一可用来源」：服务级/机器级 env 键名 **或** auth.json 有非空 llm_api_key(s)。
#   HITHINK_FINANCE_API_KEY 仍是硬要求：internal/data/hithink.go:29 只认环境变量，没有第二来源。
# 硬规矩：**只看键名/只看布尔，绝不回显值**。nssm get 的输出含密钥明文，这里只截取 '=' 左侧，
#   失败信息里也只拼键名；日志/终端出现密钥明文按事故处理。
# 判定口径 = 服务级 AppEnvironmentExtra ∪ 机器级环境变量：NSSM 的 extra 是**追加**语义，
#   进程照样继承机器级 env，而现网 HITHINK_FINANCE_API_KEY 长期只存在于机器级
#   （RUNBOOK §4.1b.2「顺带核查」+ deploy/qmt-win/run_ths_backfill.ps1:10 的读取口径）。
#   只查 extra 会造出一条永久性假红，且掩盖不了真缺键——两者取并集才是「进程实际能不能拿到」。
$envNeed = @("HITHINK_FINANCE_API_KEY")
# nssm.exe 的三个可能安装位（现网 = 第一个；备份任务/手工安装可能落在后两个）。
$nssmCandidates = @(
    "C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe",       # deploy_guangzhou.sh 上传位（现网）
    "C:\opt\quant\deploy\qmt-win\tools\nssm-2.24\win64\nssm.exe",# 备份任务安装位（register_backup_task.ps1）
    "C:\opt\quant\tools\nssm-2.24\win64\nssm.exe"
)
$haveKeys = @()
$nssmFound = $null
foreach ($np in $nssmCandidates) { if (Test-Path $np) { $nssmFound = $np; break } }
if ($nssmFound) {
    $raw = & $nssmFound get quant AppEnvironmentExtra 2>$null
    foreach ($l in (("$raw" | Out-String) -split "`r?`n")) {
        $t = $l.Replace([char]0, '').Trim()
        $i = $t.IndexOf("=")
        if ($i -gt 0) { $haveKeys += $t.Substring(0, $i) }      # 只留键名，值就地丢弃
    }
}
foreach ($k in $envNeed + @("LLM_API_KEY")) {
    if ([Environment]::GetEnvironmentVariable($k, 'Machine')) { $haveKeys += $k }   # 机器级同样算拿到
}
# LLM 来源第三条路：设置页已保存（auth.json 的 configs[].key=llm_api_key/llm_api_keys 且值非空）。
# 只取布尔结果，值不进任何变量之外的地方，也不参与下面 $envDetail 的拼接。
$llmSaved = $false
$authPath = $DataDir + "\auth.json"
if (Test-Path $authPath) {
    try {
        $aj = Get-Content -Path $authPath -Raw -Encoding UTF8 | ConvertFrom-Json
        foreach ($c in @($aj.configs)) {
            if ($null -eq $c) { continue }
            if ($c.key -eq 'llm_api_key' -or $c.key -eq 'llm_api_keys') {
                if (("$($c.value)").Trim()) { $llmSaved = $true }
            }
        }
    } catch { $llmSaved = $false }
}
$envMissing = @($envNeed | Where-Object { $haveKeys -notcontains $_ })
if ($haveKeys -notcontains 'LLM_API_KEY') { if (-not $llmSaved) { $envMissing += 'LLM-source' } }
$envDetail = "nssm=" + $(if ($nssmFound) { "ok" } else { "not-found(只按机器级判定)" }) + " 缺项=" + ($envMissing -join ",") + "（只报键名/布尔，未回显值）"
Probe "engine:quant env HITHINK key + LLM source" ($envMissing.Count -eq 0) $envDetail

# 9) §P0-B 收编（2026-09-23，第 16 探针）：夜间快照灾备链在现网真的生效了吗？
#
# 为什么必须有这条：backup_snapshot.ps1 / backup_snap.py 历史上不在部署 scp 清单里（手工安装），
#   §P0-B 把备份对象扩到 live.db + accounts/ 之后，"仓库改好了 + 任务在跑 + 产物每天在出"三件事
#   同时成立却仍然只快照 trading.db——现网跑的是旧版脚本。只看"任务存在"会给出完全假绿，
#   而这条链守的是**实盘四本账的唯一灾备**（盘坏即全损、无补救）。
# 四段判据（缺项名直接写进 FAIL 明细，不必再上机二查）：
#   ① 任务 quant-backup-snap 在位；② 落盘脚本在位且**内容认识 live.db**（新版判据）；
#   ③ 产物标记 SNAPSHOT_OK.ok=true 且 dbs 同时含 trading.db/live.db 且字节数>0、accounts_files>0；
#   ④ 标记新鲜度 ≤30h（任务存在但每晚失败的形态只有靠时间戳才能暴露）。
# 全程只读，不触发备份、不碰 restic（首跑由部署侧 schtasks /Run 负责，见 RUNBOOK §2）。
$bkMissing = @()
schtasks /Query /TN "quant-backup-snap" 2>$null | Out-Null
if ($LASTEXITCODE -ne 0) { $bkMissing += "task:quant-backup-snap" }
$bkPs1 = $BackupDir + "\backup_snapshot.ps1"
$bkPy  = $BackupDir + "\backup_snap.py"
if (-not (Test-Path $bkPs1)) { $bkMissing += "script:backup_snapshot.ps1" }
if (-not (Test-Path $bkPy))  { $bkMissing += "script:backup_snap.py" }
if (Test-Path $bkPs1) {
    # 内容版本判据取运行期代码行（注释里也会提 live.db，故按 "$DbItems" 数组形态匹配）
    $bkTxt = ""
    try { $bkTxt = Get-Content -Path $bkPs1 -Raw -Encoding UTF8 } catch { $bkTxt = "" }
    if ($bkTxt -notmatch '\$DbItems\s*=\s*@\([^)]*live\.db') { $bkMissing += "script 为旧版（无 live.db 快照项）" }
}
$bkMark = $SnapDir + "\SNAPSHOT_OK"
if (-not (Test-Path $bkMark)) {
    $bkMissing += "产物:SNAPSHOT_OK 缺失（今晚 04:00 那次没跑成）"
} else {
    try {
        $bj = Get-Content -Path $bkMark -Raw -Encoding ASCII | ConvertFrom-Json
        if (-not [bool]$bj.ok) { $bkMissing += ("产物:ok=false(" + $bj.err + ")") }
        $dbs = @()
        if ($null -ne $bj.dbs) { $dbs = @($bj.dbs.PSObject.Properties.Name) }
        foreach ($need in @("trading.db", "live.db")) {
            $bytes = 0
            if ($null -ne $bj.dbs -and $null -ne $bj.dbs.$need) { $bytes = [int64]$bj.dbs.$need }
            if (($dbs -notcontains $need) -or ($bytes -le 0)) { $bkMissing += ("产物:dbs." + $need) }
        }
        if ([int64]$bj.accounts_files -le 0) { $bkMissing += "产物:accounts_files=0" }
        $markAgeH = -1.0
        try { $markAgeH = ([datetime]::Now - [datetime]::ParseExact($bj.ts, "yyyy-MM-ddTHH:mm:ss", $null)).TotalHours } catch { }
        if ($markAgeH -lt 0) { $bkMissing += "产物:ts 不可解析" }
        elseif ($markAgeH -gt 30) { $bkMissing += ("产物:标记已 " + [math]::Round($markAgeH) + "h 未更新") }
    } catch { $bkMissing += "产物:SNAPSHOT_OK 解析失败" }
}
Probe "backup:snap task+script(live.db)+artifacts" ($bkMissing.Count -eq 0) ("缺项=" + ($bkMissing -join ","))

# 10) §P0-B-HEADROOM（2026-09-23，第 17 探针）：快照链的**前置条件**磁盘余量。
#
# 为什么第 16 探针不够：它读的是 SNAPSHOT_OK，而 ok:false 的 err 文本要等人去读那一行才知道
#   根因；04:00 那次护栏还过（≥8GB），07:1x 首跑时已经只剩 6.9GB ⇒ **余量是随时间往下走的**，
#   "昨晚的失败原因"和"今晚会不会失败"不是同一件事。护栏本身是设计正确的（不足即 ok:false
#   退 1，绝不撕裂快照），但它只在备份脚本里生效——没人跑备份就没人知道盘已经不够了。
#   同族缺陷主题：降级只在动作发生时才吵 = 事实上无人值守。本探针把余量抬成部署面日检项。
# 判据与 backup_snapshot.ps1 的 step 0 护栏**同数**（8GB；由 verify_changes.sh §72 等值锁钉住，
#   改任一侧必须同改，否则探针判绿而脚本必失败）。
# 明细顺带报三个大目录的占用（SnapRoot/中转仓/数据目录），这样判红时不需要再上机找是谁吃的盘。
function DirGB($p) {
    if (-not (Test-Path $p)) { return -1.0 }
    try {
        $s = (Get-ChildItem -LiteralPath $p -Recurse -File -ErrorAction SilentlyContinue | Measure-Object -Property Length -Sum).Sum
        return [math]::Round($s / 1GB, 1)
    } catch { return -1.0 }
}
$hd = @()
$pc = Get-PSDrive -Name C
$freeGB = [math]::Round($pc.Free / 1GB, 1)
if ($pc.Free -lt 8GB) { $hd += ("C: free " + $freeGB + "GB < 8GB guard") }
$snapGB = DirGB $SnapDir
$repoGB = DirGB "C:\var\lib\quant-restic-repo"   # 与 backup_snapshot.ps1 的 $RepoDir 同一路径（无参数入口，故此处同为字面量）
$dataGB = DirGB $DataDir
Probe "backup:disk headroom (guard=8GB)" ($hd.Count -eq 0) ("free=" + $freeGB + "GB snapshot=" + $snapGB + "GB relay=" + $repoGB + "GB datadir=" + $dataGB + "GB 缺项=" + ($hd -join ","))

# 11) §SNAP-LOCK（2026-09-23，第 18 探针）：快照单写者锁的健康度。
# 为什么需要：快照产物是**固定名覆盖**，两个进程同跑会写出混合页的"看着正常"备份；拒跑保护写在
#   backup_snapshot.ps1 里（唯一看得见所有入口的地方），但保护本身也会咬人——一次崩在跑中间的
#   进程留下死锁，此后每夜都会被"age<6h 才接管"的规则挡在门外直到龄满 6h，表现为**灾备静默停更**
#   （正是本仓反复踩的那族：任务在跑、看起来正常、其实早已断）。
# 判据：锁文件不存在=空闲（绿）；存在且龄 <6h=正在跑或刚跑完（绿，明细带 pid）；龄 ≥6h=有进程死在
#   中途（红，明细直接给 pid/host/started，不必上机）。阈值 6h 与脚本 $LockMaxMin 同数，由 §73 等值锁钉。
# 明细全 ASCII：PS 的中文输出经 SSH→bash 回传会变乱码（本脚本既有 FAIL 明细已现），而锁内容本就是
#   ASCII，直接透传最稳。
$lkMissing = @()
$lkTxt = "none"
$Lk = Join-Path $SnapDir ".backup.lock"
if (Test-Path $Lk) {
    try { $lkTxt = [string](Get-Content -Path $Lk -Raw -Encoding ASCII) } catch { $lkTxt = "unreadable" }
    $lkAgeH = 99999
    try { $lkAgeH = [math]::Round(((Get-Date) - (Get-Item $Lk).LastWriteTime).TotalHours, 1) } catch { }
    if ($lkAgeH -ge 6) { $lkMissing += ("lock age " + $lkAgeH + "h >= 6h (a run died mid-way)") }
    $lkTxt = $lkTxt.Trim() + " age_h=" + $lkAgeH
}
Probe "backup:single-writer lock" ($lkMissing.Count -eq 0) ("lock=" + $lkTxt + " miss=" + ($lkMissing -join ","))
PSEOF

# PS 5.1 无 BOM 的 UTF-8 文件按 GBK 解析——中文注释会撕裂字符串字面量直接 ParserError，
# 所以上传前补 UTF-8 BOM（deploy/qmt-win/ 各 PS1 同款约定）。
printf '\357\273\277' | cat - "$PROBES" > "$PROBES.bom" && mv "$PROBES.bom" "$PROBES"
$SCP "$PROBES" "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/verify_probes.ps1" 2>/dev/null

echo "== verify_deploy_guangzhou @ ${GZ_IP}（期望 buildCommit=${COMMIT}）=="
out=$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/verify_probes.ps1 -Commit ${COMMIT} -EnginePort ${ENGINE_PORT} -WebPort ${WEB_PORT} -GwPort ${GW_PORT} -DataDir ${DATA_DIR} -BackupDir ${BACKUP_DIR} -SnapDir ${SNAP_DIR}" 2>&1 | LC_ALL=C tr -d '\r')

PASS=0
FAIL=0
while IFS= read -r line; do
  case "$line" in
    PASS\|*) PASS=$((PASS+1)); echo "  ✓ ${line#PASS|}" ;;
    FAIL\|*) FAIL=$((FAIL+1)); echo "  ✗ ${line#FAIL|}" ;;
  esac
done <<< "$out"

rm -f "$PROBES"
echo "== 结果：${PASS} 通过 / ${FAIL} 失败 =="
[ "$FAIL" -eq 0 ] || exit 1
echo "全部探针通过。"
