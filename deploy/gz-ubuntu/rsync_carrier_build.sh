#!/usr/bin/env bash
# rsync_carrier_build.sh — 烘焙最小子集仓库→gz /opt/quant/carrier_build（方案 A 的同源同步腿）。
# 改到清单内文件后重跑本脚本；清单外文件一律不进（盒子/云端烘焙面越小越可控）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
FILES=(
  scripts/build_local_executor.py
  scripts/sync_strategy_switches.py
  scripts/sync_live_rules.py
  scripts/gen_carrier_risk_list.py
  scripts/carrier_hot_reload_gz.py
  qmt_gateway/local_executor.py
  qmt_gateway/qmt_internal_strategy.py
  qmt_gateway/handler.py
  qmt_gateway/store.py
  qmt_gateway/rules_form.py
  qmt_gateway/rules_ladder.py
  qmt_gateway/rules_factor.py
  qmt_gateway/rules_t.py
  qmt_gateway/rules_sizing.py
  qmt_gateway/local_executor_params.json
  qmt_gateway/live_rules_snapshot.json
  qmt_gateway/strategy_switches_snapshot.json
)
tar -czf /tmp/carrier_build.tgz "${FILES[@]}"
scp -q /tmp/carrier_build.tgz gz:/tmp/carrier_build.tgz
ssh gz "sudo -n mkdir -p /opt/quant/carrier_build && sudo -n tar -xzf /tmp/carrier_build.tgz -C /opt/quant/carrier_build && sudo -n chmod -R 755 /opt/quant/carrier_build && echo RSYNC_OK"
