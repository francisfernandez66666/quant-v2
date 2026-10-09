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
#      （bash 侧只 echo，不进 PASS/FAIL 计数⇒判数仍是 26）。
#
#  16) §C7-OPS（2026-09-26，第 26 探针）：Windows 服务定义单源文件 service_definitions.ps1 在
#      现网落盘复核——①在位可读；②内容含三张单源表（NSSM 服务名表/任务名表/nssm 解析器），
#      防空文件、旧回退副本、scp 截断半份这类"在位但不管用"的假绿（§ENH-5 教训）。
#
#  17) §CAL-READOUT（2026-09-26，第 27 探针，owner 令"现网体检加一条日历已加载只读读数"）：
#      交易日历加载态的磁盘侧只读读数——量规 trading_calendar_loaded 藏在鉴权后的 /api/metrics，
#      三个正规脚本都不消费它（观察项 #58 的"读数通道缺口"就是这么来的），本探针用日志/缓存两条
#      免凭据腿补上（判据与读法细节见 PS 段注释；首跑以现网实际读数校准，不预设现网是哪种形态）。
#
#  18) §A5-CURRENT（2026-09-26 深夜批，第 28 探针，判数 27→28）：跌停追卖常开闸的现网实际生效值
#      只读读数——owner 裁决「limit_down_block_sell 常开」的部署前提是云端 config.json 不留显式
#      false，此前该值无免凭据取证通道（排摸脚本按设计不读 config.json，配置端点要管理令牌）。
#      生效路径结构化读数（rules.qmt.risk_gate）+ 全文裸扫第二腿（键位漂移只点名不判红，Go 不
#      消费死键）；不对称判据：红=生效路径显式 false，其余态全绿并走 INFO 回显。细节见 PS 段。
#
#  19) §0929OPS-⑪-1（2026-09-29，第 29 探针，判数 28→29）：运维面五个执行体在位 + 内容标记复核
#      （register_service / all_service_watchdog / daily_ops_check / enable_ensure / outbox_admin）。
#      共同点：告警与日检文案指示人去跑它们，却长期不在部署清单里＝靠手工拷贝。
#      §ENH-5/§P0-B/§0927KA 三条判例同族根因＝「仓库里有」被当成「现网在跑」；本批入清单后
#      由这条独立复核落盘，判据两条：文件在位 + 内容认识各自的标记（0 字节半份、旧回退副本不绿）。
#  20) §0929SCALE-⑩（2026-09-29，第 30 探针，判数 29→30）：daily.amount 量纲只读抽检，走
#      dataload.exe amount-check（纯 SELECT 零写入、免凭据，与第 15/27 探针同一条只读通道）。
#      红＝千元/双重换算/混源/读取失败；库里没数不判红（表未装由新鲜度腿负责，重复判红会把
#      「还没装」冒充成「量纲错了」）。中位均价读数恒走 INFO，绿也要看得到数。
#  21) §0929SECKEY-A（2026-09-29，第 31 探针，判数 30→31）：灾备快照面权限收敛复核。
#      RUNBOOK 长期声称"快照目录只留 Administrator 可读"，09-29 实测是**文档幻觉**——快照根与
#      restic 中转仓都带继承来的 BUILTIN\Users RX/AD/WD，本机任意账号能读走快照 secrets\ 里的
#      明文网关口令、还能伪造 SNAPSHOT_OK。本批新增 harden_snapshot_acl.ps1（缺省预览 / -Apply
#      才动手 / 先存 icacls /save 回滚凭证 / 改完真拨一次读写自测），这条探针按 **SID** 复核
#      白名单（SYSTEM + Administrators）之外一条都不许留：明细全 ASCII，中文明细经 ssh 回传是
#      GBK 乱码（第 19 探针判例）；孤儿 ACE 计入白名单外；三处目标全不存在判红（无对象可查＝探针
#      失明，与 §70 派生空清单正锁同族）；Get-Acl 失败判红（读不到不等于安全）。
#      两处路径**从 backup_snapshot.ps1 派生**（§H8：两处字面量必然漂移），派生为空当场拒绝启动。
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/verify_deploy_guangzhou.sh
#   GZ_IP=81.71.69.17 COMMIT=beb5b80 ./scripts/verify_deploy_guangzhou.sh   # 显式指定指纹
#
# 参数（环境变量）：GZ_IP（必填）/ GZ_USER / COMMIT（默认本地 HEAD）/ DEPLOY_DIR / 端口三项
#   / 第 19-20 探针可调项：MOCK_UAT_DIR / MOCK_PORT / QMT_WIN_DIR（退役 .ps1 落盘目录，第 19 探针
#   的安全阀态判据要读它）/ GW_CFG / GW_TOKEN_SVC（默认=现网路径）
#   / 第 29 探针新增可调项：OPS_SCRIPTS_DIR（日检两件套落盘目录，默认 DEPLOY_DIR/scripts）
#     / GW_PY_DIR（网关 python 目录，默认取 GW_CFG 父目录——两处字面量必漂移，故推导不写死）
#   / 第 31 探针新增可调项：RESTIC_REPO_DIR / RESTIC_PASS_FILE（默认从 backup_snapshot.ps1 的
#     $RepoDir / $Pass 赋值行派生并转正斜杠；派生读不到则本脚本当场 exit 1，不拿空路径去查权限）
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
# §0929OPS-⑪-1（2026-09-29）：第 29/30 探针的落盘位与只读抽检入口。
# 网关 python 目录从 GW_CFG 的父目录推导——**不再写第二份字面路径**（§H8 单源化的同族姿势：
# 两处字面量必然漂移，漂移后的探针查的是一个现网不存在的位置，等于没查）。
GW_PY_DIR="${GW_PY_DIR:-$(dirname "$GW_CFG")}"
OPS_SCRIPTS_DIR="${OPS_SCRIPTS_DIR:-${DEPLOY_DIR}/scripts}"               # 第 29 探针：日检两件套落盘目录
GW_TOKEN_SVC="${GW_TOKEN_SVC:-quant}"                      # §QMT-TOKENROT 第 20 探针：QUANT_GATEWAY_TOKEN 所在 NSSM 服务
# §0929SECKEY-A（第 31 探针）：restic 中转仓 + 口令文件的现网路径**从备份脚本派生**，
# 不在这里抄第二遍字面量（§H8 同族姿势：两处字面量必然漂移，漂移后探针查的是不存在的位置）。
# backup_snapshot.ps1 是这三处的权威源（它才是每晚写这些目录的人），派生为空即拒绝继续——
# 宁可这条命令当场失败，也不要拿着空路径去 Get-Acl 然后报"全绿"。
_snap_src="${APP_DIR}/deploy/qmt-win/backup_snapshot.ps1"
# 逐行取「`$RepoDir = "…"`」的引号内字面量（bash 3.2 下 heredoc 套命令替换有解析坑，故用 grep/sed）。
RESTIC_REPO_DIR_FROM_SNAP=$(grep -E '^[[:space:]]*\$RepoDir[[:space:]]*=' "$_snap_src" 2>/dev/null | head -1 | awk -F'"' '{print $2}' | tr '\\' '/' || true)
RESTIC_PASS_FILE_FROM_SNAP=$(grep -E '^[[:space:]]*\$Pass[[:space:]]*=' "$_snap_src" 2>/dev/null | head -1 | awk -F'"' '{print $2}' | tr '\\' '/' || true)
RESTIC_REPO_DIR="${RESTIC_REPO_DIR:-${RESTIC_REPO_DIR_FROM_SNAP}}"
RESTIC_PASS_FILE="${RESTIC_PASS_FILE:-${RESTIC_PASS_FILE_FROM_SNAP}}"
if [ -z "$RESTIC_REPO_DIR" ] || [ -z "$RESTIC_PASS_FILE" ]; then
  echo "X 第 31 探针的路径派生失败：备份脚本里的 RepoDir / Pass 变量赋值行读不到（源文件：${_snap_src}）——" >&2
  echo "  宁停不错：拿空路径去 Get-Acl 会把'探针失明'报成全绿。改动备份脚本变量名时同步改这里的 grep 模式。" >&2
  exit 1
fi
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
    [string]$GwTokenService = "quant",
    # §C7-OPS（2026-09-26，第 26 探针）：Windows 服务定义单源文件在现网的落盘位（部署步 [3d] 上传）。
    [string]$SvcDefsPath = "C:\opt\quant\qmt-win\service_definitions.ps1",
    # §0929OPS-⑪-1（2026-09-29，第 29 探针）：五个运维执行体的落盘目录 + 二进制目录。
    # 前三个与 deploy_guangzhou.sh 的 scp 目标同源（qmt-win / scripts / 网关 python 目录），
    # DeployDir 供第 30 探针定位 dataload.exe——四个值都由 bash 侧显式传入，这里只留兜底默认。
    [string]$OpsWinDir = "C:\opt\quant\qmt-win",
    [string]$OpsScriptsDir = "C:\opt\quant\scripts",
    [string]$GwPyDir = "C:\qmt\quant-trading-v2\qmt_gateway",
    # §0929SECKEY-A（2026-09-29，第 31 探针）：restic 中转仓与口令文件的现网路径。
    # 三处字面量（本默认值 / backup_snapshot.ps1 的 $RepoDir,$Pass / harden_snapshot_acl.ps1 的
    # $RepoDir,$PassFile）由 verify_changes.sh §106 等值锁钉死——探针查一个不存在的位置等于没查。
    [string]$ResticRepoDir = "C:\var\lib\quant-restic-repo",
    [string]$ResticPassFile = "C:\opt\quant\tools\restic-pass.txt",
    [string]$DeployDir = "C:\opt\quant"
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
# §0926E2E-W2A 起集合含 SETUP_TOKEN（/setup 抢跑守卫令牌）：注册步保底注入，探针独立复核——
#   校验面不得依附施工面（§M7 同族）。现网补判红一项属预期：需重跑一次注册步注入后转绿。
$envNeed = @("TZ", "QUANT_DATA_DIR", "QUANT_ADDR", "HITHINK_FINANCE_API_KEY", "SETUP_TOKEN")
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
# §0926ROT-SRC5 同日修读法（09-26 发版实录）：本机 NSSM 把 AppEnvironmentExtra 落在
# Services\<svc>\Parameters 子键（§0926-ROT 在 rotate/register 两份 ps1 已锤实并修复），
# 本探针 env 腿旧读法仍只看本级 ⇒ 服务明明有键却恒记 key-absent——按本探针口径 key-absent
# 不判红，但 env 这条可比对源从未真正参与对账（探针失明同族 §TOKEN-BLIND）。
# 修法与两份 ps1 同口径：两级路径直读合并（本级在前保持兼容），绝不解析 nssm 控制台文本。
$tk2Vals = @()
$tk2MissN = 0
foreach ($tk2p in @(("HKLM:\SYSTEM\CurrentControlSet\Services\" + $GwTokenService),
                    ("HKLM:\SYSTEM\CurrentControlSet\Services\" + $GwTokenService + "\Parameters"))) {
    try {
        $tk2Key = Get-Item -LiteralPath $tk2p -ErrorAction Stop
        $tk2RawV = $tk2Key.GetValue('AppEnvironmentExtra', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        foreach ($tv2 in @($tk2RawV)) { $s2 = ("$tv2").Trim(); if ($s2 -match '^[A-Za-z_][A-Za-z0-9_]*=') { $tk2Vals += $s2 } }
    } catch { $tk2MissN++ }
}
if ($tk2Vals.Count -eq 0 -and $tk2MissN -eq 2) {
    $tk2 = "unknown"; $tk2Why = "(both-paths-unreadable)"   # 两级都摸不到＝读法失明，如实标注
} else {
    $tk2Tok = ""; $tk2Rep = ""
    foreach ($kv2 in $tk2Vals) {
        $t2 = ("$kv2").Trim()
        if ($t2 -match '^QUANT_GATEWAY_TOKEN=(.+)$') { $tk2Tok = $Matches[1] }
        elseif ($t2 -match '^QUANT_GATEWAY_REPORT_TOKEN=(.+)$') { $tk2Rep = $Matches[1] }
    }
    $tk2 = TokFp $tk2Tok; if (-not $tk2) { $tk2 = "key-absent" }   # 注册表读到了、只是没设这个键（合法态，网关回退文件值）
    $tk2r = TokFp $tk2Rep
}
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

# 14)（对应文件头清单第 15 项）§SIGNAL-DIST（2026-09-24，第 22 个 Probe 调用点／展开后第 22 探针）：
#    当日固化信号按战法分布——§KLINE-CHAIN-3 的验收眼睛。
# 为什么要这条：09-23 那晚「白天只有龙头出信号」是**owner 用肉眼在前端看出来的**，现网 24 条探针
#   一条都没红（引擎活着、链也在跑，只是拿不到日K 的那批战法整天零产出）。也就是说这条缺陷在
#   观测面上是**隐形**的：修没修好同样没人知道。所以修完之后必须把「今天有哪些战法出了信号」变成
#   一条可复跑的读数，而不是再靠人盯。
# 读法（只读，不新增凭据）：引擎的当日固化信号落 <DataDir>\signals_today.json
#   （internal/engine/engine.go:558 + signal_store.go，键 code@strategy，跨重启恢复、交易日自动滚动）。
#   按 Recurse -Depth 2 收（dataDir 为空时该文件根本不落，属合法 no-file）。
# 口径（2026-09-24 首跑后收紧，见下"为什么按文件分行"；同日 §88 扩桶后改精确等值）：战法一律取
#   ASCII 的归一桶——Signal.StrategyType（runner 类型/规则 ID：dragon/double_bump/n_shape/
#   dragon_return/momentum/高4做空桶/fac_N/pat_N）优先，缺失时按中文展示名**查表精确映射**，
#   映射不到记 other 并把**原始类型消毒后原样列出**（unmatched=），绝不靠子串猜。
#   **不把中文塞进明细**——本仓库实录过 PowerShell→SSH→bash 回传时中文 detail 会被 GBK 字节打乱、
#   在 grep 判据里恒不命中（＝把假绿写进探针）。原始值进明细前一律先剔掉非 ASCII 字符。
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
# SgKey：把一条固化信号归一到 ASCII 桶名。2026-09-24 扩桶时整体改成**精确等值查表**，理由：
#   旧实现把 `strategy_type + "|" + strategy` 拼成一串再做子串 -match，于是「龙头断板」（做空战法
#   leader_decay 的展示名）被 `'dragon|龙头'` 那条规则静默吞进 dragon 桶——09-24 现网读数
#   `dragon:14 / other:9` 里的 14 就是虚高的（卖出信号混在买入龙头里），而 owner 要的"到底是哪些
#   战法在买、哪些在卖"当时答不出来。子串匹配在这种"名字互相包含"的领域里没有修复空间：
#   补一条 if 顺序只是把下一次撞名推迟（高位滞涨/放量破位/利好兑现同理），故一律换成精确等值，
#   匹配不上就退回 other 并把原始值列出来（宁可少分类，不猜）。
#   桶名与 internal/strategy/types.go 的 SignalType 常量表逐字对齐，verify_changes.sh §88 有一道
#   "Go 里有、探针桶里没有"的完整性锁，新增战法不会再静默落进 other。
function SgKey($o) {
    $t = ([string]$o.strategy_type).Trim()
    $s = ([string]$o.strategy).Trim()
    if (-not $t -and -not $s) { return "unknown" }
    # ① 权威口径：ASCII strategy_type 精确等值（ToLower 兼容 DragonReturn 这类驼峰历史别名）
    switch -Exact ($t.ToLower()) {
        'dragon'         { return "dragon" }
        'double_bump'    { return "double_bump" }
        'n_shape'        { return "n_shape" }
        'dragon_return'  { return "dragon_return" }
        'momentum'       { return "momentum" }
        'high_churn'     { return "high_churn" }
        'break_down'     { return "break_down" }
        'leader_decay'   { return "leader_decay" }
        'good_news_fade' { return "good_news_fade" }
        'short_skeleton' { return "short_skeleton" }
        'factor'         { return "factor" }
        'pattern'        { return "pattern" }
    }
    # ② 中文展示名精确等值（strategy_type 为空的旧行、以及把中文塞进 strategy_type 的历史行都走这里）
    #    取值集＝internal/combat_agent/types.go 的 StrategyDisplayName + NormalizeStrategyName 别名表。
    #    -Exact 保证「龙头」不会命中「龙头断板」，所以卖出桶不需要靠书写顺序保命。
    foreach ($c in @($t, $s)) {
        if (-not $c) { continue }
        switch -Exact ($c) {
            '龙头'         { return "dragon" }
            '双响炮'       { return "double_bump" }
            'N形'          { return "n_shape" }
            'N形超短'      { return "n_shape" }
            'N字型'        { return "n_shape" }
            'N字'          { return "n_shape" }
            '龙回头'       { return "dragon_return" }
            '动量'         { return "momentum" }
            '高位滞涨'     { return "high_churn" }
            '放量破位'     { return "break_down" }
            '破位'         { return "break_down" }
            '龙头断板'     { return "leader_decay" }
            '断板'         { return "leader_decay" }
            '利好兑现砸盘' { return "good_news_fade" }
            '利好兑现'     { return "good_news_fade" }
            '做空骨架'     { return "short_skeleton" }
        }
    }
    # ③ 多规则战法的规则 ID 前缀（fac_1 / pat_3）：只有 ^ 锚定的前缀，仍然不是子串。
    if ($t -match '^fac_') { return "factor" }
    if ($t -match '^pat_') { return "pattern" }
    return "other"
}
# SgSide：把一条固化信号归到 buy / sell 档。轴**必须**选 direction，不是 action：
#   ① 固化存储只收 做多/做空（internal/engine/signal_store.go 的 Upsert 白名单），提醒型（止盈/止损/
#      减仓）根本进不了 signals_today.json ⇒ 该字段在本文件里恒为二选一，是这里唯一有保障的字段；
#   ② action 在不同战法里有 buy / sell / 卖出 / 减仓 / 关注 五套写法，拿它分档必错，
#      所以兜底档显式叫 side_unknown 并回显计数——出现第三种取值时看得见，不会静默并进某一档。
function SgSide($o) {
    switch -Exact (([string]$o.direction).Trim()) {
        '做多' { return "buy" }
        '做空' { return "sell" }
    }
    return "side_unknown"
}
# SgRaw：未归类桶（other/unknown）的原始类型消毒——剔掉非 ASCII 字符后原样回显，空了就记 nonascii。
# 直接打印中文会让明细在 PS→SSH→bash 回传时被 GBK 字节打乱（同 §NSSMENV 那批教训），
# 但"列出原始值"正是 owner 要的：桶没扩全时他要能看见是哪个战法没进桶。
function SgRaw($o) {
    $r = (([string]$o.strategy_type) + "/" + ([string]$o.strategy)) -replace '[^ -~/]', ''
    $r = $r.Trim('/')
    if (-not $r) { return "nonascii" }
    if ($r.Length -gt 40) { $r = $r.Substring(0, 40) }
    return $r
}
# SgInc / SgTop：计数与读数格式化的小工具（哈希表按引用传入，函数内累加对调用方可见）。
# 抽出来是因为本探针现在要同时维护四张计数表（全桶/当日桶/买入桶/卖出桶），内联写四遍必错一处。
function SgInc($h, $k) { if ($h.ContainsKey($k)) { $h[$k] = [int]$h[$k] + 1 } else { $h[$k] = 1 } }
function SgTop($h) {
    $o = ""
    foreach ($k in @($h.Keys | Sort-Object)) { if ($o) { $o += "," }; $o += ($k + ":" + $h[$k]) }
    if ($o) { return $o }
    return "none"
}
$sgBad = @()
$sgToday = (Get-Date).ToString("yyyyMMdd")
$sgTodayN = 0
$sgTodayFiles = 0
$sgTodayByType = @{}
# 当日按买/卖档分桶 + 未归类原始值：三者都是 09-24 扩桶批新增的读数（见 SgKey/SgSide 注释）。
$sgTodayBuy = @{}
$sgTodaySell = @{}
$sgTodaySideUnknown = 0
$sgRawUnmatched = @{}
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
        SgInc $sgByType $k
        if ($isToday -eq "yes") {
            SgInc $sgTodayByType $k
            # 未归类原始值只看当日文件（跨日残留桶会把"今天到底哪条没进桶"淹掉）。
            if ($k -eq 'other' -or $k -eq 'unknown') { SgInc $sgRawUnmatched (SgRaw $sg) }
            # 买/卖档只统计当日（与 today_* 其余读数同口径），跨日残留桶不进聚合。
            $sd = SgSide $sg
            if ($sd -eq 'buy') { SgInc $sgTodayBuy $k }
            elseif ($sd -eq 'sell') { SgInc $sgTodaySell $k }
            else { $sgTodaySideUnknown = $sgTodaySideUnknown + 1 }
        }
    }
    Write-Output ("INFO|signals_file " + $rel + " day=" + $sgDay + " today=" + $isToday + " n=" + $sgN +
        " kinds=" + (SgTop $sgByType) +
        " mtime=" + $sgf.LastWriteTime.ToString("yyyy-MM-dd HH:mm"))
    if ($isToday -eq "yes") {
        $sgTodayFiles = $sgTodayFiles + 1
        $sgTodayN = $sgTodayN + $sgN
    }
}
$sgTypes = @($sgTodayByType.Keys | Sort-Object)
# 买入档种类是「白天只龙头出信号」那类缺陷的判据面：卖出/做空桶再热闹，只要买入侧只有 dragon 一类，
# 打分链的买入覆盖就仍然偏窄（09-24 现网 dragon 虚高的根因正是这两个面被混在了一起）。
$sgBuyTypes = @($sgTodayBuy.Keys | Sort-Object)
$sgSellTypes = @($sgTodaySell.Keys | Sort-Object)
$sgLeaderOnly = "false"
if ($sgTodayN -gt 0 -and $sgBuyTypes.Count -eq 1 -and $sgBuyTypes[0] -eq 'dragon') { $sgLeaderOnly = "true" }
# side_unknown 恒为 0 是本探针口径成立的前提（direction 只有 做多/做空 两种取值，由固化存储白名单保证）；
# 一旦不为 0，说明上游出现了第三种方向词，买卖两档的读数就此失真，必须能在明细里一眼看到而不是静默漏计。
$sgDetail = "today_signals=" + $sgTodayN + " today_strategies=" + $sgTypes.Count + " leader_only=" + $sgLeaderOnly +
    " today_files=" + $sgTodayFiles + "/" + $sgFiles.Count + " day=" + $sgToday +
    " buy_types=" + $sgBuyTypes.Count + " sell_types=" + $sgSellTypes.Count +
    " kinds_buy=" + (SgTop $sgTodayBuy) + " kinds_sell=" + (SgTop $sgTodaySell) +
    " unmatched=" + (SgTop $sgRawUnmatched) + " side_unknown=" + $sgTodaySideUnknown +
    " top=" + (SgTop $sgTodayByType) +
    " miss=" + $(if ($sgBad.Count) { ($sgBad | Sort-Object -Unique) -join "," } else { "none" })
# 这条探针的**存在理由就是读数本身**，所以绿的时候也必须把明细打出来（其余探针只在红时回显 detail）。
# 走独立的 INFO 通道：bash 侧只 echo、不进 PASS/FAIL 计数 ⇒ 红绿语义与 26 条判数都不受影响。
Write-Output ("INFO|signals_today " + $sgDetail)
Probe "engine:today pinned signals spread across strategies" ($sgBad.Count -eq 0) $sgDetail

# 16)（对应文件头清单第 16 项）§C7-OPS（2026-09-26，第 26 探针）：Windows 服务定义单源文件在位复核。
# 背景：owner 裁决"三套拉起方式并存"要单源化——6 个运维脚本（register_engine_services /
#   register_service / ensure_gateway_config / gateway_watchdog / all_service_watchdog /
#   rotate_qmt_token）改为 dot-source deploy/qmt-win/service_definitions.ps1。§ENH-5 的教训
#   （"仓库里有、现网没有"）要求部署面独立复核落盘，不能只信 scp 步骤自己打的"完成"。
# 判据两条互相独立：①文件在位且可读；②**内容认识单源标记**——空文件、旧版回退副本、
#   被截断的半份文件都不算过（只查 Test-Path 的话，scp 打断留下 0 字节文件照样绿）。
# 明细全 ASCII（同第 19 探针的 GBK 教训）。
$svcDefsTxt = ""
$svcDefsState = "absent"
$svcDefsMiss = @()
if (Test-Path -LiteralPath $SvcDefsPath) {
    try { $svcDefsTxt = [string](Get-Content -LiteralPath $SvcDefsPath -Raw -ErrorAction Stop); $svcDefsState = "present" }
    catch { $svcDefsState = "unreadable"; $svcDefsMiss += "unreadable" }
} else { $svcDefsMiss += "file-missing" }
if ($svcDefsTxt) {
    # 三个标记各代表单源化的一类定义：NSSM 服务名表 / 任务名表 / nssm 路径解析器。
    if ($svcDefsTxt -notmatch '\$SvcNssmServices')   { $svcDefsMiss += "no-svc-name-table" }
    if ($svcDefsTxt -notmatch '\$SvcTaskGatewayEnsure') { $svcDefsMiss += "no-task-name-table" }
    if ($svcDefsTxt -notmatch 'Resolve-SvcNssm')     { $svcDefsMiss += "no-nssm-resolver" }
}
$svcDefsDetail = "path=$SvcDefsPath state=" + $svcDefsState + " bytes=" + $svcDefsTxt.Length +
    " miss=" + $(if ($svcDefsMiss.Count) { ($svcDefsMiss -join ",") } else { "none" })
Probe "ops:C7 service_definitions single source in place" ($svcDefsMiss.Count -eq 0) $svcDefsDetail

# 17) §CAL-READOUT（2026-09-26，第 27 探针，owner 令"现网体检加一条日历已加载只读读数"）：
# 背景：观察项 #58——要把"休市日照常出信号＝日历 fail-open"定性成可复跑的读数，需要
#   trading_calendar_loaded 量规的现值；但该量规只在鉴权后的 /api/metrics 里，验证链一直
#   刻意不带凭据（"只读、不新增凭据"与 signals 探针同姿势），所以改走磁盘侧两条腿。
# 腿 A 缓存文件 <DataDir>\trading_calendar.json——calendarCacheFilePath()（internal/data/
#   trade_calendar.go:170）的落盘缓存；QUANT_DATA_DIR 与本脚本 -DataDir 同源（§N-5 注册的
#   服务 env 即 QUANT_DATA_DIR=$DataDir），冷启动加载器先读它 ⇒ 文件在位＝启动时有可读的日历料。
#   JSON 键 saved_at/closed_days 纯 ASCII。
# 腿 B 引擎 stderr 服务日志——Go log.Printf 走 stderr、nssm 落盘采集（10MB 轮转，
#   prune_logs.ps1 保 20 份）。锚点行三种（全部来自日志打印点，读码钉死）：
#   · 负线 `... [cal] 交易日历未加载...`（scoring_loop.go:1311，**只在未加载时打**、OncePer 24h）；
#   · 正线①拉取成功 `... [calendar] ...窗口 20250101~20261231...`（trade_calendar.go:146，
#     唯一 ASCII 形态 \d{8}~\d{8}）；正线②缓存装载成功 `... [calendar] ...保存于 2026-09-25...`
#     （trade_calendar.go:227，特征 = [calendar] 行里带 YYYY-MM-DD 日期；失败行不含该形态，
#     err 文本没有这种日期格式）。
#   判据只用 ASCII 锚与行首时间戳：现网 PS 5.1 按 ANSI/GBK 解 UTF-8 会把中文打成乱码，
#   中文永远不做判据（§SIGNAL-DIST 同课）；行首 ts 是 Go logger LstdFlags（main.go:79），
#   三类行同一时钟，字符串比较＝时间先后，与机器时区无关。
#   日志路径先读 nssm 服务键注册表 AppStdErr/AppStdout（§N-5 教训：解析控制台文本会踩
#   UTF-16/NUL 坑，直读注册表拿原生值），读不到再回落 prune_logs.ps1:14 实测位
#   C:\opt\quant\quant_stderr.log。
# 只扫活动文件：真未加载时 OncePer 24h 会持续在活动文件补新负线；轮转文件里的旧线分不清
#   "已恢复"还是"仍没加载"，跨文件拿它做时序证据＝假红机器，故一概不进判据。
# 判据（刻意不对称，首跑校准）：
#   红 = 活动文件有 [cal] 负线、且其后再无任一正线（＝此刻仍 fail-open，决定性证据）；
#   不红 = 日志读不到/路径不存在/只有正线/什么锚线都没有——状态原样写进读数回显。
#   缓存文件缺失同样不直接判红：现网文件形态首跑前不可知，预判红＝探针出生即自伤
#   （§probe-premises 口径）；cache= 原样回显，若首跑读数确认"缓存缺失"需处置再按
#   §SIGNAL-DIST"首跑后收紧"姿势补锁。
# INFO 恒回显（PASS 也要看得到数，与第 22 探针同通道；bash 侧 INFO 不进 PASS/FAIL 计数）。
$calCachePath = $DataDir + "\trading_calendar.json"
$calCacheState = "missing"; $calSavedAt = "na"; $calClosedN = "na"
if (Test-Path -LiteralPath $calCachePath) {
    try {
        $calJson = Get-Content -LiteralPath $calCachePath -Raw -ErrorAction Stop | ConvertFrom-Json
        $calCacheState = "present"
        $calSavedAt = ([string]$calJson.saved_at) -replace '[^0-9-]', ''
        if (-not $calSavedAt) { $calSavedAt = "empty" }
        if ($null -eq $calJson.closed_days) { $calClosedN = "absent" } else { $calClosedN = ([string](@($calJson.closed_days).Count)) }
    } catch { $calCacheState = "unreadable" }
}
$calLogPath = ""
try {
    $calSvcKey = Get-Item -LiteralPath "HKLM:\SYSTEM\CurrentControlSet\Services\quant" -ErrorAction Stop
    foreach ($calV in @('AppStdErr', 'AppStdout')) {
        $calP = [string]$calSvcKey.GetValue($calV, '')
        if ($calP -and (Test-Path -LiteralPath $calP)) { $calLogPath = $calP; break }
    }
} catch { }
if (-not $calLogPath -and (Test-Path -LiteralPath 'C:\opt\quant\quant_stderr.log')) { $calLogPath = 'C:\opt\quant\quant_stderr.log' }
$calPosTs = ""; $calPosWin = ""; $calSeedTs = ""; $calNegTs = ""; $calLogState = "nopath"
if ($calLogPath) {
    $calLogState = "read"
    try {
        foreach ($calLine in (Get-Content -LiteralPath $calLogPath -ErrorAction Stop)) {
            if ($calLine -notmatch '^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})') { continue }
            $calTs = $Matches[1]
            if ($calLine -match '\[cal\]') { $calNegTs = $calTs }
            elseif ($calLine -match '(\d{8})~(\d{8})') { $calPosTs = $calTs; $calPosWin = $Matches[1] + "~" + $Matches[2] }
            elseif ($calLine -match '\[calendar\].*20\d\d-\d\d-\d\d') { $calSeedTs = $calTs }
        }
    } catch { $calLogState = "unreadable" }
}
# 正证据取两种形态里较晚的一条（同一 ts 格式，字符串比较即时间先后）。
$calGoodTs = $calPosTs
if ($calSeedTs -and ($calSeedTs -gt $calGoodTs)) { $calGoodTs = $calSeedTs }
$calBad = ($calNegTs -ne "" -and ($calNegTs -gt $calGoodTs))
$calVerdict = "no-evidence"
if ($calPosTs) { $calVerdict = "loaded-api" }
if ($calSeedTs -and (-not $calPosTs -or $calSeedTs -gt $calPosTs)) { $calVerdict = "loaded-cache" }
if ($calBad) { $calVerdict = "NOT-LOADED" }
$calLeaf = "nopath"
if ($calLogPath) { $calLeaf = [string](Split-Path -Leaf $calLogPath) }
$calDetail = "verdict=" + $calVerdict + " cache=" + $calCacheState + "," + $calSavedAt + ",n=" + $calClosedN +
    " log=" + $calLogState + "," + $calLeaf +
    " pos=" + $(if ($calPosTs) { $calPosTs + " window=" + $calPosWin } else { "none" }) +
    " seed=" + $(if ($calSeedTs) { $calSeedTs } else { "none" }) +
    " neg=" + $(if ($calNegTs) { $calNegTs } else { "none" })
Write-Output ("INFO|cal_readout " + $calDetail)
Probe "cal: trading calendar not in fail-open (readout in INFO)" (-not $calBad) $calDetail

# 18) §A5-CURRENT（2026-09-26 深夜批，第 28 探针）：跌停追卖常开闸（owner 裁决 2026-09-26
#     「limit_down_block_sell 常开＝是」，nil=开、显式 false=关）在现网 config.json 的**实际生效值**。
# 背景：该裁决的部署前提「云端 config.json 不得留 false」此前无取证通道——排摸脚本按设计只认
#     两份战法 JSON、不读 config.json；配置 GET 端点走管理会话令牌（本机令牌不在位，属 owner 输入）。
#     本探针走验证链自有一条免凭据腿：scp 上传的 ps1 在服务器侧只读 config.json（第 15/27 探针
#     同款通道，零写入、零凭据、值本身是布尔不涉密）。
# 生效路径唯一（读码锤定）：Go wrapper 只认 {"rules","d1"} 两段，risk_gate 只挂在 rules.qmt 下
#     （config.go 唯一 json tag `risk_gate`），所以**结构化读数 rules.qmt.risk_gate.limit_down_block_sell
#     才是真生效值**。裸扫 `"limit_down_block_sell"..."false"` 只做第二条腿：键位漂移（§ROOTQMT
#     根级死键同族形态）出现在生效路径之外时**不判红**（Go 不消费死键，判红即假红），但原样进
#     INFO 点名——死键意味着有人写了个不生效的开关，那正是要人来清理的形态。
# 判据（刻意不对称，与 §CAL-READOUT 同姿势）：
#     红 = 生效路径显式 false（常开裁决被现网数据静默推翻，决定性证据）；
#     不红 = 文件缺失/解析失败/键 absent/true——状态全部原样写进读数回显，首跑不预设现网形态。
# INFO 恒回显（绿也要看得到数；bash 侧 INFO 不进 PASS/FAIL 计数⇒判数 27→28 只 +1）。
$ldCfgPath = $DataDir + "\config.json"
$ldState = "missing"; $ldVal = "absent"; $ldKeyN = 0; $ldFalseN = 0
if (Test-Path -LiteralPath $ldCfgPath) {
    try {
        $ldRaw = Get-Content -LiteralPath $ldCfgPath -Raw -ErrorAction Stop
        $ldState = "read"
        $ldJson = $ldRaw | ConvertFrom-Json
        # PS 对不存在属性返回 $null（ErrorActionPreference=Continue），缺键与显式 null 同为 absent 态。
        $ldv = $ldJson.rules.qmt.risk_gate.limit_down_block_sell
        if ($null -eq $ldv) { $ldVal = "absent" } elseif ([string]$ldv -eq "True") { $ldVal = "true" } else { $ldVal = "false" }
        $ldKeyN = ([regex]::Matches($ldRaw, '"limit_down_block_sell"')).Count
        $ldFalseN = ([regex]::Matches($ldRaw, '"limit_down_block_sell"\s*:\s*false')).Count
    } catch { $ldState = "unreadable" }
}
$ldBad = ($ldVal -eq "false")
$ldDetail = "state=" + $ldState + " effective=" + $ldVal + " keys=" + $ldKeyN + " falsepat=" + $ldFalseN +
    " path=rules.qmt.risk_gate.limit_down_block_sell"
Write-Output ("INFO|limitdown_readout " + $ldDetail)
Probe "cfg: limit-down sell-chase gate not disabled by live config (readout in INFO)" (-not $ldBad) $ldDetail

# 19) §0929OPS-⑪-1（2026-09-29，第 29 探针，判数 28→29）：运维面五个执行体在位复核。
# 背景：这五个都是**现网指示会去执行的对象**（告警文案让人跑 outbox_admin.py --yes；日检第 1 节
#   调 enable_ensure.ps1；看门狗/注册计划任务跑 watchdog 与 register_service），却长期靠手工拷贝、
#   不在部署清单里。§ENH-5/§P0-B/§0927KA 三条同族判例的共同根因是同一个：
#   **「仓库里有」被当成「现网在跑」**。本批已把它们收编进 deploy_guangzhou.sh，
#   这条探针负责在发版后独立复核落盘，而不是只信 scp 自己打的"完成"。
# 判据两条互相独立（第 26 探针同款姿势）：①文件在位；②**内容认识各自的标记**——
#   scp 打断留下的 0 字节半份文件、旧版回退副本都不算过，只查 Test-Path 会照样绿。
# 明细全 ASCII（GBK 教训：现网 PS5.1 输出中文会撕裂字面量，见第 19 探针原判例）。
$opsManifest = @(
    @{ P = ($OpsWinDir + "\register_service.ps1");     M = "QMT-Gateway" },
    @{ P = ($OpsWinDir + "\all_service_watchdog.ps1");  M = "service_probe_config" },
    @{ P = ($OpsScriptsDir + "\daily_ops_check.ps1");   M = "enable_ensure" },
    @{ P = ($OpsScriptsDir + "\enable_ensure.ps1");     M = "QMT-Ensure-Running" },
    @{ P = ($GwPyDir + "\outbox_admin.py");             M = "def main" }
)
$opsMiss = @()
$opsRead = @()
foreach ($f in $opsManifest) {
    $txt = ""
    if (Test-Path -LiteralPath $f.P) {
        try { $txt = [string](Get-Content -LiteralPath $f.P -Raw -ErrorAction Stop) } catch { $txt = "" }
    }
    if (-not $txt) { $opsMiss += ($f.P + ":absent-or-empty"); $opsRead += (($f.P | Split-Path -Leaf) + "=absent"); continue }
    if ($txt -notmatch [regex]::Escape($f.M)) { $opsMiss += ($f.P + ":no-marker(" + $f.M + ")") }
    $opsRead += (($f.P | Split-Path -Leaf) + "=" + $txt.Length)
}
$opsDetail = "bytes[" + ($opsRead -join ",") + "] miss=" + $(if ($opsMiss.Count) { ($opsMiss -join ";") } else { "none" })
Probe "ops:0929 five live-executable ops files in place with content markers" ($opsMiss.Count -eq 0) $opsDetail

# 20) §0929SCALE-⑩（2026-09-29，第 30 探针，判数 29→30）：研究库成交额量纲抽检（只读，零写入）。
# 背景：daily.amount 有"元/千元"两套口径的历史缝（tushare 腿原样落千元）。本批把换算钉在写侧，
#   并在 store 侧抽出一个抽检探针；现网这一条走 dataload.exe amount-check——**它只跑 SELECT**，
#   不碰任何写路径，与第 15/27 探针同一条免凭据只读通道。
# §W7-D（2026-10-09）：同一条腿扩到 **两张表**（daily + ths_daily，`--table` 传参）。
#   为什么必须扩：ths_daily 是同花顺 dump 主源、amount 直接来自 parquet 的 turnover 列，
#   而该列口径在本仓两处注释里互相矛盾过（"换手率（%）" vs "成交额/换手率"，§W7-D 已改）。
#   该表**不在**写侧换算白名单里（data.AmountScaledTables），所以它没有任何换算兜底——
#   注释一改就结束了、口径仍然是口头承诺；只有读数能说明库里到底是元、千元还是换手率量级。
#   两张表用同一个判定体（store.AmountProbedTables），所以这里的判据形状也保持一条腿两个 rc，
#   不另开第二条 Probe/INFO（§88 的 INFO 观测行计数与 PS 侧行首 Probe 计数都由"一腿一行"保证）。
# 判据：两张表退出码都为 0（ok 或 no-data）算绿；任一为 1（千元/双重换算/混源）或 2（读取失败）算红。
#   no-data 不判红是刻意的：库里可能只有 ths_daily 一条腿有数，日线空由新鲜度腿负责报警，
#   这里重复判红只会把"表还没装"冒充成"量纲错了"（ths_daily 现网同样可能整表未装载）。
# 读数恒进 INFO（绿也要看得到中位均价，第一次跑就能看出库里到底是哪套口径）。
$scaleOut = ""
$scaleRc = -1
$scaleThsOut = ""
$scaleThsRc = -1
if (Test-Path -LiteralPath ($DeployDir + "\dataload.exe")) {
    try {
        $scaleRaw = & ($DeployDir + "\dataload.exe") "--db" ($DataDir + "\trading.db") "amount-check" "--json" 2>&1
        $scaleRc = $LASTEXITCODE
        $scaleOut = ($scaleRaw -join " ")
    } catch { $scaleRc = 2; $scaleOut = "invoke-failed: " + $_.Exception.Message }
    try {
        $scaleThsRaw = & ($DeployDir + "\dataload.exe") "--db" ($DataDir + "\trading.db") "amount-check" "--table" "ths_daily" "--json" 2>&1
        $scaleThsRc = $LASTEXITCODE
        $scaleThsOut = ($scaleThsRaw -join " ")
    } catch { $scaleThsRc = 2; $scaleThsOut = "invoke-failed: " + $_.Exception.Message }
} else {
    # 执行体缺席时两个 rc 都要落 2：只落一个的话另一条保持 -1，判红文案会显示一个没跑过的读数
    $scaleRc = 2; $scaleOut = "dataload.exe missing at " + $DeployDir
    $scaleThsRc = 2; $scaleThsOut = "dataload.exe missing at " + $DeployDir
}
$scaleBad = (($scaleRc -ne 0) -or ($scaleThsRc -ne 0))
$scaleDetail = "daily[rc=" + $scaleRc + " out=" + $scaleOut + "] ths_daily[rc=" + $scaleThsRc + " out=" + $scaleThsOut + "]"
Write-Output ("INFO|amount_scale_readout " + $scaleDetail)
Probe "data: daily+ths_daily amount caliber probe green (readout in INFO)" (-not $scaleBad) $scaleDetail

# 21) §0929SECKEY-A（2026-09-29，第 31 探针，判数 30→31）：灾备快照与口令文件的权限面复核。
# 为什么这条探针必须存在（现网实测锤实，不是推测）：RUNBOOK 一直写着灾备的补偿措施之一是
#   "快照目录只留 Administrator 可读"，而 09-29 用 icacls 读现值——快照根与 restic 中转仓都带着
#   继承来的 `BUILTIN\Users:(RX)` + `(AD)/(WD)`，即本机任意账号能读走快照里的明文网关口令
#   （auth.json/config.json 在快照 secrets\ 下），还能往快照根写文件（伪造 SNAPSHOT_OK）。
#   这句话是**文档幻觉**（§BOM-REPO/§ENH-5 同族：写在文档里的补偿措施没落地也没人判红）。
#   本批把收敛做成 harden_snapshot_acl.ps1（缺省预览、-Apply 才动手、先存 icacls /save 回滚凭证、
#   改完真拨一次读+写自测），这条探针负责在每次发版后独立复核"还裸着"这件事，而不是靠人记得跑。
# 判据形状：白名单外 ACE 数 == 0（白名单＝SYSTEM + BUILTIN\Administrators，与收敛脚本同源）。
#   ① 按 **SID** 判定，绝不解析 icacls 的文本账号名——中文名按控制台码页回传会变 GBK 乱码、
#      且 "Administrators" 在中文系统显示本地化名（第 19 探针判例）；明细因此只含 ASCII 的 S-1-5-*。
#   ② 孤儿 ACE（Translate 失败）标 UNTRANSLATABLE 计入白名单外：它同样是一条访问许可，
#      当成不存在就是自证绿。
#   ③ 目标三处**全都不存在**判红而不是绿（"无对象可查"＝探针失明，与 §70 派生空清单正锁同族）；
#      单处缺失不判红——口令文件/中转仓只在启用 restic relay 的机器上有，缺失由第 16 探针那侧管。
#   ④ Get-Acl 失败算红（读不到权限不等于权限安全）。
# 与第 22 探针一样：绿时走 INFO 回显每处的 SID 与白名单外条数，红时明细进 FAIL。
$aclAllowed = @("S-1-5-18", "S-1-5-32-544")
$aclTargets = @(
    @{ L = "snap_root";   P = $SnapDir },
    @{ L = "restic_repo"; P = $ResticRepoDir },
    @{ L = "restic_pass"; P = $ResticPassFile }
)
$aclOutside = 0
$aclChecked = 0
$aclParts = @()
foreach ($t in $aclTargets) {
    if (-not (Test-Path -LiteralPath $t.P)) { $aclParts += ($t.L + "=absent"); continue }
    $aclChecked += 1
    try {
        $acl = Get-Acl -LiteralPath $t.P -ErrorAction Stop
        $sids = @()
        foreach ($ace in $acl.Access) {
            try { $sids += $ace.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value }
            catch { $sids += ("UNTRANSLATABLE:" + ($ace.IdentityReference.Value -replace '[^\x21-\x7E]', '?')) }
        }
        $sids = @($sids | Sort-Object -Unique)
    } catch {
        $aclOutside += 1
        $aclParts += ($t.L + "=getacl-failed")
        continue
    }
    $out = @($sids | Where-Object { $aclAllowed -notcontains $_ })
    $aclOutside += $out.Count
    # 明细里只放 SID（ASCII）：白名单外那些逐条点名，其余折叠成 allowed 计数，避免长行。
    $desc = $t.L + "=outside[" + (($out | ForEach-Object { $_ -replace 'UNTRANSLATABLE:.*', 'orphan-ace' }) -join ";") + "] sids=" + $sids.Count
    $aclParts += ($desc -replace '[^\x20-\x7E]', '')
}
$aclDetail = "checked=" + $aclChecked + " outside=" + $aclOutside + " " + ($aclParts -join " ")
Write-Output ("INFO|snapshot_acl_readout " + $aclDetail)
$aclBad = ($aclOutside -gt 0 -or $aclChecked -eq 0)
Probe "sec: snapshot/restic dirs expose no ACE outside SYSTEM+Administrators" (-not $aclBad) $aclDetail

# 18) §KA-TASKREG（2026-10-07 修复批 波 3，第 32 探针）：Windows 计划任务**全集**在位 +
#     周期任务上次运行新鲜度。
# 存在理由（§AUDIT_20261005 P1-D，本机读码锤实）：17:10 盘后保活任务 QMT-Dataload-KeepAlive
#   的执行体 scripts/dataload_keepalive.py 自 §0927KA 起在部署 scp 清单里，而**任务本体从来没有
#   注册体、部署面也没有"任务在位"判据**——它是 2026-09-16 手工 schtasks 出来的（RUNBOOK §1 记的
#   正是那次手工修）。本探针是三段修法的第三段：①任务名单/阈值单源＝service_definitions.ps1 的
#   $SvcTaskRoster + $SvcTaskFreshRules + $SvcTaskInPlaceOnly；②注册体＝register_engine_services.ps1
#   §6b（缺省只预演、-RegisterKeepaliveTask 才动手）；③本探针。
# 输出协议：每任务一行 `TASK|<name>|present=..|rule=..|age_h=..|state=..|enabled=..|reg_h=..|last_run=..|en_src=..|reg_src=..|obj=..|xml_from=..|xml_err=..|action=..`，
#   **只有读数、没有判词**；红绿由 bash 侧 judge_task_roster() 判（见本文件下方）。
#   另有一条 `TASK|__live_names__|...` 观测行（现网同族任务名清单，只转 INFO、不参与判读）。
#   reg_h/last_run 是 10-09 首拨之后补的两把：前者＝注册龄（分开"刚装还没到触发点"与"该跑没跑"
#   两种从未运行），后者＝上次运行的原始时间戳（让年代界判定可被读数复核，而不是让人信判据）。
#   en_src/reg_src 是 10-10 那次"八条 enabled 与 reg_h 全 na"之后补的**读法自证**（取值
#   xml-byname / xml-adapter / defs-prop / info-prop / implausible / parse-failed / none）：
#   供给侧失灵的形态是"读数恒 na 而判据照常绿"，光有值没有来源时，红了仍然要人上机猜一次。
#   两把只进 INFO、**不参与判读**（拿读法来源当判据＝把"我的式子换了条路"当成健康度变化）。
#   缺字段容错：三条伪读数行（defs-unreadable / roster-empty / item-empty）不带这两个键，
#   bash 侧按 `|key=` 前缀取，取不到就是空——空值走 fail-closed，不会因为"没这个字段"放行。
#   为什么判读不放 PS：本机没有 PowerShell，判据写在 PS 就是"从没真跑过的判据"——§0929DRILL 四条
#   缺陷的共同根因正是"脚本写得完整但从没真跑"，DRILL-A 那条 --last 假红就是没跑过的读法。
#   放 bash 之后，门禁 §110 可以喂七种合成读数逐条验红绿，全程离线、零外呼。
# 全集怎么来（关键取向）：**不写第二份名单**，点源单源文件后遍历 $SvcTaskRoster。写死名单＝
#   §BOM-REPO-DERIVE / §107 派生正锁点名的同一个形态：清单式锁对下一个新增任务天生失明，
#   而"新增任务"恰恰是本批要防的那件事。
#   点源失败 / 名单为空 / 名单里有空项 ⇒ 各出一条**坏读数行**交给 bash 判红（探针看不见要查的
#   对象＝探针失明，与 §70 空清单正锁同族）。
# 阈值不在这里写：rule 字段直接回显单源里的 MaxAgeHours，bash 只做"age 与 rule 比大小"——
#   于是"阈值改一处、探针跟着漏改"这类面天然为零（§P0-B 的 30h 同源同口径）。
# rule=inplace 的任务是 ONLOGON / ONSTART 触发（QMT-Gateway-Logon / quant-all-wd）：它们的上次
#   运行时间由"有没有人登录、机器有没有重启"决定，不由时钟决定，拿来判新鲜度＝在一台可以连续
#   数周不重立的机器上造**结构性必红**（§107 DRILL-C：永远红的锁的结局是所有人学会忽略它）。
# enabled 读数：CHECKLIST 的止损动作是先 `/change /disable` 再杀进程，所以"任务在位但被禁用"是
#   一个真实存在的中间态；周期任务禁用即判红（守护不该长期停着），仅查在位的不判。
# action 字段：只回显任务动作行（可执行文件 + 参数，纯 ASCII 路径，截 160 字符，不含任何凭据）。
#   用途是让"现网存的那条命令"与"注册体将要写入的那条"能当面比对——09-16 的根因就写在这行里
#   （裸 `python` ⇒ SYSTEM 的 PATH 找不到 ⇒ 每天触发每天 127、日志一行不写），只查在位看不见它坏。
$krDefsOk = $false
if (Test-Path -LiteralPath $SvcDefsPath) {
    try { . $SvcDefsPath; $krDefsOk = $true } catch { $krDefsOk = $false }
}
if (-not $krDefsOk) {
    Write-Output "TASK|__service_definitions__|present=0|rule=none|age_h=na|state=defs-unreadable|enabled=na|action="
} elseif (-not $SvcTaskRoster -or @($SvcTaskRoster).Count -lt 1) {
    Write-Output "TASK|__task_roster__|present=0|rule=none|age_h=na|state=roster-empty|enabled=na|action="
} else {
    # 现网"像本仓命名家族"的任务名清单——**观测行，不参与判读**（bash 侧只转 INFO）。
    # 存在理由：present=0 有两种完全不同的成因——① 这台机器根本没装过这个任务；② 装了但名字
    #   与单源不一致（探针按单源的名字逐个查，改了名就查不到，而"改了名"正是手工时代会发生的事）。
    #   只看 present=0 分不清这两种，红项就得人工再上机查一遍；把同名族清单一次性带回来，
    #   absent 那行的旁边就有答案。★ 这一行是 10-09 首拨**之后**加的，不是首拨时用过的：
    #   那次只读到 quant-all-wd present=0，两种成因分不开（这正是它当时只能登记成"待处置"、
    #   不能顺手补注册体的原因——名字对不上就补 /Create，等于再造一条没人认领的腿）。
    # 为什么按前缀过滤而不是全量：Windows 自带几百条 \Microsoft\Windows\* 任务，全量清单会把
    #   我们那几条埋掉（观测面选错账本＝读数再多也不回答问题）。前缀只认 quant/qmt（PS 的
    #   -match 默认大小写不敏感），本仓任务名全是 ASCII，非 ASCII 字符剥掉、超 200 字符截断。
    # 位置在遍历**之前**：判读函数是单遍流式处理，absent 行要能引用这条清单，清单必须先到场。
    try {
        $krLive = @(Get-ScheduledTask -TaskPath '\' -ErrorAction Stop | ForEach-Object { [string]$_.TaskName })
        $krMine = @($krLive | Where-Object { $_ -match '^(quant|qmt)' } | Sort-Object)
        $krNames = ($krMine -join ",") -replace '[^\x20-\x7E,]', ''
        if ($krNames.Length -gt 200) { $krNames = $krNames.Substring(0, 200) }
        Write-Output ("TASK|__live_names__|present=" + $krMine.Count +
            "|rule=none|age_h=na|state=live-names|enabled=na|action=" + $krNames)
    } catch {
        Write-Output "TASK|__live_names__|present=0|rule=none|age_h=na|state=live-names-unreadable|enabled=na|action="
    }
    foreach ($krName in @($SvcTaskRoster)) {
        if (-not $krName) {
            Write-Output "TASK|__roster-item__|present=0|rule=none|age_h=na|state=item-empty|enabled=na|action="
            continue
        }
        # 规则解析三态：命中一条＝小时数；命中多条＝同名重复（编辑事故，判红）；
        # 零命中但在"仅查在位"表里＝inplace；零命中又不在那张表＝none＝新任务没定规则，判红逼出
        # 第二处刻意的决定（"它到底该多久跑一次"），而不是让探针默认放行。
        $krRuleTxt = "none"
        $krHits = @()
        if ($SvcTaskFreshRules) { $krHits = @($SvcTaskFreshRules | Where-Object { $_.Name -eq $krName }) }
        if ($krHits.Count -gt 1) { $krRuleTxt = "duplicated" }
        elseif ($krHits.Count -eq 1) { $krRuleTxt = [string]$krHits[0].MaxAgeHours }
        elseif ($SvcTaskInPlaceOnly -and (@($SvcTaskInPlaceOnly) -contains $krName)) { $krRuleTxt = "inplace" }
        $krPresent = "0"; $krAge = "na"; $krState = "absent"; $krEnabled = "na"; $krAction = ""
        $krLastRun = "na"; $krRegH = "na"; $krEnSrc = "none"; $krRegSrc = "none"
        # 三条**只观测、不判读**的自证键（10-10 二拨加）：定义对象是否取到（krObj）／那份 XML 是从
        # 哪一条通道来的（krXmlFrom：task-xml | schtasks-xml | none）／解析器说了什么（krXmlErr）。
        # 加它们的理由见下面解析块：二拨读到的 `en_src=none` 与 `reg_src=none` **有两种相反成因**
        # ——「文档没解析出来」与「解析出来了但那份 XML 里根本没有这个元素」，只看 src 分不开。
        $krObj = "no-obj"; $krXmlFrom = "none"; $krXmlErr = ""
        schtasks /Query /TN $krName 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) {
            $krPresent = "1"; $krState = "present"
            # 一次 Get-ScheduledTask 取两样东西：任务定义 XML（注册时刻）+ 动作行。
            # 分成两次调用不省事，反而多一个"两次读到不同版本"的窗口（现网中途重注册时）。
            $krTask = $null
            try { $krTask = Get-ScheduledTask -TaskName $krName -ErrorAction Stop } catch { $krTask = $null }
            # 定义侧的三把（Enabled / 注册龄 / 动作行）都从**任务 XML**取，不赌 CimInstance 的属性形状。
            # 为什么改这一条：10-09 夜间首拨的现网读数就是它自己教出来的——旧写法用
            # `[xml][string]$krTask.Task` 取注册节点，而 PS5.1 上 `.Task` 是个 CimInstance，
            # 字符串化得到的是类型名而不是那份 XML ⇒ **八条任务的 reg_h 全读成 na**；同一次拨测里
            # `PSObject.Properties['Enabled']` 在定义腿与运行信息腿上都没命中 ⇒ **enabled 也八条全 na**。
            # 两把一起失灵意味着"周期任务被禁用判红"与"从未运行按注册龄分型"在现网都是**结构性失灵**
            # （读数恒 na、bash 恒不判），而它们各自在门禁里的合成腿照绿——正是本批要根除的那一族，
            # 这次是被一次真拨测当场锤出来的，不是被读码想到的。
            # MSFT_ScheduledTask 的 `.Xml` 才是任务定义文本——**这一条到 10-10 二拨仍只是假设**，
            # 所以下面给它配了一条已证明活着的外部通道兜底；解析一次、三把各自取，
            # 任一把取不到只把它自己留成 na（一条读腿失败不连带弄坏另一条）。
            # ★ 10-10 二拨的现网读数：`.Xml` 这一式**仍然没给出值**（八条全 en_src=none、reg_src=none），
            #   而同一次读数里 `action=` 是有值的——动作行来自同一个 `$krTask` 对象的 `.Actions`，
            #   于是 `$krTask` 非空是**被读数证明了的**。这就把成因收窄成两件处置相反的事之一：
            #   ① `[xml]` 那一步抛了（这条属性在该机不给可用字符串）＝要换通道；
            #   ② 解析成功、但那份任务定义里**根本没有** `<Settings><Enabled>` / `<RegistrationInfo><Date>`
            #     节点（schtasks 造的任务常把"等于默认值"的节点整段省掉）＝要承认"XML 没写"这个事实本身，
            #     并从别处（任务对象的 State）取禁用信号。
            #   只看 src=none 分不开这两类，而按 ① 的假设去写 ② 的修法就是第三次白跑一趟。
            #   所以本批**不再猜第三种式子**，而是把区分它们的三把观测读法加进来
            #   （krObj / krXmlFrom / krXmlErr）+ 一条外部通道兜底，下一次拨测直接给结论。
            $krXml = $null
            if ($krTask) { try { $krXml = [xml][string]$krTask.Xml } catch { $krXml = $null; $krXmlErr = $_.Exception.Message } }
            if ($krTask) { $krObj = "ok" }
            # 第二条通道＝`schtasks /Query /TN <name> /XML`。为什么选它兜底而不是再换一个属性名：
            # 这条命令行在**本探针里已被证明活着**（present= 就是靠它的退出码 0 定的），
            # 而它的 stdout 就是任务定义 XML——那是微软对外的文档格式，比 cmdlet 对象的属性形状稳定得多。
            # 通道来源写进 krXmlFrom；两条通道都没拿到时，读数自己就说"XML 一面都没拿到"。
            if (-not $krXml) {
                try {
                    $krSbx = @(schtasks /Query /TN $krName /XML 2>$null)
                    $krSbTxt = ($krSbx -join "")
                    # 长度下限只用来挡"命令没输出／输出的是错误行"这一种形态，它不是判据、不参与红绿。
                    if ($krSbTxt.Length -gt 60) {
                        $krXml = [xml]$krSbTxt
                        if ($krXml) { $krXmlFrom = "schtasks-xml" }
                    }
                } catch { $krXml = $null; if (-not $krXmlErr) { $krXmlErr = $_.Exception.Message } }
            }
            if ($krXml -and $krXmlFrom -eq "none") { $krXmlFrom = "task-xml" }
            # ★ 但"换成 .Xml"本身还只是一个**新的赌注**：计划任务 XML 带默认命名空间，适配式访问
            #   （直接 $krXml.Task.Settings.Enabled）在带命名空间的文档上是否给值、`[string]` 一个 XmlElement
            #   给的是 InnerText 还是类型名——这两条我在这台机器上都**没法本机验**。而"没法验"正是上一个
            #   缺陷的出身（那次也是"我确定 `.Task` 那份是 XML"）。所以这里不选一条走到底，而是
            #   **两条都走 + 自报走了哪条**（en_src / reg_src 两个键；先按局部名取元素，它与命名空间无关，
            #   再退回适配式，且遇到元素对象就显式取 InnerText）。取向与 §N-5 服务 env 腿的
            #   `read=registry|nssm-text` 同一条先例：读数要么带出值，要么带出"哪条路都没通"，
            #   红项自己说清"是现网坏还是我的读法坏"，不需要再拿一趟上机去猜。
            # Enabled 的取值链四级：XML 按名 → XML 适配 → 定义对象属性 → 运行信息对象属性。
            # 注意**不要写 `[bool]$节点`**：PowerShell 里 `[bool]"false"` 是 $true（非空字符串即真），
            # 那会把"已禁用"洗成"已启用"，正好把这条判据反着弄坏——那不是读不到，是**读反**。
            # 所以按字面量映射成 bash 侧认的 "True"/"False" 拼写（与门禁合成读数同一口径），映射不上留 na。
            $krEnNode = ""
            if ($krXml) {
                try {
                    $krEnList = $krXml.GetElementsByTagName("Enabled")
                    if ($krEnList -and $krEnList.Count -gt 0) { $krEnNode = [string]$krEnList[0].InnerText; $krEnSrc = "xml-byname" }
                } catch { $krEnNode = "" }
                if (-not $krEnNode) {
                    try {
                        $krEnRaw = $krXml.Task.Settings.Enabled
                        if ($krEnRaw -is [System.Xml.XmlElement]) { $krEnNode = [string]$krEnRaw.InnerText } else { $krEnNode = [string]$krEnRaw }
                        if ($krEnNode) { $krEnSrc = "xml-adapter" }
                    } catch { $krEnNode = "" }
                }
            }
            if ($krEnNode -eq "true") { $krEnabled = "True" }
            elseif ($krEnNode -eq "false") { $krEnabled = "False" }
            elseif ($krTask -and $krTask.PSObject.Properties['Enabled']) { $krEnabled = [string]$krTask.Enabled; $krEnSrc = "defs-prop" }
            try {
                $krInfo = Get-ScheduledTaskInfo -TaskName $krName -ErrorAction Stop
                if ($krEnabled -eq "na" -and $krInfo.PSObject.Properties['Enabled']) { $krEnabled = [string]$krInfo.Enabled; $krEnSrc = "info-prop" }
                # 年代界用 2010 而不是 1900（2026-10-09 首拨实录逼出来的）：那次读出的
                # LastRunTime 反算 = 1999-11-30 00:01，是任务计划程序给"从未运行"的零值哨兵
                # 在 UTC+8 下的展开形态（另一形态是 1900-01-01）。旧判据只挡 1900 ⇒ 哨兵被当成
                # 真运行时间，算出 age=235445.9h 的荒谬读数并判红——**红得毫无信息量**。
                # 为什么不背哨兵日期：这台机器上所有任务都在 2025 年之后注册，任何"2010 年前跑过"
                # 在物理上不可能；与其赌哨兵的确切字节/日期（§DRILL-A 拿 --last 赌版本标志同族），
                # 不如用一个宽得多的年代界，让读法对形态变化免疫。
                if ($krInfo.LastRunTime -and $krInfo.LastRunTime.Year -gt 2010) {
                    $krAge = [string]([math]::Round(((Get-Date) - $krInfo.LastRunTime).TotalHours, 1))
                    $krState = "present+lastrun"
                    # 原始时间戳一并回显（定格式、纯 ASCII）：哨兵与真值的差别要能在读数里看见，
                    # 而不是靠人相信"我这个年代界判对了"。
                    $krLastRun = $krInfo.LastRunTime.ToString("yyyy-MM-dd HH:mm:ss")
                } else { $krAge = "never"; $krState = "never-run" }
            } catch { $krState = "info-unreadable"; $krAge = "na" }
            # 最后一级来源＝任务对象的 State（10-10 二拨后加，排在 XML 两级与两个对象属性级**之后**）。
            # 为什么这一级不是"再赌一个属性名"：这条判据要问的本来就是**这个任务现在会不会自己跑**，
            # 而 State=Disabled 正是那件事；反过来说，任务 XML 里没有 `<Enabled>` 节点恰恰等于
            # "没被禁用"（等于默认值的节点微软就是不写），那是一种**合法读数**而不是失灵。
            # 所以这里只认三种已知状态（Disabled / Ready / Running），别的状态一律留 na 不映射——
            # 宁可"读不出"也不要拿一个没见过值去把禁用判反（同一条里 [bool]"false" 恒真就是这种错的形状）。
            # src 仍写 defs-state：它给的是"从状态推出来的"，不是"读到的标志位"，读数不许冒充后者。
            if ($krEnabled -eq "na" -and $krTask) {
                $krSt = ""
                try { $krSt = [string]$krTask.State } catch { $krSt = "" }
                if ($krSt -eq "Disabled") { $krEnabled = "False"; $krEnSrc = "defs-state" }
                elseif ($krSt -eq "Ready" -or $krSt -eq "Running") { $krEnabled = "True"; $krEnSrc = "defs-state" }
            }
            # 注意这里**不回滚 $krEnabled**：Enabled 已由上面那条定义腿拿到过，TaskInfo 失败
            # 只影响"上次运行/状态"那一对读数。旧写法在 catch 里把 enabled 一并抹成 na，
            # 等于让一条腿的失败连带把另一条腿的好读数丢掉（禁用判定因此静默失效）。
            # 注册龄（小时）＝这个任务在这台机器上装了多久。为什么探针要问这一把：
            #   部署步 [2e] 每次都用 `schtasks /Create /F` 删建重注册 quant-backup-snap，重注册
            #   会把运行历史清零，于是"上次运行"永远读成从未运行——而它的触发点是次日 04:00。
            #   只看"上次运行"的判据在发版日必然红，与故障无关。有了注册龄，bash 侧能分开两种
            #   从未运行：装了还没到触发点（正常）与装了远超阈值仍没跑过一次（该跑没跑，红）。
            # 为什么算在 PS 一侧而不是把日期串交给 bash 算：现网时钟在那台机器上，而本机做日期
            #   减法要同时伺候 BSD `date -j -f` 与 GNU `date -d` 两套语法——把只有正确时钟的一侧
            #   能算对的量留在那一侧，bash 只比大小（与 age_h 同一口径）。
            # 注册时刻同样只用上面那一次解析成果（不另起一次 Get-ScheduledTask——那会多出
            #   "两次读到不同版本"的窗口）。为什么必须用 $krXml 而不是 $krTask：见上面那段首拨实录，
            #   `.Task` 字符串化得到类型名，`[xml]` 解析它必抛，于是整块进 catch、读数恒 na，
            #   而恒 na 在 bash 侧是"注册龄读不出"的 fail-closed 红项：**红的原因不是现网坏，
            #   是我的读法在那台机器上从来没走到过能算出数的那条路**。
            # 取元素的两条路与 Enabled 那四级的主干相同：先按局部名（与命名空间无关），
            #   再退回适配式；两条都不通就留 na 并把 src 写成 none，别让"我换了个式子"冒充"我读到了"。
            # 年代界（2010，与"上次运行"那把同源）在这里**不是防现网坏，是防我自己取错元素**：
            #   GetElementsByTagName("Date") 按名字取，任务 XML 里若还有别的 Date 元素，拿到的就是别的时刻；
            #   一个过老的日期正是那种错读的形状，所以取错就当读不出，而不是拿假龄去比阈值。
            $krRegNode = ""
            if ($krXml) {
                try {
                    $krRegList = $krXml.GetElementsByTagName("Date")
                    if ($krRegList -and $krRegList.Count -gt 0) { $krRegNode = [string]$krRegList[0].InnerText; $krRegSrc = "xml-byname" }
                } catch { $krRegNode = "" }
                if (-not $krRegNode) {
                    try {
                        $krRegRaw = $krXml.Task.RegistrationInfo.Date
                        if ($krRegRaw -is [System.Xml.XmlElement]) { $krRegNode = [string]$krRegRaw.InnerText } else { $krRegNode = [string]$krRegRaw }
                        if ($krRegNode) { $krRegSrc = "xml-adapter" }
                    } catch { $krRegNode = "" }
                }
            }
            if ($krRegNode) {
                try {
                    $krRegDt = [datetime]::Parse($krRegNode, [System.Globalization.CultureInfo]::InvariantCulture)
                    if ($krRegDt.Year -gt 2010) {
                        $krRegH = [string]([math]::Round(((Get-Date) - $krRegDt).TotalHours, 1))
                    } else { $krRegH = "na"; $krRegSrc = "implausible" }
                } catch { $krRegH = "na"; $krRegSrc = "parse-failed" }
            }
            if ($krTask) {
                $krParts = @()
                foreach ($krA in @($krTask.Actions)) {
                    if ($krA.Execute) {
                        $krOne = [string]$krA.Execute
                        if ($krA.Arguments) { $krOne = $krOne + " " + [string]$krA.Arguments }
                        $krParts += $krOne
                    }
                }
                # 多动作行全量拼接：不做"取第一条"的乐观截断——截断会让人以为看见了现网全貌。
                $krAction = ($krParts -join " || ")
            } else { $krAction = "action-unreadable" }
        }
        $krAction = ($krAction -replace '[^\x20-\x7E]', '')
        if ($krAction.Length -gt 160) { $krAction = $krAction.Substring(0, 160) }
        # 解析器那句话要能带回来，但它是**异常文本**、不是数据：非 ASCII 一律剥掉（zh-CN 的
        # Get-ScheduledTask 报错就是中文），剥完空了又原本有值 ⇒ 只写一个 non-ascii 标记，
        # 免得把整句话挤进读数行（行长是这条协议的隐形约束，超了会在 bash 侧劈行）。
        # ★ 竖线必须先中和成加号：`[xml]` 的失败信息里会**把原始输入整段带回来**，而 xml_err 这一键
        #   排在协议行的**中段**（action 之前）。bash 侧按「最后一处 |键名=」抽值，所以异常文本一旦
        #   自己带出 `|enabled=` / `|rule=` 这种形状，判读键就会被抽成那段文本＝读数自己把自己的
        #   判据弄反（本探针要根除的"供给侧假读数"一族，这次成因换成值撑破协议）。action 是唯一排在
        #   行尾、且 bash 用"取后半整串"抽的键，所以它的 `||` 分隔符不动（动了会弄坏既有读数口径）。
        $krXmlErrRaw = $krXmlErr
        $krXmlErr = ($krXmlErr -replace '[^\x20-\x7E]', '')
        $krXmlErr = ($krXmlErr -replace '\|', '+')
        if ($krXmlErrRaw -and -not $krXmlErr) { $krXmlErr = "non-ascii" }
        if ($krXmlErr.Length -gt 80) { $krXmlErr = $krXmlErr.Substring(0, 80) }
        Write-Output ("TASK|" + $krName + "|present=" + $krPresent + "|rule=" + $krRuleTxt +
            "|age_h=" + $krAge + "|state=" + $krState + "|enabled=" + $krEnabled +
            "|reg_h=" + $krRegH + "|last_run=" + $krLastRun +
            "|en_src=" + $krEnSrc + "|reg_src=" + $krRegSrc +
            "|obj=" + $krObj + "|xml_from=" + $krXmlFrom + "|xml_err=" + $krXmlErr +
            "|action=" + $krAction)
    }
}
PSEOF

# PS 5.1 无 BOM 的 UTF-8 文件按 GBK 解析——中文注释会撕裂字符串字面量直接 ParserError，
# 所以上传前补 UTF-8 BOM（deploy/qmt-win/ 各 PS1 同款约定）。
printf '\357\273\277' | cat - "$PROBES" > "$PROBES.bom" && mv "$PROBES.bom" "$PROBES"
$SCP "$PROBES" "${GZ_USER}@${GZ_IP}:${DEPLOY_DIR}/verify_probes.ps1" 2>/dev/null

# ── §KA-TASKREG 第 32 探针的判读（第 32 探针／bash 侧，2026-10-07 波 3）───────────────────
# 输入：PS 回传的 `TASK|<name>|present=..|rule=..|age_h=..|state=..|enabled=..|reg_h=..|last_run=..|en_src=..|reg_src=..|action=..` 行；
# 输出：每任务一行 INFO|（绿也要看得到数）+ **恰好一行** PASS| 或 FAIL|。
# 为什么判读在 bash 而不在 PS：本机没有 PowerShell，判据写在 PS 就永远只能是"从没真跑过的判据"
#   （§0929DRILL 四条缺陷的共同根因）。写成纯 bash 之后，门禁 §110 能直接喂合成读数逐条验
#   红绿（在位新鲜／过期／缺任务／未定规则／读不到上次运行／从未运行但刚装好／从未运行且早该跑过／
#   仅查在位／整段无读数），零网络。
# 解析口径四条，每条都是踩过的坑：
#   ① 按 `|key=` 前缀剥字段而不是按位置 split——action= 里可能有 `|`（多动作行拼接），
#      按位置取会把动作行的后半当成 enabled/present 读，整行错位还看不出错位；
#   ② 比较走 awk 一次退出码判断，不在 bash 里做浮点（bash 算术只认整数，
#      "12.3 > 30" 这种读分会静默语法错）；
#   ③ 计数只数 `TASK|` 开头的行，汇总行自己不再产生 TASK| 前缀——否则"统计 FAIL 行数"这类
#      下游读法会把我自己打印的那行 PASS/FAIL 也数进去（§107 同族：观测面选错账本）；
#   ④ 新增的 reg_h/last_run 两个键**取不到就是空**，空值一律走 fail-closed（三条伪读数行本来就
#      不带它们）——判据不能因为"读数里没有这个键"就退化成放行（§110 有这条的反证腿）。
judge_task_roster() {
	local line name pres rule age state en act reg lr ens rgs obj xf xe
	local n=0 absent="" stale="" norule="" unread="" disabled="" notrun="" liveNames=""
	while IFS= read -r line; do
		case "$line" in
		TASK\|*) ;;
		*) continue ;;
		esac
		name="${line#TASK|}"
		name="${name%%|*}"
		pres="$(printf '%s' "$line" | sed -n 's/.*|present=\([^|]*\).*/\1/p')"
		rule="$(printf '%s' "$line" | sed -n 's/.*|rule=\([^|]*\).*/\1/p')"
		age="$(printf '%s' "$line" | sed -n 's/.*|age_h=\([^|]*\).*/\1/p')"
		state="$(printf '%s' "$line" | sed -n 's/.*|state=\([^|]*\).*/\1/p')"
		en="$(printf '%s' "$line" | sed -n 's/.*|enabled=\([^|]*\).*/\1/p')"
		reg="$(printf '%s' "$line" | sed -n 's/.*|reg_h=\([^|]*\).*/\1/p')"
		lr="$(printf '%s' "$line" | sed -n 's/.*|last_run=\([^|]*\).*/\1/p')"
		# 读法来源两把**只观测、不判读**（不参与任何红绿分支）：它们回答的是"这条 enabled 是从
		# 哪一式读来的／为什么没读来"，把它当健康度会把"我的式子换了条路"读成现网变化。
		# 缺键就是空串（三条伪读数行与旧格式读数都没有这两个键），空串在 INFO 里显示 na。
		ens="$(printf '%s' "$line" | sed -n 's/.*|en_src=\([^|]*\).*/\1/p')"
		rgs="$(printf '%s' "$line" | sed -n 's/.*|reg_src=\([^|]*\).*/\1/p')"
		# 三把**也只观测、不判读**（10-10 二拨加）：它们回答的是「XML 到底拿到没有、从哪条通道拿的、
		# 解析器抱怨了什么」。src=none 单独一个值分不开"没解析出文档"与"文档里没有那个节点"，
		# 而这两种成因的处置相反（换通道 vs 承认默认值不写），所以要把区分它们的读数一次带回现网。
		obj="$(printf '%s' "$line" | sed -n 's/.*|obj=\([^|]*\).*/\1/p')"
		xf="$(printf '%s' "$line" | sed -n 's/.*|xml_from=\([^|]*\).*/\1/p')"
		xe="$(printf '%s' "$line" | sed -n 's/.*|xml_err=\([^|]*\).*/\1/p')"
		act="${line#*|action=}"
		if [ "$act" = "$line" ]; then act=""; fi
		n=$((n + 1))
		if [ "$name" = "__live_names__" ]; then
			# 观测行：`present=` 在这条里是"现网同族任务条数"，不是判据，所以**不能**落到下面的
			# 在位判定去（否则会凭空多出一条 absent 红）。它只回答一个问题：那些 present=0 的任务
			# 是"没装"还是"装了但名字不同"。
			liveNames="${act}"
			echo "INFO|ops:task_roster 现网同族任务 ${pres} 条：${act:-（空清单）}"
			continue
		fi
		if [ "$name" = "__service_definitions__" ] || [ "$name" = "__task_roster__" ] || [ "$name" = "__roster-item__" ]; then
			echo "INFO|ops:task_roster ${name} state=${state:-?}（单源没读到＝第 32 探针没有可查对象，判红而不是放行）"
			norule="${norule} ${name}:${state:-defs}"
			continue
		fi
		if [ "$pres" != "1" ]; then
			absent="${absent} ${name}(state=${state:-?})"
			echo "INFO|ops:task_roster name=${name} present=0 state=${state:-?} live_names=${liveNames:-未回传}"
			continue
		fi
		if [ "$rule" = "inplace" ]; then
			# ONLOGON/ONSTART：只查在位，不拿"上次运行时间"当健康度（结构上不由时钟决定）。
			echo "INFO|ops:task_roster name=${name} present=1 rule=inplace(不判新鲜度) enabled=${en:-na} en_src=${ens:-na} reg_h=${reg:-na} reg_src=${rgs:-na} obj=${obj:-na} xml_from=${xf:-na} xml_err=${xe:-none} action=${act}"
			continue
		fi
		case "$rule" in
		'' | *[!0-9]*)
			# 既不是数字也不是 inplace＝规则没定／同名重复（PS 侧回显 none|duplicated）：
			# 新加了任务却没定"该多久跑一次"，判红逼出第二处刻意的决定，而不是默认放行。
			norule="${norule} ${name}(rule=${rule:-empty})"
			echo "INFO|ops:task_roster name=${name} present=1 rule=${rule:-empty} => no-freshness-rule"
			continue
			;;
		esac
		if [ "$age" = "never" ]; then
			# PS 侧认定"从未运行"（上次运行是零值哨兵或被年代界挡下）。这里必须把它拆成两种，
			# 因为两者的处置完全相反：
			#   ① 注册龄还在阈值内＝刚装上来、触发点还没到 ⇒ 正常。不拆开就会在**每个发版日**
			#     凭空红一条（部署步 [2e] 用 schtasks /Create /F 删建重注册快照任务，运行历史被清零
			#     而触发点是次日 04:00）——10-09 首拨就是这条形态，当时的读数是 age=235445.9h 的
			#     荒谬值（哨兵没被挡住），挡住之后会变成"从未运行"，如果不看注册龄就还是同一枚假红、
			#     只是换了个数字（§107 DRILL-C：永远红的锁教出来的是所有人忽略红）。
			#   ② 注册龄已超阈值却一次都没跑过＝该跑没跑 ⇒ 红（这条才是本探针要的判据）。
			# 注册龄读不出（缺失/na/负数）＝fail-closed 归到②那一侧点名，理由与"上次运行读不出"同：
			#   本机验不了现网能不能读到任务 XML，读不到就当健康是反向失效。
			case "$reg" in
			'' | na | -* | *[!0-9.]*)
				notrun="${notrun} ${name}(age=never;reg_h=${reg:-missing})"
				echo "INFO|ops:task_roster name=${name} present=1 rule=${rule}h age=never reg_h=${reg:-missing} reg_src=${rgs:-na} enabled=${en:-na} en_src=${ens:-na} => 从未运行且注册龄读不出 obj=${obj:-na} xml_from=${xf:-na} xml_err=${xe:-none}"
				;;
			*)
				if awk "BEGIN{exit !($reg > $rule)}" 2>/dev/null; then
					notrun="${notrun} ${name}(age=never;reg_h=${reg}>rule=${rule}h)"
					echo "INFO|ops:task_roster name=${name} present=1 rule=${rule}h age=never reg_h=${reg}h reg_src=${rgs:-na} enabled=${en:-na} en_src=${ens:-na} => 从未运行且注册龄超阈值 obj=${obj:-na} xml_from=${xf:-na} xml_err=${xe:-none}"
				else
					echo "INFO|ops:task_roster name=${name} present=1 rule=${rule}h age=never reg_h=${reg}h reg_src=${rgs:-na} enabled=${en:-na} en_src=${ens:-na} => 刚注册未到触发点(不判红) obj=${obj:-na} xml_from=${xf:-na} xml_err=${xe:-none} action=${act}"
				fi
				;;
			esac
			continue
		fi
		case "$age" in
		'' | na | *[!0-9.]*)
			# 负 age 落在这里（`-` 不是 [0-9.]）但**不能和"读不出"混成一个标签**：
			# 一台时钟快了几天的机器会用"未来时间"把停更的任务洗成常绿，这条必须单独点名。
			local why="unreadable"
			case "$age" in -*) why="clock-skew" ;; esac
			unread="${unread} ${name}(age=${age:-empty};${why};state=${state:-?})"
			echo "INFO|ops:task_roster name=${name} present=1 rule=${rule}h age=${age:-empty} => ${why}"
			continue
			;;
		esac
		if awk "BEGIN{exit !($age > $rule)}" 2>/dev/null; then
			stale="${stale} ${name}(age=${age}h>rule=${rule}h)"
		fi
		if [ "$en" = "False" ]; then
			# 周期守护被 /disable 着长期停着＝现网止损动作忘了复原（CHECKLIST 的 disable 是临时的）。
			disabled="${disabled} ${name}"
		fi
		echo "INFO|ops:task_roster name=${name} present=1 rule=${rule}h age=${age}h enabled=${en:-na} en_src=${ens:-na} lastrun=${lr:-na} reg_h=${reg:-na} reg_src=${rgs:-na} obj=${obj:-na} xml_from=${xf:-na} xml_err=${xe:-none} action=${act}"
	done
	# 正锁：一条读数都没有＝PS 那一段整个没走到（被前面的异常吞掉、或 heredoc 里被误删）。
	# "没读数"绝不能算绿，也不能只打一行 INFO 就过去（§70 派生空清单正锁同族）。
	if [ "$n" -eq 0 ]; then
		echo "FAIL|ops:scheduled-task roster in place + periodic tasks fresh|no-task-readings（PS 侧一段都没回传，判据失明而不是"全部健康"）"
		return 0
	fi
	local bad=""
	[ -n "$absent" ] && bad="${bad} absent=${absent}"
	[ -n "$stale" ] && bad="${bad} stale=${stale}"
	[ -n "$norule" ] && bad="${bad} no-freshness-rule=${norule}"
	[ -n "$unread" ] && bad="${bad} last-run-unreadable=${unread}"
	[ -n "$notrun" ] && bad="${bad} never-run-beyond-rule=${notrun}"
	[ -n "$disabled" ] && bad="${bad} disabled=${disabled}"
	if [ -n "$bad" ]; then
		echo "FAIL|ops:scheduled-task roster in place + periodic tasks fresh|${bad}"
	else
		echo "PASS|ops:scheduled-task roster in place + periodic tasks fresh"
	fi
	return 0
}

echo "== verify_deploy_guangzhou @ ${GZ_IP}（期望 buildCommit=${COMMIT}）=="
out=$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${DEPLOY_DIR}/verify_probes.ps1 -Commit ${COMMIT} -EnginePort ${ENGINE_PORT} -WebPort ${WEB_PORT} -GwPort ${GW_PORT} -DataDir ${DATA_DIR} -BackupDir ${BACKUP_DIR} -SnapDir ${SNAP_DIR} -MockUatDir ${MOCK_UAT_DIR} -MockPort ${MOCK_PORT} -MockDecomScript ${QMT_WIN_DIR}/decommission_qmt_mock.ps1 -GatewayCfg ${GW_CFG} -GwTokenService ${GW_TOKEN_SVC} -SvcDefsPath ${QMT_WIN_DIR}/service_definitions.ps1 -OpsWinDir ${QMT_WIN_DIR} -OpsScriptsDir ${OPS_SCRIPTS_DIR} -GwPyDir ${GW_PY_DIR} -ResticRepoDir ${RESTIC_REPO_DIR} -ResticPassFile ${RESTIC_PASS_FILE} -DeployDir ${DEPLOY_DIR}" 2>&1 | LC_ALL=C tr -d '\r')

# 第 32 探针的读数行先摘出来判读（TASK| 不是 PASS/FAIL/INFO 三种协议行之一，
# 直接丢进下面的计数循环会落进 case 的三个分支之外被**静默丢弃**——那正是要防的"探针跑了
# 但没人判"）。判读函数只产出一行 PASS/FAIL + 若干 INFO，再并回原流，判数因此恰好 +1。
TASK_LINES=$(printf '%s
' "$out" | grep '^TASK|' || true)
out=$(printf '%s
' "$out" | grep -v '^TASK|' || true)
VERDICT=$(printf '%s
' "$TASK_LINES" | judge_task_roster)
out="${out}
${VERDICT}"

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
