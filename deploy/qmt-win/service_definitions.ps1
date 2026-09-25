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
$SvcTaskAllWatchdog     = "quant-all-wd"           # 全服务 watchdog（RUNBOOK_QMT_DAILY.md:19 在跑）
$SvcTaskBackupSnap      = "quant-backup-snap"      # 夜间快照（deploy_guangzhou.sh [6/6] 触发位）

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
