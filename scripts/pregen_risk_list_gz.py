#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""pregen_risk_list_gz.py -- 在 gz 上运行的盘前利空归因提取（Q3 自动链第一跳）。

owner 口径（2026-11-10 16:38）：「LLM 归因，每天开盘前 8:30 归因一次并且发给 qmt
物理机」。引擎的 newsagent/LLM 归因随扫描周期常开（all_day_research），其产出固化在
`<dataDir>/signals_today.json`；本脚本在 08:30 被热更新链（Mac 侧 ssh 调起）读取它：

  * 取 Direction=="做空"（或 AlertType 命中做空/利空）的信号代码 → 利空候选；
  * 输出到 stdout（Mac 侧重定向进 qmt_gateway/risk_list_llm.json），供
    gen_carrier_risk_list.py 合进载体烘参 risk_list_dynamic；
  * 只读，不写任何生产文件；signals_today.json 读不到/为空 ⇒ 输出空数组
    （下游生成器的"保留上一版"语义接管，绝不静默清空）。

English: extract today's LLM-attributed bearish codes from the brain's persisted
signal batch (signals_today.json); empty output defers to the merge step's
keep-previous semantics.
"""

import json
import os
import sys

DATA_DIR = os.environ.get("QUANT_DATA_DIR", "/var/lib/quant-trading-v2")
PATH = os.path.join(DATA_DIR, "signals_today.json")
SHORT_DIRECTIONS = ("做空",)
SHORT_ALERTS = ("做空", "利空")
SHORT_STRATEGY_TYPES = ("high_churn", "break_down", "leader_decay", "leader_decay_v2",
                        "good_news_fade")


def _norm(code):
    c = str(code or "").strip().upper()
    for suf in (".SZ", ".SH", ".BJ"):
        if c.endswith(suf):
            c = c[: -len(suf)]
    return c


def main():
    codes = []
    try:
        with open(PATH, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        data = None
    rows = data if isinstance(data, list) else (data or {}).get("signals") or []
    for s in rows:
        if not isinstance(s, dict):
            continue
        direction = str(s.get("direction") or "")
        alert = str(s.get("alert_type") or s.get("alertType") or "")
        stype = str(s.get("strategy_type") or s.get("strategyType") or "")
        if direction in SHORT_DIRECTIONS or alert in SHORT_ALERTS \
                or stype in SHORT_STRATEGY_TYPES:
            c = _norm(s.get("code"))
            if len(c) == 6 and c.isdigit():
                codes.append(c)
    print(json.dumps({"codes": sorted(set(codes)), "source": PATH}, ensure_ascii=True))


if __name__ == "__main__":
    sys.exit(main())
