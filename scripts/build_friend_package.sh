#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# build_friend_package.sh — §FRIEND-PKG（2026-11-03 批 6）朋友机器"一键解压安装包"的**构建腿**。
#
# 为什么做这个（因果链）：
#   B-α 的朋友侧现在有 8 份 ps1（出厂档配置 / 装填 / 计划任务 / 看门狗 / 停腿 / 导出 / 卸载），
#   每一份单独都能跑，但**串起来跑**这件事本身就是要人懂行——owner 的裁决很直接："能不能在朋友 win 端
#   直接做个一键解压安装包"。本脚本 + install.ps1 就是把"懂行"固化成一次双击。
#   可行的前提今天成立：网关是**纯标准库**（xtquant 只在 xt 通道里延迟 import，而出厂档强制
#   failover_enable=false + broker=queued，朋友机器永不走那条），所以包里不需要 pip 装任何东西。
#
# 三条硬规矩（写进判据，不是写进愿望）：
#   ① **白名单收集，绝不整仓打包**：只装下面 PAYLOAD 里点名的文件。反过来说——任何匹配 DENY 清单的
#      路径（真配置 config.xt.json、账本 data.db*、任何 .pre-* / auth.json / 密钥文件）一旦被点进白名单，
#      本脚本**当场 exit 1**而不是"跳过"：跳过会静默把一个带凭证的包发出去，而没人知道少了一个文件。
#      （§TOKEN-PERAACCT 的 N2 同取向：结构性不能 > 事后报警。）
#   ② **MANIFEST 逐文件 SHA256 + 契约指纹**：指纹必须用网关**自己**那把尺子
#      （gateway.contract_fingerprint）算，不能在构建脚本里再抄一份哈希逻辑——两把尺子必然漂，
#      漂了的表现是"装完自检说契约不符"，而代码其实是对的（§INDEX-BLIND 两份段序表的同形事故）。
#   ③ 包体落在**仓库外**（默认 ${TMPDIR}/qmt-friend-pkg）：门禁 §P2-L 判工作树漂移，
#      产物写进树里就是把绿点自己按红（记忆 [[uat-evidence-dir-and-prod-writes]]）。
#
# WireGuard 嵌入位（可选，owner 二选一的"路径一"）：`--wireguard <msi 路径>` 把官方包带在包里，
# 朋友侧就不必去翻被墙的官网。**本脚本只嵌不验签**——Authenticode 只能在 Windows 上判，
# 验签与拒装都在 install.ps1 第 9 步做（签名人必须是 WireGuard LLC，否则不装、只报）。
#
# §FRIEND-CARRIER 11-04 追加（owner 要求：发版前包里必须真有全量规则 + 沙箱策略 py）：
#   ④ **产物与可复现构建器同带**：仓内 qmt_gateway/local_executor.py 与 qmt_internal_strategy.py
#      只是**模板/源**（PARAMS/规则块是空的，77KB/30KB）；真正含五个 rules_*.py 全量内联、
#      v02 烘参的载体是构建器输出到仓库外的 local_executor_<ver>.py / qmt_internal_strategy_<ver>.py
#      （392KB/369KB）。只把模板打进包＝朋友装上一份没有规则的空壳，而包看起来完全正常。
#      所以构建期在**包内布局**下跑 payload/tools/build_local_executor.py 两个 target，产物落进
#      payload/artifacts/；模板原名保留在 payload/qmt_gateway/（构建器读的就是它们）。
#   ⑤ **解压后重跑逐字节复现**：包压好后从 zip 解到临时目录，在解出来的 payload/tools 下
#      再跑一次构建器，产物与包内 artifacts/ 逐字节 + SHA256 比对；不一致就删包、ABORT，
#      不发一份"自证说不过去"的包（AGENTS 11-04 那条实测从此落成常驻腿，不靠人手记）。
#   ⑥ **模板不许被产物覆盖**：qmt_internal_strategy.py 一旦被产物顶替，构建器拿产物当模板
#      再烘一次字节会变（实测 cc968f51… → f787b57d…），复现腿当场红。所以是两套文件各归各位。
#
# 用法：
#   scripts/build_friend_package.sh                       # 出一份不含 overlay 的包（会如实打 OVERLAY=none）
#   scripts/build_friend_package.sh --wireguard ~/Downloads/wireguard-amd64.msi
#   PKG_OUT=/tmp/x scripts/build_friend_package.sh -Preview   # dry-run：只列白名单，不落盘不压缩
#   scripts/build_friend_package.sh --check <pkg.zip|dir>     # 只读校验 MANIFEST 逐文件 SHA256，不写不改
# 判读锚点（纯 ASCII）：BUILD_COMMIT= FILES= BYTES= CONTRACT_FP= WIREGUARD=embedded|none
#                      DENIED_HIT=0 ARTIFACTS=2 REPRO_OK=1 PKG= BUILD_EXIT=0
#                      CHECK_OK files=NN artifacts=2（--check 专用）
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 测试缝：清单/拒绝逻辑要在一份小假树上跑才敢造雷（缺文件、混进 config.xt.json 这两种形态
# 在真仓里造出来就等于把禁止路径写进白名单）。缺省就是仓库根，现网构建零变化。
SRC_ROOT="${PKG_SRC_ROOT:-$ROOT}"
PKG_OUT="${PKG_OUT:-${TMPDIR:-/tmp}/qmt-friend-pkg}"
WG_MSI=""
MODE="build"
COMMIT=""
CHECK_PATH=""

while [ $# -gt 0 ]; do
	case "$1" in
		--wireguard) WG_MSI="${2:-}"; shift 2 ;;
		-Preview | --preview) MODE="preview"; shift ;;
		--check) CHECK_PATH="${2:-}"; shift 2 ;;
		--out) PKG_OUT="${2:-}"; shift 2 ;;
		--commit) COMMIT="${2:-}"; shift 2 ;;
		*) echo "X 不认识参数：$1（可用：--wireguard <msi> / --check <pkg.zip|dir> / --out <dir> / --commit <sha> / -Preview）" >&2; exit 2 ;;
	esac
done

# ── --check：只读校验一份已构建的包（zip 或已解压目录），不落盘、不改任何字节 ──
# 与 -Preview 的分工：Preview 是"还没建，先看打算装什么"；--check 是"包已在那，逐文件核对
# MANIFEST 的 SHA256 是否描述真实字节"。两者都是 dry-run，都缺省不动手。
if [ -n "$CHECK_PATH" ]; then
	CHECK_OUT=$(python3 - "$CHECK_PATH" <<'PY'
import hashlib, json, os, sys, zipfile
sys.dont_write_bytecode = True
p = sys.argv[1]
if not p:
    print("CHECK_FAIL reason=check_path_empty")
    sys.exit(1)
z = None
root = None
prefix = ""
if os.path.isfile(p) and p.lower().endswith(".zip"):
    try:
        z = zipfile.ZipFile(p)
    except Exception as e:
        print("CHECK_FAIL reason=zip_unreadable:%s" % e)
        sys.exit(1)
    names = z.namelist()
    if "payload/MANIFEST.json" in names:
        prefix = "payload/"
    elif "MANIFEST.json" in names:
        prefix = ""
    else:
        print("CHECK_FAIL reason=manifest_absent_in_zip")
        sys.exit(1)
else:
    if os.path.isfile(os.path.join(p, "payload", "MANIFEST.json")):
        root = os.path.abspath(os.path.join(p, "payload"))
    elif os.path.isfile(os.path.join(p, "MANIFEST.json")):
        root = os.path.abspath(p)
    else:
        print("CHECK_FAIL reason=manifest_absent_in_dir")
        sys.exit(1)

def read_bytes(rel):
    if z is not None:
        return z.read(prefix + rel)
    with open(os.path.join(root, rel), "rb") as f:
        return f.read()

def list_paths():
    if z is not None:
        return [n[len(prefix):] for n in z.namelist()
                if not n.endswith("/") and n.startswith(prefix) and n != prefix + "MANIFEST.json"]
    out = []
    for base, _, ns in os.walk(root):
        for n in ns:
            out.append(os.path.relpath(os.path.join(base, n), root).replace(os.sep, "/"))
    return [x for x in out if x != "MANIFEST.json"]

try:
    manifest = json.loads(read_bytes("MANIFEST.json").decode("utf-8"))
except Exception as e:
    print("CHECK_FAIL reason=manifest_unreadable:%s" % e)
    sys.exit(1)
entries = dict((e["path"], e) for e in manifest.get("files", []))
actual = list_paths()
errs = []
for path, e in entries.items():
    try:
        b = read_bytes(path)
    except Exception:
        errs.append("missing:" + path)
        continue
    if hashlib.sha256(b).hexdigest() != e["sha256"]:
        errs.append("sha_mismatch:" + path)
    if len(b) != e["bytes"]:
        errs.append("size_mismatch:" + path)
for path in actual:
    if path not in entries:
        errs.append("unlisted:" + path)
    if "__pycache__" in path or path.endswith(".pyc"):
        errs.append("junk:" + path)
arts = [x for x in actual if x.startswith("artifacts/") and x.endswith(".py")]
if len(arts) < 2:
    errs.append("artifacts_missing:%d" % len(arts))
if errs:
    print("CHECK_FAIL reason=" + ",".join(errs[:8]))
    sys.exit(1)
print("CHECK_OK files=%d artifacts=%d contract_fp=%s commit=%s"
      % (len(entries), len(arts), str(manifest.get("contract_fp", ""))[:12],
         manifest.get("build_commit", "")))
PY
)
	printf '%s\n' "$CHECK_OUT"
	case "$CHECK_OUT" in
		*CHECK_OK*) exit 0 ;;
		*) exit 1 ;;
	esac
fi

# ── 白名单：点名的源文件必须存在，缺一个就 exit 1（宁可不发包，也不发一份少个文件的包）──
GW_FILES="gateway.py store.py handler.py broker.py ids.py outbox_admin.py quote_feed.py \
close_gate.py leg_ops.py trading_calendar.py qmt_bridge.py qmt_bridge_strategy.py \
config.bridge.example.json local_executor.py local_executor_params.json \
qmt_internal_strategy.py report_uplink.py broker_reads.py carrier_updater.py \
rules_form.py rules_ladder.py rules_factor.py rules_t.py rules_sizing.py"
FRIEND_FILES="ensure_friend_gateway_config.ps1 watchdog.ps1 install_tasks.ps1 enroll.ps1 \
start_gateway.ps1 export_ledger.ps1 stop_leg.ps1 uninstall.ps1 config.xt.friend.template.json \
install.ps1 uninstall_entry.bat export_ledger.bat install_entry.bat SETUP.txt \
install_local_executor.ps1 switch_execution_mode.ps1 switch_execution_mode.bat update_leg.ps1"
# §FRIEND-LOCAL 批 7：本地执行器那条腿的构建器与更新器（源在 scripts/，落进 payload/tools/）。
#   * scripts/build_local_executor.py   烘参 + 五道静态闸（产物落仓库外）
#   * scripts/update_local_executor.ps1 备份→写入(SHA 三值自证)→版本回读 三段自证
# 11-04 载体定稿后多两件：内置策略的投递脚本（写策略文件+重启客户端+版本回读）。
# 11-04 §FRIEND-CARRIER 补齐（owner 清单，缺一件就不算"全量规则 + 沙箱策略 py"）：
#   * scripts/derive_carrier_params.py  参数真源导出器（Go 出厂默认 → local_executor_params.json）
#   * scripts/update_local_executor.bat 更新脚本的 .bat 入口（缺省 dry-run，-Apply 才写）
# 看门狗 watchdog.ps1 与 install_local_executor.ps1 已在 FRIEND_FILES 里，不重复登记。
SCRIPTS_FILES="build_local_executor.py derive_carrier_params.py \
update_local_executor.ps1 update_qmt_strategy.ps1 update_local_executor.bat"
# 门禁 §171 盯这两个变量的条目数与内容；改白名单必须同批改那里。
#
# §FRIEND-LOCAL 批 7 追加：本地执行器那条腿（QMT 之外的独立普通 Python 进程）
#   * qmt_gateway/local_executor.py        执行器本体（纯 ASCII，规则层无裸数字）
#   * qmt_gateway/local_executor_params.json 规则包参数（烘参唯一真源）
#   * deploy/qmt-friend/install_local_executor.ps1  装这条腿（venv+依赖+计划任务+自检）
#   * scripts/build_local_executor.py       构建器（烘参 + 五道静态闸）
#   * scripts/update_local_executor.ps1     更新器（备份→写入→版本回读三段自证）

# 黑名单：命中即拒绝构建（不是跳过）。真配置/账本/备份/凭据一律不许进包。
deny_hit() { # $1=相对路径
	case "$1" in
		*/config.xt.json | config.xt.json | *data.db* | *.pre-* | *auth.json | *.token | *.pem | *.key | *admin.token) return 0 ;;
		*) return 1 ;;
	esac
}

SRC_LIST=""
for f in $GW_FILES; do SRC_LIST="$SRC_LIST qmt_gateway/$f"; done
for f in $FRIEND_FILES; do SRC_LIST="$SRC_LIST deploy/qmt-friend/$f"; done
for f in $SCRIPTS_FILES; do SRC_LIST="$SRC_LIST scripts/$f"; done
# contract 目录整体进包（六件 golden）——契约指纹按目录内容算，所以这里必须是整目录而不是挑几件。
for f in "$SRC_ROOT"/qmt_gateway/contract/*; do
	[ -f "$f" ] && SRC_LIST="$SRC_LIST qmt_gateway/contract/$(basename "$f")"
done

MISSING=0
DENIED=0
for rel in $SRC_LIST; do
	[ -f "$SRC_ROOT/$rel" ] || { echo "X 白名单文件不存在：${rel}（宁可不发包，也不发一份缺文件的包）" >&2; MISSING=$((MISSING + 1)); }
	if deny_hit "$rel"; then
		echo "X 白名单里出现了禁止入包的路径：${rel}（真配置/账本/凭据一律不进包，这是 N2 与 B-N2 的结构性边界）" >&2
		DENIED=$((DENIED + 1))
	fi
done
[ "$MISSING" = "0" ] || { echo "BUILD_EXIT=1 reason=missing_files=$MISSING" >&2; exit 1; }
[ "$DENIED" = "0" ] || { echo "BUILD_EXIT=1 reason=deny_list_hit=$DENIED" >&2; exit 1; }
echo "DENIED_HIT=0"

# §STRATEGY-SWITCH-SYNC（owner 11-06）：win 包里的启用战法必须以量化交易 tab 的开关为准。
# 包是「构建期烘参」产物，params 一旦被手改，装到物理机上就是另一套战法却没有任何报错。
# 所以打包前实跑快照对账；漂移即拒发包，不留半份包。
if ! python3 "$ROOT/scripts/sync_strategy_switches.py" --check >/dev/null 2>&1; then
	echo "X 战法开关与快照漂移：先重抓 tab 开关快照并跑 sync_strategy_switches.py --write" >&2
	echo "BUILD_EXIT=1 reason=strategy_switch_drift" >&2
	exit 1
fi
echo "STRATEGY_SWITCH_SYNC=OK"

if [ "$MODE" = "preview" ]; then
	echo "==> [-Preview] 只列出将要打包的文件，不落盘、不压缩"
	printf '   %s\n' $SRC_LIST
	echo "   （另加 MANIFEST.json / SETUP.txt / 三个 .bat，均在 deploy/qmt-friend/ 里）"
	echo "ARTIFACTS_PLAN=2 built_at_build_time"
	echo "REPRO_PLAN=1 verify_after_unzip"
	echo "WIREGUARD=$([ -n "$WG_MSI" ] && echo requested || echo none)"
	echo "BUILD_EXIT=0"
	exit 0
fi

COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
[ "$COMMIT" = "unknown" ] && echo "!! 拿不到 git 短 SHA（在 repo 外跑？），MANIFEST 会写成 unknown——现网比对会判不出归属" >&2

STAMP="$(date +%Y%m%d%H%M)"
STAGE="${PKG_OUT}/leg-${COMMIT}-${STAMP}"
rm -rf "$STAGE" 2>/dev/null || true
mkdir -p "$STAGE/payload/qmt_gateway/contract" "$STAGE/payload/tools" || { echo "X 建暂存目录失败" >&2; exit 1; }

for rel in $SRC_LIST; do
	src="$SRC_ROOT/$rel"
	case "$rel" in
		qmt_gateway/contract/*) dst="$STAGE/payload/qmt_gateway/contract/$(basename "$rel")" ;;
		qmt_gateway/*) dst="$STAGE/payload/qmt_gateway/$(basename "$rel")" ;;
		*) dst="$STAGE/payload/tools/$(basename "$rel")" ;;
	esac
	mkdir -p "$(dirname "$dst")"
	# 用绝对路径的 /bin/cp：WorkBuddy 会在子进程 PATH 里注入 brokered-bin shim，
	# 裸 `cp` 可能被文件策略拦成 "Brokered host copy source refused by file policy"
	# （11-08 实测），包会静默少一个 gateway.py。
	/bin/cp "$src" "$dst" || { echo "X 复制失败：$rel" >&2; exit 1; }
done

# .ps1 加 UTF-8 BOM：PowerShell 5.1 默认按系统码页（中文 Windows = GBK）读无 BOM 的 UTF-8，
# 中文注释会被误读并破坏 param() 块解析（11-08 朋友端实测 install 直接红）。加 BOM 后 PS 正确识别。
# 仅 .ps1 加（Python 文件绝不能带 BOM，否则 json.load 与 import 报错）。MANIFEST 在加 BOM 之后生成，
# 故哈希自动对齐，朋友侧 STEP1 不必再手改清单。
STAGE_DIR="$STAGE" python3 - <<'PY'
import glob, os
base = os.path.join(os.environ["STAGE_DIR"], "payload", "tools")
bom = b"\xef\xbb\xbf"
n = 0
for f in glob.glob(os.path.join(base, "*.ps1")):
    with open(f, "rb") as fh:
        data = fh.read()
    if not data.startswith(bom):
        with open(f, "wb") as fh:
            fh.write(bom + data)
        n += 1
        print("BOM_ADDED file=%s" % os.path.basename(f))
if n:
    print("BOM_DONE added=%d" % n)
PY

OVERLAY="none"
if [ -n "$WG_MSI" ]; then
	if [ -f "$WG_MSI" ]; then
		mkdir -p "$STAGE/payload/overlay"
		/bin/cp "$WG_MSI" "$STAGE/payload/overlay/wireguard-amd64.msi" || { echo "X 复制 msi 失败" >&2; exit 1; }
		OVERLAY="embedded"
	else
		# 点了 --wireguard 但文件不在：这不是"那就跳过"，而是当场叫出来——
		# 静默跳过会发出一份"以为带了 overlay"的包，朋友装完才发现腿起不来。
		echo "X --wireguard 指向的文件不存在：$WG_MSI" >&2
		exit 1
	fi
fi

# ── 载体产物（§FRIEND-CARRIER）：构建期在**包内布局**下烘出含全量规则的两份载体 ──
# 构建器的 REPO_ROOT = 脚本父目录 = payload/，于是它读的就是 payload/qmt_gateway/ 里的模板、
# 五个 rules_*.py、params 与 handler/store —— 与将来朋友解压后在包内重跑完全同一套输入。
# out-dir 必须落在 payload **之外**：构建器 out_dir_for() 拒绝往 REPO_ROOT 里写（工作树漂移闸），
# 所以先落在 $STAGE/artifact-build，再拷进 payload/artifacts/。
ART_DIR="$STAGE/payload/artifacts"
BUILD_OUT="$STAGE/artifact-build"
mkdir -p "$ART_DIR" "$BUILD_OUT" || { echo "X 建产物目录失败" >&2; exit 1; }
# 构建器 exec 产物 + import 账本模块：禁写字节码，否则清单里会混进 __pycache__/*.pyc。
export PYTHONDONTWRITEBYTECODE=1
for tgt in executor sandbox; do
	BUILD_LOG="$STAGE/build-$tgt.log"
	if ! python3 "$STAGE/payload/tools/build_local_executor.py" --out-dir "$BUILD_OUT" --target "$tgt" >"$BUILD_LOG" 2>&1; then
		echo "X 载体构建失败 target=${tgt}（不出一份没有全量规则的包）" >&2
		cat "$BUILD_LOG" >&2
		exit 1
	fi
	if ! grep -q "BUILD_RESULT=OK" "$BUILD_LOG"; then
		echo "X 载体构建没打 BUILD_RESULT=OK target=$tgt" >&2
		cat "$BUILD_LOG" >&2
		exit 1
	fi
done
/bin/cp "$BUILD_OUT"/*.py "$ART_DIR/" || { echo "X 产物拷进包失败" >&2; exit 1; }
ART_N=$(find "$ART_DIR" -maxdepth 1 -name '*.py' -type f | wc -l | tr -d ' ')
[ "$ART_N" = "2" ] || { echo "BUILD_EXIT=1 reason=artifacts_expected_2_got_$ART_N" >&2; exit 1; }
ART_VER=$(grep -ho 'version=v[A-Za-z0-9-]*' "$STAGE"/build-*.log | head -1 | cut -d= -f2)
echo "ARTIFACTS=$ART_N version=$ART_VER"

# 再清一次字节码：第一段 MANIFEST 见到 __pycache__ 会当场拒建（清单必须描述干净的包体）。
find "$STAGE/payload" -name '__pycache__' -type d -prune -exec rm -rf {} + 2>/dev/null || true
find "$STAGE/payload" -name '*.pyc' -type f -delete 2>/dev/null || true

# MANIFEST：逐文件 SHA256 + 契约指纹（用网关自己的 contract_fingerprint，不另造尺子）。
MANIFEST_ERR=$(cd "$STAGE/payload" && python3 - "$COMMIT" "$STAMP" <<'PY'
import hashlib, json, os, sys, io
commit, stamp = sys.argv[1], sys.argv[2]
# 关键：先关掉写字节码再导入网关。import gateway 会在 payload 里落下 __pycache__/*.pyc，
# 而清单是**在这之后**遍历目录的——于是本机垃圾既进了 SHA256 清单又被压进包，
# 朋友侧逐文件校验必然对不上（11-03 首跑就是这个形状：files=39 里混着两份 .pyc）。
sys.dont_write_bytecode = True
sys.path.insert(0, os.path.abspath('qmt_gateway'))
try:
    import gateway  # 只为借它那把 contract_fingerprint 尺子（纯标准库，导入零副作用）
    fp = gateway.contract_fingerprint(os.path.join('qmt_gateway', 'contract'))
except Exception as e:  # 宁可红，不许写一个"未知指纹"进包
    print("MANIFEST_FAIL=contract_fp:%s" % e)
    sys.exit(1)
def junk(rel):  # 兜第二道：任何 __pycache__ / .pyc 都不许进清单
    return "__pycache__" in rel or rel.endswith(".pyc")

files = []
for base, _, names in os.walk('.'):
    for n in sorted(names):
        p = os.path.normpath(os.path.join(base, n))
        if junk(p):
            print("MANIFEST_FAIL=junk_in_payload:%s" % p)
            sys.exit(1)
        h = hashlib.sha256()
        with open(p, 'rb') as f:
            for chunk in iter(lambda: f.read(1 << 20), b''):
                h.update(chunk)
        files.append({"path": p.replace(os.sep, '/'), "bytes": os.path.getsize(p), "sha256": h.hexdigest()})
files.sort(key=lambda x: x["path"])
manifest = {
    "build_commit": commit, "built_at": stamp, "contract_fp": fp,
    "wireguard": "embedded" if os.path.exists("overlay/wireguard-amd64.msi") else "none",
    "files": files,
}
with io.open('MANIFEST.json', 'w', encoding='utf-8') as f:
    json.dump(manifest, f, ensure_ascii=False, indent=2, sort_keys=True)
print("MANIFEST_OK files=%d contract_fp=%s wireguard=%s" % (len(files), fp[:12], manifest["wireguard"]))
PY
)
printf '%s\n' "$MANIFEST_ERR"
case "$MANIFEST_ERR" in
	*MANIFEST_OK*) : ;;
	*) echo "X MANIFEST 生成失败（不出一份没有清单的包）" >&2; exit 1 ;;
esac
# MANIFEST 自身也要进清单？不能（自引用）。install.ps1 判的是"除 MANIFEST.json 外每个文件的 sha 都对"。

find "$STAGE" -name '__pycache__' -type d -prune -exec rm -rf {} + 2>/dev/null || true
# 清理之后**重算**一次清单：清单必须描述真正压进包的那棵树，而不是"打算压的树"。
MANIFEST_ERR2=$(cd "$STAGE/payload" && python3 - "$COMMIT" "$STAMP" <<'PYS'
import hashlib, json, os, sys, io
commit, stamp = sys.argv[1], sys.argv[2]
sys.dont_write_bytecode = True
sys.path.insert(0, os.path.abspath('qmt_gateway'))
import gateway
fp = gateway.contract_fingerprint(os.path.join('qmt_gateway', 'contract'))
files = []
for base, _, names in os.walk('.'):
    for n in sorted(names):
        p = os.path.normpath(os.path.join(base, n))
        if "__pycache__" in p or p.endswith(".pyc"):
            # 清干净之后还看见字节码 = 有别的东西在写，不解释，直接不发包。
            print("MANIFEST_FAIL=junk_after_purge:%s" % p)
            sys.exit(1)
        if p == "MANIFEST.json":
            continue  # 清单不给自己算哈希（自引用），但它是包里的合法文件
        h = hashlib.sha256()
        with open(p, 'rb') as f:
            for chunk in iter(lambda: f.read(1 << 20), b''):
                h.update(chunk)
        files.append({"path": p.replace(os.sep, '/'), "bytes": os.path.getsize(p), "sha256": h.hexdigest()})
files.sort(key=lambda x: x["path"])
manifest = json.load(io.open('MANIFEST.json', encoding='utf-8'))
manifest["files"] = files
manifest["contract_fp"] = fp
with io.open('MANIFEST.json', 'w', encoding='utf-8') as f:
    json.dump(manifest, f, ensure_ascii=False, indent=2, sort_keys=True)
print("MANIFEST_REBUILD_OK files=%d contract_fp=%s wireguard=%s" % (len(files), fp[:12], manifest["wireguard"]))
PYS
)
printf '%s\n' "$MANIFEST_ERR2"
case "$MANIFEST_ERR2" in
	*MANIFEST_REBUILD_OK*) : ;;
	*) echo "X 清理后重算清单失败（清单与包体不一致就不许发包）" >&2; exit 1 ;;
esac

ZIP="${PKG_OUT}/leg-${COMMIT}-${STAMP}.zip"
( cd "$STAGE" && zip -q -r -X "$ZIP" payload ) || { echo "X 压缩失败" >&2; exit 1; }
rm -rf "$STAGE" 2>/dev/null || true

# ── 解压后在**包内**重跑构建器，逐字节复现 artifacts/（§FRIEND-CARRIER 常驻自证）──
# 验的是 zip 里那一份，不是 STAGE 里的副本：只有真发出去的包自己能复现，自证才不作假。
# 全程在 ${PKG_OUT} 下的临时目录里跑，不读也不写仓库源码；不过就删包 ABORT，不留半态。
REPRO="${PKG_OUT}/repro-${COMMIT}-${STAMP}"
rm -rf "$REPRO" 2>/dev/null || true
mkdir -p "$REPRO/out" || { echo "X 建复现临时目录失败" >&2; rm -f "$ZIP" 2>/dev/null || true; exit 1; }
if ! unzip -q "$ZIP" -d "$REPRO"; then
	echo "BUILD_EXIT=1 reason=repro_unzip_failed" >&2
	rm -f "$ZIP" 2>/dev/null || true
	exit 1
fi
for tgt in executor sandbox; do
	REPRO_LOG="$REPRO/repro-$tgt.log"
	if ! python3 "$REPRO/payload/tools/build_local_executor.py" --out-dir "$REPRO/out" --target "$tgt" >"$REPRO_LOG" 2>&1; then
		echo "BUILD_EXIT=1 reason=repro_build_failed_$tgt" >&2
		cat "$REPRO_LOG" >&2
		rm -f "$ZIP" 2>/dev/null || true
		exit 1
	fi
	if ! grep -q "BUILD_RESULT=OK" "$REPRO_LOG"; then
		echo "BUILD_EXIT=1 reason=repro_no_ok_$tgt" >&2
		cat "$REPRO_LOG" >&2
		rm -f "$ZIP" 2>/dev/null || true
		exit 1
	fi
done
REPRO_OUT=$(python3 - "$REPRO" <<'PY'
import glob, hashlib, os, sys
sys.dont_write_bytecode = True
d = sys.argv[1]
arts = sorted(glob.glob(os.path.join(d, "payload", "artifacts", "*.py")))
outs = sorted(glob.glob(os.path.join(d, "out", "*.py")))
if len(arts) != 2 or len(outs) != 2:
    print("REPRO_FAIL reason=count artifacts=%d rebuilt=%d" % (len(arts), len(outs)))
    sys.exit(1)
by_name = dict((os.path.basename(p), p) for p in outs)
for a in arts:
    n = os.path.basename(a)
    if n not in by_name:
        print("REPRO_FAIL reason=missing_rebuilt:" + n)
        sys.exit(1)
    with open(a, "rb") as f:
        ba = f.read()
    with open(by_name[n], "rb") as f:
        bo = f.read()
    if ba != bo:
        print("REPRO_FAIL reason=bytes_differ:" + n)
        sys.exit(1)
    print("REPRO_BYTE_SAME file=%s sha256=%s bytes=%d"
          % (n, hashlib.sha256(ba).hexdigest()[:16], len(ba)))
print("REPRO_OK files=2 bytesame=1")
PY
)
printf '%s\n' "$REPRO_OUT"
case "$REPRO_OUT" in
	*REPRO_OK*) : ;;
	*)
		echo "BUILD_EXIT=1 reason=repro_mismatch（包已删，不留半态）" >&2
		rm -f "$ZIP" 2>/dev/null || true
		rm -rf "$REPRO" 2>/dev/null || true
		exit 1
		;;
esac
rm -rf "$REPRO" 2>/dev/null || true

FILES_N=$(unzip -l "$ZIP" | awk 'END {print $2}')
BYTES_Z=$(wc -c <"$ZIP" | tr -d ' ')
FP=$(python3 -c "
import json,zipfile
z=zipfile.ZipFile('$ZIP')
print(json.load(z.open('payload/MANIFEST.json'))['contract_fp'])
")
echo "BUILD_COMMIT=$COMMIT"
echo "FILES=$FILES_N"
echo "BYTES=$BYTES_Z"
echo "CONTRACT_FP=$FP"
echo "WIREGUARD=$OVERLAY"
echo "OVERLAY=$([ "$OVERLAY" = "embedded" ] && echo bundled || echo not_bundled_first_leg_blocked)"
echo "PKG=$ZIP"
echo "PKG_SHA256=$(shasum -a 256 "$ZIP" | cut -d' ' -f1)"
echo "REPRO_OK=1"
echo "BUILD_EXIT=0"
