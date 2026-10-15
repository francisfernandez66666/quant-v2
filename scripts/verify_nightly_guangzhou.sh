#!/bin/bash
# verify_nightly_guangzhou.sh — §0929OPS-⑪-2（2026-09-29 全量审计批）：广州现网夜间研究的**脚本级验收**。
#
# 为什么现在才补这条（缺陷的因果链，照 docs/FIX_PLAN_20260929.md ⑪-2）：
#   - 仓里唯一的夜间验收脚本 scripts/verify_nightly.sh 打的是**首尔 Ubuntu**（HOST=root@<首尔 IP>，
#     判据 systemctl is-active quant-research + journalctl + free -m）；
#   - 而现网 quant-research 是**广州 Windows 上的 NSSM 服务**（deploy/qmt-win/register_engine_services.ps1
#     的 $SvcNameResearch），首尔只是 08-29 迁移后保留的降级回滚位；
#   - Meanwhile verify_deploy_guangzhou.sh 只查四个 NSSM 服务"在跑"（服务态），不查夜间作业
#     到底跑没跑、有没有 OOM、产没产出候选（§0927KA 那次"假日空转 7+ 轮、每轮 1.5h 写 0 行"
#     就是活在服务态里、死在产出上的实录）⇒ 这三件事现在全靠人盯。
#   - 本脚本因此**新增**而不是改造 verify_nightly.sh：首尔那套 systemctl/journalctl 腿在回滚位
#     仍然成立，两条链各自的判据不该互相污染（owner 待裁决项 #11 的落码取向：新建广州专用脚本）。
#
# 判据取向：**按产物侧的数判，不按服务态判**。服务 Running 只说明没崩，说明不了有没有产出。
# 七条腿（PS 侧 4 条 + Python 只读侧 3 条，全部零写入）：
#   1) svc      NSSM quant-research / quant 两服务 Running
#   2) hb       researchd 文件心跳新鲜（§H8 判法：scheduler_status.json 的 mtime，它无 HTTP 口）
#   3) mem      researchd 进程 WorkingSet 上限（夜间 OOM 是本脚本原点名的三件事之一）
#   4) scale    成交额量纲只读抽检（dataload.exe amount-check，⑩ 那条链的现网腿，纯 SELECT）
#   5) queue    research_tasks：活跃任务积压 + 最近终态新鲜度（链停摆的第一现场在队列里）
#   6) cand     research_candidates：近 N 日新增条数 + 最近一条的 created_at
#   7) fina     fina_indicator：总行数 + 各报告期 distinct ts_code 分布（§0925EVE-B1 的"整季缺失"
#               判据原样搬过来——只打印 COUNT(*) 发现不了 0930 期被永久跳过那种形态）
# 七条判据腿之外另有两条 **只读 INFO 附加腿**（不进判数、不影响「腿数不足 7 必红」守卫）：
#   §CAL-COVER（2026-10-11）：`cal_coverage` 报交易日历末行是否覆盖今日、行情面最新交易日
#     与落后几个交易日，把"行情面停更但七腿全绿"摆到验收面上。
#   §BOARD-COVER（2026-10-12）：`board_coverage` 按 .SH/.SZ/.BJ 三板块点名（板块最新交易日
#     ＋全库最新日当日行数）——全库 MAX 口径是 .SH/.SZ 的最新，`.BJ` 整板断更六周只在
#     总数里隐形；缺板当天可见，阈值口径与 cal_coverage 记在同一张账上。
#
# 三条现网纪律（照抄本仓已锤过的姿势，不是新发明）：
#   ① 判据明细/键名一律 ASCII：PowerShell→SSH 回传按 GBK 码页打乱中文，拿中文当判据＝永久性假绿
#      （verify_deploy_guangzhou.sh 第 19 探针原判例）。人读中文只进 INFO 之外的注释，不进比字符串。
#   ② 上传的 .py/.ps1 都归一 UTF-8 BOM 同套约定（PS5.1 无 BOM 读中文注释直接 ParserError）。
#   ③ 缺省就连现网只读跑；`-Preview` 只打印将要执行的远端命令、一次 SSH 都不发
#      （门禁可以离线跑这条，§MINUTE-OPS 同姿势）。
#
# 用法（本地 macOS）：
#   GZ_IP=<广州公网 IP，从 docs/RUNBOOK 取值，别写进命令历史> ./scripts/verify_nightly_guangzhou.sh
#   GZ_IP=... CAND_MAX_AGE_DAYS=14 ./scripts/verify_nightly_guangzhou.sh      # 放宽候选新鲜度
#   ./scripts/verify_nightly_guangzhou.sh -Preview                            # 离线预览（需 GZ_IP）
# 可选环境变量：GZ_USER / DEPLOY_DIR / DATA_DIR / PY_EXE / RESEARCH_MAX_AGE_MIN /
#   TASK_MAX_AGE_HOURS / RESEARCH_MEM_LIMIT_MB / CAND_MAX_AGE_DAYS / ACTIVE_QUEUE_MAX / AMOUNT_CHECK_ROWS
#   PARKED_MAX / REQUEUE_MAX（§QUEUE-REQUEUE 队列熔断两条尺子，见队列腿注释）
# 退出码：0=七腿全绿；非 0=任一腿判红（含"读数解析失败"这种半态，绝不猜成功）。
#
# English: nightly-research acceptance for the Guangzhou (Windows/NSSM) production host, judged by
# product-side readings (queue freshness, candidate output, financial-report coverage) rather than
# service state. Seven read-only legs; -Preview prints the remote commands without connecting.
set -uo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP（与 deploy_guangzhou.sh 同一入口变量，勿写死在本脚本里）}"
GZ_USER="${GZ_USER:-Administrator}"
DEPLOY_DIR="${DEPLOY_DIR:-C:/opt/quant}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
PY_EXE="${PY_EXE:-C:/Python312/python.exe}"
RESEARCH_MAX_AGE_MIN="${RESEARCH_MAX_AGE_MIN:-5}"
RESEARCH_MEM_LIMIT_MB="${RESEARCH_MEM_LIMIT_MB:-900}"
TASK_MAX_AGE_HOURS="${TASK_MAX_AGE_HOURS:-48}"
ACTIVE_QUEUE_MAX="${ACTIVE_QUEUE_MAX:-30}"
# §QUEUE-REQUEUE（2026-10-12）队列侧两把尺子，与 Go 侧 store.TotalRequeueLimit 同源
# （§NIGHTCAP 2026-10-14 起＝8，门禁 §106 钉四门跨语言等值）：
#   PARKED_MAX＝库里 failed_needs_attention 条数上限（默认 0：挂起就是"要人看"，不该天天红着，
#                但也绝不能绿着隐身——红一条具名腿比整条链静默强）；
#   REQUEUE_MAX＝活跃行累计回队次数上限（达 store 侧阈值时本应已挂起；库里还留着一行转到这个
#                次数＝熔断闸没落上，属于判据失效，必须现形而不是当积压一起忽略）。
PARKED_MAX="${PARKED_MAX:-0}"
REQUEUE_MAX="${REQUEUE_MAX:-8}"
# §QUEUE-BACKLOG（2026-10-15 阈值口径批，owner 10-12 原话「观察一夜再裁」到期落槌）：
# 积压这把尺子从「活跃条数 > 30 就红」换成**按吞吐判**，两把新闸同源（SH/PS/PY 三处，门禁 §118 钉等值）：
#   DRAIN_MIN_DONE＝最近 24h 落终态（done/cancelled/failed_needs_attention）条数下限，
#     达到它就判「在排水」——积压超上限此时只出 INFO，不再天天抬红；
#   BACKLOG_RUNAWAY_MAX＝失控线：活跃条数冲破它恒红，**不看吞吐**（每夜排 7 条、每小时新增 10 条
#     那种"越排越多"必须在验收面现形，这是把旧尺子唯一的真价值保下来）。
# 定标依据＝现网实录（10-04~10-14 各夜落终态 7~12 条、活跃 55 条 ⇒ 54 条积压按吞吐需 5~8 夜排空）：
#   旧口径会在整段排水期恒红，而这条恒红的唯一后果就是把真停摆（stall）掩成噪音——本仓锤过的
#   「报警疲劳型失明」。30 这把旧线保留作"软上限"（判排水与否的分母），不再单独定生死。
DRAIN_MIN_DONE="${DRAIN_MIN_DONE:-6}"
BACKLOG_RUNAWAY_MAX="${BACKLOG_RUNAWAY_MAX:-90}"
CAND_MAX_AGE_DAYS="${CAND_MAX_AGE_DAYS:-7}"
AMOUNT_CHECK_ROWS="${AMOUNT_CHECK_ROWS:-800}"

PREVIEW=0
[ "${1:-}" = "-Preview" ] && PREVIEW=1

SSH="ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o BatchMode=yes ${GZ_USER}@${GZ_IP}"
SCP="scp -o StrictHostKeyChecking=accept-new -o BatchMode=yes"

PS_TARGET="${DEPLOY_DIR}/verify_nightly_probes.ps1"
PY_TARGET="${DEPLOY_DIR}/verify_nightly_readout.py"

# 远端命令原文（预览模式打印的就是这里，保证"预览"与"实跑"不会各说一套）
PS_ARGS="-DataDir ${DATA_DIR} -DeployDir ${DEPLOY_DIR} -PythonExe ${PY_EXE} -ReadoutPy ${PY_TARGET} -MaxAgeMin ${RESEARCH_MAX_AGE_MIN} -MemLimitMb ${RESEARCH_MEM_LIMIT_MB} -AmountRows ${AMOUNT_CHECK_ROWS} -ActiveMax ${ACTIVE_QUEUE_MAX} -TaskAgeHours ${TASK_MAX_AGE_HOURS} -CandAgeDays ${CAND_MAX_AGE_DAYS} -ParkedMax ${PARKED_MAX} -RequeueMax ${REQUEUE_MAX} -DrainMinDone ${DRAIN_MIN_DONE} -BacklogRunawayMax ${BACKLOG_RUNAWAY_MAX}"
RUN_PS="powershell -NoProfile -ExecutionPolicy Bypass -File ${PS_TARGET} ${PS_ARGS}"
# 队列/候选/财务三条腿由 PS 内部调这条 python 命令（阈值同样经 PS 参数透传，见 PSEOF 里的 pyCmd）
RUN_PY="${PY_EXE} ${PY_TARGET} --db ${DATA_DIR}/trading.db --active-max ${ACTIVE_QUEUE_MAX} --task-age-hours ${TASK_MAX_AGE_HOURS} --cand-age-days ${CAND_MAX_AGE_DAYS} --parked-max ${PARKED_MAX} --requeue-max ${REQUEUE_MAX} --drain-min-done ${DRAIN_MIN_DONE} --backlog-runaway-max ${BACKLOG_RUNAWAY_MAX}"

if [ "$PREVIEW" -eq 1 ]; then
  echo "== verify_nightly_guangzhou -Preview（不连现网，仅打印将要执行的远端命令）=="
  echo "  scp -> ${PS_TARGET}"
  echo "  scp -> ${PY_TARGET}"
  echo "  ssh ${RUN_PS}   | tr -d '\\r' 后按 PASS|/FAIL|/INFO| 前缀计数"
  echo "  ssh ${RUN_PY}"
  echo "  判据：七腿（svc/hb/mem/scale/queue/cand/fina），全部零写入"
  exit 0
fi

TMP_PS="$(mktemp /tmp/vng_probes_XXXXXX).ps1" || { echo "mktemp 失败"; exit 1; }
TMP_PY="$(mktemp /tmp/vng_readout_XXXXXX).py" || { echo "mktemp 失败"; exit 1; }

# ── PS 侧四条腿（服务态 / 文件心跳 / 进程内存 / 量纲抽检）──
# 输出协议与 verify_deploy_guangzhou.sh 一致：PASS|<名> / FAIL|<名> / INFO|<读数>，明细全 ASCII。
cat > "$TMP_PS" <<'PSEOF'
# 队列/候选阈值（ActiveMax / TaskAgeHours / CandAgeDays / ParkedMax / RequeueMax）必须由 bash 侧透传（见 PS_ARGS）。
# param 默认值只作「单独直跑 PS」的兜底，与 bash/python 同名默认值一致；漏透传不会报错、
# 只会静默按默认判，因此门禁 §106 用等值锁把这三个名字在两侧同时钉住（防惰性阈值）。
param(
    [string]$DataDir = "C:\var\lib\quant-trading-v2",
    [string]$DeployDir = "C:\opt\quant",
    [string]$PythonExe = "C:\Python312\python.exe",
    [string]$ReadoutPy = "C:\opt\quant\verify_nightly_readout.py",
    [int]$MaxAgeMin = 5,
    [int]$MemLimitMb = 900,
    [int]$AmountRows = 800,
    [int]$ActiveMax = 30,
    [int]$TaskAgeHours = 48,
    [int]$CandAgeDays = 7,
    [int]$ParkedMax = 0,
    [int]$RequeueMax = 8,
    # §QUEUE-BACKLOG（2026-10-15）：PS 侧默认值必须与 bash/Python 两处逐字一致（门禁 §118 钉等值）。
    # 三个阈值都靠命令行透传，任何一处"读默认"都会让环境变量在实跑里失效。
    [int]$DrainMinDone = 6,
    [int]$BacklogRunawayMax = 90
)
function Probe([string]$name, [bool]$ok, [string]$detail) {
    $tag = if ($ok) { "PASS" } else { "FAIL" }
    Write-Output ($tag + "|" + $name + " :: " + $detail)
}

# 1) svc：夜间研究归属的 NSSM 服务必须 Running（quant-research 本体 + 出信号的 quant 引擎）
$svcNames = @("quant-research", "quant")
$svcBad = @()
$svcRead = @()
foreach ($n in $svcNames) {
    $s = Get-Service -Name $n -ErrorAction SilentlyContinue
    if ($null -eq $s) { $svcBad += ($n + ":absent"); $svcRead += ($n + "=absent"); continue }
    $svcRead += ($n + "=" + $s.Status)
    if ($s.Status -ne 'Running') { $svcBad += ($n + ":" + $s.Status) }
}
Probe "svc:nightly research + engine services running" ($svcBad.Count -eq 0) ("state[" + ($svcRead -join ",") + "] bad=" + $(if ($svcBad.Count) { ($svcBad -join ";") } else { "none" }))

# 2) hb：researchd 无 HTTP 口，按 §H8 文件心跳判定（scheduler 每 30s 原子落 scheduler_status.json）
#    这条腿是"服务在跑但调度器没在转"的唯一现形处——服务态绿、心跳 stale 就是那种半死态。
$hbPath = Join-Path $DataDir "scheduler_status.json"
try {
    $hb = Get-Item -LiteralPath $hbPath -ErrorAction Stop
    $ageMin = [math]::Round(((Get-Date) - $hb.LastWriteTime).TotalMinutes, 1)
    Probe "hb:researchd scheduler heartbeat fresh" ($ageMin -le $MaxAgeMin) ("age_min=" + $ageMin + " limit=" + $MaxAgeMin)
} catch {
    Probe "hb:researchd scheduler heartbeat fresh" $false ("missing@" + $hbPath)
}

# 3) mem：researchd 进程 WorkingSet（夜间 OOM / 内存爬升是本脚本原点名的三件事之一）
#    无进程不算红？算红——服务态绿而进程查不到，正是 §0927KA 那类"服务在、活体不在"的形态。
$proc = Get-CimInstance Win32_Process -Filter "name='researchd.exe'" -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $proc) {
    Probe "mem:researchd working set under limit" $false "no-researchd-process"
} else {
    $p = Get-Process -Id $proc.ProcessId -ErrorAction SilentlyContinue
    if ($null -eq $p) {
        Probe "mem:researchd working set under limit" $false "pid-vanished"
    } else {
        $ws = [math]::Round($p.WorkingSet64 / 1MB)
        Write-Output ("INFO|researchd pid=" + $proc.ProcessId + " workingset_mb=" + $ws + " cpu_sec=" + [math]::Round($p.TotalProcessorTime.TotalSeconds))
        Probe "mem:researchd working set under limit" ($ws -le $MemLimitMb) ("workingset_mb=" + $ws + " limit=" + $MemLimitMb)
    }
}

# 4) scale：成交额量纲只读抽检（§0929SCALE-⑩ 的现网腿，纯 SELECT 零写入）
#    rc=0 绿（ok 或 no-data 都合法——日线表未装由数据新鲜度腿负责，这里重复判红会把
#    "还没装"冒充成"量纲错了"）；rc=1 千元/双重换算/混源判红；rc=2 读取失败判红。
#    §W7-D（2026-10-09）：这一腿抽**两张表**（daily + ths_daily）。ths_daily 是同花顺 dump 主源、
#    amount 直接来自 parquet 的 turnover 列，且它不在写侧换算白名单里（没有任何兜底），
#    口径只有这条读数在担保；两张表共用一个判定体（store.AmountProbedTables），
#    所以这里保持"一腿一个 PASS"而不是拆成第 8 腿——半态守卫 `PASS -lt 7` 与
#    §106 的七腿齐备锁都按七条写，拆腿不改那两处就是把判数撒谎写进夜里。
$scaleOut = ""; $scaleRc = -1
$scaleThsOut = ""; $scaleThsRc = -1
$dl = Join-Path $DeployDir "dataload.exe"
if (Test-Path -LiteralPath $dl) {
    try {
        $raw = & $dl "--db" (Join-Path $DataDir "trading.db") "amount-check" "--rows" "$AmountRows" "--json" 2>&1
        $scaleRc = $LASTEXITCODE
        $scaleOut = (($raw | ForEach-Object { [string]$_ }) -join " ")
    } catch { $scaleRc = 2; $scaleOut = "invoke-failed" }
    try {
        $rawThs = & $dl "--db" (Join-Path $DataDir "trading.db") "amount-check" "--table" "ths_daily" "--rows" "$AmountRows" "--json" 2>&1
        $scaleThsRc = $LASTEXITCODE
        $scaleThsOut = (($rawThs | ForEach-Object { [string]$_ }) -join " ")
    } catch { $scaleThsRc = 2; $scaleThsOut = "invoke-failed" }
} else {
    # 执行体缺席时两个 rc 都落 2：只落一个，另一条会以 rc=-1（从未执行）的形态进读数
    $scaleRc = 2; $scaleOut = "dataload.exe-missing"
    $scaleThsRc = 2; $scaleThsOut = "dataload.exe-missing"
}
Write-Output ("INFO|amount_scale daily rc=" + $scaleRc + " out=" + $scaleOut + " || ths_daily rc=" + $scaleThsRc + " out=" + $scaleThsOut)
$scaleBad = (($scaleRc -ne 0) -or ($scaleThsRc -ne 0))
Probe "scale:daily+ths_daily amount caliber probe green" (-not $scaleBad) ("daily_rc=" + $scaleRc + " ths_rc=" + $scaleThsRc)

# 5~7) queue/cand/fina 三条走同一份只读 Python 读数脚本（输出已是 PASS|/INFO| 协议，原样转发）
#      三个阈值必须逐字透传：PS 侧默认值和 python 侧默认值虽然一致，但「靠默认」意味着
#      环境变量 CAND_MAX_AGE_DAYS / TASK_MAX_AGE_HOURS / ACTIVE_QUEUE_MAX 在实跑里失效
#      （预览打印的命令行和真正执行的命令行会各说一套，那是本仓锤过的假绿形态）。
if (Test-Path -LiteralPath $PythonExe) {
    $pyRaw = & $PythonExe $ReadoutPy "--db" (Join-Path $DataDir "trading.db") "--active-max" "$ActiveMax" "--task-age-hours" "$TaskAgeHours" "--cand-age-days" "$CandAgeDays" "--parked-max" "$ParkedMax" "--requeue-max" "$RequeueMax" "--drain-min-done" "$DrainMinDone" "--backlog-runaway-max" "$BacklogRunawayMax" 2>&1
    foreach ($l in ($pyRaw | ForEach-Object { [string]$_ })) {
        if ($l -match '^(PASS|FAIL|INFO)\|') { Write-Output $l }
        elseif ($l.Trim().Length -gt 0) { Write-Output ("FAIL|py:readout-crashed :: " + $l.Replace("|", "/")) }
    }
} else {
    Probe "py:readout python interpreter present" $false ("missing@" + $PythonExe)
}
PSEOF

# ── Python 只读侧三条腿（队列 / 候选 / 财务分布），零写入（file:...?mode=ro）──
cat > "$TMP_PY" <<'PYEOF'
# verify_nightly_readout.py — §0929OPS-⑪-2 夜间研究的产物侧读数（只读，PASS|/INFO| 协议，全 ASCII）。
#
# 为什么单开一份 .py 而不是塞进 PowerShell 的 -Command：三层引号链（bash→SSH→PS→python）
# 在本仓已被证明不可靠（verify_deploy_guangzhou.sh 文件头记的就是"首版全部探针误报失败"），
# 而这三条腿都要跑 SQL。落盘成文件再由 PS 调用，引号只在一层里活。
#
# 判据三条都是"产物侧的数"，刻意不看服务态（服务 Running 只说明没崩，说明不了有没有产出）：
#   queue：活跃任务积压 ≤ 上限，且最近一条终态的 updated_at 距今 ≤ 时限（链停摆的第一现场在队列里）
#   cand ：research_candidates 最近 created_at 距今 ≤ 天数上限（夜间寻优没产出＝这条先红）
#   fina ：fina_indicator 总行数 > 0，并按报告期打 distinct ts_code 分布（§0925EVE-B1 原口径：
#          只打印 COUNT(*) 发现不了"整季被永久跳过"，规模基准取各期最大值，低于基准 80% 打警告行）
import argparse
import datetime as dt
import sqlite3
import sys


def emit(tag, name, detail=""):
    line = "%s|%s" % (tag, name)
    if detail:
        line += " :: " + detail
    print(line)


def parse_ts(s):
    # 现网时间串有两种形态：'YYYY-MM-DD HH:MM:SS' 与 ISO 带 T；解析失败一律按"未知"处理
    # （未知不得冒充新鲜，也不得冒充停摆——由调用方按 None 单独判红并留原始串长度）。
    if not s:
        return None
    text = str(s).strip().replace("T", " ")
    for fmt in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%d %H:%M", "%Y-%m-%d"):
        try:
            return dt.datetime.strptime(text[:len(fmt) + 2].strip(), fmt)
        except ValueError:
            continue
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", required=True)
    ap.add_argument("--active-max", type=int, default=30)
    ap.add_argument("--task-age-hours", type=int, default=48)
    ap.add_argument("--cand-age-days", type=int, default=7)
    # §QUEUE-REQUEUE（2026-10-12）两把队列熔断尺子的兜底默认，与 bash/PS/Go 四处同源：
    #   requeue-max 的字面值必须等于 Go 侧 store.TotalRequeueLimit（门禁 §106 钉这条跨语言等值）。
    ap.add_argument("--parked-max", type=int, default=0)
    ap.add_argument("--requeue-max", type=int, default=8)
    # §QUEUE-BACKLOG（2026-10-15 阈值口径批）：积压改按吞吐判，两把新尺子的兜底默认
    #   必须与 bash（DRAIN_MIN_DONE / BACKLOG_RUNAWAY_MAX）和 PS（-DrainMinDone / -BacklogRunawayMax）
    #   逐字一致——门禁 §118 钉这条三语言等值（与 §QUEUE-REQUEUE 那把 8 同一个形）。
    ap.add_argument("--drain-min-done", type=int, default=6)
    ap.add_argument("--backlog-runaway-max", type=int, default=90)
    a = ap.parse_args()

    now = dt.datetime.now()
    # ro 模式 + 5s busy timeout：夜间链可能正在写，只读腿绝不与它抢锁把对方卡住。
    # 连接失败（库不存在/被独占）必须**留三条 FAIL 行**而不是抛栈：Python 的 traceback 会被
    # PS 侧整段转成 readout-crashed 噪音，人读不出是哪条腿没跑；三条具名 FAIL 让「半态」
    # 在 bash 的 PASS<7 守卫里可读、可定位（零写入模式下这条只是读数失败，不改判据语义）。
    try:
        con = sqlite3.connect("file:%s?mode=ro" % a.db, uri=True, timeout=5)
    except Exception as e:
        for leg in ("queue:research task queue readable",
                    "cand:research_candidates readable",
                    "fina:fina_indicator readable"):
            emit("FAIL", leg, "db-open-failed=%s" % type(e).__name__)
        print("INFO|nightly_readout db=%s open_failed" % a.db)
        return 1
    cur = con.cursor()

    # ── queue ──
    try:
        active = cur.execute(
            "SELECT COUNT(*) FROM research_tasks WHERE status IN ('queued','running','paused','preempted')"
        ).fetchone()[0]
        last = cur.execute(
            "SELECT type,status,progress,error,updated_at FROM research_tasks ORDER BY updated_at DESC LIMIT 1"
        ).fetchone()
        last_age_h = None
        if last and last[4]:
            p = parse_ts(last[4])
            if p:
                last_age_h = (now - p).total_seconds() / 3600.0
        # §MAC-QSTAT（2026-10-10 观测批）：这条腿连续三夜判红（active=31 > limit=30），而只有
        #   active 一个数的读法归不了因——同一条 INFO 在 10-03~10-09 实测是 6→7→13→15→11→11→15→23→31→31
        #   （单调增长、末两次停在同一个数），光看总数分不清"在排队、会自己走完"与"有行永远不动"。
        #   取向＝**只加读数、不改判据**：阈值 30 该不该换成"只数 running（并发）"、积压该给多大上限
        #   是产品口径（Go 侧没有 30 这个约束，代码里查不到这把尺子），不在观测批里顺手改；
        #   本仓的规矩是读法改动与判据改动不同批（§DRILL-A 那课）。
        #   最老年龄走 python 侧逐行 parse_ts，而不是 SQL 的 MIN(updated_at)：TEXT 时间戳的字典序最小
        #   不等于时间最老（这张表历史上混过两种写法），比较要留在能算对的一侧。
        by_status = "none"
        oldest_age_h = None
        rows = cur.execute(
            "SELECT status, updated_at FROM research_tasks "
            "WHERE status IN ('queued','running','paused','preempted')").fetchall()
        if rows:
            stat_count = {}
            for st, upd in rows:
                stat_count[st] = stat_count.get(st, 0) + 1
                pp = parse_ts(upd)
                if pp:
                    one_age_h = (now - pp).total_seconds() / 3600.0
                    if oldest_age_h is None or one_age_h > oldest_age_h:
                        oldest_age_h = one_age_h
            by_status = ",".join("%s:%d" % (k, stat_count[k]) for k in sorted(stat_count))
        # §QUEUE-REQUEUE（2026-10-12）队列侧新口径：把"这条红的成因"直接读成字段。
        #   旧判据只有 active/last_age 两把尺子，10-11 那晚 active=31 判红之后归因要人翻库——
        #   实录是同一个任务被 8 小时单步超时送回队尾 33 次（回队循环），加上 38 条真排队，
        #   两种成因在旧读数里长得一模一样。现在：
        #     parked        = failed_needs_attention 条数（两条熔断闸挂起的总数，>0 即"要人看"）；
        #     churn_max     = 活跃行的累计回队次数最大值（回队循环的现场指纹）；
        #     churn_max_id  = 那一行的 id:type:fail_streak:requeue_total（能直接定位去查）；
        #     oldest_chain  = 活跃行里最老的夜间链日（积压是老链没走完还是新链在排）；
        #     reasons=      = 判红的具名成因列表（空＝绿），人不用再猜是哪把尺子量的。
        #   churn 读数**列缺席时报 absent 并报红**（reasons=requeue_col_absent）：该列由新 binary
        #   首次开库时幂等迁移添加，缺席＝这台机还没跑过带累计闸的版本，属于必须现形的时序差，
        #   绝不静默按 0 处理（0 会让回队循环继续绿着转——§缺数不报 0 的同族）。
        cols = [r[1] for r in cur.execute("PRAGMA table_info(research_tasks)").fetchall()]
        parked = cur.execute(
            "SELECT COUNT(*) FROM research_tasks WHERE status='failed_needs_attention'").fetchone()[0]
        parked_first = "none"
        prow = cur.execute(
            "SELECT id,type,error FROM research_tasks WHERE status='failed_needs_attention' "
            "ORDER BY updated_at DESC LIMIT 3").fetchall()
        if prow:
            parked_first = ",".join("%s:%s" % (r[0], r[1]) for r in prow)
        churn_max, churn_max_id, col_state = -1, "none", "ok"
        if "requeue_total" in cols:
            churn_max = int(cur.execute(
                "SELECT COALESCE(MAX(requeue_total),0) FROM research_tasks "
                "WHERE status IN ('queued','running','paused','preempted')").fetchone()[0] or 0)
            trow = cur.execute(
                "SELECT id, type, requeue_total, fail_streak FROM research_tasks "
                "WHERE status IN ('queued','running','paused','preempted') "
                "ORDER BY requeue_total DESC, id ASC LIMIT 1").fetchone()
            if trow:
                churn_max_id = "%s:%s:total=%s:streak=%s" % (trow[0], trow[1], trow[2], trow[3])
        else:
            col_state = "absent"
        oldest_chain = "none"
        if "chain_day" in cols:
            crow2 = cur.execute(
                "SELECT MIN(chain_day) FROM research_tasks "
                "WHERE status IN ('queued','running','paused','preempted') AND chain_day<>''").fetchone()
            oldest_chain = crow2[0] or "none"
        # ── §NIGHTCAP（2026-10-14）让位/占用读数：把"今晚谁在让位、谁占了多久"读成字段。
        #   deferred_now ＝ 已被顺延到未来某夜、还在队里的条数（54 条积压排水期天天 >0 属设计内，
        #                  只出读数不判红——判红口径挂账待裁，与 CAL-COVER 读数同批规矩）；
        #   night_used_today ＝ 活跃行里「今天」夜账占用的最大分钟数（占用上限 240 的现场读数）。
        #   列缺席报红（reasons=nightcap_col_absent）：四列由新 binary 首次开库幂等迁移添加，
        #   缺席＝这台机还没跑过带夜账的版本，属于必须现形的时序差（§缺数不报 0 同族）。
        deferred_now, deferred_ids, night_used_today, nightcap_state = 0, "none", 0, "ok"
        today_str2 = now.strftime("%Y%m%d")
        if "defer_day" in cols and "night_used_min" in cols:
            drows = cur.execute(
                "SELECT id, type, defer_day FROM research_tasks "
                "WHERE status IN ('queued','running','paused','preempted') "
                "AND COALESCE(defer_day,'')>? ORDER BY defer_day, id LIMIT 8",
                (today_str2,)).fetchall()
            deferred_now = int(cur.execute(
                "SELECT COUNT(*) FROM research_tasks "
                "WHERE status IN ('queued','running','paused','preempted') "
                "AND COALESCE(defer_day,'')>?",
                # 2026-10-15 修：这条 COUNT 的 ? 原本漏传绑定，SQLite 抛 ProgrammingError
                #   把整条队列腿判成 err=ProgrammingError（10-15 现网首夜真拨才现形——这条腿
                #   在仓里从来没有离线夹具跑过，§118 现在补上：夹具两态＋摘参反证）。
                (today_str2,)).fetchone()[0] or 0)
            if drows:
                deferred_ids = ",".join("%s:%s->%s" % (r[0], r[1], r[2]) for r in drows)
            nrow = cur.execute(
                "SELECT id, night_used_min FROM research_tasks "
                "WHERE status IN ('queued','running','paused','preempted') "
                "AND night_day=? ORDER BY night_used_min DESC, id ASC LIMIT 1",
                (today_str2,)).fetchone()
            if nrow:
                night_used_today = int(nrow[1] or 0)
        else:
            nightcap_state = "absent"
        reasons = []
        # §QUEUE-BACKLOG（2026-10-15 阈值口径批，owner 10-12 令「观察一夜再裁」到期）：
        #   积压这把尺子从「active > 30 就红」改成**按吞吐判**，两把新闸：
        #     1) drained_24h ＝ 最近 24 小时落终态（done/cancelled/failed_needs_attention）的条数。
        #        现网实录（10-04~10-14 各夜落终态 7~12 条、活跃 55 条）⇒ 54 条积压按吞吐要 5~8 夜排空，
        #        旧口径会在整段排水期天天红，唯一的后果是把真停摆（stall）掩成噪音——本仓锤过的
        #        「报警疲劳型失明」。排水期（drained_24h ≥ drain_min_done）积压超软上限只出读数。
        #     2) 失控线 backlog_runaway_max（默认 90）**恒红、不看吞吐**：
        #        "每夜排 7 条、每小时新增 10 条"那种越排越多必须在验收面现形，这是旧尺子唯一的真价值。
        #   窗口边界一律在 python 侧算好再绑进 SQL：这张表的 updated_at 是北京时间写入口径，
        #   SQL 里的 datetime('now') 是 UTC，差 8 小时会把"昨夜那一波"整段切成半个
        #   （§TEXT 时间戳的窗口边界：边界必须与写入口径同区）。
        since_24h = (now - dt.timedelta(hours=24)).strftime("%Y-%m-%d %H:%M:%S")
        try:
            drained_24h = int(cur.execute(
                "SELECT COUNT(*) FROM research_tasks "
                "WHERE status IN ('done','cancelled','failed_needs_attention') AND updated_at>=?",
                (since_24h,)).fetchone()[0] or 0)
        except Exception:
            # 读数打不出来＝按 -1 处理（负数永远 < 下限 ⇒ 判红）：绝不"查不到就当在排水"，
            #  §缺数不报 0／静默成功同族。
            drained_24h = -1
        if active > a.backlog_runaway_max:
            reasons.append("backlog_runaway_gt_%d" % a.backlog_runaway_max)
        elif active > a.active_max and drained_24h < a.drain_min_done:
            reasons.append("backlog_not_draining_lt_%d" % a.drain_min_done)
        if last_age_h is None:
            reasons.append("last_terminal_unparsed")
        elif last_age_h > a.task_age_hours:
            reasons.append("stall_gt_%dh" % a.task_age_hours)
        if parked > a.parked_max:
            reasons.append("parked_gt_%d" % a.parked_max)
        if col_state != "ok":
            reasons.append("requeue_col_absent")
        elif churn_max >= a.requeue_max:
            reasons.append("churn_ge_%d" % a.requeue_max)
        if nightcap_state != "ok":
            reasons.append("nightcap_col_absent")
        detail = ("active=%d limit=%d last_updated_age_h=%s last_type=%s last_status=%s "
                  "by_status=%s oldest_active_age_h=%s parked=%d parked_max=%d parked_first=%s "
                  "churn_max=%s churn_max_task=%s requeue_max=%d oldest_chain_day=%s "
                  "deferred_now=%d deferred_first=%s night_used_today=%d nightcap_cols=%s "
                  "drained_24h=%d drain_min=%d runaway_max=%d reasons=%s") % (
            active, a.active_max,
            ("%.1f" % last_age_h) if last_age_h is not None else "unparsed",
            (last[0] if last else "none"), (last[1] if last else "none"),
            by_status,
            ("%.1f" % oldest_age_h) if oldest_age_h is not None else "unparsed",
            parked, a.parked_max, parked_first,
            churn_max if col_state == "ok" else "absent", churn_max_id, a.requeue_max,
            oldest_chain,
            deferred_now, deferred_ids, night_used_today, nightcap_state,
            drained_24h, a.drain_min_done, a.backlog_runaway_max,
            ",".join(reasons) if reasons else "none")
        emit("INFO", "nightly_queue " + detail)
        okq = not reasons
        emit("PASS" if okq else "FAIL", "queue:research task queue drained and recent terminal state", detail)
    except Exception as e:  # 读数失败一律判红并留异常摘要：静默的"查不到"比查错更危险
        emit("FAIL", "queue:research task queue readable", "err=%s" % type(e).__name__)

    # ── §CAL-COVER 附加读数（2026-10-11 观测批）：交易日历覆盖 + 行情面新度 ──
    #   为什么必须有这条：10-08/09 两个真实交易日整段漏更，而上面七条腿全绿——量纲腿只看
    #   数值口径、队列腿只看研究任务，验收面上**没有任何一条腿看过日历是否覆盖今天**。
    #   根源（保活自愈窗口 end=today）已在 scripts/dataload_keepalive.py 修掉，这条腿负责
    #   的是"万一再次断供（sidecar 长期不可用、日历末行落后）必须在验收面现形，而不是只活在日志里"。
    #   取向＝**只出 INFO 读数、不进判数**：covers_today=no 现在不抬红——把停更判成红的阈值
    #   口径属产品裁决，与 queue 腿那把 30 的尺子记在同一张账上（读法与判据不同批是本仓规矩）。
    try:
        cal_row = cur.execute("SELECT MAX(cal_date) FROM trade_cal").fetchone()[0]
        cal_open = cur.execute("SELECT MAX(cal_date) FROM trade_cal WHERE is_open=1").fetchone()[0]
        daily_max = cur.execute("SELECT MAX(trade_date) FROM daily").fetchone()[0]
        idx_max = cur.execute("SELECT MAX(trade_date) FROM index_daily").fetchone()[0]
        today_str = now.strftime("%Y%m%d")
        covers = bool(cal_row) and str(cal_row) >= today_str
        # 行情面落后交易日数：日历里 (行情最新日, 今日] 区间的开市日个数——
        # 用「落后几个交易日」而不是「几天没更新」，节假日不会把读数染成红。
        # ★ 但**日历不覆盖今日时这个数必须是 unknown 而不是算出来的数字**：本次实录里
        #   日历末行停在 10-07、10-08/09 两行根本不在表内，按同一张残缺日历算"落后"读出 0
        #   ＝缺数据的计数冒充"算得出来"（与 [xml]"" 不抛冒充解析成功同族）。宁可交"算不了"，
        #   也不交一个会让停更继续隐形的 0。
        lag = "unknown"
        if covers:
            lag = str(cur.execute(
                "SELECT COUNT(*) FROM trade_cal WHERE is_open=1 AND cal_date>? AND cal_date<=?",
                (daily_max or "", today_str)).fetchone()[0])
        emit("INFO", "cal_coverage cal_last_row=%s cal_last_open=%s covers_today=%s "
                     "daily_max=%s index_max=%s daily_lag_td=%s" % (
            cal_row or "none", cal_open or "none",
            "yes" if covers else "no",
            daily_max or "none", idx_max or "none", lag))
    except Exception as e:
        # 读数腿自己坏了也说清楚（不进判数，但绝不沉默——沉默正是这次停更没人看见的原因）
        emit("INFO", "cal_coverage err=%s（读数腿故障，本条不进判数）" % type(e).__name__)

    # ── §BOARD-COVER 附加读数（2026-10-12 波 6）：按板块覆盖点名 ──
    #   `.BJ` 整板断更六周而全库 MAX 口径看不见的教训：任何"最新"判据都是 .SH/.SZ 的最新，
    #   整块板块停下只在总数里隐形。与 cal_coverage 同取向＝只出 INFO 不进判数；
    #   行数在无最新日时读 unknown（无从判定不冒充 0）。
    try:
        overall = cur.execute("SELECT MAX(trade_date) FROM daily").fetchone()[0]
        parts = ["latest=%s" % (overall or "none")]
        for suf in (".SH", ".SZ", ".BJ"):
            mx = cur.execute("SELECT MAX(trade_date) FROM daily WHERE ts_code LIKE ?",
                             ("%" + suf,)).fetchone()[0]
            if overall:
                n = str(cur.execute("SELECT COUNT(*) FROM daily WHERE trade_date=? AND ts_code LIKE ?",
                                    (overall, "%" + suf)).fetchone()[0])
            else:
                n = "unknown"
            parts.append("%s[max=%s rows=%s]" % (suf.strip("."), mx or "none", n))
        emit("INFO", "board_coverage " + " ".join(parts))
    except Exception as e:
        emit("INFO", "board_coverage err=%s（读数腿故障，本条不进判数）" % type(e).__name__)

    # ── cand ──
    try:
        row = cur.execute("SELECT MAX(created_at), COUNT(*) FROM research_candidates").fetchone()
        latest = parse_ts(row[0]) if row and row[0] else None
        age_d = (now - latest).days if latest else None
        recent = cur.execute(
            "SELECT COUNT(*) FROM research_candidates WHERE created_at >= ?",
            ((now - dt.timedelta(days=a.cand_age_days)).strftime("%Y-%m-%d %H:%M:%S"),)).fetchone()[0]
        detail = "total=%s latest_age_days=%s new_in_%dd=%d limit_age=%d" % (
            (row[1] if row else 0), age_d if age_d is not None else "unparsed",
            a.cand_age_days, recent, a.cand_age_days)
        emit("INFO", "nightly_candidates " + detail)
        okc = latest is not None and age_d is not None and age_d <= a.cand_age_days and recent >= 1
        emit("PASS" if okc else "FAIL", "cand:nightly research produced candidates recently", detail)
    except Exception as e:
        emit("FAIL", "cand:research_candidates readable", "err=%s" % type(e).__name__)

    # ── fina（只判总行数，分布走 INFO 供人读；警告行按 §0925EVE-B1 的 80% 基准）──
    try:
        total = cur.execute("SELECT COUNT(*) FROM fina_indicator").fetchone()[0]
        dist = cur.execute(
            "SELECT end_date, COUNT(DISTINCT ts_code) FROM fina_indicator GROUP BY end_date ORDER BY end_date DESC LIMIT 8"
        ).fetchall()
        scale = max([n for _, n in dist], default=0)
        emit("INFO", "fina total=%d scale_base=%d dist=%s" % (total, scale,
             ",".join("%s:%d%s" % (ed, n, "(!)" if scale and n < scale * 0.8 else "") for ed, n in dist)))
        emit("PASS" if total > 0 else "FAIL", "fina:financial indicator table loaded", "rows=%d" % total)
    except Exception as e:
        emit("FAIL", "fina:fina_indicator readable", "err=%s" % type(e).__name__)

    con.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
PYEOF

# PS5.1 读无 BOM 的 UTF-8 按 GBK 解析中文注释会撕裂字面量（现网实录）——两份都补恰好一个 BOM。
bom_once() {
  python3 - "$1" <<'PY'
import sys
p = sys.argv[1]
d = open(p, 'rb').read()
while d.startswith(b'\xef\xbb\xbf'):
    d = d[3:]
open(p, 'wb').write(b'\xef\xbb\xbf' + d)
PY
}
bom_once "$TMP_PS"
bom_once "$TMP_PY"

echo "== verify_nightly_guangzhou @ ${GZ_IP}（七腿，全部零写入）=="

# 先传两份执行体，再跑 PS（PS 内部要调那份 .py，顺序反了会把"文件还没上"判成队列停摆）。
$SCP "$TMP_PS" "${GZ_USER}@${GZ_IP}:${PS_TARGET}" || { echo "FAIL: 探针脚本上传失败"; rm -f "$TMP_PS" "$TMP_PY"; exit 1; }
$SCP "$TMP_PY" "${GZ_USER}@${GZ_IP}:${PY_TARGET}" || { echo "FAIL: 只读脚本上传失败"; rm -f "$TMP_PS" "$TMP_PY"; exit 1; }

out="$($SSH "$RUN_PS" 2>&1 | LC_ALL=C tr -d '\r')"
psrc=$?
rm -f "$TMP_PS" "$TMP_PY"

# CRLF 尾已由 tr -d 剥掉（`$()` 只剥 \n，行尾最后一个字段留 \r 会让等值比较恒假红——本仓锤实过）。
PASS=0
FAIL=0
while IFS= read -r line; do
  case "$line" in
    PASS\|*) PASS=$((PASS+1)); echo "  ✓ ${line#PASS|}" ;;
    FAIL\|*) FAIL=$((FAIL+1)); echo "  ✗ ${line#FAIL|}" ;;
    INFO\|*) echo "  · ${line#INFO|}" ;;
    "") : ;;
    *) echo "  ? ${line}" ;;
  esac
done <<< "$out"

echo "== 结果：${PASS} 通过 / ${FAIL} 失败（ssh rc=${psrc}）=="
[ "$psrc" -eq 0 ] || { echo "FAIL: 远端执行非零退出（rc=${psrc}）"; exit 1; }
[ "$FAIL" -eq 0 ] || exit 1
[ "$PASS" -lt 7 ] && { echo "FAIL: 判绿腿数 ${PASS} < 7（有腿没跑出来，半态不猜成功）"; exit 1; }
echo "夜间研究七腿全绿。"
