#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§STRATEGY-SWITCH-SYNC（2026-11-06）量化交易 tab 战法开关 → 载体参数派生器。

owner 裁决：「今晚有我更新的批准战法，启用的战法口径以量化交易 tab 里的战法开关为准」，
且「现网开关与因子规则写进仓库 params」。执行器被静态闸禁止联网，拿不到运行时的 tab 读数，
所以口径只能靠**构建期烘参**：本脚本把现网快照派生进 local_executor_params.json。

为什么不许在 params 里手改：
  开关列表一改，载体却还跑着旧的因子规则表，表现就是「屏上说开了 14 个、盒子只跑 1 个」。
  这类假账没有任何运行时报错，只能靠「快照 → 派生 → 门禁对账」这条链在构建前拦住。

真源（快照）来自两处现网读数：
  * auth.json#quant_config_json_v1.qmt.strategies —— 量化交易 tab 的启用白名单
  * applied_factors.json                          —— 自动研究页审批应用的因子规则
快照本身是可审计基线；改 tab 开关后重新抓快照并重跑本脚本，不要改 params。

用法：
  python3 scripts/sync_strategy_switches.py --check   # 只读对账，漂移即非零（门禁调用）
  python3 scripts/sync_strategy_switches.py --write   # 把快照派生进 params 并逐字节复读

English: derives the carrier's enabled-strategy whitelist and factor rule table from the
captured live snapshot; refuses to write when a whitelisted rule has no carrier implementation
or uses a factor the carrier cannot compute.
"""

import argparse
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SNAPSHOT = os.path.join(ROOT, "qmt_gateway", "strategy_switches_snapshot.json")
PARAMS = os.path.join(ROOT, "qmt_gateway", "local_executor_params.json")

# 量化交易 tab 开了、但载体没有移植规则代码的战法。必须显式登记并如实打 unmapped 读数：
# 把「没移植」静默当成「已启用」正是本仓一路在锤的降级报成功。
KNOWN_UNMAPPED = {
    "n_shape": "owner 11-04 裁决：N 形战法不移植进 py",
}

# 载体移植的形态规则 kind 与 Go 战法 ID 同名的三枚；其余 ID 一律要显式登记。
FORM_KIND_IDS = ("dragon", "double_bump", "dragon_return")


def _read(path):
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)


def derive(snapshot, params_doc):
    """返回 (派生块, 错误列表)。错误非空时绝不写盘。"""
    allow = snapshot.get("enabled_strategies")
    rules = snapshot.get("factor_rules") or []
    if not isinstance(allow, list) or not allow:
        return {}, ["snapshot_strategies_empty_or_broken"]
    by_id = {}
    safe_rules = []
    for rule in rules:
        rid = rule.get("id")
        if not rid or rid in by_id:
            return {}, ["rule_id_missing_or_duplicate:%s" % (rid,)]
        by_id[rid] = rule
        # gate_ascii 要求烘出的产物逐字节纯 ASCII；现网规则的显示名是中文
        # （"因子战法#118"），json.loads 会把 \uXXXX 还原成真中文写进 BAKED 块。
        # 载体归因只用 rule["id"]，不用 name，所以派生时把 name 收敛成 id。
        item = {k: rule[k] for k in
                ("factors", "weights", "directions", "buy_threshold") if k in rule}
        item["id"] = rid
        item["name"] = rid
        safe_rules.append(item)
    rules = safe_rules
    bad = []

    # ① 白名单里每个 fac_/pat_ 必须真有规则表条目：缺一条就是「开关说开了、盒子没得跑」。
    for rid in allow:
        if rid.startswith(("fac_", "pat_")) and rid not in by_id:
            bad.append("rule_missing:" + rid)
    # ② 规则表不许夹带白名单外的规则：tab 关掉的战法不许仍在载体里参与最佳规则竞争。
    for rid in by_id:
        if rid not in allow:
            bad.append("rule_not_allowed:" + rid)
    # ③ 规则用到的因子必须在载体 factor_defs 里，否则整条规则会被 declined 静默跳过。
    defs = set(((params_doc.get("params") or {}).get("factor_defs") or {}).keys())
    for rule in rules:
        for factor in rule.get("factors") or []:
            if factor not in defs:
                bad.append("factor_def_missing:%s:%s" % (rule["id"], factor))
    # ④ 形态 ID 要么有载体 kind，要么显式登记 unmapped；未知的一律判红等 owner 补登记。
    for rid in allow:
        if rid.startswith(("fac_", "pat_")):
            continue
        if rid in FORM_KIND_IDS or rid in KNOWN_UNMAPPED:
            continue
        bad.append("unmapped_form:" + rid)

    # 空白名单分支的词表与 Go internal/signalctl/signalctl.go:admit 逐字一致
    # （内置形态 + 因子/形态聚合池；动量与未知键必须显式列名）。这不是阈值，
    # 是口径词表，所以也由快照派生而不许载体自己抄第二份。
    # 派生块里任何非 ASCII 都会让两载体的 g1_ascii 拒建；宁可在这里点名红。
    for key, value in {
        "enabled_strategies": list(allow),
        "factor_rules": rules,
        "unmapped_strategies": sorted(KNOWN_UNMAPPED),
    }.items():
        if any(ord(ch) > 127 for ch in json.dumps(value, ensure_ascii=False)):
            bad.append("non_ascii_in_block:" + key)

    block = {
        "enabled_strategies": list(allow),
        "factor_rules": rules,
        "unmapped_strategies": sorted(KNOWN_UNMAPPED),
        "default_allowed_strategies": [
            "dragon", "double_bump", "n_shape", "dragon_return", "factor", "pattern",
        ],
        "factor_id_prefix": "fac_",
        "pattern_id_prefix": "pat_",
        # 载体移植的形态规则 kind -> Go 战法 ID（同名的三枚）。其余 kind 没有
        # tab 开关归属：影子档照常观察，但绝不许以 live 身份碰钱（见 decide_with_modes）。
        "strategy_kind_to_go": {
            "dragon": "dragon",
            "double_bump": "double_bump",
            "dragon_return": "dragon_return",
        },
    }
    return block, bad


def main():
    ap = argparse.ArgumentParser()
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true", help="read-only drift check")
    mode.add_argument("--write", action="store_true", help="derive snapshot into params")
    args = ap.parse_args()

    snapshot = _read(SNAPSHOT)
    params_doc = _read(PARAMS)
    block, bad = derive(snapshot, params_doc)
    if bad:
        print("SYNC_RESULT=ABORT reason=%s" % (",".join(bad),))
        return 2

    params = params_doc["params"]
    drift = [key for key in block if params.get(key) != block[key]]
    if args.check:
        if drift:
            print("SYNC_RESULT=DRIFT keys=%s" % (",".join(drift),))
            return 1
        print("SYNC_RESULT=OK enabled=%d rules=%d unmapped=%d" %
              (len(block["enabled_strategies"]), len(block["factor_rules"]),
               len(block["unmapped_strategies"])))
        return 0

    params.update(block)
    # 参数文件必须是纯 ASCII（构建器 gate_ascii 会对烘出的产物逐字节判），中文说明放快照。
    text = json.dumps(params_doc, ensure_ascii=True, indent=2, sort_keys=False) + "\n"
    tmp = PARAMS + ".tmp-sync"
    with open(tmp, "w", encoding="ascii", newline="\n") as handle:
        handle.write(text)
    os.replace(tmp, PARAMS)
    back = _read(PARAMS)
    if back["params"] != params:
        print("SYNC_RESULT=ABORT reason=readback_mismatch")
        return 2
    print("SYNC_RESULT=OK enabled=%d rules=%d unmapped=%d changed=%d" %
          (len(block["enabled_strategies"]), len(block["factor_rules"]),
           len(block["unmapped_strategies"]), len(drift)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
