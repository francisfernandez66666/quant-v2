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

# ---------------------------------------------------------------------------
# 账号真源投影层（2026-11-10，PLAN_CARRIER_RISK_REBUILD §1.1）。
# 11-10 事故的根因不是"没导出"，是 live_rules 被烘成嵌套挂件、规则层只读
# 顶层散键（散键=Go 出厂默认）。本表显式声明「账号路径 → 载体顶层键」投影：
#   * path     账号配置 JSON 里的取值路径（"." 分段）；
#   * key      载体顶层 PARAMS 键；
#   * expect   对账基线值（docs/EXPORT_ACCOUNT_RULES_20261110.json 脱敏导出）；
#   * override owner 裁决覆盖（见 PLAN §3.0），diff 必须登记在案，不许静默。
# live_rules 挂件保留作审计，但不再是唯一落点——本表就是断言本身。
# ---------------------------------------------------------------------------
PROJECTIONS = (
    {"path": "qmt.max_signal_vwap_premium_pct", "key": "max_signal_vwap_premium_pct", "expect": 5},
    {"path": "qmt.max_positions", "key": "max_positions", "expect": 6},
    {"path": "qmt.risk_gate.day_loss_limit_pct", "key": "day_loss_limit_pct", "expect": 3.5},
    {"path": "qmt.min_order_amount", "key": "min_order_amount", "expect": 10000},
    # Q4（owner 10:45）：单票 35% 覆盖账号 25%，差异登记在案。
    {"path": "qmt.risk_gate.single_stock_value_pct", "key": "single_stock_cap_pct",
     "expect": 25, "override": 35},
    {"path": "qmt.risk_gate.sector_value_pct", "key": "industry_cap_pct", "expect": 35},
    {"path": "qmt.risk_gate.concept_value_pct", "key": "cluster_cap_pct", "expect": 35},
    # Q4：日额随动态仓位 room，daily_budget_amount 仅作绝对上限旁证留痕。
    {"path": "qmt.daily_budget_amount", "key": "daily_buy_cap", "expect": 150000},
    # Q2′（owner 10:45）：恢复 Go 出厂全套动态仓位公式（账号 sizing.enabled=false
    # 是账号里未配置全 0 的表现，不是"要关"的裁决；最终裁决以 §3.0 为准）。
    {"path": "sizing.enabled", "key": "sizing_enabled", "expect": False, "override": True},
)

# owner 覆盖的登记账（随 params 烘入载体，审计可见，不许静默改）。
PROJECTION_OVERRIDES = {
    "single_stock_cap_pct": {
        "account_value": 25, "value": 35,
        "source": "owner 2026-11-10 10:45 Q4 (PLAN 3.0)"},
    "sizing_enabled": {
        "account_value": False, "value": True,
        "source": "owner 2026-11-10 10:45 Q2' (PLAN 3.0)"},
}


def _dig(rules, path):
    """按 "." 路径取账号配置值；缺路径返回 None（投影侧缺席=断言失败，不猜）。"""
    node = rules
    for part in str(path).split("."):
        if not isinstance(node, dict) or part not in node:
            return None
        node = node[part]
    return node


def projection_report(params_doc, block):
    """逐键对账：返回 [(key, expected, actual, ok, override), ...]。

    expected 取投影表：owner override 优先，否则账号快照值；账号快照缺该键时
    退回表内 expect（对账基线），两者都缺 ⇒ 该键记 (missing, FAIL)。
    """
    rules = block  # live_rules 块本身就是账号段的镜像
    out = []
    for proj in PROJECTIONS:
        key = proj["key"]
        want = proj.get("override", _dig(rules, proj["path"]))
        if want is None:
            want = proj.get("expect")
        got = (params_doc.get("params") or {}).get(key)
        try:
            ok = (float(got) == float(want)) if want is not None and got is not None else (got == want)
        except (TypeError, ValueError):
            ok = got == want
        out.append((key, want, got, ok, "override" in proj))
    return out


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


def daily_cap_pair(params_doc, block):
    """§CAP-OVERRIDE-ASCII（2026-11-09）：顶层 daily_buy_cap 与快照内
    daily_budget_amount 的两本账读数（§CAP-OVERRIDE 对账用，只读不写）。

    carrier 顶层 daily_buy_cap 于 owner 2026-11-09 15:15 裁决起对齐账号
    daily_budget_amount（提交 fa6939b）：这枚帽决定"当日还能买多少"，载体被
    静态闸禁止联网，值只能烘参，而它的真源就是这份快照。失配＝载体与现网
    分家，必须让人听见，不许哪本账悄悄赢。
    """
    return ((params_doc.get("params") or {}).get("daily_buy_cap"),
            (block.get("qmt") or {}).get("daily_budget_amount"))


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
    cap_params, cap_snapshot = daily_cap_pair(params_doc, block)
    cap_ok = cap_params == cap_snapshot
    # 投影层逐键对账（2026-11-10）：顶层散键必须与账号真源（含 owner 覆盖）一致。
    proj = projection_report(params_doc, block)
    proj_bad = [r for r in proj if not r[3]]
    if args.check:
        print("LIVE_RULES_CHANGED_TOTAL=%d" % (0 if same else 1))
        print("LIVE_RULES_SECTIONS=%d" % len([k for k in block if k in SECTIONS]))
        print("LIVE_RULES_STRATEGIES=%d" % len(block["enabled_strategies"]))
        print("LIVE_RULES_FACTORS=%d" % len(block["applied_factor_ids"]))
        for key, want, got, ok, is_override in proj:
            tag = "OVERRIDE" if is_override else "PROJ"
            print("PROJECTION %s key=%s want=%s got=%s ok=%d"
                  % (tag, key, json.dumps(want, ensure_ascii=True),
                     json.dumps(got, ensure_ascii=True), 1 if ok else 0))
        print("PROJECTION_BAD_TOTAL=%d" % len(proj_bad))
        if not cap_ok:
            # §CAP-OVERRIDE-ASCII（2026-11-09）：params.daily_buy_cap 必须**恒等于**
            # 快照里账号的 daily_budget_amount——失配就是两本账，事后没人翻旧账
            # 会发现载体帽与现网配置已经分家。帽漂移的 reason 单列，别被块漂移盖掉。
            print("LIVE_RULES_CAP_MISMATCH key=daily_buy_cap params=%s snapshot=%s"
                  % (json.dumps(cap_params, ensure_ascii=True),
                     json.dumps(cap_snapshot, ensure_ascii=True)))
        result = "OK" if (same and cap_ok and not proj_bad) else "DRIFT"
        if result == "DRIFT" and not cap_ok:
            result = "DRIFT reason=daily_buy_cap_mismatch"
        if result == "DRIFT" and proj_bad:
            result = "DRIFT reason=projection_%s" % ",".join(r[0] for r in proj_bad)
        print("LIVE_RULES_RESULT=%s" % result)
        print("LIVE_RULES_RESULT_CODE=%d" % (0 if result == "OK" else 1))
        return 0 if result == "OK" else 1

    if same:
        if not cap_ok:
            print("LIVE_RULES_RESULT=ABORT reason=daily_buy_cap_mismatch params=%s snapshot=%s"
                  % (json.dumps(cap_params, ensure_ascii=True),
                     json.dumps(cap_snapshot, ensure_ascii=True)))
            return 1
        if proj_bad:
            print("LIVE_RULES_RESULT=ABORT reason=projection_%s" % ",".join(r[0] for r in proj_bad))
            return 1
        print("LIVE_RULES_RESULT=OK unchanged=1")
        return 0
    if proj_bad:
        # 投影漂移时连审计块也不写：先让顶层键与账号真源对上，再谈携带快照。
        print("LIVE_RULES_RESULT=ABORT reason=projection_%s" % ",".join(r[0] for r in proj_bad))
        return 1
    if not cap_ok:
        # 刻意不写：把 daily_budget_amount 抄进顶层帽等于「数从数据文件进了代码路径」；
        # 对账不落数，写侧也只在两侧对上时才落盘，失配当场停让人去看，而不是猜一个。
        print("LIVE_RULES_RESULT=ABORT reason=daily_buy_cap_mismatch params=%s snapshot=%s"
              % (json.dumps(cap_params, ensure_ascii=True),
                 json.dumps(cap_snapshot, ensure_ascii=True)))
        return 1
    params_doc["params"]["live_rules"] = block
    # §CAP-OVERRIDE-ASCII（2026-11-09）：ensure_ascii=True 在这文件上是铁律——
    # 这里曾经写 False，把 derive 写好的 \uXXXX 转义整体反转义成 UTF-8 中文
    # （实测 213 处非 ASCII 入库）。产物转义有两道闸（builder g1 / 测试 ascii 门），
    # 但这是仓里唯一一处会把已经合规的文件重新写坏的姿势，2026-11-09 起禁绝。
    payload = json.dumps(params_doc, ensure_ascii=True, indent=2, sort_keys=False) + "\n"
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
