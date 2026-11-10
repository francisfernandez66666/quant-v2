#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# carrier_hot_reload.sh — 战法白名单热更新链（owner 2026-11-10 15:09 需求 3）。
#
# 需求：「战法白名单希望能热更新给到物理机」。载体被券商静态闸禁止联网，白名单
# 只能烘参；本脚本把「改 tab 开关 → 载体生效」这条链自动化：
#
#   1. 从 gz 现网只读拉取 auth.json 的 qmt.strategies + applied_factors.json；
#   2. 与本地烘参白名单比对（sha256）；
#   3. 无变化 ⇒ NOTHING_TO_DO（幂等，可反复跑）；
#   4. 有变化 ⇒ 重建两份快照 → sync_strategy_switches/sync_live_rules --write
#      → build_local_executor 9 闸 → publish_carrier.sh 版本号 +1 自动 bump；
#   5. 盒子 carrier_updater 按既有计划任务（15:40 / 盘前 --once）按 sha 拉取，
#      重启 QMT 客户端后生效。
#
# 用法：
#   scripts/carrier_hot_reload.sh            # 检查+按需发布
#   scripts/carrier_hot_reload.sh --force    # 跳过 sha 比对强制发布
# 判读锚点：HOTRELOAD_NOTHING_TO_DO / HOTRELOAD_PUBLISHED version= / HOTRELOAD_ABORT reason=
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

GZ_HOST="${GZ_HOST:-gz}"
OWNER_UID="${OWNER_UID:-u_1792728708736589500}"
STATE="/tmp/carrier_hotreload_whitelist.sha"
FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

REMOTE_JSON=$(ssh -o BatchMode=yes -o ConnectTimeout=10 "$GZ_HOST" "sudo -n python3 -c '
import json
auth=json.load(open(\"/var/lib/quant-trading-v2/auth.json\"))
cfg=[c for c in auth.get(\"configs\",[]) if c.get(\"key\")==\"quant_config_json_v1\"][0]
qc=json.loads(cfg[\"value\"])
af=json.load(open(\"/var/lib/quant-trading-v2/applied_factors.json\"))
wl=qc.get(\"qmt\",{}).get(\"strategies\") or []
rules=[r for r in (af if isinstance(af,list) else af.get(\"rules\",[])) if r.get(\"id\") in wl]
print(json.dumps({\"wl\":wl,\"rules\":rules,\"uid\":cfg.get(\"user_id\")},ensure_ascii=False))
'") || { echo "HOTRELOAD_ABORT reason=ssh_pull_failed" >&2; exit 1; }

SHA=$(printf '%s' "$REMOTE_JSON" | shasum -a 256 | cut -d' ' -f1)
WL_JSON=$(printf '%s' "$REMOTE_JSON" | python3 -c "import json,sys;print(json.dumps(json.load(sys.stdin)['wl'],ensure_ascii=True))")

if [[ "$FORCE" -eq 0 && -f "$STATE" && "$(cat "$STATE")" == "$SHA" ]]; then
    echo "HOTRELOAD_NOTHING_TO_DO sha=$SHA"
    exit 0
fi

echo "HOTRELOAD whitelist changed (sha=$SHA): $WL_JSON"

# 1) 重建快照（fresh pull 是唯一写入口，杜绝手工拼）
python3 - "$REMOTE_JSON" <<'PYEOF'
import json, sys
payload = json.loads(sys.argv[1])
WL = payload["wl"]; rules = payload["rules"]; uid = payload.get("uid", "")
def scrub(o):
    if isinstance(o, dict):
        return {k: scrub(v) for k, v in o.items()
                if not any(t in str(k).lower() for t in ("token","password","secret","api_key"))}
    if isinstance(o, list):
        return [scrub(v) for v in o]
    if isinstance(o, str):
        if any(ord(c) > 127 for c in o):
            raise ValueError("non_ascii_in_snapshot")
        return o
    return o
af = json.load(open("qmt_gateway/strategy_switches_snapshot.json", encoding="utf-8"))
# strategy_switches_snapshot: enabled_strategies + factor_rules（白名单∩审批）
d2 = json.load(open("qmt_gateway/strategy_switches_snapshot.json", encoding="utf-8"))
d2["enabled_strategies"] = WL
d2["factor_rules"] = rules
d2["captured_at"] = __import__("datetime").datetime.now().astimezone().isoformat(timespec="seconds")
d2["source"] = "gz auth.json+applied_factors.json (carrier_hot_reload)"
json.dump(d2, open("qmt_gateway/strategy_switches_snapshot.json","w",encoding="utf-8"),
          ensure_ascii=True, indent=2)
PYEOF
[[ $? -eq 0 ]] || { echo "HOTRELOAD_ABORT reason=snapshot_rebuild_failed" >&2; exit 1; }

# 2) 派生进 params（两把锁各自 --write，漂移会自己 ABORT）
python3 scripts/sync_strategy_switches.py --write || { echo "HOTRELOAD_ABORT reason=switches_sync_failed" >&2; exit 1; }
python3 scripts/sync_live_rules.py --write     || { echo "HOTRELOAD_ABORT reason=live_rules_sync_failed" >&2; exit 1; }

# 3) 版本号 +1 自动 bump（vNN-YYYYMMDD，同日重发递增 NN）
TODAY=$(date +%Y%m%d)
CUR=$(python3 -c "import json;print(json.load(open('qmt_gateway/local_executor_params.json',encoding='utf-8'))['strategy_version'])")
NN=$(printf '%02d' $(( $(echo "$CUR" | sed -E 's/^v([0-9]{2})-[0-9]{8}$/\1/') + 1 )))
VER="v${NN}-${TODAY}"
python3 - "$VER" <<'PYEOF'
import json, sys
path = "qmt_gateway/local_executor_params.json"
doc = json.load(open(path, encoding="utf-8"))
doc["strategy_version"] = sys.argv[1]
json.dump(doc, open(path,"w",encoding="utf-8"), ensure_ascii=True, indent=2)
PYEOF

# 4) 烘+推+写 manifest（9 闸不过 publish 自己会 ABORT）
bash scripts/publish_carrier.sh --version "$VER" || { echo "HOTRELOAD_ABORT reason=publish_failed version=$VER" >&2; exit 1; }

printf '%s' "$SHA" > "$STATE"
echo "HOTRELOAD_PUBLISHED version=$VER sha=$SHA"
