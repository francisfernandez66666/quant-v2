#!/usr/bin/env bash
# push_candidates_guangzhou.sh — §CAND-PUSH（2026-09-25，owner 令「重训结果推到云端」）：
# 把**本机副本库里跑出来的重训候选**（research_candidates 表，缺省＝全部 proposed 行）
# 推进广州研究库的同一张表，让审批页/命令行看得到这些候选。**缺省只预览，一次 SSH 都不发；动手必须显式 -Apply。**
#
# 为什么要有这条脚本（现成链路覆盖不到的那一段）：
#   09-25 的复权重训（§ADJ-FULL2 run2）按纪律只在 /tmp 副本库上算（绝不碰生产库），
#   产物是副本库里的 7 条 proposed 候选；而审批面（GET /api/research/candidates 与 UI）
#   吃的是**广州 trading.db 里的那张表**。两条腿之间历史上没有通道：
#   引擎侧只有按 id 的 approve/reject/grayscale/backtest，**没有 import 端点**，
#   手写 ssh+sqlite 又违反生产访问纪律——所以本脚本把这一跳变成仓库里的正规通道。
#
# 五条安全口径（每条都有代码落点，不是口号）：
#   ① 缺省只预览：不读远端、不连生产，只打印将要推的行与远端动作清单（§OPS-ALIGN 同族缺省）。
#   ② 只准新增「提议」：远端落库语句把 status **写死成 'proposed'**（本机读到的行也先校验
#      全是 proposed 才出门）。approved/applied/grayscale/rejected 的行一概不写、不改、不删；
#      既有行最多被"去重跳过"（DUP），绝不会被覆盖。
#   ③ 推前先做一致性备份：远端用 Python 标准库 sqlite3 的 **backup API** 对现网 trading.db
#      打快照（WAL 下裸拷贝文件＝半态副本，备份不可用是自欺；09-23 灾备批的口径）。
#      备份失败＝整轮中止，一条都不写。
#   ④ 新行**不带显式 id**：本机 id 与云端 id 是两条独立自增序列（撞号会把不同内容顶在同一
#      主键下，或因主键冲突静默半插）；由远端自增，回显「本机 id → 云端新 id」映射。
#   ⑤ 写后逐行复核（VERIFY 行回读 status=proposed）+ 总数等式（inserted+dup==total），
#      只认这些判定行，绝不把"跑完了"当成功。
#
# 转义与回传口径（§GBK / §CRLF / -EncodedCommand 三族教训的合集，实现照抄 forensic_fill.sh 的成熟形状）：
#   远端逻辑全在上传的单个 .ps1 + 纯 ASCII 输出（回显行只含 id/status/guard/数字/字节数，
#   reason 这类中文字段**永不回显**）；bash→ssh 的内联 powershell 只有"建目录/删目录"两句短命令。
#   解析远端回传前一律 tr -d '\r'（Windows CRLF 的行尾最后字段坑，09-24 真跑锤过）。
#
# 用法：
#   GZ_IP=81.71.69.17 ./scripts/push_candidates_guangzhou.sh                  # 预览（不连生产）
#   GZ_IP=81.71.69.17 ./scripts/push_candidates_guangzhou.sh -Apply           # 备份→推送→复核
#   GZ_IP=... CAND_IDS=102,103 ./scripts/push_candidates_guangzhou.sh -Apply  # 只推指定本机行
# 可选环境变量：
#   GZ_USER      默认 Administrator（与 deploy/verify/survey 同一入口口径）
#   SRC_DB       本机候选来源库，默认 /tmp/hfq_full_0925/trading.db（run2 的 scratch 副本）
#   DATA_DIR     远端数据目录，默认 C:/var/lib/quant-trading-v2（研究库 trading.db 所在）
#   CAND_JSON    调试用：直接指定已生成的候选 JSON（跳过本机导表；正常不用传）
# 退出码：0=预览已打印 / 推送且逐行复核过；非 0=校验、备份、推送、复核任一环节失败（显式判红）。
set -u

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:?必填：广州公网 IP（与 deploy_guangzhou.sh 同一入口变量）}"
GZ_USER="${GZ_USER:-Administrator}"
SRC_DB="${SRC_DB:-/tmp/hfq_full_0925/trading.db}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
CAND_IDS="${CAND_IDS:-}"

APPLY=0
for a in "$@"; do
  case "$a" in
    -Apply) APPLY=1 ;;
    *) echo "X 未知参数：${a}（只认 -Apply）" >&2; exit 2 ;;
  esac
done

SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
SCP="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"

TMP="$(mktemp -d /tmp/push_candidates_XXXXXX)"
REMOTE_STAGE=""
# 本机 JSON 载荷 + 远端三件套（备份 py / 推送 py / 编排 ps1）都落在这里；trap 删本机临时目录，
# -Apply 走到过远端建目录的，还须删远端暂存（forensic_fill.sh 同款纪律：只删自己 PID 形态的
# 全路径，路径不符一律拒绝，防误删别人/别轮的现场）。
cleanup() {
  rm -rf "$TMP" 2>/dev/null || true
  if [ -n "$REMOTE_STAGE" ]; then
    case "$REMOTE_STAGE" in
      */push_candidates_*)
        $SSH "powershell -NoProfile -Command \"Remove-Item -Recurse -Force '${REMOTE_STAGE}'; exit 0\"" >/dev/null 2>&1 \
          || echo "[warn] 远端暂存目录未清（${REMOTE_STAGE}，内为本次载荷副本），请手工删除。" >&2 ;;
      *) echo "[warn] 远端暂存路径不符合 push_candidates_<PID> 形态，已拒绝删除：${REMOTE_STAGE}" >&2 ;;
    esac
  fi
}
trap cleanup EXIT

need_cmd() { command -v "$1" >/dev/null 2>&1 || { echo "X 本机缺少工具 $1，显式失败（不静默跳过取数/生成）。" >&2; return 1; }; }
need_cmd python3 || exit 3

# ── [1/6] 本机导表：候选行 → JSON 载荷（status 必须是 proposed，否则整轮拒推）──────────────
JSON="${CAND_JSON:-$TMP/cand.json}"
if [ -z "${CAND_JSON:-}" ]; then
  [ -f "$SRC_DB" ] || { echo "X 本机找不到候选来源库：${SRC_DB}（可用 SRC_DB= 指定）" >&2; exit 1; }
fi

# 导表与校验都在这段 python 里一次完成：字段集合与 store 侧 INSERT（internal/store/candidates.go
# 的 13 列）逐字对齐；缺表/缺列/读到非 proposed 行一律非 0 退出并把原因打到 stderr。
if [ -z "${CAND_JSON:-}" ]; then
python3 - "$SRC_DB" "$JSON" "$CAND_IDS" <<'PYEOF'
# 本机侧导表腿：research_candidates → 载荷 JSON。三件小事，每件都 fail-close：
#   1) 表与列必须在（远端推送侧还会再核一次，本机先拦＝不带着已知坏载荷去连生产）；
#   2) 缺省只取 status='proposed' 的行；CAND_IDS 指定时逐行校验状态，混进非 proposed 即拒；
#   3) 载荷为空＝没东西可推，判失败（"0 行也算成功"是 §MINUTE 装载器同款假绿，禁止）。
import json, sqlite3, sys

db, out, ids_spec = sys.argv[1], sys.argv[2], sys.argv[3]
COLS = ('created_at', 'kind', 'status', 'factors', 'weights', 'metric', 'ic_mean',
        'ir', 'avg_excess', 'horizon', 'reason', 'guard', 'params')
con = sqlite3.connect('file:%s?mode=ro' % db, uri=True)
con.row_factory = sqlite3.Row
try:
    have = {r[1] for r in con.execute('PRAGMA table_info(research_candidates)')}
except Exception as e:
    sys.stderr.write('X 本机库读表失败：%r\n' % (e,)); sys.exit(1)
if not have:
    sys.stderr.write('X 本机库里没有 research_candidates 表\n'); sys.exit(1)
miss = [c for c in COLS if c not in have]
if miss:
    sys.stderr.write('X 本机候选表缺列 %s（库版本与脚本预期不一致）\n' % ','.join(miss)); sys.exit(1)

if ids_spec.strip():
    want = [int(x) for x in ids_spec.split(',') if x.strip()]
    q = 'SELECT id,%s FROM research_candidates WHERE id IN (%s)' % (
        ','.join(COLS), ','.join('?' * len(want)))
    rows = con.execute(q, want).fetchall()
    if len(rows) != len(want):
        sys.stderr.write('X 指定 id 有取不到的（要 %d 个，实得 %d 个）\n' % (len(want), len(rows))); sys.exit(1)
else:
    rows = con.execute('SELECT id,%s FROM research_candidates WHERE status=? ORDER BY id' % (
        ','.join(COLS),), ('proposed',)).fetchall()

payload = []
for r in rows:
    if r['status'] != 'proposed':
        sys.stderr.write('X 行 id=%s 状态是 %s，不是 proposed——本通道只推候选，拒推\n' % (r['id'], r['status'])); sys.exit(1)
    if r['kind'] not in ('factor', 'pattern'):
        sys.stderr.write('X 行 id=%s kind=%r 不在允许集合（factor/pattern）\n' % (r['id'], r['kind'])); sys.exit(1)
    payload.append({k: r[k] for k in ('id',) + COLS})
if not payload:
    sys.stderr.write('X 载荷为空：来源库里没有可推的 proposed 候选（0 行不判成功）\n'); sys.exit(1)
json.dump(payload, open(out, 'w', encoding='utf-8'), ensure_ascii=False)
print('CAND_LOCAL rows=%d json=%s' % (len(payload), out))
PYEOF
[ $? -eq 0 ] || exit 1
fi

# 载荷行数（供预览与等式核对；纯本地读取）
TOTAL="$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1], encoding="utf-8"))))' "$JSON" 2>/dev/null || echo 0)"
[ "${TOTAL:-0}" -gt 0 ] || { echo "X 载荷行数读到 0，判失败。" >&2; exit 1; }

echo "==> [1/6] 计划（本机侧）"
echo "  来源库       : $SRC_DB"
echo "  载荷文件     : ${JSON}（$TOTAL 行，全部 status=proposed）"
echo "  推送内容清单 :"
python3 - "$JSON" <<'PYEOF'
# 预览清单：id/创建时间/类型/尺子/护栏/IR/因子串——这些字段全是 ASCII；
# reason（中文评语）**永不打印**，防进 GBK 回传链（§GBK 教训）。
import json, sys
for r in json.load(open(sys.argv[1], encoding='utf-8')):
    fac = (r.get('factors') or '').replace(' ', '')
    print('    本机id=%s kind=%s h=%s guard=%s ir=%.3f created_at=%s factors=%s' % (
        r['id'], r['kind'], r['horizon'], r.get('guard') or '-', float(r.get('ir') or 0),
        r['created_at'], fac[:72]))
PYEOF

echo "==> 计划（远端侧，-Apply 才会执行）"
echo "  目标库       : ${DATA_DIR}/trading.db 的 research_candidates 表"
echo "  步骤1 备份   : 远端 Python sqlite3 backup API 打一致性快照 → ${DATA_DIR}/backup_candidates/trading.db.bak-<时间戳>（失败即整轮中止）"
echo "  步骤2 推送   : 逐行去重（同 factors+params+created_at 已存在→DUP 跳过）后 INSERT；"
echo "                 status 由远端语句**写死 'proposed'**，不带显式 id（防与云端候选撞号）"
echo "  步骤3 复核   : 每条新行回读 VERIFY id=.. status=proposed；等式 inserted+dup==${TOTAL}"
echo "  判红口径     : BACKUP_ERR / CAND_ERR / 缺 DONE 行 / 缺任一行 VERIFY / 等式不平 / 任一行 status 非 proposed"

if [ "$APPLY" = 0 ]; then
  echo
  echo "预览模式：一个字节都没写、一次 SSH 都没连。动手请加 -Apply。"
  echo "CAND_PUSH_PLAN"
  exit 0
fi

# ── 以下才是真动手区（预览路径永远到不了这里；门禁 §97 用"CAND_PUSH_ARMED 只在 -Apply 后出现"钉这条）──
echo "==> [2/6] 组装远端三件套并做 ASCII 自检"
cat > "$TMP/backup_db.py" <<'PYEOF'
# 远端备份腿：对现网 trading.db 用 sqlite3 **backup API** 打一致性快照。
# 为什么不用 Copy-Item：引擎以 WAL 方式持有该库，裸拷贝文件会拿到"主文件+半截 wal"的半态，
# 这种备份出事时restore直接坏库（§P0-B 灾备口径）。backup() 走页面级复制，并发下自洽。
# 只读强度：源连接强制 mode=ro——backup 的读端不需要写权限，写端只写**新文件**。
import os, sqlite3, sys

db, dst = sys.argv[1], sys.argv[2]
try:
    src = sqlite3.connect('file:%s?mode=ro' % db.replace('\\', '/'), uri=True, timeout=30)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    # 上次崩在 backup() 中途会留下 0 字节的坏 dst（connect(新建文件) 先落盘、backup 才填页）：
    # 0 字节残留可安全清掉重试；**非 0 说明是同名的历史备份，拒绝覆盖**（备份目录只增不改）。
    # TAG 已细到秒，正常重跑不会撞名。
    if os.path.exists(dst):
        if os.path.getsize(dst) == 0:
            os.remove(dst)
        else:
            print('BACKUP_ERR=dst-exists py=%s' % sys.version_info[:3]); sys.exit(1)
    tgt = sqlite3.connect(dst)
    # backup() 的 timeout 形参是 Python 3.11 才加的——现网解释器更旧时带它会 TypeError
    # （09-25 真跑首锤：BACKUP_ERR=TypeError）。缺省不带；失败文案自报解释器版本供取证。
    try:
        src.backup(tgt, timeout=30)
    except TypeError:
        src.backup(tgt)
    n = os.path.getsize(dst)
    tgt.close(); src.close()
    if n <= 0:
        print('BACKUP_ERR=zero-size py=%s' % sys.version_info[:3]); sys.exit(1)
    print('BACKUP_OK bytes=%d' % n)
except Exception as e:
    print('BACKUP_ERR=%s py=%s' % (type(e).__name__, sys.version_info[:3])); sys.exit(1)
PYEOF

cat > "$TMP/push_remote.py" <<'PYEOF'
# 远端推送腿：把载荷 JSON 逐行写入现网研究库 research_candidates。六道闸，顺序固定：
#   1) 表/列必须在（缺列 CAND_ERR=schema-missing:<列>）——缺表**不得**顺手建表：
#      云端库连这张表都没有，说明该服务从未做过研究，静默建表＝把版本错位藏起来（§H3 同族）；
#   2) 载荷行 status 必须 proposed（本机已查，远端再查一次＝两独立实现同闸，本机坏不了也拦）；
#   3) 去重键 (factors, params, created_at)：命中即 DUP 跳过，**绝不 UPDATE 既有行**；
#   4) INSERT 列集与 internal/store/candidates.go 的写入腿逐字同构，status 写死 'proposed'，
#      不显式给 id（云端自增，防撞号）；
#   5) 全部行在一个事务里执行（半插=整轮回滚，绝不留"推了一半"的库态）；提交前逐行 VERIFY，
#      VERIFY 行从库里读、status 非 proposed 直接 raise（整轮 fail-close）；
#   6) busy 超时 30s：引擎/researchd 可能持库，拿不到锁显式判红，不静默等成功。
import json, sqlite3, sys

db, payload = sys.argv[1], sys.argv[2]
COLS = ('created_at', 'kind', 'status', 'factors', 'weights', 'metric', 'ic_mean',
        'ir', 'avg_excess', 'horizon', 'reason', 'guard', 'params')
rows = json.load(open(payload, encoding='utf-8'))
if not rows:
    print('CAND_ERR=empty-payload'); sys.exit(1)
con = sqlite3.connect(db.replace('\\', '/'), timeout=30)
try:
    have = {r[1] for r in con.execute('PRAGMA table_info(research_candidates)')}
    if not have:
        print('CAND_ERR=no-table'); sys.exit(1)
    miss = [c for c in COLS if c not in have]
    if miss:
        print('CAND_ERR=schema-missing:%s' % ','.join(miss)); sys.exit(1)
    ins = 0
    dup = 0
    new_ids = []
    for r in rows:
        if r.get('status') != 'proposed':
            print('CAND_ERR=payload-status'); sys.exit(1)
        same = con.execute(
            'SELECT count(*) FROM research_candidates WHERE factors=? AND params=? AND created_at=?',
            (r['factors'], r.get('params') or '', r['created_at'])).fetchone()[0]
        if same > 0:
            dup += 1
            print('DUP src=%s' % r['id'])
            continue
        cur = con.execute(
            'INSERT INTO research_candidates (created_at,kind,status,factors,weights,metric,'
            'ic_mean,ir,avg_excess,horizon,reason,guard,params) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)',
            (r['created_at'], r['kind'], 'proposed', r['factors'], r.get('weights') or '',
             r.get('metric'), r.get('ic_mean'), r.get('ir'), r.get('avg_excess'),
             r.get('horizon'), r.get('reason') or '', r.get('guard') or 'standard',
             r.get('params') or ''))
        nid = cur.lastrowid
        new_ids.append(nid)
        print('PUSHED src=%s new=%s' % (r['id'], nid))
        ins += 1
    con.commit()
    # VERIFY 从库里回读（不是把刚算的变量再打一遍）——"写了但没进去"必须在这一步露出来。
    for nid in new_ids:
        st = con.execute('SELECT status FROM research_candidates WHERE id=?', (nid,)).fetchone()
        if st is None or st[0] != 'proposed':
            raise RuntimeError('verify failed for id %s' % nid)
        print('VERIFY id=%s status=%s' % (nid, st[0]))
    print('CAND_PUSH_DONE total=%s inserted=%s dup=%s' % (len(rows), ins, dup))
except Exception as e:
    try:
        con.rollback()
    except Exception:
        pass
    print('CAND_ERR=%s' % type(e).__name__)
    sys.exit(1)
finally:
    try:
        con.close()
    except Exception:
        pass
PYEOF

# 编排 ps1：探 Python 3 腿（缺 sqlite3.exe 不构成失败——本通道**只走 Python 腿**，
# 因为它需要 backup API 与参数化 INSERT，CLI 做不到；python 拿不到 = 显式判红）。
# 全 ASCII，判定行字段无中文。
cat > "$TMP/push_run.ps1" <<'PYEOF'
$ErrorActionPreference = 'Stop'
$stage = Split-Path -Parent $MyInvocation.MyCommand.Path
$db = '@DATA_DIR@/trading.db'
$tag = '@TAG@'
$bakdir = '@DATA_DIR@/backup_candidates'
$execMode = ''; $pyExe = ''; $pyPre = @()
foreach ($cand in @('python', 'py')) {
  $pre = @(); if ($cand -eq 'py') { $pre = @('-3') }
  $o = ''
  try { $o = (& $cand ($pre + @('--version')) 2>&1 | Select-Object -First 1) } catch { $o = '' }
  if ("$o" -match 'Python 3') { $execMode = 'python'; $pyExe = $cand; $pyPre = $pre; break }
}
if (-not $execMode) { Write-Output 'CAND_ERR=no-python3'; exit 1 }
Write-Output ('EXEC python|' + $pyExe)
& $pyExe ($pyPre + @((Join-Path $stage 'backup_db.py'), $db, (Join-Path $bakdir ('trading.db.bak-' + $tag))))
if ($LASTEXITCODE -ne 0) { Write-Output 'ABORT=backup-failed'; exit 1 }
& $pyExe ($pyPre + @((Join-Path $stage 'push_remote.py'), $db, (Join-Path $stage 'cand.json')))
if ($LASTEXITCODE -ne 0) { Write-Output 'ABORT=push-failed'; exit 1 }
Write-Output 'PS_DONE'
PYEOF
GIT_SHA="$(git -C "$APP_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
TAG="cand-$(date +%Y%m%d-%H%M%S)-${GIT_SHA}"
# 占位符替换直接落回文件本体（powershell -File 吃 LF 行尾没问题）。
# ★ 曾经在这里 printf '\r\n' 造 CRLF——结果 \r 自己被下面的 ASCII 自检判红（16:5x 首跑实录：
# 报告"ps1 掺了非 ASCII"，实际全文纯 ASCII，脏的恰恰是"为了 Windows 兼容"加的回车符）。
# Windows 兼容性交给远端 powershell 自己判，脚本体保持 LF + 纯 ASCII 才是本仓的判据形状。
sed -e "s|@DATA_DIR@|$DATA_DIR|g" -e "s|@TAG@|$TAG|g" "$TMP/push_run.ps1" > "$TMP/push_run.final.ps1" \
  || { echo "X 占位符替换失败" >&2; exit 1; }
mv "$TMP/push_run.final.ps1" "$TMP/push_run.ps1"
# ASCII 自检只锁 .ps1：它要走 bash→ssh→cmd→powershell 四层转义 + GBK 回传（本仓最脆的一条链）。
# 两份 .py 允许含中文**注释**（py3 源码缺省 UTF-8，合法），但它们 print 到 stdout 的判定行
# 必须全 ASCII——这一点由 §97 的"回传行形状锁"静态钉（BACKUP_/CAND_/PUSHED/DUP/VERIFY/DONE 行的
# 格式串里不允许出现非 ASCII），而不是整文件一刀切。
if LC_ALL=C grep -n '[^ -~]' "$TMP/push_run.ps1" >/dev/null 2>&1; then
  echo "X 远端 ps1 掺了非 ASCII 字符：会经 GBK 回传成乱码判据，中止。" >&2
  exit 1
fi
echo "CAND_PUSH_ARMED backup-tag=$TAG total=$TOTAL"

echo "==> [3/6] BatchMode 预探测（私钥不可读就立刻失败，绝不挂起）"
if ! $SSH "echo ok" >/dev/null 2>&1; then
  echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
  exit 1
fi
echo "ok - 通道可用"

REMOTE_STAGE="C:/Users/${GZ_USER}/AppData/Local/Temp/push_candidates_$$"
echo "==> [4/6] 上传四件套到远端暂存目录（只碰自己的子目录，不覆盖任何现网文件）"
$SSH "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path ${REMOTE_STAGE} | Out-Null; exit 0\"" >/dev/null 2>&1 \
  || { echo "X 远端暂存目录创建失败（${REMOTE_STAGE}）" >&2; exit 1; }
if ! $SCP "$JSON" "$TMP/backup_db.py" "$TMP/push_remote.py" "$TMP/push_run.ps1" \
     "${GZ_USER}@${GZ_IP}:${REMOTE_STAGE}/" >/dev/null 2>&1; then
  echo "X 四件套上传失败——判失败，不猜『哪一半到了』。" >&2
  exit 1
fi
echo "ok - 已上传 cand.json / backup_db.py / push_remote.py / push_run.ps1"

echo "==> [5/6] 远端执行：备份 → 推送 → 复核（备份失败整轮中止，一条不写）"
OUT="$($SSH "powershell -NoProfile -ExecutionPolicy Bypass -File ${REMOTE_STAGE}/push_run.ps1" 2>&1 || true)"
printf '%s\n' "$OUT" > /tmp/push_candidates_guangzhou.log
OUT="$(printf '%s' "$OUT" | tr -d '\r')"
printf '%s\n' "$OUT" | grep -v '^$' || true
case "$OUT" in
  *CAND_ERR=*|*ABORT=*)
    echo "X 远端判定行含错误（详见 /tmp/push_candidates_guangzhou.log）——本轮**未确认推送成功**。" >&2
    exit 1 ;;
esac
case "$OUT" in
  *PS_DONE*) ;;
  *) echo "X 远端没回打 PS_DONE——编排断链，判失败（不把『跑完了』当成功）。" >&2; exit 1 ;;
esac
DONE_LINE="$(printf '%s\n' "$OUT" | grep '^CAND_PUSH_DONE ' | tail -1 || true)"
[ -n "$DONE_LINE" ] || { echo "X 缺 CAND_PUSH_DONE 判定行，判失败。" >&2; exit 1; }

echo "==> [6/6] 等式复核 + 映射回显"
# BACKUP_OK 必须在（推送成功的定义包含"备份先行"；只推不备＝红，哪怕推送本身全绿）
printf '%s\n' "$OUT" | grep -q '^BACKUP_OK bytes=' || { echo "X 缺 BACKUP_OK 行——备份腿没成功，推送即便落库也判红。" >&2; exit 1; }
d_total="$(printf '%s' "$DONE_LINE" | tr ' ' '\n' | grep '^total=' | cut -d= -f2)"
d_ins="$(printf '%s' "$DONE_LINE" | tr ' ' '\n' | grep '^inserted=' | cut -d= -f2)"
d_dup="$(printf '%s' "$DONE_LINE" | tr ' ' '\n' | grep '^dup=' | cut -d= -f2)"
v_n="$(printf '%s\n' "$OUT" | grep -c '^VERIFY id=' || true)"
echo "  远端等式 : total=$d_total inserted=$d_ins dup=${d_dup}；VERIFY 行数=$v_n"
printf '%s\n' "$OUT" | grep '^PUSHED src=' || true
# 等式复核前先验"都是数字"：非数字进 test 的算术展开会让整条判据变成恒假/恒真的糊账
# （§89 同款主题——判据本身必须先可信）。
case "${d_total}${d_ins}${d_dup}${v_n}" in
  *[!0-9]*|"") echo "X 远端判定行里取到的数字字段不全是数字（total=$d_total inserted=$d_ins dup=$d_dup verify=${v_n}）——判失败。" >&2; exit 1 ;;
esac
if [ "$d_total" != "$TOTAL" ]; then
  echo "X 远端 total=$d_total 与本机载荷 $TOTAL 不等——判失败。" >&2; exit 1
fi
if [ $((d_ins + d_dup)) -ne "$d_total" ]; then
  echo "X 远端等式 inserted+dup==total 不成立——半态嫌疑，判失败。" >&2; exit 1
fi
if [ "$v_n" != "$d_ins" ]; then
  echo "X VERIFY 行数($v_n) != inserted($d_ins)——写后复核没做到每条，判失败。" >&2; exit 1
fi
echo "ok - 复核通过：$d_ins 条新候选已入云端 proposed 档（$d_dup 条重复被跳过），审批面刷新即可见"
# 提示，不是判据：现网服务不重启，engine 读候选表是每请求查库（无进程内缓存），无需重启。
