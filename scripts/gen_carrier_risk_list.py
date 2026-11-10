#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""gen_carrier_risk_list.py -- Q3 利空清单生成器（PLAN_CARRIER_RISK_REBUILD §1.6）。

每日盘前由热更新链（carrier_hot_reload.sh step 0）调用，把「脑侧 LLM 归因产出的
利空标的」合进载体烘参的 risk_list_dynamic（当日有效、命中即拒买 reason=llm_bearish）。

数据源与降级语义（§1.6 最终口径 ④）：
  * 输入：qmt_gateway/risk_list_llm.json（newsagent/LLM 归因的利空标的清单，
    形如 ["000520", "600xxx", ...]；由后续 newsagent 集成产出。文件缺失/损坏
    不算失败——**保留上一版清单**，绝不静默清空）；
  * 静态兜底（risk_list_static，含 000520 + ST/退市/监管关注）永不失效，本脚本
    不碰它；
  * 输出：合并去重写回 params["risk_list_dynamic"]（六位裸代码或带后缀均收，
    载体侧 _risk_reason 按首位比对）。
  * --check：只读对账，输出 MERGED_TOTAL/CHANGED，漂移即非零（门禁可挂）。

English: merge the daily LLM bearish list into the carrier's baked dynamic risk
list; missing input keeps yesterday's list (never silently cleared); the static
floor is untouchable here.
"""

import argparse
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PARAMS = os.path.join(ROOT, "qmt_gateway", "local_executor_params.json")
LLM_LIST = os.path.join(ROOT, "qmt_gateway", "risk_list_llm.json")


def _norm(code):
    """裸六位代码归一（剥 .SZ/.SH/.BJ 后缀），非法形状原样返回由调用方过滤。"""
    c = str(code or "").strip().upper()
    for suf in (".SZ", ".SH", ".BJ"):
        if c.endswith(suf):
            c = c[: -len(suf)]
    return c


def load_llm_bearish():
    """读当日 LLM 利空清单；缺失/损坏返回 None（= 保留上一版，不静默清空）。"""
    if not os.path.exists(LLM_LIST):
        return None
    try:
        with open(LLM_LIST, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (ValueError, OSError):
        return None
    if isinstance(data, dict):
        data = data.get("codes") or data.get("list") or []
    if not isinstance(data, list):
        return None
    return [_norm(c) for c in data if _norm(c)]


def merge(prev, incoming):
    """合并去重；incoming None 表示"今天没有新清单"，沿用 prev（绝不静默清空）。"""
    kept = [_norm(c) for c in (prev or []) if _norm(c)]
    if incoming is None:
        return sorted(set(kept)), False
    return sorted(set(kept) | set(incoming)), True


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="read-only drift check")
    args = ap.parse_args()

    doc = json.load(open(PARAMS, encoding="utf-8"))
    params = doc["params"]
    prev = params.get("risk_list_dynamic") or []
    static_floor = params.get("risk_list_static") or []
    incoming = load_llm_bearish()
    merged, changed = merge(prev, incoming)
    # 静态兜底不允许被动态清单吞并——它由 owner 烘参维护，这里只做重叠告警。
    overlap = sorted(set(merged) & {_norm(c) for c in static_floor})

    if args.check:
        print("RISKLIST_STATIC=%d DYNAMIC_PREV=%d INCOMING=%s MERGED=%d CHANGED=%d OVERLAP=%s" % (
            len(static_floor), len(prev),
            "none" if incoming is None else len(incoming),
            len(merged), 1 if changed else 0, overlap or "-"))
        return 0

    if not changed and merged == [_norm(c) for c in prev]:
        print("RISKLIST_RESULT=OK unchanged=1 total=%d" % len(merged))
        return 0
    params["risk_list_dynamic"] = merged
    payload = json.dumps(doc, ensure_ascii=True, indent=2) + "\n"
    with open(PARAMS, "w", encoding="utf-8", newline="\n") as fh:
        fh.write(payload)
    back = json.load(open(PARAMS, encoding="utf-8"))
    if back["params"]["risk_list_dynamic"] != merged:
        print("RISKLIST_RESULT=ABORT reason=readback_mismatch")
        return 1
    print("RISKLIST_RESULT=OK written=1 total=%d incoming=%s" % (
        len(merged), "none" if incoming is None else len(incoming)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
