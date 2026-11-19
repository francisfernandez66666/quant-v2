#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§REPORT-STAMP 历史行回填（2026-11-19，逐行证据，绝不 blanket UPDATE）。

## 为什么需要它

载体自闭环这条腿导出的委托/成交时间只有 `HH:MM:SS`（`report_uplink` 的
`created_at = row["time"]`）。§REPORT-STAMP 已在 handler 边界补日期（`reportStampFromParts`），
但**修复之前**落库的行仍是 `10:01:51` 这种形态：所有 day-keyed 读面
（`/api/orders`、`/api/fills`、日报盈亏、当日预算、`SweepOrders(day)`、收盘对账）
按 `created_at` 前缀过滤 ⇒ 那些行整天读不到。

本脚本只做一件事：**在能证明日期时**把缺日期的时间戳补成完整北京时间戳。

## 证据链（唯一允许的两条，缺一条就跳过）

1. **成交 → 委托**：fills.order_id 是柜台裸号（`1246`），而 orders.order_id 形如
   `1246@20261119`；同日、同码、同方向的那条委托行给出了日期。
   约束：候选日期必须**唯一**；候选里 code/side 对不上的一律不算。
2. **委托 → 成交**：反向。某个缺日期的委托，若它的成交行**已有完整日期**（同裸号、同码、同方向），
   且日期唯一，则委托也归到那一天。

两条都是"另一侧已经带着日期"的证据，**不是猜**。日期不唯一、只有时分秒拿不到、
或者两侧都没日期 ⇒ 该行原样保留并列在报告里（回填缺证据就不做，绝不按顺序猜）。

## 其它纪律

- 缺省 **dry-run**（只打印将改什么、不改库）；`--apply` 才写。
- 写前把改动行整体备份成 JSON（`--backup` 指定，缺省与库同目录 `.bak-<ts>.json`），
  并逐行 UPDATE（`WHERE trade_id=? AND traded_at=?` / `WHERE order_id=? AND created_at=?`），
  一次事务提交；任一行影响行数不是 1 就整体回滚。
- 只补**日期**：时分秒原样保留（本脚本不做时区换算，不重排顺序）。
"""

import argparse
import json
import os
import sqlite3
import sys
import time


def read_rows(con, sql, args=()):
    con.row_factory = sqlite3.Row
    return [dict(r) for r in con.execute(sql, args)]


def bare_order_id(value):
    """柜台裸号：`1246@20261119` -> `1246`；其它形态原样返回。"""
    text = (value or "").strip()
    if "@" in text:
        return text.split("@", 1)[0]
    return text


def day_of(stamp):
    """从完整时间戳取北京日（前 10 位，必须是 YYYY-MM-DD）。"""
    text = (stamp or "").strip()
    if len(text) >= 10 and text[4] == "-" and text[7] == "-":
        return text[:10]
    return ""


def resolve_fills(con):
    """成交行：用「同日同码同方向的委托行」证明日期。返回 (可回填列表, 无法回填列表)。"""
    malformed = read_rows(
        con,
        "SELECT id, order_id, trade_id, code, side, qty, price, traded_at FROM fills WHERE length(traded_at) < 10 ORDER BY id",
    )
    orders = read_rows(con, "SELECT order_id, code, side, created_at FROM orders WHERE length(created_at) >= 10")

    # 索引：(裸号, 码, 方向) -> 日期集合
    by_key = {}
    by_bare = {}
    for row in orders:
        day = day_of(row["created_at"])
        if not day:
            continue
        key = (bare_order_id(row["order_id"]), row["code"], row["side"])
        by_key.setdefault(key, set()).add(day)
        by_bare.setdefault(bare_order_id(row["order_id"]), set()).add(day)

    ready, unresolved = [], []
    for row in malformed:
        bare = bare_order_id(row["order_id"])
        days = by_key.get((bare, row["code"], row["side"]))
        if not days:
            # 裸号唯一但码/方向缺失？不宽容：宁可不动。
            days = None
        if days and len(days) == 1:
            ready.append(dict(row, day=next(iter(days))))
        else:
            unresolved.append(dict(row, reason="no_unique_order_day"))
    return ready, unresolved


def resolve_orders(con):
    """委托行：用「同裸号同码同方向、且已有完整日期」的成交行证明日期。"""
    malformed = read_rows(
        con,
        "SELECT order_id, signal_id, code, side, qty, created_at FROM orders WHERE length(created_at) < 10 ORDER BY created_at",
    )
    fills = read_rows(con, "SELECT order_id, code, side, traded_at FROM fills WHERE length(traded_at) >= 10")

    by_key = {}
    for row in fills:
        day = day_of(row["traded_at"])
        if not day:
            continue
        by_key.setdefault((bare_order_id(row["order_id"]), row["code"], row["side"]), set()).add(day)

    ready, unresolved = [], []
    for row in malformed:
        bare = bare_order_id(row["order_id"])
        days = by_key.get((bare, row["code"], row["side"]))
        if days and len(days) == 1:
            ready.append(dict(row, day=next(iter(days))))
        else:
            unresolved.append(dict(row, reason="no_dated_fill"))
    return ready, unresolved


def apply_fills(con, rows):
    """逐行补日期；影响行数必须恰好 1，否则抛错（由调用方整体回滚）。"""
    changed = 0
    for row in rows:
        clock = row["traded_at"].strip()
        new_stamp = row["day"] + " " + clock
        cur = con.execute(
            "UPDATE fills SET traded_at=? WHERE id=? AND traded_at=?",
            (new_stamp, row["id"], row["traded_at"]),
        )
        if cur.rowcount != 1:
            raise RuntimeError("fills id=%s 影响行数=%s（期望 1）" % (row["id"], cur.rowcount))
        row["new_traded_at"] = new_stamp
        changed += 1
    return changed


def apply_orders(con, rows):
    changed = 0
    for row in rows:
        clock = row["created_at"].strip()
        new_stamp = row["day"] + " " + clock
        cur = con.execute(
            "UPDATE orders SET created_at=? WHERE order_id=? AND created_at=?",
            (new_stamp, row["order_id"], row["created_at"]),
        )
        if cur.rowcount != 1:
            raise RuntimeError("orders %s 影响行数=%s（期望 1）" % (row["order_id"], cur.rowcount))
        row["new_created_at"] = new_stamp
        changed += 1
    return changed


def main():
    ap = argparse.ArgumentParser(description="按逐行证据给缺日期的时间戳补日期（§REPORT-STAMP）")
    ap.add_argument("--db", default="/var/lib/quant-trading-v2/live.db")
    ap.add_argument("--apply", action="store_true", help="真正写库（缺省只打印）")
    ap.add_argument("--backup", default="", help="改动行 JSON 备份路径")
    args = ap.parse_args()

    if not os.path.exists(args.db):
        print("BACKFILL_RESULT=ABORT reason=db_missing path=%s" % args.db)
        return 2

    con = sqlite3.connect(args.db, timeout=30)
    try:
        fill_ready, fill_unresolved = resolve_fills(con)
        order_ready, order_unresolved = resolve_orders(con)
        print("BACKFILL_FILLS ready=%d unresolved=%d" % (len(fill_ready), len(fill_unresolved)))
        print("BACKFILL_ORDERS ready=%d unresolved=%d" % (len(order_ready), len(order_unresolved)))
        for row in fill_ready:
            print("  FILL %s %s %s %s %s -> %s" % (row["trade_id"] or "-", row["order_id"], row["code"], row["side"], row["traded_at"], row["day"] + " " + row["traded_at"]))
        for row in order_ready:
            print("  ORDER %s %s %s %s -> %s" % (row["order_id"], row["code"], row["side"], row["created_at"], row["day"] + " " + row["created_at"]))
        if not args.apply:
            print("BACKFILL_RESULT=DRYRUN fills=%d orders=%d" % (len(fill_ready), len(order_ready)))
            return 0

        backup_path = args.backup or (args.db + ".bak-reportstamp-" + time.strftime("%Y%m%d%H%M%S") + ".json")
        with open(backup_path, "w", encoding="utf-8") as fh:
            json.dump({"fills": fill_ready, "orders": order_ready}, fh, ensure_ascii=False, indent=1)
        print("BACKFILL_BACKUP=%s" % backup_path)

        con.execute("BEGIN IMMEDIATE")
        n_fills = apply_fills(con, fill_ready)
        n_orders = apply_orders(con, order_ready)
        con.commit()
        print("BACKFILL_RESULT=APPLIED fills=%d orders=%d" % (n_fills, n_orders))
        return 0
    except Exception as exc:  # 任何异常都不留半态
        try:
            con.rollback()
        except Exception:
            pass
        print("BACKFILL_RESULT=ABORT reason=%s" % exc)
        return 3
    finally:
        con.close()


if __name__ == "__main__":
    sys.exit(main())
