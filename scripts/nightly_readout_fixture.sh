#!/bin/bash
# nightly_readout_fixture.sh — §QUEUE-BACKLOG（2026-10-15 阈值口径批）夜间验收「队列腿」的离线夹具（八态）。
#
# 为什么现在必须有这份夹具（缺陷本体的因果链）：
#   10-14 §NIGHTCAP 批给队列腿加了三条只读读数（deferred_now/night_used_today/nightcap_cols），
#   其中 deferred_now 那条 COUNT 写了 `>?` 却**漏传绑定**——SQLite 抛 ProgrammingError，
#   整条队列腿在 10-15 现网首夜真拨时判成 `err=ProgrammingError`。仓里当时抓不到这件事，
#   因为这份读数脚本（verify_nightly_guangzhou.sh 的 PYEOF 内嵌块）**从来没有被离线跑过一次**：
#   门禁只钉了字符串在位（写侧/读侧列名、阈值等值），而「有代码没跑过」正是本仓锤过的
#   「度量腿没自证射程」形态（go test 空跑仍打 ok、Playwright .first() 命中隐藏节点同族）。
#   ⇒ 本夹具把这份内嵌 python 原样抽出来，喂八态库跑真语句：判据与读数都必须被真执行一次。
#
# 八态设计（每态各证一件事，缺任一态这条夹具就退化成 smoke；A–E 原有五态＋F/G/G2 挂起三态）：
#   A 排水态：active=55（超软上限 30）＋ 最近 24h 落终态 8 条 ⇒ 队列腿 **绿**，
#     且读数带 deferred_now=2/night_used_today=230（证明 NIGHTCAP 三条读数真打出来了——
#     漏绑定的那条语句就在这态里被执行，A 态一红即回归现形）；
#   B 停排态：active=55 ＋ 最近 24h 落终态 0 条 ⇒ 判红 reasons=backlog_not_draining_lt_6；
#   C 失控态：active=95（超失控线 90）＋ 落终态 8 条 ⇒ 判红 reasons=backlog_runaway_gt_90
#     （**恒红、不看吞吐**，这条就是旧尺子唯一的真价值被保下来的证据）；
#   D 摘参反证：把 A 态脚本里那条 COUNT 的占位符改成比绑定多一个 ⇒ 队列腿必回
#     err=ProgrammingError（证明 A 态的绿不是"语句没跑到"蒙的——摘锁反证同形）；
#   E 摘线反证：把 C 态脚本里的失控线分支摘掉 ⇒ 同一份 95 条积压库必须转绿
#     （证明 C 态那把红真由失控线量的，不是 stall/parked 顺带红的）；
#   F 挂起容忍态：active=10（不超软上限）＋ parked=1 且 --parked-max 1 ⇒ **绿**，
#     读数带 parked=1 parked_max=1 reasons=none（§QUEUE-THRESH 2026-10-15 owner 裁决
#     「保持挂起、容忍数临时放 1」的落码取证：容忍值真打进判据，而不是只写在注释里）；
#   G 第二条挂起态：同样 --parked-max 1 但库里挂着 2 条 ⇒ 具名红 reasons=parked_gt_1
#     （★ 这条是"报警没变哑"的正面证据：临时豁免只放行那一条已知挂起，
#      熔断闸再挂第二条照样当场红——容忍数从 0 抬到 1 换来的不是失明，是去掉天天人盯）；
#     另有 G2 摘线反证：把 parked 那一支摘掉 ⇒ 同一份挂两条的库必须转绿（红确有出处）。
#   F/G/G2 三态在 §QUEUE-THRESH 之前是不存在的：build_db 从来造不出 failed_needs_attention 行，
#     所以"挂起"这把尺子在原来的五态夹具里**一次都没被执行过**——今天要把容忍临时值改成 1，
#     等于给一把从没跑过的尺子改刻度，必须先让它跑起来（「度量腿要先自证射程」）。
#
# 时区一态（顺手钉死，不再靠人记）：所有 updated_at 都从**真实当前时间**派生（today±N），
#   绝不写死日历日期——写死的夹具隔日就因窗口边界失真而假红/假绿（§DET-TIME 那一族的第二形）。
#
# 用法：bash scripts/nightly_readout_fixture.sh            # 跑八态，全过退 0
#       bash scripts/nightly_readout_fixture.sh -v         # 附 A 态原始回显
# 依赖：python3（标准库 sqlite3 即可，零第三方包、零外网、零现网连接）。
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE_SRC="$ROOT/scripts/verify_nightly_guangzhou.sh"
VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILS=0
# 两把自证尺（§QUEUE-THRESH 2026-10-15 加）：
#   STATE_N 只由 state_done 在每态判据跑完后加一，LEGS_RUN 由 run_leg 每次真拨读数脚本时加一。
#   收尾要求 `STATE_N == LEGS_RUN` 且都 ≥8——**加了态却忘了拨**（或者拨了却没判）都会当场对不平。
#   为什么要这么绕：门禁原先钉的是字符串 `run_leg ` 出现 5 次，改文案/加注释就会把数字顶偏，
#   而"顶偏"和"少跑了一态"在锁面上长得一模一样；换成实跑计数后，读数本身就是射程证据
#   （「度量腿要先自证射程」＋「清单式锁改派生式」两课的合并落码）。
STATE_N=0
LEGS_RUN=0
state_done() { STATE_N=$((STATE_N + 1)); }
note() { echo "  $*"; }
red() { echo "--- FAIL: $*"; FAILS=$((FAILS + 1)); }

[ -f "$GATE_SRC" ] || { echo "--- FAIL: 源脚本不存在 $GATE_SRC"; exit 1; }

# ── 1) 把内嵌 python 读数块原样抽出（不复制第二份实现：抽的就是实跑那一份）──
awk '/^cat > "\$TMP_PY" <<.PYEOF.$/{f=1;next} /^PYEOF$/{f=0} f' "$GATE_SRC" > "$WORK/readout.py"
PY_LINES=$(grep -c . "$WORK/readout.py")
if [ "$PY_LINES" -lt 100 ]; then
	echo "--- FAIL: PYEOF 抽取只拿到 ${PY_LINES} 行＝抽取锚随源脚本改名而失效（读数没被真跑，一切绿都是蒙的）"
	exit 1
fi
echo "==> §QUEUE-BACKLOG 夹具：抽出内嵌读数脚本 ${PY_LINES} 行，八态离线实跑（零现网/零写入）"

# ── 2) 造态库：同建表语句、只换行数与时间戳（A/B/C 三份，E 复用 C 那份；F/G 复用同函数＋第 6 参挂起数）──
# $1=活跃条数 $2=最近24h落终态条数 $3=顺延条数 $4=夜账占用分钟 $5=输出库 [$6=挂起条数，默认 0]
build_db() {
	python3 - "$1" "$2" "$3" "$4" "$5" "${6:-0}" <<'PYBUILD'
import datetime as dt
import sqlite3
import sys

active_n, drained_n, deferred_n, night_min, path, parked_n = (
    int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4]), sys.argv[5],
    int(sys.argv[6]))
now = dt.datetime.now()
con = sqlite3.connect(path)
cur = con.cursor()
# 列集现网同构：§NIGHTCAP 三列（defer_day/night_day/night_used_min）＋ §QUEUE-REQUEUE 两列
# （requeue_total/fail_streak）＋ chain_day。少造一列＝夹具在测一条现网不存在的表形。
cur.execute("""CREATE TABLE research_tasks (
    id INTEGER PRIMARY KEY, type TEXT, status TEXT, progress TEXT, error TEXT,
    updated_at TEXT, chain_day TEXT, requeue_total INTEGER DEFAULT 0,
    fail_streak INTEGER DEFAULT 0, defer_day TEXT DEFAULT '',
    night_day TEXT DEFAULT '', night_used_min INTEGER DEFAULT 0)""")
# 其余六腿要的表也建出来（空表即可）：本夹具只断队列腿，但读数脚本会逐腿查表，
# 缺表会让脚本走异常分支并打印 FAIL| 行——那些行不该被误读成"判据红"。
for t, cols in (
    ("research_candidates", "id INTEGER PRIMARY KEY, created_at TEXT"),
    ("fina_indicator", "ts_code TEXT, report_date TEXT"),
    ("trade_cal", "cal_date TEXT, is_open INTEGER"),
    ("daily", "ts_code TEXT, trade_date TEXT"),
    ("index_daily", "ts_code TEXT, trade_date TEXT"),
):
    cur.execute("CREATE TABLE %s (%s)" % (t, cols))

def ts(hours_ago):
    return (now - dt.timedelta(hours=hours_ago)).strftime("%Y-%m-%d %H:%M:%S")

i = 1
for k in range(active_n):
    # 活跃行：deferred_n 条被顺延到「明天」（>今日 ⇒ 今晚不出队），其余只排队；
    #   夜账给第一条挂 night_day=今日＋night_min 占用，复现现网「队首吃掉大半个夜窗」那一形。
    defer = (now + dt.timedelta(days=1)).strftime("%Y%m%d") if k < deferred_n else ""
    nd, nm = ((now.strftime("%Y%m%d"), night_min) if k == 0 else ("", 0))
    cur.execute(
        "INSERT INTO research_tasks (id,type,status,progress,error,updated_at,chain_day,"
        "requeue_total,fail_streak,defer_day,night_day,night_used_min) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (i, "discover_factors", "queued", "0%", "", ts(6), "20261008", 1, 1, defer, nd, nm))
    i += 1
for k in range(drained_n):
    cur.execute(
        "INSERT INTO research_tasks (id,type,status,progress,error,updated_at,chain_day,"
        "requeue_total,fail_streak,defer_day,night_day,night_used_min) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (i, "backtest_nightly", "done", "100%", "", ts(3), "", 0, 0, "", "", 0))
    i += 1
# 一条"30 小时前落终态"的行：把 last_updated_age_h 钉在 stall 线（48h）之内、又落在
#   24h 吞吐窗口之外——否则 stall 与 backlog 两把尺子会同时红（夹具分不出是哪把量到的，
#   判据必须可归因），而 B 态的 drained_24h 也会被这条行抬成 1（窗口与写入口径同区那一课）。
cur.execute(
    "INSERT INTO research_tasks (id,type,status,progress,error,updated_at,chain_day,"
    "requeue_total,fail_streak,defer_day,night_day,night_used_min) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
    (i, "sector_rebuild", "done", "100%", "", ts(30), "", 0, 0, "", "", 0))
i += 1
# §QUEUE-THRESH（2026-10-15）挂起行：status 用熔断闸写的**同一个值** failed_needs_attention，
#   且它是"终态"之一 ⇒ 同时会进 drained_24h 的计数，所以时间戳按 30 小时前给（落在 24h 窗口外），
#   免得 F/G 态的吞吐读数被挂起行抬起来、让"绿/红"变得跟 parked_max 无关（判据必须可归因）。
#   error 文案带 #416 那种"人工挂起"痕迹只是给人看的，判据只看条数与 parked_max。
for k in range(parked_n):
    cur.execute(
        "INSERT INTO research_tasks (id,type,status,progress,error,updated_at,chain_day,"
        "requeue_total,fail_streak,defer_day,night_day,night_used_min) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (i, "discover_factors", "failed_needs_attention", "83%",
         "fixture parked %d (§QUEUE-THRESH 容忍态构造)" % (k + 1), ts(30), "20261008", 6, 6, "", "", 0))
    i += 1
con.commit()
con.close()
print("built active=%d drained=%d deferred=%d night_min=%d parked=%d"
      % (active_n, drained_n, deferred_n, night_min, parked_n))
PYBUILD
}

run_leg() { # $1=库 [$2=脚本，空串＝用抽出的那份] [$3..=额外阈值覆盖]
	local db="$1"; shift
	local script="$WORK/readout.py"
	# ★ 参数处理不能用原来那句 `local db="$1" script="${2:-…}"; shift 2`：
	#   只传 1 个参数时 `shift 2` **移位失败**（返回非 0、一个都没挪走），原来靠"$@ 没被用到"侥幸成立，
	#   一旦把尾部覆盖接上（本批就接了），库路径会被当成多余位置参数喂给 argparse，
	#   整条读数直接报错退出、队列行根本不出现（本批首跑实测：A/B/C 三态读数全空、F/G 两态却正常
	#   ——差的正是那两个参数）。现在按"有没有剩参数"分支挪，绝不靠侥幸。
	if [ $# -gt 0 ]; then
		[ -n "$1" ] && script="$1"
		shift
	fi
	# 阈值全部显式传参（不吃默认）：夹具要证的是判据本身，默认值等值另有静态锁钉三语言同源。
	# 尾部 "$@" 是把额外覆盖**追加**在同一行参数最后——python argparse 对重复的可选参数取最后一次，
	#   所以 `run_leg db "" --parked-max 1` 会把前面的 0 顶掉。
	# ★ 这个"$@" 在本行第一次被写出来时是**缺的**（注释写着"额外阈值覆盖"，实现却没接），
	#   任何调用者传的覆盖都会被静默丢掉、按 0 判——F 态正是这条的现形件：如果追加腿没接上，
	#   F 态（挂 1 条＋容忍 1）会按 parked_max=0 判红，夹具当场拦下（「度量腿要先自证射程」）。
	python3 "$script" --db "$db" --active-max 30 --task-age-hours 48 --cand-age-days 7 \
		--parked-max 0 --requeue-max 8 --drain-min-done 6 --backlog-runaway-max 90 "$@" 2>&1
	# ↑ 这条管道每拨一次就记一次账（自证射程：读数脚本真被执行了几遍，账面必须看得见）
	LEGS_RUN=$((LEGS_RUN + 1))
}

qline() { grep -aE '^(PASS|FAIL)\|queue:' "$1" | head -1; }

# ── A 态：排水中 ⇒ 队列腿绿，且三条 NIGHTCAP 读数真的打出（漏绑定的语句就在这态被执行）──
build_db 55 8 2 230 "$WORK/a.db" >/dev/null
run_leg "$WORK/a.db" > "$WORK/a.out"
LA=$(qline "$WORK/a.out")
if ! printf '%s' "$LA" | grep -q '^PASS|'; then
	red "A 态（排水中）应绿，实得：$LA"
else
	note "A ok 排水态绿：$(printf '%s' "$LA" | sed -E 's/.*reasons=/reasons=/')"
fi
# 读数在位性＝这条夹具的核心：deferred_now 必须是 2（不是 0、不是缺席），
# night_used_today 必须是 230。漏绑定当年就是把整条腿打成 err，所以这里同时要求整行不含 ProgrammingError。
for kv in 'deferred_now=2' 'night_used_today=230' 'drained_24h=8'; do
	printf '%s' "$LA" | grep -q "$kv" || red "A 态读数缺项：期望 ${kv}（缺＝那条语句没被执行或执行失败）"
done
printf '%s' "$LA" | grep -q 'ProgrammingError' && red "A 态仍出现 ProgrammingError＝绑定回归"
state_done A

# ── B 态：积压超软上限但一夜零落终态 ⇒ 具名红 backlog_not_draining ──
build_db 55 0 0 0 "$WORK/b.db" >/dev/null
run_leg "$WORK/b.db" > "$WORK/b.out"
LB=$(qline "$WORK/b.out")
printf '%s' "$LB" | grep -q '^FAIL|' || red "B 态（停排）应红，实得：$LB"
printf '%s' "$LB" | grep -q 'backlog_not_draining_lt_6' || red "B 态红得没具名（缺 backlog_not_draining_lt_6）：$LB"
[ "$FAILS" -eq 0 ] && note "B ok 停排态红：reasons=backlog_not_draining_lt_6"
state_done B

# ── C 态：积压冲破失控线 ⇒ 恒红，不看吞吐（吞吐这里给足 8 条）──
build_db 95 8 0 0 "$WORK/c.db" >/dev/null
run_leg "$WORK/c.db" > "$WORK/c.out"
LC=$(qline "$WORK/c.out")
printf '%s' "$LC" | grep -q 'backlog_runaway_gt_90' || red "C 态（失控线）应具名红 backlog_runaway_gt_90，实得：$LC"
[ "$FAILS" -eq 0 ] && note "C ok 失控态红：reasons 含 backlog_runaway_gt_90（吞吐 8 条也不放过）"
state_done C

# ── D 态：摘参反证——把 A 态脚本里的绑定删掉，必须退回现网当年那个 err ──
#   没有这条反证，A 态的绿有可能来自"那条 COUNT 压根没跑到"（本仓锤过的正腿蒙对形态）。
python3 - "$WORK/readout.py" "$WORK/readout_broken.py" <<'PYSED'
import pathlib
import sys
src = pathlib.Path(sys.argv[1]).read_text()
# 破坏方式＝让那条 COUNT 的占位符比绑定多一个（与当年现网同一失败形：Incorrect number of bindings）。
# 不改成"删掉绑定"是因为那里夹着修复说明注释，字符串会随注释增删漂移；加一个 ? 稳。
target = "\"AND COALESCE(defer_day,'')>?\",\n"
broken = src.replace(target, "\"AND COALESCE(defer_day,'')>? AND type<>?\",\n", 1)
if broken == src:
    print("PATCH-NOOP")
    sys.exit(1)
if broken.count("AND type<>?") != 1:
    print("PATCH-AMBIGUOUS %d" % broken.count("AND type<>?"))
    sys.exit(1)
pathlib.Path(sys.argv[2]).write_text(broken)
print("broken written")
PYSED
[ -f "$WORK/readout_broken.py" ] || red "D 态反证件生成失败（空替换＝no-op，这条反证压根没起作用）"
if [ -f "$WORK/readout_broken.py" ]; then
	run_leg "$WORK/a.db" "$WORK/readout_broken.py" > "$WORK/d.out"
	LD=$(qline "$WORK/d.out")
	printf '%s' "$LD" | grep -q 'ProgrammingError' \
		|| red "D 态摘绑反证不红（实得：${LD}）＝A 态的绿是蒙的，夹具抓不住当年那个现网缺陷"
	[ "$FAILS" -eq 0 ] && note "D ok 摘绑反证红：err=ProgrammingError（证明那条 COUNT 在 A 态真被执行）"
	state_done D
fi

# ── E 态：摘失控线反证——把 C 态脚本里的 runaway 分支摘掉，95 条积压应转绿 ──
#   没有这条，C 态的红有可能来自 stall/parked 那几把尺子（三条判据同形，摘错一把也照样红），
#   也就证明不了"失控线"这个出口真在拦东西（§负向腿恒零命中必须补反证 那一族）。
python3 - "$WORK/readout.py" "$WORK/readout_norw.py" <<'PYSED2'
import pathlib
import sys
src = pathlib.Path(sys.argv[1]).read_text()
broken = src.replace("if active > a.backlog_runaway_max:", "if False and active > a.backlog_runaway_max:", 1)
if broken == src or broken.count("if False and active >") != 1:
    print("PATCH2-NOOP")
    sys.exit(1)
pathlib.Path(sys.argv[2]).write_text(broken)
print("norw written")
PYSED2
[ -f "$WORK/readout_norw.py" ] || red "E 态反证件生成失败（空替换＝no-op）"
if [ -f "$WORK/readout_norw.py" ]; then
	run_leg "$WORK/c.db" "$WORK/readout_norw.py" > "$WORK/e.out"
	LE=$(qline "$WORK/e.out")
	# 期望的是「文案消失」而不是「文案还在」——断言方向按"摘掉后不该再有这个出口"写。
	# （10-15 本夹具首跑就是被这条的反向写法判红：grep -q 命中才是真，未命中 || red 于是"正确结果"触发红。）
	printf '%s' "$LE" | grep -q 'backlog_runaway_gt_90' \
		&& red "E 态摘线反证不成立：摘掉失控线后读数仍带 backlog_runaway（说明文案是从别处拼出来的，C 态的红不是那把线量到的）"
	printf '%s' "$LE" | grep -q '^PASS|' \
		|| red "E 态摘线反证不成立：摘掉失控线后 C 态仍红（实得 ${LE}）＝C 态的红压根不是那把线量到的"
	[ "$FAILS" -eq 0 ] && note "E ok 摘线反证：同一份 95 条积压库，摘掉失控线即转绿（C 态的红确有出处）"
	state_done E
fi

# ── F 态：挂起容忍（§QUEUE-THRESH 2026-10-15 owner 裁决＝「保持挂起、容忍数临时放 1」）──
#   库里挂着 1 条 failed_needs_attention（就是现网那条为了疏通队列被人为挂起的三年窗因子任务），
#   容忍值经 run_leg 追加的 --parked-max 1 透传 ⇒ 队列腿必须**绿**，且读数把两个数都打出来
#   （parked=1 parked_max=1 reasons=none）。这条同时自证两件事：
#     ① 追加参数腿真接上了（没接上就按 0 判红，F 态当场红——见 run_leg 那条 ★ 注释）；
#     ② 抬的是"调度入口的临时豁免"，不是判据本体（被调脚本默认值仍是 0，另有静态锁钉着）。
build_db 10 6 0 0 "$WORK/f.db" 1 >/dev/null
run_leg "$WORK/f.db" "" --parked-max 1 > "$WORK/f.out"
LF=$(qline "$WORK/f.out")
printf '%s' "$LF" | grep -q '^PASS|' || red "F 态（挂 1 条＋容忍 1）应绿，实得：$LF"
for kv in 'parked=1' 'parked_max=1' 'reasons=none'; do
	printf '%s' "$LF" | grep -q "$kv" || red "F 态读数缺项：期望 ${kv}（缺＝容忍值没真打进判据，豁免只写在注释里等于没豁免）"
done
[ "$FAILS" -eq 0 ] && note "F ok 挂起容忍态绿：parked=1 parked_max=1 reasons=none"
state_done F

# ── G 态：豁免只放行那一条，第二条挂起照样当场红（报警没变哑的正面证据）──
#   与 F 态**只差一行库形**（挂 1 条→挂 2 条），容忍值同一个 1：两态一比就是"这把尺子还在量"。
build_db 10 6 0 0 "$WORK/g.db" 2 >/dev/null
run_leg "$WORK/g.db" "" --parked-max 1 > "$WORK/g.out"
LG=$(qline "$WORK/g.out")
printf '%s' "$LG" | grep -q '^FAIL|' || red "G 态（挂 2 条、容忍 1）应红，实得：${LG}＝容忍值把整条挂起判据洗掉了"
printf '%s' "$LG" | grep -q 'parked_gt_1' || red "G 态红得没具名（缺 parked_gt_1）：$LG"
state_done G

# ── G2 摘线反证：把 parked 那一支摘掉，同一份挂两条的库必须转绿 ──
#   没有这条，G 态的红可能来自 stall/吞吐那几把尺子（与 E 态同一设计，理由同一）。
python3 - "$WORK/readout.py" "$WORK/readout_nopark.py" <<'PYSED3'
import pathlib
import sys

src = pathlib.Path(sys.argv[1]).read_text()
broken = src.replace("if parked > a.parked_max:", "if False and parked > a.parked_max:", 1)
if broken == src or broken.count("if False and parked >") != 1:
    print("PATCH3-NOOP")
    sys.exit(1)
pathlib.Path(sys.argv[2]).write_text(broken)
print("nopark written")
PYSED3
[ -f "$WORK/readout_nopark.py" ] || red "G2 态反证件生成失败（空替换＝no-op，这条反证压根没起作用）"
if [ -f "$WORK/readout_nopark.py" ]; then
	run_leg "$WORK/g.db" "$WORK/readout_nopark.py" --parked-max 1 > "$WORK/g2.out"
	LG2=$(qline "$WORK/g2.out")
	printf '%s' "$LG2" | grep -q 'parked_gt_1' \
		&& red "G2 摘线反证不成立：摘掉挂起那一支后读数仍带 parked_gt_1（说明文案是从别处拼的，G 态的红不是这把尺子量到的）"
	printf '%s' "$LG2" | grep -q '^PASS|' \
		|| red "G2 摘线反证不成立：摘掉挂起那一支后 G 态仍红（实得 ${LG2}）＝G 态的红压根不是这把尺子量到的"
	[ "$FAILS" -eq 0 ] && note "G2 ok 摘线反证：同一份挂两条的库，摘掉挂起判据即转绿（G 态的红确有出处）"
	state_done G2
fi

# ── 旧尺子不许复活：backlog_gt_%d 这个文案必须整体退出台账 ──
grep -q 'backlog_gt_%d' "$WORK/readout.py" && red "旧判据文案 backlog_gt_%d 仍在读数脚本里（两把尺子并存＝同一形态两个出口）"

if [ "$VERBOSE" -eq 1 ]; then
	echo "--- A 态原始回显"; cat "$WORK/a.out"
fi
# 自证射程两把尺（读数以实跑计数为准，不写死）：态数＝拨数＝≥8，任一不等＝"加了态没拨"或"拨了没判"。
echo "FIXTURE_COUNT states=${STATE_N} legs=${LEGS_RUN} min=8"
if [ "$STATE_N" -ne "$LEGS_RUN" ] || [ "$STATE_N" -lt 8 ]; then
	echo "FIXTURE_RESULT=fail reason=state-leg-mismatch states=${STATE_N} legs=${LEGS_RUN}（每态必须各拨一次读数脚本，且总数≥8）"
	exit 1
fi
if [ "$FAILS" -ne 0 ]; then
	echo "FIXTURE_RESULT=fail fails=$FAILS"
	exit 1
fi
echo "FIXTURE_RESULT=ok states=${STATE_N}（A 排水绿·B 停排红·C 失控红·D 摘绑反证红·E 摘线反证转绿·F 挂起容忍绿·G 第二条挂起具名红·G2 摘挂起线转绿）"
