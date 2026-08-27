#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.store — 网关本地 SQLite 账本（AUTO_TRADING_PLAN M2）。

三表与首尔侧 Go store（real_positions/orders/fills）字段对齐，作为断线时的本地缓存与
幂等去重依据；联机后把增量事件（trade/order/positions/disconnect）经 outbox 推给首尔
POST /api/qmt/report。

线程安全：单连接 + 全部读写统一持 RLock（ThreadingHTTPServer 每连接一线程并发访问）。
§R1 幂等占位（G1/G2）：claim_order 以 signal_id UNIQUE 先插 pending 占位再下单——
原子抢占，杜绝 check→place→record 窗口内的重复真实下单与崩溃后重下；
首个非占位 order_id 一旦落库不再被覆盖（回调回报的交易所委托号只做一次替换）。
§G10 成交去重：fills 按 (order_id,side,price,qty) 在时间窗内判重，防通道重放导致持仓翻倍。
（English: local SQLite book aligned with the Seoul-side Go store. Single connection guarded by an
RLock for ALL reads/writes. Claim-before-place idempotency on orders.signal_id UNIQUE prevents
duplicate real orders across concurrent retries and crashes; the first real order_id wins and is
never overwritten. Trade fills are de-duplicated in a sliding time window against channel replays.）
"""
import json
import os
import sqlite3
import threading
import time
from datetime import datetime, timedelta, timezone

# 占位 order_id 前缀：pending:<signal_id>（下单前占位）；真实委托号落库后不可被占位值覆盖
CN_TZ = timezone(timedelta(hours=8))  # 北京时间（UTC+8），用于时间戳字段统一
FILL_DEDUP_WINDOW_SEC = 120  # 成交去重时间窗（秒）


def is_placeholder_order_id(oid):
    """空串/pending:/seq: 视为占位，允许被真实交易所委托号替换。"""
    if not oid:
        return True
    return oid.startswith("pending:") or oid.startswith("seq:")


def order_id_rank(oid):
    """§G4 委托引用等级：pending(0) < seq(1) < 交易所真实委托号(2)。只允许升级替换。"""
    if not oid:
        return 0
    if oid.startswith("pending:"):
        return 0
    if oid.startswith("seq:"):
        return 1
    return 2


class Store:
    """SQLite 账本。"""

    def __init__(self, path):
        self.path = path
        # 全部读写统一持 RLock，保证多线程并发安全
        self._lock = threading.RLock()
        # 已存在的库不再建表（保留数据）
        need_init = not os.path.exists(path)
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        with self._lock:
            if need_init:
                self._init_schema()
            else:
                self._migrate_schema()
            # §ROBUST 持久化 outbox：回报先落库再发送，崩溃/重启后自动续发（新老库都建）
            self._conn.execute(
                """CREATE TABLE IF NOT EXISTS outbox (
                    id         INTEGER PRIMARY KEY AUTOINCREMENT,
                    payload    TEXT NOT NULL,
                    created_at TEXT
                )""")
            self._conn.commit()

    def _init_schema(self):
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

    def _migrate_schema(self):
        """老库兼容：无破坏性变更即跳过（当前 schema 与初版一致）。"""
        return

    # ── orders ──
    def claim_order(self, draft):
        """§G1 原子占位：以 signal_id 抢占一个 pending 行。

        返回 (claimed:bool, existing:dict|None)。已存在时返回既有行（含 pending 占位），
        调用方据此实现幂等或拒绝进行中请求。崩溃残留的 pending 行会永久阻塞该 signal_id
        （安全侧失效：宁可拒绝也不重复真实下单），启动时由 Gateway 打警告日志。
        """
        # 取出幂等键 signal_id，作为占位行的唯一约束
        sid = draft.get("signal_id", "")
        with self._lock:
            # 以 signal_id UNIQUE 做原子 INSERT；冲突则啥也不做（占位失败=已被抢占）
            cur = self._conn.execute(
                """INSERT INTO orders(order_id, signal_id, code, side, status, price, qty, created_at)
                   VALUES(?,?,?,?,?,?,?,?)
                   ON CONFLICT(signal_id) DO NOTHING""",
                (
                    "pending:" + sid,
                    sid,
                    draft.get("code", ""),
                    draft.get("side", ""),
                    "pending",
                    float(draft.get("price", 0) or 0),
                    int(draft.get("qty", 0) or 0),
                    draft.get("created_at", "") or _now_cn(),
                ),
            )
            if cur.rowcount == 0:
                row = self._conn.execute(
                    "SELECT * FROM orders WHERE signal_id = ?", (sid,)
                ).fetchone()
                self._conn.commit()
                return False, (dict(row) if row else None)
            self._conn.commit()
            return True, None

    def release_pending(self, signal_id):
        """下单失败时释放 pending 占位，允许后续重试。仅删未结算的占位行。"""
        with self._lock:
            self._conn.execute(
                "DELETE FROM orders WHERE signal_id = ? AND status = 'pending'",
                (signal_id,),
            )
            self._conn.commit()

    def upsert_order(self, order):
        """插入/更新委托。返回是否新订单。

        §G2 语义：status/price/qty/created_at 随最新事件刷新；order_id 只在「存量是占位
        且新值是真实委托号」时替换一次（pending:→seq:→交易所委托号），真实委托号互不覆盖。
        """
        # 取 signal_id 与待写入的 order_id（可能是占位串或真实委托号）
        sid = order.get("signal_id", "")
        new_oid = str(order.get("order_id", "") or "")
        with self._lock:
            # 查现存行：无则插入新订单，有则按等级规则升级 order_id
            row = self._conn.execute(
                "SELECT * FROM orders WHERE signal_id = ?", (sid,)
            ).fetchone()
            if row is None:
                self._conn.execute(
                    """INSERT INTO orders(order_id, signal_id, code, side, status, price, qty, created_at)
                       VALUES(?,?,?,?,?,?,?,?)""",
                    (
                        new_oid or ("pending:" + sid),
                        sid,
                        order.get("code", ""),
                        order.get("side", ""),
                        order.get("status", ""),
                        float(order.get("price", 0) or 0),
                        int(order.get("qty", 0) or 0),
                        order.get("created_at", ""),
                    ),
                )
                self._conn.commit()
                return True
            # 仅在「新引用等级高于存量（占位→seq→真实号）」时才替换 order_id，真实号互不覆盖
            final_oid = row["order_id"]
            if order_id_rank(new_oid) > order_id_rank(final_oid):
                final_oid = new_oid
            self._conn.execute(
                """UPDATE orders SET order_id=?, status=?, price=?, qty=?, created_at=?
                   WHERE signal_id=?""",
                (
                    final_oid,
                    order.get("status", row["status"]),
                    float(order.get("price", 0) or row["price"] or 0),
                    int(order.get("qty", 0) or 0) or row["qty"] or 0,
                    order.get("created_at", "") or row["created_at"],
                    sid,
                ),
            )
            self._conn.commit()
            return False

    def order_by_signal(self, signal_id):
        """按 signal_id 查委托（幂等键）。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders WHERE signal_id = ?", (signal_id,))
            return cur.fetchone()

    def order_by_id(self, order_id):
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders WHERE order_id = ?", (order_id,))
            return cur.fetchone()

    def list_orders(self):
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders ORDER BY created_at DESC")
            return [dict(r) for r in cur.fetchall()]

    def list_pending(self):
        """全部未结算占位行（进程重启后仍 pending = 经历过下单窗口 crash，需人工确认）。"""
        with self._lock:
            cur = self._conn.execute("SELECT * FROM orders WHERE status = 'pending'")
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

    def _fill_is_duplicate(self, f, cutoff):
        """§G10 时间窗内相同 (order_id,side,price,qty) 视为通道重放。"""
        if not f.get("order_id"):
            return False
        cur = self._conn.execute(
            """SELECT 1 FROM fills
               WHERE order_id = ? AND side = ? AND price = ? AND qty = ? AND traded_at >= ?
               LIMIT 1""",
            (f["order_id"], f.get("side", ""), float(f.get("price", 0) or 0),
             int(f.get("qty", 0) or 0), cutoff),
        )
        return cur.fetchone() is not None

    def apply_fill(self, f):
        """成交应用到持仓（买=加仓加权成本；卖=减仓/清仓删行）。

        返回 (position_dict|None, is_duplicate:bool)。重复回报不改动持仓、不重复入 fills。
        """
        # 取成交方向，并计算去重时间窗下界（早于该时间的重放允许）
        fill_side = f.get("side", "")
        cutoff = (datetime.now(CN_TZ) - timedelta(seconds=FILL_DEDUP_WINDOW_SEC)).strftime(
            "%Y-%m-%dT%H:%M:%S")
        with self._lock:
            # 先判重：窗口内相同 (order_id,side,price,qty) 视为通道重放，直接返回不改动
            if self._fill_is_duplicate(f, cutoff):
                row = self._conn.execute(
                    "SELECT * FROM real_positions WHERE ts_code = ?", (f["code"],)
                ).fetchone()
                return (dict(row) if row else None), True
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
            else:
                if row is None:
                    return None, False
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
            return (dict(row) if row else None), False

    def list_positions(self):
        with self._lock:
            cur = self._conn.execute(
                "SELECT * FROM real_positions ORDER BY amount DESC"
            )
            return [dict(r) for r in cur.fetchall()]

    def reconcile_positions(self, positions):
        """全量对账：upsert 全部 + 删除不在集合内的持仓。返回新持仓数量。

        注意：空集合的删除语义由调用方（handler.on_positions）守卫——连续空快照才接受清空。
        """
        # 收集本次对账中的全部 ts_code，作为“应保留”集合
        codes = set()
        with self._lock:
            for p in positions:
                codes.add(p["ts_code"])
                # 逐条 upsert 持仓
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

    # ── §ROBUST 持久化 outbox（回报队列）──
    # 事件先落库再发送：进程崩溃/重启后由 sender 续发，杜绝内存队列丢回报。
    # fills 表幂等 + 首尔 ApplyRealFill 幂等兜底，重发安全（重复投递被唯一键拦截）。

    def outbox_enqueue(self, payload):
        """回报事件入队，返回行 id。payload 为可 JSON 序列化 dict。"""
        with self._lock:
            cur = self._conn.execute(
                "INSERT INTO outbox(payload, created_at) VALUES(?,?)",
                (json.dumps(payload, ensure_ascii=False, default=json_default),
                 time.strftime("%Y-%m-%dT%H:%M:%S+08:00")))
            self._conn.commit()
            return cur.lastrowid

    def outbox_oldest(self):
        """取最旧一条待发回报。返回 (id, payload) 或 None（队空）。坏行返回 (id, None)。"""
        with self._lock:
            row = self._conn.execute(
                "SELECT id, payload FROM outbox ORDER BY id LIMIT 1").fetchone()
            if row is None:
                return None
            try:
                return row["id"], json.loads(row["payload"])
            except ValueError:
                return row["id"], None

    def outbox_delete(self, oid):
        """发送成功后出队。"""
        with self._lock:
            self._conn.execute("DELETE FROM outbox WHERE id=?", (oid,))
            self._conn.commit()

    def outbox_trim(self, cap):
        """超上限删最旧（首尔长期失联的极端保护）。返回删除条数；positions 每 60s 一条，
        cap=20000 约两周量。English: drops oldest rows past cap; returns deleted count."""
        with self._lock:
            n = self._conn.execute("SELECT COUNT(*) AS c FROM outbox").fetchone()["c"]
            if n <= cap:
                return 0
            self._conn.execute(
                "DELETE FROM outbox WHERE id IN "
                "(SELECT id FROM outbox ORDER BY id LIMIT ?)", (n - cap,))
            self._conn.commit()
            return n - cap

    def outbox_count(self):
        with self._lock:
            return self._conn.execute("SELECT COUNT(*) AS c FROM outbox").fetchone()["c"]


def _now_cn():
    return time.strftime("%Y-%m-%dT%H:%M:%S+08:00")


def json_default(o):
    """sqlite3.Row → dict 序列化兜底。"""
    if isinstance(o, sqlite3.Row):
        return dict(o)
    raise TypeError("not JSON serializable")


def to_json(obj):
    return json.dumps(obj, ensure_ascii=False, default=json_default)
