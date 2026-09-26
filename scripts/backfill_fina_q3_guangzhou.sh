#!/usr/bin/env bash
# backfill_fina_q3_guangzhou.sh — §FINA-Q3（2026-09-26，owner 令「云端三季报重灌做了」）：
# 把**本机库已补齐的三季报（0930 期，2023 起）**推入广州研究库 trading.db 的
# fina_indicator / income 两张表，补上 §0925EVE-B1 锤实的「0930 期整季缺失」云端数据腿
# （§B2 因子回放喂财务、§B4 时点池的数据前提就卡在这条腿上）。
# **缺省＝只读复核（一次 SSH、零写入）；动手必须显式 -Apply。**
#
# 为什么要有这条脚本（现成链路覆盖不到的那一段）：
#   ① 云端取源被封（广州 baostock/hithink 的 IP 侧限制），三季报只能在**本机**补齐后推云；
#   ② 既有的 sync_delta_to_cloud.sh 是上一代 Linux 云（root@43.108...，/var/lib 路径）的
#      export-delta/import-delta 管线，对广州 Windows 现网（Administrator + C:/ 路径）不通；
#   ③ backfill_minute_guangzhou.sh 只管分钟 K 表；push_candidates 只管候选表。
#   所以照 §CAND-PUSH 的先例把这一跳变成仓库里的正规通道（同一套 ssh 口径、同一套判据形状）。
#
# 六条安全口径（每条都有代码落点）：
#   ① 缺省只读复核：远端一切查询走 sqlite3 **mode=ro** URI（连接即只读，拿不到写锁），
#      回传的判定行纯 ASCII；预览同时给远端 C 盘余量（护栏：-Apply 要求 free>=10GB，见⑤）。
#   ② 只补**缺失键**：以 (ts_code,end_date) 主键逐行判存在，已存在＝DUP 跳过；
#      全脚本对既有行零 UPDATE 零 DELETE（INSERT 撞主键按 DUP 计，绝不覆盖云端现值）。
#   ③ 双层回滚依据（与 §CAND-PUSH 的整库快照不同，理由在磁盘）：整库 6GB 级快照在
#      「C 盘余量护栏 8GB」的现网里再落一份 6GB 备份**本身就是磁盘事故**（09-23 磁盘红 +
#      09-25 候选备份 6.01GB 的实录都在同一块盘上）。本通道的写是纯增量插键，故：
#      第一层＝**插入键台账**（远端 backup_fina/inserted-<tag>.txt 逐行 ts_code|end_date|table，
#      撤销=按键删除这些自己插的行，不碰任何既有行）；第二层＝**受影响表的 Q3 窗口表级快照**
#      （ATTACH 一个全新小库、单事务 CREATE TABLE AS SELECT 把两表 0930 窗口整段抄进去，
#      一致性由同一读事务保证，体量只有几十 MB）。台账与快照都失败＝整轮不写。
#   ④ 列集对齐：本机导出腿先 PRAGMA table_info 取列，远端推送前逐表比对列集，
#      不一致＝ABORT=schema-mismatch（云端库版本错位不许静默插半行）。
#   ⑤ 备份/磁盘任一前置失败整轮中止，一条不写；插入前重新数一遍缺失键，
#      写完核对等式 inserted+dup==total 且 inserted==计划数（远端计划先于写落盘）。
#   ⑥ 写后复核＝重跑只读分布查询回显 POST 行（0930 计数与其余三季同量级是 owner 验收口径，
#      FIX_PLAN §0925EVE-B1），判绿只认判定行，绝不把"跑完了"当成功。
#
# cashflow 表的如实交代：本机库该表 0 行（2026-09-26 只读实测），本机没有的可推之源，
#   所以本通道只推 fina_indicator/income；复核腿会把远端 cashflow 的分布一并回显，
#   缺不缺、缺多少摆在读数里，补法属上游取数（dataload finance 的 cashflow 腿）另案。
#
# 转义与回传口径（§GBK/§CRLF/ASCII 自检三族教训照抄 push_candidates_guangzhou.sh 的成熟形状）：
#   远端编排 ps1 纯 ASCII（含非 ASCII 本机判红）；py 判定行全 ASCII；
#   解析远端回传前一律 tr -d '\r'；数字字段先验数字再进算术。
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/backfill_fina_q3_guangzhou.sh              # 只读复核（预览）
#   GZ_IP=81.71.69.17 ./scripts/backfill_fina_q3_guangzhou.sh -Apply       # 快照+台账 → 插缺失 → 复核
# 可选环境变量：
#   GZ_USER      默认 Administrator（与 deploy/verify/survey/CAND-PUSH 同一入口口径）
#   DATA_DIR     远端数据目录，默认 C:/var/lib/quant-trading-v2（研究库 trading.db 所在）
#   LOCAL_DB     本机研究库，默认 ~/.quant-trading-v2/trading.db（只读使用）
#   Q3_FROM      重灌起始年份（默认 2023，含当年 0930 期；FIX_PLAN 口径「重灌 2023 起全部 Q3」）
#   MIN_FREE_GB  -Apply 的磁盘护栏（默认 10GB：表级快照+台账的落盘余量，低于即拒写）
# 退出码：0=复核腿打印完成 / 推送且等式与 POST 分布复核全过；非 0=任一环节显式判红。
set -u

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP（与 deploy_guangzhou.sh 同一入口变量）}"
GZ_USER="${GZ_USER:-Administrator}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
LOCAL_DB="${LOCAL_DB:-$HOME/.quant-trading-v2/trading.db}"
Q3_FROM="${Q3_FROM:-2023}"
MIN_FREE_GB="${MIN_FREE_GB:-10}"

APPLY=0
for a in "$@"; do
  case "$a" in
    -Apply) APPLY=1 ;;
    *) echo "X 未知参数：${a}（只认 -Apply）" >&2; exit 2 ;;
  esac
done

SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"

TMP="$(mktemp -d /tmp/backfill_fina_q3_XXXXXX)"
REMOTE_STAGE=""
cleanup() {
  rm -rf "$TMP" 2>/dev/null || true
  if [ -n "$REMOTE_STAGE" ]; then
    case "$REMOTE_STAGE" in
      */backfill_fina_q3_*)
        $SSH "powershell -NoProfile -Command \"Remove-Item -Recurse -Force '${REMOTE_STAGE}'; exit 0\"" >/dev/null 2>&1 \
          || echo "[warn] 远端暂存目录未清（${REMOTE_STAGE}，内为本次载荷副本），请手工删除。" >&2 ;;
      *) echo "[warn] 远端暂存路径不符合 backfill_fina_q3_<PID> 形态，已拒绝删除：${REMOTE_STAGE}" >&2 ;;
    esac
  fi
}
trap cleanup EXIT

command -v python3 >/dev/null 2>&1 || { echo "X 本机缺 python3，显式失败（不静默跳过导表）。" >&2; exit 3; }
[ -f "$LOCAL_DB" ] || { echo "X 本机研究库不存在：${LOCAL_DB}（LOCAL_DB= 可指）" >&2; exit 1; }

TABLES="fina_indicator income"
Q3_MIN="${Q3_FROM}0101"

# ── [1] 本机导出腿：0930 期缺失候选行 → payload.jsonl + keys.txt（纯本地，只读）──────────
# 导出格式：JSONL 每行 {table, cols(下标即远端 INSERT 的列序来源), vals}；为防两表列序漂移，
# 载荷里**自带列名表**，远端按列名拼 INSERT（见推送腿），绝不按本机位置序号盲插远端表。
python3 - "$LOCAL_DB" "$Q3_MIN" "$TMP/payload.jsonl" "$TMP/keys.txt" $TABLES <<'PYEOF'
# 本机侧导表腿（fail-close 三件）：
#   1) 表/主键必须在（缺表或缺 (ts_code,end_date) 主键＝库版本错位，拒载）；
#   2) 只取 substr(end_date,5,4)='0930' 且 >= Q3_MIN 的行；
#   3) 载荷为空＝没东西可推，判失败（0 行不判成功，§MINUTE/CAND-PUSH 同款口径）。
import json, sqlite3, sys

db, q3min, outp, outk = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
tables = sys.argv[5:]
con = sqlite3.connect('file:%s?mode=ro' % db.replace('\\', '/'), uri=True)
con.row_factory = sqlite3.Row
n = 0
kf = open(outk, 'w', encoding='utf-8')
with open(outp, 'w', encoding='utf-8') as f:
    for t in tables:
        cols = [r[1] for r in con.execute('PRAGMA table_info(%s)' % t)]
        if not cols:
            sys.stderr.write('X 本机库没有表 %s\n' % t); sys.exit(1)
        if 'ts_code' not in cols or 'end_date' not in cols:
            sys.stderr.write('X 本机表 %s 缺 ts_code/end_date 列（结构错位）\n' % t); sys.exit(1)
        rows = con.execute(
            'SELECT * FROM %s WHERE end_date>=? AND substr(end_date,5,4)=?' % t,
            (q3min, '0930')).fetchall()
        for r in rows:
            rec = {'table': t, 'cols': cols, 'vals': {c: r[c] for c in cols}}
            f.write(json.dumps(rec, ensure_ascii=False) + '\n')
            kf.write('%s|%s|%s\n' % (t, r['ts_code'], r['end_date']))
            n += 1
kf.close()
if n == 0:
    sys.stderr.write('X 本机 0930 期载荷为空（%s 起），无源可推\n' % q3min); sys.exit(1)
print('FINA_LOCAL rows=%d' % n)
PYEOF
[ $? -eq 0 ] || exit 1
TOTAL="$(wc -l < "$TMP/payload.jsonl" | tr -d ' ')"
echo "本机 0930 期可推行数：${TOTAL}（${TABLES}，${Q3_FROM} 起）"

# ── [2] 远端三件套（只读复核 py / 推送 py / 编排 ps1），形状抄 §CAND-PUSH ──────────────
cat > "$TMP/review_remote.py" <<'PYEOF'
# 远端只读复核腿：mode=ro 连接，逐表打印季度分布（REVIEW 行）+ 对照 keys.txt 数缺失键
# （MISSING 行）。一切判定行纯 ASCII；busy_timeout 60s 只是"拿不到锁就等一会"，读不阻塞写。
import json, sqlite3, sys

db, keysfile, q3min = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    keys = {}
    for line in open(keysfile, encoding='utf-8'):
        t, code, ed = line.rstrip('\n').split('|')
        keys.setdefault(t, set()).add((code, ed))
    con = sqlite3.connect('file:%s?mode=ro' % db.replace('\\', '/'), uri=True, timeout=60)
    # C 盘余量随复核腿回显（DISK 行）：预览就要给 -Apply 护栏的判断依据，
    # 而不是等到 -Apply 才发现磁盘不够白跑一趟。读不到不判红（纯信息行）。
    try:
        import shutil
        _dir = db.replace('\\', '/')
        _dir = _dir.rsplit('/', 1)[0] if '/' in _dir else '.'
        print('DISK|free_gb=%s' % (shutil.disk_usage(_dir).free // (1024 ** 3)))
    except Exception as e:
        print('DISK|free_gb=ERR:%s' % type(e).__name__)
    for t in ('fina_indicator', 'income', 'cashflow'):
        cols = {r[1] for r in con.execute('PRAGMA table_info(%s)' % t)}
        if 'end_date' not in cols:
            print('REVIEW|table=%s state=no-table' % t)
            continue
        for q, n, codes in con.execute(
                "SELECT substr(end_date,5,4) q, COUNT(*), COUNT(DISTINCT ts_code) "
                "FROM %s WHERE end_date>=? GROUP BY q ORDER BY q" % t, (q3min,)):
            print('REVIEW|table=%s q=%s n=%s codes=%s' % (t, q, n, codes))
        want = keys.get(t, set())
        have = {(r[0], r[1]) for r in con.execute(
            "SELECT ts_code, end_date FROM %s WHERE end_date>=? AND substr(end_date,5,4)='0930'" % t,
            (q3min,))}
        print('MISSING|table=%s want=%s have=%s missing=%s' % (t, len(want), len(have & want), len(want - have)))
    print('FINA_REVIEW_OK')
except Exception as e:
    print('FINA_ERR=%s py=%s' % (type(e).__name__, sys.version_info[:3]))
    sys.exit(1)
PYEOF

cat > "$TMP/push_remote.py" <<'PYEOF'
# 远端推送腿，顺序固定、任何一步失败都非 0：
#   1) 磁盘护栏：C 盘 free < MIN_FREE_GB 即 ABORT（表级快照+台账落盘的余量前置，09-23 磁盘红教训）；
#   2) 表级快照：ATTACH 全新小库，单事务把两表 0930 窗口整段抄进去（Q3 窗口回滚的第二层依据）；
#   3) 插入台账：本次实际插入的每个键先落 ledger 文件（flush 后随 DONE 回显路径）——
#      纯增量插键的撤销=按键删自己插的行，不碰任何既有行；
#   4) 逐行 INSERT **只补缺失键**（远端自查，不信本机预数），列名表来自载荷（本机表结构
#      与远端列集必须一致，缺列即 ABORT=schema-mismatch），撞主键按 DUP 计（零 UPDATE 零 DELETE）；
#   5) 单事务：任何一行异常整轮回滚，绝不留"插了一半"；成功后重开只读分布查询回显 POST 行。
import json, os, sqlite3, sys

db, payload, q3min, ledger, snap, min_free = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5], int(sys.argv[6])
try:
    import shutil
    free_gb = shutil.disk_usage(db.rsplit('/', 1)[0] if '/' in db else '.').free // (1024 ** 3)
    if free_gb < min_free:
        print('ABORT=disk free_gb=%s below guard=%s' % (free_gb, min_free)); sys.exit(1)
    print('DISK|free_gb=%s guard=%s' % (free_gb, min_free))
    # 先收载荷：按表分组（列名表取该表第一行的 cols，行内 vals 必须与列名一一对应）
    rows_by_table = {}
    cols_by_table = {}
    for line in open(payload, encoding='utf-8'):
        rec = json.loads(line)
        t = rec['table']
        rows_by_table.setdefault(t, []).append(rec['vals'])
        if t not in cols_by_table:
            cols_by_table[t] = rec['cols']
        elif rec['cols'] != cols_by_table[t]:
            print('ABORT=payload-cols-drift table=%s' % t); sys.exit(1)
    con = sqlite3.connect(db.replace('\\', '/'), timeout=60)
    # 列集对齐（口径④）：远端必须有本机列名的全集，缺列＝版本错位，静默插半行比不插更坏。
    for t, cols in cols_by_table.items():
        have = {r[1] for r in con.execute('PRAGMA table_info(%s)' % t)}
        miss = [c for c in cols if c not in have]
        if miss:
            print('ABORT=schema-mismatch %s:%s' % (t, ','.join(miss))); sys.exit(1)
    # 快照（第二层依据）：ro 副本 + 单事务 AS SELECT，读端与推送端互不干扰。
    os.makedirs(os.path.dirname(snap), exist_ok=True)
    if os.path.exists(snap):
        if os.path.getsize(snap) == 0:
            os.remove(snap)
        else:
            print('ABORT=snap-exists'); sys.exit(1)
    ro = sqlite3.connect('file:%s?mode=ro' % db.replace('\\', '/'), uri=True, timeout=60)
    ro.execute("ATTACH DATABASE ? AS snap", (snap.replace('\\', '/'),))
    ro.execute('BEGIN')
    for t in cols_by_table:
        ro.execute("CREATE TABLE snap.%s_q3 AS SELECT * FROM main.%s "
                   "WHERE end_date>=? AND substr(end_date,5,4)='0930'" % (t, t), (q3min,))
    ro.execute('COMMIT')
    snap_n = sum(ro.execute('SELECT COUNT(*) FROM snap.%s_q3' % t).fetchone()[0] for t in cols_by_table)
    ro.close()
    print('SNAP_OK rows=%d path=%s' % (snap_n, snap))
    # 插入（第一层依据=台账）：一个事务内逐表逐行；键存在性远端自查，插一条记一条台账。
    ins = {}
    dup = {}
    lf = open(ledger, 'w', encoding='utf-8')
    try:
        con.execute('BEGIN')
        for t, rows in rows_by_table.items():
            cols = cols_by_table[t]
            ins.setdefault(t, 0); dup.setdefault(t, 0)
            sql = 'INSERT INTO %s (%s) VALUES (%s)' % (t, ','.join(cols), ','.join('?' * len(cols)))
            for v in rows:
                if con.execute('SELECT 1 FROM %s WHERE ts_code=? AND end_date=?' % t,
                               (v['ts_code'], v['end_date'])).fetchone() is not None:
                    dup[t] += 1
                    continue
                con.execute(sql, [v.get(c) for c in cols])
                ins[t] += 1
                lf.write('%s|%s|%s\n' % (t, v['ts_code'], v['end_date']))
        con.commit()
    except Exception:
        con.rollback()
        lf.close()
        raise
    lf.flush(); lf.close()
    total = sum(ins.values()) + sum(dup.values())
    for t in rows_by_table:
        print('TABLE|name=%s inserted=%s dup=%s' % (t, ins[t], dup[t]))
    print('FINA_BACKFILL_DONE total=%s inserted=%s dup=%s ledger=%s' % (
        total, sum(ins.values()), sum(dup.values()), ledger))
    # POST 分布：从库里回读（不复述刚写的变量），owner 验收口径的直接读数。
    post = sqlite3.connect('file:%s?mode=ro' % db.replace('\\', '/'), uri=True, timeout=60)
    for t in rows_by_table:
        for q, n in post.execute(
                "SELECT substr(end_date,5,4) q, COUNT(*) FROM %s "
                "WHERE end_date>=? GROUP BY q ORDER BY q" % t, (q3min,)):
            print('POST|table=%s q=%s n=%s' % (t, q, n))
    post.close()
except Exception as e:
    print('FINA_ERR=%s py=%s' % (type(e).__name__, sys.version_info[:3]))
    sys.exit(1)
PYEOF

cat > "$TMP/run.ps1" <<'PSEOF'
$ErrorActionPreference = 'Stop'
$stage = Split-Path -Parent $MyInvocation.MyCommand.Path
$db = '@DATA_DIR@/trading.db'
$bkdir = '@DATA_DIR@/backup_fina'
$tag = '@TAG@'
$apply = '@APPLY@'
$minFree = '@MIN_FREE@'
$pyExe = ''; $pyPre = @()
foreach ($cand in @('python', 'py')) {
  $pre = @(); if ($cand -eq 'py') { $pre = @('-3') }
  $o = ''
  try { $o = (& $cand ($pre + @('--version')) 2>&1 | Select-Object -First 1) } catch { $o = '' }
  if ("$o" -match 'Python 3') { $pyExe = $cand; $pyPre = $pre; break }
}
if (-not $pyExe) { Write-Output 'FINA_ERR=no-python3'; exit 1 }
Write-Output ('EXEC python|' + $pyExe)
New-Item -ItemType Directory -Force -Path $bkdir | Out-Null
if ($apply -eq '0') {
  & $pyExe ($pyPre + @((Join-Path $stage 'review_remote.py'), $db, (Join-Path $stage 'keys.txt'), '@Q3_MIN@'))
  if ($LASTEXITCODE -ne 0) { Write-Output 'ABORT=review-failed'; exit 1 }
  Write-Output 'PS_DONE'
  exit 0
}
& $pyExe ($pyPre + @((Join-Path $stage 'push_remote.py'), $db, (Join-Path $stage 'payload.jsonl'), '@Q3_MIN@', (Join-Path $bkdir ('inserted-' + $tag + '.txt')), (Join-Path $bkdir ('fina_q3_snap-' + $tag + '.db')), $minFree))
if ($LASTEXITCODE -ne 0) { Write-Output 'ABORT=push-failed'; exit 1 }
# Re-run the read-only review leg after push: POST lines + full quarter distribution
# in one echo, so the acceptance readout (0930 in line with other quarters) is directly visible.
# NOTE: comments inside this ps1 must stay pure ASCII (the outer self-check gate rejects otherwise).
& $pyExe ($pyPre + @((Join-Path $stage 'review_remote.py'), $db, (Join-Path $stage 'keys.txt'), '@Q3_MIN@'))
if ($LASTEXITCODE -ne 0) { Write-Output 'ABORT=post-review-failed'; exit 1 }
Write-Output 'PS_DONE'
PSEOF

GIT_SHA="$(git -C "$APP_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
TAG="finaq3-$(date +%Y%m%d-%H%M%S)-${GIT_SHA}"
sed -e "s|@DATA_DIR@|$DATA_DIR|g" -e "s|@TAG@|$TAG|g" -e "s|@APPLY@|$APPLY|g" \
    -e "s|@Q3_MIN@|$Q3_MIN|g" -e "s|@MIN_FREE@|$MIN_FREE_GB|g" "$TMP/run.ps1" > "$TMP/run.final.ps1" \
  || { echo "X 占位符替换失败" >&2; exit 1; }
mv "$TMP/run.final.ps1" "$TMP/run.ps1"
if LC_ALL=C grep -n '[^ -~]' "$TMP/run.ps1" >/dev/null 2>&1; then
  echo "X 远端 ps1 掺了非 ASCII 字符：会经 GBK 回传成乱码判据，中止。" >&2
  exit 1
fi

echo "== §FINA-Q3 $([ "$APPLY" = 1 ] && echo 'APPLY（真写）' || echo 'REVIEW（只读复核）') tag=$TAG =="

echo "==> BatchMode 预探测（不通即失败，绝不挂起）"
if ! $SSH "echo ok" >/dev/null 2>&1; then
  echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：本脚本不使用密码认证，直接失败。" >&2
  exit 1
fi
echo "ok - 通道可用"

REMOTE_STAGE="C:/Users/${GZ_USER}/AppData/Local/Temp/backfill_fina_q3_$$"
$SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path ${REMOTE_STAGE} | Out-Null; exit 0\"" >/dev/null 2>&1 \
  || { echo "X 远端暂存目录创建失败（${REMOTE_STAGE}）" >&2; exit 1; }
if [ "$APPLY" = 1 ]; then
  $SCP "$TMP/payload.jsonl" "$TMP/keys.txt" "$TMP/review_remote.py" "$TMP/push_remote.py" "$TMP/run.ps1" \
    "${GZ_USER}@${GZ_IP}:${REMOTE_STAGE}/" >/dev/null 2>&1 \
    || { echo "X 五件套上传失败——判失败，不猜哪一半到了。" >&2; exit 1; }
else
  $SCP "$TMP/keys.txt" "$TMP/review_remote.py" "$TMP/run.ps1" \
    "${GZ_USER}@${GZ_IP}:${REMOTE_STAGE}/" >/dev/null 2>&1 \
    || { echo "X 三件套上传失败——判失败，不猜哪一半到了。" >&2; exit 1; }
fi
echo "ok - 载荷已上传（$([ "$APPLY" = 1 ] && echo 'payload/keys/review/push/run 五件套' || echo 'keys/review/run 只读三件套')）"

echo "==> 远端执行"
OUT="$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${REMOTE_STAGE}/run.ps1" 2>&1 || true)"
printf '%s\n' "$OUT" > /tmp/backfill_fina_q3_guangzhou.log
OUT="$(printf '%s' "$OUT" | tr -d '\r')"
printf '%s\n' "$OUT" | grep -v '^$' || true
case "$OUT" in
  *FINA_ERR=*|*ABORT=*)
    echo "X 远端判定行含错误（详见 /tmp/backfill_fina_q3_guangzhou.log）——本轮**未确认成功**。" >&2
    exit 1 ;;
esac
case "$OUT" in
  *PS_DONE*) ;;
  *) echo "X 远端没回打 PS_DONE——编排断链，判失败。" >&2; exit 1 ;;
esac
printf '%s\n' "$OUT" | grep -q '^FINA_REVIEW_OK' || { echo "X 缺 FINA_REVIEW_OK 行（复核腿没跑完），判失败。" >&2; exit 1; }

if [ "$APPLY" = 0 ]; then
  echo
  echo "只读复核完成：一个字节都没写。上面 MISSING 行就是 -Apply 会补的计划数。"
  echo "FINA_Q3_REVIEW_DONE"
  exit 0
fi

echo "==> 等式复核（本机独立核，不采信远端自述的绿）"
DONE_LINE="$(printf '%s\n' "$OUT" | grep '^FINA_BACKFILL_DONE ' | tail -1 || true)"
[ -n "$DONE_LINE" ] || { echo "X 缺 FINA_BACKFILL_DONE 行，判失败。" >&2; exit 1; }
a_total="$(printf '%s' "$DONE_LINE" | tr ' ' '\n' | grep '^total=' | cut -d= -f2 || true)"
a_ins="$(printf '%s' "$DONE_LINE" | grep -o 'inserted=[0-9]*' | cut -d= -f2 || true)"
a_dup="$(printf '%s' "$DONE_LINE" | grep -o 'dup=[0-9]*' | cut -d= -f2 || true)"
a_ledger="$(printf '%s\n' "$DONE_LINE" | sed -n 's/.*ledger=\(.*\)$/\1/p' || true)"
case "${a_total}${a_ins}${a_dup}" in
  *[!0-9]*|"") echo "X 远端等式字段不全是数字（total=${a_total} inserted=${a_ins} dup=${a_dup}）——判失败。" >&2; exit 1 ;;
esac
[ "$a_total" = "$TOTAL" ] || { echo "X 远端 total=${a_total} != 本机载荷 ${TOTAL}——判失败。" >&2; exit 1; }
[ $((a_ins + a_dup)) -eq "$a_total" ] || { echo "X inserted+dup==total 不成立——半态嫌疑，判失败。" >&2; exit 1; }
# 台账行数与 inserted 等值（台账是第一层回滚依据，行数对不上＝回滚依据不可信，判红）。
LEDGER_REMOTE="$DATA_DIR/backup_fina/inserted-${TAG}.txt"
L_N="$($SSH "powershell -NoProfile -Command \"if (Test-Path -LiteralPath '${LEDGER_REMOTE}') { (Get-Content -LiteralPath '${LEDGER_REMOTE}').Count } else { Write-Output 'nolock' }\"" 2>/dev/null | tr -d '\r' | tail -1 || true)"
[ "$L_N" = "$a_ins" ] || { echo "X 台账行数(${L_N:-取读失败}) != inserted(${a_ins})——回滚依据不可信，判失败。" >&2; exit 1; }
printf '%s\n' "$OUT" | grep -q '^SNAP_OK' || { echo "X 缺 SNAP_OK 行——表级快照没落地，判失败。" >&2; exit 1; }
# 口径⑤的第二遍：写后只读复核里每张推送表的 missing 必须归 0（＝inserted 确实补平了计划数；
# MISSING 行来自独立只读连接从库里回读，不是把刚写的变量再打一遍）。
for t in $TABLES; do
  printf '%s\n' "$OUT" | grep "^MISSING|table=${t} " | tail -1 | grep -q 'missing=0$' \
    || { echo "X 写后复核 ${t} 的 MISSING 行未归 0——计划没补平或复核缺行，判失败。" >&2; exit 1; }
done
echo "ok - 推送复核：inserted=${a_ins} dup=${a_dup}（total=${a_total}）；台账=${LEDGER_REMOTE}（${L_N} 行）"
echo "   验收读数（POST 行）看 /tmp/backfill_fina_q3_guangzhou.log——0930 应与其余三季同量级。"
echo "FINA_Q3_APPLY_DONE inserted=${a_ins} dup=${a_dup} ledger=${LEDGER_REMOTE}"
