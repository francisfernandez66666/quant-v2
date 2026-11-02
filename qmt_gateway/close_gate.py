#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.close_gate —— 远端机器"收盘可以关 QMT 了吗"的**唯一判据源**（§FRIEND-PROFILE 批 2）。

为什么需要它，以及为什么它是 Python 而不是 PowerShell：
  朋友机器的看门狗要在 15:30 之后决定"能不能把 QMT 关掉"。这一判如果写在 ps1 里，
  判据就有两份（网关里那套账的语义 + ps1 里手抄的中文状态串），漂开的那次不会红——
  §INDEX-BLIND 那批"两份段序表"（卡上三条、实际四条）就是这么发生的。
  所以这里做唯一真源，ps1 只调用、只读 ASCII 锚点（和 gateway.py --profile-check 同一姿势）。

关掉 QMT 本身**没有交易风险**：A 股委托当日收盘即失效，不会带着旧委托活到次日。
门真正要保的是**远端本地账收敛到终态**——非终态行留在朋友机器上，就是 2026-10-30
那两笔「撤单中」挂住 3.67 万战略额度的形状，差别只在于这次发生在别人家里、没人核对。

三条判据（全部是网关能自证的读数；明细里原写的"拉取对账跑过一轮"网关证明不了，
已按 F-1 的结论换成下面 C2/C3 的可证形态——见 `docs/IMPL_MULTI_ACCOUNT_B_DETAIL_20261102.md` §13）：
  C1 收盘快照已发出：`/admin/status.close_snapshot.day == 今日` 且 `at` 非空
     （§CLOSE-RECON 的观测位；缺它就是 600362 事故"收盘 3 分钟不在账上"的现网判据）
  C2 没有"撤单中"卡单：`stuck_cancel_orders.n == 0`；读到 -1（查询失败）⇒ **保守不关**
  C3 当日委托无非终态行：`/orders?day=今日` 每行状态都在终态集合内
     读不到（None）⇒ **保守不关**；「未知(原始值)」一律算非终态（§W7-C：那种串不进三本资金账）

取向：任何"读不到"都判**不关**（宁可让朋友的 QMT 整晚开着，也不带着没收敛的账关门）。
CLI 输出是**纯 ASCII**（PowerShell 管道里中文会被码页换成 `?` ⇒ 锚点判读会恒假，11-02 现网两次假象同源）。
"""
from __future__ import annotations

import json
import sys

from store import TERMINAL_STATUSES

# 终态集合**不在这里另立一份**：真源是 store.TERMINAL_STATUSES（已成/已撤/部撤/废单/已废，
# 与 Go 侧 orderStatusRank 同口径）。第一版我在这儿手写了一个三元组 (已成,已撤,废单)，
# 少掉「部撤/已废」——那一天只要有任意一笔部撤，门就永远不开、朋友的 QMT 天天整晚开着，
# 而且现场读起来像"网络问题"。这正是本文件存在的理由：判据只许有一份。
TERMINAL_STATUS = TERMINAL_STATUSES


# 退出码：看门狗据此分流（0 才允许关）
EXIT_ALLOWED = 0
EXIT_HELD = 10
EXIT_GIVE_UP = 20
EXIT_UNREADABLE = 30


def _norm_day(s):
    """把 20261102 / 2026-11-02 归一成 YYYYMMDD（两本账各写一种，比对必须先归一）。"""
    return str(s or "").replace("-", "").strip()


def decide(admin_status, orders_rows, day, elapsed_sec=0, hold_limit_sec=3600):
    """返回 {"allowed","reason","blocked_n","stuck_n","over_limit"}。纯函数：不读网络、不写盘。

    reason 一律 ASCII 蛇形码（给 ps1 判读用）：
      ok / no_close_snapshot / stuck_cancel / nonterminal / status_unreadable / orders_unreadable
    over_limit：等待已超过 hold_limit_sec——**不改变 allowed**，只是让看门狗转成人工 + 告警。
    """
    out = {"allowed": False, "reason": "", "blocked_n": -1, "stuck_n": -1, "over_limit": 0}
    if not isinstance(admin_status, dict):
        # 读不到 ≠ 没问题：这条判断是整个门的存在理由（11-02 J0 同族：零回包曾被当成健康）
        out["reason"] = "status_unreadable"
        out["over_limit"] = 1 if elapsed_sec >= hold_limit_sec else 0
        return out

    snap = admin_status.get("close_snapshot") or {}
    if _norm_day(snap.get("day")) != _norm_day(day) or not str(snap.get("at") or ""):
        out["reason"] = "no_close_snapshot"
        out["over_limit"] = 1 if elapsed_sec >= hold_limit_sec else 0
        return out

    stuck = admin_status.get("stuck_cancel_orders") or {}
    if "n" not in stuck:
        out["reason"] = "status_unreadable"
        out["over_limit"] = 1 if elapsed_sec >= hold_limit_sec else 0
        return out
    stuck_n = int(stuck.get("n") or 0)
    out["stuck_n"] = stuck_n
    if stuck_n != 0:
        out["reason"] = "stuck_cancel"
        out["over_limit"] = 1 if elapsed_sec >= hold_limit_sec else 0
        return out

    if orders_rows is None:
        # 委托表读不到 ⇒ 无从证明"无非终态行"，保守不关
        out["reason"] = "orders_unreadable"
        out["over_limit"] = 1 if elapsed_sec >= hold_limit_sec else 0
        return out

    blocked = [r for r in orders_rows if str((r or {}).get("status") or "") not in TERMINAL_STATUS]
    out["blocked_n"] = len(blocked)
    if blocked:
        out["reason"] = "nonterminal"
        out["over_limit"] = 1 if elapsed_sec >= hold_limit_sec else 0
        return out

    out["allowed"] = True
    out["reason"] = "ok"
    return out


def exit_code_for(d):
    """allowed → 0；读不到 → 30（比"还差一步"更该被看见）；超时未开 → 20；否则 10。"""
    if d["allowed"]:
        return EXIT_ALLOWED
    if d["reason"] in ("status_unreadable", "orders_unreadable"):
        return EXIT_UNREADABLE
    return EXIT_GIVE_UP if d["over_limit"] else EXIT_HELD


def main(argv=None):
    """CLI：stdin 收 {"admin_status":{...},"orders":[...]|null,"day":"20261102","elapsed_sec":N}，
    打一行纯 ASCII 锚点后按退出码返回。

    参数走 stdin 而不是命令行：委托表里是中文状态串，经 PowerShell 管道传参正是
    11-02 那两次假象的来源（守卫永远不匹配、过滤器永远不过滤）。
    """
    try:
        payload = json.loads(sys.stdin.read() or "{}")
    except ValueError:
        print("CLOSE_GATE allowed=0 reason=payload_unparseable blocked_n=-1 stuck_n=-1 over_limit=1")
        return EXIT_UNREADABLE
    d = decide(
        payload.get("admin_status"),
        payload.get("orders"),
        payload.get("day", ""),
        elapsed_sec=int(payload.get("elapsed_sec") or 0),
        hold_limit_sec=int(payload.get("hold_limit_sec") or 3600),
    )
    print("CLOSE_GATE allowed=%d reason=%s blocked_n=%d stuck_n=%d over_limit=%d" % (
        1 if d["allowed"] else 0, d["reason"], d["blocked_n"], d["stuck_n"], d["over_limit"]))
    return exit_code_for(d)


if __name__ == "__main__":
    sys.exit(main())
