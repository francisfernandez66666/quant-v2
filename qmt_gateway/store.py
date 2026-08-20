#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.store — 网关本地 SQLite 账本（AUTO_TRADING_PLAN M2）。

三表与首尔侧 Go store（real_positions/orders/fills）字段对齐，作为断线时的本地缓存与
幂等去重依据；联机后把增量事件（trade/order/positions/disconnect）即时推给首尔 /api/qmt/report。

线程安全：每线程独立连接（check_same_thread=False + 锁序列化写），配合 ThreadingHTTPServer 并发。
（English: local SQLite book aligned with the Seoul-side Go store (real_positions/orders/fills) —
the offline cache and idempotency source; incremental events (trade/order/positions/disconnect) are
pushed to Seoul POST /api/qmt/report as they occur. Thread-safe: per-thread connection plus a write lock.）
"""
import json
import os
import sqlite3
import threading


class Store:
    """SQLite 账本。"""

    def __init__(self, path):
        self.path = path
        self._lock = threading.RLock()
        # 已存在的库不再建表（保留数据）
        need_init = not os.path.exists(path)
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        if need_init:
            self._init_schema()

    def _init_schema(self):
        with self._lock:
            cur = self._conn.cursor()
            cur.executescript(
                """
                CREATE TABLE IF NOT EXISTS real_positions (
                    ts_code       TEXT PRIMARY KEY,
                    name          TEXT,
                    qty           INTEGER,
                    cost_price    REAL,
                    amount        REAL,
                    highest_price REAL,
                    strategy      TEXT,
                    signal_id     TEXT,
                    updated_at    TEXT
                );
                CREATE TABLE IF NOT EXISTS orders (
                    order_id   TEXT PRIMARY KEY,
                    signal_id  TEXT UNIQUE,
                    code       TEXT,
                    side       TEXT,
                    status     TEXT,
                    price      REAL,
                    qty        INTEGER,
                    created_at TEXT
                );
                CREATE TABLE IF NOT EXISTS fills (
                    id        INTEGER PRIMARY KEY AUTOINCREMENT,
                    order_id  TEXT,
                    code      TEXT,
                    side      TEXT,
                    price     REAL,
                    qty       INTEGER,
                    amount    REAL,
                    traded_at TEXT,
                    signal_id TEXT
                );
                """
            )
            self._conn.commit()

    # ── orders ──
    def order_by_signal(self, signal_id):
        """按 signal_id 查委托（幂等键）。"""
        cur = self._conn.execute("SELECT * FROM orders WHERE signal_id = ?", (signal_id,))
        return cur.fetchone()

    def upsert_order(self, order):
        """插入/更新委托（signal_id 冲突时更新 order_id/status）。返回是否新订单。"""
        with self._lock:
            cur = self._conn.execute(
                "SELECT 1 FROM orders WHERE signal_id = ?", (order["signal_id"],)
            )
            is_new = cur.fetchone() is None
            self._conn.execute(
                """INSERT INTO orders(order_id, signal_id, code, side, status, price, qty, created_at)
                   VALUES(?,?,?,?,?,?,?,?)
                   ON CONFLICT(signal_id) DO UPDATE SET
                     order_id=excluded.order_id, status=excluded.status, price=excluded.price,
                     qty=excluded.qty, created_at=excluded.created_at""",
                (
                    order.get("order_id", ""),
                    order["signal_id"],
                    order.get("code", ""),
                    order.get("side", ""),
                    order.get("status", ""),
                    order.get("price", 0.0),
                    order.get("qty", 0),
                    order.get("created_at", ""),
                ),
            )
            self._conn.commit()
            return is_new

    def order_by_id(self, order_id):
        cur = self._conn.execute("SELECT * FROM orders WHERE order_id = ?", (order_id,))
        return cur.fetchone()

    def list_orders(self):
        cur = self._conn.execute("SELECT * FROM orders ORDER BY created_at DESC")
        return [dict(r) for r in cur.fetchall()]

    # ── positions ──
    def upsert_position(self, p):
        """upsert 单条持仓，保持 highest_price 单调。"""
        with self._lock:
            row = self._conn.execute(
                "SELECT * FROM real_positions WHERE ts_code = ?", (p["ts_code"],)
            ).fetchone()
            highest = p.get("highest_price", 0) or 0
            if row and row["highest_price"] > highest:
                highest = row["highest_price"]
            self._conn.execute(
                """INSERT INTO real_positions
                     (ts_code, name, qty, cost_price, amount, highest_price, strategy, signal_id, updated_at)
                   VALUES(?,?,?,?,?,?,?,?,?)
                   ON CONFLICT(ts_code) DO UPDATE SET
                     name=excluded.name, qty=excluded.qty, cost_price=excluded.cost_price,
                     amount=excluded.amount, highest_price=excluded.highest_price,
                     strategy=excluded.strategy, signal_id=excluded.signal_id, updated_at=excluded.updated_at""",
                (
                    p["ts_code"], p.get("name", ""), p.get("qty", 0), p.get("cost_price", 0.0),
                    p.get("amount", 0.0), highest, p.get("strategy", ""), p.get("signal_id", ""),
                    p.get("updated_at", ""),
                ),
            )
            self._conn.commit()

    def apply_fill(self, f):
        """成交应用到持仓（买=加仓加权成本；卖=减仓/清仓删行）。返回更新后的持仓 dict 或 None。"""
        fill_side = f.get("side", "")
        with self._lock:
            row = self._conn.execute(
                "SELECT * FROM real_positions WHERE ts_code = ?", (f["code"],)
            ).fetchone()
            if fill_side == "买入":
                if row is None:
                    self._conn.execute(
                        """INSERT INTO real_positions
                             (ts_code, name, qty, cost_price, amount, highest_price, updated_at)
                           VALUES(?,?,?,?,?,?,?)""",
                        (f["code"], f.get("name", ""), f.get("qty", 0), f.get("price", 0.0),
                         f.get("amount", 0.0), f.get("price", 0.0), f.get("traded_at", "")),
                    )
                else:
                    old_qty, old_cost = row["qty"], row["cost_price"]
                    new_qty = old_qty + f["qty"]
                    new_cost = (old_qty * old_cost + f["qty"] * f["price"]) / new_qty
                    highest = max(row["highest_price"], f["price"])
                    self._conn.execute(
                        """UPDATE real_positions
                           SET qty=?, cost_price=?, amount=?, highest_price=?, updated_at=?
                           WHERE ts_code=?""",
                        (new_qty, new_cost, new_qty * f["price"], highest, f.get("traded_at", ""), f["code"]),
                    )
                self._conn.execute(
                    """INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id)
                       VALUES(?,?,?,?,?,?,?,?)""",
                    (f.get("order_id", ""), f["code"], fill_side, f["price"], f["qty"],
                     f.get("amount", 0.0), f.get("traded_at", ""), f.get("signal_id", "")),
                )
            else:
                if row is None:
                    return None
                remain = row["qty"] - f["qty"]
                if remain <= 0:
                    self._conn.execute(
                        "DELETE FROM real_positions WHERE ts_code = ?", (f["code"],)
                    )
                else:
                    self._conn.execute(
                        """UPDATE real_positions SET qty=?, amount=?, updated_at=?
                           WHERE ts_code=?""",
                        (remain, remain * f["price"], f.get("traded_at", ""), f["code"]),
                    )
                self._conn.execute(
                    """INSERT INTO fills(order_id, code, side, price, qty, amount, traded_at, signal_id)
                       VALUES(?,?,?,?,?,?,?,?)""",
                    (f.get("order_id", ""), f["code"], fill_side, f["price"], f["qty"],
                     f.get("amount", 0.0), f.get("traded_at", ""), f.get("signal_id", "")),
                )
            self._conn.commit()
            cur = self._conn.execute(
                "SELECT * FROM real_positions WHERE ts_code = ?", (f["code"],)
            )
            row = cur.fetchone()
            return dict(row) if row else None

    def list_positions(self):
        cur = self._conn.execute(
            "SELECT * FROM real_positions ORDER BY amount DESC"
        )
        return [dict(r) for r in cur.fetchall()]

    def reconcile_positions(self, positions):
        """全量对账：upsert 全部 + 删除不在集合内的持仓。返回新持仓数量。"""
        codes = set()
        with self._lock:
            for p in positions:
                codes.add(p["ts_code"])
                self.upsert_position(p)
            placeholders = ",".join("?" * len(codes))
            if placeholders:
                self._conn.execute(
                    "DELETE FROM real_positions WHERE ts_code NOT IN (%s)" % placeholders,
                    tuple(codes),
                )
            else:
                self._conn.execute("DELETE FROM real_positions")
            self._conn.commit()
        return len(codes)

    def close(self):
        with self._lock:
            self._conn.close()


def json_default(o):
    """sqlite3.Row → dict 序列化兜底。"""
    if isinstance(o, sqlite3.Row):
        return dict(o)
    raise TypeError("not JSON serializable")


def to_json(obj):
    return json.dumps(obj, ensure_ascii=False, default=json_default)