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
#  11) §SNAP-LOCK（2026-09-23，第 18 探针）：快照单写者锁健康度（锁龄 + 按命令行统计在跑进程数）。
#  12) §QMT-MOCK-DECOM（2026-09-23 建，同日 §OPS-ALIGN 改判据，第 19 探针）：实盘机残留 UAT
#      qmt-mock 退役复核。两条独立判据：①**产物态**——C:\qmt\uat 下无 qmt-mock.exe（改名
#      .disabled-* 不算在位）且 :8799 无该目标监听；②**安全阀态**——落盘的 decommission_qmt_mock.ps1
#      确实是"缺省预览 + 显式 -Apply 才动手"。判据 ② 是本次改缺省方向后新增的，理由见探针正文注释。
#      判据明细全 ASCII：PS→SSH→bash 回传按 GBK 解码，任何中文明细的 grep 都是永久性假绿。
#  13) §QMT-TOKENROT（2026-09-23，第 20 探针）：网关 token 四源指纹分歧检测（只比 sha256 前
#      8 位，绝不回显值）。四源=①config.xt.json ②服务 AppEnvironmentExtra 的 QUANT_GATEWAY_*
#      覆盖（注册表直读）③引擎 config.json rules.qmt.token ④桥进程 --token/cmdline。
#
#  14) §FILL-AMEND（2026-09-23 夜批，第 21 探针）：勘误/守恒三条新端点路由在位且受 admin 收权——
#      未鉴权 GET /api/qmt/fill-amendments、/api/qmt/fills/conservation 必须 401，
#      且 POST /api/qmt/fill-amendments/1/apply 也必须 401（**判据按运行时真实取值链**：
#      404=二进制未更新（前端有按钮却全链路不可用）；200/403 都不能算过——200 等于把资金账写端点
#      裸奔到公网，403 说明鉴权层被跳过而只剩归属校验，同 §M-14 收权面回归形态）。
#
#  15) §SIGNAL-DIST（2026-09-24，第 22 探针 + INFO 观测通道）：当日固化信号按战法分布的读数。
#      只观测不判资金：红=文件在但解析失败/没有 signals 数组；文件缺失、日期不是今天、
#      当日零信号都是合法态（详见 PS 段口径注释）。绿时也要回显读数，故走 INFO 通道
#      （bash 侧只 echo，不进 PASS/FAIL 计数⇒判数仍是 25）。
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/verify_deploy_guangzhou.sh
#   GZ_IP=81.71.69.17 COMMIT=beb5b80 ./scripts/verify_deploy_guangzhou.sh   # 显式指定指纹
#
# 参数（环境变量）：GZ_IP（必填）/ GZ_USER / COMMIT（默认本地 HEAD）/ DEPLOY_DIR / 端口三项
#   / 第 19-20 探针可调项：MOCK_UAT_DIR / MOCK_PORT / QMT_WIN_DIR（退役 .ps1 落盘目录，第 19 探针
#   的安全阀态判据要读它）/ GW_CFG / GW_TOKEN_SVC（默认=现网路径）
set -uo pipefail

: "${GZ_IP:?请设置 GZ_IP（广州服务器公网 IP）}"
GZ_USER="${GZ_USER:-Administrator}"
APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMMIT="${COMMIT:-$(git -C "$APP_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"   # §N-5 第 15 探针：auth.json（LLM 权威源）所在
BACKUP_DIR="${BACKUP_DIR:-${DEPLOY_DIR}/deploy/qmt-win}"   # §P0-B 第 16 探针：快照脚本落盘位（= 部署步 [2e]）
SNAP_DIR="${SNAP_DIR:-C:/var/lib/quant-snapshot}"          # §P0-B 第 16 探针：每晚产物 + SNAPSHOT_OK 所在
MOCK_UAT_DIR="${MOCK_UAT_DIR:-C:/qmt/uat}"                 # §QMT-MOCK-DECOM 第 19 探针：残留 mock 目录
MOCK_PORT="${MOCK_PORT:-8799}"                             # §QMT-MOCK-DECOM 第 19 探针：假柜台对外口
# §OPS-ALIGN：第 19 探针还要复核**落盘的那份退役脚本**的安全阀方向（缺省预览还是缺省动手），
# 故需知道它现网落在哪——就是部署步上传的 ${DEPLOY_DIR}/qmt-win/（与 deploy_guangzhou.sh [3d] 同路径）。
QMT_WIN_DIR="${QMT_WIN_DIR:-${DEPLOY_DIR}/qmt-win}"        # §QMT-MOCK-DECOM 第 19 探针：运维 .ps1 落盘目录
GW_CFG="${GW_CFG:-C:/qmt/quant-trading-v2/qmt_gateway/config.xt.json}"   # §QMT-TOKENROT 第 20 探针：网关配置文件
GW_TOKEN_SVC="${GW_TOKEN_SVC:-quant}"                      # §QMT-TOKENROT 第 20 探针：QUANT_GATEWAY_TOKEN 所在 NSSM 服务
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
    [string]$SnapDir = "C:\var\lib\quant-snapshot",
    # §QMT-MOCK-DECOM 第 19 探针 + §QMT-TOKENROT 第 20 探针用（2026-09-23）。
    [string]$MockUatDir = "C:\qmt\uat",
    [int]$MockPort = 8799,
    # §OPS-ALIGN（2026-09-23）：第 19 探针的安全阀态判据读这份落盘脚本的内容（只读，不执行它）。
    [string]$MockDecomScript = "C:\opt\quant\qmt-win\decommission_qmt_mock.ps1",
    [string]$GatewayCfg = "C:\qmt\quant-trading-v2\qmt_gateway\config.xt.json",
    [string]$GwTokenService = "quant"
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

# 7b) §FILL-AMEND（2026-09-23 夜批）第 21 探针：勘误/守恒端点在位且全受鉴权收口。
#     这三条是**资金账写端点**（批准勘误会直接重算买入笔数/预算/回款/已实现盈亏），
#     现网必须与 test-attribution 同口径：未鉴权一律 401。
#     ⚠ 判据取的是"未鉴权时的第一道闸"，不是"归属校验"——adminMiddleware 挂在 authMiddleware 之后，
#     所以未带 token 的调用必然 401；若回 403 说明鉴权层被跳过、只剩归属判定（收权面回归），
#     若回 200 说明端点裸奔。404=二进制未更新，此时前端的「改判」按钮点了必然全错。
$code = HCode "GET" ("http://127.0.0.1:" + $EnginePort + "/api/qmt/fill-amendments")
Probe "engine:/api/qmt/fill-amendments unauth=401" ($code -eq "401") ("got=" + $code + "；404=二进制未更新（勘误台账不可用），200=资金账读端点裸奔")
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/qmt/fill-amendments") '{"fill_id":1,"new_side":"卖出","reason":"probe"}'
Probe "engine:POST /api/qmt/fill-amendments unauth=401" ($code -eq "401") ("got=" + $code + "；200=未鉴权即可提交勘误（写端点收权失效）")
$code = HCode "POST" ("http://127.0.0.1:" + $EnginePort + "/api/qmt/fill-amendments/1/apply") '{}'
Probe "engine:POST /api/qmt/fill-amendments/apply unauth=401" ($code -eq "401") ("got=" + $code + "；批准动作是唯一让账目变动的入口，必须 401")
$code = HCode "GET" ("http://127.0.0.1:" + $EnginePort + "/api/qmt/fills/conservation")
Probe "engine:/api/qmt/fills/conservation unauth=401" ($code -eq "401") ("got=" + $code + "；404=守恒自检未上线（勘误后无法复核差异）")

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
# 必需键集合与注册步 `register_engine_services.ps1` 的 `$envRequired["quant"]` **逐项同集**
#   （§N-5 同源锁，verify_changes.sh §67 钉相等）。09-23 08:2x 实录：注册步写完后自读回，
#   把刚写进去的 QUANT_DATA_DIR/QUANT_ADDR/HITHINK 全判成"缺键"并 `exit 1` 中断部署（[5/5] 健康
#   检查与 [6/6] 都没跑到），而本探针判绿——两侧口径不一致就是因为探针只查 1 个键、且 HITHINK
#   走了机器级兜底，掩盖了解析缺陷。所以这里扩到同集：**解析错必红、注册错也必红**，不再一侧独绿。
$envNeed = @("TZ", "QUANT_DATA_DIR", "QUANT_ADDR", "HITHINK_FINANCE_API_KEY")
# nssm.exe 的三个可能安装位（现网 = 第一个；备份任务/手工安装可能落在后两个）。
$nssmCandidates = @(
    "C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe",       # deploy_guangzhou.sh 上传位（现网）
    "C:\opt\quant\deploy\qmt-win\tools\nssm-2.24\win64\nssm.exe",# 备份任务安装位（register_backup_task.ps1）
    "C:\opt\quant\tools\nssm-2.24\win64\nssm.exe"
)
$haveKeys = @()
$envReadVia = "none"
$nssmFound = $null
foreach ($np in $nssmCandidates) { if (Test-Path $np) { $nssmFound = $np; break } }
# 口径与 register_engine_services.ps1 的 Get-ExistingEnvExtra **完全一致**（09-23 现网实录：
#   两侧都靠解析 `nssm get` 的控制台文本读 AppEnvironmentExtra——①Out-String 按 120 列折行会让
#   续行丢键、②改按 NUL 切分又更糟：nssm 写的是 UTF-16，PS 按 OEM 码页解码后每个 ASCII 字符后面
#   都跟一个 NUL，切分等于把字符逐个劈开，结果一个键都认不出，只剩机器级兜底的 HITHINK。
#   同一份坏解析在施工侧判红、在校验侧判绿，正是"校验面必须独立于施工面"要防的形态。）
# ⇒ 唯一可靠读法：直读 NSSM 落盘的注册表值（REG_MULTI_SZ 原生 string[]，无编码、无折行、无分隔歧义）。
try {
    $svcKey = Get-Item -LiteralPath "HKLM:\SYSTEM\CurrentControlSet\Services\quant" -ErrorAction Stop
    $envPairs = @($svcKey.GetValue('AppEnvironmentExtra', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames))
    $envReadVia = "registry"
} catch { $envPairs = @(); $envReadVia = "registry-unavailable" }
$envPairs = @($envPairs | Where-Object { $_ })
if ($envPairs.Count -eq 0 -and $nssmFound) {
    # 兜底才回到文本：先把"≥2 连续 NUL"当作条目边界换成换行，再清残留单 NUL（顺序颠倒即重犯 ②）。
    $raw = & $nssmFound get quant AppEnvironmentExtra 2>$null
    $txt = (($raw | ForEach-Object { [string]$_ }) -join "`n") -replace '[\u0000]{2,}', "`n"
    $txt = $txt -replace '[\u0000]', ''
    $envPairs = @($txt -split "`r?`n" | Where-Object { $_ -match '^[A-Za-z_][A-Za-z0-9_]*=' })
    if ($envPairs.Count -gt 0) { $envReadVia = "nssm-text" }
}
foreach ($kv in $envPairs) {
    $t = ("$kv").Trim()
    $i = $t.IndexOf("=")
    if ($i -gt 0) { $haveKeys += $t.Substring(0, $i) }          # 只留键名，值就地丢弃（绝不回显）
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
$envDetail = "read=" + $envReadVia + " keys=" + $haveKeys.Count + " nssm=" + $(if ($nssmFound) { "ok" } else { "not-found(只按机器级判定)" }) + " 缺项=" + ($envMissing -join ",") + "（只报键名/计数，未回显值）"
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
# 数一下"到底有几个快照进程在跑"：锁文件只看得见**认锁**的跑批，看不见旧版脚本/交互直跑留下的
# 孤儿（09-23 就是它撞掉了 restic）。命令行匹配 backup_snapshot.ps1 才算，verify_probes.ps1 自身
# 不在其列。writers>=2 = 正在互相覆盖；writers>=1 而无锁 = 有个不认锁的进程在跑（锁保护不到它）。
$writers = 0
try {
    $procList = Get-CimInstance -ClassName Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue
    foreach ($pr in $procList) {
        $cl = [string]$pr.CommandLine
        if ($cl -match 'backup_snapshot\.ps1') { $writers += 1 }
    }
} catch { $writers = -1 }
if ($writers -ge 2) { $lkMissing += ("writers=" + $writers) }
if ($writers -eq 1 -and -not (Test-Path $Lk)) { $lkMissing += "writer without lock (unguarded run)" }
Probe "backup:single-writer lock" ($lkMissing.Count -eq 0) ("lock=" + $lkTxt + " writers=" + $writers + " miss=" + ($lkMissing -join ","))

# 12) §QMT-MOCK-DECOM（2026-09-23 建，同日 §OPS-ALIGN 改判据，第 19 探针）：实盘机残留 UAT qmt-mock 退役复核。
# 背景：C:\qmt\uat\qmt-mock.exe 自 09-10 起常驻监听 0.0.0.0:8799（docs/PROGRESS.md 遗留项）。
#   退役动作在部署侧 [3d]（QMT_MOCK_DECOMMISSION=1，exe 改名 .disabled-* 不删除）；本探针是
#   校验面独立复核（同 §M7 教训：不能只信施工面自己打的"完成"）。
# ⚠ 判据为什么必须跟着改（owner 裁决 4 / §OPS-ALIGN，2026-09-23 夜批）：退役脚本
#   decommission_qmt_mock.ps1 的缺省方向已从"跑一次就动手"对齐成"缺省 dry-run、显式 -Apply 才改名"
#   （与 rotate_qmt_token.ps1 同口径）。旧探针的**隐含前提**是"部署步 [3d] 跑过一次＝退役完成"，
#   这个前提现在不成立了——一次不带 -Apply 的调用退出码照样是 0、照样打一行 done，却什么都没改。
#   如果判据继续只查"目录里没有 qmt-mock.exe"，会出现两种互相掩盖的形态：
#     (a) 施工步忘了带 -Apply → 现网 exe 仍在 → 本探针判红（这种倒不骗人，但明细必须说清根因）；
#     (b) 真正要防的是**已经退役成功之后**：exe 从此永远不在位，这条探针从此无条件绿，
#         于是"脚本被回退成缺省即动手""-Apply 门被删掉""[3d] 整步被删"这类回归**再也报不出来**——
#         一条只在故障发生前有效的探针就是假绿探针（同 §M13/§ENH-5 那族的"前提过期"形状）。
#   ⇒ 按运行时真实取值链重写成两条**互相独立、都不会过期**的判据：
#   ①**产物态**（原三条，pass=不在位）：$MockUatDir 下无**现役名** qmt-mock.exe（改名 .disabled-*
#     视为已退役，且 .disabled-* 个数进明细当"确实动过手"的证据）；无 exe 路径落在 $MockUatDir 下的
#     进程；:8799 的监听里没有属于该目录进程的。
#   ②**安全阀态**：落盘的 $MockDecomScript 必须在位，且内容确实是"缺省预览 + 显式 -Apply"——
#     认四个静态特征：param 里有 [switch]$Apply、有 `if (-not $Apply) { $DryRun = $true }` 的缺省归一
#     语句、dry-run 早退块排在 Rename-Item 改名原语**之前**、正文没有裸 Remove-Item。
#     这条查的是"这台机器上的退役脚本还会不会在无人显式确认时改生产文件名"，与 ① 一样是现网事实，
#     不会因为"早就退役完了"而失去鉴别力。
#   其它进程占用 8799（other_listeners>0）只写明细不判红——本探针守的是"mock 没了 + 安全阀在位"，
#   不是"端口空闲"。
# ⚠ 明细必须全 ASCII：PS→SSH→bash 回传按 GBK 解码，中文明细要么乱码要么让下游 grep 变
#   永久性假绿（schtasks 状态文案那次就是这么骗过守卫的，见 deploy_guangzhou.sh [6/6] 注释）。
$mockRootPrefix = $MockUatDir.TrimEnd('\') + '\'
$mockExePresent = $false
$mockDisabledN = 0
if (Test-Path -LiteralPath $MockUatDir) {
    $mockExePresent = (@(Get-ChildItem -LiteralPath $MockUatDir -Filter "qmt-mock.exe" -File -ErrorAction SilentlyContinue).Count -gt 0)
    $mockDisabledN = @(Get-ChildItem -LiteralPath $MockUatDir -Filter "qmt-mock.exe.disabled-*" -File -ErrorAction SilentlyContinue).Count
}
$mockProcN = 0; $mockListenN = 0; $otherListenN = 0
try {
    $mockPids = @{}
    foreach ($mp in @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)) {
        $mep = [string]$mp.ExecutablePath
        if ($mep -and $mep.StartsWith($mockRootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            $mockProcN += 1
            $mockPids[[int]$mp.ProcessId] = $true
        }
    }
    foreach ($ml in @(Get-NetTCPConnection -LocalPort $MockPort -State Listen -ErrorAction SilentlyContinue)) {
        if ($mockPids.ContainsKey([int]$ml.OwningProcess)) { $mockListenN += 1 } else { $otherListenN += 1 }
    }
} catch { }
# 判据②：安全阀态（只读落盘 .ps1 的**文本特征**，绝不执行它；这里也没有任何密钥/口令值可打）。
$mockGate = "unknown"
$mockGateMiss = @()
if (Test-Path -LiteralPath $MockDecomScript) {
    $mds = ""
    try { $mds = [string](Get-Content -LiteralPath $MockDecomScript -Raw -ErrorAction Stop) } catch { $mockGateMiss += "script-unreadable" }
    if ($mds) {
        $gSwitch = ($mds -match '\[switch\]\$Apply')
        $gDefault = ($mds -match 'if \(-not \$Apply\) \{ \$DryRun = \$true \}')
        $iPreview = $mds.IndexOf('if ($DryRun)')
        $iRename = $mds.IndexOf('Rename-Item')
        $gOrder = (($iPreview -ge 0) -and ($iRename -ge 0) -and ($iPreview -lt $iRename))
        $gNoDel = -not ($mds -match '(?m)^\s*Remove-Item')
        if ($gSwitch -and $gDefault -and $gOrder -and $gNoDel) {
            $mockGate = "preview-by-default"
        } else {
            $mockGate = "regressed"
            if (-not $gSwitch) { $mockGateMiss += "gate-no-apply-switch" }
            if (-not $gDefault) { $mockGateMiss += "gate-not-preview-by-default" }
            if (-not $gOrder) { $mockGateMiss += "gate-write-before-preview-exit" }
            if (-not $gNoDel) { $mockGateMiss += "gate-irreversible-delete" }
        }
    } else { $mockGate = "unreadable" }
} else { $mockGate = "absent"; $mockGateMiss += "script-missing" }
$mockMiss = @()
if ($mockExePresent) { $mockMiss += "exe-present" }
if ($mockProcN -gt 0) { $mockMiss += ("procs=" + $mockProcN) }
if ($mockListenN -gt 0) { $mockMiss += ("listeners=" + $mockListenN) }
$mockMiss += $mockGateMiss
$mockDetail = "exe=" + $(if ($mockExePresent) { "present" } else { "absent" }) + " procs=" + $mockProcN + " mock_listeners=" + $mockListenN + " other_listeners=" + $otherListenN + " disabled_copies=" + $mockDisabledN + " gate=" + $mockGate + " miss=" + $(if ($mockMiss.Count) { ($mockMiss -join ",") } else { "none" })
Probe ("qmt:mock retired (no uat exe, no :" + $MockPort + " listener, preview gate)") ($mockMiss.Count -eq 0) $mockDetail

# 13) §QMT-TOKENROT（2026-09-23 建，2026-09-24 §TOKEN-BLIND 修读法，第 20 探针）：
#     网关 token 指纹一致性（只比指纹，绝不回显值）。
# 可比对源最多五条（③b 只在"全部账号快照收敛到同一指纹"时才进来）：
#   ①网关 config.xt.json（token+report_token）；
#   ②NSSM 服务 AppEnvironmentExtra 的 QUANT_GATEWAY_TOKEN/…_REPORT_TOKEN——进程 env 覆盖文件值
#   （gateway.py :186/:199），读法=注册表直读（规矩①：绝不解析 nssm 控制台文本）；
#   ③引擎 config.json rules.qmt.token（全局兜底那一级）；③b 账号快照 auth.json configs[] 的
#     .qmt.token（§TOKEN-BLIND 补，仅在所有快照收敛到同一指纹时参与比对）；④桥进程命令行的 --token。
# 可达性口径（如实声明，防"探针只能变绿"）：各腿都经现网唯一 sanctioned 通道（管理员 SSH +
#   powershell）读取。注册表读不到（服务名不对/权限）记 unknown；④桥没在跑时该源**无从读取**，
#   记 no-bridge-proc（合法运行态，不判红）——但**一个可读源都没有**时判红（"no readable source"），
#   否则这条探针就成了摆设。env 侧未设 QUANT_GATEWAY_TOKEN 也是合法态（网关回退文件值）记 key-absent。
# 判定：所有"可读且存在"的源的 sha256 前 8 位指纹必须一致（token 腿 + report 腿分别聚合）；
#   任何两个可读源指纹不同 → 红。空态/unknown 不算分歧、但如实列进明细（明细自带每条腿的空态字）。
#
# §TOKEN-BLIND（2026-09-24）——**只改读法与自证，不改红/绿语义**（方案 docs/FIX_PLAN_20260923NIGHT.md §11）：
# 09-23 21:40 与 22:46 两次部署后复验，这条都稳定红在 `token_fp_agree=0/4 file=unknown env=missing
# engine=missing bridge=missing`，而同刻 `gw:/health` 绿 ⇒ **现网确实在用一份口令跑着，是探针读不到**。
# 四条腿的"读不到"是三件不同的事：① 被自己的解码读法弄瞎（真缺陷）；②③④ 是合法空态被混成了同一个
# `missing` 字，看起来像"三处都没配"。夜间真正可达的只有 ①，所以 `no readable source` 是**结构性必红**。
# 三步修的全是"读法/取值链/口径"：① 腿补 `-Encoding UTF8` 并把异常类型回显（下次失明可直接归因）；
# ③ 腿把"文件不在"与"字段为空"拆成两个字，并补一条**账号级**读法（现网权威 token 在 auth.json 的
#    configs[].key=quant_config_json_v1 → .qmt.token，全局 rules.qmt 只是三级优先级最后兜底，见
#    internal/config/config.go:1903 GetQMTConfigFor）；④ 顶部显式声明"本次期望几个源可读"。
#    判定式（分歧即红 / 全读不到即红 / 单源不成红）一个字没动。
$tkExpectReadable = 1   # 夜间实测：只有网关 config.xt.json 一条腿真正可读；env/引擎/桥按设计合法为空或不在跑
function TokFp([string]$v) {
    if (-not "$v") { return "" }
    try {
        $ts = [Security.Cryptography.SHA256]::Create()
        return ("sha256:" + ((@($ts.ComputeHash([Text.Encoding]::UTF8.GetBytes([string]$v))) | ForEach-Object { $_.ToString("x2") }) -join "").Substring(0, 8))
    } catch { return "sha256:err" }
}
$tk1 = "unknown"; $tk1r = ""; $tk1Why = ""
if (Test-Path $GatewayCfg) {
    try {
        # 必须显式 -Encoding UTF8：这份文件是 ensure_gateway_config.ps1 刻意以**无 BOM UTF-8**
        # 落盘的（网关 json.load 见 BOM 直接抛）。PS 5.1 的 Get-Content 缺省按 ANSI(GBK) 解，
        # 中文字段的最后一个字节会把紧随的 `"` 当 GBK 尾字节吞掉 ⇒ 字符串闭合被劈开 ⇒
        # ConvertFrom-Json 抛 ⇒ 记 unknown。同段读引擎 config.json 一直带着 -Encoding UTF8，
        # 两腿读法不一致就是这条探针 09-23 连红两晚的本体。（09-24 待现网复验确认转绿。）
        $tk1RawTxt = Get-Content -Path $GatewayCfg -Raw -Encoding UTF8
        if (-not "$tk1RawTxt".Trim()) { $tk1 = "empty-file" } else {
            $tk1j = "$tk1RawTxt" | ConvertFrom-Json
            $tk1 = TokFp([string]$tk1j.token); if (-not $tk1) { $tk1 = "empty-field" }
            $tk1r = TokFp([string]$tk1j.report_token)
        }
    } catch { $tk1 = "unknown"; $tk1Why = "(" + $_.Exception.GetType().Name + ")" }
} else { $tk1 = "no-file" }
$tk2 = "unknown"; $tk2r = ""; $tk2Why = ""
try {
    $tk2Key = Get-Item -LiteralPath ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $GwTokenService) -ErrorAction Stop
    $tk2Vals = @($tk2Key.GetValue('AppEnvironmentExtra', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames))
    $tk2Tok = ""; $tk2Rep = ""
    foreach ($kv2 in $tk2Vals) {
        $t2 = ("$kv2").Trim()
        if ($t2 -match '^QUANT_GATEWAY_TOKEN=(.+)$') { $tk2Tok = $Matches[1] }
        elseif ($t2 -match '^QUANT_GATEWAY_REPORT_TOKEN=(.+)$') { $tk2Rep = $Matches[1] }
    }
    $tk2 = TokFp $tk2Tok; if (-not $tk2) { $tk2 = "key-absent" }   # 注册表读到了、只是没设这个键（合法态，网关回退文件值）
    $tk2r = TokFp $tk2Rep
} catch { $tk2 = "unknown"; $tk2Why = "(" + $_.Exception.GetType().Name + ")" }
$tk3 = "unknown"; $tk3Why = ""
$tk3Path = $DataDir + "\config.json"
if (Test-Path $tk3Path) {
    try {
        $tk3j = Get-Content -Path $tk3Path -Raw -Encoding UTF8 | ConvertFrom-Json
        $tk3 = TokFp([string]$tk3j.rules.qmt.token); if (-not $tk3) { $tk3 = "empty-field" }
    } catch { $tk3 = "unknown"; $tk3Why = "(" + $_.Exception.GetType().Name + ")" }
} else { $tk3 = "no-file" }
# ③b 引擎侧**账号级** token（§TOKEN-BLIND 修法第 2 步）：多账号实盘起，引擎实际用的是账号快照，
# 全局 rules.qmt 只是三级优先级里的最后兜底（internal/config/config.go:1903 GetQMTConfigFor：
# 账号自身覆盖 → 运营账号覆盖 → 全局）。快照的持久层是 auth.json 的 configs[]，key
# =quant_config_json_v1，value 是整棵 Rules 的 JSON 串（token 在 .qmt.token）——所以旧读法只看
# 全局文件就把引擎腿记成 `missing` 是**前提过期**，不是配置漂移。读 auth.json 的这条路子
# 在同脚本 §N-5 探针（判 LLM 来源）已有现成实现，属复用；只算 sha256 前缀，值不进任何输出。
# 诚实边界：多账号本就允许各配各的网关与口令，所以**只有"全部快照恰好收敛到同一个指纹"时**才把它
# 当成可比对的源；两个以上不同指纹时记 multi-account(N) 参与展示、不参与判红（绝不凭空造红）。
$tkAuthPath = $DataDir + "\auth.json"
$tk3Auth = "no-file"; $tkAccFps = @()
if (Test-Path $tkAuthPath) {
    try {
        $tkAj = Get-Content -Path $tkAuthPath -Raw -Encoding UTF8 | ConvertFrom-Json
        $tk3Auth = "absent"      # 文件能读，但没有带 token 的账号快照
        foreach ($tkC in @($tkAj.configs)) {
            if ($null -eq $tkC) { continue }
            if ($tkC.key -ne 'quant_config_json_v1') { continue }
            if (-not ("$($tkC.value)").Trim()) { continue }
            try {
                $tkR = "$($tkC.value)" | ConvertFrom-Json
                $tkFp = TokFp([string]$tkR.qmt.token)
                if ($tkFp) { $tkAccFps += $tkFp; $tk3Auth = "readable" }
            } catch { $tk3Auth = "parse-error" }
        }
    } catch { $tk3Auth = "parse-error" }
}
$tk3Uniq = @($tkAccFps | Sort-Object -Unique)
$tk3b = "empty"
if ($tk3Uniq.Count -eq 1) { $tk3b = [string]$tk3Uniq[0] }
elseif ($tk3Uniq.Count -gt 1) { $tk3b = "multi-account(" + $tk3Uniq.Count + ")" }
$tk4 = "no-bridge-proc"   # 收盘后桥不在跑＝合法运行态（原记 missing 与"配了空 token"混成一个字）
try {
    foreach ($pr4 in @(Get-CimInstance -ClassName Win32_Process -ErrorAction Stop)) {
        $cl4 = [string]$pr4.CommandLine
        if ($cl4 -match 'qmt_bridge\.py') {
            if ($cl4 -match '--token[= ](\S+)') { $tk4 = TokFp $Matches[1]; if (-not $tk4) { $tk4 = "empty-field" } } else { $tk4 = "no-cmdline-token" }
            break
        }
    }
} catch { $tk4 = "unknown" }
# 可比对的源 = 真的读出指纹的那些（③b 只在单指纹时进来自证，multi-account 不进判据）。
$tkPres = @($tk1, $tk2, $tk3, $tk3b, $tk4 | Where-Object { $_ -match '^sha256:' })
$tkGroups = @($tkPres | Group-Object | Sort-Object -Property Count -Descending)
$tkAgreeN = 0
if ($tkGroups.Count -ge 1) { $tkAgreeN = [int]$tkGroups[0].Count }
$tkBad = @()
if (($tkGroups | Measure-Object).Count -gt 1) { $tkBad += ("token_diverged distinct=" + ($tkGroups | Measure-Object).Count) }
$tkRep = @(@($tk1r, $tk2r) | Where-Object { $_ -match '^sha256:' }) | Where-Object { $_ }
if (($tkRep | Sort-Object -Unique | Measure-Object).Count -gt 1) { $tkBad += "report_token_diverged" }
if ($tkPres.Count -eq 0) { $tkBad += "no readable source" }
# 明细必须能自证"为什么读不到"：每条腿的空态分开写（no-file / empty-file / empty-field /
# key-absent / no-bridge-proc / unknown(异常类型)），再给出"本次可读源数 / 期望源数"。
# 09-23 那两次红只留下一串 `missing`，分不清"探针读法瞎"还是"现网真没配"，白付一晚排查。
$tkDetail = "token_fp_agree=" + $tkAgreeN + "/" + $tkPres.Count + " readable=" + $tkPres.Count + "/expect=" + $tkExpectReadable + " file=" + $tk1 + $tk1Why + " env=" + $tk2 + $tk2Why + " engine=" + $tk3 + $tk3Why + " engineAuth=" + $tk3b + "(auth=" + $tk3Auth + ")" + " bridge=" + $tk4 + " report_file=" + $(if ($tk1r) { $tk1r } else { "none" }) + " report_env=" + $(if ($tk2r) { $tk2r } else { "none" }) + " miss=" + $(if ($tkBad.Count) { ($tkBad -join ",") } else { "none" })
Probe "qmt:token fp agree across readable sources" ($tkBad.Count -eq 0) $tkDetail

# 14) §SIGNAL-DIST（2026-09-24，第 21 探针）：当日固化信号按战法分布——§KLINE-CHAIN-3 的验收眼睛。
# 为什么要这条：09-23 那晚「白天只有龙头出信号」是**owner 用肉眼在前端看出来的**，现网 24 条探针
#   一条都没红（引擎活着、链也在跑，只是拿不到日K 的那批战法整天零产出）。也就是说这条缺陷在
#   观测面上是**隐形**的：修没修好同样没人知道。所以修完之后必须把「今天有哪些战法出了信号」变成
#   一条可复跑的读数，而不是再靠人盯。
# 读法（只读，不新增凭据）：引擎的当日固化信号落 <DataDir>\signals_today.json
#   （internal/engine/engine.go:558 + signal_store.go，键 code@strategy，跨重启恢复、交易日自动滚动）。
#   按 Recurse -Depth 2 收（dataDir 为空时该文件根本不落，属合法 no-file）。
# 口径（2026-09-24 首跑后收紧，见下"为什么按文件分行"）：战法一律取 ASCII 的归一桶——
#   Signal.StrategyType（runner 类型/规则 ID：dragon/double_bump/n_shape/dragon_return/momentum/
#   fac_N/pat_N）优先，缺失时按中文展示名查表映射，映射不到记 other（宁可少分类，不猜）。
#   **不把中文塞进明细**——本仓库实录过 PowerShell→SSH→bash 回传时中文 detail 会被 GBK 字节打乱、
#   在 grep 判据里恒不命中（＝把假绿写进探针）。
# 为什么按文件分行（首跑 2026-09-24 实测教训）：DataDir 下 Recurse 能收到 4 份 signals_today.json
#   （根目录一份 + accounts/<uid>/ 每账号一份，见 engine.go:558 的 per-acctDir 装配），
#   合并计数会把「昨日/前日的残留桶」和「另一个账号的产出」混进同一个数字里，读出来是 64 却
#   回答不了 owner 的问题（"今天"有几类战法出了信号）。故：明细逐文件回显 path/day/today/n，
#   聚合值另算一组**只统计 trading_day==今天**的数（today_signals/today_strategies/
#   today_leader_only），并回显 today_files。日期的两种历史形态（2026-08-19 与 20260819）
#   统一去掉非数字后再比，避免"格式不同⇒永远不等"的假失明。
# 判红边界（刻意收窄，防"这条只能变绿"也防"只能变红"，首跑后未改动）：
#   红：文件存在但解析失败（parse-error）／JSON 结构里没有 signals 数组（shape-error）。
#   不红：文件不存在（引擎未出信号、非交易日、dataDir 未配都是合法态）、trading_day 不是今天
#   （盘前复验必然看到昨日桶，跨日滚动发生在第一次打分轮，拿它判红＝每天清晨自找一红）、
#   signals=0（当天确实可以一个信号都没有）。这三种都如实把状态写进明细。
$sgFiles = @()
try { $sgFiles = @(Get-ChildItem -LiteralPath $DataDir -Filter 'signals_today.json' -Recurse -Depth 2 -ErrorAction SilentlyContinue) } catch { $sgFiles = @() }
# SgKey：把一条固化信号归一到 ASCII 桶名。判定顺序即优先级，dragon_return 必须排在 dragon 之前
# （子串包含关系），fac_/pat_ 规则 ID 排在最后兜底。
function SgKey($o) {
    $t = [string]$o.strategy_type
    $s = [string]$o.strategy
    if (-not $t -and -not $s) { return "unknown" }
    $u = $t + "|" + $s
    if ($u -match 'dragon_return|龙回头') { return "dragon_return" }
    if ($u -match 'double_bump|双响炮') { return "double_bump" }
    if ($u -match 'n_shape|N形') { return "n_shape" }
    if ($u -match 'momentum|动量') { return "momentum" }
    if ($u -match 'dragon|龙头') { return "dragon" }
    if ($t -match '^fac_' -or $u -match '\bfactor\b') { return "factor" }
    if ($t -match '^pat_' -or $u -match '\bpattern\b') { return "pattern" }
    return "other"
}
$sgBad = @()
$sgToday = (Get-Date).ToString("yyyyMMdd")
$sgTodayN = 0
$sgTodayFiles = 0
$sgTodayByType = @{}
foreach ($sgf in $sgFiles) {
    $sgJson = $null
    try {
        $sgTxt = Get-Content -LiteralPath $sgf.FullName -Raw -Encoding UTF8
        if ("$sgTxt".Trim()) { $sgJson = "$sgTxt" | ConvertFrom-Json }
    } catch {
        $sgBad += ("parse-error(" + $_.Exception.GetType().Name + " file=" + $sgf.Name + ")")
        continue
    }
    $rel = $sgf.FullName
    # StartsWith 默认区分大小写：DataDir 的盘符大小写与 FullName 不一致时会退化成整条绝对路径
    # （只是明细变长，不影响判据），显式走忽略大小写比较。
    if ($rel.StartsWith($DataDir, [StringComparison]::OrdinalIgnoreCase)) { $rel = $rel.Substring($DataDir.Length).TrimStart('\','/') }
    if ($null -eq $sgJson) {
        Write-Output ("INFO|signals_file " + $rel + " day=none n=0 empty")
        continue
    }
    if ($null -eq $sgJson.PSObject.Properties['signals']) {
        $sgBad += ("shape-error(no-signals-array file=" + $rel + ")")
        continue
    }
    # trading_day 是 JSON 标量，直接 [string] 转换即可。不用 `| Out-String`：那条管道在 PS 控制台
    # 按 120 列折行（§N-5 的教训即在此），本仓 §NSSMENV 负锁对整个脚本禁该写法，这里也不留例外。
    $sgDay = ([string]$sgJson.trading_day) -replace '[^0-9]', ''
    if (-not $sgDay) { $sgDay = "none" }
    $isToday = if ($sgDay -eq $sgToday) { "yes" } else { "no" }
    $sgByType = @{}
    $sgN = 0
    foreach ($sg in @($sgJson.signals)) {
        if ($null -eq $sg) { continue }
        $sgN = $sgN + 1
        $k = SgKey $sg
        if ($sgByType.ContainsKey($k)) { $sgByType[$k] = [int]$sgByType[$k] + 1 } else { $sgByType[$k] = 1 }
        if ($isToday -eq "yes") {
            if ($sgTodayByType.ContainsKey($k)) { $sgTodayByType[$k] = [int]$sgTodayByType[$k] + 1 } else { $sgTodayByType[$k] = 1 }
        }
    }
    $sgTop = ""
    foreach ($k in @($sgByType.Keys | Sort-Object)) { if ($sgTop) { $sgTop += "," }; $sgTop += ($k + ":" + $sgByType[$k]) }
    Write-Output ("INFO|signals_file " + $rel + " day=" + $sgDay + " today=" + $isToday + " n=" + $sgN +
        " kinds=" + $(if ($sgTop) { $sgTop } else { "none" }) +
        " mtime=" + $sgf.LastWriteTime.ToString("yyyy-MM-dd HH:mm"))
    if ($isToday -eq "yes") {
        $sgTodayFiles = $sgTodayFiles + 1
        $sgTodayN = $sgTodayN + $sgN
    }
}
$sgTypes = @($sgTodayByType.Keys | Sort-Object)
$sgTop2 = ""
foreach ($k in $sgTypes) { if ($sgTop2) { $sgTop2 += "," }; $sgTop2 += ($k + ":" + $sgTodayByType[$k]) }
$sgLeaderOnly = "false"
if ($sgTodayN -gt 0 -and $sgTypes.Count -eq 1 -and $sgTypes[0] -eq 'dragon') { $sgLeaderOnly = "true" }
$sgDetail = "today_signals=" + $sgTodayN + " today_strategies=" + $sgTypes.Count + " leader_only=" + $sgLeaderOnly +
    " today_files=" + $sgTodayFiles + "/" + $sgFiles.Count + " day=" + $sgToday +
    " top=" + $(if ($sgTop2) { $sgTop2 } else { "none" }) +
    " miss=" + $(if ($sgBad.Count) { ($sgBad | Sort-Object -Unique) -join "," } else { "none" })
# 这条探针的**存在理由就是读数本身**，所以绿的时候也必须把明细打出来（其余探针只在红时回显 detail）。
# 走独立的 INFO 通道：bash 侧只 echo、不进 PASS/FAIL 计数 ⇒ 红绿语义与 25 条判数都不受影响。
Write-Output ("INFO|signals_today " + $sgDetail)
Probe "engine:today pinned signals spread across strategies" ($sgBad.Count -eq 0) $sgDetail
PSEOF

# PS 5.1 无 BOM 的 UTF-8 文件按 GBK 解析——中文注释会撕裂字符串字面量直接 ParserError，
# 所以上传前补 UTF-8 BOM（deploy/qmt-win/ 各 PS1 同款约定）。
printf '\357\273\277' | cat - "$PROBES" > "$PROBES.bom" && mv "$PROBES.bom" "$PROBES"
$SCP "$PROBES" "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/verify_probes.ps1" 2>/dev/null

echo "== verify_deploy_guangzhou @ ${GZ_IP}（期望 buildCommit=${COMMIT}）=="
out=$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/verify_probes.ps1 -Commit ${COMMIT} -EnginePort ${ENGINE_PORT} -WebPort ${WEB_PORT} -GwPort ${GW_PORT} -DataDir ${DATA_DIR} -BackupDir ${BACKUP_DIR} -SnapDir ${SNAP_DIR} -MockUatDir ${MOCK_UAT_DIR} -MockPort ${MOCK_PORT} -MockDecomScript ${QMT_WIN_DIR}/decommission_qmt_mock.ps1 -GatewayCfg ${GW_CFG} -GwTokenService ${GW_TOKEN_SVC}" 2>&1 | LC_ALL=C tr -d '\r')

PASS=0
FAIL=0
while IFS= read -r line; do
  case "$line" in
    PASS\|*) PASS=$((PASS+1)); echo "  ✓ ${line#PASS|}" ;;
    FAIL\|*) FAIL=$((FAIL+1)); echo "  ✗ ${line#FAIL|}" ;;
    # INFO = 观测通道（§SIGNAL-DIST 起）：绿的时候也要把读数打出来，供盘中/盘后直接看数。
    # 只 echo，不累加 PASS/FAIL，因此不参与 `结果：N 通过 / M 失败` 的计数口径。
    INFO\|*) echo "  · ${line#INFO|}" ;;
  esac
done <<< "$out"

rm -f "$PROBES"
echo "== 结果：${PASS} 通过 / ${FAIL} 失败 =="
[ "$FAIL" -eq 0 ] || exit 1
echo "全部探针通过。"
