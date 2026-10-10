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
CAND_MAX_AGE_DAYS="${CAND_MAX_AGE_DAYS:-7}"
AMOUNT_CHECK_ROWS="${AMOUNT_CHECK_ROWS:-800}"

PREVIEW=0
[ "${1:-}" = "-Preview" ] && PREVIEW=1

SSH="ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -o BatchMode=yes ${GZ_USER}@${GZ_IP}"
SCP="scp -o StrictHostKeyChecking=accept-new -o BatchMode=yes"

PS_TARGET="${DEPLOY_DIR}/verify_nightly_probes.ps1"
PY_TARGET="${DEPLOY_DIR}/verify_nightly_readout.py"

# 远端命令原文（预览模式打印的就是这里，保证"预览"与"实跑"不会各说一套）
PS_ARGS="-DataDir ${DATA_DIR} -DeployDir ${DEPLOY_DIR} -PythonExe ${PY_EXE} -ReadoutPy ${PY_TARGET} -MaxAgeMin ${RESEARCH_MAX_AGE_MIN} -MemLimitMb ${RESEARCH_MEM_LIMIT_MB} -AmountRows ${AMOUNT_CHECK_ROWS} -ActiveMax ${ACTIVE_QUEUE_MAX} -TaskAgeHours ${TASK_MAX_AGE_HOURS} -CandAgeDays ${CAND_MAX_AGE_DAYS}"
RUN_PS="powershell -NoProfile -ExecutionPolicy Bypass -File ${PS_TARGET} ${PS_ARGS}"
# 队列/候选/财务三条腿由 PS 内部调这条 python 命令（阈值同样经 PS 参数透传，见 PSEOF 里的 pyCmd）
RUN_PY="${PY_EXE} ${PY_TARGET} --db ${DATA_DIR}/trading.db --active-max ${ACTIVE_QUEUE_MAX} --task-age-hours ${TASK_MAX_AGE_HOURS} --cand-age-days ${CAND_MAX_AGE_DAYS}"

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
# 三个队列/候选阈值（ActiveMax / TaskAgeHours / CandAgeDays）必须由 bash 侧透传（见 PS_ARGS）。
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
    [int]$CandAgeDays = 7
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
    $pyRaw = & $PythonExe $ReadoutPy "--db" (Join-Path $DataDir "trading.db") "--active-max" "$ActiveMax" "--task-age-hours" "$TaskAgeHours" "--cand-age-days" "$CandAgeDays" 2>&1
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
        detail = ("active=%d limit=%d last_updated_age_h=%s last_type=%s last_status=%s "
                  "by_status=%s oldest_active_age_h=%s") % (
            active, a.active_max,
            ("%.1f" % last_age_h) if last_age_h is not None else "unparsed",
            (last[0] if last else "none"), (last[1] if last else "none"),
            by_status,
            ("%.1f" % oldest_age_h) if oldest_age_h is not None else "unparsed")
        emit("INFO", "nightly_queue " + detail)
        okq = active <= a.active_max and last_age_h is not None and last_age_h <= a.task_age_hours
        emit("PASS" if okq else "FAIL", "queue:research task queue drained and recent terminal state", detail)
    except Exception as e:  # 读数失败一律判红并留异常摘要：静默的"查不到"比查错更危险
        emit("FAIL", "queue:research task queue readable", "err=%s" % type(e).__name__)

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
