#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# publish_carrier.sh — 烘入式内置策略的**每日自动更新发布腿**（2026-11-09）。
#
# 一条命令保证三件事同源，缺一不同源盒子就会「如实失败」：
#   ① 用 scripts/build_local_executor.py --target sandbox 烘产物（9 道闸，含 g6 禁文件/网络、
#      g7 加载冒烟）；
#   ② 推到 /leg/carrier/（Caddy 静态直出，盒子 carrier_artifact_url 指向它）；
#   ③ 把期望态 {version, sha256, artifact_url, updated_at, request_state:pending}
#      写进决策机 <dataDir>/accounts/<uid>/carrier_manifest.json。
#
# 为什么必须原子：brain 侧 GET /api/qmt/carrier/manifest 是盒子唯一期望源，
# 盒子拿到后先比 installed_sha256、再下载字节、再与 strategy_sha256 比对，
# 只有全一致才允许 update_qmt_strategy.ps1 写盘。产物/清单/通道任一漂移 =
# 安静地「这一轮什么都不做」（本仓最忌讳的 degrade-and-report-success）。
#
# 用法：
#   scripts/publish_carrier.sh --version v04-20261109
#   scripts/publish_carrier.sh --version v90-20261109 --mode shadow --params <影子参数.json>
#   scripts/publish_carrier.sh --version v04-20261109 --dry-run     # 只烘不推不写
#
# 判读锚点：CARRIER_BAKE_OK / CARRIER_PUBLISH_OK / CARRIER_PUBLISH_ABORT reason=
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GZ_HOST="${GZ_HOST:-gz}"
GZ_CARRIER_DIR="${GZ_CARRIER_DIR:-/opt/quant/friend-pkg/carrier}"
DATA_DIR="${DATA_DIR:-/var/lib/quant-trading-v2}"
ACCOUNT_UID="${ACCOUNT_UID:-u_1785377969825355000}"
BASE_URL="${BASE_URL:-https://quant-trading.top}"
VERSION=""
PARAMS=""
MODE="live"
DRY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --params)  PARAMS="$2"; shift 2 ;;
    --mode)    MODE="$2"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    *) echo "CARRIER_PUBLISH_ABORT reason=bad_argument arg=$1" >&2; exit 2 ;;
  esac
done

[[ -n "$VERSION" ]] || { echo "CARRIER_PUBLISH_ABORT reason=version_required" >&2; exit 2; }
if ! [[ "$VERSION" =~ ^v[0-9]{2}-[0-9]{8}$ ]]; then
  echo "CARRIER_PUBLISH_ABORT reason=bad_version_shape want=vNN-YYYYMMDD got=$VERSION" >&2; exit 2
fi

PARAMS="${PARAMS:-qmt_gateway/local_executor_params.json}"
[[ -f "$PARAMS" ]] || { echo "CARRIER_PUBLISH_ABORT reason=params_absent path=$PARAMS" >&2; exit 2; }

OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT

# 1) 烘：版本由参数文件里的 strategy_version 决定，先核对它与 --version 一致（两把尺子）
P_VER=$(python3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8'))['strategy_version'])" "$PARAMS")
[[ "$P_VER" = "$VERSION" ]] || {
  echo "CARRIER_PUBLISH_ABORT reason=version_params_mismatch params=$P_VER want=$VERSION" >&2; exit 2; }

python3 scripts/build_local_executor.py --target sandbox --params "$PARAMS" --out-dir "$OUT" >"$OUT/build.log" 2>&1 || {
  tail -5 "$OUT/build.log" >&2; echo "CARRIER_PUBLISH_ABORT reason=bake_failed" >&2; exit 1; }
ART="$OUT/qmt_internal_strategy_${VERSION}.py"
[[ -f "$ART" ]] || { echo "CARRIER_PUBLISH_ABORT reason=artifact_missing" >&2; exit 1; }
SHA=$(python3 -c "import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],'rb').read()).hexdigest())" "$ART")
grep -q 'BUILD_GATES.*all_passed=1' "$OUT/build.log" || {
  echo "CARRIER_PUBLISH_ABORT reason=bake_gates_not_green" >&2; exit 1; }
echo "CARRIER_BAKE_OK version=$VERSION sha256=$SHA mode=$MODE"

if [[ "$DRY" -eq 1 ]]; then
  echo "CARRIER_PUBLISH_OK dryrun=1 sent=0"
  exit 0
fi

# 2) 推产物（先落地再改期望清单，顺序不能反：盒子可能随时来问）
scp -q "$ART" "$GZ_HOST:/tmp/" || { echo "CARRIER_PUBLISH_ABORT reason=scp_failed" >&2; exit 1; }
ssh "$GZ_HOST" "sudo mkdir -p $GZ_CARRIER_DIR && sudo cp /tmp/$(basename "$ART") $GZ_CARRIER_DIR/ && sudo chmod -R a+rX $GZ_CARRIER_DIR" || {
  echo "CARRIER_PUBLISH_ABORT reason=stage_failed" >&2; exit 1; }
URL="$BASE_URL/leg/carrier/$(basename "$ART")"
HTTP=$(curl -sS -o /tmp/_carrier_verify.py -w '%{http_code}' "$URL" || true)
[[ "$HTTP" = "200" ]] || { echo "CARRIER_PUBLISH_ABORT reason=url_not_live http=$HTTP" >&2; exit 1; }
GOT=$(python3 -c "import hashlib;print(hashlib.sha256(open('/tmp/_carrier_verify.py','rb').read()).hexdigest())")
[[ "$GOT" = "$SHA" ]] || { echo "CARRIER_PUBLISH_ABORT reason=served_bytes_sha_mismatch" >&2; exit 1; }

# 3) 写期望清单（保留 last_ack / requested_at 等回执段，只覆盖发布侧字段）
python3 - "$VERSION" "$SHA" "$URL" "$MODE" <<'PY' > "$OUT/manifest.json"
import json, sys, datetime
v, sha, url, mode = sys.argv[1:5]
print(json.dumps({
    "strategy_version": v,
    "strategy_sha256": sha,
    "artifact_url": url,
    "executor_version": "", "executor_sha256": "",
    "updated_at": datetime.datetime.now().strftime("%Y-%m-%dT%H:%M:%S+08:00"),
    "request_state": "pending",
    "mode": mode,
}, ensure_ascii=False, indent=2))
PY
scp -q "$OUT/manifest.json" "$GZ_HOST:/tmp/carrier_manifest.json" || {
  echo "CARRIER_PUBLISH_ABORT reason=manifest_scp_failed" >&2; exit 1; }
ssh "$GZ_HOST" "sudo mkdir -p $DATA_DIR/accounts/$ACCOUNT_UID && sudo cp /tmp/carrier_manifest.json $DATA_DIR/accounts/$ACCOUNT_UID/carrier_manifest.json && sudo chmod a+r $DATA_DIR/accounts/$ACCOUNT_UID/carrier_manifest.json" || {
  echo "CARRIER_PUBLISH_ABORT reason=manifest_write_failed" >&2; exit 1; }

echo "CARRIER_PUBLISH_OK version=$VERSION sha256=$SHA url=$URL uid=$ACCOUNT_UID"
