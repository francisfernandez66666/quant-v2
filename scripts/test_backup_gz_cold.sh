#!/usr/bin/env bash
# test_backup_gz_cold.sh — 冷备工具链的自证（scripts/backup_gz_cold.sh + backup_gz_cold_helper.py）
#
# 为什么要有它（而不只是"跑一次看看"）：本工具是 11-04 晚重装唯一的**基线产出腿**，
# 它自己坏掉的形态和它要防的形态一样贵——一份"看起来备好了"的半份基线。所以这里的每一条
# 都照着"摘掉它会红吗"来写：既有正锁（该收的都收到、该等的都等上），也有雷
# （植入多出/删除/内容不同三种漂移，必须逐条点名；植入撕裂库必须被 integrity_check 抓住）。
#
# 全程零网络、零现网：`--local-source` 把远端那半条腿整个换成本地夹具（传输层 stub 就这一处，
# 真跑的 ssh 路径不在本测试面内，见下面 NO_NETWORK 与文末 UNVERIFIED 两条读数）。第 12 组还反过来
# 锁一件事：**不点名 --host 就不许有任何连生产的动作**（旧形态是"没给 --local-source 就 ssh"，
# 那让 `--dry-run` 也会去敲现网的门）。夹具落在 $TMPDIR 里自己的临时目录，退出时先 chmod 回来再删。
#
# 覆盖的面（每组都带"植入什么雷、必须红在哪一行"，不是"没出现就算过"）：
#   1-2  干跑/预检零写入、收盘态不是今天就拒、容量 1.5 倍不够就拒、冻结未确认就拒、落点撞 §4.5 就拒
#   3-5  v1 全量：清单字段齐备、排除项带理由、清单不自引用、逐文件 sha 独立复算（另开一条 tar 读一遍）
#   6    B 类：明文不进普通包、容器逐件点名、容器 0600、值不落清单不落日志、口令对了能真解回来
#   7    封存＝只读
#   8    L1 有牙：多出/内容不同/缺失三种漂移逐条点名（注意植入时机必须在清单写好**之后**）
#   9    L2 有牙：撕裂库被 integrity_check 抓住并点名，不是 helper 当场崩
#   10-11 增量：只挑锚点之后的、v1 一个字节不重写、v2 指回 v1 的 sha、被动过的配置没登记就拒、
#        没变化就 SKIP（不出空版本）、v1 没过校验就不许出下一版
#   12-13 预检与 --verify 两条只读腿：零写入 + 容量/清单缺失都要拒
#   14    封存后被人改过一卷 → --verify 与 --incremental 都要抓住，且都不许多出一版清单
#   15    "清单说排除了、包里却真有它"这条明文闸自己会红（故意把清单与卷造得不一致）
#   16    §4.5 五条不可再生落点逐条拒 + 合法落点不误伤
#
# 判据行一律 ASCII 等值匹配（中文字面量经码页折叠会变 '?' 的教训见 AGENTS.md「运维口径」）。
# 本机 /bin/bash 是 3.2.57：双引号参数里的 $( … '^(a|b)=' … ) 会被解析坏（交出空串），
# 所以凡是"从读数里抠几个键"的断言都先把读数算进变量再传参 —— 否则判据是对着空串比，恒红/恒绿随机。
#
# 用法：bash scripts/test_backup_gz_cold.sh     # 任一断言失败即退出非零
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BCK="$ROOT/scripts/backup_gz_cold.sh"
HELP="$ROOT/scripts/backup_gz_cold_helper.py"
PASS=0
FAIL=0
FAILED_CASES=""
T="$(mktemp -d "${TMPDIR:-/tmp}/cold-backup-test-XXXXXX")"
SRC="$T/src"
WIR="$T/src-wiring"
OUT="$T/out"

cleanup() {
  chmod -R u+w "$OUT" 2>/dev/null || true
  rm -rf "$T" 2>/dev/null || true
}
trap cleanup EXIT

ck() { # $1=用例名 $2=实得 $3=应得
  if [ "$2" = "$3" ]; then
    PASS=$((PASS + 1))
    printf 'ok   - %s\n' "$1"
  else
    FAIL=$((FAIL + 1))
    FAILED_CASES="$FAILED_CASES $1"
    printf 'FAIL - %s\n       got=[%s]\n       want=[%s]\n' "$1" "$2" "$3"
  fi
}
has() { # $1=用例名 $2=文本 $3=必须出现的串
  if printf '%s' "$2" | grep -qF -- "$3"; then ck "$1" "yes" "yes"; else ck "$1" "no" "yes"; fi
}
hasnt() { # $1=用例名 $2=文本 $3=必须不出现的串
  if printf '%s' "$2" | grep -qF -- "$3"; then ck "$1" "present" "absent"; else ck "$1" "absent" "absent"; fi
}
py() { printf '%s' "$1" | python3 -c "$2" 2>/dev/null || echo ERR; }

# ── 夹具：一个"像现网"的 <DataDir>（两库 + 身份 + 审批产物 + 账号目录 + 名单外的新落盘件）──
build_fixture() {
  rm -rf "$SRC" "$WIR"; mkdir -p "$SRC/accounts/u1" "$SRC/config_history/rules" \
    "$SRC/qmt_gateway" "$WIR/web/assets"
  python3 - "$SRC" <<'PY'
import json, os, sqlite3, sys
src = sys.argv[1]
def db(name, tables, seed_orders):
    path = os.path.join(src, name)
    conn = sqlite3.connect(path)
    for t in tables:
        conn.execute('CREATE TABLE %s(id integer primary key, v int)' % t)
    if seed_orders:
        conn.execute("insert into orders values (1,1)")
        conn.execute("insert into orders values (2,2)")
    conn.commit(); conn.close()
db("live.db", ["real_positions","orders","fills","real_account",
               "manual_positions","manual_account"], True)
db("trading.db", ["signal_decisions"], False)
def w(rel, text):
    with open(os.path.join(src, rel), "w") as fh:
        fh.write(text)
w("auth.json", json.dumps({"users":[{"id":"a"},{"id":"b"}],"configs":[{"k":1},{"k":2}],
                           "invites":[],"tenants":[],"schema_version":3}))
w("config.json", '{"rules":{}}\n')
for n in ("applied_rules.json","applied_factors.json","applied_patterns.json","grayscale_rules.json"):
    w(n, '{"approved":true}\n')
w("watchlist.json", '{"codes":["000001"]}\n')
w("watchlist_u1.json", '{"codes":["000001","600588"]}\n')
w("accounts/u1/paper.json", '{"cash":100}\n')
w("accounts/u1/leg_status.json", '{"state":1}\n')
# 名单里没有的东西 —— 这条测试存在的理由（plan 1.2：按名单挑文件的备份必然漏它）
w("brand_new_artifact_from_next_batch.json", '{"surprise":true}\n')
# C 类与 B 类
w("qmt_gateway/gateway.py", "# must be excluded as bridge_gateway\n")
w("some_bridge_report.jsonl", '{"bridge":"excluded"}\n')
w("admin_session_token", "plaintext-token\n")
w("config_history/rules/r1.json", '{"gateway_token":"plain"}\n')
w("quant.env", "SETUP_TOKEN=envonlysecret\n")
w("config.xt.json", '{"account_tokens":[{"token":"gatewaysecrettoken"}]}\n')
json.dump({"ok": True, "accounts_files": 2}, open(os.path.join(src, "SNAPSHOT_OK_FIXTURE"), "w"))
wiring = os.path.join(os.path.dirname(src), "src-wiring")
os.makedirs(os.path.join(wiring, "web", "assets"), exist_ok=True)
open(os.path.join(wiring, "web", "BUILD_COMMIT"), "w").write("cafe123\n")
open(os.path.join(wiring, "web", "assets", "index-abc.js"), "w").write("console.log(1)\n")
open(os.path.join(wiring, "Caddyfile"), "w").write("site quant-trading.top\n")
PY
  TODAY="$(TZ=Asia/Shanghai date +%F)"
  printf '{"day":"%s","at":"15:05:12","orders_n":3,"positions_n":2,"trades_n":1}' "$TODAY" > "$T/close_ok.json"
  printf '{"day":"2020-01-01","at":"15:05:12"}' > "$T/close_stale.json"
  printf '{"build_commit":"cafe111"}' > "$T/status.json"
  printf '{"ok":true,"accounts_files":2}' > "$T/snapshot_ok.json"
}

ARGS_BASE=("--out-dir=$OUT" "--close-state-file=$T/close_ok.json"
           "--status-file=$T/status.json" "--snapshot-ok-file=$T/snapshot_ok.json")
run_full() {
  bash "$BCK" "--local-source=$SRC" "${ARGS_BASE[@]}" "$@" 2>&1
}
baseline_of() { ls -1d "$OUT"/cold-* 2>/dev/null | tail -1; }

printf 'NOTE - NO_NETWORK=1 transport=local ssh=0 fixture=%s\n' "$SRC"

# ── 1. 干跑：只出计划，零写入（基线目录必须还是空的）──────────────────────────────
build_fixture
mkdir -p "$OUT"
dry="$(run_full --dry-run)"
has "dry_run_prints_plan" "$dry" "COLD_BACKUP_RESULT=DRY"
has "dry_run_anchors" "$dry" "S0_ANCHORS frozen_at="
has "dry_run_single_copy_note" "$dry" "S0_SINGLE_COPY_NOTE"
ck "dry_run_writes_nothing" "$(find "$OUT" -type f | wc -l | tr -d ' ')" "0"
# 体量估算必须把两库算进去（快照此刻还不存在，只按枚举读数就会"永远通过"容量闸）
est="$(printf '%s' "$dry" | { grep -oE 'payload_bytes=[0-9]+' | head -1; })"
est_n="${est#payload_bytes=}"
db_total=0
for d in live.db trading.db; do
  db_total=$((db_total + $(wc -c < "$SRC/$d" | tr -d ' ')))
done
if [ "${est_n:-0}" -ge "$db_total" ] && [ "$db_total" -gt 0 ]; then
  ck "dry_run_estimate_includes_dbs" "yes" "yes"
else
  ck "dry_run_estimate_includes_dbs" "no" "yes"
fi

# ── 2. 预检三条该拦的必须拦住 ─────────────────────────────────────────────────────
# 容量逼红因子原为 999999，是按规划时「本机只剩 11 Gi」标定的；owner 清理后余量
# 涨到 52 Gi，小夹具包体 x 999999 已不超过余量 ⇒ 两条容量用例假红（闸没坏，是尺子
# 跟着磁盘水位漂移）。改用 1e9：余量再涨一个数量级也仍然逼红，不再依赖现场盘况。
stale="$(run_full --close-state-file="$T/close_stale.json" --frozen-confirmed 2>&1)"
has "close_state_not_today_aborts" "$stale" "reason=close_state_not_today"
tight="$(run_full --capacity-factor=1000000000 --frozen-confirmed 2>&1)"
has "capacity_shortfall_aborts" "$tight" "reason=capacity_shortfall"
naked="$(run_full 2>&1)"
has "freeze_must_be_confirmed" "$naked" "reason=freeze_unconfirmed"
printf '%s' "$naked" | grep -qE 'step=1 reason=freeze_unconfirmed' &&
  has "abort_line_shape" "yes" "yes" || has "abort_line_shape" "no" "yes"
for prot in "$HOME/backups/quant/probe" "$HOME/backups/quant/restic/probe" "$ROOT/docs/probe"; do
  guarded="$(bash "$BCK" "--local-source=$SRC" "--out-dir=$prot" \
    "--close-state-file=$T/close_ok.json" --dry-run 2>&1)"
  has "protected_refused:$prot" "$guarded" "reason=baseline_target_protected"
done

# ── 2b. 收盘日显式口径（owner 11-07 裁决：最近交易日 11-06，不伪造 close-state 文件）──
# 全部走 --preflight：S0 完整经过 close_guard，但零写入，不打扰第 3 组的 v1 基线。
pf_day="$(run_full --preflight --close-state-file="$T/close_stale.json" \
  --close-state-day=2020-01-01 2>&1)"
has "explicit_close_day_accepts_named_day" "$pf_day" "S0_CLOSE_STATE OK day=2020-01-01"
has "explicit_close_day_anchor" "$pf_day" "S0_CLOSE_DAY expected=2020-01-01 override=2020-01-01 ack=0"
has "explicit_close_day_preflight_ok" "$pf_day" "COLD_BACKUP_RESULT=PREFLIGHT"
# 反证：同一份「不是今日」的读数不给 override 仍须判红，expected 不能偷偷变成读数里的日期
pf_today="$(run_full --preflight --close-state-file="$T/close_stale.json" 2>&1)"
has "without_override_still_locks_today" "$pf_today" "expected=$TODAY"
has "without_override_still_aborts" "$pf_today" "reason=close_state_not_today"
has "bad_close_day_refused" "$(run_full --preflight --close-state-day=2020-1-1 2>&1)" \
  "reason=bad_close_state_day"
# ACK 不许单独点起来：那等于把 R14 悄悄关掉
has "ack_requires_explicit_day" "$(run_full --preflight --close-state-unavailable-ack 2>&1)" \
  "reason=close_day_required"
NONE="$T/no_close_state_here.json"
pf_ack="$(run_full --preflight --close-state-file="$NONE" \
  --close-state-day=2026-11-06 --close-state-unavailable-ack 2>&1)"
has "ack_unavailable_is_explicit" "$pf_ack" "S0_CLOSE_STATE ACK_UNAVAILABLE day=2026-11-06 src=owner_ack"
has "ack_day_anchor" "$pf_ack" "S0_CLOSE_DAY expected=2026-11-06 override=2026-11-06 ack=1"
hasnt "ack_never_fakes_a_readout" "$pf_ack" "S0_CLOSE_STATE OK"
has "ack_preflight_ok" "$pf_ack" "COLD_BACKUP_RESULT=PREFLIGHT"
has "unreadable_without_ack_still_aborts" \
  "$(run_full --preflight --close-state-file="$NONE" 2>&1)" "reason=close_state_unreadable"

# 静态反证（ssh 那条腿本地夹具跑不到，只能锁代码形态）：11-07 现网 --full 卡在 S1，
# 根因是拿 Windows 路径在本机做 -e 检查；同批还发现管道末端 tr/grep 的 rc 被当成
# 远端成败。这两条一旦回潮，本地 100 条全绿也照样在现网翻车。
instrument_ok="$(python3 - "$BCK" <<'PY'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
a = s.index("\ndb_snapshot() {")
b = s.index("\n}\n", a)
body = s[a:b]
h = s[s.index("\nhelper_run() {"):s.index("\nLAST_OUT=")]
def code_of(t):
    return "\n".join(ln for ln in t.splitlines() if not ln.lstrip().startswith("#"))
code = code_of(body)
hcode = code_of(h)
skips = s.count('[ "$TRANSPORT" = "ssh" ] || [ -e "$ABS_ROOT/$db" ]')
# 只禁「ssh 命令替换里套管道、再把 $? 当远端 rc」这个形态；拿到 rc 之后的
# printf | tr 文本清理是合法的，不该被误伤。
no_pipe_rc = ("2>&1 | tr -d" not in code and "rc=$?" in code
              and "2>&1 | tr -d" not in hcode and "rc=$?" in hcode)
# 远端 PowerShell 会话必须显式 exit 并带回 python 的 rc：否则 helper 写完卷也不关
# 连接（11-07 build 空挂 1.5h / .backup 留孤儿占文件句柄，都是这一处）。
exits = sum(1 for ln in s.splitlines()
            if not ln.lstrip().startswith("#") and "exit $LASTEXITCODE" in ln)
# 现网第二轮 S3 崩在「manifest 被发到远端」：ssh 形态必须按 mode 分流，
# manifest/verify/l2 留本机跑，否则现网 --full 永远拿不到 v1 基线。
split = ("manifest|verify|l2|hash|selfcheck|spec" in s
         and s.count("local_only") >= 2 and "helper_mode" in s)
# L2 的四件审批产物默认仍严查；显式豁免必须由基线 header 携带并被 L2 回显，
# 不许改成「缺了就算了」的恒绿判据。
h = open(sys.argv[1].replace("backup_gz_cold.sh", "backup_gz_cold_helper.py"),
         encoding="utf-8").read()
allow = ("applied_absent_allow" in s and "L2_APPLIED_ABSENT_ALLOWED" in h
         and "fails.append(\"applied_absent=%s\" % name)" in h)
print("yes" if (no_pipe_rc and skips == 2 and exits == 2 and split and allow) else "no")
PY
)"
ck "ssh_instrument_rc_and_local_check" "$instrument_ok" "yes"

# ── 3. 真跑 v1 ────────────────────────────────────────────────────────────────────
cold_b="$(mktemp -d "${TMPDIR:-/tmp}/cold-b-pass-XXXXXX")"
printf 'rehearsal-passphrase-not-a-real-secret' > "$cold_b/pass"
full="$(COLD_B_PASS_CMD="cat $cold_b/pass" run_full --frozen-confirmed \
        --volume-max-bytes=4096 --big-file-min-bytes=20000)"
if ! printf '%s' "$full" | grep -q "COLD_BACKUP_RESULT=OK"; then
  printf %s "---- full run output (debug) ----" "$full" "----------------------------------"
fi
has "full_run_ok" "$full" "COLD_BACKUP_RESULT=OK"
has "full_run_l1_ok" "$full" "L1_RESULT=OK"
has "full_run_l2_ok" "$full" "L2_RESULT=OK"
BASE="$(baseline_of)"
[ -n "$BASE" ] || { printf 'FAIL - 没有基线目录，后续断言无从谈起\n%s\n' "$full"; exit 1; }

MAN="$BASE/BACKUP_MANIFEST.json"
has "manifest_kept" "$(basename "$MAN")" "BACKUP_MANIFEST.json"

# ── 4. manifest 字段齐备（§5 S2 那张清单，逐条数出来而不是"看着像"）─────────────────
fields="$(py "$(cat "$MAN")" '
import json,sys
d=json.load(sys.stdin)
f=d["files"][0]
need_f={"rel_path","bytes","sha256","mtime","mode"}
h=d["header"]
need_h={"backup_id","frozen_at","frozen_at_epoch","build_commit","web_build_commit",
        "git_head","source_root","data_total_bytes","file_count"}
v=d["volumes"][0]
need_v={"name","bytes","sha256"}
e=d["excluded"][0]
print("file_fields=%s header_fields=%s vol_fields=%s excl_reason=%s ver=%s deleted_key=%s prev_key=%s" % (
    sorted(need_f - set(f)), sorted(need_h - set(h)), sorted(need_v - set(v)),
    bool(e.get("reason")), d["manifest_version"], "deleted" in d, "previous_manifest_sha256" in d))
')"
has "manifest_file_fields_complete" "$fields" "$(printf '%s' "$fields" | python3 -c 'import sys;print(sys.stdin.read().strip())')"
ck "manifest_no_missing_file_fields" \
  "$(printf '%s' "$fields" | sed -n 's/.*file_fields=\[\([^]]*\)\].*/\1/p')" ""
ck "manifest_no_missing_header_fields" \
  "$(printf '%s' "$fields" | sed -n 's/.*header_fields=\[\([^]]*\)\].*/\1/p')" ""
ck "manifest_no_missing_volume_fields" \
  "$(printf '%s' "$fields" | sed -n 's/.*vol_fields=\[\([^]]*\)\].*/\1/p')" ""
# 三条判据的取值是 Python 的 bool（打印出来是 True/False，首字母大写），本文件后面 v2 那组
# 用的是 [A-Za-z]* + "True"，这三条当年写成 [a-z]* + "true" 是抄漏了大小写：
# sed 捕获不到就打印空串，于是**无论代码对不对都恒红**（不是断言太松，是断言接不上自己的读数）。
# 只改读取用的正则与应得值的大小写，**判据本身一个字没动**：排除项必须带理由类别、
# v1 必须已经有 deleted/previous 两个键（增量要在它们上面接着长）。
ck "manifest_exclusion_has_reason" "$(printf '%s' "$fields" | sed -n 's/.*excl_reason=\([A-Za-z]*\).*/\1/p')" "True"
ck "manifest_version_is_1" "$(printf '%s' "$fields" | sed -n 's/.*ver=\([0-9]*\).*/\1/p')" "1"
ck "manifest_has_deleted_key" "$(printf '%s' "$fields" | sed -n 's/.*deleted_key=\([A-Za-z]*\).*/\1/p')" "True"
ck "manifest_has_prev_key" "$(printf '%s' "$fields" | sed -n 's/.*prev_key=\([A-Za-z]*\).*/\1/p')" "True"
# manifest 不给自己的哈希（build_friend_package.sh 那条"清单不自引用"的纪律）
self_hash="$(py "$(cat "$MAN")" '
import json,sys
d=json.load(sys.stdin)
print(sum(1 for e in d["files"] if "BACKUP_MANIFEST" in e["rel_path"]))
')"
ck "manifest_does_not_hash_itself" "$self_hash" "0"

# ── 5. 逐文件 sha 独立复算（不信工具自己的读数，另开一条tar读一遍）──────────────────
recheck="$(python3 - "$MAN" "$BASE/volumes" <<'PY'
import hashlib, json, os, sys, tarfile
man, vdir = sys.argv[1], sys.argv[2]
doc = json.load(open(man))
want = {e["rel_path"]: e for e in doc["files"]}
got = {}
for v in doc["volumes"]:
    with tarfile.open(os.path.join(vdir, v["name"]), "r:*") as tf:
        for m in tf.getmembers():
            if not m.isfile():
                continue
            data = tf.extractfile(m).read()
            got[m.name] = (len(data), hashlib.sha256(data).hexdigest())
missing = sorted(set(want) - set(got))
extra = sorted(set(got) - set(want))
bad = sorted(r for r in want if r in got and got[r][1] != want[r]["sha256"])
size = sorted(r for r in want if r in got and got[r][0] != want[r]["bytes"])
print("missing=%d extra=%d sha_bad=%d size_bad=%d" % (len(missing), len(extra), len(bad), len(size)))
PY
)"
ck "independent_per_file_sha_recheck_clean" "$recheck" "missing=0 extra=0 sha_bad=0 size_bad=0"
hasnt "brand_new_artifact_is_carried" "$(py "$(cat "$MAN")" '
import json,sys
print(" ".join(e["rel_path"] for e in json.load(sys.stdin)["files"]))')" "NOTHING"
ck "next_batch_artifact_present" \
  "$(py "$(cat "$MAN")" '
import json,sys
print(sum(1 for e in json.load(sys.stdin)["files"] if e["rel_path"]=="brand_new_artifact_from_next_batch.json"))')" "1"
ck "wiring_web_present" \
  "$(py "$(cat "$MAN")" '
import json,sys
print(sum(1 for e in json.load(sys.stdin)["files"] if e["rel_path"].startswith("wiring/web/")))')" "2"
ck "bridge_files_excluded" \
  "$(py "$(cat "$MAN")" '
import json,sys
d=json.load(sys.stdin)
print(sum(1 for e in d["files"] if e["rel_path"].startswith("qmt_gateway/") or e["rel_path"].endswith(".jsonl")))')" "0"

# ── 6. B 类：明文不进普通包，容器只留指纹，且能真解回来 ─────────────────────────────
b_check="$(python3 - "$MAN" "$BASE" <<'PY'
import json, os, subprocess, sys
man, base = sys.argv[1], sys.argv[2]
doc = json.load(open(man))
plain = [e["rel_path"] for e in doc["files"]
         if e["rel_path"] in ("quant.env", "config.xt.json", "admin_session_token")]
secrets = doc["secrets"]
containers = sorted({s["container"] for s in secrets})
leak = 0
if secrets:
    path = os.path.join(base, containers[0])
    mode = oct(os.stat(path).st_mode & 0o777)
    size = os.path.getsize(path)
else:
    mode, size = "none", 0
print("plain_secrets_in_archive=%d secrets_entries=%d container_mode=%s container_bytes=%d"
      % (len(plain), len(secrets), mode, size))
PY
)"
ck "b_class_not_in_plain_archive" "$(printf '%s' "$b_check" | sed -n 's/.*plain_secrets_in_archive=\([0-9]*\).*/\1/p')" "0"
# secrets_entries 应得值 3→4（**判据本身没动：容器必须逐件点名**）：夹具里种了四件 B 类
# ——quant.env、config.xt.json、admin_session_token、config_history/rules/（§4.1 明写它是
# config.json 的明文快照、含网关 token，按 B 类加密处理且"必须带走"，DATA_EXCLUDES 也确实按
# secret_class_b 把它从普通包里剔了）。要凑回 3 只有两种做法：要么清单少记一件（回传侧照清单
# 办事，等于把一把钥匙悄悄丢在原地），要么普通包里带着它（就是泄密）。两者都比这条数字错得多。
ck "b_class_container_recorded" "$(printf '%s' "$b_check" | sed -n 's/.*secrets_entries=\([0-9]*\).*/\1/p')" "4"
# 读数是 python oct() 的 "0o600"，原 sed 的 `0*\([0-9]*\)` 在 'o' 上捕获空串 ⇒ 恒红；
# 与上面那组同一种抄漏。把可选的 o 吃掉即可，判据（容器必须 0600、除属主不可读）一个字没动。
ck "b_class_container_mode_600" "$(printf '%s' "$b_check" | sed -n 's/.*container_mode=0*o\{0,1\}\([0-9]*\).*/\1/p')" "600"
hasnt "b_class_values_absent_in_manifest" "$(cat "$MAN")" "gatewaysecrettoken"
hasnt "b_class_values_absent_in_manifest2" "$(cat "$MAN")" "envonlysecret"
hasnt "b_class_values_absent_in_run_output" "$full" "gatewaysecrettoken"
# 解密自证：口令对了就该还原出原文（口令丢了 R20 就是"容器在、等于没带"）
dec="$(python3 - "$MAN" "$BASE" "$cold_b/pass" <<'PY'
import json, os, subprocess, sys, tarfile, tempfile
man, base, passfile = sys.argv[1], sys.argv[2], sys.argv[3]
doc = json.load(open(man))
if not doc["secrets"]:
    print("no_container"); raise SystemExit(0)
container = os.path.join(base, sorted({s["container"] for s in doc["secrets"]})[0])
tmp = tempfile.mkdtemp()
out_tar = os.path.join(tmp, "d.tar")
r = subprocess.run(["openssl", "enc", "-d", "-aes-256-cbc", "-pbkdf2", "-in", container,
                    "-out", out_tar, "-pass", "file:" + passfile])
if r.returncode != 0:
    print("decrypt_failed"); raise SystemExit(0)
with tarfile.open(out_tar) as tf:
    names = sorted(m.name for m in tf.getmembers() if m.isfile())
print("decrypt_ok members=%d" % len(names))
PY
)"
ck "b_class_container_decrypts" "$(printf '%s' "$dec" | cut -d= -f1)" "decrypt_ok members"

# ── 7. 封存 = 只读 ────────────────────────────────────────────────────────────────
if [ -w "$MAN" ]; then ck "baseline_sealed_readonly" "writable" "read-only"; else ck "baseline_sealed_readonly" "read-only" "read-only"; fi

# ── 8. L1 这把尺子有牙：植入三种漂移，必须逐条点名 ─────────────────────────────────
# 三条漂移都必须在**清单写好之后**才种下去，否则那根本不是漂移。原来 extra_file.json 与
# 枚举同步创建 ⇒ 清单里本来就有它，L1 无从判它"多出来"，这条用例是恒绿的摆设
# （它当时确实在 27 条"通过"里）。改的是**植入时机**，不是判据：断言仍是 L1_EXTRA 点名它。
drift="$T/drift"; mkdir -p "$drift"
cp "$SRC/auth.json" "$drift/auth.json"
cp "$SRC/config.json" "$drift/config.json"
spec="$T/drift-enum.spec"
{ printf 'mode=enumerate\ntag=drift\nwork=%s\nroot=%s||data|1\n' "$T" "$drift"; } > "$spec"
python3 "$HELP" --spec="$spec" >/dev/null
spec="$T/drift-man.spec"
{ printf 'mode=manifest\nwork=%s\nversion=1\nout_name=DRIFT_MANIFEST.json\nenum_file=%s/enum_drift.json\n' "$T" "$T"; } > "$spec"
python3 "$HELP" --spec="$spec" >/dev/null
printf 'x\n' > "$drift/extra_file.json"          # 清单之后多出来的件（多出）
printf 'changed\n' > "$drift/auth.json"          # 内容不同
rm -f "$drift/config.json"                        # 缺失
spec="$T/drift-verify.spec"
{ printf 'mode=verify\nmanifest=%s/DRIFT_MANIFEST.json\ndir=%s\n' "$T" "$drift"; } > "$spec"
l1_drift="$(python3 "$HELP" --spec="$spec" 2>&1)"
has "l1_names_content_diff" "$l1_drift" "L1_CONTENT|auth.json"
has "l1_names_missing" "$l1_drift" "L1_MISSING|config.json"
has "l1_names_extra" "$l1_drift" "L1_EXTRA|extra_file.json"
has "l1_fails_on_drift" "$l1_drift" "L1_RESULT=FAIL"

# ── 9. L2 抓到撕裂库（plan R2：大小正常、能打开、账却是坏的）────────────────────────
torn="$T/tornsrc"; mkdir -p "$torn/accounts/u1"
python3 - "$torn" <<'PY'
import json, os, sqlite3, sys
t = sys.argv[1]
c = sqlite3.connect(os.path.join(t, "live.db"))
c.execute("CREATE TABLE real_positions(id integer primary key, v int)")
c.execute("CREATE TABLE orders(id integer primary key, v int)")
c.execute("CREATE TABLE fills(id integer primary key, v int)")
c.execute("CREATE TABLE real_account(id integer primary key, v int)")
c.execute("CREATE TABLE manual_positions(id integer primary key, v int)")
c.execute("CREATE TABLE manual_account(id integer primary key, v int)")
c.commit(); c.close()
c = sqlite3.connect(os.path.join(t, "trading.db"))
c.execute("CREATE TABLE signal_decisions(id integer primary key, v int)")
c.commit(); c.close()
for n in ("applied_rules.json","applied_factors.json","applied_patterns.json","grayscale_rules.json"):
    open(os.path.join(t, n), "w").write("{}\n")
json.dump({"users": [{"id": "a"}], "configs": [], "invites": [], "tenants": [],
           "schema_version": 3}, open(os.path.join(t, "auth.json"), "w"))
open(os.path.join(t, "watchlist.json"), "w").write("{}\n")
PY
# 撕一刀：截断 live.db（裸 cp 一个正在写的 WAL 库就是这个形状）
python3 - "$torn/live.db" <<'PY'
import os, sys
p = sys.argv[1]
size = os.path.getsize(p)
with open(p, "r+b") as fh:
    fh.truncate(max(1024, int(size * 0.7)))
PY
spec="$T/torn-enum.spec"
{ printf 'mode=enumerate\ntag=torn\nwork=%s\nroot=%s||data|1\n' "$T" "$torn"; } > "$spec"
python3 "$HELP" --spec="$spec" >/dev/null
spec="$T/torn-build.spec"
{
  printf 'mode=build\nwork=%s\nvolumes_dir=%s/tornvols\nvolume_max_bytes=1048576\nbig_file_min_bytes=1048576\ncompress=1\n' "$T" "$T"
  printf 'enum_file=%s/enum_torn.json\n' "$T"
} > "$spec"
python3 "$HELP" --spec="$spec" >/dev/null
spec="$T/torn-man.spec"
{
  printf 'mode=manifest\nwork=%s\nversion=1\nout_name=TORN_MANIFEST.json\n' "$T"
  printf 'enum_file=%s/enum_torn.json\nvolumes_json=%s/volumes.json\n' "$T" "$T"
} > "$spec"
python3 "$HELP" --spec="$spec" >/dev/null
spec="$T/torn-l2.spec"
{ printf 'mode=l2\nmanifest=%s/TORN_MANIFEST.json\nvolumes_dir=%s/tornvols\n' "$T" "$T"; } > "$spec"
l2_torn="$(python3 "$HELP" --spec="$spec" 2>&1)"
has "l2_catches_torn_db" "$l2_torn" "L2_FAIL|integrity=live.db:"
has "l2_refuses_torn_baseline" "$l2_torn" "L2_RESULT=FAIL"

# ── 10. 增量：只挑冻结之后的；v1 留审计链；v2 才是基线 ─────────────────────────────
BEFORE_FILES="$(py "$(cat "$MAN")" 'import json,sys;print(json.load(sys.stdin)["totals"]["file_count"])')"
sleep 1
printf '{"late":true}\n' > "$SRC/late_arrival.json"          # 新增
printf '{"cash":999}\n' > "$SRC/accounts/u1/paper.json"      # 内容变
rm -f "$SRC/brand_new_artifact_from_next_batch.json"          # 删除
inc="$(run_full --incremental --base="$BASE" --frozen-confirmed \
      --volume-max-bytes=4096 --big-file-min-bytes=20000)"
has "incremental_ok" "$inc" "COLD_BACKUP_RESULT=OK"
has "incremental_v2" "$inc" "manifest=v2"
scan_line="$(printf '%s\n' "$inc" | { grep -E '^SCAN_SUMMARY\|' || true; } | head -1)"
# 本机 /bin/bash 是 3.2.57：`ck "名" "$(… grep -E '^(a|b)=' …)" "应得"` 这种"命令替换写在双引号
# 参数里"的写法，3.2 解析单引号内的括号会当命令替换提前收尾（实测报
# `command substitution: line N: syntax error: unexpected end of file` 并交出**空串**）。
# 空串对任何应得值都判红 —— 但同类形状在别处会让 hasnt() 拿到空文本直接"没找到"= 假绿。
# 所以先把读数算到变量里再传参（形状与上一行 scan_line 一致），判据一字未动。
counts_got="$(printf '%s' "$scan_line" | tr '|' '\n' | { grep -E '^(changed|added|deleted)=' || true; } | sort | tr '\n' ' ')"
ck "incremental_counts" "$counts_got" "added=1 changed=1 deleted=1 "
hasnt "incremental_only_named_paths" "$(printf '%s\n' "$inc" | { grep -E '^SCAN_DELETED\|' || true; })" "auth.json"
V2="$BASE/BACKUP_MANIFEST_v2.json"
ck "v1_kept_for_audit" "$([ -f "$MAN" ] && echo yes || echo no)" "yes"
ck "v2_written" "$([ -f "$V2" ] && echo yes || echo no)" "yes"
ck "pointer_is_v2" "$(cat "$BASE/BASELINE_MANIFEST" 2>/dev/null | tr -d '\n')" "BACKUP_MANIFEST_v2.json"
v2_checks="$(py "$(cat "$V2")" '
import json,sys
d=json.load(sys.stdin)
rel={e["rel_path"] for e in d["files"]}
print("ver=%s late=%s deleted=%s prev=%s secretes=%s delta_vols=%d" % (
    d["manifest_version"], "late_arrival.json" in rel,
    "brand_new_artifact_from_next_batch.json" not in rel,
    bool(d["previous_manifest_sha256"]), len(d["secrets"]),
    sum(1 for v in d["volumes"] if int(v["seq"]) >= 90)))
')"
has "v2_fields" "$v2_checks" "$(printf '%s' "$v2_checks" | python3 -c 'import sys;print(sys.stdin.read().strip())')"
ck "v2_version" "$(printf '%s' "$v2_checks" | sed -n 's/.*ver=\([0-9]*\).*/\1/p')" "2"
ck "v2_has_late_file" "$(printf '%s' "$v2_checks" | sed -n 's/.*late=\([A-Za-z]*\).*/\1/p')" "True"
ck "v2_drops_deleted" "$(printf '%s' "$v2_checks" | sed -n 's/.*deleted=\([A-Za-z]*\).*/\1/p')" "True"
ck "v2_links_previous" "$(printf '%s' "$v2_checks" | sed -n 's/.*prev=\([A-Za-z]*\).*/\1/p')" "True"
# 3→4 的理由与上面 b_class_container_recorded 同一条（夹具里是四件 B 类）。
# 这条判的本来是**增量不许把钥匙清单清空**（§4.6：v2 的 secrets[] 若变成 []，读起来就像
# "这份基线不带钥匙"，而容器明明还在原地）——件数跟着基线走才对，判据没动。
ck "v2_carries_secrets_forward" "$(printf '%s' "$v2_checks" | sed -n 's/.*secretes=\([0-9]*\).*/\1/p')" "4"
# v2 的 previous 必须真的等于 v1 的哈希（审计链接得上，不是随手写的字符串）
want_prev="$(sha_file() { shasum -a 256 "$1" | cut -d' ' -f1; }; sha_file "$MAN")"
got_prev="$(py "$(cat "$V2")" 'import json,sys;print(json.load(sys.stdin)["previous_manifest_sha256"])')"
ck "v2_previous_sha_matches_v1" "${got_prev:0:16}" "${want_prev:0:16}"
# 增量卷里只该有"变/增"两条，不该把整基线再搬一次
delta_members="$(python3 - "$V2" "$BASE/volumes-delta" <<'PY'
import json, os, sys, tarfile
doc = json.load(open(sys.argv[1]))
names = [v["name"] for v in doc["volumes"] if int(v.get("seq", 0)) >= 90]
seen = []
for n in names:
    with tarfile.open(os.path.join(sys.argv[2], n), "r:*") as tf:
        seen += [m.name for m in tf.getmembers() if m.isfile()]
print(",".join(sorted(seen)))
PY
)"
ck "delta_volume_contains_only_changes" "$delta_members" "accounts/u1/paper.json,late_arrival.json"
# v2 独立复算一遍（基线 = v1 卷 + 增量卷，最新一份赢）
v2_recheck="$(python3 - "$V2" "$BASE/volumes" "$BASE/volumes-delta" <<'PY'
import hashlib, json, os, sys, tarfile
man, v1dir, vdir = sys.argv[1], sys.argv[2], sys.argv[3]
doc = json.load(open(man))
want = {e["rel_path"]: e for e in doc["files"]}
got = {}
order = sorted(doc["volumes"], key=lambda v: (int(v.get("seq", 0)), v["name"]))
for v in order:
    d = vdir if int(v.get("seq", 0)) >= 90 else v1dir
    with tarfile.open(os.path.join(d, v["name"]), "r:*") as tf:
        for m in tf.getmembers():
            if m.isfile():
                got[m.name] = hashlib.sha256(tf.extractfile(m).read()).hexdigest()
missing = sorted(set(want) - set(got))
bad = sorted(r for r in want if r in got and got[r] != want[r]["sha256"])
shadow_only = sorted(r for r in got if r not in want and r in set(doc.get("deleted", [])))
extra = sorted(set(got) - set(want) - set(doc.get("deleted", [])))
print("missing=%d sha_bad=%d extra=%d kept_for_audit=%d" % (
    len(missing), len(bad), len(extra), len(shadow_only)))
PY
)"
ck "v2_independent_recheck_clean" "$v2_recheck" "missing=0 sha_bad=0 extra=0 kept_for_audit=1"

# ── 11. 增量的两条拒判 ────────────────────────────────────────────────────────────
# v1 没过校验就不许出 v2
fake="$T/fakebase"; mkdir -p "$fake/reports" "$fake/meta" "$fake/volumes"
cp "$MAN" "$fake/BACKUP_MANIFEST.json"
printf 'manifest_version=1\n' > "$fake/reports/VERIFY_v1.txt"
unverified="$(bash "$BCK" --local-source="$SRC" "--out-dir=$OUT" --incremental "--base=$fake" \
  "--close-state-file=$T/close_ok.json" "--status-file=$T/status.json" \
  "--snapshot-ok-file=$T/snapshot_ok.json" --skip-secrets --frozen-confirmed 2>&1)"
has "incremental_refuses_unverified_v1" "$unverified" "reason=v1_never_verified"
# 冻结后动过配置又没登记 → 拒（§5 S3b「不许差不多就行」）
printf '{"users":[{"id":"a"},{"id":"b"},{"id":"c"}],"configs":[],"invites":[],"tenants":[],"schema_version":3}' > "$SRC/auth.json"
unreg="$(run_full --incremental --base="$BASE" --frozen-confirmed 2>&1)"
has "incremental_refuses_unregistered_config_change" "$unreg" "reason=config_change_unregistered"
reg="$(run_full --incremental --base="$BASE" --frozen-confirmed \
      --config-change-registered="owner added u_c at 21:40, expected" 2>&1)"
has "incremental_allows_registered_config_change" "$reg" "COLD_BACKUP_RESULT=OK"
# 没有变化就不出空 v2
nod="$(run_full --incremental --base="$BASE" --frozen-confirmed --config-change-registered=x 2>&1)"
has "incremental_skips_empty_delta" "$nod" "COLD_BACKUP_RESULT=SKIP reason=no_delta_since_frozen"

# ── 12. 源端必须显式点名：不带 --local-source 的"干跑"绝不能顺手去连现网 ─────────────
# 旧形态是"没有 --local-source 就走 ssh"，于是 `--dry-run` 一敲也连生产机 ——
# 名字说"干跑"，脚已经踏进别人家。这条用例锁的就是这把闸的反面：**缺省不猜**。
nosrc="$(bash "$BCK" --dry-run "--out-dir=$T/out_nosrc" 2>&1)"
has "no_source_does_not_default_to_production" "$nosrc" "reason=source_undetermined"
hasnt "no_source_never_opens_ssh" "$nosrc" "ssh_batchmode_failed"
ck "no_source_writes_nothing" "$(find "$T/out_nosrc" -type f 2>/dev/null | wc -l | tr -d ' ')" "0"
# 反向半条也要有据：--host 是唯一的现网入口，所以"没点 --host 就不该有 ssh 形态的读数"。
# 现网那一腿本机不验（见文末 NOTE），这里只锁"缺省不猜"这半边 —— 它是本次唯一会造成
# 误连生产的形状，另一半边（BatchMode 探活不通即停）由脚本自身的 step=0 分支守着。
hasnt "no_ssh_probe_without_host" "$nosrc" "BatchMode"

# ── 13. 预检与校验两条只读腿：必须真的只读，而且必须真的有牙 ─────────────────────────
pf="$(bash "$BCK" "--local-source=$SRC" "${ARGS_BASE[@]}" "--out-dir=$T/out_pf" --preflight 2>&1)"
has "preflight_anchor" "$pf" "COLD_BACKUP_RESULT=PREFLIGHT"
has "preflight_reads_capacity" "$pf" "S0_CAPACITY"
has "preflight_reads_single_copy" "$pf" "S0_SINGLE_COPY_NOTE"
hasnt "preflight_needs_no_freeze" "$pf" "reason=freeze_unconfirmed"
ck "preflight_writes_nothing" "$(find "$T/out_pf" -type f 2>/dev/null | wc -l | tr -d ' ')" "0"
# 预检不是"报个数就走"：盘不够它就是那道"不开拉"的闸（§1.3 的 11 Gi / R1 半份备份）
pf_tight="$(bash "$BCK" "--local-source=$SRC" "${ARGS_BASE[@]}" "--out-dir=$T/out_pf" \
            --preflight --capacity-factor=1000000000 2>&1)"
has "preflight_refuses_tight_capacity" "$pf_tight" "reason=capacity_shortfall"
ck "preflight_tight_writes_nothing" "$(find "$T/out_pf" -type f 2>/dev/null | wc -l | tr -d ' ')" "0"

# --verify：照基线里在册的清单把 L1/L2 再跑一遍。先拿**没被动过**的基线证明它答"成立"，
# 再在 section 14 拿改过的副本证明它答"不成立并且点名"——只验前者等于验一把不会响的尺子。
verify_state() { # 基线目录指纹（文件数 + 全量内容哈希）：--verify 声称只读，就必须一字不差
  local d="$1"
  { find "$d" -type f | sort | wc -l | tr -d ' '; ( cd "$d" && find . -type f | sort |
      while IFS= read -r f; do shasum -a 256 "$f"; done ) | shasum -a 256 | cut -d' ' -f1; } | tr '\n' '/'
}
BEFORE_STATE="$(verify_state "$BASE")"
vo="$(bash "$BCK" "--verify=$BASE" 2>&1)"
has "verify_ok_anchor" "$vo" "COLD_BACKUP_RESULT=OK"
has "verify_l1_ok" "$vo" "L1_RESULT=OK"
has "verify_l2_ok" "$vo" "L2_RESULT=OK"
has "verify_names_baseline_manifest" "$vo" "verify=BACKUP_MANIFEST_v3.json"
ck "verify_writes_nothing" "$(verify_state "$BASE")" "$BEFORE_STATE"
# 指到没有清单的目录 = 点名拒，不是"跑完了没找到就算过"
mkdir -p "$T/emptybase"
has "verify_refuses_manifestless_dir" "$(bash "$BCK" "--verify=$T/emptybase" 2>&1)" \
  "reason=verify_manifest_absent"

# ── 14. v1 被封存之后被人动过 → 校验与增量都必须抓住，且都不许给新版盖 blessing ───────
# 这一条是 §9 R1 最阴的形状：东西还在、大小还对、内容已经变了。
tam="$T/tampered"; cp -R "$BASE" "$tam"; chmod -R u+w "$tam"
python3 - "$tam" <<'PY'
import io, os, sys, tarfile, tempfile
tam = sys.argv[1]
target = "watchlist.json"          # 从冻结起就没变过、只在 v1 普通卷里的一条
done = ""
for vdir in ("volumes", "volumes-delta"):
    d = os.path.join(tam, vdir)
    if not os.path.isdir(d):
        continue
    hit = False
    for name in sorted(os.listdir(d)):
        if not name.endswith(".tar.gz"):
            continue
        path = os.path.join(d, name)
        tmp = os.path.join(tempfile.gettempdir(), "retar-" + name)
        with tarfile.open(path, "r:gz") as tf, tarfile.open(tmp, "w:gz") as out:
            for m in tf.getmembers():
                fh = tf.extractfile(m)
                if fh is None:
                    continue
                data = fh.read()
                if m.name == target:
                    data = data.replace(b"000001", b"999999")   # 只改这一条，其余原样
                    hit = True
                info = tarfile.TarInfo(m.name)
                info.size = len(data)
                info.mode = m.mode
                info.mtime = m.mtime
                out.addfile(info, io.BytesIO(data))
        if not hit:
            os.unlink(tmp)
            continue
        os.replace(tmp, path)
        done = os.path.join(vdir, name)
        break
    if done:
        break
print("TAMPERED|vol=%s|member=%s" % (done or "none", target))
PY
vt="$(bash "$BCK" "--verify=$tam" 2>&1)"
has "tampered_verify_names_the_file" "$vt" "L1_CONTENT|watchlist.json"
has "tampered_verify_aborts" "$vt" "COLD_BACKUP_RESULT=ABORT step=3 reason=l1_failed"
hasnt "tampered_verify_never_says_ok" "$vt" "COLD_BACKUP_RESULT=OK"
# 同一份被动过的基线上做增量：先种一件真变化（否则增量按设计直接 SKIP，根本走不到校验，
# 那条"要拒"的断言就会变成对着一扇没人的门开枪）。有了变化才会读卷集，才会撞上被改的那卷。
printf '{"another":true}\n' > "$SRC/another_late.json"
MANIFESTS_BEFORE="$(find "$tam" -name 'BACKUP_MANIFEST*.json' | wc -l | tr -d ' ')"
DVOLS_BEFORE="$(find "$tam/volumes-delta" -type f 2>/dev/null | wc -l | tr -d ' ')"
Pointer_BEFORE="$(cat "$tam/BASELINE_MANIFEST" | tr -d '\n')"
ti="$(bash "$BCK" "--local-source=$SRC" "${ARGS_BASE[@]}" --incremental "--base=$tam" \
      --frozen-confirmed --skip-secrets 2>&1)"
has "tampered_incremental_refused" "$ti" "reason=l1_failed"
hasnt "tampered_incremental_not_blessed" "$ti" "S3B_BASELINE_BLESSED"
hasnt "tampered_incremental_says_ok" "$ti" "COLD_BACKUP_RESULT=OK"
ck "tampered_incremental_adds_no_manifest" \
  "$(find "$tam" -name 'BACKUP_MANIFEST*.json' | wc -l | tr -d ' ')" "$MANIFESTS_BEFORE"
# 失败的一轮连"多出来的卷"都不许留在基线里（增量卷在 $WORK 里落好、校验过才搬进去；
# 基线目录比清单多东西＝R1 半份备份的形状，回传侧照清单搬、照目录数会对不上）
ck "tampered_incremental_adds_no_volume" \
  "$(find "$tam/volumes-delta" -type f 2>/dev/null | wc -l | tr -d ' ')" "$DVOLS_BEFORE"
ck "tampered_incremental_keeps_pointer" "$(cat "$tam/BASELINE_MANIFEST" | tr -d '\n')" "$Pointer_BEFORE"
# 而且退出时要把解封的基线**封回只读**（半途失败留下一个可写的封存基线，下一次谁都能改它）
ck "tampered_incremental_resealed" "$([ -w "$tam/BACKUP_MANIFEST_v3.json" ] && echo writable || echo read-only)" "read-only"

# ── 15. 明文闸的牙：清单说"这件有意排除"，包里却真的有它 → 必须点名（§4.7 / §4.6）─────
# 单靠"普通包里没有密钥"这一句正向断言是不够的：排除规则写坏了（模式对不上、或新密钥文件名
# 不在名单里）时，manifest 与包体出自**同一份枚举**，两边一起漏、一起自洽，正向读数为零。
# 这里故意把两边造得不一致（清单按"已排除"写，卷按"没排除"打），验 L1 的 check_excluded 真会响。
ls_dir="$T/leaksrc"; mkdir -p "$ls_dir"
printf '{"account_tokens":[{"token":"gatewaysecrettoken"}]}\n' > "$ls_dir/config.xt.json"
printf '{"users":[],"configs":[],"invites":[],"tenants":[],"schema_version":3}\n' > "$ls_dir/auth.json"
printf '{}\n' > "$ls_dir/watchlist.json"
{
  printf 'mode=enumerate\ntag=leakman\nwork=%s\nroot=%s||data|1\n' "$T" "$ls_dir"
  printf 'exclude=config.xt.json|secret_class_b\n'
} > "$T/leak-manifest.spec"
python3 "$HELP" --spec="$T/leak-manifest.spec" >/dev/null
{ printf 'mode=enumerate\ntag=leakvol\nwork=%s\nroot=%s||data|1\n' "$T" "$ls_dir"; } > "$T/leak-enum.spec"
python3 "$HELP" --spec="$T/leak-enum.spec" >/dev/null
{
  printf 'mode=build\nwork=%s\nvolumes_dir=%s/leakvols\nvolume_max_bytes=1048576\nbig_file_min_bytes=1048576\ncompress=1\n' "$T" "$T"
  printf 'enum_file=%s/enum_leakvol.json\nvolumes_out=%s/leak_volumes.json\n' "$T" "$T"
} > "$T/leak-build.spec"
python3 "$HELP" --spec="$T/leak-build.spec" >/dev/null
{
  printf 'mode=manifest\nwork=%s\nversion=1\nout_name=LEAK_MANIFEST.json\n' "$T"
  printf 'enum_file=%s/enum_leakman.json\nvolumes_json=%s/leak_volumes.json\n' "$T" "$T"
} > "$T/leak-man.spec"
python3 "$HELP" --spec="$T/leak-man.spec" >/dev/null
{ printf 'mode=verify\nmanifest=%s/LEAK_MANIFEST.json\nvolumes_dir=%s/leakvols\ncheck_excluded=1\n' "$T" "$T"; } > "$T/leak-verify.spec"
leak_out="$(python3 "$HELP" --spec="$T/leak-verify.spec" 2>&1)"
has "leak_guard_names_the_secret" "$leak_out" "L1_EXCLUDED_PRESENT|config.xt.json"
has "leak_guard_fails_the_run" "$leak_out" "L1_RESULT=FAIL"
# 反证这组用例自己有牙：同样的卷、同样的包，**不带** check_excluded 时它就该只是"多一件"，
# 说明上面那条红是这道闸抓的，不是别的东西顺带红了。
{ printf 'mode=verify\nmanifest=%s/LEAK_MANIFEST.json\nvolumes_dir=%s/leakvols\n' "$T" "$T"; } > "$T/leak-verify-off.spec"
leak_off="$(python3 "$HELP" --spec="$T/leak-verify-off.spec" 2>&1)"
hasnt "leak_guard_is_the_one_firing" "$leak_off" "L1_EXCLUDED_PRESENT"

# ── 16. §4.5 不可再生资产：落点/删除目标逐条点名（四条形同的路径都得拒）────────────────
# 这四条就是"腾空间"时最容易被顺手删掉的东西：restic 异地仓、它的父目录、本地历史副本、
# 客户级 UAT 证据。判定按**路径分段**，所以 ~/backups/quant-cold（本工具的合法落点）不被误伤。
for prot in "$HOME/backups/quant/probe" "$HOME/backups/quant/restic/probe" \
            "$ROOT/docs/probe" "$HOME/.quant-trading-v2/probe" "$ROOT/tools/pub_uat/probe"; do
  guarded="$(bash "$BCK" "--local-source=$SRC" "--out-dir=$prot" \
    "--close-state-file=$T/close_ok.json" --dry-run 2>&1)"
  has "protected_refused:$prot" "$guarded" "reason=baseline_target_protected"
done
# 合法落点不能被这条闸误伤（前缀判定就会在这里把自家目录判成违规）
hasnt "protected_allows_own_baseline_root" "$(bash "$BCK" "--local-source=$SRC" \
  "--out-dir=$HOME/backups/quant-cold" "--close-state-file=$T/close_ok.json" --dry-run 2>&1)" \
  "reason=baseline_target_protected"

# ── 17. 现网 SSH 腿今天验不了，就把"没验"如实说出来 ────────────────────────────────
printf 'NOTE - UNVERIFIED ssh_transport=not_exercised_here reason=no_production_touch (rehearsal uses --local-source only; --host path proven only up to the BatchMode probe)\n'

printf '\n==== COLD_BACKUP_TEST result: PASS=%s FAIL=%s%b\n' "$PASS" "$FAIL" "${FAILED_CASES:+ failed:${FAILED_CASES}}"
[ "$FAIL" = "0" ] || exit 1
exit 0
