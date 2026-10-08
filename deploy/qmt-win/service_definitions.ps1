# service_definitions.ps1 — §C7-OPS（2026-09-26，FIX_PLAN_20260925EVE ⑯）Windows 部署
# 「服务/任务定义」唯一来源（dot-source 配置片段，样式承袭 §H8 service_probe_config.ps1）。
#
# 消费方：all_service_watchdog.ps1、gateway_watchdog.ps1、register_engine_services.ps1、
#         register_service.ps1、rotate_qmt_token.ps1、ensure_gateway_config.ps1。
# 分工：端口/探针 URL 仍归 §H8 service_probe_config.ps1（本文件 dot-source 它并对外转发，
#       §H8 既有静态锁不动）；本文件管「服务名、计划任务名、NSSM 路径、网关目录与绑定面」。
#
# 要根除的缺陷（§C7 Windows 部署三径并存，审计锤实 docs/AUDIT_20260925EVE_全量评价报告.md C7）：
#   ① 拉起方式打架：register_service.ps1（08-31 实障裁决）说网关**只能**跑交互会话计划任务
#      （xtquant↔QMT 客户端走按会话隔离的共享内存，Session 0 恒连不上），
#      gateway_watchdog.ps1 却自带 SYSTEM 计划任务安装说明（Session 0 路径），
#      all_service_watchdog.ps1 更把 qmt_gateway 当 NSSM 服务 status/restart——而现网 NSSM
#      服务只有四个（verify_deploy_guangzhou.sh 探针①清单 quant/quant-research/pydata/quant-web，
#      不含网关）⇒ 网关守护腿在旧径上是空转（nssm restart 一个不存在的服务，永远不恢复）。
#   ② 绑定面打架：gateway_watchdog.ps1 注 QUANT_GATEWAY_BIND=0.0.0.0:8789 + ALLOWED_IPS 含首尔出口，
#      install_guangzhou.ps1:118,131-132 把引擎 gateway_url 指到 127.0.0.1:8789 且防火墙 8789
#      收严 remoteip=127.0.0.1，docs/MIGRATION_GUANGZHOU_ALLINONE.md §3.5/R2 裁决移除首尔 IP，
#      service_probe_config.ps1 头注「全部 127.0.0.1 回环绑定」，gateway.py 在无显式 BIND 时还会
#      把 0.0.0.0 自动收敛为 127.0.0.1 ⇒ 现网形态＝仅回环，公网 0.0.0.0 是首尔→广州分离期的旧物。
#   ③ 路径/任务名各写各的：网关目录（C:\qmt\quant-trading-v2\qmt_gateway vs register_service 的
#      仓库相对推导）、nssm.exe 四个候选位（C:\qmt\nssm 旧位 / $PSScriptRoot\tools /
#      C:\opt\quant\qmt-win\tools / C:\opt\quant\deploy\qmt-win\tools，verify_deploy_guangzhou.sh
#      :241-243 注释直陈「现网＝第一个」）、任务名散落在五个脚本里。
#   ④ token 腿错位：rotate_qmt_token.ps1 写进 quant 服务 AppEnvironmentExtra 的
#      QUANT_GATEWAY_TOKEN 对网关进程只是冗余（网关由交互任务拉起，不继承 NSSM 服务 env；
#      gateway.py :186/:199 的 env 覆盖读的是网关**自己进程**的环境变量）——
#      网关的真源＝$SvcGatewayConfigFile（本文件定义），回读自证见 rotate_qmt_token.ps1 §C7-OPS 段。
#
# 现网形态判定依据（一切以仓库内脚本/文档反推，本机不触生产）：
#   deploy_guangzhou.sh:48-50（DEPLOY_DIR=C:/opt/quant、DATA_DIR=C:/var/lib/quant-trading-v2、
#   QMT_GATEWAY_DIR=C:/qmt/quant-trading-v2/qmt_gateway）、:85（NSSM 现网位）；
#   verify_deploy_guangzhou.sh:81,108,110-111（GW_PORT=8789、GatewayCfg、GwTokenService=quant）、
#   :241-243（nssm 候选位注释）、探针①（NSSM 四服务清单）；
#   docs/RUNBOOK_QMT_DAILY.md:19,89-90（网关由 QMT-Gateway-Logon/-Ensure 维持；失联修复走
#   schtasks /change + /run QMT-Gateway-Ensure）；install_guangzhou.ps1:118,131-132（回环收严）；
#   deploy/qmt-win/CHECKLIST.md 常驻化段（交互任务裁决 + 勿用 NSSM）。
#
# 编码规矩：本文件带单个 UTF-8 BOM（PS 5.1 无 BOM 按 GBK 读中文会撕裂字面量）；行尾与同目录
# 主流一致（LF，file 实证邻居均为 LF）；上传链 ps1_bom 只管 BOM 不管行尾，幂等（先剥再补）。

# ── §H8 端口/URL 单源转发（消费者 dot-source 本文件后即同时拿到端口定义）────────────
# Join-Path $PSScriptRoot 在被 dot-source 的函数体内求值 ⇒ 兜底位指向**消费方**自身目录，
# 正是各脚本自己的落盘位置（现网 C:\opt\quant\qmt-win\）。
if ($null -eq $ProbeGatewayPort) {
    $__svcProbeCfg = Join-Path $PSScriptRoot "service_probe_config.ps1"
    if (Test-Path $__svcProbeCfg) { . $__svcProbeCfg }
}
if ($null -eq $ProbeGatewayPort) {
    Write-Warning "service_definitions: service_probe_config.ps1 未找到（§H8 端口单源脱钩）"
}

# ── NSSM 服务（四服务＝现网唯一 NSSM 清单；qmt_gateway **不在**其中，见头注①）──────
$SvcNameQuant     = if ($ProbeQuantName)    { $ProbeQuantName }    else { "quant" }
$SvcNameResearch  = if ($ProbeResearchName) { $ProbeResearchName } else { "quant-research" }
$SvcNamePydata    = if ($ProbePydataName)   { $ProbePydataName }   else { "pydata" }
$SvcNameWeb       = if ($ProbeWebName)      { $ProbeWebName }      else { "quant-web" }
$SvcNssmServices  = @($SvcNameQuant, $SvcNameResearch, $SvcNamePydata, $SvcNameWeb)
# 历史遗留的 NSSM 网关服务名（register_service.ps1 负责停用/禁用，绝不可复活为拉起路径）
$SvcLegacyGwServiceNames = @("qmt-gateway", "quant-gateway", "qmt_gateway")

# ── 计划任务（现网唯一守护清单）────────────────────────────────────────────────────
$SvcTaskGatewayEnsure   = "QMT-Gateway-Ensure"     # 网关幂等守护：每 5 分钟，8789 未监听才拉起（交互会话）
$SvcTaskGatewayLogon    = "QMT-Gateway-Logon"      # 网关登录即拉起（交互会话）
$SvcGatewayEnsureIntervalMin = 5
$SvcTaskQmtctl          = "QMT-Ensure-Running"     # qmtctl 客户端守护（交互会话，每 10 分钟）
$SvcQmtctlIntervalMin   = 10
$SvcTaskLogPrune        = "Quant-Log-Prune"        # 日志清理（每日 07:30）
# ⚠ 这一行原来的行尾注释写着"在跑"——那是**引用 RUNBOOK §1 的说法，不是读数**。
#   2026-10-09 第 32 探针首拨实测按这个名字查＝present=0（这台机器上没有叫 `quant-all-wd` 的计划任务）。
#   注释与现网不一致时以现网为准，并把不一致留在原地当证据（铲掉注释＝铲掉这次反证）。
#   ★ 只到"按这个名字查不到"为止，**不写成"从来没注册过"**：探针按单源的名字逐个查，
#   现网若装成了别的名字，这条读数同样是 present=0，而两种成因的处置相反（一种要补注册，
#   一种要先查是谁改的名）。分型靠探针本轮新加的 __live_names__ 观测行，分型之前不写注册体；
#   登记与处置动作见 RUNBOOK §4.1b.16。
$SvcTaskAllWatchdog     = "quant-all-wd"           # 全服务 watchdog（10-09 首拨读数 present=0；是否装了别名＝待 __live_names__ 分型）
$SvcTaskBackupSnap      = "quant-backup-snap"      # 夜间快照（deploy_guangzhou.sh [6/6] 触发位）
# §KA-TASKREG 同族的第三条（10-09 批）：08:40 把"杀手任务"QMT-Ensure-Running 重新 enable 并触发一次。
#   它的存在理由写在 docs/MIGRATION_QMT_DUAL_PATH.md:259 与 RUNBOOK §1 的节奏表第一行——白天测试窗口
#   会临时 /disable 那个每 10 分钟杀 XtMiniQmt 的任务，靠这条每天早盘把它收回来。执行体
#   scripts/enable_ensure.ps1 自 §0929OPS-⑪-1 起随部署下发到 ${DEPLOY_DIR}\scripts\（第 29 探针查在位），
#   **任务本体同样是 2026-09-10 手工 schtasks 出来的**，与本文件此前的名单里没有它＝同一枚缺陷：
#   名单里没有＝第 32 探针不查它＝"换机/误删后没人知道这条腿没了"。本批把它补进全集。
#   补进名单的取向：先补**名单**（判据），注册体等读数区分开"根本没装"与"装了但改名"之后再定
#   （探针本轮新增的 __live_names__ 观测行就是为这件事加的——名字对不上时补注册体会造出第二条腿）。
$SvcTaskEnsureRestore   = "QMT-Ensure-Restore-0840"           # 工作日 08:40 复原杀手任务

# ── §KA-TASKREG（2026-10-07 修复批 波 3）：17:10 盘后保活任务的注册入口 + 任务全集单源 ──────
# 缺陷本体（§AUDIT_20261005 P1-D，本机读码锤实，全程未触生产）：
#   scripts/dataload_keepalive.py 是 daily / index_daily / ths_limit_up_daily 三表的盘后补数执行体，
#   §0927KA 已把它收进 deploy_guangzhou.sh 的 scp 清单（**脚本面**到位了），但**计划任务本身
#   从来没有注册体**：register_engine_services.ps1 只创建 QMT-Ensure-Running 与 Quant-Log-Prune
#   两个任务，本文件此前的任务表也没有这一项——现网那个 QMT-Dataload-KeepAlive 是 2026-09-16
#   手工 schtasks /Create 出来的（RUNBOOK §1 记录的正是那次手工修，含"裸 python 必 127 静默失败"
#   这个根因）。⇒ 三条后果：① 换机/重建现网时这条腿不会自己回来；② 任务被误删后无脚本可复原；
#   ③ 部署面也没有"任务在位"判据，所以"脚本每晚都在、日线却停更"只能靠人肉发现。
#   这是本仓第 N 次「有脚本无调度」同族（§ENH-5 网关 py 漏清单、§P0-B 备份脚本手工安装、
#   §0927KA keepalive 不在清单、§0929DRILL 演练/夜间验收"有脚本无 launchd"），
#   每一次的形态都一样：判据写得很完整，只是没有钟去触发它，于是纸面上永远成立。
# 修法三段（缺一段就回到同一个形态）：
#   ① 本节＝任务名 / 触发时点 / 执行体路径的单源；
#   ② register_engine_services.ps1 §KA-TASKREG＝注册体（本仓纪律 §0929OPS-⑪：运维脚本**只上传
#      不自动执行**，schtasks /Create 属现网特权变更，必须 owner 当面看预演读数后执行）；
#   ③ verify_deploy_guangzhou.sh 第 32 探针＝按 $SvcTaskRoster 逐个查"在位"+ 周期任务查
#      "上次运行新鲜度"。PS 侧只回 ASCII 读数、判读函数在 bash 里单实现——理由是**本机没有
#      PowerShell**，判读放 PS 就永远无法离线自证三态（绿/过期红/缺任务红）。
$SvcTaskDataloadKeepAlive = "QMT-Dataload-KeepAlive"   # 现网名（RUNBOOK §1），/ru SYSTEM
$SvcKeepaliveDailyAt      = "17:10"                    # 每日盘后；脚本自身幂等（三表拉齐即退出）
# ⚠ 绝对路径不是风格问题：09-16 断供 5 日的根因就是任务动作行写了裸 `python`，
#   而 SYSTEM 账号的 PATH 里没有 python ⇒ 每天触发、每天退出码 127、日志一行不写、无人知晓。
$SvcKeepalivePythonExe    = "C:\Python312\python.exe"
$SvcKeepaliveScript       = "C:\opt\quant\dataload_keepalive.py"   # deploy_guangzhou.sh:211 的落盘位
$SvcKeepaliveLog          = "C:\opt\quant\dataload_keepalive.log"  # 脚本 log() 与 cmd 重定向同名（已加固）

# ── 任务全集（部署面"这台机器该有哪些计划任务"的唯一答案）──────────────────────────────
# 第 32 探针按这个集合逐个查，不再在各脚本里各写各的名单；
# 新增任务只改这里一处，探针自动覆盖（清单式锁会漏下一个新增项，§BOM-REPO-DERIVE / §107
# 派生正锁两条同族教训：能派生的就不要写死）。
$SvcTaskRoster = @(
    $SvcTaskGatewayEnsure,
    $SvcTaskGatewayLogon,
    $SvcTaskQmtctl,
    $SvcTaskLogPrune,
    $SvcTaskAllWatchdog,
    $SvcTaskBackupSnap,
    $SvcTaskDataloadKeepAlive,
    $SvcTaskEnsureRestore
)

# ── 新鲜度规则（阈值按**触发周期**定，全仓只此一份）───────────────────────────────────
# 为什么用 pscustomobject 数组而不是 hashtable 字面量：`@{ $var = 2 }` 的键求值在 PS5.1 上
# 是可用的但可读性差、且拼错变量名会静默造出一个空键（探针查不到规则⇒要么恒红要么恒绿）。
# 数组形状让"任务名"与"该多久跑一次"成对出现，加一条就写一行。
# 10-09 追加第六条（QMT-Ensure-Restore-0840）时的一条取向：**阈值不是统一抄 30，而是按它自己的
# 触发日历算**——工作日 08:40 的合法最长间隔是周五 08:40 → 周一 08:40 = 72h，给一天余量取 96h。
# 写 30 的话，每个周一早上的体检都会在系统完全健康时判红，那就是 §107 DRILL-C 那一课的重演。
$SvcTaskFreshRules = @(
    [pscustomobject]@{ Name = $SvcTaskGatewayEnsure;     MaxAgeHours = 2 },   # 每 5 分钟 ⇒ 2h＝24 个周期没跑
    [pscustomobject]@{ Name = $SvcTaskQmtctl;            MaxAgeHours = 4 },   # 每 10 分钟 ⇒ 4h＝24 个周期
    [pscustomobject]@{ Name = $SvcTaskLogPrune;          MaxAgeHours = 30 },  # 每日 07:30 ⇒ 一天 + 余量
    [pscustomobject]@{ Name = $SvcTaskBackupSnap;        MaxAgeHours = 30 },  # 每日 04:00 ⇒ 与 §P0-B 标记阈值同数
    [pscustomobject]@{ Name = $SvcTaskDataloadKeepAlive; MaxAgeHours = 30 },  # 每日 17:10 ⇒ 一天 + 余量
    [pscustomobject]@{ Name = $SvcTaskEnsureRestore;     MaxAgeHours = 96 }   # 工作日 08:40 ⇒ 周末 72h + 一天
)
# 非周期触发（ONLOGON / ONSTART）的任务**不进** $SvcTaskFreshRules：它们的上次运行时间由
# "有没有人登录 / 机器有没有重启"决定，不由时钟决定。拿它判新鲜度＝在一台可以连续运行数周
# 不重立的服务器上造出**结构性必红**（§107 DRILL-C 的教训：一条在健康现网上永远红的锁，
# 结局是所有人学会忽略它，真正的红也就没人看了）。
$SvcTaskInPlaceOnly = @($SvcTaskGatewayLogon, $SvcTaskAllWatchdog)

# ── nssm.exe 落位（verify_deploy_guangzhou.sh:241 注释直陈：现网＝第一个）───────────
$SvcNssmCandidates = @(
    "C:\opt\quant\qmt-win\tools\nssm-2.24\win64\nssm.exe",        # deploy_guangzhou.sh:85 现网位
    "C:\opt\quant\deploy\qmt-win\tools\nssm-2.24\win64\nssm.exe", # 备份任务安装位（register_backup_task.ps1）
    "C:\opt\quant\tools\nssm-2.24\win64\nssm.exe",                # 手工安装位
    "C:\qmt\nssm\nssm.exe"                                        # 旧形态位（all_service_watchdog 曾硬编码）
)
# 解析顺序：消费方脚本自身目录下的 tools（= register_engine_services.ps1 的安装位，随脚本
# 落盘位置自适应；现网 = 候选表第一个 C:\opt\quant\qmt-win\tools\…）→ 候选表。
# 逗号 return 防单元素数组被 PowerShell 标量化。
function Resolve-SvcNssm {
    $rel = Join-Path $PSScriptRoot "tools\nssm-2.24\win64\nssm.exe"
    foreach ($c in (@($rel) + $SvcNssmCandidates)) {
        if (Test-Path $c) { return , $c }
    }
    return , $null
}

# ── 路径（现网形态，依据见文件头）──────────────────────────────────────────────────
$SvcDeployDir  = "C:\opt\quant"                                   # deploy_guangzhou.sh:48
$SvcDataDir    = if ($ProbeResearchDataDir) { $ProbeResearchDataDir } else { "C:\var\lib\quant-trading-v2" }  # deploy_guangzhou.sh:49，§H8 同源
$SvcQmtRepoRoot = "C:\qmt\quant-trading-v2"                       # CHECKLIST.md B1 仓库上传位
$SvcGatewayDir  = "C:\qmt\quant-trading-v2\qmt_gateway"           # deploy_guangzhou.sh:50 = verify GatewayCfg 目录
$SvcGatewayConfigFile = Join-Path $SvcGatewayDir "config.xt.json" # 网关进程 token 的真源（§C7-OPS ④）
$SvcGatewayPy   = Join-Path $SvcGatewayDir "gateway.py"
$SvcGatewayTemplate = Join-Path $SvcGatewayDir "config.xt.template.json"
$SvcPythonExe   = "C:\Python312\python.exe"                       # gateway_watchdog.ps1:11 现值
$SvcMiniQmtClientExe = "C:\Program Files (x86)\东莞证券QMT实盘交易端\bin.x64\XtItClient.exe"  # register_engine_services.ps1:35 现值（完整交易端，自动登录）
$SvcWatchdogBase = "C:\qmt\quant-trading-v2"                      # all_service_watchdog 配置探测兜底位（旧 C:\qmt 形态，保留兼容）
$SvcWatchdogLog  = "C:\qmt\watchdog_all.log"                      # all_service_watchdog.ps1:11 现值

# ── 网关绑定面（裁决：仅回环；公网 0.0.0.0 属首尔→广州分离期旧物，依据见头注②）─────
$SvcGatewayBindLoopback = "127.0.0.1:$ProbeGatewayPort"
$SvcGatewayAllowedIpsLoopback = "127.0.0.1"
# 旧首尔出口 IP：仅作历史注释保留，任何脚本不得再把它写进 ALLOWED_IPS（R2 裁决）。
$SvcLegacySeoulEgressIp = "43.108.86.140"

# ── 守护清单（all_service_watchdog 消费；NSSM 四条 + 网关交互任务一条）──────────────
#   Type=http       NSSM 服务：GET 无鉴权探针 200 = 健康（§H8 口径不变）
#   Type=heartbeat  无 HTTP 口（quant-research）：文件 mtime 判定（§H8 口径不变）
#   Type=gwtask     qmt_gateway：**不是 NSSM 服务**（头注①裁决）。健康判定＝/health 探针；
#                   恢复动作＝schtasks /Change /Enable + /Run 交互任务（RUNBOOK_QMT_DAILY.md:89-90
#                   09-11 实录的 sanctioned 通道），绝不 nssm restart 一个不存在的服务。
if ($ProbeQuantUrl) {
    $SvcWatchServices = @(
        @{ Name = $SvcNameQuant;     Type = "http";      Probe = $ProbeQuantUrl },
        @{ Name = $SvcNameResearch;  Type = "heartbeat"; Probe = $ProbeResearchHeartbeat; MaxAgeMin = $ProbeResearchMaxAgeMin },
        @{ Name = $SvcNamePydata;    Type = "http";      Probe = $ProbePydataUrl },
        @{ Name = $SvcNameWeb;       Type = "http";      Probe = $ProbeWebUrl },
        @{ Name = "qmt_gateway";     Type = "gwtask";    Probe = $ProbeGatewayUrl; Task = $SvcTaskGatewayEnsure }
    )
} else {
    $SvcWatchServices = @()
}
