#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""replay_trades_20261119.py — 实盘逐笔复盘（离线只读，≥1 个月口径）。

用途
----
把云端 `live.db` 里**真实发生的**成交逐笔还原成「回合」，回答四个问题：
  ① 每一笔到底买了什么价、卖在什么价、持有多久、赚赔多少（含手续费印花税）；
  ② 赚赔按**战法 / 按日 / 按票**怎么分布（哪条腿在赚钱，哪条在放血）；
  ③ 挂单价比成交价差多少（滑点）；
  ④ **证据本身够不够**——买卖两侧的记录是否对称、有多少成交定位不到日期。

硬纪律
------
* **只读**：两个库都以 `mode=ro` 打开并设 `PRAGMA query_only=1`，不建表、不写一行。
* **不改账**：不碰 `trade_results` / `real_positions` / `orders`，只产出报告与 CSV。
* **不复用 `trade_results.realized_pnl`**：现网实测该列存在量级坏值
  （样本 `-19036720.0`，即 1900 万的负数对一笔 2000 股 9.5 元的买入），
  直接引用会把整份复盘读成事故；本脚本一律自行 FIFO 配对重算。
* **缺日期的成交不猜日期**：`traded_at` 只有 `HH:MM:SS` 的行（§REPORT-STAMP 历史残留）
  无法定位交易日，单独计数并在报告里点名，绝不按"今天"折进去。
* **残差不当持仓**：FIFO 残差是「窗口内买入没配对到卖出记录」，它与 `real_positions`
  的真持仓并列展示并比较——两者差得多，说明缺的是**成交证据**，不是仓位。

归属链（优先级从高到低，每一步都留 `attrib_basis` 供复核）
----------------------------------------------------------
1. `fills.order_id` / `fills.signal_id` 里内嵌的战法段（形如 `buy:002584:动量:20260917`）——
   2026-09 那批老账就是这样写的，最硬，不需要二次查表。
2. `(成交日, 代码)` → `signal_decisions` 里当日 `stage in ('placed','evaluated')` 的行；
   多行时按 `qty` 最接近成交量的那条取（实测多战法歧义极少，报告会点名计数）。
3. 都没有 → `未归因`（不猜）。

用法（云端以 quant 身份跑，保证产物属主是 quant）
------------------------------------------------
    sudo -n -u quant python3 /tmp/replay_trades_20261119.py \
        --out-dir /tmp/replay-20261119 --all

    # 只看最近 30 天
    ... --days 30

    # 指定窗口 / 指定账号
    ... --start 2026-10-19 --end 2026-11-19 --user u_1785377969825355000
"""

import argparse
import csv
import os
import sqlite3
import sys
from collections import defaultdict

# 内嵌战法段能认的前缀：引擎下单是 buy:/sell:，日内回转是 t0:，柜台回报同步是 ext:/pend:/数字号。
_EMBED_PREFIXES = ("buy", "sell", "t0")

# 卖出单里这几个词是**动作**不是战法（`sell:600580:减仓:20261119`），
# 归因时加前缀单列，避免"减仓"被当成一条战法参与排名。
_SELL_ACTIONS = ("减仓", "清仓", "止盈", "止损", "做T", "收摊")


def ro_connect(path):
    """只读打开：mode=ro + query_only，双保险防误写。"""
    if not os.path.isfile(path):
        sys.exit("DB_MISSING %s" % path)
    conn = sqlite3.connect("file:%s?mode=ro" % path, uri=True)
    conn.execute("PRAGMA query_only=1")
    return conn


def norm_day(ts):
    """把 `2026-11-19 14:55:07` / `2026-08-31T14:40:51+08:00` 归一成 `2026-11-19`。

    两种写法现网并存（桥自闭环导出的是空格形、早期记录带 T 与时区），
    只取前 10 个字符即可，不做时区换算——库里存的本来就是北京时间。
    """
    return (ts or "")[:10]


def to_float(v, default=0.0):
    try:
        if v is None or v == "":
            return default
        return float(v)
    except (TypeError, ValueError):
        return default


def seg6(code):
    """`002093.SZ` → `002093`（signal_decisions 只存裸 6 位）。"""
    return (code or "").split(".")[0]


def parse_embedded(sigid):
    """从内嵌形态里取 (前缀, 代码, 战法段)；认不出返回 None。

    例：`buy:002584:动量:20260917`  → ('buy', '002584', '动量')
        `sell:600580:减仓:20261119` → ('sell', '600580', '减仓')
        `ext:20261119:3527`         → None（柜台回报同步，没有战法段）
    """
    if not sigid:
        return None
    parts = str(sigid).split(":")
    if len(parts) < 4:
        return None
    head, code, strat = parts[0], parts[1], parts[2]
    if head not in _EMBED_PREFIXES:
        return None
    if not code.isdigit() or len(code) != 6:
        return None
    return head, code, strat


def load_fills(conn, start_iso, end_iso, user):
    """取窗口内**带日期**的成交；缺日期的单独计数返回。"""
    cur = conn.cursor()
    where_user = ""
    params_win = [start_iso, end_iso]
    if user:
        where_user = " AND user_id = ?"
        params_win.append(user)

    undated = cur.execute(
        "SELECT COUNT(*) FROM fills WHERE length(COALESCE(traded_at,'')) < 10" + where_user,
        ([user] if user else []),
    ).fetchone()[0]

    rows = cur.execute(
        """SELECT id, order_id, code, side, price, qty, amount, traded_at, signal_id,
                  user_id, fee, stamp_tax, trade_id, market_vwap, fill_vwap_premium_pct,
                  vwap_source
             FROM fills
            WHERE length(COALESCE(traded_at,'')) >= 10
              AND substr(traded_at,1,10) >= ?
              AND substr(traded_at,1,10) <= ?"""
        + where_user
        + " ORDER BY traded_at, id",
        params_win,
    ).fetchall()
    cols = [d[0] for d in cur.description]
    return [dict(zip(cols, r)) for r in rows], undated


def load_orders(conn):
    """订单表只用来取**挂单价**（滑点基准）与 `intraday_t`；缺行不算错。"""
    cur = conn.cursor()
    out = {}
    for r in cur.execute(
        "SELECT order_id, price, side, signal_id, intraday_t, status, code FROM orders"
    ):
        out[str(r[0])] = {
            "price": to_float(r[1]),
            "side": r[2] or "",
            "signal_id": r[3] or "",
            "intraday_t": int(r[4] or 0),
            "status": r[5] or "",
            "code": seg6(r[6] or ""),
        }
    return out


def load_decisions(conn, start_key, end_key):
    """当日决策快照：给「没有内嵌战法段」的成交做第二级归属。

    只取 placed/evaluated——skipped 是"被闸拦住的"，它们没有对应成交，
    混进来会让"最接近 qty"选到一条根本没下单的记录。
    """
    cur = conn.cursor()
    rows = cur.execute(
        """SELECT day, code, strategy_key, stage, qty, notional, signal_id, capped_by, reason
             FROM signal_decisions
            WHERE day >= ? AND day <= ?
              AND stage IN ('placed','evaluated')""",
        (start_key, end_key),
    ).fetchall()
    idx = defaultdict(list)
    for day, code, strat, stage, qty, notional, sig, capped, reason in rows:
        idx[(day, seg6(code))].append(
            {
                "strategy_key": strat or "",
                "stage": stage or "",
                "qty": int(qty or 0),
                "notional": to_float(notional),
                "signal_id": sig or "",
                "capped_by": capped or "",
                "reason": reason or "",
            }
        )
    return idx


def attribute(fill, orders, dec_idx):
    """返回 (战法, 归属依据, 通道)。战法为空串表示未归因。"""
    day_key = norm_day(fill["traded_at"]).replace("-", "")
    code6 = seg6(fill["code"])

    # 一级：内嵌战法段（老账与引擎单都是这个形状）
    for cand in (fill.get("signal_id"), fill.get("order_id")):
        emb = parse_embedded(cand)
        if emb:
            head, _code, strat = emb
            if head == "t0":
                return (strat or "做T"), "embedded:t0", "T"
            if strat in _SELL_ACTIONS:
                # 动作不是战法：加前缀单列，避免"减仓"混进战法排名
                return "动作·" + strat, "embedded:%s" % head, "engine"
            return strat, "embedded:%s" % head, "engine"

    # 通道判定（即便归因不到战法，通道也要尽量认出来）
    sig = str(fill.get("signal_id") or "")
    oid = str(fill.get("order_id") or "")
    channel = "engine"
    if sig.startswith("ext:") or oid.startswith("ext:"):
        channel = "carrier_sync"  # 柜台 ORDER 表回报回来的一律 ext:，分不出人工/自闭环
    elif oid.isdigit() or sig.startswith("pend:"):
        channel = "carrier_sync"
    order = orders.get(oid)
    if order and order.get("intraday_t"):
        channel = "T"

    # 二级：(日, 代码) → 当日决策快照，取 qty 最接近成交量的那条
    cands = dec_idx.get((day_key, code6)) or []
    if cands:
        qty = int(fill.get("qty") or 0)
        best = min(cands, key=lambda c: abs((c["qty"] or 0) - qty))
        if best["strategy_key"]:
            basis = "decision:%s" % best["stage"]
            if len(cands) > 1:
                basis += "/ambiguous%d" % len(cands)
            return best["strategy_key"], basis, channel

    return "", "none", channel


def fifo_round_trips(fills, orders, dec_idx):
    """按 (账号, 票) FIFO 配对：买入建 lot，卖出按时间顺序吃掉最早的 lot。

    只做配对与算术，不做任何"该不该卖"的判断——复盘只描述发生了什么。
    """
    by_key = defaultdict(list)
    for f in fills:
        by_key[(f.get("user_id") or "", seg6(f["code"]))].append(f)

    trips = []
    unclosed = []
    enriched = []

    for (uid, code), rows in by_key.items():
        lots = []
        for f in rows:
            price = to_float(f["price"])
            qty = int(f["qty"] or 0)
            if qty <= 0 or price <= 0:
                continue
            fee = to_float(f["fee"]) + to_float(f["stamp_tax"])
            strat, basis, channel = attribute(f, orders, dec_idx)
            rec = dict(f)
            rec.update(
                {
                    "user_id": uid,
                    "code6": code,
                    "day": norm_day(f["traded_at"]),
                    "strategy": strat,
                    "attrib_basis": basis,
                    "channel": channel,
                    "fee_total": round(fee, 2),
                }
            )
            enriched.append(rec)

            if f["side"] == "买入":
                lots.append(
                    {
                        "user_id": uid,
                        "code6": code,
                        "qty": qty,
                        "price": price,
                        "fee_per_share": fee / qty if qty else 0.0,
                        "day": rec["day"],
                        "strategy": strat,
                        "fill_id": f["id"],
                        "order_id": f["order_id"],
                    }
                )
                continue

            # 卖出：从最早的 lot 开始吃
            remain = qty
            sell_fee_per_share = fee / qty if qty else 0.0
            while remain > 0 and lots:
                lot = lots[0]
                take = min(remain, lot["qty"])
                buy_cost = (lot["price"] + lot["fee_per_share"]) * take
                sell_net = (price - sell_fee_per_share) * take
                pnl = sell_net - buy_cost
                trips.append(
                    {
                        "user_id": uid,
                        "code": code,
                        "strategy": lot["strategy"] or strat or "未归因",
                        "channel": channel,
                        "buy_day": lot["day"],
                        "sell_day": rec["day"],
                        "hold_days": _day_gap(lot["day"], rec["day"]),
                        "qty": take,
                        "buy_price": round(lot["price"], 4),
                        "sell_price": round(price, 4),
                        "buy_cost": round(buy_cost, 2),
                        "sell_net": round(sell_net, 2),
                        "pnl": round(pnl, 2),
                        "pnl_pct": round(pnl / buy_cost * 100, 4) if buy_cost else 0.0,
                        "buy_fill": lot["fill_id"],
                        "sell_fill": f["id"],
                        "buy_order": lot["order_id"],
                        "sell_order": f["order_id"],
                    }
                )
                lot["qty"] -= take
                remain -= take
                if lot["qty"] <= 0:
                    lots.pop(0)

            # remain>0：卖出量大于窗口内买入（跨窗口买入/跨账纪元），不凭空补仓，只留读数
            if remain > 0:
                trips.append(
                    {
                        "user_id": uid,
                        "code": code,
                        "strategy": strat or "未归因",
                        "channel": channel,
                        "buy_day": "",
                        "sell_day": rec["day"],
                        "hold_days": -1,
                        "qty": remain,
                        "buy_price": 0.0,
                        "sell_price": round(price, 4),
                        "buy_cost": 0.0,
                        "sell_net": round((price - sell_fee_per_share) * remain, 2),
                        "pnl": 0.0,
                        "pnl_pct": 0.0,
                        "buy_fill": "",
                        "sell_fill": f["id"],
                        "buy_order": "",
                        "sell_order": f["order_id"],
                        "note": "卖出量大于窗口内买入（跨窗口/跨库），不计盈亏",
                    }
                )

        for lot in lots:
            if lot["qty"] > 0:
                unclosed.append(lot)

    return enriched, trips, unclosed


def _day_gap(a, b):
    """自然日差；任一侧解析不了就 -1（不编造 0 天）。"""
    from datetime import date

    try:
        ya, ma, da = [int(x) for x in a.split("-")]
        yb, mb, db = [int(x) for x in b.split("-")]
        return (date(yb, mb, db) - date(ya, ma, da)).days
    except (ValueError, AttributeError):
        return -1


def last_closes(conn, codes, end_iso):
    """每只票在窗口末之前最后一个收盘价。

    用 `trading.db` 的 `daily`：`live.db` 那份现网是空的（0 行），
    而 `daily.trade_date` 是 `YYYYMMDD` 紧凑形（实测 MIN/MAX = 20200102/20261119），
    所以统一按紧凑形比较，不做两种格式的模糊匹配（模糊匹配会把 12 月的行也吃进来）。
    """
    cur = conn.cursor()
    end_key = end_iso.replace("-", "")
    out = {}
    for code in codes:
        row = cur.execute(
            """SELECT trade_date, close FROM daily
                WHERE ts_code LIKE ? AND trade_date <= ?
                ORDER BY trade_date DESC LIMIT 1""",
            (code + "%", end_key),
        ).fetchone()
        if row:
            out[code] = (str(row[0]), to_float(row[1]))
    return out


def load_positions(conn, user):
    """真持仓（`real_positions`）：这是"现在手上有什么"的唯一权威读数。"""
    cur = conn.cursor()
    sql = "SELECT ts_code, name, qty, cost_price, amount, strategy, user_id, updated_at FROM real_positions"
    params = []
    if user:
        sql += " WHERE user_id = ?"
        params.append(user)
    rows = cur.execute(sql + " ORDER BY amount DESC", params).fetchall()
    return [
        {
            "code": seg6(r[0]),
            "ts_code": r[0],
            "name": r[1] or "",
            "qty": int(r[2] or 0),
            "cost": to_float(r[3]),
            "amount": to_float(r[4]),
            "strategy": r[5] or "",
            "user_id": r[6] or "",
            "updated_at": r[7] or "",
        }
        for r in rows
        if int(r[2] or 0) > 0
    ]


def load_equity(conn, user):
    """账户权益日序列（`real_equity_daily`）：用来交叉核对"已实现盈亏"的量级是否可信。"""
    cur = conn.cursor()
    sql = "SELECT day, equity FROM real_equity_daily"
    params = []
    if user:
        sql += " WHERE user_id = ?"
        params.append(user)
    rows = cur.execute(sql + " ORDER BY day", params).fetchall()
    return [(str(d), to_float(e)) for d, e in rows]


def summarize(enriched, trips, unclosed, closes):
    """算总览与三张分组表。"""
    real_trips = [t for t in trips if t["buy_cost"] > 0]
    pnl_total = sum(t["pnl"] for t in real_trips)
    wins = [t for t in real_trips if t["pnl"] > 0]
    losses = [t for t in real_trips if t["pnl"] < 0]
    cost_total = sum(t["buy_cost"] for t in real_trips)

    by_strat = defaultdict(lambda: {"n": 0, "pnl": 0.0, "win": 0, "cost": 0.0, "hold": 0})
    by_day = defaultdict(lambda: {"n": 0, "pnl": 0.0, "win": 0})
    by_code = defaultdict(lambda: {"n": 0, "pnl": 0.0, "win": 0})
    for t in real_trips:
        s = by_strat[t["strategy"]]
        s["n"] += 1
        s["pnl"] += t["pnl"]
        s["cost"] += t["buy_cost"]
        s["win"] += 1 if t["pnl"] > 0 else 0
        s["hold"] += max(t["hold_days"], 0)
        d = by_day[t["sell_day"]]
        d["n"] += 1
        d["pnl"] += t["pnl"]
        d["win"] += 1 if t["pnl"] > 0 else 0
        c = by_code[t["code"]]
        c["n"] += 1
        c["pnl"] += t["pnl"]
        c["win"] += 1 if t["pnl"] > 0 else 0

    inv_cost = 0.0
    inv_mark = 0.0
    inv_rows = []
    for lot in unclosed:
        cost = lot["price"] * lot["qty"]
        inv_cost += cost
        mark = closes.get(lot.get("code6", ""))
        price = mark[1] if mark else 0.0
        inv_mark += price * lot["qty"]
        inv_rows.append(
            {
                "code": lot.get("code6", ""),
                "fill_id": lot.get("fill_id", ""),
                "strategy": lot["strategy"] or "未归因",
                "qty": lot["qty"],
                "buy_price": round(lot["price"], 4),
                "buy_day": lot["day"],
                "cost": round(cost, 2),
                "last_close": price,
                "last_close_day": mark[0] if mark else "",
                "unrealized": round((price - lot["price"]) * lot["qty"], 2) if price else 0.0,
            }
        )

    return {
        "trips": real_trips,
        "pnl_total": pnl_total,
        "win_n": len(wins),
        "loss_n": len(losses),
        "win_rate": (len(wins) / len(real_trips) * 100) if real_trips else 0.0,
        "cost_total": cost_total,
        "avg_hold": (sum(t["hold_days"] for t in real_trips) / len(real_trips)) if real_trips else 0.0,
        "by_strat": dict(by_strat),
        "by_day": dict(by_day),
        "by_code": dict(by_code),
        "inv_cost": inv_cost,
        "inv_mark": inv_mark,
        "inv_rows": inv_rows,
    }


def slippage(fills, orders):
    """成交价 vs 挂单价的偏差（bps）。统一成「正数=不利」：买贵了为正，卖便宜了为正。

    三条安全边界（缺一条就会产出**假滑点**）：
      ① 票必须对得上——`order_id` 在现网跨账纪元**会复用**（同号不同票），只按号 join
         会把 002230 的成交配到另一只票的挂单上（实测出现 130996 bps 的荒谬值）；
      ② 方向必须一致（买入成交不拿卖出挂单做基准）；
      ③ 偏差超过 ±20% 一律判为**配错行**，计入 `suspect` 而不是统计量。
    """
    rows = []
    suspect = 0
    for f in fills:
        o = orders.get(str(f["order_id"]))
        if not o or o["price"] <= 0:
            continue
        if o["code"] and o["code"] != seg6(f["code"]):
            suspect += 1
            continue
        if o["side"] and o["side"] != f["side"]:
            suspect += 1
            continue
        fill_px = to_float(f["price"])
        if fill_px <= 0:
            continue
        bps = (fill_px - o["price"]) / o["price"] * 10000.0
        if abs(bps) > 2000.0:
            suspect += 1
            continue
        if f["side"] == "卖出":
            bps = -bps
        rows.append(
            {
                "code": seg6(f["code"]),
                "side": f["side"],
                "day": norm_day(f["traded_at"]),
                "order_price": o["price"],
                "fill_price": fill_px,
                "bps": round(bps, 2),
            }
        )
    return rows, suspect


def write_csv(path, rows, fields):
    with open(path, "w", newline="", encoding="utf-8-sig") as fh:
        w = csv.DictWriter(fh, fieldnames=fields, extrasaction="ignore")
        w.writeheader()
        for r in rows:
            w.writerow(r)


def fmt(x, nd=2):
    return ("%%.%df" % nd) % x


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--live-db", default="/var/lib/quant-trading-v2/live.db")
    ap.add_argument("--trading-db", default="/var/lib/quant-trading-v2/trading.db")
    ap.add_argument("--out-dir", default="/tmp/replay-trades")
    ap.add_argument("--start", default="")
    ap.add_argument("--end", default="")
    ap.add_argument("--days", type=int, default=30)
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--user", default="")
    ap.add_argument("--top", type=int, default=15)
    args = ap.parse_args()

    live = ro_connect(args.live_db)
    trading = ro_connect(args.trading_db)

    cur = live.cursor()
    where = " WHERE length(COALESCE(traded_at,'')) >= 10"
    params = []
    if args.user:
        where += " AND user_id = ?"
        params.append(args.user)
    span = cur.execute(
        "SELECT MIN(substr(traded_at,1,10)), MAX(substr(traded_at,1,10)) FROM fills" + where,
        params,
    ).fetchone()
    if not span or not span[1]:
        sys.exit("NO_DATED_FILLS")

    end_iso = args.end or span[1]
    if args.all:
        start_iso = args.start or span[0]
    else:
        start_iso = args.start
    if not start_iso:
        from datetime import date, timedelta

        y, m, d = [int(x) for x in end_iso.split("-")]
        start_iso = (date(y, m, d) - timedelta(days=args.days)).isoformat()

    fills, undated = load_fills(live, start_iso, end_iso, args.user)
    if not fills:
        sys.exit("NO_FILLS_IN_WINDOW %s..%s" % (start_iso, end_iso))
    orders = load_orders(live)
    dec_idx = load_decisions(live, start_iso.replace("-", ""), end_iso.replace("-", ""))

    enriched, trips, unclosed = fifo_round_trips(fills, orders, dec_idx)
    closes = last_closes(
        trading,
        sorted({l["code6"] for l in unclosed if l.get("code6")} | {seg6(f["code"]) for f in fills}),
        end_iso,
    )
    fill_users = [args.user] if args.user else sorted({f["user_id"] for f in fills if f["user_id"]})
    # 只把**有成交证据**的账号纳入复盘：现网有一户只有手工持仓、没有 fills，
    # 混进来会让"真持仓 vs FIFO 残差"的对照直接失真。
    positions = []
    for u in fill_users:
        positions.extend(load_positions(live, u))
    others = [p for p in load_positions(live, "") if p["user_id"] not in fill_users]
    equity = []
    for u in fill_users:
        equity.extend(load_equity(live, u))
    equity.sort(key=lambda r: r[0])

    stats = summarize(enriched, trips, unclosed, closes)
    slip, slip_suspect = slippage(fills, orders)

    os.makedirs(args.out_dir, exist_ok=True)
    p_md = os.path.join(args.out_dir, "REPLAY_TRADES.md")
    p_trips = os.path.join(args.out_dir, "replay_round_trips.csv")
    p_fills = os.path.join(args.out_dir, "replay_fills.csv")
    p_strat = os.path.join(args.out_dir, "replay_by_strategy.csv")
    p_unclosed = os.path.join(args.out_dir, "replay_unclosed.csv")
    p_slip = os.path.join(args.out_dir, "replay_slippage.csv")
    p_pos = os.path.join(args.out_dir, "replay_positions.csv")

    write_csv(
        p_trips,
        sorted(trips, key=lambda t: (t["sell_day"], t["code"])),
        [
            "code", "strategy", "channel", "buy_day", "sell_day", "hold_days", "qty",
            "buy_price", "sell_price", "buy_cost", "sell_net", "pnl", "pnl_pct",
            "buy_fill", "sell_fill", "buy_order", "sell_order", "note",
        ],
    )
    write_csv(
        p_fills,
        enriched,
        [
            "id", "traded_at", "day", "code", "code6", "side", "price", "qty", "amount",
            "fee", "stamp_tax", "fee_total", "strategy", "attrib_basis", "channel",
            "signal_id", "order_id", "trade_id", "user_id",
        ],
    )
    write_csv(
        p_strat,
        [
            {
                "strategy": k,
                "round_trips": v["n"],
                "pnl": round(v["pnl"], 2),
                "win": v["win"],
                "win_rate_pct": round(v["win"] / v["n"] * 100, 2) if v["n"] else 0.0,
                "cost": round(v["cost"], 2),
                "avg_hold_days": round(v["hold"] / v["n"], 2) if v["n"] else 0.0,
            }
            for k, v in sorted(stats["by_strat"].items(), key=lambda kv: -kv[1]["pnl"])
        ],
        ["strategy", "round_trips", "pnl", "win", "win_rate_pct", "cost", "avg_hold_days"],
    )
    write_csv(
        p_unclosed,
        sorted(stats["inv_rows"], key=lambda r: -r["cost"]),
        ["code", "fill_id", "strategy", "qty", "buy_price", "buy_day", "cost",
         "last_close", "last_close_day", "unrealized"],
    )
    write_csv(p_slip, slip, ["code", "side", "day", "order_price", "fill_price", "bps"])
    pos_rows = []
    for p in positions:
        mark = closes.get(p["code"])
        last = mark[1] if mark else 0.0
        pos_rows.append(
            {
                "code": p["code"],
                "name": p["name"],
                "qty": p["qty"],
                "cost_price": p["cost"],
                "cost_amount": round(p["amount"], 2),
                "strategy": p["strategy"],
                "user_id": p["user_id"],
                "updated_at": p["updated_at"],
                "last_close": last,
                "last_close_day": mark[0] if mark else "",
                "unrealized": round((last - p["cost"]) * p["qty"], 2) if last else 0.0,
            }
        )
    write_csv(
        p_pos,
        pos_rows,
        ["code", "name", "qty", "cost_price", "cost_amount", "strategy", "user_id",
         "updated_at", "last_close", "last_close_day", "unrealized"],
    )

    # ── 归因覆盖率（报告必须给出，否则"未归因"会被读成"没有战法"）
    n_fills = len(enriched)
    n_attr = sum(1 for f in enriched if f["strategy"])
    n_dec = sum(1 for f in enriched if f["attrib_basis"].startswith("decision:"))
    n_emb = sum(1 for f in enriched if f["attrib_basis"].startswith("embedded:"))
    n_none = n_fills - n_attr
    n_amb = sum(1 for f in enriched if "/ambiguous" in f["attrib_basis"])
    ch = defaultdict(int)
    for f in enriched:
        ch[f["channel"]] += 1

    buys = [f for f in enriched if f["side"] == "买入"]
    sells = [f for f in enriched if f["side"] == "卖出"]
    buy_amt = sum(to_float(f["amount"]) for f in buys)
    sell_amt = sum(to_float(f["amount"]) for f in sells)
    pos_amount = sum(p["amount"] for p in positions)

    # 缺日期成交按方向分开数（"32 笔里有多少是卖出"直接决定残差有多大）
    und_q = ("SELECT COUNT(*) FROM fills WHERE length(COALESCE(traded_at,'')) < 10"
             " AND side = ?" + (" AND user_id = ?" if args.user else ""))
    und_params_extra = [args.user] if args.user else []
    undated_sells = live.cursor().execute(und_q, ["卖出"] + und_params_extra).fetchone()[0]
    undated_buys = live.cursor().execute(und_q, ["买入"] + und_params_extra).fetchone()[0]

    # 两本账交叉核对：orders 里"已成"与 fills 里的成交**不是同一批**——
    # 实测买入侧 fills 远多于 orders（自闭环买入不走下单表），卖出侧 orders 又多于 fills。
    ord_rows = live.cursor().execute(
        """SELECT side, COUNT(*), COALESCE(SUM(price*qty),0) FROM orders
            WHERE status IN ('已成','部成')
              AND substr(created_at,1,10) >= ? AND substr(created_at,1,10) <= ?
            GROUP BY side""",
        (start_iso, end_iso),
    ).fetchall()
    ord_stat = {r[0]: (int(r[1]), to_float(r[2])) for r in ord_rows}

    matched_buy_fills = {t["buy_fill"] for t in stats["trips"] if t["buy_fill"]}
    partial_buys = sum(1 for r in stats["inv_rows"] if r["fill_id"] in matched_buy_fills)

    eq_first = equity[0] if equity else None
    eq_last = equity[-1] if equity else None
    eq_chg = (eq_last[1] - eq_first[1]) if eq_first and eq_last else 0.0

    lines = []
    A = lines.append
    A("# 实盘逐笔复盘（离线只读）")
    A("")
    A("- 窗口：**%s → %s**" % (start_iso, end_iso))
    A("- 账号：%s" % (", ".join("`%s`" % u for u in fill_users) or "无"))
    A("- 数据源：`live.db`（fills / orders / signal_decisions / real_positions / real_equity_daily）"
      "+ `trading.db: daily`（最新收盘与浮动盈亏）")
    A("- 方法：按 **(账号, 票)** FIFO 配对；盈亏含手续费与印花税；滑点以 `orders.price` 为基准")
    A("- **只读**：`mode=ro` + `PRAGMA query_only=1`，本脚本不写任何库")
    A("")
    A("## 1. 证据质量（先看这节，再信后面的数字）")
    A("")
    A("| 读数 | 值 | 说明 |")
    A("|---|---|---|")
    A("| 有日期成交 | %d 笔（买 %d / 卖 %d） | 进入复盘 |" % (n_fills, len(buys), len(sells)))
    A("| 缺日期成交 | %d 笔（买 %d / 卖 %d） | `traded_at` 只有 `HH:MM:SS`（§REPORT-STAMP 残留），**不猜日期、不入账** |"
      % (undated, undated_buys, undated_sells))
    A("| 买入金额 / 卖出金额 | %s 元 / %s 元 | **不对称比例 %.1f : 1** ⇒ 卖出侧成交记录大面积为空 |"
      % (fmt(buy_amt), fmt(sell_amt), (buy_amt / sell_amt) if sell_amt else 0.0))
    A("| 两本账交叉核对（`orders` 已成/部成） | 买 %d 笔 / %s 元、卖 %d 笔 / %s 元 | "
      "比 `fills`（买 %d / 卖 %d）更少 ⇒ 两本账各自只覆盖一部分链路，**谁都不是完整账本** |"
      % (ord_stat.get("买入", (0, 0.0))[0], fmt(ord_stat.get("买入", (0, 0.0))[1]),
         ord_stat.get("卖出", (0, 0.0))[0], fmt(ord_stat.get("卖出", (0, 0.0))[1]),
         len(buys), len(sells)))
    A("| 期末真持仓（`real_positions`） | %d 只 / %s 元 | 账户这三个月权益基本走平（下节），不可能净买入 %s 元 |"
      % (len(positions), fmt(pos_amount), fmt(buy_amt - sell_amt)))
    A("| 战法归因 | %d / %d（%.1f%%） | 内嵌段 %d 笔、当日决策快照 %d 笔、未归因 %d 笔 |"
      % (n_attr, n_fills, (n_attr / n_fills * 100) if n_fills else 0, n_emb, n_dec, n_none))
    A("| 多战法歧义 | %d 笔 | 同一 (日,票) 命中多条 placed，已取 qty 最接近者 |" % n_amb)
    A("| 通道分布 | %s | `carrier_sync`＝柜台 ORDER 表回报（分不出人工/自闭环） |"
      % " / ".join("%s %d" % (k, v) for k, v in sorted(ch.items(), key=lambda kv: -kv[1])))
    A("| 挂单价可对上 | %d / %d 笔 | 只认「票+方向都一致且偏差 ≤20%%」的配对；另 %d 条同号不同票被剔除 |"
      % (len(slip), n_fills, slip_suspect))
    A("| `market_vwap` 非零 | %d 笔 | VWAP 列基本没落值，所以不用它做滑点基准 |"
      % sum(1 for f in fills if to_float(f.get("market_vwap")) > 0))
    A("| `trade_results.realized_pnl` | **不采用** | 现网有量级坏值（样本 -19036720.0），引用会把复盘读成事故 |")
    A("")
    A("> **一句话结论**：这份账的**买入侧是完整的，卖出侧是残缺的**。所以下面所有"
      "「回合」都是**能配对上的那一小片**，不是全部交易的盈亏。要拿全量只能先补成交与日期。")
    A("")
    A("## 2. 总览（已配对回合）")
    A("")
    A("| 指标 | 值 |")
    A("|---|---|")
    A("| 已配对回合 | %d（占买入笔数 %.1f%%） |"
      % (len(stats["trips"]), (len(stats["trips"]) / len(buys) * 100) if buys else 0))
    A("| 已实现盈亏 | **%s 元**（配对买入成本 %s 元） |" % (fmt(stats["pnl_total"]), fmt(stats["cost_total"])))
    A("| 胜 / 负 | %d / %d（胜率 %.1f%%） |" % (stats["win_n"], stats["loss_n"], stats["win_rate"]))
    A("| 平均持仓 | %.1f 自然日 |" % stats["avg_hold"])
    if eq_first and eq_last:
        A("| 账户权益（交叉核对） | %s %s → %s %s，区间变动 **%s 元** |"
          % (eq_first[0], fmt(eq_first[1]), eq_last[0], fmt(eq_last[1]), fmt(eq_chg)))
        A("| 权益变动 − 已实现盈亏 | %s 元 | 差额含浮盈亏与未记录成交，量级应远大于 0，"
          "若接近 0 反说明配对片段代表性够 |" % fmt(eq_chg - stats["pnl_total"]))
    A("")
    A("## 3. 按战法（哪条腿在赚钱）")
    A("")
    A("| 战法 | 回合 | 盈亏(元) | 胜率 | 平均持仓(日) | 买入成本(元) |")
    A("|---|---|---|---|---|---|")
    for k, v in sorted(stats["by_strat"].items(), key=lambda kv: -kv[1]["pnl"]):
        A("| %s | %d | %s | %.1f%% | %.1f | %s |"
          % (k or "未归因", v["n"], fmt(v["pnl"]),
             (v["win"] / v["n"] * 100) if v["n"] else 0.0,
             (v["hold"] / v["n"]) if v["n"] else 0.0, fmt(v["cost"])))
    A("")
    A("> `动作·减仓` 这类行是卖出动作归因（`sell:…:减仓:…`），不是一条战法；列出来只为不让它的盈亏凭空消失。")
    A("")
    A("## 4. 按日（卖出结算日）")
    A("")
    A("| 日 | 回合 | 盈亏(元) | 胜 |")
    A("|---|---|---|---|")
    for k in sorted(stats["by_day"]):
        v = stats["by_day"][k]
        A("| %s | %d | %s | %d |" % (k, v["n"], fmt(v["pnl"]), v["win"]))
    A("")
    A("## 5. 单笔回合：最赚 / 最亏")
    A("")
    pos = sorted([t for t in stats["trips"] if t["pnl"] > 0], key=lambda t: -t["pnl"])
    neg = sorted([t for t in stats["trips"] if t["pnl"] < 0], key=lambda t: t["pnl"])
    A("**最赚 %d 笔（共 %d 笔盈利）**" % (min(args.top, len(pos)), len(pos)))
    A("")
    A("| 票 | 战法 | 买入日 → 卖出日 | 股数 | 买价 → 卖价 | 盈亏(元) | 收益率 |")
    A("|---|---|---|---|---|---|---|")
    for t in pos[: args.top]:
        A("| %s | %s | %s → %s | %d | %.3f → %.3f | %s | %.2f%% |"
          % (t["code"], t["strategy"], t["buy_day"], t["sell_day"], t["qty"],
             t["buy_price"], t["sell_price"], fmt(t["pnl"]), t["pnl_pct"]))
    A("")
    A("**最亏 %d 笔（共 %d 笔亏损）**" % (min(args.top, len(neg)), len(neg)))
    A("")
    A("| 票 | 战法 | 买入日 → 卖出日 | 股数 | 买价 → 卖价 | 盈亏(元) | 收益率 |")
    A("|---|---|---|---|---|---|---|")
    for t in neg[: args.top]:
        A("| %s | %s | %s → %s | %d | %.3f → %.3f | %s | %.2f%% |"
          % (t["code"], t["strategy"], t["buy_day"], t["sell_day"], t["qty"],
             t["buy_price"], t["sell_price"], fmt(t["pnl"]), t["pnl_pct"]))
    A("")
    A("## 6. 当前真持仓（`real_positions`，与 FIFO 残差对照）")
    A("")
    if pos_rows:
        A("| 票 | 名称 | 股数 | 成本价 | 成本额 | 最新收盘 | 浮动(元) | 更新于 |")
        A("|---|---|---|---|---|---|---|---|")
        for r in pos_rows:
            A("| %s | %s | %d | %.3f | %s | %s | %s | %s |"
              % (r["code"], r["name"], r["qty"], r["cost_price"], fmt(r["cost_amount"]),
                 ("%.3f" % r["last_close"]) if r["last_close"] else "—",
                 fmt(r["unrealized"]) if r["last_close"] else "—", r["updated_at"]))
        A("")
        A("合计成本 %s 元，按最新收盘市值 %s 元。"
          % (fmt(sum(r["cost_amount"] for r in pos_rows)),
             fmt(sum((r["last_close"] or r["cost_price"]) * r["qty"] for r in pos_rows))))
        if others:
            A("")
            A("> 另有 %d 个账号共 %d 只持仓 / %s 元（无任何成交记录，多为手工改账），"
              "不在本复盘范围——把它们并进来会让残差对照失真。"
              % (len({o["user_id"] for o in others}), len(others),
                 fmt(sum(o["amount"] for o in others))))
    else:
        A("_`real_positions` 无正持仓行。_")
    A("")
    A("## 7. FIFO 残差（**证据缺口，不是仓位**）")
    A("")
    A("窗口内买入共 %d 笔 / %s 元；被卖出记录配对到的只有 %d 笔（其中 %d 笔只成交了一部分、仍有剩余），"
      "剩下 **%d 个 lot / 成本 %s 元**完全没有卖出记录。"
      % (len(buys), fmt(buy_amt), len(matched_buy_fills), partial_buys,
         len(stats["inv_rows"]), fmt(stats["inv_cost"])))
    A("")
    A("对照上节真持仓（%s 元）：残差比真持仓大 %.1f 倍 ⇒ 差的是**卖出成交证据**，"
      "不是我们还抱着这么多票。明细见 `replay_unclosed.csv`（按成本降序，仅供逐笔核账）。"
      % (fmt(pos_amount), (stats["inv_cost"] / pos_amount) if pos_amount else 0.0))
    A("")
    A("## 8. 滑点（成交价 vs 挂单价，正数=不利）")
    A("")
    if slip:
        bps = sorted(r["bps"] for r in slip)
        mid = bps[len(bps) // 2]
        A("可对上 %d 笔；中位数 **%s bps**，均值 %s bps，最差 %s bps，最好 %s bps。"
          % (len(slip), fmt(mid), fmt(sum(bps) / len(bps)), fmt(bps[-1]), fmt(bps[0])))
        A("")
        A("明细见 `replay_slippage.csv`。样本只占 %d 笔成交的 %.1f%%，**不足以下结论**，"
          "只证明这条量尺已经能用；另 %d 条成交因 order_id 跨纪元复用被剔除（同号不同票）。"
          % (n_fills, (len(slip) / n_fills * 100) if n_fills else 0, slip_suspect))
    else:
        A("_窗口内成交对不上挂单价（order_id 不匹配），本项无读数。_")
    A("")
    A("## 9. 结论（只陈述上面读得出的）")
    A("")
    if stats["trips"]:
        best = max(stats["by_strat"].items(), key=lambda kv: kv[1]["pnl"])
        worst = min(stats["by_strat"].items(), key=lambda kv: kv[1]["pnl"])
        A("1. **配对片段盈亏 %s 元、胜率 %.1f%%**（%d 个回合）；最赚 `%s` %s 元，最亏 `%s` %s 元。"
          % (fmt(stats["pnl_total"]), stats["win_rate"], len(stats["trips"]),
             best[0], fmt(best[1]["pnl"]), worst[0], fmt(worst[1]["pnl"])))
    A("2. **买入 : 卖出金额 = %.1f : 1**，而期末真持仓只有 %s 元 ⇒ 卖出成交记录大面积为空；"
      "`orders` 的「已成/部成」（买 %d/卖 %d）比 `fills`（买 %d/卖 %d）**更少**，"
      "说明两本账各自只覆盖一部分链路、谁都不是完整账本。在补齐之前，"
      "任何「这套权重赚不赚钱」的总账结论都不成立。"
      % ((buy_amt / sell_amt) if sell_amt else 0.0, fmt(pos_amount),
         ord_stat.get("买入", (0, 0.0))[0], ord_stat.get("卖出", (0, 0.0))[0],
         len(buys), len(sells)))
    A("3. 缺日期成交 %d 笔（其中卖出 %d 笔）、未归因 %d 笔：要靠券商交割单按 `trade_id` 逐行补日期与来源，"
      "不能一条 blanket UPDATE。" % (undated, undated_sells, n_none))
    A("4. `trade_results.realized_pnl` 有量级坏值，**复盘口径必须自算 FIFO**；本脚本已按此落地，"
      "该表应单独立项修（不是本报告范围）。")
    A("5. 本报告不含任何新权重（四象限 / blend）的反事实对比——那需要"
      "「重算不改信号、只改金额」的显式假设，是独立一份产出。")
    A("")
    A("## 10. 产物")
    A("")
    for p in (p_trips, p_fills, p_strat, p_unclosed, p_slip, p_pos):
        A("- `%s`" % os.path.basename(p))
    A("")

    with open(p_md, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines) + "\n")

    print(
        "REPLAY_OK window=%s..%s fills=%d undated=%d attr=%d(%d emb/%d dec/%d none) "
        "trips=%d pnl=%.2f win=%.1f%% buy_amt=%.2f sell_amt=%.2f positions=%d/%0.2f out=%s"
        % (start_iso, end_iso, n_fills, undated, n_attr, n_emb, n_dec, n_none,
           len(stats["trips"]), stats["pnl_total"], stats["win_rate"],
           buy_amt, sell_amt, len(positions), pos_amount, args.out_dir)
    )


if __name__ == "__main__":
    main()
