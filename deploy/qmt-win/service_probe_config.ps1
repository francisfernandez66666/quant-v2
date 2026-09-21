# service_probe_config.ps1 — §H8（2026-09-22 修复批）运维探针端口/端点唯一来源
# 消费方：all_service_watchdog.ps1、daily_ops_check.ps1、register_engine_services.ps1（兜底回退同源字面量）。
# 教训（H8 三连错实录）：旧版各运维脚本独立硬编码 URL——
#   ① quant 探 :8080/api/status：:8080 是 Caddy 应急直连口，引擎实听 :8081（register 注入
#      QUANT_ADDR=127.0.0.1:8081，见 GUANGZHOU_CADDY.md 端口表），且 /api/status 经 authMiddleware
#      包裹，无 token 必 401/403 → 服务明明健康却天天误重启；
#   ② researchd（quant-research）探 :9091/health：该进程只有调度循环，从无 HTTP 监听（cmd/researchd/main.go），
#      9091 纯属虚构 → 探针恒失败；改为文件心跳判定（见下方 $ProbeResearchHeartbeat）；
#   ③ quant-web 探引擎 :8081 的 `/`：引擎根路径无路由（server.go 仅注册 /setup 与 /api/*），
#      真实前端静态口是 Caddy :8080（guangzhou.conf ":8080" 块 file_server 恒回 index.html）。
# 新增/变更端口只改本文件，运维脚本一律 dot-source 引用——这是 verify_changes 部署面静态锁
# （「探针 URL 端口必须与 register_engine_services.ps1 同源变量」）的前提。
# 编码：仓内保持无 BOM UTF-8（与 all_service_watchdog.ps1 同现状）；上传 Windows 前由
# deploy_guangzhou.sh 的 ps1_bom 统一补单个 BOM——PS 5.1 无 BOM 按 GBK 读中文会撕裂字面量。

# ── 端口（全部 127.0.0.1 回环绑定，公网入口交给 Caddy 80/443/8080）────────────
$ProbeQuantPort   = 8081   # quant 引擎监听口（register_engine_services.ps1 的 QUANT_ADDR）
$ProbeCaddyPort   = 8080   # quant-web = Caddy 服务（deploy/caddy/guangzhou.conf ":8080" 应急块）
$ProbePydataPort  = 8787   # pydata sidecar（register_engine_services.ps1 --port 8787）
$ProbeGatewayPort = 8789   # qmt_gateway（register 的 ensure 包装 -gateway-url :8789/health）

# ── HTTP 探针 URL（必须是无鉴权端点，401 不等于挂）──────────────────────────
# quant 引擎：GET /setup 无鉴权恒 200（internal/server/server.go handleSetupStatus，仅 GET 不受频控）；
# 注意 /api/health 也包着 authMiddleware（server.go 路由表），机器探针无 token 不可用。
$ProbeQuantUrl   = "http://127.0.0.1:$ProbeQuantPort/setup"
# quant-web（Caddy 静态页）：:8080 块 file_server 根路径恒回 index.html，等价静态健康页。
$ProbeWebUrl     = "http://127.0.0.1:$ProbeCaddyPort/"
# pydata：cmd/pydata/server.py _ROUTES["health"]（旧 daily_ops_check 探 /pydata_status 无此路由，404 误报）。
$ProbePydataUrl  = "http://127.0.0.1:$ProbePydataPort/health"
# qmt_gateway：/health（internal/trading/qmt_client.go 心跳探测同口）。
$ProbeGatewayUrl = "http://127.0.0.1:$ProbeGatewayPort/health"

# ── researchd 文件心跳（§H8：无 HTTP 口 → mtime 判定，Go 侧零改动）────────────
# 现成心跳落盘点：internal/scheduler/scheduler.go Run→tick 每 30s 一支，所有分支收尾都调
# writeStatus → AtomicWrite 落 <DataDir>\scheduler_status.json（含 Ts/Busy/Reason 字段）。
# 判定规则：文件存在且 mtime 距今 ≤ $ProbeResearchMaxAgeMin 分钟 = 存活；
# 30s 周期给 5 分钟容差（GC/磁盘抖动/慢盘），不再依赖任何虚构 HTTP 端口。
$ProbeResearchDataDir   = "C:\var\lib\quant-trading-v2"   # register_engine_services.ps1 -DataDir 默认值
$ProbeResearchHeartbeat = Join-Path $ProbeResearchDataDir "scheduler_status.json"
$ProbeResearchMaxAgeMin = 5
