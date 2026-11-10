#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""账号真源投影层（PLAN_CARRIER_RISK_REBUILD §1.1 / P1）。

根因（§1.1 实锤）：载体顶层散键来自 Go 出厂默认，而现网账号真值被当作
`params.live_rules` 嵌套挂件，规则层一个键都不读它 ⇒ "配置导出了但没应用"。

注意最终裁决（§3.0，owner 10:45 六卡拍板，覆盖 §1.1 早期任何相反表述）：
- Q2′ 动态仓位：恢复 Go 出厂全套公式（power_mean + 10%~30% 单笔 + T 仓净额剔除 +
  总资产量纲护栏）。账号 `sizing.enabled=false`（全套 0）视为"账号未配置动态仓位"，
  **不是**"要关"的裁决 ⇒ 载体 `sizing_enabled` 终裁为 True（与 §1.1 早期"与账号反向"
  的初判相反，以 §3.0 为准）。
- Q4 额度：单票 35%（覆盖账号 `single_stock_value_pct=25`）、持仓上限 6 只、
  日额随动态仓位 room 走、`daily_budget_amount=150000` 仅作绝对上限旁证。

本模块把「账号真源（auth.json 导出的 quant_config_json_v1）」投影成载体顶层
PARAMS 的**账号派生键**，并对 §3.0 已裁决的背离显式登记（`OWNER_OVERRIDES`），
暴露漂移门禁（`check_projection_drift`）把"烘参又漂回出厂默认"这种事故钉死——但
已登记背离不报漂移（差异登记在案，不许静默）。

注意分工：
- 账号派生键（本模块投影）：单票/行业/概念帽、持仓数、日额、最低成交额、日亏熔断、
  VWAP 溢价、sizing 开关、战法白名单、现金保留比等 —— 全部来自账号真值。
- 静态烘入键（不归本模块）：vwap_sanity_*/industry_map/concept_map/
  strategy_kind_to_go/risk_list_static/emergency_stop/op_type/buy_confirm 常量等
  —— 来自脑侧静态数据或 owner 裁决常量，按既有 local_executor_params.json 烘入。

用法：
  python3 scripts/project_account_params.py EXPORT_JSON [--current CURRENT_PARAMS_JSON]
    输出投影后的账号派生 PARAMS（JSON），并打印与当前烘参的漂移（若有）。
"""
import json
import os
import sys


# owner 最终裁决覆盖（§3.0 终裁，覆盖 §1.1 早期相反表述）：账号值被 owner 拍板值覆盖时
# 显式登记，不得静默。键名 = 载体顶层 PARAMS 键。
#   - single_stock_cap_pct：账号 25，owner 终裁 35 覆盖（Q4，差异登记在案）。
#   - cluster_cap_pct：账号 concept 35，沿用 owner 35（与 §1.1 表初提的 40 不同，终裁以 Q4 为准）。
#   - sizing_enabled：账号 enabled=false（全套 0 视为未配置），§3.0 Q2′ 终裁恢复 Go 出厂
#     动态仓位 True（不是"要关"）；与 §1.1 早期"与账号反向"的初判相反，以最终裁决为准。
OWNER_OVERRIDES = {
    "single_stock_cap_pct": 35.0,
    "cluster_cap_pct": 35.0,
    "sizing_enabled": True,
}


def _dig(d, *path, default=None):
    cur = d
    for p in path:
        if not isinstance(cur, dict) or p not in cur:
            return default
        cur = cur[p]
    return cur


def project_account_rules(export, overrides=None):
    """把账号导出 JSON 投影成载体顶层 PARAMS 的账号派生键。

    返回 dict：键为顶层 PARAMS 键，值为账号真源（或 owner 覆盖值）。
    """
    ov = dict(overrides or OWNER_OVERRIDES)
    qmt = export.get("qmt", {}) or {}
    rg = qmt.get("risk_gate", {}) or {}
    sizing = export.get("sizing", {}) or {}
    money = qmt.get("money", {}) or {}

    out = {
        # 持仓 / 预算
        "max_positions": int(qmt.get("max_positions", 0)),
        "daily_buy_cap": float(qmt.get("daily_budget_amount", 0)),
        "min_order_amount": float(qmt.get("min_order_amount", 0)),
        # 质量 / 新鲜度
        "max_signal_vwap_premium_pct": float(qmt.get("max_signal_vwap_premium_pct", 0)),
        "entry_timeout_sec": int(qmt.get("entry_timeout_sec", 0)),
        "entry_pullback_pct": float(qmt.get("entry_pullback_pct", 0)),
        # 风险闸（来自账号 risk_gate 真值）
        "day_loss_limit_pct": float(rg.get("day_loss_limit_pct", 0)),
        "single_stock_cap_pct": float(rg.get("single_stock_value_pct", 0)),
        "industry_cap_pct": float(rg.get("sector_value_pct", 0)),
        "cluster_cap_pct": float(rg.get("concept_value_pct", 0)),
        "risk_per_trade_pct": float(rg.get("risk_per_trade_pct", 0)),
        "risk_budget_shadow": bool(rg.get("risk_budget_shadow", False)),
        "limit_up_block_buy": bool(rg.get("limit_up_block_buy", False)),
        "limit_down_block_sell": bool(rg.get("limit_down_block_sell", False)),
        # 动态仓位开关：账号 sizing.enabled=false（全套 0）视为"账号未配置动态仓位"，
        # 非"要关"；§3.0 Q2′ 终裁恢复 Go 出厂动态仓位 ⇒ override 为 True（见 OWNER_OVERRIDES）。
        "sizing_enabled": bool(sizing.get("enabled", False)),
        # 战法白名单：直接沿用账号 qmt.strategies（含 fac_* 因子前缀，_go_admitted 透传）
        "enabled_strategies": list(qmt.get("strategies", []) or []),
        # 现金保留：账号是比例（cash_reserve_ratio），载体保持比例量纲
        "min_cash_reserve_ratio": float(money.get("cash_reserve_ratio", 0)),
    }

    # owner 终裁覆盖（显式，差异登记在案）
    for k, v in ov.items():
        out[k] = v
    return out


def check_projection_drift(current_params, projection):
    """漂移门禁：把当前烘参（current_params）与投影真源（projection）逐键比对，
    返回 [(key, baked, projected, severity), ...]。

    current_params 既可以是已经展开的 PARAMS 块（扁平 dict），也可以是
    local_executor_params.json 整文件（顶层含 "params" 子对象）。二者都支持：
    若顶层存在 "params" 子对象且其本身为 dict，则下钻到该子对象再比对
    —— 因为载体的 PARAMS 正是从 local_executor_params.json 的 "params"
    子对象烘入的（build_local_executor.py 的落点），不是 JSON 根。

    severity：
      - "CRITICAL"：安全帽（含 sizing 开关）与账号真源不一致
        （如 sizing_enabled True vs 账号 False，或单票/行业/概念帽、日亏熔断、持仓数、
        最低成交额、VWAP 溢价漂回出厂默认）
      - "WARN"：非安全键不一致（如白名单缺项），或该键在烘参中不存在（未烘入）
    """
    findings = []
    safety_keys = {
        "sizing_enabled", "single_stock_cap_pct", "industry_cap_pct",
        "cluster_cap_pct", "day_loss_limit_pct", "max_positions",
        "min_order_amount", "max_signal_vwap_premium_pct",
    }
    # 下钻到 PARAMS 块：local_executor_params.json 的落点是 "params" 子对象。
    if isinstance(current_params, dict) and isinstance(current_params.get("params"), dict):
        current_params = current_params["params"]
    for key, projected in projection.items():
        baked = current_params.get(key)
        if baked is None:
            findings.append((key, None, projected, "WARN"))
            continue
        # 列表（白名单）按集合比较，顺序无关
        if isinstance(projected, list) and isinstance(baked, list):
            if set(projected) != set(baked):
                sev = "CRITICAL" if key in safety_keys else "WARN"
                findings.append((key, baked, projected, sev))
            continue
        if baked != projected:
            sev = "CRITICAL" if key in safety_keys else "WARN"
            findings.append((key, baked, projected, sev))
    return findings


def main(argv):
    if len(argv) < 2:
        sys.stderr.write("usage: project_account_params.py EXPORT_JSON [--current CURRENT_PARAMS_JSON]\n")
        return 2
    export_path = argv[1]
    current_path = None
    if "--current" in argv:
        idx = argv.index("--current")
        if idx + 1 < len(argv):
            current_path = argv[idx + 1]
    export = json.load(open(export_path, encoding="utf-8"))
    projection = project_account_rules(export)
    print(json.dumps(projection, ensure_ascii=False, indent=2))
    if current_path:
        current = json.load(open(current_path, encoding="utf-8"))
        drift = check_projection_drift(current, projection)
        if drift:
            sys.stderr.write("\n=== 漂移门禁（烘参 vs 账号真源）===\n")
            for key, baked, projected, sev in drift:
                sys.stderr.write("[%s] %s : baked=%r projected=%r\n" % (sev, key, baked, projected))
        else:
            sys.stderr.write("\n=== 漂移门禁：无漂移 ===\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
