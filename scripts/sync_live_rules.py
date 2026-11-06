#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§LIVE-RULES-SYNC（2026-11-06 晚批）现网账号级量化规则 → 载体 params 派生器。

owner 令：「更新 win 的一键安装包（把现网的量化交易规则，打开的战法，动态仓位，
做 T 规则等全部送进去）」。载体被静态闸禁止联网，运行时拿不到现网 tab 读数，
所以这些口径只能靠**构建期烘参**：本脚本把只读导出的现网快照派生进
qmt_gateway/local_executor_params.json 的 params.live_rules，随后
scripts/build_local_executor.py 会把整个 params 字典烘进载体的 PARAMS 块，
scripts/build_friend_package.sh 再把含全量规则模块的产物打进 win 一键包。

为什么单开 live_rules 而不覆盖派生键：
  params 里那 349 个阈值由 derive_carrier_params.py 从 **Go 出厂默认**派生，
  §173 ⑩ 的「逐键同源」锁钉死它们不许手抄。现网账号级配置是另一层真源
  （auth.json#configs[].value，可含用户手调的 risk_gate/预算/仓位），两者语义不同：
  前者是代码默认，后者是现网生效值。混写会让单点导出锁当场失明，所以现网值
  独立成块随包携带，既可被安装/切换模式脚本读取，也能逐字节对账。

真源：qmt_gateway/live_rules_snapshot.json（由远端只读导出，凭据整键剔除）。
用法：
  python3 scripts/sync_live_rules.py --check   # 只读对账，漂移即非零（门禁调用）
  python3 scripts/sync_live_rules.py --write   # 派生进 params 并逐字节复读

English: derive the live account-level rule snapshot into the carrier params as a
separate, ASCII-safe block; never touch the Go-factory-derived threshold keys.
"""

import argparse
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SNAPSHOT = os.path.join(ROOT, "qmt_gateway", "live_rules_snapshot.json")
PARAMS = os.path.join(ROOT, "qmt_gateway", "local_executor_params.json")

# 随包携带的现网规则段。缺段不判红（有的账号没配过），但段内值必须逐字节同源。
SECTIONS = (
    "qmt", "sizing", "strategy", "paper", "risk_ctrl", "runtime", "position",
    "enhance", "signal_ctl", "emotion_cycle", "concept", "main_sector", "theme",
)


def _read(path):
    with open(path, "r", encoding="utf-8") as handle:
        return json.load(handle)


def _ascii_safe(value, path="live_rules"):
    """GBK 控制台 + g1_ascii 闸：任何非 ASCII 字符都必须在烘参前现形，不许静默转义。"""
    if isinstance(value, dict):
        return {k: _ascii_safe(v, path + "." + str(k)) for k, v in value.items()}
    if isinstance(value, list):
        return [_ascii_safe(v, path + "[]") for v in value]
    if isinstance(value, str):
        if any(ord(ch) > 127 for ch in value):
            raise ValueError("non_ascii_at:%s" % path)
        return value
    return value


def derive(snapshot):
    cfg = snapshot.get("configs") or []
    if not isinstance(cfg, list) or not cfg:
        raise ValueError("snapshot_configs_empty")
    # 运营主账号：带 qmt.strategies 的那份账号级配置（一键包是单账号使用）。
    primary = next((c for c in cfg if c.get("has_qmt_strategies")), cfg[0])
    rules = primary.get("rules") or {}
    block = {
        "captured_at": snapshot.get("captured_at", ""),
        "source": snapshot.get("source", ""),
        "account_key": primary.get("key", ""),
        "account_user_id": primary.get("user_id", ""),
        "enabled_strategies": (rules.get("qmt") or {}).get("strategies") or [],
        "applied_factor_ids": snapshot.get("applied_factor_ids") or [],
    }
    for name in SECTIONS:
        if name in rules:
            block[name] = _ascii_safe(rules[name], name)
    # 与战法快照对账：现网打开的白名单必须与随包派生的那份完全一致。
    switches = _read(os.path.join(ROOT, "qmt_gateway", "strategy_switches_snapshot.json"))
    if sorted(block["enabled_strategies"]) != sorted(switches.get("enabled_strategies") or []):
        raise ValueError("enabled_strategies_mismatch_with_switch_snapshot")
    return block


def main():
    ap = argparse.ArgumentParser()
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true", help="read-only drift check")
    mode.add_argument("--write", action="store_true", help="derive snapshot into params")
    args = ap.parse_args()

    try:
        snapshot = _read(SNAPSHOT)
        params_doc = _read(PARAMS)
        block = derive(snapshot)
    except Exception as exc:
        print("LIVE_RULES_RESULT=ABORT reason=%s" % exc)
        return 1

    current = (params_doc.get("params") or {}).get("live_rules")
    same = current == block
    if args.check:
        print("LIVE_RULES_CHANGED_TOTAL=%d" % (0 if same else 1))
        print("LIVE_RULES_SECTIONS=%d" % len([k for k in block if k in SECTIONS]))
        print("LIVE_RULES_STRATEGIES=%d" % len(block["enabled_strategies"]))
        print("LIVE_RULES_FACTORS=%d" % len(block["applied_factor_ids"]))
        print("LIVE_RULES_RESULT=%s" % ("OK" if same else "DRIFT"))
        return 0 if same else 1

    if same:
        print("LIVE_RULES_RESULT=OK unchanged=1")
        return 0
    params_doc["params"]["live_rules"] = block
    payload = json.dumps(params_doc, ensure_ascii=False, indent=2, sort_keys=False) + "\n"
    with open(PARAMS, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(payload)
    # 逐字节复读：写盘被拦/半截文件不许报成成功。
    back = open(PARAMS, "r", encoding="utf-8").read()
    if back != payload or (_read(PARAMS).get("params") or {}).get("live_rules") != block:
        print("LIVE_RULES_RESULT=ABORT reason=readback_mismatch")
        return 1
    print("LIVE_RULES_RESULT=OK written=1 sections=%d strategies=%d factors=%d" % (
        len([k for k in block if k in SECTIONS]),
        len(block["enabled_strategies"]), len(block["applied_factor_ids"])))
    return 0


if __name__ == "__main__":
    sys.exit(main())
